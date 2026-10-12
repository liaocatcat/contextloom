// Package acp 实现标准 agent-client-protocol 1.0（Zed 发起、OpenAI Codex CLI 采用的
// agent 互操作事实标准），替代原自研 WebSocket JSON-RPC 协议。
//
// 协议要点（agent-client-protocol 1.0）：
//   - 传输：WebSocket，消息为 JSON-RPC 2.0（jsonrpc/id/method/params）；
//   - 生命周期：连接建立 → server/hello（版本+能力）→ session/new → session/init
//   - init_response → session/prompt（流式 agent_message/tool_call/
//     request_permission）→ session/complete → session/close；
//   - 审批：agent 推送 session/request_permission，客户端回
//     session/request_permission_response{response:"allow"|"deny"}；
//   - 工具：agent 内部执行工具并以 agent_message 的 tool_result 内容块上报
//     （或推送 session/tool_call 由客户端执行并回 tool_call_response）。
//
// 零外部依赖（仅 gorilla/websocket，已在 go.mod），全平台可交叉编译。
package acp

import "encoding/json"

// Version 协议版本（agent-client-protocol 1.0）。
const Version = "1.0"

// ── JSON-RPC 2.0 结构 ──

// RPCRequest 客户端 → agent 请求。
type RPCRequest struct {
	RawID  json.RawMessage `json:"id"` // string|number|null
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// RPCResponse agent → 客户端响应（id 与请求一致）。
type RPCResponse struct {
	RawID  json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// RPCError JSON-RPC 错误对象。
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC 标准错误码。
const (
	ErrInvalidRequest = -32600 // 请求无效（非法 JSON/非对象）
	ErrMethodNotFound = -32601 // 方法不存在
	ErrInvalidParams  = -32602 // 参数无效
	ErrInternal       = -32000 // 服务端内部错误
)

// ── 方法名（严格按 agent-client-protocol 1.0 规范）──

// Client → Agent。
const (
	MethodSessionNew           = "session/new"
	MethodSessionUpdate        = "session/update"
	MethodSessionPrompt        = "session/prompt"
	MethodSessionComplete      = "session/complete"
	MethodSessionCancel        = "session/cancel"
	MethodSessionClose         = "session/close"
	MethodPermissionResponse   = "session/request_permission_response"
	MethodToolCallResponse     = "session/tool_call_response"
	MethodAgentMessageResponse = "session/agent_message_response"
)

// Agent → Client。
const (
	MethodServerHello       = "server/hello"
	MethodSessionInit       = "session/init"
	MethodInitResponse      = "session/init_response"
	MethodPromptResponse    = "session/prompt_response"
	MethodCompleteResponse  = "session/complete_response"
	MethodUpdateResponse    = "session/update_response"
	MethodCloseResponse     = "session/close_response"
	MethodCancelResponse    = "session/cancel_response"
	MethodPermissionRequest = "session/request_permission"
	MethodAgentMessage      = "session/agent_message"
	MethodToolCall          = "session/tool_call"
	MethodSummary           = "session/summary"
)

// ── Params 结构体 ──

// SessionNewParams session/new 参数。
type SessionNewParams struct {
	SessionID string           `json:"session_id,omitempty"` // 空=由 agent 生成
	Tools     []ToolDefinition `json:"tools,omitempty"`      // 客户端能力工具（可选）
	Model     string           `json:"model,omitempty"`
}

// SessionPromptParams session/prompt 参数。
type SessionPromptParams struct {
	SessionID string           `json:"session_id"`
	Prompt    string           `json:"prompt"`
	Tools     []ToolDefinition `json:"tools,omitempty"`
	Model     string           `json:"model,omitempty"`
}

// SessionUpdateParams session/update 参数。
type SessionUpdateParams struct {
	SessionID string           `json:"session_id"`
	Tools     []ToolDefinition `json:"tools"`
}

// SessionCompleteParams session/complete 参数。
type SessionCompleteParams struct {
	SessionID string `json:"session_id"`
}

// SessionCancelParams session/cancel 参数。
type SessionCancelParams struct {
	SessionID string `json:"session_id"`
}

// SessionCloseParams session/close 参数。
type SessionCloseParams struct {
	SessionID string `json:"session_id"`
}

// PermissionResponseParams session/request_permission_response 参数。
type PermissionResponseParams struct {
	SessionID           string `json:"session_id"`
	PermissionRequestID string `json:"permission_request_id"`
	Response            string `json:"response"` // "allow" | "deny"
}

// ToolCallResponseParams session/tool_call_response 参数。
type ToolCallResponseParams struct {
	SessionID  string         `json:"session_id"`
	ToolCallID string         `json:"tool_call_id"`
	Content    []ContentBlock `json:"content"`
}

// AgentMessageResponseParams session/agent_message_response 参数。
type AgentMessageResponseParams struct {
	SessionID string `json:"session_id"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
}

// SessionInitParams session/init（agent → 客户端）参数。
type SessionInitParams struct {
	SessionID    string       `json:"session_id"`
	Capabilities Capabilities `json:"capabilities"`
}

// HelloParams server/hello 参数。
type HelloParams struct {
	Version      string       `json:"version"`
	Capabilities Capabilities `json:"capabilities"`
}

// ResponseParams 各 *_response 通用参数。
type ResponseParams struct {
	Success   bool   `json:"success"`
	SessionID string `json:"session_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// PermissionRequestParams session/request_permission 参数。
type PermissionRequestParams struct {
	SessionID         string            `json:"session_id"`
	PermissionRequest PermissionRequest `json:"permission_request"`
}

// AgentMessageParams session/agent_message 参数（message 可为字符串或 ContentBlock 数组）。
type AgentMessageParams struct {
	SessionID string          `json:"session_id"`
	Message   json.RawMessage `json:"message"`
}

// ToolCallParams session/tool_call 参数（请求客户端执行工具）。
type ToolCallParams struct {
	SessionID  string          `json:"session_id"`
	Name       string          `json:"name"`
	Arguments  json.RawMessage `json:"arguments"`
	ToolCallID string          `json:"tool_call_id"`
}

// SummaryParams session/summary 参数（agent 请求客户端决定摘要保留策略）。
type SummaryParams struct {
	SessionID string `json:"session_id"`
	Summary   string `json:"summary"`
	Action    string `json:"action"` // "keep" | "discard" | "keep_using"
}

// ── 内容块 ──

// ContentBlock 内容块类型常量。
const (
	ContentText       = "text"        // 纯文本
	ContentReasoning  = "reasoning"   // 思考过程
	ContentToolResult = "tool_result" // 工具执行结果
	ContentInputText  = "input_text"  // 用户输入
	ContentOutputText = "output_text" // agent 输出
)

// ContentBlock agent_message 内容块。
type ContentBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
}

// TextBlock 便捷构造文本块。
func TextBlock(text string) ContentBlock {
	return ContentBlock{Type: ContentText, Text: text}
}

// ReasoningBlock 便捷构造思考块。
func ReasoningBlock(text string) ContentBlock {
	return ContentBlock{Type: ContentReasoning, Text: text}
}

// ToolResultBlock 便捷构造工具结果块。
func ToolResultBlock(toolCallID, content string) ContentBlock {
	return ContentBlock{
		Type:       ContentToolResult,
		ToolCallID: toolCallID,
		Content:    json.RawMessage([]byte(`"` + content + `"`)),
	}
}

// ── 权限请求 ──

// 权限种类常量（agent-client-protocol 1.0）。
const (
	PermOpenFile       = "open_file"
	PermCreateFile     = "create_file"
	PermEditFile       = "edit_file"
	PermExecuteCommand = "execute_command"
	PermUseTool        = "use_tool"
)

// PermissionRequest 权限请求。
type PermissionRequest struct {
	ID      string          `json:"id"`
	Kind    string          `json:"kind"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ── 能力与工具定义 ──

// Capabilities agent 能力声明（server/hello 与 session/init 携带）。
type Capabilities struct {
	Tools         []ToolDefinition `json:"tools,omitempty"`
	ContextWindow int              `json:"context_window,omitempty"`
	Models        []string         `json:"models,omitempty"`
	PromptCaching bool             `json:"prompt_caching,omitempty"`
}

// ToolDefinition 工具定义（与 OpenAI function calling 形状对齐，
// 便于从 internal/tools registry 转换）。
type ToolDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// Params 便捷访问：把请求 params 解析到目标结构（空 params 视为 {}）。
func Params(raw json.RawMessage, dst any) error {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	return json.Unmarshal(raw, dst)
}
