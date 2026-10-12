package tools

import "context"

// ── 会话归属上下文 ──
//
// 设计动机：进程内单例 Runtime 下 GUI 可并行运行多个会话（各自 chat/task/goal
// 消息流并发执行）。Agent 在 Run/RunStream 启动时绑定所属会话（SetSessionID），
// 但工具执行发生在不同 goroutine，且当前会话（currentSessionID）可能已被
// 其他会话的消息流切换——若 harness_search 等检索类工具在执行时读取全局
// 当前会话，会检索到其他会话的内容（串对话根因之一）。
// 方案：Agent 启动时把归属会话注入 context，工具执行优先取 context 中的
// 会话，回退到运行时全局当前会话。

// sessionIDKey 是 context 中携带当前 Agent 所属会话 UUID 的 key
type sessionIDKey struct{}

// WithSessionID 将会话 UUID 注入 context，供检索类工具识别调用者所属会话。
func WithSessionID(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, sessionIDKey{}, sid)
}

// SessionIDFromContext 从 context 提取会话 UUID（未注入返回空串=未绑定，
// 调用方应回退到全局当前会话或仅检索项目级共享块）。
func SessionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	sid, _ := ctx.Value(sessionIDKey{}).(string)
	return sid
}
