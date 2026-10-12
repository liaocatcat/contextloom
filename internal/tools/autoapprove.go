package tools

import "context"

// ── 自动批准机制（task 模式自动化执行）──
//
// 设计动机：task 模式（多 Agent 拆分子任务）面向自动化执行，若每个敏感操作
// （写文件、执行命令）都弹出权限确认弹窗，会打断自动化流程、极其繁琐。
// 因此 task 模式下跳过确认弹窗，由模型自身负责操作正确性。
//
// 实现：通过 context 传递 autoApprove 标记。Registry.Execute 在执行前检查
// 该标记，若开启则跳过 CautiousAnalyzer 的确认回调（直接执行），
// chat 模式（用户在场交互）不受影响，仍保留确认弹窗。

// autoApproveKey 是 context 中携带"自动批准"标记的 key
type autoApproveKey struct{}

// WithAutoApprove 返回携带自动批准标记的 context。
// enabled=true 时，该 context 下的所有工具执行跳过权限确认弹窗。
func WithAutoApprove(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, autoApproveKey{}, enabled)
}

// IsAutoApprove 判断当前 context 是否启用了自动批准。
func IsAutoApprove(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(autoApproveKey{}).(bool)
	return v
}
