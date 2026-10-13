package runtime

// 本次改造（思考长度与输出/输入长度释放）runtime 层测试。
//
// 钉死的不变式：
//  1. 零回归：未配置时 applyGenerationParams 不下发任何参数（后端保持历史行为，
//     请求体 max_tokens == maxContext、不发 thinking 字段——发送矩阵由 model 包测试锁定）；
//  2. 输出与输入解耦：max_output_tokens 影响请求体 max_tokens，但 ctxBudget
//     只取有效输入预算（max_input_tokens 或 max_context）；
//  3. 思考关闭时禁止自相矛盾组合：off 档请求体发 thinking:{type:disabled}
//     且不发 reasoning_effort；切回思考档位后 thinkingEnabled 复位为 nil
//     （请求体不再显式发 thinking）；
//  4. 配置优先：SetEffort("off") 不得永久覆盖预设的 thinking_enabled=true；
//  5. 热更新：applyLiveConfig 后 ctxBudget.MaxTokens == effectiveInputBudget()。
//
// 构造方式：字面量 &Runtime{Config, Backend}——被测方法均为纯方法，
// 不依赖 harness/plugins/memory 等启动期组件（SetEffort 会调 saveSettings，
// 故 Project.Root 指向 t.TempDir() 避免污染源码目录）。
// 后端选择：runtime 包自身的 mockBackend 未实现 GenerationParamsBackend，
// applyGenerationParams 会静默跳过；因此断言"后端收到参数"必须用
// *model.DeepSeekBackend（model.NewBackend("mock",...) 返回它，sendEffort=false）
// 或本文件的 genParamsRecorder（在 mockBackend 之上实现 GenerationParamsBackend）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ────────────────────────────────────────────────────────────────
// 测试基础设施
// ────────────────────────────────────────────────────────────────

// newGenParamsRuntime 构造只含纯方法依赖的最小 Runtime：
// Config（单预设，Project.Root 指向临时目录）+ 指定后端。
func newGenParamsRuntime(t *testing.T, preset config.ModelPreset, b model.Backend) *Runtime {
	t.Helper()
	if preset.Name == "" {
		preset.Name = "test"
	}
	if preset.Backend == "" {
		preset.Backend = "mock"
	}
	if preset.MaxContext == 0 {
		preset.MaxContext = 131072
	}
	cfg := &config.Config{
		ActiveModelName: preset.Name,
		Models:          []config.ModelPreset{preset},
		Project:         config.ProjectConfig{Root: t.TempDir()},
	}
	return &Runtime{Config: cfg, Backend: b}
}

// boolPtr 返回 bool 指针（config.ModelPreset.ThinkingEnabled 为 *bool）。
func boolPtr(v bool) *bool { return &v }

// newMockChannelBackend 返回 OpenAI 兼容通道的真实 DeepSeekBackend
// （sendEffort=false；GenerationParamsBackend 全部方法可用、getter 可断言）。
func newMockChannelBackend(t *testing.T) *model.DeepSeekBackend {
	t.Helper()
	b, err := model.NewBackend("mock", "http://127.0.0.1:1/v1", "", "m", 131072, false)
	if err != nil {
		t.Fatalf("NewBackend(mock) 失败: %v", err)
	}
	ds, ok := b.(*model.DeepSeekBackend)
	if !ok {
		t.Fatalf("mock 通道应返回 *model.DeepSeekBackend，got %T", b)
	}
	return ds
}

// genParamsRecorder 在既有 mockBackend 之上实现 GenerationParamsBackend，
// 精确记录每次 setter 调用。必要性：DeepSeekBackend.ThinkingEnabled() 对
// "nil（未显式设置）"与"显式 false"都返回 false，无法区分两种语义；
// 调用记录能精确表达"未下发（不显式发送 thinking 字段）"。
type genParamsRecorder struct {
	*mockBackend
	thinkingCalls []bool
	maxOutCalls   []int
	maxInCalls    []int
}

func (g *genParamsRecorder) SetThinkingEnabled(on bool) {
	g.thinkingCalls = append(g.thinkingCalls, on)
}
func (g *genParamsRecorder) ThinkingEnabled() bool {
	return len(g.thinkingCalls) > 0 && g.thinkingCalls[len(g.thinkingCalls)-1]
}
func (g *genParamsRecorder) SetMaxOutputTokens(n int) { g.maxOutCalls = append(g.maxOutCalls, n) }
func (g *genParamsRecorder) MaxOutputTokens() int {
	if len(g.maxOutCalls) == 0 {
		return 0
	}
	return g.maxOutCalls[len(g.maxOutCalls)-1]
}
func (g *genParamsRecorder) SetMaxInputTokens(n int) { g.maxInCalls = append(g.maxInCalls, n) }
func (g *genParamsRecorder) MaxInputTokens() int {
	if len(g.maxInCalls) == 0 {
		return 0
	}
	return g.maxInCalls[len(g.maxInCalls)-1]
}

// bodyCapture 线程安全保存最近一次请求体（httptest handler 与测试主协程并发）。
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

// newEffortCaptureServer 启动捕获请求体的 Chat 服务器（固定返回成功响应）。
func newEffortCaptureServer(t *testing.T) (*httptest.Server, *bodyCapture) {
	t.Helper()
	cap := &bodyCapture{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		cap.set(body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(ts.Close)
	return ts, cap
}

// chatOnce 向后端发一次非流式请求（验证请求体发送矩阵）。
func chatOnce(t *testing.T, d *model.DeepSeekBackend) {
	t.Helper()
	if _, _, err := d.Chat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil); err != nil {
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

// ────────────────────────────────────────────────────────────────
// effectiveInputBudget：有效输入预算 = max_input_tokens 优先，回退 max_context
// ────────────────────────────────────────────────────────────────

func TestEffectiveInputBudget(t *testing.T) {
	// 未配置 max_input_tokens（0）→ 回退激活预设的 max_context
	rt := newGenParamsRuntime(t, config.ModelPreset{MaxContext: 131072}, nil)
	if got := rt.effectiveInputBudget(); got != 131072 {
		t.Fatalf("未配置 max_input_tokens 时应回退 max_context=131072，got %d", got)
	}

	// 显式配置 max_input_tokens → 以它为有效输入预算（与 max_context 解耦）
	rt2 := newGenParamsRuntime(t, config.ModelPreset{MaxContext: 131072, MaxInputTokens: 65536}, nil)
	if got := rt2.effectiveInputBudget(); got != 65536 {
		t.Fatalf("配置 max_input_tokens=65536 时应返回 65536，got %d", got)
	}

	// 边界：max_input_tokens=1 是合法最小值（>0 即生效）
	rt3 := newGenParamsRuntime(t, config.ModelPreset{MaxContext: 131072, MaxInputTokens: 1}, nil)
	if got := rt3.effectiveInputBudget(); got != 1 {
		t.Fatalf("max_input_tokens=1 应生效，got %d", got)
	}

	// 边界：max_input_tokens 为负（异常输入）→ 视为未设置，回退 max_context
	rt4 := newGenParamsRuntime(t, config.ModelPreset{MaxContext: 131072, MaxInputTokens: -5}, nil)
	if got := rt4.effectiveInputBudget(); got != 131072 {
		t.Fatalf("max_input_tokens<0 应回退 max_context=131072，got %d", got)
	}
}

// ────────────────────────────────────────────────────────────────
// applyGenerationParams：thinking 解析规则（显式值优先 → ThinkingLength → nil）
// ────────────────────────────────────────────────────────────────

func TestApplyGenerationParams_ConfigPriority(t *testing.T) {
	cases := []struct {
		name      string
		preset    config.ModelPreset
		wantCalls int  // SetThinkingEnabled 调用次数
		wantVal   bool // 最后一次调用值
	}{
		{
			name:      "显式 false 优先（ThinkingLength 为空）",
			preset:    config.ModelPreset{ThinkingEnabled: boolPtr(false)},
			wantCalls: 1,
			wantVal:   false,
		},
		{
			name:      "ThinkingLength=off 关闭思考",
			preset:    config.ModelPreset{ThinkingLength: "off"},
			wantCalls: 1,
			wantVal:   false,
		},
		{
			name:      "ThinkingLength=high 开启思考",
			preset:    config.ModelPreset{ThinkingLength: "high"},
			wantCalls: 1,
			wantVal:   true,
		},
		{
			name:      "两者皆空 → 不显式发送（保持 nil 语义）",
			preset:    config.ModelPreset{},
			wantCalls: 0,
		},
		{
			name:      "显式 true 优先于 ThinkingLength=off",
			preset:    config.ModelPreset{ThinkingEnabled: boolPtr(true), ThinkingLength: "off"},
			wantCalls: 1,
			wantVal:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &genParamsRecorder{mockBackend: &mockBackend{}}
			rt := newGenParamsRuntime(t, tc.preset, rec)
			rt.applyGenerationParams(rt.Backend)

			if len(rec.thinkingCalls) != tc.wantCalls {
				t.Fatalf("SetThinkingEnabled 调用次数=%d, want %d（calls=%v）",
					len(rec.thinkingCalls), tc.wantCalls, rec.thinkingCalls)
			}
			if tc.wantCalls > 0 && rec.thinkingCalls[0] != tc.wantVal {
				t.Fatalf("SetThinkingEnabled(%v), want %v", rec.thinkingCalls[0], tc.wantVal)
			}
			if tc.wantCalls == 0 {
				// "两者皆空"的语义是【不显式发送 thinking 字段】（nil），而非
				// 显式发送 false——必须表现为"未调用 setter"（调用记录为 0）。
				// 后端据此的请求体行为（nil→只发 reasoning_effort、不发 thinking）
				// 由 model 包发送矩阵测试锁定。
			}
		})
	}

	// 长度参数独立下发：>0 才下发，三项互不干扰
	rec := &genParamsRecorder{mockBackend: &mockBackend{}}
	rt := newGenParamsRuntime(t, config.ModelPreset{
		MaxOutputTokens: 65536,
		MaxInputTokens:  32768,
		ThinkingLength:  "high",
	}, rec)
	rt.applyGenerationParams(rt.Backend)
	if len(rec.maxOutCalls) != 1 || rec.maxOutCalls[0] != 65536 {
		t.Fatalf("MaxOutputTokens 下发=%v, want [65536]", rec.maxOutCalls)
	}
	if len(rec.maxInCalls) != 1 || rec.maxInCalls[0] != 32768 {
		t.Fatalf("MaxInputTokens 下发=%v, want [32768]", rec.maxInCalls)
	}
	if len(rec.thinkingCalls) != 1 || rec.thinkingCalls[0] != true {
		t.Fatalf("ThinkingLength=high 应下发 true，calls=%v", rec.thinkingCalls)
	}

	// 零回归：未配置（0/nil）的项不得下发（后端保持"未设置"状态）
	rec2 := &genParamsRecorder{mockBackend: &mockBackend{}}
	rt2 := newGenParamsRuntime(t, config.ModelPreset{}, rec2)
	rt2.applyGenerationParams(rt2.Backend)
	if len(rec2.maxOutCalls) != 0 || len(rec2.maxInCalls) != 0 || len(rec2.thinkingCalls) != 0 {
		t.Fatalf("未配置的项不应下发: out=%v in=%v thinking=%v",
			rec2.maxOutCalls, rec2.maxInCalls, rec2.thinkingCalls)
	}

	// 补充观测：两者皆空时对真实 DeepSeekBackend 的影响——后端保持 nil 语义
	// （ThinkingEnabled() 返回 false 表示"未显式设置"——getter 无法区分
	// nil 与显式 false，精确区分以上面的 recorder 调用记录为准），
	// 且长度参数未被触碰（保持 0=未设置）。
	b := newMockChannelBackend(t)
	rt3 := newGenParamsRuntime(t, config.ModelPreset{}, b)
	rt3.applyGenerationParams(rt3.Backend)
	if b.ThinkingEnabled() {
		t.Fatal("未配置 thinking 时后端不应变为显式开启")
	}
	if b.MaxOutputTokens() != 0 || b.MaxInputTokens() != 0 {
		t.Fatalf("未配置长度参数时后端应保持 0（未设置）: out=%d in=%d",
			b.MaxOutputTokens(), b.MaxInputTokens())
	}
}

// ────────────────────────────────────────────────────────────────
// 核心不变式：输出上限与输入预算解耦
// ────────────────────────────────────────────────────────────────

func TestApplyGenerationParams_MaxOutputTokensDecoupled(t *testing.T) {
	b := newMockChannelBackend(t)
	rt := newGenParamsRuntime(t, config.ModelPreset{
		MaxContext:      131072,
		MaxOutputTokens: 65536,
	}, b)

	rt.applyGenerationParams(rt.Backend)
	if got := b.MaxOutputTokens(); got != 65536 {
		t.Fatalf("后端应收到独立输出上限 65536，got %d", got)
	}

	// 核心不变式：输出上限与输入预算解耦——ctxBudget 取有效输入预算
	// （max_input_tokens 或 max_context），不受 max_output_tokens 影响。
	// 模拟启动路径的 ctxBudget 构造（与 runtime.New 内一致）。
	rt.ctxBudget = ctxmgr.NewTokenBudget(rt.effectiveInputBudget())
	if rt.ctxBudget.MaxTokens != rt.effectiveInputBudget() {
		t.Fatalf("ctxBudget.MaxTokens=%d 应等于 effectiveInputBudget()=%d",
			rt.ctxBudget.MaxTokens, rt.effectiveInputBudget())
	}
	if rt.ctxBudget.MaxTokens != 131072 {
		t.Fatalf("输出上限 65536 不得挤占输入预算：ctxBudget.MaxTokens=%d, want 131072（=max_context）",
			rt.ctxBudget.MaxTokens)
	}

	// 反向对照：同时配置输出与输入上限时，预算只认输入侧
	rt.Config.Models[0].MaxInputTokens = 32768
	rt.ctxBudget = ctxmgr.NewTokenBudget(rt.effectiveInputBudget())
	if rt.ctxBudget.MaxTokens != 32768 {
		t.Fatalf("配置 max_input_tokens=32768 时预算应为 32768，got %d", rt.ctxBudget.MaxTokens)
	}
	if b.MaxOutputTokens() != 65536 {
		t.Fatalf("输入侧变化不应影响输出上限：got %d", b.MaxOutputTokens())
	}
}

// ────────────────────────────────────────────────────────────────
// SetEffort：off 档关闭思考（请求体矩阵）+ 切回复位
// ────────────────────────────────────────────────────────────────

func TestSetEffort_OffDisablesThinking(t *testing.T) {
	ts, cap := newEffortCaptureServer(t)
	// sendEffort=true 的官方通道后端：请求体才会带 thinking/reasoning_effort
	b := model.NewDeepSeekBackend(ts.URL, "k", "m", 131072)
	rt := newGenParamsRuntime(t, config.ModelPreset{MaxContext: 131072}, b)

	// ① off：关闭思考
	rt.SetEffort("off")
	if b.ThinkingEnabled() {
		t.Fatal("SetEffort(off) 后后端思考开关应为关闭")
	}
	if got := b.ReasoningEffort(); got != "off" {
		t.Fatalf("档位应保留 off 供 UI 回显，got %q", got)
	}
	chatOnce(t, b)
	body := cap.get()
	if body == nil {
		t.Fatal("请求未被捕获")
	}
	if tt, ok := thinkingTypeOf(t, body); !ok || tt != "disabled" {
		t.Fatalf("off 档请求体 thinking=%v, want {type:disabled}", body["thinking"])
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Fatalf("思考关闭时禁止同时发 reasoning_effort（自相矛盾组合）: %v", body["reasoning_effort"])
	}

	// ② 切回 max：thinkingEnabled 复位为"未显式设置"（nil），请求体不再显式发 thinking
	rt.SetEffort("max")
	if got := b.ReasoningEffort(); got != "max" {
		t.Fatalf("切回后档位应为 max，got %q", got)
	}
	chatOnce(t, b)
	body2 := cap.get()
	if _, ok := body2["thinking"]; ok {
		t.Fatalf("切回思考档位后 thinkingEnabled 应复位为 nil（不显式发送 thinking）: %v", body2["thinking"])
	}
	if got, _ := body2["reasoning_effort"].(string); got != "max" {
		t.Fatalf("切回后应发 reasoning_effort=max，got %v", body2["reasoning_effort"])
	}
}

// TestSetEffort_ConfigThinkingWins：配置优先——/effort off 不得永久覆盖
// 预设的 thinking_enabled=true（SetEffort 内部先切档位、后按配置重新收敛）。
func TestSetEffort_ConfigThinkingWins(t *testing.T) {
	b := newMockChannelBackend(t)
	rt := newGenParamsRuntime(t, config.ModelPreset{
		MaxContext:      131072,
		ThinkingEnabled: boolPtr(true), // 配置显式开启思考
	}, b)

	// 模拟启动路径：配置先在后端生效
	rt.applyGenerationParams(rt.Backend)
	if !b.ThinkingEnabled() {
		t.Fatal("前置条件：配置 thinking_enabled=true 应先在后端生效")
	}

	// SetEffort("off")：内部先 SetReasoningEffort("off")（会把 thinkingEnabled
	// 置 false），随后 applyGenerationParams 按"配置优先"重新收敛回 true。
	rt.SetEffort("off")
	if !b.ThinkingEnabled() {
		t.Fatal("SetEffort(off) 后配置 thinking_enabled=true 应重新收敛（配置优先，不得被档位永久覆盖）")
	}
	// 档位本身仍是 off（UI 回显）；"不发矛盾组合"由后端发送矩阵保证：
	// 档位 off 时不发 reasoning_effort，thinking 显式 enabled。
	if got := b.ReasoningEffort(); got != "off" {
		t.Fatalf("档位应保留 off，got %q", got)
	}
}

// ────────────────────────────────────────────────────────────────
// applyLiveConfig：热更新预算重建（与启动路径同一口径）
// ────────────────────────────────────────────────────────────────

func TestApplyLiveConfig_RecomputesBudget(t *testing.T) {
	b := newMockChannelBackend(t)
	rt := newGenParamsRuntime(t, config.ModelPreset{MaxContext: 131072}, b)
	rt.ctxBudget = ctxmgr.NewTokenBudget(131072)

	// 初次热更新：预算重建为有效输入预算（回退 max_context）
	rt.applyLiveConfig(nil)
	if rt.ctxBudget == nil {
		t.Fatal("applyLiveConfig 后 ctxBudget 不应为空")
	}
	if got := rt.ctxBudget.MaxTokens; got != rt.effectiveInputBudget() {
		t.Fatalf("ctxBudget.MaxTokens=%d 应等于 effectiveInputBudget()=%d", got, rt.effectiveInputBudget())
	}
	if rt.ctxBudget.MaxTokens != 131072 {
		t.Fatalf("ctxBudget.MaxTokens=%d, want 131072", rt.ctxBudget.MaxTokens)
	}

	// 热改 max_input_tokens：下一次热更新立即反映（设置面板保存即生效）
	rt.Config.Models[0].MaxInputTokens = 65536
	rt.applyLiveConfig(nil)
	if got := rt.ctxBudget.MaxTokens; got != 65536 {
		t.Fatalf("热改 max_input_tokens=65536 后预算应为 65536，got %d", got)
	}
	if rt.ctxBudget.MaxTokens != rt.effectiveInputBudget() {
		t.Fatal("热更新后预算与 effectiveInputBudget() 口径不一致")
	}

	// 输出上限参与热更新但不影响预算（解耦不变式）
	rt.Config.Models[0].MaxOutputTokens = 32768
	rt.applyLiveConfig(nil)
	if got := rt.ctxBudget.MaxTokens; got != 65536 {
		t.Fatalf("输出上限变化不得影响输入预算：got %d, want 65536", got)
	}
	if got := b.MaxOutputTokens(); got != 32768 {
		t.Fatalf("输出上限应热下发到后端：got %d, want 32768", got)
	}
}

// ────────────────────────────────────────────────────────────────
// ModelInfo：三项长度参数回显（读后端实际生效值；配置显式值时以配置为准）
// ────────────────────────────────────────────────────────────────

func TestModelInfo_ExposesLengthParams(t *testing.T) {
	b := newMockChannelBackend(t)
	rt := newGenParamsRuntime(t, config.ModelPreset{
		MaxContext:      131072,
		MaxOutputTokens: 65536,
		MaxInputTokens:  32768,
		ThinkingLength:  "high",
	}, b)
	rt.applyGenerationParams(rt.Backend)

	mi := rt.ModelInfo()
	if got := mi["maxOutputTokens"]; got != 65536 {
		t.Fatalf("ModelInfo.maxOutputTokens=%v, want 65536", got)
	}
	if got := mi["maxInputTokens"]; got != 32768 {
		t.Fatalf("ModelInfo.maxInputTokens=%v, want 32768", got)
	}
	if got := mi["thinkingEnabled"]; got != true {
		t.Fatalf("ModelInfo.thinkingEnabled=%v, want true（ThinkingLength=high 解析为开启）", got)
	}

	// 配置显式值时以配置为准（后端状态被其他路径改动也不漂移）
	rt.Config.Models[0].ThinkingEnabled = boolPtr(false)
	b.SetThinkingEnabled(true)
	if got := rt.ModelInfo()["thinkingEnabled"]; got != false {
		t.Fatalf("ThinkingEnabled 配置显式 false 时 ModelInfo 应以配置为准，got %v", got)
	}
}
