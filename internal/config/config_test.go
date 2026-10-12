package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/modelgateway"
)

// =============================================================================
// Load tests
// =============================================================================

func TestLoad_ValidConfig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
model:
  backend: "flash-model"
  endpoint: "https://api.example.com/v1"
  api_key: "sk-test-key"
  max_context: 65536

harness:
  score_threshold: 70
  decay_factor: 0.85
  anchor_ratio: 0.3

project:
  root: "/home/user/project"
  vocab: ".contextloom/vocab.json"

acp:
  enabled: true
  port: 8080

plugins:
  enabled: true
  directory: "/tmp/plugins"
  sandbox:
    memory_limit_mb: 512
    cpu_limit: 1.0

overseer:
  pass_threshold: 85
  node_auto_plan: true
  test_tee: true
  temp_doc_policy: "persistent"

memory:
  directory: "/tmp/memory"
  auto_title: true
  max_sessions: 500

cli_defaults:
  model: "default-model"
  effort: "high"
  threshold: 75
  overseer_threshold: 85
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	if cfg == nil {
		t.Fatal("Load returned nil config")
	}

	// Model
	if cfg.ActiveModel().Name != "flash-model" {
		t.Errorf("expected backend 'flash-model', got %q", cfg.ActiveModel().Name)
	}
	if cfg.ActiveModel().Endpoint != "https://api.example.com/v1" {
		t.Errorf("expected endpoint 'https://api.example.com/v1', got %q", cfg.ActiveModel().Endpoint)
	}
	if cfg.ActiveModel().APIKey != "sk-test-key" {
		t.Errorf("expected api_key 'sk-test-key', got %q", cfg.ActiveModel().APIKey)
	}
	if cfg.ActiveModel().MaxContext != 65536 {
		t.Errorf("expected max_context 65536, got %d", cfg.ActiveModel().MaxContext)
	}

	// Harness
	if cfg.Harness.ScoreThreshold != 70 {
		t.Errorf("expected score_threshold 70, got %f", cfg.Harness.ScoreThreshold)
	}
	if cfg.Harness.DecayFactor != 0.85 {
		t.Errorf("expected decay_factor 0.85, got %f", cfg.Harness.DecayFactor)
	}
	if cfg.Harness.AnchorRatio != 0.3 {
		t.Errorf("expected anchor_ratio 0.3, got %f", cfg.Harness.AnchorRatio)
	}

	// Project
	if cfg.Project.Root != "/home/user/project" {
		t.Errorf("expected root '/home/user/project', got %q", cfg.Project.Root)
	}
	if cfg.Project.Vocab != ".contextloom/vocab.json" {
		t.Errorf("expected vocab '.contextloom/vocab.json', got %q", cfg.Project.Vocab)
	}

	// ACP
	if !cfg.ACP.Enabled {
		t.Error("expected ACP enabled")
	}
	if cfg.ACP.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.ACP.Port)
	}

	// Plugins
	if !cfg.Plugins.Enabled {
		t.Error("expected plugins enabled")
	}
	if cfg.Plugins.Directory != "/tmp/plugins" {
		t.Errorf("expected plugins directory '/tmp/plugins', got %q", cfg.Plugins.Directory)
	}
	if cfg.Plugins.Sandbox.MemoryLimitMB != 512 {
		t.Errorf("expected sandbox memory_limit_mb 512, got %d", cfg.Plugins.Sandbox.MemoryLimitMB)
	}
	if cfg.Plugins.Sandbox.CPULimit != 1.0 {
		t.Errorf("expected sandbox cpu_limit 1.0, got %f", cfg.Plugins.Sandbox.CPULimit)
	}

	// Overseer
	if cfg.Overseer.PassThreshold != 85 {
		t.Errorf("expected pass_threshold 85, got %f", cfg.Overseer.PassThreshold)
	}
	if !cfg.Overseer.NodeAutoPlan {
		t.Error("expected node_auto_plan true")
	}
	if !cfg.Overseer.TestTEE {
		t.Error("expected test_tee true")
	}
	if cfg.Overseer.TempDocPolicy != "persistent" {
		t.Errorf("expected temp_doc_policy 'persistent', got %q", cfg.Overseer.TempDocPolicy)
	}

	// Memory
	if cfg.Memory.Directory != "/tmp/memory" {
		t.Errorf("expected memory directory '/tmp/memory', got %q", cfg.Memory.Directory)
	}
	if !cfg.Memory.AutoTitle {
		t.Error("expected auto_title true")
	}
	if cfg.Memory.MaxSessions != 500 {
		t.Errorf("expected max_sessions 500, got %d", cfg.Memory.MaxSessions)
	}

	// CLI Defaults
	if cfg.CLIDefaults.Model != "default-model" {
		t.Errorf("expected cli model 'default-model', got %q", cfg.CLIDefaults.Model)
	}
	if cfg.CLIDefaults.Effort != "high" {
		t.Errorf("expected effort 'high', got %q", cfg.CLIDefaults.Effort)
	}
	if cfg.CLIDefaults.Threshold != 75 {
		t.Errorf("expected threshold 75, got %f", cfg.CLIDefaults.Threshold)
	}
	if cfg.CLIDefaults.OverseerThreshold != 85 {
		t.Errorf("expected overseer_threshold 85, got %f", cfg.CLIDefaults.OverseerThreshold)
	}
}

// =============================================================================
// Load: default path (empty path)
// =============================================================================

func TestLoad_EmptyPath(t *testing.T) {
	// 无配置文件时不再报错：返回内置默认配置（全新部署可启动），
	// 关键项由 Validate() 兜底校验。需保证测试 CWD 无 config.yaml。
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("无配置文件应返回默认配置而非报错: %v", err)
	}
	if cfg.ConfigPath != "" {
		t.Errorf("默认配置 ConfigPath 应为空，得到 %q", cfg.ConfigPath)
	}
	if cfg.Project.Root == "" {
		t.Error("默认配置应推导项目根")
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	// 文件不存在 → 内置默认配置（不报错）；存在但非法 → 仍报错
	cfg, err := Load("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("不存在的配置文件应回退默认配置而非报错: %v", err)
	}
	if cfg.ActiveModel().Name == "" {
		t.Error("默认配置应有后端模型")
	}
}

// =============================================================================
// Load: invalid YAML
// =============================================================================

func TestLoad_InvalidYAML(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	content := "this is not valid: yaml: {{{[[[broken"
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	_, err = Load(configPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

// =============================================================================
// applyDefaults: all defaults
// =============================================================================

func TestApplyDefaults_AllZeros(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()

	if cfg.ActiveModel().Name != "default-model" {
		t.Errorf("expected default backend 'default-model', got %q", cfg.ActiveModel().Name)
	}
	if cfg.ActiveModel().MaxContext != 131072 {
		t.Errorf("expected default max_context 131072, got %d", cfg.ActiveModel().MaxContext)
	}
	if cfg.Harness.ScoreThreshold != 50 {
		t.Errorf("expected default score_threshold 50, got %f", cfg.Harness.ScoreThreshold)
	}
	if cfg.Harness.DecayFactor != 0.9 {
		t.Errorf("expected default decay_factor 0.9, got %f", cfg.Harness.DecayFactor)
	}
	if cfg.Harness.AnchorRatio != 0.2 {
		t.Errorf("expected default anchor_ratio 0.2, got %f", cfg.Harness.AnchorRatio)
	}
	if cfg.ACP.Port != 20250 {
		t.Errorf("expected default ACP port 20250, got %d", cfg.ACP.Port)
	}
	if cfg.CLIDefaults.Effort != "max" {
		t.Errorf("expected default effort 'max', got %q", cfg.CLIDefaults.Effort)
	}
	if cfg.Overseer.PassThreshold != 90 {
		t.Errorf("expected default overseer pass_threshold 90, got %f", cfg.Overseer.PassThreshold)
	}
	if cfg.Memory.MaxSessions != 1000 {
		t.Errorf("expected default memory max_sessions 1000, got %d", cfg.Memory.MaxSessions)
	}
	if cfg.ActiveModel().SendEffort == nil || !*cfg.ActiveModel().SendEffort {
		t.Errorf("expected send_effort default true, got %v", cfg.ActiveModel().SendEffort)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Name != "default-model" {
		t.Errorf("expected 1 default model preset named default-model, got %+v", cfg.Models)
	}
	if cfg.Models[0].Backend != "openai-compatible" {
		t.Errorf("expected default preset backend 'openai-compatible', got %q", cfg.Models[0].Backend)
	}
	if cfg.Models[0].SendEffort == nil || !*cfg.Models[0].SendEffort {
		t.Errorf("expected default preset send_effort true, got %v", cfg.Models[0].SendEffort)
	}
	if cfg.Tools.ApprovalMode != "standard" {
		t.Errorf("expected default approval_mode 'standard', got %q", cfg.Tools.ApprovalMode)
	}
	if cfg.Memory.Directory != ".contextloom/memory" {
		t.Errorf("expected default memory directory '.contextloom/memory', got %q", cfg.Memory.Directory)
	}
	if cfg.Plugins.Directory != "~/.contextloom/plugins" {
		t.Errorf("expected default plugins directory '.contextloom/plugins', got %q", cfg.Plugins.Directory)
	}
}

func TestApplyDefaults_PreservesNonZeroValues(t *testing.T) {
	cfg := &Config{
		ActiveModelName: "custom-backend",
		Models: []ModelPreset{{
			Name:       "custom-backend",
			Backend:    "custom-backend",
			MaxContext: 32768,
		}},
		Harness: HarnessConfig{
			ScoreThreshold: 80,
			DecayFactor:    0.5,
			AnchorRatio:    0.4,
		},
		ACP: ACPConfig{
			Port: 9090,
		},
		Overseer: OverseerConfig{
			PassThreshold: 95,
		},
		Memory: MemoryConfig{
			MaxSessions: 50,
			Directory:   "/custom/memory",
		},
		Plugins: PluginsConfig{
			Directory: "/custom/plugins",
		},
	}
	cfg.applyDefaults()

	// Non-zero values should be preserved
	if cfg.ActiveModel().Name != "custom-backend" {
		t.Errorf("expected backend 'custom-backend' preserved, got %q", cfg.ActiveModel().Name)
	}
	if cfg.ActiveModel().MaxContext != 32768 {
		t.Errorf("expected max_context 32768 preserved, got %d", cfg.ActiveModel().MaxContext)
	}
	if cfg.Harness.ScoreThreshold != 80 {
		t.Errorf("expected score_threshold 80 preserved, got %f", cfg.Harness.ScoreThreshold)
	}
	if cfg.Harness.DecayFactor != 0.5 {
		t.Errorf("expected decay_factor 0.5 preserved, got %f", cfg.Harness.DecayFactor)
	}
	if cfg.Harness.AnchorRatio != 0.4 {
		t.Errorf("expected anchor_ratio 0.4 preserved, got %f", cfg.Harness.AnchorRatio)
	}
	if cfg.ACP.Port != 9090 {
		t.Errorf("expected port 9090 preserved, got %d", cfg.ACP.Port)
	}
	if cfg.Overseer.PassThreshold != 95 {
		t.Errorf("expected pass_threshold 95 preserved, got %f", cfg.Overseer.PassThreshold)
	}
	if cfg.Memory.MaxSessions != 50 {
		t.Errorf("expected max_sessions 50 preserved, got %d", cfg.Memory.MaxSessions)
	}
	if cfg.Memory.Directory != "/custom/memory" {
		t.Errorf("expected memory directory '/custom/memory' preserved, got %q", cfg.Memory.Directory)
	}
	if cfg.Plugins.Directory != "/custom/plugins" {
		t.Errorf("expected plugins directory '/custom/plugins' preserved, got %q", cfg.Plugins.Directory)
	}
}

func TestApplyDefaults_PartialDefaults(t *testing.T) {
	// Only set some fields to zero, others non-zero
	cfg := &Config{
		// Model 不设置：applyDefaults 应填充内置默认预设
		Harness: HarnessConfig{
			ScoreThreshold: 0,   // should get default
			DecayFactor:    0.7, // should be preserved
		},
		Memory: MemoryConfig{
			Directory:   "", // should get default
			MaxSessions: 25, // should be preserved
		},
	}
	cfg.applyDefaults()

	if cfg.ActiveModel().Name != "default-model" {
		t.Errorf("expected default backend, got %q", cfg.ActiveModel().Name)
	}
	if cfg.ActiveModel().MaxContext != 131072 {
		t.Errorf("expected default max_context, got %d", cfg.ActiveModel().MaxContext)
	}
	if cfg.Harness.ScoreThreshold != 50 {
		t.Errorf("expected default score_threshold, got %f", cfg.Harness.ScoreThreshold)
	}
	if cfg.Harness.DecayFactor != 0.7 {
		t.Errorf("expected decay_factor 0.7 preserved, got %f", cfg.Harness.DecayFactor)
	}
	if cfg.Memory.Directory != ".contextloom/memory" {
		t.Errorf("expected default memory directory, got %q", cfg.Memory.Directory)
	}
	if cfg.Memory.MaxSessions != 25 {
		t.Errorf("expected max_sessions 25 preserved, got %d", cfg.Memory.MaxSessions)
	}
}

// =============================================================================
// Load with YAML that omits fields (should get defaults)
// =============================================================================

func TestLoad_MinimalConfig_DefaultsApplied(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	content := `
model:
  backend: "default-model"
harness:
  score_threshold: 60
`
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Specified values
	if cfg.ActiveModel().Name != "default-model" {
		t.Errorf("expected backend 'default-model', got %q", cfg.ActiveModel().Name)
	}
	if cfg.Harness.ScoreThreshold != 60 {
		t.Errorf("expected score_threshold 60, got %f", cfg.Harness.ScoreThreshold)
	}

	// Defaults applied for unspecified fields
	if cfg.ActiveModel().MaxContext != 131072 {
		t.Errorf("expected default max_context 131072, got %d", cfg.ActiveModel().MaxContext)
	}
	if cfg.Harness.DecayFactor != 0.9 {
		t.Errorf("expected default decay_factor 0.9, got %f", cfg.Harness.DecayFactor)
	}
	if cfg.Harness.AnchorRatio != 0.2 {
		t.Errorf("expected default anchor_ratio 0.2, got %f", cfg.Harness.AnchorRatio)
	}
	if cfg.Overseer.PassThreshold != 90 {
		t.Errorf("expected default overseer pass_threshold 90, got %f", cfg.Overseer.PassThreshold)
	}
	if cfg.Memory.MaxSessions != 1000 {
		t.Errorf("expected default memory max_sessions 1000, got %d", cfg.Memory.MaxSessions)
	}
	if cfg.Memory.Directory != ".contextloom/memory" {
		t.Errorf("expected default memory directory '.contextloom/memory', got %q", cfg.Memory.Directory)
	}
	if cfg.Plugins.Directory != "~/.contextloom/plugins" {
		t.Errorf("expected default plugins directory '~/.contextloom/plugins', got %q", cfg.Plugins.Directory)
	}
	if cfg.ACP.Port != 20250 {
		t.Errorf("expected default ACP port 20250, got %d", cfg.ACP.Port)
	}
}

// =============================================================================
// Config struct field types
// =============================================================================

func TestConfigStructTypes(t *testing.T) {
	cfg := &Config{
		ActiveModelName: "test-backend",
		Models: []ModelPreset{{
			Name:    "test-backend",
			Backend: "test-backend",
		}},
		Harness: HarnessConfig{
			ScoreThreshold: 50,
			DecayFactor:    0.9,
			AnchorRatio:    0.2,
		},
		Project: ProjectConfig{
			Root:  "/project",
			Vocab: "vocab.json",
		},
		ACP: ACPConfig{
			Enabled: true,
			Port:    20250,
		},
		Plugins: PluginsConfig{
			Enabled:   false,
			Directory: "/plugins",
			Sandbox: PluginsSandbox{
				MemoryLimitMB: 256,
				CPULimit:      0.5,
			},
		},
		Overseer: OverseerConfig{
			PassThreshold: 90,
			NodeAutoPlan:  true,
			TestTEE:       false,
			TempDocPolicy: "session_only",
		},
		Memory: MemoryConfig{
			Directory:   "/memory",
			AutoTitle:   false,
			MaxSessions: 100,
		},
		CLIDefaults: CLIDefaultsConfig{
			Model:             "model-v1",
			Effort:            "high",
			Threshold:         70,
			OverseerThreshold: 85,
		},
	}

	// Verify all fields are accessible and correctly typed
	if cfg.ActiveModel().Name != "test-backend" {
		t.Error("model backend mismatch")
	}
	if cfg.Harness.AnchorRatio != 0.2 {
		t.Error("harness anchor_ratio mismatch")
	}
	if cfg.Project.Root != "/project" {
		t.Error("project root mismatch")
	}
	if cfg.ACP.Enabled != true {
		t.Error("acp enabled mismatch")
	}
	if cfg.Plugins.Sandbox.MemoryLimitMB != 256 {
		t.Error("plugins sandbox memory mismatch")
	}
	if cfg.Overseer.TestTEE != false {
		t.Error("overseer test_tee mismatch")
	}
	if cfg.Memory.AutoTitle != false {
		t.Error("memory auto_title mismatch")
	}
	if cfg.CLIDefaults.Threshold != 70 {
		t.Error("cli threshold mismatch")
	}
}

// =============================================================================
// Load: edge cases
// =============================================================================

func TestLoad_EmptyFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(""), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load of empty file should not error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load of empty file returned nil")
	}
	// All defaults should be applied
	if cfg.ActiveModel().Name != "default-model" {
		t.Errorf("expected default backend, got %q", cfg.ActiveModel().Name)
	}
}

func TestLoad_OnlyComments(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "config-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "config.yaml")
	content := "# This is a comment\n# Another comment\n"
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load of comments-only file should not error: %v", err)
	}
	if cfg == nil {
		t.Fatal("Load of comments-only file returned nil")
	}
	// All defaults should be applied
	if cfg.ActiveModel().Name != "default-model" {
		t.Errorf("expected default backend, got %q", cfg.ActiveModel().Name)
	}
}

func TestKeyNewline_DefaultAuto(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if cfg.CLIDefaults.KeyNewline != "auto" {
		t.Errorf("key_newline 默认应为 auto, got %q", cfg.CLIDefaults.KeyNewline)
	}
}

func TestKeyNewline_ParsedFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	content := "model:\n  backend: x\n  endpoint: http://x\ncli_defaults:\n  key_newline: \"alt+enter\"\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLIDefaults.KeyNewline != "alt+enter" {
		t.Errorf("key_newline 应解析为 alt+enter, got %q", cfg.CLIDefaults.KeyNewline)
	}
}

// =============================================================================
// resolveProjectRoot: 项目根推导（默认 = 启动时工作目录 CWD，与配置文件位置解耦）
// =============================================================================

func writeMinimalConfig(t *testing.T, dir, rootLine string) string {
	t.Helper()
	content := "model:\n  backend: x\n  endpoint: http://x\n"
	if rootLine != "" {
		content += "project:\n  root: " + rootLine + "\n"
	}
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveProjectRoot_EmptyUsesCWD(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	// 配置文件放在临时目录，但项目根应为"启动时所在目录"（与配置位置解耦）
	cfgDir := t.TempDir()
	projDir := t.TempDir()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	path := writeMinimalConfig(t, cfgDir, "")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// macOS 上 /var 是 /private/var 符号链接，Getwd 返回真实路径，需统一
	if real, err := filepath.EvalSymlinks(projDir); err == nil {
		projDir = real
	}
	if cfg.Project.Root != projDir {
		t.Errorf("root 为空时应为启动时工作目录 %q, got %q", projDir, cfg.Project.Root)
	}
}

func TestResolveProjectRoot_DotUsesCWD(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	cfgDir := t.TempDir()
	projDir := t.TempDir()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	path := writeMinimalConfig(t, cfgDir, `"."`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(projDir); err == nil {
		projDir = real
	}
	if cfg.Project.Root != projDir {
		t.Errorf("root 为 '.' 时应为启动时工作目录 %q, got %q", projDir, cfg.Project.Root)
	}
}

func TestResolveProjectRoot_ExplicitRootPreserved(t *testing.T) {
	dir := t.TempDir()
	path := writeMinimalConfig(t, dir, `"/custom/project"`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project.Root != "/custom/project" {
		t.Errorf("显式 root 应保留, got %q", cfg.Project.Root)
	}
}

func TestResolveProjectRoot_RelativeConfigPath(t *testing.T) {
	// 模拟在项目目录内执行: ./contextloom (默认 config.yaml 跟随可执行文件目录，此处回退 CWD)
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("config.yaml", []byte("model:\n  backend: x\n  endpoint: http://x\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.Abs(dir)
	// macOS 上 /var 是 /private/var 的符号链接，Getwd 返回解析后路径，需统一
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	if cfg.Project.Root != want {
		t.Errorf("相对配置路径应以当前目录为项目根, want %q got %q", want, cfg.Project.Root)
	}
}

// 部署场景：config.yaml 跟随可执行文件所在目录（DefaultConfigPath 优先 exe 目录），
// 项目根不受影响，仍为启动时 CWD。
func TestResolveProjectRoot_ConfigInExeDir_ProjectInCWD(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	exeDir := t.TempDir()
	projDir := t.TempDir()
	if err := os.Chdir(projDir); err != nil {
		t.Fatal(err)
	}
	// 模拟 exe 目录存在 config.yaml（DefaultConfigPath 会优先找到它）
	writeMinimalConfig(t, exeDir, "")
	// 显式指定该配置（等价于 DefaultConfigPath 命中 exe 目录）
	cfg, err := Load(filepath.Join(exeDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(projDir); err == nil {
		projDir = real
	}
	if cfg.Project.Root != projDir {
		t.Errorf("项目根应为启动目录 %q, got %q", projDir, cfg.Project.Root)
	}
}

// =============================================================================
// Load: default path resolution (contextloom 所在目录优先)
// =============================================================================

func TestDefaultConfigPath_FallbackToCWD(t *testing.T) {
	// 可执行文件所在目录通常无 config.yaml（测试二进制位于 /tmp/go-build...），
	// 应回退到当前工作目录的 "config.yaml"
	p := DefaultConfigPath()
	if p == "" {
		t.Fatal("DefaultConfigPath returned empty")
	}
	// 无论回退与否，路径必须以 config.yaml 结尾
	if filepath.Base(p) != "config.yaml" {
		t.Fatalf("DefaultConfigPath should end with config.yaml, got %q", p)
	}
}

func TestFileExists(t *testing.T) {
	if fileExists("/nonexistent/definitely/not/here.yaml") {
		t.Fatal("fileExists should return false for missing file")
	}
	tmp := filepath.Join(t.TempDir(), "x.yaml")
	if err := os.WriteFile(tmp, []byte("a: b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !fileExists(tmp) {
		t.Fatal("fileExists should return true for existing file")
	}
}

// =============================================================================
// DefaultConfigPath 查找链（符号链接解析 / CONTEXTLOOM_CONFIG / ~/.contextloom 兜底）
// =============================================================================

// normPath 归一化路径（EvalSymlinks 失败时原样返回），
// 用于消除 macOS 上 /var→/private/var、/tmp→/private/tmp 的符号链接差异。
func normPath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return real
}

// TestDefaultConfigPathFromExe_SymlinkResolution：exe 是符号链接（PATH 全局命令
// 最常见形态），真实本体目录有 config.yaml → 命中本体目录而非链接目录/CWD。
// 这是历史缺陷的核心场景：os.Executable() 返回链接路径，原实现会静默回退 CWD。
func TestDefaultConfigPathFromExe_SymlinkResolution(t *testing.T) {
	realDir := t.TempDir()
	linkDir := t.TempDir()
	wantCfg := writeMinimalConfig(t, realDir, "")
	// 本体文件（内容无所谓，仅需存在以便建符号链接）
	realExe := filepath.Join(realDir, "contextloom")
	if err := os.WriteFile(realExe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkExe := filepath.Join(linkDir, "contextloom")
	if err := os.Symlink(realExe, linkExe); err != nil {
		t.Skipf("当前平台不支持符号链接: %v", err)
	}
	got := defaultConfigPathFromExe(linkExe)
	if normPath(t, got) != normPath(t, wantCfg) {
		t.Fatalf("符号链接执行应命中真实本体目录配置, want %q got %q", wantCfg, got)
	}
}

// TestDefaultConfigPathFromExe_ExeDirOverCWD：exe 目录与 CWD 同时有配置 →
// exe 目录优先（向后兼容语义不变）。
func TestDefaultConfigPathFromExe_ExeDirOverCWD(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	exeDir := t.TempDir()
	cwdDir := t.TempDir()
	wantCfg := writeMinimalConfig(t, exeDir, "")
	// CWD 放一份不同的配置（不应被选中）
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwdDir, "config.yaml"), []byte("model:\n  backend: cwd\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := defaultConfigPathFromExe(filepath.Join(exeDir, "contextloom"))
	if normPath(t, got) != normPath(t, wantCfg) {
		t.Fatalf("exe 目录配置应优先于 CWD, want %q got %q", wantCfg, got)
	}
}

// TestDefaultConfigPathFromExe_CWDFallback：exe 目录无配置 → 回退 CWD config.yaml。
func TestDefaultConfigPathFromExe_CWDFallback(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	cwdDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatal(err)
	}
	writeMinimalConfig(t, cwdDir, "")
	got := defaultConfigPathFromExe(filepath.Join(t.TempDir(), "no-such-dir", "contextloom"))
	// CWD 回退返回相对路径 "config.yaml"（与原实现语义一致），基于 CWD 归一化后比对
	if !filepath.IsAbs(got) {
		got = filepath.Join(cwdDir, got)
	}
	if normPath(t, got) != normPath(t, filepath.Join(cwdDir, "config.yaml")) {
		t.Fatalf("exe 目录无配置时应回退 CWD, got %q", got)
	}
}

// TestDefaultConfigPathFromExe_HomeFallback：exe 目录与 CWD 均无配置 →
// ~/.contextloom/config.yaml 兜底。
func TestDefaultConfigPathFromExe_HomeFallback(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	home := t.TempDir()
	// 同时设置 HOME/USERPROFILE，覆盖 macOS 与 Windows 的 os.UserHomeDir 读取源
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, ".contextloom", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte("model:\n  backend: home\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := defaultConfigPathFromExe(filepath.Join(t.TempDir(), "no-such-dir", "contextloom"))
	if normPath(t, got) != normPath(t, cfg) {
		t.Fatalf("应回退 ~/.contextloom/config.yaml, want %q got %q", cfg, got)
	}
}

// TestDefaultConfigPathFromExe_NoConfigAnywhere：全部不存在 → 兜底返回
// "config.yaml"（Load 内 IsNotExist → 内置默认配置）。
func TestDefaultConfigPathFromExe_NoConfigAnywhere(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got := defaultConfigPathFromExe(filepath.Join(t.TempDir(), "no-such-dir", "contextloom"))
	if got != "config.yaml" {
		t.Fatalf("全部不存在时应兜底返回 config.yaml, got %q", got)
	}
}

// TestDefaultConfigPathFromExe_EmptyExe：exe 为空（os.Executable 失败场景）→
// 跳过 exe 目录探测，仍走 CWD/~/.contextloom 链。
func TestDefaultConfigPathFromExe_EmptyExe(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	cwdDir := t.TempDir()
	if err := os.Chdir(cwdDir); err != nil {
		t.Fatal(err)
	}
	writeMinimalConfig(t, cwdDir, "")
	got := defaultConfigPathFromExe("")
	// CWD 回退返回相对路径 "config.yaml"，基于 CWD 归一化后比对
	if !filepath.IsAbs(got) {
		got = filepath.Join(cwdDir, got)
	}
	if normPath(t, got) != normPath(t, filepath.Join(cwdDir, "config.yaml")) {
		t.Fatalf("exe 为空应回退 CWD, got %q", got)
	}
}

// TestDefaultConfigPath_EnvOverride：CONTEXTLOOM_CONFIG 环境变量最高优先级
// （覆盖 exe 目录探测）。
func TestDefaultConfigPath_EnvOverride(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "custom-config.yaml")
	if err := os.WriteFile(cfg, []byte("model:\n  backend: env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXTLOOM_CONFIG", cfg)
	if got := DefaultConfigPath(); got != cfg {
		t.Fatalf("CONTEXTLOOM_CONFIG 应优先, want %q got %q", cfg, got)
	}
}

// TestSendEffort_ExplicitFalse：显式 send_effort: false 不被默认值覆盖
func TestSendEffort_ExplicitFalse(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("model:\n  backend: deepseek\n  send_effort: false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ActiveModel().SendEffort == nil || *cfg.ActiveModel().SendEffort {
		t.Fatalf("显式 false 应保持, got %v", cfg.ActiveModel().SendEffort)
	}
}

// TestModels_ParsedFromYAML：models 列表从配置解析（多预设）
func TestModels_ParsedFromYAML(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	body := `model:
  backend: default-model
models:
  - name: default-model
    backend: deepseek
    endpoint: ""
    api_key: ""
    max_context: 131072
  - name: gpt-4o
    backend: openai
    endpoint: https://api.openai.com/v1
    api_key: sk-test
    max_context: 128000
    supports_vision: true
    vision_model: gpt-4o
    send_effort: false
`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Models) != 2 {
		t.Fatalf("want 2 presets, got %d", len(cfg.Models))
	}
	if cfg.Models[0].Name != "default-model" || cfg.Models[0].MaxContext != 131072 {
		t.Fatalf("preset0 wrong: %+v", cfg.Models[0])
	}
	m1 := cfg.Models[1]
	if m1.Name != "gpt-4o" || m1.Backend != "openai" || m1.Endpoint != "https://api.openai.com/v1" ||
		m1.APIKey != "sk-test" || !m1.SupportsVision || m1.VisionModel != "gpt-4o" {
		t.Fatalf("preset1 wrong: %+v", m1)
	}
	if m1.SendEffort == nil || *m1.SendEffort {
		t.Fatalf("preset1 send_effort 应为 false, got %v", m1.SendEffort)
	}
}

// TestApplyPatch_SendEffort：config.set 可热更新 send_effort 开关
func TestApplyPatch_SendEffort(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if err := cfg.ApplyPatch(map[string]any{"model.send_effort": false}); err != nil {
		t.Fatal(err)
	}
	if am := cfg.ActiveModel(); am.SendEffort == nil || *am.SendEffort {
		t.Fatalf("send_effort 应为 false, got %v", am.SendEffort)
	}
	if err := cfg.ApplyPatch(map[string]any{"model.send_effort": true}); err != nil {
		t.Fatal(err)
	}
	if am := cfg.ActiveModel(); am.SendEffort == nil || !*am.SendEffort {
		t.Fatalf("send_effort 应为 true, got %v", am.SendEffort)
	}
}

// TestGatewayModelConfig_BridgesVisionCapability：gateway.pools 同名预设
// 桥接时必须补齐 supports_vision / vision_model。网关路由路径（配置了
// gateway: 节时全部请求都经此）曾漏桥接能力标志 → 建出的实例
// SupportsVision() 恒 false → 即便预设 supports_vision=true 也提示
// "当前模型不支持视觉输入"而降级（用户报障根因）。
func TestGatewayModelConfig_BridgesVisionCapability(t *testing.T) {
	sv := true
	cfg := &Config{
		ActiveModelName: "vision-model",
		Models: []ModelPreset{{
			Name:           "vision-model",
			Backend:        "mock",
			Endpoint:       "http://vision.local/v1",
			APIKey:         "sk-v",
			MaxContext:     262144,
			SupportsVision: true,
			VisionModel:    "vision-model-x",
			SendEffort:     &sv,
		}},
		Gateway: modelgateway.GatewayConfig{
			Default: "default",
			// 池名与预设名同名才桥接（真实 config.yaml 的 gateway.pools 即如此：
			// 池 "moonshotai/kimi-k3" 对应同名预设）。池只声明治理参数，
			// 连接与能力参数全部靠同名预设桥接。
			Pools: map[string]modelgateway.PoolConfig{
				"vision-model": {MaxConcurrent: 4},
			},
		},
	}

	out := cfg.GatewayModelConfig()
	pc, ok := out.Pools["vision-model"]
	if !ok {
		t.Fatal("vision-model 池应存在")
	}
	if !pc.SupportsVision {
		t.Fatal("池应从同名预设桥接 supports_vision=true（缺失会让网关路径误降级）")
	}
	if pc.Endpoint != "http://vision.local/v1" || pc.APIKey != "sk-v" || pc.MaxContext != 262144 {
		t.Fatalf("连接参数桥接不完整: %+v", pc)
	}
}
