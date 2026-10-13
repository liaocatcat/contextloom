package runtime

// 多轮思考策略运行时装配测试（M2/M7/M11/M12 验收：§6.1 #13/#14/#15 的 runtime 侧）。
// 覆盖：policy=off 零回归、goal 硬排除、会话覆盖持久化往返、升档仅 verify/refine、
// think_round 事件与 BiasRushed 快照合并。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/internal/thinkmgr"
	"github.com/contextloom/contextloom-agent/internal/trace"
)

// ── 测试替身 ──

// effortSpyBackend 在 mockBackend 基础上增加 SetNextEffort（M7 升档断言用；
// 运行时探测路径：rt.Backend.(interface{ SetNextEffort(string) })）。
type effortSpyBackend struct {
	mockBackend
	nextEfforts []string
}

func (m *effortSpyBackend) SetNextEffort(level string) { m.nextEfforts = append(m.nextEfforts, level) }

// complexTaskText 构造确定性命中复杂度的任务文本：
// 540 runes（≥400 → +40）+ 模糊度 3 命中（无执行动词/无可测量/无目标物 → +30）= 70 ≥ 阈值 60。
func complexTaskText() string {
	return strings.Repeat("这里描述一个需要深入处理的问题场景。", 30)
}

// simpleTaskText 短文本且信号齐全（有执行动词/可测量/目标物）→ 评分 0（simple）。
func simpleTaskText() string { return "修复 login.go 的测试失败" }

// ── 用例 ──

// TestBuildThinkTracker_OffNil：policy=off（默认零值/显式 off）→ nil（零回归）；
// strict+complex 文本 → 非 nil 且 Policy().Mode=="strict"、Complexity==complex；
// goal 模式硬排除 → nil；会话覆盖 strict 覆盖配置 off → 非 nil。
func TestBuildThinkTracker_OffNil(t *testing.T) {
	rt := &Runtime{Config: &config.Config{}}

	// 默认零值配置：policy=off → nil
	if tr := rt.buildThinkTracker("任意任务", "task", 0, ""); tr != nil {
		t.Fatalf("policy=off 应返回 nil，实际非 nil（mode=%s）", tr.Policy().Mode)
	}

	// 显式 off + 复杂文本：仍 nil（off 在复杂度评估前短路，零开销）
	rt.Config.Thinking.Policy = "off"
	if tr := rt.buildThinkTracker(complexTaskText(), "task", 0, ""); tr != nil {
		t.Fatal("policy=off（显式）应返回 nil")
	}

	// strict + complex 文本 → 非 nil
	rt.Config.Thinking.Policy = "strict"
	tr := rt.buildThinkTracker(complexTaskText(), "task", 0, "")
	if tr == nil {
		t.Fatal("strict+complex 文本应返回非 nil Tracker")
	}
	if tr.Policy().Mode != "strict" {
		t.Fatalf("Policy().Mode 期望 strict，实际 %q", tr.Policy().Mode)
	}
	if tr.Policy().Complexity != thinkmgr.ComplexityComplex {
		t.Fatalf("复杂文本应判为 complex，实际 %q（score=%d）", tr.Policy().Complexity, tr.Policy().Score)
	}

	// simple 文本 → Tracker 非 nil 但 Complexity=simple（executor 按 simple 零注入路由）
	trs := rt.buildThinkTracker(simpleTaskText(), "task", 0, "")
	if trs == nil {
		t.Fatal("strict+simple 文本应返回 Tracker（由 executor 按 simple 零注入）")
	}
	if trs.Policy().Complexity != thinkmgr.ComplexitySimple {
		t.Fatalf("短任务文本应判为 simple，实际 %q（score=%d）", trs.Policy().Complexity, trs.Policy().Score)
	}

	// goal 模式硬排除：即使 strict 也返回 nil（goal 已有打磨轮/门禁，不叠加）
	if tr := rt.buildThinkTracker(complexTaskText(), "goal", 0, ""); tr != nil {
		t.Fatal("goal 模式应返回 nil（硬排除）")
	}

	// 会话覆盖 > 配置：配置 off + 覆盖 strict → 非 nil
	rt.Config.Thinking.Policy = "off"
	if err := rt.SetThinkingPolicy("strict"); err != nil {
		t.Fatal(err)
	}
	if tr := rt.buildThinkTracker(complexTaskText(), "task", 0, ""); tr == nil {
		t.Fatal("会话覆盖 strict 应覆盖配置 off，返回非 nil")
	}
}

// TestThinkingSession_PersistRestore：SetThinkingPolicy("strict")+SetVerifyPass("on") →
// saveSettings → settings.json 含 thinking_session → 新 Runtime loadSettings 恢复覆盖；
// 旧 settings（无该键）→ 无覆盖、行为不变（零回归）；清除覆盖后键删除。
func TestThinkingSession_PersistRestore(t *testing.T) {
	dir := t.TempDir()
	rt := &Runtime{Config: &config.Config{}}
	rt.Config.Project.Root = dir
	rt.Backend = &mockBackend{}
	rt.Harness = harness.New(harness.Config{})
	if err := rt.SetThinkingPolicy("strict"); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetVerifyPass("on"); err != nil {
		t.Fatal(err)
	}
	rt.saveSettings()

	path := filepath.Join(dir, ".contextloom", "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 settings.json 失败: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("解析 settings.json 失败: %v", err)
	}
	ts, ok := s["thinking_session"].(map[string]any)
	if !ok {
		t.Fatalf("settings.json 缺少 thinking_session 键: %s", string(data))
	}
	if ts["policy"] != "strict" || ts["verify_pass"] != "on" {
		t.Fatalf("thinking_session 内容不符: %v", ts)
	}

	// 新 Runtime（模拟重启）loadSettings 恢复覆盖
	rt2 := &Runtime{Config: &config.Config{}}
	rt2.Config.Project.Root = dir
	rt2.Backend = &mockBackend{}
	rt2.Harness = harness.New(harness.Config{})
	rt2.loadSettings()
	eff := rt2.effectiveThinkStrategy()
	if eff.Policy != "strict" || eff.VerifyPass != "on" {
		t.Fatalf("覆盖未恢复：policy=%q verify=%q", eff.Policy, eff.VerifyPass)
	}
	if st := rt2.ThinkingStatus(); st.Source != "override" || st.Policy != "strict" {
		t.Fatalf("ThinkingStatus 期望 override/strict，实际 %+v", st)
	}

	// 旧 settings（无 thinking_session）→ 无覆盖、行为不变（零回归）
	if err := os.WriteFile(path, []byte(`{"mode":"chat"}`), 0644); err != nil {
		t.Fatal(err)
	}
	rt3 := &Runtime{Config: &config.Config{}}
	rt3.Config.Project.Root = dir
	rt3.Backend = &mockBackend{}
	rt3.Harness = harness.New(harness.Config{})
	rt3.loadSettings()
	if rt3.thinkOverride.policy != nil || rt3.thinkOverride.verifyPass != nil {
		t.Fatalf("旧 settings 不应产生覆盖: %+v", rt3.thinkOverride)
	}
	if got := rt3.effectiveThinkStrategy(); got.Policy != "off" || got.VerifyPass != "" {
		t.Fatalf("无覆盖时策略应保持零值（off），实际 policy=%q verify=%q", got.Policy, got.VerifyPass)
	}

	// 清除覆盖 → 保存后 settings.json 不再含 thinking_session 键（回到旧格式）
	if err := rt.SetThinkingPolicy("clear"); err != nil {
		t.Fatal(err)
	}
	if err := rt.SetVerifyPass("clear"); err != nil {
		t.Fatal(err)
	}
	rt.saveSettings()
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 settings.json 失败: %v", err)
	}
	s = map[string]any{}
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("解析 settings.json 失败: %v", err)
	}
	if _, exists := s["thinking_session"]; exists {
		t.Fatalf("清除覆盖后不应写 thinking_session 键: %s", string(data))
	}

	// 非法值拒绝（校验兜底；不改变已有覆盖状态）
	if err := rt.SetThinkingPolicy("yolo"); err == nil {
		t.Fatal("非法策略应报错")
	}
	if err := rt.SetVerifyPass("maybe"); err == nil {
		t.Fatal("非法验证轮档位应报错")
	}
}

// TestApplyRoundEffort_EscalateOnly：escalate+verify/refine → SetNextEffort(cap)；
// none（零值归一）/execute/explore → 不调；cap 未设 → Normalize 兜底 max（升档仅向上）；
// 后端未实现 SetNextEffort → 静默跳过（零回归）。
func TestApplyRoundEffort_EscalateOnly(t *testing.T) {
	rt := &Runtime{Config: &config.Config{}}
	spy := &effortSpyBackend{}
	rt.Backend = spy

	// none（零值归一）：任何轮次都不调用（零回归）
	rt.applyRoundEffort("verify")
	rt.applyRoundEffort("refine")
	if len(spy.nextEfforts) != 0 {
		t.Fatalf("RoundEffortPolicy=none 不应升档，实际 %v", spy.nextEfforts)
	}

	// escalate：verify 轮升档 cap
	rt.Config.Thinking.RoundEffortPolicy = "escalate"
	rt.Config.Thinking.RoundEffortCap = "high"
	rt.applyRoundEffort("verify")
	if len(spy.nextEfforts) != 1 || spy.nextEfforts[0] != "high" {
		t.Fatalf("escalate+verify 期望 SetNextEffort(high)，实际 %v", spy.nextEfforts)
	}

	// refine 轮同样升档
	rt.applyRoundEffort("refine")
	if len(spy.nextEfforts) != 2 || spy.nextEfforts[1] != "high" {
		t.Fatalf("escalate+refine 期望第 2 次 SetNextEffort(high)，实际 %v", spy.nextEfforts)
	}

	// 其余轮不覆盖（首轮不降档）
	rt.applyRoundEffort("execute")
	rt.applyRoundEffort("explore")
	rt.applyRoundEffort("")
	if len(spy.nextEfforts) != 2 {
		t.Fatalf("execute/explore 轮不应升档，实际 %v", spy.nextEfforts)
	}

	// cap 未设置 → Normalize 兜底 max（不发 off）
	rt2 := &Runtime{Config: &config.Config{}}
	rt2.Config.Thinking.RoundEffortPolicy = "escalate"
	spy2 := &effortSpyBackend{}
	rt2.Backend = spy2
	rt2.applyRoundEffort("verify")
	if len(spy2.nextEfforts) != 1 || spy2.nextEfforts[0] != "max" {
		t.Fatalf("cap 未设置应兜底 max，实际 %v", spy2.nextEfforts)
	}

	// 后端未实现 SetNextEffort（mockBackend）→ 静默跳过，不 panic
	rt3 := &Runtime{Config: &config.Config{}}
	rt3.Config.Thinking.RoundEffortPolicy = "escalate"
	rt3.Backend = &mockBackend{}
	rt3.applyRoundEffort("verify") // 不 panic 即通过
}

// TestTraceThinkRound_Event：trace 开关开时写 think_round 事件（Entry.Event=="think_round"，
// Mode/Round/Detail 完整）；同时刷新 BiasRushed 快照（M11 合并输入）：
// 1 轮无证据命中 rushed_answer；第 2 轮有工具证据不命中；policy=off 重建 → 零合并；
// simple 任务不误伤。
func TestTraceThinkRound_Event(t *testing.T) {
	dir := t.TempDir()
	rec, err := trace.New(filepath.Join(dir, "trace"), true)
	if err != nil {
		t.Fatalf("创建 trace Recorder 失败: %v", err)
	}
	defer rec.Close()

	rt := &Runtime{Config: &config.Config{}, Trace: rec}
	rt.Config.Thinking.Policy = "auto"
	if tr := rt.buildThinkTracker(complexTaskText(), "task", 0, ""); tr == nil {
		t.Fatal("auto+complex 应构造 Tracker")
	}

	detail := `{"round":1,"kind":"explore","has_tools":false,"tool_n":0,"body_len":10}`
	rt.TraceThinkRound("task", 1, detail)

	entries := rec.Recent(10)
	found := false
	for _, e := range entries {
		if e.Event == trace.EventThinkRound {
			found = true
			if e.Mode != "task" || e.Round != 1 || e.Detail != detail {
				t.Fatalf("think_round 事件字段不符: %+v", e)
			}
		}
	}
	if !found {
		t.Fatalf("未找到 think_round 事件，实际 entries=%+v", entries)
	}

	// 快照：1 轮、0 工具 → complex 任务命中 rushed_answer（-10）
	hits := rt.rushedBiasHits()
	if len(hits) != 1 || hits[0].Kind != overseer.BiasRushedAnswer {
		t.Fatalf("1 轮无证据应命中 rushed_answer，实际 %+v", hits)
	}

	// 第 2 轮（有工具证据）→ 不命中
	rt.TraceThinkRound("task", 2, `{"round":2,"kind":"execute","tool_n":2}`)
	if hits := rt.rushedBiasHits(); len(hits) != 0 {
		t.Fatalf("有工具证据不应命中 rushed_answer，实际 %+v", hits)
	}

	// policy=off 重建（新任务）→ 快照清空、零合并（零回归）
	rt.Config.Thinking.Policy = "off"
	if tr := rt.buildThinkTracker(complexTaskText(), "task", 0, ""); tr != nil {
		t.Fatal("off 应返回 nil")
	}
	if hits := rt.rushedBiasHits(); len(hits) != 0 {
		t.Fatalf("policy=off 应零合并，实际 %+v", hits)
	}

	// simple 任务快照不误伤：重建 → simple 文本 → 快照 complexity=simple → 零命中
	rt.Config.Thinking.Policy = "auto"
	if tr := rt.buildThinkTracker(simpleTaskText(), "task", 0, ""); tr == nil {
		t.Fatal("auto+simple 应构造 Tracker（executor 按 simple 零注入）")
	}
	rt.TraceThinkRound("task", 1, `{"round":1,"kind":"explore","tool_n":0}`)
	if hits := rt.rushedBiasHits(); len(hits) != 0 {
		t.Fatalf("simple 任务不应命中 rushed_answer（不误伤），实际 %+v", hits)
	}
}
