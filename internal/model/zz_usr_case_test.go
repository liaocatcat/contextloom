package model

import "testing"

func Test_XMLUserCase(t *testing.T) {
	content := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd">git log --oneline -10 && echo "---STATUS---" && git status --short</parameter>
<parameter name="justification">查看最近提交与工作区状态，了解项目进展基线</parameter>
<parameter name="desc">git历史与状态</parameter>
<parameter name="timeout">30000</parameter>
</invoke>
</tool_calls>`
	calls := ExtractToolCallsFromText(content)
	t.Logf("解析到 %d 个调用", len(calls))
	for i, c := range calls {
		t.Logf("call%d name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	if len(calls) == 0 {
		t.Error("未解析出工具调用！")
	}
}
