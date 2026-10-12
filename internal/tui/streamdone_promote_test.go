package tui

import (
	"strings"
	"testing"
)

// TestStreamDone_PromotesThinkBody 端到端回归：整轮流输出只有思维链没有正文
// （R 系列模型把结论全部写进 reasoning_content、content 为空的上游行为）时，
// streamDoneMsg 收尾必须把思考尾段提升为可见正文消息——此前正文区一条消息
// 都不追加，回答只存在于折叠思考块中（用户症状"正文为空，回答出现在思考内"）。
func TestStreamDone_PromotesThinkBody(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.width = 100
	m.height = 40
	m.streaming = true
	m.streamBuf = "<|reasoning|>先分析问题。逐步推演……最终结论：把 enableX 设为 true 即可修复。<|/reasoning|>"

	m.streaming = false
	updated, _ := m.Update(streamDoneMsg{})

	tm := updated.(interface{ View() string })
	_ = tm

	// 收尾后应有：1 个折叠思考块 + 1 条提升的正文消息
	thinks, bodies := 0, 0
	for _, msg := range m.messages {
		switch msg.Type {
		case MsgThinking:
			thinks++
		case MsgAssistant:
			bodies++
			if !strings.Contains(msg.Content, "把 enableX 设为 true 即可修复") {
				t.Errorf("提升的正文应取思考结论尾段, got %q", msg.Content)
			}
			if !strings.Contains(msg.Content, "回答取自思考") {
				t.Errorf("正文应含来源标记, got %q", msg.Content)
			}
		}
	}
	if thinks != 1 {
		t.Errorf("应保留 1 个思考块, got %d", thinks)
	}
	if bodies != 1 {
		t.Errorf("应有 1 条提升的正文消息（此前为 0 条——正文区空无一物）, got %d", bodies)
	}
}

// TestStreamDone_BodyNotPromoted 有正文时不得重复追加提升消息。
func TestStreamDone_BodyNotPromoted(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.width = 100
	m.height = 40
	m.streaming = true
	m.streamBuf = "<|reasoning|>思考<|/reasoning|>这是正常正文。"

	m.streaming = false
	if _, _ = m.Update(streamDoneMsg{}); true {
	}
	bodies := 0
	for _, msg := range m.messages {
		if msg.Type == MsgAssistant {
			bodies++
			if strings.Contains(msg.Content, "回答取自思考") {
				t.Errorf("有正文时不应出现提升标记, got %q", msg.Content)
			}
			if msg.Content != "这是正常正文。" {
				t.Errorf("正文应原样保留, got %q", msg.Content)
			}
		}
	}
	if bodies != 1 {
		t.Errorf("应只有 1 条正文, got %d", bodies)
	}
}
