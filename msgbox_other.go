//go:build !windows

// 非 Windows 平台的替代实现：有控制台，所以消息直接打到标准输出。

package main

import "fmt"

// messageBox 没有图形对话框可用，退化成往 stdout 打一行。
func messageBox(title, text string) {
	fmt.Printf("%s: %s\n", title, text)
}

// hasConsole 在非 Windows 平台上恒为真（从终端启动就有）。
func hasConsole() bool { return true }
