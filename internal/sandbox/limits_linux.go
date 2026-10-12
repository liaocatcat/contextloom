//go:build linux

package sandbox

import (
	"os/exec"
	"syscall"
)

// ApplyLimits 对沙箱模式下的子进程施加资源限制（Linux 原生支持）：
//   - RLIMIT_AS：地址空间上限 4GB（防内存炸弹）；
//   - RLIMIT_NOFILE：文件描述符上限 256（防 fd 耗尽）；
//   - RLIMIT_CPU：CPU 时间上限 300s（防失控计算）。
func ApplyLimits(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	var as = uint64(4 << 30) // 4GB
	var nofile = uint64(256)
	var cpu = uint64(300)
	cmd.SysProcAttr.Rlimit = []syscall.Rlimit{
		{Cur: as, Max: as},
		{Cur: nofile, Max: nofile},
		{Cur: cpu, Max: cpu},
	}
}
