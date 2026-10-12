//go:build !windows

package main

import "syscall"

// Unix 平台（macOS/Linux）backend 进程终止：
//   - signalTerminate：优雅终止（SIGTERM，Go 程序默认行为是退出）
//   - signalKill：强制终止（SIGKILL，仅优雅退出超时后兜底）
//
// 注意：必须直接对 PID 发信号（syscall.Kill），不能用 os.FindProcess +
// Process.Signal——后者在 Windows 上不支持 SIGTERM（返回 "not supported
// by windows"，导致 stop() 等满 3 秒超时才强杀，切换/退出卡顿）。
func signalTerminate(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func signalKill(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}

// processAlive 检查进程是否存活（测试用；syscall.Kill(pid, 0) 仅探测不发送信号）。
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
