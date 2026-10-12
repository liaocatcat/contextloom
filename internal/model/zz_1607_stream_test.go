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

// sse1607Server 逐 token 输出 16:07 真实内容（第二个 list_files 被截断），
// 支持两种结束方式：finish_reason=stop / 直接断流
func sse1607Server(t *testing.T, content string, withFinish bool, chunkSize int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter 不支持 Flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		// 按 chunkSize 切分模拟逐 token 流
		runes := []rune(content)
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			delta := string(runes[i:end])
			evt := map[string]any{
				"choices": []any{map[string]any{
					"delta": map[string]any{"content": delta},
					"index": 0,
				}},
			}
			if withFinish && end == len(runes) {
				evt["choices"] = []any{map[string]any{
					"delta":         map[string]any{},
					"finish_reason": "stop",
					"index":         0,
				}}
			}
			b, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}))
}

// 16:07 真实内容：两个 list_files，第二个截断
const content1607 = `<tool_calls>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom/.contextloom/memory`

// 端到端：逐 token 流式输出 16:07 截断内容 → StreamEx 应产出 2 个 ToolCall chunk
func TestStreamEx_Real1607_Truncated(t *testing.T) {
	for _, withFinish := range []bool{true, false} {
		for _, chunkSize := range []int{1, 3, 8} {
			ts := sse1607Server(t, content1607, withFinish, chunkSize)
			ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ch, errCh := ds.StreamEx(ctx, []types.Message{{Role: "user", Content: "列出历史对话"}})

			var calls []types.ToolCall
			var content strings.Builder
			for chunk := range ch {
				if chunk.IsToolCall {
					calls = append(calls, chunk.ToolCalls...)
				}
				content.WriteString(chunk.Content)
			}
			if e := <-errCh; e != nil {
				t.Fatalf("finish=%v chunk=%d stream error: %v", withFinish, chunkSize, e)
			}
			ts.Close()

			t.Logf("finish=%v chunk=%d: calls=%d content=%q", withFinish, chunkSize, len(calls), content.String())
			if len(calls) != 2 {
				t.Fatalf("finish=%v chunk=%d: 期望 2 个 ToolCall，实际 %d", withFinish, chunkSize, len(calls))
			}
			if calls[0].Function.Name != "list_files" || calls[1].Function.Name != "list_files" {
				t.Fatalf("finish=%v chunk=%d: 调用名错误: %s, %s", withFinish, chunkSize, calls[0].Function.Name, calls[1].Function.Name)
			}
			// XML 调用块不应作为正文泄漏
			if strings.Contains(content.String(), "<invoke") || strings.Contains(content.String(), "<tool_calls") {
				t.Fatalf("finish=%v chunk=%d: XML 调用块泄漏到正文: %q", withFinish, chunkSize, content.String())
			}
			ts.Close()
		}
	}
}
