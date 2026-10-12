package tui

import "time"

type MsgType int

const (
	MsgSystem     MsgType = iota // startup, command results
	MsgUser                      // user input
	MsgAssistant                 // AI response body
	MsgThinking                  // thinking/reasoning block (collapsible)
	MsgToolCall                  // tool invocation
	MsgToolResult                // tool success
	MsgToolError                 // tool failure
	MsgError                     // system error
)

type ChatMessage struct {
	Type      MsgType
	Content   string
	ToolName  string
	ToolArgs  string
	Collapsed bool
	Streaming bool
	Timestamp time.Time
}

func (m *ChatMessage) clone() ChatMessage { return *m }

// constructors
func sysMsg(s string) ChatMessage {
	return ChatMessage{Type: MsgSystem, Content: s, Timestamp: time.Now()}
}
func usrMsg(s string) ChatMessage {
	return ChatMessage{Type: MsgUser, Content: s, Timestamp: time.Now()}
}
func asstMsg(s string) ChatMessage {
	return ChatMessage{Type: MsgAssistant, Content: s, Timestamp: time.Now()}
}
func thinkMsg() ChatMessage {
	return ChatMessage{Type: MsgThinking, Collapsed: false, Streaming: true, Timestamp: time.Now()}
}
func toolCall(name, args string) ChatMessage {
	return ChatMessage{Type: MsgToolCall, ToolName: name, ToolArgs: args, Timestamp: time.Now()}
}
func toolOK(name, result string) ChatMessage {
	return ChatMessage{Type: MsgToolResult, ToolName: name, Content: result, Timestamp: time.Now()}
}
func toolFail(name, err string) ChatMessage {
	return ChatMessage{Type: MsgToolError, ToolName: name, Content: err, Timestamp: time.Now()}
}
func errMsg(s string) ChatMessage {
	return ChatMessage{Type: MsgError, Content: s, Timestamp: time.Now()}
}
