package plugin

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create dir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file %s: %v", path, err)
	}
}

func TestManager_LoadFromDir_Success(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create plugin "hello" with manifest.yaml
	pluginDir := filepath.Join(tmpDir, "hello")
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: hello
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
    prompt_inject: "Hello from plugin"
`)

	// Create plugin "world" with manifest.yaml
	pluginDir2 := filepath.Join(tmpDir, "world")
	writeFile(t, filepath.Join(pluginDir2, "manifest.yaml"), `
name: world
version: "2.0.0"
stages:
  - post
hooks:
  post:
    finalize: "echo done"
`)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir returned error: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 plugins loaded, got %d", count)
	}

	// Verify both plugins are accessible
	p1, ok := mgr.Get("hello")
	if !ok {
		t.Error("expected plugin 'hello' to be loaded")
	}
	if p1 != nil && p1.Manifest.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", p1.Manifest.Version)
	}

	p2, ok := mgr.Get("world")
	if !ok {
		t.Error("expected plugin 'world' to be loaded")
	}
	if p2 != nil && p2.Manifest.Version != "2.0.0" {
		t.Errorf("expected version '2.0.0', got %q", p2.Manifest.Version)
	}
}

func TestManager_LoadFromDir_EmptyDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-empty-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir returned error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 plugins loaded from empty dir, got %d", count)
	}
	if len(mgr.List()) != 0 {
		t.Errorf("expected 0 plugins in manager, got %d", len(mgr.List()))
	}
}

// ============================================================================
// Manager: Load / Get / List / Unload
// ============================================================================

func TestManager_Load(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "test-plugin",
		Version: "1.0.0",
		Stages:  []string{"pre", "post"},
	}
	err := mgr.Load("test-plugin", manifest)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	p, ok := mgr.Get("test-plugin")
	if !ok {
		t.Fatal("plugin not found after Load")
	}
	if p.Name != "test-plugin" {
		t.Errorf("expected name 'test-plugin', got %q", p.Name)
	}
	if p.Status != "loaded" {
		t.Errorf("expected status 'loaded', got %q", p.Status)
	}
	if p.Manifest.Version != "1.0.0" {
		t.Errorf("expected version '1.0.0', got %q", p.Manifest.Version)
	}
}

func TestManager_Load_Overwrite(t *testing.T) {
	mgr := New("/tmp/test-plugins")

	manifest1 := &Manifest{Name: "p", Version: "1.0.0"}
	mgr.Load("p", manifest1)

	manifest2 := &Manifest{Name: "p", Version: "2.0.0"}
	mgr.Load("p", manifest2)

	p, _ := mgr.Get("p")
	if p.Manifest.Version != "2.0.0" {
		t.Errorf("expected version '2.0.0' after overwrite, got %q", p.Manifest.Version)
	}
}

func TestManager_Get_NotFound(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	_, ok := mgr.Get("nonexistent")
	if ok {
		t.Error("Get should return false for nonexistent plugin")
	}
}

func TestManager_List(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	mgr.Load("a", &Manifest{Name: "a", Version: "1.0.0"})
	mgr.Load("b", &Manifest{Name: "b", Version: "1.0.0"})
	mgr.Load("c", &Manifest{Name: "c", Version: "1.0.0"})

	list := mgr.List()
	if len(list) != 3 {
		t.Errorf("expected 3 plugins, got %d", len(list))
	}
}

func TestManager_List_Empty(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	list := mgr.List()
	if len(list) != 0 {
		t.Errorf("expected empty list, got %d", len(list))
	}
}

func TestManager_Unload(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	mgr.Load("p", &Manifest{Name: "p", Version: "1.0.0"})
	mgr.Unload("p")

	_, ok := mgr.Get("p")
	if ok {
		t.Error("plugin should be removed after Unload")
	}
}

func TestManager_Unload_NotFound(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	// Should not panic
	mgr.Unload("nonexistent")
}

// ============================================================================
// Manager: RunPreHooks
// ============================================================================

func TestManager_RunPreHooks_WithInject(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "injector",
		Version: "1.0.0",
		Stages:  []string{"pre"},
		Hooks: HookDef{
			Pre: HookStageDef{
				PromptInject: "SYSTEM: You are a helpful assistant.",
			},
		},
	}
	mgr.Load("injector", manifest)

	snippets := mgr.RunPreHooks()
	if len(snippets) != 1 {
		t.Fatalf("expected 1 pre-hook snippet, got %d", len(snippets))
	}
	if snippets[0].Role != "system" {
		t.Errorf("expected role 'system', got %q", snippets[0].Role)
	}
	if snippets[0].Content != "SYSTEM: You are a helpful assistant." {
		t.Errorf("unexpected content: %q", snippets[0].Content)
	}
}

func TestManager_RunPreHooks_NoPreStage(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "post-only",
		Version: "1.0.0",
		Stages:  []string{"post"},
		Hooks: HookDef{
			Pre: HookStageDef{
				PromptInject: "should not be injected",
			},
		},
	}
	mgr.Load("post-only", manifest)

	snippets := mgr.RunPreHooks()
	if len(snippets) != 0 {
		t.Errorf("expected 0 pre-hook snippets for post-only plugin, got %d", len(snippets))
	}
}

func TestManager_RunPreHooks_EmptyInject(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "empty-injector",
		Version: "1.0.0",
		Stages:  []string{"pre"},
		Hooks: HookDef{
			Pre: HookStageDef{
				PromptInject: "",
			},
		},
	}
	mgr.Load("empty-injector", manifest)

	snippets := mgr.RunPreHooks()
	if len(snippets) != 0 {
		t.Errorf("expected 0 snippets for empty inject, got %d", len(snippets))
	}
}

func TestManager_RunPreHooks_MultiplePlugins(t *testing.T) {
	mgr := New("/tmp/test-plugins")

	mgr.Load("p1", &Manifest{
		Name: "p1", Version: "1.0.0", Stages: []string{"pre"},
		Hooks: HookDef{Pre: HookStageDef{PromptInject: "inject1"}},
	})
	mgr.Load("p2", &Manifest{
		Name: "p2", Version: "1.0.0", Stages: []string{"pre"},
		Hooks: HookDef{Pre: HookStageDef{PromptInject: "inject2"}},
	})
	mgr.Load("p3", &Manifest{
		Name: "p3", Version: "1.0.0", Stages: []string{"post"},
		Hooks: HookDef{Pre: HookStageDef{PromptInject: "should-not-appear"}},
	})

	snippets := mgr.RunPreHooks()
	if len(snippets) != 2 {
		t.Errorf("expected 2 pre-hook snippets, got %d", len(snippets))
	}
}

// ============================================================================
// Manager: RunDuringHooks
// ============================================================================

func TestManager_RunDuringHooks(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "during-plugin",
		Version: "1.0.0",
		Stages:  []string{"during"},
		Hooks: HookDef{
			During: HookStageDef{
				PreTool:  "before_tool.sh",
				PostTool: "after_tool.sh",
			},
		},
	}
	mgr.Load("during-plugin", manifest)

	hooks := mgr.RunDuringHooks()
	if len(hooks) != 2 {
		t.Fatalf("expected 2 during hooks, got %d: %v", len(hooks), hooks)
	}
	foundPre := false
	foundPost := false
	for _, h := range hooks {
		if h == "before_tool.sh" {
			foundPre = true
		}
		if h == "after_tool.sh" {
			foundPost = true
		}
	}
	if !foundPre || !foundPost {
		t.Errorf("missing expected hooks: pre=%v post=%v", foundPre, foundPost)
	}
}

func TestManager_RunDuringHooks_NoDuringStage(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "pre-only",
		Version: "1.0.0",
		Stages:  []string{"pre"},
		Hooks: HookDef{
			During: HookStageDef{
				PreTool: "should_not_run.sh",
			},
		},
	}
	mgr.Load("pre-only", manifest)

	hooks := mgr.RunDuringHooks()
	if len(hooks) != 0 {
		t.Errorf("expected 0 during hooks for pre-only plugin, got %d", len(hooks))
	}
}

func TestManager_RunDuringHooks_EmptyHooks(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "during-empty",
		Version: "1.0.0",
		Stages:  []string{"during"},
	}
	mgr.Load("during-empty", manifest)

	hooks := mgr.RunDuringHooks()
	// Empty PreTool and PostTool are still appended (implementation always appends both)
	if len(hooks) != 2 {
		t.Errorf("expected 2 hooks (empty strings appended), got %d: %v", len(hooks), hooks)
	}
	// Both should be empty strings
	if hooks[0] != "" || hooks[1] != "" {
		t.Errorf("expected empty hooks, got %v", hooks)
	}
}

// ============================================================================
// Manager: RunPostHooks
// ============================================================================

func TestManager_RunPostHooks(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "post-plugin",
		Version: "1.0.0",
		Stages:  []string{"post"},
		Hooks: HookDef{
			Post: HookStageDef{
				Finalize: "finalize.sh",
			},
		},
	}
	mgr.Load("post-plugin", manifest)

	hooks := mgr.RunPostHooks()
	if len(hooks) != 1 {
		t.Fatalf("expected 1 post hook, got %d", len(hooks))
	}
	if hooks[0] != "finalize.sh" {
		t.Errorf("expected 'finalize.sh', got %q", hooks[0])
	}
}

func TestManager_RunPostHooks_NoPostStage(t *testing.T) {
	mgr := New("/tmp/test-plugins")
	manifest := &Manifest{
		Name:    "pre-only-again",
		Version: "1.0.0",
		Stages:  []string{"pre"},
		Hooks: HookDef{
			Post: HookStageDef{
				Finalize: "should_not_run.sh",
			},
		},
	}
	mgr.Load("pre-only-again", manifest)

	hooks := mgr.RunPostHooks()
	if len(hooks) != 0 {
		t.Errorf("expected 0 post hooks for pre-only plugin, got %d", len(hooks))
	}
}

// ============================================================================
// Manager: LoadFromDir Errors
// ============================================================================

func TestManager_LoadFromDir_NonexistentDir(t *testing.T) {
	mgr := New("/tmp")
	n, err := mgr.LoadFromDir("/nonexistent/dir/path")
	if err != nil {
		t.Errorf("expected no error for nonexistent directory, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 plugins from nonexistent directory, got %d", n)
	}
}

func TestManager_LoadFromDir_InvalidYAML(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-bad-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	pluginDir := filepath.Join(tmpDir, "bad-yaml")
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: bad
version: "1.0.0"
stages:
  - pre
  this is invalid yaml: [[[
`)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir should not fail on invalid YAML: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 plugins from invalid YAML, got %d", count)
	}
}

func TestManager_LoadFromDir_NoManifestName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-noname-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Plugin without explicit name should use directory name
	pluginDir := filepath.Join(tmpDir, "inferred-name")
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
version: "1.0.0"
stages:
  - pre
`)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir error: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 plugin loaded, got %d", count)
	}
	p, ok := mgr.Get("inferred-name")
	if !ok {
		t.Error("plugin should be loaded with directory name")
	}
	if p != nil && p.Name != "inferred-name" {
		t.Errorf("expected name 'inferred-name', got %q", p.Name)
	}
}

func TestManager_LoadFromDir_FileNotDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-file-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a file, not a directory
	writeFile(t, filepath.Join(tmpDir, "not-a-plugin.txt"), "hello")

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 plugins (files should be skipped), got %d", count)
	}
}

func TestManager_LoadFromDir_NoManifest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-nomanifest-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Directory without manifest.yaml
	pluginDir := filepath.Join(tmpDir, "no-manifest")
	os.MkdirAll(pluginDir, 0755)
	writeFile(t, filepath.Join(pluginDir, "README.md"), "# Not a plugin")

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir error: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 plugins (no manifest), got %d", count)
	}
}

// ============================================================================
// Plugin: String()
// ============================================================================

func TestPlugin_String(t *testing.T) {
	p := &Plugin{
		Name:     "test",
		Manifest: &Manifest{Version: "1.2.3"},
		Status:   "loaded",
	}
	s := p.String()
	if s == "" {
		t.Error("String() should not return empty")
	}
	// Should contain name, version, status
	for _, substr := range []string{"test", "1.2.3", "loaded"} {
		if !contains(s, substr) {
			t.Errorf("String() should contain %q, got %q", substr, s)
		}
	}
}

// ============================================================================
// Concurrent Safety Tests
// ============================================================================

func TestManager_ConcurrentLoad(t *testing.T) {
	mgr := New("/tmp/concurrent-test")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "plugin-" + itoaPlugin(id)
			mgr.Load(name, &Manifest{Name: name, Version: "1.0.0"})
		}(i)
	}
	wg.Wait()

	list := mgr.List()
	if len(list) != 20 {
		t.Errorf("expected 20 plugins after concurrent load, got %d", len(list))
	}
}

func TestManager_ConcurrentGet(t *testing.T) {
	mgr := New("/tmp/concurrent-get")
	mgr.Load("shared", &Manifest{Name: "shared", Version: "1.0.0"})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, ok := mgr.Get("shared")
			if !ok || p.Name != "shared" {
				t.Error("concurrent Get failed")
			}
		}()
	}
	wg.Wait()
}

func TestManager_ConcurrentLoadUnload(t *testing.T) {
	mgr := New("/tmp/concurrent-lu")
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			name := "lu-" + itoaPlugin(id)
			mgr.Load(name, &Manifest{Name: name, Version: "1.0.0"})
			mgr.Unload(name)
		}(i)
	}
	wg.Wait()

	if len(mgr.List()) != 0 {
		t.Errorf("expected 0 plugins after all unloaded, got %d", len(mgr.List()))
	}
}

func TestManager_ConcurrentRunPreHooks(t *testing.T) {
	mgr := New("/tmp/concurrent-hooks")
	mgr.Load("p1", &Manifest{
		Name: "p1", Version: "1.0.0", Stages: []string{"pre"},
		Hooks: HookDef{Pre: HookStageDef{PromptInject: "inject"}},
	})

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snippets := mgr.RunPreHooks()
			if len(snippets) != 1 {
				t.Errorf("expected 1 snippet, got %d", len(snippets))
			}
		}()
	}
	wg.Wait()
}

// ============================================================================
// Manifest: YAML Marshal/Unmarshal
// ============================================================================

func TestManifest_YAMLRoundtrip(t *testing.T) {
	manifest := &Manifest{
		Name:    "test-plugin",
		Version: "2.0.0",
		Stages:  []string{"pre", "during", "post"},
		Tools: []ToolDef{
			{Name: "search", Description: "Search files", Parameters: map[string]any{"pattern": "string"}},
			{Name: "replace", Description: "Replace content", Parameters: map[string]any{"old": "string", "new": "string"}},
		},
		Hooks: HookDef{
			Pre:    HookStageDef{PromptInject: "You are helpful."},
			During: HookStageDef{PreTool: "before.sh", PostTool: "after.sh"},
			Post:   HookStageDef{Finalize: "cleanup.sh"},
		},
	}

	// Marshal to YAML
	data, err := yaml.Marshal(manifest)
	if err != nil {
		t.Fatalf("yaml marshal: %v", err)
	}

	// Unmarshal back
	var decoded Manifest
	if err := yaml.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("yaml unmarshal: %v", err)
	}

	if decoded.Name != manifest.Name {
		t.Errorf("name: got %q, want %q", decoded.Name, manifest.Name)
	}
	if decoded.Version != manifest.Version {
		t.Errorf("version: got %q, want %q", decoded.Version, manifest.Version)
	}
	if len(decoded.Stages) != 3 {
		t.Errorf("stages: got %d, want 3", len(decoded.Stages))
	}
	if len(decoded.Tools) != 2 {
		t.Errorf("tools: got %d, want 2", len(decoded.Tools))
	}
	if decoded.Tools[0].Name != "search" {
		t.Errorf("tool[0] name: got %q", decoded.Tools[0].Name)
	}
	if decoded.Hooks.Pre.PromptInject != "You are helpful." {
		t.Errorf("pre inject: got %q", decoded.Hooks.Pre.PromptInject)
	}
}

func TestManifest_HasStage(t *testing.T) {
	m := &Manifest{Stages: []string{"pre", "post"}}

	if !m.HasStage("pre") {
		t.Error("should have 'pre' stage")
	}
	if !m.HasStage("post") {
		t.Error("should have 'post' stage")
	}
	if m.HasStage("during") {
		t.Error("should not have 'during' stage")
	}
	if m.HasStage("") {
		t.Error("should not have empty stage")
	}
}

func TestManifest_HasStage_Empty(t *testing.T) {
	m := &Manifest{Stages: nil}
	if m.HasStage("pre") {
		t.Error("nil stages should not have any stage")
	}
}

func TestManifest_GetPreInject(t *testing.T) {
	m := &Manifest{
		Hooks: HookDef{
			Pre: HookStageDef{PromptInject: "SYSTEM PROMPT"},
		},
	}
	if m.GetPreInject() != "SYSTEM PROMPT" {
		t.Errorf("expected 'SYSTEM PROMPT', got %q", m.GetPreInject())
	}
}

func TestManifest_GetPreInject_Empty(t *testing.T) {
	m := &Manifest{}
	if m.GetPreInject() != "" {
		t.Errorf("expected empty, got %q", m.GetPreInject())
	}
}

func TestToolDef_Fields(t *testing.T) {
	td := ToolDef{
		Name:        "read_file",
		Description: "Read a file from disk",
		Parameters: map[string]any{
			"path":   "string",
			"offset": "int",
			"limit":  "int",
		},
	}
	if td.Name != "read_file" {
		t.Errorf("name: got %q", td.Name)
	}
	if td.Description == "" {
		t.Error("description should not be empty")
	}
	if len(td.Parameters) != 3 {
		t.Errorf("parameters: got %d, want 3", len(td.Parameters))
	}
}

func TestHookStageDef_AllFields(t *testing.T) {
	hsd := HookStageDef{
		PromptInject: "system prompt",
		PreTool:      "pre-tool.sh",
		PostTool:     "post-tool.sh",
		Finalize:     "finalize.sh",
	}
	if hsd.PromptInject != "system prompt" {
		t.Error("PromptInject mismatch")
	}
	if hsd.PreTool != "pre-tool.sh" {
		t.Error("PreTool mismatch")
	}
	if hsd.PostTool != "post-tool.sh" {
		t.Error("PostTool mismatch")
	}
	if hsd.Finalize != "finalize.sh" {
		t.Error("Finalize mismatch")
	}
}

// ============================================================================
// Helpers
// ============================================================================

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func itoaPlugin(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := make([]byte, 0, 10)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func TestManager_LoadFromDir_NonExistentDir(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-ne-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	nonExistent := filepath.Join(tmpDir, "does-not-exist")
	mgr := New(tmpDir)
	n, err := mgr.LoadFromDir(nonExistent)
	if err != nil {
		t.Errorf("expected no error for non-existent directory, got %v", err)
	}
	if n != 0 {
		t.Errorf("expected 0 plugins from non-existent directory, got %d", n)
	}
}

func TestManager_LoadFromDir_SkipsFiles(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-files-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a regular file (not a directory) - should be skipped
	writeFile(t, filepath.Join(tmpDir, "README.md"), "# Plugins")

	// Also create a valid plugin
	pluginDir := filepath.Join(tmpDir, "hello")
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: hello
version: "1.0.0"
`)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir returned error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 plugin loaded (file skipped), got %d", count)
	}
}

func TestManager_LoadFromDir_SkipsNoManifest(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-noman-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a directory without a manifest
	noManifestDir := filepath.Join(tmpDir, "no-manifest")
	writeFile(t, filepath.Join(noManifestDir, "script.sh"), "#!/bin/bash\necho hi")

	// Create a valid plugin
	pluginDir := filepath.Join(tmpDir, "hello")
	writeFile(t, filepath.Join(pluginDir, "manifest.yaml"), `
name: hello
version: "1.0.0"
`)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir returned error: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 plugin loaded (no-manifest dir skipped), got %d", count)
	}
}

func TestManager_LoadFromDir_DeduplicateByName(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-plugins-dup-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create two directories that both define the same plugin name
	pluginDir1 := filepath.Join(tmpDir, "a")
	writeFile(t, filepath.Join(pluginDir1, "manifest.yaml"), `
name: same-name
version: "1.0.0"
`)

	pluginDir2 := filepath.Join(tmpDir, "b")
	writeFile(t, filepath.Join(pluginDir2, "manifest.yaml"), `
name: same-name
version: "2.0.0"
`)

	mgr := New(tmpDir)
	count, err := mgr.LoadFromDir(tmpDir)
	if err != nil {
		t.Fatalf("LoadFromDir returned error: %v", err)
	}
	// Both are loaded but the second overwrites the first
	if count != 2 {
		t.Errorf("expected 2 loaded, got %d", count)
	}

	p, ok := mgr.Get("same-name")
	if !ok {
		t.Error("expected plugin 'same-name' to exist")
	}
	// Last one wins
	if p != nil && p.Manifest.Version != "2.0.0" {
		t.Errorf("expected version '2.0.0' (last wins), got %q", p.Manifest.Version)
	}
}
