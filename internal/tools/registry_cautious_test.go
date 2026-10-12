package tools

import (
	"context"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestRegistry_CautiousConfirm 测试完整的谨慎性确认流程
func TestRegistry_CautiousConfirm(t *testing.T) {
	r := NewRegistry()
	ca := NewCautiousAnalyzer(ModeStandard)
	r.SetCautious(ca)

	// 注册 mock 工具替代真实工具，避免实际文件操作
	mockWriteCalled := false
	r.Register("write_file", func(ctx context.Context, params map[string]any) (any, error) {
		mockWriteCalled = true
		return map[string]any{"success": true}, nil
	})
	r.Register("read_file", func(ctx context.Context, params map[string]any) (any, error) {
		return map[string]any{"success": true}, nil
	})

	// 创建确认回调
	confirmCh := make(chan *ConfirmDecision, 1)
	respCh := make(chan bool, 1)

	r.SetConfirmFunc(func(ctx context.Context, decision *ConfirmDecision) (bool, string) {
		select {
		case confirmCh <- decision:
		case <-ctx.Done():
			return false, ""
		}

		select {
		case approved := <-respCh:
			return approved, ""
		case <-ctx.Done():
			return false, ""
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 测试1: 安全操作不应触发确认
	t.Run("安全操作不应触发确认", func(t *testing.T) {
		mockWriteCalled = false
		result, err := r.Execute(ctx, types.ToolCall{
			ID:   "call_1",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "read_file",
				Arguments: `{"path": "test.txt"}`,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Fatalf("unexpected error: %s", result.Error)
		}
		// read_file 是安全操作，不应触发确认
		select {
		case <-confirmCh:
			t.Error("安全操作不应触发确认")
		default:
			// 正确
		}
	})

	// 测试2: 敏感操作应触发确认，用户可以批准
	t.Run("敏感操作可批准", func(t *testing.T) {
		mockWriteCalled = false
		go func() {
			select {
			case <-confirmCh:
				respCh <- true // 批准
			case <-ctx.Done():
			}
		}()

		result, err := r.Execute(ctx, types.ToolCall{
			ID:   "call_2",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "write_file",
				Arguments: `{"path": "/etc/config.yaml", "content": "test"}`,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.Error != "" {
			t.Fatalf("unexpected error: %s", result.Error)
		}
		if !mockWriteCalled {
			t.Error("批准后应执行工具")
		}
	})

	// 测试3: 敏感操作可被拒绝
	t.Run("敏感操作可拒绝", func(t *testing.T) {
		mockWriteCalled = false
		go func() {
			select {
			case <-confirmCh:
				respCh <- false // 拒绝
			case <-ctx.Done():
			}
		}()

		result, err := r.Execute(ctx, types.ToolCall{
			ID:   "call_3",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "write_file",
				Arguments: `{"path": "/etc/config.yaml", "content": "test"}`,
			},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// 拒绝后应该返回 skipped
		m, ok := result.Result.(map[string]any)
		if !ok {
			t.Fatal("expected map result")
		}
		skipped, _ := m["skipped"].(bool)
		if !skipped {
			t.Error("拒绝后应返回 skipped=true")
		}
		if mockWriteCalled {
			t.Error("拒绝后不应执行工具")
		}
	})
}

// TestRegistry_Cautious_AlwaysAllowed 测试"总是允许"功能
func TestRegistry_Cautious_AlwaysAllowed(t *testing.T) {
	r := NewRegistry()
	ca := NewCautiousAnalyzer(ModeStandard)
	r.SetCautious(ca)

	// 注册 mock 替代
	r.Register("write_file", func(ctx context.Context, params map[string]any) (any, error) {
		return map[string]any{"success": true}, nil
	})

	var confirmCalled bool

	r.SetConfirmFunc(func(ctx context.Context, decision *ConfirmDecision) (bool, string) {
		confirmCalled = true
		return true, "always"
	})

	ctx := context.Background()

	// 第一次调用：应触发确认
	confirmCalled = false
	r.Execute(ctx, types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path": "/etc/config.yaml", "content": "test"}`,
		},
	})
	if !confirmCalled {
		t.Error("第一次调用应触发确认")
	}

	// 第二次调用同一工具+参数：不应触发确认（因为"总是允许"）
	confirmCalled = false
	r.Execute(ctx, types.ToolCall{
		ID:   "call_2",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path": "/etc/config.yaml", "content": "test"}`,
		},
	})
	if confirmCalled {
		t.Error("第二次调用不应触发确认（总是允许）")
	}

	// 不同参数：应触发确认
	confirmCalled = false
	r.Execute(ctx, types.ToolCall{
		ID:   "call_3",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path": "/etc/other.conf", "content": "test"}`,
		},
	})
	if !confirmCalled {
		t.Error("不同参数应触发确认")
	}
}

// TestRegistry_Cautious_QuietMode 测试安静模式
func TestRegistry_Cautious_QuietMode(t *testing.T) {
	r := NewRegistry()
	ca := NewCautiousAnalyzer(ModeQuiet) // 安静模式
	r.SetCautious(ca)

	// 注册 mock 替代
	r.Register("write_file", func(ctx context.Context, params map[string]any) (any, error) {
		return map[string]any{"success": true}, nil
	})

	var confirmCalled bool

	r.SetConfirmFunc(func(ctx context.Context, decision *ConfirmDecision) (bool, string) {
		confirmCalled = true
		return true, ""
	})

	ctx := context.Background()

	// 即使敏感操作，安静模式下也不应触发确认
	r.Execute(ctx, types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "write_file",
			Arguments: `{"path": "/etc/config.yaml", "content": "test"}`,
		},
	})
	if confirmCalled {
		t.Error("安静模式不应触发确认")
	}
}
