package modelgateway

// handle 独立生成参数透传单测：验证 SetMaxOutputTokens/SetMaxInputTokens/
// SetThinkingEnabled 的缓存 + applyParams 下发链路，以及探针回退语义。

import (
	"context"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// paramBackend 在 mockBackend 基础上实现可选的 model.GenerationParamsBackend，
// 记录 handle 透传下发的独立生成参数（输出/输入上限与思考开关）。
type paramBackend struct {
	mockBackend
	pmu      sync.Mutex
	maxOut   int
	maxIn    int
	thinking bool
}

func (p *paramBackend) SetMaxOutputTokens(n int) {
	p.pmu.Lock()
	p.maxOut = n
	p.pmu.Unlock()
}

func (p *paramBackend) MaxOutputTokens() int {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return p.maxOut
}

func (p *paramBackend) SetMaxInputTokens(n int) {
	p.pmu.Lock()
	p.maxIn = n
	p.pmu.Unlock()
}

func (p *paramBackend) MaxInputTokens() int {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return p.maxIn
}

func (p *paramBackend) SetThinkingEnabled(on bool) {
	p.pmu.Lock()
	p.thinking = on
	p.pmu.Unlock()
}

func (p *paramBackend) ThinkingEnabled() bool {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return p.thinking
}

// TestHandleGenerationParamsPassthrough handle 缓存的独立参数在 applyParams
// 阶段下发到池内实例（与 temp/topP/effort 同模式）。
func TestHandleGenerationParamsPassthrough(t *testing.T) {
	pb := &paramBackend{mockBackend: mockBackend{name: "param"}}
	g := mkPool(t, "default", pb)()
	h := selectMust(t, g, Req{})

	h.SetMaxOutputTokens(65536)
	h.SetMaxInputTokens(4096)
	h.SetThinkingEnabled(true)

	// getter：有缓存返回缓存
	if got := h.MaxOutputTokens(); got != 65536 {
		t.Fatalf("MaxOutputTokens()=%d, 期望 65536", got)
	}
	if got := h.MaxInputTokens(); got != 4096 {
		t.Fatalf("MaxInputTokens()=%d, 期望 4096", got)
	}
	if !h.ThinkingEnabled() {
		t.Fatal("ThinkingEnabled() 应为 true")
	}
	// 未触发请求前，实例尚未收到下发
	if pb.MaxOutputTokens() != 0 || pb.MaxInputTokens() != 0 {
		t.Fatalf("applyParams 前实例不应收到参数: out=%d in=%d",
			pb.MaxOutputTokens(), pb.MaxInputTokens())
	}

	// Chat 触发 applyParams → 下发到池内实例
	if _, _, err := h.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if pb.MaxOutputTokens() != 65536 || pb.MaxInputTokens() != 4096 || !pb.ThinkingEnabled() {
		t.Fatalf("实例未收到下发: out=%d in=%d thinking=%v",
			pb.MaxOutputTokens(), pb.MaxInputTokens(), pb.ThinkingEnabled())
	}

	// 关闭思考再触发一次：false 必须下发（不能因零值被跳过）
	h.SetThinkingEnabled(false)
	if _, _, err := h.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if pb.ThinkingEnabled() {
		t.Fatal("thinking=false 应下发到实例")
	}
}

// TestHandleGenerationParamsProbeFallback 未设置缓存时 getter 探针底层实例；
// 底层不支持可选接口时返回零值、不 panic（applyParams 静默跳过）。
func TestHandleGenerationParamsProbeFallback(t *testing.T) {
	pb := &paramBackend{mockBackend: mockBackend{name: "param"}}
	g := mkPool(t, "default", pb)()
	h := selectMust(t, g, Req{})
	pb.SetMaxOutputTokens(7777) // 直接改实例（模拟实例默认/外部设置）
	if got := h.MaxOutputTokens(); got != 7777 {
		t.Fatalf("未设置缓存时应探针底层实例, got %d", got)
	}

	// 底层不支持 GenerationParamsBackend：零值返回，不 panic
	mb := &mockBackend{name: "plain"}
	g2 := mkPool(t, "default", mb)()
	h2 := selectMust(t, g2, Req{})
	if h2.MaxOutputTokens() != 0 || h2.MaxInputTokens() != 0 || h2.ThinkingEnabled() {
		t.Fatal("底层不支持时 getter 应返回零值")
	}
	h2.SetMaxOutputTokens(1024) // 缓存可用；下发时静默跳过（不 panic）
	if _, _, err := h2.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if h2.MaxOutputTokens() != 1024 {
		t.Fatal("缓存值应可读回")
	}
}
