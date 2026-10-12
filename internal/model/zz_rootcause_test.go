package model

import (
	"strings"
	"testing"
)

// 用户 16:14 第二轮真实输出（第二个 invoke 截断在 </parameter> 前）
const real1615 = `我来继续查看项目中存储的历史对话记录。先浏览 memory 目录内容。

<tool_calls>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom/.contextloom/memory</parameter>
<parameter name="recursive" string="false">true</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom
`

func TestRootCause_Real1615_Parse(t *testing.T) {
	calls := ExtractToolCallsFromText(real1615)
	t.Logf("解析出 %d 个调用", len(calls))
	for i, c := range calls {
		t.Logf("  [%d] name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	if len(calls) < 2 {
		t.Fatalf("期望 ≥2 个调用，实际 %d", len(calls))
	}
}

func TestRootCause_Real1615_Strip(t *testing.T) {
	cleaned := stripTextToolBlocks(real1615)
	t.Logf("剥离后剩余正文: %q", cleaned)
	// 正文应保留、XML 调用块应被剔除
	if strings.Contains(cleaned, "<invoke") || strings.Contains(cleaned, "<tool_calls") || strings.Contains(cleaned, "<parameter") {
		t.Fatalf("XML 调用块未剔除干净: %q", cleaned)
	}
}

// 用户 16:14 第一轮：只有正文、无工具调用（截断在工具调用之前）
func TestRootCause_Real1614_NoToolCall(t *testing.T) {
	body := "我来查找项目中存储的历史对话记录。先查看"
	calls := ExtractToolCallsFromText(body)
	t.Logf("解析出 %d 个调用（期望 0）", len(calls))
	if len(calls) != 0 {
		t.Fatalf("期望 0 个调用，实际 %d", len(calls))
	}
}
