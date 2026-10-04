//go:build !windows

// 非 Windows 平台不做单实例限制：没有共用的 WebView2 数据目录问题，
// 而且这些平台上程序本来就是「起服务 + 用浏览器打开」，多开无害。

package main

// acquireSingleInstance 恒为真（不做限制）。
func acquireSingleInstance() bool { return true }

// focusExistingWindow 无事可做。
func focusExistingWindow(title string) {}
