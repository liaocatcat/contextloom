package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 穷举：16:15 真实文本 + reasoning 前缀，不同分片大小跑 StreamEx
// 找出会让 XML 泄漏或解析失败的切分方式
func TestRootCause_Exhaustive_ChunkSizes(t *testing.T) {
	content := `我来继续查看项目中存储的历史对话记录。先浏览 memory 目录内容。

<tool_calls>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom/.contextloom/memory</parameter>
<parameter name="recursive" string="false">true</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom
`
	reasoning := "用户要求列出项目所有历史对话记录。我需要先查看 memory 目录下的会话文件。然后调用 list_files 工具。"
	runes := []rune(content)

	failures := 0
	for chunkSize := 1; chunkSize <= 20; chunkSize++ {
		var chunks []string
		for i := 0; i < len(runes); i += chunkSize {
			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			chunks = append(chunks, string(runes[i:end]))
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fl, _ := w.(http.Flusher)
			// 先输出 reasoning_content
			for i := 0; i < len(reasoning); i += 5 {
				end := i + 5
				if end > len(reasoning) {
					end = len(reasoning)
				}
				chunk := map[string]any{
					"choices": []any{map[string]any{
						"delta": map[string]any{"reasoning_content": reasoning[i:end]},
					}},
				}
				b, _ := json.Marshal(chunk)
				w.Write([]byte("data: " + string(b) + "\n\n"))
				fl.Flush()
			}
			for _, c := range chunks {
				chunk := map[string]any{
					"choices": []any{map[string]any{
						"delta": map[string]any{"content": c},
					}},
				}
				b, _ := json.Marshal(chunk)
				w.Write([]byte("data: " + string(b) + "\n\n"))
				fl.Flush()
			}
			chunk := map[string]any{
				"choices": []any{map[string]any{
					"delta":         map[string]any{},
					"finish_reason": "length",
				}},
			}
			b, _ := json.Marshal(chunk)
			w.Write([]byte("data: " + string(b) + "\n\n"))
			w.Write([]byte("data: [DONE]\n\n"))
			fl.Flush()
		}))

		d := &DeepSeekBackend{
			client:   srv.Client(),
			endpoint: srv.URL,
			modelID:  "test",
			apiKey:   "test",
		}
		ch, errCh := d.StreamEx(context.Background(), nil)
		var calls []types.ToolCall
		var contents []string
		done := false
		for !done {
			select {
			case chunk, ok := <-ch:
				if !ok {
					done = true
					break
				}
				if chunk.IsToolCall {
					calls = append(calls, chunk.ToolCalls...)
				}
				if chunk.Content != "" {
					contents = append(contents, chunk.Content)
				}
			case e := <-errCh:
				if e != nil {
					t.Fatalf("chunkSize=%d stream error: %v", chunkSize, e)
				}
			}
		}
		if e := <-errCh; e != nil {
			t.Fatalf("chunkSize=%d errCh: %v", chunkSize, e)
		}
		srv.Close()

		joined := strings.Join(contents, "")
		leak := strings.Contains(joined, "<invoke") || strings.Contains(joined, "<tool_calls") || strings.Contains(joined, "<parameter")
		if len(calls) < 2 || leak {
			failures++
			t.Logf("chunkSize=%d: FAIL calls=%d leak=%v", chunkSize, len(calls), leak)
			t.Logf("  正文: %q", joined)
			if len(joined) > 200 {
				t.Logf("  正文前200: %q", joined[:200])
			}
		}
	}
	t.Logf("共 %d 个分片大小，%d 个失败", 20, failures)
	if failures > 0 {
		t.Fatalf("存在失败分片")
	}
}
