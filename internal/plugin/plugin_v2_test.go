package plugin

import (
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// ── v2 扩展：message/route 钩子 + tools 真实注册 ──

func TestManifest_ParseV2Fields(t *testing.T) {
	data := []byte(`
name: router
version: "0.1.0"
stages:
  - pre
  - message
  - route
hooks:
  message:
    inject: "scripts/inject.sh"
  route:
    script: "scripts/route.sh"
tools:
  - name: dev_router_status
    description: "查询路由状态"
    parameters:
      type: object
      properties:
        session:
          type: string
      required:
        - session
    script: "scripts/router_status.sh"
    timeout: 10
`)
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !m.HasMessageHook() {
		t.Error("HasMessageHook = false, want true")
	}
	if m.Hooks.Message.Inject != "scripts/inject.sh" {
		t.Errorf("message.inject = %q", m.Hooks.Message.Inject)
	}
	if !m.HasRouteHook() {
		t.Error("HasRouteHook = false, want true")
	}
	if m.Hooks.Route.Script != "scripts/route.sh" {
		t.Errorf("route.script = %q", m.Hooks.Route.Script)
	}
	if len(m.Tools) != 1 {
		t.Fatalf("tools len = %d, want 1", len(m.Tools))
	}
	tool := m.Tools[0]
	if tool.Script != "scripts/router_status.sh" {
		t.Errorf("tool.script = %q", tool.Script)
	}
	if tool.ToolTimeout() != 10 {
		t.Errorf("tool timeout = %d, want 10", tool.ToolTimeout())
	}
	props, ok := tool.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties 类型错误: %T", tool.Parameters["properties"])
	}
	if _, ok := props["session"]; !ok {
		t.Error("properties 缺少 session")
	}
}

func TestManifest_OldFormatCompatible(t *testing.T) {
	// 旧格式（无 message/route、parameters 为 map[string]string）必须仍可解析
	data := []byte(`
name: legacy
version: "1.0.0"
stages:
  - pre
tools:
  - name: greet
    description: "Say hello"
    parameters:
      name: string
hooks:
  pre:
    prompt_inject: "Hello"
`)
	var m Manifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal legacy: %v", err)
	}
	if m.HasMessageHook() {
		t.Error("legacy 不应有 message hook")
	}
	if m.HasRouteHook() {
		t.Error("legacy 不应有 route hook")
	}
	if m.GetPreInject() != "Hello" {
		t.Errorf("pre inject = %q", m.GetPreInject())
	}
	if m.Tools[0].Script != "" {
		t.Error("legacy tool 不应有 script")
	}
	if m.Tools[0].ToolTimeout() != 0 {
		t.Error("legacy tool timeout 应为 0（默认）")
	}
}

func TestManager_MessageInjects_And_RouteScripts(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "a", "manifest.yaml"), `
name: a
version: "1.0.0"
stages:
  - message
hooks:
  message:
    inject: "inject.sh"
`)
	writeFile(t, filepath.Join(tmpDir, "b", "manifest.yaml"), `
name: b
version: "1.0.0"
stages:
  - route
hooks:
  route:
    script: "route.sh"
`)
	// c 无钩子
	writeFile(t, filepath.Join(tmpDir, "c", "manifest.yaml"), `
name: c
version: "1.0.0"
stages:
  - pre
hooks:
  pre:
    prompt_inject: "x"
`)

	mgr := New(tmpDir)
	if _, err := mgr.LoadFromDir(tmpDir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	msgs := mgr.MessageInjects()
	if len(msgs) != 1 || msgs[0].PluginName != "a" {
		t.Fatalf("MessageInjects = %+v, want [a]", msgs)
	}
	if filepath.Base(msgs[0].ScriptPath) != "inject.sh" {
		t.Errorf("script path = %q", msgs[0].ScriptPath)
	}

	routes := mgr.RouteScripts()
	if len(routes) != 1 || routes[0].PluginName != "b" {
		t.Fatalf("RouteScripts = %+v, want [b]", routes)
	}
}

func TestManager_ToolRegistrations(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "p", "manifest.yaml"), `
name: p
version: "1.0.0"
stages:
  - pre
tools:
  - name: with_script
    description: "有脚本实现"
    parameters:
      type: object
      properties: {}
    script: "tool.sh"
  - name: meta_only
    description: "纯声明无脚本，不注册"
    parameters:
      name: string
`)
	mgr := New(tmpDir)
	if _, err := mgr.LoadFromDir(tmpDir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}

	regs := mgr.ToolRegistrations()
	if len(regs) != 1 {
		t.Fatalf("ToolRegistrations len = %d, want 1（无脚本工具不注册）", len(regs))
	}
	r := regs[0]
	if r.Name != "with_script" || r.PluginName != "p" {
		t.Errorf("reg = %+v", r)
	}
	if filepath.Base(r.ScriptPath) != "tool.sh" {
		t.Errorf("script path = %q", r.ScriptPath)
	}
	if _, ok := r.Schema["type"]; !ok {
		t.Error("schema 应透传 type 字段")
	}
	if r.TimeoutSec != 0 {
		t.Errorf("timeout = %d, want 0（默认）", r.TimeoutSec)
	}
}

func TestManager_ToolRegistrations_DefaultSchema(t *testing.T) {
	tmpDir := t.TempDir()
	writeFile(t, filepath.Join(tmpDir, "p", "manifest.yaml"), `
name: p
version: "1.0.0"
stages:
  - pre
tools:
  - name: no_params
    description: "无 parameters"
    script: "tool.sh"
`)
	mgr := New(tmpDir)
	if _, err := mgr.LoadFromDir(tmpDir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	regs := mgr.ToolRegistrations()
	if len(regs) != 1 {
		t.Fatalf("len = %d", len(regs))
	}
	if regs[0].Schema["type"] != "object" {
		t.Errorf("缺省 schema 应为 object: %v", regs[0].Schema)
	}
}
