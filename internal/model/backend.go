package model

import (
	"context"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Backend interface {
	Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error)
	Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error)
	ModelID() string
	MaxContext() int
	// SupportsVision 报告当前后端是否支持 image_url 多模态输入。
	// DeepSeek 官方 API 当前是纯文本模型（content 只接受 text 变体，发
	// image_url 直接 400），默认 false；接入 OpenAI 兼容视觉模型时
	// 通过 config model.supports_vision 开启。
	SupportsVision() bool
	SetReasoningEffort(level string)
	ReasoningEffort() string
	SetTopP(p float64)
	TopP() float64
	SetTemperature(t float64)
	Temperature() float64
}

// StreamExBackend 可选接口：支持结构化流式输出（Content + ToolCalls +
// FinishReason 全量 chunk）。DeepSeekBackend 原生实现；modelgateway.handle
// 转发底层实例实现。agent 据此在 gateway 路由路径下也能解析工具调用——
// 否则 handle 包装使类型断言 *model.DeepSeekBackend 失败 → 走普通 Stream
// （只转发文本，ToolCalls 被底层吞掉）→ 工具从不执行、对话一轮中止。
type StreamExBackend interface {
	StreamEx(ctx context.Context, messages []types.Message) (<-chan StreamChunk, <-chan error)
	SetStreamTools(tools []types.ToolDefinition)
}
