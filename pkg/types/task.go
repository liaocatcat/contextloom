package types

import "time"

// ── 指令与阶段常量 ──

// TaskDirective 是任务指令类型。
type TaskDirective string

const (
	DirectiveCodeAgent TaskDirective = "CODE_AGENT"
	DirectiveReview    TaskDirective = "REVIEW"
	DirectivePlan      TaskDirective = "PLAN"
	DirectiveExecute   TaskDirective = "EXECUTE"
	// DirectiveSpec 需求细化指令（goal 模式 spec 阶段）：模型先产出
	// [GOAL_SPEC] 细化需求，经确定性 SpecGate 通过后才进入 plan。
	DirectiveSpec TaskDirective = "SPEC"
)

// PluginStage 是插件生命周期的阶段。
type PluginStage string

const (
	StagePre    PluginStage = "pre"
	StageDuring PluginStage = "during"
	StagePost   PluginStage = "post"
)

// ── 任务类型 ──

// TaskCreate 是从 ACP 或 TUI 接收到的任务创建请求。
//
// 与旧版的区别：
//   - Payload 从 any 改为 json.RawMessage，延迟解析以便各模块按需反序列化
//   - 新增 Status / CreatedAt / UpdatedAt 用于任务生命周期追踪
//   - ContextPolicy 改为枚举类型以增强类型安全
type TaskCreate struct {
	Type          string        `json:"type"`
	ID            string        `json:"id"`
	Directive     string        `json:"directive"`
	Task          string        `json:"task"`
	Payload       any           `json:"payload"`
	Mode          string        `json:"mode,omitempty"` // 提示词模式: "chat"(自然语言) | "task"(JSON极简)
	ContextPolicy ContextPolicy `json:"context_policy"` // 上下文策略: full/recent/minimal/none
	Priority      int           `json:"priority"`
	Status        TaskStatus    `json:"status,omitempty"`
	CreatedAt     time.Time     `json:"created_at,omitempty"`
	UpdatedAt     time.Time     `json:"updated_at,omitempty"`

	// 多 Agent 扩展（多agent实现建议.md §3.1）
	Relation   *AgentRelation   `json:"relation,omitempty"`
	Manifest   *ContextManifest `json:"manifest,omitempty"`
	ParentGoal string           `json:"parent_goal,omitempty"`

	// Images 本轮输入检测到的图片内容片段（OpenAI 兼容 image_url 多模态）。
	// 由 runtime.ProcessInput 填充：chat 模式图片随对话历史（多模态消息）
	// 透传——多轮追问仍可见图；task/goal 模式历史被展平为纯文本，
	// 由 executor 显式附到最终 user 消息。executor 同时据此决定是否
	// 切换 vision_model（config.model.vision_model）。
	Images []ContentPart `json:"images,omitempty"`
}

// AgentRelation 描述 Agent 间的父子/兄弟关系
type AgentRelation struct {
	ParentID   string   `json:"parent_id,omitempty"`
	ChildrenID []string `json:"children_ids,omitempty"`
	DependsOn  []string `json:"depends_on,omitempty"`
}

// ContextManifest 是传递给子 Agent 的自包含上下文
type ContextManifest struct {
	WorkDir       string         `json:"work_dir"`
	ProjectRoot   string         `json:"project_root"`
	FilePaths     []string       `json:"file_paths,omitempty"`
	Summary       string         `json:"summary,omitempty"`
	ContextFile   string         `json:"context_file,omitempty"`
	Constraints   map[string]any `json:"constraints,omitempty"`
	MaxIterations int            `json:"max_iterations,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`
}

// ContextPolicy 定义任务使用的上下文策略。
type ContextPolicy string

const (
	PolicyFullContext ContextPolicy = "full"
	PolicyRecentOnly  ContextPolicy = "recent"
	PolicyMinimal     ContextPolicy = "minimal"
	PolicyNone        ContextPolicy = "none"
)

// TaskStatus 是任务的生命周期状态。
type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskRunning   TaskStatus = "running"
	TaskCompleted TaskStatus = "completed"
	TaskFailed    TaskStatus = "failed"
	TaskCancelled TaskStatus = "cancelled"
)

// ── Agent 状态类型 ──

// AgentStatus 是 Agent 的运行状态。
type AgentStatus string

const (
	AgentIdle      AgentStatus = "idle"
	AgentRunning   AgentStatus = "running"
	AgentWaiting   AgentStatus = "waiting" // 等待工具执行结果
	AgentCompleted AgentStatus = "completed"
	AgentError     AgentStatus = "error"
	AgentCancelled AgentStatus = "cancelled"
)

// AgentState 描述一个 Agent 实例的当前状态。
type AgentState struct {
	ID            string      `json:"id"`
	TaskID        string      `json:"task_id"`
	Status        AgentStatus `json:"status"`
	CurrentRound  int         `json:"current_round"`   // 当前对话轮次
	MaxRounds     int         `json:"max_rounds"`      // 最大对话轮次
	ToolCallCount int         `json:"tool_call_count"` // 已执行工具调用数
	TokenUsage    Usage       `json:"token_usage"`
	Error         string      `json:"error,omitempty"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// ── 任务结果类型 ──

// TaskResult 是任务执行完成后的结果。
type TaskResult struct {
	TaskID   string          `json:"task_id"`
	AgentID  string          `json:"agent_id"`
	Success  bool            `json:"success"`
	Output   any             `json:"output,omitempty"`
	Error    string          `json:"error,omitempty"`
	Overseer *OverseerReport `json:"overseer,omitempty"`
	Duration time.Duration   `json:"duration"`
}

// ── Agent 间通信 ──

// AgentMessage 是 Agent 间通信消息（多agent实现建议.md §8.1）。
// To 支持："main"（主线程）、具体 agentID、"broadcast"（广播）。
// Type 支持："query"（期待应答）、"notify"（单向通知）、"result"（结果上报）。
type AgentMessage struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Type    string `json:"type"`
	Content string `json:"content"`
}
