package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestMCPServerHelper 是 runtime 层 MCP 集成测试的 fake server helper：
// GO_WANT_RT_MCP_HELPER=1 时从 stdin 读 JSON-RPC 并响应 initialize/tools/list/tools/call。
func TestMCPServerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_RT_MCP_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &req) != nil || req.ID == nil {
			continue
		}
		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{"id": *req.ID, "result": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}}}
		case "tools/list":
			resp = map[string]any{"id": *req.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "hello", "description": "打招呼", "inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string"}},
						"required": []string{"name"},
					}},
				},
			}}
		case "tools/call":
			resp = map[string]any{"id": *req.ID, "result": map[string]any{
				"content": []map[string]any{{"type": "text", "text": "hello from mcp"}},
			}}
		default:
			resp = map[string]any{"id": *req.ID, "error": map[string]any{"code": -32601, "message": "not found"}}
		}
		enc.Encode(resp)
	}
	os.Exit(0)
}

// TestRuntime_MCPServerRegistration 集成验证：runtime.New 按配置启动 MCP server，
// 工具注册为 mcp.<server>.<tool>，ToolDefinitions 携带完整 schema，执行转发正常。
func TestRuntime_MCPServerRegistration(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models: []config.ModelPreset{{
			Name:       "mock",
			Backend:    "mock",
			Endpoint:   "http://localhost:8080",
			APIKey:     "sk-test",
			MaxContext: 4096,
		}},
		Harness: config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:  config.MemoryConfig{Directory: filepath.Join(tmpDir, "memory"), MaxSessions: 10},
		Plugins: config.PluginsConfig{Directory: filepath.Join(tmpDir, "plugins")},
		Project: config.ProjectConfig{Root: tmpDir, Vocab: filepath.Join(tmpDir, "vocab.json")},
		MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"fake": {
				Command: os.Args[0],
				Args:    []string{"-test.run=TestMCPServerHelper"},
				Env:     []string{"GO_WANT_RT_MCP_HELPER=1"},
			},
		}},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.CloseMCPServers()

	// 工具已注册且 schema 完整（required 保留）
	defs := rt.Tools.ToolDefinitions()
	found := false
	var desc string
	var params map[string]any
	for _, d := range defs {
		if d.Function.Name == "mcp_fake_hello" {
			found = true
			desc = d.Function.Description
			params = d.Function.Parameters
			break
		}
	}
	if !found {
		var names []string
		for _, d := range defs {
			names = append(names, d.Function.Name)
		}
		t.Fatalf("应注册 mcp_fake_hello，实际工具: %v", names)
	}
	if !strings.Contains(desc, "MCP[fake]") {
		t.Fatalf("描述应含 server 名: %q", desc)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok || props["name"] == nil {
		t.Fatalf("schema 应含 name 属性: %v", params)
	}
	req, ok := params["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "name" {
		t.Fatalf("schema required 应含 name: %v", params["required"])
	}

	// 执行转发：mcp_fake_hello → tools/call → "hello from mcp"
	args, _ := json.Marshal(map[string]any{"name": "x"})
	res, err := rt.Tools.Execute(context.Background(), types.ToolCall{
		ID: "c1",
		Function: types.FunctionCall{
			Name:      "mcp_fake_hello",
			Arguments: string(args),
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := ""
	if res.Result != nil {
		got = fmt.Sprintf("%v", res.Result)
	}
	if !strings.Contains(got, "hello from mcp") {
		t.Fatalf("执行结果应含 hello from mcp，got %q", got)
	}
}
