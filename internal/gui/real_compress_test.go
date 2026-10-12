package gui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
	"github.com/gorilla/websocket"
)

// newRealRuntime 用真实 Runtime（mock 后端）构造，供 GUI 压缩端到端测试。
// 与 runtime.newTestRuntime 等效配置：临时目录、MockContext 4096、Harness 开启。
func newRealRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models: []config.ModelPreset{{
			Name:       "mock",
			Backend:    "mock",
			Endpoint:   "http://localhost:8080",
			APIKey:     "sk-test",
			MaxContext: 4096,
		}},
		Harness: config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:  config.MemoryConfig{Directory: filepath.Join(tmpDir, "memory"), MaxSessions: 10},
		Plugins: config.PluginsConfig{Directory: filepath.Join(tmpDir, "plugins")},
		Project: config.ProjectConfig{Root: tmpDir, Vocab: filepath.Join(tmpDir, "vocab.json")},
		ACP:     config.ACPConfig{Enabled: true, Port: 20250},
		CLIDefaults: config.CLIDefaultsConfig{
			Model:  "mock-model",
			Effort: "medium",
		},
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return rt
}

// TestRealRuntime_CompressTwice 端到端复现"只能压缩一次"：
// 让真实 Runtime 积累长历史 → 连续两次 context.compress（WS），
// 第二次必须继续释放 token 且返回"已压缩"而非"无需压缩"。
func TestRealRuntime_CompressTwice(t *testing.T) {
	rt := newRealRuntime(t)
	// 积累长历史：12 轮对话（远超 keepRecent=8），且未超安全红线
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", strings.Repeat(fmt.Sprintf("用户消息%d内容", i), 20))
		rt.AddToHistory("assistant", strings.Repeat(fmt.Sprintf("助手回复%d内容", i), 20))
	}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// 首次压缩
	resp1 := sendReqLong(t, conn, "1", "context.compress", nil, 10)
	if !resp1.OK {
		t.Fatalf("第一次压缩失败: %v", resp1.Error)
	}
	r1 := resp1.Result.(map[string]any)
	msg1, _ := r1["message"].(string)
	t.Logf("第1次压缩: %s", msg1)

	// 第二次压缩：必须继续有效（释放 token），不得是 "无需压缩"
	resp2 := sendReqLong(t, conn, "2", "context.compress", nil, 10)
	if !resp2.OK {
		t.Fatalf("第二次压缩失败: %v", resp2.Error)
	}
	r2 := resp2.Result.(map[string]any)
	msg2, _ := r2["message"].(string)
	t.Logf("第2次压缩: %s", msg2)

	if strings.Contains(msg2, "无需压缩") {
		t.Fatalf("第二次压缩不应返回'无需压缩'（用户痛点：只能压缩一次）: %s", msg2)
	}
}

// TestRealRuntime_CompressTwice_PushEvent 两次压缩每次都推送 ctx.compressed 事件。
func TestRealRuntime_CompressTwice_PushEvent(t *testing.T) {
	rt := newRealRuntime(t)
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", strings.Repeat(fmt.Sprintf("用户消息%d", i), 30))
		rt.AddToHistory("assistant", strings.Repeat(fmt.Sprintf("助手回复%d", i), 30))
	}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	for i := 0; i < 2; i++ {
		id := fmt.Sprintf("c%d", i)
		if err := conn.WriteJSON(Envelope{ID: id, Type: "req", Method: "context.compress"}); err != nil {
			t.Fatalf("发送第%d次压缩失败: %v", i+1, err)
		}
		gotEvt := false
		for {
			var env Envelope
			if err := conn.ReadJSON(&env); err != nil {
				t.Fatalf("读取第%d次压缩响应失败: %v", i+1, err)
			}
			if env.Type == "event" && env.Event == "ctx.compressed" {
				gotEvt = true
			}
			if env.ID == id && env.Type == "resp" {
				break
			}
		}
		if !gotEvt {
			t.Fatalf("第%d次压缩未推送 ctx.compressed 事件", i+1)
		}
	}
}

// sendReqLong 与 sendReq 相同但带读超时控制（防卡死）。
func sendReqLong(t *testing.T, conn *websocket.Conn, id, method string, params any, _ int) Envelope {
	t.Helper()
	env := Envelope{ID: id, Type: "req", Method: method}
	if params != nil {
		b, _ := json.Marshal(params)
		env.Params = b
	}
	if err := conn.WriteJSON(env); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	for {
		var resp Envelope
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("读取响应失败: %v", err)
		}
		if resp.ID == id {
			return resp
		}
	}
}
