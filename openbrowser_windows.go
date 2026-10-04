//go:build windows

// 本文件是「不建内嵌窗口」时的退路：用系统浏览器把界面打开。
//
// 两条路，按优先级试：
//  1. Edge 的应用模式（--app=）：得到一个没有地址栏的窗口，看起来最接近原生应用；
//  2. rundll32 走系统默认浏览器：Edge 找不到时兜底（比如只有 Firefox 的机器）。
//
// 只在 --browser 模式、或内嵌 WebView2 创建失败之后才会走到这里。

package main

import (
	"os"
	"os/exec"
	"path/filepath"

	"golang.org/x/sys/windows/registry"
)

// msedgeRelPath 是 msedge.exe 相对于某个安装根目录的路径片段。
// 它是相对片段，不是绝对路径：拼到下面那些根目录后面才成为完整位置。
const msedgeRelPath = `Microsoft\Edge\Application\msedge.exe`

// appPathsKey 是 Windows 记录「某个可执行文件装在哪」的约定位置。
// 安装程序（包括 Edge 的安装器）会把完整路径写进这一项的默认值。
const appPathsKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`

// openUI 打开界面地址。preferApp 为真时先试 Edge 应用模式。
//
// 启动命令失败（二进制不存在、被安全策略拦）不算致命：往下退到默认浏览器。
// 只判断「进程起没起来」，不等待浏览器退出。它是独立进程，程序退出后
// 浏览器里的界面会因为服务关闭而失效，这是预期行为。
func openUI(url string, preferApp bool) error {
	if preferApp {
		if edge := findEdge(); edge != "" {
			cmd := exec.Command(edge,
				"--app="+url,
				"--window-size=1280,880",
				"--no-first-run",
				"--no-default-browser-check",
			)
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// findEdge 找出 msedge.exe 的完整路径，找不到返回空串。
//
// 这个文件里没有写死的绝对路径。四个来源依次试，谁先命中用谁：
//
//  1. 注册表 App Paths（HKLM 再 HKCU）。安装程序写入的，不依赖环境变量与盘符
//     （Windows 可以装在 D: 上）。
//  2. %LOCALAPPDATA%\Microsoft\Edge\Application\msedge.exe
//  3. %ProgramFiles(x86)% 下的同一相对路径
//  4. %ProgramFiles% 下的同一相对路径
//
// 后三个不能省。注册表项可能被清理工具删掉，环境变量在精简过的进程里也可能是空的
// （本机会话里 ProgramFiles 与 ProgramFiles(x86) 都是空）。
//
// 全找不到就返回空串，调用方退回系统默认浏览器。找不到 Edge 不该让界面打不开。
func findEdge() string {
	if p := edgeFromRegistry(); p != "" {
		return p
	}
	for _, root := range []string{
		os.Getenv("LOCALAPPDATA"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("ProgramFiles"),
	} {
		if root == "" {
			continue
		}
		if p := existingFile(filepath.Join(root, msedgeRelPath)); p != "" {
			return p
		}
	}
	return ""
}

// edgeFromRegistry 从 App Paths 里读 Edge 的安装位置，读不到返回空串。
//
// 先机器级（HKLM）再用户级（HKCU）：机器级安装写前者，用户级安装写后者，
// 两者都可能存在。
//
// 读不到不算错误。调用方还会试别的位置，所以这里静默返回空串，
// 不把「注册表里没有」变成一次启动失败。
func edgeFromRegistry() string {
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, appPathsKey, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		// App Paths 的路径存在默认值（无名值）里。
		v, _, err := k.GetStringValue("")
		_ = k.Close()
		if err != nil {
			continue
		}
		if p := existingFile(v); p != "" {
			return p
		}
	}
	return ""
}

// existingFile 在路径确实指向一个存在的普通文件时返回它，否则返回空串。
// 目录不算：注册表项被写成目录时不该被当成可执行文件。
func existingFile(path string) string {
	if path == "" {
		return ""
	}
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		return path
	}
	return ""
}
