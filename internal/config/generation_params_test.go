package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 生成参数（输出/输入/思考长度）解耦配置的契约测试。
// 对标 DeepSeek Harness 的 max_tokens 与 contextWindow 解耦设计：
// max_output_tokens/max_input_tokens 独立于 max_context；
// thinking_length/thinking_enabled 独立控制思考行为。

// TestGenerationParams_Schema 校验 4 个新字段的 schema 元数据
// （类型/分组/默认值/枚举/数值范围）——GUI 设置面板依赖此清单渲染。
func TestGenerationParams_Schema(t *testing.T) {
	byKey := map[string]FieldSchema{}
	for _, f := range ConfigSchema() {
		byKey[f.Key] = f
	}

	mo, ok := byKey["model.max_output_tokens"]
	if !ok {
		t.Fatal("schema 缺少 model.max_output_tokens")
	}
	if mo.Type != "number" || mo.Group != "模型" {
		t.Errorf("max_output_tokens 类型/分组错误: %+v", mo)
	}
	if mo.Default != 0 {
		t.Errorf("max_output_tokens 默认应为 0, got %v", mo.Default)
	}
	if mo.Min == nil || *mo.Min != 0 || mo.Max == nil || *mo.Max != 100000000 || mo.Step != 1024 {
		t.Errorf("max_output_tokens 数值范围错误: %+v", mo)
	}

	mi, ok := byKey["model.max_input_tokens"]
	if !ok {
		t.Fatal("schema 缺少 model.max_input_tokens")
	}
	if mi.Type != "number" || mi.Group != "模型" {
		t.Errorf("max_input_tokens 类型/分组错误: %+v", mi)
	}
	if mi.Default != 0 {
		t.Errorf("max_input_tokens 默认应为 0, got %v", mi.Default)
	}
	if mi.Min == nil || *mi.Min != 0 || mi.Max == nil || *mi.Max != 100000000 || mi.Step != 1024 {
		t.Errorf("max_input_tokens 数值范围错误: %+v", mi)
	}

	tl, ok := byKey["model.thinking_length"]
	if !ok {
		t.Fatal("schema 缺少 model.thinking_length")
	}
	if tl.Type != "select" || tl.Group != "模型" || tl.Default != "" {
		t.Errorf("thinking_length 类型/分组/默认值错误: %+v", tl)
	}
	wantEnums := []string{"", "off", "low", "medium", "high", "xhigh", "max"}
	if len(tl.Enums) != len(wantEnums) {
		t.Fatalf("thinking_length 枚举错误: %v", tl.Enums)
	}
	for i := range wantEnums {
		if tl.Enums[i] != wantEnums[i] {
			t.Fatalf("thinking_length 枚举[%d] = %q, 期望 %q", i, tl.Enums[i], wantEnums[i])
		}
	}
	if tl.Enums[0] != "" {
		t.Fatal("thinking_length 枚举首项必须为空字符串（表达继承语义）")
	}

	te, ok := byKey["model.thinking_enabled"]
	if !ok {
		t.Fatal("schema 缺少 model.thinking_enabled")
	}
	if te.Type != "bool" || te.Group != "模型" || te.Default != true {
		t.Errorf("thinking_enabled 类型/分组/默认值错误: %+v", te)
	}
}

// TestGenerationParams_SanitizedDefaults 默认值零回归：未配置时
// 输出/输入上限为 0（不限制）、思考长度为 ""（继承）、思考开关为 true。
func TestGenerationParams_SanitizedDefaults(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	san := cfg.Sanitized()
	if v, ok := san["model.max_output_tokens"]; !ok || v != 0 {
		t.Errorf("Sanitized[max_output_tokens] = %v (ok=%v), 期望 0", v, ok)
	}
	if v, ok := san["model.max_input_tokens"]; !ok || v != 0 {
		t.Errorf("Sanitized[max_input_tokens] = %v (ok=%v), 期望 0", v, ok)
	}
	if v, ok := san["model.thinking_length"]; !ok || v != "" {
		t.Errorf("Sanitized[thinking_length] = %v (ok=%v), 期望空串", v, ok)
	}
	if v, ok := san["model.thinking_enabled"]; !ok || v != true {
		t.Errorf("Sanitized[thinking_enabled] = %v (ok=%v), 期望 true", v, ok)
	}
}

// TestGenerationParams_SanitizedNilThinkingEnabledTrue：
// ThinkingEnabled 为 nil（未显式配置，未走 applyDefaults）时
// Sanitized 输出 true，与 schema Default 一致。
func TestGenerationParams_SanitizedNilThinkingEnabledTrue(t *testing.T) {
	cfg := &Config{
		ActiveModelName: "m",
		Models:          []ModelPreset{{Name: "m", Backend: "deepseek"}},
	}
	if cfg.ActiveModel().ThinkingEnabled != nil {
		t.Fatal("前置条件：ThinkingEnabled 应为 nil")
	}
	if v := cfg.Sanitized()["model.thinking_enabled"]; v != true {
		t.Fatalf("nil ThinkingEnabled 时 Sanitized 应输出 true, got %v", v)
	}
}

// TestGenerationParams_ApplyWritesActivePreset 验证 4 个 key 的 ApplyPatch
// 写透激活预设（多预设时仅改激活项），且 Sanitized 立即反映新值。
func TestGenerationParams_ApplyWritesActivePreset(t *testing.T) {
	cfg := &Config{
		ActiveModelName: "second",
		Models: []ModelPreset{
			{Name: "first", Backend: "deepseek"},
			{Name: "second", Backend: "deepseek"},
		},
	}
	cfg.applyDefaults()
	if err := cfg.ApplyPatch(map[string]any{
		"model.max_output_tokens": 65536.0, // GUI 数值控件通常传 float64
		"model.max_input_tokens":  200000,  // 也支持 int
		"model.thinking_length":   "high",
		"model.thinking_enabled":  false,
	}); err != nil {
		t.Fatalf("ApplyPatch: %v", err)
	}
	am := cfg.ActiveModel()
	if am.Name != "second" {
		t.Fatalf("激活预设错误: %s", am.Name)
	}
	if am.MaxOutputTokens != 65536 {
		t.Errorf("MaxOutputTokens = %d, 期望 65536", am.MaxOutputTokens)
	}
	if am.MaxInputTokens != 200000 {
		t.Errorf("MaxInputTokens = %d, 期望 200000", am.MaxInputTokens)
	}
	if am.ThinkingLength != "high" {
		t.Errorf("ThinkingLength = %q, 期望 high", am.ThinkingLength)
	}
	if am.ThinkingEnabled == nil || *am.ThinkingEnabled {
		t.Errorf("ThinkingEnabled = %v, 期望 false", am.ThinkingEnabled)
	}
	// 非激活预设不受影响
	if cfg.Models[0].MaxOutputTokens != 0 || cfg.Models[0].ThinkingLength != "" {
		t.Fatalf("非激活预设被误改: %+v", cfg.Models[0])
	}
	// Sanitized 反映新值
	san := cfg.Sanitized()
	if san["model.max_output_tokens"] != 65536 || san["model.max_input_tokens"] != 200000 {
		t.Errorf("Sanitized 数值未同步: %v / %v", san["model.max_output_tokens"], san["model.max_input_tokens"])
	}
	if san["model.thinking_length"] != "high" || san["model.thinking_enabled"] != false {
		t.Errorf("Sanitized 思考配置未同步: %v / %v", san["model.thinking_length"], san["model.thinking_enabled"])
	}
}

// TestGenerationParams_ThinkingLengthInvalidRejected 非法 thinking_length
// 被 ApplyPatch 拒绝（错误信息含全部合法取值），合法档位逐一可写。
func TestGenerationParams_ThinkingLengthInvalidRejected(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	err := cfg.ApplyPatch(map[string]any{"model.thinking_length": "medium-high"})
	if err == nil {
		t.Fatal("非法 thinking_length 应被拒绝")
	}
	for _, want := range []string{"off", "low", "medium", "high", "xhigh", "max"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误信息缺少合法取值 %q: %v", want, err)
		}
	}
	if cfg.ActiveModel().ThinkingLength != "" {
		t.Fatalf("非法值不应写入: %q", cfg.ActiveModel().ThinkingLength)
	}
	// 非字符串类型同样拒绝
	if err := cfg.ApplyPatch(map[string]any{"model.thinking_length": 3}); err == nil {
		t.Fatal("非字符串 thinking_length 应被拒绝")
	}
	// 合法档位逐一可写（含空串=继承）
	for _, v := range []string{"off", "low", "medium", "high", "xhigh", "max", ""} {
		if err := cfg.ApplyPatch(map[string]any{"model.thinking_length": v}); err != nil {
			t.Fatalf("合法值 %q 应可写入: %v", v, err)
		}
		if cfg.ActiveModel().ThinkingLength != v {
			t.Fatalf("写入 %q 后读到 %q", v, cfg.ActiveModel().ThinkingLength)
		}
	}
}

// TestGenerationParams_LegacyMigrationCarriesNewFields 旧 model: 节迁移时
// 新字段一并携带（避免升级后丢失输出/输入/思考长度设置）。
func TestGenerationParams_LegacyMigrationCarriesNewFields(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "legacy.yaml")
	body := "model:\n" +
		"  backend: \"old-model\"\n" +
		"  api_key: \"sk-x\"\n" +
		"  max_context: 65536\n" +
		"  max_output_tokens: 32768\n" +
		"  max_input_tokens: 60000\n" +
		"  thinking_length: \"xhigh\"\n" +
		"  thinking_enabled: false\n"
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	am := cfg.ActiveModel()
	if am.MaxOutputTokens != 32768 || am.MaxInputTokens != 60000 {
		t.Errorf("迁移后 token 上限丢失: %+v", am)
	}
	if am.ThinkingLength != "xhigh" {
		t.Errorf("迁移后 thinking_length = %q, 期望 xhigh", am.ThinkingLength)
	}
	if am.ThinkingEnabled == nil || *am.ThinkingEnabled {
		t.Errorf("迁移后 thinking_enabled 应保持显式 false: %v", am.ThinkingEnabled)
	}
}

// TestGenerationParams_DefaultsZeroRegression 未配置新字段时默认值零回归：
// 输出/输入上限 0、思考长度 ""、思考开关 true；旧配置加载同样零回归。
func TestGenerationParams_DefaultsZeroRegression(t *testing.T) {
	cfg := &Config{}
	cfg.applyDefaults()
	am := cfg.ActiveModel()
	if am.MaxOutputTokens != 0 || am.MaxInputTokens != 0 {
		t.Fatalf("默认输出/输入上限应为 0（不限制）: %+v", am)
	}
	if am.ThinkingLength != "" {
		t.Fatalf("默认 thinking_length 应为空（继承 cli_defaults.effort）: %q", am.ThinkingLength)
	}
	if am.ThinkingEnabled == nil || !*am.ThinkingEnabled {
		t.Fatalf("默认 thinking_enabled 应为 true: %v", am.ThinkingEnabled)
	}
	// 旧配置（不含新字段）加载后同样零回归
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("model:\n  backend: x\n  endpoint: http://x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	am2 := cfg2.ActiveModel()
	if am2.MaxOutputTokens != 0 || am2.MaxInputTokens != 0 || am2.ThinkingLength != "" {
		t.Fatalf("旧配置加载应零回归: %+v", am2)
	}
	if am2.ThinkingEnabled == nil || !*am2.ThinkingEnabled {
		t.Fatalf("旧配置加载后 thinking_enabled 应缺省 true: %v", am2.ThinkingEnabled)
	}
}

// TestGenerationParams_SaveRoundTrip 新字段 YAML 保存/加载往返一致
// （覆盖 yaml tag 拼写）。
func TestGenerationParams_SaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	cfg := &Config{}
	cfg.applyDefaults()
	if err := cfg.ApplyPatch(map[string]any{
		"model.max_output_tokens": 65536,
		"model.max_input_tokens":  131072,
		"model.thinking_length":   "low",
		"model.thinking_enabled":  false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	re, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	am := re.ActiveModel()
	if am.MaxOutputTokens != 65536 || am.MaxInputTokens != 131072 || am.ThinkingLength != "low" {
		t.Fatalf("往返丢失生成参数: %+v", am)
	}
	if am.ThinkingEnabled == nil || *am.ThinkingEnabled {
		t.Fatalf("thinking_enabled=false 往返丢失: %v", am.ThinkingEnabled)
	}
}

// TestGenerationParams_Validate Validate 软校验：负值/非法档位/单值超窗口
// 4 倍报错；输出+输入之和超窗口不报错（运行时按实际用量钳制）。
func TestGenerationParams_Validate(t *testing.T) {
	newCfg := func(mut func(*ModelPreset)) *Config {
		cfg := &Config{}
		cfg.applyDefaults()
		cfg.Models[0].Endpoint = "https://api.example.com/v1"
		cfg.Models[0].APIKey = "sk-test"
		mut(&cfg.Models[0])
		return cfg
	}

	// 输出+输入之和 > MaxContext（各值均 < 4 倍）：允许，交给运行时钳制
	cfg := newCfg(func(m *ModelPreset) { m.MaxOutputTokens = 100000; m.MaxInputTokens = 100000 })
	if err := cfg.Validate(); err != nil {
		t.Fatalf("和超窗口应允许（运行时钳制），得到: %v", err)
	}
	// 负值报错
	if err := newCfg(func(m *ModelPreset) { m.MaxOutputTokens = -1 }).Validate(); err == nil {
		t.Fatal("负 max_output_tokens 应报错")
	}
	if err := newCfg(func(m *ModelPreset) { m.MaxInputTokens = -5 }).Validate(); err == nil {
		t.Fatal("负 max_input_tokens 应报错")
	}
	// 单值超窗口 4 倍报错（明显误配）
	if err := newCfg(func(m *ModelPreset) { m.MaxOutputTokens = 131072*4 + 1 }).Validate(); err == nil {
		t.Fatal("输出超 4 倍窗口应报错")
	}
	if err := newCfg(func(m *ModelPreset) { m.MaxInputTokens = 131072*4 + 1 }).Validate(); err == nil {
		t.Fatal("输入超 4 倍窗口应报错")
	}
	// 恰好 4 倍不报错（边界）
	if err := newCfg(func(m *ModelPreset) { m.MaxOutputTokens = 131072 * 4 }).Validate(); err != nil {
		t.Fatalf("恰好 4 倍窗口应允许: %v", err)
	}
	// 非法 thinking_length（绕过 Apply 直接构造，如手工改 yaml）报错
	if err := newCfg(func(m *ModelPreset) { m.ThinkingLength = "bogus" }).Validate(); err == nil {
		t.Fatal("非法 thinking_length 应报错")
	}
	// 合法档位全通过
	for _, v := range []string{"", "off", "low", "medium", "high", "xhigh", "max"} {
		if err := newCfg(func(m *ModelPreset) { m.ThinkingLength = v }).Validate(); err != nil {
			t.Fatalf("合法档位 %q 应通过: %v", v, err)
		}
	}
}
