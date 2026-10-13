# ContextLoom 思考/推理/多轮循环机制现状盘点报告

> 生成时间：本次盘点任务自动生成
> 项目：`H:\创业相关\contextloom-agent\contextloom`（Go 编码智能体，TUI + Wails GUI）
> 目的：为「复杂问题杜绝一次思考解决、鼓励并积极触发多次思考」深化设计提供**现状证据**。
> 方法：逐文件精读 + 行号级核对；所有结论以源码为准，格式 `文件:行号`。
> 说明：本报告只做盘点，不改动任何 `.go` 文件；行号基于当前工作区版本，后续改动可能漂移。

---

## 0. 阅读指南

- **机制名**：盘点对象。
- **代码落点**：`文件:行号`（函数定义或关键分支）。
- **与"多次思考"的关系**：支持（已有机制可促成多轮）/ 缺失（无相关机制）/ 冲突（现有逻辑鼓励一次收尾）。
- **可挂钩点**：未来可注入"多次思考"逻辑的代码位置。

---

## 1. 总览表

| # | 机制 | 代码落点 | 与"多次思考"关系 | 一句话现状 |
|---|------|---------|----------------|-----------|
| 1 | RunStream 主循环（流式） | `internal/executor/agent.go:1009` | 冲突 | 无轮次上限；模型给出非空回答且无工具调用即收尾（`agent.go:1284-1286`） |
| 1b | Run 主循环（同步/子 Agent） | `internal/executor/agent.go:358` | 冲突 | 更激进：`len(toolCalls)==0` 直接 break（`agent.go:416-419`） |
| 2 | PromoteThinkBody 空正文提升 | `internal/runtime/helpers.go:291` | 缺失（兜底型） | 只把思考尾段提升为正文展示，不触发第二轮思考 |
| 3 | 思考标记处理 | `agent.go:2025` / `helpers.go:319` / `tui/render.go:578` / `gui app.js:1888` | 支持（展示层） | 标签解析/分离完备，为"思考轮次可视化"打好地基 |
| 4 | 思考发送矩阵（effort/thinking） | `internal/model/deepseek.go:85,961,1078` | 缺失（静态单值） | 全局单档位，不随轮次/复杂度动态提升 |
| 5 | 思考链压缩（ctxmgr） | `internal/ctxmgr/budget.go:163,533,608,654` | 支持（空间释放） | 思维链被计入预算并可压缩，为多轮思考腾空间 |
| 6 | goal 模式多轮续跑 | `internal/goalmgr/goalmgr.go:256,607` | **支持（最强）** | 状态机驱动的跨轮循环：反思/缺陷/收敛/打磨/证据链 |
| 7 | 监工评分 | `internal/overseer/overseer.go:47`、`bias.go:64`、`scoring.go:10` | 支持（goal/task 评估） | 四维加权 + 偏差检测；task 模式评估不触发续跑 |
| 8 | 质量门禁（selfcheck） | `internal/selfcheck/gate.go:138` | 支持（goal 接线） | Pass/Fail/HumanReview 三态；仅 goal 模式接线 |
| 9 | generation params 热更新 | `internal/runtime/runtime.go:1108`、`settings.go:24` | 缺失（静态） | 思考开关/档位持久化，无按轮次动态调整 |
| 10 | 系统提示词策略 | `agent.go:591-700`、`agent.go:193-204` | 冲突（chat 明确"停止"） | task 鼓励并行派发；chat 明确"完成后直接停止，不要反复调用工具" |
| 11 | TUI/GUI 思考展示 | `tui/tui.go:389,426`、`gui/server.go:1411,1643` | 支持（可视化） | 折叠思考块 + goal 续跑；chat/task 无续跑 |
| 12 | 多 Agent（spawn/handoff/bus） | `executor/handoff.go:95`、`messagebus.go:52`、`tools/task.go:191` | 支持（可做验证轮） | 有并行派发与串行交接原语，但无"默认复核"流程 |
| 13 | trace/复杂度/其他 | `internal/trace/trace.go:25`、`runtime/helpers.go:64` | 缺失 | 有轮次埋点；无思考质量指标、无复杂度→策略映射 |

---

## 2. 逐机制详述

### 2.1 `internal/executor/agent.go` 主循环 RunStream

**代码落点**：`agent.go:1009`（函数）、`1060`（`for {` 主循环）、`1039-1042`（循环状态变量）。

**现状描述**：
- 循环状态变量：`noToolRounds`（连续无工具调用轮次）、`brokenToolRounds`（工具调用痕迹/截断累计轮次）、`forcedFinal`（token 预算兜底）、`round`（检查点轮次计数）（`agent.go:1039-1042`）。
- **无轮次上限**：注释明确"工具执行无轮次上限：任务该做多久做多久"（`agent.go:1052-1059`）。
- 终止条件共 5 条（`agent.go:1054-1059`）：
  1. `ctx.Done()` 用户中断（`agent.go:1080-1085`）；
  2. token 预算红线：压缩后仍超 `SafetyExceeded` → `forcedFinal=true` + 注入收尾提示（`agent.go:1065-1078`）；
  3. `detectLoop`：同工具同参数连续 ≥3 次（`agent.go:1303-1311`，实现见 `agent.go:1566-1590`）；
  4. `noToolRounds`：模型连续空输出（无有效正文）≥2 次（`agent.go:1232-1247`）；
  5. **模型不再调工具且正文非空 → 直接 `break` 收尾**（`agent.go:1284-1286`）。
- 每轮末尾 `sinkCheckpoint` 落盘（`agent.go:1325-1327`）；收尾 defer 落最终快照（`agent.go:1044-1050`）。
- 另有 `RunStreamWithBudget`（`agent.go:1768-1774`）：签名带 `maxIter` 参数但**已明确弃用**——`_ = maxIter`，注释"不再作为硬上限——工具执行无轮次上限"。即系统内没有任何"轮次数"控制概念（既无上限也无下限）。

**与"多次思考"的关系：冲突**。终止条件 5 是"一次思考"的直通车：模型第一轮给出任何非空正文即结束，无任何"复杂问题再想一轮/验证一轮"逻辑。注释还明确删除了旧的"任务尚未完成，请继续"催促（`agent.go:1284-1285`）——方向正确（防死循环），但也移除了唯一的续跑引导。

**可挂钩点**：
- `agent.go:1232-1286`（无工具调用分支）：收尾前注入"复杂问题强制验证轮"。
- `agent.go:1284-1286`（final break）：条件化拦截（复杂度/质量信号）。
- `agent.go:1060-1064`（主循环入口）：轮次策略钩子（最小思考轮数、轮次→effort 升级）。

### 2.1b `Run` 同步主循环（子 Agent 路径）

**代码落点**：`agent.go:358-480`。

**现状描述**：
- 子 Agent（`SpawnAgent` 路径）走同步 `Chat` 循环：`round := 0`（`agent.go:375`），每轮 `round++` 后直接 `a.backend.Chat`（`agent.go:385-407`）。
- **`if len(toolCalls) == 0 { a.Task.Payload = ...; break }`（`agent.go:416-419`）**：无任何空正文提示、无截断检测、无痕迹检测——比 RunStream 更"一次即终"。
- 循环检测 `detectLoop`（`agent.go:425-435`）、工具串行执行（`agent.go:448-472`）、`batchNudge` 批次提示（`agent.go:478`）、检查点（`agent.go:480`）。

**与"多次思考"的关系：冲突**。子 Agent 的产出质量完全依赖单轮模型行为；这是"子任务一次思考就交差"的底层原因之一。

**可挂钩点**：`agent.go:416-419`（同步路径收尾点）、`agent.go:478`（batchNudge 位置可并列加 thinkNudge）。

### 2.2 `internal/runtime/helpers.go` PromoteThinkBody

**代码落点**：`helpers.go:284-318`（实现），`helpers.go:319-410`（SplitReasoning，配套分离）。

**现状描述**：
- 当正文为空、结论滞留思考内时，取**最后一段非空思考的尾段**（≤1600 runes）加来源标记返回（`helpers.go:291-318`）。
- 调用方：
  - TUI：正文空且思考非空时提升展示（`tui/tui.go:385-394`）；
  - GUI：`promoteIfThinkOnly` 保留原始思维链 + 追加提升正文（`gui/server.go:1643-1657`）。
- 测试：`internal/runtime/promote_think_test.go:11`（基础）、`:53`（尾段边界）。

**与"多次思考"的关系：缺失（兜底型）**。它是"单轮思考失败"的展示层补救，不触发第二轮请求——提升出来的"结论"没有经过验证。

**可挂钩点**：`helpers.go:291`（可在提升前触发"重问一轮"）；调用点 `tui.go:389`、`gui/server.go:1651`（可改为"先重试再提升"）。

### 2.3 思考标记处理（splitThinkTagsAgent / 流式标记）

**代码落点**：
- `agent.go:2025-2060` `splitThinkTagsAgent`：剥离 `<|reasoning|>/<|/reasoning|>`、`<thinking>`、`<think>`；
- `helpers.go:319-410` `SplitReasoning`：正文/思维链分离（含旧格式 `thinking…response`）；
- `deepseek.go:619-699`：流式产出 `<|reasoning|>` 开/闭标记（`deepseek.go:631-633, 655-657, 690-699`）；
- TUI：`tui/render.go:578` `splitThinkTags`（支持 4 种格式）；
- GUI：`cmd/contextloom-gui/web/app.js:1888` `THINK_RE`、`:2059-2064` `think-block` 渲染。

**与"多次思考"的关系：支持（展示层）**。标签协议已完备，可承载"第 N 轮思考"的轮次标记扩展。

**可挂钩点**：`agent.go:2025`、`helpers.go:319`（思考计数/质量统计入口）；`app.js:1888`（多轮思考分组展示）。

### 2.4 `internal/model/deepseek.go` 思考发送矩阵

**代码落点**：
- `deepseek.go:74-95` `deepseekEffortRemap`：`low→low`、`medium/high→high`、`xhigh/max→max`；`off` 不进表（特殊语义）；
- `deepseek.go:961-990` `SetReasoningEffort`：`off` → `thinkingEnabled=false`；从 off 切回档位复位 `nil`；
- `deepseek.go:1078-1100` `applyThinkingParams`：4 条发送规则（sendEffort=false 全不发；nil 只发 effort；true 发 thinking+effort；false 发 thinking=disabled 且不发 effort）；
- `deepseek.go:102-119` `maxTokens`、`121-131` `inputLimit`：输出/输入预算独立；
- `deepseek.go:232-350` `Chat`（`261` 调 applyThinkingParams）、`382-780` `StreamEx`（`415` 调 applyThinkingParams）；
- 流式 reasoning 处理：`deepseek.go:619-699`；400 自修复：`144-185`（parseMaxTokensLimit / nextMaxTokensCap）。

**与"多次思考"的关系：缺失（静态单值）**。档位是全局设置（`settings.json` 持久化，`settings.go:24,50-52`），同一任务内不随轮次/复杂度动态变化；也没有"验证轮用更高 effort"的机制。

**可挂钩点**：`deepseek.go:1078`（applyThinkingParams 是按请求生效的注入点，可加"本轮 effort 覆盖"）；`deepseek.go:961`（档位 API 已具备）。

### 2.5 `internal/ctxmgr` 思考链压缩机制

**代码落点**：`internal/ctxmgr/budget.go`：
- `budget.go:163-217` `MessageTokens`：**思维链（ReasoningContent）与正文同规则计入**（`173-176`）；
- `budget.go:355+` `ForceCompact`：分级压缩；思维链截断参与（`533-560` 前缀截断时正文/思维链一起削；`608-612` 安全红线二次截断限 300；`654-695` 最终兜底折半思维链 `tcIdx=-2`）；
- 测试：`thinking_compact_test.go:16`（ForceCompact_ThinkingChainReleasesTokens）、`thinking_keeprecent_test.go:18`（ExactlyKeepRecentThinking）、`manual_unlimited_test.go:15,73`。

**与"多次思考"的关系：支持（空间释放）**。思维链真实占预算且可被压缩——多轮思考不会立刻打爆上下文；但压缩只保"空间"，不保"思考连续性"（旧轮思维链被削）。

**可挂钩点**：`budget.go:533-695`（可加"验证轮保留思考"策略）；`budget.go:163`（思考 token 统计可暴露为指标）。

### 2.6 `internal/goalmgr` goal 模式多轮续跑

**代码落点**：`internal/goalmgr/goalmgr.go` + `evidence.go` + `spec.go`。

**现状描述**：
- `DefaultMaxRounds = 0`（无上限，`goalmgr.go:31-34`）；`Start`（`goalmgr.go:77-99`）；
- 状态机：spec → plan → execute → check（打磨）→ done（`goalmgr.go:165-213` SetSpec、`256-465` ApplyRoundResultEx）；
- v2 自进化六机制（`ApplyRoundResultEx` 内）：
  ① 每轮反思 `[GOAL_REFLECT]`（`goalmgr.go:278-283`）；
  ② 缺陷清单 `[GOAL_DEFECTS]` + 监工低分自动缺陷（`goalmgr.go:324-355`）；
  ③ DoD 有效完成（`AllStepsValidDone`，`pkg/types/goal.go:191-205`）；
  ④ 收敛检测：进展签名不变 → `StalledRounds++`，≥2 轮注入"换策略"告警（`goalmgr.go:357-369`、`607-660` buildContinuation）；
  ⑤ 打磨轮：`[GOAL_DONE]` 首次只进打磨（`goalmgr.go:417-435`），`[GOAL_POLISH_DONE]` 经外部验收（`goalmgr.go:388-420`）；
  ⑥ 经验档案（`Lessons`）。
- 证据链：`evidence.go:101-190` `VerifyGoalProofs`（file/diff/test 三类物理通道，`goTestRun` 实测退出码）；`evidence.go:37-50` `ProofIO` 注入点；
- spec 门槛：`spec.go:19` `MaxSpecRounds=3`；`spec.go:110-150` `SpecGate` R1-R5（目标物指代/假设显式/验收可测量）；`spec.go:178-195` `DetectFuzziness`（弱动词/可测量/目标物三信号）。
- 接线：`runtime.go:616-624`（SetEvidenceVerifier / SetPolishVerifier）。

**与"多次思考"的关系：支持（最强机制）**。goal 模式本质上就是"多轮思考 + 多轮执行 + 外部验收"的完整闭环；但它**需要用户显式进入 goal 模式**，chat/task 模式完全不享受。

**可挂钩点**：
- `goalmgr.go:607` buildContinuation：注入"深思/多方案对比"指令的现成通道；
- `goalmgr.go:256` ApplyRoundResultEx：完成判定可加"思考质量"维度；
- `spec.go:178` DetectFuzziness：复杂度/模糊度信号源，可复用到 task/chat。

### 2.7 `internal/overseer` 监工评分

**代码落点**：
- `scoring.go:10-25`：四维权重 0.40/0.30/0.20/0.10（完整性/测试/质量/副作用）；
- `overseer.go:47-90` `Evaluate`：加权分 + 防作弊（相似度 <0.8 → 判失败并 `purgeAgentCheckpoints`，`95-105`）+ 约束门（min_test_coverage/min_code_quality）；
- `anti_cheat.go:5-38` `VerifyExecution`：claimed vs actual 测试计数相似度；
- `bias.go:64-130` `BiasAnalyze`：三类偏差（无证据全量声明 -20、早产完成 -30、自答式测试 -15），`TotalPenalty` 封顶 100（`bias.go:160-170`）；
- `code_quality.go:45+` `Analyze`：行宽/函数长/注释静态质量；
- `plan_nodes.go:22,88`：输入→计划节点拆分（`registerPlanNodes` 由 `runtime/helpers.go:21` 调用、`runtime.go:2036` 接线）。
- 评估入口：`runtime.go:2962-3018` `OverseerEvaluate`（偏差扣分合并）；报告构造 `runtime.go:3020-3055` `buildOverseerReport`（完整性/测试覆盖/代码质量/副作用）。

**与"多次思考"的关系：支持（评估层）**。评分是"要不要再想一轮"的天然判据；但现状：**task 模式评分后不触发任何续跑**（TUI 仅展示，`tui.go:400-415`），只有 goal 模式把评分接入状态机（`gui/server.go:1429-1431`、`tui.go:426-433`）。

**可挂钩点**：`overseer.go:47`（Evaluate 返回值可驱动"验证轮"）；`bias.go:64`（可加"单次草率回答"检测）；`runtime.go:2962`（task/chat 评估扩展点）。

### 2.8 `internal/selfcheck` 质量门

**代码落点**：
- `gate.go:9-32` 三档 `QualityNone/Fast/Standard`；`56-70` 三态裁决 `Pass/Fail/HumanReview`；
- `gate.go:138-152` `Gate.Admit`：静态不通过或评分 <90 → Fail；连续 3 轮不收敛 → HumanReview（`113-121` TrackRound）；
- `gate.go:155-170` `DefectsFromReport`：high/medium 问题转缺陷；`gate.go:184` `ForbidDone`；
- `static.go:85-113` `Check`：残留标记/未决 `[?]`（high）/未用符号；
- `tee.go:73-91` `Verify`：TEE 三态（Go 代码实测/非 Go/文档）；
- `ledger.go:29-49`：缺陷签名累计。
- 接线：`wiring.go:607` `recordGateVerdict`、`720-770` `goalGateReport`（监工分 70% + 静检 30%）、`857` `goalTEEPassed`、`897` `stripGoalDone`、`906-921` `selfcheckGateDefects`、`931-975` `verifyGoalPolish`（打磨轮外部验收）。

**与"多次思考"的关系：支持（goal 接线）**。门禁"有牙齿"（`runtime.go:2774-2790`：Fail 剥离 `[GOAL_DONE]`、强制缺陷清单）——这正是"一次交付不合格 → 再想再改"的现成回路；但同样仅 goal 模式。

**可挂钩点**：`gate.go:138`（Admit 可扩展为 task 模式出口门）；`wiring.go:720`（goalGateReport 可加"思考深度"输入）。

### 2.9 `internal/runtime/runtime.go` 生成参数热更新

**代码落点**：
- `runtime.go:1108-1126` `applyGenerationParams`：thinking 解析（配置显式值优先；否则 `ThinkingLength != "off"` 决定；都未配置传 nil）；调用时机铁律（必须先 SetReasoningEffort）；
- `runtime.go:1092-1101` `effectiveInputBudget`：输入预算（max_input_tokens > max_context）；
- `settings.go:13-40` `saveSettings`（`24` effort 持久化）、`42-100` `loadSettings`（`50-52` effort 恢复并下发）；
- 调用点：`runtime.go:473,1086,1469`。

**与"多次思考"的关系：缺失（静态）**。参数是"会话级"而非"轮次级"——无法实现"第一轮低 effort 快速试探、验证轮高 effort 深挖"。

**可挂钩点**：`runtime.go:1108`（加 per-round 覆盖入口）；`settings.go:24`（思考策略配置扩展位）。

### 2.10 系统提示词（buildMessages / strategy / chatSystemPrompt）

**代码落点**：
- `agent.go:591-700` task 模式 JSON sysPrompt：`strategy` 字段（`607`）鼓励 spawn_task 并行派发 + 多批次拆分；`multi_agent` 接口规范（`632-660`，仅 depth=0）；工具表/技能/持久化路径注入；
- `agent.go:707-760` goal 模式 sysPrompt：phase/round（`712` `"round": "%d/%d"`）；
- `agent.go:810-860` chat 模式：仅 system 提示 + 历史（`810-812`）；
- **`agent.go:193-204` chatSystemPrompt**："任务完成后直接给出最终答案并停止，不要反复调用工具"（`agent.go:194`）。

**与"多次思考"的关系：冲突（chat）/部分支持（task）**。task 的 strategy 引导"多步拆分/并行"，但针对的是**派发子任务**而非"多次思考"；chat 提示词明确鼓励**停止**。

**可挂钩点**：`agent.go:607`（strategy 加"复杂问题多轮思考/自查"条款）；`agent.go:193-204`（chat 提示词改造）；`agent.go:707-760`（goal 提示词加"深思"阶段说明）。

### 2.11 TUI/GUI 思考展示与续跑

**代码落点**：
- TUI：`tui/tui.go:385-394`（空正文提升）、`400-415`（监工评估展示）、`426-433`（goal 续跑判定 `GoalOnRoundEnd`）、`448-470`（`startGoalRound` 自动下一轮）；`tui/render.go:578`（splitThinkTags）；`tui/chatmsg.go:11,40`（MsgThinking/thinkMsg 折叠块）；
- GUI：`gui/server.go:1411-1460`（goal 循环：每轮 `OverseerEvaluate` → `GoalOnRoundEnd` → `ProcessGoalRound`；跨会话防护）；`1643-1657`（promoteIfThinkOnly）；
- 前端：`app.js:1888`（THINK_RE）、`2027-2064`（think-block 渲染/折叠）。

**与"多次思考"的关系：支持（goal 可视化+续跑）/缺失（chat/task）**。GUI goal 循环是唯一"自动发起下一轮"的前端路径；chat/task 一轮即完。

**可挂钩点**：`tui.go:426-470`、`gui/server.go:1411-1460`（续跑触发条件可扩展"验证轮"）。

### 2.12 多 Agent 机制（spawn_task / handoff / messagebus）

**代码落点**：
- `executor/handoff.go:20-40`（对标 OpenAI Agents SDK handoff 的注释）、`113` `handoffMaxDepth=3`、`120-147` `MakeHandoffTool`（串行控制权转移）；
- `executor/messagebus.go:11-16`（总线）、`52-75` `DrainInbox`（agent.go:1088-1097 每轮 drain）；
- `tools/task.go:191-217` `MakeSpawnTaskTool`（singleflight 合并重复派发）；
- `agent.go:89-97` `hasSpawnTask`、`101-116` `batchNudge`（批次提示，注入"可反复派发"引导）；
- `agent.go:1429-1470` `executeToolsConcurrent`：只读并行 / spawn_task 并行 / 写工具串行；
- 接线：`runtime.go:3134+` `SpawnTask`（子 Agent 创建 + 员工装配 + 监工评分返回）。

**与"多次思考"的关系：支持（可做验证轮）**。已有"verifier"角色概念（handoff 目标、员工中心），但没有**默认**的"回答后自动送 verifier 复核"流程——完全依赖模型主动调用。

**可挂钩点**：`handoff.go:100`（可做强制 verifier 交接）；`agent.go:101-116`（batchNudge 模式可复制为 thinkNudge）；`runtime.go:3134`（SpawnTask 可加"复核子任务"类型）。

### 2.13 其他相关机制

- **trace**：`internal/trace/trace.go:25-26`（Entry 含 `Round`）；`goal_round` 事件埋点 `runtime.go:2796-2805`（passed/score/stalled/defects）。→ 有轮次可观测性，**无思考质量指标**。
- **preprocessor**：`internal/preprocessor/vocab.go`（仅词汇提案），**无输入复杂度分析**。
- **audit**：搜索无思考相关内容。
- **复杂度**：`runtime/helpers.go:64-74` `estimateComplexity`（按长度 1/2/3 档）仅用于 `registerPlanNodes`（`helpers.go:21-37`、`runtime.go:2036`），**不影响思考策略**。
- **场景**：`runtime.go:1958-1962` chat 模式场景接管（`matchScene`）。
- **goal 启动**：`runtime.go:1991-2005`（goal 模式绑定/新建 GoalSession；`1999` token 预算 = maxContext × DefaultTokenMultiplier）。
- **目标循环入口**：`runtime.go:2831-2857` `ProcessGoalRound`（注入 continuation）；`runtime.go:2723-2805` `GoalOnRoundEnd`（协议解析/偏差/门禁/追踪）。

---

## 3. "一次思考问题"实锤清单

> 以下均为"复杂问题单次思考即结束 / 无强制验证"的**代码级证据**。

1. **RunStream 收尾直通车**：`agent.go:1284-1286`——"模型已给出实质回答且未调用工具 → 任务结束，直接收尾"，且注释明确不再注入续跑催促。第一轮非空回答 = 任务完成。
2. **同步路径更激进**：`agent.go:416-419`——`if len(toolCalls) == 0 { break }`，无空正文提示、无截断检测，单轮回答直接终止（子 Agent 全部走此路径）。
3. **chat 提示词鼓励停止**：`agent.go:194`——"任务完成后直接给出最终答案并停止，不要反复调用工具"。
4. **终止条件清单无"质量不足"项**：`agent.go:1053-1059` 五条终止条件全部是"防失控/防死循环"，没有一条是"思考深度不足需继续"。
5. **无最小思考轮数**：`round` 从 1 开始无下限约束（`agent.go:1061`、`375`），第一轮即可结束；`RunStreamWithBudget`（`agent.go:1768-1774`）的 `maxIter` 被显式弃用（`_ = maxIter`）——系统完全没有"轮次数"控制概念。
6. **task 模式评估不续跑**：监工在 task 模式创建（`runtime.go:2022-2039`），但 `OverseerEvaluate` 结果在 TUI 仅展示（`tui.go:400-415`）、在 GUI 仅 goal 循环使用（`gui/server.go:1429-1431`）——task 模式评分低不会触发任何下一轮。
7. **goal 续跑依赖协议标记而非思考质量**：`goalmgr.go:417-435`（`[GOAL_DONE]` 判定）、`442-450`（轮次提示）；模型不输出标记就没有额外轮次。
8. **空正文提示只保底不提质**：`agent.go:1232-1247`——`noToolRounds>=2` 的"继续"提示仅在正文为空时触发；非空但草率的回答不触发。
9. **无复杂度→思考策略映射**：`estimateComplexity`（`helpers.go:64`）只服务计划节点注册（`runtime.go:2036`），与 thinking/effort/轮次策略零耦合。
10. **思考强度全局静态**：`settings.go:24,50-52` 持久化单一 effort；`applyThinkingParams`（`deepseek.go:1078`）按请求常量下发，无"轮次级升级"。
11. **无强制验证轮**：回答后没有"自查/复算/交叉验证"注入路径；唯一的验证闭环（打磨轮 `goalmgr.go:388-420`、外部验收 `wiring.go:931`）只在 goal 模式且需 `[GOAL_DONE]` 触发。
12. **handoff 需模型主动**：`handoff.go:100-147` 的 verifier 交接完全依赖模型在工具循环中主动调用，无默认"回答后复核"。

---

## 4. "可挂钩点总表"（为深化设计列出所有可注入位置）

| # | 挂钩点（文件:行号） | 现状 | 可注入的"多次思考"逻辑 |
|---|--------------------|------|----------------------|
| H1 | `executor/agent.go:1232-1286` | 无工具分支收尾 | 复杂问题→强制验证轮（追问/自查/换角度复算） |
| H2 | `executor/agent.go:1284-1286` | final break | 收尾拦截条件（复杂度/质量信号） |
| H3 | `executor/agent.go:1060-1064` | 主循环入口 | 最小思考轮数、轮次计数→策略升级 |
| H4 | `executor/agent.go:416-419` | 同步路径 break | 子 Agent 同样接入验证轮 |
| H5 | `executor/agent.go:607` | task strategy 文本 | 加"多次思考/自查"指令 |
| H6 | `executor/agent.go:193-204` | chat 系统提示 | 改写"停止"导向为"复杂问题深思"导向 |
| H7 | `executor/agent.go:101-116` | batchNudge | 复制模式做 thinkNudge（轮次提示注入） |
| H8 | `executor/agent.go:2025` / `runtime/helpers.go:319` | 思考标签解析 | 思考轮次计数、思考质量统计 |
| H9 | `runtime/helpers.go:291` | PromoteThinkBody | 提升前触发重问一轮 |
| H10 | `model/deepseek.go:1078` | applyThinkingParams | per-round effort 覆盖（验证轮升级） |
| H11 | `model/deepseek.go:961` | SetReasoningEffort | 档位动态切换 API（已具备） |
| H12 | `runtime/runtime.go:1108` | applyGenerationParams | 轮次级参数下发入口 |
| H13 | `runtime/runtime.go:2962` | OverseerEvaluate | task/chat 模式评估扩展（驱动续跑） |
| H14 | `runtime/runtime.go:2723-2805` | GoalOnRoundEnd | 加"思考质量"状态维度 |
| H15 | `goalmgr/goalmgr.go:607` | buildContinuation | 注入"多方案对比/深思"文案 |
| H16 | `goalmgr/goalmgr.go:256` | ApplyRoundResultEx | 完成判定加思考轮数条件 |
| H17 | `overseer/bias.go:64` | BiasAnalyze | 加"单次草率回答"偏差类型 |
| H18 | `selfcheck/gate.go:138` | Admit | 出口门禁接入 task 模式 |
| H19 | `tui/tui.go:426-470` / `gui/server.go:1411-1460` | 续跑触发 | 扩展"验证轮"触发条件 |
| H20 | `executor/handoff.go:100` | handoff 工具 | 强制 verifier 复核流程 |
| H21 | `trace/trace.go:25` / `runtime.go:2796` | 轮次埋点 | 思考轮次/深度指标上报 |
| H22 | `runtime/helpers.go:64` / `goalmgr/spec.go:178` | 复杂度/模糊度信号 | 作为"多次思考"触发源接入策略层 |
| H23 | `runtime/settings.go:24` | effort 持久化 | 思考策略配置扩展位 |
| H24 | `ctxmgr/budget.go:533-695` | 思考链压缩 | "验证轮保留思考"压缩策略 |
| H25 | `executor/messagebus.go:52` / `tools/task.go:191` | 多 Agent 原语 | 子 Agent 交叉复核（多视角思考） |

---

## 5. 缺口总结（对照"杜绝一次思考、鼓励多次思考"目标）

1. **无"思考轮次"概念**：系统只统计工具执行轮次（`round`），没有"思考轮/验证轮"的显式建模与下限约束（H3/H14）。
2. **无复杂度分级触发**：`estimateComplexity`/`DetectFuzziness` 存在但未接入思考策略；复杂问题与简单问题走同一条"一轮收尾"路径（H22）。
3. **chat/task 无自动验证闭环**：监工/门禁/证据链全部只在 goal 模式接线；chat/task 回答质量低时无兜底（H13/H18）。
4. **无"回答前自查"环节**：收尾路径（H1/H2/H4）没有强制自检/复算/交叉验证注入。
5. **思考强度静态**：effort 全局单值、按会话持久化，无法"首轮轻探→验证轮深挖"（H10/H11/H12/H23）。
6. **无多轮思考度量**：trace 有轮次但无思考轮数/深度/收益指标，深化设计缺乏基线数据（H21）。
7. **多 Agent 复核非默认**：verifier/handoff 原语齐备但依赖模型主动，无"关键产出默认送审"流程（H20/H25）。
8. **提示词方向不一致**：task 鼓励"多步拆分"、chat 明确"停止"、goal 有打磨轮——三模式对"多思考"的引导强度不一致（H5/H6）。
9. **压缩可能截断思考连续性**：旧轮思维链被优先截断/折半（`budget.go:608-695`），多轮深度思考的"记忆"会被压缩策略侵蚀（H24）。

---

## 6. 附：证据与测试文件索引

| 机制 | 关键源码 | 对应测试 |
|------|---------|---------|
| RunStream 主循环 | `internal/executor/agent.go:1009-1330` | `agent_stream_test.go`、`zz_1607_agent_test.go`、`zz_1655_reasoning_test.go` |
| Run 同步路径 | `internal/executor/agent.go:358-480` | `agent_test.go`、`agent_v2_test.go` |
| PromoteThinkBody | `internal/runtime/helpers.go:284-318` | `internal/runtime/promote_think_test.go` |
| 思考标记 | `agent.go:2025`、`helpers.go:319`、`tui/render.go:578` | `order_regression_test.go`、`help_bug_test.go` |
| 思考矩阵 | `internal/model/deepseek.go:74-95,961-1100` | `internal/model/*_test.go` |
| 思考链压缩 | `internal/ctxmgr/budget.go:163-217,533-695` | `thinking_compact_test.go`、`thinking_keeprecent_test.go`、`manual_unlimited_test.go` |
| goal 状态机 | `internal/goalmgr/goalmgr.go:256-465,607-660` | `goalmgr_test.go`、`spec_test.go`、`evidence_test.go` |
| 证据链 | `internal/goalmgr/evidence.go:101-190` | `evidence_test.go` |
| spec 门槛 | `internal/goalmgr/spec.go:110-195` | `spec_test.go`、`runtime/spec_flow_test.go` |
| 监工 | `internal/overseer/*.go` | `overseer_test.go`、`scoring_test.go`、`anti_cheat_test.go`、`bias_test.go`、`code_quality_test.go`、`runtime/overseer_evaluate_test.go` |
| 门禁 | `internal/selfcheck/*.go` | `selfcheck_test.go`、`runtime/wiring_test.go`、`ledger_test.go` |
| 生成参数 | `runtime/runtime.go:1108-1126`、`runtime/settings.go` | `generation_params_test.go`、`settings_test.go` |
| 多 Agent | `executor/handoff.go`、`messagebus.go`、`tools/task.go:191` | `handoff_test.go`、`messagebus_test.go`、`bus_subagent_test.go`、`spawn_task_test.go` |

---

*报告完。所有行号均来自当前工作区代码实测，若代码演进请以最新源码为准。*
