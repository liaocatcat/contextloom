//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// prepareProcess 在 Start 前配置子进程（Unix 实现）：
// 设为进程组长（Setpgid），使超时后 kill(-pgid, SIGKILL) 能杀整棵进程树。
// 放在独立文件（build tag !windows）中——syscall.SysProcAttr 的 Setpgid
// 字段与 syscall.Kill 在 Windows 上不存在，若写在 exec.go 会导致
// Windows 编译失败（runtime.GOOS 是运行时判断，编译期两个分支都会检查）。
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree 终止命令的整棵进程树（Unix 实现）：
// kill(-pgid, SIGKILL) 杀掉 shell 及其所有孙子进程——只杀 shell 会让
// 孙子进程残留并持有 stdout 管道，导致输出收集永久阻塞。
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
