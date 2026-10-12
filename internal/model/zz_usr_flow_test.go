package model

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 模拟用户实际案例：流式输出纯 XML 风格 <tool_calls><invoke><parameter>
func TestStream_XMLUserCase(t *testing.T) {
	xmlChunks := []string{
		"<tool_calls>\n",
		"<invoke name=\"exec_command\">\n",
		"<parameter name=\"cmd\">git log --oneline -10 && echo \"---STATUS---\" && git status --short</parameter>\n",
		"<parameter name=\"justification\">查看最近提交与工作区状态</parameter>\n",
		"<parameter name=\"desc\">git历史与状态</parameter>\n",
		"<parameter name=\"timeout\">30000</parameter>\n",
		"</invoke>\n",
		"</tool_calls>",
	}
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range xmlChunks {
			data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}
		data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ch, errCh := b.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "分析项目进展"}})

	var content strings.Builder
	var calls []types.ToolCall
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return
				}
				if c.IsToolCall && len(c.ToolCalls) > 0 {
					calls = append(calls, c.ToolCalls...)
				} else if c.Content != "" {
					content.WriteString(c.Content)
				}
			case e, ok := <-errCh:
				if ok && e != nil {
					streamErr = e
					return
				}
			}
		}
	}()
	<-done

	if streamErr != nil {
		t.Fatalf("StreamEx error: %v", streamErr)
	}
	t.Logf("正文: %q", content.String())
	t.Logf("调用数: %d", len(calls))
	for i, c := range calls {
		t.Logf("call%d name=%s args=%s", i, c.Function.Name, c.Function.Arguments)
	}
	if len(calls) != 1 {
		t.Fatalf("期望 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "exec_command" {
		t.Errorf("name=%s", calls[0].Function.Name)
	}
}
