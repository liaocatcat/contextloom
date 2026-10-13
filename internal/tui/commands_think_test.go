package tui

// 多轮思考策略 UI 面测试（设计 §3.8）：
//   - /think 命令族：注册、无运行时降级、总览回显、非法值本地拒绝、
//     配置级设置（ApplyConfigPatch）、会话级覆盖（可选接口断言跨任务兼容）；
//   - 状态栏思考段：policy=off 不显示（零回归）、policy≠off 显示策略。
//
// 跨任务兼容：runtime 并行任务的 SetThinkingPolicy/SetVerifyPass/ThinkingStatus
// 可能尚未落盘——覆盖类用例接受"运行时未就绪"回显（断言不 panic + 文案可辨识），
// 落盘后自动走真实 API；不硬依赖并行任务进度。

import (
	"os"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
)

// newThinkRuntime 造一个带多轮思考配置的最小 Runtime。
// ConfigPath 指向临时目录：/think rounds|nudge|effort 走 ApplyConfigPatch 会
// 落盘配置，必须隔离到临时目录，避免污染真实配置。
func newThinkRuntime(t *testing.T, think config.ThinkingConfig) *runtime.Runtime {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-tui-think-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	cfg := &config.Config{
		ConfigPath:      tmpDir + "/config.yaml",
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plugins"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "chat"},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
		Thinking:        think,
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return rt
}

// TestCmd_Think_Registered /think 已注册进命令表（补全/帮助可发现）且描述含子命令。
func TestCmd_Think_Registered(t *testing.T) {
	cr := NewCommandRegistry(nil)
	if !cr.IsCommand("/think") {
		t.Fatalf("/think 应已注册，命令表: %v", cr.Commands())
	}
	desc := ""
	for _, e := range cr.AllEntries() {
		if e.Name == "/think" {
			desc = e.Desc
		}
	}
	for _, want := range []string{"多轮思考策略", "policy", "verify", "rounds", "nudge", "effort"} {
		if !strings.Contains(desc, want) {
			t.Errorf("desc 应含 %q, got %q", want, desc)
		}
	}
}

// TestCmd_Think_NoRuntime 未连接运行时下各子命令优雅提示（不 panic）。
func TestCmd_Think_NoRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	for _, in := range []string{"/think", "/think policy strict", "/think verify on", "/think rounds 3", "/think nudge on", "/think effort escalate"} {
		got, _ := cr.Execute(in)
		if !strings.Contains(got, "未连接") {
			t.Errorf("%s 应提示未连接运行时, got %q", in, got)
		}
	}
}

// TestCmd_Think_Overview_Config 无实时状态时 /think 展示配置值全貌。
func TestCmd_Think_Overview_Config(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{
		Policy: "strict", MinThinkRounds: 3, MaxThinkRounds: 5, VerifyPass: "auto",
		NudgeEnabled: true, NudgeMax: 2, RoundEffortPolicy: "escalate", RoundEffortCap: "high",
	})
	cr := NewCommandRegistry(rt)
	got, _ := cr.Execute("/think")
	for _, want := range []string{"strict", "来源", "最少轮数: 3", "续轮上限: 5", "验证轮: auto", "nudge: 开", "escalate", "封顶high"} {
		if !strings.Contains(got, want) {
			t.Errorf("/think 应含 %q, got:\n%s", want, got)
		}
	}
}

// TestCmd_Think_Overview_DefaultOff 零值配置 → 展示 off（零回归：不 panic、语义可辨）。
func TestCmd_Think_Overview_DefaultOff(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{})
	cr := NewCommandRegistry(rt)
	got, _ := cr.Execute("/think")
	if !strings.Contains(got, "思考策略: off") {
		t.Errorf("零值应展示 思考策略: off, got:\n%s", got)
	}
	// 零值兜底展示：续轮上限取运行时 Normalize 同口径 3
	if !strings.Contains(got, "续轮上限: 3") {
		t.Errorf("零值续轮上限应兜底为 3, got:\n%s", got)
	}
}

// TestCmd_Think_Invalid 非法值/缺参本地拒绝（不写配置、不调 runtime）。
func TestCmd_Think_Invalid(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{})
	cr := NewCommandRegistry(rt)
	cases := []struct{ in, want string }{
		{"/think policy bogus", "非法取值"},
		{"/think verify bogus", "非法取值"},
		{"/think policy", "用法: /think policy"},
		{"/think verify", "用法: /think verify"},
		{"/think rounds abc", "非法数值"},
		{"/think rounds -1", "非法数值"},
		{"/think rounds 99", "非法数值"},
		{"/think nudge maybe", "用法: /think nudge"},
		{"/think effort fast", "用法: /think effort"},
		{"/think wat", "用法: /think"},
	}
	for _, c := range cases {
		if got, _ := cr.Execute(c.in); !strings.Contains(got, c.want) {
			t.Errorf("%s 应含 %q, got %q", c.in, c.want, got)
		}
	}
	// 非法输入不得写配置
	if rt.Config.Thinking.MinThinkRounds != 0 || rt.Config.Thinking.NudgeEnabled || rt.Config.Thinking.RoundEffortPolicy != "" {
		t.Errorf("非法输入污染配置: %+v", rt.Config.Thinking)
	}
}

// TestCmd_Think_ConfigPatch /think rounds|nudge|effort 走 ApplyConfigPatch 落配置。
func TestCmd_Think_ConfigPatch(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{})
	cr := NewCommandRegistry(rt)

	if got, _ := cr.Execute("/think rounds 5"); strings.Contains(got, "设置失败") {
		t.Fatalf("/think rounds 5 失败: %s", got)
	}
	if rt.Config.Thinking.MinThinkRounds != 5 {
		t.Errorf("MinThinkRounds=%d, want 5", rt.Config.Thinking.MinThinkRounds)
	}

	if got, _ := cr.Execute("/think nudge on"); strings.Contains(got, "设置失败") {
		t.Fatalf("/think nudge on 失败: %s", got)
	}
	if !rt.Config.Thinking.NudgeEnabled {
		t.Error("NudgeEnabled 应为 true")
	}
	if got, _ := cr.Execute("/think nudge off"); strings.Contains(got, "设置失败") {
		t.Fatalf("/think nudge off 失败: %s", got)
	}
	if rt.Config.Thinking.NudgeEnabled {
		t.Error("NudgeEnabled 应为 false")
	}

	if got, _ := cr.Execute("/think effort escalate"); strings.Contains(got, "设置失败") {
		t.Fatalf("/think effort escalate 失败: %s", got)
	}
	if rt.Config.Thinking.RoundEffortPolicy != "escalate" {
		t.Errorf("RoundEffortPolicy=%q, want escalate", rt.Config.Thinking.RoundEffortPolicy)
	}
	if got, _ := cr.Execute("/think effort none"); strings.Contains(got, "设置失败") {
		t.Fatalf("/think effort none 失败: %s", got)
	}
	if rt.Config.Thinking.RoundEffortPolicy != "none" {
		t.Errorf("RoundEffortPolicy=%q, want none", rt.Config.Thinking.RoundEffortPolicy)
	}
}

// TestCmd_Think_Override_Tolerant policy/verify 会话级覆盖（跨任务兼容）：
// runtime 未落盘 → "运行时未就绪"；落盘后 → 已设置/已清除（或环境受限时优雅报错）。
// 核心断言：四种路径都不 panic 且文案可辨识。
func TestCmd_Think_Override_Tolerant(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{})
	cr := NewCommandRegistry(rt)
	for _, in := range []string{"/think policy strict", "/think policy clear", "/think verify on", "/think verify clear"} {
		got, _ := cr.Execute(in)
		ok := strings.Contains(got, "已设置") || strings.Contains(got, "已清除") ||
			strings.Contains(got, "未就绪") || strings.Contains(got, "设置失败")
		if !ok {
			t.Errorf("%s 回显异常（应可辨识）: %q", in, got)
		}
	}
}

// TestThinkOrthogonalHint /effort off 与策略的矛盾提示（纯函数，设计 §3.8）。
func TestThinkOrthogonalHint(t *testing.T) {
	if h := thinkOrthogonalHint("off"); !strings.Contains(h, "不会实际发生") {
		t.Errorf("effort=off 应给出矛盾提示, got %q", h)
	}
	if h := thinkOrthogonalHint("medium"); h != "" {
		t.Errorf("effort=medium 不应提示, got %q", h)
	}
	if h := thinkOrthogonalHint(""); h != "" {
		t.Errorf("effort 空值不应提示, got %q", h)
	}
}

// TestCmd_Think_EffortOff_OverviewHint /effort off + 配置 strict 时总览回显矛盾提示。
func TestCmd_Think_EffortOff_OverviewHint(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{Policy: "strict"})
	rt.Config.CLIDefaults.Effort = "off"
	cr := NewCommandRegistry(rt)
	got, _ := cr.Execute("/think")
	if !strings.Contains(got, "不会实际发生") {
		t.Errorf("effort=off 且策略 strict 时总览应含矛盾提示, got:\n%s", got)
	}
}

// TestRenderStatusBar_ThinkSegment 状态栏思考段：
//   - policy=off（默认）→ 不显示（零回归）；
//   - policy=strict → 显示 思考:strict（实时通道缺失时仅策略名）。
func TestRenderStatusBar_ThinkSegment(t *testing.T) {
	// off（零回归）：默认配置不显示思考段
	rtOff := newThinkRuntime(t, config.ThinkingConfig{})
	mOff := initialModel(rtOff)
	if bar := renderStatusBar(mOff); strings.Contains(bar, "思考:") {
		t.Errorf("policy=off 状态栏不应显示思考段, got %q", bar)
	}

	// strict：显示 思考:strict
	rtOn := newThinkRuntime(t, config.ThinkingConfig{Policy: "strict"})
	mOn := initialModel(rtOn)
	bar := renderStatusBar(mOn)
	if !strings.Contains(bar, "思考:strict") {
		t.Errorf("policy=strict 状态栏应显示 思考:strict, got %q", bar)
	}
}

// TestThinkingStatusSegment_ConfigOnly 无实时通道时的降级：仅策略名、无轮次/验证。
func TestThinkingStatusSegment_ConfigOnly(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{Policy: "auto"})
	if seg := thinkingStatusSegment(rt); seg != "思考:auto" {
		t.Errorf("无实时通道应仅显示策略名, got %q", seg)
	}
	if seg := thinkingStatusSegment(nil); seg != "" {
		t.Errorf("nil runtime 应返回空串, got %q", seg)
	}
	// policy=off 且无实时通道 → 空串（零回归）
	rtOff := newThinkRuntime(t, config.ThinkingConfig{})
	if seg := thinkingStatusSegment(rtOff); seg != "" {
		t.Errorf("off 应返回空串, got %q", seg)
	}
}

// TestFormatThinkingSegment_TableDriven 状态栏段纯函数表驱动（设计 §3.8 格式）：
// 覆盖零回归（off/空不显示）、实时轮次/上限/验证中、会话覆盖、默认上限兜底。
func TestFormatThinkingSegment_TableDriven(t *testing.T) {
	cases := []struct {
		name         string
		cfgPolicy    string
		cfgMaxRounds int
		live         map[string]any
		want         string
	}{
		{"off 不显示（零回归）", "off", 3, nil, ""},
		{"空策略不显示（零回归）", "", 3, nil, ""},
		{"off+实时也 off 不显示", "off", 3, map[string]any{"policy": "off"}, ""},
		{"仅配置策略", "strict", 3, nil, "思考:strict"},
		{"实时轮次+上限", "strict", 3, map[string]any{"round": 2, "maxRound": 3}, "思考:strict·轮2/3"},
		{"上限缺失取配置", "strict", 5, map[string]any{"round": 2}, "思考:strict·轮2/5"},
		{"上限配置也为 0 兜底 3", "strict", 0, map[string]any{"round": 2}, "思考:strict·轮2/3"},
		{"验证中", "strict", 3, map[string]any{"round": 2, "maxRound": 3, "verifying": true}, "思考:strict·轮2/3·验证中"},
		{"会话覆盖：配置 off 实时 strict", "off", 3, map[string]any{"policy": "strict", "round": 1, "maxRound": 3}, "思考:strict·轮1/3"},
		{"覆盖清回 off 不显示", "strict", 3, map[string]any{"policy": "off"}, ""},
		{"实时无轮次仅策略", "auto", 3, map[string]any{"verifying": false}, "思考:auto"},
	}
	for _, c := range cases {
		if got := formatThinkingSegment(c.cfgPolicy, c.cfgMaxRounds, c.live); got != c.want {
			t.Errorf("%s: formatThinkingSegment=%q, want %q", c.name, got, c.want)
		}
	}
}

// TestShortenThinkingSegment_TableDriven 窄终端自适应（§3.8 风险项：轮次信息优先）：
// 空间不足依次丢验证标记 → 丢策略名前缀（保留 轮N/M）→ 省略。
func TestShortenThinkingSegment_TableDriven(t *testing.T) {
	full := "思考:strict·轮2/3·验证中"
	noVerify := "思考:strict·轮2/3"
	roundOnly := "轮2/3"
	cases := []struct {
		name  string
		seg   string
		avail int
		want  string
	}{
		{"空间充足原样", full, 100, full},
		{"恰好只够去验证标记", full, lipgloss.Width(noVerify), noVerify},
		{"只够轮次→轮次优先", full, lipgloss.Width(roundOnly), roundOnly},
		{"连轮次都不够→省略", full, 1, ""},
		{"avail=0 省略", full, 0, ""},
		{"负数省略", full, -3, ""},
		{"无轮次且策略名放不下→省略", "思考:strict", 2, ""},
		{"无轮次且放得下", "思考:strict", 100, "思考:strict"},
		{"无验证标记直接轮次优先", noVerify, lipgloss.Width(roundOnly), roundOnly},
	}
	for _, c := range cases {
		if got := shortenThinkingSegment(c.seg, c.avail); got != c.want {
			t.Errorf("%s: shortenThinkingSegment(%q,%d)=%q, want %q", c.name, c.seg, c.avail, got, c.want)
		}
	}
}

// TestRenderStatusBar_ThinkSegment_Narrow 窄终端渲染：空间不足时思考段省略（不撑爆状态栏）；
// 宽终端正常展示；宽度未初始化（0）时全量展示（既有测试口径）。
func TestRenderStatusBar_ThinkSegment_Narrow(t *testing.T) {
	rt := newThinkRuntime(t, config.ThinkingConfig{Policy: "strict"})
	m := initialModel(rt)
	m.width = 200
	if bar := renderStatusBar(m); !strings.Contains(bar, "思考:strict") {
		t.Errorf("宽终端应显示思考段, got %q", bar)
	}
	m.width = 12
	if bar := renderStatusBar(m); strings.Contains(bar, "思考:strict") {
		t.Errorf("极窄终端空间不足应省略思考段, got %q", bar)
	}
	m.width = 0
	if bar := renderStatusBar(m); !strings.Contains(bar, "思考:strict") {
		t.Errorf("宽度未初始化（0）应全量展示, got %q", bar)
	}
}

// TestThinkMapHelpers map 取值辅助函数（实时通道解析的纯函数）。
func TestThinkMapHelpers(t *testing.T) {
	m := map[string]any{"policy": "strict", "round": float64(2), "maxRound": 3, "verifying": true}
	if got := mapStr(m, "policy"); got != "strict" {
		t.Errorf("mapStr=%q", got)
	}
	if got := mapStr(m, "missing"); got != "" {
		t.Errorf("缺失键应空串, got %q", got)
	}
	if got := mapInt(m, "round"); got != 2 {
		t.Errorf("float64 2 应解析为 2, got %d", got)
	}
	if got := mapInt(m, "maxRound"); got != 3 {
		t.Errorf("int 3 应解析为 3, got %d", got)
	}
	if got := mapInt(m, "missing"); got != 0 {
		t.Errorf("缺失键应 0, got %d", got)
	}
	if got := toMapAny(nil); got != nil {
		t.Errorf("nil 应返回 nil, got %v", got)
	}
	if got := toMapAny(m); got == nil || got["policy"] != "strict" {
		t.Errorf("map 透传失败: %v", got)
	}
	// 结构体经 JSON 往返兜底（并行任务可能放结构体）
	type st struct {
		Policy string `json:"policy"`
		Round  int    `json:"round"`
	}
	if got := toMapAny(st{Policy: "auto", Round: 1}); got == nil || got["policy"] != "auto" || got["round"] != float64(1) {
		t.Errorf("结构体 JSON 往返失败: %v", got)
	}
}
