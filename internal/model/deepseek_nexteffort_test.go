package model

// 轮次临时层 effort（M7，设计文档 §3.3）单测：
// 覆盖"验证轮升档生效 / 一次性复位（跨请求、跨 Chat-StreamEx 路径不泄漏）/
// off 与 thinking 互斥矩阵不变 / thinking=enabled 分支叠加覆盖 / 空值清除 /
// 并发安全（配合 go test -race）"。
//
// 复用 generation_params_test.go 的 bodyCapture / newCaptureChatServer /
// chatOnce / thinkingTypeOf 与 deepseek_stream_test.go 的 collectStream。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// newCaptureMixedServer 启动"Chat + StreamEx 双通道"捕获服务器：非流式请求
// （无 stream 字段）返回 JSON 响应；stream:true 请求返回 SSE 流。请求体统一
// 写入线程安全的 bodyCapture，供两条构造路径的断言共用。
func newCaptureMixedServer(t *testing.T) (*httptest.Server, *bodyCapture) {
	t.Helper()
	cap := &bodyCapture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.set(body)
		if isStream, _ := body["stream"].(bool); isStream {
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
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	t.Cleanup(ts.Close)
	return ts, cap
}

// effortOf 返回请求体中 reasoning_effort 字符串值（缺失时 ok=false）。
func effortOf(body map[string]any) (string, bool) {
	v, ok := body["reasoning_effort"]
	if !ok {
		return "", false
	}
	s, _ := v.(string)
	return s, true
}

// TestRoundEffort_VerifyEscalation §6.1 #8：验证轮升档——SetNextEffort("max")
// 后下一次请求体 reasoning_effort=="max"；请求后复位，第二次请求恢复全局档位。
func TestRoundEffort_VerifyEscalation(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("low") // 全局档位 low

	d.SetNextEffort("max") // runtime 在 verify/refine 轮下发升档（默认 cap=max）
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if body == nil {
		t.Fatal("请求未被捕获")
	}
	if got, ok := effortOf(body); !ok || got != "max" {
		t.Fatalf("升档请求 reasoning_effort=%v（存在=%v），期望 max", body["reasoning_effort"], ok)
	}
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinkingEnabled==nil 时不应发送 thinking 字段: %v", body["thinking"])
	}
	if got := d.currentEffort(); got != "low" {
		t.Fatalf("请求后临时层应复位：currentEffort=%q，期望全局 low", got)
	}

	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body2 := cap.get()
	if got, _ := effortOf(body2); got != "low" {
		t.Fatalf("第二次请求 reasoning_effort=%v，期望恢复全局 low", body2["reasoning_effort"])
	}
}

// TestNextEffort_OneShot 一次性语义：连续两次请求，第一次覆盖生效、第二次恢复
// 全局档位；第二次改走 StreamEx 路径，同时验证两条构造路径共享同一消费语义
// （临时层不跨路径泄漏）。
func TestNextEffort_OneShot(t *testing.T) {
	ts, cap := newCaptureMixedServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("high") // 全局档位 high

	d.SetNextEffort("max")
	if got := d.currentEffort(); got != "max" {
		t.Fatalf("设置临时层后 currentEffort=%q，期望 max（临时层优先）", got)
	}

	// ① Chat 请求：覆盖生效
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body1 := cap.get()
	if got, _ := effortOf(body1); got != "max" {
		t.Fatalf("第一次请求 reasoning_effort=%v，期望 max（覆盖生效）", body1["reasoning_effort"])
	}

	// ② StreamEx 请求：恢复全局（临时层已消费）
	got := collectStream(t, d, []types.Message{{Role: "user", Content: "hi"}})
	if got == "" {
		t.Fatal("StreamEx 未收到内容")
	}
	body2 := cap.get()
	if got, _ := effortOf(body2); got != "high" {
		t.Fatalf("第二次（StreamEx）请求 reasoning_effort=%v，期望恢复全局 high", body2["reasoning_effort"])
	}
	if _, ok := body2["thinking"]; ok {
		t.Fatalf("thinkingEnabled==nil 时不应发送 thinking 字段: %v", body2["thinking"])
	}
}

// TestNextEffort_OffMatrix §6.1 #8：全局 off 档时 SetNextEffort 不影响 thinking
// 字段互斥逻辑——请求体仍为 thinking:{type:"disabled"} 且【不发】reasoning_effort；
// 临时层照常被消费，不泄漏到下一请求。
func TestNextEffort_OffMatrix(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("off") // 全局 off：thinkingEnabled=false
	d.SetNextEffort("max")      // 升档不得破坏 off 互斥

	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "disabled" {
		t.Fatalf("off 档应发 thinking:disabled，got=%v（存在=%v）", body["thinking"], ok)
	}
	if got, ok := effortOf(body); ok {
		t.Fatalf("off 档不应发送 reasoning_effort，got=%q", got)
	}
	if d.ThinkingEnabled() {
		t.Fatal("SetNextEffort 不应改变 thinkingEnabled=false 状态")
	}
	if got := d.currentEffort(); got != "off" {
		t.Fatalf("临时层应已消费：currentEffort=%q，期望全局 off", got)
	}

	// 第二次请求：互斥规则与全局 off 均保持（无泄漏）。
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body2 := cap.get()
	if tt, ok := thinkingTypeOf(t, body2); !ok || tt != "disabled" {
		t.Fatalf("第二次请求 thinking=%v，期望 {type:disabled}", body2["thinking"])
	}
	if _, ok := effortOf(body2); ok {
		t.Fatalf("第二次请求不应发送 reasoning_effort: %v", body2["reasoning_effort"])
	}
}

// TestNextEffort_ThinkingEnabled 发送矩阵 enabled 分支不变：thinking 显式开启时
// 临时层升档照常写入 reasoning_effort（叠加不替换全局 thinking 状态）。
func TestNextEffort_ThinkingEnabled(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("low")
	d.SetThinkingEnabled(true)
	d.SetNextEffort("max")

	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "enabled" {
		t.Fatalf("thinking=%v，期望 {type:enabled}", body["thinking"])
	}
	if got, _ := effortOf(body); got != "max" {
		t.Fatalf("reasoning_effort=%v，期望 max（临时层生效）", body["reasoning_effort"])
	}
}

// TestSetNextEffort_EmptyClears 空值写入语义：清除临时层、请求回到全局档位
// （与不调用 SetNextEffort 语义等同）。
func TestSetNextEffort_EmptyClears(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("low")
	d.SetNextEffort("max")
	d.SetNextEffort("") // 清除

	if got := d.currentEffort(); got != "low" {
		t.Fatalf("清空临时层后 currentEffort=%q，期望全局 low", got)
	}
	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if got, _ := effortOf(body); got != "low" {
		t.Fatalf("reasoning_effort=%v，期望 low（空临时层不生效）", body["reasoning_effort"])
	}
}

// TestNextEffort_SendEffortFalseChannel 发送矩阵规则 1 不变：OpenAI 兼容通道
// （sendEffort=false）不发 thinking / reasoning_effort；临时层照常被消费
// （一次性语义——不在通道后续重开时泄漏到未来请求）。
func TestNextEffort_SendEffortFalseChannel(t *testing.T) {
	ts, cap := newCaptureChatServer(t)
	d := NewOpenAICompatible(ts.URL, "k", "m", 131072)
	d.SetReasoningEffort("low")
	d.SetNextEffort("max")

	chatOnce(t, d, []types.Message{{Role: "user", Content: "hi"}})
	body := cap.get()
	if _, ok := body["thinking"]; ok {
		t.Fatalf("sendEffort=false 通道不应发送 thinking: %v", body["thinking"])
	}
	if _, ok := effortOf(body); ok {
		t.Fatalf("sendEffort=false 通道不应发送 reasoning_effort: %v", body["reasoning_effort"])
	}
	if got := d.currentEffort(); got != "low" {
		t.Fatalf("临时层应被消费：currentEffort=%q，期望全局 low", got)
	}
}

// TestNextEffort_ConcurrentSafety 并发安全冒烟：SetNextEffort 与请求构造
// （applyThinkingParams）并发执行，配合 go test -race 验证 genMu 下无数据竞争。
// 说明：并发 SetNextEffort 与在途请求的"谁消费临时层"属调用方约定场景，
// 本测试只断言无数据竞争、字段取值合法。
func TestNextEffort_ConcurrentSafety(t *testing.T) {
	d := NewDeepSeekBackend("http://127.0.0.1:1", "k", "m", 4096)
	d.SetReasoningEffort("low")
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				d.SetNextEffort("max")
				d.SetNextEffort("")
				d.SetThinkingEnabled(i%2 == 0)
			}
		}()
	}
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				d.applyThinkingParams(map[string]any{})
				_ = d.currentEffort()
				_ = d.ReasoningEffort()
				_ = d.ThinkingEnabled()
			}
		}()
	}
	wg.Wait()

	// 收尾：清空临时层后状态确定可读（全局档位在并发期未被改写）。
	d.SetNextEffort("")
	if got := d.currentEffort(); got != "low" {
		t.Fatalf("清空临时层后 currentEffort=%q，期望全局 low", got)
	}
}
