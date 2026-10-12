package types

// ── 工具调用类型 ──

// ToolCall 表示模型请求的一次工具调用，兼容 OpenAI Function Calling 格式。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // 固定为 "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall 是工具调用中的函数名和参数。
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON 编码的参数字符串
}

// ToolDefinition 是工具注册表中的工具定义，用于发送给模型。
type ToolDefinition struct {
	Type     string             `json:"type"` // 固定为 "function"
	Function FunctionDefinition `json:"function"`
}

// FunctionDefinition 描述一个可调用函数的签名。
type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema
}

// ToolResult 是工具执行后的结果。
type ToolResult struct {
	CallID string `json:"call_id"` // 关联的 ToolCall.ID
	Name   string `json:"name"`
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
}

// ── Chat Completions 类型 ──

// ChatCompletionRequest 是发送给 LLM 后端的 Chat 请求。
type ChatCompletionRequest struct {
	Model           string           `json:"model"`
	Messages        []Message        `json:"messages"`
	Tools           []ToolDefinition `json:"tools,omitempty"`
	MaxTokens       int              `json:"max_tokens,omitempty"`
	Temperature     float64          `json:"temperature,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	Stream          bool             `json:"stream,omitempty"`
}

// ChatCompletionResponse 是 LLM 后端返回的 Chat 响应。
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"` // "chat.completion"
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// Choice 是 Chat Completion 中的一个候选回复。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"` // "stop" | "tool_calls" | "length" | "content_filter"
}

// StreamChunk 是流式 Chat Completion 的一个增量数据块。
type StreamChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"` // "chat.completion.chunk"
	Model   string        `json:"model"`
	Choices []DeltaChoice `json:"choices"`
}

// DeltaChoice 是流式响应中的一个增量候选。
type DeltaChoice struct {
	Index        int    `json:"index"`
	Delta        Delta  `json:"delta"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// Delta 是流式响应中的增量内容。
type Delta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Usage 是 Token 使用统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
