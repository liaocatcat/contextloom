package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestSpawnTask_TimeoutOverride：spawn_task 豁免统一 120s 固定超时——
// 工具执行 ctx 无 deadline（由内部活动式超时管理），其他工具仍有 deadline。
func TestSpawnTask_TimeoutOverride(t *testing.T) {
	r := NewRegistry()

	// 记录工具收到的 ctx 是否带 deadline
	var spawnDeadline, normalDeadline bool
	r.Register("spawn_task", func(ctx context.Context, params map[string]any) (any, error) {
		_, spawnDeadline = ctx.Deadline()
		return "spawn-ok", nil
	})
	r.Register("_probe_normal", func(ctx context.Context, params map[string]any) (any, error) {
		_, normalDeadline = ctx.Deadline()
		return "normal-ok", nil
	})

	if d, ok := toolTimeoutOverride["spawn_task"]; !ok || d != 0 {
		t.Fatalf("toolTimeoutOverride 缺少 spawn_task=0 豁免项: %v", toolTimeoutOverride)
	}

	if _, err := r.Execute(context.Background(), types.ToolCall{
		ID: "c1", Type: "function",
		Function: types.FunctionCall{Name: "spawn_task", Arguments: `{"task":"x"}`},
	}); err != nil {
		t.Fatalf("spawn_task 执行失败: %v", err)
	}
	if spawnDeadline {
		t.Error("spawn_task 不应有统一超时 deadline（活动式超时由内部管理）")
	}

	if _, err := r.Execute(context.Background(), types.ToolCall{
		ID: "c2", Type: "function",
		Function: types.FunctionCall{Name: "_probe_normal", Arguments: `{}`},
	}); err != nil {
		t.Fatalf("普通工具执行失败: %v", err)
	}
	if !normalDeadline {
		t.Error("普通工具应保留统一超时 deadline")
	}
}

// TestSpawnTask_TimeoutOverrideMsg：豁免后的错误信息不出现"120s 强制终止"。
func TestSpawnTask_TimeoutOverrideMsg(t *testing.T) {
	r := NewRegistry()
	r.Register("spawn_task", func(ctx context.Context, params map[string]any) (any, error) {
		<-ctx.Done() // 无 deadline → 永不超时返回；由测试 ctx 取消
		return nil, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	res, _ := r.Execute(ctx, types.ToolCall{
		ID: "c1", Type: "function",
		Function: types.FunctionCall{Name: "spawn_task", Arguments: `{"task":"x"}`},
	})
	if res != nil && strings.Contains(res.Error, "120s 后强制终止") {
		t.Errorf("spawn_task 不应报告固定超时文案: %s", res.Error)
	}
}
