//go:build windows

// 本文件负责进程级 DPI 感知，以及把「逻辑尺寸」换算成当前 DPI 下的物理像素。
//
// 背景：进程不声明 DPI 感知时，在 125% 缩放的机器上 Windows 会把整个窗口
// 按 1.25 倍做位图放大，画面（文字、边框、图标）全部发虚。声明感知之后，
// 代码里写的 1180 就真的是 1180 个物理像素，窗口会比原来小一圈，
// 所以尺寸要乘上缩放比，外观大小才和以前一致。

package main

var (
	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	procGetDpiForSystem               = user32.NewProc("GetDpiForSystem")
)

// makeProcessDPIAware 声明进程的 DPI 感知级别，必须在创建任何窗口之前调用。
//
// 优先 Per-Monitor V2（DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2，值为 -4）：
// 窗口在不同缩放比的显示器之间拖动时，系统会让应用重新布局，而不是拉伸位图。
// 系统太老（Win10 1703 之前）时这个调用失败，退回 SetProcessDPIAware()。
//
// 调用时机是关键：窗口一建出来，进程的 DPI 感知就定死了，
// 之后再调会被静默忽略（函数返回 0，不报错），排查起来毫无线索。
func makeProcessDPIAware() {
	// ^uintptr(3) 就是 -4 的无符号形式；Go 里不能直接写负数的 uintptr。
	const perMonitorAwareV2 = ^uintptr(3)
	if r, _, _ := procSetProcessDpiAwarenessContext.Call(perMonitorAwareV2); r != 0 {
		return
	}
	procSetProcessDPIAware.Call()
}

// scaleForDPI 把逻辑尺寸按系统 DPI 换算成物理像素。
//
// 96 DPI 是 100% 缩放。取不到 DPI 时原样返回，不让一次失败的系统调用
// 把窗口尺寸变成 0。
func scaleForDPI(v uint) uint {
	dpi, _, _ := procGetDpiForSystem.Call()
	if dpi == 0 {
		return v
	}
	return v * uint(dpi) / 96
}
