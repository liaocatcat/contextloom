//go:build linux

package sandbox

import (
	"os/exec"
	"strings"
)

// ApplyLimits 对沙箱模式下的子进程施加资源限制（Linux）。
//
// 历史 bug：早期实现写的是 `cmd.SysProcAttr.Rlimit = []syscall.Rlimit{...}`，
// 但 Go 标准库的 syscall.SysProcAttr 根本没有 Rlimit 字段——该文件自创建起
// 就无法编译，导致【所有 GOOS=linux 目标构建全部失败】（仓库里因此长期没有
// 任何 Linux 产物，交叉编译矩阵形同虚设）。
//
// 修复思路：os/exec 没有"在子进程 exec 前设置 rlimit"的原生钩子
// （x/sys/unix.Prlimit 在父进程侧调用存在竞态：子进程可能已 exec）。
// 这里采用 POSIX 便携且无竞态的做法——利用调用方固定以 `sh -c <cmd>` 执行的
// 事实，把 ulimit 前缀注入 shell 命令字符串，由 shell 自身对进程施加限制，
// 限制随 fork 继承到整棵进程树。
//
// 映射关系（与原注释意图一致）：
//   - RLIMIT_AS    地址空间上限 4GB  -> ulimit -v 4194304（单位 KB）
//   - RLIMIT_NOFILE 文件描述符 256   -> ulimit -n 256
//   - RLIMIT_CPU   CPU 时间 300s     -> ulimit -t 300（单位秒）
//
// 每项加 2>/dev/null 兜底：个别内核/容器不允许调低某限制时 ulimit 会报错，
// 静默忽略即可，不能让沙箱因"限制设不上"而整体执行失败。
func ApplyLimits(cmd *exec.Cmd) {
	if cmd == nil || len(cmd.Args) < 3 {
		return
	}
	// 仅在调用方按 `sh -c <command>` 构造时注入（exec.go 的 Unix 路径）。
	if cmd.Args[0] != "sh" || cmd.Args[1] != "-c" {
		return
	}
	const preamble = "ulimit -v 4194304 2>/dev/null; ulimit -n 256 2>/dev/null; ulimit -t 300 2>/dev/null; "
	if strings.HasPrefix(cmd.Args[2], preamble) {
		return // 幂等：避免重复注入
	}
	cmd.Args[2] = preamble + cmd.Args[2]
}
