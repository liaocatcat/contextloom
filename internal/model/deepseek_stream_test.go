package model

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// newSSEServer 构造模拟 DeepSeek SSE 流式服务器
func newSSEServer(t *testing.T, events []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter 不支持 Flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
			flusher.Flush()
		}
	}))
}

// collectStream 收集 StreamEx 全部 chunk.Content 拼接，验证完整性
func collectStream(t *testing.T, ds *DeepSeekBackend, messages []types.Message) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh := ds.StreamEx(ctx, messages)
	var sb strings.Builder
	for chunk := range ch {
		sb.WriteString(chunk.Content)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream 返回错误: %v", e)
	}
	return sb.String()
}

func TestStreamEx_NormalReasoningThenContent(t *testing.T) {
	events := []string{
		`{"choices":[{"delta":{"reasoning_content":"分析用户需求"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"需要调用文件工具"}}]}`,
		`{"choices":[{"delta":{"content":"你好！很高兴为您服务。"}}]}`,
		`{"choices":[{"delta":{"content":"我可以帮你编写代码"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := newSSEServer(t, events)
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	got := collectStream(t, ds, []types.Message{{Role: "user", Content: "你好"}})

	// 期望完整拼接：思考标签包裹两段 reasoning + 两段正文
	want := "<|reasoning|>分析用户需求需要调用文件工具<|/reasoning|>你好！很高兴为您服务。我可以帮你编写代码"
	if got != want {
		t.Errorf("拼接不完整/丢字:\nwant: %q\n got: %q", want, got)
	}
}

func TestStreamEx_AlternatingReasoningContent(t *testing.T) {
	// 模型回炉思考：reasoning 和 content 交替出现
	events := []string{
		`{"choices":[{"delta":{"reasoning_content":"思1"}}]}`,
		`{"choices":[{"delta":{"content":"正1"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"思2"}}]}`,
		`{"choices":[{"delta":{"content":"正2"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := newSSEServer(t, events)
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	got := collectStream(t, ds, []types.Message{{Role: "user", Content: "hi"}})

	// 每段思考与正文都应正确配对，无丢失
	want := "<|reasoning|>思1<|/reasoning|>正1<|reasoning|>思2<|/reasoning|>正2"
	if got != want {
		t.Errorf("交替拼接错误:\nwant: %q\n got: %q", want, got)
	}
}

func TestStreamEx_NoFinishReason_CloseTagFallback(t *testing.T) {
	// 流式响应缺少 finish_reason（连接中断/异常结束），仍应补发闭标签
	events := []string{
		`{"choices":[{"delta":{"reasoning_content":"还没想完"}}]}`,
		`{"choices":[{"delta":{"content":"部分正文"}}]}`,
		// 无 finish_reason chunk，流直接结束
	}
	ts := newSSEServer(t, events)
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	got := collectStream(t, ds, []types.Message{{Role: "user", Content: "hi"}})

	// 兜底闭标签必须补上，否则思考泄漏到正文
	want := "<|reasoning|>还没想完<|/reasoning|>部分正文"
	if got != want {
		t.Errorf("无 finish_reason 兜底失败:\nwant: %q\n got: %q", want, got)
	}
}

func TestStreamEx_ToolCallsAccumulation(t *testing.T) {
	// tool_calls 分片到达：先 id+name，再 arguments 分两片
	events := []string{
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a.txt\"}"}}]}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	ts := newSSEServer(t, events)
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh := ds.StreamEx(ctx, []types.Message{{Role: "user", Content: "读a.txt"}})

	var gotToolCall *types.ToolCall
	for chunk := range ch {
		if chunk.IsToolCall && len(chunk.ToolCalls) > 0 {
			tc := chunk.ToolCalls[0]
			gotToolCall = &tc
		}
	}
	if e := <-errCh; e != nil {
		t.Fatalf("stream 错误: %v", e)
	}

	if gotToolCall == nil {
		t.Fatal("未收到 tool_calls")
	}
	if gotToolCall.ID != "call_1" || gotToolCall.Function.Name != "read" {
		t.Errorf("tool_call 元信息错误: id=%q name=%q", gotToolCall.ID, gotToolCall.Function.Name)
	}
	wantArgs := `{"path":"a.txt"}`
	if gotToolCall.Function.Arguments != wantArgs {
		t.Errorf("tool_call arguments 累积错误:\nwant: %q\n got: %q", wantArgs, gotToolCall.Function.Arguments)
	}
}

func TestStreamEx_OnlyContent_NoReasoning(t *testing.T) {
	// 简单问答：无 reasoning，仅有正文分片
	events := []string{
		`{"choices":[{"delta":{"content":"你好"}}]}`,
		`{"choices":[{"delta":{"content":"！我是"}}]}`,
		`{"choices":[{"delta":{"content":"ContextLoom"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	ts := newSSEServer(t, events)
	defer ts.Close()

	ds := NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	got := collectStream(t, ds, []types.Message{{Role: "user", Content: "hi"}})

	want := "你好！我是ContextLoom"
	if got != want {
		t.Errorf("纯正文拼接错误:\nwant: %q\n got: %q", want, got)
	}
}
