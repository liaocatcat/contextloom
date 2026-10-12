package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSchema_Completeness 验证 schema 覆盖所有 Sanitized 字段，且与 yaml 配置一致。
func TestSchema_Completeness(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	san := cfg.Sanitized()
	schema := ConfigSchema()
	if len(schema) == 0 {
		t.Fatal("schema 为空")
	}
	// schema 中的每个 key 必须能在 Sanitized 中找到（当前值可读）
	seen := map[string]bool{}
	for _, f := range schema {
		seen[f.Key] = true
		if _, ok := san[f.Key]; !ok {
			t.Errorf("schema 字段 %s 不在 Sanitized 输出中", f.Key)
		}
		if f.Type == "" || f.Group == "" || f.Label == "" {
			t.Errorf("schema 字段 %s 缺类型/分组/标签", f.Key)
		}
		if f.Type == "select" && len(f.Enums) == 0 {
			t.Errorf("select 字段 %s 缺枚举", f.Key)
		}
	}
	// Sanitized 每个 key 必须在 schema 中（无遗漏字段）
	for k := range san {
		if !seen[k] {
			t.Errorf("Sanitized 字段 %s 不在 schema 中", k)
		}
	}
	// 分组完整性
	groups := map[string]bool{}
	for _, f := range schema {
		groups[f.Group] = true
	}
	for _, g := range []string{"模型", "Harness 检索", "项目", "ACP", "插件", "监工", "记忆", "上下文预算", "CLI 默认", "工具"} {
		if !groups[g] {
			t.Errorf("缺少分组: %s", g)
		}
	}
}

// TestSanitized_MasksAPIKey 验证 api_key 脱敏。
func TestSanitized_MasksAPIKey(t *testing.T) {
	cfg := &Config{Models: []ModelPreset{{Name: "deepseek", Backend: "deepseek", APIKey: "sk-abcdef1234567890"}}, ActiveModelName: "deepseek"}
	cfg.applyDefaults()
	san := cfg.Sanitized()
	k, _ := san["model.api_key"].(string)
	if !strings.Contains(k, "****") || strings.Contains(k, "abcdef1234567890") {
		t.Fatalf("api_key 未脱敏: %q", k)
	}
	if !strings.HasSuffix(k, "7890") {
		t.Fatalf("脱敏应保留末尾: %q", k)
	}
	// 空 key 不 panic
	cfg2 := &Config{}
	cfg2.applyDefaults()
	if k2, _ := cfg2.Sanitized()["model.api_key"].(string); k2 != "" {
		t.Fatalf("空 key 应为空串: %q", k2)
	}
}

// TestApplyPatch_BasicFields 验证各类字段应用。
func TestApplyPatch_BasicFields(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	patch := map[string]any{
		"model.max_context":         262144.0,
		"harness.score_threshold":   70.0,
		"acp.enabled":               false,
		"cli_defaults.effort":       "xhigh",
		"overseer.temp_doc_policy":  "persistent",
		"plugins.sandbox.cpu_limit": 0.8,
		"model.temperature":         0.7,
		"tools.approval_mode":       "permissive",
		"context.keep_recent":       16,
		"cli_defaults.key_newline":  "ctrl+j",
		"memory.auto_title":         false,
		"model.api_key":             "sk-new-real-key-123456",
	}
	if err := cfg.ApplyPatch(patch); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if am := cfg.ActiveModel(); am.MaxContext != 262144 {
		t.Errorf("max_context: %d", am.MaxContext)
	}
	if cfg.Harness.ScoreThreshold != 70 {
		t.Errorf("score_threshold: %v", cfg.Harness.ScoreThreshold)
	}
	if cfg.ACP.Enabled {
		t.Error("acp.enabled 应为 false")
	}
	if cfg.CLIDefaults.Effort != "xhigh" {
		t.Errorf("effort: %s", cfg.CLIDefaults.Effort)
	}
	if cfg.Overseer.TempDocPolicy != "persistent" {
		t.Errorf("temp_doc_policy: %s", cfg.Overseer.TempDocPolicy)
	}
	if cfg.Plugins.Sandbox.CPULimit != 0.8 {
		t.Errorf("cpu_limit: %v", cfg.Plugins.Sandbox.CPULimit)
	}
	if am := cfg.ActiveModel(); am.Temperature == nil || *am.Temperature != 0.7 {
		t.Errorf("temperature: %v", am.Temperature)
	}
	if cfg.Tools.ApprovalMode != "permissive" {
		t.Errorf("approval_mode: %s", cfg.Tools.ApprovalMode)
	}
	if cfg.Context.KeepRecent != 16 {
		t.Errorf("keep_recent: %d", cfg.Context.KeepRecent)
	}
	if cfg.CLIDefaults.KeyNewline != "ctrl+j" {
		t.Errorf("key_newline: %s", cfg.CLIDefaults.KeyNewline)
	}
	if cfg.Memory.AutoTitle {
		t.Error("auto_title 应为 false")
	}
	if am := cfg.ActiveModel(); am.APIKey != "sk-new-real-key-123456" {
		t.Errorf("api_key: %s", am.APIKey)
	}
}

// TestApplyPatch_MaskedKeySkipped 验证脱敏 api_key 不覆盖真实值。
func TestApplyPatch_MaskedKeySkipped(t *testing.T) {
	cfg := &Config{Models: []ModelPreset{{Name: "deepseek", Backend: "deepseek", APIKey: "sk-real-key-abc"}}, ActiveModelName: "deepseek"}
	cfg.applyDefaults()
	if err := cfg.ApplyPatch(map[string]any{"model.api_key": "sk-****abcd"}); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	if cfg.ActiveModel().APIKey != "sk-real-key-abc" {
		t.Fatalf("脱敏值不应覆盖真实 key: %s", cfg.ActiveModel().APIKey)
	}
}

// TestApplyPatch_UnknownKey 验证未知 key 报错。
func TestApplyPatch_UnknownKey(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	err := cfg.ApplyPatch(map[string]any{"model.bogus": 1})
	if err == nil || !strings.Contains(err.Error(), "未知配置项") {
		t.Fatalf("未知 key 应报错: %v", err)
	}
	// 类型错误
	err = cfg.ApplyPatch(map[string]any{"acp.enabled": "yes"})
	if err == nil {
		t.Fatal("类型错误应报错")
	}
}

// TestSave_RoundTrip 验证 Save 写盘 → Load 重载一致（往返）。
func TestSave_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	cfg := &Config{}
	cfg.applyDefaults()
	cfg.ActiveModelName = "legacy-model-id-0731"
	cfg.Models = []ModelPreset{{Name: "legacy-model-id-0731", Backend: "deepseek", APIKey: "sk-test-key", MaxContext: 131072}}
	cfg.Models[0].APIKey = "sk-test-key"
	cfg.Context.HardRatio = 0.88
	cfg.CLIDefaults.Effort = "high"
	cfg.Overseer.NodeAutoPlan = false
	if err := cfg.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// 首次保存无原文件 → 无 .bak（正常）；再次保存应生成 .bak 备份
	if _, err := os.Stat(p + ".bak"); err == nil {
		t.Error("首次保存不应有 .bak")
	}
	cfg.ActiveModelName = "default-model-changed"
	cfg.Models = []ModelPreset{{Name: "default-model-changed", Backend: "deepseek", APIKey: "sk-test-key", MaxContext: 131072}}
	if err := cfg.Save(p); err != nil {
		t.Fatalf("Save#2: %v", err)
	}
	if _, err := os.Stat(p + ".bak"); err != nil {
		t.Errorf("再次保存应有 .bak 备份: %v", err)
	}
	reloaded, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded.ActiveModelName != "default-model-changed" {
		t.Errorf("active_model 不一致: %s", reloaded.ActiveModelName)
	}
	if reloaded.ActiveModel().APIKey != "sk-test-key" {
		t.Errorf("api_key 不一致: %s", reloaded.ActiveModel().APIKey)
	}
	if reloaded.Context.HardRatio != 0.88 {
		t.Errorf("hard_ratio 不一致: %v", reloaded.Context.HardRatio)
	}
	if reloaded.CLIDefaults.Effort != "high" {
		t.Errorf("effort 不一致: %s", reloaded.CLIDefaults.Effort)
	}
	if reloaded.Overseer.NodeAutoPlan {
		t.Error("node_auto_plan 不一致")
	}
	if reloaded.ConfigPath != p {
		t.Errorf("ConfigPath 不一致: %s", reloaded.ConfigPath)
	}
}
