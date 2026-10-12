package types

import "encoding/json"

// ── ACP 消息类型常量 ──

const (
	ACPTypeTaskCreate      = "task.create"
	ACPTypeTaskAccepted    = "task.accepted"
	ACPTypeContextReady    = "context.ready"
	ACPTypeContextScoreChg = "context.score_change"
	ACPTypeTaskProgress    = "task.progress"
	ACPTypeTokenStream     = "token.stream"
	ACPTypeToolCall        = "tool.call"
	ACPTypeToolResult      = "tool.result"
	ACPTypeCommand         = "command"
	ACPTypeSync            = "sync"
	ACPTypeAck             = "ack"
)

// ── ACP 消息信封 ──

// ACPMessage 是 ACP WebSocket 协议的通用消息信封。
// 所有 ACP 消息都通过此结构进行序列化/反序列化。
type ACPMessage struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Seq     int64           `json:"seq"`
	Payload json.RawMessage `json:"payload"`
}

// ── ACP Payload 类型 ──

// ACPTaskCreatePayload 是 task.create 消息的 payload。
type ACPTaskCreatePayload struct {
	Directive     string `json:"directive"`
	Task          string `json:"task"`
	ContextPolicy string `json:"context_policy"`
	Priority      int    `json:"priority"`
}

// ACPContextReadyPayload 是 context.ready 消息的 payload。
type ACPContextReadyPayload struct {
	BlockCount int     `json:"block_count"`
	WindowPct  float64 `json:"window_pct"`
}

// ACPTaskProgressPayload 是 task.progress 消息的 payload。
type ACPTaskProgressPayload struct {
	LoadedBlocks int     `json:"loaded_blocks"`
	WindowUsage  float64 `json:"window_usage"`
}

// ACPTokenStreamPayload 是 token.stream 消息的 payload。
type ACPTokenStreamPayload struct {
	Token string `json:"token"`
	Index int    `json:"index"`
}

// ACPToolCallPayload 是 tool.call 消息的 payload。
type ACPToolCallPayload struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Params string `json:"params"` // JSON 编码的参数
}

// ACPToolResultPayload 是 tool.result 消息的 payload。
type ACPToolResultPayload struct {
	CallID string `json:"call_id"`
	Name   string `json:"name"`
	Result any    `json:"result"`
	Error  string `json:"error,omitempty"`
}

// ACPCommandPayload 是 command 消息的 payload。
type ACPCommandPayload struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
}

// ACPSyncPayload 是 sync 消息的 payload。
type ACPSyncPayload struct {
	LastSeq int64 `json:"last_seq"`
}

// ACPAckPayload 是 ack 消息的 payload。
type ACPAckPayload struct {
	AckSeq int64  `json:"ack_seq"`
	Status string `json:"status"` // "ok" | "error"
}
