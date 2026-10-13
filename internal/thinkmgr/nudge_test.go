package thinkmgr

import (
	"strings"
	"testing"
)

// TestVerifyTemplate_FourElements 默认模板含 CoVe 四要素且 %d 格式化正确（§6.1 #6）。
func TestVerifyTemplate_FourElements(t *testing.T) {
	if n := strings.Count(DefaultVerifyTemplate, "%d"); n != 1 {
		t.Errorf("模板 %%d 占位符数 = %d, want 1", n)
	}
	got := BuildVerifyNudge(2, "")
	for _, want := range []string{
		"【验证轮 · 第 2 轮】", "1)", "2)", "3)", "4)",
		"关键假设", "验证问题", "先修正", "最终答案",
		"[THINK_NEEDS_FIX]", "[THINK_VERIFIED]",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("默认验证模板缺少 %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "%") {
		t.Errorf("格式化后残留 %%：%s", got)
	}
}

// TestBuildVerifyNudge_CustomAndWhitespace 自定义模板与空白回退。
func TestBuildVerifyNudge_CustomAndWhitespace(t *testing.T) {
	if got := BuildVerifyNudge(3, "自定义轮次 %d"); got != "自定义轮次 3" {
		t.Errorf("自定义模板 = %q, want %q", got, "自定义轮次 3")
	}
	if got := BuildVerifyNudge(4, "   "); !strings.Contains(got, "第 4 轮") {
		t.Errorf("空白模板应回退内置：%q", got)
	}
}

// TestContinueTemplates_Rotation 续思模板数量/前缀/轮换索引。
func TestContinueTemplates_Rotation(t *testing.T) {
	if len(ContinueTemplates) != 2 {
		t.Fatalf("续思模板数 = %d, want 2", len(ContinueTemplates))
	}
	for i, tpl := range ContinueTemplates {
		if !strings.HasPrefix(tpl, "【续思提示】") {
			t.Errorf("模板 %d 缺少【续思提示】前缀：%q", i, tpl)
		}
	}
	if got := ContinueTemplates[3%len(ContinueTemplates)]; got != ContinueTemplates[1] {
		t.Errorf("轮换索引错误：%q", got)
	}
}

// TestMarkConstants 判定标记常量字面值（与模型输出侧约定一致）。
func TestMarkConstants(t *testing.T) {
	if MarkVerified != "[THINK_VERIFIED]" {
		t.Errorf("MarkVerified = %q", MarkVerified)
	}
	if MarkNeedsFix != "[THINK_NEEDS_FIX]" {
		t.Errorf("MarkNeedsFix = %q", MarkNeedsFix)
	}
}
