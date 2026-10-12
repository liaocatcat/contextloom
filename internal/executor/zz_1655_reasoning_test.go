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

// 精确复现用户 16:55 真实场景：
// 中转 API 思考模式：reasoning_content 输出长思考（40+ 行），
// content 只输出截断的 <tool_calls><invoke name="list_files">（连 parameter 都没有），
// finish_reason=stop。
// 用户实测表现：思考全文显示，工具调用 XML 截断显示，然后直接"工作完成"，工具从不执行。
func TestAgentRunStream_Real1655_ReasoningTruncatedXML(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			// 16:55 第一轮：reasoning_content 长思考 + content 截断 XML + stop
			reasoning := "我们需要理解当前状态。用户消息历史中只有两条：\n1. user: 分析项目的内容，前面的目标放弃\n2. 系统给我们一个任务框架。\n\n但是实际上这里可能有误解。\n\n首先，我们需要列出项目文件，读README等。任务说这是一个分析任务。\n\n让我们开始。首先列出根目录文件。"
			// 分片发送 reasoning_content（模拟逐 token 流式）
			for _, rc := range []string{reasoning[:30], reasoning[30:60], reasoning[60:]} {
				w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"` + rc + `"}}]}` + "\n"))
				fl.Flush()
			}
			// content：截断的 XML（<invoke name="list_files"> 之后什么都没有）
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"<tool_calls>\n  <invoke name=\"list_files\">"}}]}` + "\n"))
			fl.Flush()
			w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
			w.Write([]byte("data: [DONE]\n"))
			fl.Flush()
			return
		}
		// 后续轮次：正常回答（工具结果已回传）
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"以下是项目整体内容分析结果。"}}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-1655",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "分析项目的内容", Mode: "task"},
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

	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("16:55 reasoning+截断XML 场景应执行 list_files，实际输出: %q", got)
	}
	if reqCount.Load() < 2 {
		t.Errorf("应发起 ≥2 轮请求（执行工具后继续），实际 %d", reqCount.Load())
	}
}
