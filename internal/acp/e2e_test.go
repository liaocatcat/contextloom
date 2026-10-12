package acp

// e2e 冒烟：协议层完整会话链路快速回归。
// （真实 runtime 全链路已由 TestHandler_* 系列覆盖：handler → executor → mock 模型）

import (
	"encoding/json"
	"testing"
	"time"
)

// TestE2E_ProtocolChain 验证协议层完整链路（hello → new → prompt 流 → complete → close）。
func TestE2E_ProtocolChain(t *testing.T) {
	addr, _ := startTestServer(t, &mockHandler{})
	c, err := Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// hello
	m, p, err := c.Recv()
	if err != nil || m != MethodServerHello {
		t.Fatalf("hello: %s %v", m, err)
	}
	var hello HelloParams
	json.Unmarshal(p, &hello)
	if hello.Version != "1.0" {
		t.Fatalf("version = %s", hello.Version)
	}

	// session/new → init → init_response
	result, err := c.Request(MethodSessionNew, SessionNewParams{})
	if err != nil {
		t.Fatal(err)
	}
	var rp ResponseParams
	json.Unmarshal(result, &rp)
	sid := rp.SessionID
	recvUntil(t, c, MethodSessionInit)
	recvUntil(t, c, MethodInitResponse)

	// prompt → 2 条 agent_message → prompt_response
	if _, err := c.Request(MethodSessionPrompt, SessionPromptParams{SessionID: sid, Prompt: "hi"}); err != nil {
		t.Fatal(err)
	}
	got := 0
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && got < 2 {
		m, _, err := c.Recv()
		if err != nil {
			break
		}
		if m == MethodAgentMessage {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("agent_message = %d, want 2", got)
	}
	recvUntil(t, c, MethodPromptResponse)

	// complete → summary + complete_response
	if _, err := c.Request(MethodSessionComplete, SessionCompleteParams{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
	recvUntil(t, c, MethodCompleteResponse)

	// close
	if _, err := c.Request(MethodSessionClose, SessionCloseParams{SessionID: sid}); err != nil {
		t.Fatal(err)
	}
}
