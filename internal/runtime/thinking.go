package runtime

// 多轮思考策略运行时装配（M2/M7/M11/M12）。
//
// 职责：
//   - 会话级覆盖（thinkOverride）：/think 命令写入、settings.json 的 thinking_session 持久化；
//   - 策略合成（effectiveThinkStrategy）：会话覆盖 > 配置（Config.Thinking）> 零值 Normalize；
//   - 任务装配（buildThinkTracker）：复杂度评估 → 构造 thinkmgr.Tracker（policy=off 返回 nil，
//     executor 全部短路——零回归铁律的落点）；
//   - 轮次升档（applyRoundEffort）：verify/refine 轮下发一次性 effort 覆盖（仅 escalate）；
//   - 度量（M11）：trace 写 think_round 事件 + 最近任务思考快照（BiasRushed 合并输入）；
//   - 状态（ThinkingStatus）：供 TUI/GUI 展示。
//
// 零回归约定：policy=off（默认）→ buildThinkTracker 返回 nil、TraceThinkRound 零事件、
// rushedBiasHits 零合并；settings.json 无覆盖时不写 thinking_session 键。

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/goalmgr"
	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/internal/thinkmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 会话级覆盖（M2/M12）──

// thinkOverride 会话级思考策略覆盖（nil=未覆盖，取配置值）。
// 仅覆盖 policy / verify_pass 两个会话级字段；其余字段走配置面
// （/think 的 rounds/nudge/effort 子命令写配置）。
type thinkOverride struct {
	policy     *string // off/auto/strict
	verifyPass *string // off/auto/on
}

// strategyFromThinkingConfig 配置面 9 字段 → 轮次面 Strategy（零值语义一致：
// ""→off/none/max、0→运行时 3/60/2 由 Normalize 兜底）。
func strategyFromThinkingConfig(c config.ThinkingConfig) thinkmgr.Strategy {
	return thinkmgr.Strategy{
		Policy:              c.Policy,
		MinRounds:           c.MinThinkRounds,
		MaxThinkRounds:      c.MaxThinkRounds,
		VerifyPass:          c.VerifyPass,
		ComplexityThreshold: c.ComplexityThreshold,
		NudgeEnabled:        c.NudgeEnabled,
		NudgeMax:            c.NudgeMax,
		RoundEffortPolicy:   c.RoundEffortPolicy,
		RoundEffortCap:      c.RoundEffortCap,
	}
}

// effectiveThinkStrategy 合成生效策略：会话覆盖 > 配置 > 零值（Normalize 兜底）。
func (rt *Runtime) effectiveThinkStrategy() thinkmgr.Strategy {
	base := rt.thinkStrategy
	if rt.Config != nil {
		base = strategyFromThinkingConfig(rt.Config.Thinking)
	}
	rt.thinkMu.Lock()
	ov := rt.thinkOverride
	rt.thinkMu.Unlock()
	if ov.policy != nil {
		base.Policy = *ov.policy
	}
	if ov.verifyPass != nil {
		base.VerifyPass = *ov.verifyPass
	}
	return base.Normalize()
}

// SetThinkingPolicy 设置会话级思考策略覆盖（off/auto/strict；"clear"/"" 清除覆盖）。
// 仅更新内存覆盖，持久化由调用方在合适时机调用 SaveSettings
// （settings.json 的 thinking_session 键仅在存在覆盖时写入——零回归）。
func (rt *Runtime) SetThinkingPolicy(p string) error {
	v := strings.ToLower(strings.TrimSpace(p))
	switch v {
	case "", "clear":
		rt.thinkMu.Lock()
		rt.thinkOverride.policy = nil
		rt.thinkMu.Unlock()
		return nil
	case "off", "auto", "strict":
		rt.thinkMu.Lock()
		rt.thinkOverride.policy = &v
		rt.thinkMu.Unlock()
		return nil
	default:
		return fmt.Errorf("非法思考策略 %q（可选 off/auto/strict/clear）", p)
	}
}

// SetVerifyPass 设置会话级验证轮覆盖（off/auto/on；"clear"/"" 清除覆盖）。
func (rt *Runtime) SetVerifyPass(v string) error {
	nv := strings.ToLower(strings.TrimSpace(v))
	switch nv {
	case "", "clear":
		rt.thinkMu.Lock()
		rt.thinkOverride.verifyPass = nil
		rt.thinkMu.Unlock()
		return nil
	case "off", "auto", "on":
		rt.thinkMu.Lock()
		rt.thinkOverride.verifyPass = &nv
		rt.thinkMu.Unlock()
		return nil
	default:
		return fmt.Errorf("非法验证轮档位 %q（可选 off/auto/on/clear）", v)
	}
}

// thinkingSessionSettings 构建 thinking_session 持久化结构（无覆盖返回 nil，
// 调用方仅在非 nil 时写入 settings.json 键——零回归）。
func (rt *Runtime) thinkingSessionSettings() map[string]any {
	rt.thinkMu.Lock()
	ov := rt.thinkOverride
	rt.thinkMu.Unlock()
	if ov.policy == nil && ov.verifyPass == nil {
		return nil
	}
	m := map[string]any{}
	if ov.policy != nil {
		m["policy"] = *ov.policy
	}
	if ov.verifyPass != nil {
		m["verify_pass"] = *ov.verifyPass
	}
	return m
}

// ── 任务装配（M2）──

// buildThinkTracker 任务创建时调用一次：复杂度评估 → 构造 Tracker。
// 返回 nil 的场景（executor 全部短路，零开销）：
//   - policy=off（默认零回归）；
//   - mode=="goal"（goal 已有打磨轮/门禁，不叠加——§2.3 拦截条件⑥）。
//
// imageN 为任务携带的图片数（launchAgent 传 len(task.Images)；拿不到时传 0）；
// explicit 为显式标记（"complex"/"simple"，评分直通；常规装配传 ""）。
func (rt *Runtime) buildThinkTracker(taskText, mode string, imageN int, explicit string) *thinkmgr.Tracker {
	s := rt.effectiveThinkStrategy()
	if s.Policy == "off" || mode == "goal" {
		rt.resetThinkSnapshot()
		return nil
	}
	fuzzy := 0
	if fz := goalmgr.DetectFuzziness(taskText); len(fz.Hits) > 0 {
		fuzzy = len(fz.Hits) // 只读复用（spec.go:167），不硬性拦截
	}
	as := thinkmgr.NewAssessor(s.ComplexityThreshold)
	sig := thinkmgr.Signals{TaskText: taskText, Mode: mode, FuzzyHits: fuzzy, ImageN: imageN, Explicit: explicit}
	c := as.Assess(sig)
	score := as.Score(sig)
	t := thinkmgr.NewTracker(thinkmgr.BuildPolicy(s, c, score), score, c)
	rt.setThinkSnapshot(t)
	return t
}

// ── 轮次升档（M7）──

// applyRoundEffort 按轮次类型下发一次性 effort 覆盖（executor 轮首经钩子调用）。
// 仅 escalate 且 kind==verify/refine 时升档（升档仅向上，不发 off）；其余一律不覆盖。
func (rt *Runtime) applyRoundEffort(kind string) {
	s := rt.effectiveThinkStrategy()
	if s.RoundEffortPolicy != "escalate" {
		return // none=不覆盖（零回归）
	}
	if kind != "verify" && kind != "refine" {
		return // 其余轮不覆盖（首轮不降档）
	}
	if b, ok := rt.Backend.(interface{ SetNextEffort(string) }); ok {
		b.SetNextEffort(s.RoundEffortCap) // 临时层单请求自过期（model 层已实现）
	}
}

// ── 度量（M11）──

// thinkSnapshot 最近一次任务的思考快照（BiasRushed 合并的确定性输入；
// 由 buildThinkTracker 重置、TraceThinkRound 逐轮刷新）。
type thinkSnapshot struct {
	active     bool   // 多轮策略已启用（tracker 非 nil；policy=off 时恒 false→零合并）
	complexity string // 任务复杂度（simple/complex；来自任务级 Policy）
	rounds     int    // 最近任务已登记思考轮数
	toolCalls  int    // 累计工具调用数（含子任务）
	verified   bool   // 出现过验证/修订轮（verify/refine）
	lastKind   string // 最近一轮类型（供 ThinkingStatus 展示）
}

// setThinkSnapshot 以新构造的 Tracker 重置快照（新任务开始）。
func (rt *Runtime) setThinkSnapshot(t *thinkmgr.Tracker) {
	snap := thinkSnapshot{active: true, complexity: string(t.Policy().Complexity)}
	rt.thinkMu.Lock()
	rt.thinkSnap = snap
	rt.thinkMu.Unlock()
}

// resetThinkSnapshot 清空快照（policy=off / goal 排除时调用——保证零合并）。
func (rt *Runtime) resetThinkSnapshot() {
	rt.thinkMu.Lock()
	rt.thinkSnap = thinkSnapshot{}
	rt.thinkMu.Unlock()
}

// TraceThinkRound 实现 executor 的可选 thinkRoundTracer 接口（M11 度量）：
// 写 think_round 事件（trace 开关关时零操作）并刷新最近任务思考快照。
// detail 为 thinkmgr.ThinkRound 的 JSON（executor 序列化传入）。
func (rt *Runtime) TraceThinkRound(mode string, round int, detail string) {
	if rt.Trace != nil {
		rt.Trace.TraceThinkRound(mode, round, detail)
	}
	// 快照刷新（BiasRushed 输入）：解析失败静默跳过（度量不阻断主流程）。
	var r struct {
		Round int    `json:"round"`
		Kind  string `json:"kind"`
		ToolN int    `json:"tool_n"`
	}
	if json.Unmarshal([]byte(detail), &r) != nil {
		return
	}
	if r.Round <= 0 {
		r.Round = round
	}
	rt.thinkMu.Lock()
	if rt.thinkSnap.active {
		rt.thinkSnap.rounds = r.Round
		rt.thinkSnap.toolCalls += r.ToolN
		rt.thinkSnap.lastKind = r.Kind
		if r.Kind == string(thinkmgr.KindVerify) || r.Kind == string(thinkmgr.KindRefine) {
			rt.thinkSnap.verified = true
		}
	}
	rt.thinkMu.Unlock()
}

// rushedBiasHits 草率回答偏差（M11 合并）：仅当多轮策略已启用（tracker 曾构造）
// 时生效；policy=off → 零合并（零回归）。
func (rt *Runtime) rushedBiasHits() []overseer.BiasHit {
	rt.thinkMu.Lock()
	s := rt.thinkSnap
	rt.thinkMu.Unlock()
	if !s.active {
		return nil
	}
	return overseer.BiasRushed(s.rounds, s.toolCalls, s.verified, s.complexity)
}

// ── 状态（UI）──

// ThinkStatus 思考策略状态快照（供 TUI/GUI 展示；policy=off 时调用方不显示）。
type ThinkStatus struct {
	Policy    string // off/auto/strict（生效值，含会话覆盖）
	Round     int    // 最近任务已登记思考轮数
	MaxRound  int    // 续轮注入总上限（拦截+nudge）
	Verify    string // off/auto/on（生效值，含会话覆盖）
	Verifying bool   // 处于验证阶段（策略要求验证且尚未验证）
	Source    string // "override"（会话覆盖生效）/ "config"（配置生效）
}

// ThinkingStatus 返回当前思考策略状态（零依赖，随时可调）。
func (rt *Runtime) ThinkingStatus() ThinkStatus {
	s := rt.effectiveThinkStrategy()
	rt.thinkMu.Lock()
	ov := rt.thinkOverride
	snap := rt.thinkSnap
	rt.thinkMu.Unlock()
	source := "config"
	if ov.policy != nil {
		source = "override"
	}
	verifying := snap.active && !snap.verified &&
		(s.VerifyPass == "on" || (s.Policy == "strict" && s.VerifyPass != "off"))
	return ThinkStatus{
		Policy:    s.Policy,
		Round:     snap.rounds,
		MaxRound:  s.MaxThinkRounds,
		Verify:    s.VerifyPass,
		Verifying: verifying,
		Source:    source,
	}
}

// ── 压缩保护消费点（M10）──

// thinkingRetentionForCompaction 当前压缩保护策略（M10）：
// 验证阶段（策略要求验证且尚未验证）→ 保护最近 1 轮思考；
// 其余（含 policy=off / 非验证阶段）→ 零值（恒等，零回归）。
func (rt *Runtime) thinkingRetentionForCompaction() (ctxmgr.ThinkingRetention, bool) {
	if !rt.ThinkingStatus().Verifying {
		return ctxmgr.ThinkingRetention{}, false
	}
	return ctxmgr.ThinkingRetention{ProtectVerifyRound: true, ProtectRounds: 1}, true
}

// compactHistoryWithThinking 预算压缩（M10 保护消费点，替代直接调用
// CompactToBudgetWithEvict）：
//   - 保护策略启用：压缩前给最近思考打保护标记（MarkProtected——压缩选择
//     截断目标时跳过思考，正文仍可削），压缩发生后用 ThinkingSummary 提取
//     思考尾段以 "[历史思考摘要]（已压缩）" 注入消息尾部（防"思考被压缩后
//     结论丢失"；已存在注入时跳过，防重复）；
//   - 未启用：与 CompactToBudgetWithEvict 现状逐字一致（零回归）。
//
// budget 参数：压缩使用的预算。nil 时取 rt.ctxBudget（历史行为，锁内/无并发
// 场景）；AddToHistory 锁外压缩路径传 Clone 副本——压缩耗时期间配置热更新
// 可能整体替换 rt.ctxBudget 指针，锁外持有旧引用继续压缩会把摘要/审计写到
// 已退役对象上（丢失更新）。调用方回写前用 MergeSummaryInto 把副本累积的
// 滚动摘要合并回现役预算。
func (rt *Runtime) compactHistoryWithThinking(msgs []types.Message, sid string, budget ...*ctxmgr.TokenBudget) ([]types.Message, bool) {
	b := rt.ctxBudget
	if len(budget) > 0 && budget[0] != nil {
		b = budget[0]
	}
	if b == nil {
		return msgs, false
	}
	ret, protected := rt.thinkingRetentionForCompaction()
	src := msgs
	if protected {
		src = ctxmgr.MarkProtected(msgs, ret)
	}
	compacted, changed := ctxmgr.CompactToBudgetWithEvict(src, b, func(m types.Message) { rt.indexCompressedEvictedFor(sid, m) })
	if changed && protected && !hasThinkingSummary(compacted) {
		if sm := ctxmgr.ThinkingSummary(msgs, ctxmgr.DefaultThinkingSummaryRunes); sm != "" {
			compacted = append(compacted, types.Message{Role: "user", Content: sm})
		}
	}
	return compacted, changed
}

// hasThinkingSummary 历史中是否已含思考摘要注入（防重复注入）。
func hasThinkingSummary(msgs []types.Message) bool {
	for _, m := range msgs {
		if s, ok := m.Content.(string); ok && strings.HasPrefix(s, ctxmgr.ThinkingSummaryMarker) {
			return true
		}
	}
	return false
}

// 编译期校验：Runtime 满足 executor.thinkRoundTracer 可选接口
// （TraceThinkRound(mode string, round int, detail string)；未实现时 executor 静默跳过）。
var _ interface {
	TraceThinkRound(mode string, round int, detail string)
} = (*Runtime)(nil)
