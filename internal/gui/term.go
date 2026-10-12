// Package gui 终端管理器：多窗口命令行支持（需求：像 业界主流桌面编码 Agent 那样，
// 全平台命令行窗口，多窗口并行运行、随时切换）。
//
// 设计（对标业界主流桌面编码 Agent 的 EUI 命令行的 node-pty 方案）：
//   - 每个终端 = 一个独立 shell 子进程，挂载【真正的伪终端 PTY】
//     （Unix: creack/pty；Windows: ConPTY）。PTY 让 shell 进入交互模式：
//     提示符、回显、行编辑、颜色、Ctrl+C 中断、vim/top 等全屏程序
//     全部原生可用——普通管道做不到（这就是"没有输入方式、不能操作"
//     的根源）。
//   - 工作目录 = backend 项目根（随"打开目录"切换）。
//   - stdin 由 term.input 写入 PTY 主端；stdout/stderr 从 PTY 合并读出，
//     经 WS 事件 term.output 广播给所有连接（多窗口可同时看到同一终端）。
//   - 每个终端保留最近 64KB 输出环形缓冲，term.buffer 可拉取历史
//     （前端重连/刷新时恢复画面）。
//   - 尺寸同步：前端 xterm fit 后经 term.resize 上报，PTY 同步
//     （Unix: TIOCSWINSZ；Windows: ResizePseudoConsole），保证 vim/top
//     等全屏程序布局正确。
//   - 进程树终止：Unix 用进程组（Setpgid + kill(-pgid)），Windows 用
//     taskkill /T /F——平台差异在 term_pty_unix.go / term_pty_windows.go
//     中隔离（build tag），避免 Windows 编译失败。
package gui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// termMaxBuffer 每个终端保留的输出缓冲上限（64KB，防止无限膨胀）。
const termMaxBuffer = 64 * 1024

// ptyProc 平台无关的伪终端接口：
//   - Unix（term_pty_unix.go）：creack/pty（纯 Go ioctl，无 CGO，可交叉编译）
//   - Windows（term_pty_windows.go）：ConPTY（CreatePseudoConsole + 动态加载，
//     无 CGO，可交叉编译）
type ptyProc interface {
	// Read 读取终端输出（shell stdout/stderr 已合并为一路原始字节流，
	// 包含 ANSI 转义序列——不能按行读，否则破坏光标移动/进度条/全屏程序）。
	Read(p []byte) (int, error)
	// Write 写入输入（键盘序列/粘贴文本，原样交给 shell 处理）。
	Write(p []byte) (int, error)
	// Resize 调整终端行列数（前端 fit 后上报）。
	Resize(cols, rows uint16) error
	// Close 关闭终端并终止整个进程树。
	Close() error
	// Wait 阻塞等待进程退出并返回退出码。
	Wait() int
}

// ringBuf 固定大小环形字节缓冲。
type ringBuf struct {
	mu  sync.Mutex
	buf []byte
}

func newRingBuf(cap int) *ringBuf { return &ringBuf{buf: make([]byte, 0, cap)} }

func (r *ringBuf) append(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.buf)+len(p) > cap(r.buf) {
		// 丢弃最旧，保留最新
		drop := len(r.buf) + len(p) - cap(r.buf)
		if drop >= len(r.buf) {
			r.buf = append(r.buf[:0], p[len(p)-cap(r.buf):]...)
			return
		}
		r.buf = r.buf[drop:]
	}
	r.buf = append(r.buf, p...)
}

func (r *ringBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}

// TermProc 一个运行中的终端。
type TermProc struct {
	ID    string `json:"id"`
	Shell string `json:"shell"`
	CWD   string `json:"cwd"`
	// 进程退出后置位（term.exit 事件已发送）
	Exited bool `json:"exited"`

	pty    ptyProc
	output *ringBuf
	done   chan struct{}
	mu     sync.Mutex
}

// TermManager 管理多个并行终端。
type TermManager struct {
	s     *Server
	mu    sync.Mutex
	terms map[string]*TermProc
	seq   int
}

// newTermManager 创建终端管理器。
func newTermManager(s *Server) *TermManager {
	return &TermManager{s: s, terms: map[string]*TermProc{}}
}

// spawn 启动一个新终端（cwd 为空时使用 backend 项目根；cols/rows 为初始
// 终端尺寸，0 表示使用默认 80x24，前端 fit 后会经 term.resize 校正）。
func (m *TermManager) spawn(cwd string, cols, rows uint16) (*TermProc, error) {
	shell := defaultShell()
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	p, err := startPTY(shell, cwd, cols, rows)
	if err != nil {
		return nil, fmt.Errorf("启动终端 shell %q 失败: %w", shell, err)
	}

	m.mu.Lock()
	m.seq++
	id := fmt.Sprintf("t%d", m.seq)
	t := &TermProc{
		ID:     id,
		Shell:  shell,
		CWD:    cwd,
		pty:    p,
		output: newRingBuf(termMaxBuffer),
		done:   make(chan struct{}),
	}
	m.terms[id] = t
	m.mu.Unlock()

	go m.readLoop(t)
	go m.waitLoop(t)
	return t, nil
}

// readLoop 持续从 PTY 读取终端输出并广播。
// 注意：必须按【原始字节流】读取（32KB 块），不能用 bufio.Scanner 按行
// 读——行读会破坏 ANSI 转义序列（\r 回车、光标移动、进度条刷新、全屏
// 程序重绘），导致显示错乱。
func (m *TermManager) readLoop(t *TermProc) {
	buf := make([]byte, 32*1024)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			// 复制一份（buf 复用，避免事件异步消费时被覆盖）
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			t.output.append(chunk)
			m.s.broadcast(Envelope{
				Type: "event", Event: "term.output",
				Data: map[string]any{"id": t.ID, "data": string(chunk)},
			})
		}
		if err != nil {
			if !isTermClosedErr(err) {
				fmt.Fprintf(os.Stderr, "[term %s] 读取输出结束: %v\n", t.ID, err)
			}
			break
		}
	}
}

// waitLoop 等待进程退出并广播 term.exit。
func (m *TermManager) waitLoop(t *TermProc) {
	code := t.pty.Wait()
	close(t.done)
	t.mu.Lock()
	t.Exited = true
	t.mu.Unlock()
	m.s.broadcast(Envelope{
		Type: "event", Event: "term.exit",
		Data: map[string]any{"id": t.ID, "code": code},
	})
}

// input 向终端写入输入（键盘序列/粘贴文本，原样写入 PTY）。
func (m *TermManager) input(id, data string) error {
	m.mu.Lock()
	t, ok := m.terms[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("终端 %s 不存在", id)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Exited {
		return fmt.Errorf("终端 %s 已退出", id)
	}
	if _, err := t.pty.Write([]byte(data)); err != nil {
		return fmt.Errorf("写入终端 %s 失败: %w", id, err)
	}
	return nil
}

// resize 调整终端尺寸（前端 xterm fit 后上报，保证全屏程序布局正确）。
func (m *TermManager) resize(id string, cols, rows uint16) error {
	m.mu.Lock()
	t, ok := m.terms[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("终端 %s 不存在", id)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.Exited {
		return fmt.Errorf("终端 %s 已退出", id)
	}
	if err := t.pty.Resize(cols, rows); err != nil {
		return fmt.Errorf("调整终端 %s 尺寸失败: %w", id, err)
	}
	return nil
}

// kill 终止终端（PTY 关闭 + 进程树）。
func (m *TermManager) kill(id string) error {
	m.mu.Lock()
	t, ok := m.terms[id]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("终端 %s 不存在", id)
	}
	_ = t.pty.Close() // 关闭 PTY 并终止进程树（Unix kill(-pgid) / Windows taskkill+ClosePseudoConsole）
	select {
	case <-t.done:
	case <-time.After(2 * time.Second):
		// 兜底：进程未及时退出也继续清理（避免 kill 卡死）
	}
	m.mu.Lock()
	delete(m.terms, id)
	m.mu.Unlock()
	return nil
}

// buffer 返回终端输出缓冲（重连恢复画面）。
func (m *TermManager) buffer(id string) (string, error) {
	m.mu.Lock()
	t, ok := m.terms[id]
	m.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("终端 %s 不存在", id)
	}
	return t.output.String(), nil
}

// list 列出全部终端（不含已删除的）。
func (m *TermManager) list() []map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]map[string]any, 0, len(m.terms))
	for _, t := range m.terms {
		t.mu.Lock()
		out = append(out, map[string]any{
			"id": t.ID, "shell": t.Shell, "cwd": t.CWD, "exited": t.Exited,
		})
		t.mu.Unlock()
	}
	return out
}

// closeAll 关闭全部终端（服务退出时调用）。
func (m *TermManager) closeAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.terms))
	for id := range m.terms {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.kill(id)
	}
}

// isTermClosedErr 判断读循环的终止错误是否为"正常关闭"（PTY 关闭/进程退出
// 时各平台的典型错误），避免误报日志。
func isTermClosedErr(err error) bool {
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
		return true
	}
	msg := err.Error()
	// Unix: PTY 主端关闭后 Read 返回 EIO
	if strings.Contains(msg, "input/output error") {
		return true
	}
	// Windows ConPTY: 子进程退出/伪控制台关闭时的典型错误
	if strings.Contains(msg, "pipe has been ended") || // ERROR_BROKEN_PIPE
		strings.Contains(msg, "operation has been aborted") || // ERROR_OPERATION_ABORTED
		strings.Contains(msg, "handle is invalid") { // ERROR_INVALID_HANDLE
		return true
	}
	return false
}

// defaultShell 返回平台默认 shell。
func defaultShell() string {
	if runtime.GOOS == "windows" {
		// cmd.exe 全平台 Windows 自带（PowerShell 7 可能未安装，不作为默认）
		return "cmd.exe"
	}
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/sh"
}

// broadcast 向所有 WS 连接广播事件（终端输出、流式 agent.stream/done 等
// 全局事件）。流式事件必须广播：前端刷新/切换项目会换连接，只发给发起流
// 的旧连接会导致重连后界面收不到后续 delta（表现为"对话被中止"）。
func (s *Server) broadcast(env Envelope) {
	s.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	for _, c := range conns {
		_ = s.send(c, env)
	}
}
