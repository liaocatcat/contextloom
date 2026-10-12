# 可观测、可评估与可审计

**读者：** 在 CI 或生产环境运行 ContextLoom、需要**看见** agent 做了什么、**度量**一次改动是否有效、**证明**某个产出是真货的开发与运维人员。

**目标：** 把 ContextLoom 的“可观测 + 可评估 + 可审计”能力写成可操作指南——下文每个字段名与命令均逐字取自 `internal/` 与 `cmd/` 源码，不做臆造。

---

## 1. 总览

ContextLoom 把信任体系分成**三层可观测**与**两层评估**。它们有一个共同点：全部零依赖、纯 Go、以普通 JSON/JSONL 落在项目内——进程重启不丢，可与代码一起提交。

```
        ┌──────────────────────────── 可观测（发生了什么） ────────────────────────────┐
        │                                                                              │
        │   JSONL Trace            文件操作审计             质量账本                     │
        │   internal/trace         internal/audit           internal/selfcheck          │
        │   .contextloom/trace/    .contextloom/audit/      quality_ledger.yaml         │
        │   每轮/工具调用/耗时/错误  每次写/执行（按日轮转）   每次 Fail 缺陷签名（持久）    │
        │                                                                              │
        └──────────────────────────────────────┬───────────────────────────────────────┘
                                               │  原始数据喂给
        ┌──────────────────────────────────────▼───────────────────────────────────────┐
        │                             评估（是否变好了）                                  │
        │                                                                              │
        │   智能体级评估                              检索质量评估                         │
        │   contextloom eval                          contextloom eval-retrieval          │
        │   internal/eval (eval.go)                   internal/eval (retrieval.go)        │
        │   任务集通过率                               查询集 Recall@K / MRR                │
        │                                                                              │
        └──────────────────────────────────────────────────────────────────────────────┘
```

| 层 | 包 | 落盘产物 | 回答的问题 |
|---|---|---|---|
| JSONL trace | `internal/trace` | `.contextloom/trace/trace-YYYYMMDD.jsonl` | 每轮迭代/工具调用做了什么、多久、是否失败？ |
| OTLP 导出 | `internal/trace`（`exporter.go`） | 向你的采集端 HTTP POST | 能否把同样的记录送往 Collector / Langfuse / Jaeger？ |
| 文件操作审计 | `internal/audit` | `.contextloom/audit/audit-YYYYMMDD.jsonl` | 哪些文件被写/替换/删除、由哪个工具做的？ |
| 质量账本 | `internal/selfcheck`（`ledger.go`） | `quality_ledger.yaml` | 哪些缺陷跨轮反复出现？ |
| 智能体级评估 | `internal/eval`（`eval.go`） | 你的 `eval.yaml` → JSON 报告 | agent 是否通过我的任务集？ |
| 检索评估 | `internal/eval`（`retrieval.go`） | 你的 `retrieval.yaml` → JSON 报告 | 检索质量（Recall@K / MRR）是否退化？ |

---

## 2. JSONL Trace

### 是什么

由 `internal/trace` 写入的轻量、仅追加 JSONL 日志。每条记录对应一次 agent 迭代、工具调用、goal 轮次或错误。它是**跨轮失败归因**与“改动后能力退化”检测的原始数据源。

### 位置

```
.contextloom/trace/trace-YYYYMMDD.jsonl
```

- **按日轮转** —— 文件名内嵌日期（`trace-20060102.jsonl`，Go 布局 `20060102`）。新的一天开新文件，写入始终追加。
- **保留策略** —— 保留最近 **30** 个轮转文件（`maxKeep = 30`，与 audit 一致）。清理在轮转时触发，长运行项目不会让 trace 目录无限膨胀。
- **关闭即零副作用** —— `trace.enabled: false` 时不创建任何目录/文件，`Recent` 返回 `nil`。

### 记录字段（`trace.Entry`）

| JSON 字段 | 类型 | 说明 |
|---|---|---|
| `ts` | RFC3339 时间戳 | 记录时间；为零时自动填充。 |
| `session` | string | 会话 ID，**仅前 8 位**。 |
| `mode` | string | `chat` / `task` / `goal`。 |
| `round` | int | agent 迭代轮次。 |
| `event` | string | `agent_round` / `tool_call` / `goal_round` / `error` 之一。 |
| `tool` | string | 工具名（用于 `tool_call`）。 |
| `ms` | int64 | 耗时毫秒。 |
| `tokens` | int | token 估算（尽力而为）。 |
| `err` | string | 错误信息。 |
| `detail` | string | 附加说明（如 goal 评估分）。 |

空字段按 `omitempty` 省略。示例行：

```json
{"ts":"2026-01-15T09:30:12.345Z","session":"a1b2c3d4","mode":"goal","round":3,"event":"tool_call","tool":"exec_command","ms":345}
{"ts":"2026-01-15T09:30:13.001Z","session":"a1b2c3d4","mode":"goal","round":3,"event":"error","err":"boom"}
```

### 查看方式

**TUI 内：**

```
/trace        # 最近 N 条（默认）
/trace 100    # 最近 100 条
```

**命令行：**

```bash
contextloom trace --tail 50
contextloom trace --tail 50 --dir /path/to/project   # 不在项目根目录运行时指定项目目录
```

`--dir` 默认取当前工作目录，命令读取其下的 `.contextloom/trace/`。输出按时间倒序，每行一条：

```
最近 3 条 trace（/path/to/project/.contextloom/trace/）:
  2026-01-15 09:30:13 [goal] error 轮#3 错误=boom
  2026-01-15 09:30:12 [goal] tool_call 轮#3 工具=exec_command 345ms
  ...
```

### 配置

```yaml
trace:
  enabled: true
```

### 价值

多轮运行失败时，trace 告诉你**哪一轮、哪个工具、耗时多少、是否报错**——不必上调试器。它也是你改动前后 diff 的数据集：用来判断一次“修复”是否引入了退化。

---

## 3. OTLP 导出

### 是什么

JSONL 记录器之上的可选导出目标：每条 `trace.Entry` 还会**批量**送往任意 **OTLP/HTTP + JSON** 接收端（OpenTelemetry Collector 的 `/v1/logs`、Langfuse、Jaeger 等）。实现（`internal/trace/exporter.go`）是**零依赖、手写**的 OpenTelemetry `LogsData` 映射——只用 `net/http` + 标准库，可交叉编译到所有平台。

### 语义

- **非阻塞** —— `Enqueue` 绝不阻塞 agent。内部 channel（容量 128）满时，记录被**丢弃并计数**，绝不内联重试。
- **批量 + 定时双触发** —— 缓冲达到 `batchSize` 条 **或** `interval` 到期即冲刷，先到先发。
- **失败静默** —— 非 2xx 响应或网络错误只累加 `dropped` 计数，不影响记录器。
- **退出冲刷** —— `Recorder.Close` 冲刷导出器剩余缓冲，退出不丢数据。

### 载荷映射

记录被包装为 OTLP `LogsData` → `resourceLogs` → `scopeLogs` → `logRecords`：

| OTLP 字段 | 取值 |
|---|---|
| 资源属性 `service.name` | `contextloom` |
| scope `name` | `contextloom.trace` |
| `body` | `event` 的值 |
| `severityText` | `INFO` |
| `timeUnixNano` | `ts` 的 int64 字符串（proto3 JSON） |
| attributes | `session`、`mode`、`round`、`tool`、`ms`、`tokens`、`err`、`detail`（仅非空项） |

### 配置

```yaml
trace:
  enabled: true
  # 留空 = 仅本地 JSONL。填你的采集端 /v1/logs 端点即开启导出。
  export_endpoint: "http://127.0.0.1:4318/v1/logs"
  # 附加 HTTP 头（如鉴权）。YAML 映射。
  export_headers:
    Authorization: "Bearer <token>"
  # 批量条数（满即导出）与定时冲刷秒。
  export_batch: 32
  export_interval: 5
```

| 键 | 默认 | 含义 |
|---|---|---|
| `export_endpoint` | `""` | OTLP/HTTP+JSON 目标；留空则禁用导出。 |
| `export_headers` | `{}` | 每次 POST 附加的 HTTP 头。 |
| `export_batch` | `32` | 缓冲达到该条数即冲刷。 |
| `export_interval` | `5` | 定时冲刷间隔（秒）。 |

`export_endpoint` 留空即保持本地 JSONL——行为与不接导出器完全一致。

---

## 4. 智能体级离线评估 —— `contextloom eval`

### 是什么

零依赖、可复现的评估 harness（`internal/eval/eval.go`）。你在 `eval.yaml` 里声明任务集（prompt + 文件/gold/命令/正则检查器）；harness 为每个任务开一个全新临时工作区执行，输出**通过率报告**。对标 OpenHands/Aider“把评估做成可复现一等公民”的落地，可在 CI 复跑。

### `eval.yaml` 结构（`eval.Spec`）

```yaml
name: <string>            # 报告名；缺省用文件名
tasks:                    # 必填、非空
  - id: <string>          # 可选；缺省自动补 task-1、task-2、…
    prompt: <string>      # 交给 agent 的任务
    files: [<path>, ...]  # 从 spec 所在目录拷贝到临时工作区的文件（相对路径）
    gold_contains: [...]  # agent 输出包含任一子串即过（不区分大小写）
    file_contains:        # 工作区文件包含子串即过（不区分大小写）
      - file: <path>
        substr: <string>
    check_regex: [...]    # 所有正则必须全部匹配 agent 输出（dry-run 跳过）
    check: <shell 命令>   # 在工作区执行，exit 0 即过（dry-run 跳过）
```

任务有效的条件：有 `prompt` **或**至少一个检查器（`file_contains` / `check` / `check_regex`）。每个 `check_regex` 必须在加载时可编译——非法正则在加载期报错退出，避免运行期 panic。

**判定逻辑**（real 模式）：`(gold 命中或 gold 为空) AND (所有文件检查命中) AND (所有正则匹配)`，全部通过后若 `check` 非空，命令还须 exit 0。

### `--dry-run` vs `--real`

| 模式 | 标志 | 调用模型？ | 执行 `check` / `check_regex`？ | 执行 `file_contains`？ |
|---|---|---|---|---|
| Dry-run（**默认**） | `--dry-run`（默认 true） | 否 | 否 | 是 |
| Real | `--real` | 是 | 是 | 是 |

- `--dry-run` 是默认：只验证解析、文件拷贝与文件检查器，**零副作用**——不调用模型、不跑命令、不跑正则。CI 可安全复跑。
- `--real` 为每个任务的 prompt 调用已配置的模型后端，需可用端点/API Key。
- `--direct` 走轻量直接调用路径（仅加载 config + 模型后端，不走完整 runtime/TUI/插件链），适合大规模基准集（GSM8K / HumanEval / Terminal-Bench）。
- `--model <preset>` 覆盖使用的模型预设（默认 active_model）。

### 命令

```bash
contextloom eval --file eval.yaml --dry-run
contextloom eval --file eval.yaml --real --json report.json
contextloom eval --file tasks.jsonl --direct --model local-model   # 大数据集，直接模式
```

### 报告 JSON（`eval.Report`）

```json
{
  "name": "go-basics",
  "tasks": [
    {"id": "add-func", "pass": true, "ms": 12, "output": "..."}
  ],
  "passed": 1,
  "total": 1,
  "pass_rate": 1.0,
  "dry_run": true
}
```

| 字段 | 含义 |
|---|---|
| `tasks[].id` / `pass` | 任务 ID 与判定。 |
| `tasks[].err` | 失败原因（`omitempty`）。 |
| `tasks[].ms` | 墙钟耗时。 |
| `tasks[].output` | agent 输出，截断至 300 字符（`omitempty`）。 |
| `passed` / `total` / `pass_rate` | 汇总。 |
| `dry_run` | 是否为 dry-run。 |

### 完整可运行示例

`eval.yaml`：

```yaml
name: go-basics
tasks:
  - id: add-func
    prompt: "写一个 Go 函数 add(a, b int) int，返回两者之和。"
    gold_contains: ["func add"]

  - id: create-main
    prompt: "创建 main.go，包含一个 main 函数。"
    files: ["main.go"]          # 从本目录拷贝到临时工作区
    file_contains:
      - file: "main.go"
        substr: "func main"
    check: "test -f main.go"    # exit 0 即过（仅 real 模式）

  - id: reply-done
    prompt: "只回复一个单词 DONE。"
    check_regex: ["(?i)done"]   # 每个正则都必须匹配输出
```

```bash
# CI 安全：仅解析 + 文件检查器验证，不调用模型：
contextloom eval --file eval.yaml --dry-run

# 真实运行：调用已配置模型，产出 JSON 报告：
contextloom eval --file eval.yaml --real --json report.json
```

---

## 5. 检索质量评估 —— `contextloom eval-retrieval`

### 是什么

Harness 检索引擎的纯离线基线（`internal/eval/retrieval.go`）。它对固定语料建索引、逐查询检索，输出 **Recall@K** 与 **MRR**——让检索改进由数据驱动，而非拍脑袋。零 API 依赖，CI 可复跑。

### `retrieval.yaml` 结构（`eval.RetrievalSpec`）

```yaml
name: <string>
corpus:                    # 待索引的块
  - id: <string>           # 块 ID（被 gold 引用）
    content: <string>
    meta: {k: v}           # 可选；预留未来按元数据过滤的检索评测
queries:                   # 必填、非空
  - query: <string>
    gold: [<id>, ...]      # 相关块 ID 全集
ks: [1, 3, 5]              # Recall@K 的 K 列表；缺省 [1, 3, 5, 10]
```

### Recall@K 与 MRR 如何计算

对每条查询，引擎返回块的排序列表。设 `gold` 为相关 ID 集。

- **排名（ranks）** —— 每个 gold id 在结果中的 1-based 位置（未命中 = 0）。
- **Recall@K** —— `[1..K] 内命中的 gold 数 / len(gold)`，对所有查询取均值；对 `ks` 中每个 `K` 输出为 `"R@<k>"`。
- **MRR** —— 每条查询取**首个**（最优，即最小排名）gold 命中的 `1 / 排名`；无命中为 0。对所有查询取均值。
- 明细中每条查询的 `recall_at_k` 用的是**最大的 K**。

### 命令

```bash
contextloom eval-retrieval --file retrieval.yaml
contextloom eval-retrieval --file retrieval.yaml --json retrieval.json
```

### 报告 JSON（`eval.RetrievalReport`）

```json
{
  "name": "retrieval-smoke",
  "queries": 3,
  "corpus": 4,
  "ks": [1, 3, 5],
  "recall_at_k": {"R@1": 1.0, "R@3": 1.0, "R@5": 1.0},
  "mrr": 1.0,
  "details": [
    {"query": "search cache LRU capacity", "ranks": [1], "recall_at_k": 1.0, "mrr": 1.0}
  ]
}
```

文本输出（`RenderText` 加 CLI 追加的逐查询明细行）：

```
检索评测 retrieval-smoke：语料 4 块 / 查询 3 条
  Recall@1 = 1.00
  Recall@3 = 1.00
  Recall@5 = 1.00
  MRR = 1.000
  search cache LRU …  R=1.00 MRR=1.00 gold排名=[1]
  检索缓存容量淘汰        R=1.00 MRR=1.00 gold排名=[1]
  倒排索引删除           R=1.00 MRR=1.00 gold排名=[1]
```

### 完整示例 —— 中英混合语料，展示跨语言同义词命中

这对应引擎的双向中英同义词表：**中文查询能命中英文块**，反之亦然。

```yaml
name: cross-language-smoke
corpus:
  - id: zh-cache
    content: "搜索缓存使用 LRU 淘汰策略，容量上限 256 条，过期时间 2 秒"
  - id: en-lru
    content: "searchCache implements LRU eviction with capacity limit 256"
  - id: zh-index
    content: "倒排索引维护 token 到块的映射，支持快速检索与删除"
  - id: en-idf
    content: "IDF weighting downgrades common tokens like the and err"
queries:
  - query: "检索缓存容量淘汰"             # 中文查询 → zh-cache
    gold: [zh-cache]
  - query: "search cache LRU capacity"    # 英文查询 → en-lru
    gold: [en-lru]
  - query: "倒排索引删除"                 # 中文查询 → zh-index
    gold: [zh-index]
ks: [1, 3, 5]
```

```bash
contextloom eval-retrieval --file retrieval.yaml --json retrieval.json
```

健康基线在该集上应为 `R@1 = 1.00`、`MRR = 1.000`。任一指标下降，即为一次待排查的检索退化。

---

## 6. 文件操作审计与质量账本

### 6.1 文件操作审计（`internal/audit`）

对**敏感文件/工具操作**的仅追加 JSONL 审计追踪，按日轮转——企业“审计追踪”治理的轻量落地（鉴权不在范围，审计在内）。

**位置：** `.contextloom/audit/audit-YYYYMMDD.jsonl`（按日轮转，保留最近 **30** 个文件）。

**记录的动作**（`audit.Action`）：`write_file`、`replace_in_file`、`delete`、`exec_command`、`read_file`、`list_files`，以及 `tool_call`（兜底）。

**记录字段**（`audit.Entry`）：

| JSON 字段 | 类型 | 说明 |
|---|---|---|
| `ts` | RFC3339 | 记录时间。 |
| `session` | string | 会话 ID。 |
| `action` | string | 上述动作之一。 |
| `tool` | string | 工具名。 |
| `path` | string | 目标路径。 |
| `ok` | bool | 操作是否成功。 |
| `ms` | int64 | 耗时。 |
| `err` | string | 错误信息。 |
| `detail` | string | 附加说明。 |

审计器由 tools 层注入，`nil` 安全（不审计）。记录为尽力写入，绝不阻塞主流程。

### 6.2 质量账本（`quality_ledger.yaml`，`internal/selfcheck/ledger.go`）

每次质量门禁返回 **Fail**，缺陷签名就写入 `quality_ledger.yaml`——一份**持久的怀疑记录**，重启不丢。累计签名让质检员在下一轮优先核查同类问题。

```yaml
defect_history:
  - signature: "改完不跑测试"       # 可读缺陷签名
    count: 3                        # 出现次数
    last_seen: "2026-01-15T09:30:00Z"   # RFC3339，可空
```

| 字段 | 含义 |
|---|---|
| `signature` | 缺陷签名字符串。 |
| `count` | 累计出现次数。 |
| `last_seen` | 最近一次出现时间（RFC3339）。 |

账本随 checkpoint 在目标级持久化。`TopSuspects(n)` 返回频次最高的 n 个签名（频次相同按签名排序以保证确定性）。

---

## 7. 证据链（`internal/goalmgr/evidence.go`）

### 为什么

口头声明（“测试通过”“做完了”）**不算**证据。`VerifyGoalProofs` 只认三类**物理**通道，全部由确定性进程（文件系统 / git / `go test`）核验，零模型参与：

| `kind` | `target` | 核验方式 |
|---|---|---|
| `file` | 相对路径 | `os.Stat` —— 文件存在且**非**目录 |
| `diff` | 相对路径 | 出现在 `git diff --name-only`（工作区确有改动） |
| `test` | 包路径（如 `./internal/x`） | `go test <target>` 退出码 0（实测，带超时；默认 120s） |

### 证据结构（`types.GoalProof`）

```json
{
  "kind": "test",
  "target": "./internal/eval",
  "note": "检索回归套件"
}
```

| 字段 | 含义 |
|---|---|
| `kind` | `file` / `diff` / `test`（其他值 → 核验失败；未知类型宁可拒绝，不放行）。 |
| `target` | file=相对路径；diff=相对路径；test=包路径。 |
| `note` | 自由说明；**不参与**判定。 |

### 语义

- 仅当步骤 `Status == done` 且携带 `Proofs` 时才核验。
- 每条证据独立核验，失败项以可读的 `ProofFailure{StepID, Kind, Target, Reason}` 返回。
- `test` 类证据必须实际执行 `go test` 且退出码 0——模型口述“测试通过”不算。
- 失败项转为缺陷条目（`EvidenceDefects`）进入下一轮修复清单；已 done 步骤若证据核验失败会被降级回 pending。

---

## 8. 自检（`internal/selfcheck`）

质量自我进化三件套：**质疑有主体**（静态核查器）、**证据要实测**（测试 TEE）、**不过不外显**（强制门禁）。三者基于标准库 + `gopkg.in/yaml.v3`（`go.mod` 零新增依赖），与 runtime/goalmgr/executor/overseer 完全解耦。

### 8.1 质量三档（`QualityLevel`）

| 档 | 字符串 | 静态核查 | 测试 TEE | 门禁 |
|---|---|---|---|---|
| `standard`（默认） | `standard` | 开 | 开 | 开 |
| `fast` | `fast` | 开 | 关 | 开 |
| `none` | `none` | 关 | 关 | 关（回旧行为） |

由 `gate.yaml: level` 配置。空/非法字符串回退 `standard`（防呆：质量默认拉满）。

### 8.2 静态核查器（`static.go`）

扫描改动文件并报告问题。`StaticReport.Pass` 仅当**零 high/medium** 问题时为 true。

| 问题 `kind` | 含义 |
|---|---|
| `residue` | 残留标记：`TODO` / `FIXME` / `XXX` / `HACK` / `panic("TODO")` / 临时文件标记 / debug 打印 |
| `unresolved` | 未决标记 `[?]`（严重度 **high**） |
| `unused` | 本次声明但项目内未见使用的符号 |

`Issue.Severity` ∈ `high` / `medium` / `low`。字段：`file`、`line`、`kind`、`hint`、`severity`。

### 8.3 测试 TEE（`tee.go`）

重跑测试以防自我欺骗。`Verify(TestTEEResult)` 按产物类型裁决：

| 产物类型 | 字符串 | 通过条件 |
|---|---|---|
| Go 代码 | `go_code` | `compiled` **且** `test_passed` |
| 非 Go 代码 | `non_go_code` | `test_passed` |
| 文档 | `doc` | TEE 不适用 → `needs_logic_check` |
| 未知/空 | — | 保守判**失败** |

`TestTEEResult` 字段：`artifact_type`、`compiled`、`test_passed`、`test_count`、`output`、`sha`。裁决：`passed` / `failed` / `needs_logic_check`。

### 8.4 门禁（`gate.go`）

门禁有牙齿。`Admit(QualityReport)` 返回三态之一：

- **`pass`** —— 静态核查 + TEE + 评分全通过。
- **`fail`** —— 任一不通过；该裁决禁写 `[GOAL_DONE]`，并把缺陷清单（`DefectsFromReport`）注入下一轮。
- **`human_review`** —— 连续 **3** 轮不收敛（`maxNonConverge = 3`）转人工 / 换质检员。

通过阈值默认 **90**。文档类产物跳过 TEE，以静态核查 + 评分裁决。

---

## 9. 相关文档

- [README.zh-CN.md](../README.zh-CN.md) —— 项目总览、CLI 用法、TUI 命令速查（中文）
- [README.md](../README.md) —— Project overview (English)
- [docs/architecture.md](architecture.md) —— 整体系统架构
- [docs/context-engine.md](context-engine.md) —— 上下文管理与 harness_search 检索引擎（`eval-retrieval` 所度量的对象）
- [docs/toolchain.md](toolchain.md) —— 员工 / 场景 / 工具链 / 门禁
- [docs/comparison.zh-CN.md](comparison.zh-CN.md) —— 与同类 agent harness 的特性对比
- [config.example.yaml](../config.example.yaml) —— 完整配置参考（`trace`、`overseer`、`harness` 章节）

---

## 关键结论摘要

1. **三层可观测，一种格式。** trace、audit、质量账本都是 `.contextloom/` 下的普通 JSON/JSONL——可用 `cat`/`jq` 查看、可提交、重启不丢。
2. **两层评估，均可离线。** `contextloom eval` 给 agent 任务集打分；`contextloom eval-retrieval` 用 Recall@K / MRR 给检索打分。`--dry-run` 无需网络。
3. **`--dry-run` 是 CI 默认。** 它零副作用地验证解析与文件检查器；`--real` 是显式开启、真正调用模型的那一步。
4. **证据胜过声明。** 步骤 `done` 只有在其 `file`/`diff`/`test` 证据通过确定性核验时才可信——口述“测试通过”被拒绝。
5. **不过不外显。** 门禁在失败时禁写 `[GOAL_DONE]`，且每个 Fail 签名落进 `quality_ledger.yaml`，让反复出现的缺陷跨轮可见。
