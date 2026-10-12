package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 调试：16:42 逐 token 截断 XML + stop，看 StreamEx 的 chunk 流
func TestStreamEx_Debug1642(t *testing.T) {
	chunks := []string{
		"我先查看项目根目录结构，了解整体组成。\\n\\n",
		"<tool_calls>\\n",
		"<invoke name=\\\"list_files\\\">\\n",
		"<parameter name=\\\"path\\\" string=\\\"true\\\">/path/to/contextloom",
	}
	reqCount := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount++
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			line := `data: {"choices":[{"delta":{"content":"` + c + `"}}]}`
			w.Write([]byte(line + "\n"))
			fl.Flush()
		}
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	ch, errCh := ds.StreamEx(context.Background(), nil)
	for {
		select {
		case sc, ok := <-ch:
			if !ok {
				<-errCh
				return
			}
			t.Logf("CHUNK content=%q toolCalls=%d isTool=%v finish=%q", sc.Content, len(sc.ToolCalls), sc.IsToolCall, sc.FinishReason)
			for _, tc := range sc.ToolCalls {
				t.Logf("  TOOLCALL id=%s name=%s args=%q", tc.ID, tc.Function.Name, tc.Function.Arguments)
			}
		case e, ok := <-errCh:
			if ok && e != nil {
				t.Fatalf("err: %v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timeout")
		}
	}
}
