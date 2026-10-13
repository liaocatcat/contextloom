# ContextLoom 改造详细设计（P0 架构级）

> **读者**：ContextLoom 核心开发者（runtime / executor / memory / sandbox / tools 维护者）。
> **目标**：把《业界前沿对标分析文档》第 5 节的 P0 三项展开为可直接排期实施的接口级设计。
> **范围**：①会话事件溯源化 ②Seam 能力抽象 ③OS 级沙箱（Landlock 优先）；P1/P2 见姊妹文档《contextloom改造详细设计-P1P2能力级.md》。
> **口径**：现状全部取自仓库实际源码（给出文件与函数名）；目标接口为完整 Go 定义；dsh 参照均注明对应包名。

## 0 总览

| 编号 | 改造项 | 一句话目标 | dsh 参照 |
|---|---|---|---|
| P0-1 | 会话事件溯源化 | 事件日志成为唯一真相源，模型可见历史一律从日志派生 | `packages/core/session`、`packages/session/session-persistence-jsonl`、`packages/compaction/compaction-basic` |
| P0-2 | Seam 能力抽象 | fs/shell/subagent/sandbox 四接口化，换实现不改调用方 | `docs/capability-seams.zh.md`、`packages/fs`、`packages/shell`、`packages/subagent`、`packages/sandbox` |
| P0-3 | OS 级沙箱后端 | Linux Landlock 内核级强制 + full/partial 完整性报告 | `packages/sandbox/sandbox-local`、`native/system`（landlock-run）、`docs/postmortem/0004` |

**术语**：**真相源**＝可唯一重建状态的数据（P0-1 后为 `sessions/<uuid>.jsonl`）；**surface**＝日志中产生模型可见消息的有序子序列；**seam**＝能力接口 + Provider + Consumer 三方拆分；**enforcement**＝`full`（管辖全部承诺效果）/`partial`（仅子集，调用方须可区分）。
**依赖与顺序**：P0-2 的 sandbox seam 是 P0-3 挂载点；P0-1 可并行。建议：P0-2 接口层 → P0-1 → P0-3。

---

## 1 P0-1 会话事件溯源化（Event-Sourced Session Log）

### 1.1 现状分析

**关键类型与位置**

- `pkg/types/types.go`：`Session`（UUID/Title/Preview/Project/时间戳/MessageCount/TokenUsage + `CompactedMessages []Message`、`CompactedSummary string`、`CompactedSeq int64`）；`SessionEvent{Seq int64; Timestamp; Type string; Data any}`，注释声明类型含 `tool_call/tool_result/error/status`，实际只写两种。
- `internal/memory/storage.go`：`Storage` 管 `sessions/<uuid>.json`（元数据+快照）与 `sessions/<uuid>.jsonl`（追加日志）；方法 `AppendEvent / LoadEvents / LoadEventsAfter / LastEventSeq / SaveSession / LoadSession / DeleteSession`。
- `internal/memory/memory.go`：`System` 加内存 map 与 `sync.RWMutex` 后转发。
- `internal/runtime/runtime.go`：写入 `AddToHistory / AddToHistoryForSession`；读取 `LoadSession / eventToMessage / GetDisplayHistory / DreamMemory`；压缩 `persistCompactionSnapshot / CompressContext / indexCompressedEvictedFor`。
- `internal/ctxmgr/budget.go`：`TokenBudget`（CompactRatio/HardRatio/SafetyRatio/KeepRecent/Summary/audit）、`CompactToBudgetWithEvict`、`ForceCompactWithEvict`、`MessagesTokens`。

**三条数据流**

1. **写入**：`ProcessInput → Agent.RunStream` 流式回调 → `AddToHistory`：`buildHistoryMessage` 构造内存 `types.Message`（assistant 用 `SplitReasoning` 拆思维链；user 可组装多模态）；追加 `conversationHistory` 后就地压缩；解锁后 `AppendEvent(sid, &SessionEvent{Seq: time.Now().UnixNano(), Type: "user_msg"|"assistant_msg", Data: eventText})` 追加 JSONL；若本轮压缩过，`persistCompactionSnapshot` 把整个 `conversationHistory` 写进 `session.json`，`CompactedSeq` 推进到日志末条 Seq。
2. **加载**（`LoadSession`）：快照存在 → 以 `CompactedMessages` 为基线，`LoadEventsAfter(uuid, CompactedSeq)` 取增量合并后重压缩、重写快照；不存在 → `LoadEvents` 全量重放 → 重压缩 → 注入滚动摘要。
3. **压缩**：仅在内存发生；动作进 `TokenBudget.audit`（`/ctx` 展示）；被丢弃原文经 `EvictFunc` 写入 Harness 检索索引（`.contextloom/harness/index.json`）。

**差距清单（对照 dsh）**

1. **模型可见 ≠ 已记录**：模型实际看到 system 提示词、assistant 的 `tool_calls`、tool 结果；日志只有 user/assistant 正文，重放与所见不一致。
2. **Seq 不连续**：`Seq` 是墙钟纳秒，加载需 `sort.Slice` 兜底，时钟回拨/并发有乱序风险；dsh 为 `seq = log.length`。
3. **双真相源**：`CompactedMessages` 是派生状态却独立落盘，`CompactedSeq` 合并逻辑特判多、历史缺陷多（源码注释记录"快照覆盖新对话""压缩后历史不能加载"等实锤）。
4. **压缩不可重放**：压缩动作无事件、摘要只在 json，换预算/版本重放结果不同。
5. **容错过度**：损坏行 `continue`、未知类型静默忽略；dsh 规则是"未知必需事件必须拒绝重建"。
6. **无边界与替换语义**：没有 turn/step 边界、没有 `surfaceOp=replace`、没有 fork/种子。
7. **消费方各自解析**：`eventToMessage` 只认两种类型；`DreamMemory` 直接断言 `ev.Data.(string)`；TUI/GUI 各写一套加载。

### 1.2 目标设计

**设计原则（五条）**

1. **唯一真相源**：`sessions/<uuid>.jsonl` 唯一权威；内存 `conversationHistory` 降级为"日志派生 + 缓存"，可随时重建。
2. **模型可见 = 已记录**：请求中每条消息（含 system、tool_calls、tool 结果）都能从日志派生；请求组装统一走 `sessionlog.Derive`，禁止旁路。
3. **历史从日志派生**：纯函数 `DeriveMessages(events)`；展示、检索、压缩、迁移统一调用。
4. **压缩动作事件化**：`compaction/start → compaction/summary → compaction/end` 落日志；`compaction/summary` 带 `surfaceOp=replace` 声明遮蔽范围。
5. **快照降级为投影**：`session.json` 的 `CompactedMessages` 只作过渡版读缓存（可关、可校验、可丢弃），不再作加载基线。

> 参照 dsh：`packages/core/session`（SessionEventMap / deriveMessages / SurfaceOp / 严格回放）；`packages/compaction/compaction-basic`（压缩事件）。

**事件词汇（新包 `internal/sessionlog`）**

```go
// internal/sessionlog/event.go
package sessionlog

import (
	"encoding/json"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// EventType 是会话事件类型（对应 dsh SessionEventMap 的键）。
// 未知类型且非 ignorable → 拒绝重建（严格回放）。
type EventType string

const (
	EventSystemMessage     EventType = "system/message"     // 渲染后的系统提示词（hash 变化才追加）
	EventUserMessage       EventType = "user/message"       // 用户输入/注入上下文（source 区分）
	EventAssistantMessage  EventType = "assistant/message"  // 一次模型回复（含思维链与 tool_calls）
	EventToolCall          EventType = "tool/call"          // 工具调用（仅日志，不进 surface）
	EventToolResult        EventType = "tool/result"        // 工具结果（surface，tool role 消息）
	EventTurnStart         EventType = "turn/start"         // 轮次边界（一次用户输入的处理循环）
	EventTurnEnd           EventType = "turn/end"
	EventStepStart         EventType = "step/start"         // 步边界（一次模型调用 + 其工具执行）
	EventStepEnd           EventType = "step/end"
	EventRequestHeader     EventType = "request/header"     // 请求信封快照（模型配置 + 工具 schema 摘要）
	EventCompactionStart   EventType = "compaction/start"
	EventCompactionSummary EventType = "compaction/summary" // surfaceOp=replace，声明遮蔽范围
	EventCompactionEnd     EventType = "compaction/end"
)

// IsSurface 报告该类型是否产生模型可见消息（对应 dsh SurfaceEventType）。
func (t EventType) IsSurface() bool {
	switch t {
	case EventSystemMessage, EventUserMessage, EventAssistantMessage, EventToolResult:
		return true
	}
	return false
}

// SurfaceOp 描述事件如何进入 surface（对应 dsh SurfaceOp）。
type SurfaceOp struct {
	Op       string `json:"op"`                  // "append" | "replace"
	StartSeq int64  `json:"start_seq,omitempty"` // replace：被遮蔽范围（含）
	EndSeq   int64  `json:"end_seq,omitempty"`   // replace：被遮蔽范围（含）
}

// Event 是一条日志记录（对应 dsh SessionEvent<T>）。
type Event struct {
	Seq       int64           `json:"seq"`                  // 连续位置：0 起
	Time      time.Time       `json:"time"`
	Type      EventType       `json:"type"`
	Data      json.RawMessage `json:"data"`
	Ignorable bool            `json:"ignorable,omitempty"`  // true=读取方可安全跳过
	SurfaceOp *SurfaceOp      `json:"surface_op,omitempty"`
	LegacySeq int64           `json:"legacy_seq,omitempty"` // v1 迁移保留的原 Seq（纳秒）
}

// ── 类型化 payload ──
type SystemMessageData struct {
	Content string `json:"content"`
	Hash    string `json:"hash"` // sha256(content)：hash 未变则不重复追加
}
type UserMessageData struct {
	Content string     `json:"content"`
	Source  string     `json:"source,omitempty"` // user | inject | goal | scene | cron
	Images  []ImageRef `json:"images,omitempty"` // 图片只存引用，不内联 base64
}
// ImageRef 图片引用：统一落盘为项目内路径（GUI 上传图先落盘再引用）。
type ImageRef struct {
	Path string `json:"path,omitempty"`
	Mime string `json:"mime,omitempty"`
}
type AssistantMessageData struct {
	Content     string           `json:"content"`
	Reasoning   string           `json:"reasoning_content,omitempty"` // thinking 模式必须原样回传
	ToolCalls   []types.ToolCall `json:"tool_calls,omitempty"`        // 与 API 请求一致
	Usage       *types.Usage     `json:"usage,omitempty"`
	Interrupted bool             `json:"interrupted,omitempty"` // 流式中断：已交付前缀也入日志
}
type ToolCallData struct {
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // 原样保留模型输出（不解析、不规范化）
}
type ToolResultData struct {
	CallID  string `json:"call_id"`
	Content string `json:"content"`
	IsError bool   `json:"is_error,omitempty"`
	Error   string `json:"error,omitempty"` // 失败身份（不进模型内容）
}
type RequestHeaderData struct {
	Model           string   `json:"model"`
	MaxContext      int      `json:"max_context"`
	ToolNames       []string `json:"tool_names"`
	ToolsHash       string   `json:"tools_hash"`  // 工具 schema 集合哈希
	SystemHash      string   `json:"system_hash"` // 与最近 system/message 对应
	ReasoningEffort string   `json:"reasoning_effort,omitempty"`
}
type CompactionSummaryData struct {
	Trigger      string          `json:"trigger"`               // auto | manual | load
	Summary      string          `json:"summary"`               // 滚动摘要文本（[历史摘要]）
	StartSeq     int64           `json:"start_seq"`             // 被替换 surface 范围（含）
	EndSeq       int64           `json:"end_seq"`               // 被替换 surface 范围（含）
	ShadowedSeqs []int64         `json:"shadowed_seqs"`         // 实际遮蔽的 surface 节点
	Replacement  []types.Message `json:"replacement,omitempty"` // 替换节点（截断版消息；可空=仅摘要）
}
```

```go
// internal/sessionlog/log.go
package sessionlog

import (
	"context"
	"errors"
	"sync"
)

// 稳定错误码：调用方按码分支，绝不解析消息文本。
var (
	ErrCorruptLine  = errors.New("sessionlog: 日志行损坏")        // 严格模式：拒绝重建
	ErrSeqGap       = errors.New("sessionlog: seq 不连续")
	ErrUnknownEvent = errors.New("sessionlog: 未知的必需事件类型") // ignorable 不触发
	ErrBadSurfaceOp = errors.New("sessionlog: surface 替换范围非法")
	ErrNotJSON      = errors.New("sessionlog: payload 不可序列化为 JSON")
)

// Log 是单个会话的追加日志（对应 dsh Session + JSONL 后端的最小合并视图）。
type Log struct {
	mu      sync.Mutex
	path    string // sessions/<uuid>.jsonl
	lastSeq int64
}

// Open 打开（不存在则延迟创建）会话日志；检测 v1 旧格式并记录迁移需求。
func Open(path string) (*Log, error)

// Append 追加一条事件：分配连续 seq（lastSeq+1）、校验 payload 可 JSON 序列化、
// 单次 write+fsync 落盘；surface 事件必须携带 SurfaceOp。返回落盘后的事件快照。
func (l *Log) Append(ctx context.Context, t EventType, data any, op *SurfaceOp) (*Event, error)

// Events 读取全量事件并执行严格校验（连续 seq、类型合法性、surface 规则）。
func (l *Log) Events(ctx context.Context) ([]Event, error)

// EventsAfter 读取 seq > after 的事件（增量加载/展示用），同样严格校验。
func (l *Log) EventsAfter(ctx context.Context, after int64) ([]Event, error)

// LastSeq 返回当前最后一条事件 seq（空日志返回 -1）。
func (l *Log) LastSeq() int64

// Derive 按 surface 折叠派生模型可见消息。
func (l *Log) Derive(ctx context.Context) ([]types.Message, error)
```

```go
// internal/sessionlog/derive.go
package sessionlog

// DerivedMessage 是派生消息与其来源事件 seq 的配对（压缩规划/测试断言用）。
type DerivedMessage struct {
	Seq     int64
	Message types.Message
}

// DeriveMessages 把事件日志投影为模型可见消息列表（纯函数，可离线单测）。
// 规则（对齐 dsh deriveMessages）：
//  1. surface 节点序 = 事件按 seq 应用 surfaceOp 的结果；
//  2. append → 尾部追加；replace{start,end} → 删除 surface 序中位于 [start,end]
//     的节点，在原位置插入新节点（被删节点记入 shadowed）；
//  3. 投影：user/message→user；assistant/message→assistant（含 ReasoningContent、
//     ToolCalls）；tool/result→tool（带 ToolCallID）；system/message→system；其余跳过；
//  4. 空 assistant（无正文/思维链/tool_calls）跳过（max_tokens 截断保护）；
//  5. 严格校验：未知必需类型→ErrUnknownEvent；seq 缺口→ErrSeqGap；
//     replace 端点不存在/遮蔽不完整→ErrBadSurfaceOp。
func DeriveMessages(events []Event) ([]types.Message, error)

// DeriveDetailed 返回带来源 seq 的派生结果（压缩规划与审计用）。
func DeriveDetailed(events []Event) ([]DerivedMessage, error)
```

**压缩事件化（ctxmgr 改造）**。现状 `CompactToBudgetWithEvict` 直接改写调用方 slice；目标拆为"规划→落日志→派生"：

```go
// internal/ctxmgr/plan.go（新增；旧函数保留作过渡包装）
package ctxmgr

// CompactionPlan 描述一次压缩对 surface 的精确影响（不直接改消息切片）。
type CompactionPlan struct {
	Trigger        string          // auto | manual | load
	Summary        string          // 新的滚动摘要（累积）
	ShadowedSeqs   []int64         // 被替换的 surface 节点（连续区间）
	Replacement    []types.Message // 替换节点（截断后的保留消息；[历史摘要] 由 runtime 统一生成）
	ReleasedTokens int             // 释放的估算 token（审计/状态栏）
}

// PlanCompaction 只计算、不落盘：输入派生消息（含来源 seq）与预算，
// 输出替换范围与替换节点。规则沿用现有五级渐进压缩，但把"就地改写"
// 改为"声明 surface 替换"。
func PlanCompaction(msgs []sessionlog.DerivedMessage, b *TokenBudget, trigger string) (*CompactionPlan, error)
```

runtime 侧动作（`CompressContext` / `AddToHistory` 压缩分支）：`PlanCompaction` 得到计划后，依次 `Append(EventCompactionStart)`、`Append(EventCompactionSummary, data, &SurfaceOp{Op:"replace", StartSeq:..., EndSeq:...})`、`Append(EventCompactionEnd)`；被遮蔽原文仍写 Harness 索引（`indexCompressedEvictedFor` 路径不变）。要点：摘要文本、替换范围、替换节点全部进事件，重放即重得同样结果；手动 `/compress` 与自动压缩同路径，仅 `Trigger` 不同。

**模型可见 = 已记录（强制路径）**：每轮请求组装改为 `system（最近 system/message） + sessionlog.Derive(events)`；`conversationHistory` 保留为派生缓存（`derivedCache + cacheSeq`，追加后失效重建）。system 提示词 hash 变化时追加 `system/message`；工具 schema/模型配置变化时追加 `request/header`（ToolsHash/SystemHash），保证"同一日志+同一配置=同一请求信封"可验证。

**严格回放与容错**：未知类型 `Ignorable==true` 跳过、否则 `ErrUnknownEvent`（拒绝重建）；损坏行默认 `ErrCorruptLine`，提供 `contextloom session repair <uuid>` 保留可解析前缀、损坏尾部截到 `.corrupt` 备份（对齐 dsh 撕裂尾部恢复）；加载时校验 seq 连续性，缺口 → `ErrSeqGap`（修复工具按行序重编号）。

### 1.3 数据/存储格式变更与向后兼容

**JSONL v2 格式**（首行固定 header，事件 seq 从 0 连续递增）：

```
sessions/<uuid>.jsonl
{"v":2,"kind":"header","uuid":"...","created_at":"2026-01-01T00:00:00Z","format":"contextloom-session/2"}
{"seq":0,"time":"...","type":"user/message","data":{"content":"你好","source":"user"},"surface_op":{"op":"append"}}
{"seq":1,"time":"...","type":"assistant/message","data":{"content":"...","reasoning_content":"...","tool_calls":[...]},"surface_op":{"op":"append"}}
{"seq":2,"time":"...","type":"tool/call","data":{"call_id":"call_1","name":"read_file","arguments":"{...}"}}
{"seq":3,"time":"...","type":"tool/result","data":{"call_id":"call_1","content":"..."},"surface_op":{"op":"append"}}
{"seq":4,"time":"...","type":"compaction/summary","data":{"trigger":"auto","summary":"[历史摘要] ...","start_seq":0,"end_seq":3,"shadowed_seqs":[0,1,3],"replacement":[...]},"surface_op":{"op":"replace","start_seq":0,"end_seq":3}}
```

`sessions/<uuid>.json` 保留为元数据文件（Title/Preview/Project/时间戳/TokenUsage），快照字段标记 Deprecated。

**v1 识别与升级**：首行非 header 且事件行含 `"seq"` 数字与 `"type":"user_msg"|"assistant_msg"` → 判 v1。只读加载：按行序重编号 `seq=0..N-1`（原 Seq 存 `legacy_seq`），`user_msg → user/message{source:"user"}`、`assistant_msg → assistant/message`，Data 为 string/map 均取 content，统一 `surface_op=append`。首次追加写时落盘升级：写 `.tmp` → fsync → rename，原文件备份 `<uuid>.jsonl.v1.bak`（`DeleteSession` 一并清理），幂等可重入。v1 文本中的 `[图片: 路径]` 标记在升级时解析为 `images:[{path}]`（复用 `vision.MessageFromEventText` 等价逻辑）。

**快照降级与双写过渡**：新读取路径对 v2 日志只走"严格回放+派生"，完全忽略 `CompactedMessages`（防双真相）。日志缺失但快照存在（极端旧数据）→ 从快照合成事件（compaction/summary + user/assistant，标 `synthesized:true`）并落盘，原 json 备份 `.json.legacy.bak`。双写过渡 1 个版本周期：仍把派生缓存写回 `CompactedMessages`（附 `SnapshotLogSeq` + `SnapshotHash`），供未升级旧二进制读取，配置 `session.snapshot_cache`（默认 true）可关；读取侧校验 `SnapshotLogSeq == LastSeq()` 才信任缓存，缓存不一致不再是缺陷来源。

### 1.4 改造点清单（逐文件）

| 文件 | 动作 | 内容 |
|---|---|---|
| `internal/sessionlog/event.go` | 新增 | 事件类型、payload、`SurfaceOp`、错误码 |
| `internal/sessionlog/log.go` | 新增 | `Log`：`Append`（连续 seq + JSON 校验 + fsync）、`Events/EventsAfter/LastSeq/Derive` |
| `internal/sessionlog/derive.go` | 新增 | `DeriveMessages/DeriveDetailed`（纯函数 + surface 折叠 + 严格校验） |
| `internal/sessionlog/migrate.go` | 新增 | v1 识别/内存升级/落盘升级/合成回填 |
| `internal/sessionlog/*_test.go` | 新增 | 严格回放、替换折叠、v1 升级、损坏行、seq 缺口 |
| `internal/ctxmgr/plan.go` | 新增 | `CompactionPlan`、`PlanCompaction`（只规划不改写） |
| `internal/ctxmgr/budget.go` | 修改 | 旧函数保留作过渡包装；`SummaryMessage` 复用 |
| `pkg/types/types.go` | 修改 | `SessionEvent` 标注 Deprecated（v1 兼容保留）；快照字段加 Deprecated 注释与 `SnapshotLogSeq/SnapshotHash` |
| `internal/memory/storage.go` | 修改 | `AppendEvent/LoadEvents/...` 委托 `sessionlog.Log`；`DeleteSession` 清理 `.v1.bak/.corrupt`；`SaveSession` 双写开关 |
| `internal/memory/memory.go` | 修改 | 转发新 API（`AppendTyped/Replay/Derive`）；旧方法签名兼容 |
| `internal/runtime/runtime.go` | 修改 | `AddToHistory` 拆为 `recordUserMessage/recordAssistantMessage/recordToolCall/recordToolResult/recordSystemMessage`；`conversationHistory` 改派生缓存；`LoadSession` 改严格回放；`CompressContext` 事件化；`GetDisplayHistory/DreamMemory` 走 `Derive` |
| `internal/executor/agent.go` | 修改 | 工具调用/结果经回调写回 runtime 记录（沿用 `checkpointSink` 注入模式，不直接依赖 sessionlog） |
| `internal/tui/*`、`internal/gui/server.go` | 修改 | 历史加载统一走 sessionlog 查询包装；展示行为不变 |
| `internal/acp`、`internal/mcpclient` | 修改 | 使用 `GetDisplayHistory` 的路径随接口适配 |
| `internal/runtime/*_test.go`、`internal/memory/*_test.go` | 修改 | 适配 v2；新增 1.6 断言 |

### 1.5 分步迁移路径（每步独立交付、可回滚）

1. **接口与模型落地（不接线）**：新增 `sessionlog` 包与单测，旧路径不动。回滚＝删包。
2. **双读兼容**：`Storage.LoadEvents` 内部识别 v1/v2 并只读升级；`AppendEvent` 仍写 v1 行。回滚＝还原 storage.go。
3. **双写切换**：新增写 v2 路径（`AppendTyped`），runtime 新增 `recordX` 记录 tool/system 事件，v1 行停写、快照双写保留。此步后"模型可见=已记录"成立（可跑 1.6 断言）。回滚＝切回 `AppendEvent`。
4. **加载切换**：`LoadSession` 改严格回放 + `Derive`，快照仅作缓存校验。回滚＝恢复快照基线路径（字段仍在）。
5. **压缩事件化**：压缩分支切到 `PlanCompaction` + compaction 事件；`/ctx` 审计改读事件。回滚＝恢复旧压缩调用。
6. **收尾**：移除旧 `SessionEvent` 写入与快照写入（`snapshot_cache:false` 默认），更新文档与 GUI 契约。

### 1.6 验收标准（可测试断言）与风险

**验收断言**

- `TestDerivedEqualsRequestMessages`：含工具调用的 mock 后端会话，捕获请求消息与 `DeriveMessages` 输出逐条相等（"模型可见=已记录"）。
- `TestV1LogUpcast`：旧 `.jsonl` 加载结果与旧加载器一致；首次追加后为 v2 且 `.v1.bak` 存在。
- `TestStrictReplay`：未知必需类型→`ErrUnknownEvent`；`ignorable:true` 未知类型→跳过重建成功；损坏行→`ErrCorruptLine`；`repair` 后保留前缀。
- `TestSeqContiguous`：append 序列无缺口；人为制造缺口→`ErrSeqGap`。
- `TestCompactionReplay`：压缩后重放得到相同摘要与保留消息；被遮蔽原文可在 Harness 索引检索；`/ctx` 占用与压缩后一致。
- `TestSnapshotCacheMismatchIgnored`：篡改 `SnapshotLogSeq` 后以日志为准、不信任缓存。
- `TestReloadCtxNoRegression`：压缩后重启 ctx 占比不回退（现有回归语义保持）。
- `TestDreamMemoryAndDisplayHistory`：两个消费者在 v2 数据上行为不变。

**风险**：迁移期混合格式 → 按文件判定 + 幂等升级 + `.v1.bak`；工具结果体积大 → 沿用压缩/截断与 Harness 索引（Scanner 上限保持 64MB）；严格回放性能 → `EventsAfter` 增量 + 派生缓存 + 展示分页；行为漂移 → 压缩语义逐条对齐现有测试，差异即缺陷。

---

## 2 P0-2 Seam 能力抽象（fs / shell / subagent / sandbox）

### 2.1 现状分析（硬编码点清单）

| 能力 | 现状实现 | 耦合点 |
|---|---|---|
| fs | `internal/tools/file.go`：`ReadFile/WriteFile/ReplaceInFile` 直接 `os.ReadFile/os.WriteFile/os.Stat/os.MkdirAll/os.Chmod`；`cleanPathIn` 做项目根锚定 + 沙箱镜像感知 | 工具函数即实现；沙箱经 `sandbox.FromContext` |
| shell | `internal/tools/exec.go`：`ExecCommand` 直接 `exec.CommandContext`；`shell()` 选 pwsh/powershell/cmd/sh；`envDenylist` 净化；`runWithReliableTimeout` + `exec_unix.go/exec_windows.go` 进程树终止；沙箱分支设镜像 Dir、`ApplyLimits`、`NetDenyEnv` | 同上 |
| subagent | `internal/tools/task.go`：`TaskSpawner` 接口（runtime 注入）+ singleflight；`spawn_task` 工具 | 已有接口雏形，但无统一 Bundle / 具名 provider |
| sandbox | `internal/sandbox`：`ShadowFS`（镜像重定向）、`ApplyLimits`（rlimit）、`NetDenyEnv`（代理死端口）；`internal/plugin/sandbox.go` 另有脚本沙箱 | 经 context 传递；无接口、无完整性报告 |
| 消费方 | `internal/executor/agent.go` 持有具体 `*tools.Registry`、`*harness.Engine`；runtime 直接 new 具体实现 | 换实现需改调用方 |

结论：fs/shell 完全硬编码；subagent 有雏形未统一；sandbox 是 context 魔法值——均未达 dsh "Service Definition / Provider / Consumer 三方拆分"。

### 2.2 目标设计（新包 `internal/seam`）

> 参照 dsh：`docs/capability-seams.zh.md`；`ctx.fs`（packages/fs）、`ctx.shell`（packages/shell）、`ctx.subagents`（packages/subagent）、`ctx.sandbox`（packages/sandbox）。

```go
// internal/seam/seam.go
package seam

import "context"

// Bundle 聚合四类能力 seam 的当前 Provider 组合。
// 消费方（tools/executor/runtime）只依赖 Bundle，不依赖具体实现包。
type Bundle struct {
	FS       FS
	Shell    Shell
	Subagent Subagent
	Sandbox  Sandbox
}

type ctxKey struct{}

// WithBundle 注入能力组合（runtime 每轮请求注入；测试可注入假实现）。
func WithBundle(ctx context.Context, b *Bundle) context.Context {
	return context.WithValue(ctx, ctxKey{}, b)
}

// FromContext 取能力组合（无则返回 nil，调用方回退包级默认）。
func FromContext(ctx context.Context) *Bundle
```

```go
// internal/seam/fs.go
package seam

import "context"

// FS 是文件系统能力 seam（对应 dsh ctx.fs）。
type FS interface {
	ReadFile(ctx context.Context, req ReadFileRequest) (ReadFileResult, error)
	WriteFile(ctx context.Context, req WriteFileRequest) (WriteFileResult, error)
	ReplaceInFile(ctx context.Context, req ReplaceFileRequest) (ReplaceFileResult, error)
	ListDir(ctx context.Context, req ListDirRequest) (ListDirResult, error)
	SearchContent(ctx context.Context, req SearchRequest) (SearchResult, error)
}
type ReadFileRequest struct {
	Path          string
	Offset, Limit int // Limit<=0 表示到文件尾
}
type ReadFileResult struct {
	Content      string
	TotalLines   int
	ResolvedPath string // 绝对路径：模型/用户可核对落盘位置
}
type WriteFileRequest struct {
	Path    string
	Content string
}
type WriteFileResult struct {
	Written      int
	ResolvedPath string
}
type ReplaceFileRequest struct {
	Path, OldStr, NewStr string
}
type ReplaceFileResult struct {
	Replaced     bool
	Size         int
	ResolvedPath string
}
type ListDirRequest struct{ Path string }
type ListDirResult struct{ Entries []DirEntry }
type DirEntry struct {
	Name  string
	IsDir bool
	Size  int64
}
type SearchRequest struct {
	Path, Query string
	MaxResults  int
}
type SearchResult struct{ Matches []SearchMatch }
type SearchMatch struct {
	Path string
	Line int
	Text string
}

// ErrorCode 稳定错误码（调用方按码分支，绝不解析文本；对齐 dsh FsError）。
type ErrorCode string

const (
	CodeNotFound  ErrorCode = "FS_NOT_FOUND"
	CodeNotText   ErrorCode = "FS_NOT_TEXT"
	CodeTooLarge  ErrorCode = "FS_TOO_LARGE"
	CodeDenied    ErrorCode = "FS_DENIED"
	CodeAmbiguous ErrorCode = "FS_AMBIGUOUS_EDIT"
)

type Error struct {
	Code ErrorCode
	Op   string
	Path string
	Msg  string
}

func (e *Error) Error() string { return string(e.Code) + " " + e.Op + " " + e.Path + ": " + e.Msg }
```

```go
// internal/seam/shell.go
package seam

import (
	"context"
	"time"
)

// Shell 是命令执行能力 seam（对应 dsh ctx.shell）。
// 同一入口同时服务前台执行与（未来）后台句柄；P0 仅落地前台。
type Shell interface {
	Run(ctx context.Context, spec ShellSpec) (ShellResult, error)
}
type ShellSpec struct {
	Command   string        // 命令原文（交 shell -c / -Command）
	Timeout   time.Duration // 0=默认 20s，上限 5min（沿用现状）
	Dir       string        // 空=项目根（由 Provider 解析）
	Env       []string      // 附加环境变量（叠加在净化后的宿主环境上）
	ShellKind string        // ""=自动（Windows pwsh→powershell→cmd / POSIX sh）
}
type ShellResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Denied   bool // 沙箱拒绝（区别于普通非零退出）
	Duration time.Duration
	Sandbox  SandboxFacts
}

// SandboxFacts 是本次执行携带的沙箱事实（对应 dsh ConfinedArgv 报告字段）。
type SandboxFacts struct {
	Mechanism   string // "landlock" | "shadowfs" | "none"
	Enforcement string // "full" | "partial"
	Denied      bool
}
```

```go
// internal/seam/subagent.go
package seam

import "context"

// Subagent 是子智能体委派能力 seam（对应 dsh ctx.subagents）。
type Subagent interface {
	Spawn(ctx context.Context, req SubagentRequest) (SubagentResult, error)
}
type SubagentRequest struct {
	ParentID  string
	Directive string
	Task      string
	Context   string
	Depth     int
	Tools     []string // 工具白名单（nil=继承默认）
}
type SubagentResult struct {
	ChildID string
	Output  string
}
```

```go
// internal/seam/sandbox.go
package seam

import "context"

// SandboxMode 仅管控文件系统效果（对应 dsh SandboxMode）。
type SandboxMode string

const (
	ModeReadOnly       SandboxMode = "read-only"
	ModeWorkspaceWrite SandboxMode = "workspace-write"
	ModeDangerFull     SandboxMode = "danger-full-access"
)

// SandboxPolicy 是逐次调用解析出的完整策略（对应 dsh SandboxPolicy）。
type SandboxPolicy struct {
	Mode          SandboxMode
	WorkspaceRoot string
	WritableRoots []string
	ReadOnlyRoots []string
	AllowNetwork  bool
}

// Enforcement 是后端报告的强制执行完整性（对应 dsh SandboxEnforcement）。
type Enforcement string

const (
	EnforcementFull    Enforcement = "full"
	EnforcementPartial Enforcement = "partial"
)

// ConfinedArgv 是 Confine 的结果：消费方 spawn 这个 argv 而不是自己的。
// 携带分类方言（拒绝签名 / runner 失败规则），让消费方能区分
// "沙箱基础设施故障"与"策略拒绝"（对齐 dsh 与 postmortem 0004 教训）。
type ConfinedArgv struct {
	Argv                    []string
	Mechanism               string
	Enforcement             Enforcement
	DenialSignatures        []string // 例：["permission denied"]
	RunnerFailureSignatures []string // 例：["contextloom-sandbox-run: "]
	RunnerFailureExitCodes  []int    // 例：[125]
	InformationalLines      []string // 逐行精确排除：例 partial 通知行
}

// Sandbox 是进程沙箱能力 seam（对应 dsh ctx.sandbox）。
// 约束：confine 必须返回"强制执行"的 argv 或明确失败；禁止静默无隔离透传。
type Sandbox interface {
	Confine(ctx context.Context, argv []string, policy SandboxPolicy) (ConfinedArgv, error)
	Capabilities(ctx context.Context) (SandboxCapabilities, error)
}
type SandboxCapabilities struct {
	Mechanism   string
	Enforcement Enforcement
	Notes       []string
}
```

### 2.3 默认 Provider 与消费方改造

- **默认 Provider（行为与现状逐字对齐）**：`internal/seam/localfs`（迁移 `tools/file.go`：项目根锚定、Windows 只读预检、沙箱镜像重定向经注入的 `PathMapper`）；`internal/seam/localshell`（迁移 `tools/exec.go`：shell 选择、envDenylist、进程树终止、沙箱分支；预留 `Confine` 调用点供 P0-3）；`internal/seam/localsubagent`（适配 `runtime.SpawnTask`，保留 singleflight）；`internal/seam/shadowfs`（`ShadowFS + NetDenyEnv + ApplyLimits` 组合为 `Sandbox` 实现，`Capabilities()` 返回 `{Mechanism:"shadowfs", Enforcement:"partial", Notes:["路径重定向+代理拒绝，非内核强制"]}`）。
- **消费方**：`tools/file.go|exec.go|task.go` 改为"从 ctx 取 Bundle → 调 seam → 组装结果"；`tools.Registry` 增加 `SetSeams(*seam.Bundle)` 与 ctx 覆盖（`seam.WithBundle`）。
- **装配**：`runtime.New` 构造 `seam.Bundle{localfs, localshell, localsubagent, shadowfs}`；`launchAgent` 在 ctx 注入（与 `sbox.WithSandbox` 同位置）。

### 2.4 数据/接口兼容

不改变工具名、参数 schema 与返回 JSON——模型侧零感知；`tools.TaskSpawner` 以适配器方式桥接 `seam.Subagent`（旧测试零改动）；P0-2 无新增配置（sandbox 配置沿用 `sandbox.*`，P0-3 扩展）。

### 2.5 改造点清单（逐文件）

| 文件 | 动作 | 内容 |
|---|---|---|
| `internal/seam/seam.go` | 新增 | `Bundle`、ctx 注入/读取 |
| `internal/seam/fs.go / shell.go / subagent.go / sandbox.go` | 新增 | 四接口 + 请求/结果/策略/错误码 |
| `internal/seam/localfs/fs.go` | 新增 | 迁移 `tools/file.go` 实现（含影子重定向钩子接口） |
| `internal/seam/localshell/shell.go` | 新增 | 迁移 `tools/exec.go` 实现（含进程树终止调用） |
| `internal/seam/localsubagent/subagent.go` | 新增 | 适配 `runtime.SpawnTask` + singleflight |
| `internal/seam/shadowfs/sandbox.go` | 新增 | `ShadowFS+NetDenyEnv+ApplyLimits` 的 Sandbox 实现 |
| `internal/tools/file.go` | 修改 | 函数体改调 `seam.FS`；删除直接 `os.*` 写路径（保留 `os.ErrInvalid` 语义） |
| `internal/tools/exec.go` | 修改 | 函数体改调 `seam.Shell`；`shell()/envDenylist/runWithReliableTimeout` 迁出 |
| `internal/tools/task.go` | 修改 | `TaskSpawner` 改为适配 `seam.Subagent` |
| `internal/tools/registry.go` | 修改 | 新增 `SetSeams/Seams`；`Execute` 流程不变 |
| `internal/tools/exec_unix.go、exec_windows.go` | 移动 | 迁入 `internal/seam/localshell`（build tag 保持） |
| `internal/runtime/runtime.go` | 修改 | 构造默认 Bundle；`launchAgent` 注入 ctx |
| `internal/seam/*_test.go` | 新增 | 接口一致性（`var _ FS = (*localfs.FS)(nil)`）、假实现注入、静态检查 |

### 2.6 分步迁移路径

1. **接口落地**：新增 `seam` 包（纯接口+类型），零接线。回滚＝删包。
2. **Provider 迁移**：`file.go/exec.go` 逻辑逐字搬进 `localfs/localshell`，原函数改薄转发；全部现有测试必须原样通过。回滚＝还原两文件。
3. **ctx 注入**：runtime 构造 Bundle 并注入；tools 优先 ctx、回退包级默认。回滚＝保留包级默认即可。
4. **sandbox 接口化**：`shadowfs` provider 包装现沙箱；`localshell` 经 `Confine` 拿 argv（此时恒返回"不包装"的 partial 事实，行为不变）。回滚＝恢复直读 `sandbox.FromContext`。
5. **subagent 接口化**：`localsubagent` 适配层替换 `TaskSpawner` 直连。回滚＝适配器保留旧接口。
6. **换实现演练**：以 P0-3 的 Landlock provider 做一次真实替换，验证"换实现不改调用方"。

### 2.7 验收标准与风险

**验收断言**：`TestSeamInterfacesConformance`（四 provider 编译期满足接口）；`TestToolsBehaviorUnchanged`（tools 全部现有测试零修改通过）；`TestFakeFSInjection`（注入假 FS，`write_file` 不触碰真实磁盘）；`TestNoDirectOSExecInTools`（静态检查 tools 除适配文件外无 `os.WriteFile`/`exec.Command`）；`TestSubagentAdapterParity`（经 seam 的 spawn 与旧行为一致：singleflight、超时豁免）。

**风险**：迁移期双路径（ctx 与包级默认）可能测试串扰 → 测试显式注入、包级默认仅 runtime 设置一次；`exec_*.go` 搬迁涉 build tag → 纯移动不改逻辑 + CI 双平台编译；接口过度设计 → 坚持"先对齐现状再扩展"（P0 不加后台句柄/注册表）。

---

## 3 P0-3 OS 级沙箱后端（Landlock 优先）

### 3.1 现状分析

- `internal/sandbox/sandbox.go`：仅 `ShadowFS`（写走镜像、读镜像优先、`Merge` 回写）＋ ctx 注入——纯用户态路径层，**非内核强制**。
- `internal/sandbox/limits_linux.go`：`ApplyLimits` 给子进程设 `RLIMIT_AS=4GB / NOFILE=256 / CPU=300s`；非 Linux 为 no-op（`limits_other.go`）。
- `internal/sandbox/network.go`：`NetDenyEnv` 把 6 个代理变量指向死端口 `http://127.0.0.1:9` 并删 `NO_PROXY`——只对遵守代理规范的程序生效，非内核级隔离。
- `internal/tools/exec.go`：仅 `sandbox.FromContext(ctx)!=nil` 时应用上述措施。
- 缺口：无内核级文件系统强制；无 enforcement 报告；无 fail-closed；拒绝与普通失败不可区分。

> 参照 dsh：`packages/sandbox/sandbox-local`（runner 链、`enforcement: full|partial`、`denialSignatures`/`runnerFailureRules`）；`native/system`（`landlock-run` CLI 约定：`--ro/--rw/--probe`、退出码 125、`no_new_privs`、ruleset 跨 execve 继承）；`docs/postmortem/0004`（信息行与致命行共享前缀的误分类教训）。

### 3.2 目标设计

**总体结构**

```
执行路径（Linux）：
  tools/exec_command → seam.Shell(localshell)
    → sandbox.WrapArgv(policy)                     // internal/sandbox
    → argv = [contextloom, __sandbox-run, --ro ..., --rw ..., --, sh, -c, cmd]
    → exec.Command(argv...)                        // 子进程是"我们自己的二进制"
       └─ __sandbox-run：prctl(no_new_privs) → landlock_restrict_self
          → syscall.Exec(真实命令)                  // 约束随 execve 继承给全部后代
```

要点（对齐 dsh 并适配 Go 单二进制）：**Launcher 复用自身二进制**（dsh 用独立预编译 `landlock-run`；ContextLoom 走隐藏子命令 `__sandbox-run`，`os.Executable()` 路径，零外部依赖、零额外分发物；子命令挂在 `main` 最前，避开 GUI/配置初始化）；**两阶段强制**（Landlock 对当前进程不可撤销，绝不在主进程内 `restrict_self`；由 launcher 自限制后 `execve`，规则集随 exec 继承）；**允许列表语义**（`--ro` 授予读+执行、`--rw` 授予该 ABI 能管辖的全部访问，未授予一律拒绝）；**fail-closed**（探针不可用/内核不支持时返回错误而非静默透传；`require_full=true` 时 partial 也拒绝）。

**syscall 层（`internal/sandbox/landlock_linux.go`）**

```go
//go:build linux

package sandbox

import (
	"errors"
	"os"
	"syscall"
	"unsafe"
)

// Landlock 系统调用号（内核 5.13+ 通用表 444-446；x86_64/arm64 一致，CI 双架构验证）。
const (
	sysLandlockCreateRuleset = 444
	sysLandlockAddRule       = 445
	sysLandlockRestrictSelf  = 446
)

// 权限位（按 ABI 演进；对应内核 uapi/linux/landlock.h）。
const (
	accessExecute    uint64 = 1 << 0
	accessWriteFile  uint64 = 1 << 1
	accessReadFile   uint64 = 1 << 2
	accessReadDir    uint64 = 1 << 3
	accessRemoveDir  uint64 = 1 << 4
	accessRemoveFile uint64 = 1 << 5
	accessMakeChar   uint64 = 1 << 6
	accessMakeDir    uint64 = 1 << 7
	accessMakeReg    uint64 = 1 << 8
	accessMakeSock   uint64 = 1 << 9
	accessMakeFifo   uint64 = 1 << 10
	accessMakeBlock  uint64 = 1 << 11
	accessMakeSym    uint64 = 1 << 12
	accessRefer      uint64 = 1 << 13 // ABI >= 2
	accessTruncate   uint64 = 1 << 14 // ABI >= 3
	accessIoctlDev   uint64 = 1 << 15 // ABI >= 5
)

const (
	landlockCreateRulesetVersion = 1 // LANDLOCK_CREATE_RULESET_VERSION
	rulesetRulePathBeneath       = 1 // LANDLOCK_RULE_PATH_BENEATH
)

type rulesetAttr struct {
	handledAccessFS uint64
}
type pathBeneathAttr struct {
	allowedAccess uint64
	parentFD      int32
	_             int32 // 对齐填充
}

// landlockABIVersion 查询内核 ABI（landlock_create_ruleset(NULL, 0, VERSION)）；
// EOPNOTSUPP/ENOSYS 表示不可用。
func landlockABIVersion() (int, error) {
	r0, _, errno := syscall.Syscall(sysLandlockCreateRuleset, 0, 0, landlockCreateRulesetVersion)
	if errno != 0 {
		return 0, errno
	}
	return int(r0), nil
}

// handledAccess 返回当前 ABI 可管辖的完整权限集合；
// ABI<2 无 REFER、ABI<3 无 TRUNCATE —— 缺位即 partial 的来源。
func handledAccess(abi int) uint64 {
	h := accessExecute | accessWriteFile | accessReadFile | accessReadDir |
		accessRemoveDir | accessRemoveFile | accessMakeChar | accessMakeDir |
		accessMakeReg | accessMakeSock | accessMakeFifo | accessMakeBlock | accessMakeSym
	if abi >= 2 {
		h |= accessRefer
	}
	if abi >= 3 {
		h |= accessTruncate
	}
	return h
}

// Grant 是一次授权：路径 + 只读标记（ro=读/执行，rw=全部可管辖位）。
type Grant struct {
	Path     string
	ReadOnly bool
}

// ApplyLandlock 在当前进程上安装规则集并自限制（仅可在 launcher 子进程中调用）：
// 1) prctl(PR_SET_NO_NEW_PRIVS, 1)；2) create_ruleset(handled)；
// 3) 对每个 Grant：open(O_PATH|O_CLOEXEC) + add_rule(PATH_BENEATH, allowed)
//    （非目录路径按文件兼容位处理，对齐 dsh --rw /dev/null 语义）；4) restrict_self()。
// 返回实际生效的 ABI 与是否 partial。
func ApplyLandlock(grants []Grant) (abi int, partial bool, err error)

var errLandlockUnavailable = errors.New("landlock: 内核不支持")
```

**Launcher 子命令（`internal/sandbox/runner.go`）**——CLI 约定逐条对齐 dsh `native/system` 的 landlock-run（见其 `docs/cli-contract.md`）：

```go
// internal/sandbox/runner.go
package sandbox

// CLI 约定（对齐 dsh landlock-run）：
//   contextloom __sandbox-run [--ro <path>]... [--rw <path>]... -- <argv>...
//   contextloom __sandbox-probe
// 退出码 125 = launcher 级失败（未执行命令）；执行成功后原样透传子进程状态。
// 致命诊断：stderr 一行 "contextloom-sandbox-run: ..."；
// partial 通知行（信息行，非致命）：
//   "contextloom-sandbox-run: partial enforcement (older Landlock ABI)"
const (
	LauncherFailureExit = 125
	launcherPrefix      = "contextloom-sandbox-run: "
	partialNoticeLine   = "contextloom-sandbox-run: partial enforcement (older Landlock ABI)"
)

// RunLauncher 实现 __sandbox-run 子命令；返回值即进程退出码。
func RunLauncher(args []string) int {
	grants, argv, err := parseLauncherArgs(args)
	if err != nil {
		launcherFatal(err) // stderr 一行 launcherPrefix + exit 125
	}
	_, partial, err := ApplyLandlock(grants)
	if err != nil {
		launcherFatal(err)
	}
	if partial {
		// 信息行：先打印再继续执行（postmortem 0004：消费方必须精确排除本行）
		_, _ = os.Stderr.WriteString(partialNoticeLine + "\n")
	}
	if err := syscall.Exec(argv[0], argv, os.Environ()); err != nil {
		launcherFatal(err)
	}
	return LauncherFailureExit // 不可达
}

// RunProbe 实现 __sandbox-probe：内核可强制（full 或 partial）→ 退出 0 并打印
// "landlock: fully enforced" 或 "landlock: partially enforced (older ABI)"；
// 否则退出 125。
func RunProbe() int
```

**探针、策略与包装**

```go
// internal/sandbox/probe.go
package sandbox

// Status 汇总当前生效的沙箱后端事实（供 /sandbox、GUI 与 seam.Sandbox 报告）。
type Status struct {
	Mechanism   string // "landlock" | "shadowfs" | "none"
	Enforcement string // "full" | "partial"
	ABI         int
	Notes       []string
}

// ProbeLandlock 探测内核 Landlock 可用性并缓存（进程生命周期内）：
// 1) landlockABIVersion() 查询 ABI；
// 2) 以 os.Executable() 执行 __sandbox-probe 子进程做功能验证
//    （防容器 seccomp 允许查询但拒绝 restrict_self 的场景）；
// 3) ABI>=3 → full；1..2 → partial（TRUNCATE/REFER 不受管辖）；不可用 → shadowfs。
func ProbeLandlock() Status
```

```go
// internal/sandbox/policy.go
package sandbox

// Policy 是内部策略（由 seam.SandboxPolicy 映射而来）。
type Policy struct {
	ReadOnlyRoots []string
	WritableRoots []string
	AllowNetwork  bool
}

// Resolve 把 seam.SandboxPolicy 解析为授权列表：
//  - read-only：--ro 宿主根（默认 "/"）+ --rw 临时区（/tmp）与 /dev/null；
//  - workspace-write：追加 --rw 工作区根与配置的附加可写根；
//  - danger-full-access：不包装（消费方直接 spawn）。
func Resolve(p seam.SandboxPolicy) ([]Grant, error)
```

```go
// internal/sandbox/enforce.go
package sandbox

// WrapArgv 按策略包装 argv（对齐 dsh ctx.sandbox.confine 语义）：
//  1. 探测后端；不可用 → 返回 ErrSandboxUnavailable（fail-closed，不静默透传）；
//  2. 构造 [exe, "__sandbox-run", grants..., "--", argv...]；
//  3. 返回分类方言：denialSignatures=["permission denied"]（Landlock EACCES）；
//     runnerFailureSignatures=[launcherPrefix]、RunnerFailureExitCodes=[125]、
//     InformationalLines=[partialNoticeLine]。
func WrapArgv(exe string, argv []string, p Policy) (seam.ConfinedArgv, error)
```

**执行接入与结果分类（localshell）**：`Shell.Run` 构造 argv 后，非 `danger-full-access` 模式经 `WrapArgv` 包装（失败即拒绝执行，fail-closed）；结果分类规则（对齐 postmortem 0004）——① 先按 `InformationalLines` 逐行精确排除（partial 通知不算失败证据）；② 退出码 ∈ `RunnerFailureExitCodes` 且 stderr 命中 `RunnerFailureSignatures` → runner 失败（沙箱基础设施故障，命令未运行）；③ stderr 命中 `DenialSignatures` → `Denied=true`（策略拒绝，属正常结果）；④ 其余非零退出 → 普通结果。

### 3.3 强制执行报告与 fail-closed

- `Status.Enforcement`：ABI≥3 → `full`；ABI 1-2 → `partial`（Notes 注明"TRUNCATE 不受管辖"）；Windows/macOS/容器内不可用 → 回退 `shadowfs`（`partial`，Notes 注明"路径重定向+代理拒绝，非内核强制"）。
- `sandbox.require_full=true` 时，`localshell` 在 enforcement!=full 且模式受限时拒绝执行，错误码 `SANDBOX_UNAVAILABLE`（对齐 dsh fail-closed）。
- 状态出口：`Runtime.SandboxStatus()`、`/sandbox` 命令、GUI 设置面板统一读 `sandbox.ProbeLandlock()`；文案区分"内核强制 / 应用层近似"。

### 3.4 配置与格式变更

```yaml
sandbox:
  enabled: false
  discard_on_exit: true
  backend: "auto"            # 新增: auto | landlock | shadowfs | off
                             #   auto=Linux 探针可用用 landlock，否则 shadowfs
  require_full: false        # 新增: true=仅接受 full 强制执行（partial/不可用→拒绝执行）
  workspace_write_roots: []  # 新增: 附加可写根（默认仅项目根）
  read_only_roots: ["/"]     # 新增: 只读授予根（默认整个宿主根）
```

无落盘格式变更（沙箱是运行时行为）；每次受限执行在 `trace` 追加 `sandbox.mechanism/enforcement/denied` 字段（追加字段，向后兼容）。

### 3.5 改造点清单（逐文件）

| 文件 | 动作 | 内容 |
|---|---|---|
| `internal/sandbox/landlock_linux.go` | 新增 | syscall 封装、ABI 查询、权限位、`ApplyLandlock` |
| `internal/sandbox/landlock_other.go` | 新增 | 非 Linux：`errLandlockUnavailable` 占位 |
| `internal/sandbox/runner.go` | 新增 | `__sandbox-run` / `__sandbox-probe`（CLI 约定对齐 dsh） |
| `internal/sandbox/probe.go` | 新增 | `Status`、`ProbeLandlock`（子进程功能验证 + 缓存） |
| `internal/sandbox/policy.go` | 新增 | `Policy`、`Resolve`（模式→授权根映射） |
| `internal/sandbox/enforce.go` | 新增 | `WrapArgv`、`ErrSandboxUnavailable`、分类方言 |
| `internal/sandbox/sandbox.go` | 修改 | 新增 `Status()` 汇总；`ShadowFS` 不变 |
| `internal/sandbox/network.go` | 修改 | 注释明确"应用层纵深防御"；landlock 可用时可配置关闭 |
| `cmd/contextloom/main.go` | 修改 | 最前部分发隐藏子命令（任何 GUI/初始化之前） |
| `internal/config/config.go` | 修改 | `sandbox.backend/require_full/workspace_write_roots/read_only_roots` + 默认值 |
| `config.example.yaml` | 修改 | 新配置项说明 |
| `internal/seam/localshell/shell.go` | 修改 | 接入 `WrapArgv` + 分类规则（denial vs runner failure） |
| `internal/seam/shadowfs/sandbox.go` | 修改 | `Capabilities()` 读 `ProbeLandlock`，报告真实机制/完整性 |
| `internal/runtime/runtime.go` | 修改 | `SandboxStatus` 输出 enforcement；`require_full` fail-closed 提示 |
| `internal/sandbox/landlock_*_test.go`、`*_e2e_test.go` | 新增 | 探针/包装/分类/e2e（内核不可用自动跳过） |

### 3.6 分步迁移路径

1. **探针与状态**：`ProbeLandlock` + `Status` + `/sandbox` 展示；无执行行为变化。回滚＝删文件。
2. **Launcher 落地**：`__sandbox-run/__sandbox-probe` 子命令 + `ApplyLandlock`；独立可测。回滚＝子命令不可达即死代码。
3. **灰度接入**：`localshell` 在 `backend=landlock` 时包装 argv；默认仍 `shadowfs`。回滚＝改配置。
4. **默认 auto**：Linux 探针可用 → landlock；不可用 → shadowfs（status 如实报告 partial）。
5. **fail-closed 开关**：`require_full` 生效 + 错误码；配套 GUI 提示。
6. **分类加固**：按 postmortem 0004 场景补分类测试（信息行+子进程非零、125+致命行、拒绝签名等）。

### 3.7 验收标准与风险

**验收断言**

- `TestLandlockProbe`：内核支持时 probe 退出 0 且输出 full/partial 之一；不支持时 125（CI 按内核能力跳过）。
- `TestReadOnlyDeniesWrite`（e2e，Linux）：`read-only` 下写工作区文件失败、读允许文件成功；stderr 命中拒绝签名 → `Denied=true`。
- `TestWorkspaceWriteScope`（e2e）：工作区根内写成功；根外写失败。
- `TestPartialNoticeNotRunnerFailure`：构造"partial 信息行 + 子进程退出 1"→ 分类为普通结果（不误报沙箱故障）；"退出 125 + 致命行"→ runner 失败（postmortem 0004 回归）。
- `TestRequireFullFailsClosed`：partial 环境下 `require_full=true` → 受限执行被拒且错误码 `SANDBOX_UNAVAILABLE`。
- `TestStatusReported`：`SandboxStatus()` 与 `/sandbox` 含 mechanism/enforcement/ABI；shadowfs 回退时注明非内核强制。
- `TestNoSilentPassthrough`：`WrapArgv` 探针不可用时返回错误（断言不存在"未包装 argv"返回路径）。

**风险**

- **内核覆盖**：Landlock 需 5.13+（ABI3 需 6.2+）；容器 seccomp 可能拦截 landlock 系统调用 → 功能验证子进程兜底；低版本一律 partial/回退并如实报告。
- **Go 进程内工具不受 Landlock 约束**：`write_file` 等进程内文件写无法被限制（同进程不可自我限制）；P0 保留 ShadowFS 作为工具层近似（partial），"fs 操作子进程化"超出 P0 范围（已在 P1/P2 文档记录）。
- **syscall 号跨架构**：444-446 属内核通用表；仍须 x86_64/arm64 CI 实测。
- **launcher 路径健壮性**：GUI `.app`/单文件分发下 `os.Executable()` 可用性需覆盖测试；子命令须在任何副作用初始化之前分发。
- **stderr 仍是带内通道**：受限子进程可伪造致命行（dsh 同款已知限制）；分类规则只做"最小误报"，不作为安全边界。

---

## 4 交付顺序、回滚与总验收

**排期**：第 1 周 P0-2 步骤 1-2（接口 + Provider 迁移，行为零变化，无依赖）；第 2-3 周 P0-1 步骤 1-4（可与 P0-2 并行）；第 3-4 周 P0-3 步骤 1-3（依赖 P0-2 sandbox seam）；第 5 周 P0-1 步骤 5-6 + P0-3 步骤 4-6；收尾做总验收与文档更新。

**总验收（跨三项）**：端到端——一次含工具调用 + 压缩 + 重启的会话：日志严格回放、请求可复现、`/ctx` 占比一致、沙箱状态如实报告；回归——`go test ./...` 全绿、旧数据目录（v1 日志 + 快照）可直接加载并升级；文档——README、GUI 契约与本设计同步更新。

**回滚策略总则**：三项均"接口先行、行为后置"，每步可单独回退（配置开关或还原文件）；数据升级只增不改（`.v1.bak`、`.json.legacy.bak`），任何回滚不丢用户数据。

---

## 附录：dsh 参照索引与关键结论

**dsh 参照索引**：P0-1 对应 `packages/core/session`（SessionEventMap、`seq=log.length`、surfaceOp、deriveMessages、严格回放）、`packages/session/session-persistence-jsonl`（逐会话 JSONL、header、撕裂尾部恢复）、`packages/compaction/compaction-basic`（压缩事件）；P0-2 对应 `docs/capability-seams.zh.md`（Service Definition/Provider/Consumer 拆分）与 `packages/fs`、`packages/shell`、`packages/subagent`、`packages/sandbox`（四个 ctx seam）；P0-3 对应 `packages/sandbox/sandbox-local`（runner 链、enforcement full/partial、拒绝/失败方言）、`native/system`（landlock-run CLI 约定）与 `docs/postmortem/0004`（分类教训）。

**关键结论摘要**

1. **P0-1 的本质是"把派生状态从真相源里赶出去"**：日志只存事件（含工具调用与结果、压缩事件），消息列表、滚动摘要、快照全部降级为可重建投影；"模型可见=已记录"用请求捕获测试钉死。
2. **P0-2 的本质是"把 import 边界改成接口边界"**：四个 seam 按"接口 + 默认 Provider（逐字迁移现状）+ Consumer 薄转发"落地，第一步零行为变化，最后一步用 P0-3 完成真实换实现演练。
3. **P0-3 的本质是"把近似隔离升级为内核强制 + 如实报告"**：Landlock 经"自身二进制 launcher 子命令"落地（零外部依赖），强制执行完整性（full/partial）是报告事实而非承诺；不可用时 fail-closed，绝不静默透传。
4. **三项共同的可回滚性来自数据只增不改、行为接口先行**：任何一步回退都不丢数据、不破坏旧版本可读性。
