package model

import "testing"

// TestSetReasoningEffort_Remap 验证思考深度映射规则：
//
//	low → low（V4.1 起原生支持 low(50) 档，低档解锁）；medium/high → high；
//	xhigh/max → max；未知值原样透传。
//
// 回归背景：cli_defaults.effort 默认已改为 max，需确保 max 真正映射为 API 层 max。
func TestSetReasoningEffort_Remap(t *testing.T) {
	d := NewDeepSeekBackend("http://localhost:8080", "sk-test", "default-model", 131072)

	cases := []struct {
		input string
		want  string
	}{
		{"low", "low"},
		{"medium", "high"},
		{"high", "high"},
		{"xhigh", "max"},
		{"max", "max"},
		{"", ""},
		{"custom", "custom"},
	}
	for _, c := range cases {
		d.SetReasoningEffort(c.input)
		if got := d.ReasoningEffort(); got != c.want {
			t.Errorf("SetReasoningEffort(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}

// TestReasoningEffort_DefaultMax 验证未显式设置时（空值）不发送 reasoning_effort 字段；
// 默认 max 由 config.applyDefaults / runtime.New 兜底下发，此处保证空值不产生意外字段。
func TestReasoningEffort_DefaultEmpty(t *testing.T) {
	d := NewDeepSeekBackend("http://localhost:8080", "sk-test", "default-model", 131072)
	if got := d.ReasoningEffort(); got != "" {
		t.Errorf("expected empty reasoning effort by default, got %q", got)
	}
}
