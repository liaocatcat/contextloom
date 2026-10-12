package modelgateway

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/pkg/types"
	"gopkg.in/yaml.v3"
)

// ── mock 后端 ──

// mockBackend 可编程后端：按调用顺序返回预设结果（成功/失败），
// 记录调用次数与时间戳，支持阻塞（并发限流测试，entered 原子计数观测）。
type mockBackend struct {
	mu       sync.Mutex
	name     string
	failures []error // 按序返回；耗尽后成功
	calls    int
	times    []time.Time
	block    chan struct{} // 非空时：Chat 进入即阻塞，直到关闭或 ctx 取消
	entered  atomic.Int64  // 进入阻塞的调用数（并发限流观测）
}

func (m *mockBackend) Chat(ctx context.Context, _ []types.Message, _ []types.ToolDefinition) (string, []types.ToolCall, error) {
	m.mu.Lock()
	idx := m.calls
	m.calls++
	m.times = append(m.times, time.Now())
	if m.block != nil {
		m.mu.Unlock()
		m.entered.Add(1)
		select {
		case <-ctx.Done():
			return "", nil, ctx.Err()
		case <-m.block:
		}
		return "ok-" + m.name, nil, nil
	}
	var err error
	if idx < len(m.failures) {
		err = m.failures[idx]
	}
	m.mu.Unlock()
	if err != nil {
		return "", nil, err
	}
	return "ok-" + m.name, nil, nil
}

func (m *mockBackend) Stream(context.Context, []types.Message) (<-chan string, <-chan error) {
	out := make(chan string)
	errC := make(chan error)
	close(out)
	close(errC)
	return out, errC
}
func (m *mockBackend) ModelID() string           { return m.name }
func (m *mockBackend) MaxContext() int           { return 8000 }
func (m *mockBackend) SupportsVision() bool      { return false }
func (m *mockBackend) SetReasoningEffort(string) {}
func (m *mockBackend) ReasoningEffort() string   { return "" }
func (m *mockBackend) SetTopP(float64)           {}
func (m *mockBackend) TopP() float64             { return 0 }
func (m *mockBackend) SetTemperature(float64)    {}
func (m *mockBackend) Temperature() float64      { return 0 }

func (m *mockBackend) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

var err429 = errors.New("429 too many requests: rate exceeded")

func mkPool(t *testing.T, name string, b ...model.Backend) func() *Gateway {
	t.Helper()
	instances := make([]model.Backend, len(b))
	for i, x := range b {
		instances[i] = x
	}
	return func() *Gateway {
		g, err := New(GatewayConfig{
			Default: "default",
			Pools: map[string]PoolConfig{
				name: {Instances: instances, MaxConcurrent: 4, FailThreshold: 3, Retries: 3, RetryBase: Duration{time.Millisecond}, RetryFactor: 2},
			},
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		return g
	}
}

// 1. 规则匹配优先级与交集谓词
func TestRoutePriorityAndIntersection(t *testing.T) {
	a := &mockBackend{name: "poolA"}
	b := &mockBackend{name: "poolB"}
	c := &mockBackend{name: "poolC"}
	g, err := New(GatewayConfig{
		Default: "default",
		Rules: []RuleConfig{
			{When: WhenConfig{Role: "coder"}, Use: "A"},
			{When: WhenConfig{Task: "vision"}, Use: "B"},
			{When: WhenConfig{Role: "coder", Task: "vision"}, Use: "C"}, // 交集，但排最后不生效
		},
		Pools: map[string]PoolConfig{
			"A": {Instances: []model.Backend{a}},
			"B": {Instances: []model.Backend{b}},
			"C": {Instances: []model.Backend{c}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// role=coder 命中第一条 → A
	if h := selectMust(t, g, Req{Role: "coder"}); h.PoolName() != "A" {
		t.Fatalf("role=coder 应路由到 A，得 %s", h.PoolName())
	}
	// task=vision 命中第二条 → B
	if h := selectMust(t, g, Req{Task: "vision"}); h.PoolName() != "B" {
		t.Fatalf("task=vision 应路由到 B，得 %s", h.PoolName())
	}
	// 交集规则排最后，被第一条优先拦截 → 仍 A
	if h := selectMust(t, g, Req{Role: "coder", Task: "vision"}); h.PoolName() != "A" {
		t.Fatalf("交集但优先级靠后，应仍命中 A，得 %s", h.PoolName())
	}
}

// 规则按顺序优先：把交集规则放最前 → 命中 C
func TestRouteIntersectionFirst(t *testing.T) {
	c := &mockBackend{name: "C"}
	g, err := New(GatewayConfig{
		Default: "default",
		Rules: []RuleConfig{
			{When: WhenConfig{Role: "coder", Task: "vision"}, Use: "C"},
			{When: WhenConfig{Role: "coder"}, Use: "A"},
		},
		Pools: map[string]PoolConfig{
			"C": {Instances: []model.Backend{c}},
			"A": {Instances: []model.Backend{&mockBackend{name: "A"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if h := selectMust(t, g, Req{Role: "coder", Task: "vision"}); h.PoolName() != "C" {
		t.Fatalf("交集规则应命中 C，得 %s", h.PoolName())
	}
}

// 2. 池内轮询
func TestPoolRoundRobin(t *testing.T) {
	a := &mockBackend{name: "a"}
	b := &mockBackend{name: "b"}
	g, _ := New(GatewayConfig{
		Default: "default",
		Pools: map[string]PoolConfig{
			"default": {Instances: []model.Backend{a, b}, Retries: 1},
		},
	})
	h := selectMust(t, g, Req{})
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		text, _, err := h.Chat(ctx, nil, nil)
		if err != nil {
			t.Fatalf("chat %d: %v", i, err)
		}
		wantA := i%2 == 0
		if wantA && text != "ok-a" {
			t.Fatalf("第 %d 次应轮到 a，得 %s", i, text)
		}
		if !wantA && text != "ok-b" {
			t.Fatalf("第 %d 次应轮到 b，得 %s", i, text)
		}
	}
	if a.callCount() != 2 || b.callCount() != 2 {
		t.Fatalf("轮询应各调 2 次，得 a=%d b=%d", a.callCount(), b.callCount())
	}
}

// 3. 限流信号量：并发数不超 max_concurrent
func TestSemaphoreLimit(t *testing.T) {
	const mc = 2
	bs := make([]*mockBackend, 4)
	instances := make([]model.Backend, 4)
	for i := range bs {
		bs[i] = &mockBackend{name: fmt.Sprintf("b%d", i), block: make(chan struct{})}
		instances[i] = bs[i]
	}
	sumEntered := func() int64 {
		var n int64
		for _, b := range bs {
			n += b.entered.Load()
		}
		return n
	}
	g, _ := New(GatewayConfig{
		Default: "default",
		Pools: map[string]PoolConfig{
			"default": {Instances: instances, MaxConcurrent: mc, Retries: 1, AcquireTimeout: Duration{3 * time.Second}},
		},
	})
	h := selectMust(t, g, Req{})

	var wg sync.WaitGroup
	var done atomic.Int64
	for i := 0; i < 6; i++ { // 6 个并发请求，限流 2
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := h.Chat(context.Background(), nil, nil)
			if err == nil {
				done.Add(1)
			}
		}()
	}
	// 等待恰好 mc 个进入阻塞
	deadline := time.Now().Add(3 * time.Second)
	for sumEntered() < mc {
		if time.Now().After(deadline) {
			t.Fatalf("只进入 %d 个，预期 %d", sumEntered(), mc)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// 观察 100ms：不得超载（第 3 个被信号量挡在 acquire）
	time.Sleep(100 * time.Millisecond)
	if n := sumEntered(); n != mc {
		t.Fatalf("信号量应限流在 %d，实际进入 %d", mc, n)
	}
	// 放行 → 全部完成
	for _, b := range bs {
		close(b.block)
	}
	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(10 * time.Second):
		t.Fatal("放行后请求未全部完成")
	}
	if done.Load() != 6 {
		t.Fatalf("应全部成功 6 个，得 %d", done.Load())
	}
}

// 4. 连续失败熔断 + 整池降级 + Degraded 标记 + 回调
func TestCircuitBreakerAndDegrade(t *testing.T) {
	fail := &mockBackend{name: "flaky", failures: []error{err429, err429, err429}}
	g, _ := New(GatewayConfig{
		Default: "default",
		Rules:   []RuleConfig{{When: WhenConfig{Role: "coder"}, Use: "pro"}},
		Pools: map[string]PoolConfig{
			"pro":     {Instances: []model.Backend{fail}, FailThreshold: 2, Retries: 1},
			"default": {Instances: []model.Backend{&mockBackend{name: "cheap"}}, Retries: 1},
		},
	})
	var notices []string
	g.SetOnDegrade(func(s string) { notices = append(notices, s) })

	ctx := context.Background()
	// 第一次：pro 失败 1 次
	if _, err := g.Select(ctx, Req{Role: "coder"}); err != nil {
		t.Fatal(err)
	}
	h1 := selectMust(t, g, Req{Role: "coder"})
	if _, _, err := h1.Chat(ctx, nil, nil); err == nil {
		t.Fatal("第 1 次应失败")
	}
	// 第二次：pro 失败达阈值 2 → 熔断
	h2 := selectMust(t, g, Req{Role: "coder"})
	if _, _, err := h2.Chat(ctx, nil, nil); err == nil {
		t.Fatal("第 2 次应失败")
	}
	// 第三次：pro 已熔断 → 降级到 default
	h3 := selectMust(t, g, Req{Role: "coder"})
	if !h3.Degraded() {
		t.Fatal("pro 熔断后应降级，Degraded 应 true")
	}
	if h3.PoolName() != "default" {
		t.Fatalf("降级后应绑定 default 池，得 %s", h3.PoolName())
	}
	if h3.Notice() == "" {
		t.Fatal("降级应携带 notice")
	}
	text, _, err := h3.Chat(ctx, nil, nil)
	if err != nil || text != "ok-cheap" {
		t.Fatalf("降级后应成功走 cheap，得 text=%q err=%v", text, err)
	}
	if len(notices) == 0 {
		t.Fatal("应触发降级回调")
	}
}

// 5. 重试退避对可重试错误生效
func TestRetryBackoff(t *testing.T) {
	m := &mockBackend{name: "retry", failures: []error{err429, err429}} // 前两次失败，第三次成功
	g, _ := New(GatewayConfig{
		Default: "default",
		Pools: map[string]PoolConfig{
			"default": {Instances: []model.Backend{m}, Retries: 3, RetryBase: Duration{10 * time.Millisecond}, RetryFactor: 2, FailThreshold: 10},
		},
	})
	h := selectMust(t, g, Req{})
	start := time.Now()
	text, _, err := h.Chat(context.Background(), nil, nil)
	elapsed := time.Since(start)
	if err != nil || text != "ok-retry" {
		t.Fatalf("应重试成功，得 text=%q err=%v", text, err)
	}
	if m.callCount() != 3 {
		t.Fatalf("应调用 3 次（1 首试 + 2 重试），得 %d", m.callCount())
	}
	// 退避 10ms + 20ms ≈ 30ms
	if elapsed < 20*time.Millisecond {
		t.Fatalf("退避间隔应 ≥20ms，实际 %v", elapsed)
	}
}

// 6. 未命中规则走 default（且文字"default"何时命中的示例）
func TestDefaultPoolFallthrough(t *testing.T) {
	cheap := &mockBackend{name: "cheap"}
	g, _ := New(GatewayConfig{
		Default: "cheap",
		Rules:   []RuleConfig{{When: WhenConfig{Role: "coder"}, Use: "pro"}},
		Pools: map[string]PoolConfig{
			"pro":   {Instances: []model.Backend{&mockBackend{name: "pro"}}},
			"cheap": {Instances: []model.Backend{cheap}, Retries: 1},
		},
	})
	h := selectMust(t, g, Req{Role: "writer"}) // 未命中 → default=cheap
	if h.PoolName() != "cheap" {
		t.Fatalf("未命中应走 default=cheap，得 %s", h.PoolName())
	}
}

// 7. yaml 反序列化往返
func TestYAMLRoundTrip(t *testing.T) {
	y := `
default: cheap
rules:
  - when: "role=coder"
    use: pro
  - when: "budget<=low"
    use: cheap
  - when: default
    use: cheap
pools:
  pro:
    backend: deepseek
    max_concurrent: 3
    retry_base: 100ms
  cheap:
    keys: [sk-a, sk-b]
    max_concurrent: 4
`
	var cfg GatewayConfig
	if err := yaml.Unmarshal([]byte(y), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Default != "cheap" {
		t.Fatalf("default 应 cheap，得 %q", cfg.Default)
	}
	if len(cfg.Rules) != 3 {
		t.Fatalf("应 3 条规则，得 %d", len(cfg.Rules))
	}
	if !cfg.Rules[0].When.match(Req{Role: "coder"}) {
		t.Fatal("rule0 应匹配 role=coder")
	}
	if !cfg.Rules[1].When.budgetLE {
		t.Fatal("budget<=low 应记录 budgetLE")
	}
	if !cfg.Rules[1].When.match(Req{Budget: "low"}) {
		t.Fatal("budget<=low 应匹配 low")
	}
	if !cfg.Rules[1].When.match(Req{Budget: ""}) {
		t.Fatal("budget<=low 应匹配空预算（最低档）")
	}
	if cfg.Rules[1].When.match(Req{Budget: "high"}) {
		t.Fatal("budget<=low 不应匹配 high")
	}
	if !cfg.Rules[2].When.MatchAll {
		t.Fatal("when: default 应解析为 MatchAll")
	}
	if cfg.Pools["pro"].RetryBase.Duration != 100*time.Millisecond {
		t.Fatalf("retry_base 应 100ms，得 %v", cfg.Pools["pro"].RetryBase.Duration)
	}
	if len(cfg.Pools["cheap"].allKeys()) != 2 {
		t.Fatalf("cheap 应 2 个 key，得 %v", cfg.Pools["cheap"].allKeys())
	}

	// 往返：marshal 再 unmarshal，语义等价
	out, err := yaml.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var cfg2 GatewayConfig
	if err := yaml.Unmarshal(out, &cfg2); err != nil {
		t.Fatalf("roundtrip unmarshal: %v", err)
	}
	if cfg2.Default != cfg.Default || len(cfg2.Rules) != len(cfg.Rules) {
		t.Fatalf("roundtrip 不等价: %+v vs %+v", cfg2.Default, cfg.Default)
	}
	if !cfg2.Rules[1].When.budgetLE {
		t.Fatal("roundtrip 后 budgetLE 丢失")
	}
}

// 8. fallback 后端：未配 default 池（空池）→ 全部路由降级到注入后端
func TestFallbackBackend(t *testing.T) {
	fallback := &mockBackend{name: "fb"}
	// 场景 A：无 fallback、default 空池 → Select 报错（池全灭）
	g0, _ := New(GatewayConfig{Default: "default"})
	if _, err := g0.Select(context.Background(), Req{}); err == nil {
		t.Fatal("default 空池且无 fallback 时 Select 应报错")
	}
	// 场景 B：有 fallback → 未配置 default 池时，fallback 即为 default 池实例
	//（旧路径零改动语义：无降级标记）
	g, _ := New(GatewayConfig{Default: "default"}, fallback)
	h := selectMust(t, g, Req{})
	if h.Degraded() {
		t.Fatal("fallback 作为 default 池实例时不应标记降级")
	}
	text, _, err := h.Chat(context.Background(), nil, nil)
	if err != nil || text != "ok-fb" {
		t.Fatalf("应走 fallback，得 text=%q err=%v", text, err)
	}
	// 场景 C：显式配置了 default 池，注入 fallback 仅作最后兜底——
	// default 池熔断后才降级到 fallback（Degraded=true）
	fb2 := &mockBackend{name: "fb2"}
	g2, _ := New(GatewayConfig{
		Default: "default",
		Pools: map[string]PoolConfig{
			"default": {
				Instances:     []model.Backend{&mockBackend{name: "d", failures: []error{err429, err429}}},
				FailThreshold: 2,
				Retries:       1,
			},
		},
	}, fb2)
	// 打熔断 default 池（failThreshold=2）
	h2 := selectMust(t, g2, Req{})
	for i := 0; i < 2; i++ {
		if _, _, err := h2.Chat(context.Background(), nil, nil); err == nil {
			t.Fatalf("第 %d 次应失败", i+1)
		}
	}
	// default 熔断 → 降级 fallback
	h3 := selectMust(t, g2, Req{})
	if !h3.Degraded() {
		t.Fatal("default 池熔断后应降级到 fallback，Degraded 应 true")
	}
	if text, _, err := h3.Chat(context.Background(), nil, nil); err != nil || text != "ok-fb2" {
		t.Fatalf("应走 fallback fb2，得 text=%q err=%v", text, err)
	}
}

// 9. 不可重试错误：不触发熔断、直接上抛
func TestNonRetryableNoTrip(t *testing.T) {
	bad := &mockBackend{name: "bad", failures: []error{errors.New("invalid model id: 400 bad request")}}
	g, _ := New(GatewayConfig{
		Default: "default",
		Pools: map[string]PoolConfig{
			"default": {Instances: []model.Backend{bad}, FailThreshold: 2, Retries: 3},
		},
	})
	h := selectMust(t, g, Req{})
	if _, _, err := h.Chat(context.Background(), nil, nil); err == nil {
		t.Fatal("应失败")
	}
	// 不可重试错误不应触发熔断（池仍健康）
	st, _ := g.PoolStatus("default")
	if st.Tripped {
		t.Fatal("不可重试错误不应熔断池")
	}
}

// 10. 成功清零：失败 1 次后成功，计数归零，再失败不熔断
func TestSuccessResetsCounter(t *testing.T) {
	m := &mockBackend{name: "m", failures: []error{err429}} // 第 1 次失败，之后成功
	g, _ := New(GatewayConfig{
		Default: "default",
		Pools: map[string]PoolConfig{
			"default": {Instances: []model.Backend{m}, FailThreshold: 2, Retries: 1},
		},
	})
	h := selectMust(t, g, Req{})
	ctx := context.Background()
	if _, _, err := h.Chat(ctx, nil, nil); err == nil {
		t.Fatal("第 1 次应失败")
	}
	if _, _, err := h.Chat(ctx, nil, nil); err != nil {
		t.Fatalf("第 2 次应成功（清零后），err=%v", err)
	}
	st, _ := g.PoolStatus("default")
	if st.Tripped {
		t.Fatal("成功清零后池不应熔断")
	}
}

func selectMust(t *testing.T, g *Gateway, req Req) *handle {
	t.Helper()
	b, err := g.Select(context.Background(), req)
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	h, ok := b.(*handle)
	if !ok {
		t.Fatalf("Select 应返回 *handle，得 %T", b)
	}
	return h
}

// ── 多模态能力注入（网关路由路径回归）──

// visionMockBackend 支持 SetSupportsVision 的 mock：验证池建厂/注入实例后
// 能力标志确实落到实例上（网关路径曾漏注入，导致 supports_vision=true 的
// 模型被误判为纯文本而降级）。
type visionMockBackend struct {
	mockBackend
	supportsVision bool
}

func (v *visionMockBackend) SetSupportsVision(b bool) { v.supportsVision = b }
func (v *visionMockBackend) SupportsVision() bool     { return v.supportsVision }

// TestPoolInjectsSupportsVision_ByKeys 按 keys 建厂路径：池配置
// supports_vision=true 时，建出的实例能力标志必须为 true。
func TestPoolInjectsSupportsVision_ByKeys(t *testing.T) {
	p := buildPool("vis", PoolConfig{
		Backend: "mock", Endpoint: "http://x", Model: "m", APIKey: "k",
		MaxContext: 8000, SupportsVision: true,
	})
	if len(p.instances) == 0 {
		t.Fatal("池应至少建出 1 个实例")
	}
	inner, ok := p.instances[0].inner.(*model.DeepSeekBackend)
	if !ok {
		t.Fatalf("实例应为 *model.DeepSeekBackend，得 %T", p.instances[0].inner)
	}
	if !inner.SupportsVision() {
		t.Fatal("池配置 supports_vision=true 应注入实例，实际 false（网关路径会误降级）")
	}
	// 对照：未声明能力 → 保持默认 false（纯文本模型语义不变）
	p2 := buildPool("txt", PoolConfig{Backend: "mock", Endpoint: "http://x", Model: "m", APIKey: "k"})
	if inner2, ok := p2.instances[0].inner.(*model.DeepSeekBackend); !ok || inner2.SupportsVision() {
		t.Fatal("未声明 supports_vision 应保持 false")
	}
}

// TestPoolInjectsSupportsVision_Instances 预注入实例路径：注入的实例同样
// 按池配置设置能力标志。
func TestPoolInjectsSupportsVision_Instances(t *testing.T) {
	vb := &visionMockBackend{mockBackend: mockBackend{name: "v"}}
	buildPool("vis", PoolConfig{Instances: []model.Backend{vb}, SupportsVision: true})
	if !vb.supportsVision {
		t.Fatal("预注入实例应按池配置注入 supports_vision=true")
	}
}

// TestHandleSupportsVision_ProxiesPool 端到端：网关 Select 出的 handle
// SupportsVision() 必须透传池内实例能力（曾恒为 false）。
func TestHandleSupportsVision_ProxiesPool(t *testing.T) {
	vb := &visionMockBackend{mockBackend: mockBackend{name: "v"}}
	g, err := New(GatewayConfig{
		Default: "default",
		Pools:   map[string]PoolConfig{"default": {Instances: []model.Backend{vb}, SupportsVision: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := selectMust(t, g, Req{Task: "vision"})
	if !h.SupportsVision() {
		t.Fatal("handle.SupportsVision() 应透传池实例的 true（网关路径识图不再误降级）")
	}
}
