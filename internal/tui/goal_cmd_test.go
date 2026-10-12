package tui

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// newGoalRuntime 构造 goal 模式的真实 runtime，并用 ProcessInput 绑定一个目标会话。
func newGoalRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-tui-goal-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })

	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plugins"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "goal"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	rt.Backend = &gateMockBackend{response: "分析完成，输出步骤清单 [GOAL_PLAN]\n[{\"id\":\"s1\",\"desc\":\"步骤1\"}]\n[/GOAL_PLAN]"}
	return rt
}

func TestCmd_Mode_Goal(t *testing.T) {
	rt := newGoalRuntime(t)
	cr := NewCommandRegistry(rt)
	result, _ := cr.Execute("/mode goal")
	if !strings.Contains(result, "目标驱动模式") {
		t.Errorf("expected 目标驱动模式, got %q", result)
	}
	if rt.GetMode() != "goal" {
		t.Errorf("mode 应为 goal, got %s", rt.GetMode())
	}
	result, _ = cr.Execute("/mode")
	if !strings.Contains(result, "goal") {
		t.Errorf("expected current mode goal, got %q", result)
	}
}

func TestCmd_Mode_BackToChatResetsGoal(t *testing.T) {
	rt := newGoalRuntime(t)
	cr := NewCommandRegistry(rt)
	// 绑定目标会话
	ctx := context.Background()
	ch, _, err := rt.ProcessInput(ctx, "实现登录功能")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	for range ch {
	}
	if rt.GoalSession() == nil {
		t.Fatal("goal 模式发送目标后应有活动会话")
	}
	// 切回 chat → 会话停用
	cr.Execute("/mode chat")
	if rt.GoalSession() != nil {
		t.Fatal("切到 chat 后 GoalSession 应为 nil")
	}
}

func TestCmd_Goal_Status(t *testing.T) {
	rt := newGoalRuntime(t)
	cr := NewCommandRegistry(rt)
	// 无会话时显示引导
	result, _ := cr.Execute("/goal")
	if !strings.Contains(result, "无活动目标") {
		t.Errorf("expected 无活动目标, got %q", result)
	}

	// 绑定会话后显示状态
	ctx := context.Background()
	ch, _, err := rt.ProcessInput(ctx, "实现登录功能")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	for range ch {
	}
	// plan 轮结束：提取步骤
	rt.GoalOnRoundEnd(`[GOAL_PLAN]
[{"id":"s1","desc":"分析"},{"id":"s2","desc":"实现"}]
[/GOAL_PLAN]`, true, 70, types.OverseerReport{})

	result, _ = cr.Execute("/goal")
	for _, want := range []string{"实现登录功能", "步骤", "轮 1/0"} {
		if !strings.Contains(result, want) {
			t.Errorf("状态应包含 %q, got:\n%s", want, result)
		}
	}
}

func TestCmd_Goal_Abort(t *testing.T) {
	rt := newGoalRuntime(t)
	cr := NewCommandRegistry(rt)
	// 无会话
	result, _ := cr.Execute("/goal abort")
	if !strings.Contains(result, "无活动目标") {
		t.Errorf("expected 无活动目标, got %q", result)
	}
	// 有会话
	ctx := context.Background()
	ch, _, err := rt.ProcessInput(ctx, "实现登录功能")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	for range ch {
	}
	result, _ = cr.Execute("/goal abort")
	if !strings.Contains(result, "已中止") {
		t.Errorf("expected 已中止, got %q", result)
	}
	if !rt.GoalSession().Done() {
		t.Fatal("abort 后会话应终止")
	}
}

func TestCmd_Goal_ListAndResume(t *testing.T) {
	rt := newGoalRuntime(t)
	cr := NewCommandRegistry(rt)
	// 绑定一个会话并 abort 保留历史
	ctx := context.Background()
	ch, _, err := rt.ProcessInput(ctx, "历史目标")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	for range ch {
	}
	rt.GoalOnRoundEnd(`[GOAL_PLAN]
[{"id":"s1","desc":"步骤1"}]
[/GOAL_PLAN]`, true, 70, types.OverseerReport{})
	id := rt.GoalSession().ID
	cr.Execute("/goal abort")

	// list
	result, _ := cr.Execute("/goal list")
	if !strings.Contains(result, "历史目标") {
		t.Errorf("list 应包含目标名, got %q", result)
	}

	// resume（先切走）
	cr.Execute("/mode chat")
	if rt.GoalSession() != nil {
		t.Fatal("chat 模式不应有活动会话")
	}
	result, _ = cr.Execute("/goal resume " + id)
	if !strings.Contains(result, "已恢复目标") {
		t.Errorf("resume 应提示已恢复, got %q", result)
	}
	if rt.GetMode() != "goal" {
		t.Errorf("resume 后 mode 应为 goal, got %s", rt.GetMode())
	}
	if rt.GoalSession() == nil || rt.GoalSession().ID != id {
		t.Fatal("resume 应加载指定会话")
	}
}
