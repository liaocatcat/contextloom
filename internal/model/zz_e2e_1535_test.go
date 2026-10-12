package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 端到端：模拟 API 逐 token 输出用户 15:35 的 XML 格式（带 string="true" 属性、
// 多 invoke、正文前置），验证 StreamEx 全链路能解析出 ToolCalls 且不吞正文。
func TestStreamEx_EndToEnd_Real1535XML(t *testing.T) {
	xmlBody := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd" string="true">cd /path/to/contextloom && pwd && git log --oneline -20 2>/dev/null; echo '---STATUS---'; git status --short 2>/dev/null | head -50</parameter>
</invoke>
<invoke name="exec_command">
<parameter name="cmd" string="true">cd /path/to/contextloom && ls -la && echo '---' && find . -maxdepth 2 -type d -not -path '*/node_modules/*' -not -path '*/.git/*' | head -60</parameter>
</invoke>
</tool_calls>`

	// 逐 token 模拟 SSE（每个 token 1-3 字符，模拟真实流式分片）
	var tokens []string
	runes := []rune(xmlBody)
	for i := 0; i < len(runes); {
		n := 1 + i%3
		if i+n > len(runes) {
			n = len(runes) - i
		}
		tokens = append(tokens, string(runes[i:i+n]))
		i += n
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter 不支持 Flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, tk := range tokens {
			evt, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{"content": tk},
				}},
			})
			fmt.Fprintf(w, "data: %s\n\n", evt)
			flusher.Flush()
		}
		// 结束标记
		evt, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{
				"delta":         map[string]any{},
				"finish_reason": "stop",
			}},
		})
		fmt.Fprintf(w, "data: %s\n\n", evt)
		flusher.Flush()
	}))
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, errCh := ds.StreamEx(ctx, []types.Message{{Role: "user", Content: "分析项目整体进展"}})

	var content strings.Builder
	var calls []types.ToolCall
	for chunk := range ch {
		if chunk.IsToolCall && len(chunk.ToolCalls) > 0 {
			calls = append(calls, chunk.ToolCalls...)
		}
		content.WriteString(chunk.Content)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream 错误: %v", e)
	}

	t.Logf("正文输出: %q", content.String())
	t.Logf("工具调用数: %d", len(calls))
	for i, c := range calls {
		t.Logf("call%d name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}

	if len(calls) != 2 {
		t.Fatalf("期望解析出 2 个调用，实际 %d（正文=%q）", len(calls), content.String())
	}
	if calls[0].Function.Name != "exec_command" {
		t.Errorf("call0 名称错误: %s", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, "git log") {
		t.Errorf("call0 参数缺失: %s", calls[0].Function.Arguments)
	}
	// 工具调用壳不应出现在正文中
	lc := content.String()
	if strings.Contains(lc, "<invoke") || strings.Contains(lc, "<tool_calls") || strings.Contains(lc, "</tool_calls>") {
		t.Errorf("正文中残留工具调用壳: %q", lc)
	}
}
