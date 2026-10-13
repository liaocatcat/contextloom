# ContextLoom 改造详细设计：多轮思考触发

> **读者**：ContextLoom 核心开发者（executor / runtime / model / config / ctxmgr / overseer / tui / gui 维护者）。
> **目标**：把"复杂问题杜绝一次思考解决、鼓励并积极触发多次思考"展开为可直接排期实施的接口级设计——12 项机制 M1-M12（思考轮模型 / 复杂度路由 / 收尾拦截 / 验证轮 / 轮次下限 / 续思注入 / 轮次级 effort / 计划门 / 验证子代理 / 思考链保留 / 度量 / 过度思考护栏）。
> **范围**：轮次面（"想几轮 / 是否验证"），与参数面《contextloom改造详细设计-思考与长度释放.md》（"每轮想多长"）正交衔接；对标 Claude extended thinking / OpenAI effort / CoVe / s1 / sequential-thinking / Claude Code plan mode / dsh goal-round-driver 等（§7.1）。
> **口径**：现状取自仓库源码实测（文件 + 函数 + 约行号，H1-H25 挂钩点体系）；参数面落码现状以《research/design-e-config-ui.md》§0.1 为准（其差异清单为本文零回归基线）；目标接口为完整 Go 定义（紧凑排版，与 gofmt 展开等价），可直接落码。
> **关联**：《research/contextloom-thinking-inventory.md》（H1-H25 盘点）、《research/industry-multi-pass-thinking-survey.md》（业界 Top 10）；分册草案 design-c（触发）/ design-d（鼓励）/ design-e（配置 UI）已并入本文，独立草案不再维护。

## 0 总览

### 0.1 机制总表（M1-M12）

| 编号 | 改造项 | 一句话目标 | 业界参照 | 依赖 |
|---|---|---|---|---|
| M1 | 思考轮模型与追踪器 | "思考轮"成为一等概念（类型化/可判定），thinkmgr 包承载 | dsh goal-round-driver | 无（先做） |
| M2 | 复杂度评估器 | 确定性评分（0-100）路由：复杂任务才启用多轮 | RouteLLM / Snell / overthinking 研究 | M1 |
| M3 | 收尾拦截机制 | 复杂任务收尾前必过验证轮（"第一轮非空即结束"被拦截） | s1 budget forcing / dsh 门控 | M1+M2 |
| M4 | 验证轮协议（CoVe 形态） | 草稿→独立验证问题→独立作答→修订（单轮内完成） | CoVe / Self-Refine | M3 |
| M5 | 思考轮次下限 | 复杂任务最少 N 轮（防草率），token 红线绝对优先 | Anthropic budget_tokens / dsh round 预算 | M3 |
| M6 | thinkNudge 续思注入 | 结构性催轮：harness 注入提示而非等模型自觉 | sequential-thinking / batchNudge | M3 |
| M7 | 轮次级 effort 升级 | 首轮不降档→验证轮深挖（轮次临时层，不写回配置） | Claude interleaved thinking / OpenAI effort | M4 |
| M8 | 计划门（plan gate） | 复杂任务先出计划再执行，计划为验证轮提供基准 | Claude Code plan mode / TodoWrite | M2 |
| M9 | 验证子代理默认化 | 关键产出默认送独立子代理复核（只读评审） | Claude Code subagent / Devin | M4 |
| M10 | 思考链保留策略 | 压缩不侵蚀多轮思考记忆：验证轮保护 + 摘要注入 | Anthropic thinking blocks 回传 | M4 |
| M11 | 思考度量与偏差检测 | 轮次/触发/收益可观测，新增"草率回答"偏差 | metering / anti-cheat 实测比对 | M1 |
| M12 | 过度思考护栏 | 简单任务零触发 + 复杂任务止损上限 | overthinking 缓解（Chen et al.） | M2+M6 |

### 0.2 术语表（本文口径）

- **思考轮**：一次"模型请求 → 产出 → 本轮结束"循环；与执行轮 `round`（`agent.go:1060-1062`）**1:1 同序**，不新增第二套计数器（防漂移）。类型：`explore` 首探 / `execute` 执行 / `verify` 验证 / `refine` 修订；复杂度：二级 `simple/complex`（评分 0-100，≥阈值=complex）。
- **收尾拦截**：在"非空回答且未调工具"收尾点（`agent.go:1284-1286`、`416-419`）不放行、注入验证指令续轮；与 nudge 合计称**续轮注入**（统一受 `max_think_rounds` 上限）；**验证轮**：对草稿做独立复核的轮次（CoVe 四要素，§3.1 模板）。
- **轮次临时层**：仅单次请求生效的 effort 覆盖（`SetNextEffort`，不写回）；**零回归**：`policy=off` 时请求体 / 消息序列 / settings / UI 与当前落码版本逐字一致。

### 0.3 统一配置面（thinking.* 9 字段，权威）

design-c 的 `thinking_policy.enabled`（bool）、design-d 的 `think_policy.*` 结构 → 统一为 design-e 的顶层 `thinking.*` 分组（9 字段）：

| # | YAML key | 类型 | 范围 | 默认 | 一句话语义 |
|---|---|---|---|---|---|
| 1 | `thinking.policy` | string | off/auto/strict | ""→off | 总开关：off=现状；auto=复杂任务软触发；strict=复杂任务硬拦截 |
| 2 | `thinking.min_think_rounds` | int | 0-20 | 0 | 复杂任务最少思考轮数（strict 硬下限/auto 软下限；0=不强制） |
| 3 | `thinking.max_think_rounds` | int | 1-20 | 3 | 续轮注入总上限（拦截+nudge；工具执行轮不受限） |
| 4 | `thinking.verify_pass` | string | off/auto/on | ""→off | 验证轮：off=无；auto=复杂触发；on=收尾必验（含 simple） |
| 5 | `thinking.complexity_threshold` | int | 0-100 | 60 | 复杂度评分阈值（≥阈值=复杂） |
| 6 | `thinking.nudge_enabled` | bool | true/false | false | 续思提示注入开关（M6） |
| 7 | `thinking.nudge_max` | int | 0-10 | 2 | nudge 单独上限 |
| 8 | `thinking.round_effort_policy` | string | none/escalate | ""→none | 轮次档位：none=保持（现状）；escalate=验证轮升档 |
| 9 | `thinking.round_effort_cap` | string | low/medium/high/xhigh/max | ""→max | 升档封顶（仅 escalate 生效） |

其余细化参数（验证模板、nudge 间隔/短答阈值、止损轮数、保护轮数、复核清单/超时）**首版以包内常量固定、不进配置面**，待 M11 度量数据验证收益后再评估暴露。

### 0.4 依赖与顺序

`M1 → M2 → M3 → {M4, M5, M6} → M7 → M8 → M9 → M10`；`M11` 依赖 M1；`M12` 依赖 M2+M6。每步独立可交付、可回滚（§5），全部默认关闭。

## 1 问题定义：一次思考问题的根因与目标

### 1.1 十二条实锤（承接盘点，压缩）

| # | 实锤 | 证据（实测行号） |
|---|---|---|
| 1 | RunStream 收尾直通车：第一轮非空回答 = 任务完成 | `agent.go:1284-1286` |
| 2 | 同步路径更激进：`len(toolCalls)==0` 直接 break（子 Agent 全走此路径） | `agent.go:416-419` |
| 3 | chat 提示词鼓励停止："任务完成后直接给出最终答案并停止" | `agent.go:194` |
| 4 | 终止条件清单无"质量不足"项（5 条全是防失控/防死循环） | `agent.go:1053-1059` |
| 5 | 无最小思考轮数；`RunStreamWithBudget` 的 `maxIter` 被显式弃用 | `agent.go:1061`、`1768-1774` |
| 6 | task 模式监工评分不触发续跑（仅展示） | `runtime.go:2022-2039`、`tui.go:400-415` |
| 7 | goal 续跑依赖协议标记而非思考质量 | `goalmgr.go:417-435` |
| 8 | 空正文提示只保底不提质（`noToolRounds>=2` 才触发） | `agent.go:1232-1247` |
| 9 | 无复杂度→思考策略映射（`estimateComplexity` 只服务计划节点） | `helpers.go:64`、`runtime.go:2036` |
| 10 | 思考强度全局静态（会话级单值，无轮次升级） | `settings.go:24,50-52`、`deepseek.go:1078` |
| 11 | 无强制验证轮（唯一闭环=goal 打磨轮，需 `[GOAL_DONE]` 触发） | `goalmgr.go:388-420` |
| 12 | handoff verifier 需模型主动调用，无默认复核流程 | `handoff.go:100-147` |

**共同根因**：系统只把"轮次"当循环计数（`round`），没有"思考轮"这一等概念——无下限（防草率）、无验证（防漏错）、无路由（防过度）；修复必须发生在轮次结构上，而非提示词（业界共识：跨轮思考默认不保留，DeepSeek reasoning_content 不回传）。

### 1.2 目标状态定义

- **复杂任务**（评估器判定）：收尾前必过验证轮（strict）；最少 N 轮可配；验证轮 effort 升档；关键产出默认送独立复核；计划先外化。
- **简单任务**：与现状逐字一致（零注入、零升档、零送审）；**默认** `policy=off` 时全部新路径短路——释放的是"能力"，不是"新行为"。

### 1.3 与参数面的关系（落码现状基线）

参数面解决"每轮想多长"（effort / 输出上限 / 输入预算，已落码）；本文解决"想几轮、是否验证"。共用挂钩点 H10-H12/H23，**只消费不修改**其语义。零回归基线以 design-e §0.1 实测为准，关键差异：① `generationOverride` 未落码（会话覆盖=直写 `CLIDefaults.Effort` + settings `effort` 键）；② `model.EffectiveInputBudget` 双边收紧未落码（runtime 简化版）；③ `cli_defaults.effort` 枚举仍五档（无 off），但 `/effort` 与 GUI model.set 接受 off；④ `applyDefaults` 把 `ThinkingEnabled` 缺省补 `&true`——**零回归基线=当前落码行为（含此语义）**；⑤ `thinking_enabled=false` 与 `thinking_length` 互斥为软裁决（发送矩阵），无硬校验。

## 2 目标设计：多轮思考机制体系

### 2.1 思考轮模型（M1）

- **类型流转**：`explore`（第 1 轮）→ `execute`（常规推进）→ `verify`（拦截/nudge 注入后）→ `refine`（`[THINK_NEEDS_FIX]` 触发）→ 复验。
- **三层状态载体**（单一事实源）：① Agent 内存字段 `think *thinkmgr.Tracker`（拦截决策唯一事实源；nil=关闭）；② trace 事件 `think_round`（观测；复用 `trace.Entry` 既有 Round/Event 字段）；③ goal 状态轻量字段（仅 goal 模式消费，预留）。

### 2.2 复杂度路由（M2）

评分表（确定性信号，总分 0-100）：

| 信号 | 条件 | 分值 |
|---|---|---|
| 任务长度 | ≥120 runes / ≥400 runes | +20 / 再 +20（累计 40） |
| 模糊度（`DetectFuzziness` 命中数，`spec.go:167` 实测） | ≥1 / ≥2 / ≥3 | +10 / 再 +10 / 再 +10（累计 30） |
| 模式 | goal / task 且命中多目标词（同时/分别/并行/并且/以及/多个/批量） | +10 / +10 |
| 图片 | >0 | +10 |
| 显式标记 | "complex" / "simple" | 直通（100 / 0） |

**路由规则**：`score ≥ complexity_threshold`（默认 60）→ `complex`；否则 `simple`。design-c 的三级（simple/moderate/complex）收敛为二级（单阈值 + 单 `min_think_rounds`，评分保留展示用途）；阈值首版保守、由 M11 度量数据校准。

### 2.3 触发-验证-放行流程

```
任务创建：评估复杂度一次 → off/simple 不构造 Tracker（现状路径）；complex 且 policy≠off 构造 Tracker（strict=硬/auto=软）
主循环每轮：BeginRound 登记类型 →（escalate）applyRoundEffort 下发验证轮档位 → 请求 → 产出 → FinishRound/SettleRound
收尾点：strict 且（未达下限或未验证）且未超续轮上限 → 注入 CoVe 验证指令 continue（拦截）
        nudge/verify 命中（频控内） → 注入续思/验证指令 continue；否则 break（现状收尾）
放行（任一）：验证完成且达下限 / 续轮上限 / token 红线（forcedFinal）/ 中断 / policy=off / simple
```

**拦截条件（AND）**：① 策略启用且 strict；② complex；③ 轮数<MinRounds 或未验证；④ 续轮注入数 < `max_think_rounds`；⑤ 非 forcedFinal；⑥ 非 goal 模式（goal 已有打磨轮/门禁，不叠加）。

### 2.4 关键不变式

1. **零回归**：`policy=off` → `a.think==nil` → 全部新分支短路；消息序列、请求数、settings、状态栏与当前版本逐字一致。
2. **条件路由**：简单任务零注入/零升档/零送审；误判代价由"显式标记直通 + 阈值可配 + M11 校准"封顶；goal 模式硬排除（不叠加）。
3. **上限护栏**：续轮注入总数 ≤ `max_think_rounds`（默认 3）；nudge 另受 `nudge_max` 限；止损（连续 2 轮无新证据）停止催轮；token 红线绝对优先。
4. **叠加不替换**：轮次临时层（M7）单请求生效、自过期；不改 L1/L2/L3 持久值；全局档位变更仍走 `applyGenerationParams` 统一出口（参数面调用铁律）。

## 3 接口级设计

### 3.1 internal/thinkmgr 新包（M1：Tracker/Complexity/Policy 完整 Go 定义）

**设计目标**：把"思考轮"落成类型化、可判定、单一事实源的一等概念；纯标准库依赖（fmt/strings/regexp/time），可独立单测。以下代码为紧凑排版（与 gofmt 展开等价，可直接落码）。

```go
// Package thinkmgr 多轮思考机制：思考轮模型、复杂度评估、任务级策略、验证/续思协议与护栏。
// 默认关闭（Policy=off）时 executor 不构造 Tracker，全部新分支短路——零回归铁律的落点。
package thinkmgr

// ── 轮次模型（M1）──
type Kind string

const (
	KindExplore Kind = "explore" // 首探轮
	KindExecute Kind = "execute" // 执行轮
	KindVerify  Kind = "verify"  // 验证轮（CoVe）
	KindRefine  Kind = "refine"  // 修订轮
)

// ThinkRound 一次思考轮记录（与执行轮 round 1:1 同序；json tag 供 trace 上报）。
type ThinkRound struct {
	Round int; Kind Kind; HasTools bool; ToolN, BodyLen int; Reason string; TS time.Time
}

// State 状态快照：score 评分 / complexity 分级 / rounds 轮次表 / intercepts 拦截数 / nudges 续思数 / verified 验证通过。
type State struct {
	Score int; Complexity Complexity; Rounds []ThinkRound; Intercepts, Nudges int; Verified bool
}

// Tracker 思考轮追踪器（每任务一个；策略关闭时不构造）。
type Tracker struct {
	policy   Policy
	state    State
	kindNext Kind // 下一轮预设类型（注入后为 verify/refine，只生效一次）
}

func NewTracker(p Policy, score int, c Complexity) *Tracker {
	return &Tracker{policy: p, state: State{Score: score, Complexity: c}, kindNext: KindExplore}
}

// BeginRound 登记本轮并返回类型（预设只生效一次）；FinishRound 回填末条 ToolN/HasTools/BodyLen。
func (t *Tracker) BeginRound() Kind {
	k := t.kindNext
	if k == KindVerify || k == KindRefine { t.kindNext = KindExecute }
	t.state.Rounds = append(t.state.Rounds, ThinkRound{Round: len(t.state.Rounds) + 1, Kind: k, TS: time.Now()})
	return k
}
func (t *Tracker) FinishRound(toolN, bodyLen int) { /* 回填末条 */ }

// SettleRound 轮末结算（计数为主、标记为辅）：MarkVerified 标记→提前放行；verify 轮非空回答→
// 兜底通过；[THINK_NEEDS_FIX]→末条降级 KindRefine 待复验（逻辑见左注释，标记常量见本包 nudge 段）。
func (t *Tracker) SettleRound(body string) { /* 见左注释 */ }

// ShouldInterceptFinal 收尾拦截判定（M3/M5）；budgetExceeded=token 红线（永不拦截）。
func (t *Tracker) ShouldInterceptFinal(budgetExceeded bool) (bool, string) {
	if t.policy.Mode != "strict" { return false, "" } // auto=软路由，不硬拦截
	if budgetExceeded { return false, "token 预算红线优先" }
	if t.state.Intercepts+t.state.Nudges >= t.policy.MaxExtraRounds { return false, "已达续轮上限" }
	n := len(t.state.Rounds)
	needRounds := t.policy.MinRounds > 0 && n < t.policy.MinRounds
	needVerify := t.policy.RequireVerify && !t.state.Verified
	if !needRounds && !needVerify { return false, "已达最少轮数且验证通过" }
	reason := "收尾拦截："
	if needRounds { reason += fmt.Sprintf("轮数 %d<%d；", n, t.policy.MinRounds) }
	if needVerify { reason += "未完成验证轮" }
	return true, reason
}

// ShouldNudge 软注入判定（M4/M6）：续思提示或验证指令的注入决策。
func (t *Tracker) ShouldNudge(round, bodyRunes, lastNudgeRound int) (bool, string) {
	if t.state.Intercepts+t.state.Nudges >= t.policy.MaxExtraRounds { return false, "已达续轮上限" }
	if t.policy.VerifyPass == "on" && !t.state.Verified && t.state.Nudges == 0 { return true, "验证轮（verify_pass=on）" }
	if !t.policy.NudgeEnabled || t.state.Nudges >= t.policy.NudgeMax { return false, "nudge 上限或关闭" }
	if lastNudgeRound > 0 && round-lastNudgeRound < t.policy.NudgeGap { return false, "间隔不足" }
	if round >= t.policy.MinRounds && bodyRunes >= t.policy.NudgeMinRunes { return false, "轮数达标且回答充分" }
	return true, "续思提示"
}

// Inject/InjectNudge 记录注入并预设下一轮类型（verify）；其余访问器见名知义。
func (t *Tracker) Inject(kind Kind, reason string) { t.state.Intercepts++; t.kindNext = kind }
func (t *Tracker) InjectNudge(reason string)       { t.state.Nudges++; t.kindNext = KindVerify }
func (t *Tracker) MarkVerified()                   { t.state.Verified = true }
func (t *Tracker) Policy() Policy                  { return t.policy }
func (t *Tracker) Snapshot() State                 { return t.state }

// ── 复杂度评估（M2）──
type Complexity string

const (
	ComplexitySimple  Complexity = "simple"  // 现状单轮路径
	ComplexityComplex Complexity = "complex" // 触发多轮机制
)

// Signals 评估输入（全部确定性信号）：TaskText 任务文本 / Mode chat|task|goal /
// FuzzyHits DetectFuzziness 命中数（0-3；spec.go:167）/ ImageN 图片数 / Explicit 显式标记（auto 直通）。
type Signals struct {
	TaskText string; Mode string; FuzzyHits, ImageN int; Explicit string
}

// Assessor 复杂度评估器（确定性、可解释、阈值可配；默认 60）。
type Assessor struct{ Threshold int }

func NewAssessor(threshold int) *Assessor { if threshold <= 0 { threshold = 60 }; return &Assessor{Threshold: threshold} }

var multiGoalRe = regexp.MustCompile(`(同时|分别|并行|并且|以及|多个|批量)`)

// Score 复杂度评分（0-100；评分表见 §2.2；Explicit 直通 100/0）。
func (a *Assessor) Score(s Signals) int {
	switch strings.ToLower(strings.TrimSpace(s.Explicit)) {
	case "complex":
		return 100
	case "simple":
		return 0
	}
	score := 0
	if n := len([]rune(s.TaskText)); n >= 120 {
		score += 20
		if n >= 400 { score += 20 }
	}
	if s.FuzzyHits > 0 { score += 10 * min(s.FuzzyHits, 3) }
	if s.Mode == "goal" { score += 10 }
	if s.Mode == "task" && multiGoalRe.MatchString(s.TaskText) { score += 10 }
	if s.ImageN > 0 { score += 10 }
	if score > 100 { score = 100 }
	return score
}

// Assess 分级：score >= Threshold → complex（否则 simple）。
func (a *Assessor) Assess(s Signals) Complexity {
	if a.Score(s) >= a.Threshold { return ComplexityComplex }
	return ComplexitySimple
}

// ── 策略（M1/M2 汇合点）──
// Strategy 轮次面策略输入（runtime 从 config.ThinkingConfig 装配；与配置面 9 字段同构）。
type Strategy struct {
	Policy string // off/auto/strict
	MinRounds, MaxThinkRounds int // 最少思考轮数（0=不强制）/ 续轮注入总上限（0→3）
	VerifyPass string // off/auto/on
	ComplexityThreshold int // 0-100（0→60）
	NudgeEnabled bool; NudgeMax int // 0→2
	RoundEffortPolicy, RoundEffortCap string // none|escalate；low..max（""→max）
}

// Normalize 零值兜底（不改变 off 语义：Policy==""→off）。
func (s Strategy) Normalize() Strategy {
	if s.Policy == "" { s.Policy = "off" }
	if s.MaxThinkRounds <= 0 { s.MaxThinkRounds = 3 }
	if s.ComplexityThreshold <= 0 { s.ComplexityThreshold = 60 }
	if s.NudgeMax <= 0 { s.NudgeMax = 2 }
	if s.RoundEffortPolicy == "" { s.RoundEffortPolicy = "none" }
	if s.RoundEffortCap == "" { s.RoundEffortCap = "max" }
	return s
}

// Policy 任务级策略（executor 消费）：MinRounds strict 硬下限/auto 软下限；MaxExtraRounds=拦截+nudge
// 合计上限；NudgeGap=2/NudgeMinRunes=200/StallRounds=2 为常量；VerifyEffort 仅 escalate 时=cap。
type Policy struct {
	Enabled bool; Mode string; Complexity Complexity; Score int
	MinRounds, MaxExtraRounds int; RequireVerify bool
	VerifyPass, VerifyEffort string
	NudgeEnabled bool; NudgeMax, NudgeGap, NudgeMinRunes, StallRounds int
}

// BuildPolicy 生成任务级策略（简单任务全零 → executor 不注入）。
func BuildPolicy(s Strategy, c Complexity, score int) Policy {
	s = s.Normalize()
	p := Policy{Enabled: s.Policy != "off", Mode: s.Policy, Complexity: c, Score: score,
		MaxExtraRounds: s.MaxThinkRounds, VerifyPass: s.VerifyPass, NudgeEnabled: s.NudgeEnabled,
		NudgeMax: s.NudgeMax, NudgeGap: 2, NudgeMinRunes: 200, StallRounds: 2}
	if c != ComplexityComplex {
		return p // 简单任务：零注入、零升档、零送审（M12 条件路由）
	}
	if s.Policy == "strict" { p.MinRounds, p.RequireVerify = s.MinRounds, s.VerifyPass != "off" }
	if s.RoundEffortPolicy == "escalate" { p.VerifyEffort = s.RoundEffortCap }
	return p
}

// ── 验证/续思协议（M4/M6）──
// DefaultVerifyTemplate 内置验证轮模板（CoVe 四要素；%d=轮次；中文、可配置）。
const DefaultVerifyTemplate = `【验证轮 · 第 %d 轮】请对上面的结论做一次独立复核（不要复述原答案）：
1) 列出结论依赖的关键假设（≤3 条），逐条判断证据是否充分；
2) 挑最不确定的 2 个点，各提 1 个验证问题并独立作答（不引用原答案措辞）；
3) 发现矛盾或缺口：先修正，再输出修正后的完整最终答案，并以 [THINK_NEEDS_FIX] 开头说明修正点；
4) 全部验证通过：直接输出最终答案，以 [THINK_VERIFIED] 结尾。`

// ContinueTemplates 续思模板（nudge 轮换）；BuildVerifyNudge 生成注入文本（tmpl 空=内置）。
var ContinueTemplates = []string{
	"【续思提示】请对上述结论做一次独立验证：列出关键假设并逐一核对，可调用工具查证；若结论不变，请说明验证过程后收尾。",
	"【续思提示】换一个角度复核：若上述结论有错，最可能错在哪里？请给出复核结论或补充边界条件。",
}

func BuildVerifyNudge(round int, tmpl string) string {
	if strings.TrimSpace(tmpl) == "" { tmpl = DefaultVerifyTemplate }
	return fmt.Sprintf(tmpl, round)
}

// 判定标记（模型输出侧约定；executor 轮末 SettleRound 解析）。
const (
	MarkVerified = "[THINK_VERIFIED]"  // 验证通过（提前放行）
	MarkNeedsFix = "[THINK_NEEDS_FIX]" // 发现并修正（进入 refine）
)

// ── 护栏（M12）──
// ThinkSignals 单轮证据信号（主循环确定性采集）；ShouldStopNudging 边际收益检测：连续 stallRounds
// 轮无新证据（无工具调用、结果摘要与正文无变化）→ 停止催轮（same→stallCount++，否则清零）。
type ThinkSignals struct {
	ToolCalls int; ResultDigest string; BodyRunes int
}

func ShouldStopNudging(prev, cur ThinkSignals, stallCount, stallRounds int) (bool, int) {
	same := cur.ToolCalls == 0 && cur.ResultDigest == prev.ResultDigest && cur.BodyRunes == prev.BodyRunes
	if same { stallCount++ } else { stallCount = 0 }
	return stallCount >= stallRounds, stallCount
}
```

**挂钩点**：H3（`agent.go:1060-1062` 轮次登记口径）、H7（`agent.go:101-116` 注入形态母本）、H8（`agent.go:2025` 计数入口）、H21（`trace.go:21-25` 事件常量）、H22（`helpers.go:64`/`spec.go:167` 只读复用）。**测试断言**：类型流转表驱动（explore→execute→verify→refine）；`ShouldInterceptFinal` 逃生通道表驱动；`assessor_test.go` ≥10 组（§6.1 #3）；模板/标记解析。**零回归论证**：策略关闭→不构造 Tracker→零新事件、零新消息、零新请求。**风险**：双计数漂移（复用 `round`，不新增计数器）；中文关键词脆弱（词表集中为常量）。

### 3.2 internal/executor 层（M3/M4/M5/M6/M8）

**设计目标**：在收尾点（"非空回答且未调工具"）加一道策略门——策略关闭时与现状逐字一致。

```go
// internal/executor/agent.go（修改）：Agent 新增字段（与 SetTracer 同列注入）：
// think *thinkmgr.Tracker（nil=关闭，零开销）/ roundEffort func(kind string)（M7 钩子）/
// lastNudgeRound int（频控）/ planOutlined bool + planOutline string（M8）/ verifierDone bool（M9）。

func (a *Agent) SetThinkPolicy(t *thinkmgr.Tracker) { a.think = t } // nil 安全
func (a *Agent) SetRoundEffortHook(fn func(kind string)) { a.roundEffort = fn }

// thinkGate 收尾点注入判定（RunStream agent.go:1284-1286 / Run agent.go:416-419 共用）。
// 返回 (追加后的 messages, 是否续轮)；策略关闭/简单任务/逃生通道 → (原样, false)。
func (a *Agent) thinkGate(messages []types.Message, body string, round int, forcedFinal bool) ([]types.Message, bool) {
	if a.think == nil {
		return messages, false // 策略关闭：与现状逐字一致
	}
	// ① strict 硬拦截（M3/M5）：最少轮数/验证轮未达 → 注入 CoVe 验证指令
	if hit, reason := a.think.ShouldInterceptFinal(forcedFinal); hit {
		a.think.Inject(thinkmgr.KindVerify, reason)
		return append(messages, types.Message{Role: "user", Content: thinkmgr.BuildVerifyNudge(round+1, "")}), true
	}
	// ② 软注入（M4/M6）：nudge / verify_pass 命中 → 续思或验证指令
	if hit, reason := a.think.ShouldNudge(round, len([]rune(body)), a.lastNudgeRound); hit {
		content := thinkmgr.BuildVerifyNudge(round+1, "")
		if a.think.Policy().VerifyPass == "off" {
			tpls := thinkmgr.ContinueTemplates
			content = tpls[a.think.Snapshot().Nudges%len(tpls)]
		}
		a.think.InjectNudge(reason)
		a.lastNudgeRound = round
		return append(messages, types.Message{Role: "user", Content: content}), true
	}
	return messages, false
}
```

**注入点 diff（默认路径逐字保留）**：RunStream（`agent.go:1284-1286`）改为 `if msgs, cont := a.thinkGate(...); cont { messages = msgs; continue }` 后再 `break`；同步路径（`416-419`）同构接入（budgetExceeded 传 false），在 `a.Task.Payload = ...; break` 之前拦截。轮次登记与结算：主循环入口（`1060-1062`）`BeginRound` + roundEffort 钩子；每轮结束 `FinishRound` + `SettleRound` + trace `think_round`（M11）。

**计划门（M8）**：`planGateInject(messages, round)` 复杂任务首轮注入"【执行计划】1. …（可验证步骤）2. …【计划完】"（soft，默认）；`planGateFilterTools(tools, mode, planned)` 硬门（可选，默认 soft=恒等返回）；提示词条件化（H6）：`agent.go:194`"任务完成后直接给出最终答案并停止"→"简单问题直接回答；复杂问题先列计划、执行后自查再收尾"（开关控制）；task strategy（H5，`agent.go:607`）追加计划门条款。

**挂钩点**：H1（`agent.go:1232-1286`）、H2（`1284-1286`）、H3、H4（`416-419`）、H5（`607`）、H6（`193-204`）、H7（`101-116`）。**测试断言**：mock 首轮非空→1 次拦截 + 多 1 条验证指令 + 最终回答来自验证轮；逃生通道表驱动（forcedFinal/上限/goal/simple 均不拦截）。**零回归论证**：`a.think==nil` 短路→break 路径与现状逐字一致（diff 仅新增 nil 判断）；同一 mock 流开/关两遍断言消息序列与请求数一致。**风险**：验证轮死循环（续轮上限 + detectLoop 仍生效）；与 goal 打磨轮叠加（goal 硬排除）。

### 3.3 internal/model 层（M7）

**设计目标**：把静态全局档位升级为"按轮次类型动态下发"，且不破坏参数面三层优先级。

```go
// internal/model/deepseek.go（修改）：DeepSeekBackend 新增字段 nextEffort string（轮次临时层；""=无）。
// SetNextEffort 一次性覆盖下一次请求 effort（发送后自动复位；空值=no-op）；与 SetReasoningEffort
// （全局档位，settings 持久化语义）正交：不改全局值、不持久化。
func (d *DeepSeekBackend) SetNextEffort(level string) { d.modelMu.Lock(); d.nextEffort = level; d.modelMu.Unlock() }

// currentEffort 解析本轮实际档位：临时层 > 全局档位。
func (d *DeepSeekBackend) currentEffort() string {
	d.modelMu.Lock()
	defer d.modelMu.Unlock()
	if d.nextEffort != "" { return d.nextEffort }
	return d.reasoningEffort
}
// applyThinkingParams（deepseek.go:1078）改造：effort 读取改用 currentEffort()；请求体构造完成后复位
// nextEffort=""（一次性语义）。发送矩阵四规则（off/nil/true/false）原样保留——升档仅向上（不发 off）。
```

**升级语义（escalate）**：验证轮（verify/refine）→ `SetNextEffort(cap)`（默认 max）；其余轮不覆盖（首轮不降档）。design-e 的"逐级升档"收敛为"验证轮直达封顶"——单档跳变更简单可测，业界证据（budget forcing / interleaved thinking）支持的是验证轮深挖而非连续爬档。**恢复**：临时层单请求自过期；全局档位变更仍走 `applyGenerationParams`（参数面调用铁律）。

**挂钩点**：H10（`deepseek.go:1078` 临时值读取）、H11（`deepseek.go:961` 不动其语义）。**测试断言**：httptest 断言 verify 轮请求体 `reasoning_effort=cap`；请求后复位；`escalate=none` 时请求体与现状逐字一致（含 thinking=disabled 不带 reasoning_effort 的互斥断言）。**零回归论证**：临时层不存在→解析链与现状逐字一致。**风险**：验证轮抬档增加时延/成本（上限 max、仅 complex、仅 verify 轮）。

### 3.4 internal/config 层（thinking.* 9 字段五处接线）

```go
// internal/config/config.go：Config 顶层新增 Thinking ThinkingConfig `yaml:"thinking"`。
// ThinkingConfig 多轮思考策略（9 字段；零值=off/现状语义，见 §0.3；yaml key 同字段名下划线形式）。
type ThinkingConfig struct {
	Policy string // ""|off|auto|strict
	MinThinkRounds, MaxThinkRounds int // ≥0（0=不强制）/ ≥1（0→运行时取 3）
	VerifyPass string // ""|off|auto|on
	ComplexityThreshold int // 0-100（0→运行时取 60）
	NudgeEnabled bool; NudgeMax int // ≥0（0→运行时取 2）
	RoundEffortPolicy, RoundEffortCap string // ""|none|escalate；""|low|medium|high|xhigh|max
}

// 选项清单（ConfigSchema/Sanitized/applyOne/Validate 共用，杜绝多处漂移）
var (
	thinkingPolicyOptions    = []string{"off", "auto", "strict"}
	verifyPassOptions        = []string{"off", "auto", "on"}
	roundEffortPolicyOptions = []string{"none", "escalate"}
	roundEffortCapOptions    = []string{"low", "medium", "high", "xhigh", "max"}
)
```

**接线① ConfigSchema（Group "思考策略"，9 条）**——示例（其余同构）：

```go
{Key: "thinking.policy", Label: "思考策略", Type: "select", Group: "思考策略", Default: "off",
 Enums: thinkingPolicyOptions, Desc: "off=不干预（现状）；auto=复杂任务软触发；strict=强制验证轮并拦截收尾"},
{Key: "thinking.min_think_rounds", Label: "最少思考轮数", Type: "number", Group: "思考策略", Default: 0,
 Min: f64p(0), Max: f64p(20), Step: 1, Desc: "复杂任务最少思考轮数（0=不强制；建议 3=首探+执行+验证）"},
// … max_think_rounds / verify_pass / complexity_threshold / nudge_enabled / nudge_max / round_effort_* 同构
```

**接线② Sanitized**：`m["thinking.policy"] = normalize(c.Thinking.Policy)`（""→"off"）等 9 键；数值 0 原样输出（语义=未设置）。**接线③ applyOne**：9 case；枚举走 `inList(val, opts)` 校验后 `setStr`；数值 `setInt`；布尔 `setBool`。**接线④ Validate**：枚举 + 范围 + 交叉校验（`min_think_rounds > max_think_rounds` 且均 >0 → 报错）；`policy=off` 时其余字段不做硬拒绝（软裁决：运行时 policy 优先，GUI 提示"不生效"——对齐参数面互斥软裁决现状）。**接线⑤ applyDefaults**：不写任何值（零值=off/现状）；归一由运行时 `Normalize()` 负责。

**挂钩点**：`config.go` 五处；`config.example.yaml`（`thinking:` 示例段）。**测试断言**：`TestThinkingConfig_FiveWiring`（§6.1 #1）。**零回归论证**：不写配置→Sanitized 与旧版一致（新增键除外）。**风险**：与参数面 `model.thinking_*` 命名混淆——Group 名"思考策略"与"模型"分组区隔。

### 3.5 internal/runtime 层（M2/M7/M11/M12：评估器接线、度量、护栏；applyRoundEffort）

```go
// internal/runtime/thinking.go（新增）：thinkOverride 会话级策略覆盖（nil=未覆盖；settings.json 持久化）。
type thinkOverride struct {
	policy     *string // off/auto/strict
	verifyPass *string // off/auto/on
}
// Runtime 新字段：thinkStrategy thinkmgr.Strategy；thinkOverride thinkOverride
// 新方法：SetThinkingPolicy(p string) error / SetVerifyPass(v string) error / ThinkingStatus() ThinkStatus

// buildThinkTracker 任务创建时调用一次：评估复杂度 → 构造 Tracker（policy=off 返回 nil）。
func (rt *Runtime) buildThinkTracker(taskText, mode string, imageN int, explicit string) *thinkmgr.Tracker {
	s := rt.effectiveThinkStrategy() // 覆盖 > 配置 > 零值（Normalize 兜底）
	if s.Policy == "off" { return nil } // 零回归：不构造、零开销
	fuzzy := 0
	if hits := goalmgr.DetectFuzziness(taskText); len(hits) > 0 { fuzzy = len(hits) } // 只读复用（spec.go:167）
	as := thinkmgr.NewAssessor(s.ComplexityThreshold)
	sig := thinkmgr.Signals{TaskText: taskText, Mode: mode, FuzzyHits: fuzzy, ImageN: imageN, Explicit: explicit}
	return thinkmgr.NewTracker(thinkmgr.BuildPolicy(s, as.Assess(sig), as.Score(sig)), as.Score(sig), as.Assess(sig))
}

// applyRoundEffort 按轮次类型下发一次性 effort 覆盖（M7；executor 轮首经钩子调用）。
func (rt *Runtime) applyRoundEffort(kind string) {
	s := rt.effectiveThinkStrategy()
	if s.RoundEffortPolicy != "escalate" { return } // none=不覆盖（零回归）
	if kind != "verify" && kind != "refine" { return } // 其余轮不覆盖（首轮不降档）
	if b, ok := rt.Backend.(interface{ SetNextEffort(string) }); ok { b.SetNextEffort(s.RoundEffortCap) }
}
```

**装配点**：任务创建处（chat/task 入口与 `SpawnTask` 子 Agent 构造处）`agent.SetThinkPolicy(rt.buildThinkTracker(...))` + `agent.SetRoundEffortHook(rt.applyRoundEffort)`；策略关闭传 nil（零回归）。

**度量（M11）**：trace 写 `think_round` 事件（`EventThinkRound` 常量，Entry 结构不变）；`internal/overseer/bias.go` 新增 `BiasRushedAnswer = "rushed_answer"`（默认 -10）与判据 `BiasRushed(rounds, toolCalls int, verified bool, c thinkmgr.Complexity) []BiasHit`（确定性：complex + 总轮数<2 + 无验证证据；simple 不误伤；有工具证据或已送审不命中）；runtime 在 `biasHits`（`runtime.go:3005`）旁并列合并，`OverseerEvaluate`（`runtime.go:2962`）签名不变。

**护栏装配（M12）**：止损参数来自 `Policy.StallRounds`（常量 2，`ShouldStopNudging` 判定）；续轮上限由 Tracker 内 `Intercepts+Nudges`/`MaxExtraRounds` 统一裁决；forcedFinal 由调用点先行短路。

**挂钩点**：H12（`runtime.go:1108` 旁路，不动参数面出口）、H13（`runtime.go:2962/3005` 合并点）、H14（`runtime.go:2717-2805` goal 快照读取，预留）、H17（`bias.go:64` 旁新增）、H21（`trace.go:21-26`）、H22（`helpers.go:64`）。**测试断言**：§6.1 #13/#14/#15。**零回归论证**：policy=off→nil Tracker + 零事件 + 零合并。**风险**：复杂度误判（阈值可配 + M11 校准）；网关路径临时层丢失（v1 主链路为准，handle 透传列入后续）。

### 3.6 internal/ctxmgr 层（M10）

```go
// internal/ctxmgr/thinking_protect.go（新增）：ThinkingRetention 压缩时的思考保护策略（零值=现行为）。
type ThinkingRetention struct {
	ProtectVerifyRound bool // 验证轮保护上一轮思考尾段（默认 false）
	ProtectRounds int // 保护最近 N 轮思考不被折半（默认 0=现行为）
}
func MarkProtected(msgs []types.Message, r ThinkingRetention) []types.Message // 零值策略恒等返回
func ThinkingSummary(msgs []types.Message, maxRunes int) string // 被削思考尾段摘要；无内容返回 ""
```

**机制**：① 验证轮保护——`verify_pass≠off` 且任务处于 verify 阶段时，对上一轮 assistant 的 `ReasoningContent` 打保护标记，压缩选择截断目标时跳过（正文仍可削）；② 摘要注入——压缩发生时用 `ThinkingSummary` 提取被削思考尾段（≤400 runes，复用 `helpers.go:291` PromoteThinkBody 尾段提取思路），以 `[历史思考摘要]（已压缩）` 注入系统侧。**挂钩点**：H24（`budget.go:643-695` 截断目标选择处；`budget.go:163-176` 思考 token 统计复用为保护上限）、H9（思路复用，不改本体）。**测试断言**：§6.1 #11。**零回归论证**：零值→`MarkProtected` 恒等、`ThinkingSummary` 不被调用；`CompactToBudgetWithEvict` 分层顺序与阈值零改动。**风险**：保护占预算（上限 ≤1 轮/≤8000 tokens）；摘要过时（标注来源与轮次号）。

### 3.7 多 Agent（M9）

```go
// internal/executor/handoff.go（修改；复用既有 handoff/spawn 通道）
// VerifierRequest 复核请求规格（复用现有子 Agent 装配路径 runtime.go:3137 SpawnTask）。
type VerifierRequest struct {
	Kind string // 固定 "verify"
	Target string // 被复核产出（正文摘要/文件清单）
	Checks []string // 复核清单（空=内置四项：证据核对/关键数字复算/边界与失败路径/与计划对照）
}
func (a *Agent) maybeVerifierHandoff(round int) *VerifierRequest // nil=不触发
```

**触发条件（v1 常量口径）**：`policy=strict` 且 complex 且验证轮已过（`Verified`）且有文件写入/`run_tests` 证据且未中断且未复核过 → 收尾前送审一次（`a.verifierDone` 防重复）；auto 模式不送审（防成本失控）。**复核纪律**：只读评审（不写产出、不改主链上下文，对齐 Cognition 单线程原则）；深度复用 `handoffMaxDepth=3`（`handoff.go:113`）；默认通道 handoff（串行交接）；结果以"通过/问题清单"回注主线程（与 `buildOverseerReport` 对齐，`runtime.go:3020`）；超时 3m。**挂钩点**：H20（`handoff.go:100`）、H25（`tools/task.go:191`）、装配路径 `runtime.go:3134-3137`。**测试断言**：§6.1 #10。**零回归论证**：条件不满足恒返 nil，不创建任何子任务。**风险**：成本/延迟（仅 strict+complex+产出证据；一次上限）。

### 3.8 UI 接线（TUI /think；GUI schema 驱动）

**TUI（`internal/tui/commands.go`）**：

| 命令 | 行为 |
|---|---|
| `/think` | 展示：策略（含来源：覆盖/配置）+ 最少轮数/上限/验证轮/nudge/升档 + 当前轮次进度 |
| `/think policy <off\|auto\|strict\|clear>` / `/think verify <off\|auto\|on\|clear>` | 设置/清除会话覆盖（settings.json；策略/验证轮） |
| `/think rounds <n>` / `/think nudge <on\|off>` / `/think effort <none\|escalate>` | 配置级设置（走 `ApplyConfigPatch`，与 `/maxlen` 同路径） |

与 `/effort` 正交：`/effort` 管"每轮思考多长"（参数面）；`/think` 管"思考几轮/是否验证"。矛盾组合（`/effort off` + `/think strict`）以 effort=off 优先（不发思考字段），`/think` 回显提示"当前档位 off，思考不会实际发生"。

**状态栏（`internal/tui/widgets.go:154`）**：policy≠off 时追加 ` · 思考:%s·轮%d/%d`（如 `思考:strict·轮2/3`）；验证轮进行中追加 `·验证中`；off 时**不显示**（零回归）；数据源 `rt.ThinkingStatus()`。

**GUI**：① 设置面板——`ConfigSchema()` 增 9 条 + `Sanitized()` 增 9 键 + `applyOne` 增 9 case 后，前端 schema 驱动渲染（按 Group 分组）**预期零前端改动**自动出现"思考策略"分组（实施时核对是否存在字段级白名单）；② 状态展示——`status.bar`（`server.go:743-748`）经 `ModelInfo`（`runtime.go:3415`）扩展 `thinking: {policy, round, maxRound, verify, verifying, source}`，`app.js modelInfoSuffix`（`3208-3210`）追加 ` · 思考:strict 轮2/3`；③ 会话级即时调整（可选，P2）——RPC `thinking.set` 复用 `model.set` 交互模式。**挂钩点**：H19（`tui.go:426-470`、`gui/server.go:1411-1460`）。**测试断言**：§6.1 #15。**零回归论证**：policy=off 不显示、不注册新命令语法冲突。**风险**：状态栏溢出（窄终端截断优先级：轮次信息优先）。

## 4 改造点清单

| 文件 | 动作 | 内容 |
|---|---|---|
| `internal/thinkmgr/*.go` | 新增 | thinkmgr.go（Kind/ThinkRound/State/Tracker）/assessor.go（Complexity/Signals/Assessor/Score）/policy.go（Strategy/Normalize/Policy/BuildPolicy）/nudge.go（模板/标记）/guard.go（ThinkSignals/ShouldStopNudging） |
| `internal/executor/agent.go` | 修改 | think 字段/SetThinkPolicy/SetRoundEffortHook/thinkGate（1284-1286、416-419 接入）/轮次登记结算/planGateInject+planGateFilterTools/提示词条件化（193-204、607） |
| `internal/executor/handoff.go` | 修改 | VerifierRequest/maybeVerifierHandoff（复用既有通道） |
| `internal/model/deepseek.go` | 修改 | nextEffort 字段/SetNextEffort/currentEffort；applyThinkingParams 读临时层并复位 |
| `internal/config/config.go` | 修改 | ThinkingConfig 9 字段；ConfigSchema 9 条；Sanitized 9 键；applyOne 9 case；Validate 规则；applyDefaults 不写值 |
| `internal/runtime/thinking.go` + `runtime.go` + `settings.go` | 新增/修改 | thinkOverride/SetThinkingPolicy/SetVerifyPass/ThinkingStatus/effectiveThinkStrategy/buildThinkTracker/applyRoundEffort；装配点；trace 写点；biasHits 合并；ModelInfo 扩展；`thinking_session.*` 读写 |
| `internal/ctxmgr/thinking_protect.go` + `budget.go` | 新增/修改 | ThinkingRetention/MarkProtected/ThinkingSummary；截断目标选择处跳过保护标记（643-695；零值零改动） |
| `internal/overseer/bias.go` + `internal/trace/trace.go` | 修改 | BiasRushedAnswer 常量 + BiasRushed 判据；EventThinkRound 常量（Entry 结构不变） |
| `internal/tui/commands.go` + `widgets.go` | 修改 | /think 命令族注册与回显；状态栏追加思考轮次（policy≠off 时） |
| `internal/gui/server.go` + `app.js` | 修改（视实现） | status.bar/model.info 扩展；modelInfoSuffix 追加；schema 表单自动（实施时核对白名单）；可选 thinking.set（P2） |
| `config.example.yaml` | 修改 | `thinking:` 示例段（9 字段注释，默认 off） |

## 5 分步迁移路径

每步独立可交付、可回滚（回滚 = 关闭开关 / 删除对应提交；所有新增行为默认关闭）。

| 步骤 | 交付物 | 验证 | 回滚 |
|---|---|---|---|
| S1（M1+M2+配置面） | thinkmgr 包（模型/评估器/策略）+ config 9 字段五处接线 | thinkmgr 表驱动单测；config 五处接线测试；policy=off 时 Sanitized 与现状一致 | 删包/删字段（无调用方） |
| S2（M3+M5） | executor thinkGate + strict 拦截 + 轮次登记结算 + 逃生通道 | 拦截集成测试（首轮非空被拦截/验证后放行）；逃生通道表驱动；零回归快照 | SetThinkPolicy(nil) 短路 |
| S3（M4） | CoVe 模板 + SettleRound 标记解析 + verify_pass 语义 | 模板四要素断言；标记解析；verify=off 零注入 | verify_pass=off 短路 |
| S4（M6） | nudge 注入 + 频控 + 模板轮换 | 频控单测；间隔/上限断言；关闭时零注入 | nudge_enabled=false 短路 |
| S5（M7） | SetNextEffort + applyRoundEffort + 验证轮升档 | httptest 请求体断言（verify 轮=cap）；一次性复位；关闭时请求体逐字一致 | escalate=none 短路 |
| S6（M11） | trace think_round + BiasRushed + 轮次埋点 | 事件字段断言；complex+1 轮+无证据→扣分；simple 不误伤 | 无事件/不合并（policy=off） |
| S7（M12） | 止损（stall 检测）+ 上限联动 + simple 零触发 | 连续无新证据停止催轮；round≥上限；forcedFinal 零注入 | 护栏为加法条件，默认即现状 |
| S8（M8+M9） | 计划门注入 + 复核子代理默认化 | complex 首轮注入；simple 不注入；complex+产出→1 次只读复核 | 关闭/条件不满足恒等 |
| S9（M10+UI 收口） | 思考保护 + 摘要注入；/think + 状态栏 + GUI schema + 示例配置 | 保护后验证轮思考存活；零值策略压缩结果与现状一致；命令测试；schema 9 条；状态栏 off 隐藏 | 零值恒等；不注册 /think（保留旧语法） |

**默认值切换说明**：本改造**不切换任何默认**（policy=off、verify=off、nudge=false、escalate=none）——"释放"体现在能力被打开，而不是默认行为改变。

## 6 验收标准与风险清单

### 6.1 可测试断言（测试名 + 断言内容）

| # | 测试名 | 断言内容 |
|---|---|---|
| 1 | `TestThinkingConfig_FiveWiring` | schema 9 条（group=思考策略）/Sanitized 9 键/applyOne 往返/Validate 拒绝表/defaults 零值不改；Load 空配置→policy 归一 "off" |
| 2 | `TestThinkPolicy_OffShortCircuit` | policy=off → 不构造 Tracker、无 think_round、零注入；同一 mock 流开/关两遍，消息序列与请求数一致（零回归快照） |
| 3 | `TestAssessor_TableDriven` | ≥10 组（空/短/长/超长/模糊 1/2/3/goal/多目标词/显式标记）；显式 simple 覆盖评分、complex 直通；阈值 60 路由正确 |
| 4 | `TestStrictPolicy_Intercept` | strict+min=2：首轮非空被拦截并注入验证指令、第 2 轮验证后放行；strict+min=0：首轮非空被拦截（未验证），验证轮后放行 |
| 5 | `TestIntercept_EscapeHatches` | forcedFinal/续轮上限/goal 模式/simple 任务表驱动均不拦截（0 注入） |
| 6 | `TestVerifyTemplate_And_Marks` | 默认模板含四要素且 %d 格式化正确；VERIFIED→Verified；NEEDS_FIX→refine 且保持未验证；verify 轮非空无标记→兜底通过 |
| 7 | `TestThinkNudge_FrequencyCap` | nudge_max=2 第 3 次不注入；间隔 <2 轮不注入；simple 零注入；关闭时零注入 |
| 8 | `TestRoundEffort_VerifyEscalation` | httptest：verify 轮 `reasoning_effort=cap`；请求后复位（下一轮恢复全局）；escalate=none 时请求体逐字一致；升档后下一任务首轮为全局档（不写 settings） |
| 9 | `TestPlanGate_SoftInject` | complex 首轮注入 1 条计划提示；simple 不注入；soft 模式工具列表逐项相等 |
| 10 | `TestVerifier_DefaultTrigger` | complex+文件产出→1 次只读复核；simple/中断→0；复核问题清单回注主线程可见；子代理无写操作 |
| 11 | `TestThinkingProtect_VerifyRound` | 保护后验证轮思考在压缩后存活；零值策略压缩结果与现状逐字段一致；摘要 ≤400 runes 带来源标记 |
| 12 | `TestBiasRushed_Deterministic` | complex+1 轮+无证据→命中 -10；有工具证据或已送审→不命中；simple 同形不误伤 |
| 13 | `TestThinkRound_TraceEvent` | think_round 字段完整、JSON 可解析；policy=off 时零事件 |
| 14 | `TestThinkingSession_PersistRestore` | policy/verify 覆盖→settings.json→重建 Runtime 恢复；旧 settings（无 thinking_session）→无覆盖、行为不变 |
| 15 | `TestThinkUI_AndGuard` | 达到轮上限后强制放行收尾（不死循环）；/think 子命令回显/持久化；状态栏 policy=off 隐藏（零回归）；GUI schema 含 9 条思考策略字段 |
| 16 | `TestZeroRegression_FullSuite` | `go build ./... && go test -count=1 ./internal/executor/ ./internal/runtime/ ./internal/model/ ./internal/config/ ./internal/ctxmgr/ ./internal/thinkmgr/` 全绿（默认配置） |

### 6.2 风险清单

| 风险 | 影响 | 缓解 |
|---|---|---|
| 复杂度误判 | simple 误判 complex→多轮伤害；反向→机制空转 | 保守阈值 + 显式标记直通 + 阈值可配 + M11 数据校准 |
| 拦截与既有护栏交互（detectLoop/noToolRounds/forcedFinal） | 死循环风险 | 拦截仅在收尾分支；续轮总上限；token 红线绝对优先；逃生通道测试（#5） |
| 验证轮死循环 | 交互无法结束 | `max_think_rounds` 硬护栏 + detectLoop 仍生效 + refine 复验受上限 |
| 轮次档位泄漏 | 后续任务被高档位污染 | 临时层单请求自过期；断言（#8）；升档仅向上（不发 off） |
| 与 goal 模式叠加膨胀 | 双重循环 | goal 硬排除（拦截/送审/计划门不叠加；goal 已有打磨轮/门禁） |
| token 成本上升 / 思考保护占预算 | 按输出计费多轮抬成本；压缩效果下降 | 默认 off；条件路由；降本路径；保护上限 ≤1 轮/≤8000 tokens、摘要 ≤400 runes |
| 子代理复核成本/延迟 | 关键路径变慢 | 仅 strict+complex+产出证据；一次上限；Timeout 3m；只读评审 |
| 模型无视注入指令 | 验证质量不达预期 | 轮次计数兜底（非标记依赖）；标记仅作提前放行/提前修正信号 |
| 网关路径 effort 临时层丢失 | 多实例轮换时升档失效 | v1 以主链路为准；handle 透传列入后续（复用 GenerationParamsBackend 模式） |
| 旧二进制/旧 settings 兼容 | 降级运行异常 | settings 逐键读取未知键天然忽略；无键=无覆盖=零回归 |

## 7 附录

### 7.1 业界参照索引（机制 → 本文落点）

| 业界机制 | 核心借鉴 | 本文对应 |
|---|---|---|
| Claude extended / interleaved thinking | 思考是显式、可计量、可回传资产；工具结果后继续想 | M1、M7、M10 |
| OpenAI reasoning effort / summary | 档位即控制面；跨轮不保留推理 | M7、M1（harness 制造轮次） |
| CoVe / Reflexion / Self-Refine | 独立验证问题→修订；反思记忆；迭代反馈 | M4（refine）、M9 |
| sequential-thinking MCP | 轮次由字段驱动，不依赖模型自觉 | M6 |
| Claude Code plan mode / TodoWrite / subagent | 审批门 + 清单外化 + 独立上下文复核 | M8、M9 |
| DeepSeek R1 reasoning_content | 跨轮不回收；Aha 式单轮回查 | M1、M4（"换角度复算"提示词化） |
| dsh goal-round-driver / plan-mode / 事件溯源 | 轮次=一等状态、round 预算、可重放 | M1、M5、M11 |
| s1 budget forcing / RouteLLM / Snell / overthinking 研究 | 强制续思与截断；思考量按难度分配；无条件多思考有害 | M3；M2、M12 |
| Cognition《Don't Build Multi-Agents》 | 单线程深上下文优先；多 Agent 用于独立复核 | M3/M4（同上下文优先）、M9 |

### 7.2 关键结论摘要

1. **"一次思考"是结构性缺陷，不是提示词问题**：12 条实锤全部指向"收尾直通车"（`agent.go:1284-1286`/`416-419`）与"无轮次概念"；修复必须发生在轮次结构上——这正是 dsh goal-round-driver 与 ContextLoom 的核心代差。
2. **三纪律贯穿全篇**：默认关闭（off=现状逐字一致）、条件路由（复杂任务才启用，防 overthinking）、上限护栏（`max_think_rounds` + 止损 + token 红线优先）。
3. **配置面收敛为 thinking.\* 9 字段**（design-e 权威）：三草案的 `thinking_policy.enabled`/`think_policy.*` 命名冲突全部消除；细化参数以包内常量固定，配置面最小化。
4. **验证轮用 CoVe 形态、同上下文优先**（Cognition 原则）：独立子代理仅用于关键产出复核（只读、一次、有产出证据才触发）。
5. **轮次临时层与参数面三层优先级正交**：`SetNextEffort` 单请求自过期，不写回、不泄漏；全局档位变更仍走 `applyGenerationParams` 统一出口。
6. **度量先行（M11）为深化提供基线**：think_round 事件 + "草率回答"偏差把"多轮是否提升质量"变成可回查数据，复杂度阈值与护栏参数由此校准。

### 7.3 与《思考与长度释放》衔接核对表

| 衔接点 | 参数面口径（已落码） | 本文口径 | 关系 |
|---|---|---|---|
| H10 `applyThinkingParams`（`deepseek.go:1078`） | 按请求常量下发 effort | 临时层优先读取（单请求）；发送矩阵四规则不变 | 叠加不替换（M7） |
| H11 `SetReasoningEffort`（`deepseek.go:961`） | 全局档位 API（settings 持久化） | 不动其语义；新增 `SetNextEffort` 正交 API | 正交 |
| H12 `applyGenerationParams`（`runtime.go:1108`） | 统一热应用出口（调用铁律：档位先于开关） | 轮次层不经此出口（自过期）；全局变更仍走此出口 | 正交 |
| H23 settings.json `effort` 键 | effort 持久化 | 新增 `thinking_session.*` 独立键 | 独立键互不覆盖 |
| L3 会话覆盖 | `generationOverride` 未落码（直写 `CLIDefaults.Effort`） | `thinkOverride`（policy/verify_pass 两把开关） | 结构独立；未来可合并为 `sessionOverrides`（预留） |
| 零回归基线 | 未配置时请求体逐字一致 | 同上 + 消息序列/注入零新增 | 同一铁律 |
| 落码差异清单 | design-e §0.1 六条（`generationOverride` 未落码、`EffectiveInputBudget` 简化版、`ThinkingEnabled` 缺省 `&true` 等） | 本文基线=当前落码行为 | 以 design-e 实测为准 |

---

*文档完。本文整合 design-c（触发与拦截）/ design-d（鼓励与支撑）/ design-e（配置、UI、实施）三份分册草案；M1-M12 全量保留，配置命名、接口命名、轮次类型定义已统一。所有挂钩点行号沿用 H1-H25 体系；参数面现状以 design-e §0.1 实测为准。*
