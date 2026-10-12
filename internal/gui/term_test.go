package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// waitTermOutput 读取 WS 事件直到收到指定终端的输出包含 want（或超时）。
func waitTermOutput(t *testing.T, conn *websocket.Conn, id, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var acc strings.Builder
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type != "event" || env.Event != "term.output" {
			continue
		}
		data, ok := env.Data.(map[string]any)
		if !ok || data["id"] != id {
			continue
		}
		s, _ := data["data"].(string)
		acc.WriteString(s)
		if strings.Contains(acc.String(), want) {
			return acc.String()
		}
	}
	return acc.String()
}

// TestWS_Term_SpawnListInputKill 多终端全链路：spawn → list → input → output
// 事件收到 → buffer 可拉取 → kill 清理。
func TestWS_Term_SpawnListInputKill(t *testing.T) {
	requirePTYEnv(t)
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)

	// spawn 两个终端（多窗口并行）
	resp := sendReq(t, conn, "1", "term.spawn", nil)
	if !resp.OK {
		t.Fatalf("term.spawn 失败: %v", resp.Error)
	}
	id1 := resp.Result.(map[string]any)["id"].(string)
	resp = sendReq(t, conn, "2", "term.spawn", nil)
	if !resp.OK {
		t.Fatalf("term.spawn(2) 失败: %v", resp.Error)
	}
	id2 := resp.Result.(map[string]any)["id"].(string)
	if id1 == id2 {
		t.Fatalf("两个终端 ID 重复: %s", id1)
	}

	// list：两个都在
	resp = sendReq(t, conn, "3", "term.list", nil)
	if !resp.OK {
		t.Fatalf("term.list 失败: %v", resp.Error)
	}
	if got := len(resp.Result.([]any)); got != 2 {
		t.Fatalf("term.list 期望 2 个终端，实际 %d", got)
	}

	// input：向终端 1 发 echo（shell 会回显输出）
	if resp = sendReq(t, conn, "4", "term.input", map[string]any{"id": id1, "data": "echo TERM_TEST_ECHO_42\n"}); !resp.OK {
		t.Fatalf("term.input 失败: %v", resp.Error)
	}

	// 等待 term.output 事件携带输出
	got := waitTermOutput(t, conn, id1, "TERM_TEST_ECHO_42", 8*time.Second)
	if !strings.Contains(got, "TERM_TEST_ECHO_42") {
		t.Fatalf("未收到终端 1 输出，累计: %q", got)
	}

	// buffer：重连恢复画面（拉取历史）
	resp = sendReq(t, conn, "5", "term.buffer", map[string]any{"id": id1})
	if !resp.OK {
		t.Fatalf("term.buffer 失败: %v", resp.Error)
	}
	if buf, _ := resp.Result.(string); !strings.Contains(buf, "TERM_TEST_ECHO_42") {
		t.Fatalf("term.buffer 不包含输出: %q", buf)
	}

	// kill：清理终端 2
	if resp = sendReq(t, conn, "6", "term.kill", map[string]any{"id": id2}); !resp.OK {
		t.Fatalf("term.kill 失败: %v", resp.Error)
	}
	resp = sendReq(t, conn, "7", "term.list", nil)
	if !resp.OK {
		t.Fatalf("term.list(2) 失败: %v", resp.Error)
	}
	if got := len(resp.Result.([]any)); got != 1 {
		t.Fatalf("kill 后期望 1 个终端，实际 %d", got)
	}
}

// TestWS_Term_KillUnknown 不存在的终端 kill 报错而非 panic。
func TestWS_Term_KillUnknown(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "term.kill", map[string]any{"id": "t999"})
	if resp.OK {
		t.Fatal("kill 不存在终端应报错")
	}
}

// TestWS_Term_InputUnknown 不存在的终端 input 报错。
func TestWS_Term_InputUnknown(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "term.input", map[string]any{"id": "t999", "data": "x"})
	if resp.OK {
		t.Fatal("input 不存在终端应报错")
	}
}

// TestWS_Term_Resize PTY 尺寸同步链路：spawn → resize（后端 PTY 调整行列）
// → 未知终端 resize 报错。
func TestWS_Term_Resize(t *testing.T) {
	requirePTYEnv(t)
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "term.spawn", nil)
	if !resp.OK {
		t.Fatalf("term.spawn 失败: %v", resp.Error)
	}
	id := resp.Result.(map[string]any)["id"].(string)

	// 正常 resize（前端 xterm fit 后上报）
	resp = sendReq(t, conn, "2", "term.resize", map[string]any{"id": id, "cols": 120, "rows": 40})
	if !resp.OK {
		t.Fatalf("term.resize 失败: %v", resp.Error)
	}

	// 未知终端 resize → 报错（不 panic）
	resp = sendReq(t, conn, "3", "term.resize", map[string]any{"id": "t999", "cols": 80, "rows": 24})
	if resp.OK {
		t.Fatal("未知终端 resize 应失败")
	}

	// resize 后终端仍可输入（PTY 未受影响）
	if resp = sendReq(t, conn, "4", "term.input", map[string]any{"id": id, "data": "echo TERM_RESIZE_ALIVE\n"}); !resp.OK {
		t.Fatalf("resize 后 term.input 失败: %v", resp.Error)
	}
	got := waitTermOutput(t, conn, id, "TERM_RESIZE_ALIVE", 8*time.Second)
	if !strings.Contains(got, "TERM_RESIZE_ALIVE") {
		t.Fatalf("resize 后未收到终端输出，累计: %q", got)
	}
}

// TestWS_Term_PTYInteractive 验证 PTY 交互模式：spawn 后【无任何输入】，
// shell 也应主动输出提示符（普通管道模式下非交互 shell 不输出提示符——
// 这正是"没有输入方式、不能操作"的根源，PTY 化后必须出现提示符）。
func TestWS_Term_PTYInteractive(t *testing.T) {
	requirePTYEnv(t)
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "term.spawn", nil)
	if !resp.OK {
		t.Fatalf("term.spawn 失败: %v", resp.Error)
	}
	id := resp.Result.(map[string]any)["id"].(string)

	// 无输入等待 shell 初始输出（提示符）
	got := waitTermOutputAny(t, conn, id, 5*time.Second)
	if got == "" {
		t.Fatal("PTY 模式下 shell 应主动输出提示符（交互模式标志），但未收到任何输出")
	}
}

// waitTermOutputAny 读取 WS 事件直到收到指定终端的任意输出（或超时）。
func waitTermOutputAny(t *testing.T, conn *websocket.Conn, id string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var acc strings.Builder
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type != "event" || env.Event != "term.output" {
			continue
		}
		data, ok := env.Data.(map[string]any)
		if !ok || data["id"] != id {
			continue
		}
		s, _ := data["data"].(string)
		acc.WriteString(s)
		if acc.Len() > 0 {
			return acc.String()
		}
	}
	return acc.String()
}
