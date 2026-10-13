package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThinkingConfig_FiveWiring 多轮思考触发配置面（thinking.* 9 字段）五处接线契约：
// ① ConfigSchema 9 条（Group=思考策略，与"模型"分组区隔）
// ② Sanitized 9 键（空串归一 off/off/none/max；数值 0 原样输出）
// ③ applyOne 9 case（枚举校验 + 往返读写）
// ④ Validate 软校验（非法枚举/越界/交叉校验拒绝；policy=off 时其余字段不硬拒绝）
// ⑤ applyDefaults 不写值（零值=off/现状）；Load 空配置 → Sanitized policy 归一 "off"。
func TestThinkingConfig_FiveWiring(t *testing.T) {
	allKeys := []string{
		"thinking.policy", "thinking.min_think_rounds", "thinking.max_think_rounds",
		"thinking.verify_pass", "thinking.complexity_threshold", "thinking.nudge_enabled",
		"thinking.nudge_max", "thinking.round_effort_policy", "thinking.round_effort_cap",
	}

	t.Run("schema", func(t *testing.T) {
		byKey := map[string]FieldSchema{}
		for _, f := range ConfigSchema() {
			byKey[f.Key] = f
		}
		for _, k := range allKeys {
			f, ok := byKey[k]
			if !ok {
				t.Fatalf("schema 缺少 %s", k)
			}
			if f.Group != "思考策略" {
				t.Errorf("%s 分组 = %q, 期望 思考策略", k, f.Group)
			}
			if f.Type == "" || f.Label == "" {
				t.Errorf("%s 缺类型/标签: %+v", k, f)
			}
		}
		// 抽检：类型 / 默认值 / 枚举 / 数值范围（GUI 设置面板依赖此元数据）
		if f := byKey["thinking.policy"]; f.Type != "select" || f.Default != "off" || !eqStrs(f.Enums, thinkingPolicyOptions) {
			t.Errorf("thinking.policy schema 错误: %+v", f)
		}
		if f := byKey["thinking.min_think_rounds"]; f.Type != "number" || f.Default != 0 || f.Min == nil || *f.Min != 0 || f.Max == nil || *f.Max != 20 || f.Step != 1 {
			t.Errorf("thinking.min_think_rounds schema 错误: %+v", f)
		}
		if f := byKey["thinking.max_think_rounds"]; f.Type != "number" || f.Default != 3 || f.Min == nil || *f.Min != 1 || f.Max == nil || *f.Max != 20 || f.Step != 1 {
			t.Errorf("thinking.max_think_rounds schema 错误: %+v", f)
		}
		if f := byKey["thinking.verify_pass"]; f.Type != "select" || f.Default != "off" || !eqStrs(f.Enums, verifyPassOptions) {
			t.Errorf("thinking.verify_pass schema 错误: %+v", f)
		}
		if f := byKey["thinking.complexity_threshold"]; f.Type != "number" || f.Default != 60 || f.Min == nil || *f.Min != 0 || f.Max == nil || *f.Max != 100 || f.Step != 1 {
			t.Errorf("thinking.complexity_threshold schema 错误: %+v", f)
		}
		if f := byKey["thinking.nudge_enabled"]; f.Type != "bool" || f.Default != false {
			t.Errorf("thinking.nudge_enabled schema 错误: %+v", f)
		}
		if f := byKey["thinking.nudge_max"]; f.Type != "number" || f.Default != 2 || f.Min == nil || *f.Min != 0 || f.Max == nil || *f.Max != 10 || f.Step != 1 {
			t.Errorf("thinking.nudge_max schema 错误: %+v", f)
		}
		if f := byKey["thinking.round_effort_policy"]; f.Type != "select" || f.Default != "none" || !eqStrs(f.Enums, roundEffortPolicyOptions) {
			t.Errorf("thinking.round_effort_policy schema 错误: %+v", f)
		}
		if f := byKey["thinking.round_effort_cap"]; f.Type != "select" || f.Default != "max" || !eqStrs(f.Enums, roundEffortCapOptions) {
			t.Errorf("thinking.round_effort_cap schema 错误: %+v", f)
		}
	})

	t.Run("sanitized", func(t *testing.T) {
		// 零值（未经 applyDefaults 的裸 Config）也应输出 9 键且空串归一
		cfg := &Config{}
		san := cfg.Sanitized()
		for _, k := range allKeys {
			if _, ok := san[k]; !ok {
				t.Fatalf("Sanitized 缺少 %s", k)
			}
		}
		if san["thinking.policy"] != "off" || san["thinking.verify_pass"] != "off" ||
			san["thinking.round_effort_policy"] != "none" || san["thinking.round_effort_cap"] != "max" {
			t.Errorf("空串未归一: %v %v %v %v", san["thinking.policy"], san["thinking.verify_pass"], san["thinking.round_effort_policy"], san["thinking.round_effort_cap"])
		}
		if san["thinking.min_think_rounds"] != 0 || san["thinking.max_think_rounds"] != 0 ||
			san["thinking.complexity_threshold"] != 0 || san["thinking.nudge_max"] != 0 || san["thinking.nudge_enabled"] != false {
			t.Errorf("数值 0 应原样输出: %v", san)
		}
		// 显式值透出
		cfg.Thinking = ThinkingConfig{
			Policy: "strict", MinThinkRounds: 2, MaxThinkRounds: 5, VerifyPass: "auto",
			ComplexityThreshold: 75, NudgeEnabled: true, NudgeMax: 4,
			RoundEffortPolicy: "escalate", RoundEffortCap: "high",
		}
		san2 := cfg.Sanitized()
		if san2["thinking.policy"] != "strict" || san2["thinking.min_think_rounds"] != 2 ||
			san2["thinking.max_think_rounds"] != 5 || san2["thinking.verify_pass"] != "auto" ||
			san2["thinking.complexity_threshold"] != 75 || san2["thinking.nudge_enabled"] != true ||
			san2["thinking.nudge_max"] != 4 || san2["thinking.round_effort_policy"] != "escalate" ||
			san2["thinking.round_effort_cap"] != "high" {
			t.Errorf("显式值未透出: %v", san2)
		}
	})

	t.Run("applyOne_roundtrip", func(t *testing.T) {
		cfg := &Config{}
		cfg.applyDefaults()
		// GUI 数值控件通常传 float64；字符串/布尔照常
		patch := map[string]any{
			"thinking.policy":               "strict",
			"thinking.min_think_rounds":     2.0,
			"thinking.max_think_rounds":     5,
			"thinking.verify_pass":          "auto",
			"thinking.complexity_threshold": 75.0,
			"thinking.nudge_enabled":        true,
			"thinking.nudge_max":            4,
			"thinking.round_effort_policy":  "escalate",
			"thinking.round_effort_cap":     "high",
		}
		if err := cfg.ApplyPatch(patch); err != nil {
			t.Fatalf("ApplyPatch: %v", err)
		}
		want := ThinkingConfig{
			Policy: "strict", MinThinkRounds: 2, MaxThinkRounds: 5, VerifyPass: "auto",
			ComplexityThreshold: 75, NudgeEnabled: true, NudgeMax: 4,
			RoundEffortPolicy: "escalate", RoundEffortCap: "high",
		}
		if cfg.Thinking != want {
			t.Fatalf("往返不一致: %+v", cfg.Thinking)
		}
		// 设置 → Sanitized 读回
		san := cfg.Sanitized()
		if san["thinking.policy"] != "strict" || san["thinking.max_think_rounds"] != 5 ||
			san["thinking.verify_pass"] != "auto" || san["thinking.round_effort_cap"] != "high" {
			t.Errorf("Sanitized 读回失败: %v", san)
		}
		// 非法枚举逐一拒绝且不写入（错误信息含全部合法取值）
		for _, tc := range []struct{ key, val string }{
			{"thinking.policy", "bogus"},
			{"thinking.verify_pass", "bogus"},
			{"thinking.round_effort_policy", "bogus"},
			{"thinking.round_effort_cap", "bogus"},
		} {
			before := cfg.Thinking
			err := cfg.ApplyPatch(map[string]any{tc.key: tc.val})
			if err == nil {
				t.Fatalf("%s=%q 应被拒绝", tc.key, tc.val)
			}
			if !strings.Contains(err.Error(), "非法取值") {
				t.Errorf("%s 错误信息应含「非法取值」: %v", tc.key, err)
			}
			if cfg.Thinking != before {
				t.Fatalf("%s 非法值不应写入", tc.key)
			}
		}
		// 类型错误拒绝
		if err := cfg.ApplyPatch(map[string]any{"thinking.nudge_enabled": "yes"}); err == nil {
			t.Fatal("nudge_enabled 非布尔应被拒绝")
		}
		if err := cfg.ApplyPatch(map[string]any{"thinking.nudge_max": "many"}); err == nil {
			t.Fatal("nudge_max 非整数应被拒绝")
		}
		if err := cfg.ApplyPatch(map[string]any{"thinking.policy": 3}); err == nil {
			t.Fatal("policy 非字符串应被拒绝")
		}
		// 未知 thinking 键拒绝
		if err := cfg.ApplyPatch(map[string]any{"thinking.bogus": 1}); err == nil {
			t.Fatal("未知 thinking 键应被拒绝")
		}
	})

	t.Run("validate", func(t *testing.T) {
		newCfg := func(mut func(*ThinkingConfig)) *Config {
			cfg := &Config{}
			cfg.applyDefaults()
			cfg.Models[0].Endpoint = "https://api.example.com/v1"
			cfg.Models[0].APIKey = "sk-test"
			mut(&cfg.Thinking)
			return cfg
		}
		// 合法全量通过
		if err := newCfg(func(th *ThinkingConfig) {
			th.Policy = "strict"
			th.MinThinkRounds = 3
			th.MaxThinkRounds = 5
			th.VerifyPass = "on"
			th.ComplexityThreshold = 60
			th.NudgeEnabled = true
			th.NudgeMax = 2
			th.RoundEffortPolicy = "escalate"
			th.RoundEffortCap = "max"
		}).Validate(); err != nil {
			t.Fatalf("合法配置应通过: %v", err)
		}
		// 拒绝表：非法枚举（policy 始终校验）
		if err := newCfg(func(th *ThinkingConfig) { th.Policy = "bogus" }).Validate(); err == nil {
			t.Fatal("非法 policy 应报错")
		}
		// 拒绝表：非法枚举（其余枚举在 policy 生效时校验）
		for i, mut := range []func(*ThinkingConfig){
			func(th *ThinkingConfig) { th.Policy = "auto"; th.VerifyPass = "bogus" },
			func(th *ThinkingConfig) { th.Policy = "auto"; th.RoundEffortPolicy = "bogus" },
			func(th *ThinkingConfig) { th.Policy = "auto"; th.RoundEffortCap = "bogus" },
		} {
			if err := newCfg(mut).Validate(); err == nil {
				t.Fatalf("非法枚举 case#%d 应报错", i)
			}
		}
		// 拒绝表：越界
		for i, mut := range []func(*ThinkingConfig){
			func(th *ThinkingConfig) { th.Policy = "strict"; th.MinThinkRounds = -1 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.MinThinkRounds = 21 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.MaxThinkRounds = -1 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.MaxThinkRounds = 21 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.ComplexityThreshold = -1 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.ComplexityThreshold = 101 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.NudgeMax = -1 },
			func(th *ThinkingConfig) { th.Policy = "strict"; th.NudgeMax = 11 },
		} {
			if err := newCfg(mut).Validate(); err == nil {
				t.Fatalf("越界值 case#%d 应报错", i)
			}
		}
		// 拒绝表：交叉校验（min>max 且均>0）
		if err := newCfg(func(th *ThinkingConfig) { th.Policy = "strict"; th.MinThinkRounds = 5; th.MaxThinkRounds = 3 }).Validate(); err == nil {
			t.Fatal("min>max 应报错")
		}
		// 边界：min==max 允许；max=0（未设置）允许；min=0 允许
		if err := newCfg(func(th *ThinkingConfig) { th.Policy = "strict"; th.MinThinkRounds = 3; th.MaxThinkRounds = 3 }).Validate(); err != nil {
			t.Fatalf("min==max 应允许: %v", err)
		}
		if err := newCfg(func(th *ThinkingConfig) { th.Policy = "strict"; th.MinThinkRounds = 5; th.MaxThinkRounds = 0 }).Validate(); err != nil {
			t.Fatalf("max=0（未设置）应允许: %v", err)
		}
		// 边界：上限值全通过
		if err := newCfg(func(th *ThinkingConfig) {
			th.Policy = "strict"
			th.MinThinkRounds = 20
			th.MaxThinkRounds = 20
			th.ComplexityThreshold = 100
			th.NudgeMax = 10
		}).Validate(); err != nil {
			t.Fatalf("边界上限应通过: %v", err)
		}
		// policy=off 时其余字段不做硬拒绝（软裁决：运行时 policy 优先，GUI 提示"不生效"）
		if err := newCfg(func(th *ThinkingConfig) {
			th.Policy = "off"
			th.MinThinkRounds = 5
			th.MaxThinkRounds = 3
			th.ComplexityThreshold = 999
			th.NudgeEnabled = true
			th.NudgeMax = 11
			th.VerifyPass = "on"
			th.RoundEffortPolicy = "escalate"
			th.RoundEffortCap = "high"
		}).Validate(); err != nil {
			t.Fatalf("policy=off 时其余字段不应硬拒绝: %v", err)
		}
		// policy 空值（=off 归一）同样软裁决
		if err := newCfg(func(th *ThinkingConfig) {
			th.MinThinkRounds = -1
			th.ComplexityThreshold = 101
		}).Validate(); err != nil {
			t.Fatalf("policy 空值（=off）时其余字段不应硬拒绝: %v", err)
		}
		// 零值整体零回归
		if err := newCfg(func(th *ThinkingConfig) {}).Validate(); err != nil {
			t.Fatalf("thinking 零值应通过: %v", err)
		}
	})

	t.Run("defaults_no_write", func(t *testing.T) {
		cfg := &Config{}
		cfg.applyDefaults()
		if cfg.Thinking != (ThinkingConfig{}) {
			t.Fatalf("applyDefaults 不应写 thinking 值: %+v", cfg.Thinking)
		}
		// 旧配置（无 thinking 节）加载零回归
		dir := t.TempDir()
		p := filepath.Join(dir, "old.yaml")
		if err := os.WriteFile(p, []byte("model:\n  backend: old\n  endpoint: http://x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		old, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		if old.Thinking != (ThinkingConfig{}) {
			t.Fatalf("旧配置加载不应带 thinking 值: %+v", old.Thinking)
		}
	})

	t.Run("load_empty_norm", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "empty.yaml")
		if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(p)
		if err != nil {
			t.Fatal(err)
		}
		san := cfg.Sanitized()
		if san["thinking.policy"] != "off" {
			t.Fatalf("空配置 policy 应归一为 off: %v", san["thinking.policy"])
		}
		for _, k := range allKeys {
			if _, ok := san[k]; !ok {
				t.Fatalf("空配置 Sanitized 缺少 %s", k)
			}
		}
		// 示例配置：thinking 段可加载且默认 off（schema 默认档 3/60/2 落盘可见）
		ex, err := Load("../../config.example.yaml")
		if err != nil {
			t.Fatalf("Load example: %v", err)
		}
		if ex.Sanitized()["thinking.policy"] != "off" {
			t.Fatalf("example policy 应为 off: %v", ex.Sanitized()["thinking.policy"])
		}
		if ex.Thinking.MaxThinkRounds != 3 || ex.Thinking.ComplexityThreshold != 60 || ex.Thinking.NudgeMax != 2 {
			t.Fatalf("example 未按 §0.3 默认档示例: %+v", ex.Thinking)
		}
	})
}

// eqStrs 比较两个字符串切片是否逐项相等。
func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
