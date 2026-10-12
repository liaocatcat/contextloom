package gui

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/runtime"
)

// TestPromoteIfThinkOnly 验证 GUI 轮收尾的思考兜底：整轮只有思维链没有正文
// 时保留思维链（折叠可回看）并追加提升的可见正文；正常回复原样返回。
func TestPromoteIfThinkOnly(t *testing.T) {
	// 1. 只有思考 → 保留原始思维链 + 追加提升正文
	raw := "<|reasoning|>推理过程……结论：答案是 42<|/reasoning|>"
	got := promoteIfThinkOnly(raw)
	if !strings.HasPrefix(got, raw) {
		t.Errorf("应保留原始思维链前缀")
	}
	if !strings.Contains(got, "回答取自思考") {
		t.Errorf("应追加来源标记正文, got %q", got)
	}
	if !strings.Contains(got, "答案是 42") {
		t.Errorf("提升正文应含结论, got %q", got)
	}
	// 入史语义：SplitReasoning 应把提升段归入 body、标签段归入 reasoning
	body, reasoning := runtime.SplitReasoning(got)
	if !strings.Contains(body, "答案是 42") || strings.TrimSpace(reasoning) == "" {
		t.Errorf("入史分离应 body=提升正文 reasoning=思维链, got body=%q reasoning=%q", body, reasoning)
	}

	// 2. 有正文 → 原样返回
	normal := "<|reasoning|>思考<|/reasoning|>正常正文"
	if got := promoteIfThinkOnly(normal); got != normal {
		t.Errorf("正常回复应原样返回, got %q", got)
	}

	// 3. 无思考纯正文 → 原样返回
	plain := "纯文本回复"
	if got := promoteIfThinkOnly(plain); got != plain {
		t.Errorf("纯文本应原样返回, got %q", got)
	}
}
