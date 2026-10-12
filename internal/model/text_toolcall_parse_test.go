package model

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 14:07 实际输出：DSML 竖线风格（带 DSML| 前缀）
const dsmlSample = `<|DSML|tool_calls>
<|DSML|invoke name="list_files">
<|DSML|parameter name="dir">.</|DSML|parameter>
</|DSML|invoke>
</|DSML|tool_calls>`

// 14:28 实际输出：纯 XML 风格
const xmlSample = `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd">git log --oneline -10 && echo "---STATUS---" && git status --short</parameter>
<parameter name="justification">查看最近提交与工作区状态，了解项目进展基线</parameter>
<parameter name="desc">git历史与状态</parameter>
<parameter name="timeout">30000</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path">/path/to/contextloom</parameter>
<parameter name="depth">2</parameter>
</invoke>
</tool_calls>`

func TestTextToolCallParse_DSMLWithPrefix(t *testing.T) {
	calls := ExtractToolCallsFromText(dsmlSample)
	t.Logf("DSML calls=%d", len(calls))
	for _, c := range calls {
		t.Logf("  name=%s args=%s", c.Function.Name, c.Function.Arguments)
	}
}

func TestTextToolCallParse_PureXML(t *testing.T) {
	calls := ExtractToolCallsFromText(xmlSample)
	t.Logf("XML calls=%d", len(calls))
	for _, c := range calls {
		t.Logf("  name=%s args=%s", c.Function.Name, c.Function.Arguments)
	}
}

// 流式路径：DSML 带前缀格式，模拟逐 token 分片
func TestTextToolCallStream_DSMLWithPrefix(t *testing.T) {
	events := []string{
		`{"choices":[{"delta":{"content":"<|DSML|tool_calls>\n"}}]}`,
		`{"choices":[{"delta":{"content":"<|DSML|invoke name=\"list_files\"|>\n"}}]}`,
		`{"choices":[{"delta":{"content":"<|DSML|parameter name=\"dir\"|>."}}]}`,
		`{"choices":[{"delta":{"content":"</|DSML|parameter>\n"}}]}`,
		`{"choices":[{"delta":{"content":"</|DSML|invoke>\n"}}]}`,
		`{"choices":[{"delta":{"content":"</|DSML|tool_calls>\n"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := newSSEServer(t, events)
	defer ts.Close()
	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh := ds.StreamEx(ctx, []types.Message{{Role: "user", Content: "hi"}})
	var calls []types.ToolCall
	var text strings.Builder
	for c := range ch {
		if c.IsToolCall {
			calls = append(calls, c.ToolCalls...)
		}
		text.WriteString(c.Content)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream err: %v", e)
	}
	t.Logf("stream text=%q", text.String())
	t.Logf("stream calls=%d", len(calls))
	for _, c := range calls {
		t.Logf("  name=%s args=%s", c.Function.Name, c.Function.Arguments)
	}
	if len(calls) != 1 || calls[0].Function.Name != "list_files" {
		t.Fatalf("期望解析出 list_files 调用，实际 %d 个", len(calls))
	}
}

// 流式路径：纯 XML 格式，模拟逐 token 分片
func TestTextToolCallStream_PureXML(t *testing.T) {
	events := []string{
		`{"choices":[{"delta":{"content":"<tool_calls>\n"}}]}`,
		`{"choices":[{"delta":{"content":"<invoke name=\"exec_command\">\n"}}]}`,
		`{"choices":[{"delta":{"content":"<parameter name=\"cmd\">git log --oneline -10</parameter>\n"}}]}`,
		`{"choices":[{"delta":{"content":"</invoke>\n"}}]}`,
		`{"choices":[{"delta":{"content":"</tool_calls>\n"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := newSSEServer(t, events)
	defer ts.Close()
	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh := ds.StreamEx(ctx, []types.Message{{Role: "user", Content: "hi"}})
	var calls []types.ToolCall
	var text strings.Builder
	for c := range ch {
		if c.IsToolCall {
			calls = append(calls, c.ToolCalls...)
		}
		text.WriteString(c.Content)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream err: %v", e)
	}
	t.Logf("stream text=%q", text.String())
	t.Logf("stream calls=%d", len(calls))
	for _, c := range calls {
		t.Logf("  name=%s args=%s", c.Function.Name, c.Function.Arguments)
	}
	if len(calls) != 1 || calls[0].Function.Name != "exec_command" {
		t.Fatalf("期望解析出 exec_command 调用，实际 %d 个", len(calls))
	}
}
