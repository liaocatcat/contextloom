package acp

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockHandler 内存实现 SessionHandler，记录调用并支持推送。
type mockHandler struct {
	mu          sync.Mutex
	cap         Capabilities
	newSessions []string
	prompts     []string
	completes   []string
	cancels     []string
	closes      []string
	updates     []string
	permResp    []string // "allow"/"deny"
	toolResp    []string
	agentResp   []bool
	server      *Server
}

func (m *mockHandler) GetCapabilities() Capabilities {
	if m.cap.Tools == nil {
		m.cap = Capabilities{
			Tools:         []ToolDefinition{{Name: "read_file", Description: "读文件", Parameters: json.RawMessage(`{"type":"object"}`)}},
			ContextWindow: 131072,
			Models:        []string{"default-model"},
			PromptCaching: true,
		}
	}
	return m.cap
}

func (m *mockHandler) OnNewSession(sessionID string, tools []ToolDefinition, model string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.newSessions = append(m.newSessions, sessionID)
	return nil
}

func (m *mockHandler) OnPrompt(sessionID, prompt string, tools []ToolDefinition, model string) error {
	m.mu.Lock()
	m.prompts = append(m.prompts, prompt)
	m.mu.Unlock()
	// 模拟流式推送：两条文本消息
	_ = m.server.Push(sessionID, MethodAgentMessage, AgentMessageParams{
		SessionID: sessionID,
		Message:   json.RawMessage(`[{"type":"text","text":"hello "}]`),
	})
	_ = m.server.Push(sessionID, MethodAgentMessage, AgentMessageParams{
		SessionID: sessionID,
		Message:   json.RawMessage(`[{"type":"text","text":"world"}]`),
	})
	// 流结束后推送 prompt_response（handler 契约：执行完成后推送）
	_ = m.server.Push(sessionID, MethodPromptResponse, ResponseParams{Success: true, SessionID: sessionID})
	return nil
}

func (m *mockHandler) OnComplete(sessionID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.completes = append(m.completes, sessionID)
	return "summary-ok", nil
}

func (m *mockHandler) OnCancel(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancels = append(m.cancels, sessionID)
	return nil
}

func (m *mockHandler) OnClose(sessionID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closes = append(m.closes, sessionID)
	return nil
}

func (m *mockHandler) OnUpdate(sessionID string, tools []ToolDefinition) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updates = append(m.updates, sessionID)
	return nil
}

func (m *mockHandler) OnPermissionResponse(sessionID, reqID string, allow bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if allow {
		m.permResp = append(m.permResp, "allow")
	} else {
		m.permResp = append(m.permResp, "deny")
	}
	return nil
}

func (m *mockHandler) OnToolCallResponse(sessionID, toolCallID string, content []ContentBlock) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolResp = append(m.toolResp, toolCallID+":"+content[0].Text)
	return nil
}

func (m *mockHandler) OnAgentMessageResponse(sessionID string, success bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agentResp = append(m.agentResp, success)
	return nil
}

// startTestServer 启动 ACP 测试服务器，返回 (地址, handler)。
func startTestServer(t *testing.T, h *mockHandler) (string, *mockHandler) {
	t.Helper()
	if h == nil {
		h = &mockHandler{}
	}
	srv := NewServer(0)
	h.server = srv
	srv.SetHandler(h)
	// 绑定随机端口
	ts := httptest.NewServer(nil)
	addr := ts.Listener.Addr().String()
	ts.Close()
	go func() { _ = srv.StartAddr(addr) }()
	t.Cleanup(srv.Shutdown)
	// 等待端口就绪（轮询 Dial，最多 2s）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c, err := Dial(addr)
		if err == nil {
			c.Close()
			return addr, h
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("ACP 服务器未就绪: %s", addr)
	return "", h
}

// recvUntil 读取直到 method 匹配（超时 3s），返回 params。
func recvUntil(t *testing.T, c *Client, method string) json.RawMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		m, p, err := c.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if m == method {
			return p
		}
	}
	t.Fatalf("3s 内未收到 %s", method)
	return nil
}

func TestHello_OnConnect(t *testing.T) {
	addr, _ := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer c.Close()
	m, p, err := c.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if m != MethodServerHello {
		t.Fatalf("首条消息应为 server/hello, got %s", m)
	}
	var hello HelloParams
	if err := json.Unmarshal(p, &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Version != "1.0" {
		t.Fatalf("version = %q, want 1.0", hello.Version)
	}
	if len(hello.Capabilities.Tools) == 0 || hello.Capabilities.ContextWindow <= 0 {
		t.Fatalf("capabilities 不完整: %+v", hello.Capabilities)
	}
}

func TestSessionNew_InitAndResponse(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// 跳过 hello
	_, _, _ = c.Recv()

	result, err := c.Request(MethodSessionNew, SessionNewParams{})
	if err != nil {
		t.Fatalf("session/new: %v", err)
	}
	var rp ResponseParams
	if err := json.Unmarshal(result, &rp); err != nil {
		t.Fatal(err)
	}
	if !rp.Success || rp.SessionID == "" {
		t.Fatalf("响应异常: %+v", rp)
	}
	// 推送：session/init → session/init_response
	var initP SessionInitParams
	params := recvUntil(t, c, MethodSessionInit)
	if err := json.Unmarshal(params, &initP); err != nil {
		t.Fatal(err)
	}
	if initP.SessionID != rp.SessionID || len(initP.Capabilities.Tools) == 0 {
		t.Fatalf("session/init 异常: %+v", initP)
	}
	recvUntil(t, c, MethodInitResponse)

	if len(h.newSessions) != 1 {
		t.Fatalf("OnNewSession 调用 %d 次", len(h.newSessions))
	}
}

func TestSessionPrompt_Stream(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv() // hello

	result, _ := c.Request(MethodSessionNew, SessionNewParams{})
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	sid := rp.SessionID
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)

	_, err = c.Request(MethodSessionPrompt, SessionPromptParams{SessionID: sid, Prompt: "帮我重构"})
	if err != nil {
		t.Fatalf("session/prompt: %v", err)
	}

	// 收到两条 agent_message 文本
	var got []string
	for len(got) < 2 {
		m, p, err := c.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if m == MethodAgentMessage {
			var amp AgentMessageParams
			json.Unmarshal(p, &amp)
			var blocks []ContentBlock
			json.Unmarshal(amp.Message, &blocks)
			for _, b := range blocks {
				got = append(got, b.Text)
			}
		}
	}
	if got[0] != "hello " || got[1] != "world" {
		t.Fatalf("流内容错误: %v", got)
	}
	recvUntil(t, c, MethodPromptResponse)

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.prompts) != 1 || h.prompts[0] != "帮我重构" {
		t.Fatalf("OnPrompt 未收到内容: %v", h.prompts)
	}
}

func TestPermissionFlow(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv()

	result, _ := c.Request(MethodSessionNew, SessionNewParams{})
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	sid := rp.SessionID
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)

	// handler 推送权限请求
	_ = h.server.Push(sid, MethodPermissionRequest, PermissionRequestParams{
		SessionID: sid,
		PermissionRequest: PermissionRequest{
			ID: "p1", Kind: PermUseTool, Message: "执行写文件?",
			Data: json.RawMessage(`{"tool_name":"write_file"}`),
		},
	})
	recvUntil(t, c, MethodPermissionRequest)

	// 客户端回复 allow
	if _, err := c.Request(MethodPermissionResponse, PermissionResponseParams{
		SessionID: sid, PermissionRequestID: "p1", Response: "allow",
	}); err != nil {
		t.Fatalf("permission_response: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.permResp) != 1 || h.permResp[0] != "allow" {
		t.Fatalf("OnPermissionResponse 异常: %v", h.permResp)
	}
}

func TestToolCallResponse_Routing(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv()

	result, _ := c.Request(MethodSessionNew, SessionNewParams{})
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	sid := rp.SessionID
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)

	if _, err := c.Request(MethodToolCallResponse, ToolCallResponseParams{
		SessionID: sid, ToolCallID: "tc1",
		Content: []ContentBlock{{Type: ContentText, Text: "done"}},
	}); err != nil {
		t.Fatalf("tool_call_response: %v", err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.toolResp) != 1 || h.toolResp[0] != "tc1:done" {
		t.Fatalf("OnToolCallResponse 异常: %v", h.toolResp)
	}
}

func TestUnknownMethod(t *testing.T) {
	addr, _ := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv()
	_, err = c.Request("no/such_method", map[string]any{})
	if err == nil {
		t.Fatal("未知方法应报错")
	}
	if err.Error() != "acp no/such_method: 未知方法: no/such_method (code -32601)" {
		t.Fatalf("错误信息异常: %v", err)
	}
}

func TestInvalidParams(t *testing.T) {
	addr, _ := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv()
	// session/prompt 缺 session_id → 参数无效
	_, err = c.Request(MethodSessionPrompt, map[string]any{"prompt": "x"})
	if err == nil {
		t.Fatal("缺 session_id 应报错")
	}
	if err.Error() != "acp session/prompt: session/prompt 参数无效 (code -32602)" {
		t.Fatalf("错误信息异常: %v", err)
	}
}

func TestPushOrder(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv()
	result, _ := c.Request(MethodSessionNew, SessionNewParams{})
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	sid := rp.SessionID
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)

	// 连续推送 10 条
	for i := 0; i < 10; i++ {
		_ = h.server.Push(sid, MethodAgentMessage, AgentMessageParams{
			SessionID: sid,
			Message:   json.RawMessage(fmt.Sprintf(`[{"type":"text","text":"m%d"}]`, i)),
		})
	}
	for i := 0; i < 10; i++ {
		_, p, err := c.Recv()
		if err != nil {
			t.Fatal(err)
		}
		var amp AgentMessageParams
		json.Unmarshal(p, &amp)
		var blocks []ContentBlock
		json.Unmarshal(amp.Message, &blocks)
		if blocks[0].Text != fmt.Sprintf("m%d", i) {
			t.Fatalf("顺序错乱: 第 %d 条 = %q", i, blocks[0].Text)
		}
	}
}

func TestSessionClose_RemovesSession(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv()
	result, _ := c.Request(MethodSessionNew, SessionNewParams{})
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	sid := rp.SessionID
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)

	if _, err := c.Request(MethodSessionClose, SessionCloseParams{SessionID: sid}); err != nil {
		t.Fatalf("session/close: %v", err)
	}
	// 关闭后再 prompt → 会话不存在
	_, err = c.Request(MethodSessionPrompt, SessionPromptParams{SessionID: sid, Prompt: "x"})
	if err == nil {
		t.Fatal("关闭后 prompt 应报错")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.closes) != 1 {
		t.Fatalf("OnClose 调用 %d 次", len(h.closes))
	}
}

func TestConcurrentSessions(t *testing.T) {
	addr, h := startTestServer(t, &mockHandler{})
	c1, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	_, _, _ = c1.Recv()
	_, _, _ = c2.Recv()

	// 两个客户端各建会话
	r1, _ := c1.Request(MethodSessionNew, SessionNewParams{})
	r2, _ := c2.Request(MethodSessionNew, SessionNewParams{})
	var p1, p2 ResponseParams
	json.Unmarshal(r1, &p1)
	json.Unmarshal(r2, &p2)
	if p1.SessionID == p2.SessionID {
		t.Fatal("会话 ID 应不同")
	}
	recvUntil(t, c1, MethodSessionInit)
	recvUntil(t, c1, MethodInitResponse)
	recvUntil(t, c2, MethodSessionInit)
	recvUntil(t, c2, MethodInitResponse)

	// 各自 prompt，互不干扰
	if _, err := c1.Request(MethodSessionPrompt, SessionPromptParams{SessionID: p1.SessionID, Prompt: "A"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c2.Request(MethodSessionPrompt, SessionPromptParams{SessionID: p2.SessionID, Prompt: "B"}); err != nil {
		t.Fatal(err)
	}
	// c1 收到两条自己的 agent_message
	got := 0
	for got < 2 {
		m, p, err := c1.Recv()
		if err != nil {
			t.Fatal(err)
		}
		if m == MethodAgentMessage {
			var amp AgentMessageParams
			json.Unmarshal(p, &amp)
			if amp.SessionID != p1.SessionID {
				t.Fatalf("串扰: c1 收到 session %s", amp.SessionID)
			}
			got++
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.prompts) != 2 {
		t.Fatalf("prompts = %v", h.prompts)
	}
}

func TestVersionConstant(t *testing.T) {
	if Version != "1.0" {
		t.Fatalf("Version = %q", Version)
	}
}

func TestContentBlockConstructors(t *testing.T) {
	tb := TextBlock("hi")
	if tb.Type != ContentText || tb.Text != "hi" {
		t.Fatal("TextBlock 异常")
	}
	rb := ReasoningBlock("think")
	if rb.Type != ContentReasoning || rb.Text != "think" {
		t.Fatal("ReasoningBlock 异常")
	}
	tr := ToolResultBlock("tc9", "result")
	if tr.Type != ContentToolResult || tr.ToolCallID != "tc9" || len(tr.Content) == 0 {
		t.Fatal("ToolResultBlock 异常")
	}
}

// TestPushToClosedSession：推送不存在的会话返回错误。
func TestPushToClosedSession(t *testing.T) {
	_, h := startTestServer(t, &mockHandler{})
	if err := h.server.Push("nope", MethodAgentMessage, AgentMessageParams{}); err == nil {
		t.Fatal("推送不存在会话应报错")
	}
}

// TestHandlerNil：未设置 handler 时请求返回内部错误。
func TestHandlerNil(t *testing.T) {
	srv := NewServer(0)
	var started atomic.Bool
	addr := ""
	// 用临时 listener
	ts := httptest.NewServer(nil)
	addr = ts.Listener.Addr().String()
	ts.Close()
	go func() { _ = srv.StartAddr(addr); started.Store(true) }()
	t.Cleanup(srv.Shutdown)
	time.Sleep(50 * time.Millisecond)

	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _, _ = c.Recv() // hello（capabilities 为空）
	_, err = c.Request(MethodSessionNew, SessionNewParams{})
	if err == nil {
		t.Fatal("无 handler 时应报错")
	}
}
