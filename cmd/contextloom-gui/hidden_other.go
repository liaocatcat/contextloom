//go:build !windows

package main

import "syscall"

// hiddenProcAttr 非 Windows 平台无需隐藏控制台（终端环境本就无弹出窗口）。
func hiddenProcAttr() *syscall.SysProcAttr { return nil }
