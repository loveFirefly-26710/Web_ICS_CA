//go:build windows

// 本文件提供两个能力：判断当前进程有没有控制台，以及在没有控制台时弹一个错误框。

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	procMessageBoxW      = user32.NewProc("MessageBoxW")
	procGetConsoleWindow = user32.NewProc("GetConsoleWindow")
)

// hasConsole 判断当前进程是否附着在控制台上。
//
// 程序编译成窗口子系统（-H windowsgui），双击启动时没有控制台。
// fatal 用它决定要不要弹框：有控制台就直接往 stderr 写，弹框反而碍事。
func hasConsole() bool {
	h, _, _ := procGetConsoleWindow.Call()
	return h != 0
}

// MessageBoxW 的标志位。
const (
	mbOK            = 0x00000000
	mbIconError     = 0x00000010
	mbSetForeground = 0x00010000
)

// messageBox 弹一个模态错误框。没有控制台时这是把启动失败原因告诉用户的唯一途径。
//
// 用 MB_SETFOREGROUND 是必要的：程序从后台启动时，不带这个标志的对话框
// 会开在别的窗口后面，用户只会觉得「双击了没反应」。
//
// 传字符串给 Win32 必须先转成 UTF-16 并以 NUL 结尾；转换失败就放弃弹框，
// 不在错误处理路径上再制造一个错误。
func messageBox(title, text string) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return
	}
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		mbOK|mbIconError|mbSetForeground)
}
