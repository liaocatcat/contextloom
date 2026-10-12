//go:build !windows

package plugin

import (
	"os/exec"
	"syscall"
)

// applySysProcAttr 设置 Unix 进程属性：无条件用 Setpgid 隔离进程组——
// 进程组是超时后 killProcessTree 杀整棵进程树（kill(-pgid)）的前提，
// 与内存限制无关，故不依赖 memLimit。
func applySysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree 终止命令的整棵进程树（Unix 实现）：
// kill(-pgid, SIGKILL) 杀掉脚本及其全部孙子进程——只杀直接子进程会让
// 孙子进程残留并持有 stdout 管道，导致输出收集永久阻塞。
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
