package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 调试：gatewayHandleBackend.Stream 转发行为——确认 XML 是否被 StreamEx 解析
// 成 ToolCalls（不转发）导致 agent else 分支文本里没有 XML。
func TestDebug_HandleStreamForward(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"先查看项目结构"}}]}` + "\n"))
		fl.Flush()
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"<tool_calls>\n  <invoke name=\"list_files\">\n  <parameter name=\"path\" string=\"true\">/tmp</parameter>\n  </invoke>\n</tool_calls>"}}]}` + "\n"))
		fl.Flush()
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	h := &gatewayHandleBackend{inner: ds}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh := h.Stream(ctx, []types.Message{{Role: "user", Content: "hi"}})

	var sb strings.Builder
	done := make(chan struct{})
	go func() {
		for tok := range ch {
			sb.WriteString(tok)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Stream 转发超时，已收集: %q", sb.String())
	}
	select {
	case e := <-errCh:
		if e != nil {
			t.Fatalf("Stream 错误: %v", e)
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("errCh 未关闭")
	}
	t.Logf("handle.Stream 转发内容: %q", sb.String())
}
