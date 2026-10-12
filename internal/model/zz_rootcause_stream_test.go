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

// 模拟 16:15 真实流式：正文 + 工具调用 + finish_reason=length（max_tokens 截断）
func TestRootCause_StreamEx_Real1615_Length(t *testing.T) {
	content := `我来继续查看项目中存储的历史对话记录。先浏览 memory 目录内容。

<tool_calls>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom/.contextloom/memory</parameter>
<parameter name="recursive" string="false">true</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom
`

	// 把内容切成小片模拟 SSE 流
	runes := []rune(content)
	var chunks []string
	for i := 0; i < len(runes); i += 3 {
		end := i + 3
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
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
		// 最后一条带 finish_reason=length（max_tokens 截断）
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
	defer srv.Close()

	d := &DeepSeekBackend{
		client:   srv.Client(),
		endpoint: srv.URL,
		modelID:  "test",
		apiKey:   "test",
	}

	ch, errCh := d.StreamEx(context.Background(), nil)
	var toolCalls []types.ToolCall
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
				toolCalls = append(toolCalls, chunk.ToolCalls...)
				t.Logf("TOOLCALL chunk: %d calls", len(chunk.ToolCalls))
			}
			if chunk.Content != "" {
				contents = append(contents, chunk.Content)
			}
		case e := <-errCh:
			if e != nil {
				t.Fatalf("stream error: %v", e)
			}
		}
	}
	// drain errCh
	if e := <-errCh; e != nil {
		t.Fatalf("errCh: %v", e)
	}

	t.Logf("toolCalls=%d", len(toolCalls))
	for i, c := range toolCalls {
		t.Logf("  [%d] %s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	joined := strings.Join(contents, "")
	t.Logf("正文输出: %q", joined)
	if len(toolCalls) == 0 {
		t.Fatalf("期望解析出工具调用，实际 0 个；正文=%q", joined)
	}
	if strings.Contains(joined, "<invoke") || strings.Contains(joined, "<tool_calls") {
		t.Fatalf("XML 泄漏到正文: %q", joined)
	}
}

// 模拟 16:14 第一轮：正文只有半句（截断在工具调用之前）+ finish_reason=length
func TestRootCause_StreamEx_Real1614_NoToolCall_Length(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		chunk := map[string]any{
			"choices": []any{map[string]any{
				"delta": map[string]any{"content": "我来查找项目中存储的历史对话记录。先查看"},
			}},
		}
		b, _ := json.Marshal(chunk)
		w.Write([]byte("data: " + string(b) + "\n\n"))
		chunk = map[string]any{
			"choices": []any{map[string]any{
				"delta":         map[string]any{},
				"finish_reason": "length",
			}},
		}
		b, _ = json.Marshal(chunk)
		w.Write([]byte("data: " + string(b) + "\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
		fl.Flush()
	}))
	defer srv.Close()

	d := &DeepSeekBackend{
		client:   srv.Client(),
		endpoint: srv.URL,
		modelID:  "test",
		apiKey:   "test",
	}

	ch, errCh := d.StreamEx(context.Background(), nil)
	var contents []string
	done := false
	for !done {
		select {
		case chunk, ok := <-ch:
			if !ok {
				done = true
				break
			}
			if chunk.Content != "" {
				contents = append(contents, chunk.Content)
			}
		case e := <-errCh:
			if e != nil {
				t.Fatalf("stream error: %v", e)
			}
		}
	}
	if e := <-errCh; e != nil {
		t.Fatalf("errCh: %v", e)
	}
	t.Logf("正文: %q", strings.Join(contents, ""))
}
