// Package thinkmgr 多轮思考机制：思考轮模型、复杂度评估、任务级策略、验证/续思协议与护栏。
// 默认关闭（Policy=off）时 executor 不构造 Tracker，全部新分支短路——零回归铁律的落点。
package thinkmgr

import (
	"fmt"
	"strings"
	"time"
)

// ── 轮次模型（M1）──

// Kind 思考轮类型。
type Kind string

const (
	KindExplore Kind = "explore" // 首探轮
	KindExecute Kind = "execute" // 执行轮
	KindVerify  Kind = "verify"  // 验证轮（CoVe）
	KindRefine  Kind = "refine"  // 修订轮
)

// ThinkRound 一次思考轮记录（与执行轮 round 1:1 同序；json tag 供 trace 上报）。
type ThinkRound struct {
	Round    int       `json:"round"`     // 轮次序号（1 起）
	Kind     Kind      `json:"kind"`      // 轮次类型
	HasTools bool      `json:"has_tools"` // 本轮是否调用工具
	ToolN    int       `json:"tool_n"`    // 工具调用数
	BodyLen  int       `json:"body_len"`  // 正文长度（runes）
	Reason   string    `json:"reason"`    // 备注（拦截/修订原因；预留）
	TS       time.Time `json:"ts"`        // 登记时间
}

// State 状态快照：score 评分 / complexity 分级 / rounds 轮次表 / intercepts 拦截数 / nudges 续思数 / verified 验证通过。
type State struct {
	Score      int          // 复杂度评分（0-100）
	Complexity Complexity   // 复杂度分级
	Rounds     []ThinkRound // 轮次表（与执行轮 1:1 同序）
	Intercepts int          // 硬拦截注入数
	Nudges     int          // 软续思注入数
	Verified   bool         // 是否完成验证轮
}

// Tracker 思考轮追踪器（每任务一个；策略关闭时不构造）。
type Tracker struct {
	policy   Policy // 任务级策略（只读）
	state    State  // 状态（拦截决策唯一事实源）
	kindNext Kind   // 下一轮预设类型（注入后为 verify/refine，只生效一次）
}

// NewTracker 创建追踪器；第 1 轮类型固定为 explore。
func NewTracker(p Policy, score int, c Complexity) *Tracker {
	return &Tracker{policy: p, state: State{Score: score, Complexity: c}, kindNext: KindExplore}
}

// BeginRound 登记本轮并返回类型；FinishRound 回填末条 ToolN/HasTools/BodyLen。
// 预设只生效一次：explore/verify/refine 返回后自动恢复 execute（常规推进）。
func (t *Tracker) BeginRound() Kind {
	k := t.kindNext
	if k != KindExecute {
		t.kindNext = KindExecute
	}
	t.state.Rounds = append(t.state.Rounds, ThinkRound{Round: len(t.state.Rounds) + 1, Kind: k, TS: time.Now()})
	return k
}

// FinishRound 回填末条轮次记录的工具数与正文长度（无轮次时为空操作）。
func (t *Tracker) FinishRound(toolN, bodyLen int) {
	if n := len(t.state.Rounds); n > 0 {
		r := &t.state.Rounds[n-1]
		r.ToolN = toolN
		r.HasTools = toolN > 0
		r.BodyLen = bodyLen
	}
}

// SettleRound 轮末结算（计数为主、标记为辅）：
//   - 含 MarkVerified → 验证通过，提前放行；
//   - 含 MarkNeedsFix → 末条降级 KindRefine 且保持未验证（保证复验）；
//   - verify 轮非空且无标记 → 兜底通过（不依赖模型标记）。
func (t *Tracker) SettleRound(body string) {
	text := strings.TrimSpace(body)
	switch {
	case strings.Contains(text, MarkVerified):
		t.state.Verified = true
	case strings.Contains(text, MarkNeedsFix):
		t.state.Verified = false
		if n := len(t.state.Rounds); n > 0 {
			t.state.Rounds[n-1].Kind = KindRefine
		}
	default:
		if n := len(t.state.Rounds); n > 0 && t.state.Rounds[n-1].Kind == KindVerify && text != "" {
			t.state.Verified = true
		}
	}
}

// ShouldInterceptFinal 收尾拦截判定（M3/M5）：返回（是否拦截, 原因）。
// 逃生通道（任一即放行）：非 strict（auto=软路由）/ token 预算红线 / 已达续轮上限 / 达标且验证通过。
func (t *Tracker) ShouldInterceptFinal(budgetExceeded bool) (bool, string) {
	if t.policy.Mode != "strict" {
		return false, "" // auto=软路由，不硬拦截
	}
	if budgetExceeded {
		return false, "token 预算红线优先"
	}
	if t.state.Intercepts+t.state.Nudges >= t.policy.MaxExtraRounds {
		return false, "已达续轮上限"
	}
	n := len(t.state.Rounds)
	needRounds := t.policy.MinRounds > 0 && n < t.policy.MinRounds
	needVerify := t.policy.RequireVerify && !t.state.Verified
	if !needRounds && !needVerify {
		return false, "已达最少轮数且验证通过"
	}
	reason := "收尾拦截："
	if needRounds {
		reason += fmt.Sprintf("轮数 %d<%d；", n, t.policy.MinRounds)
	}
	if needVerify {
		reason += "未完成验证轮"
	}
	return true, reason
}

// ShouldNudge 软注入判定（M4/M6）：续思提示或验证指令的注入决策，返回（是否注入, 原因）。
// 优先级：续轮总上限 → verify_pass=on 首次验证 → nudge 开关/上限 → 间隔 → 达标豁免 → 续思提示。
func (t *Tracker) ShouldNudge(round, bodyRunes, lastNudgeRound int) (bool, string) {
	if t.state.Intercepts+t.state.Nudges >= t.policy.MaxExtraRounds {
		return false, "已达续轮上限"
	}
	if t.policy.VerifyPass == "on" && !t.state.Verified && t.state.Nudges == 0 {
		return true, "验证轮（verify_pass=on）"
	}
	if !t.policy.NudgeEnabled || t.state.Nudges >= t.policy.NudgeMax {
		return false, "nudge 上限或关闭"
	}
	if lastNudgeRound > 0 && round-lastNudgeRound < t.policy.NudgeGap {
		return false, "间隔不足"
	}
	if round >= t.policy.MinRounds && bodyRunes >= t.policy.NudgeMinRunes {
		return false, "轮数达标且回答充分"
	}
	return true, "续思提示"
}

// Inject 记录一次硬拦截注入，并预设下一轮类型（verify）。
func (t *Tracker) Inject(kind Kind, reason string) {
	t.state.Intercepts++
	t.kindNext = kind
}

// InjectNudge 记录一次软续思注入，并预设下一轮类型（verify）。
func (t *Tracker) InjectNudge(reason string) {
	t.state.Nudges++
	t.kindNext = KindVerify
}

// MarkVerified 标记验证通过（提前放行）。
func (t *Tracker) MarkVerified() { t.state.Verified = true }

// Policy 返回任务级策略（只读）。
func (t *Tracker) Policy() Policy { return t.policy }

// Snapshot 返回状态快照（只读；调用方不得修改 Rounds 内容）。
func (t *Tracker) Snapshot() State { return t.state }
