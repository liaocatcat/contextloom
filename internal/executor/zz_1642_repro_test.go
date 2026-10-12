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

// 精确复现用户 16:42 真实场景：
// 模型逐 token 流式输出正文 + 截断的 XML 工具调用（无 </parameter></invoke></tool_calls>），
// 然后 finish_reason=stop（模型自己停，不是 max_tokens 截断）。
// 用户实测表现：XML 显示在屏幕上但工具从不执行，直接"工作完成"。
func TestAgentRunStream_Real1642_TruncatedXMLStop(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			// 16:42 第一轮：正文 + 逐 token 截断 XML + stop
			chunks := []string{
				`我先查看项目根目录结构，了解整体组成。\n\n`,
				`<tool_calls>\n`,
				`<invoke name=\"list_files\">\n`,
				`<parameter name=\"path\" string=\"true\">/path/to/contextloom`,
			}
			for _, c := range chunks {
				w.Write([]byte(`data: {"choices":[{"delta":{"content":"` + c + `"}}]}` + "\n"))
				fl.Flush()
			}
			w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
			w.Write([]byte("data: [DONE]\n"))
			fl.Flush()
			return
		}
		// 后续轮次：正常回答（工具结果已回传）
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"以下是项目历史对话记录…"}}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-1642",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "分析项目内容", Mode: "task"},
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
		t.Errorf("截断 XML+stop 场景应执行 list_files（修复缺失闭合后解析），实际输出: %q", got)
	}
	if reqCount.Load() < 2 {
		t.Errorf("应发起 ≥2 轮请求（执行工具后继续），实际 %d", reqCount.Load())
	}
	// XML 不应残留在最终输出中（被解析并剥离）
	if strings.Contains(got, "<invoke name=") {
		t.Errorf("XML 工具调用不应残留为正文: %q", got)
	}
}

// 变体：截断发生在 </parameter> 之前（16:26 的 exec_command 形态），且连接异常中断
// （http.Hijack 关闭连接 → scanner.Err() != nil → StreamEx 发 streamErr）。
// 这是 16:26/16:42 真实失败路径：flushPending 已解析出工具调用，但 streamErr
// 让旧代码在 append assistant 前 return → 工具从不执行、对话中止。
func TestAgentRunStream_Real1626_TruncatedExecConnReset(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			chunks := []string{
				`我来查找项目中存储历史对话记录的位置。先查看项目结构和内存目录。\n\n`,
				`<tool_calls>\n`,
				`<invoke name=\"list_files\">\n`,
				`<parameter name=\"path\" string=\"true\">/path/to/contextloom/.contextloom/memory</parameter>\n`,
				`</invoke>\n`,
				`<invoke name=\"exec_command\">\n`,
				`<parameter name=\"command\" string=\"true\">echo hello; ls /tmp\n`,
				`| head -1`,
			}
			for _, c := range chunks {
				w.Write([]byte(`data: {"choices":[{"delta":{"content":"` + c + `"}}]}` + "\n"))
				fl.Flush()
			}
			// 无 finish_reason，异常关闭连接（Hijack + Close → 客户端 scanner.Err() 非空）
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
			w.Write([]byte("data: [DONE]\n"))
			fl.Flush()
			return
		}
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"已找到，以下是历史对话记录。"}}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-1626",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "列出项目所有历史对话记录", Mode: "task"},
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

	// 多调用 + 部分截断：2 个工具都应执行
	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("应执行 list_files，实际输出: %q", got)
	}
	if !strings.Contains(got, "✔ exec_command:") {
		t.Errorf("应执行 exec_command，实际输出: %q", got)
	}
}
