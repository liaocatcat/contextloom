package model

import (
	"testing"
)

// 用户 16:07 真实输出：两个 list_files，第二个被截断（缺 </parameter>/</invoke>/</tool_calls>）
// 与 16:01 案例差异：带 string="true" 属性 + 多调用部分截断
func Test_Real1607_Repro(t *testing.T) {
	content := `<tool_calls>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom/.contextloom/memory`

	// 1) 原始解析
	calls := ExtractToolCallsFromText(content)
	t.Logf("解析到 %d 个调用", len(calls))
	for i, c := range calls {
		t.Logf("call%d name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	if len(calls) != 2 {
		t.Fatalf("期望 2 个调用，实际 %d", len(calls))
	}
	if calls[1].Function.Name != "list_files" {
		t.Fatalf("call1 期望 list_files，实际 %s", calls[1].Function.Name)
	}
	if calls[1].Function.Arguments != `{"path":"/path/to/contextloom/.contextloom/memory"}` {
		t.Fatalf("call1 参数解析异常: %s", calls[1].Function.Arguments)
	}

	// 2) hasUnclosedToolCall 判定
	if !hasUnclosedToolCall(content) {
		t.Fatalf("期望 hasUnclosedToolCall=true")
	}

	// 3) repair 结果
	repaired := repairTruncatedToolCall(content)
	t.Logf("repair 后:\n%s", repaired)
}
