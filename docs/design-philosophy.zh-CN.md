# ContextLoom 设计哲学

> 读者：想理解 ContextLoom **为什么这样设计**（而不只是"哪个文件干什么"）的工程师、集成者与评审者。下文每个数值常量与文件路径都取自当前源码树。
>
> 相关文档：[README.zh-CN.md](../README.zh-CN.md) · [README.md](../README.md) · [docs/architecture.md](architecture.md) · [docs/context-engine.md](context-engine.md) · [docs/plugins.md](plugins.md) · [docs/toolchain.md](toolchain.md)

---

## 〇、一句话说清 ContextLoom 是什么

ContextLoom 是一个用 Go 编写的**编码智能体 Harness（驾驶舱）**：它通过终端（TUI）、原生桌面窗口（GUI）或任何兼容 MCP/ACP 的客户端驱动大模型，同时由上下文引擎保证每次请求都处于恰当的信息密度。

核心赌注很简单：**不要指望模型靠自身"聪明"一直守在任务上。** 把模型的固有局限当作**工程问题**处理，每一条局限都用代码里可核验的机制来回答。

---

## 一、模型的五类固有局限

大模型直接用于长时间、无人值守的编码工作时，会撞上五个问题。ContextLoom 逐条给出机制性回答：

| # | 局限 | 大白话 | ContextLoom 的机制性回答 |
|---|---|---|---|
| 1 | **上下文窗口有限** | 模型一次装不下太多，对话一长前面的内容就被挤出去。 | token 预算驱动的**渐进式分层压缩**；被丢弃的原文**永不丢失**——仍可通过 `harness_search` 找回。 |
| 2 | **注意力稀释** | 无关材料一多，注意力被分散，关键信息抓不住。 | **混合检索**只挑相关内容块；**三区滑动窗口 + `KEY_REMIND`** 把最重要的材料放在模型注意力最强处。 |
| 3 | **token 成本高** | 请求按 token 计费，上下文越长越贵。 | **极简 JSON 提示词**、词汇压缩、`singleflight` 去重、超大输出转存——"省"是默认项，不是可选优化。 |
| 4 | **输出与实际执行不一致** | 模型可能声称"测试通过"，而测试根本没跑。 | **监工 + 反作弊三件套**：测试透明代理（TEE）、证据链核验、静态核查。不达标的产出绝不外显。 |
| 5 | **重复调用 / 无法结束** | 同一个工具、同一组参数，反复调用没完没了。 | **工程护栏**：`detectLoop`（同工具同参数 3 次）、空输出轮检测、token 红线截断、手动中止。 |

本文其余内容，都是把上面五条回答认真做实之后的自然结果。

---

## 二、三条核心设计原则

### 2.1 原则一 —— "省"是设计原则，不是事后优化

降 token 是一等目标，贯穿每一层：

- **极简 JSON 提示词**（`internal/preprocessor`）：用户输入先过语义映射器，60+ 关键词规则（`fix/bug/crash → BUG_FIX`、`test/verify → TEST`……）加词汇表（`authentication → AUTH`、`null_pointer → NPE`），把一句话压成一条紧凑指令。
- **词汇学习**（`propose_vocab`、`.contextloom/vocab.json`）：模型为项目专有名词提议缩写；**提案须经用户确认才生效**（`/vocab accept`）。模型提议、人类审批。
- **`singleflight`**（`internal/tools/task.go`）：并发发起的相同子任务只执行一次、结果共享。
- **超大输出转存**：工具输出超过阈值即落临时文件，消息里只留预览 + 引用（模型可 `read_file` 读全文）。
- **滚动摘要**替代原始历史，检索中对低分块做**溢出压缩**。

### 2.2 原则二 —— "兜底"而非"预防"

ContextLoom **不**试图阻止模型犯错——那会打断无人值守的自动化。它保证错误**可被发现**、**可被审计**：

- 可发现：**监工**给每份产出打分；**TEE** 重跑声称通过的测试；**证据链**要求物理证据。
- 可审计：**压缩审计**（`/ctx`，最近 20 条）、**检查点**（每轮 goal 状态）、**JSONL trace**（按日轮转）、**质量账本**（`quality_ledger.yaml`）。
- 可恢复：被压缩掉的内容都会写入检索索引——**压缩 ≠ 永久丢失**。

这是工程化的信任管理：用一张安全网，换取自动化。

### 2.3 原则三 —— "模型是执行者，系统是组织者"

ContextLoom 借鉴组织管理，而非提示词工程：

- **Harness** 是资料室（检索）。
- **ctxmgr** 是预算科（压缩）。
- **监工** 是质检员（评分）。
- **`spawn_task`** 是任务分派（子智能体）。
- **消息总线** 是对讲机。
- **Goal 模式** 是目标管理循环。
- **员工** 是有编制的角色。

模型提供智能；ContextLoom 提供**可靠性**。

---

## 三、逐机制拆解

每小节统一按同一结构展开：**做什么（大白话）→ 为什么重要（设计要点）→ 关键参数 → 怎么配置。**

### 3.1 极简 JSON 提示词与词汇学习

**做什么。** 不再发几千字的自然语言系统提示词，而是用紧凑 JSON 与模型通信。语义映射器把请求压成一条短指令；词汇表把项目术语替换为缩写。

**设计要点。** 这不是"把提示词写短一点"，而是**换协议**：把"聊天"变成"结构化通信"。配上由模型提议、用户确认的词汇表，压缩会随项目持续适配。

**关键参数。** 60+ 意图关键词规则；内置词汇条目（NPE / AUTH / REFACTOR……）首次启动自动写入；提案仅在确认后写入 `.contextloom/vocab.json`。

**怎么配置。** TUI 里用 `/vocab [list|accept|reject]` 管理提案；`propose_vocab` 是模型提交提案的工具。

---

### 3.2 Harness —— 上下文引擎（`internal/harness`）

**做什么。** Harness 是模型的资料室：只挑最相关的块、打分、并按注意力最强的位置编排进窗口。它同时也是压缩产物的归宿，所以没有任何内容真正丢失。

**设计要点 —— 分级流水线，而非单一索引。** 单一倒排索引要么漏掉同义改写，要么淹没在候选里。ContextLoom 把多级串起来，每一级都在收窄或重排上一级（`internal/harness/harness.go` 的 `Engine.Search`）：

```
查询 + 元数据
   │
   ▼
[1] LRU 缓存查找            key = "sid:<sid>|<query>"（容量 256，TTL 2s）
   │ 未命中
   ▼
[2] HybridIndex.Query       倒排候选，按会话过滤
   │
   ▼
[3] SymbolIndex boost       查询命中符号的块 +30
   │
   ▼
[4] RelevanceScorer         关键词基础分（阈值来自 score_threshold）
   │
   ▼
[5] semanticRerank          对 top-N（30）候选：
   │                        sem = 查询覆盖率 × 100
   │                        cos = FNV 哈希嵌入余弦 × 100
   │                        new = kw + (sem×0.5 + cos×0.5) × 0.5（只升不降）
   ▼
[6] 溢出压缩                > 50 块时，得分 < 60 的块截为前 100 字 + "...(压缩)"
   │
   ▼
[7] 滑动窗口                按分排序 → 锚定区 / 核心区 / 参考区
   │
   ▼
[8] cache.set               缓存最终窗口形态
```

**四路检索信号。**

- **倒排索引** —— 关键词全文检索。
- **符号索引**（`internal/harness/symbols.go`）—— 用正则从 Go/Python/JS/TS/Rust 中提取函数/类型/常量/类名，建立"符号 → 块"映射；查询命中符号时加 `symbolScoreBoost = 30`。这是"repo map"式符号感知的零依赖轻量替代（不引入 tree-sitter）。
- **语义重排**（`internal/harness/semantic.go`）—— 由**查询覆盖率**与**哈希嵌入余弦**按 0.5/0.5 融合：
  - *查询覆盖率* = |查询特征 ∩ 文本特征| / |查询特征|。中文按 2-gram；ASCII 词小写后取 3-gram 前缀；内置**中英编码术语同义词表**（数百条，如 `登录→login/signin`、`超时→timeout`、`缓存→cache`）**双向**扩展，于是"修登录超时"这类中文查询能命中含 `login`/`timeout` 的英文代码块。
  - *哈希嵌入余弦*（`internal/harness/embedding.go`）—— 查询与文本各自经 **FNV-1a hashing trick** 嵌入为 **256 维** L2 归一化向量；余弦相似度对特征分布/长度更鲁棒，与覆盖率互补。
  - 融合公式：`final = keyword + (coverage×100×0.5 + cosine×100×0.5) × 0.5`，仅当能提升关键词分时应用，上限 100——关键词强命中永不被稀释。
  - ⚠️ **没有 ONNX，没有 FAISS，没有外部嵌入模型。** 语义层完全零依赖、纯标准库。
- **时间衰减** —— `Engine.ApplyDecay()` 每轮把每个块的分数乘以 `decay_factor`（默认 **0.9**），未被引用的块逐渐下沉，被刷新的块保持高位。

**三区窗口与 `KEY_REMIND`**（`internal/harness/window.go`）。按分排序后划分为：

- **锚定区** —— `max(1, 总数 × anchor_ratio)` 个最高分块；
- **核心区** —— 中段；
- **参考区** —— `max(1, (1 − anchor_ratio) × 0.2)` 个最低分块。

随后用两次"首尾重复"对抗注意力衰减：

- 得分 **≥ 95** 的块，会在**参考区**开头插入一条 `KEY_REMIND:` 副本（前 100 字）——即"关键公式写两遍"。
- 当结果集超过 **10** 块时，锚定区最高分块（得分 **≥ 85**）会在**末尾**再追加一次。

**会话隔离。** 每个块携带可选的 `SessionID`。查询返回"项目级共享块（无 `SessionID`）+ 当前会话私有块"的并集。压缩原文块按构造即为会话私有，且缓存键内嵌会话（`"sid:<sid>|<query>"`），因此并发会话绝不串扰。归属判定优先级：ctx 绑定会话 → runtime 全局当前会话 → 无。

**持久化与"压缩过就能搜到"不变式。** 索引变更用 `changeSeq` 对 `savedSeq` 记账；后台 goroutine 每 **30 秒**轮询，需要时落盘。`SaveIndex` **原子写**（先写后改名）`.contextloom/harness/index.json`。关键在于：驱逐回调是在**持有历史锁时**触发的，所以它只向 `evictPending` 缓冲**追加**（O(1)）；真正的索引入索引由批量 `flushEvictPending()` 完成——而 `Runtime.HarnessSearch` **在检索前先冲刷**，保证刚被压缩的块一定可搜到。

**关键参数。**

| 参数 | 默认 | 来源 |
|---|---|---|
| `symbolScoreBoost` | 30 | `harness.go` |
| `SemanticRerankTopN` | 30 | `harness.Config` |
| `OverflowCompressBlocks` / `Score` / `Keep` | 50 / 60 / 100 字 | `harness.Config` |
| 嵌入维度 | 256 | `embedding.go` |
| 缓存容量 / TTL | 256 / 2s | `harness.Config` |
| `decay_factor` | 0.9 | `config.example.yaml` |
| `anchor_ratio` | 0.2 | `config.example.yaml` |
| 自动保存间隔 | 30s | `startHarnessAutoSave` |

**怎么配置。** `harness:` 节（`score_threshold`、`decay_factor`、`anchor_ratio`）；引擎级参数经 `Engine.ApplyConfig()` 热生效。手动控制：`/compress`（衰减 + 强制压缩）、`/inject <id>`（把某块提升到锚定区）。

---

### 3.3 ctxmgr —— 渐进式、可审计的压缩（`internal/ctxmgr`）

**做什么。** 让每次请求都不超窗，同时避免断崖式失忆。

**设计要点 —— 一个决策者、三档阈值、五步降级。** 早期按条数截断，要么砍太早、要么溢出窗口（API 400）。修正方案：由唯一的 `TokenBudget`（`internal/ctxmgr/budget.go`）统一决策，Runtime / Agent / Backend 全部接入。

| 字段 | 默认 | 含义 |
|---|---|---|
| `CompactRatio` | **0.70** | 用量超过此值 → 前缀截断旧消息 |
| `HardRatio` | **0.85** | 用量超过此值 → 丢弃最旧 + 滚动摘要 |
| `SafetyRatio` | **0.92** | 发送前兜底红线（自动压缩 / 强制收尾） |
| `KeepRecent` | **8** | 最近 N 条消息永不被**丢弃** |

`Decide(current, msgCount)` 返回 `ActionNone` / `ActionCompact` / `ActionHardCompact`。`ApplyConfig()` 自动保证单调（`compact < hard < safety`），把矛盾输入修复到剩余区间的中点。

**五步降级阶梯**（`CompactToBudgetWithEvict`）。`role=system` 与 `[历史摘要]` 消息在每一步都**受保护**：

1. **前缀截断** —— 旧消息截为 `len/3` 字，钳制在 **[60, 400]**。
2. **丢弃最旧** —— 仍超 `HardRatio` 且 `len > KeepRecent` 时，从最旧的非保护消息开始丢弃；其头部 **80 字**喂给滚动摘要。每条原始消息只记录一次驱逐。
3. **二次截断** —— 若仍超 `SafetyRatio`，除最新一条外全部再截到 **300 字**。
4. **折半兜底** —— 反复折半最大的可压缩字段（正文、工具调用的 `arguments`——经 `truncateArgumentsJSON` 保持 JSON 合法，或 `ReasoningContent`）。下限：非 system **100 字**，system **200 字**（仅最后手段）。
5. **孤儿清理** —— `StripOrphanToolMessages()` 移除其 assistant 调用已被丢弃的 `tool` 消息，并剥掉其 tool 响应已被丢弃的悬空 `tool_calls`——两者都会触发 OpenAI 兼容 API 400。

**每次压缩都留下三条痕迹。**

- **滚动摘要** —— 每条被丢弃消息贡献其头部 80 字（带角色标签），以 `；` 连接，上限 **1200 字**，包装成 `[历史摘要]` 消息。它按会话隔离（新会话清空、加载时恢复、热重载配置时保留）。
- **审计日志** —— 每个动作追加一行（保留最近 **20** 条）；`/ctx` 展示用量、阈值、keep-recent、摘要预览（前 200 字）与审计行。
- **检索索引块** —— `EvictFunc` 把每条原文包成 `ContextBlock`：`ID = compressed:<unixnano>:<seq>`、`Content = [压缩原文 <role>] ` + 完整原文、`Score = 50`、`SessionID = 归属会话`。可通过 `harness_search` 检索。

**多模态与思考消息的正确性。**

- **图片**：每个图片片段固定 **85 + 384 = 469 token**（对齐 DeepSeek 官方 ~800×800 缩放规则：每张图 ≤ 384 token，与 base64 长度无关）。若把 base64 当文本计数，一张 5MB 图会被估算成 ~167 万 token，瞬间"打爆"预算。压缩时图片降级为占位文本（`ImageOmittedPlaceholder`）；快照会剥离 data URI（`StripImageDataURI`），使 `session.json` 不会膨胀。
- **思考链**（`ReasoningContent`）：由 `MessageTokens()` 完整计入，ctx 指示器才反映真实占用。`keepRecent` 只保护消息**不被丢弃**，从不保护**不被截断**——最近 8 条里的单条巨型消息仍会被截断。

**token 估算**（`MessageTokens`）覆盖**完整**结构：每条消息 8 token 外壳、每个工具调用 6 token、工具调用 `arguments` JSON、`ReasoningContent`、`Name` / `ToolCallID`、图片。这里的低估正是"ctx% 不动 / API 400"一类缺陷的根因。

**怎么配置。** `context:` 节：

```yaml
context:
  compact_ratio: 0.70
  hard_ratio: 0.85
  safety_ratio: 0.92
  keep_recent: 8
```

`MaxTokens` 来自当前模型预设的 `max_context`。修改 `context.*` 或 `model.max_context` 会热重载预算，同时保留滚动摘要。

---

### 3.4 监工与反作弊三件套（`internal/overseer`）

**做什么。** `task` 模式下系统派生一个独立的**监工**智能体，持有原始目标，给每份子智能体产出打分。

**设计要点 —— 核验，而非信任。**

- **四维加权评分**（`scoring.go`）：功能完整性 **40%** + 测试覆盖 **30%** + 代码质量 **20%** + 无副作用 **10%**；通过阈值默认 **90/100**。非代码任务（删文件、改配置）不要求测试——否则会被误判不及格。
- **反作弊三件套**：
  - **测试透明代理（TEE）**（`anti_cheat.go`）：当运行声称"测试通过"时，沙箱内重跑并对比声称与实际的通过/失败数。相似度 **< 0.8** → 判不可信。
  - **证据链核验**：产出必须引用物理证据（文件路径、测试运行结果）；口头声明无效。
  - **静态核查**：目标循环每轮结束强制静态核查 + TEE；不过不外显。
- **节点树**：每个子任务成为一个节点（`pending → running → passed/failed`），记录尝试次数。
- **作弊即清检查点**（`overseer.go`）：检测到作弊时，清除该智能体的历史检查点，防止过程污染。
- **质量三档**（`gate.yaml: level`）：`standard`（全开）/ `fast`（仅静检）/ `none`（关闭）。
- **质量账本**（`quality_ledger.yaml`）：每次 Fail 裁决写入缺陷签名——持久怀疑记录，重启不丢。

**怎么配置。** `overseer:` 节（`pass_threshold`、`node_auto_plan`、`test_tee`、`temp_doc_policy`）；TUI 里 `/overseer_threshold <n>`。

---

### 3.5 多 Agent 协作

**做什么。** 主智能体可调用 `spawn_task` 把任务拆给子智能体；用户可用 `/split` 强制拆分。

**设计要点 —— 拆不拆由模型决定，规则由系统提供。** ContextLoom 只提供接口与规范，不写死流水线：

| 机制 | 大白话 | 实现 |
|---|---|---|
| **入口契约** | 每个子智能体收到一份"入职手册"：工作目录 + 项目文件树 + Harness 结果 + 父对话背景。 | `ContextManifest` / `buildMessagesWithManifest` |
| **`singleflight`** | 并发的相同请求只执行一次，结果共享。 | `internal/tools/task.go` |
| **防递归** | 子智能体的工具表移除 `spawn_task`。 | `CloneWithout("spawn_task", …)` |
| **消息总线** | 智能体经 `send_message(to: main/agentID/broadcast)` 通信；每轮循环清空收件箱。 | `internal/executor/messagebus.go` |
| **活动式超时** | 子智能体只要持续产出就不设限；仅连续 10 分钟无输出才判定死卡终止。 | `spawn_task` 活动监控 |
| **多批次** | 一批结束后，主智能体可按需再拆新一批。 | `batchNo / batchNudge` |
| **工具执行策略** | 只读工具并行执行；写工具串行执行。 | `executeToolsConcurrent` |

**设计要点。** 子任务**必须产出可验证成果**（文件/测试/报告）；无产出的查询、分析、评估由主智能体自己执行，不得派发。

---

### 3.6 Goal 模式 —— 自进化闭环（`internal/goalmgr`）

**做什么。** `task` 模式执行一次即结束；`goal` 模式跨轮循环直至目标达成，无需用户干预。自 v2.4 起升级为**自我完善闭环**——不是"跑圈"，而是"爬坡"。

**设计要点 —— 六机制**（状态机见 `goalmgr.go`）：

```
你提出目标
  → [PLAN 轮]    模型输出步骤清单 + 验收标准（[GOAL_PLAN]）
  → [EXECUTE 轮] 模型执行 → 监工评分（先剥离协议标记防自报刷分）
  → 未通过？自动生成【缺陷清单】→ 下一轮逐条修，修完一条划掉一条
  → 连续 2 轮无实质进展？强制【换策略】
  → 步骤全 done + 验收标准全过？→ 追加一轮【打磨轮】（质量加固）
  → 打磨结束 → [GOAL_POLISH_DONE] 正式收尾
  → 每轮教训写入【经验档案】
```

1. **每轮必反思** —— `[GOAL_REFLECT]`（做了什么 / 发现问题 / 下一步），下一轮开局先读。
2. **返工有清单** —— 监工产出具体缺陷清单（非笼统分数），下一轮逐条修复并回报 `[GOAL_DEFECTS]`。
3. **完成有验收标准（DoD）** —— 目标启动即生成硬指标清单，"步骤全 done + 验收标准全过"才完成。
4. **无进展换策略** —— 同一缺陷返工 2 次无变化，强制换策略（拆分 / 检索 / 请求澄清）。
5. **打磨轮** —— 达标后追加一轮质量加固（边界 / 测试 / 死代码 / 文档 / 副作用）。
6. **经验沉淀** —— `[GOAL_LESSON]` 跨轮复用。

**配套设计。** 按会话隔离（`SessionID`），多会话各持独立目标；**防自报刷分**（评分前 `stripGoalProtocol` 剥离全部协议标记，反思里写"补了测试"不算数）；**检查点**每轮写入（`.contextloom/goals/`），崩溃后 `/goal resume <id>` 续跑；**双预算**（轮次上限 + token 预算，默认 `MaxContext × 2`）；**步骤证据链核验**（done 步骤的 proofs 未通过则降级 `pending` 并追加缺陷）。

**怎么配置。** `/goal [status|abort|list|resume <id>]`、`/mode goal`。

---

### 3.7 记忆 —— 纯文件、零锁（`internal/memory`）

**做什么。** 一个会话 = **追加式 JSONL 事件日志**（`sessions/<uuid>.jsonl`）+ 一个小 JSON 元数据文件；压缩快照存在元数据里。加载会话时回放事件（或恢复快照 + 增量）。

**设计要点 —— 为什么不用数据库。** 早期设计用带**文件锁**的内嵌键值存储，导致同一项目目录无法同时启动两个实例（第二个超时）。改为纯文件扫描后，锁没了：**同项目多实例安全**。会话标题自动取首条用户消息（前 50 字）；列表按最近活跃倒序、带预览；超过上限淘汰最旧会话。

**关键参数。** `memory.directory`（`.contextloom/memory`）、`auto_title`、`max_sessions`（1000）。长期教训存于 `lessons.md`（`/teach` 手动、`/dream` 自动整理）。

---

### 3.8 员工体系与经验自动蒸馏（`internal/orchestrator`）

**做什么。** 员工是一份**持久角色档案**（`.contextloom/employees/<name>/employee.yaml`）：人设、专精、工作流、约束、经验、产出格式、模型绑定、工具白/黑名单、审批预设、技能。内置六员工：`coder / tester / reviewer / product / writer / translator`。

**设计要点 —— 从"临时工"到"编制员工"。** 模型在对话里自封的角色下轮就忘；员工被写进档案：身份、模型、工具权限都稳定。`spawn_task(directive=<员工名>)` 即按档案装配子智能体。

**深入本体与蒸馏。** 档案不止几句提示词，而是承载一套**可蒸馏的能力库**：`expertise`、`workflow`、`constraints`、`experience`、`output_format`，外加 `ontology` 文本 / `ontology_file`（`workspace/ONTOLOGY.md`）与 `workspace` 语料目录。`DeepProfile()` 把它们编译成一段可注入的能力声明。

**经验自动蒸馏**（本项目特色，`internal/runtime/distill.go`）。子任务完成并命中员工时，模型把产出提炼成经验条目（金规 / 踩坑 / 风格），去重追加进该员工的 `workspace/ONTOLOGY.md` 的 `## 经验蒸馏（自动沉淀）` 段落。下次指派自动注入——**员工越干越熟，蒸馏是 Agent 的职责，不是用户的功课**。非阻塞（后台 goroutine）、幂等（SHA-256 内容哈希去重）、容量受限（单次增长 **≤ 1KB**、≤ 4 条、单条 ≤ 300 字节）、失败静默降级。

**自选模型与多来源搭配组合。** `model.preset` 直接绑定模型预设；`model.fallbacks` 声明跨来源降级链（本地 + 远程混搭）。优先级：`spawn_task model` 参数 > 员工 `model.preset`+`fallbacks` > 网关规则 > 默认模型。

**怎么配置。** `.contextloom/employees/<name>/employee.yaml`；TUI 里 `/emp`。

---

### 3.9 插件 —— 场景 / 工具链 / 门禁 / Manifest 插件（`internal/plugin`、`internal/scenes`、`internal/toolchain`）

**做什么。** 一切皆声明式 YAML 插件，从两级目录发现并合并：用户级（`~/.contextloom/plugins/`）与项目级（`.contextloom/plugins/`）；同名项目级优先。

- **场景**（`internal/scenes`）—— 关键词命中即自动接管对话（如"帮我写周报"→ weekly 场景），捆绑团队编排 / 模型 / 工具链 / 门禁；未命中则与裸 chat 完全一致。
- **工具链**（`internal/toolchain`）—— 声明式 **DAG** 流水线，三类节点：`tool` / `llm` / `ref`，加 `depends_on` 依赖编排、`$ref` 产物协议、`{{param.*}}` 参数注入、整步重试。静态校验覆盖环检测、缺失依赖、未知步骤类型、id 重复、未知工具名。内置：`bug-fix-chain`、`code-review-chain`、`code-test-review-chain`、`pr-review-chain`、`weekly-report-chain`。
- **门禁** —— `.contextloom/gates/` 目录存在即启用；定义通过阈值与证据要求。
- **Manifest 插件** —— `stages`（pre/during/post）+ `hooks`（message/route）+ 自定义脚本工具（含超时与沙箱）。示例见 [`examples/plugins/`](../examples/plugins/)。

**设计要点。** 模型可像调一个工具一样按名字调用整条链（`run_toolchain`）；用户得到的是整套标准流程的产物。

---

### 3.10 工具沙箱（`internal/sandbox`、`internal/tools`）

**做什么。** 为高风险操作提供隔离。

**设计要点 —— 容器语义的零依赖落地。**

- **影子文件系统**：所有写操作重定向到 `.contextloom/sandbox/`（读镜像优先、列表合并）；`/sandbox commit` 两段式合并写回，`/sandbox discard` 全部丢弃。
- **网络强制隔离**（`internal/sandbox/network.go`）：沙箱会话内 `HTTP(S)_PROXY` / `ALL_PROXY` 强制指向**死端口** `http://127.0.0.1:9`（连接立即失败），删除 `NO_PROXY` 防绕过，并置 `CONTEXTLOOM_SANDBOX_NET=deny`——零依赖的 `--network=none`。
- **资源限制**：单次运行内存限额、CPU 限制、exec 超时。
- **审批预设**：`standard`（敏感操作确认）/ `permissive`（全自动）/ `strict`（全部确认）；task/goal 自动化模式自动批准。
- **文件操作审计**（`internal/audit`）：JSONL 按日轮转。

---

### 3.11 模型网关（`internal/model`、`internal/modelgateway`）

**做什么。** 在多个模型 provider 之间路由请求。

**设计要点 —— 从"一口锅"到"电网"。** 单模型就是一口锅：坏了全停，而且所有任务一个价。

- **规则路由**：`role=reviewer → strong-model`、`task=vision → vision-model`、`budget<=low → cheap-model`；逗号分隔谓词取交集。
- **多 Key 池化**（`pool.go`）：Key 轮换、每池并发上限、指数退避重试。
- **熔断降级**：连续失败触发熔断自动切池；切换对用户可见。
- **Provider 后端**：任意 OpenAI 兼容端点（llama.cpp、ollama、vLLM、本地、云端）；可选 `deepseek` 通道额外发送 `reasoning_effort`。
- **模型预设热切换**（`/model <name>`）、图片输入时自动切换 `vision_model`。

**怎么配置。** `gateway:` 节（规则 + 池）；不配置 = 行为与单模型完全一致。

---

### 3.12 ACP + MCP —— 双向开口

**做什么。** 把系统对外打开。

- **MCP Client**（`internal/mcpclient`）：消费外部 MCP server；其工具动态注册为 `mcp.<server>.<tool>`。
- **MCP Server**（`internal/mcpserver`）：以 stdio JSON-RPC **只读**暴露 Harness 检索 / 记忆 / 技能，供 Claude Code、Cursor 等驱动 ContextLoom。
- **ACP 服务**（`internal/acp`）：`--serve` headless 模式（GUI 启动器即拉起它）。

**设计要点。** "Server 优先、Client 后置"——先把核心资产以标准协议开放，再接入外部生态。手写 JSON-RPC 2.0，不依赖 MCP SDK。

---

### 3.13 TUI 驾驶舱与 GUI 桌面端

**做什么。** 同一 runtime 上的两个外壳。

- **TUI**（`internal/tui`，Bubbletea，三栏）：思考块默认折叠（Space 展开；思维链不写回历史）、实时的子任务面板、状态栏显示 `ctx 42% (12k/131k)`。`Ctrl+J` 是终端协议下唯一可区分的新行键——这也是 README 专门说明它的原因。
- **GUI**（`cmd/contextloom-gui`，Wails/WebView）：项目管理器、多终端（xterm.js + PTY）、Markdown 双栏编辑器（含 PlantUML 渲染）、schema 驱动配置面板、可拖拽布局、可观测性（折叠思考块、工具调用卡片、ctx 用量、Agent 活动、goal 缺陷清单）。它从自身所在目录拉起 `contextloom` 后端，共用同一份 `.contextloom` 数据。

**设计要点。** GUI 与 TUI **零功能分裂**——GUI 是同一 runtime 之上的 WS 薄封装。

---

### 3.14 识图模式与三层能力门（`internal/vision`）

**做什么。** 在对话里写图片路径（如 `./设计图.png`），ContextLoom 识别后缀、读取文件、base64 编码，发送 OpenAI 兼容的 `image_url` 多模态消息——**不依赖文件上传**。

**设计要点 —— 三层能力门**，避免对纯文本 API 报 400：

1. **配置层**：`model.supports_vision`（默认 false——DeepSeek 官方 API 当前为纯文本）。
2. **运行时层**：不支持时历史保持纯文本，Agent 清空图片并输出降级提示（含原因与配置指引）。
3. **模型层兜底**：发送前剥离全部 `image_url`；400 重试路径同样剥离——**任何路径漏网的多模态消息都不会真正发出**。

**关键细节。** `vision_model` 在图片轮次自动切换视觉模型，结束后恢复主模型。JSONL 事件日志只存 `[图片: <路径>]` 标记（绝不存 base64），恢复时按路径重建。

---

### 3.15 工具谨慎性 —— 预警而非禁止（`internal/tools`）

**做什么。** `CautiousAnalyzer` 在敏感操作前预警，而非直接禁止。

**设计要点 —— 安全决策权保留在用户手中。** 40+ 条敏感路径规则、50+ 条危险命令规则，按严重程度分级：仅通知 / 标准确认 / 强确认 / **直接拒绝**。命令匹配做词边界处理（以空格结尾的模式如 `"sudo "` 不会把 `sudoku` 误判为 sudo）。三级审批预设：`standard` / `permissive` / `strict`。"总是允许"记忆让同一路径的重复操作不再弹窗。

---

### 3.16 工程护栏 —— 故障驱动（`internal/executor`、`internal/runtime`）

每条护栏都因真实问题而存在：

| 护栏 | 针对的问题 |
|---|---|
| **循环检测**（`detectLoop`） | 同工具同参数连续 3 次 → 判为循环，提示模型换方法。 |
| **迭代软提示** | 轮次达阈值仅温和提示"整理进展"（不强制收尾）；终止由 token 红线 + 用户中断约束。 |
| **孤儿 tool 消息清理** | 压缩后残留"有 tool 响应但调用被丢弃"的消息会触发 API 400——按 pending 集合清理。 |
| **消息自修复** | 空 assistant 消息删除；缺失 tool 响应补占位。 |
| **400 自修复** | API 返回 400 时，清理非法消息 + 压缩长度后重试一次。 |
| **重试退避 + 抖动** | 网络错误按指数退避 `1s × 2ⁿ` + 0~1s 随机抖动重试。 |
| **`normalizeSystemFirst`** | llama.cpp 等后端要求 system 消息在列表开头；发送前全部归一化到最前。 |
| **超大结果转存** | 工具输出 > 128KB 转存临时文件，消息仅保留预览 + 引用。 |
| **中文 bigram 分词** | 中文单字匹配过于发散；索引采用单字 + 双字。 |
| **effort 档位重映射** | `low/medium/high → high`；`xhigh/max → max`（避免无效档位）。 |
| **配置查找链** | `--config` > `CONTEXTLOOM_CONFIG` > 二进制真实本体目录（解析符号链接）> exe 目录 > CWD > `~/.contextloom/config.yaml` > 内置默认。 |

---

## 四、设计特点速查表

| 特点 | 一句话大白话 | 设计要点 |
|---|---|---|
| 极简 JSON 提示词 | 与 AI 用"电报码"说话 | 省 70–90% token；语义映射 + 词汇表 |
| Harness 上下文引擎 | AI 的图书馆管理员 | 倒排 + 符号 + 语义 + 三区窗口 + `KEY_REMIND` |
| ctxmgr 预算管理 | AI 的记忆银行 | 0.70/0.85/0.92、keep 8、五步降级、滚动摘要、原文可检索、可审计 |
| Overseer 监工 | 质检员 | 四维评分（40/30/20/10）+ TEE 反作弊 + 复盘节点树 |
| 多 Agent 协作 | 任务拆分 + 对讲机 | 模型自主编排 + 消息总线 + `singleflight` + 防递归 + 活动式超时 |
| Goal 模式 | 跨轮循环直至完成 | 状态机 + continuation + 检查点 + 双预算 + 会话隔离 + 自进化 |
| 词汇学习 | 缩写由模型提议 | 模型提议 + 用户审批 |
| 纯文件记忆 | 不用数据库的存储 | 零锁零依赖、多实例并行 |
| 插件沙箱 | 带围栏的游乐场 | 双级目录 + 生命周期 + 子进程隔离 |
| ACP + MCP | 对内全双工 / 对外双向开放 | 标准协议，手写 JSON-RPC |
| TUI 驾驶舱 | 可视化的 AI 工作状态 | 思考块折叠 + 子任务面板 + ctx 用量 |
| GUI 桌面端 | 原生窗口 + 浏览器内核 | Wails 壳 + 项目管理器 + xterm.js + 双栏编辑器 |
| 识图模式 | 路径识别，按需多模态 | 后缀检测 + `image_url` + 三层能力门 |
| 谨慎性预警 | 预警而非禁止 | 三级审批预设 + 敏感规则库 |
| 工程护栏 | 故障驱动的安全网 | 循环检测 / 消息自修复 / 400 自修复 / 重试退避 / 配置查找链 |

---

## 五、相关文档

- [README.zh-CN.md](../README.zh-CN.md) — 项目总览（中文）
- [README.md](../README.md) — project overview (English)
- [docs/architecture.md](architecture.md) — 整体系统架构
- [docs/context-engine.md](context-engine.md) — 压缩与 `harness_search` 机制深读
- [docs/plugins.md](plugins.md) — 插件体系
- [docs/toolchain.md](toolchain.md) — 员工 / 场景 / 工具链 / 门禁
- [config.example.yaml](../config.example.yaml) — 完整配置参考

---

## 关键结论摘要

- **一个决策者、三档阈值、五步降级。** `TokenBudget`（0.70 / 0.85 / 0.92，keep 8）驱动一条渐进式降级阶梯，单调性自动修复；每次压缩都留下三条痕迹（滚动摘要、审计日志、可检索的原文）。
- **检索是分级流水线，零重资产。** 语义层（覆盖率 0.5 + 余弦 0.5、中英双向同义词、256 维 FNV 哈希）把中文查询接上英文代码，不依赖任何外部嵌入服务——没有 ONNX，没有 FAISS。
- **可靠性靠工程，而非假设。** 监工 + 反作弊三件套、会话隔离、以及"压缩过就能搜到"不变式，使无人值守多会话安全；纯文件记忆、无全局锁，使多实例并行安全。
