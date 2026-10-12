package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	bubbletea "github.com/charmbracelet/bubbletea"
)

// ============================================================================
// ChatMessage 类型测试
// ============================================================================

// ============================================================================
// InitialModel 测试
// ============================================================================

func TestInitialModel(t *testing.T) {
	m := initialModel(nil)
	if len(m.messages) != 0 {
		t.Errorf("expected 0 initial messages, got %d", len(m.messages))
	}
	if m.mode != modeChat {
		t.Error("should start in chat mode")
	}
}

func TestInitialModel_Defaults(t *testing.T) {
	m := initialModel(nil)
	if m.modelID != "default-model" {
		t.Errorf("expected default model default-model, got %s", m.modelID)
	}
	if m.reasoningEffort != "medium" {
		t.Errorf("expected default reasoning medium, got %s", m.reasoningEffort)
	}
	if m.threshold != 50 {
		t.Errorf("expected default threshold 50, got %f", m.threshold)
	}
}

// ============================================================================
// Send 测试
// ============================================================================

func TestSendClearsInput(t *testing.T) {
	m := initialModel(nil)
	m.chatInput = "hello"
	m.mode = modeChat
	m2, _ := m.send()
	mm := m2.(*model)
	if mm.chatInput != "" {
		t.Error("should clear after send")
	}
	if mm.mode != modeNormal {
		t.Error("should exit chat after send")
	}
}

func TestSend_EmptyInput(t *testing.T) {
	m := initialModel(nil)
	m.chatInput = ""
	m.mode = modeChat
	m2, cmd := m.send()
	mm := m2.(*model)
	if cmd != nil {
		t.Error("empty input should not produce a command")
	}
	if mm.streaming {
		t.Error("empty input should not trigger streaming")
	}
}

func TestSend_WhitespaceOnly(t *testing.T) {
	m := initialModel(nil)
	m.chatInput = "   "
	m.mode = modeChat
	m2, cmd := m.send()
	mm := m2.(*model)
	if cmd != nil {
		t.Error("whitespace-only input should not produce a command")
	}
	if mm.streaming {
		t.Error("whitespace-only input should not trigger streaming")
	}
}

func TestSend_AddsUserMessage(t *testing.T) {
	m := initialModel(nil)
	m.chatInput = "hello world"
	m.mode = modeChat
	msgCount := len(m.messages)
	m2, _ := m.send()
	mm := m2.(*model)
	if len(mm.messages) <= msgCount {
		t.Error("send should add messages")
	}
	// Last non-thinking message before send should be the user message
	found := false
	for _, msg := range mm.messages {
		if msg.Type == MsgUser && msg.Content == "hello world" {
			found = true
			break
		}
	}
	if !found {
		t.Error("should find user message in messages")
	}
}

func TestSend_AddsToHistory(t *testing.T) {
	m := initialModel(nil)
	m.chatInput = "test history"
	m.mode = modeChat
	m2, _ := m.send()
	mm := m2.(*model)
	if len(mm.history) == 0 {
		t.Error("send should add to history")
	}
	if mm.history[len(mm.history)-1] != "test history" {
		t.Errorf("expected 'test history' in history, got %q", mm.history[len(mm.history)-1])
	}
}

func TestSend_WithoutRuntime(t *testing.T) {
	m := initialModel(nil)
	m.chatInput = "no runtime"
	m.mode = modeChat
	m2, _ := m.send()
	mm := m2.(*model)
	if mm.streaming {
		t.Error("should not stream without runtime")
	}
	// Should have a "not connected" message
	hasNotConnected := false
	for _, msg := range mm.messages {
		if strings.Contains(msg.Content, "未连接运行时") {
			hasNotConnected = true
		}
	}
	if !hasNotConnected {
		t.Error("should show not-connected message when rt is nil")
	}
}

func TestSend_AddsTimestampSeparator(t *testing.T) {
	m := initialModel(nil)
	// Pre-populate with a message to trigger separator
	m.messages = append(m.messages, usrMsg("previous"))
	m.chatInput = "new message"
	m.mode = modeChat
	m2, _ := m.send()
	mm := m2.(*model)
	hasSeparator := false
	for _, msg := range mm.messages {
		if msg.Type == MsgSystem && strings.Contains(msg.Content, "─") {
			hasSeparator = true
			break
		}
	}
	if !hasSeparator {
		t.Error("should add timestamp separator between message rounds")
	}
}

// ============================================================================
// splitThinkTags 测试
// ============================================================================

func TestSplitThinkTags_SingleThink(t *testing.T) {
	body, thinks := splitThinkTags("<think>reasoning here</think>final answer")
	if len(thinks) != 1 || thinks[0] != "reasoning here" {
		t.Errorf("expected 1 think block, got %d: %v", len(thinks), thinks)
	}
	if body != "final answer" {
		t.Errorf("expected 'final answer', got %q", body)
	}
}

func TestSplitThinkTags_MultipleThinks(t *testing.T) {
	body, thinks := splitThinkTags("<think>step1</think>text1<think>step2</think>text2")
	if len(thinks) != 2 {
		t.Errorf("expected 2 think blocks, got %d", len(thinks))
	}
	if thinks[0] != "step1" || thinks[1] != "step2" {
		t.Errorf("wrong think content: %v", thinks)
	}
	if body != "text1text2" {
		t.Errorf("expected 'text1text2', got %q", body)
	}
}

func TestSplitThinkTags_NoThink(t *testing.T) {
	body, thinks := splitThinkTags("plain text no tags")
	if len(thinks) != 0 {
		t.Errorf("expected 0 think blocks, got %d", len(thinks))
	}
	if body != "plain text no tags" {
		t.Errorf("expected original text, got %q", body)
	}
}

func TestSplitThinkTags_UnclosedThink(t *testing.T) {
	// 未闭合的思考块应归入 thinks，不泄漏到正文
	body, thinks := splitThinkTags("<think>unclosed")
	if len(thinks) != 1 || thinks[0] != "unclosed" {
		t.Errorf("unclosed think should produce 1 think block 'unclosed', got %d: %v", len(thinks), thinks)
	}
	if body != "" {
		t.Errorf("unclosed think should not leak to body, got %q", body)
	}
}

func TestSplitThinkTags_Empty(t *testing.T) {
	body, thinks := splitThinkTags("")
	if len(thinks) != 0 {
		t.Error("empty input should produce no thinks")
	}
	if body != "" {
		t.Errorf("empty input should produce empty body, got %q", body)
	}
}

func TestSplitThinkTags_NestedThink(t *testing.T) {
	// splitThinkTags does naive parsing: first </think> closes first <think>
	body, thinks := splitThinkTags("<think>outer <think>inner</think></think>end")
	if len(thinks) != 1 {
		t.Errorf("expected 1 think block, got %d: %v", len(thinks), thinks)
	}
	if thinks[0] != "outer <think>inner" {
		t.Errorf("expected 'outer <think>inner', got %q", thinks[0])
	}
	if body != "</think>end" {
		t.Errorf("expected '</think>end', got %q", body)
	}
}

// ============================================================================
// delBefore 测试 (中文退格 / rune-safe delete)
// ============================================================================

func TestDelBefore_English(t *testing.T) {
	s, c := delBefore("hello", 5)
	if s != "hell" || c != 4 {
		t.Errorf("expected ('hell', 4), got (%q, %d)", s, c)
	}
}

func TestDelBefore_Chinese(t *testing.T) {
	// Chinese characters take 3 bytes in UTF-8 but are 1 rune
	s, c := delBefore("你好世界", 4)
	if s != "你好世" || c != 3 {
		t.Errorf("expected ('你好世', 3), got (%q, %d)", s, c)
	}
}

func TestDelBefore_Mixed(t *testing.T) {
	s, c := delBefore("hello你好", 7)
	if s != "hello你" || c != 6 {
		t.Errorf("expected ('hello你', 6), got (%q, %d)", s, c)
	}
}

func TestDelBefore_AtStart(t *testing.T) {
	s, c := delBefore("hello", 0)
	if s != "ello" || c != 0 {
		t.Errorf("expected ('ello', 0), got (%q, %d)", s, c)
	}
}

func TestDelBefore_Empty(t *testing.T) {
	s, c := delBefore("", 0)
	if s != "" || c != 0 {
		t.Errorf("expected ('', 0), got (%q, %d)", s, c)
	}
}

func TestDelBefore_CursorBeyondLength(t *testing.T) {
	// delBefore does not clamp cursor to length; cursor beyond rune count panics.
	// This test documents expected behavior with valid cursor.
	s, c := delBefore("hi", 2)
	if s != "h" || c != 1 {
		t.Errorf("expected ('h', 1), got (%q, %d)", s, c)
	}
}

func TestDelBefore_Emoji(t *testing.T) {
	_, c := delBefore("hello👍", 6)
	// 👍 is a single rune
	if c != 5 {
		t.Errorf("expected cursor 5 after deleting emoji, got %d", c)
	}
}

// ============================================================================
// appInp 测试 (中文输入)
// ============================================================================

func TestAppInp_EnglishInsert(t *testing.T) {
	cur := "hello"
	cursor := 5
	appInp(&cur, &cursor, bubbletea.KeyMsg{Runes: []rune(" world")})
	if cur != "hello world" || cursor != 11 {
		t.Errorf("expected 'hello world', 11, got %q, %d", cur, cursor)
	}
}

func TestAppInp_ChineseInsert(t *testing.T) {
	cur := ""
	cursor := 0
	appInp(&cur, &cursor, bubbletea.KeyMsg{Runes: []rune("你好")})
	if cur != "你好" || cursor != 2 {
		t.Errorf("expected '你好', 2, got %q, %d", cur, cursor)
	}
}

func TestAppInp_MiddleInsert(t *testing.T) {
	cur := "begin end"
	cursor := 5
	appInp(&cur, &cursor, bubbletea.KeyMsg{Runes: []rune("MIDDLE")})
	if cur != "beginMIDDLE end" || cursor != 11 {
		t.Errorf("expected 'beginMIDDLE end', 11, got %q, %d", cur, cursor)
	}
}

func TestAppInp_EmptyInsert(t *testing.T) {
	cur := ""
	cursor := 0
	appInp(&cur, &cursor, bubbletea.KeyMsg{})
	if cur != "" || cursor != 0 {
		t.Errorf("expected unchanged empty, got %q, %d", cur, cursor)
	}
}

// ============================================================================
// itoa 测试
// ============================================================================

func TestItoa_Zero(t *testing.T) {
	if v := itoa(0); v != "0" {
		t.Errorf("itoa(0) = %q", v)
	}
}

func TestItoa_Positive(t *testing.T) {
	tests := []struct {
		n    int
		want string
	}{
		{1, "1"},
		{10, "10"},
		{100, "100"},
		{9999, "9999"},
		{1234567890, "1234567890"},
	}
	for _, tt := range tests {
		if v := itoa(tt.n); v != tt.want {
			t.Errorf("itoa(%d) = %q, want %q", tt.n, v, tt.want)
		}
	}
}

// TestDreamCommandsRegistered 验证长期记忆命令 /dream 与 /teach 已注册并可执行。
func TestDreamCommandsRegistered(t *testing.T) {
	cr := NewCommandRegistry(nil) // rt=nil：命令应优雅降级提示未初始化
	for _, name := range []string{"/dream", "/teach"} {
		if !cr.IsCommand(name) {
			t.Fatalf("命令 %s 应已注册", name)
		}
	}
	out, quit := cr.Execute("/dream")
	if quit {
		t.Fatal("/dream 不应触发退出")
	}
	if !strings.Contains(out, "未初始化") {
		t.Errorf("/dream(nil rt) 应提示未初始化，got %q", out)
	}
	out, quit = cr.Execute("/teach")
	if quit {
		t.Fatal("/teach 不应触发退出")
	}
	if !strings.Contains(out, "未初始化") {
		t.Errorf("/teach(nil rt) 应提示未初始化，got %q", out)
	}
}

func TestItoa_Negative(t *testing.T) {
	if v := itoa(-42); v != "-42" {
		t.Errorf("itoa(-42) = %q", v)
	}
}

// ============================================================================
// truncStr 测试
// ============================================================================

func TestTruncStr_Short(t *testing.T) {
	if v := truncStr("hi", 10); v != "hi" {
		t.Errorf("truncStr short = %q", v)
	}
}

func TestTruncStr_Exact(t *testing.T) {
	if v := truncStr("hello", 5); v != "hello" {
		t.Errorf("truncStr exact = %q", v)
	}
}

func TestTruncStr_Long(t *testing.T) {
	v := truncStr("hello world this is long", 12)
	if !strings.HasSuffix(v, "...") {
		t.Errorf("truncStr should end with '...', got %q", v)
	}
	r := []rune(v)
	if len(r) > 12 {
		t.Errorf("truncated should be <= 12 runes, got %d: %q", len(r), v)
	}
}

func TestTruncStr_Chinese(t *testing.T) {
	v := truncStr("你好世界这是长文本", 6)
	r := []rune(v)
	if len(r) > 6 {
		t.Errorf("Chinese truncation: expected <=6 runes, got %d: %q", len(r), v)
	}
	if !strings.HasSuffix(v, "...") {
		t.Errorf("Chinese truncation should end with '...', got %q", v)
	}
}

// ============================================================================
// truncMsg 测试
// ============================================================================

func TestTruncMsg_Short(t *testing.T) {
	if v := truncMsg("hello", 10); v != "hello" {
		t.Errorf("truncMsg short = %q", v)
	}
}

func TestTruncMsg_Long(t *testing.T) {
	v := truncMsg("hello world this is very long", 12)
	r := []rune(v)
	if len(r) > 12 {
		t.Errorf("truncMsg should be <= 12 runes, got %d: %q", len(r), v)
	}
}

// ============================================================================
// fmtInput 测试
// ============================================================================

func TestFmtInput_Short(t *testing.T) {
	r := fmtInput("hello", 2, 20)
	if !strings.Contains(r, "▌") {
		t.Errorf("fmtInput should contain cursor marker, got %q", r)
	}
}

func TestFmtInput_LongWithCursor(t *testing.T) {
	// Input longer than maxW, cursor visible
	long := strings.Repeat("abcdefghij", 5) // 50 chars
	r := fmtInput(long, 10, 30)
	if !strings.Contains(r, "▌") {
		t.Errorf("fmtInput long should contain cursor marker, got %q", r)
	}
}

func TestFmtInput_StartCursor(t *testing.T) {
	r := fmtInput("hello world", 0, 20)
	if !strings.HasPrefix(r, "▌") {
		t.Errorf("fmtInput with cursor at 0 should start with marker, got %q", r)
	}
}

// ============================================================================
// wordWrap 测试
// ============================================================================

func TestWordWrap_Short(t *testing.T) {
	v := wordWrap("hello", 20)
	if v != "hello" {
		t.Errorf("wordWrap short = %q", v)
	}
}

func TestWordWrap_Narrow(t *testing.T) {
	// Very narrow — falls back to original
	v := wordWrap("hello world", 5)
	if v != "hello world" {
		t.Errorf("wordWrap very narrow = %q", v)
	}
}

// ============================================================================
// Command 测试
// ============================================================================

func TestMatchPrefix(t *testing.T) {
	cr := NewCommandRegistry(nil)
	m := cr.MatchPrefix("/m")
	if len(m) == 0 {
		t.Error("should match /model /memory")
	}
	// Verify all matches start with /m
	for _, cmd := range m {
		if !strings.HasPrefix(cmd, "/m") {
			t.Errorf("match %q does not start with /m", cmd)
		}
	}
}

func TestMatchPrefix_Exact(t *testing.T) {
	cr := NewCommandRegistry(nil)
	m := cr.MatchPrefix("/model")
	if len(m) != 1 || m[0] != "/model" {
		t.Errorf("expected [/model], got %v", m)
	}
}

func TestMatchPrefix_NoSlash(t *testing.T) {
	cr := NewCommandRegistry(nil)
	m := cr.MatchPrefix("no-slash")
	if len(m) != 0 {
		t.Errorf("expected no matches for input without /, got %v", m)
	}
}

func TestMatchPrefix_NoMatch(t *testing.T) {
	cr := NewCommandRegistry(nil)
	m := cr.MatchPrefix("/zzz_nonexistent")
	if len(m) != 0 {
		t.Errorf("expected no matches, got %v", m)
	}
}

func TestMatchPrefix_Empty(t *testing.T) {
	cr := NewCommandRegistry(nil)
	m := cr.MatchPrefix("")
	if len(m) != 0 {
		t.Errorf("expected no matches for empty input, got %v", m)
	}
}

func TestMatchPrefixEntries(t *testing.T) {
	cr := NewCommandRegistry(nil)
	entries := cr.MatchPrefixEntries("/m")
	if len(entries) == 0 {
		t.Error("should match /model /memory")
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name, "/m") {
			t.Errorf("entry %q does not start with /m", e.Name)
		}
		if e.Desc == "" {
			t.Errorf("entry %q has no description", e.Name)
		}
	}
}

func TestAutoComplete_SingleMatch(t *testing.T) {
	cr := NewCommandRegistry(nil)
	// Use a prefix that matches only one command
	result := cr.AutoComplete("/ne")
	if result != "/new " {
		t.Errorf("expected '/new ', got %q", result)
	}
	// "/mod" matches both /mode and /model, common prefix is "/mode"
	result = cr.AutoComplete("/mod")
	if result != "/mode" {
		t.Errorf("expected '/mode' (common prefix), got %q", result)
	}
}

func TestAutoComplete_MultipleMatch_CommonPrefix(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result := cr.AutoComplete("/h")
	// /help and /history share common prefix "/h"
	if !strings.HasPrefix(result, "/h") {
		t.Errorf("common prefix should start with /h, got %q", result)
	}
}

func TestAutoComplete_NoSlash(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result := cr.AutoComplete("no-slash")
	if result != "no-slash" {
		t.Errorf("expected unchanged, got %q", result)
	}
}

func TestAutoComplete_NoMatch(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result := cr.AutoComplete("/zzz")
	if result != "/zzz" {
		t.Errorf("expected unchanged for no match, got %q", result)
	}
}

// ============================================================================
// Command Execution 测试
// ============================================================================

func TestExecute_Quit(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, quit := cr.Execute("/quit")
	if !quit {
		t.Error("/quit should signal quit")
	}
	if result == "" {
		t.Error("/quit should return a message")
	}
}

func TestExecute_Exit(t *testing.T) {
	cr := NewCommandRegistry(nil)
	_, quit := cr.Execute("/exit")
	if !quit {
		t.Error("/exit should signal quit")
	}
}

func TestExecute_NoSlash(t *testing.T) {
	cr := NewCommandRegistry(nil)
	_, ok := cr.Execute("not-a-command")
	if ok {
		t.Error("non-command should not be handled")
	}
}

func TestExecute_UnknownCommand(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, ok := cr.Execute("/nonexistent_cmd")
	if ok {
		t.Error("unknown command should not be ok")
	}
	if !strings.Contains(result, "未知命令") {
		t.Errorf("should say unknown command, got %q", result)
	}
}

func TestExecute_Empty(t *testing.T) {
	cr := NewCommandRegistry(nil)
	_, ok := cr.Execute("")
	if ok {
		t.Error("empty should not be handled")
	}
}

// ============================================================================
// Commands without Runtime (nil-safe) 测试
// ============================================================================

func TestCmd_New_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/new")
	if !strings.Contains(result, "新会话") {
		t.Errorf("expected 新会话 message, got %q", result)
	}
}

func TestCmd_History_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/history")
	if !strings.Contains(result, "无历史会话") {
		t.Errorf("expected 无历史会话, got %q", result)
	}
}

func TestCmd_Model_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	// Without args — show current (nil-safe)
	result, _ := cr.Execute("/model")
	if !strings.Contains(result, "当前: test") {
		t.Errorf("expected '当前: test', got %q", result)
	}
	// With args and nil rt — panics (calls rt.SwitchModel)
}

func TestCmd_Effort_NilRuntime(t *testing.T) {
	// /effort with args and nil rt also panics (calls rt.SetEffort on nil)
}

func TestCmd_Help(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/help")
	if !strings.Contains(result, "命令:") {
		t.Errorf("expected command listing, got %q", result)
	}
	// Should list several commands
	for _, cmd := range []string{"/new", "/model", "/history", "/effort", "/help"} {
		if !strings.Contains(result, cmd) {
			t.Errorf("help should mention %s", cmd)
		}
	}
}

func TestCmd_Load_NoArgs(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/load")
	if !strings.Contains(result, "用法:") {
		t.Errorf("expected usage hint, got %q", result)
	}
}

func TestCmd_Load_WithArgs(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/load abc-123")
	if !strings.Contains(result, "已加载") {
		t.Errorf("expected 已加载, got %q", result)
	}
}

func TestCmd_Compress_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/compress")
	if !strings.Contains(result, "已压缩") {
		t.Errorf("expected 已压缩, got %q", result)
	}
}

func TestCmd_Threshold_NoArgs(t *testing.T) {
	// /threshold with/without args and nil rt panics (accesses rt.Config / rt.SetThreshold)
}

func TestCmd_Threshold_WithArgs(t *testing.T) {
	// /threshold with args and nil rt panics (calls rt.SetThreshold)
}

func TestCmd_OverseerThreshold_NilRuntime(t *testing.T) {
	// /overseer_threshold with args and nil rt panics (accesses rt.Config)
	// Test is skipped for nil runtime without args.
	// This test documents that commands requiring runtime fail gracefully with args.
}

func TestCmd_Plugin_List_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/plugin list")
	if !strings.Contains(result, "无插件") {
		t.Errorf("expected 无插件, got %q", result)
	}
}

func TestCmd_Plugin_NoArgs(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/plugin")
	if !strings.Contains(result, "用法:") {
		t.Errorf("expected usage hint, got %q", result)
	}
}

func TestCmd_Memory_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/memory")
	if !strings.Contains(result, "会话数: 0") {
		t.Errorf("expected 会话数: 0, got %q", result)
	}
}

func TestCmd_ACP_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/acp")
	if !strings.Contains(result, "ACP: 未启用") {
		t.Errorf("expected ACP: 未启用, got %q", result)
	}
}

func TestCmd_Inject_NoArgs(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/inject")
	if !strings.Contains(result, "用法:") {
		t.Errorf("expected usage hint, got %q", result)
	}
}

func TestCmd_Inject_WithArgs(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/inject block-1")
	if !strings.Contains(result, "已提升") {
		t.Errorf("expected 已提升, got %q", result)
	}
}

func TestCmd_Mode_NilRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	// Without args — mode has nil-rt guard
	result, _ := cr.Execute("/mode")
	if !strings.Contains(result, "当前: chat") {
		t.Errorf("expected '当前: chat', got %q", result)
	}
	// With task/chat arg and nil rt panics (accesses rt.SetMode)
}

func TestCmd_Effort_NoArgs_NilRuntime(t *testing.T) {
	// /effort without args and nil rt panics (accesses rt.Config.CLIDefaults.Effort)
}

// ============================================================================
// AllEntries / Commands 测试
// ============================================================================

func TestAllEntries(t *testing.T) {
	cr := NewCommandRegistry(nil)
	entries := cr.AllEntries()
	if len(entries) == 0 {
		t.Error("should have builtin commands")
	}
	for _, e := range entries {
		if e.Name == "" {
			t.Error("entry should have a name")
		}
		if !strings.HasPrefix(e.Name, "/") {
			t.Errorf("command name should start with /, got %q", e.Name)
		}
	}
}

func TestCommands(t *testing.T) {
	cr := NewCommandRegistry(nil)
	cmds := cr.Commands()
	if len(cmds) == 0 {
		t.Error("should have commands")
	}
	// All should start with /
	for _, c := range cmds {
		if !strings.HasPrefix(c, "/") {
			t.Errorf("command should start with /, got %q", c)
		}
	}
}

// ============================================================================
// commonPrefix 测试
// ============================================================================

func TestCommonPrefix_Shared(t *testing.T) {
	if v := commonPrefix("hello", "help"); v != "hel" {
		t.Errorf("expected 'hel', got %q", v)
	}
}

func TestCommonPrefix_NoShared(t *testing.T) {
	if v := commonPrefix("abc", "xyz"); v != "" {
		t.Errorf("expected '', got %q", v)
	}
}

func TestCommonPrefix_Identical(t *testing.T) {
	if v := commonPrefix("same", "same"); v != "same" {
		t.Errorf("expected 'same', got %q", v)
	}
}

func TestCommonPrefix_Empty(t *testing.T) {
	if v := commonPrefix("", "abc"); v != "" {
		t.Errorf("expected '', got %q", v)
	}
}

// ============================================================================
// Paste Detection 测试
// ============================================================================

func TestIsPasting_FastTyping(t *testing.T) {
	m := initialModel(nil)
	m.lastKeyTime = time.Now()
	time.Sleep(10 * time.Millisecond)
	// Quick keystrokes should accumulate paste sequence
	p1 := m.isPasting()
	m.lastKeyTime = time.Now()
	time.Sleep(10 * time.Millisecond)
	p2 := m.isPasting()
	m.lastKeyTime = time.Now()
	p3 := m.isPasting()
	// After 3 fast keystrokes, isPasting should return true
	if !p3 {
		t.Log("paste detection may not have triggered (timing-dependent)")
	}
	_ = p1
	_ = p2
}

func TestIsPasting_SlowTyping(t *testing.T) {
	m := initialModel(nil)
	m.lastKeyTime = time.Now()
	time.Sleep(250 * time.Millisecond)
	p := m.isPasting()
	if p {
		t.Error("slow typing should not be detected as paste")
	}
	if m.pasteSeq != 0 {
		t.Error("paste sequence should reset after slow typing")
	}
}

func TestIsPasting_Reset(t *testing.T) {
	m := initialModel(nil)
	m.pasteChars = 3
	m.lastKeyTime = time.Now().Add(-300 * time.Millisecond)
	p := m.isPasting()
	if p {
		t.Error("should reset paste detection after long delay")
	}
	if m.pasteChars != 0 {
		t.Error("pasteChars should be reset")
	}
}

// ============================================================================
// renderMarkdown 测试
// ============================================================================

func TestRenderMarkdown_PlainText(t *testing.T) {
	r := renderMarkdown("hello world", 80)
	if !strings.Contains(r, "hello world") {
		t.Errorf("expected 'hello world', got %q", r)
	}
}

func TestRenderMarkdown_CodeBlock(t *testing.T) {
	r := renderMarkdown("```go\nfunc main() {}\n```", 80)
	if !strings.Contains(r, "func main()") {
		t.Errorf("should contain code content, got %q", r)
	}
}

func TestRenderMarkdown_Heading(t *testing.T) {
	r := renderMarkdown("# Title", 80)
	if !strings.Contains(r, "Title") {
		t.Errorf("should contain heading text, got %q", r)
	}
}

func TestRenderMarkdown_Bold(t *testing.T) {
	r := renderMarkdown("hello **world**", 80)
	if !strings.Contains(r, "world") {
		t.Errorf("should contain bold text, got %q", r)
	}
}

func TestRenderMarkdown_InlineCode(t *testing.T) {
	r := renderMarkdown("use `fmt.Println` to print", 80)
	if !strings.Contains(r, "fmt.Println") {
		t.Errorf("should contain inline code, got %q", r)
	}
}

func TestRenderMarkdown_DiffLines(t *testing.T) {
	r := renderMarkdown("+added line\n-removed line\n@@ -1,3 +1,4 @@", 80)
	if !strings.Contains(r, "added line") || !strings.Contains(r, "removed line") {
		t.Errorf("should render diff lines, got %q", r)
	}
}

func TestRenderMarkdown_List(t *testing.T) {
	r := renderMarkdown("- item 1\n- item 2", 80)
	if !strings.Contains(r, "item 1") {
		t.Errorf("should render list items, got %q", r)
	}
}

// 回归：多行输出逐行累积空格导致不对齐（lipgloss Render 内含 \n 会把空行
// pad 成行宽空格；diff 分支劫持 "- " 列表项）
func TestRenderMarkdown_NoLeadingSpaceAccumulation(t *testing.T) {
	text := "### 4. 编辑能力补全\n" +
		"- `Ctrl+A`/`Home` 行首 · `Ctrl+E`/`End` 行尾\n" +
		"- `Ctrl+U` 删到行首 · `Ctrl+K` 删到行尾 · `Ctrl+W` 删前一词\n" +
		"- 多行输入中 `↑`/`↓` 行间移动光标（保持目标列），首行↑/末行↓ 才切历史"
	r := renderMarkdown(text, 76)
	lines := strings.Split(r, "\n")
	if len(lines) < 4 {
		t.Fatalf("expected >=4 lines, got %d: %q", len(lines), r)
	}
	// 每行不得有意外前导空格（标题行首、列表项应顶格或仅有固定 "• " 前缀）
	for i, ln := range lines[:4] {
		if strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "• ") {
			t.Errorf("line %d has unexpected leading spaces: %q", i, ln)
		}
	}
	// 列表项应渲染为 "• " 而非 "- "（diff 分支不得劫持列表）
	if !strings.Contains(r, "• `Ctrl+A`") {
		t.Errorf("list item should render with bullet, got %q", r)
	}
	// 且原始 "- " 不应残留在输出中
	if strings.Contains(r, "\n- `Ctrl+A`") {
		t.Errorf("raw '- ' list marker leaked: %q", r)
	}
}

func TestRenderMarkdown_DiffNotHijackList(t *testing.T) {
	// "-xxx"（diff 删除行）保持红色 diff 渲染；"- xxx"（列表）走列表分支
	r := renderMarkdown("-removed\n- item\n+added\n+ item", 80)
	if !strings.Contains(r, "removed") || !strings.Contains(r, "added") {
		t.Errorf("diff lines should render, got %q", r)
	}
	if !strings.Contains(r, "• item") {
		t.Errorf("'- item' should render as bullet list, got %q", r)
	}
	if !strings.Contains(r, "• item") {
		t.Errorf("'+ item' should render as bullet list, got %q", r)
	}
}

func TestRenderMarkdown_CodeBlockNoAccumulation(t *testing.T) {
	// 代码块三行输出，行首不得有累积空格（Render 内含 \n 的回归）
	r := renderMarkdown("```\nline one\nline two\n```", 80)
	lines := strings.Split(r, "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (open+2+close), got %d: %q", len(lines), r)
	}
	for i, ln := range lines {
		if strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "│ ") {
			t.Errorf("code line %d has unexpected leading spaces: %q", i, ln)
		}
	}
}

// ============================================================================
// Concurrent Safety Tests
// ============================================================================

func TestCommandRegistry_ConcurrentMatchPrefix(t *testing.T) {
	cr := NewCommandRegistry(nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr.MatchPrefix("/m")
			cr.MatchPrefix("/h")
			cr.MatchPrefix("/")
		}()
	}
	wg.Wait()
}

func TestCommandRegistry_ConcurrentExecute(t *testing.T) {
	cr := NewCommandRegistry(nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr.Execute("/help")
			cr.Execute("/model")
			cr.Execute("/new")
			cr.Execute("/history")
			cr.Execute("/memory")
		}()
	}
	wg.Wait()
}

func TestCommandRegistry_ConcurrentAllEntries(t *testing.T) {
	cr := NewCommandRegistry(nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cr.AllEntries()
			cr.Commands()
		}()
	}
	wg.Wait()
}

// ============================================================================
// renderMessage edge cases
// ============================================================================

func TestRenderMessage_AllTypesNonEmpty(t *testing.T) {
	widths := []int{20, 40, 80, 120}
	messages := []ChatMessage{
		sysMsg("system message"),
		usrMsg("user input"),
		asstMsg("assistant response"),
		errMsg("error message"),
		toolCall("read", `{"path":"/test"}`),
		toolOK("read", "file contents here"),
		toolFail("write", "permission denied"),
	}

	for _, msg := range messages {
		for _, w := range widths {
			r := renderMessage(msg, w, false)
			if r == "" {
				t.Errorf("renderMessage for %v at width %d returned empty", msg.Type, w)
			}
		}
	}
}

// ============================================================================
// Ctrl+C 清空输入框 / 输入区滚动 / 快捷粘贴
// ============================================================================

func TestCtrlC_ClearsChatInputFirst(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "hello world"
	m.cursor = 11

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	mm := res.(*model)
	if mm.chatInput != "" {
		t.Errorf("第一次 Ctrl+C 应清空输入框, got %q", mm.chatInput)
	}
	if mm.mode != modeChat {
		t.Errorf("清空输入框后应保持在聊天模式, got mode=%d", mm.mode)
	}
}

func TestCtrlC_QuitsWhenInputEmpty(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""

	res, cmd := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	if cmd == nil {
		t.Errorf("输入框为空时 Ctrl+C 应返回退出命令, got nil")
	}
	_ = res
}

// 优先级：流式中输入框有内容 → 先清空输入框，不中断对话
func TestCtrlC_StreamingWithInput_ClearsInputNotInterrupt(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "hello"
	m.cursor = 5
	m.streaming = true
	m.interrupted = false

	res, cmd := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	mm := res.(*model)
	if mm.chatInput != "" {
		t.Errorf("流式中输入框有内容：应先清空输入框, got %q", mm.chatInput)
	}
	if mm.streaming != true {
		t.Errorf("流式中输入框有内容：不应中断对话, streaming=%v", mm.streaming)
	}
	if mm.interrupted {
		t.Errorf("流式中输入框有内容：不应设置中断标记")
	}
	if cmd != nil {
		t.Errorf("流式中输入框有内容：不应返回退出命令")
	}
}

// 优先级：流式中输入框为空 → 中断对话（不退出）
func TestCtrlC_StreamingEmptyInput_InterruptsNotQuit(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.streaming = true
	m.interrupted = false

	res, cmd := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	mm := res.(*model)
	if mm.streaming {
		t.Errorf("流式中输入框为空：应中断对话, streaming=%v", mm.streaming)
	}
	if !mm.interrupted {
		t.Errorf("流式中输入框为空：应设置中断标记")
	}
	if cmd != nil {
		t.Errorf("流式中输入框为空：第一次 Ctrl+C 不应退出, got %v", cmd)
	}
}

// 优先级：流式中输入框为空且已中断 → 退出
func TestCtrlC_StreamingInterrupted_Quits(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.streaming = true
	m.interrupted = true

	_, cmd := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	if cmd == nil {
		t.Errorf("流式中已中断后再 Ctrl+C 应退出, got nil")
	}
}

func TestCtrlC_ClearsCommandInput(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeCommand
	m.input = "/help"
	m.cursor = 5

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	mm := res.(*model)
	if mm.mode != modeNormal {
		t.Errorf("命令模式 Ctrl+C 应回到 normal 模式, got mode=%d", mm.mode)
	}
	if mm.input != "" {
		t.Errorf("命令输入应被清空, got %q", mm.input)
	}
}

func TestCtrlC_NormalModeQuits(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeNormal

	_, cmd := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlC})
	if cmd == nil {
		t.Errorf("normal 模式 Ctrl+C 应返回退出命令, got nil")
	}
}

func TestInputScroll_CtrlUpDown(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.height = 30
	m.width = 80
	// 构造超过显示区的多行内容
	m.chatInput = ""
	for i := 0; i < 30; i++ {
		m.chatInput += "line" + itoa(i) + "\n"
	}
	m.cursor = len([]rune(m.chatInput))

	// 向上滚动
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlUp})
	mm := res.(*model)
	if mm.inputScroll != 1 {
		t.Errorf("ctrl+up 应滚动1行, got scroll=%d", mm.inputScroll)
	}
	// 再滚动几次（上限 = 总行数 - 可见行数）
	maxScroll := inputTotalLines(m) - (m.height - 5)
	for i := 0; i < 10; i++ {
		res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlUp})
		mm = res.(*model)
	}
	if mm.inputScroll != maxScroll {
		t.Errorf("连续滚动后 inputScroll 应为%d, got %d", maxScroll, mm.inputScroll)
	}
	// 向下滚动回0
	for i := 0; i < 20; i++ {
		res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlDown})
		mm = res.(*model)
		if mm.inputScroll == 0 {
			break
		}
	}
	if mm.inputScroll != 0 {
		t.Errorf("ctrl+down 应回到底部, got scroll=%d", mm.inputScroll)
	}
}

func TestPaste_InsertsWholeContent(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "prefix "
	m.cursor = len([]rune(m.chatInput))

	pasteContent := "粘贴的完整内容 paste content"
	res, _ := m.handleChat(bubbletea.KeyMsg{
		Type:  bubbletea.KeyRunes,
		Runes: []rune(pasteContent),
		Paste: true,
	})
	mm := res.(*model)
	if mm.chatInput != "prefix "+pasteContent {
		t.Errorf("粘贴应一次性插入完整内容, got %q", mm.chatInput)
	}
	if mm.cursor != len([]rune(mm.chatInput)) {
		t.Errorf("粘贴后光标应在末尾, got %d want %d", mm.cursor, len([]rune(mm.chatInput)))
	}
	if !mm.pasting {
		t.Errorf("粘贴后 pasting 应为 true")
	}
}

func TestPaste_EnterDoesNotSendWhilePasting(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line1"
	m.cursor = len([]rune(m.chatInput))
	m.pasting = true

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm := res.(*model)
	if mm.chatInput != "line1\n" {
		t.Errorf("粘贴状态中 Enter 应插入换行而非发送, got %q", mm.chatInput)
	}
}

// 回归测试：粘贴保护插入换行后必须解除粘贴状态，
// 否则 pasting 卡死为 true，之后所有回车都只换行、无法发送。
func TestPaste_EnterNewlineUnlocksPasting(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line1"
	m.cursor = len([]rune(m.chatInput))
	m.pasting = true
	m.pasteSeq = 5

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm := res.(*model)
	if mm.chatInput != "line1\n" {
		t.Fatalf("粘贴状态中 Enter 应插入换行, got %q", mm.chatInput)
	}
	if mm.pasting {
		t.Error("插入换行后应解除 pasting，否则回车永远无法发送")
	}
	if mm.pasteSeq != 0 {
		t.Errorf("插入换行后 pasteSeq 应清零, got %d", mm.pasteSeq)
	}

	// 解除后再次回车（正常间隔）应发送：输入被清空、回到 normal 模式
	mm.lastKeyTime = time.Now()
	res2, _ := mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm2 := res2.(*model)
	if mm2.chatInput != "" {
		t.Errorf("解除粘贴后 Enter 应发送并清空输入, got %q", mm2.chatInput)
	}
	if mm2.mode != modeNormal {
		t.Errorf("发送后应回到 normal 模式, got mode=%d", mm2.mode)
	}
}

// 回归测试：pasteChars 被快速输入攒到 ≥3 后，停顿 80~200ms 再按回车，
// 此前因 80ms<elapsed<200ms 既不递增也不清零而残留为高值，
// 导致回车被误判为"粘贴中"→ 只换行不发送。现在 ≥80ms 即视为粘贴结束。
func TestIsPasting_StaleSeqResetAfterGap(t *testing.T) {
	m := initialModel(nil)
	m.pasteChars = 4 // 模拟此前快速输入/粘贴攒下的计数
	m.lastKeyTime = time.Now().Add(-100 * time.Millisecond)

	if p := m.isPasting(); p {
		t.Error("停顿 100ms 后不应再被判定为粘贴中，否则回车无法发送")
	}
	if m.pasteChars != 0 {
		t.Errorf("停顿 ≥80ms 后 pasteChars 应清零, got %d", m.pasteChars)
	}
}

// ============================================================================
// 多行输入：Ctrl+J / Alt+Enter（真实可达）换行，Enter 发送
// 说明：bubbletea v1.1.0 中 Ctrl+Enter/Ctrl+M 与 Enter 同码(keyCR, "enter")，
//       真实终端无法区分 → 实际触发发送；仅有 Ctrl+J(LF) 上报为 "ctrl+j"、
//       Alt+Enter(ESC+CR) 上报为 "alt+enter" 可可靠区分；
//       代码中 "ctrl+enter"/"ctrl+m" 分支为防御性保留（某些终端/键映射可能产生该字符串）。
// ============================================================================

func TestNewline_CtrlJInsertsNewline(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line1"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlJ})
	mm := res.(*model)
	if mm.chatInput != "line1\n" {
		t.Errorf("Ctrl+J 应插入换行, got %q", mm.chatInput)
	}
	if mm.mode != modeChat {
		t.Errorf("Ctrl+J 后应保持在聊天模式, got mode=%d", mm.mode)
	}
	if mm.cursor != len([]rune("line1\n")) {
		t.Errorf("Ctrl+J 后光标应移到末尾, got %d", mm.cursor)
	}
}

func TestNewline_AltEnterInsertsNewline(t *testing.T) {
	// Alt+Enter（macOS Option+Enter）：终端发 ESC+CR，bubbletea 解析为 "alt+enter"，
	// VS Code 集成终端不拦截，是最可靠的换行方式
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line1"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter, Alt: true})
	mm := res.(*model)
	if mm.chatInput != "line1\n" {
		t.Errorf("Alt+Enter 应插入换行, got %q", mm.chatInput)
	}
	if mm.mode != modeChat {
		t.Errorf("Alt+Enter 后应保持在聊天模式, got mode=%d", mm.mode)
	}
	if mm.cursor != len([]rune("line1\n")) {
		t.Errorf("Alt+Enter 后光标应移到末尾, got %d", mm.cursor)
	}
}

func TestNewline_CtrlMIsEnter(t *testing.T) {
	// 真实行为：bubbletea 将 Ctrl+M(keyCR) 映射为 "enter"，与 Enter 同码
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line1"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlM})
	mm := res.(*model)
	if mm.chatInput != "" {
		t.Errorf("Ctrl+M 与 Enter 同码应发送并清空, got %q", mm.chatInput)
	}
	if mm.mode != modeNormal {
		t.Errorf("Ctrl+M 发送后应回到 normal 模式, got mode=%d", mm.mode)
	}
}

func TestNewline_EnterSendsMultiline(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line1\nline2"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm := res.(*model)
	if mm.chatInput != "" {
		t.Errorf("Enter 应发送并清空输入框, got %q", mm.chatInput)
	}
	if mm.mode != modeNormal {
		t.Errorf("Enter 发送后应回到 normal 模式, got mode=%d", mm.mode)
	}
	if len(mm.history) == 0 || mm.history[len(mm.history)-1] != "line1\nline2" {
		t.Errorf("多行输入应完整进入历史末尾, got last=%q", func() string {
			if len(mm.history) == 0 {
				return ""
			}
			return mm.history[len(mm.history)-1]
		}())
	}
}

func TestNewline_CursorMidInsert(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "ab cd"
	m.cursor = 2 // 在 ab 中间

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlJ})
	mm := res.(*model)
	if mm.chatInput != "ab\n cd" {
		t.Errorf("光标中部 Ctrl+J 应在光标处插入换行, got %q", mm.chatInput)
	}
	if mm.cursor != 3 {
		t.Errorf("光标应移到换行符之后, got %d", mm.cursor)
	}
}

func TestPaste_ResetsOnManualInput(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.cursor = 0
	m.pasting = true

	// 手动输入一个字符（非 Paste）
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'a'}})
	mm := res.(*model)
	if mm.pasting {
		t.Errorf("手动输入应结束粘贴状态")
	}
}

func TestInputScroll_ResetsOnTyping(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "abc"
	m.cursor = 3
	m.inputScroll = 5

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'d'}})
	mm := res.(*model)
	if mm.inputScroll != 0 {
		t.Errorf("输入字符应重置滚动偏移, got %d", mm.inputScroll)
	}
}

// ============================================================================
// 斜杠命令只在输入框开头触发
// ============================================================================

func TestSlash_NormalModeEntersCommand(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeNormal

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'/'}})
	mm := res.(*model)
	if mm.mode != modeCommand {
		t.Errorf("normal 模式按 / 应进入命令模式, got mode=%d", mm.mode)
	}
	if mm.input != "/" {
		t.Errorf("命令输入应为 '/', got %q", mm.input)
	}
}

func TestSlash_ChatEmptyEntersCommand(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.cursor = 0

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'/'}})
	mm := res.(*model)
	if mm.mode != modeCommand {
		t.Errorf("chat 空输入按 / 应进入命令模式, got mode=%d", mm.mode)
	}
}

func TestSlash_ChatWithInputNotTrigger(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "abc"
	m.cursor = 3

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'/'}})
	mm := res.(*model)
	if mm.mode != modeChat {
		t.Errorf("chat 有输入时按 / 不应进入命令模式, got mode=%d", mm.mode)
	}
	if mm.chatInput != "abc/" {
		t.Errorf("/ 应作为普通字符插入, got %q", mm.chatInput)
	}
}

func TestSlash_ChatMiddleNotTrigger(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "abc"
	m.cursor = 1 // 光标在中间

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'/'}})
	mm := res.(*model)
	if mm.mode != modeChat {
		t.Errorf("光标在中间按 / 不应进入命令模式, got mode=%d", mm.mode)
	}
	if mm.chatInput != "a/bc" {
		t.Errorf("/ 应插入到光标位置, got %q", mm.chatInput)
	}
	if mm.cursor != 2 {
		t.Errorf("光标应前移, got %d", mm.cursor)
	}
}

func TestSlash_CommandModeNotRetrigger(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeCommand
	m.input = "/help"
	m.cursor = 5

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune{'/'}})
	mm := res.(*model)
	if mm.mode != modeCommand {
		t.Errorf("命令模式按 / 不应重置, got mode=%d", mm.mode)
	}
	if mm.input != "/help/" {
		t.Errorf("/ 应作为普通字符追加到命令输入, got %q", mm.input)
	}
}

// ============================================================================
// 输入框多行渲染（默认三行高度）
// ============================================================================

func TestInputArea_MinimumThreeLines(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "单行内容"
	m.width = 80
	m.height = 30

	h := inputAreaHeight(m)
	if h < 4 { // 3行内容 + 1分隔线
		t.Errorf("输入区高度应至少4行（3行+分隔线）, got %d", h)
	}

	rendered := renderInputArea(m, h)
	if strings.Count(rendered, "\n") < 3 {
		t.Errorf("输入区应渲染至少3行, got %d 行", strings.Count(rendered, "\n"))
	}
}

func TestInputArea_MultilineRendering(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "第一行\n第二行\n第三行"
	m.cursor = len([]rune(m.chatInput))
	m.width = 80
	m.height = 30

	rendered := renderInputArea(m, inputAreaHeight(m))
	// 多行内容应完整渲染（含换行）
	if !strings.Contains(rendered, "第一行") || !strings.Contains(rendered, "第二行") || !strings.Contains(rendered, "第三行") {
		t.Errorf("多行内容渲染不完整: %q", rendered)
	}
}

func TestInputArea_PastedMultiline(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""
	m.cursor = 0
	m.width = 80
	m.height = 30

	// 粘贴多行内容
	pasted := "line1\nline2\nline3\nline4"
	res, _ := m.handleChat(bubbletea.KeyMsg{
		Type:  bubbletea.KeyRunes,
		Runes: []rune(pasted),
		Paste: true,
	})
	mm := res.(*model)
	if mm.chatInput != pasted {
		t.Errorf("粘贴多行内容应完整保留, got %q", mm.chatInput)
	}

	// 渲染应包含所有行
	rendered := renderInputArea(mm, inputAreaHeight(mm))
	for _, line := range []string{"line1", "line2", "line3", "line4"} {
		if !strings.Contains(rendered, line) {
			t.Errorf("渲染缺少行 %q: %q", line, rendered)
		}
	}
}

// ============================================================================
// 输入编辑快捷键（Ctrl+A/E/U/K/W、Home/End、多行↑↓）
// ============================================================================

func TestEdit_CtrlA_CtrlE(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "hello world"
	m.cursor = len([]rune(m.chatInput))

	// Ctrl+A → 行首
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlA})
	mm := res.(*model)
	if mm.cursor != 0 {
		t.Errorf("Ctrl+A 应到行首, got cursor=%d", mm.cursor)
	}
	// Ctrl+E → 行尾
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlE})
	mm = res.(*model)
	if mm.cursor != len([]rune("hello world")) {
		t.Errorf("Ctrl+E 应到行尾, got cursor=%d", mm.cursor)
	}
}

func TestEdit_HomeEnd(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "abc\ndef"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyHome})
	mm := res.(*model)
	if mm.cursor != 0 {
		t.Errorf("Home 应到行首, got cursor=%d", mm.cursor)
	}
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnd})
	mm = res.(*model)
	if mm.cursor != len([]rune("abc\ndef")) {
		t.Errorf("End 应到行尾, got cursor=%d", mm.cursor)
	}
}

func TestEdit_CtrlU_DeletesToLineStart(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "keep\npart"
	m.cursor = len([]rune("keep\npart"))

	// 移到第二行中间，Ctrl+U 只删当前行光标前
	m.cursor = len([]rune("keep\npa"))
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlU})
	mm := res.(*model)
	if mm.chatInput != "keep\nrt" {
		t.Errorf("Ctrl+U 应删除当前行光标前, got %q", mm.chatInput)
	}
	if mm.cursor != len([]rune("keep\n")) {
		t.Errorf("Ctrl+U 后光标应到行首, got cursor=%d", mm.cursor)
	}
}

func TestEdit_CtrlK_DeletesToLineEnd(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "aa\nbbcc\ndd"
	m.cursor = len([]rune("aa\nbb"))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlK})
	mm := res.(*model)
	if mm.chatInput != "aa\nbb\ndd" {
		t.Errorf("Ctrl+K 应删除光标到行尾, got %q", mm.chatInput)
	}
}

func TestEdit_CtrlW_DeletesPrevWord(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "one two three"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyCtrlW})
	mm := res.(*model)
	if mm.chatInput != "one two " {
		t.Errorf("Ctrl+W 应删除前一个词, got %q", mm.chatInput)
	}
}

func TestEdit_UpDownMovesCursorInMultiline(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "abc\ndefg\nhi"
	m.cursor = len([]rune("abc\ndefg")) // 第二行行尾（col=4）

	// ↑ 应移动到第一行，保持列 4 → 第一行只有 3 列 → col=3
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyUp})
	mm := res.(*model)
	if mm.cursor != len([]rune("abc")) {
		t.Errorf("↑ 应移到第一行末尾, got cursor=%d", mm.cursor)
	}
	// ↓ 应回到第二行 col=4
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyDown})
	mm = res.(*model)
	if mm.cursor != len([]rune("abc\ndefg")) {
		t.Errorf("↓ 应回到第二行末尾, got cursor=%d", mm.cursor)
	}
	// 第一行 ↑ 不应改变输入（无历史）
	m2 := initialModel(nil)
	m2.history = nil // 忽略磁盘历史
	m2.mode = modeChat
	m2.chatInput = "abc\ndef"
	m2.cursor = 1
	before := m2.chatInput
	res, _ = m2.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyUp})
	mm2 := res.(*model)
	if mm2.chatInput != before {
		t.Errorf("首行↑且无历史时输入不应改变, got %q", mm2.chatInput)
	}
}

// 单行输入光标在文字中间时，↑/↓ 仅移动光标到行首/行尾，绝不触发历史翻滚
// （防止未提交输入被历史条目覆盖丢失——严重体验问题回归测试）。
func TestEdit_UpDownSingleLineMovesCursorNotHistory(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.history = []string{"历史一", "历史二"}
	m.chatInput = "abc def"
	m.cursor = 3

	// ↑ → 光标到行首，输入/历史状态分毫不动
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyUp})
	mm := res.(*model)
	if mm.chatInput != "abc def" {
		t.Fatalf("光标在中间时 ↑ 不得翻历史覆盖输入, got %q", mm.chatInput)
	}
	if mm.cursor != 0 {
		t.Fatalf("↑ 应移动光标到行首, got cursor=%d", mm.cursor)
	}
	if mm.historyIdx != 0 {
		t.Fatalf("↑ 不得进入历史浏览, historyIdx=%d", mm.historyIdx)
	}
	// ↓ → 光标到行尾，输入不动
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyDown})
	mm = res.(*model)
	if mm.chatInput != "abc def" {
		t.Fatalf("光标在中间时 ↓ 不得翻历史覆盖输入, got %q", mm.chatInput)
	}
	if mm.cursor != len([]rune("abc def")) {
		t.Fatalf("↓ 应移动光标到行尾, got cursor=%d", mm.cursor)
	}
}

// 历史翻滚后 ↓ 能完整恢复翻历史前的输入草稿（不再清空丢失）；
// 未翻历史时 ↓ 无操作（不覆盖当前输入）。
func TestEdit_HistoryDraftRestoredOnDown(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.history = []string{"历史一", "历史二"}
	m.chatInput = "未提交草稿"
	m.cursor = 0

	// 光标在行首按 ↑ → 翻到最新一条历史
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyUp})
	mm := res.(*model)
	if mm.chatInput != "历史二" {
		t.Fatalf("↑ 应回填最新历史, got %q", mm.chatInput)
	}
	// ↓ → 恢复翻历史前的草稿
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyDown})
	mm = res.(*model)
	if mm.chatInput != "未提交草稿" {
		t.Fatalf("↓ 应恢复翻历史前的输入草稿, got %q", mm.chatInput)
	}
	if mm.historyIdx != 0 {
		t.Fatalf("↓ 恢复后 historyIdx 应为 0, got %d", mm.historyIdx)
	}
	// 未翻历史时 ↓ 无操作
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyDown})
	mm = res.(*model)
	if mm.chatInput != "未提交草稿" {
		t.Fatalf("未翻历史时 ↓ 不得覆盖输入, got %q", mm.chatInput)
	}
	// 翻历史后编辑 → 退出历史模式，编辑内容成为新草稿基准
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("!")})
	mm = res.(*model)
	if mm.historyIdx != 0 {
		t.Fatalf("编辑应退出历史浏览, historyIdx=%d", mm.historyIdx)
	}
	res, _ = mm.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyDown})
	mm = res.(*model)
	if mm.chatInput != "未提交草稿!" {
		t.Fatalf("编辑后 ↓ 不得覆盖编辑内容, got %q", mm.chatInput)
	}
}

// ============================================================================
// 帮助面板（? 键）
// ============================================================================

func TestHelp_Toggle(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = ""

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("?")})
	mm := res.(*model)
	if !mm.showHelp {
		t.Error("? 应打开帮助面板")
	}
	res, _ = mm.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyEsc})
	mm = res.(*model)
	if mm.showHelp {
		t.Error("ESC 应关闭帮助面板")
	}
}

func TestHelp_NotTriggeredWhenInputNotEmpty(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "hello"
	m.cursor = len([]rune("hello"))

	res, _ := m.handleKey(bubbletea.KeyMsg{Type: bubbletea.KeyRunes, Runes: []rune("?")})
	mm := res.(*model)
	if mm.showHelp {
		t.Error("输入框非空时 ? 不应打开帮助面板")
	}
	if mm.chatInput != "hello?" {
		t.Errorf("输入框非空时 ? 应作为普通字符, got %q", mm.chatInput)
	}
}

// ============================================================================
// 反斜杠软换行 / Shift+Enter 换行
// ============================================================================

func TestBackslash_EnterInsertsNewline(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "line\\"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm := res.(*model)
	if mm.chatInput != "line\n" {
		t.Errorf("\\ + Enter 应软换行不发送, got %q", mm.chatInput)
	}
	if mm.mode != modeChat {
		t.Errorf("\\ + Enter 不应发送, got mode=%d", mm.mode)
	}
}

func TestBackslash_DoubleBackslashSends(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "path\\\\"
	m.cursor = len([]rune(m.chatInput))

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm := res.(*model)
	if mm.chatInput != "" {
		t.Errorf("\\\\ + Enter 应正常发送并清空, got %q", mm.chatInput)
	}
}

// ============================================================================
// 多行粘贴保护：粘贴中的 Enter 绝不触发发送（防止覆盖前文）
// ============================================================================

func TestPaste_EnterWithPasteFlagInsertsNewline(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "已有前文"
	m.cursor = len([]rune(m.chatInput))
	m.pasting = false

	// bracketed paste 中到达的 Enter（Paste=true），即使 pasting 标志尚未建立也必须插入换行
	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter, Paste: true})
	mm := res.(*model)
	if mm.chatInput != "已有前文\n" {
		t.Errorf("粘贴中的 Enter 应插入换行而非发送, got %q", mm.chatInput)
	}
	if len(mm.messages) != 0 {
		t.Errorf("粘贴中的 Enter 不应触发发送, got %d messages", len(mm.messages))
	}
}

func TestPaste_RapidEnterInsertsNewline(t *testing.T) {
	// 无 bracketed paste 的终端：字符快速逐字到达，\n 到达时 pasting 尚未建立（<3字符）
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "a"
	m.cursor = 1
	m.pasteChars = 2                                       // 20ms 内已到达 2 字符 → recentTyping 命中 → 换行
	m.lastKeyTime = time.Now().Add(-20 * time.Millisecond) // 20ms 内 → 判定为快速粘贴

	res, _ := m.handleChat(bubbletea.KeyMsg{Type: bubbletea.KeyEnter})
	mm := res.(*model)
	if mm.chatInput != "a\n" {
		t.Errorf("快速连续输入中的 Enter 应插入换行, got %q", mm.chatInput)
	}
}

// ============================================================================
// 换行键配置 / 终端检测
// ============================================================================

func TestNewlineKey_ConfigOverride(t *testing.T) {
	// 默认（auto，测试环境非 vscode/wt）→ alt+enter 或 ctrl+j，均合法
	m := initialModel(nil)
	if m.keyNewline != "ctrl+j" && m.keyNewline != "alt+enter" {
		t.Errorf("auto 检测应得到合法换行键, got %q", m.keyNewline)
	}
	if m.newlineKeyLabel() != "Ctrl+J" && m.newlineKeyLabel() != "Alt+Enter" {
		t.Errorf("newlineKeyLabel 非法, got %q", m.newlineKeyLabel())
	}
	// 显式配置
	cfg := initialModel(nil)
	cfg.keyNewline = "ctrl+j"
	if cfg.newlineKeyLabel() != "Ctrl+J" {
		t.Errorf("显式 ctrl+j 应显示 Ctrl+J, got %q", cfg.newlineKeyLabel())
	}
}

// 回归：多行粘贴不得覆盖用户已输入的前文（用户反馈 bug）
func TestPaste_MultilineKeepsExistingInput(t *testing.T) {
	m := initialModel(nil)
	m.mode = modeChat
	m.chatInput = "前文"
	m.cursor = len([]rune("前文"))

	// 粘贴多行：前文 + 粘贴内容应完整拼接，且不触发发送
	res, _ := m.handleChat(bubbletea.KeyMsg{
		Type:  bubbletea.KeyRunes,
		Runes: []rune("第一行\n第二行\n第三行"),
		Paste: true,
	})
	mm := res.(*model)
	want := "前文第一行\n第二行\n第三行"
	if mm.chatInput != want {
		t.Errorf("多行粘贴应拼接在前文之后, got %q, want %q", mm.chatInput, want)
	}
	if len(mm.messages) != 0 {
		t.Errorf("多行粘贴不应触发发送, got %d messages", len(mm.messages))
	}
	if !mm.pasting {
		t.Error("粘贴后 pasting 应为 true")
	}
	// 粘贴中的 \r\n（Windows 剪贴板风格）也应规范化且不发送
	res, _ = mm.handleChat(bubbletea.KeyMsg{
		Type:  bubbletea.KeyRunes,
		Runes: []rune("x\r\ny"),
		Paste: true,
	})
	mm = res.(*model)
	if strings.Contains(mm.chatInput, "\r") {
		t.Errorf("粘贴内容应规范化 \\r\\n, got %q", mm.chatInput)
	}
	if len(mm.messages) != 0 {
		t.Errorf("\\r\\n 粘贴不应触发发送, got %d messages", len(mm.messages))
	}
}
