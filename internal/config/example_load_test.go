package config

import (
	"os"
	"testing"
)

// TestExampleYAML_NewFormat 验证 config.example.yaml 新格式（active_model + models）加载正确。
func TestExampleYAML_NewFormat(t *testing.T) {
	cfg, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("Load example: %v", err)
	}
	if cfg.ActiveModelName != "local-model" {
		t.Errorf("active_model = %q, 期望 local-model", cfg.ActiveModelName)
	}
	am := cfg.ActiveModel()
	if am.Name != "local-model" || am.Backend != "openai-compatible" {
		t.Errorf("激活预设错误: %+v", am)
	}
	if am.MaxContext != 1000000 || am.Endpoint == "" {
		t.Errorf("激活预设字段未完整加载: maxCtx=%d ep=%q", am.MaxContext, am.Endpoint)
	}
	if am.SendEffort == nil || !*am.SendEffort {
		t.Error("send_effort 默认应 true")
	}
}

// TestLegacyModelSection_Migrated 验证旧 model: 配置节自动迁移为 active_model + models。
func TestLegacyModelSection_Migrated(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/legacy.yaml"
	os.WriteFile(p, []byte("model:\n  backend: \"old-model\"\n  api_key: \"sk-x\"\n  max_context: 65536\n"), 0644)
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ActiveModelName != "old-model" {
		t.Errorf("迁移后 active_model = %q, 期望 old-model", cfg.ActiveModelName)
	}
	am := cfg.ActiveModel()
	if am.Name != "old-model" || am.MaxContext != 65536 || am.APIKey != "sk-x" {
		t.Errorf("迁移后预设字段错误: %+v", am)
	}
	// 保存后不应再含 model: 节
	if err := cfg.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(p)
	if containsYAMLKey(string(data), "model:") {
		t.Errorf("保存后不应包含 model: 节:\n%s", data)
	}
}
