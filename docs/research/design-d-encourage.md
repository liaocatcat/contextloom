# ContextLoom 多轮思考触发 · 鼓励与支撑侧设计（设计稿 D）

> 系列：并入主设计《contextloom改造详细设计-多轮思考触发.md》；上游依据：《research/contextloom-thinking-inventory.md》（H1-H25 盘点）、《research/industry-multi-pass-thinking-survey.md》（业界对标 Top 10）。
> 范围：只覆盖"鼓励与支撑"侧（多轮思考发生得顺、有记忆、可度量、不伤简单任务）；"强制触发判定"（收尾拦截/验证轮判定）见姊妹稿，本稿为其提供支撑接口。
> 行号：所有 `文件:行号` 均为写作时工作区实测核对（grep/read 二次确认）；H 编号引自盘点报告 §4，代码演进后以源码为准。
> 兼容：与《思考与长度释放》三层优先级（L3 会话覆盖 > L1 模型预设 > L2 CLI 默认 > 内置默认，该文档 §2.1）不冲突——本稿只新增"轮次临时层"，单请求生效、不持久化。

## 0 总则（贯穿 7 机制）

1. **默认关闭**：7 机制全部由 `settings.json → think_policy` 门控，缺省 `enabled=false`；关闭时走现有代码路径（逐机制附零回归论证）。
2. **条件路由**：仅 `complexity=complex` 的任务启用多轮机制；simple/normal 零注入、零升档、零送审（防 overthinking，调研 §8）。
3. **复杂度信号**：复用 `estimateComplexity`（runtime/helpers.go:64）与 `DetectFuzziness`（goalmgr/spec.go:167），runtime 任务启动时计算一次并注入 Agent 字段（不新增网络协议字段）。
4. **确定性度量**：轮次/触发计数以 harness 事件为准（trace + 原子计数器，复制 batchNudge 的 batchNo 模式），不信任模型自报（对齐 overseer 防作弊原则）。
5. **叠加不替换**：参数类改动均为"轮次临时层"，单次请求生效、请求后复位，不修改 L1/L2/L3 任何持久值。

## 1 thinkNudge 续思注入（H7）

**设计目标**：把"要不要第二轮"从模型自愿变为 harness 结构性催轮——低成本、可配置、可频控。

**机制描述**
- 复制 `batchNudge`（agent.go:101-116）模式：在"无工具调用、即将收尾"分支（agent.go:1232-1286）注入一条 user 消息，以 `continue` 代替 `break`（1284-1286），不结束循环。
- 触发时机（全部满足才注入）：① 复杂度=complex；② 本轮 `len(toolCalls)==0` 且正文非空（空正文已有 noToolRounds 兜底，agent.go:1235 起）；③ `round < min_round` 或正文 < `min_answer_runes`（回答过短）；④ 频控：已注入 < `max_nudges`（默认 2）且距上次 ≥ `min_round_gap`（默认 2 轮）。
- 文案模板（中文、可配置、按次数轮换防机械重复；存 `think_policy.nudge.templates[]`）：T1="【续思提示】请对上述结论做一次独立验证：列出关键假设并逐一核对，可调用工具查证；若结论不变，请说明验证过程后收尾。"；T2="【续思提示】换一个角度复核：若上述结论有错，最可能错在哪里？请给出复核结论或补充边界条件。"
- 防"提示轰炸"：`max_nudges`+`min_round_gap` 双闸，与机制 7 止损联动；同步子 Agent 路径（agent.go:416-419）同等接入。

**Go 接口草案**
```go
// ThinkNudgePolicy 续思注入策略（runtime 由 settings 构造后注入 executor；nil=关闭）。
type ThinkNudgePolicy struct {
	Enabled        bool     // 总开关，默认 false
	MaxNudges      int      // 单任务最多注入次数，默认 2
	MinRoundGap    int      // 两次注入最小轮间隔，默认 2
	MinAnswerRunes int      // 正文短于此值视为"草率回答"，默认 200
	Templates      []string // 文案模板（中文），空则用内置
}
// SetThinkNudge 注入续思策略（nil 安全，遵循 SetTracer 注入模式）。
func (a *Agent) SetThinkNudge(p *ThinkNudgePolicy)
// thinkNudge 收尾前按策略注入；返回追加后的 messages 与是否注入，未触发时原样返回。
func (a *Agent) thinkNudge(messages []types.Message, round int, lastBody string) ([]types.Message, bool)
```

**挂钩点**：H7 `internal/executor/agent.go:101-116`（复制母本，调用点示例 agent.go:472）；H1/H2 `agent.go:1232-1286`（注入位置）；H4 `agent.go:416-419`（同步路径）。

**测试断言**（`internal/executor/think_nudge_test.go`）
- `TestThinkNudgeInjectsOnComplexShortAnswer`：complex+短回答→追加 1 条 user 消息且循环未 break；
- `TestThinkNudgeFrequencyCap`：累计注入 ≤ max_nudges；间隔 < min_round_gap 不注入；
- `TestThinkNudgeDisabledNoop`：Enabled=false→返回 (原 messages, false)，无任何 append。

**零回归论证**：策略 nil/未启用时函数首行返回；收尾 break 分支与现有 5 条终止条件（含 forcedFinal，agent.go:1065）不改；默认配置不注入任何消息。

**风险**：模型把续思当"再答一遍"敷衍→文案强制"验证+可调用工具"；token 成本上升→频控+机制 7 上限兜底。

## 2 轮次级 effort 升级（H10/H11/H12/H23）

**设计目标**：首轮轻探→验证轮深挖，把静态全局档位升级为"按轮次类型动态下发"，同时不破坏三层优先级。

**机制描述**
- 轮次类型（主循环/触发侧维护，确定性）：`explore`（首探）/ `verify`（验证轮）/ `polish`（打磨）/ `final`（收尾）。
- 档位策略表（默认值可配置；未配置的轮次类型=不覆盖，沿用全局档位）：

| 轮次类型 | effort 档位 | 说明 |
|---|---|---|
| explore | 全局档位（不降档） | 零回归：首轮行为与现状一致 |
| verify | max | 验证轮深挖（对标 budget forcing / interleaved thinking） |
| polish | high | 打磨轮 |
| final | 全局档位 | 收尾轮不额外抬档 |

- 下发路径：runtime 发起该轮前调用 `SetNextEffort`（一次性），`applyThinkingParams`（deepseek.go:1078）优先读取临时值，请求后自动复位；不触碰 `SetReasoningEffort`（deepseek.go:961）的全局语义与 settings 持久化（settings.go:24）。
- 与三层优先级关系：叠加在 L3 之上的"轮次临时层"——仅进程内单次请求生效、不写回 settings.json、不影响 `/effort` 命令语义；关闭时该层不存在，解析链与现状逐字一致。

**Go 接口草案**
```go
// RoundEffortPolicy 轮次级 effort 覆盖策略（轮次临时层；不写回 settings）。
type RoundEffortPolicy struct {
	Enabled bool              // 默认 false
	ByKind  map[string]string // "verify"→"max"、"polish"→"high"；缺省类型不覆盖
}
// roundEffortOverride 计算本轮覆盖值；返回 "" 表示不覆盖（沿用现有解析链）。
func (a *Agent) roundEffortOverride(kind string) string
// SetNextEffort 一次性覆盖下一次请求的 effort（发送后自动复位；空值=no-op），与全局 API 语义正交。
func (d *DeepSeekBackend) SetNextEffort(level string)
// applyRoundEffort 按轮次类型向活跃 backend 下发临时覆盖（调用时机铁律同 applyGenerationParams，runtime.go:1108）。
func (rt *Runtime) applyRoundEffort(kind string)
```

**挂钩点**：H10 `internal/model/deepseek.go:1078`（临时值读取）；H11 `deepseek.go:961`（新增 SetNextEffort）；H12 `internal/runtime/runtime.go:1108`（旁路新增）；H23 `internal/runtime/settings.go:24`（配置扩展位）。

**测试断言**（`internal/model/round_effort_test.go` + 扩展 `internal/runtime/generation_params_test.go`）
- httptest 捕获请求 JSON：verify 轮出现 `reasoning_effort:"max"`；
- 关闭时请求体与现状逐字一致（含 thinking=disabled 时不带 reasoning_effort 的互斥断言）；
- 请求后复位：下一轮恢复全局档位。

**零回归论证**：Enabled=false→`roundEffortOverride` 恒返 ""、`SetNextEffort("")` 为 no-op；`applyThinkingParams`（deepseek.go:1078）四条发送规则不改。

**风险**：验证轮抬档增加时延/成本→上限 max、仅 complex、仅 verify 轮；与模型预设冲突→临时层单请求即失效（可测）。

## 3 计划门 plan gate（H5/H6）

**设计目标**：复杂任务"先出计划再执行"，计划外化为可对照清单，为验证轮提供基准。

**机制描述**
- 软门（默认）：复杂任务第 1 轮注入"先出计划"提示（chat/task），要求输出结构化清单 `【执行计划】1. …（可验证步骤）2. …【计划完】`；计划原文存入 Agent 状态（`planOutline`），验证轮对照"计划项是否闭环"。
- 硬门（可选，默认关）：首轮工具白名单过滤——只放行只读工具（read_file/list_files/search_content/git_diff/run_tests），写类工具首轮不可见，计划产出后放开。
- 与 goal 模式 spec 门（spec.go:19 MaxSpecRounds=3、R1-R5 门槛）的关系：spec 是 goal 专属强门；本设计是 chat/task 的最小计划门（软默认），两者不叠加启用。
- chat 提示词改造（H6）：agent.go:194 现文案"任务完成后直接给出最终答案并停止"→条件化："简单问题直接回答；复杂问题先列计划、执行后自查再收尾"；task strategy（agent.go:607）追加计划门条款。

**Go 接口草案**
```go
// PlanGatePolicy 计划门策略（chat/task 模式；goal 模式已有 spec 门，不重复启用）。
type PlanGatePolicy struct {
	Enabled bool   // 默认 false
	Mode    string // "soft"（默认，提示注入）| "hard"（首轮只读工具白名单）
}
// planGateInject 复杂任务首轮注入"先出计划"提示；非复杂/非首轮/关闭时原样返回。
func (a *Agent) planGateInject(messages []types.Message, round int) []types.Message
// planGateFilterTools 硬门：首轮过滤写类工具；soft 模式恒返回原列表（零回归）。
func planGateFilterTools(tools []types.ToolDefinition, mode string, planned bool) []types.ToolDefinition
// 计划状态（Agent 字段）：planOutlined bool；planOutline string（首轮计划原文，供验证轮对照）。
```

**挂钩点**：H5 `internal/executor/agent.go:607`（task strategy 文本）；H6 `agent.go:194`（chat 提示词条件化）；工具过滤点 `agent.go:373 / 1013`（toolList 构造处）。

**测试断言**（`internal/executor/plan_gate_test.go`）
- complex 首轮注入 1 条计划提示；simple 不注入；
- hard 模式首轮移除写类工具、计划产出后恢复全量；
- soft 模式与关闭时工具列表逐项相等。

**零回归论证**：默认关闭→注入函数与过滤函数均为恒等变换；chat 提示词改造由开关控制，关闭时保留原文。

**风险**：拖慢简单/复杂边界任务→仅 complex+可配置阈值；硬门可能阻塞合理首轮写入→默认 soft、hard 显式开启。

## 4 验证子代理默认化（H20/H25）

**设计目标**：复杂任务收尾默认送一次独立复核（关键产出送审），用独立上下文复核关键结论。

**机制描述**
- 触发条件：复杂度=complex 且产出为代码/文档（有文件写入或 run_tests 证据）且非用户中断；简单任务/中断/已复核过不触发。
- 复核通道二选一（配置）：`handoff`（handoff.go:100，串行交接）或 `spawn_task`（tools/task.go:191，并行子代理）；复核任务独立决策（不共享主线程偏见），结果以"通过/问题清单"回注主线程。
- 与 Cognition《Don't Build Multi-Agents》的平衡：主线程决策链不断裂——复核是只读评审（不写产出、不改主链上下文），仅回注结论；深度限制复用 handoffMaxDepth=3（handoff.go:95）。
- 复核清单默认四项：证据核对、关键数字/结论复算、边界与失败路径、与计划（机制 3）对照。

**Go 接口草案**
```go
// VerifierPolicy 复核子代理默认化策略（复杂任务收尾默认送审）。
type VerifierPolicy struct {
	Enabled bool          // 默认 false
	Channel string        // "handoff"（默认）| "spawn"
	Timeout time.Duration // 复核超时，默认 3m
	Checks  []string      // 复核清单；空则用内置四项
}
// VerifierRequest 复核请求规格（复用现有子 Agent 装配路径 runtime.go:3137 SpawnTask）。
type VerifierRequest struct {
	Kind   string   // 固定 "verify"
	Target string   // 被复核产出（正文摘要/文件清单）
	Checks []string // 复核清单
}
// maybeVerifierHandoff 复杂任务收尾时按策略生成复核请求；nil=不触发（简单任务/中断/已复核）。
func (a *Agent) maybeVerifierHandoff(round int, complexity int) *VerifierRequest
```

**挂钩点**：H20 `internal/executor/handoff.go:100`（复核通道）；H25 `internal/tools/task.go:191`（复核通道）；装配路径 `internal/runtime/runtime.go:3137`；展示联动 H19 `internal/tui/tui.go:427` / `internal/gui/server.go:1429`。

**测试断言**（`internal/runtime/verifier_default_test.go`）
- complex+有文件产出→触发 1 次复核；simple→0 次；
- 用户中断（ctx.Done）→不触发；
- 复核失败清单回注后主线程消息可见；复核子代理无写操作（只读断言）。

**零回归论证**：Enabled=false→`maybeVerifierHandoff` 恒返 nil，不创建任何子任务；现有 handoff/spawn 调用路径不变。

**风险**：增加成本与延迟→Timeout+仅 complex+一次上限；复核与主线程结论冲突→以工具实测证据裁决。

## 5 思考链保留策略（H8/H24）

**设计目标**：压缩不再"一刀切"侵蚀多轮思考记忆——验证轮保留必要思考尾段，或注入思考摘要保连续性。

**机制描述**
- 现状问题：思维链计入预算（budget.go:163-176），压缩时优先截断旧轮思维链、最终兜底折半（budget.go:643-695，tcIdx=-2 标识"折思维链"）；KeepRecent=8（budget.go:75）只保护"不丢弃"不保护"不截断"（budget.go:132-133）。
- 方案 A（验证轮保护）：任务处于 verify 阶段时，对"上一轮 assistant 消息的 ReasoningContent"打保护标记；压缩选择截断目标时跳过其思维链截断（正文仍可削）。
- 方案 B（思考摘要注入）：压缩发生时，用 `ThinkingSummary` 提取被削思考尾段（复用 PromoteThinkBody 尾段提取思路，helpers.go:291）生成 ≤400 runes 摘要，以 `[历史思考摘要]（已压缩）` 注入系统侧，下一轮可见。
- 与既有 ctxBudget 设计衔接：不改 `CompactToBudgetWithEvict`（budget.go:474）分层顺序与阈值；仅在"选择截断目标"处（budget.go:643-656）增加保护判断。

**Go 接口草案**
```go
// ThinkingRetention 压缩时的思考保护策略（runtime 按轮次类型设置；零值=现行为）。
type ThinkingRetention struct {
	ProtectVerifyRound bool // 验证轮保护上一轮思考尾段，默认 false
	ProtectRounds      int  // 保护最近 N 轮思考不被折半，默认 0（0=现行为）
}
// MarkProtected 标记需保护的思考消息（返回带标记副本；零值策略返回原列表）。
func MarkProtected(msgs []types.Message, r ThinkingRetention) []types.Message
// ThinkingSummary 提取被压缩思考的尾段摘要（≤maxRunes），供注入下一轮；无内容返回 ""。
func ThinkingSummary(msgs []types.Message, maxRunes int) string
```

**挂钩点**：H24 `internal/ctxmgr/budget.go:643-695`（截断目标选择处加保护判断）、`budget.go:163-176`（思考 token 统计复用为保护上限）；H8 `internal/executor/agent.go:2025`（splitThinkTagsAgent，轮次计数入口）。

**测试断言**（`internal/ctxmgr/thinking_protect_test.go`）
- 保护后：验证轮思考在压缩后存活（对照 `thinking_compact_test.go` 基线用例）；
- 零值策略：压缩结果与现状逐字段一致（复用现有用例作回归）；
- 摘要注入：被削思考产生 ≤400 runes 摘要且带来源标记。

**零回归论证**：策略零值→`MarkProtected` 恒等返回、`ThinkingSummary` 不被调用；ForceCompact（budget.go:355）/ CompactToBudgetWithEvict 路径不新增分支副作用。

**风险**：保护占用预算→设保护上限（≤1 轮或 ≤8000 tokens）；摘要注入过时信息→标注来源与轮次号。

## 6 思考度量与偏差检测（H13/H17/H21）

**设计目标**：多轮思考可观测、可评分、防作弊——为深化设计提供基线数据。

**机制描述**
- trace 扩展：新增 `think_round` 事件（additive，不改现有 Entry 结构，trace.go:21-26 已有 Round/Event 字段）；记录轮次类型/effort/累计 nudge/是否送审/总轮数。
- 偏差类型扩展：`rushed_answer`（单次草率回答）——确定性判据：complex 任务、总思考轮数 <2、无验证证据（无工具调用记录且未送审）；默认 -10 分（可配置），与现有三类偏差（bias.go:24 起 const 块）并列，封顶规则复用 TotalPenalty。
- 防作弊：计量只采用确定性事件（轮次计数、工具调用记录、verifier 事件），模型自报（正文声称"已验证"）不参与——对齐 anti_cheat 实测比对原则。
- 用途：深化设计基线（多轮是否提升质量）、监工评分参考（H13）、TUI/GUI 展示（轮次徽标）。

**Go 接口草案**
```go
// ThinkRoundEvent trace 事件负载（event="think_round"；Entry 结构 additive 扩展，不删改字段）。
type ThinkRoundEvent struct {
	Round    int    `json:"round"`    // 轮次
	Kind     string `json:"kind"`     // explore|verify|polish|nudge|plan
	Effort   string `json:"effort"`   // 本轮实际档位
	Nudges   int    `json:"nudges"`   // 累计注入次数
	Verifier bool   `json:"verifier"` // 是否已送复核
	Rounds   int    `json:"rounds"`   // 任务总思考轮数
}
// BiasRushedAnswer 偏差类型：单次草率回答（复杂任务、总轮数<2、无验证证据）。
const BiasRushedAnswer = "rushed_answer" // 默认 -10
// BiasRushed 确定性判据（输入全部来自 harness 记录，模型自报不参与）；不改 BiasAnalyze 签名。
func BiasRushed(rounds int, toolCalls int, verified bool, complexity int) []BiasHit
// runtime 侧：biasHits（runtime.go:3005）合并新判据；OverseerEvaluate（runtime.go:2962）无需改签名。
```

**挂钩点**：H21 `internal/trace/trace.go:21-26`（事件扩展）；H17 `internal/overseer/bias.go:64`（BiasAnalyze 旁新增 BiasRushed）；H13 `internal/runtime/runtime.go:2962 / 3005`（合并点）。

**测试断言**（`internal/overseer/bias_test.go` 扩展 + `internal/trace/think_round_test.go`）
- complex+1 轮+无证据→命中 rushed_answer 扣分；simple 同形不命中（防误伤）；
- 有工具证据或已送审→不命中；
- think_round 事件字段完整、JSON 可解析；关闭 metering 时零事件（零回归）。

**零回归论证**：metering.enabled=false（默认）→不写 think_round、BiasRushed 不合并；现有四维评分与三类偏差行为不变。

**风险**：误报惩罚正常快答→仅 complex 生效+阈值保守+可配置；度量增加少量 IO→走现有 trace 通道批量写。

## 7 过度思考护栏（H3/H23）

**设计目标**：防"一律多轮"伤害简单任务——简单任务零触发；复杂任务有轮次上限与止损。

**机制描述**
- 简单任务零触发：机制 1/2/3/4/5 的公共前置条件均为 `complexity=complex`；simple/normal 不注入、不升档、不送审、不保护（对齐 overthinking 研究结论，调研 §8）。
- 止损（边际收益检测）：连续 `stall_rounds`（默认 2）轮无新证据（无新工具调用、工具结果摘要不变、正文无变化）→立即停止催轮（nudge 停、effort 复位、不再送审）。
- 轮次上限与 token 预算联动：`max_think_rounds`（默认 3）后不再催轮；`forcedFinal`（agent.go:1065 起，token 预算红线）触发时一切多轮机制让位——不注入任何续思提示。
- 配置扩展位：`settings.json → think_policy.budget`（H23），与思考长度配置同级存放。

**Go 接口草案**
```go
// ThinkBudget 多轮思考止损与上限（与 token 预算红线联动）。
type ThinkBudget struct {
	MaxThinkRounds int // 复杂任务思考轮上限（超过不再催轮），默认 3
	StallRounds    int // 连续无新证据轮数阈值，默认 2
}
// ThinkSignals 单轮证据信号（主循环确定性采集，不依赖模型输出）。
type ThinkSignals struct {
	ToolCalls    int    // 本轮工具调用数
	ResultDigest string // 本轮工具结果摘要（哈希）
	BodyRunes    int    // 正文长度
}
// shouldStopNudging 边际收益检测：连续 StallRounds 轮无新证据→停止催轮。
func shouldStopNudging(prev, cur ThinkSignals, stallCount int, b ThinkBudget) (stop bool, newStall int)
// allowNudge 轮次上限判定：round >= MaxThinkRounds→不再注入；forcedFinal 由调用点先行短路（agent.go:1065 起）。
func allowNudge(round int, b ThinkBudget) bool
```

**挂钩点**：H3 `internal/executor/agent.go:1060-1062`（主循环入口，轮次计数与策略钩子）；H23 `internal/runtime/settings.go:24`（think_policy.budget 持久化扩展位）；预算红线协同点 `agent.go:1065` 起。

**测试断言**（`internal/executor/think_budget_test.go`）
- 连续 2 轮无新证据→第 3 轮起不注入 nudge；
- `round >= MaxThinkRounds`→allowNudge=false；
- simple 任务全链路（机制 1-5）零触发断言；forcedFinal=true 时 nudge 注入数为 0。

**零回归论证**：护栏是"加法条件"——默认配置下 simple/normal 路径与现状完全一致；complex 路径仅在开关开启时新增行为；forcedFinal 分支不改。

**风险**：复杂度误判（complex 误判为 simple→不触发；反向→多轮伤害）→保守阈值+可配置+用机制 6 度量数据校准。

## 8 配置汇总（settings.json → think_policy）

```json
{
  "think_policy": {
    "enabled": false,
    "complexity_gate": true,
    "nudge":   {"enabled": false, "max_nudges": 2, "min_round_gap": 2, "min_answer_runes": 200, "templates": []},
    "round_effort": {"enabled": false, "by_kind": {"verify": "max", "polish": "high"}},
    "plan_gate": {"enabled": false, "mode": "soft"},
    "verifier": {"enabled": false, "channel": "handoff", "timeout_sec": 180, "checks": []},
    "retention": {"protect_verify_round": false, "protect_rounds": 0},
    "metering": {"enabled": false, "rushed_answer_penalty": 10},
    "budget": {"max_think_rounds": 3, "stall_rounds": 2}
  }
}
```
缺省（不写该键）=全关闭=现状行为；`loadSettings`（settings.go:50-52 恢复并下发 effort）的迁移规则与《思考与长度释放》§3.4 一致（旧用户无新键为空操作）。

## 9 H1-H25 挂钩点逐条对应表

| H | 挂钩点 → 本稿机制 | H | 挂钩点 → 本稿机制 |
|---|---|---|---|
| H1 | agent.go:1232-1286 → 机制1（支撑：注入位置） | H14 | runtime.go:2717 → 机制6（可选） |
| H2 | agent.go:1284-1286 → 机制1（支撑） | H15 | goalmgr.go:607 → 不涉及（goal 已有深思通道） |
| H3 | agent.go:1060-1062 → 机制7/机制2 | H16 | goalmgr.go:256 → 不涉及（goal 完成判定） |
| H4 | agent.go:416-419 → 机制1 | H17 | bias.go:64 → 机制6 |
| H5 | agent.go:607 → 机制3 | H18 | gate.go:138 → 不涉及（出口门禁，触发侧） |
| H6 | agent.go:194 → 机制3 | H19 | tui.go:427 / gui/server.go:1429 → 机制4（可选） |
| H7 | agent.go:101-116 → 机制1（复制母本） | H20 | handoff.go:100 → 机制4 |
| H8 | agent.go:2025 → 机制6 | H21 | trace.go:21-26 → 机制6 |
| H9 | helpers.go:291 → 机制5（思路复用，不改本体） | H22 | helpers.go:64 / spec.go:167 → 总则（信号源） |
| H10 | deepseek.go:1078 → 机制2 | H23 | settings.go:24 → 机制2/机制7 |
| H11 | deepseek.go:961 → 机制2 | H24 | budget.go:643-695 → 机制5 |
| H12 | runtime.go:1108 → 机制2 | H25 | tools/task.go:191 → 机制4 |
| H13 | runtime.go:2962 / 3005 → 机制6 |  |  |

注：行号均经实测核对（完整文件路径见各机制"挂钩点"行）；"不涉及"项属触发侧/既有闭环，本稿不改动。

## 10 实施顺序与测试矩阵

| 批次 | 机制 | 理由 | 测试文件 |
|---|---|---|---|
| P0 | 机制7 + 机制6 | 先有护栏与度量基线，防多轮伤害、供后续校准 | think_budget_test.go / bias_test.go / think_round_test.go |
| P1 | 机制1 + 机制2 | 核心催轮与深挖（低成本高收益） | think_nudge_test.go / round_effort_test.go |
| P2 | 机制3 + 机制4 | 计划门与复核（结构化管理复杂任务） | plan_gate_test.go / verifier_default_test.go |
| P3 | 机制5 | 思考保留（依赖度量数据验证收益后开启） | thinking_protect_test.go |

验收自检命令（每批完成后执行）：
```bash
go build ./... && go test -count=1 ./internal/executor/ ./internal/model/ ./internal/runtime/ ./internal/ctxmgr/ ./internal/overseer/ ./internal/trace/
```

## 11 遗留问题与依赖

1. 复杂度阈值标定依赖机制 6 度量数据（首版保守常量，二版数据校准）。
2. 机制 4 的复核子代理输出协议（通过/问题清单）需与 overseer 报告结构对齐（buildOverseerReport，runtime.go:3020）。
3. 机制 2 的"轮次类型"判定需触发侧提供 verify 轮边界事件；接口为 `roundEffortOverride(kind)`，kind 由主循环维护。
4. GUI/TUI 多轮展示（轮次徽标、计划清单视图）列入后续 UI 稿；本稿只保证 trace 数据可消费。

*稿完。7 机制均"默认关闭 + 条件路由 + 零回归"，挂钩点行号经工作区实测核对。*
