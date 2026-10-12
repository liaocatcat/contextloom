//go:build !windows

package mcpclient

import (
	"os/exec"
	"syscall"
)

// prepareProcess 在 Start 前配置子进程（Unix 实现）：
// 设为进程组长（Setpgid），使 Close 时 kill(-pgid, SIGKILL) 能杀整棵进程树。
func prepareProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessTree 终止命令的整棵进程树（Unix 实现）：
// kill(-pgid, SIGKILL) 杀掉 MCP server 及其所有子进程。
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
