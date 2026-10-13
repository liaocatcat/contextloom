# ContextLoom 多轮思考【触发与拦截侧】设计草案
> 分册：design-c-trigger.md（并入主设计文档《contextloom改造详细设计-多轮思考触发.md》"触发与拦截"章）
> 基线：《docs/research/contextloom-thinking-inventory.md》（H1-H25）、《docs/research/industry-multi-pass-thinking-survey.md》（Top 10）
> 行号口径：本稿行号为本次工作区实测（Select-String + read_file 抽查）；与盘点漂移见 §8，以本稿为准。
> 铁律：**默认关闭（thinking_policy.enabled=false）时全部路径与现状逐字一致**——新逻辑一律走"策略非空才生效"分支。

## 0. 总览
把"第一轮非空即收尾"（agent.go:1284-1286、416-419）改为"复杂问题收尾前必过验证轮"：复杂度评估器（确定性）→ 思考轮追踪器（内存）→ 收尾拦截（注入 CoVe 验证指令）→ 达标放行；配置 `thinking_policy` 控制，默认关闭。本稿覆盖触发与拦截；提示词侧（H5/H6）、评估续跑（H13/H17）、多 Agent（H20/H25）、UI（H19）为另稿，本稿仅预留接口。

## 1. 思考轮模型（核心概念）
**目标**：引入"思考轮"一等概念——类型化（explore/execute/verify/refine）+ 可判定（是否验证过）；单一事实源：决策看 Agent 内存态、观测看 trace、持久化看 goal 状态（仅 goal 模式）。
**机制**：一次思考轮 = 一次"模型请求 → 产出（正文/工具调用）→ 本轮结束"的完整循环；与现有执行轮（round，agent.go:1060-1062）**1:1 同序**，不新增第二套计数器（防漂移）。类型：explore 首探轮（第 1 轮）/ execute 执行轮 / verify 验证轮（拦截注入 CoVe 后）/ refine 修订轮（验证发现矛盾后修正）。状态载体三层：① Agent 字段 `think *thinkmgr.Tracker`（内存，拦截决策唯一事实源；nil=关闭）；② trace 事件 `think_round`（复用 trace.Entry:21-25，与 goal_round runtime.go:2795 并列）；③ goal 状态（pkg/types/goal.go:117 追加轻量字段，GoalPhase 枚举 22-30 为先例）。
**Go 接口草案**
```go
// Package thinkmgr 多轮思考策略（触发与拦截侧）。依赖：fmt/strings/regexp/time（标准库）。
// 默认关闭（Strategy.Enabled=false）：executor 不构造 Tracker，全部新分支短路。
package thinkmgr
type Kind string // explore/execute/verify/refine
const (
	KindExplore Kind = "explore" // 首探轮：理解任务、定位影响面
	KindExecute Kind = "execute" // 执行轮：推进产出
	KindVerify  Kind = "verify"  // 验证轮：对结论独立复核（CoVe）
	KindRefine  Kind = "refine"  // 修订轮：验证发现矛盾后的修正
)
// ThinkRound 一次思考轮记录（与执行轮 1:1 同序）。
type ThinkRound struct {
	Round    int       `json:"round"`            // 从 1 开始
	Kind     Kind      `json:"kind"`
	HasTools bool      `json:"has_tools"`        // 本轮是否执行工具调用
	ToolN    int       `json:"tool_n"`           // 工具调用数（0=纯思考轮）
	BodyLen  int       `json:"body_len"`         // 正文长度（runes；0=空输出）
	Reason   string    `json:"reason,omitempty"` // 拦截/注入原因
	TS       time.Time `json:"ts"`
}
// State 状态快照（trace/checkpoint 上报用）。
type State struct {
	Complexity Complexity   `json:"complexity"`
	Rounds     []ThinkRound `json:"rounds"`
	Intercepts int          `json:"intercepts"` // 已注入次数
	Verified   bool         `json:"verified"`   // 是否已完成验证轮
}
// Tracker 思考轮追踪器（每任务一个；策略关闭时不构造）。
type Tracker struct {
	policy   Policy
	state    State
	kindNext Kind // 下一轮预设类型（注入验证后为 verify）
}
func NewTracker(p Policy, c Complexity) *Tracker {
	return &Tracker{policy: p, state: State{Complexity: c}, kindNext: KindExplore}
}
// BeginRound 每轮模型请求前调用：登记并返回本轮类型（预设只生效一次）。
func (t *Tracker) BeginRound() Kind {
	k := t.kindNext
	if k == KindVerify || k == KindRefine {
		t.kindNext = KindExecute
	}
	t.state.Rounds = append(t.state.Rounds, ThinkRound{Round: len(t.state.Rounds) + 1, Kind: k, TS: time.Now()})
	return k
}
// FinishRound 每轮结束时回填工具数与正文长度。
func (t *Tracker) FinishRound(toolN, bodyLen int) {
	if n := len(t.state.Rounds); n > 0 {
		r := &t.state.Rounds[n-1]
		r.ToolN, r.HasTools, r.BodyLen = toolN, toolN > 0, bodyLen
	}
}
// MarkVerified 标记验证通过（[THINK_VERIFIED] 或验证轮计数兜底）。
func (t *Tracker) MarkVerified() { t.state.Verified = true }
// SettleRound 轮末结算：verify 轮非空回答→兜底通过；[THINK_NEEDS_FIX]→降级 refine 待复验。
func (t *Tracker) SettleRound(body string) {
	if strings.Contains(body, MarkVerified) { t.MarkVerified(); return }
	if n := len(t.state.Rounds); n > 0 && t.state.Rounds[n-1].Kind == KindVerify && strings.TrimSpace(body) != "" {
		if strings.Contains(body, MarkNeedsFix) { t.state.Rounds[n-1].Kind = KindRefine; return }
		t.MarkVerified()
	}
}
func (t *Tracker) Policy() Policy  { return t.policy }
func (t *Tracker) Snapshot() State { return t.state }
```
**挂钩点**：H3（agent.go:1060-1062 主循环入口，BeginRound/FinishRound 落点）、H8（agent.go:2025 splitThinkTagsAgent，计数入口）、H21（trace.go:21-25，Event 常量）、H14（runtime.go:2717 GoalOnRoundEnd，goal 侧消费快照）。
**测试断言**：thinkmgr_test.go——BeginRound 类型流转表驱动（explore→execute→verify→refine）；集成——策略开启时 trace 含 think_round 且 Round/Kind 正确，关闭时**零** think_round。
**零回归/默认关闭**：策略关闭 → `a.think==nil` → 全部新分支短路；无新事件、无新消息。
**风险**：双计数漂移（复用 round 变量，不新增计数器）；主/子 Agent 路径差异（同一 Tracker API）。

## 2. 复杂度评估器（复杂问题的判定）
**目标与信号源**：零模型依赖、确定性、可解释、阈值可配。信号（全部确定性）：任务文本长度（helpers.go:64 estimateComplexity 的"长度分档"思想，只读复用）；模糊度（spec.go:167 DetectFuzziness 3 信号：弱动词/可测量/目标物）；任务模式（TaskCreate.Mode；goal 默认至少 moderate）；多目标词 regex（同时/分别/并行/多个/批量）；用户显式标记（complex/simple 直通）；图片数（可选加分）。
**判定规则（评分制）**：文本 ≥120 runes +1、≥400 再 +1；FuzzyHits ≥1 +1、≥2 再 +1；mode=goal +1；task 且多目标词 +1；图片 >0 +1。总分 0-1→simple；2-3→moderate；≥4→complex。**输出映射**：simple=1 轮/不验证；moderate=2 轮/强制验证；complex=3 轮（首探+执行+验证）/强制验证；验证轮 effort=high（可配）、首轮 effort 默认不改（可配）。
**Go 接口草案**
```go
type Complexity string
const (
	ComplexitySimple   Complexity = "simple"   // 现状单轮路径
	ComplexityModerate Complexity = "moderate" // 最少 2 轮 + 验证
	ComplexityComplex  Complexity = "complex"  // 最少 3 轮 + 验证
)
// Signals 评估输入（全部确定性信号）。
type Signals struct {
	TaskText  string // 任务文本（原始输入）
	Mode      string // chat / task / goal
	FuzzyHits int    // goalmgr.DetectFuzziness 命中条数（0-3，spec.go:167）
	ImageN    int    // 图片数
	Explicit  string // 用户显式标记："complex" / "simple" / ""（auto）
}
// Assessor 复杂度评估器（阈值可配，纯函数可测）。
type Assessor struct {
	LenModerate   int // 长度加分档 1（默认 120 runes）
	LenComplex    int // 长度加分档 2（默认 400 runes）
	ScoreModerate int // 总分 ≥ 该值 → moderate（默认 2）
	ScoreComplex  int // 总分 ≥ 该值 → complex（默认 4）
}
func DefaultAssessor() *Assessor {
	return &Assessor{LenModerate: 120, LenComplex: 400, ScoreModerate: 2, ScoreComplex: 4}
}
// multiGoalRe 多目标词（并行/多产出任务信号）。
var multiGoalRe = regexp.MustCompile(`(同时|分别|并行|并且|以及|多个|批量)`)
// Assess 打分并分级（规则见上）。
func (a *Assessor) Assess(s Signals) Complexity {
	switch strings.ToLower(strings.TrimSpace(s.Explicit)) {
	case "complex":
		return ComplexityComplex
	case "simple":
		return ComplexitySimple
	}
	score := 0
	if n := len([]rune(s.TaskText)); n >= a.LenModerate {
		score++
		if n >= a.LenComplex {
			score++
		}
	}
	if s.FuzzyHits >= 1 { score++ }
	if s.FuzzyHits >= 2 { score++ }
	if s.Mode == "goal" { score++ }
	if s.Mode == "task" && multiGoalRe.MatchString(s.TaskText) { score++ }
	if s.ImageN > 0 { score++ }
	switch {
	case score >= a.ScoreComplex: return ComplexityComplex
	case score >= a.ScoreModerate: return ComplexityModerate
	default: return ComplexitySimple
	}
}
// AssessComplexity 便捷纯函数（默认阈值；供 runtime 任务创建路径调用）。
func AssessComplexity(task, mode string) Complexity {
	return DefaultAssessor().Assess(Signals{TaskText: task, Mode: mode})
}
```
**挂钩点**：H22（helpers.go:64、spec.go:167 只读复用）、H23（settings.go:13/41；config.example.yaml:152）、H3（策略随复杂度注入）。
**测试断言**：assessor_test.go 表驱动 ≥10 组（空/短/长/超长/模糊 1/2/3 条/goal/多目标词/显式标记）；显式 simple 覆盖评分、complex 直通。
**零回归/默认关闭**：Assessor 为新增纯函数；estimateComplexity 与 DetectFuzziness 零改动，仅新增调用方；策略关闭时评估结果被丢弃。
**风险**：误判复杂（simple 直通 + MaxIntercepts + 阈值可配）；中文关键词脆弱（词表集中为常量）。

## 3. 收尾拦截机制（杜绝"第一轮非空即结束"）
**目标**：复杂任务收尾前必过验证轮；简单任务零打扰；token 红线绝对优先。
**机制**：现状三条直通车——agent.go:1284-1286（RunStream 非空即 break）、416-419（Run 同步路径直接 break）、1232-1247（仅空正文提示）。拦截条件（AND）：① 策略启用；② 复杂度 ≥ moderate；③ `!verified || round < MinRounds`；④ `intercepts < MaxIntercepts`（默认 3）；⑤ 非 forcedFinal（token 红线，agent.go:1041/1064-1078）；⑥ 非 goal 模式（已有打磨轮/门禁，不叠加）。拦截动作：不 break——把 CoVe 验证指令作为 user 消息追加在回答之后（1232-1247 同构先例），continue 进入验证轮；intercepts++；写 trace。放行（任一）：验证完成且达下限；达注入上限；token 红线；ctx 取消；策略关闭；simple。
**Go 接口草案**
```go
// ── Tracker 拦截判定（thinkmgr）──
// ShouldInterceptFinal：返回 (拦截, 原因)；budgetExceeded=token 红线（永不拦截）。
func (t *Tracker) ShouldInterceptFinal(budgetExceeded bool) (bool, string) {
	if budgetExceeded { return false, "token 预算红线优先" }
	if t.state.Intercepts >= t.policy.MaxIntercepts { return false, "已达拦截续轮上限" }
	n := len(t.state.Rounds)
	needRounds := n < t.policy.MinRounds
	needVerify := t.policy.RequireVerify && !t.state.Verified
	if !needRounds && !needVerify { return false, "已达最少轮数且验证通过" }
	var reasons []string
	if needRounds { reasons = append(reasons, fmt.Sprintf("轮数 %d<%d", n, t.policy.MinRounds)) }
	if needVerify { reasons = append(reasons, "未完成验证轮") }
	return true, "收尾拦截：" + strings.Join(reasons, "；")
}
// Inject 记录一次注入并预设下一轮类型（verify）。
func (t *Tracker) Inject(kind Kind, reason string) {
	t.state.Intercepts++
	t.kindNext = kind
}
// ── executor 接入（agent.go）──
// Agent 新增字段（置于 runRound（agent.go:49-50）之后，与 SetTracer（agent.go:118）同列注入）：
//   think *thinkmgr.Tracker // nil=策略关闭，主循环零开销
func (a *Agent) SetThinkPolicy(t *thinkmgr.Tracker) { a.think = t }
// shouldInterceptFinal 收尾拦截判定（RunStream:1284-1286 / Run:416-419 调用）。
func (a *Agent) shouldInterceptFinal(budgetExceeded bool) (bool, string) {
	if a.think == nil { return false, "" }        // 策略关闭：零回归
	if a.Task.Mode == "goal" { return false, "" } // goal 已有打磨轮/门禁，不叠加
	if hit, _ := a.think.ShouldInterceptFinal(budgetExceeded); !hit {
		return false, ""
	}
	return true, thinkmgr.BuildVerifyNudge(a.runRound+1, a.think.Policy().VerifyTemplate)
}
// ── 注入点（RunStream，agent.go:1284-1286 改造；默认路径逐字保留）──
// 现状：模型已给出实质回答且未调用工具 → 直接 break。
// 改造后（新增分支仅在策略启用时进入）：
if a.think != nil {
	a.think.FinishRound(len(toolCalls), len([]rune(body)))
	a.think.SettleRound(body)
	if hit, nudge := a.shouldInterceptFinal(forcedFinal); hit {
		a.think.Inject(thinkmgr.KindVerify, "收尾拦截")
		messages = append(messages, types.Message{Role: "user", Content: nudge})
		continue // 不 break：进入验证轮
	}
}
break
// 同步路径（Run，agent.go:416-419）同构接入：同一 shouldInterceptFinal（budgetExceeded 传 false），
// 在 `a.Task.Payload = ...; break` 之前拦截；拦截时追加 nudge 并 continue。
```
**挂钩点**：H1（agent.go:1232-1286 无工具分支）、H2（agent.go:1284-1286 final break）、H4（agent.go:416-419 同步路径）、H7（agent.go:101-116 batchNudge 为注入形态模板）。
**测试断言**：zz_think_intercept_test.go——mock 后端首轮非空答案 → 断言 1 次拦截 + messages 多 1 条验证指令 + 最终回答来自验证轮；逃生通道表驱动（forcedFinal/上限/goal/simple 均不拦截）。
**零回归（diff 级）**：策略关闭 → 两个 `if a.think != nil` 短路 → break 路径与现状逐字一致（diff 仅新增 nil 判断）；simple 任务同样返回 false。验证：同一 mock 流开/关两遍，断言消息序列与请求数一致。
**风险**：验证轮死循环（MaxIntercepts + detectLoop 仍生效）；与 goal 打磨轮叠加（goal 硬排除）。

## 4. 验证轮协议（CoVe 形态）
**目标**：草稿 → 生成验证问题（独立）→ 独立作答 → 修订；把 goal 打磨轮泛化为 chat/task 最小形态。
**机制**：CoVe 四步落为一条注入指令（模型单轮内完成）；轮次结构由 harness 制造（业界共识"跨轮思考默认不保留"，survey §0.4）。与 goal 打磨轮（goalmgr.go:393/692，外部验收 polishVerifier runtime.go:621）关系：完整版 vs 最小版（模型自查），仅 chat/task，goal 不叠加。判定（**轮次计数为主、标记为辅**）：注入后非空回答=验证轮完成（确定性兜底）；[THINK_VERIFIED] 提前放行；[THINK_NEEDS_FIX] 记 refine 复验一次（受上限）。
**Go 接口草案**
```go
// DefaultVerifyTemplate 内置验证轮模板（%d=当前轮次；中文、四要素、可配置）。
const DefaultVerifyTemplate = `【验证轮 · 第 %d 轮】请对上面的结论做一次独立复核（不要复述原答案）：
1) 列出结论依赖的关键假设（≤3 条），逐条判断证据是否充分；
2) 挑最不确定的 2 个点，各提 1 个验证问题并独立作答（不引用原答案措辞）；
3) 发现矛盾或缺口：先修正，再输出修正后的完整最终答案，并以 [THINK_NEEDS_FIX] 开头说明修正点；
4) 全部验证通过：直接输出最终答案，以 [THINK_VERIFIED] 结尾。`
// BuildVerifyNudge 生成注入文本（tmpl 空时用内置模板）。
func BuildVerifyNudge(round int, tmpl string) string {
	if strings.TrimSpace(tmpl) == "" { tmpl = DefaultVerifyTemplate }
	return fmt.Sprintf(tmpl, round)
}
// 判定标记（模型输出侧约定；executor 轮末 SettleRound 解析）。
const (
	MarkVerified = "[THINK_VERIFIED]"  // 验证通过（提前放行）
	MarkNeedsFix = "[THINK_NEEDS_FIX]" // 发现并修正（进入 refine）
)
```
**挂钩点**：H1（agent.go:1232-1286 注入落点）、H15（goalmgr.go:607 buildContinuation 注入通道形态对齐）、H18（gate.go:138 Admit 门禁泛化时 Verified 作输入，预留）。
**测试断言**：nudge_test.go——默认模板含四要素、自定义模板生效、轮次格式化正确；标记解析（VERIFIED→Verified；NEEDS_FIX→refine 且保持未验证）。
**零回归/默认关闭**：注入仅发生在拦截分支（策略启用且复杂度过闸）；关闭时无注入、无标记解析。
**风险**：模型无视指令（计数兜底，标记缺失供 H17 后续用）；模板抬 token（≤200 字且低频注入）。

## 5. 思考轮次下限（复杂任务最少 N 轮）
**目标**：complex 最少 3 轮（首探+执行+验证）、moderate 最少 2 轮；下限不得突破 token 硬约束。
**机制**：与 goal MaxRounds 关系——goalmgr.go:34 DefaultMaxRounds=0 是"续跑上限（防失控）"，本下限是"思考下限（防草率）"；两者正交，本下限只作用于 executor 收尾拦截，不改 goal 状态机（goal 模式被排除，§3）。下限表（可配）：simple=1 / moderate=2 / complex=3。计量口径：**一轮 = 一次模型请求-响应循环**（即 agent.go:1060-1062 的 round）——不数工具调用、不数思考标记，口径唯一、与现有计数同源。优先级：**token 预算 > 轮次下限**——forcedFinal（agent.go:1041、1064-1078）置位后拦截永不触发；预算检查（budget.go:126）每轮先于拦截判定；压缩释放（budget.go:474）优先于拦截。上限护栏：MaxIntercepts（默认 3），对齐 overthinking 缓解（survey §8）。
**Go 接口草案**
```go
// Policy 复杂度 → 思考策略（由 Strategy 生成，executor 消费）。
type Policy struct {
	Enabled        bool       // false 时 executor 不构造 Tracker
	Complexity     Complexity // 任务复杂度
	MinRounds      int        // 最少思考轮数（含首轮）
	RequireVerify  bool       // 是否强制验证轮
	MaxIntercepts  int        // 拦截续轮上限
	FirstEffort    string     // 首轮 effort（""=不改，沿用全局）
	VerifyEffort   string     // 验证轮 effort（""=不改）
	VerifyTemplate string     // 验证轮模板（""=内置）
}
// Strategy 配置面（config.yaml thinking_policy 节 / settings.json 同名键持久化）。
type Strategy struct {
	Enabled           bool   `yaml:"enabled" json:"enabled"`
	ModerateMinRounds int    `yaml:"moderate_min_rounds" json:"moderate_min_rounds"`
	ComplexMinRounds  int    `yaml:"complex_min_rounds" json:"complex_min_rounds"`
	MaxIntercepts     int    `yaml:"max_intercepts" json:"max_intercepts"`
	FirstEffort       string `yaml:"first_effort" json:"first_effort"`
	VerifyEffort      string `yaml:"verify_effort" json:"verify_effort"`
	VerifyTemplate    string `yaml:"verify_template" json:"verify_template"`
}
// Normalize 默认值兜底（Enabled 零值=false，保持"关闭"语义）。
func (s Strategy) Normalize() Strategy {
	if s.ModerateMinRounds <= 0 { s.ModerateMinRounds = 2 }
	if s.ComplexMinRounds <= 0 { s.ComplexMinRounds = 3 }
	if s.MaxIntercepts <= 0 { s.MaxIntercepts = 3 }
	return s
}
// BuildPolicy 生成任务级策略（复杂度 → 轮数/验证映射）。
func BuildPolicy(s Strategy, c Complexity) Policy {
	s = s.Normalize()
	p := Policy{Enabled: s.Enabled, Complexity: c, MaxIntercepts: s.MaxIntercepts,
		FirstEffort: s.FirstEffort, VerifyEffort: s.VerifyEffort, VerifyTemplate: s.VerifyTemplate}
	switch c {
	case ComplexityComplex: p.MinRounds, p.RequireVerify = s.ComplexMinRounds, true
	case ComplexityModerate: p.MinRounds, p.RequireVerify = s.ModerateMinRounds, true
	default: p.MinRounds, p.RequireVerify = 1, false
	}
	return p
}
// effort 切换复用已有 API：SetReasoningEffort（deepseek.go:961）、ReasoningEffort（deepseek.go:987）。
// executor 首轮 FirstEffort / 验证轮 VerifyEffort（空=跳过），任务结束恢复；后端未实现则类型断言静默跳过。
type EffortSwitcher interface {
	SetReasoningEffort(level string)
	ReasoningEffort() string
}
```
**挂钩点**：H3（轮次计数=计量口径）、H14（runtime.go:2717 goal 侧读取快照，预留）、H16（goalmgr.go:256 完成判定接入点，预留）、H24（budget.go:126/163 预算信号）。
**测试断言**：下限表驱动（complex 第 1/2 轮拦截、第 3 轮验证后放行；moderate 2 轮）；预算优先级（forcedFinal=true → 0 拦截）；effort 切换 mock 断言调用序列（首轮/验证轮/恢复）。
**零回归/默认关闭**：下限仅参与 ShouldInterceptFinal 判定（§3 已论证关闭即短路）；DefaultMaxRounds 等 goal 侧常量零改动。
**风险**：下限抬成本（simple 直通 + 可关）；与 goal 打磨轮叠加膨胀（goal 排除）。

## 6. 配置面与实施清单
**配置草案**（config.example.yaml:152 cli_defaults 同区新增；settings.json 同名键持久化，模式同 "effort" 键 settings.go:13/41）：
```yaml
# 多轮思考触发策略（默认关闭；开启后复杂问题收尾前强制验证轮）
thinking_policy:
  enabled: false           # 总开关（false=与旧行为逐字一致）
  moderate_min_rounds: 2   # 中等复杂任务最少思考轮数
  complex_min_rounds: 3    # 复杂任务最少思考轮数（首探+执行+验证）
  max_intercepts: 3        # 收尾拦截续轮上限（防过度思考）
  first_effort: ""         # 首轮 effort 档位（""=不改，沿用 cli_defaults.effort:162）
  verify_effort: "high"    # 验证轮 effort 档位（""=不改；经 SetReasoningEffort:961）
  verify_template: ""      # 验证轮指令模板（""=内置中文模板）
```
**文件改动清单**：新增 internal/thinkmgr/{thinkmgr,assessor,policy,nudge}.go（模型/评估器/策略/注入）；修改 internal/executor/agent.go（字段 think、SetThinkPolicy、shouldInterceptFinal、1060-1062 登记、1232-1286 与 416-419 接入、轮末 SettleRound）；修改 internal/runtime/runtime.go（装配处 904/2067/3212 注入 SetThinkPolicy；TraceThinkRound 可选接口实现）；修改 internal/runtime/settings.go（13/41 持久化）；修改 internal/trace/trace.go（EventThinkRound 常量，Entry 结构不变）；可选 pkg/types/goal.go:117 追加轻量思考字段。
**实施顺序**：P0 thinkmgr 包+单测 → P0 RunStream 拦截接入（1284-1286）+集成测试 → P1 同步路径（416-419）+子 Agent → P1 effort 切换（961/987）+配置持久化 → P2 goal 对接（H14/H16）与 UI（H19，另稿）。
**验收指标（V）**：V1 策略关闭既有测试全绿且无 think_round（`go test ./internal/executor/ ./internal/runtime/`）；V2 复杂任务首轮作答被拦截、验证后放行（thinkmgr/zz_think_intercept 测试）；V3 simple 零拦截（表驱动）；V4 逃生通道均不拦截（表驱动）；V5 effort 覆盖仅首轮/验证轮且结束恢复（mock 断言）。

## 7. H1-H25 对应总表
| H | 落点（实测） | 本稿覆盖 |
|---|---|---|
| H1 | agent.go:1232-1286 | ✅ 收尾拦截注入点 |
| H2 | agent.go:1284-1286 | ✅ 拦截判定 |
| H3 | agent.go:1060-1062 | ✅ 轮次登记/下限口径 |
| H4 | agent.go:416-419 | ✅ 同步路径接入 |
| H5/H6 | agent.go:607/193-194 | ⛔ 提示词侧（另稿） |
| H7 | agent.go:101-116 | ✅ thinkNudge 形态复用 |
| H8 | agent.go:2025 | ✅ 计数入口 |
| H9 | helpers.go:291 | ⛔ 展示兜底（另稿） |
| H10-H12 | deepseek.go:961/987/1078；runtime.go:1108 | ◐ effort 覆盖接口（本稿定义，参数侧落地） |
| H13 | runtime.go:2962 | ⛔ 评估续跑（另稿） |
| H14 | runtime.go:2717 | ◐ goal 快照读取（预留） |
| H15 | goalmgr.go:607 | ◐ 注入形态对齐 |
| H16 | goalmgr.go:256 | ◐ 完成判定接入点（预留） |
| H17 | bias.go:64 | ⛔ 度量（另稿；verified=false 可作输入） |
| H18 | gate.go:138 | ◐ 出口门禁输入信号（预留） |
| H19 | tui.go:427-431；gui/server.go:1429-1431 | ⛔ UI（另稿） |
| H20 | handoff.go:95/100 | ⛔ 多 Agent（另稿） |
| H21 | trace.go:21-25；runtime.go:2795 | ✅ think_round 事件 |
| H22 | helpers.go:64；spec.go:167 | ✅ 评估信号接入 |
| H23 | settings.go:13/41；config.example.yaml:152 | ✅ 配置扩展 |
| H24 | budget.go:126/163 | ✅ 预算优先级 |
| H25 | task.go:191 | ⛔ 多 Agent（另稿） |

## 8. 行号核验与漂移说明
核验方式：Select-String 精确匹配 + read_file 抽查（本工作区版本）。与《现状盘点》漂移（以本稿为准）：spec.go DetectFuzziness 实测 167（盘点 178）；SpecGate.Check 实测 102（盘点 110-150）；overseer.go Evaluate 实测 44（盘点 47）。本稿未改动任何 .go 文件；产出为设计文档。
