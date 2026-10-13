package model

// 独立生成参数（GenerationParamsBackend）单测：
// 覆盖"未设置零回归 / 独立输出上限 / 思考开关发送规则 / off 等效关闭思考 /
// 独立输入预算触发压缩 / StreamEx 同源 / 并发安全 / ApplyGenerationParams"。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 测试基础设施 ──

// bodyCapture 线程安全地保存最近一次请求体（httptest handler 与测试主协程
// 并发，避免 -race 下读写竞争）。
type bodyCapture struct {
	mu  sync.Mutex
	raw map[string]any
}

func (c *bodyCapture) set(m map[string]any) {
	c.mu.Lock()
	c.raw = m
	c.mu.Unlock()
}

func (c *bodyCapture) get() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.raw
}

// newCaptureChatServer 启动捕获请求体的 Chat 测试服务器（固定返回成功响应）。
func newCaptureChatServer(t *testing.T) (*httptest.Server, *bodyCapture) {
	t.Helper()
	cap := &bodyCapture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.set(body)
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(ts.Close)
	return ts, cap
}

func chatOnce(t *testing.T, d *DeepSeekBackend, messages []types.Message) {
	t.Helper()
	if _, _, err := d.Chat(context.Background(), messages, nil); err != nil {
		t.Fatalf("Chat 不应报错: %v", err)
	}
}

// thinkingTypeOf 返回请求体中 thinking 字段的 type 值（缺失时 ok=false）。
func thinkingTypeOf(t *testing.T, body map[string]any) (string, bool) {
	t.Helper()
	v, ok := body["thinking"]
	if !ok {
		return "", false
	}
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("thinking 字段类型异常: %T (%v)", v, v)
	}
	s, _ := m["type"].(string)
	return s, true
}

// firstUserContent 返回请求体 messages 中第一条 user 消息的 content。
func firstUserContent(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, ok := body["messages"].([]any)
	if !ok {
		t.Fatalf("messages 字段类型异常: %T", body["messages"])
	}
	for _, m := range msgs {
		mm, ok := m.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := mm["role"].(string); role == "user" {
			s, _ := mm["content"].(string)
			return s
		}
	}
	t.Fatal("未找到 user 消息")
	return ""
}

// ── 请求体断言 ──

// TestGenerationParams_MaxTokensUnsetEqualsMaxContext 零回归：未设置独立输出
// 上限时，请求体 max_tokens 仍等于 maxContext（历史行为逐字不变），且不发
// thinking 字段。
func TestGenerationParams_MaxTokensUnsetEqualsMaxContext(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if body == nil {
		t.Fatal("请求未被捕获")
	}
	if mt, _ := body["max_tokens"].(float64); int(mt) != 131072 {
		t.Fatalf("未设置时 max_tokens=%v, 期望 131072（=maxContext）", body["max_tokens"])
	}
	if _, ok := body["thinking"]; ok {
		t.Fatalf("未设置 thinking 时不应发送 thinking 字段: %v", body["thinking"])
	}
}

// TestGenerationParams_MaxOutputTokensOverride 设置独立输出上限后，请求体
// max_tokens 反映独立值（不再随上下文窗口膨胀）。
func TestGenerationParams_MaxOutputTokensOverride(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetMaxOutputTokens(65536)
	if got := d.MaxOutputTokens(); got != 65536 {
		t.Fatalf("MaxOutputTokens()=%d, 期望 65536", got)
	}
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if mt, _ := body["max_tokens"].(float64); int(mt) != 65536 {
		t.Fatalf("设置后 max_tokens=%v, 期望 65536", body["max_tokens"])
	}
}

// TestGenerationParams_ThinkingDisabledNoEffort thinking 显式关闭时：
// 发 thinking:{type:disabled}，且不发 reasoning_effort（对齐 dsh 组合约束）。
func TestGenerationParams_ThinkingDisabledNoEffort(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("high")
	d.SetThinkingEnabled(false)
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "disabled" {
		t.Fatalf("thinking=%v, 期望 {type:disabled}", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("thinking 关闭时不应发送 reasoning_effort: %v", body["reasoning_effort"])
	}
}

// TestGenerationParams_ThinkingEnabledWithEffort thinking 显式开启时：
// 发 thinking:{type:enabled}，并照常发 reasoning_effort。
func TestGenerationParams_ThinkingEnabledWithEffort(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("high")
	d.SetThinkingEnabled(true)
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "enabled" {
		t.Fatalf("thinking=%v, 期望 {type:enabled}", body["thinking"])
	}
	if got, _ := body["reasoning_effort"].(string); got != "high" {
		t.Fatalf("reasoning_effort=%v, 期望 high", body["reasoning_effort"])
	}
}

// TestGenerationParams_EffortOffDisablesThinking SetReasoningEffort("off")
// 等效关闭思考：发 thinking:{type:disabled}、不发 reasoning_effort，
// 且 ReasoningEffort() 保留 "off" 供 UI 回显。
func TestGenerationParams_EffortOffDisablesThinking(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("off")
	if got := d.ReasoningEffort(); got != "off" {
		t.Fatalf("ReasoningEffort()=%q, 期望 off（UI 回显）", got)
	}
	if d.ThinkingEnabled() {
		t.Fatal("off 档应将思考开关置为关闭")
	}
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "disabled" {
		t.Fatalf("thinking=%v, 期望 {type:disabled}", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("off 档不应发送 reasoning_effort: %v", body["reasoning_effort"])
	}
}

// TestGenerationParams_ThinkingNilKeepsLegacyBody 零回归：未显式设置 thinking
// （nil）时保持历史请求体——只发 reasoning_effort、不发 thinking。
func TestGenerationParams_ThinkingNilKeepsLegacyBody(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("medium") // medium → high（既有映射）
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinking 未设置时不应发送该字段: %v", body["thinking"])
	}
	if got, _ := body["reasoning_effort"].(string); got != "high" {
		t.Fatalf("reasoning_effort=%v, 期望 high", body["reasoning_effort"])
	}
}

// TestGenerationParams_InputLimitTriggersCompaction 独立输入预算生效：
// maxInputTokens 远小于 maxContext 时，发送前压缩按 maxInputTokens 触发
// （同样的消息在未设置输入预算时原样发送，零回归对照）。
func TestGenerationParams_InputLimitTriggersCompaction(t *testing.T) {
	long := strings.Repeat("这是一段用于触发输入预算压缩的长文本。", 80)
	mkMessages := func() []types.Message {
		return []types.Message{
			{Role: "system", Content: "你是助手"},
			{Role: "user", Content: long},
		}
	}

	// ① 未设置输入预算：maxContext 足够大 → 不触发压缩，原文发送
	ts1, cap1 := newCaptureChatServer(t)
	d1 := NewDeepSeekBackend(ts1.URL, "k", "m", 200000)
	chatOnce(t, d1, mkMessages())
	body1 := cap1.get()
	if got := firstUserContent(t, body1); got != long {
		t.Fatalf("未设置输入预算时消息不应被压缩：len(got)=%d, len(want)=%d",
			len([]rune(got)), len([]rune(long)))
	}

	// ② 设置 maxInputTokens=100：安全红线被突破 → 压缩触发，内容被截短
	ts2, cap2 := newCaptureChatServer(t)
	d2 := NewDeepSeekBackend(ts2.URL, "k", "m", 200000)
	d2.SetMaxInputTokens(100)
	if got := d2.inputLimit(); got != 100 {
		t.Fatalf("inputLimit()=%d, 期望 100", got)
	}
	chatOnce(t, d2, mkMessages())
	body2 := cap2.get()
	got2 := firstUserContent(t, body2)
	if got2 == "" {
		t.Fatal("压缩后用户消息不应为空")
	}
	if len([]rune(got2)) >= len([]rune(long)) {
		t.Fatalf("输入预算 100 应触发压缩（内容变短）：len(got)=%d, want<%d",
			len([]rune(got2)), len([]rune(long)))
	}
}

// TestGenerationParams_StreamExBody StreamEx 路径同样反映独立参数
// （与 Chat 共用 applyThinkingParams，防止两条路径行为漂移）。
func TestGenerationParams_StreamExBody(t *testing.T) {
	cap := &bodyCapture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.set(body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		if flusher != nil {
			flusher.Flush()
		}
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetMaxOutputTokens(32768)
	d.SetThinkingEnabled(false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh := d.StreamEx(ctx, []types.Message{{Role: "user", Content: "hi"}})
	for range ch {
	}
	if e := <-errCh; e != nil {
		t.Fatalf("StreamEx 不应报错: %v", e)
	}
	body := cap.get()
	if body == nil {
		t.Fatal("请求未被捕获")
	}
	if mt, _ := body["max_tokens"].(float64); int(mt) != 32768 {
		t.Fatalf("StreamEx max_tokens=%v, 期望 32768", body["max_tokens"])
	}
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "disabled" {
		t.Fatalf("StreamEx thinking=%v, 期望 {type:disabled}", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("StreamEx thinking 关闭时不应发送 reasoning_effort: %v", body["reasoning_effort"])
	}
}

// TestGenerationParams_ConcurrentSetters 并发安全冒烟：setter/getter 与
// maxTokens/inputLimit 并发调用（配合 go test -race 验证无数据竞争）。
func TestGenerationParams_ConcurrentSetters(t *testing.T) {
	d := NewDeepSeekBackend("http://127.0.0.1:1", "k", "m", 4096)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				d.SetMaxOutputTokens(i)
				d.SetMaxInputTokens(i)
				d.SetThinkingEnabled(i%2 == 0)
				d.SetReasoningEffort("high")
				d.SetReasoningEffort("off")
				_ = d.MaxOutputTokens()
				_ = d.MaxInputTokens()
				_ = d.ThinkingEnabled()
				_ = d.ReasoningEffort()
				_ = d.maxTokens()
				_ = d.inputLimit()
			}
		}()
	}
	wg.Wait()
}

// ── ApplyGenerationParams ──

// plainBackend 仅实现 Backend 本体的最小后端（不含 GenerationParamsBackend），
// 验证 ApplyGenerationParams 对不支持的后端返回 false 且不 panic。
type plainBackend struct{}

func (plainBackend) Chat(context.Context, []types.Message, []types.ToolDefinition) (string, []types.ToolCall, error) {
	return "", nil, nil
}

func (plainBackend) Stream(context.Context, []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string)
	errC := make(chan error)
	close(ch)
	close(errC)
	return ch, errC
}

func (plainBackend) ModelID() string { return "plain" }

func (plainBackend) MaxContext() int { return 4096 }

func (plainBackend) SupportsVision() bool { return false }

func (plainBackend) SetReasoningEffort(string) {}

func (plainBackend) ReasoningEffort() string { return "" }

func (plainBackend) SetTopP(float64) {}

func (plainBackend) TopP() float64 { return 0 }

func (plainBackend) SetTemperature(float64) {}

func (plainBackend) Temperature() float64 { return 0 }

// TestApplyGenerationParams_Supported 支持的后端：显式项逐项下发。
func TestApplyGenerationParams_Supported(t *testing.T) {
	d := NewDeepSeekBackend("http://x", "k", "m", 4096)
	on := true
	if !ApplyGenerationParams(d, 65536, 2048, &on) {
		t.Fatal("DeepSeekBackend 支持独立参数，应返回 true")
	}
	if d.MaxOutputTokens() != 65536 || d.MaxInputTokens() != 2048 || !d.ThinkingEnabled() {
		t.Fatalf("下发值未生效: out=%d in=%d thinking=%v",
			d.MaxOutputTokens(), d.MaxInputTokens(), d.ThinkingEnabled())
	}
}

// TestApplyGenerationParams_OnlyExplicitItems 仅显式项被下发：
// 0 值与 nil 不产生副作用，全部未显式时返回 false。
func TestApplyGenerationParams_OnlyExplicitItems(t *testing.T) {
	d := NewDeepSeekBackend("http://x", "k", "m", 4096)
	if ApplyGenerationParams(d, 0, 0, nil) {
		t.Fatal("三项都未显式设置时应返回 false")
	}
	if d.MaxOutputTokens() != 0 || d.MaxInputTokens() != 0 || d.ThinkingEnabled() {
		t.Fatal("未显式设置的项不应产生副作用")
	}
	if !ApplyGenerationParams(d, 0, 1024, nil) {
		t.Fatal("仅输入预算显式设置也应返回 true")
	}
	if d.MaxInputTokens() != 1024 || d.MaxOutputTokens() != 0 {
		t.Fatalf("仅应下发输入预算: out=%d in=%d", d.MaxOutputTokens(), d.MaxInputTokens())
	}
}

// TestApplyGenerationParams_UnsupportedBackend 不支持的后端：返回 false、不 panic。
func TestApplyGenerationParams_UnsupportedBackend(t *testing.T) {
	var b Backend = plainBackend{}
	on := true
	if ApplyGenerationParams(b, 1024, 1024, &on) {
		t.Fatal("不支持的后端应返回 false")
	}
}
