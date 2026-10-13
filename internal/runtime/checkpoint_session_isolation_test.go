package runtime

import (
	"encoding/json"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/checkpoint"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/memory"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// newCheckpointTestRuntime 按 checkpoint_restore_test.go 同款装配测试 Runtime。
func newCheckpointTestRuntime(t *testing.T) *Runtime {
	rt, tmpDir := newTestRuntime(t)
	t.Cleanup(func() { rt.CloseHarness() })
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)
	return rt
}

// saveSessionCheckpoint 写入指定会话归属的主任务检查点
// （task/goal 主 Agent 恒写 run-main，同项目多对话共用该文件）。
func saveSessionCheckpoint(t *testing.T, rt *Runtime, sid, content string) {
	t.Helper()
	store := rt.CheckpointStore()
	if store == nil {
		t.Fatal("checkpoint store 未初始化")
	}
	msgs, err := json.Marshal([]types.Message{{Role: "user", Content: content}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Save(checkpoint.Checkpoint{
		RunID:     "main",
		SessionID: sid,
		Round:     1,
		Messages:  msgs,
		Done:      false,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
}

// 回归：restoreFromCheckpoint 会话隔离——B 会话绝不恢复 A 会话留下的
// run-main 检查点（同项目多对话共用文件，跨会话串扰/复活根因：切到 B 发
// 新任务时被 A 的检查点上下文"接管"）。
func TestRestoreFromCheckpoint_SessionIsolation(t *testing.T) {
	rt := newCheckpointTestRuntime(t)

	rt.NewSession("A 的任务")
	sidA := rt.GetSessionID()
	if sidA == "" {
		t.Fatal("会话 A 未创建")
	}
	rt.AddToHistory("user", "A的任务")
	rt.AddToHistory("assistant", "A的回复")
	saveSessionCheckpoint(t, rt, sidA, "A的任务")

	// 切到 B 会话发新任务：A 的检查点不得接管 B
	rt.NewSession("B 的新对话")
	sidB := rt.GetSessionID()
	if sidB == "" || sidB == sidA {
		t.Fatalf("会话切换异常: A=%q B=%q", sidA, sidB)
	}
	_, hasRestore := rt.restoreFromCheckpoint("main")
	if hasRestore {
		t.Fatal("B 会话恢复了 A 会话的检查点（跨会话串扰）")
	}
}

// 同会话检查点保持可恢复：隔离校验不得误伤正常恢复路径。
func TestRestoreFromCheckpoint_SameSessionRestores(t *testing.T) {
	rt := newCheckpointTestRuntime(t)

	rt.NewSession("当前会话任务")
	sid := rt.GetSessionID()
	saveSessionCheckpoint(t, rt, sid, "当前会话的任务内容")

	restored, hasRestore := rt.restoreFromCheckpoint("main")
	if !hasRestore || restored == nil {
		t.Fatalf("同会话检查点应可恢复, got hasRestore=%v restored=%v", hasRestore, restored)
	}
	found := false
	for _, m := range restored {
		if s, ok := m.Content.(string); ok && s == "当前会话的任务内容" {
			found = true
		}
	}
	if !found {
		t.Fatalf("恢复结果应包含检查点消息: %+v", restored)
	}
}

// 向后兼容：SessionID 为空的旧检查点（历史数据）保持可恢复。
func TestRestoreFromCheckpoint_LegacyNoSessionID(t *testing.T) {
	rt := newCheckpointTestRuntime(t)

	rt.NewSession("旧检查点兼容")
	store := rt.CheckpointStore()
	if store == nil {
		t.Fatal("checkpoint store 未初始化")
	}
	msgs, err := json.Marshal([]types.Message{{Role: "user", Content: "旧数据任务"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Save(checkpoint.Checkpoint{RunID: "main", Round: 1, Messages: msgs, Done: false}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	restored, hasRestore := rt.restoreFromCheckpoint("main")
	if !hasRestore || restored == nil {
		t.Fatalf("无 SessionID 的旧检查点应可恢复, got hasRestore=%v restored=%v", hasRestore, restored)
	}
}
