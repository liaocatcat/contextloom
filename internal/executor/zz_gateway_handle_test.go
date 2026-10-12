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

// gatewayHandleBackend 模拟 modelgateway.handle：包装真实 DeepSeekBackend，
// 暴露 model.StreamExBackend（StreamEx + SetStreamTools 转发底层）——
// agent 的接口断言成功 → 走 StreamEx 完整路径（工具调用可达执行层）。
// 这正是 config 启用 gateway 后用户的真实运行路径。
type gatewayHandleBackend struct {
	inner *model.DeepSeekBackend
}

func (g *gatewayHandleBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return g.inner.Chat(ctx, messages, tools)
}
func (g *gatewayHandleBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	// 与 handle.Stream 一致：转发底层 Stream（只转发 Content 文本）
	return g.inner.Stream(ctx, messages)
}
func (g *gatewayHandleBackend) StreamEx(ctx context.Context, messages []types.Message) (<-chan model.StreamChunk, <-chan error) {
	// 与 handle.StreamEx 一致：完整转发底层 StreamEx（Content+ToolCalls+FinishReason）
	return g.inner.StreamEx(ctx, messages)
}
func (g *gatewayHandleBackend) SetStreamTools(tools []types.ToolDefinition) {
	g.inner.SetStreamTools(tools)
}
func (g *gatewayHandleBackend) ModelID() string             { return g.inner.ModelID() }
func (g *gatewayHandleBackend) MaxContext() int             { return g.inner.MaxContext() }
func (g *gatewayHandleBackend) SupportsVision() bool        { return g.inner.SupportsVision() }
func (g *gatewayHandleBackend) SetReasoningEffort(s string) { g.inner.SetReasoningEffort(s) }
func (g *gatewayHandleBackend) ReasoningEffort() string     { return g.inner.ReasoningEffort() }
func (g *gatewayHandleBackend) SetTopP(p float64)           { g.inner.SetTopP(p) }
func (g *gatewayHandleBackend) TopP() float64               { return g.inner.TopP() }
func (g *gatewayHandleBackend) SetTemperature(t float64)    { g.inner.SetTemperature(t) }
func (g *gatewayHandleBackend) Temperature() float64        { return g.inner.Temperature() }

// plainTextBackend 模拟无 StreamEx 的纯 model.Backend 后端（本地 llama/ollama 等）：
// 只提供普通 Stream（文本流）——agent 走文本兜底解析分支。
type plainTextBackend struct {
	inner *model.DeepSeekBackend
	calls atomic.Int32
}

func (p *plainTextBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return p.inner.Chat(ctx, messages, tools)
}
func (p *plainTextBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	// 模拟无 StreamEx 的纯文本后端（llama/ollama 等）：文本流就是模型的
	// 原始输出（含 XML 工具调用），不会像 DeepSeekBackend.Stream 那样把
	// XML 解析吞掉——agent 文本兜底分支应能直接解析。
	n := p.calls.Add(1)
	ch := make(chan string, 8)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		select {
		case <-ctx.Done():
			return
		default:
		}
		if n == 1 {
			ch <- "我需要先查看项目结构。\n\n<tool_calls>\n  <invoke name=\"list_files\">\n  <parameter name=\"path\" string=\"true\">/tmp</parameter>\n  </invoke>\n</tool_calls>"
		} else {
			ch <- "完成，以上是项目分析结果。"
		}
	}()
	return ch, errCh
}
func (p *plainTextBackend) ModelID() string             { return p.inner.ModelID() }
func (p *plainTextBackend) MaxContext() int             { return p.inner.MaxContext() }
func (p *plainTextBackend) SupportsVision() bool        { return p.inner.SupportsVision() }
func (p *plainTextBackend) SetReasoningEffort(s string) { p.inner.SetReasoningEffort(s) }
func (p *plainTextBackend) ReasoningEffort() string     { return p.inner.ReasoningEffort() }
func (p *plainTextBackend) SetTopP(v float64)           { p.inner.SetTopP(v) }
func (p *plainTextBackend) TopP() float64               { return p.inner.TopP() }
func (p *plainTextBackend) SetTemperature(v float64)    { p.inner.SetTemperature(v) }
func (p *plainTextBackend) Temperature() float64        { return p.inner.Temperature() }

// 精确复现用户真实运行路径（config 启用 gateway → backend 是 *handle 包装）：
// 模型输出 XML 工具调用文本（无 tools 参数时 DeepSeek 被 system prompt 引导
// 输出 XML），handle.Stream 只转发文本 → agent else 分支。
// 旧代码：直接 break → 工具从不执行、对话一轮中止（检查点 round=1 无 assistant）。
func TestAgentRunStream_GatewayHandlePath_XMLToolCall(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			// 第一轮：reasoning + 完整 XML 工具调用（list_files + exec_command）
			w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"先查看项目结构"}}]}` + "\n"))
			fl.Flush()
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"<tool_calls>\n  <invoke name=\"list_files\">\n  <parameter name=\"path\" string=\"true\">/path/to/contextloom</parameter>\n  </invoke>\n  <invoke name=\"exec_command\">\n  <parameter name=\"command\" string=\"true\">ls</parameter>\n  </invoke>\n</tool_calls>"}}]}` + "\n"))
			fl.Flush()
			w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
			w.Write([]byte("data: [DONE]\n"))
			fl.Flush()
			return
		}
		// 后续轮次：正常回答（工具结果已回传）
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"已完成，以上是项目内容。"}}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-gateway",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "分析项目的内容", Mode: "task"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: &gatewayHandleBackend{inner: ds}, // 关键：非 *DeepSeekBackend
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	t.Logf("agent 输出: %q", got)

	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("gateway 路径应执行 list_files（文本兜底解析），实际输出: %q", got)
	}
	if !strings.Contains(got, "✔ exec_command:") {
		t.Errorf("gateway 路径应执行 exec_command（并发执行显示 ✔ 前缀），实际输出: %q", got)
	}
	if reqCount.Load() < 2 {
		t.Errorf("应发起 ≥2 轮请求（执行工具后继续），实际 %d", reqCount.Load())
	}
	if strings.Contains(got, "<invoke name=") {
		t.Errorf("XML 工具调用不应残留为正文: %q", got)
	}
}

// 变体：gateway 路径 + 模型输出截断 XML（16:55 形态：<invoke name="list_files"> 后无闭合）。
// 解析层截断修复应补全并执行。
func TestAgentRunStream_GatewayHandlePath_TruncatedXML(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			w.Write([]byte(`data: {"choices":[{"delta":{"reasoning_content":"分析项目内容"}}]}` + "\n"))
			fl.Flush()
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"<tool_calls>\n  <invoke name=\"list_files\">"}}]}` + "\n"))
			fl.Flush()
			w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
			w.Write([]byte("data: [DONE]\n"))
			fl.Flush()
			return
		}
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"以下是分析结果。"}}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-gateway-trunc",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "分析项目的内容", Mode: "task"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: &gatewayHandleBackend{inner: ds},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	t.Logf("agent 输出: %q", got)

	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("gateway 截断场景应执行 list_files（补全后解析），实际输出: %q", got)
	}
	if reqCount.Load() < 2 {
		t.Errorf("应发起 ≥2 轮请求，实际 %d", reqCount.Load())
	}
}

// 纯 model.Backend（无 StreamEx）后端：agent 走文本兜底分支。
// 底层 StreamEx 已把 XML 解析为 ToolCalls（不转发文本）→ 文本里没有 XML →
// 但若模型输出普通正文含 XML 痕迹（无 tools 时模型被 prompt 引导输出 XML），
// 文本兜底应能解析并执行。
func TestAgentRunStream_PlainTextBackend_XMLInText(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		if n == 1 {
			// 无 tools 请求：模型把 XML 直接输出为普通正文（StreamEx 三态处理
			// 在 pending 内缓冲并解析 → 解析成功后 XML 不转发 → 文本里无 XML）
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"我需要先查看项目结构。\n\n<tool_calls>\n  <invoke name=\"list_files\">\n  <parameter name=\"path\" string=\"true\">/tmp</parameter>\n  </invoke>\n</tool_calls>"}}]}` + "\n"))
			fl.Flush()
			w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
			w.Write([]byte("data: [DONE]\n"))
			fl.Flush()
			return
		}
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"完成。"}}]}` + "\n"))
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-plain",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "分析项目的内容", Mode: "task"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: &plainTextBackend{inner: ds},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	t.Logf("agent 输出: %q", got)

	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("纯文本后端应执行 list_files，实际输出: %q", got)
	}
}
