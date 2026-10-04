//go:build windows

// 本文件做单实例保护：同一时间只允许开一个窗口。
//
// 为什么必须有：两个实例会共用同一个 WebView2 用户数据目录，于是共用同一个
// 浏览器进程，而那个进程挂在先启动的实例下。先启动的一退出，浏览器进程被一起
// 回收，后启动那个的窗口立刻变全黑（实测过：截图从 400KB 掉到 15KB）。
// 顺带也避免了两个窗口同时改同一个证书目录。

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procCreateMutexW        = kernel32.NewProc("CreateMutexW")
	procFindWindowW         = user32.NewProc("FindWindowW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
)

// errAlreadyExists 是 CreateMutexW 在互斥体已存在时给出的错误码（ERROR_ALREADY_EXISTS）。
const errAlreadyExists = syscall.Errno(183)

// singleInstanceMutex 保存互斥体句柄。
//
// 故意不释放、不关闭：互斥体随进程结束自动释放，而如果在这里 CloseHandle，
// 保护会立刻失效（第二个实例就能进来了）。所以它是包级变量而不是局部变量。
var singleInstanceMutex uintptr

// acquireSingleInstance 尝试成为唯一实例，返回 true 表示「可以继续启动」。
//
// 名字带 Local\ 前缀，只在当前登录会话内有效。不同用户各开一个互不干扰，
// 这比全局命名更合适（各用户有各自的配置目录与 WebView2 数据目录）。
//
// 拿不到句柄（系统调用失败）时返回 true：宁可能出现两个窗口，
// 也不要因为一次失败的系统调用让程序完全起不来。
func acquireSingleInstance() bool {
	name, err := windows.UTF16PtrFromString(`Local\Web_ICS_CA.SingleInstance`)
	if err != nil {
		return true
	}
	h, _, lastErr := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return true
	}
	singleInstanceMutex = h
	return lastErr != errAlreadyExists
}

// focusExistingWindow 把已经在跑的实例的窗口叫到前面来。
//
// 按窗口标题找（FindWindowW），所以标题必须是程序自己设定的那一个（main.appTitle）。
// SW_RESTORE（9）而不是 SW_SHOW：窗口可能是被最小化的，只 Show 不会恢复。
//
// 找不到窗口就安静返回。用户双击两次，第二次没反应，比弹个错误框要好。
func focusExistingWindow(title string) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	hwnd, _, _ := procFindWindowW.Call(0, uintptr(unsafe.Pointer(t)))
	if hwnd == 0 {
		return
	}
	const swRestore = 9
	procShowWindow.Call(hwnd, swRestore)
	procSetForegroundWindow.Call(hwnd)
}
