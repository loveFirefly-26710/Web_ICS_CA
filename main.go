// web-ics-ca 是给 web_ics 用的证书签发工具：建内部 CA、签服务器证书与客户端证书、
// 导出浏览器可直接导入的 .pfx、维护吊销名单、生成配置片段。
//
// 运行形态是一个「自带界面的本地服务」：
//
//	启动 → 在回环地址上起 HTTP 服务（端口由系统挑）
//	     → 用内嵌的 WebView2 开一个属于本程序的窗口指向它
//	     → 窗口关掉（或界面点「退出程序」）即进程退出
//
// 界面资源用 go:embed 打进二进制，所以产物是一个没有外部依赖的单文件 EXE。
//
// 三种启动模式（由参数决定）：
//
//	默认       内嵌窗口，窗口即程序
//	--browser  改用系统浏览器打开界面（WebView2 运行时缺失时的退路）
//	--no-open  只起服务并打印地址，不开任何界面（给脚本用）
package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"web_ics_ca/internal/app"
)

// uiFS 把 ui/ 目录整个嵌进二进制。注意它嵌的是目录本身，
// 下面用 fs.Sub 去掉 "ui" 这一层前缀，所以服务里读的是 "index.html" 而不是 "ui/index.html"。
//
//go:embed ui
var uiFS embed.FS

// version 由构建脚本用 -ldflags "-X main.version=..." 注入，源码里跑就是 dev。
var version = "dev"

// appTitle 同时用作窗口标题和单实例唤出时的查找键，
// 所以它必须与 window 创建时用的标题完全一致（见 main 里传给 openAppWindow 的值）。
const appTitle = "web-ics-ca · 证书签发"

func main() {
	// 必须在创建任何窗口之前声明 DPI 感知：窗口一建出来，进程的 DPI 感知就定死了，
	// 之后再调会被静默忽略。非 Windows 平台是空实现。
	makeProcessDPIAware()

	var (
		outDir     = flag.String("out-dir", "", "证书输出目录。给了就记住它，下次启动直接用")
		useBrowser = flag.Bool("browser", false, "不建内嵌窗口，改用系统浏览器打开界面")
		noOpen     = flag.Bool("no-open", false, "只启动服务并打印地址，不打开任何界面")
		showVer    = flag.Bool("version", false, "打印版本号后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("web-ics-ca", version)
		return
	}

	// 状态文件（上次的输出目录 + 吊销名单）。读不出来就直接退出：
	// 带着半个状态继续跑，会做出「以为在吊销、其实没吊销」这种事。
	store, err := app.LoadStore()
	if err != nil {
		fatal(err, *noOpen)
	}
	if *outDir != "" {
		abs, err := filepath.Abs(*outDir)
		if err != nil {
			fatal(fmt.Errorf("输出目录不是合法路径: %w", err), *noOpen)
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			fatal(fmt.Errorf("创建输出目录失败: %w", err), *noOpen)
		}
		if err := store.SetOutDir(abs); err != nil {
			fatal(fmt.Errorf("保存设置失败: %w", err), *noOpen)
		}
	}

	// 去掉 embed 里的 "ui" 前缀，服务那边就当它是根目录。
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		fatal(err, *noOpen)
	}
	srv, err := app.New(store, sub, version)
	if err != nil {
		fatal(err, *noOpen)
	}
	base, err := srv.Listen()
	if err != nil {
		fatal(err, *noOpen)
	}
	// 带令牌的地址：谁拿到它谁就能访问界面，所以 banner 里专门提醒不要外传。
	url := base + "?t=" + srv.Token()

	banner(url, store.OutDir)

	if *noOpen {
		<-exitChan(srv)
		srv.Shutdown()
		return
	}

	if !*useBrowser {
		// 单实例保护只在「要建窗口」这条路上判：--no-open 是脚本模式，
		// 同时跑几个没有害处，不该被拦住。
		if !acquireSingleInstance() {
			// 已经有实例在跑：把它的窗口叫到前面来，自己安静退出。
			focusExistingWindow(appTitle)
			return
		}
		win, err := openAppWindow(url, appTitle, 1180, 820)
		if err == nil {
			// 退出信号（界面点「退出程序」，或 Ctrl+C）到达时去关窗口；
			// 窗口一关，下面的 win.Run() 就会返回。
			go func() {
				<-exitChan(srv)
				win.Close()
			}()
			win.Run()
			win.Destroy()
			srv.Shutdown()
			// 不用普通 return：WebView2 运行时的进程收尾会卡住，
			// 这里必须走硬退出（见 window_windows.go 的 hardExit）。
			hardExit(0)
		}
		// 内嵌窗口失败（最常见的是没装 WebView2 运行时）不致命：
		// 弹框说明原因，然后退回系统浏览器。
		messageBox(appTitle, "内嵌窗口没能创建：\n\n"+err.Error()+"\n\n改用系统浏览器打开界面。")
	}

	if err := openUI(url, true); err != nil {
		fatal(fmt.Errorf("打不开界面: %w", err), *noOpen)
	}
	<-exitChan(srv)
	srv.Shutdown()
}

// exitChan 把两个退出来源合成一个 channel：进程信号（Ctrl+C / SIGTERM）
// 与界面上的「退出程序」（服务收到 /api/quit 后关闭 srv.Quit()）。
//
// 两者谁先来都算退出。返回的 channel 在触发时被关闭，所以可以反复读。
func exitChan(srv *app.Server) <-chan struct{} {
	out := make(chan struct{})
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer close(out)
		select {
		case <-sig:
		case <-srv.Quit():
		}
	}()
	return out
}

// banner 在控制台打印启动信息。
//
// 双击运行时没有控制台，这几行会被丢掉。它们主要是给 --no-open 和
// 从命令行启动的场景用的（尤其是那行带令牌的地址，是手工打开界面的唯一途径）。
func banner(url, outDir string) {
	line := strings.Repeat("─", 66)
	fmt.Println()
	fmt.Println("  web-ics-ca  ·  web_ics 证书签发工具  " + version)
	fmt.Println("  " + line)
	if outDir == "" {
		fmt.Println("  证书输出目录：还没设置，在界面顶部填一个")
	} else {
		fmt.Println("  证书输出目录：" + outDir)
	}
	fmt.Println()
	fmt.Println("  界面地址（带会话令牌，别外传）：")
	fmt.Println("  " + url)
	fmt.Println()
	fmt.Println("  " + line)
	fmt.Println()
}

// fatal 报告启动阶段的致命错误并退出。
//
// 程序编译成窗口子系统（-H windowsgui），双击时没有控制台，stderr 没人看得到，
// 所以有窗口可用时（hasConsole() 为假）额外弹一个对话框把原因说出去。
// headless 为真（--no-open）时不弹框：脚本调用不该被一个模态框卡住。
func fatal(err error, headless bool) {
	fmt.Fprintln(os.Stderr, "启动失败："+err.Error())
	if !headless && !hasConsole() {
		messageBox(appTitle, "启动失败：\n\n"+err.Error())
	}
	os.Exit(1)
}
