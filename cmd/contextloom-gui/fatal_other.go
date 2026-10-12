//go:build !windows

package main

// 非 Windows 平台（macOS/Linux）：终端/Finder 启动时日志可见或可从
// 系统日志查看，无需弹对话框（保持零额外依赖）。
func showFatalBox(title, msg string) {}
