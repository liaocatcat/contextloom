package executor

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── v2 插件钩子注入测试（message 每轮注入 / route 路由决策）──

func TestBuildMessages_Chat_MessageHookInjected(t *testing.T) {
	a := &Agent{
		pluginMessageHook: func(input string, round int) string {
			if input != "hello" {
				t.Errorf("hook input = %q, want hello", input)
			}
			return "[引导] 请先规划再执行"
		},
		runRound: 2,
	}
	task := types.TaskCreate{
		Directive: "CHAT",
		Task:      "hello",
		Payload:   "hello",
	}
	messages := a.buildMessages(task, nil)
	last := messages[len(messages)-1]
	if last.Role != "user" {
		t.Fatalf("钩子注入消息 role = %q, want user（应随用户消息之后）", last.Role)
	}
	if s, ok := last.Content.(string); !ok || !strings.Contains(s, "[引导]") {
		t.Errorf("钩子内容 = %v, want 含引导段", last.Content)
	}
	// 用户消息（Payload）在钩子之前
	if len(messages) < 2 {
		t.Fatalf("消息数不足: %d", len(messages))
	}
	prev := messages[len(messages)-2]
	if s, ok := prev.Content.(string); !ok || !strings.Contains(s, "hello") {
		t.Errorf("钩子前一条应为用户消息, got %v", prev.Content)
	}
}

func TestBuildMessages_Chat_MessageHookEmpty_NotInjected(t *testing.T) {
	a := &Agent{
		pluginMessageHook: func(input string, round int) string { return "" },
	}
	task := types.TaskCreate{Directive: "CHAT", Task: "x", Payload: "x"}
	messages := a.buildMessages(task, nil)
	last := messages[len(messages)-1]
	if s, ok := last.Content.(string); ok && strings.Contains(s, "[引导]") {
		t.Error("空输出不应注入")
	}
}

func TestBuildMessages_Chat_RouteHookInjected(t *testing.T) {
	a := &Agent{
		pluginRouteHook: func(mode, input string, round int) string {
			// chat 模式 TaskCreate.Mode 为空字符串（仅 task/goal 有显式值）
			if mode != "" {
				t.Errorf("hook mode = %q, want 空（chat）", mode)
			}
			return "[路由] 采用 spec 模式：先计划后执行"
		},
	}
	task := types.TaskCreate{Directive: "CHAT", Task: "x", Payload: "x"}
	messages := a.buildMessages(task, nil)
	// route 注入为 system role，且位于 pluginInject 之后、用户消息之前
	found := false
	for _, m := range messages {
		if m.Role == "system" {
			if s, ok := m.Content.(string); ok && strings.Contains(s, "[路由]") {
				found = true
			}
		}
	}
	if !found {
		t.Error("route 钩子输出未注入 system 区")
	}
}

func TestBuildMessages_Task_MessageHookInjected(t *testing.T) {
	a := &Agent{
		tools: tools.NewRegistry(),
		pluginMessageHook: func(input string, round int) string {
			return "[引导] task 模式注入"
		},
	}
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "实现登录功能",
		Mode:      "task",
		Payload:   map[string]any{"task": "实现登录功能"},
	}
	messages := a.buildMessages(task, nil)
	last := messages[len(messages)-1]
	if s, ok := last.Content.(string); !ok || !strings.Contains(s, "[引导]") {
		t.Errorf("task 模式钩子未注入: %v", last.Content)
	}
}

func TestBuildMessages_Goal_MessageHookInjected(t *testing.T) {
	a := &Agent{
		tools: tools.NewRegistry(),
		pluginMessageHook: func(input string, round int) string {
			return "[引导] goal 模式注入"
		},
	}
	task := types.TaskCreate{
		Directive: "GOAL",
		Task:      "完成目标",
		Mode:      "goal",
		Payload:   &types.GoalSession{Round: 1, MaxRounds: 5},
	}
	messages := a.buildMessages(task, nil)
	last := messages[len(messages)-1]
	if s, ok := last.Content.(string); !ok || !strings.Contains(s, "[引导]") {
		t.Errorf("goal 模式钩子未注入: %v", last.Content)
	}
}

func TestBuildMessages_NoHooks_NoInjection(t *testing.T) {
	a := &Agent{}
	task := types.TaskCreate{Directive: "CHAT", Task: "x", Payload: "x"}
	messages := a.buildMessages(task, nil)
	for _, m := range messages {
		if s, ok := m.Content.(string); ok && (strings.Contains(s, "[引导]") || strings.Contains(s, "[路由]")) {
			t.Errorf("未设置钩子却注入了插件内容: %v", s)
		}
	}
}

// round 同步：Run/RunStream 循环中 runRound 随 round++ 递增
func TestAgent_RunRoundSync(t *testing.T) {
	a := &Agent{}
	if a.runRound != 0 {
		t.Errorf("初始 runRound = %d, want 0", a.runRound)
	}
	// 验证 buildMessages 中钩子收到的 round 与设置一致
	a.pluginMessageHook = func(input string, round int) string {
		if round != 3 {
			t.Errorf("hook round = %d, want 3", round)
		}
		return ""
	}
	a.runRound = 3
	a.buildMessages(types.TaskCreate{Directive: "CHAT", Task: "x", Payload: "x"}, nil)
}

// ── tool_scope 工具范围约束测试（对标 dsh 极简模式）──

func TestPluginRouteMessages_JSONWithToolScope(t *testing.T) {
	reg := tools.NewRegistry()
	a := &Agent{
		tools: reg,
		pluginRouteHook: func(mode, input string, round int) string {
			return `{"prompt":"【路由】首轮极简锁定","tool_scope":"core"}`
		},
	}
	task := types.TaskCreate{Directive: "CHAT", Task: "x", Payload: "x"}
	msgs := a.pluginRouteMessages(task)
	if len(msgs) != 1 {
		t.Fatalf("JSON 路由注入消息数 = %d, want 1", len(msgs))
	}
	if s, ok := msgs[0].Content.(string); !ok || !strings.Contains(s, "【路由】") {
		t.Errorf("路由提示词内容 = %v", msgs[0].Content)
	}
	if a.toolScope != "core" {
		t.Errorf("toolScope = %q, want core", a.toolScope)
	}
}

func TestRouteMessages_PlainText_NoToolScope(t *testing.T) {
	a := &Agent{
		pluginRouteHook: func(mode, input string, round int) string {
			return "[路由] 普通文本"
		},
	}
	task := types.TaskCreate{Directive: "CHAT", Task: "x", Payload: "x"}
	msgs := a.pluginRouteMessages(task)
	if len(msgs) != 1 {
		t.Fatalf("注入消息数 = %d, want 1", len(msgs))
	}
	if a.toolScope != "" {
		t.Errorf("纯文本路由 toolScope = %q, want 空（无约束）", a.toolScope)
	}
}

func TestToolNames_ScopeFiltering(t *testing.T) {
	reg := tools.NewRegistry()
	a := &Agent{tools: reg}
	all := a.toolNames()
	if len(all) == 0 {
		t.Fatal("注册表工具列表为空")
	}
	got := make(map[string]bool)
	for _, n := range all {
		got[n] = true
	}
	// core 范围：read_file 应保留，web_fetch 应被过滤
	a.toolScope = "core"
	coreNames := a.toolNames()
	if !contains(coreNames, "read_file") {
		t.Error("core 范围应包含 read_file")
	}
	if len(coreNames) >= len(all) && len(coreNames) == len(all) {
		t.Error("core 范围不应与全部工具相同（至少过滤部分工具）")
	}
	// read 范围：web_fetch 保留，write_file 被过滤
	a.toolScope = "read"
	readNames := a.toolNames()
	if !contains(readNames, "web_fetch") {
		t.Error("read 范围应包含 web_fetch")
	}
	if contains(readNames, "write_file") {
		t.Error("read 范围不应包含 write_file")
	}
	// full 与空：全部放行
	a.toolScope = "full"
	fullNames := a.toolNames()
	if len(fullNames) != len(all) {
		t.Errorf("full 范围工具数 = %d, want %d", len(fullNames), len(all))
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
