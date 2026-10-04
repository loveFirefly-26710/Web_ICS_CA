//go:build !windows

// 非 Windows 平台上没有内嵌窗口，openAppWindow 一律失败，
// main 收到错误后会弹提示并退回系统浏览器。保留这组空实现是为了让
// main.go 里的窗口流程不用写平台分支。

package main

import (
	"errors"
	"os"
)

// appWindow 是空壳，只为了对上 Windows 版本的接口。
type appWindow struct{}

// openAppWindow 恒返回错误，提示调用方改用系统浏览器。
func openAppWindow(url, title string, width, height uint) (*appWindow, error) {
	return nil, errors.New("当前平台没有内嵌窗口实现，改用系统浏览器")
}

func (a *appWindow) Run()     {}
func (a *appWindow) Close()   {}
func (a *appWindow) Destroy() {}

// hardExit 在非 Windows 平台上没有 WebView2 收尾问题，普通退出即可。
func hardExit(code uint32) { os.Exit(int(code)) }
