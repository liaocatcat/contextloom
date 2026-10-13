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

// GenerationParamsBackend 可选接口：支持独立设置输出/输入 token 上限与思考开关。
//
// 设计动机（对齐 DeepSeek Harness 的 max_tokens 三级优先级与 thinking 独立字段）：
// 输出预算（max_tokens）、输入预算（发送前压缩阈值）、思考开关是三个相互独立的
// 用户参数，不应再被"上下文窗口"一个值笼统代表。但把它并进 Backend 接口本体
// 会强制所有实现（含大量测试 mock 与 OpenAI 兼容通道）补方法，破坏面过大；
// 因此沿用 StreamExBackend 的可选接口模式：只有真正支持的后端（DeepSeekBackend、
// modelgateway.handle）实现它，调用方用类型断言探测，未实现者保持原有行为。
//
// 约定语义：
//   - n<=0 表示"未设置"，回退 maxContext（保持历史行为逐字不变）；
//   - ThinkingEnabled() 仅在显式设置过 true 时返回 true（nil=不显式发送
//     thinking 字段，跟随 reasoning_effort 的历史行为）。
type GenerationParamsBackend interface {
	SetMaxOutputTokens(n int) // n<=0 表示未设置（回退 maxContext，保持现状语义）
	MaxOutputTokens() int
	SetMaxInputTokens(n int) // n<=0 表示未设置（回退 maxContext）
	MaxInputTokens() int
	SetThinkingEnabled(on bool)
	ThinkingEnabled() bool
}
