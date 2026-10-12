package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/plugin"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── v2 插件体系：工具注册 / message 钩子 / route 钩子 ──

// writePluginScript 写入插件脚本并赋予可执行权限
func writePluginScript(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestNew_PluginToolsRegistered(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins", "demo")
	writePluginScript(t, filepath.Join(pluginDir, "tool.sh"), `#!/bin/sh
cat
`)
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: demo
version: "0.1.0"
stages:
  - pre
tools:
  - name: router_status
    description: "查询路由状态"
    parameters:
      type: object
      properties:
        session:
          type: string
    script: "tool.sh"
    timeout: 10
`)
	cfg := testConfigAt(tmpDir)
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defs := rt.Tools.ToolDefinitions()
	found := false
	for _, d := range defs {
		if d.Function.Name == "plugin_router_status" {
			found = true
			if d.Function.Description != "查询路由状态" {
				t.Errorf("description = %q", d.Function.Description)
			}
			props, ok := d.Function.Parameters["properties"].(map[string]any)
			if !ok || props["session"] == nil {
				t.Errorf("schema 未完整透传: %v", d.Function.Parameters)
			}
		}
	}
	if !found {
		t.Fatalf("plugin_router_status 未注册，已注册: %v", toolNamesFromDefs(defs))
	}
}

func TestNew_PluginToolExecution(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins", "demo")
	writePluginScript(t, filepath.Join(pluginDir, "tool.sh"), `#!/bin/sh
cat
`)
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: demo
version: "0.1.0"
stages:
  - pre
tools:
  - name: echo_params
    description: "回显参数"
    parameters:
      type: object
      properties:
        msg:
          type: string
    script: "tool.sh"
`)
	rt, err := New(testConfigAt(tmpDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	result, err := rt.Tools.Execute(toolCtxForTest(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "plugin_echo_params",
			Arguments: `{"msg":"你好插件"}`,
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("result.Error = %q", result.Error)
	}
	out := resultString(result)
	if !strings.Contains(out, "你好插件") {
		t.Errorf("脚本输出 = %q, want 含参数回显", out)
	}
}

func TestRuntime_PluginMessageHooks(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins", "p")
	writePluginScript(t, filepath.Join(pluginDir, "inject.sh"), `#!/bin/sh
# stdin = JSON，stdout = 引导段
echo "[引导] 请先规划再执行"
`)
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: p
version: "1.0.0"
stages:
  - message
hooks:
  message:
    inject: "inject.sh"
`)
	rt, err := New(testConfigAt(tmpDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	refs := rt.Plugins.MessageInjects()
	if len(refs) != 1 {
		t.Fatalf("MessageInjects len = %d", len(refs))
	}
	out := rt.runPluginMessageHooks(refs, "实现登录", 1)
	if !strings.Contains(out, "请先规划再执行") {
		t.Errorf("message 钩子输出 = %q", out)
	}
	// 失败语义：脚本不存在 → 静默空（不阻断）
	bad := []plugin.MessageInjectRef{{PluginName: "x", ScriptPath: filepath.Join(tmpDir, "nope.sh")}}
	if out := rt.runPluginMessageHooks(bad, "x", 1); out != "" {
		t.Errorf("失败脚本应返回空, got %q", out)
	}
}

func TestRuntime_PluginRouteHooks(t *testing.T) {
	tmpDir := t.TempDir()
	pluginDir := filepath.Join(tmpDir, "plugins", "p")
	writePluginScript(t, filepath.Join(pluginDir, "route.sh"), `#!/bin/sh
echo "[路由] 采用 spec 模式"
`)
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: p
version: "1.0.0"
stages:
  - route
hooks:
  route:
    script: "route.sh"
`)
	rt, err := New(testConfigAt(tmpDir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	refs := rt.Plugins.RouteScripts()
	if len(refs) != 1 {
		t.Fatalf("RouteScripts len = %d", len(refs))
	}
	out := rt.runPluginRouteHooks(refs, "task", "实现登录", 1)
	if !strings.Contains(out, "spec 模式") {
		t.Errorf("route 钩子输出 = %q", out)
	}
}

// ── 辅助 ──

func toolCtxForTest() context.Context {
	return context.Background()
}

func resultString(r *types.ToolResult) string {
	if r == nil {
		return ""
	}
	if s, ok := r.Result.(string); ok {
		return s
	}
	return ""
}

func testConfigAt(tmpDir string) *config.Config {
	return &config.Config{
		ActiveModelName: "mock",
		Models: []config.ModelPreset{{
			Name:       "mock",
			Backend:    "mock",
			Endpoint:   "http://localhost:8080",
			APIKey:     "sk-test",
			MaxContext: 4096,
		}},
		Harness: config.HarnessConfig{
			ScoreThreshold: 50,
			DecayFactor:    0.9,
			AnchorRatio:    0.2,
		},
		Memory: config.MemoryConfig{
			Directory:   filepath.Join(tmpDir, "memory"),
			MaxSessions: 10,
		},
		Plugins: config.PluginsConfig{
			Directory: filepath.Join(tmpDir, "plugins"),
			Enabled:   true,
		},
		Project: config.ProjectConfig{
			Root:  tmpDir,
			Vocab: filepath.Join(tmpDir, "vocab.json"),
		},
		CLIDefaults: config.CLIDefaultsConfig{
			Model:  "mock-model",
			Effort: "medium",
		},
	}
}

func toolNamesFromDefs(defs []types.ToolDefinition) []string {
	var names []string
	for _, d := range defs {
		names = append(names, d.Function.Name)
	}
	return names
}

func TestSanitizeToolName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"router_status", "router_status"},
		{"plugin.router", "plugin_router"},
		{"mcp.github.search", "mcp_github_search"},
		{"中文.工具", "_____"},
		{"a b-c.d", "a_b-c_d"},
		{"", "tool"},
	}
	for _, c := range cases {
		if got := sanitizeToolName(c.in); got != c.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
