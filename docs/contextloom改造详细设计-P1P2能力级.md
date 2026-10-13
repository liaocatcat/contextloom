# ContextLoom 改造详细设计（P1/P2 能力级）

> **性质**：改造实施设计（接口级）· 中文 Markdown · 设计草案（待评审）
> **范围**：P1 能力级 5 项 + P2 增强级 6 项，共 11 项
> **读者**：ContextLoom 维护者与后续实施者
> **关联文档**：《contextloom改造详细设计-P0架构级.md》（同目录，负责 P0-1/P0-2/P0-3，本文档按其编号交叉引用）；《业界前沿对标分析文档.md》（同目录，其第 5 节为本文档的展开依据）
> **证据来源**：ContextLoom 源码逐一核实（`internal/{tools,runtime,goalmgr,ctxmgr,harness,sandbox,acp,executor,orchestrator,selfcheck,plugin,scheduler}`）；dsh 参照 `packages/{lsp,subagent,workflow,jobs,plan,spill,goal,core}`。

**总述**：把"对标分析给出的 11 条借鉴建议"翻译成 ContextLoom 语境下可开工的接口设计。全部遵循三条底线：**零依赖、默认行为不变（增量可关闭）、可独立交付**。

---

## 0. 总览与设计约定

### 0.1 总览表

| 编号 | 能力项 | 前置依赖 | 估算（人日） | 批次 |
|---|---|---|---|---|
| P1-1 | LSP 集成（lsp 工具 + stdio provider，符号索引兜底） | 无 | 6–8 | 批次 2 |
| P1-2 | subagent provider 注册表（按名多后端，含 ACP） | P2-6 | 7–9 | 批次 3 |
| P1-3 | workflow 脚本编排（受限 DSL + 总额度上限） | P1-2、P2-6 | 8–10 | 批次 4 |
| P1-4 | jobs 后台任务治理（job_list/job_output/job_kill） | P2-9（协同） | 4–6 | 批次 2 |
| P1-5 | plan-mode 软引导（/plan + exit_plan_mode 审批） | P2-6 | 4–5 | 批次 3 |
| P2-6 | scope 原语（per-agent 注册隔离，替代 CloneWithout） | 无 | 3–4 | 批次 1 |
| P2-7 | goal block 统一语义（四类原因共用 durable 阻塞态） | 无 | 3–4 | 批次 2 |
| P2-8 | compaction 锁与孤儿检测 | 无（P0-1 可选增强） | 2–3 | 批次 1 |
| P2-9 | 工具结果 spill 标准化（locator + retrievalHint） | P1-4（协同） | 3–4 | 批次 3 |
| P2-10 | 文档生成校验门禁（registry → docs/tools.md + CI） | 无 | 1.5–2 | 批次 1 |
| P2-11 | SandboxEnforcement 完整性报告 | P0-3 | 2 | 批次 4 |

合计约 **44–59 人日**。

### 0.2 设计原则

1. **零依赖底线**：优先用 Go 标准库与现有模块；引入第三方库（如嵌入式 JS 引擎）须单独评审，默认不做。
2. **接口先行，默认实现不变**：统一"接口 + 注册表 + 默认实现"模式；未配置/未启用时行为与现状一致（延续 v1.8 工具链体系"零配置兜底"惯例，见 `internal/runtime/wiring.go`）。
3. **模型面最小扩张**：新增工具必须可按 scope 关闭（与 P2-6 联动），避免工具表膨胀稀释模型注意力。
4. **可独立交付**：11 项解耦、按依赖批次推进，每项自带验收标准。
5. **P0 交叉点显式标注**：依赖或受益于 P0-1/P0-2/P0-3 的项标注"可选增强 / 硬依赖"（交叉点表见 §13.1）。

### 0.3 通用落地模式（ContextLoom 轻量 Seam）

dsh 的 Seam 三角（Service Definition / Service Provider / Consumer）在 ContextLoom 的轻量落地约定：新包放 `internal/<capability>/`，注册接线统一放 `internal/runtime/wiring.go`，配置缺失即注册默认实现。

```go
type Provider interface {
    Name() string     // 注册名（配置/工具参数引用）
    Available() error // 可用性探测（fail-loud：不可用要说清原因）
}
type Registry struct { mu sync.RWMutex; providers map[string]Provider }
func (r *Registry) Register(p Provider)
func (r *Registry) Get(name string) (Provider, bool)
func (r *Registry) Default() Provider // 默认实现 = 现有行为
```

---

## 1. P1-1 LSP 集成（lsp 工具 + stdio provider 抽象）

### 1.1 现状（源码定位）

- 代码理解：`internal/harness/` 自研三层检索（倒排 `index.go`、符号 `symbols.go`、语义 `semantic.go`/`embedding.go`）+ 三区窗口（`window.go`）；符号索引为**正则/启发式**，非语义级。
- 全仓库无 LSP 代码（`internal/` 无 lsp 包）；工具面在 `internal/tools/registry.go`（`registerBuiltins` 9 个内置工具；`toolDescriptions`/`toolSchemas` 为单一事实源，新增工具需补注册/描述/schema 三处）。
- dsh 参照：`packages/lsp/`（`lsp` 核心 + `lsp-stdio` 子进程 provider + `tool-lsp`）。

### 1.2 目标设计

LSP 提供语义级理解（定义/引用/诊断/悬停），**符号索引保留兜底**（服务器不可用时回退 harness 检索）。

```go
// internal/lsp/provider.go
type ServerSpec struct {
    ID          string   // "gopls"
    Command     string
    Args        []string
    Languages   []string // ["go"]
    RootMarkers []string // ["go.mod"]
}
type Provider interface {
    ID() string
    Languages() []string
    Detect(root string) bool
    Spawn(ctx context.Context, root string) (Session, error)
}

// internal/lsp/session.go
type Session interface {
    Initialize(ctx context.Context) (Capabilities, error)
    DidOpen(ctx context.Context, file string) error
    Definition(ctx context.Context, file string, line, col int) ([]Location, error)
    References(ctx context.Context, file string, line, col int) ([]Location, error)
    Diagnostics(ctx context.Context, file string) ([]Diagnostic, error)
    Hover(ctx context.Context, file string, line, col int) (string, error)
    DocumentSymbols(ctx context.Context, file string) ([]Symbol, error)
    Shutdown(ctx context.Context) error
}
```

- **协议**：LSP base protocol（JSON-RPC 2.0 over stdio，`Content-Length` 头分帧）；请求/响应按 `id` 配对，服务器通知（`textDocument/publishDiagnostics`）缓冲待取；只实现**只读查询子集**（写类 rename/applyEdit 不开放）；单请求超时 15s，崩溃隔离（重启或标记不可用）。
- **工具 `lsp`**：参数 `{action: definition|references|diagnostics|hover|symbols, path, line, column}`；无可用服务器时 fail-loud 提示并建议 `search_content`/符号检索。
- **配置**：`lsp.servers: [{id, command, args, languages, root_markers}]`（懒启动）。

### 1.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/lsp/{provider,jsonrpc,stdio,session,tool}.go`（新） | 接口 + 分帧编解码 + 子进程 provider + 工具 |
| `internal/tools/registry.go` | 注册 `lsp`（注册/描述/schema 三处） |
| `internal/config/` | lsp 配置节 |
| `internal/runtime/wiring.go` | 按配置注册 provider（懒启动） |
| `docs/tools.md` | 由 P2-10 门禁自动生成 |

### 1.4 验收标准

1. 含 `go.mod` 项目中对已知函数 `lsp(definition)` 返回正确文件:行列（对标 gopls 手工结果）；
2. 无语言服务器时返回明确错误 + 兜底建议，不 panic、不阻塞；
3. 服务器崩溃/超时：会话隔离，进程无残留；
4. 单测覆盖 JSON-RPC 分帧/粘包、id 配对、通知缓冲、超时终止；
5. 未启用 lsp 配置时工具面零回归。

### 1.5 工作量估算

**6–8 人日**：stdio provider + JSON-RPC 3d；工具与接线 1.5d；测试 1.5d；配置与文档 1d。

### 1.6 依赖关系

无 P0 硬依赖；与 P0-2（Seam）命名对齐（不阻塞）；P2-10 落地后自动入文档。

### 1.7 风险

语言服务器环境差异（CI 无 gopls）→ 只读子集 + 探测降级；Windows 子进程管道差异 → 复用 `exec_windows.go` 经验；协议面大 → 明确只读子集边界。

---

## 2. P1-2 subagent provider 注册表

### 2.1 现状（源码定位）

- `spawn_task` 为**进程内唯一实现**：schema 见 `internal/tools/registry.go`（参数 task/directive/context；活动式超时"连续 10 分钟无输出终止"，`toolTimeoutOverride` 豁免统一 120s）；执行入口 `internal/runtime/runtime.go`（SpawnTask），测试 `spawn_task_test.go`/`spawn_timeout_test.go`。
- 装配：`runtime/wiring.go` 员工中心（`.contextloom/employees/<name>/employee.yaml`，directive 命中员工名或 CODE_AGENT→coder 映射）——装配与执行后端耦合在 runtime 内。
- 工具隔离现状：`Registry.CloneWithout(exclude ...string)`（黑名单删工具，调用点 `runtime.go`/`wiring.go`）。
- 已有 ACP 基建：`internal/acp/`（`server.go`、`client.go` WebSocket JSON-RPC 客户端、`protocol.go`、`runtime_handler.go`）——acp provider 低成本基础。
- dsh 参照：`packages/subagent/` 6 个 provider（spawn-in-process / fork-in-process / acp / codex / claude-code / dsh-sdk）+ `tool-subagent` / `tool-subagent-control`。

### 2.2 目标设计

spawn_task 从"进程内唯一"变为"按名多后端"：

```go
// internal/subagent/provider.go
type Caps struct {
    Streaming   bool // 流式进度
    Interrupt   bool // 中断
    Continuable bool // 续跑（追加指令）
    Reports     bool // 主动上报
}
type TaskSpec struct {
    Prompt    string
    WorkDir   string
    Model     string        // 空 = 继承
    Tools     []string      // scope 白名单（与 P2-6 联动）
    Directive string        // 兼容 CODE_AGENT/员工名语义
    Timeout   time.Duration // 0 = 活动式超时（现状语义）
}
type Result struct {
    Status  string // done | failed | interrupted
    Summary string
    Outputs []string
}
type Handle interface {
    ID() string
    Wait(ctx context.Context) (*Result, error)
    Interrupt(ctx context.Context) error
    Resume(ctx context.Context, message string) error // Continuable 时可用
    Progress() <-chan string                          // Streaming 时可用
}
type Provider interface {
    Name() string
    Caps() Caps
    Available() error
    Spawn(ctx context.Context, spec TaskSpec) (Handle, error)
}
```

- **注册表**：`Register/Get`；未知或不可用后端 **fail-loud**（含可用列表），不静默回退；
- **provider 实现**：`in-process`（默认，包装现有 SpawnTask，行为不变）；`acp`（复用 `internal/acp` Client：initialize → session/new → session/prompt，消费 session/update）；`cli`（可选，外部 agent 子进程 + 适配器，Caps 保守声明，不支持的能力明确报错）；
- **工具面**：`spawn_task` 增加可选参数 `backend`（默认 `"in-process"`，向后兼容）；`list_agents` 可先并入 jobs（P1-4）。

### 2.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/subagent/`（新包） | 接口 + Registry + in-process provider |
| `internal/subagent/acp_provider.go`（新） | 基于 `internal/acp` Client 的 ACP provider |
| `internal/subagent/cli_provider.go`（新，可选） | 外部 agent CLI 适配 |
| `internal/runtime/runtime.go` | SpawnTask 改走 registry（默认保旧行为） |
| `internal/runtime/wiring.go` | 按配置注册 provider |
| `internal/tools/registry.go` | spawn_task schema 增 `backend` |
| `internal/acp/client.go` | 会话生命周期/通知消费增强（复用优先） |

### 2.4 验收标准

1. 全部现有 spawn_task 测试通过（directive/员工装配/活动式超时保留）；
2. `spawn_task(backend="acp")` 对接测试 ACP agent 完成一次子任务（e2e）；
3. 未知/不可用 backend 返回明确错误（含可用列表）；
4. Caps 不满足的操作明确拒绝（如非 Continuable 后端 Resume）；
5. 并发/超时/中断语义与现状一致。

### 2.5 工作量估算

**7–9 人日**：接口 + in-process 包装 2d；acp provider 2.5d；cli provider 1.5d（可选）；接线与工具 1d；测试 1.5d。

### 2.6 依赖关系

P2-6（scope：工具集传递）建议先行；P0-2 风格对齐（不阻塞）；被 P1-3 依赖为执行底座。

### 2.7 风险

外部 agent 协议/版本差异 → Caps 协商 + fail-loud；权限边界（外部 agent 不受 ContextLoom 沙箱约束）→ 默认不启用 + 文档化；行为回归 → 默认包装 + 全量回归测试。

---

## 3. P1-3 workflow 脚本编排

### 3.1 现状（源码定位）

- 无脚本化编排：多代理协作靠**提示词引导** + `internal/executor/handoff.go`（planner/coder/verifier 串行）+ `runtime.go` 多批次拆分。
- 可复用基建：`internal/orchestrator/`（`driver.go`/`graph.go`/`queue.go`/`team.go`）、`internal/executor/messagebus.go`。
- dsh 参照：`packages/workflow/`（`workflow` + `workflow-ptc` + `tool-workflow`）：模型编写编排脚本（top-level await、`agent()` 原语、`return json`），worker 线程隔离，`maxTotalAgents` 总额度。

### 3.2 目标设计

**默认采用受限 DSL（YAML/JSON 步骤图），不引入嵌入式 JS 引擎**（零依赖底线；goja 等列入待评估）。

```yaml
name: review-and-fix
max_agents: 6          # 总额度上限（对齐 dsh maxTotalAgents）
timeout: 30m
on_error: fail_fast    # fail_fast | continue
steps:
  - id: scan
    agent: { backend: in-process, directive: CODE_AGENT }
    prompt: "扫描 src/ 找出未处理错误"
    output: scan_result
  - id: fix
    needs: [scan]
    agent: { backend: acp, endpoint: "ws://127.0.0.1:7777" }
    prompt: "根据 ${scan_result} 修复"
  - id: verify
    needs: [fix]
    when: "${fix_result.changed} == true"
    agent: { backend: in-process, directive: TEST }
    prompt: "验证 ${fix_result}"
```

```go
// internal/workflow/{spec,engine,tool}.go
type Spec struct {
    Name      string
    MaxAgents int           // 累计启动数上限，超限拒绝
    Timeout   time.Duration
    OnError   string        // fail_fast | continue
    Steps     []Step
}
type Step struct {
    ID     string
    Needs  []string // DAG 依赖
    When   string   // 受限布尔表达式（==/!=/&&/||）
    Agent  subagent.AgentSpec
    Prompt string   // 支持 ${step.output} 引用
    Output string
}
type Engine struct {
    subs      *subagent.Registry
    maxAgents int
}
func (e *Engine) Run(ctx context.Context, spec *Spec) (*RunResult, error)
```

- 执行：按 `needs` 拓扑排序 → 无依赖步骤并行 → 每步经 P1-2 registry 启动子代理 → 输出聚合 JSON；
- **额度即护栏**：累计启动数 > `max_agents` → 显式拒绝（非排队）；
- 变量引用仅 `${step.output}` / `${step.output.field}`（JSON path 一级），不做通用表达式引擎；
- 工具 `run_workflow`：`{spec: inline | path}`。

### 3.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/workflow/{spec,engine,tool}.go`（新） | DSL 解析校验（环检测）+ 引擎 + 工具 |
| `internal/tools/registry.go` | 注册 run_workflow（三处） |
| `internal/runtime/wiring.go` | 接线 Engine（注入 subagent.Registry） |
| `config.example.yaml` | 默认额度/超时 |

### 3.4 验收标准

1. 串行/并行/DAG 三类编排跑通，结果 JSON 完整；
2. 超 `max_agents` 拒绝并说明原因；
3. `fail_fast`/`continue` 语义正确；
4. 非法 DSL（环、未定义变量、未知 backend）校验期报错；
5. 每步有审计记录。

### 3.5 工作量估算

**8–10 人日**：解析校验 2d；引擎 3d；工具与接线 1.5d；测试 2d；文档 0.5d。

### 3.6 依赖关系

**硬依赖 P1-2**；P2-6（每步限定工具集）；P0-2 可选。

### 3.7 风险

DSL 表达力权衡 → 一期只做"步骤图 + 变量引用"；与 goal 交互 → 一期不嵌套 goal；并发资源 → 额度 + 超时护栏。

---

## 4. P1-4 jobs 后台任务治理

### 4.1 现状（源码定位）

- `exec_command`（`internal/tools/exec.go`）：前台执行，默认 20s、上限 5min；超时杀整棵进程树（`runWithReliableTimeout` + `exec_unix.go`/`exec_windows.go`）；**无后台模式**（schema 仅 command/timeout_ms）。
- `spawn_task` 为长任务（活动式超时）但无统一治理面；无 `job_*` 工具（registry 工具表无；搜索"后台"无结果）。
- dsh 参照：`packages/jobs/`（`jobs` + `jobs-local` + `tool-jobs`）：`job_list`/`job_output`/`job_kill` 统一管理后台命令、PTY、后台子代理。

### 4.2 目标设计

```go
// internal/jobs/manager.go
type State string // running | done | failed | killed
type Job struct {
    ID        string // "job-<n>"
    Kind      string // cmd | subagent | acp
    State     State
    StartedAt time.Time
    EndedAt   time.Time
    ExitCode  int
    LogPath   string // .contextloom/jobs/<id>.log
}
type Manager struct {
    mu   sync.Mutex
    jobs map[string]*Job
}
func (m *Manager) Start(kind string, run func(ctx context.Context, sink io.Writer) error) (*Job, error)
func (m *Manager) List() []*Job
func (m *Manager) Output(id string, offset, limit int) (string, bool /*truncated*/, error)
func (m *Manager) Kill(id string) error
```

| 工具 | 参数 | 语义 |
|---|---|---|
| `job_list` | 无 | 列出全部 job（id/kind/state/耗时/输出量） |
| `job_output` | `job_id`, `offset`, `limit` | 读输出（环形缓冲 + 落盘；超限提示走 spill） |
| `job_kill` | `job_id` | 终止（复用进程树终止逻辑） |

- 接入：`exec_command` 增 `background: true` → 立即返回 `job_id`；`spawn_task` 外部后端注册为 job；
- 输出：环形缓冲（内存上限如 1MB）+ 全量落盘；与 P2-9 locator 对齐。

### 4.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/jobs/`（新包） | Job 模型 + Manager + 落盘/缓冲 |
| `internal/tools/registry.go` | 注册 job_list/job_output/job_kill |
| `internal/tools/exec.go` | `background` 分支接入 Manager |
| `internal/runtime/runtime.go` | 子代理任务注册为 job |
| `internal/runtime/wiring.go` | Manager 单例接线 |

### 4.4 验收标准

1. 后台任务可列出/增量读输出/终止（进程树无残留）；
2. 缓冲截断时明确提示 + 落盘全量可读；
3. 退出后状态正确（done/failed + exit_code）；
4. 进程重启后旧 job 标记 `dead`；
5. 前台 exec 零回归。

### 4.5 工作量估算

**4–6 人日**：Manager + 落盘 2d；三工具 1d；接入 1d；测试 1.5d。

### 4.6 依赖关系

与 P2-9 协同（job 输出是 spill 来源之一）；无 P0 硬依赖。

### 4.7 风险

跨平台进程终止差异（复用 exec_unix/windows 封装）；内存/磁盘占用（缓冲上限 + 清理）；后台任务需加入 `toolTimeoutOverride` 豁免类。

---

## 5. P1-5 plan-mode 软引导

### 5.1 现状（源码定位）

- "计划"是 **goal 模式内部概念**：`internal/goalmgr/goalmgr.go` 的 `GoalPhasePlan` + `SetPlan()`；无 `/plan` 命令、无 `exit_plan_mode` 工具、无审批呈现（计划以普通文本出现）。
- 模式体系：chat/task/goal 并存（goalmgr 注释"mode 切回 chat/task"）。
- 审批基建已有：ACP `runtime_handler.go` 的 `request_permission`；TUI 确认对话框（`ConfirmFunc`，`tools/registry.go`）。
- dsh 参照：`packages/plan/plan-mode`：`/plan [message]` 只读规划；`exit_plan_mode` 提交；plan-review 把计划呈现为"决策"。

### 5.2 目标设计

在 chat/task/goal 之外新增 `plan` 模式（**与 goal 解耦**，不进 goalmgr 状态机）：

- `/plan [message]`：进入只读模式（工具集切为只读 scope：read_file/search_content/list_files/git_diff/lsp/符号检索）；写类工具调用被拒（提示"plan 模式只读"；exec_command 默认禁用）；
- `exit_plan_mode` 工具：`{plan: string, steps: []string}` → 触发审批呈现：TUI 决策面板（[批准执行/继续修改/放弃]）、GUI 弹窗（复用确认组件）、ACP `request_permission`；
- 批准 → 退出 plan 模式，计划作为执行上下文注入；拒绝 → 留在 plan 模式修改。

```go
// internal/runtime/plan.go（概念）
type PlanState struct {
    Active    bool
    SessionID string
    Draft     string
    Steps     []string
}
// exit_plan_mode: { plan, steps? } → ConfirmFunc 审批 → { approved: bool }
```

### 5.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/runtime/`（或新 `plan.go`） | plan 模式状态与切换 |
| `internal/tools/registry.go` | 注册 `exit_plan_mode`；plan scope 下写类工具不可见/被拒 |
| `internal/tui/` | `/plan` 命令 + 决策面板 |
| `internal/gui/` | 审批弹窗 |
| `internal/acp/runtime_handler.go` | 审批走 request_permission |
| `internal/tools/scope.go`（P2-6） | 只读 scope 预设 |

### 5.4 验收标准

1. `/plan` 后写类工具被拒、只读工具正常；
2. `exit_plan_mode` 触发三端审批（至少 TUI + ACP）；
3. 批准退出并注入计划；拒绝留在 plan 模式；
4. chat/task/goal 零回归；
5. 无 UI 场景（headless/ACP）审批语义明确。

### 5.5 工作量估算

**4–5 人日**：模式与只读 scope 1.5d；exit_plan_mode + 审批桥 1.5d；TUI/GUI 1.5d；测试 0.5d。

### 5.6 依赖关系

P2-6（只读 scope）建议先行；P0-1 可选（plan 事件化）；复用 ACP 审批流。

### 5.7 风险

三端呈现一致性；与 goal 边界（明确"plan 不进 goalmgr"）；只读约束绕过面（exec_command 禁用）。

---

## 6. P2-6 scope 原语（per-agent 注册隔离）

### 6.1 现状（源码定位）

- `internal/tools/registry.go`：`Registry` 持全局工具表；`ToolDefinitions()` 返回全量（无过滤）。
- 隔离靠**黑名单**：`CloneWithout(exclude ...string)`——"返回排除指定工具的注册表副本（子 Agent 过滤 spawn_task 防递归）"；调用点 `runtime.go`/`wiring.go`。
- 问题：默认"全量开放"，新增工具**自动泄漏**给所有子代理；无法表达"只给这些工具"的白名单语义。

### 6.2 目标设计

```go
// internal/tools/scope.go（新）
type Scope struct {
    Name  string          // readonly | coder | verifier | planner | full
    Allow map[string]bool // nil = 全部允许
}
func FullScope() *Scope
func ReadOnlyScope() *Scope              // read_file/search_content/list_files/git_diff/lsp/符号检索
func ScopeOf(names ...string) *Scope     // 显式白名单
func (r *Registry) WithScope(s *Scope) *Registry // 浅拷贝共享 ToolFunc，独立可见集
func (r *Registry) ToolDefinitions() []types.ToolDefinition // 按视图过滤
// Execute 层双保险：视图外工具即使被构造调用也拒绝
```

- 预设：`readonly`（只读查询）、`coder`（读写 + exec）、`verifier`（只读 + run_tests）、`planner`（只读）、`full`（默认 = 现状）；
- 动态工具（`RegisterExtra`，MCP 等）：注册表全量持有，视图过滤统一在 `ToolDefinitions`/`Execute`；
- 迁移：`runtime.go`/`wiring.go` 的 `CloneWithout(...)` → `WithScope(ScopeOf(...))`；保留 `CloneWithout` 为 deprecated 兼容 API。

### 6.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/tools/scope.go`（新） | Scope 定义 + 预设 |
| `internal/tools/registry.go` | `WithScope`/视图过滤/Execute 拒绝 |
| `internal/runtime/runtime.go` | CloneWithout → scope |
| `internal/runtime/wiring.go` | 同上 + 员工档案 → scope 映射（可选） |
| `internal/executor/` | 按 scope 装配子代理工具面 |

### 6.4 验收标准

1. 子代理工具面 = 白名单（`ToolDefinitions` 与 `Execute` 双重验证）；
2. 默认全量路径零回归；
3. `CloneWithout` 调用点全部迁移（或仅剩 deprecated 包装）；
4. 反例测试：新增工具后 readonly scope 不可见。

### 6.5 工作量估算

**3–4 人日**：scope 与过滤 1.5d；调用点迁移 1d；测试 1d。

### 6.6 依赖关系

无 P0 依赖；**被 P1-2/P1-3/P1-5 依赖**（批次 1 先行）。

### 6.7 风险

动态工具与 scope 交互（约定：动态注册默认进 full，受限 scope 需显式放行）；插件工具（`internal/plugin`）归属同上。

---

## 7. P2-7 goal block 统一语义

### 7.1 现状（源码定位）

- `internal/goalmgr/goalmgr.go`：`Manager`（按会话隔离 `bySID`，检查点持久化 `.contextloom/goals/`）；完成判定：`[GOAL_DONE]` / 步骤全 done + Overseer / 轮次软提示（`DefaultMaxRounds=0`）/ **Token 超预算 → failed**（`DefaultTokenMultiplier=2`）/ continuation；`Abort()` 中止。
- **无独立阻塞态**：provider 限额（429）、预算耗尽、执行错误、需人工输入四类混在"failed/继续/软提示"语义里，恢复语义不清。
- dsh 参照：`packages/goal/`（`goal` + `goal-round-driver` + `command-goal` + `tool-goal`）：block 阶段统一（四类原因共用一个 durable phase）。

### 7.2 目标设计

新增 durable 阻塞态（可恢复），与 `failed`（终止）严格区分：

```go
// pkg/types（GoalSession 增补）
const GoalPhaseBlocked = "blocked"
type BlockReason string
const (
    BlockQuota     BlockReason = "quota"      // provider 限额（429/配额归一化）
    BlockBudget    BlockReason = "budget"     // token/轮次预算
    BlockExecError BlockReason = "exec_error" // 致命执行错误
    BlockNeedInput BlockReason = "need_input" // 请求人工输入/决策
)
// GoalSession 增补: BlockedReason / BlockedDetail / BlockedAt

// goalmgr 增补 API
func (m *Manager) Block(reason BlockReason, detail string) // 进入 blocked（持久化）
func (m *Manager) Unblock() bool                           // 恢复（用户输入/手动 resume）
```

- 进入 blocked：写检查点（含原因/详情）；TUI 状态栏 + ACP 通知呈现；
- 恢复：用户新消息 / `/goal resume` → `Unblock` → 从检查点续跑（不丢步骤进度）；
- 区分：blocked 可恢复；failed 终止。token 预算超限**默认改为 blocked**（`goal.fail_on_budget: bool` 可切回 failed）；
- 触发点：模型 API 错误分类 → Block；预算检查点 → Block；证据链致命失败 → Block(exec_error)；模型显式信号 `[GOAL_NEED_INPUT]` → Block(need_input)。

### 7.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/goalmgr/goalmgr.go` | Block/Unblock + 判定调整 |
| `pkg/types/` | GoalSession 字段增补 |
| `internal/runtime/runtime.go` | 错误分类 → Block；恢复路径 |
| `internal/tui/` | blocked 呈现 |
| `internal/acp/runtime_handler.go` | blocked 通知 |
| `config.example.yaml` | `goal.fail_on_budget` 等开关 |

### 7.4 验收标准

1. 四类原因均可进入 blocked 并持久化（重启可恢复）；
2. 恢复后不丢进度（步骤/轮次/预算完整）；
3. `failed` 与 `blocked` 不混淆（现有 failed 用例回归）；
4. 默认配置行为与现状一致。

### 7.5 工作量估算

**3–4 人日**：状态与持久化 1d；触发点接线 1.5d；呈现 0.5d；测试 1d。

### 7.6 依赖关系

无硬依赖；P0-1 落地后可升级为 `goal/blocked` 事件（可选增强）。

### 7.7 风险

错误分类边界（什么算致命）→ 保守白名单 + 配置覆盖；恢复时机（自动 vs 手动）与用户预期。

---

## 8. P2-8 compaction 锁与孤儿检测

### 8.1 现状（源码定位）

- `internal/ctxmgr/budget.go`：`TokenBudget` 五档渐进压缩——`CompactRatio 0.70` → `HardRatio 0.85`（丢弃最旧 + 滚动摘要）→ `SafetyRatio 0.92` 兜底；`KeepRecent 8`；摘要 ≤1200 字符；审计环 20 条（`/ctx`）；`EvictFunc` 把压缩原文交 Harness 索引（可找回）。
- **无压缩锁记录**：压缩直接改写消息数组；崩溃后无法区分"完成/半途"——静默风险。相关测试：`compact_guard_test.go`、runtime `compaction_reload_test.go` 等。
- dsh 参照：`compaction/start → summary → end` 带锁记录，崩溃可检出孤儿。

### 8.2 目标设计

压缩前后写两条配对记录（当前落会话 `.jsonl`；P0-1 后升级为事件）：

```go
// internal/ctxmgr/orphan.go（新）
type CompactionStart struct {
    TS        time.Time `json:"ts"`
    SessionID string    `json:"session_id"`
    Action    string    `json:"action"` // compact | hard_compact
    Usage     float64   `json:"usage"`
}
type CompactionEnd struct {
    TS        time.Time `json:"ts"`
    SessionID string    `json:"session_id"`
    Dropped   int       `json:"dropped"`
    Digest    string    `json:"digest"`
}
func DetectOrphans(jsonlPath string) ([]CompactionStart, error) // start 无配对 end = 孤儿
```

- 流程：压缩前 append start → 执行压缩（现有逻辑不变）→ 完成后 append end；
- 启动扫描（runtime 初始化）：检出孤儿 → 审计告警 + TUI 提示 + 保守恢复（不自动重写历史；提示 `/ctx` 检查 + 手动 `/compact` 重试）。

### 8.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/ctxmgr/budget.go` | 压缩流程包裹（start/end 钩子） |
| `internal/ctxmgr/orphan.go`（新） | 孤儿检测 |
| `internal/runtime/runtime.go` | 启动扫描接线 |
| `internal/memory/` | 会话 jsonl 追加 API 复用 |
| `internal/audit/` | 孤儿事件审计 |

### 8.4 验收标准

1. 模拟中断（压缩中 kill）→ 重启检出孤儿并告警；
2. 正常路径 start/end 成对、无残留；
3. 性能零回归（仅两次 append）；
4. 多实例同目录不互锁死（只追加，不做全局锁）。

### 8.5 工作量估算

**2–3 人日**：记录与检测 1d；扫描接线与呈现 1d；测试 0.5d。

### 8.6 依赖关系

无硬依赖；P0-1 后升级为 `compaction/start→summary→end` 正式事件（可选增强）。

### 8.7 风险

写放大（每压缩两条记录，压缩低频可接受）；与审计日志重复（审计=人类可读，jsonl=机器对账）。

---

## 9. P2-9 工具结果 spill 标准化

### 9.1 现状（源码定位）

- 超大输出以**截断为主**：阈值/截断逻辑散落 `internal/executor/agent.go`、`internal/model/deepseek.go` 等（128 级阈值出现于这些文件）；无统一存储与取回提示。
- 取回默认假设 read_file——对"命令输出/子代理报告/检索结果"不适用。
- dsh 参照：`packages/spill/`（`spill` + `spill-local` + `spill-policy`）：统一存储 + **locator + retrievalHint**（不假设唯一取回方式）+ 清理策略。

### 9.2 目标设计

```go
// internal/spill/store.go（新）
type Locator struct {
    ID     string `json:"id"`   // "spill-<hash8>"
    Path   string `json:"path"` // .contextloom/spill/<id>.txt
    Size   int64  `json:"size"`
    SHA256 string `json:"sha256"`
}
type RetrievalHint struct {
    Kind    string `json:"kind"`             // file | range | job | search
    Path    string `json:"path,omitempty"`   // file/range：read_file 取回
    Offset  int    `json:"offset,omitempty"`
    Limit   int    `json:"limit,omitempty"`
    JobID   string `json:"job_id,omitempty"` // job：job_output 取回
    Query   string `json:"query,omitempty"`  // search：search_content/harness_search
    Suggest string `json:"suggest,omitempty"`
}
func Put(ctx context.Context, name string, content []byte) (*Locator, error)
func Get(ctx context.Context, id string) ([]byte, error)
```

工具结果封装（统一出口）：

```json
{
  "spilled": true,
  "locator": {"id":"spill-a1b2c3d4","path":".contextloom/spill/spill-a1b2c3d4.txt","size":524288,"sha256":"..."},
  "retrieval_hint": {"kind":"file","path":"...","suggest":"read_file(offset/limit) 分段读取，或 search_content 检索"},
  "preview": "前 4KB 预览..."
}
```

- 阈值默认 128KB（可配 `tools.spill_threshold`）；超出 → Put + locator/hint + 前 4KB 预览；
- hint.kind 按来源自动选择：命令输出 → `job`/`file`；子代理报告 → `file`；检索结果 → `search`；
- 清理（对齐 spill-policy）：容量上限（如 64MB）+ TTL（如 7 天）+ 与 jobs 联动；启动巡检。

### 9.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/spill/`（新包） | Locator/RetrievalHint/Put/Get/清理 |
| `internal/executor/agent.go` | 截断点改为 spill 封装 |
| `internal/tools/exec.go` / `registry.go` | 结果封装统一出口 |
| `internal/model/deepseek.go` | 相关截断路径对接 |
| `internal/runtime/wiring.go` | 清理巡检接线 |
| `config.example.yaml` | 阈值/容量/TTL |

### 9.4 验收标准

1. 大输出自动落盘 + locator/hint（三种 kind 各有用例）；
2. 多取回路径可用（read_file/job_output/search）；
3. 清理不误删（容量/TTL 边界测试）；`.contextloom/` 已在忽略范围；
4. 小输出零行为变化。

### 9.5 工作量估算

**3–4 人日**：store 与策略 1.5d；封装接入 1d；清理 0.5d；测试 1d。

### 9.6 依赖关系

与 P1-4（jobs）协同；无 P0 依赖。

### 9.7 风险

模型对 hint 的理解（需提示词解释 locator 用法）；旧测试断言原始截断文本 → 需更新。

---

## 10. P2-10 文档生成校验门禁

### 10.1 现状（源码定位）

- **单一事实源已存在**：`internal/tools/registry.go` 的 `toolDescriptions` + `toolSchemas`；`registerBuiltins()` 注册内置工具。
- 文档现状：`contextloom/docs/` 与根目录多篇手写文档；**无生成、无校验**——工具清单人工维护，存在漂移。
- 基建：`contextloom/Makefile`、`.github/` 均存在。
- dsh 参照：tool-catalog 由脚本生成 + CI 校验"新工具不可漏文档"。

### 10.2 目标设计

```
contextloom docs gen     # 从 registry 生成 docs/tools.md（覆盖写）
contextloom docs check   # 生成到内存并与 docs/tools.md 比对；不一致 → 非零退出 + diff 摘要
```

- 生成物 `docs/tools.md`：头部声明"自动生成勿手改"；工具总表（工具/描述/风险等级/参数）+ 各工具 JSON Schema 明细（按名排序保证幂等）；
- 导出 API：`internal/tools` 增 `ToolsSnapshot() []ToolDoc`（name/desc/schema/risk/scope）；
- 门禁：Makefile `docs-gen`/`docs-check`；CI PR 跑 `docs-check`——新增工具未同步文档 → 构建失败。

### 10.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/tools/snapshot.go`（新） | ToolsSnapshot 导出 |
| `cmd/`（docs 子命令） | gen / check |
| `docs/tools.md`（新，生成物） | 首次生成入库 |
| `Makefile` | docs-gen / docs-check |
| `.github/workflows/` | CI 接入 |

### 10.4 验收标准

1. 新增/改工具未跑 gen → `docs-check` 失败并给 diff；
2. `docs-gen` 幂等（排序固定）；
3. 描述缺失 → 警告占位不崩溃；
4. CI 校验 < 10s。

### 10.5 工作量估算

**1.5–2 人日**：snapshot + gen 1d；check + Makefile + CI 0.5d；测试 0.5d。

### 10.6 依赖关系

无依赖；建议批次 1 先行——后续新增工具（lsp/job_*/run_workflow/exit_plan_mode）自动获文档覆盖。

### 10.7 风险

现有描述质量参差 → 生成时标注"待补充"；生成物与手写文档并存 → 头部声明"自动生成勿手改"。

---

## 11. P2-11 SandboxEnforcement 完整性报告（简述）

> 与 P0-3（OS 级沙箱后端）配套，详细设计见《contextloom改造详细设计-P0架构级.md》；此处仅定义输出面与消费约定。

### 11.1 现状（源码定位）

- `internal/sandbox/sandbox.go`：ShadowFS（写重定向镜像、读镜像优先、commit/discard）；`limits_linux.go`/`limits_other.go`：资源限制；`network.go`：代理级网络拒绝（`NetDenyEnv`）。
- **无完整性声明**：沙箱"保证什么/缺口在哪"（ShadowFS 防写错不防读泄漏等）无机器可读输出；`tools/autoapprove.go` 无法据此决策。
- dsh 参照：`SandboxEnforcement`（full/partial；Landlock 旧 ABI 与 Windows ACL 缺口显式声明）。

### 11.2 目标设计

```go
// internal/sandbox/report.go（新）
type EnforcementReport struct {
    Level      string   `json:"level"`      // full | partial
    Platform   string   `json:"platform"`   // linux | darwin | windows
    Mechanisms []string `json:"mechanisms"` // ["shadowfs","rlimit","netdeny"] / +["landlock"]
    Gaps       []string `json:"gaps"`       // ["no-read-isolation","no-kernel-fs"]
}
func Report() EnforcementReport // 按平台能力矩阵静态声明（保守）
```

- 消费：`exec_command` 结果附 `sandbox_report`（沙箱会话内）；`autoapprove.go` 在 `partial` 时收紧自动批准；TUI/ACP 首启呈现缺口。

### 11.3 改动点清单

| 文件 | 改动 |
|---|---|
| `internal/sandbox/report.go`（新） | 报告结构 + 平台矩阵 |
| `internal/tools/exec.go` | 结果附报告 |
| `internal/tools/autoapprove.go` | 按 level 决策 |
| `internal/tui/` / `internal/acp/` | partial 呈现 |

### 11.4 验收标准

1. 三平台返回正确 level 与缺口（单测表驱动）；
2. 未接 OS 级后端时显式 partial（不夸大）；接 Landlock（P0-3）后升级 full；
3. autoapprove 在 partial 下行为收紧（用例覆盖）。

### 11.5 工作量估算

**2 人日**（P0-3 落地后的增量；未动 P0-3 时可先交付静态矩阵）。

### 11.6 依赖关系

**硬依赖 P0-3**（full 能力来源；静态声明不阻塞）。

### 11.7 风险

平台差异测试成本；与 ShadowFS 语义衔接（"写隔离 ≠ 读隔离"如实声明）。

---

## 12. 实施路线图

### 12.1 依赖关系（DAG）

```
P2-6 (scope) ──► P1-2 (subagent) ──► P1-3 (workflow)；P2-6 ──► P1-5 (plan-mode)
P1-4 (jobs) ◄──协同──► P2-9 (spill)；P0-3 ──► P2-11 (enforcement)
独立项：P1-1 (LSP) · P2-7 (goal block) · P2-8 (compaction 锁) · P2-10 (文档门禁)
```

### 12.2 分批建议

| 批次 | 内容 | 理由 |
|---|---|---|
| 批次 1 | P2-6 · P2-10 · P2-8 | 低风险、高确定性；scope 是后续前置，门禁覆盖后续新工具 |
| 批次 2 | P1-1 · P1-4 · P2-7 | 能力缺口型，互相独立 |
| 批次 3 | P1-2 · P1-5 · P2-9 | 依赖批次 1 的 scope；开放子代理与规划模式 |
| 批次 4 | P1-3 · P2-11 | 编排层收口（依赖 P1-2）；沙箱报告随 P0-3 |

---

## 13. 交叉引用汇总

### 13.1 与 P0 文档的交叉点

| P0 项 | 关联本文档项 | 关系 |
|---|---|---|
| P0-1 会话事件溯源 | P2-8 / P2-7 / P1-5 | 可选增强（记录/状态升级为事件） |
| P0-2 Seam 能力抽象 | P1-1 / P1-2 / P1-3 | 接口风格对齐（不阻塞） |
| P0-3 OS 级沙箱 | P2-11 | **硬依赖**（full 能力来源） |

### 13.2 与对标文档第 5 节的映射

| 对标文档第 5 节条目 | 本文档章节 |
|---|---|
| P1-4 LSP 集成 | §1（P1-1） |
| P1-5 subagent provider 注册表 | §2（P1-2） |
| P1-6 workflow 脚本编排 | §3（P1-3） |
| P1-7 jobs 后台任务治理 | §4（P1-4） |
| P1-8 plan-mode 软引导 | §5（P1-5） |
| P2-9 scope 原语 | §6（P2-6） |
| P2-10 goal block 统一语义 | §7（P2-7） |
| P2-11 compaction 锁与孤儿检测 | §8（P2-8） |
| P2-12 工具结果 spill 标准化 | §9（P2-9） |
| P2-13 文档生成校验门禁 | §10（P2-10） |
| P2-14 SandboxEnforcement 完整性报告 | §11（P2-11） |

## 附录：新增接口速查

| 接口/结构体 | 所属包（新） | 用途 |
|---|---|---|
| `lsp.Provider` / `lsp.Session` | internal/lsp | 语言服务器接入与查询 |
| `subagent.Provider` / `Registry` / `Handle` | internal/subagent | 子代理多后端注册与执行 |
| `workflow.Spec` / `Engine` | internal/workflow | 编排 DSL 与执行引擎 |
| `jobs.Manager` / `Job` | internal/jobs | 后台任务治理 |
| `tools.Scope` / `Registry.WithScope` | internal/tools | per-agent 工具隔离 |
| `goalmgr.Block/Unblock` / `types.BlockReason` | internal/goalmgr / pkg/types | goal 阻塞态 |
| `ctxmgr.DetectOrphans` | internal/ctxmgr | 孤儿压缩检测 |
| `spill.Locator` / `RetrievalHint` | internal/spill | 大输出标准化存取 |
| `tools.ToolsSnapshot` | internal/tools | 文档生成事实源导出 |
| `sandbox.EnforcementReport` | internal/sandbox | 沙箱完整性声明 |

## 结语：关键结论摘要

1. 11 项均可在 ContextLoom 现有架构内以"接口 + 注册表 + 默认实现"增量落地，**无需新增依赖**（workflow 用 DSL 而非 JS 引擎是关键决策）。
2. 仅两条硬依赖链：**P2-6 → P1-2 → P1-3** 与 **P0-3 → P2-11**；其余 7 项独立可并行。
3. 总工作量约 **44–59 人日**，按 4 批次推进约 2–3 个月（单人）完成主体。
4. 底线三连：零依赖、默认行为不变、可独立交付——不损害单二进制交付优势与 token 节约资产。
5. 建议先启批次 1（P2-6 / P2-10 / P2-8）：低风险、确定性收益，并为后续批次扫清前置。
