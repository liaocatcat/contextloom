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

// 用户 15:35 完整场景：前置正文 + 两个 invoke 的 XML 块，逐 token 流式
func TestStreamEx_EndToEnd_PrefixPlusXML(t *testing.T) {
	prefix := "我先查看项目基本结构与版本库状态，以获取整体进展的依据。"
	xmlBody := `<tool_calls>
<invoke name="exec_command">
<parameter name="cmd" string="true">cd /path/to/contextloom && pwd && git log --oneline -20 2>/dev/null; echo '---STATUS---'; git status --short 2>/dev/null | head -50</parameter>
</invoke>
<invoke name="exec_command">
<parameter name="cmd" string="true">cd /path/to/contextloom && ls -la && echo '---' && find . -maxdepth 2 -type d -not -path '*/node_modules/*' -not -path '*/.git/*' | head -60</parameter>
</invoke>
</tool_calls>`
	full := prefix + "\n\n" + xmlBody

	// 逐 token（1-4 字符）模拟流式分片
	var tokens []string
	runes := []rune(full)
	for i := 0; i < len(runes); {
		n := 1 + (i*7)%4
		if i+n > len(runes) {
			n = len(runes) - i
		}
		tokens = append(tokens, string(runes[i:i+n]))
		i += n
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, tk := range tokens {
			evt, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": tk}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", evt)
			flusher.Flush()
		}
		evt, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
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
		t.Logf("call%d name=%s", i, c.Function.Name)
	}

	if len(calls) != 2 {
		t.Fatalf("期望 2 个调用，实际 %d（正文=%q）", len(calls), content.String())
	}
	// 前置正文必须保留
	if !strings.Contains(content.String(), "我先查看项目基本结构") {
		t.Errorf("前置正文丢失: %q", content.String())
	}
	// 工具调用壳不得残留
	lc := content.String()
	if strings.Contains(lc, "<invoke") || strings.Contains(lc, "<tool_calls") || strings.Contains(lc, "`</") {
		t.Errorf("正文残留工具调用壳: %q", lc)
	}
}
