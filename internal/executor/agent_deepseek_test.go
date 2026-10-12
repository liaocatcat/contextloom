package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// sseServer 构造模拟 DeepSeek SSE 服务器（executor 包内联版）
func sseServer(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("not flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
			flusher.Flush()
		}
	}))
}

// collectAgentStream 收集 RunStream 全部 token 拼接
func collectAgentStream(t *testing.T, ch <-chan string, errCh <-chan error) string {
	t.Helper()
	var sb strings.Builder
	for token := range ch {
		sb.WriteString(token)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("RunStream 错误: %v", e)
	}
	return sb.String()
}

func newDeepSeekAgent(t *testing.T, endpoint string) *Agent {
	t.Helper()
	ds := model.NewDeepSeekBackend(endpoint, "sk-test", "flash-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	toolReg := tools.NewRegistry()
	return &Agent{
		ID:      "test-ds-agent",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "你好", Mode: "task"},
		harness: eng,
		tools:   toolReg,
		backend: ds,
	}
}

// TestRunStream_ForwardIntegrity 验证 agent.RunStream 转发 StreamEx 输出无丢字
func TestRunStream_ForwardIntegrity(t *testing.T) {
	events := []string{
		`{"choices":[{"delta":{"reasoning_content":"分析需求"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"需要工具"}}]}`,
		`{"choices":[{"delta":{"content":"你好！很高兴为您服务。"}}]}`,
		`{"choices":[{"delta":{"content":"我可以帮你"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := sseServer(t, events)
	defer ts.Close()

	agent := newDeepSeekAgent(t, ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}

	got := collectAgentStream(t, ch, errCh)
	// 期望与 StreamEx 直连输出一致：思考标签 + 正文，无丢失。
	// 模型给出实质回答且未调用工具 → 直接结束（不再注入"请继续"催促）
	want := "<|reasoning|>分析需求需要工具<|/reasoning|>你好！很高兴为您服务。我可以帮你"
	if got != want {
		t.Errorf("RunStream 转发丢字/乱序:\nwant: %q\n got: %q", want, got)
	}
}

// TestRunStream_PureContentForward 纯正文分片转发完整性
func TestRunStream_PureContentForward(t *testing.T) {
	events := []string{
		`{"choices":[{"delta":{"content":"你好"}}]}`,
		`{"choices":[{"delta":{"content":"！我是"}}]}`,
		`{"choices":[{"delta":{"content":"ContextLoom"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := sseServer(t, events)
	defer ts.Close()

	agent := newDeepSeekAgent(t, ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}

	got := collectAgentStream(t, ch, errCh)
	want := "你好！我是ContextLoom"
	if got != want {
		t.Errorf("纯正文转发丢字:\nwant: %q\n got: %q", want, got)
	}
}
