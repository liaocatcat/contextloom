//go:build !windows

package gui

import (
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// unixPTY 基于 creack/pty 的伪终端（macOS/Linux/BSD）。
//
// creack/pty 是纯 Go 实现（ioctl 调用），不依赖 CGO，保持"全平台可交叉编译"
// 特性不变。伪终端让 shell 进入真正的交互模式：提示符、回显、行编辑、
// 颜色、Ctrl+C 中断、vim/top 等全屏程序全部原生可用——对标业界主流桌面编码 Agent
// EUI 命令行的 node-pty 方案（原版注释：node-pty 才能让 PowerShell 进入
// 交互模式，普通管道不行）。
type unixPTY struct {
	file *os.File
	cmd  *exec.Cmd
	pid  int
}

// startPTY 以伪终端启动交互 shell。
func startPTY(shell, cwd string, cols, rows uint16) (*unixPTY, error) {
	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
	)
	// 注意：不能手动设置 SysProcAttr.Setpgid——creack/pty 的 StartWithSize
	// 会设置 Setsid+Setctty；若先 Setpgid(0,0) 使子进程成为进程组长，
	// setsid() 会返回 EPERM（"fork/exec: operation not permitted"）。
	// creack/pty 的 Setsid 已使子进程成为会话首进程（pgid=pid），
	// kill(-pid) 同样可终止 shell 及其全部子进程（vim/top/管道）。
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: cols, Rows: rows})
	if err != nil {
		return nil, err
	}
	return &unixPTY{file: f, cmd: cmd, pid: cmd.Process.Pid}, nil
}

// Read 从 pty 主端读取输出（shell 的 stdout/stderr 已合并为一路）。
func (p *unixPTY) Read(buf []byte) (int, error) { return p.file.Read(buf) }

// Write 向 pty 写入输入（键盘序列/粘贴文本）。
func (p *unixPTY) Write(b []byte) (int, error) { return p.file.Write(b) }

// Resize 调整伪终端行列数（前端 fit 后上报，保证 vim/top 布局正确）。
func (p *unixPTY) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	return pty.Setsize(p.file, &pty.Winsize{Cols: cols, Rows: rows})
}

// Close 关闭 pty 并终止整个进程组。
func (p *unixPTY) Close() error {
	// 先关闭 pty 主端（shell 收到 EOF/HUP），再杀进程组兜底
	_ = p.file.Close()
	if p.pid > 0 {
		// 优雅终止：SIGHUP 到进程组（shell 会通知前台子进程）
		_ = syscall.Kill(-p.pid, syscall.SIGHUP)
		// 短等后强杀，避免残留
		deadline := time.Now().Add(800 * time.Millisecond)
		for time.Now().Before(deadline) {
			if err := syscall.Kill(-p.pid, 0); err != nil {
				return nil // 进程组已全部退出
			}
			time.Sleep(20 * time.Millisecond)
		}
		_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	}
	return nil
}

// Wait 等待进程退出并返回退出码。
func (p *unixPTY) Wait() int {
	if p.cmd == nil {
		return -1
	}
	err := p.cmd.Wait()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			if ws.Exited() {
				return ws.ExitStatus()
			}
			if ws.Signaled() {
				return 128 + int(ws.Signal())
			}
		}
	}
	return -1
}
