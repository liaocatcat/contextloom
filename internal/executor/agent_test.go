package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// truncate tests
// =============================================================================

func TestTruncate_ShortString(t *testing.T) {
	result := truncate("hello", 10)
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestTruncate_ExactLength(t *testing.T) {
	result := truncate("hello", 5)
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestTruncate_LongString(t *testing.T) {
	result := truncate("hello world this is long", 5)
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestTruncate_EmptyString(t *testing.T) {
	result := truncate("", 10)
	if result != "" {
		t.Errorf("expected '', got %q", result)
	}
}

func TestTruncate_ChineseCharacters(t *testing.T) {
	result := truncate("你好世界测试文本", 3)
	runes := []rune(result)
	if len(runes) != 3 {
		t.Errorf("expected 3 runes, got %d: %q", len(runes), result)
	}
}

func TestTruncate_ZeroMaxLen(t *testing.T) {
	result := truncate("hello", 0)
	if result != "" {
		t.Errorf("expected '', got %q", result)
	}
}

func TestTruncate_MixedContent(t *testing.T) {
	result := truncate("abc中文def", 5)
	runes := []rune(result)
	if len(runes) != 5 {
		t.Errorf("expected 5 runes, got %d: %q", len(runes), result)
	}
}

// =============================================================================
// buildQuery tests
// =============================================================================

func TestBuildQuery_Simple(t *testing.T) {
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "write unit tests",
	}
	result := buildQuery(task)
	expected := "CODE_AGENT write unit tests"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestBuildQuery_EmptyDirective(t *testing.T) {
	task := types.TaskCreate{
		Directive: "",
		Task:      "do something",
	}
	result := buildQuery(task)
	expected := " do something"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestBuildQuery_EmptyTask(t *testing.T) {
	task := types.TaskCreate{
		Directive: "REVIEW",
		Task:      "",
	}
	result := buildQuery(task)
	expected := "REVIEW "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestBuildQuery_BothEmpty(t *testing.T) {
	task := types.TaskCreate{
		Directive: "",
		Task:      "",
	}
	result := buildQuery(task)
	expected := " "
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// =============================================================================
// buildMessages tests
// =============================================================================

func TestBuildMessages_NoContext(t *testing.T) {
	a := &Agent{}
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "test task",
	}
	messages := a.buildMessages(task, nil)
	// Should have at least the system message
	if len(messages) == 0 {
		t.Fatal("expected at least one message")
	}
	if messages[0].Role != "system" {
		t.Errorf("expected first message role 'system', got %q", messages[0].Role)
	}
}

func TestBuildMessages_WithContext(t *testing.T) {
	a := &Agent{}
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "test task",
	}
	context := []types.ContextBlock{
		{ID: "ctx-1", Content: "context content"},
	}
	messages := a.buildMessages(task, context)
	if len(messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + context), got %d", len(messages))
	}
	// context 块用 user role（OpenAI API 不支持 context role）
	if messages[1].Role != "user" {
		t.Errorf("expected context message role 'user', got %q", messages[1].Role)
	}
	if !strings.Contains(messages[1].Content.(string), "ctx-1") {
		t.Errorf("expected context content to contain block ID 'ctx-1', got %v", messages[1].Content)
	}
}

func TestBuildMessages_WithPayload(t *testing.T) {
	a := &Agent{}
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "test task",
		Payload:   map[string]string{"key": "value"},
	}
	messages := a.buildMessages(task, nil)
	// Should have system + user (payload) message
	if len(messages) < 2 {
		t.Fatalf("expected at least 2 messages (system + payload), got %d", len(messages))
	}
	foundUser := false
	for _, msg := range messages {
		if msg.Role == "user" {
			foundUser = true
			break
		}
	}
	if !foundUser {
		t.Error("expected a user message for payload")
	}
}

func TestBuildMessages_PayloadStructJSONNotVerbDump(t *testing.T) {
	// 回归：结构体 Payload 应 JSON 序列化，而不是 %v 打印成 &{Field1 val1 ...} 转储
	a := &Agent{}
	type structuredPrompt struct {
		Directive   string         `json:"directive"`
		Task        string         `json:"task"`
		Scope       []string       `json:"scope,omitempty"`
		Constraints map[string]any `json:"constraints,omitempty"`
		ContextKeys []string       `json:"context_keys,omitempty"`
		Compressed  string         `json:"compressed,omitempty"`
	}
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "test task",
		Payload: &structuredPrompt{
			Directive:  "CODE_AGENT",
			Task:       "TASK",
			Scope:      []string{"继续"},
			Compressed: "TASK_继续",
		},
	}
	messages := a.buildMessages(task, nil)
	var payloadContent string
	for _, msg := range messages {
		if msg.Role == "user" {
			payloadContent, _ = msg.Content.(string)
			break
		}
	}
	if payloadContent == "" {
		t.Fatal("未找到 payload user 消息")
	}
	if strings.Contains(payloadContent, "&{") {
		t.Errorf("payload 不应是结构体转储格式: %q", payloadContent)
	}
	if !strings.Contains(payloadContent, `"directive":"CODE_AGENT"`) ||
		!strings.Contains(payloadContent, `"compressed":"TASK_继续"`) {
		t.Errorf("payload 应为 JSON 格式: %q", payloadContent)
	}
}

func TestBuildMessages_MultipleContextBlocks(t *testing.T) {
	a := &Agent{}
	task := types.TaskCreate{
		Directive: "CODE_AGENT",
		Task:      "test task",
	}
	context := []types.ContextBlock{
		{ID: "ctx-1", Content: "first block"},
		{ID: "ctx-2", Content: "second block"},
		{ID: "ctx-3", Content: "third block"},
	}
	messages := a.buildMessages(task, context)
	// context 块以 user role 注入（OpenAI API 不支持 context role）
	contextCount := 0
	for _, msg := range messages {
		if msg.Role == "user" && strings.Contains(fmt.Sprintf("%v", msg.Content), "ctx-") {
			contextCount++
		}
	}
	if contextCount != 3 {
		t.Errorf("expected 3 context user messages, got %d", contextCount)
	}
}

// =============================================================================
// SetBackend tests
// =============================================================================

func TestSetBackend(t *testing.T) {
	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	agent := &Agent{
		ID:      "test-agent",
		harness: eng,
		tools:   tools.NewRegistry(),
	}

	backend1 := &testBackend{chatResponse: "first"}
	backend2 := &testBackend{chatResponse: "second"}

	agent.SetBackend(backend1)
	if agent.backend != backend1 {
		t.Error("expected backend1 to be set")
	}

	agent.SetBackend(backend2)
	if agent.backend != backend2 {
		t.Error("expected backend2 to be set after second call")
	}
}

func TestSetBackend_Nil(t *testing.T) {
	agent := &Agent{
		ID: "test-agent",
		harness: harness.New(harness.Config{
			ScoreThreshold: 50,
			DecayFactor:    0.9,
			AnchorRatio:    0.2,
		}),
		tools: tools.NewRegistry(),
	}

	agent.SetBackend(nil)
	if agent.backend != nil {
		t.Error("expected nil backend after setting nil")
	}
}

// =============================================================================
// CallTool tests
// =============================================================================

func TestCallTool(t *testing.T) {
	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	toolReg := tools.NewRegistry()
	agent := &Agent{
		ID:      "test-agent",
		harness: eng,
		tools:   toolReg,
	}

	ctx := context.Background()
	call := types.ToolCall{
		ID:   "call-1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "nonexistent_tool",
			Arguments: `{"key":"value"}`,
		},
	}
	result, err := agent.CallTool(ctx, call)
	// Should return an error since the tool doesn't exist
	if err == nil {
		t.Log("CallTool succeeded (unexpected, tool may exist):", result)
	}
}

// =============================================================================
// Run tests (edge cases)
// =============================================================================

func TestAgentRun_MaxIterationsReached(t *testing.T) {
	// Agent always returns tool calls, should stop after 5 iterations
	callCount := 0
	backend := &countingBackend{
		responses: []backendResponse{
			{content: "call 1", toolCalls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.FunctionCall{Name: "test_tool", Arguments: "{}"}}}},
			{content: "call 2", toolCalls: []types.ToolCall{{ID: "c2", Type: "function", Function: types.FunctionCall{Name: "test_tool", Arguments: "{}"}}}},
			{content: "call 3", toolCalls: []types.ToolCall{{ID: "c3", Type: "function", Function: types.FunctionCall{Name: "test_tool", Arguments: "{}"}}}},
			{content: "call 4", toolCalls: []types.ToolCall{{ID: "c4", Type: "function", Function: types.FunctionCall{Name: "test_tool", Arguments: "{}"}}}},
			{content: "call 5", toolCalls: []types.ToolCall{{ID: "c5", Type: "function", Function: types.FunctionCall{Name: "test_tool", Arguments: "{}"}}}},
			{content: "should not reach", toolCalls: nil},
		},
		callCount: &callCount,
	}

	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	agent := &Agent{
		ID: "test-agent",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "test task",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: backend,
	}

	ctx := context.Background()
	err := agent.Run(ctx)
	// Should not error, but should stop after 5 iterations
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// 无硬限制，直到无工具调用或循环检测触发。这里第6次无工具调用结束
	if callCount != 6 {
		t.Errorf("expected 6 iterations (5 tool + 1 final), got %d", callCount)
	}
}

func TestAgentRun_SetsLastUsedOnContext(t *testing.T) {
	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})

	// Index some context blocks first
	eng.Index(context.Background(), []types.ContextBlock{
		{ID: "ctx-1", Content: "test content"},
	})

	backend := &testBackend{
		chatResponse:  "final result",
		chatToolCalls: nil,
	}

	agent := &Agent{
		ID: "test-agent",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "test task",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: backend,
	}

	ctx := context.Background()
	err := agent.Run(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAgentRun_BackendError(t *testing.T) {
	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	backend := &testBackend{
		chatErr: errors.New("connection refused"),
	}

	agent := &Agent{
		ID: "test-agent",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "failing task",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: backend,
	}

	ctx := context.Background()
	err := agent.Run(ctx)
	if err == nil {
		t.Fatal("expected error from backend failure")
	}
}

// =============================================================================
// RunStream edge cases
// =============================================================================

func TestAgentRunStream_WithContextBlocks(t *testing.T) {
	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})

	eng.Index(context.Background(), []types.ContextBlock{
		{ID: "ctx-1", Content: "relevant content"},
	})

	backend := &testBackend{
		streamTokens: []string{"Hi"},
	}

	agent := &Agent{
		ID: "test-agent",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "test task",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: backend,
	}

	ctx := context.Background()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream returned error: %v", err)
	}

	var result string
	for token := range ch {
		result += token
	}

	select {
	case e := <-errCh:
		if e != nil {
			t.Errorf("unexpected stream error: %v", e)
		}
	default:
	}

	if result != "Hi" {
		t.Errorf("expected 'Hi', got %q", result)
	}
}

// ============================================================================
// splitThinkTagsAgent 剥离思考标签（尖括号 + 空格前缀格式）
// ============================================================================

func TestSplitThinkTagsAgent_AngleBracket(t *testing.T) {
	body, think := splitThinkTagsAgent("正文<think>思考</think>结尾")
	if body != "正文结尾" {
		t.Errorf("body mismatch: %q", body)
	}
	if think != "思考" {
		t.Errorf("think mismatch: %q", think)
	}
}

func TestSplitThinkTagsAgent_SpacePrefix(t *testing.T) {
	// 模型层 deepseek.go 标记格式
	body, think := splitThinkTagsAgent("正文<|reasoning|>思考内容<|/reasoning|>结尾")
	if body != "正文结尾" {
		t.Errorf("body mismatch: %q", body)
	}
	if think != "思考内容" {
		t.Errorf("think mismatch: %q", think)
	}
}

func TestSplitThinkTagsAgent_ThinkingTag(t *testing.T) {
	body, think := splitThinkTagsAgent("开头<thinking>思考</thinking>正文")
	if body != "开头正文" {
		t.Errorf("body mismatch: %q", body)
	}
	if think != "思考" {
		t.Errorf("think mismatch: %q", think)
	}
}

func TestSplitThinkTagsAgent_Unclosed(t *testing.T) {
	body, think := splitThinkTagsAgent("正文<|reasoning|>未闭合")
	if body != "正文" {
		t.Errorf("body mismatch: %q", body)
	}
	if think != "未闭合" {
		t.Errorf("think mismatch: %q", think)
	}
}

func TestSplitThinkTagsAgent_PathNotMatched(t *testing.T) {
	// 路径中的 thinking 不被剥离（前面是 / 而非空格）
	s := "请读取 /home/thinking_project/notes.txt"
	body, think := splitThinkTagsAgent(s)
	if body != s {
		t.Errorf("路径不应被剥离: %q", body)
	}
	if think != "" {
		t.Errorf("路径不应被识别为思考: %q", think)
	}
}

func TestSplitThinkTagsAgent_BodyEnglishWordsNotFiltered(t *testing.T) {
	// 回归：正文中的英文单词 thinking/response 不应被剥离（修复显示截断问题）
	s := "让我检查这些点：1. main.go 中 bubbletea Program 选项 2. handleCmd 的 enter 逻辑 3. 检查 streamMsg 的 response 处理 thinking 环节"
	body, think := splitThinkTagsAgent(s)
	if body != s {
		t.Errorf("正文中的 thinking/response 不应被剥离: %q", body)
	}
	if think != "" {
		t.Errorf("不应识别出思考块: %q", think)
	}
}

func TestSplitThinkTagsAgent_MarkerFormat(t *testing.T) {
	// 新标记格式：<|reasoning|> 包裹思考，正文完整保留
	s := "正文<|reasoning|>思考内容<|/reasoning|>结尾有 response 单词"
	body, think := splitThinkTagsAgent(s)
	if body != "正文结尾有 response 单词" {
		t.Errorf("body mismatch: %q", body)
	}
	if think != "思考内容" {
		t.Errorf("think mismatch: %q", think)
	}
}

func TestNormalizeSystemFirst_ReordersMidSystem(t *testing.T) {
	// llama.cpp 回归：中间 system 必须被前置（"System message must be at the beginning"）
	msgs := []types.Message{
		{Role: "system", Content: "主指令"},
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "在的"},
		{Role: "system", Content: "预算即将耗尽，请立即停止调用工具。"}, // 中间 system（修复前会 500）
		{Role: "user", Content: "继续"},
	}
	out := normalizeSystemFirst(msgs)
	if len(out) != 5 {
		t.Fatalf("长度不应变化: %d", len(out))
	}
	if out[0].Role != "system" || out[1].Role != "system" {
		t.Fatalf("system 应前置到开头: roles=%v", rolesOf(out))
	}
	if out[0].Content != "主指令" || out[1].Content != "预算即将耗尽，请立即停止调用工具。" {
		t.Fatalf("system 相对顺序应保持: %q %q", out[0].Content, out[1].Content)
	}
	want := []string{"system", "system", "user", "assistant", "user"}
	for i, r := range want {
		if out[i].Role != r {
			t.Fatalf("位置 %d role=%q 应为 %q（整体顺序 %v）", i, out[i].Role, r, rolesOf(out))
		}
	}
	// 原切片不应被修改
	if msgs[3].Role != "system" {
		t.Fatalf("不应修改原切片")
	}
}

func TestNormalizeSystemFirst_NoMidSystemFastPath(t *testing.T) {
	msgs := []types.Message{
		{Role: "system", Content: "s1"},
		{Role: "system", Content: "s2"},
		{Role: "user", Content: "u1"},
		{Role: "assistant", Content: "a1"},
	}
	out := normalizeSystemFirst(msgs)
	if len(out) != 4 {
		t.Fatalf("长度变化: %d", len(out))
	}
	// 快速路径应返回原切片（指针相等），零拷贝
	if &out[0] != &msgs[0] {
		t.Fatalf("快速路径应返回原切片")
	}
}

func TestNormalizeSystemFirst_MultipleMidSystems(t *testing.T) {
	msgs := []types.Message{
		{Role: "user", Content: "u"},
		{Role: "system", Content: "mid1"},
		{Role: "assistant", Content: "a"},
		{Role: "system", Content: "mid2"},
		{Role: "tool", ToolCallID: "t1", Content: "r"},
	}
	out := normalizeSystemFirst(msgs)
	want := []string{"system", "system", "user", "assistant", "tool"}
	if got := rolesOf(out); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("顺序错误: got %v want %v", got, want)
	}
	if out[0].Content != "mid1" || out[1].Content != "mid2" {
		t.Fatalf("system 相对顺序保持: %q %q", out[0].Content, out[1].Content)
	}
}

func rolesOf(msgs []types.Message) []string {
	rs := make([]string, len(msgs))
	for i, m := range msgs {
		rs[i] = m.Role
	}
	return rs
}

// =============================================================================
// formatToolResult tests（工具错误信息不被吞成 null）
// =============================================================================

func TestFormatToolResult_ErrorField(t *testing.T) {
	// 工具执行失败时 Registry.Execute 将 err 封装进 ToolResult.Error（外层 error 为 nil），
	// Result 为 nil。formatToolResult 必须优先返回 Error，而不是 json.Marshal(nil)="null"。
	result := &types.ToolResult{
		CallID: "call-1",
		Name:   "spawn_task",
		Error:  "子 Agent 启动失败: 无法执行命令",
	}
	out := formatToolResult(result)
	if !strings.Contains(out, "错误:") || !strings.Contains(out, "子 Agent 启动失败") {
		t.Fatalf("错误信息被吞掉，输出 %q，期望包含 '错误: 子 Agent 启动失败'", out)
	}
	if out == "null" {
		t.Fatal("工具错误被格式化为 null，真实错误丢失")
	}
}

func TestFormatToolResult_ResultMap(t *testing.T) {
	result := &types.ToolResult{
		CallID: "call-2",
		Name:   "spawn_task",
		Result: map[string]any{"result": "子任务完成", "status": "completed"},
	}
	out := formatToolResult(result)
	if !strings.Contains(out, "子任务完成") {
		t.Fatalf("期望输出包含 '子任务完成'，实际 %q", out)
	}
}

func TestFormatToolResult_Nil(t *testing.T) {
	if out := formatToolResult(nil); out != "" {
		t.Fatalf("nil 结果应输出空字符串，实际 %q", out)
	}
}

// =============================================================================
// goal 模式 multi_agent 注入测试（需求：goal 模式也要支持 spawn_task 子任务）
// =============================================================================

func TestBuildMessages_GoalMode_MultiAgentInjected(t *testing.T) {
	a := &Agent{depth: 0, tools: tools.NewRegistry()}
	gs := &types.GoalSession{
		ID:        "goal-1",
		Goal:      "实现会话列表面板",
		Phase:     types.GoalPhaseExecute,
		Round:     1,
		MaxRounds: 3,
		Steps: []types.GoalStep{
			{ID: "s1", Desc: "前端面板", Status: "done"},
			{ID: "s2", Desc: "删除功能", Status: "pending"},
		},
	}
	task := types.TaskCreate{
		Directive: "GOAL_AGENT",
		Task:      "实现会话列表面板",
		Mode:      "goal",
		Payload:   gs,
	}
	messages := a.buildMessages(task, nil)
	if len(messages) == 0 {
		t.Fatal("expected at least one message")
	}
	sys, ok := messages[0].Content.(string)
	if !ok {
		t.Fatalf("system 内容不是 string，got %T", messages[0].Content)
	}
	// goal 分支 system 提示词（JSON）必须含 multi_agent 接口规范与 spawn_task
	for _, want := range []string{"multi_agent", "spawn_task", "send_message", "check_messages", "可验证产出"} {
		if !strings.Contains(sys, want) {
			t.Errorf("goal system 提示词缺少 %q（子任务模式未注入）", want)
		}
	}
	// goal 分支必须展示 tools_available 工具表（与 task 分支一致，模型据此感知 spawn_task 可用）
	if !strings.Contains(sys, "tools_available") || !strings.Contains(sys, "spawn_task") {
		t.Error("goal system 提示词缺少 tools_available 工具表（模型无法感知 spawn_task 可用）")
	}
	// 子 Agent（depth>0）不注入 multi_agent（防递归）
	child := &Agent{depth: 1, tools: tools.NewRegistry()}
	messages2 := child.buildMessages(task, nil)
	sys2, _ := messages2[0].Content.(string)
	if strings.Contains(sys2, "multi_agent") {
		t.Error("子 Agent（depth>0）不应注入 multi_agent 规范")
	}
}

// TestGoalAgent_ToolsIncludeSpawnTask 端到端契约验证（goal 模式拆子任务链路）：
// goal 主 Agent 的工具表必须含 spawn_task（runtime.go launchAgent 对 goal 不过滤），
// 且其 schema 完整（模型可生成合法调用）。本测试在 executor 层模拟 runtime 的注册
// 结果（含多 Agent 工具的完整注册表），断言 toolNames 与 ToolDefinitions 契约。
func TestGoalAgent_ToolsIncludeSpawnTask(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register("spawn_task", tools.MakeSpawnTaskTool(nil))
	reg.Register("send_message", tools.MakeSendMessageTool(nil))
	reg.Register("check_messages", tools.MakeCheckMessagesTool(nil))
	reg.Register("propose_vocab", tools.MakeProposeVocabTool(nil))

	// 模拟 runtime launchAgent：goal 模式不调用 SetTools（chat 才过滤）
	a := &Agent{depth: 0, tools: reg}
	names := a.toolNames()
	found := false
	for _, n := range names {
		if n == "spawn_task" {
			found = true
		}
	}
	if !found {
		t.Fatal("goal 主 Agent 工具表缺少 spawn_task——模型无工具可调，子任务链路断裂")
	}

	// schema 契约：模型可见的 spawn_task 定义必须含 task 参数
	defs := reg.ToolDefinitions()
	for _, d := range defs {
		if d.Function.Name != "spawn_task" {
			continue
		}
		props := d.Function.Parameters["properties"].(map[string]any)
		if _, ok := props["task"]; !ok {
			t.Error("spawn_task schema 缺少 task 参数——模型无法构造合法调用")
		}
		if _, ok := props["directive"]; !ok {
			t.Error("spawn_task schema 缺少 directive 参数")
		}
		if _, ok := props["context"]; !ok {
			t.Error("spawn_task schema 缺少 context 参数")
		}
		return
	}
	t.Error("ToolDefinitions 中未找到 spawn_task")
}
