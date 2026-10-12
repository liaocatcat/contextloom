package model

import (
	"testing"
)

// 用户 16:01 真实场景：模型输出工具调用块被截断（缺 </parameter>/</invoke>/</tool_calls>）
// 旧行为：解析 0 个调用 → agent 误判"模型已回答"直接收尾 → 对话中止、工具不执行。
// 修复后：repairTruncatedToolCall 补全闭合标签 → 解析出调用。
func TestExtractTruncatedXMLToolCall_Real1601(t *testing.T) {
	// 与用户 16:01 贴出的输出完全一致（被截断，无闭合标签）
	content := `<tool_calls>
<invoke name="list_files">
<parameter name="dir">/path/to/contextloom`

	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "list_files" {
		t.Fatalf("期望 list_files，实际 %s", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != `{"dir":"/path/to/contextloom"}` {
		t.Fatalf("参数解析异常: %s", calls[0].Function.Arguments)
	}
}

// 截断发生在 </invoke> 与 </tool_calls> 之间：invoke 已闭合、tool_calls 未闭合
func TestExtractTruncatedXMLToolCall_CloseInvokeOnly(t *testing.T) {
	content := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd">cd /tmp && ls</parameter>
<parameter name="justification">查看目录</parameter>
</invoke>`

	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "exec_command" {
		t.Fatalf("期望 exec_command，实际 %s", calls[0].Function.Name)
	}
}

// DSML 竖线形态截断同样可修复
func TestExtractTruncatedDSMLToolCall(t *testing.T) {
	content := `<|tool_calls|>
<|invoke name="list_files"|>
<|parameter name="dir">/tmp`

	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "list_files" {
		t.Fatalf("期望 list_files，实际 %s", calls[0].Function.Name)
	}
}

// 完整闭合的调用块不受影响（回归：不能被 repair 破坏）
func TestExtractCompleteToolCall_Unaffected(t *testing.T) {
	content := `<tool_calls>
<invoke name="list_files">
<parameter name="dir">/tmp</parameter>
</invoke>
</tool_calls>`

	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Arguments != `{"dir":"/tmp"}` {
		t.Fatalf("参数解析异常: %s", calls[0].Function.Arguments)
	}
}

// 多个调用且其中一个截断：全部应解析出来
func TestExtractTruncated_MultipleCalls(t *testing.T) {
	content := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd">git log --oneline -10</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path">/tmp`

	calls := ExtractToolCallsFromText(content)
	if len(calls) != 2 {
		t.Fatalf("期望解析出 2 个调用，实际 %d: %+v", len(calls), calls)
	}
	if calls[0].Function.Name != "exec_command" || calls[1].Function.Name != "list_files" {
		t.Fatalf("工具名解析异常: %s, %s", calls[0].Function.Name, calls[1].Function.Name)
	}
}

// 无工具调用标记的普通正文：repair 不得改动、解析仍为 0
func TestRepairTruncated_PlainText(t *testing.T) {
	content := "这是一个普通的回答，没有任何工具调用标记"
	if got := repairTruncatedToolCall(content); got != content {
		t.Fatalf("普通正文不应被修改: %s", got)
	}
	if calls := ExtractToolCallsFromText(content); len(calls) != 0 {
		t.Fatalf("普通正文不应解析出调用: %+v", calls)
	}
}
