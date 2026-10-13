# dsh 能力释放机制深度分析

> **副题**：DeepSeek Harness（dsh）为什么更能"用出"模型的全部能力——机制级、源码级归因
> **关系说明**：本文是《业界前沿对标分析：ContextLoom vs DeepSeek Harness（dsh）》（`contextloom/docs/业界前沿对标分析文档.md`）的深化篇。对标文档回答"两者差在哪"；本文只回答"dsh 的差异为什么能释放模型能力"，逐条给出机制论证与源码证据，不重复对标文档的功能清单与改造清单。
> **对象版本**：dsh v0.1.0-rc.5（开发者预览，TypeScript monorepo + Cordis 插件微内核）；ContextLoom v2.5.0+（Go 单二进制，约 6.4 万行）。
> **证据基准**：dsh 仓库源码 `packages/*` 与官方文档 `docs/*.zh.md`、`docs/subsystems/*.zh.md`。文中"证据"标注到包名/文件/接口名；接口片段来自文档中的源码粘贴（type-equiv 块），可直接回源码核对。
> **行文约定**：每节结尾给出"→ 为什么这释放了模型能力"的因果结论；第 9 节汇总证据表与能力释放公式。

**先读结论（TL;DR）**

1. dsh 释放模型能力的本质，是把五件事从"编译期常量"变成"运行时可组合量"：**行动面**（插件 + Seam）、**记忆**（事件溯源）、**编排表达力**（subagent + workflow + PTC）、**放权安全度**（OS 沙箱 + 审批 + 完整性报告）、**自举速度**（可逆插件 + 模型自定义插件 + 全树 patch）。
2. 五个维度是**乘法关系**（工程隐喻）：任一维度接近零，模型能力都会被 harness 的漏斗限制住——这解释了"功能对比看不出差距、机制对比看到代差"。
3. 本文按 8 个机制逐条给出源码证据；第 9 节给出"能力释放公式 + 证据总表 + 反面检验"；第 10 节对照 ContextLoom 现状（简述，指向对标文档）。

---

## 0. 导言："发挥模型全部能力"的操作化定义

### 0.1 从"功能对比"到"机制归因"

对标文档已完成两件事：列出 dsh 与 ContextLoom 的功能差异、给出 dsh 可借鉴清单。但"dsh 更能发挥模型能力"仍是一个未拆解的主张——功能罗列解释不了它，因为：

- 单看功能，ContextLoom 在一些维度更强（token 节约、防作弊、交付形态）；
- 但模型在 dsh 中的"实际可达状态"确实更大（能扩展、能编排、能放权、能自举）。

所以本文把问题改写为：**harness 的哪些机制，决定了"模型能力被用出来多少"？** 然后逐条归因。

### 0.2 五个可检验维度

"发挥模型全部能力"操作化为五个可检验维度：

| # | 维度 | 操作化定义（可检验） | 能力被浪费时的表现 |
|---|------|----------------------|--------------------|
| 1 | 可扩展行动面 | 模型能触达的动作集合可随需求扩大，且不需要改 harness 核心 | 模型"想到但做不到"；每加一种能力都要改核心代码 |
| 2 | 一致记忆 | 模型每一轮看到的上下文都能从一份持久真相源精确重建；"模型见过的东西"可追溯、可重放 | 压缩/恢复后上下文错乱；历史不可重建；行为缺陷无法复现 |
| 3 | 可组合编排 | 模型能自主组织"多步、多代理、并行、后台"的工作，而非被限定为单线程一问一答 | 复杂任务靠提示词硬磨；无法结构化拆解与调度 |
| 4 | 安全放权 | 存在真实边界，使系统敢于自动批准、无人值守；安全机制与能力机制互不拖累 | 要么过度限制（能力受限），要么过度信任（不敢无人值守） |
| 5 | 自举进化 | 模型/harness 能在运行时增加新能力（定义插件、写编排脚本、注册工具），能力增长不依赖重新发版 | 每次扩展都要开发者介入、重新构建、重启 |

### 0.3 方法论与证据规则

- **只做机制归因**：每个结论必须能指到"哪个包、哪个文件、哪个接口/事件"。
- **证据三级**：① 源码文件与接口名；② 事件/类型定义片段；③ 官方文档中的行为约定（可回源码核对）。
- **反向检验**：每个机制补"缺了它会怎样"（第 9.3 节），避免把"存在"当作"有用"。

---

## 1. 机制一：插件微内核与可逆效果——能力扩展没有上限

### 1.1 事实：没有特权核心

`docs/architecture.zh.md` 开宗明义：

> **"不存在需要打补丁的特权内核：扩展 dsh 的方式是把插件挂载到其他插件旁边，而各项注册都是副作用，会在其插件卸载时撤销。"**

产品每一部分都是插件：模型适配器、工具注册表、会话日志、以及 agent loop 本身。核心包一览（同文档"核心包"表）：

| 包 | 职责 | `ctx` 键 |
|---|---|---|
| `core/session` | 仅追加的 `SessionEvent` 日志与内存存储 | `ctx.sessions` |
| `core/system-prompt` | 提示词片段与工具 schema 的组装 | `ctx.systemPrompt` |
| `core/tools` | 作用域化工具注册表与带把关的执行流水线 | `ctx.tools` |
| `core/agent` | `Agent` 接口、活跃 agent 注册表 | `ctx.agents` |
| `core/agent-loop` | 实现该接口的默认驱动器 | `ctx.agentLoop` |
| `core/scope` | 按 agent 划分作用域的注册原语 | 库，无 ctx 键 |
| `llm/llm` | 消息与流式词汇表、适配器 seam | `ctx.llm` |

### 1.2 Cordis 的五个核心概念（`docs/cordis-primer.zh.md`）

1. **插件 = 实现 Service 的对象**：可以是带 `inject`/`apply(ctx)` 的函数，也可以是 Service 子类；
2. **上下文 = 服务的容器**：服务占据稳定的 `ctx.<key>`（`ctx.tools`、`ctx.llm`、`ctx.sessions`），插件通过 key 查找服务，而非导入具体实现；
3. **`inject` 声明服务依赖**：加载顺序由服务依赖推导，而非手工编排启动序列；
4. **类型化事件通信**：五种分发模式——`emit`（观察）/ `waterfall`（环绕包装）/ `parallel`（并行扇出）/ `serial`（按序）/ `bail`（首个 bail 值停止）；
5. **注册是可逆副作用**：提示词片段、工具 schema、适配器、提供方、监听器通过 `ctx.effect()` 或 `ctx.on()` 安装，reload/teardown 时按 disposer 撤销。

其中 `waterfall` 是"环绕中间件"：监听器接收 `(...args, next)`，调用 `next()` 执行下游；不调用 `next()` 直接返回即短路。**"拥有决策权的策略监听器可以短路，仅观察者必须委托"**——同一套原语同时承载"能力可插拔"与"策略可插入"。

### 1.3 "注册=效果、卸载=回滚"的具体含义

- 每个注册都应有对应 disposer；teardown 顺序有要求时，把相关工作放进同一个 effect；
- 结果是**插件生命周期自治**：装上即生效、卸载即回到之前状态，不留下"半注册"残留；
- 对模型的含义：能力集（工具、模型、策略）的变更属于**运行时可组合**，而不是编译期绑定。

### 1.4 agent-loop 本身可替换

`dsh-agent-loop` 是 `ctx.agentLoop` 的默认实现；扩展插件只依赖 `dsh-agent` 接口，从不依赖 loop 具体包。"loop 可换"是架构承诺而非口号（architecture.zh.md"核心包"表 + 对标文档已核实）。

### 1.5 自指扩展：模型自己定义插件

`packages/extensions` 提供两个服务（源码：`packages/extensions/cordis-host-runner/src/index.ts`）：

- **`ctx.dynamicCordisRunner`**：`define(request)` 把一个**带版本的 Cordis 包**注册为真实插件（host 半 + browser 半），`undefine(agent, pluginId)` 移除插件及其全部不可变包；未授权的 Client 包等待审批（`start`/update 语义见 `docs/subsystems/extensions.zh.md`）；
- **`ctx.cordisInspect`**：模型在写代码前查询获准公开的运行时元数据（`register` / `syncClientManifest` / `list` / `query` / `resolveClientQuery`）。

模型侧入口为 `cordis_define` 工具（源码检索命中：`packages/extensions/cordis-host-runner/src/index.ts`；前端呈现组件 `packages/extensions/ui-cordis/src/client/CordisDefineRow.tsx`）。即：**模型可以通过工具调用为 harness 增加新插件**——这是当前 agent 框架中最激进的自举形态之一。

### 1.6 scope：能力集的作用域隔离

`core/scope` 是"按 agent 划分作用域的注册原语"：注册可绑定到单个 agent 的 `agent.ctx`，实现"一个会话一套能力集"（配合 agent preset + `isolate` realm）。子代理的能力集由**注册作用域**决定，而不是在全局工具表上做黑名单删减。

> **→ 为什么这释放了模型能力**：模型能力的上限 = 模型智能 × harness 可提供的动作/策略集合。传统单体 harness 中，这个集合是编译期常量；dsh 中它是**运行时组合量**——插件并排挂载、按 agent 作用域隔离、可逆卸载，甚至由模型自己定义。于是"给模型加一种能力"从"改核心 + 发版"变成"挂一个插件"，行动面的扩展成本趋近于零，扩展速度上限从"开发排期"变为"模型与用户的需求速度"。

---

## 2. 机制二：Seam 能力抽象三角——能力不绑定实现

### 2.1 三角定义

一个 **seam** 是一项可替换能力，包含三种角色（architecture.zh.md / capability-seams.zh.md）：

- **Service Definition（SD）**：声明接口的包；
- **Service Provider（SP）**：实现接口的包（可多个并存，按名选择或按配置替换）；
- **Consumer**：使用它的包（通常是面向模型的工具）。

关键约定：**"单一角色本身不是 seam；添加一项能力意味着把三者一并设计。"**

### 2.2 可替换面清单（节选，来自 `docs/capability-seams.zh.md` 服务图）

| `ctx` 键（能力） | 已知 Provider | Consumer（示例） |
|---|---|---|
| `ctx.fs` 文件系统 | fs-local / fs-sandbox / fs-ssh | tool-fs |
| `ctx.subprocess` 子进程 | subprocess-local / subprocess-ssh | bash-local 等 |
| `ctx.shell` Bash 执行 | bash-local / bash-sandbox / pwsh-local | tool-bash / tool-pwsh |
| `ctx.sandbox` 进程沙箱 | sandbox-local（bwrap/Landlock/Seatbelt/ACL）/ sandbox-windows-acl / sandbox-ssh | bash-sandbox / pwsh-sandbox |
| `ctx.subagents` 子代理 | spawn-in-process / fork-in-process / acp / codex / claude-code / dsh-sdk | tool-subagent |
| `ctx.compaction` 压缩 | compaction-basic / compaction-tool-result-pruner | command-compact |
| `ctx.spillStore` 溢出存储 | spill-local | spill-policy / session-reference |
| `ctx.jobs` 后台任务 | jobs-local | tool-jobs |
| `ctx.ptcRuntime` PTC 运行时 | ptc-runtime-node / experimental-ptc-runtime-python | run_code（PTC 模式） |
| `ctx.workflowEngine` 工作流引擎 | workflow-ptc | tool-workflow |
| `ctx.lsp` 语义导航 | lsp-stdio | tool-lsp |
| `ctx.llm` 模型适配器 | llm-deepseek / llm-pi-ai / llm-replay | agent-loop |
| `ctx.terminals` 持久终端 | terminal-bash | tool-terminal |

### 2.3 "执行世界"共享效应

architecture.zh.md 的关键论断：

> **"文件系统与进程提供方共享同一个执行世界，因此把它们指向远程沙箱，也就把 Bash、PTY 和 LSP 一并搬了过去，无需提供方专用 fork。"**

即：换 `ctx.fs` + `ctx.subprocess` 两个 provider，模型侧所有执行类工具（bash/终端/LSP/文件读写）**自动跟随**——行动半径的扩大是"换后端"，不是"改工具"。

### 2.4 新行为的归属位置（官方映射，节选）

architecture.zh.md 的"新行为归属位置"表把 20+ 种扩展意图映射到既有扩展点：加模型提供方→`ctx.llm`；加面向模型能力→`ctx.tools`；单会话不同能力集→agent preset + `isolate` realm；加 shell→`ctx.shell`；持久终端→`ctx.terminals`；后台任务→`ctx.jobs`；文件访问/策略→`ctx.fs` 或 `fs/*` 事件；限制进程→`ctx.sandbox`；拦截请求/工具/轮次→`agent/*`、`tools/*` 事件；加模型可见上下文→`agent.inject()`；持久会话状态→扩展 `SessionEventMap`；换会话存储→实现 `SessionPersistence`（`create`/`open`/`stat`/`list`/`export`）；把注册限定到单 agent→`agent.ctx`。

> **→ 为什么这释放了模型能力**：模型能力受"接口背后能接什么"限制。Seam 三角把"能力"与"实现"解耦：行动半径（本地→远程→云端）与行动种类（文件/进程/浏览器/电脑操作）都通过换 provider 扩展，消费者（模型工具）语义不变。模型不需要知道后端换没换，但它的实际可行动范围可以持续扩大——这正是"平滑扩大行动半径"的工程形态。

---

## 3. 机制三：事件溯源会话与"模型可见=已记录"

### 3.1 真相源

`packages/core/session`（`docs/subsystems/session.zh.md`）：**`Session` 是一份由类型化 `SessionEvent` 组成的仅追加日志，是 agent 完整交互历史的唯一真源。LLM 消息历史从日志*派生*而来，从不单独存储；回放即从同一组事件重新派生。**

### 3.2 事件词汇（源码：`packages/core/session/src/types.ts`）

`SessionEventMap`（可声明合并扩展）核心成员（节选）：

- `turn/start` / `turn/end`（含 `TurnEndReason`）；
- `step/start` / `step/end`（一步 = 一次模型请求 + 其请求的工具执行）；
- `user/message`（人类提示词 / `agent.inject()` 注入 / goal 续跑轮，`source` 区分）；
- `developer/message`（轮次/步骤粒度接纳的增量会话变更）；
- `system/message`（渲染后的系统提示词，作为 surface 节点）；
- `assistant/message`（**内嵌产生其组装内容的精确紧凑带时间流** `stream` 与 `usage`；中途取消记 `interrupted: true`）；
- `assistant/attempt`（失败/重试/取消/流错误的尝试，保留流但不伪造模型历史）；
- `tool/call`（`name` + **模型产出的原始参数字符串**，未解析）；
- `tool/result`（模型可见结果 + 错误身份 `error{name,code,reason}` + 工具私有 `meta`，`meta` 必须 JSON 可序列化，回放可复现同一卡片）；
- `request/header`（请求信封 `EpochHeader`：调用配置 + 适配器默认值 + 工具 schema）；
- `request/context`（路由元数据：provider / model / contextWindow / `systemPromptUpdate`）；
- `session/end-seed`（fork 切割标记 `{inherited?: true}`）。

**事件词汇本身是可扩展的**：compaction seam 通过声明合并加入 `compaction/start` / `compaction/summary` / `compaction/end`；hook 桥接加入 `hook/invoked` / `hook/result`。

### 3.3 不变式："模型可见即已记录"（Model-visible means logged）

- **每个模型请求都必须能从日志重建**；新增模型可见输入必须新增会话事件；
- 修改消息内容的插件必须注册"纯消息投影"（`ctx.sessionProjections`，独立读取器显式传入同一处理器）；
- `Session.append` 运行时用 `isJsonValue` 校验所有事件数据——不可序列化的数据在源头被拒绝。

### 3.4 请求信封与路由状态分离

- `request/header`：`foldRequestHeader(events)` 选择最新快照重建请求头；规范形式被强制（禁止 `system` 字段等，违规拒绝而非静默规范化）；
- `request/context`：容量与 `systemPromptUpdate` 描述的是**路由**，不进入 `EpochHeader` 的逐字段比较（`headerEquals`）——一次路由变化不会被误登记为请求信封变化。

意义：**"请求是什么"与"历史是什么"都被记录，且各自可重建**；UI、遥测、压缩、fork 全部从同一流派生。

### 3.5 fork / replay / resume / projections

- fork 会话 = 以 seed 前缀重放；`buildForkSeed` 在切割点追加 `session/end-seed {inherited: true}` 标记；`ctx.agents.create({ sessionId, seed, meta: { parentSession, seedLength } })` 在轮次边界 fork；
- `deriveMessages()` 从日志投影模型历史（源码检索命中：`packages/core/session/src/index.ts`）；`assistant/message` 内嵌的精确流使 transcript、遥测、持久化与 UI 增量同源；
- `ctx.sessionProjections`：已注册单元增量折叠已提交事件，`stateOf()` 读单类型状态、`snapshot()` 取裁剪后的客户端视图；agent loop 注册共享的 `turnBoundary` 状态。

### 3.6 崩溃与一致性

- **严格重放**（对标文档已核实）：拒绝畸形事件、断号 revision、非法生命周期迁移、时间戳回退；
- **compaction 带锁记录**：`start` 无匹配 `end` = 可检测的孤儿锁，而不是"假称压缩完成"（见 5.3）；
- **JSONL provider**：负责物理 framing、压缩、generation 选择与排他发布；已提交 generation 路径绝不重命名/替换/删除；迁移链 `vN → vN+1` 每包只负责一步。

> **→ 为什么这释放了模型能力**：模型能力受"记忆质量"约束。事件溯源把三件事同时变成保证：① **一致性**——模型看到的每轮上下文都能从日志精确重建，压缩、恢复、fork 不产生"漂移的历史"；② **可调试性**——任何行为可逐事件回放，harness 缺陷可复现（这反过来允许系统更激进地自动化）；③ **可实验性**——fork + 重放使"同一历史、不同策略"的对比可行，行为优化不再靠碰运气。没有这条不变式，其他机制（压缩、多代理、审批）都会因"历史不可信"而被迫保守。

---

## 4. 机制四：工具面工程——从"一次一个调用"到"模型写程序"

### 4.1 工具是声明式契约（`ToolDefinition`）

源码：`packages/core/tools/src/index.ts`。每个注册工具包含：

- `ToolSchema`（模型可见字段：name/description/parameters）；
- **必需的规范输出声明** `output`（`schema` + `render` + 可选 `presentationMeta`）；
- `execute(args, exec)`；
- 调度元数据：`timeoutMs`（由 `tools/execute` 包装层强制执行，**永不发给模型**）、`isConcurrencySafe`（纯同步分类器，仅 `true` 允许与兄弟调用并行）；
- `projectContent` / `finalizeContent`（内容投影与最后不变式）；
- UI 投影 `presentCall` / `presentResult`（纯函数，供实时流与回放共用）。

关键约束：**`schemas()` 通过显式允许列表构建模型可见 schema——`output`/`execute`/回调绝不泄漏到模型请求。** 参数与输出共用一套 `ValueSchemaSpec` DSL（`packages/core/tools/src/schema.ts`），`defineTool` 负责校验与类型收窄。

### 4.2 执行流水线（`docs/tool-execution-pipeline.zh.md`）

```text
tool/call（先记日志）
→ tools/pre-execute（waterfall：hooks、permission、sandbox；allow / deny / ask）
→ 已注册单调 guard（deny 或弃权；身份受保护）
→ [ask 时] ctx.approval 一次性询问（缺失或无法回答 = 拒绝）
→ tools/execute（环绕分派：timeout、retry、metrics）
→ 工具体 execute()（可触发 fs/write-intent、fs/edit-intent 与工具自有事件）
→ projectContent（执行侧准备内容）
→ tools/post-execute（accept / block / replace / add context）
→ finalizeContent（最后的内容不变式）
→ tools/result（冻结的权威结果，同步通知）
→ tool/result 事件（唯一的模型可见结果）
```

要点：

- 三个 waterfall（pre-execute / execute / post-execute）都可改写一次调用；`projectContent` 在 post-execute **之前**，`finalizeContent` 在其**之后**；
- 任何一环抛异常 → 注册表外层归一化为 `isError`（工具失败永不击穿循环）；
- 钩子因此**跨工具族**生效，而无需工具与策略服务耦合；
- PTC 模式把保留的 `run_code` 传输与序列化子调用**都送入同一条流水线**；子调用记录 `tool/ptc-dispatch`，拒绝以"有约束力的驳回"呈现。

### 4.3 作用域过滤：`ToolRestriction`

`ToolRestriction { allow?, deny? }` 是"单个作用域对其继承内容的实时过滤器"：作用于部署全局 + 祖先作用域链；多个限制取**交集**；**该作用域自身的注册不受约束**（被委派的子代理保留其回报所依赖的工具）。语义：仅 deny 的过滤器允许未列出的继承工具通过；allow 列表则排除未列出者。

### 4.4 PTC：模型写程序调用工具

源码：`packages/ptc-runtime/ptc-runtime/src/types.ts`。

```ts
interface PtcRunRequest {
  program: string                 // 作为 async 函数体运行；可用 top-level await 与 return
  bindings: PtcBindingNamespace[] // 宿主函数成为程序全局对象；PTC 传入 tools
  cwd?: string
  timeoutMs?: number | null       // 省略=提供方默认；null=无截止
  sandboxPolicy?: SandboxExecutionPolicy
  signal?: AbortSignal
}
```

- `PtcRunResult`：`value`（程序 return 值，必须跨**无损 JSON 边界**）、`logs`、`error`；`sandbox` 独立上报 `{ mode, denied, enforcement }`——**"程序成功"不等于"限制全部被强制执行"**（正交事实独立上报）；
- 绑定命名空间用 null-prototype 构造，`__proto__` 等按普通自有属性处理；绑定名限定语言可移植标识符子集（同一份命名空间列表对 JS/Python 后端都有效）；
- 流水线集成：`mode: 'ptc'` 下**只有带 parent 的调用**才能执行原生工具名——模型直呼（无 parent）在策略流水线之前就被 `UNKNOWN_TOOL` 拒绝。

意义：模型不再受"一次一个 JSON 工具调用"的带宽限制；它可以写循环、条件、并发、组合——**工具调用的编排逻辑从 harness 移交给模型自己**。

### 4.5 spill：大结果不丢，取回方式可声明

源码：`packages/spill/spill/src/types.ts`。`ctx.spillStore.saveText(content)` → `SpillRef { locator, bytes, retrievalHint }`：

- `SpillLocator` 是**不透明**的模型可见句柄（本地后端渲染为路径，远程/数据库后端可以是 URI/键）；
- 消费者用 `retrievalHint` 渲染，**不假设 `read` 一定是取回方式**；
- 存储只管保存（原样持久化 + 精确字节数）；保留策略、替换决策、检索 API 都在消费方；fork 会话继承 seed 日志中已有 locator，fork 后的新 spill 用子会话 id。

### 4.6 jobs：后台任务统一治理

源码：`packages/jobs/jobs/src/types.ts`。`ctx.jobs`：

- `JobId` 为 `<kind>-N` 品牌化 id；`JobKindMap` 可声明合并（默认 `bash`）；状态 `running | stopping | completed | killed | failed`；
- `JobSpec` 声明拥有者会话（`owner`，**访问控制依赖拥有者授权，而非 id 保密**）、输出上限（`outputLimitBytes`）、拉取式输出源；`JobHandle.append/progress` 写输出环；
- 后台 bash、PTY、子代理统一走 `ctx.jobs`；模型侧由 `job_*` 工具读取或停止（architecture.zh.md）。

> **→ 为什么这释放了模型能力**：模型对世界的操作带宽 = 工具面的表达力。三个升级叠加：① 声明式工具契约 + 统一流水线，使"加工具"不再威胁正确性（把关、超时、并发、错误归一化是平台责任）；② PTC 把"调用方式"交给模型——从"点菜"升级为"写程序"，表达力上了一个数量级；③ spill/jobs 消除两个隐性天花板——大结果被截断、长任务无法后台化。模型因此可以在**单轮**内完成此前需要多轮甚至人类介入的工作。

---

## 5. 机制五：上下文工程

### 5.1 提示词是"组装"出来的

源码：`packages/core/system-prompt/src/index.ts`（`ctx.systemPrompt`）：

- **`PromptSection`**：具名、有序（order 升序 + 名称代码单元序）、文本可静态或按组装上下文动态求值、可 `interpolate`、可 `complete`（成为唯一提示词段；多于一个有效 complete 则组装失败）；
- **`PromptContext`**：动态上下文，物化为**持久 user-role 快照**；`required: true` 在可选运行时上下文被抑制时保留；
- **`ToolProviderResult`**：当前组装中模型可见的工具 schema 集 + 限制前名称全集（用于区分"配置拼写错误"与"有意隐藏"）；
- **作用域**：`AssembleContext.scope` 决定哪些提供者/监听器参与——同一 harness 可以为不同 agent 组装不同提示词。

### 5.2 提示词是 surface 节点，不是请求字段

agent loop 用 `renderPrompt` 渲染组装结果，并把文本作为 `system/message` surface 节点提交：

- 首步作为节点 0 追加；渲染文本变化时**原地替换**；
- 当已准备调用声明 `systemPromptUpdate: 'in-history'` 时，非空更新**追加到已缓存历史之后**（缓存前缀不失效）；
- **空渲染会清除所有生效的系统节点**——模型不再看到旧提示词。

### 5.3 compaction：带锁、可审计、可重建

源码：`packages/compaction/compaction/src/types.ts`。

- 三个**仅日志**事件：`compaction/start {turn}`（锁）→ `compaction/summary {...}` → `compaction/end {turn, error?}`；
- `summary` 记录：安全摘要投影、`shadowedRange`（surface 位置跨度）、`shadowedSeqs`（被遮蔽节点序列）、`shadowedTokenCount`、生成调用的 provider/model/usage（`llmStreamCall` 标记）——**摘要请求本身可由日志 + 代码重建**；
- 唯一 surface 变更：一条 `surfaceOp: { op: 'replace', startSeq, endSeq }` 的 `user/message`；
- 触发：`pressure` / `context-overflow`；接口 `compactIfNeeded` / `compactNow` / `compactRegion`（显式范围）；
- 剪枝：`ctx.toolResultPruner` 先剪 tool-result（**无模型调用**），重新测量后再决定是否摘要；
- 配对检查：`toolPairingBalancedBefore/After(session, seq)`——区域边界保持工具调用/结果配对；
- 崩溃：中途崩溃 = "有 start 无 end"的**可检测孤儿锁**；手动失败码 `busy | cancelled | changed | summary | commit | persistence`。

### 5.4 LSP：语义级代码上下文

源码：`packages/lsp/lsp/src/types.ts`（`ctx.lsp` seam）：

- 恰好 4 项**封闭**查询：`goToDefinition | findReferences | goToImplementation | hover`——新增查询是**编译强制**的跨 seam/提供方/工具变更；
- `LspQueryRequest` 全字段必填、无默认化步骤；provider 注册自带扩展名映射（`languageId` 只用于同步瞬态文档，不参与选择）；
- 官方承诺：**"更换提供方不会改变模型请求导航的方式"**。

### 5.5 工作目录与 workspace 语义

`working-directory` 提供用户上下文与执行路径（不改变原始项目标识、沙箱写入根目录）；`ctx.workspaceRegistry` 管理 workspace 实体；`fs/*` 事件承载文件观察策略（`fs-observation-policy`）。

> **→ 为什么这释放了模型能力**：模型输出质量 ≈ 模型能力 × 上下文的信噪比与结构。dsh 的上下文工程有三个层次：① **组装层**——提示词/工具 schema/动态上下文由注册表按作用域组装，可插拔、可测试；② **生命周期层**——提示词作为 surface 节点、压缩作为带锁日志事件，使"上下文怎么变"永远可审计、可重建（配合机制三）；③ **语义层**——LSP 把代码理解从"文本检索"提升到"语义导航"。三者叠加的结果：模型把注意力花在任务上，而不是花在"理解自己被喂了什么"上。

---

## 6. 机制六：多代理与编排

### 6.1 subagent seam：能力协商 + activation

源码与文档：`packages/subagent/subagent`、`docs/subsystems/subagent.zh.md`。

- 所有后端统一走 `startActivation`；`prepareContinuable` 选择本地 Agent 创建，`start` 选择一次外部执行；
- **`SubagentCapabilities { agentOptions, outputSchema, depthLimit, toolFilter, persona }`**——请求的选项先与后端能力校验，不支持则启动前拒绝（fail loud）；
- `SubagentActivationSpec { provider, label, childId, request, signal, delivery: 'parent' | 'caller' }`：`delivery` 决定结果是否回注父代理；
- `toolFilter`：进程内后端在子代理创建窗口内应用作用域化 `tools.restrict()`——被过滤工具**从提示词消失且拒绝执行**（"一种可见性"），未知名字大声报错；
- `persona`：为子代理注册作用域化 `deployment:persona-prefix` 段，**只对子代理遮蔽**部署 persona；
- 本地子代理拥有持久 Session、至多一个在线 activation；支持冷恢复（descriptor 不可用时以 `mode:'unknown'` 保持可发现性，但不授予续跑能力）。

### 6.2 六种 provider（capability-seams 服务图）

`subagent-spawn-in-process` / `subagent-fork-in-process` / `subagent-acp` / `subagent-codex` / `subagent-claude-code` / `subagent-dsh-sdk`——**子代理可以是另一个产品**（Codex / Claude Code 作为后端），按名选择。

### 6.3 workflow：模型编写的编排脚本

源码：`packages/workflow/workflow/src/{types,runtime-types}.ts`。

```ts
interface WorkflowStartRequest {
  script: string          // 纯 JS 脚本体；允许 top-level await，以 return <json-value> 结束
  meta: WorkflowMeta      // 身份块（name/description/whenToUse/phases），引擎用 schema 校验
  args?: unknown          // 普通 JSON，脚本以 args 全局读取
  subagentProvider?: string
  maxTotalAgents?: number // 每次运行的子代理总额度
  parent: Agent           // 必填：脚本启动的每个子代理都归属于它
  signal?: AbortSignal
}
```

- 引擎**绝不对脚本文本求值**来获取 `meta`/`args`；脚本无法观察或替换 `subagentProvider`/`maxTotalAgents`（调用方策略）；
- `WorkflowResult { value, stopReason: 'completed'|'cancelled'|'error', agentsStarted }`——`stopReason` 是封闭联合，非 completed 一律映射为 `isError` 工具结果（**不把部分输出当成功**）；
- `WorkflowRun.result` **永不 reject**；`cancel()` / `dispose()` 语义明确；
- 执行：`dsh-workflow-ptc` 通过共享 Node PTC 进程运行时执行 VM 与辅助函数，按**调用 Session 的文件策略**受沙箱约束。

### 6.4 plan-mode 与 agent preset / teams

- `ctx.planMode`（plan collaboration state）：`/plan` 软引导 + `exit_plan_mode` 工具 + plan-review 呈现（把计划作为"决策"呈现给用户）；
- `ctx.agentPresets`：per-session agent composition（配合 scope/`isolate` realm）；
- `ctx.agentTeams`（实验性）：在可续跑 subagent 之上提供持久 roster、任务状态与直接 inbox 消息协作。

> **→ 为什么这释放了模型能力**：复杂任务的完成度受"编排能力"约束。dsh 把编排拆成三层：**原语层**（subagent 激活/续跑/消息）、**策略层**（provider 选择、toolFilter、persona、额度）、**表达层**（workflow 脚本——模型用代码写编排，而非用自然语言暗示）。加上"子代理可以是另一个产品"，模型可以调度异构能力而不被绑定在单一 runtime 内——把"一个大脑"扩展为"一个可编程团队"。

---

## 7. 机制七：OS 级沙箱与安全放权

### 7.1 三模式与完整性报告

源码：`packages/sandbox/sandbox/src/index.ts`。

```ts
type SandboxMode = 'read-only' | 'workspace-write' | 'danger-full-access'
// 仅管控文件系统效果；网络与进程可见性明确不在词汇内
type SandboxEnforcement = 'full' | 'partial'
// full=后端管控该模式承诺的所有文件效果；partial=旧 Landlock ABI / Windows ACL 缺口
```

- 只有前两种模式可以发给 provider；`danger-full-access` 的消费方直接 spawn 原始 argv；
- `partial` 意味着"**要求绝对保证的消费方必须拒绝或显式暴露差异**"——保证强度是数据，不是形容词。

### 7.2 逐调用策略

`SandboxExecutionPolicy { mode, workspaceRoot, sessionId? }`：**按每次能力调用解析并携带**——两个消费者可在同一时刻使用不同策略（bash 在 read-only、被约束的子代理需要其状态目录可写）；已批准的重试 = 一次**新调用**（更宽策略），不改 provider 状态。`ctx.sandboxPolicy.resolve()` 拥有优先级与 root 回退规则，bash 与 fs 不重复实现。

### 7.3 argv 包装与两类分类器

`ConfinedArgv { argv, enforcement, denialSignatures, runnerFailureRules }`：

- `argv`：runner + profile + 分隔符 + 调用方原始 argv（**包装而不改造消费方**）；
- `denialSignatures`：沙箱正常工作时"受限命令被阻止"的特征；
- `runnerFailureRules`（`allowedExitCodes` / `informationalLines` / `fatalSignatures`）：**runner 在执行命令前就失败**的特征——消费方必须把这类失败当"沙箱基础设施故障"上报，而非普通任务失败（退出码本身永不证明 runner 失败）。

### 7.4 后端矩阵

`dsh-sandbox-local`：Linux bwrap / Landlock、macOS Seatbelt、Windows ACL 受限令牌；`dsh-sandbox-ssh`：通过与 SSH 文件系统及子进程提供方配套的远端后端执行同一策略；消费方 `bash-sandbox` / `pwsh-sandbox`；PTC 运行时同样接受 `sandboxPolicy` 并在结果中报告 `denied`/`enforcement`。

### 7.5 审批与单调 guard

工具流水线中：`tools/pre-execute` 的 `ask` 进入 `ctx.approval` **一次性询问**（缺失或无法回答 = 拒绝；allowed-once 后继续）；"不得重新排序的所有者策略"注册为**单调 guard**（只能 deny 或弃权，不能把 deny 改回 allow）。

### 7.6 防御性模式（节选，`docs/defensive-patterns.zh.md`）

- 启动命令使用清理过的环境变量（移除匹配 `*KEY*` / `*SECRET*` / `*TOKEN*` / `*PASSWORD*` 的项）——**防止凭证经命令输出、env 或 spill 文件泄漏**；
- 临时与 spill 文件放 0700 私有目录、随机文件名、`'wx'`/`0o600` 独占打开（防符号链接竞态）；
- 正交结果独立上报（超时 ≠ 成功）；dispose 必须达到完全停稳（等待子进程退出，而非只发信号）。

> **→ 为什么这释放了模型能力**：能力释放的真实瓶颈往往不是"模型会不会"，而是"系统敢不敢"。沙箱机制做了三件事：① 用内核级隔离把"最坏情况"限制在文件效果上，使自动批准/无人值守成为可接受的默认；② 用完整性报告（full/partial）把"保证的强度"变成显式数据，让消费方**基于事实**决定放权程度，而不是基于乐观假设；③ 把审批、策略、分类器都做成 seam/事件，安全与能力**各自独立演进**。安全边界越清晰，系统敢于给出的行动自由越大——这是"安全反哺能力"的直接机制。

---

## 8. 机制八：全配置树可 patch + profile/bundle 组合

### 8.1 分层组装（`docs/architecture.zh.md`）

运行中的 dsh 是"一棵插件树，由启动时按序叠加的各层组合而成"：

- **profile**：具名组装（`web` / `headless` / `sdk` / `sdk-minimal` / `acp`），列出组合包、树外插件、用户 `cordis.patch.yml`；
- **组合包（bundle）**：Cordis 配置项 + 挂载代码的分发格式。`dsh-base` 是共享第一层（模型适配器、工具、持久化、沙箱与审批策略、设置、凭据、遥测）；`dsh-web-app` / `dsh-headless` / `dsh-sdk-app` / `dsh-acp-app` 各自叠加；`dsh-sdk-minimal` 是刻意保留的例外；
- **叠加顺序**：profile 有序组合包 → 其 `cordis.patch.yml` → home patch → `--patch` overlay。

### 8.2 patch 语义

**"一条 patch 按 id 定位某个条目并替换其整个 config，或插入新条目。"** `preset` patch 在目标声明的 `config.plugins` 内应用，沿用相同层优先级。`dsh.profile` / `dsh.bundle` 字段在各自 `package.json` 中声明。

### 8.3 可观测与热更新

- `dsh --profile web --dump-config`：**打印出的任何条目都可以被用户 patch 替换**——"可调性"是打印得出来、看得见的；
- HMR（`dsh-hmr`）：base 启用仅监视配置的热更新；headless/sdk/acp 默认禁用；profile patch 可覆盖。

> **→ 为什么这释放了模型能力**：模型能力释放依赖"行为面可调"。当提示词、工具、沙箱、模型适配器、审批策略全都在一棵可打印、可 patch 的配置树里时：① 用户/研究者可以把 harness 调整到**适配具体模型缺陷**的形态（不用 fork 代码）；② 实验（换策略、换后端）成本降到"改一行 YAML"；③ 每个调整都可解释（dump-config 显示生效结果）。**行为面的可调深度 = 模型能力被适配的深度。**

---

## 9. 综合归因：dsh 能力释放公式

### 9.1 公式

> **模型能力释放度 ≈ 行动面宽度 × 记忆一致性 × 编排表达力 × 放权安全度 × 自举速度**

- **行动面宽度**：由"插件 + Seam"决定——能触达的动作集合是否可运行时扩大；
- **记忆一致性**：由"事件溯源 + 模型可见不变式"决定——模型看到的历史是否可精确重建；
- **编排表达力**：由"subagent + workflow + PTC"决定——模型能否用代码组织多步/多代理工作；
- **放权安全度**：由"OS 沙箱 + 审批 + 完整性报告"决定——系统敢给模型多大自由度；
- **自举速度**：由"可逆插件 + 模型自定义插件 + 全树 patch"决定——能力增长是否需要发版。

每一项都是**乘法因子**：任一维度接近零，模型能力都会被"漏斗"限制在 harness 允许的范围内——这是为什么"功能对比"看不出差距，而"机制对比"能看到代差。

### 9.2 源码证据总表

| 机制 | 关键包/文件 | 核心接口/事件 | 释放的能力维度 |
|---|---|---|---|
| 插件微内核 | `packages/core/*`、Cordis（vendor）、`packages/extensions/cordis-host-runner/src/index.ts` | `ctx.effect()` / `ctx.on()` / `inject` / `define()` / `undefine()` | 可扩展行动面、自举进化 |
| Seam 三角 | `docs/capability-seams.zh.md` 服务图；`packages/{fs,shell,sandbox,subagent,lsp,compaction,spill,jobs,ptc-runtime,workflow}/*` | `ctx.fs` / `ctx.shell` / `ctx.sandbox` / `ctx.subagents` / `ctx.lsp` / `ctx.spillStore` / `ctx.jobs` / `ctx.ptcRuntime` / `ctx.workflowEngine` | 可扩展行动面 |
| 事件溯源 | `packages/core/session/src/types.ts`、`src/index.ts` | `SessionEventMap` / `deriveMessages()` / `EpochHeader` / `session/end-seed` / `ctx.sessionProjections` | 一致记忆 |
| 工具面工程 | `packages/core/tools/src/{index,schema,presentation}.ts`、`packages/ptc-runtime/ptc-runtime/src/types.ts`、`packages/spill/spill/src/types.ts`、`packages/jobs/jobs/src/types.ts` | `ToolDefinition` / `tools/pre-execute\|execute\|post-execute` / `PtcRunRequest` / `SpillRef` / `JobSpec` | 可扩展行动面、可组合编排 |
| 上下文工程 | `packages/core/system-prompt/src/index.ts`、`packages/compaction/compaction/src/types.ts`、`packages/lsp/lsp/src/types.ts` | `PromptSection` / `compaction/*` 事件 / `LspOperation` | 一致记忆 |
| 多代理编排 | `packages/subagent/subagent`、`packages/workflow/workflow/src/{types,runtime-types}.ts` | `SubagentCapabilities` / `startActivation` / `WorkflowStartRequest` / `WorkflowResult` | 可组合编排 |
| OS 沙箱 | `packages/sandbox/sandbox/src/index.ts`、`packages/sandbox/sandbox-local`、`packages/ssh/sandbox-ssh` | `SandboxMode` / `SandboxEnforcement` / `ConfinedArgv` / `SandboxExecutionPolicy` | 安全放权 |
| 配置树 patch | `packages/boot/app-boot`、`docs/config-catalog.zh.md`、`dsh --dump-config` | profile / bundle / `cordis.patch.yml` / `--patch` | 自举进化、可扩展行动面 |

### 9.3 反面检验：缺一个机制会怎样

| 缺失机制 | 直接后果 | 模型能力的表现 |
|---|---|---|
| 无可逆插件 | 扩展 = 改核心 | 能力增长 = 发版速度 |
| 无 Seam | 换后端 = 改工具 | 行动半径被当前实现锁死 |
| 无事件溯源 | 历史不可重建 | 压缩/恢复 = 信息损失，行为不可复现 |
| 无工具流水线/PTC | 一次一个调用 | 复杂操作被拆成大量轮次，注意力被消耗 |
| 无上下文组装 | 提示词是字符串常量 | 无法为不同模型/会话适配 |
| 无多代理/编排 | 单线程智能 | 复杂任务靠"提示词硬磨" |
| 无沙箱/审批 | 要么禁，要么赌 | 无人值守不可行 |
| 无配置 patch | 行为面编译期固定 | 无法适配具体模型的缺陷 |

### 9.4 边界与代价（诚实声明）

- **复杂度成本**：~50 包 + 事件图 + 生成目录，理解成本高于单体（对标文档已述）；
- **开发者预览**：v0.1.0-rc.5，官方明示破坏性变更；
- **机制强 ≠ 闭环全**：dsh 无内置质量评估闭环、无 token 节约设计、compaction 摘要后原文不可找回（对标文档 3.1 已列）；
- **公式的局限**："乘法"是工程隐喻，不是可测量函数；它表达的是"短板效应"，用于判断改造优先级。

---

## 10. 对照：ContextLoom 当前机制限制点（指向对标文档）

### 10.1 差距的机制归类

| dsh 机制 | ContextLoom 现状（对标文档核实） | 差距性质 |
|---|---|---|
| 可逆插件微内核 | manifest 脚本钩子（无服务/事件/可逆卸载/scope） | 机制缺失（P0） |
| Seam 三角 | fs/shell/subagent/sandbox 硬编码 | 机制缺失（P0） |
| 事件溯源会话 | `.json` 快照为真相源 + `.jsonl` 追加日志 | 机制缺失（P0，最高价值） |
| 工具流水线 + PTC | 有工具注册与循环检测，无统一 pre/post 策略面与 PTC | 机制缺失（P1） |
| 上下文组装 | ctxmgr 五级渐进 + 检索（token 节约是强项） | 部分对齐，可借鉴"组装化" |
| 多代理编排 | spawn_task/handoff/messagebus（全部进程内） | 部分对齐（P1 补 provider/workflow/jobs） |
| OS 级沙箱 | ShadowFS + rlimit（防"写错"，不防"读泄漏"） | 机制缺失（P0/P1） |
| 全树 patch | 配置查找链（无 patch 树 / 无 dump-config 等价物） | 机制缺失（P2） |

### 10.2 不应对照借鉴的（保持 ContextLoom 现状）

- **全盘 Cordis 化 / TS 技术栈**：自毁式重构，违背单二进制交付哲学；
- **256 轮 goal 预算**：ContextLoom 的轮次 + token 双预算更合理；
- **用"摘要替换"取代"压缩原文可检索"**：ContextLoom 的压缩原文可找回是稀缺资产。

（详细改造路径见对标文档第 5 节与姊妹任务的 P0/P1/P2 详细设计。）

### 10.3 关键结论摘要

1. **dsh 释放模型能力的本质**：把"能力边界"从编译期常量变成运行时可组合量（插件 + Seam + 配置树）；把"记忆"从近似快照变成可重建事实（事件溯源）；把"操作带宽"从单调用升级为程序化（工具流水线 + PTC + spill + jobs）；把"放权"从赌注变成可度量工程（OS 沙箱 + 审批 + 完整性报告）；并把扩展权部分交给模型自己（`cordis_define` + workflow 脚本）。
2. **对 ContextLoom 的启示**：五个乘法因子中，"一致记忆（事件溯源）"与"行动面（Seam）"是机制级短板，属于"不动则其余借鉴都是打补丁"的主线（与对标文档结论一致）。
3. **一个判断标准**：任何改造方案可以用本文公式自查——它是在扩大行动面、加固记忆、提升编排表达力、改善放权安全，还是加快自举？四者之外的改造，多半是功能，而不是机制。

---

## 附录 A：本文证据来源索引

- **dsh 官方文档**：`docs/architecture.zh.md`、`docs/cordis-primer.zh.md`、`docs/capability-seams.zh.md`、`docs/tool-execution-pipeline.zh.md`、`docs/defensive-patterns.zh.md`、`docs/subsystems/{session,tools,ptc-runtime,subagent,sandbox,system-prompt,compaction,lsp,workflow,spill,jobs,extensions}.zh.md`
- **dsh 源码（文中标注）**：`packages/core/{session,tools,system-prompt,agent,agent-loop,scope}`、`packages/{llm,compaction,subagent,sandbox,lsp,ptc-runtime,extensions,workflow,jobs,spill,session-persistence,fs,shell,boot}/*`
- **姊妹文档**：《业界前沿对标分析：ContextLoom vs DeepSeek Harness（dsh）》（`contextloom/docs/业界前沿对标分析文档.md`）
