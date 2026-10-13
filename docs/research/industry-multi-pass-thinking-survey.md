# 业界前沿"多轮思考触发"机制调研报告

> 副题：为「复杂问题杜绝一次思考解决、鼓励并积极触发多次思考」深化设计提供对标依据
> 读者：ContextLoom 深化设计者（executor / runtime / model / goalmgr / overseer / selfcheck 维护者）
> 目标：回答两个问题——① 业界用什么机制"强制或鼓励"模型多想一轮；② 这些机制如何映射到 ContextLoom 现状盘点的 H1-H25 挂钩点。
> 方法：本环境无网络工具（web_fetch 不在可用工具集），以既有文档实证（A 级）+ 高置信模型知识（B 级）+ 待核实项（C 级）综合成文；未做在线核实。
> 承接：功能对比见《业界前沿对标分析文档.md》；ContextLoom 现状与 12 条"一次思考"实锤见《research/contextloom-thinking-inventory.md》；dsh 机制归因见《dsh能力释放机制深度分析.md》。本文只写"多轮思考触发"这一横切主题，不重复以上三篇。
> 置信度约定：A=文档/源码实证（可回查）；B=高置信模型知识（未在线核实）；C=需核实（具体数值/版本可能过时）。

---

## 0 结论先行

1. 业界没有单一"多想一轮"开关；有效做法是**七类机制的组合**：门控（Gate）、预算下限（Budget Floor）、验证轮（Verification Pass）、多采样（Multi-Sample）、度量防作弊（Metering）、状态外化（External State）、提示注入（Prompt Nudge）。
2. "强制多轮"最可靠的载体是**状态机 + 外部门禁**（dsh goal-round-driver 的 round 预算、ContextLoom goalmgr 打磨轮、Claude Code plan 审批），而不是提示词请求模型"多想想"。
3. "鼓励多轮"的协议级手段是**思考预算显式化**：Anthropic budget_tokens（≥1024）、OpenAI reasoning_effort 档位、s1 budget forcing（"Wait" 续思 / "Final Answer" 截断）。
4. 跨轮思考**默认不保留**：DeepSeek reasoning_content 不回传、OpenAI 推理隐式丢弃——"多次思考"必须由 harness 在**轮次结构**上制造，不能指望模型自己记住。
5. 验证轮已有成熟范式：CoVe（独立验证问题）、Reflexion（反思记忆）、Self-Refine（迭代反馈）——可分别落到 ContextLoom 的 H1/H15/H9。
6. 多轮必须**有上限控制**：overthinking 研究显示简单问题多思考损害准确率与成本；正解是"复杂度→轮数/预算"条件路由（RouteLLM / Snell）。
7. 行业共识正从"多 Agent 扇出"转向"单线程深上下文"（Cognition《Don't Build Multi-Agents》）；多次思考优先在同一上下文内做验证/修订轮，多 Agent 用于独立复核。
8. 对 ContextLoom 最高优先级四件事：收尾拦截（H1/H2）、复杂度路由（H22/H3）、轮次预算显式化（H3/H16）、轮次级 effort 升级（H10-H12）；均可在现有挂钩点以"配置开关 + 条件注入"实现，无需改协议。

### 0.1 机制域 → 挂钩点速查

| 机制域 | 主要落点（H 编号） | 对应章节 |
|---|---|---|
| 思考预算 / 档位 | H10-H12、H23 | §1、§2、§8 |
| 验证轮 / 收尾拦截 | H1、H2、H4、H9、H18 | §3、§10 |
| 轮次状态 / 驱动 | H3、H7、H14、H16、H19、H21 | §4、§7 |
| 计划 / 清单外化 | H5、H6、H15 | §5、§9 |
| 复核子代理 | H20、H25 | §5、§7、§9 |
| 思考资产保留 | H8、H24 | §1、§6 |
| 度量 / 防作弊 | H13、H17、H21 | §8、§10 |

## 1 Claude extended thinking / interleaved thinking

- **来源**：Anthropic API 文档与 Claude 3.7/4 发布说明（B；关键词档位数值 C）。
- **核心思想**：把"想多久"变成请求级参数（budget_tokens）；把"工具调用之间能否继续想"变成协议能力（interleaved thinking）；把"上一轮的思考"变成必须回传的显式块（thinking blocks 含 signature）。
- **关键参数/接口**：
  - `thinking: {type: "enabled", budget_tokens: N}`，N ≥ 1024 且必须小于 `max_tokens`（Claude 3.7 起）；
  - 多轮工具调用场景中，thinking 块需**原样回传**（服务端校验签名）——思考是有状态资产，不是可丢弃的日志；
  - Claude 4 系列：`interleaved-thinking-2025-05-14` beta，允许每次工具结果返回后重新思考（"think more after each tool result"）；
  - Claude 4 默认返回**思考摘要**（summarized thinking），完整思考不对外暴露。
- **效果证据**：官方定位为复杂推理/编码任务的准确率杠杆；Claude Code 用关键词分级（think / think hard / ultrathink，约 4k/10k/32k 档，C）把预算控制权交给用户。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - 轮次级思考预算：`applyThinkingParams`（H10，`model/deepseek.go:1078`）已是"按请求生效"的注入点，可加 per-round effort/budget 覆盖；
  - 思考资产的跨轮保留：对应 ctxmgr 思考链压缩（H24，`ctxmgr/budget.go:533-695`）——至少保留"验证轮所需的上一轮思考尾段"；
  - 预算下限：为复杂问题设"最小思考预算/最少思考轮"，防首轮草率收尾（H3，`agent.go:1060-1064`）；
  - 成本含义：思考 token 按输出计费，多轮思考直接抬升成本——预算护栏是"鼓励多轮"的配套件（H23）；
  - 注：ContextLoom 走 OpenAI 兼容协议，无 thinking 块回传语义；借鉴的是"思考是显式、可计量、可回传资产"的设计观，而非照搬字段。

## 2 OpenAI o1/o3/o4-mini：reasoning effort 与 reasoning summary

- **来源**：OpenAI 平台文档与模型发布说明（B；GPT-5 档位细节 C）。
- **核心思想**：用**离散档位**（reasoning_effort）替代连续预算；用摘要（reasoning summary）暴露思考但不暴露原文；推理 token 计费但**跨轮不保留**。
- **关键参数/接口**：
  - `reasoning_effort: low | medium | high`（o1 / o3 / o4-mini；GPT-5 起另有 minimal 等档，C）；
  - `reasoning.summary: auto | concise | detailed`（o3/o4-mini），仅摘要可见；
  - Responses API 的 `reasoning.encrypted_content` 可在无状态调用间携带推理项；Chat Completions 路径不保留；
  - 官方建议：不要再靠"think step by step"提示词催思考，档位即控制面。
- **效果证据**：档位与准确率/成本单调相关（官方基准，B）；"跨轮 reasoning 丢失"导致每轮从零思考、长任务一致性差（社区广泛报告，B）。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - ContextLoom 的 `deepseekEffortRemap`（H10/H11）已是五档映射，缺的是**按轮次/复杂度动态选择**：首轮低档试探、验证轮升档（H12，`runtime.go:1108`）；
  - "推理不回传"与 ContextLoom 现状一致（ctxmgr 不回传 reasoning）：深化设计不能依赖模型自记忆，必须显式制造轮次；
  - 摘要策略可作展示参考：TUI/GUI 思考折叠块（`tui/chatmsg.go`、`app.js:1888`）已有地基，可加"轮次摘要"。

## 3 经典论文矩阵：多轮思考的学术底座

- **来源**：以下均为公开论文（B；效果数值为论文口径，未逐项复核）。
- **核心思想**：多轮思考有三种原语——**反思**（对失败/反馈再思考）、**验证**（独立复核再修订）、**搜索**（多路径采样与选择）。

| 机制 | 来源（B） | 核心思想 | 关键机制细节 | 效果证据（论文口径） |
|---|---|---|---|---|
| Reflexion | Shinn et al., NeurIPS 2023 | 语言化强化学习：失败→反思→写入记忆→重试 | actor/evaluator/self-reflection 三件套；反思存 episodic memory | ALFWorld/HumanEval 显著提升（HumanEval 91% pass@1, GPT-4） |
| Self-Refine | Madaan et al., NeurIPS 2023 | 同一模型自我反馈迭代修订 | generate→feedback→refine 循环，无外部信号 | 7 类任务平均约 +20% 绝对提升 |
| Tree of Thoughts | Yao et al., NeurIPS 2023 | 思考=搜索树节点，可前瞻/回溯 | BFS/DFS + 状态评估器 | Game of 24：CoT 4% → ToT 74%（GPT-4） |
| Graph of Thoughts | Besta et al., AAAI 2024 | 思考图：可聚合/合并多路思考 | 顶点=思考，边=聚合/精炼操作 | 排序任务较 ToT 质量 +62%、成本 -31% |
| Plan-and-Solve | Wang et al., ACL 2023 | 先规划后执行（零样本） | "devise a plan, then execute" 提示 | 多数学/推理数据集超零样本 CoT |
| ReWOO | Xu et al., 2023 | 推理与观察解耦：先出全计划再执行 | planner 输出带变量引用的计划；worker 执行 | HotpotQA 同精度下 token -64% |
| Least-to-Most | Zhou et al., ICLR 2023 | 分解→按序求解，前答案喂后问 | 子问题链式求解 | SCAN length split 16%→99.7% |
| Self-Consistency | Wang et al., ICLR 2023 | 多路径采样 + 多数投票 | 采样 k 条 CoT 后聚合 | GSM8K +17.9%，SVAMP +11%，AQuA +12.2% |
| Step-Back | Zheng et al., ICLR 2024 | 先退一步抽象原理再推理 | 抽象提问→基于原理回答 | STEM/多跳 QA 稳定提升（TimeQA 约 +27%） |
| Chain-of-Verification | Dhuliawala et al., 2023 | 草稿→生成验证问题→独立作答→修订 | 验证问题**隔离作答**，避免同上下文偏差 | 列表类 QA 幻觉显著下降 |

- **对 ContextLoom 可借鉴点 + 落点建议**：
  - 验证轮首选 CoVe 形态（独立复核，而非"再读一遍自己的答案"）：落 H1/H2（`agent.go:1232-1286`、`1284-1286`）；
  - 反思记忆可复用 goalmgr `Lessons` 机制并扩展到 chat/task（H15，`goalmgr.go:607`）；
  - 多采样（Self-Consistency）成本高，只建议用于"高价值低置信"结论（H13，`runtime.go:2962`）；
  - 搜索类（ToT/GoT）不引入为默认；以"多方案对比"轻量提示词形态服务复杂问题（H5/H15）。

## 4 sequential-thinking MCP server

- **来源**：modelcontextprotocol/servers 官方参考实现（B；字段清单与任务给定一致）。
- **核心思想**：把"思考"外化为一次工具调用流——每一步思考是一个 tool call，host 依 `nextThoughtNeeded` 自动续调用，从**协议层结构性保证**多步思考发生。
- **关键参数/接口**（think 工具 schema）：
  - `thought`（本步思考文本）、`thoughtNumber` / `totalThoughts`（进度，**总数可在过程中上调/下调**）；
  - `isRevision` / `revisesThought`（修订某一步）、`branchFromThought` / `branchId`（分支探索）、`nextThoughtNeeded`（是否继续）；另有 `needsMoreThoughts` 等字段（C）。
- **效果证据**：无正式基准；社区广泛用于复杂问题分解与修订（B/C）。其价值在**交互协议**而非算法：多轮由字段驱动，不依赖模型自觉。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - "轮次提示注入"的现成范式：ContextLoom 已有 `batchNudge`（H7，`agent.go:101-116`），复制为 `thinkNudge` 即可实现"复杂问题第 N 轮提示继续/修订"；
  - 修订/分支语义可映射为"验证轮：修订上一轮结论"（H1）与"多方案轮：分支对比"（H15）；
  - 关键差异：MCP 版是模型自愿调用工具；ContextLoom 应把它升级为 **harness 条件强制注入**，否则仍受"模型不调用即无效"制约。

## 5 Claude Code：plan mode / TodoWrite / subagent / thinking 展示

- **来源**：Claude Code 官方文档与实践（B；关键词档位数值 C）。
- **核心思想**：四层"防一次思考"——plan mode 把"先想后做"变成**审批门**；TodoWrite 把计划外化为可追踪状态；subagent 用独立上下文做研究/验证；thinking 展示让用户看见思考。
- **关键机制**：
  - **plan mode**：只读探索→产出计划→用户批准后才执行（Shift+Tab 切换模式）；复杂任务默认先进入计划态；
  - **TodoWrite**：结构化 todo（pending/in_progress/completed），系统提示要求频繁更新——"每步都有状态"迫使多步推进；
  - **subagent（Task 工具）**：独立上下文窗口的子代理（通用/探索/计划），用于并行研究、隔离脏上下文、复核；
  - **thinking 关键词**：think < think hard < think harder < ultrathink（约 4k/10k/16k/32k，C），把思考预算交给用户表达。
- **效果证据**：Anthropic 内部与社区大规模采用（编码任务多步执行的默认形态）；无公开量化（B）。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - plan 审批门可映射为 goalmgr spec/plan 阶段的"用户确认"轻量版（H15/H19）；chat/task 模式可加"复杂问题先出计划再执行"软引导（H5/H6 提示词，`agent.go:193-204`、`607`）；
  - TodoWrite 对应 ContextLoom 缺失的"任务清单外化"：可用现有 checkpoint/trace（H21）加轻量 todo 状态；
  - subagent 复核对应 handoff verifier（H20/H25）：把"回答后送独立子代理复核"从可选变默认（条件触发）。

## 6 DeepSeek R1：长思考、Aha moment、reasoning_content 协议

- **来源**：DeepSeek-R1 技术报告与 API 文档（A/B 混合；R1-0528 数值 C）。
- **核心思想**：纯 RL 训练出**自发长思考**（含"等待/重查"的 Aha moment）；API 以 `reasoning_content` 暴露思考，但**跨轮不回收**——思考只属于当前轮。
- **关键参数/接口**：
  - `reasoning_content` 与 `content` 并列返回；多轮对话中**不要将 reasoning_content 回传**（服务端忽略/可能报错）；
  - R1-Zero 的 Aha moment：模型自发出现"Wait, let me re-check"式回查（技术报告实证，A）；
  - R1-0528：思考深度显著加长（平均思考长度近乎翻倍，约 12k→23k tokens，C），幻觉率下降。
- **效果证据**：R1 在数学/代码基准对标 o1（技术报告）；Aha moment 是"单轮内多路径回查"的自发形态证据——模型**单轮内**会多路回查，但**跨轮**无记忆。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - 现状盘点已确认 ContextLoom 不回传 reasoning、思维链计入预算可压缩（H24）——与 R1 协议一致；因此多轮思考要靠轮次结构显式制造（H1/H3/H7）；
  - 协议一致性检查：ContextLoom 发送矩阵（`deepseek.go:1078`）与 R1 协议对齐（thinking 开关 + effort 映射）；多轮思考不需要改协议，只需要轮次结构；
  - "Aha 式回查"可提示词化：验证轮注入"请换一个角度复算/回查关键假设"（H1/H15），成本低于多采样；
  - reasoning 展示与轮次标记（H8）：思考块加"第 N 轮/验证轮"前缀，为 TUI/GUI 多轮分组展示铺路（`app.js:1888`）。

## 7 dsh（DeepSeek Harness）：goal round-driver / plan-mode / subagent / 事件溯源

- **来源**：《dsh能力释放机制深度分析.md》《业界前沿对标分析文档.md》（A，源码/文档实证）。
- **核心思想**：把"多轮"做成**事件溯源的显式状态机**：goal 有 round 预算、plan 有模式事件、subagent 有 6 种 provider、全过程可重放。
- **关键机制**：
  - **goal 包 + goal-round-driver**：事件溯源 goal 状态 + **round 预算**，驱动器自动推进轮次（对标文档实证）；
  - **plan-mode（/plan 软引导）**：计划是 `plan/mode` 事件，进入计划态先探索再行动；
  - **subagent Seam**：6 种 provider（in-process / fork / ACP / Codex / Claude Code / dsh-sdk）+ workflow 脚本编排 + jobs 后台任务；
  - **事件溯源不变式**："Model-visible means logged"——思考/计划/轮次全部可从 log 重放（turn/start、assistant/chunk、goal/change、plan/mode…）。
- **效果证据**：机制完备性为文档级实证（A）；相对 ContextLoom 的核心差距是"轮次是一等状态" vs "轮次只是循环计数"。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - **轮次预算显式化**：goalmgr 现为 `DefaultMaxRounds=0`（无上限）的状态机（H14/H16，`goalmgr.go:256`）；可加"最小轮数/验证轮预算"字段，对齐 dsh round 预算思路；
  - plan-mode 软引导：goalmgr 的 spec→plan 阶段可加"计划事件"记录（H21 trace 扩展）；
  - step 事件可观测性：dsh 每轮工具步骤可追踪（turn/start、tool/call），对应 ContextLoom trace 埋点（H21，`trace.go:25`）；
  - 事件溯源思想轻量落地：trace 加 `think_round` 事件（轮次/触发原因/结果），为度量与回放提供基线；
  - subagent 复核：dsh subagent Seam 对应 ContextLoom spawn_task/handoff（H20/H25），差距在"默认触发"而非原语缺失。

## 8 复杂度评估与路由：让"多思考"有条件、有上限

- **来源**：RouteLLM、FrugalGPT、Hybrid LLM、Snell test-time scaling、s1、overthinking 系列（B）。
- **核心思想**：思考量应按**问题难度**分配：简单问题快速通道（防过度思考），复杂问题加轮加预算（防一次思考）；最优策略随难度切换（顺序修订 vs 搜索采样）。
- **关键机制**：
  - RouteLLM（LMSYS 2024）：强弱模型路由，约 85% 成本下降保持 95% GPT-4 性能（论文口径）；
  - FrugalGPT（2023）：级联 + 打分，最高约 98% 成本下降；
  - Snell et al.（2024）：**最优 test-time compute 策略依赖难度**——简单题用修订（顺序多轮），难题用搜索（多采样选择）；FLOPs 匹配下可超 14× 大模型；
  - s1（Muennighoff et al. 2025）：**budget forcing**——追加 "Wait" 强制续思、"Final Answer" 强制截断；s1-32B 在 MATH500/AIME24 超 o1-preview；
  - overthinking（Chen et al. 2024；OverThink 2025）：强模型在简单问题上过度思考，切非思考模式可提准确率并降成本。
- **效果证据**：均为论文报告（B）；共同结论：**无条件的多思考有害**，条件路由是正解。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - ContextLoom 已有信号但未接线：`estimateComplexity`（`helpers.go:64`）与 `DetectFuzziness`（`spec.go:178`）→ 接入思考策略层（H22，P0）；
  - "budget forcing" 的 ContextLoom 版：收尾拦截时注入一次"续思提示"（H1），并设**最大验证轮数**上限（防过度）；
  - 简单问题护栏：低复杂度直接走现状单轮（默认行为不变），只对中高复杂度启用多轮（H3）。

## 9 产品策略：Cognition/Devin、Cursor、Windsurf

- **来源**：产品文档与工程博客（B/C；版本细节 C）。
- **核心思想**：编码 Agent 的"多轮"主流形态 = **计划-批准-执行-自测循环**；并行化是第二手段；上下文工程优先于多 Agent 扇出。
- **关键机制**：
  - Devin（Cognition）：先出计划、用户可改，再执行；2.0 起并行 Devins + Wiki/Search；工程博客《Don't Build Multi-Agents》（2025）主张**单线程深上下文**优于多代理扇出；
  - Cursor：Agent 模式自动多步（读写/运行/迭代），MAX 模式用长上下文推理模型承载深思考；2.0 起并行 agent 与计划视图（C）；
  - Windsurf：Cascade 自动迭代（执行命令→读输出→继续修正），Write/Chat 模式分离（C）。
- **效果证据**：产品行为与采用度为间接证据；Cognition 博客给出"上下文共享原则"的工程论证（B）。
- **对 ContextLoom 可借鉴点 + 落点建议**：
  - 验证轮优先**同上下文**（Cognition 原则）：落 H1/H2，避免过早 spawn 子代理丢失上下文；
  - 独立验证（无共享上下文偏差）用于关键结论复核：落 H20/H25（verifier 子代理）；
  - 计划视图：GUI/TUI 已有思考折叠与 goal 续跑展示（H19），可加"计划清单"视图（对齐 TodoWrite / Devin plan）。

## 10 "防一次思考"机制清单（横切汇总）

按触发方式分七类（每类：代表机制 | 强制/自愿 | ContextLoom 落点）：

1. **门控类（Gate）**：plan 审批（Claude Code）、goal 完成剥离（ContextLoom `stripGoalDone`）、spec 门槛 R1-R5（`spec.go:110-150`）、出口门禁（`gate.go:138`）——强制；落点 H15/H16/H18。
2. **预算下限类（Budget Floor）**：budget_tokens≥1024（Anthropic）、effort 档位（OpenAI）、budget forcing（s1）——半强制；落点 H3/H10-H12。
3. **验证轮类（Verification Pass）**：CoVe、Reflexion、打磨轮（goalmgr）、监工低分 Fail（`gate.go:138`）——半强制；落点 H1/H2/H4/H9。
4. **多采样类（Multi-Sample）**：Self-Consistency、best-of-N、ToT 搜索——自愿 + 成本敏感；落点 H13（高价值场景专用）。
5. **度量防作弊类（Metering）**：ContextLoom 三类偏差（无证据 -20 / 早产完成 -30 / 自答式测试 -15，`bias.go:64-130`）、anti-cheat 实测比对——强制；落点 H17/H21（加"单次草率回答"偏差）。
6. **状态外化类（External State）**：sequential-thinking 轮次字段、TodoWrite、dsh 事件溯源、goal 轮次状态——结构性；落点 H7/H14/H21。
7. **提示注入类（Prompt Nudge）**：think 关键词（Claude Code）、"Wait" 续思（s1）、ContextLoom chat 提示词反向修改（`agent.go:194` 现鼓励"停止"）——自愿但低成本；落点 H5/H6。

小结：七类中，ContextLoom **已具备原语但未接线**的是——验证轮（H1/H2）、门控（H18）、度量（H17）、状态外化（H21）；这正是深化设计的四个起点。

## 11 对比总表

| 机制 | 多轮触发方式 | 状态载体 | 强制/自愿 | 上限控制 | 对 ContextLoom 优先级 |
|---|---|---|---|---|---|
| Claude thinking budget | 请求级预算 | thinking 块（签名回传） | 半强制 | budget < max_tokens | P1（H10-H12） |
| Claude interleaved thinking | 工具结果后自动续思 | 同上 | 自愿（协议支持） | 每步预算 | P1（H10） |
| OpenAI reasoning effort | 档位选择 | 无（跨轮不保留） | 半强制 | 档位上限 | P1（H11/H12） |
| Reflexion | 失败后反思重试 | 反思记忆 | 半强制 | 重试次数 | P1（H15） |
| Self-Refine | 自我反馈迭代 | 对话内 | 自愿 | 迭代次数 | P2（H9） |
| CoVe | 独立验证问题 | 草稿+验证问答 | 半强制 | 验证问题数 | P0（H1/H2） |
| ToT / GoT | 搜索 / 分支 | 思考树/图 | 自愿 | 搜索预算 | P2（H15） |
| Self-Consistency | 多采样投票 | 无 | 自愿 | 采样数 k | P2（H13） |
| sequential-thinking MCP | 工具流自动续调用 | thoughtNumber / totalThoughts | 自愿（工具协议） | totalThoughts | P1（H7） |
| Claude Code plan mode | 审批门 | 计划文本 | 强制（门） | 单计划 | P1（H15/H19） |
| Claude Code TodoWrite | 清单状态推进 | todo 列表 | 半强制 | 列表项 | P2（H21） |
| Claude Code subagent | 独立上下文复核 | 子代理上下文 | 自愿 | 深度 3（ContextLoom 已有） | P1（H20/H25） |
| DeepSeek R1 reasoning_content | 单轮内自发回查 | 无（跨轮不回收） | 自愿（训练获得） | 输出上限 | P0 参考（H24 协议一致性） |
| dsh goal-round-driver | 状态机驱动轮次 | 事件溯源 goal 状态 + round 预算 | 强制（驱动） | round 预算 | P0（H3/H14/H16） |
| dsh plan-mode | 计划事件软引导 | plan/mode 事件 | 半强制 | — | P1（H21） |
| s1 budget forcing | 追加 "Wait" 续思 | 输出流 | 强制（解码注入） | "Final Answer" 截断 | P1（H1 注入形态） |
| RouteLLM / FrugalGPT | 难度路由 | 路由器 | 强制（路由） | 成本预算 | P0（H22） |
| Snell test-time scaling | 难度→策略映射 | 无 | 策略选择 | 计算预算 | P0（H22/H3） |
| overthinking 缓解 | 简单题降档/截断 | 无 | 强制（护栏） | 思考上限 | P1（H3/H23） |
| Devin / Cursor / Windsurf | 计划-执行-自测循环 | 计划 / todo / checkpoint | 半强制 | 轮次/步数 | P1（H5/H6） |
| ContextLoom goal 模式（现状） | 状态机 + 协议标记 | goal 状态机 | 强制（goal 内） | 无上限 | 扩展基准（H14-H16） |

## 12 Top 10 可直接借鉴机制（按优先级排序）

| 排序 | 机制 | 为什么（依据） | 落点 |
|---|---|---|---|
| 1 | 收尾拦截 + 验证轮（CoVe 形态） | 直接消灭"第一轮非空即结束"（实锤 1/2/4）；独立复核比自我重读有效（§3） | H1/H2/H4（`agent.go:1284-1286`、`416-419`） |
| 2 | 复杂度→思考策略路由 | 多思考必须有条件（overthinking 反例）；现成信号未接线（实锤 9） | H22（`helpers.go:64`、`spec.go:178`）+ H3 |
| 3 | 轮次预算显式化（dsh round 预算） | "轮次"当前不是一等状态；无下限也无"验证轮"概念（缺口 1） | H3/H14/H16（`goalmgr.go:256`） |
| 4 | 轮次级 effort/预算升级 | 首轮轻探→验证轮深挖；注入点已就绪（§1/§2） | H10/H11/H12（`deepseek.go:1078`） |
| 5 | thinkNudge 续思注入（sequential-thinking / s1） | 低成本结构性催轮；batchNudge 模式可直接复制（§4） | H7（`agent.go:101-116`） |
| 6 | 计划门 / 计划外化（plan mode + TodoWrite） | 复杂问题先出计划；对抗 chat"停止"提示词（实锤 3） | H5/H6（`agent.go:193-204`、`607`） |
| 7 | 默认验证子代理（verifier handoff） | 独立上下文复核关键结论；原语齐备缺默认触发（缺口 7） | H20/H25（`handoff.go:100`、`tools/task.go:191`） |
| 8 | 思考链保留策略（thinking 块 / reasoning 协议） | 多轮需要"上一轮思考"作输入；压缩策略须区分验证轮（缺口 9） | H8/H24（`budget.go:533-695`） |
| 9 | 思考度量与"草率回答"偏差 | 无度量则深化设计无基线；bias 机制现成（实锤 6） | H17/H21（`bias.go:64`、`trace.go:25`） |
| 10 | 过度思考护栏 | 防"一律多轮"伤害简单任务与成本（§8） | H3/H23（`settings.go:24`） |

## 13 置信度说明与使用建议

- **A 级（可回查）**：dsh 机制（《dsh能力释放机制深度分析.md》《业界前沿对标分析文档.md》）；ContextLoom 现状与 H1-H25（《research/contextloom-thinking-inventory.md》）；sequential-thinking 字段（任务给定）；R1 技术报告核心事实（Aha moment、长思考）。
- **B 级（高置信模型知识，未在线核实）**：Anthropic/OpenAI 参数机制主体；论文方法与效果数值（口径可能略有出入）；Claude Code 四层机制；路由/测试时计算结论。
- **C 级（需核实）**：Claude 关键词具体 token 档位；R1-0528 具体数字；Cursor/Windsurf/Devin 版本细节；GPT-5 档位细节；sequential-thinking 完整字段集。
- **使用建议**：深化设计引用本文时，P0 项（验证轮、复杂度路由、轮次预算）机制结论均为 B 级以上且与 A 级现状证据闭环；C 级数值仅作背景，不进入验收标准。
- **与姊妹文档衔接**：《contextloom改造详细设计-思考与长度释放.md》解决"思考长度释放"（参数面）；本文解决"多轮触发"（轮次面）；两者共用 H10-H12/H23 挂钩点。落地顺序建议：先参数面（已有设计）后轮次面（本文 Top 10）。

## 附录 A 参考来源清单

1. Anthropic：Extended thinking 文档、Claude 3.7/4 发布说明、interleaved thinking beta（B/C）。
2. OpenAI：o1/o3/o4-mini 模型与 reasoning 文档、reasoning summaries（B/C）。
3. 论文：Reflexion（2023）、Self-Refine（2023）、ToT（2023）、GoT（2024）、Plan-and-Solve（2023）、ReWOO（2023）、Least-to-Most（2023）、Self-Consistency（2023）、Step-Back（2024）、CoVe（2023）、Snell（2024）、s1（2025）、RouteLLM（2024）、FrugalGPT（2023）、Hybrid LLM（2024）、overthinking（2024/2025）。
4. modelcontextprotocol/servers：sequential-thinking 参考实现（B）。
5. Claude Code 文档与实践：plan mode、TodoWrite、subagents、thinking 关键词（B/C）。
6. DeepSeek：R1 技术报告、API reasoning_content 文档、R1-0528 发布说明（A/B/C）。
7. dsh：`packages/*` 源码与 `docs/subsystems/*`——经《dsh能力释放机制深度分析.md》《业界前沿对标分析文档.md》转引（A）。
8. ContextLoom：`internal/*` 源码——经《research/contextloom-thinking-inventory.md》转引（A）。
9. Cognition：《Don't Build Multi-Agents》（2025）、Devin 2.0 发布（B/C）。
10. Cursor / Windsurf：产品文档（C）。

---

*报告完。本文为"多轮思考触发"横切调研，全部落点均指向现状盘点 H1-H25；深化设计请以本文 §12 Top 10 作为实施优先级输入。*
