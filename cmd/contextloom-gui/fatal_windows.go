//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// Windows GUI 程序（双击运行）没有控制台，log 输出不可见——启动失败时
// 必须弹系统对话框，否则用户看到的就是"闪退"（进程静默消失）。
// 此处直接调用 user32.MessageBoxW（无需引入额外依赖）。

var (
	user32          = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32.NewProc("MessageBoxW")
)

const (
	mbOK      = 0x00000000
	mbIconErr = 0x00000010
)

// showFatalBox 弹出错误对话框并阻塞到用户点击（Windows）。
// 平台无关的 fatalExit 在调用前已把错误写入 gui.log，对话框内容
// 引用日志路径，用户可据此进一步排查。
func showFatalBox(title, msg string) {
	t, err1 := syscall.UTF16PtrFromString(title)
	m, err2 := syscall.UTF16PtrFromString(msg)
	if err1 != nil || err2 != nil {
		return
	}
	// MessageBoxW(HWND, LPCWSTR text, LPCWSTR caption, UINT type)
	_, _, _ = procMessageBoxW.Call(0,
		uintptr(unsafe.Pointer(m)),
		uintptr(unsafe.Pointer(t)),
		mbOK|mbIconErr)
}
