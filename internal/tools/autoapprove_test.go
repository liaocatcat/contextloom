package tools

import (
	"context"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── autoApprove 上下文标记 ──

func TestWithAutoApprove_Flag(t *testing.T) {
	ctx := context.Background()
	if IsAutoApprove(ctx) {
		t.Error("默认 context 不应是 autoApprove")
	}

	on := WithAutoApprove(ctx, true)
	if !IsAutoApprove(on) {
		t.Error("WithAutoApprove(true) 后应返回 true")
	}

	off := WithAutoApprove(ctx, false)
	if IsAutoApprove(off) {
		t.Error("WithAutoApprove(false) 后应返回 false")
	}

	if IsAutoApprove(nil) {
		t.Error("nil context 应返回 false")
	}
}

// TestRegistry_AutoApproveSkipsConfirm 测试 task 模式 autoApprove 下跳过确认弹窗：
// 敏感写操作（write_file 高风路径）不触发确认回调，直接执行。
func TestRegistry_AutoApproveSkipsConfirm(t *testing.T) {
	r := NewRegistry()
	ca := NewCautiousAnalyzer(ModeStandard)
	r.SetCautious(ca)

	mockWriteCalled := false
	r.Register("write_file", func(ctx context.Context, params map[string]any) (any, error) {
		mockWriteCalled = true
		return map[string]any{"success": true}, nil
	})

	confirmCalled := false
	r.SetConfirmFunc(func(ctx context.Context, decision *ConfirmDecision) (bool, string) {
		confirmCalled = true
		return false, ""
	})

	ctx := context.Background()

	// 1) 无 autoApprove（chat 模式）：敏感写操作触发确认
	_, err := r.Execute(ctx, types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path": "/etc/passwd", "content": "hack"}`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !confirmCalled {
		t.Error("chat 模式敏感写操作应触发确认")
	}
	if mockWriteCalled {
		t.Error("chat 模式确认被拒后不应执行工具")
	}

	// 2) autoApprove（task 模式）：跳过确认，直接执行
	confirmCalled = false
	mockWriteCalled = false
	actx := WithAutoApprove(ctx, true)
	_, err = r.Execute(actx, types.ToolCall{
		ID:   "call_2",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path": "/etc/passwd", "content": "hack"}`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if confirmCalled {
		t.Error("task 模式 autoApprove 不应触发确认弹窗")
	}
	if !mockWriteCalled {
		t.Error("task 模式 autoApprove 应直接执行工具")
	}
}

// TestRegistry_AutoApproveWithContext 测试 confirmFn 阻塞等待时，
// autoApprove 上下文不会卡死（确认回调根本不会被调用）。
func TestRegistry_AutoApproveWithContext(t *testing.T) {
	r := NewRegistry()
	ca := NewCautiousAnalyzer(ModeStrict) // 严格模式：所有写操作都确认
	r.SetCautious(ca)

	r.Register("write_file", func(ctx context.Context, params map[string]any) (any, error) {
		return map[string]any{"success": true}, nil
	})

	// 永不响应的确认回调（模拟 TUI 弹窗无人应答）
	r.SetConfirmFunc(func(ctx context.Context, decision *ConfirmDecision) (bool, string) {
		<-ctx.Done()
		return false, ""
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// chat 模式：严格模式下写操作会卡在确认（但这里我们验证 autoApprove 不卡）
	actx := WithAutoApprove(ctx, true)
	done := make(chan struct{})
	go func() {
		_, _ = r.Execute(actx, types.ToolCall{
			ID:   "call_3",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "write_file",
				Arguments: `{"path": "a.txt", "content": "x"}`,
			},
		})
		close(done)
	}()

	select {
	case <-done:
		// autoApprove 直接放行，无需等待确认
	case <-time.After(500 * time.Millisecond):
		t.Error("autoApprove 下工具执行不应等待确认回调")
	}
}
