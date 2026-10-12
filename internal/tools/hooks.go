package tools

import (
	"context"
	"fmt"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ─────────────────────────────────────────────────────────────
// 工具双向 hooks（对标 Claude Code PreToolUse/PostToolUse 的零依赖落地）
//
// PreToolUse：工具执行前拦截——可修改调用参数、拒绝执行；
// PostToolUse：工具执行后回调——可修改结果、记录审计。
// 由 Registry 维护 hook 链，按注册顺序执行；任一 Pre 拒绝即终止。
// ─────────────────────────────────────────────────────────────

// Hook 工具生命周期钩子。
type Hook interface {
	// PreToolUse 工具执行前调用。返回 modified 参数（可原样返回）、
	// allow=false 时拒绝执行（reason 传给模型）。
	PreToolUse(ctx context.Context, call types.ToolCall) (modified types.ToolCall, allow bool, reason string)

	// PostToolUse 工具执行后调用（无论成功失败）。可修改结果。
	PostToolUse(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult
}

// HookFunc 函数式 Hook 适配器。
type HookFunc struct {
	Pre  func(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string)
	Post func(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult
}

// PreToolUse 实现 Hook。
func (h HookFunc) PreToolUse(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string) {
	if h.Pre == nil {
		return call, true, ""
	}
	return h.Pre(ctx, call)
}

// PostToolUse 实现 Hook。
func (h HookFunc) PostToolUse(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult {
	if h.Post == nil {
		return result
	}
	return h.Post(ctx, call, result)
}

// LoggingHook 记录所有工具调用（Pre 阶段记录，失败审计在 Post）。
func LoggingHook(logf func(format string, args ...any)) Hook {
	return HookFunc{
		Pre: func(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string) {
			if logf != nil {
				logf("[hook] 工具调用: %s %s", call.Function.Name, call.Function.Arguments)
			}
			return call, true, ""
		},
		Post: func(ctx context.Context, call types.ToolCall, result *types.ToolResult) *types.ToolResult {
			if logf != nil {
				if result != nil && result.Error != "" {
					logf("[hook] 工具失败: %s → %s", call.Function.Name, result.Error)
				}
			}
			return result
		},
	}
}

// BlockHook 按规则拒绝特定工具/参数（如禁止 rm -rf、禁止写敏感路径）。
// rules: 工具名 → 参数子串黑名单（参数 JSON 包含任一子串即拒绝）。
func BlockHook(rules map[string][]string) Hook {
	return HookFunc{
		Pre: func(ctx context.Context, call types.ToolCall) (types.ToolCall, bool, string) {
			blocks, ok := rules[call.Function.Name]
			if !ok || len(blocks) == 0 {
				return call, true, ""
			}
			for _, b := range blocks {
				if b != "" && containsStr(call.Function.Arguments, b) {
					return call, false, fmt.Sprintf("被安全规则拦截（%s 含敏感内容 %q）", call.Function.Name, b)
				}
			}
			return call, true, ""
		},
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && indexStr(s, sub) >= 0
}

func indexStr(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
