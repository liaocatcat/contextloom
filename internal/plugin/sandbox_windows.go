//go:build windows

package plugin

import (
	"os/exec"
	"strconv"
	"syscall"
)

// applySysProcAttr Windows 版本：SysProcAttr 无 Setpgid 字段，
// 使用 CreationFlags 隐藏控制台窗口（CREATE_NO_WINDOW），避免插件弹窗。
func applySysProcAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// killProcessTree 终止命令的整棵进程树（Windows 实现）：
// taskkill /T /F 递归终止，覆盖脚本及其派生的全部子进程，
// 防止孙子进程残留并持有 stdout 管道导致输出收集永久阻塞。
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
