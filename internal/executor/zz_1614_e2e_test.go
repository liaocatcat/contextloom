package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 模拟 16:14 真实场景：第一轮模型只输出半句开场白（工具调用前被
// finish_reason=length 截断），第二轮输出完整工具调用。
// 验证：agent 不再"对话中止"——截断轮提示继续，第二轮正常执行工具。
func TestAgentRunStream_Real1614_TruncatedThenToolCall(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		var payload string
		if n == 1 {
			// 第一轮：半句正文 + length 截断（无工具调用）
			payload = `data: {"choices":[{"delta":{"content":"我来查找项目中存储的历史对话记录。先查看"}}]}
data: {"choices":[{"delta":{},"finish_reason":"length"}]}
data: [DONE]
`
		} else if n == 2 {
			// 第二轮：正文 + 完整工具调用 + stop
			payload = `data: {"choices":[{"delta":{"content":"好的，我来列出。"}}]}
data: {"choices":[{"delta":{"content":"<tool_calls>\n<invoke name=\"list_files\"><parameter name=\"path\">/tmp</parameter></invoke>\n</tool_calls>"}}]}
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
		} else {
			// 第三轮起：纯文本回答（工具已执行完，模型总结收尾）
			payload = `data: {"choices":[{"delta":{"content":"已列出 /tmp 目录内容。"}}]}
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
		}
		w.Write([]byte(payload))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-agent",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "列出历史对话", Mode: "task"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: ds,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	t.Logf("agent 输出: %q", got)

	// 第一轮截断后应提示继续，而不是直接"工作完成"
	if !strings.Contains(got, "输出被截断") {
		t.Errorf("第一轮截断后应提示继续，实际输出: %q", got)
	}
	// 第二轮应执行工具
	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("第二轮应执行 list_files，实际输出: %q", got)
	}
	if reqCount.Load() < 2 {
		t.Errorf("应发起 ≥2 轮请求（截断后继续），实际 %d", reqCount.Load())
	}
}
