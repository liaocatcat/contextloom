//go:build !linux

package sandbox

import "os/exec"

// ApplyLimits 非 Linux 占位：macOS/Windows 的资源限制依赖
// 系统级机制（超时+进程树终止已由 exec 层兜底），此处不额外施加。
func ApplyLimits(cmd *exec.Cmd) {}
