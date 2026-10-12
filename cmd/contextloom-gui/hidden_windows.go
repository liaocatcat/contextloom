//go:build windows

package main

import "syscall"

// hiddenProcAttr 返回 Windows 子进程的 SysProcAttr：设置
// CREATE_NO_WINDOW（0x08000000）。
//
// 背景：contextloom-gui 以 GUI 子系统构建（-H windowsgui）后自身没有控制台，
// 若拉起 console 子系统子进程（contextloom --serve、taskkill、cmd /c start），
// Windows 会为它们【新建控制台窗口】——每次启动 GUI/切换项目/退出都弹出
// 黑色命令行窗口（用户痛点："GUI 不能静默后台打开"）。设置
// CREATE_NO_WINDOW 后子进程不创建任何窗口，静默后台运行；
// 与 StdoutPipe/Stderr 重定向兼容（输出走管道，不需要控制台）。
func hiddenProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
