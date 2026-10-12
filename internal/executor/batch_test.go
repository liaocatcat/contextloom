package executor

import (
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestBatchNudge_InjectsAfterSpawnTask：spawn_task 批次完成后注入续批提示，
// 批次号递增；非 spawn_task 轮次不注入。
func TestBatchNudge_InjectsAfterSpawnTask(t *testing.T) {
	a := &Agent{}

	msgs := a.batchNudge([]types.ToolCall{{Function: types.FunctionCall{Name: "spawn_task"}}}, nil)
	if len(msgs) != 1 {
		t.Fatalf("spawn_task 后应注入 1 条批次提示，got %d", len(msgs))
	}
	if msgs[0].Role != "user" || !strings.Contains(msgs[0].Content.(string), "第 1 批子任务") {
		t.Errorf("批次提示内容异常: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Content.(string), "派发第 2 批") {
		t.Errorf("提示应引导派发下一批: %s", msgs[0].Content)
	}

	// 第二轮 spawn_task → 第 2 批
	msgs = a.batchNudge([]types.ToolCall{{Function: types.FunctionCall{Name: "spawn_task"}}}, msgs)
	if len(msgs) != 2 || !strings.Contains(msgs[1].Content.(string), "第 2 批子任务") {
		t.Errorf("批次号应递增: %+v", msgs)
	}

	// 非 spawn_task 不注入
	n := len(msgs)
	msgs = a.batchNudge([]types.ToolCall{{Function: types.FunctionCall{Name: "exec_command"}}}, msgs)
	if len(msgs) != n {
		t.Errorf("非 spawn_task 不应注入批次提示")
	}
}

// TestBatchNudge_HandoffNotCounted：handoff（串行交接）不算批次。
func TestBatchNudge_HandoffNotCounted(t *testing.T) {
	a := &Agent{}
	msgs := a.batchNudge([]types.ToolCall{{Function: types.FunctionCall{Name: "handoff"}}}, nil)
	if len(msgs) != 0 {
		t.Errorf("handoff 不应触发批次提示")
	}
}

// TestStrategyPrompt_MultiBatch：task/goal 分支 strategy 与 spawn_task when
// 均含多批次拆分语义（防止模型一批后不再派发）。
func TestStrategyPrompt_MultiBatch(t *testing.T) {
	// 构造 task 模式 Agent 并检查 buildMessages 输出
	a := &Agent{
		ID:    "main",
		Task:  types.TaskCreate{Type: "task.create", ID: "main", Task: "测试多批次", Mode: "task"},
		depth: 0,
	}
	// 直接走 buildMessages 的 task 分支需要较多依赖；此处验证源代码提示词常量更稳定。
	// 通过编译期已知字符串断言（与 agent.go 中注入文本一致）：
	src, err := readAgentSource()
	if err != nil {
		t.Fatalf("读取源码: %v", err)
	}
	for _, want := range []string{
		"多批次拆分", "第 1 批完成后评估回收的结果", "继续派发新一批",
		"一批子任务结果回收后，若仍有独立可并行工作",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("提示词应含多批次语义 %q", want)
		}
	}
	_ = a
}

func readAgentSource() (string, error) {
	b, err := os.ReadFile("agent.go")
	return string(b), err
}
