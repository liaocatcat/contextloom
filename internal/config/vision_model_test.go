package config

import (
	"os"
	"strings"
	"testing"
)

// TestVisionModel_RoundTrip 识图模型配置：ApplyPatch 设置 → Sanitized 可读 →
// YAML 保存/加载往返一致；默认空（不切换）。
func TestVisionModel_RoundTrip(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	if am := cfg.ActiveModel(); am.VisionModel != "" {
		t.Errorf("默认 VisionModel = %q，期望空", am.VisionModel)
	}

	if err := cfg.ApplyPatch(map[string]any{"model.vision_model": "  vision-model-1  "}); err != nil {
		t.Fatalf("ApplyPatch 失败: %v", err)
	}
	// setStr 保留原值（含空格），验证 Sanitized 可读即可
	if cfg.ActiveModel().VisionModel == "" {
		t.Fatalf("VisionModel 未生效")
	}
	san := cfg.Sanitized()
	if san["model.vision_model"] != cfg.ActiveModel().VisionModel {
		t.Errorf("Sanitized[model.vision_model] = %v，期望 %v", san["model.vision_model"], cfg.ActiveModel().VisionModel)
	}

	// schema 中存在且分组正确
	found := false
	for _, f := range ConfigSchema() {
		if f.Key == "model.vision_model" {
			found = true
			if f.Group != "模型" || f.Type != "text" {
				t.Errorf("schema 分组/类型错误: %+v", f)
			}
		}
	}
	if !found {
		t.Errorf("schema 缺少 model.vision_model")
	}
}

// TestVisionModel_LoadFromYAML 从 YAML 加载 vision_model（大小写/注释无碍）。
func TestVisionModel_LoadFromYAML(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.yaml"
	yamlText := `
model:
  backend: "default-model"
  vision_model: "my-vision-model"
harness:
  score_threshold: 50
`
	if err := os.WriteFile(path, []byte(yamlText), 0o644); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load 失败: %v", err)
	}
	if cfg.ActiveModel().VisionModel != "my-vision-model" {
		t.Errorf("VisionModel = %q，期望 my-vision-model", cfg.ActiveModel().VisionModel)
	}
	if !strings.Contains(cfg.ActiveModel().VisionModel, "vision") {
		t.Errorf("加载值异常: %q", cfg.ActiveModel().VisionModel)
	}
}
