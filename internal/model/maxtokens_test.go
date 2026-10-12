package model

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestMaxTokens_Bounds 验证单次输出上限的计算边界：
//   - 取 maxContext（不设人为上限，用户配多大就发多大）
//   - 不设上限钳制——服务端 400 自修复会降级重试
//   - 下限 1（API 规范要求 ≥1）
//   - 且**必须**返回正数——若请求体不发送 max_tokens，DeepSeek 服务端默认
//     仅 4096，长思考时更易触发"输出被截断"（历史回归：曾因彻底移除
//     max_tokens 字段导致截断频发）。
func TestMaxTokens_Bounds(t *testing.T) {
	cases := []struct {
		name       string
		maxContext int
		want       int
	}{
		{"默认131072上下文", 131072, 131072}, // maxContext 直接透传
		{"标准65536上下文", 65536, 65536},    // maxContext 直接透传
		{"小16384上下文", 16384, 16384},     // maxContext 直接透传
		{"极小上下文4096", 4096, 4096},       // maxContext 直接透传
		{"超大上下文512K", 524288, 524288},   // 不钳制！客户端全量直发，服务端若拒绝由 400 自修复降级
		{"1M上下文", 1048576, 1048576},     // 不钳制！用户配 1M 就发 1M
		{"零上下文", 0, 1},                  // 保底 1
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDeepSeekBackend("http://x", "k", "m", tc.maxContext)
			got := d.maxTokens()
			if got != tc.want {
				t.Fatalf("maxContext=%d → maxTokens()=%d, 期望 %d", tc.maxContext, got, tc.want)
			}
			if got <= 0 {
				t.Fatalf("maxTokens() 必须为正数, got %d", got)
			}
		})
	}
}

// TestChatRequest_ContainsMaxTokens 验证 Chat 请求体包含 max_tokens 字段
// （而非依赖服务端默认 4096）——拦截请求体 JSON 断言字段存在且值合理。
// 防止回归：曾因"彻底移除 max_tokens"导致长任务频繁触发"输出被截断"。
func TestChatRequest_ContainsMaxTokens(t *testing.T) {
	var captured map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)
		captured = reqBody
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	_, _, err := d.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("Chat 不应报错: %v", err)
	}
	if captured == nil {
		t.Fatal("请求未被捕获")
	}
	mt, ok := captured["max_tokens"].(float64)
	if !ok {
		t.Fatalf("请求体缺少 max_tokens 字段（回归：依赖服务端默认上限会截断）: %v", captured)
	}
	if int(mt) != 131072 {
		t.Fatalf("max_tokens=%v, 期望 = maxContext=131072（用满上下文窗口）", mt)
	}
}

// TestMaxTokens_400Fallback 验证 1M 上下文直发 max_tokens=1048576 →
// 服务端 400 拒绝 → 自动钳制到 393216 重试 → 成功（请求不丢）。
func TestMaxTokens_400Fallback(t *testing.T) {
	var callCount int
	var capturedMaxTokens float64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)
		callCount++
		mt, _ := reqBody["max_tokens"].(float64)
		capturedMaxTokens = mt
		if mt > 393216 {
			// 第一次请求：max_tokens 超官方上限 → 返回 400
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"max_tokens超出上限","type":"invalid_request_error"}}`)
			return
		}
		// 第二次请求：已钳制 → 返回成功
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "k", "m", 1048576) // 1M 上下文
	_, _, err := d.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("预期 400 自动降级后成功, got err: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("预期 2 次请求（1 次 400 + 1 次降级重试）, got %d", callCount)
	}
	if capturedMaxTokens != 393216 {
		t.Fatalf("降级后 max_tokens 应为 393216, got %v", capturedMaxTokens)
	}
}

// TestMaxTokens_400Fallback_GLM 回归：glm-5.3-flash 上限 131072（错误信息
// "限制数值范围[1,131072]"），1M 上下文直发 1048576 → 400 → 解析错误文本
// 中的合法上限 131072 → 钳制重发 → 成功。此前硬编码钳到 393216 仍超限，
// 二次 400 直接报错（用户实测报错）。
func TestMaxTokens_400Fallback_GLM(t *testing.T) {
	var callCount int
	var capturedMaxTokens float64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)
		callCount++
		mt, _ := reqBody["max_tokens"].(float64)
		capturedMaxTokens = mt
		if mt > 131072 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"The max_tokens parameter is illegal.：限制数值范围[1,131072]","type":"invalid_request_error"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "k", "m", 1048576) // 1M 上下文
	_, _, err := d.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("预期按错误文本解析上限 131072 降级后成功, got err: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("预期 2 次请求（1 次 400 + 1 次钳制重试）, got %d", callCount)
	}
	if capturedMaxTokens != 131072 {
		t.Fatalf("钳制后 max_tokens 应为错误文本声明的 131072, got %v", capturedMaxTokens)
	}
}

// TestMaxTokens_400Fallback_StepLadder 验证错误文本不含显式上限时，按已知
// 阶梯逐级下调：1M → 384K 仍超限 → 128K 成功（多级 400 连续降级）。
func TestMaxTokens_400Fallback_StepLadder(t *testing.T) {
	var callCount int
	var captured []float64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)
		callCount++
		mt, _ := reqBody["max_tokens"].(float64)
		captured = append(captured, mt)
		if mt > 131072 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"max_tokens too large","type":"invalid_request_error"}}`)
			return
		}
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "k", "m", 1048576) // 1M 上下文
	_, _, err := d.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatalf("预期阶梯降级后成功, got err: %v", err)
	}
	// 1M → 384K(393216) → 128K(131072) 成功
	if callCount != 3 {
		t.Fatalf("预期 3 次请求（400→400→成功）, got %d", callCount)
	}
	if len(captured) != 3 || captured[0] != 1048576 || captured[1] != 393216 || captured[2] != 131072 {
		t.Fatalf("阶梯钳制序列异常: %v", captured)
	}
}

// TestParseMaxTokensLimit 验证从 400 错误文本解析服务端上限的两种形态：
// 范围 [1,N]（glm）与 <= N（OpenAI 风格）。
func TestParseMaxTokensLimit(t *testing.T) {
	cases := []struct {
		errText string
		want    int
	}{
		{"The max_tokens parameter is illegal.：限制数值范围[1,131072]", 131072},
		{"max_tokens must be <= 131072", 131072},
		{"max_tokens must be less than 393216", 393216},
		{"参数不合法[1, 65536]", 65536},
		{"generic error without limit", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := parseMaxTokensLimit(tc.errText); got != tc.want {
			t.Errorf("parseMaxTokensLimit(%q)=%d, 期望 %d", tc.errText, got, tc.want)
		}
	}
}

// TestNextMaxTokensCap 验证阶梯降级：当前值在阶梯内时返回下一档，否则 0。
func TestNextMaxTokensCap(t *testing.T) {
	cases := []struct {
		cur  int
		want int
	}{
		{1048576, 393216}, // 1M → 384K
		{393216, 131072},  // 384K → 128K
		{131072, 65536},   // 128K → 64K
		{65536, 0},        // 已是最低档
		{1000, 0},         // 低于所有档
	}
	for _, tc := range cases {
		if got := nextMaxTokensCap(tc.cur); got != tc.want {
			t.Errorf("nextMaxTokensCap(%d)=%d, 期望 %d", tc.cur, got, tc.want)
		}
	}
}

// TestStreamEx_400Fallback_GLM 流式路径回归：glm-5.3-flash 上限 131072，
// 1M 上下文直发 1048576 → 400（错误文本含 [1,131072]）→ 钳制到 131072
// 重发 → SSE 正常返回。GUI 主链路走 StreamEx，必须同样自修复。
func TestStreamEx_400Fallback_GLM(t *testing.T) {
	var callCount int
	var capturedMaxTokens float64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		json.NewDecoder(r.Body).Decode(&reqBody)
		callCount++
		mt, _ := reqBody["max_tokens"].(float64)
		capturedMaxTokens = mt
		if mt > 131072 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"The max_tokens parameter is illegal.：限制数值范围[1,131072]","type":"invalid_request_error"}}`)
			return
		}
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"},"index":0}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "k", "m", 1048576) // 1M 上下文
	ch, errCh := d.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "hi"}})
	var got string
	for chunk := range ch {
		got += chunk.Content
	}
	if err := <-errCh; err != nil {
		t.Fatalf("预期 400 按错误上限降级后流式成功, got err: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("预期 2 次请求（1 次 400 + 1 次钳制重试）, got %d", callCount)
	}
	if capturedMaxTokens != 131072 {
		t.Fatalf("钳制后 max_tokens 应为 131072, got %v", capturedMaxTokens)
	}
	if got != "ok" {
		t.Fatalf("流式内容异常: %q", got)
	}
}
