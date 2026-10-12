package executor

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// task 模式上下文注入测试（用户反馈：task 模式拿不到 chat 上下文）
// =============================================================================

func newTaskTestAgent(history []types.Message) *Agent {
	return &Agent{
		tools:               tools.NewRegistry(),
		conversationHistory: history,
	}
}

func TestBuildMessages_TaskModeInjectsHistory(t *testing.T) {
	a := newTaskTestAgent([]types.Message{
		{Role: "user", Content: "请质检刚才对上下文管理的修改"},
		{Role: "assistant", Content: "已完成 ctxmgr 包实现，测试全绿"},
	})
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "质检 ctxmgr 修改",
		Mode:      "task",
	}

	messages := a.buildMessages(task, nil)
	if len(messages) == 0 {
		t.Fatal("expected messages")
	}

	// 应存在注入对话历史的 system 消息
	var found bool
	var injected string
	for _, m := range messages {
		content, _ := m.Content.(string)
		if strings.Contains(content, "[对话历史]") {
			found = true
			injected = content
			break
		}
	}
	if !found {
		t.Fatal("task 模式应注入对话历史（[对话历史] 标记缺失）")
	}
	if !strings.Contains(injected, "请质检刚才对上下文管理的修改") {
		t.Error("注入的历史应包含用户消息内容")
	}
	if !strings.Contains(injected, "已完成 ctxmgr 包实现") {
		t.Error("注入的历史应包含 assistant 回复内容")
	}
}

func TestBuildMessages_TaskModeNoHistory(t *testing.T) {
	a := newTaskTestAgent(nil) // 无历史
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "简单任务",
		Mode:      "task",
	}
	messages := a.buildMessages(task, nil)
	for _, m := range messages {
		content, _ := m.Content.(string)
		if strings.Contains(content, "[对话历史]") {
			t.Error("无历史时不应注入对话历史标记")
		}
	}
}

func TestBuildMessages_TaskModeHistoryNotInChatMode(t *testing.T) {
	a := newTaskTestAgent([]types.Message{
		{Role: "user", Content: "历史消息"},
	})
	// chat 模式：历史通过原样消息列表注入（不包 [对话历史] 标记）
	task := types.TaskCreate{
		Directive: "CHAT",
		Task:      "你好",
		Mode:      "chat",
	}
	messages := a.buildMessages(task, nil)
	for _, m := range messages {
		content, _ := m.Content.(string)
		if strings.Contains(content, "[对话历史]") {
			t.Error("chat 模式不应使用 task 的 [对话历史] 包装标记")
		}
	}
	// chat 模式原样注入历史消息
	var foundUser bool
	for _, m := range messages {
		if m.Role == "user" && m.Content == "历史消息" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Error("chat 模式应原样注入历史消息")
	}
}

func TestBuildMessagesWithManifest_InjectsParentHistory(t *testing.T) {
	a := newTaskTestAgent([]types.Message{
		{Role: "user", Content: "父会话：我们刚讨论过质检方案"},
		{Role: "assistant", Content: "父会话：已确认方案可行"},
	})
	task := types.TaskCreate{
		Type:      "task.create",
		ID:        "sub-1",
		Directive: "CODE_AGENT",
		Task:      "子任务：执行质检",
		Manifest: &types.ContextManifest{
			WorkDir:     "/tmp/proj",
			ProjectRoot: "/tmp/proj",
			Summary:     "子任务摘要",
		},
	}

	messages := a.buildMessagesWithManifest(task)
	if len(messages) == 0 {
		t.Fatal("expected messages")
	}

	var found bool
	var injected string
	for _, m := range messages {
		content, _ := m.Content.(string)
		if strings.Contains(content, "[父对话上下文]") {
			found = true
			injected = content
			break
		}
	}
	if !found {
		t.Fatal("子 Agent 应注入父对话上下文（[父对话上下文] 标记缺失）")
	}
	if !strings.Contains(injected, "我们刚讨论过质检方案") {
		t.Error("注入的父上下文应包含父会话用户消息")
	}
	if !strings.Contains(injected, "已确认方案可行") {
		t.Error("注入的父上下文应包含父会话 assistant 消息")
	}
}

func TestCompactHistoryForTask_Empty(t *testing.T) {
	a := newTaskTestAgent(nil)
	if got := a.compactHistoryForTask(); got != nil {
		t.Errorf("无历史时 compactHistoryForTask 应返回 nil, got %v", got)
	}
}

func TestCompactHistoryForTask_UnderBudget(t *testing.T) {
	a := newTaskTestAgent([]types.Message{
		{Role: "user", Content: "短消息1"},
		{Role: "assistant", Content: "短消息2"},
	})
	got := a.compactHistoryForTask()
	if len(got) != 2 {
		t.Errorf("历史在预算内应原样返回, got %d 条", len(got))
	}
}
