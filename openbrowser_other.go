//go:build !windows

// 非 Windows 平台的打开方式：macOS 用 open，其余（Linux/BSD）用 xdg-open。

package main

import (
	"os/exec"
	"runtime"
)

// openUI 调系统自带的「用默认程序打开 URL」命令。
//
// preferApp 在这里没有意义（没有 Edge 应用模式这回事），参数保留是为了
// 让调用方不必写平台分支。
func openUI(url string, preferApp bool) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", url).Start()
	}
	return exec.Command("xdg-open", url).Start()
}
