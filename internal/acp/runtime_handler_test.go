package acp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/executor"
	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// fakeBackend 流式 mock：输出固定 tokens。
type fakeBackend struct {
	tokens []string
}

func (m *fakeBackend) Chat(ctx context.Context, msgs []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	s := ""
	for _, t := range m.tokens {
		s += t
	}
	return s, nil, nil
}

func (m *fakeBackend) Stream(ctx context.Context, msgs []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, len(m.tokens)+1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, t := range m.tokens {
			select {
			case <-ctx.Done():
				return
			case ch <- t:
			}
		}
	}()
	return ch, errCh
}

func (m *fakeBackend) ModelID() string                 { return "mock" }
func (m *fakeBackend) MaxContext() int                 { return 131072 }
func (m *fakeBackend) SupportsVision() bool            { return false }
func (m *fakeBackend) SetReasoningEffort(level string) {}
func (m *fakeBackend) ReasoningEffort() string         { return "" }
func (m *fakeBackend) SetTopP(p float64)               {}
func (m *fakeBackend) TopP() float64                   { return 0 }
func (m *fakeBackend) SetTemperature(t float64)        {}
func (m *fakeBackend) Temperature() float64            { return 0 }

// mockSpawner 通过公开 API 创建 agent（executor.New + SpawnAgent）。
type mockSpawner struct {
	mu      sync.Mutex
	agents  []*executor.Agent
	backend model.Backend
	eng     *harness.Engine
	reg     *tools.Registry
}

func newMockSpawner(backend model.Backend) *mockSpawner {
	return &mockSpawner{
		backend: backend,
		eng:     harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2}),
		reg:     tools.NewRegistry(),
	}
}

func (s *mockSpawner) spawn(id string, task types.TaskCreate, backend model.Backend) *executor.Agent {
	ex := executor.New(s.eng, s.reg)
	a := ex.SpawnAgent(id, task, s.backend)
	s.mu.Lock()
	s.agents = append(s.agents, a)
	s.mu.Unlock()
	return a
}

// startHandlerServer 启动带 RuntimeHandler 的测试服务器。
func startHandlerServer(t *testing.T, backend model.Backend, policy string) (string, *RuntimeHandler, *mockSpawner) {
	t.Helper()
	srv := NewServer(0)
	reg := tools.NewRegistry()
	sp := newMockSpawner(backend)
	h := NewRuntimeHandler(HandlerDeps{
		Spawn:            sp.spawn,
		Tools:            reg,
		Backend:          backend,
		PersistencePaths: func() map[string]string { return map[string]string{"config_file": "/x/config.yaml"} },
		MaxContext:       131072,
		ModelID:          "mock",
		Tracer:           nil,
	}, srv, policy, false)
	srv.SetHandler(h)
	ts := httptest.NewServer(nil)
	addr := ts.Listener.Addr().String()
	ts.Close()
	go func() { _ = srv.StartAddr(addr) }()
	t.Cleanup(srv.Shutdown)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := Dial(addr)
		if err == nil {
			c.Close()
			return addr, h, sp
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ACP 服务器未就绪: %s", addr)
	return "", h, sp
}

func newSession(t *testing.T, c *Client) string {
	t.Helper()
	_, _, _ = c.Recv() // hello
	result, err := c.Request(MethodSessionNew, SessionNewParams{})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)
	return rp.SessionID
}

// TestHandler_PromptStreamsAgentMessage：OnPrompt → 流式 agent_message → prompt_response。
func TestHandler_PromptStreamsAgentMessage(t *testing.T) {
	addr, h, _ := startHandlerServer(t, &fakeBackend{tokens: []string{"你好", "，世界"}}, "quiet")
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sid := newSession(t, c)

	if _, err := c.Request(MethodSessionPrompt, SessionPromptParams{SessionID: sid, Prompt: "说你好"}); err != nil {
		t.Fatal(err)
	}
	var texts []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m, p, err := c.Recv()
		if err != nil {
			break
		}
		if m == MethodAgentMessage {
			var amp AgentMessageParams
			json.Unmarshal(p, &amp)
			var blocks []ContentBlock
			json.Unmarshal(amp.Message, &blocks)
			for _, b := range blocks {
				texts = append(texts, b.Text)
			}
			if len(texts) >= 2 {
				break
			}
		}
		if m == MethodPromptResponse {
			break
		}
	}
	if len(texts) < 2 {
		t.Fatalf("未收到足够 agent_message: %v", texts)
	}
	joined := ""
	for _, s := range texts {
		joined += s
	}
	if joined != "你好，世界" {
		t.Fatalf("流内容 = %q", joined)
	}
	// 会话历史已更新（两轮）
	h.mu.Lock()
	hist := h.histories[sid]
	h.mu.Unlock()
	if len(hist) != 2 || hist[0].Role != "user" || hist[1].Role != "assistant" {
		t.Fatalf("历史未更新: %+v", hist)
	}
}

// TestHandler_MultiTurnHistory：第二次 prompt 携带历史（多轮连续性）。
func TestHandler_MultiTurnHistory(t *testing.T) {
	addr, h, sp := startHandlerServer(t, &fakeBackend{tokens: []string{"ok"}}, "quiet")
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sid := newSession(t, c)

	for i := 0; i < 2; i++ {
		if _, err := c.Request(MethodSessionPrompt, SessionPromptParams{SessionID: sid, Prompt: "第" + string(rune('1'+i)) + "轮"}); err != nil {
			t.Fatal(err)
		}
		// 消费到 prompt_response
		for {
			m, _, err := c.Recv()
			if err != nil {
				t.Fatal(err)
			}
			if m == MethodPromptResponse {
				break
			}
		}
	}
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if len(sp.agents) != 2 {
		t.Fatalf("agent 数 = %d", len(sp.agents))
	}
	// 第二个 agent 的历史包含第一轮 user+assistant
	hist2 := sp.agents[1].ConversationHistory()
	if len(hist2) != 2 {
		t.Fatalf("第二轮历史 = %d 条, want 2", len(hist2))
	}
	if hist2[0].Role != "user" || hist2[1].Role != "assistant" {
		t.Fatalf("历史角色错误: %+v", hist2)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.histories[sid]) != 4 {
		t.Fatalf("会话历史 = %d 条, want 4", len(h.histories[sid]))
	}
}

// TestHandler_Capabilities：工具定义与上下文窗口声明。
func TestHandler_Capabilities(t *testing.T) {
	addr, _, _ := startHandlerServer(t, &fakeBackend{tokens: []string{"x"}}, "quiet")
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	m, p, err := c.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if m != MethodServerHello {
		t.Fatalf("首条消息 = %s", m)
	}
	var hello HelloParams
	json.Unmarshal(p, &hello)
	if hello.Version != "1.0" || hello.Capabilities.ContextWindow != 131072 {
		t.Fatalf("hello 异常: %+v", hello)
	}
	if len(hello.Capabilities.Tools) == 0 {
		t.Fatal("capabilities 无工具")
	}
	// 多 Agent 工具被过滤
	for _, td := range hello.Capabilities.Tools {
		if td.Name == "spawn_task" || td.Name == "handoff" {
			t.Fatalf("不应暴露多 Agent 工具: %s", td.Name)
		}
	}
}

// TestHandler_PermissionBridge：标准策略下敏感工具推送 request_permission。
func TestHandler_PermissionBridge(t *testing.T) {
	addr, h, _ := startHandlerServer(t, &fakeBackend{tokens: []string{"done"}}, "standard")
	// 安装桥接（本测试不经过 NewRuntimeHandler 的 installConfirm 参数，手动验证桥接逻辑）
	_ = addr
	_ = h
	// 直接用 mockHandler 验证协议层权限流程已在 TestPermissionFlow 覆盖；
	// 这里验证 RuntimeHandler.OnPermissionResponse 投递通道
	ch := make(chan bool, 1)
	h.mu.Lock()
	h.permChans["s1/p1"] = ch
	h.mu.Unlock()
	if err := h.OnPermissionResponse("s1", "p1", true); err != nil {
		t.Fatal(err)
	}
	select {
	case allow := <-ch:
		if !allow {
			t.Fatal("应允许")
		}
	case <-time.After(time.Second):
		t.Fatal("未收到投递")
	}
}

// TestHandler_CompleteSummary：OnComplete 返回最后 assistant 文本摘要。
func TestHandler_CompleteSummary(t *testing.T) {
	addr, h, _ := startHandlerServer(t, &fakeBackend{tokens: []string{"这是一段很长的回答内容，用于验证摘要"}}, "quiet")
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sid := newSession(t, c)
	if _, err := c.Request(MethodSessionPrompt, SessionPromptParams{SessionID: sid, Prompt: "q"}); err != nil {
		t.Fatal(err)
	}
	// 消费到 prompt_response
	for {
		m, _, err := c.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if m == MethodPromptResponse {
			break
		}
	}
	summary, err := h.OnComplete(sid)
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" {
		t.Fatal("摘要为空")
	}
}

// TestHandler_CloseCleansHistory：OnClose 清理会话。
func TestHandler_CloseCleansHistory(t *testing.T) {
	addr, h, _ := startHandlerServer(t, &fakeBackend{tokens: []string{"x"}}, "quiet")
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	sid := newSession(t, c)
	if err := h.OnClose(sid); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.histories[sid]; ok {
		t.Fatal("历史未清理")
	}
}
