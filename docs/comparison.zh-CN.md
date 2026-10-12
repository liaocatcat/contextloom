# ContextLoom 对比说明

> 读者：正在评估 ContextLoom 的开发者——对比"直接调用大模型"与主流 Agent 框架 / 编码智能体。
> 目标：只陈述**结构化、可核查的差异**，不写营销话术。下文每一项 ContextLoom 能力都标注了本仓库中的模块路径；竞品列描述的是公开的、一般的特性，可能随时间变化（具体以各项目自身文档为准）。
>
> 相关：[README.zh-CN](../README.zh-CN.md) · [README](../README.md) · [architecture.md](architecture.md) · [context-engine.md](context-engine.md) · [toolchain.md](toolchain.md) · [plugins.md](plugins.md)

---

## 1. ContextLoom 与"裸跑大模型"的对比

裸跑大模型就是：贴一段提示词，拿回一段文本。请求之间无状态、不记得你的项目、也无法核验自己的说法。下表把常见痛点逐行映射到 ContextLoom 补充的**机制**（并给出源码位置）。

| 痛点（裸跑大模型） | ContextLoom 的机制性回答 | 所在模块 |
|---|---|---|
| **断崖失忆**——对话超出窗口后，早期内容被静默丢弃 | token 预算驱动的**渐进式分层压缩**：前缀截断（>70%）→ 丢弃最旧 + 滚动摘要（>85%）→ 折半兜底（>92%）。滚动摘要（`[历史摘要]`，≤1200 字）被重新注入，要点得以保留 | `internal/ctxmgr`（`TokenBudget`、`CompactToBudgetWithEvict`） |
| 朴素截断下**压缩 = 永久丢失** | **压缩原文 100% 可找回**：所有被截断/丢弃的原文写入检索索引并持久化；模型（或 MCP 客户端）可经 `harness_search` 找回，标记为 `[压缩原文]` | `internal/ctxmgr`（`EvictFunc`）→ `internal/harness`（`index.json`） |
| **检索需外挂向量库**、嵌入服务与基础设施 | **内置混合检索，零依赖**：倒排索引 + 符号索引（+30 加权）+ 轻量语义重排（查询覆盖率 + FNV-1a 哈希嵌入余弦，中英双向同义词表）。纯内存、可持久化、无外部嵌入服务 | `internal/harness`（`harness.go`、`semantic.go`、`embedding.go`、`symbols.go`） |
| **子产出不可验证**——"完成了"不可信 | **监工 + 质量门禁**：证据链核验（必须有物理证据）、**测试透明代理（TEE）**在沙箱内实测对比声称与实际通过/失败数、goal 每轮静态核查。低于阈值打回，绝不外显 | `internal/overseer`（`anti_cheat.go`、`scoring.go`）、`internal/toolchain` gates |
| **会话崩溃丢进度** | **JSONL 多 Agent 检查点**：每 run 一个 `run-<id>.jsonl`，每轮追加完整快照，崩溃后 `LoadLatest` 续跑（对标 LangGraph checkpointer，以零依赖文件落地） | `internal/checkpoint`、`internal/goalmgr` |
| **提示词靠手工调、越干越笨** | **员工经验自动蒸馏**：子任务完成后模型自动提炼"金规/踩坑/风格"沉淀进员工 `ONTOLOGY.md`，下次指派自动注入（幂等哈希去重、单次 ≤1KB） | `internal/orchestrator`、`internal/memory` |
| **模型路由、限流、故障切换各写各的** | **模型网关**：规则路由（`role=`、`task=`、`budget<=`）、多 Key 池化轮换、指数退避重试、熔断自动降级 | `internal/modelgateway` |
| **工具不可回滚、有安全风险** | **工具沙箱**：写操作重定向影子文件系统、网络强制隔离（代理指向死端口）、`commit`/`discard` 两段式、内存/CPU/超时限额 | `internal/sandbox`（`sandbox.go`、`network.go`、`limits_*.go`） |
| **无可观测性，调试靠猜** | **JSONL trace**（按日轮转）+ **OTLP 导出**（手写 LogsData 协议，不引入 OTel SDK）+ **离线评估**（`eval`、`eval-retrieval`，Recall@K / MRR） | `internal/trace`、`internal/eval` |

**一句话总结差异**：裸跑大模型给你*文本*；ContextLoom 用一层工程 Harness 包裹它，让产出**可验证、可续跑、可审计**。

---

## 2. ContextLoom 与通用 Agent 框架的对比

**LangChain**、**LangGraph**、**AutoGPT**、**CrewAI** 都是强大、被广泛采用、生态丰富的框架。ContextLoom 做出的是不同的结构性取舍：**单二进制、零外部服务**。下表只陈述事实差异；某维度对方更强时如实标注。

| 维度 | ContextLoom | LangChain / LangGraph | AutoGPT | CrewAI |
|---|---|---|---|---|
| **主语言** | Go（1.22+） | Python（另有 LangChain.js） | Python | Python |
| **分发形态** | 单一静态二进制；TUI 构建 `CGO_ENABLED=0`，可交叉编译到各主流平台 | 安装进 Python 应用的库 | 应用 / 库（Python 环境） | 库（Python 环境） |
| **上下文压缩** | 一等公民：token 预算阶梯（0.70/0.85/0.92）+ 滚动摘要，`/ctx` 可审计 | 因应用而异，通常由应用自行组合（如摘要链） | 应用自行组合 | 应用自行组合 |
| **压缩原文找回** | 内置——重启后 `harness_search` 仍可召回被压缩的原文 | 非框架原语（需自建） | 非框架原语 | 非框架原语 |
| **检索** | 内置混合（倒排 + 符号 + 哈希嵌入重排）；**无需外部向量库 / 嵌入服务** | 集成丰富；通常搭配向量库（FAISS、Pinecone、pgvector…）与嵌入服务 | 向量库集成 | 经 LangChain / 工具 |
| **质量门禁 / 反作弊** | 监工评分 + 证据链 + 测试 TEE 实测（沙箱） | 无内置门禁，靠自定义回调组合 | 无内置 | 无内置 |
| **崩溃恢复检查点** | 每 run 的 JSONL 检查点（文件式、零依赖） | **LangGraph 有一等 checkpointer**（内存/DB 后端） | 有限 | 有限 |
| **持久记忆形态** | 追加式 JSONL 会话 + `lessons.md`；无全局锁、多实例安全 | 记忆模块 + 外部存储 | 向量记忆 | 记忆模块 |
| **依赖足迹** | 零外部服务；无数据库、无消息队列、无向量库、无 OTel SDK | 依赖树庞大；含向量库 / LLM Provider SDK | 类似，Python 生态 | 依赖 LangChain 栈 |
| **可观测性** | 内置 JSONL trace + OTLP 导出 | LangSmith（托管）/ 各类集成 | 第三方 | 第三方 |
| **生态与语言广度** | 窄：Go、自包含 | **远更广**：上百集成、多语言（经 LangChain.js）、社区庞大 | 插件生态广 | 多 Agent 见长 |

**诚实说明**：若你需要大量第三方集成、Python 数据/ML 生态，或托管式追踪/可观测平台，Python 框架更合适。ContextLoom 押的是相反的方向——**把上下文、质量、记忆机制整体装进一个可拷贝到任意机器、离线运行的二进制**，用生态广度换取零基础设施的自包含性。

---

## 3. ContextLoom 与编码智能体的对比

**Claude Code**、**Cursor**、**OpenHands**、**aider** 都是成熟、好用的工具，各有长处。ContextLoom 的差异主要在**形态、扩展模型、许可证/依赖取向**。下表只陈述事实，不做褒贬。

| 维度 | ContextLoom | Claude Code | Cursor | OpenHands | aider |
|---|---|---|---|---|---|
| **形态** | **双形态**：TUI（纯 Go）+ 原生桌面 GUI（Wails/WebView），共用同一 runtime | 终端 CLI | 桌面 IDE（编辑器） | Web/CLI 应用 | 终端 CLI |
| **本地 / 云端** | 完全本地运行；驱动任意 OpenAI 兼容端点（llama.cpp、ollama、vLLM、云端），模型无关 | 云端（Anthropic 模型） | 云端模型（部分本地） | 本地或云端；Docker 沙箱 | 本地；多家模型 provider |
| **扩展性** | 场景 / 工具链（DAG）/ 员工 / 门禁 / Manifest 插件——声明式 YAML，项目级与用户级合并 | MCP 工具、hooks | 扩展、MCP | 插件、MCP | 脚本 / 配置，以 git 为中心 |
| **沙箱** | 影子文件系统（写重定向）+ 网络强制隔离；`/sandbox commit|discard` | 权限提示 | IDE 级 | Docker 容器沙箱 | 基于 git 的回滚 |
| **多 Agent 与质量门禁** | 子 Agent 风扇出 + 监工评分 + 证据链 + 测试 TEE；goal 循环带检查点 | Subagents | 有限 | 多 Agent | — |
| **可观测性** | 内置 JSONL trace + OTLP 导出 + 离线评估 | provider 侧 | IDE 级 | 日志 | 日志 |
| **MCP** | **双向**：MCP client（消费外部工具）+ MCP server（只读暴露检索/记忆/技能） | MCP client | MCP | MCP client | MCP |
| **许可证** | MIT（开源） | 专有 | 专有 | 开源（MIT） | 开源（Apache-2.0） |
| **实现语言** | Go（单二进制、零外部依赖） | — | — | Python | Python |

**诚实说明**：Claude Code 与 Cursor 是深度集成、带一方模型调优、UX 成熟的商业产品；ContextLoom 不在模型质量或编辑器打磨度上竞争。OpenHands 与 aider 与 ContextLoom 一样持开源、模型无关立场。ContextLoom 的差异化供给是**上下文引擎 + 质量门禁 + TUI/GUI 双形态，打包为一个无依赖的 Go 二进制**。

---

## 4. ContextLoom 独有 / 少见的能力清单

以下机制或为 ContextLoom 独有，或作为**内置、零依赖**特性较为少见。每项均可在源码树中核查。

| 能力 | 为何少见 | 源码 |
|---|---|---|
| **`harness_search`：压缩原文找回** | 多数系统把压缩当丢失；ContextLoom 把"压缩 ≠ 丢失"做成不变式（搜索前自动冲刷，保证刚压缩的块已在索引中） | `internal/ctxmgr` + `internal/harness` |
| **员工经验自动蒸馏** | 由 Agent 自己把经验沉淀进本体档案——不是用户的功课；幂等且容量受限 | `internal/orchestrator`、`internal/memory` |
| **测试透明代理（TEE）** | 声称的测试结果会在沙箱中实测对比；相似度 < 0.8 即判不可信 | `internal/overseer/anti_cheat.go` |
| **零依赖混合检索** | 倒排 + 符号索引 + 手写 FNV 哈希嵌入做的语义重排 + 内置中英同义词表——无向量库、无嵌入服务 | `internal/harness` |
| **JSONL 多 Agent 检查点** | 文件式每 run 快照（对标 LangGraph checkpointer），可续跑，滚动重写限制体积 | `internal/checkpoint` |
| **MCP 双向** | 既消费外部 MCP 工具，**又**以 stdio JSON-RPC 只读暴露自身的检索/记忆/技能 | `internal/mcpclient`、`internal/mcpserver` |
| **手写 OTLP 导出** | 输出 OpenTelemetry LogsData 而不引入 OTel SDK——可被 Langfuse / Jaeger / 任意 Collector 接收 | `internal/trace/exporter.go` |

---

## 5. 何时不该用 ContextLoom

诚实的边界——以下场景 ContextLoom 不合适：

- **你需要海量第三方集成**（几十个向量库、SaaS 连接器、特定语言 ML 工具）。Python 框架生态更适合你。
- **你想要托管云服务**（弹性伸缩、多租户鉴权、Web 仪表盘）。ContextLoom 是本地二进制，不是托管平台。
- **你打算用非 Go 语言扩展核心**。核心是 Go；扩展点是 YAML 插件与 MCP/ACP，而非到其它运行时的进程内绑定。
- **你需要完整 IDE 体验**（富编辑器、语言服务器、调试器界面）。ContextLoom 提供 TUI 与轻量 GUI，不是 IDE。
- **你只需一次性的 LLM 调用**，无持久化、检索、验证需求——此时 Harness 是多余开销。

---

## 6. 延伸阅读

- [README.zh-CN](../README.zh-CN.md) / [README](../README.md) —— 项目总览与完整特性清单
- [docs/architecture.md](architecture.md) —— 分层架构
- [docs/context-engine.md](context-engine.md) —— 压缩与 `harness_search` 机制深读
- [docs/toolchain.md](toolchain.md) —— 员工 / 场景 / 工具链 / 门禁
- [docs/plugins.md](plugins.md) —— 插件模型与发现顺序

---

## 关键结论摘要

1. 对比**裸跑大模型**，ContextLoom 的差异在**机制而非模型**：产出可验证（TEE + 证据链）、会话可续跑（检查点）、压缩不丢失（检索找回）。
2. 对比**通用 Agent 框架**，差异在**打包形态**：一个无依赖 Go 二进制 vs Python 库生态——以生态广度换取零基础设施自包含。
3. 对比**编码智能体**，差异在**形态与扩展性**：一份 runtime 出 TUI + GUI、声明式场景/工具链/员工/门禁、双向 MCP、MIT 许可的 Go 内核。
4. 当需要海量集成、托管服务、非 Go 内核或完整 IDE 时，ContextLoom **不**适合。
