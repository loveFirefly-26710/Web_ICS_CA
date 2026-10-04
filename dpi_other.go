//go:build !windows

// 非 Windows 平台上的空实现：这些平台本来就没有系统级的 DPI 位图放大问题，
// 尺寸也不需要换算。保留同名函数是为了让 main.go 与 window_*.go 不用写平台分支。

package main

// makeProcessDPIAware 在非 Windows 平台上什么也不做。
func makeProcessDPIAware() {}

// scaleForDPI 在非 Windows 平台上原样返回。
func scaleForDPI(v uint) uint { return v }
