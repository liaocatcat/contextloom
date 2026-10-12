//go:build windows

package mcpclient

import (
	"os/exec"
	"strconv"
)

// prepareProcess 在 Start 前配置子进程（Windows 实现）：
// Windows 无进程组概念，无需额外配置。
func prepareProcess(cmd *exec.Cmd) {}

// killProcessTree 终止命令的整棵进程树（Windows 实现）：
// taskkill /T /F 递归终止，覆盖 MCP server 及其派生的全部子进程。
func killProcessTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
