//go:build windows

package gui

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestStartPTY_Interactive 回归测试：ConPTY 启动 cmd.exe 后必须保持存活并
// 可交互（写命令 → 读到输出），而不是 banner 后立即退出。
//
// 历史根因（"进程已退出，代码0"）：CreateProcess 未设置 STARTF_USESTDHANDLES
// 时子进程【继承父进程标准句柄】——父进程是管道/SSH/agent（stdin=EOF 管道）
// 或 GUI 双击（stdin=NULL）时，cmd 读 stdin 立即 EOF → 提示符显示后瞬间退出
// 0。修复：对齐 node-pty conpty.cc 设置 STARTF_USESTDHANDLES 且三个标准句柄
// 全 NULL，强制 cmd 走 ConPTY 控制台（conhost）的输入输出路径。
func TestStartPTY_Interactive(t *testing.T) {
	requirePTYEnv(t)
	p, err := startPTY("cmd.exe", "", 80, 24)
	if err != nil {
		t.Fatalf("startPTY 失败: %v", err)
	}
	defer p.Close()

	// 读输出（goroutine，收集到 channel）
	outCh := make(chan string, 100)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		var sb strings.Builder
		for {
			n, err := p.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
				s := sb.String()
				// 每收到数据就上报一次（调用方做包含判断）
				select {
				case outCh <- s:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// 等待 banner（3 秒内）
	waitOutput(t, outCh, ">", 3*time.Second, "cmd 提示符")

	// 写入命令并等待回显/结果
	if _, err := p.Write([]byte("echo PTY_OK\r\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	waitOutput(t, outCh, "PTY_OK", 5*time.Second, "命令输出")

	// 进程必须仍存活（没有立即退出）
	var code uint32
	if err := windows.GetExitCodeProcess(windows.Handle(p.(*conptyProc).proc), &code); err != nil {
		t.Fatalf("GetExitCodeProcess 失败: %v", err)
	}
	if code != 259 { // STILL_ACTIVE
		t.Fatalf("cmd 已退出（代码 %d）——ConPTY 交互失败", code)
	}
	_ = readDone
}

// waitOutput 等待 outCh 中出现包含 want 的输出。
func waitOutput(t *testing.T, ch chan string, want string, timeout time.Duration, what string) {
	t.Helper()
	deadline := time.After(timeout)
	var last string
	for {
		select {
		case s := <-ch:
			last = s
			if strings.Contains(s, want) {
				return
			}
		case <-deadline:
			t.Fatalf("超时 %v 未等到 %q（最后输出: %q）", timeout, what, last)
		}
	}
}
