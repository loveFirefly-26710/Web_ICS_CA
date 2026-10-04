//go:build windows

// 本文件是 Windows 上的窗口实现：用内嵌的 WebView2 开一个属于本程序自己的窗口，
// 而不是借 Edge 的外壳。这个 EXE 本身就是应用。
//
// 三个不那么显然的点都集中在这里：WebView2 的数据目录必须自己给绝对路径
// （见 webviewDataPath）；窗口关闭要用 Destroy 而不是 Terminate；
// 进程退不干净，最后得走 TerminateProcess（见 hardExit）。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

var (
	kernel32              = windows.NewLazySystemDLL("kernel32.dll")
	procGetCurrentProcess = kernel32.NewProc("GetCurrentProcess")
	procTerminateProcess  = kernel32.NewProc("TerminateProcess")
)

// appWindow 包住 WebView2 实例。
//
// once 只保护 Close：界面点「退出程序」和用户点窗口右上角 ✕ 可能几乎同时发生，
// 重复 Destroy 会崩。Destroy 不重复保护，因为它只在 Run 返回之后调用一次。
type appWindow struct {
	w    webview2.WebView
	once sync.Once
}

// openAppWindow 创建并加载窗口。返回的窗口还没开始跑消息循环，要再调 Run。
//
// width/height 是逻辑尺寸，会按当前 DPI 换算成物理像素；
// 最小尺寸限制成 880×560，再小的话界面里的表格和表单会被压得没法用。
//
// 返回 nil 窗口时不 panic，而是带着数据目录路径一起报错。这个错误最终会变成
// 一个弹框（见 main），把「运行时没装」和「目录不可写」这两种常见原因说清楚。
func openAppWindow(url, title string, width, height uint) (*appWindow, error) {
	dataPath, err := webviewDataPath()
	if err != nil {
		return nil, err
	}
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  dataPath,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  title,
			Width:  scaleForDPI(width),
			Height: scaleForDPI(height),
			Center: true,
		},
	})
	if w == nil {
		return nil, fmt.Errorf("WebView2 窗口创建失败：运行时可能没装，或数据目录不可写（%s）", dataPath)
	}
	w.SetSize(int(scaleForDPI(880)), int(scaleForDPI(560)), webview2.HintMin)
	w.Navigate(url)
	return &appWindow{w: w}, nil
}

// Run 进入窗口的消息循环，阻塞到窗口被关闭。
func (a *appWindow) Run() { a.w.Run() }

// Close 关闭窗口。可以被多路调用（退出信号、界面按钮），用 once 保证只真正关一次。
//
// 用 Destroy（内部是 PostMessage WM_CLOSE）而不是 Terminate：
// Terminate 走的 PostQuitMessage 只对调用线程有效，而从 goroutine 里调它
// 等于什么也没做。Destroy 是跨线程安全的。
func (a *appWindow) Close() { a.once.Do(func() { a.w.Destroy() }) }

// Destroy 直接销毁窗口，供 Run 返回之后收尾用（此时不会再有别的路径来关它）。
func (a *appWindow) Destroy() { a.w.Destroy() }

// hardExit 结束进程，跳过所有清理。
//
// 需要这么粗暴的原因：窗口关掉、消息循环退出、main 一路走到 return，
// 进程却一直留在任务管理器里，WebView2 的 6 个子进程也不退，卡在
// WebView2 运行时的进程收尾阶段（DLL detach）。os.Exit 一样不行，
// 因为 ExitProcess 会逐个跑 DllMain。TerminateProcess 不跑 DllMain，
// 实测 2 秒内主进程与全部子进程都退干净。
//
// 代价是跳过所有清理。这个工具没有需要清理的东西：证书与状态每次改动都已经落盘。
func hardExit(code uint32) {
	h, _, _ := procGetCurrentProcess.Call()
	procTerminateProcess.Call(h, uintptr(code))
	// 正常情况走不到这里；万一 TerminateProcess 失败，至少别让 main 继续往下跑。
	os.Exit(int(code))
}

// webviewDataPath 返回 WebView2 用户数据目录，并确保它存在。
//
// 必须返回绝对路径，而且必须由我们自己给：
// go-webview2 在 DataPath 为空时会去读 os.Getenv("AppData") 拼路径。
// 那个环境变量一旦为空（精简过的进程环境、某些服务上下文里很常见），
// 拼出来就是相对路径，而 WebView2 要求绝对路径，控制器创建于是失败并返回
// 0x80080005（CO_E_SERVER_EXEC_FAILURE）。更麻烦的是这个库用 int64(res) < 0
// 判 HRESULT，而 HRESULT 是 32 位，负的错误码被零扩展成正数，判不出失败，
// 接着拿 nil 控制器解引用直接崩，现象是一句毫无线索的空指针 panic。
//
// 放在用户缓存目录下而不是 exe 旁边：这里有几十 MB 的浏览器 profile，
// 不该混进用户的证书目录。UserCacheDir 取不到时退回临时目录。
func webviewDataPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil || base == "" {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "Web_ICS_CA", "webview")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建 WebView2 数据目录失败: %w", err)
	}
	return dir, nil
}
