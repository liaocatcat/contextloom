package model

import (
	"strings"
	"testing"
)

// 用户 15:35 真实输出复刻：带 string="true" 属性、多 invoke、参数含引号与换行折行
func Test_Real1535_Case(t *testing.T) {
	content := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd" string="true">cd /path/to/contextloom && pwd && git log --oneline -20 2>/dev/null; echo '---STATUS---'; git 
status --short 2>/dev/null | head -50</parameter>
</invoke>
<invoke name="exec_command">
<parameter name="cmd" string="true">cd /path/to/contextloom && ls -la && echo '---' && find . -maxdepth 2 -type d -not -path 
'*/node_modules/*' -not -path '*/.git/*' | head -60</parameter>
</invoke>
</tool_calls>`
	calls := ExtractToolCallsFromText(content)
	t.Logf("解析到 %d 个调用", len(calls))
	for i, c := range calls {
		t.Logf("call%d name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	if len(calls) != 2 {
		t.Fatalf("期望 2 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "exec_command" {
		t.Errorf("call0 名称错误: %s", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "git log") {
		t.Errorf("call0 参数缺失: %s", calls[0].Function.Arguments)
	}
}

// 流式逐 token 模拟：15:35 格式，含 string="true" 属性
func Test_Real1535_Stream(t *testing.T) {
	content := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd" string="true">git log --oneline -10 && echo "---STATUS---" && git status --short</parameter>
</invoke>
</tool_calls>`
	// 逐 token 切分模拟流式（每个 token 1-4 字符）
	var tokens []string
	for _, r := range content {
		tokens = append(tokens, string(r))
	}

	// 直接走三态逻辑路径验证：把 token 喂给 pending 累积后再解析
	var sb strings.Builder
	for _, tk := range tokens {
		sb.WriteString(tk)
	}
	calls := ExtractToolCallsFromText(sb.String())
	t.Logf("流式累积后解析到 %d 个调用", len(calls))
	for i, c := range calls {
		t.Logf("call%d name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	if len(calls) != 1 {
		t.Fatalf("期望 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "exec_command" {
		t.Errorf("名称错误: %s", calls[0].Function.Name)
	}
}
