package runtime

import (
	"strings"
	"testing"
)

// TestPromoteThinkBody 验证正文为空、回答滞留思考内的兜底提升。
// 用户症状：正文区空无一物，回答只出现在折叠思考块里（R 系列模型偶发把
// 结论全部写进 reasoning_content、content 为空的上游行为）。
func TestPromoteThinkBody(t *testing.T) {
	// 1. 基础提升：取最后一段非空思考 + 来源标记
	p, ok := PromoteThinkBody([]string{"先分析", "最终结论：答案是 42"})
	if !ok {
		t.Fatal("应成功提升")
	}
	if !strings.Contains(p, "回答取自思考") {
		t.Errorf("应含来源标记, got %q", p)
	}
	if !strings.Contains(p, "答案是 42") {
		t.Errorf("应取最后一段思考(结论段), got %q", p)
	}
	if strings.Contains(p, "先分析") {
		t.Errorf("不应包含更早的思考段, got %q", p)
	}

	// 2. 空思考/全空白 → 不提升
	if _, ok := PromoteThinkBody([]string{"  ", "\n"}); ok {
		t.Error("全空白思考不应提升")
	}
	if _, ok := PromoteThinkBody(nil); ok {
		t.Error("nil 思考不应提升")
	}

	// 3. 超长思考只取尾段（结论通常在末尾），并带省略标记
	long := strings.Repeat("铺", 3000) + "结论段"
	p, ok = PromoteThinkBody([]string{long})
	if !ok {
		t.Fatal("长思考应提升")
	}
	if !strings.Contains(p, "结论段") {
		t.Error("长思考应保留尾段结论")
	}
	if !strings.Contains(p, "前段略") {
		t.Error("截断应带省略标记")
	}
	if got := len([]rune(strings.TrimPrefix(p, "〔回答取自思考：模型未输出正文，以下为思考结论〕\n\n…（思考前段略，仅取结论尾段）"))); got > 1600 {
		t.Errorf("尾段应截到 1600 rune, got %d", got)
	}
}

// TestPromoteThinkBody_TailBoundary 验证恰好等于上限时不加省略标记。
func TestPromoteThinkBody_TailBoundary(t *testing.T) {
	exact := strings.Repeat("尾", 1600)
	p, ok := PromoteThinkBody([]string{exact})
	if !ok {
		t.Fatal("应成功提升")
	}
	if strings.Contains(p, "前段略") {
		t.Error("恰好 1600 rune 不应截断")
	}
	if !strings.Contains(p, "尾尾尾") {
		t.Error("内容应完整保留")
	}
}
