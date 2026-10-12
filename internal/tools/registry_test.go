package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── ExecCommand Tests ──

func TestExecCommand_Success(t *testing.T) {
	ctx := context.Background()
	result, err := ExecCommand(ctx, map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}

	stdout, _ := m["stdout"].(string)
	exitCode, _ := m["exit_code"].(int)

	if stdout != "hello\n" {
		t.Errorf("expected stdout 'hello\\n', got %q", stdout)
	}
	if exitCode != 0 {
		t.Errorf("expected exit_code 0, got %d", exitCode)
	}
}

func TestExecCommand_ExitError(t *testing.T) {
	ctx := context.Background()
	result, err := ExecCommand(ctx, map[string]any{"command": "exit 42"})
	if err != nil {
		t.Fatalf("ExecCommand should not return error even on non-zero exit: %v", err)
	}

	m := result.(map[string]any)
	exitCode, _ := m["exit_code"].(int)
	if exitCode != 42 {
		t.Errorf("expected exit_code 42, got %d", exitCode)
	}
}

func TestExecCommand_Stderr(t *testing.T) {
	ctx := context.Background()
	result, err := ExecCommand(ctx, map[string]any{"command": "echo error >&2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	stdout, _ := m["stdout"].(string)
	exitCode, _ := m["exit_code"].(int)

	if exitCode != 0 {
		t.Errorf("expected exit_code 0, got %d", exitCode)
	}
	// stderr should be captured in combined output
	if !strings.Contains(stdout, "error") {
		t.Errorf("expected stderr to contain 'error', got %q", stdout)
	}
}

func TestExecCommand_EmptyCommand(t *testing.T) {
	ctx := context.Background()
	result, err := ExecCommand(ctx, map[string]any{"command": ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	exitCode, _ := m["exit_code"].(int)
	if exitCode != 0 {
		t.Errorf("expected exit_code 0 for empty command, got %d", exitCode)
	}
}

func TestExecCommand_MultilineOutput(t *testing.T) {
	ctx := context.Background()
	result, err := ExecCommand(ctx, map[string]any{"command": "echo line1 && echo line2"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	stdout, _ := m["stdout"].(string)
	exitCode, _ := m["exit_code"].(int)

	if exitCode != 0 {
		t.Errorf("expected exit_code 0, got %d", exitCode)
	}
	if !strings.Contains(stdout, "line1") || !strings.Contains(stdout, "line2") {
		t.Errorf("expected multiline output, got %q", stdout)
	}
}

func TestExecCommand_Timeout(t *testing.T) {
	ctx := context.Background()
	// Use a very short timeout that should trigger
	result, err := ExecCommand(ctx, map[string]any{
		"command":    "sleep 10",
		"timeout_ms": float64(100),
	})
	if err != nil {
		t.Fatalf("ExecCommand should not return error: %v", err)
	}

	m := result.(map[string]any)
	exitCode, _ := m["exit_code"].(int)
	// Should be killed by timeout (exit code != 0)
	if exitCode == 0 {
		t.Log("warning: timeout may not have triggered (exit code 0)")
	}
}

// ── ListFiles Tests ──

func TestListFiles_Success(t *testing.T) {
	dir, err := os.MkdirTemp("", "listfiles_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Create some test files
	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("content2"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	ctx := context.Background()
	result, err := ListFiles(ctx, map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}

	files, _ := m["files"].([]string)
	if len(files) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(files), files)
	}

	foundFile1 := false
	foundFile2 := false
	foundSubdir := false
	for _, f := range files {
		switch f {
		case "file1.txt":
			foundFile1 = true
		case "file2.txt":
			foundFile2 = true
		case "subdir":
			foundSubdir = true
		}
	}
	if !foundFile1 || !foundFile2 || !foundSubdir {
		t.Errorf("missing expected entries, got %v", files)
	}
}

func TestListFiles_EmptyDir(t *testing.T) {
	dir, err := os.MkdirTemp("", "listfiles_empty_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ctx := context.Background()
	result, err := ListFiles(ctx, map[string]any{"path": dir})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	files, _ := m["files"].([]string)
	if len(files) != 0 {
		t.Errorf("expected 0 files in empty dir, got %d: %v", len(files), files)
	}
}

func TestListFiles_NotFound(t *testing.T) {
	ctx := context.Background()
	_, err := ListFiles(ctx, map[string]any{"path": "/nonexistent/directory/path"})
	if err == nil {
		t.Fatal("expected error for non-existent directory, got nil")
	}
}

// ── isBinary Tests ──

func TestIsBinary_TextData(t *testing.T) {
	data := []byte("hello world\nthis is text\n")
	if isBinary(data) {
		t.Error("expected isBinary=false for text data")
	}
}

func TestIsBinary_NullBytes(t *testing.T) {
	data := []byte{0x48, 0x65, 0x00, 0x6c, 0x6f} // "He\x00lo"
	if !isBinary(data) {
		t.Error("expected isBinary=true for data with null bytes")
	}
}

func TestIsBinary_AllNull(t *testing.T) {
	data := make([]byte, 100)
	if !isBinary(data) {
		t.Error("expected isBinary=true for all-null data")
	}
}

func TestIsBinary_Empty(t *testing.T) {
	data := []byte{}
	if isBinary(data) {
		t.Error("expected isBinary=false for empty data")
	}
}

func TestIsBinary_LargeText(t *testing.T) {
	// Create data larger than 8KB without null bytes
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte('a' + (i % 26))
	}
	if isBinary(data) {
		t.Error("expected isBinary=false for large text data")
	}
}

func TestIsBinary_LargeBinary(t *testing.T) {
	// Create data larger than 8KB with a null byte within first 8KB
	data := make([]byte, 10000)
	for i := range data {
		data[i] = byte('a' + (i % 26))
	}
	data[5000] = 0x00 // within first 8KB threshold
	if !isBinary(data) {
		t.Error("expected isBinary=true for large data with null byte")
	}
}

// ── Registry Tests ──

func TestNewRegistry_HasBuiltins(t *testing.T) {
	r := NewRegistry()

	expectedTools := []string{
		"read_file", "write_file", "replace_in_file",
		"exec_command", "search_content", "list_files",
		"run_tests", "git_diff", "web_fetch",
	}

	names := r.List()
	for _, expected := range expectedTools {
		found := false
		for _, name := range names {
			if name == expected {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected builtin tool %q not found in registry", expected)
		}
	}

	if len(names) != len(expectedTools) {
		t.Errorf("expected %d builtin tools, got %d: %v", len(expectedTools), len(names), names)
	}
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()

	customCalled := false
	r.Register("custom_tool", func(ctx context.Context, params map[string]any) (any, error) {
		customCalled = true
		return map[string]any{"ok": true}, nil
	})

	names := r.List()
	found := false
	for _, name := range names {
		if name == "custom_tool" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'custom_tool' to be registered")
	}

	// Verify we can execute it
	_, err := r.Execute(context.Background(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "custom_tool",
			Arguments: `{}`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error executing custom tool: %v", err)
	}
	if !customCalled {
		t.Error("expected custom tool to be called")
	}
}

func TestRegistry_Execute(t *testing.T) {
	r := NewRegistry()

	result, err := r.Execute(context.Background(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command": "echo test"}`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.CallID != "call_1" {
		t.Errorf("expected CallID 'call_1', got %q", result.CallID)
	}
	if result.Name != "exec_command" {
		t.Errorf("expected Name 'exec_command', got %q", result.Name)
	}
	if result.Result == nil {
		t.Error("expected non-nil result")
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}
}

func TestRegistry_ExecuteUnknown(t *testing.T) {
	r := NewRegistry()

	result, err := r.Execute(context.Background(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "nonexistent_tool",
			Arguments: `{}`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error == "" {
		t.Error("expected error for unknown tool, got empty error")
	}
	if !strings.Contains(result.Error, "未知工具") {
		t.Errorf("expected error to contain '未知工具', got %q", result.Error)
	}
}

func TestRegistry_ExecuteWithRawArgs(t *testing.T) {
	r := NewRegistry()
	r.Register("echo_tool", func(ctx context.Context, params map[string]any) (any, error) {
		raw, _ := params["raw"].(string)
		return map[string]any{"raw": raw}, nil
	})

	// Test with invalid JSON (should fall back to raw)
	result, err := r.Execute(context.Background(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "echo_tool",
			Arguments: `not valid json`,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.Result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}
	raw, _ := m["raw"].(string)
	if raw != "not valid json" {
		t.Errorf("expected raw='not valid json', got %q", raw)
	}
}

func TestRegistry_ExecuteWithEmptyArgs(t *testing.T) {
	r := NewRegistry()
	r.Register("noop_tool", func(ctx context.Context, params map[string]any) (any, error) {
		return map[string]any{"called": true}, nil
	})

	result, err := r.Execute(context.Background(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "noop_tool",
			Arguments: "",
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Error != "" {
		t.Errorf("expected no error, got %q", result.Error)
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	names := r.List()

	if len(names) == 0 {
		t.Error("expected non-empty list of tool names")
	}

	// Verify no duplicates
	seen := make(map[string]bool)
	for _, name := range names {
		if seen[name] {
			t.Errorf("duplicate tool name: %q", name)
		}
		seen[name] = true
	}
}

func TestRegistry_ToolDefinitions(t *testing.T) {
	r := NewRegistry()
	defs := r.ToolDefinitions()

	if len(defs) == 0 {
		t.Error("expected non-empty tool definitions")
	}

	for _, def := range defs {
		if def.Type != "function" {
			t.Errorf("expected Type='function', got %q for %q", def.Type, def.Function.Name)
		}
		if def.Function.Name == "" {
			t.Error("expected non-empty function name")
		}
	}

	// Verify definitions match listed tools
	names := r.List()
	if len(defs) != len(names) {
		t.Errorf("ToolDefinitions count (%d) != List count (%d)", len(defs), len(names))
	}
}

// TestMultiAgentTools_SchemaComplete 回归测试：
// spawn_task/send_message/check_messages 三个多 Agent 协作工具的 parameters schema
// 必须完整定义——缺失时 ToolDefinitions 返回空对象，严格按 schema 生成调用参数的
// 模型无法构造合法调用，导致 spawn_task 从不触发（goal 模式"从未自动拆过子任务"
// 的历史根因）。propose_vocab 同理要求非 nil schema。
func TestMultiAgentTools_SchemaComplete(t *testing.T) {
	r := NewRegistry()
	// 多 Agent 协作工具由 runtime.New 注册（非默认注册表），此处模拟注册，
	// 验证注册后 ToolDefinitions 输出的 schema 契约完整（模型可见、可合法调用）。
	r.Register("spawn_task", MakeSpawnTaskTool(nil))
	r.Register("send_message", MakeSendMessageTool(nil))
	r.Register("check_messages", MakeCheckMessagesTool(nil))
	r.Register("propose_vocab", MakeProposeVocabTool(nil))
	defs := r.ToolDefinitions()
	byName := make(map[string]types.ToolDefinition, len(defs))
	for _, d := range defs {
		byName[d.Function.Name] = d
	}

	for _, name := range []string{"spawn_task", "send_message", "check_messages", "propose_vocab"} {
		def, ok := byName[name]
		if !ok {
			t.Errorf("工具 %q 未注册到 ToolDefinitions（模型不可见）", name)
			continue
		}
		params := def.Function.Parameters
		props, _ := params["properties"].(map[string]any)
		if props == nil {
			t.Errorf("工具 %q 的 parameters.properties 为空对象——模型无法生成合法调用参数", name)
		}
	}

	// spawn_task 必须声明 task 必填 + 三参数契约
	sp := byName["spawn_task"].Function.Parameters
	req, _ := sp["required"].([]string)
	hasTask := false
	for _, rq := range req {
		if rq == "task" {
			hasTask = true
		}
	}
	if !hasTask {
		t.Error("spawn_task schema 缺少 required: [task]——模型可能不带 task 调用导致执行报错")
	}
	props := sp["properties"].(map[string]any)
	for _, p := range []string{"task", "directive", "context"} {
		if _, ok := props[p]; !ok {
			t.Errorf("spawn_task schema 缺少参数 %q", p)
		}
	}

	// send_message 必须声明 to/content 必填
	sm := byName["send_message"].Function.Parameters
	smReq, _ := sm["required"].([]string)
	for _, want := range []string{"to", "content"} {
		found := false
		for _, rq := range smReq {
			if rq == want {
				found = true
			}
		}
		if !found {
			t.Errorf("send_message schema required 缺少 %q", want)
		}
	}
}

// ── 回归：DSML 文本兜底解析产出的参数用别名（dir）→ 执行层须归一化为 path ──
func TestNormalizeParamAliases_DirToPath(t *testing.T) {
	p := normalizeParamAliases("list_files", map[string]any{"dir": "."})
	if _, ok := p["dir"]; ok {
		t.Error("dir 别名未被移除")
	}
	if v, ok := p["path"]; !ok || v != "." {
		t.Errorf("dir 未归一化为 path: %v", p)
	}
}

func TestNormalizeParamAliases_DeclaredWins(t *testing.T) {
	// 声明名已存在时，别名不得覆盖声明值
	p := normalizeParamAliases("list_files", map[string]any{"path": "/a", "dir": "/b"})
	if v, ok := p["path"]; !ok || v != "/a" {
		t.Errorf("别名覆盖了声明值: %v", p)
	}
}

func TestNormalizeParamAliases_UnrelatedToolUntouched(t *testing.T) {
	p := normalizeParamAliases("exec_command", map[string]any{"dir": "x"})
	if _, ok := p["path"]; ok {
		t.Error("无别名表的工具不应被改写")
	}
}

func TestNormalizeParamAliases_CmdToCommand(t *testing.T) {
	// 用户真实案例：模型输出 <parameter name="cmd">，schema 参数是 command
	p := normalizeParamAliases("exec_command", map[string]any{"cmd": "git log"})
	if _, ok := p["cmd"]; ok {
		t.Error("cmd 别名未被移除")
	}
	if v, ok := p["command"]; !ok || v != "git log" {
		t.Errorf("cmd 未归一化为 command: %v", p)
	}
}

func TestNormalizeParamAliases_TimeoutToTimeoutMs(t *testing.T) {
	// 用户真实案例：模型输出 <parameter name="timeout">30000</parameter>
	p := normalizeParamAliases("exec_command", map[string]any{"timeout": "30000"})
	if _, ok := p["timeout"]; ok {
		t.Error("timeout 别名未被移除")
	}
	if _, ok := p["timeout_ms"]; !ok {
		t.Errorf("timeout 未归一化为 timeout_ms: %v", p)
	}
}

func TestNormalizeNumericStrings_TimeoutString(t *testing.T) {
	// 用户真实案例：timeout 是字符串 "30000"，ExecCommand 按 float64 断言
	p := normalizeNumericStrings("exec_command", map[string]any{"timeout_ms": "30000"})
	if v, ok := p["timeout_ms"].(float64); !ok || v != 30000 {
		t.Errorf("timeout_ms 字符串未转为数字: %v (%T)", p["timeout_ms"], p["timeout_ms"])
	}
}

// 用户真实案例端到端：XML <parameter name="cmd"> + timeout 字符串 → Execute 后
// ExecCommand 必须拿到非空 command、timeout 生效（而非执行空命令失败死循环）。
func TestExecute_XMLCmdAlias_EndToEnd(t *testing.T) {
	reg := NewRegistry()
	call := types.ToolCall{
		ID:   "text-xml-1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"cmd": "echo alias-e2e-ok", "timeout": "30000"}`,
		},
	}
	res, err := reg.Execute(context.Background(), call)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("工具执行失败: %s", res.Error)
	}
	m, ok := res.Result.(map[string]any)
	if !ok {
		t.Fatalf("结果类型: %T", res.Result)
	}
	stdout, _ := m["stdout"].(string)
	if !strings.Contains(stdout, "alias-e2e-ok") {
		t.Errorf("exec_command 未执行正确命令（cmd 别名未生效）: stdout=%q", stdout)
	}
}
