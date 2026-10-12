package orchestrator

import (
	"reflect"
	"testing"
)

// ── ModelBinding 多来源搭配组合测试 ──

// TestModelBinding_Select：Select 槽位语义（0=主来源，1..n=备选来源，越界空）。
func TestModelBinding_Select(t *testing.T) {
	m := ModelBinding{Preset: "pro-model", Fallbacks: []string{"flash-model", "local-model"}}
	cases := map[int]string{
		0:  "pro-model", // 主来源
		1:  "flash-model",
		2:  "local-model",
		3:  "", // 越界
		-1: "", // 非法
	}
	for slot, want := range cases {
		if got := m.Select(slot); got != want {
			t.Errorf("Select(%d) = %q, want %q", slot, got, want)
		}
	}
	// 空主来源：Select(0) 返回空（由网关路由）
	empty := ModelBinding{Fallbacks: []string{"f1"}}
	if empty.Select(0) != "" {
		t.Error("空主来源 Select(0) 应返回空串")
	}
}

// TestModelBinding_Presets：Presets 去重保序、跳过空项。
func TestModelBinding_Presets(t *testing.T) {
	m := ModelBinding{Preset: "pro-model", Fallbacks: []string{"flash-model", "", "pro-model", "local-model"}}
	got := m.Presets()
	want := []string{"pro-model", "flash-model", "local-model"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Presets = %v, want %v（去重保序、去空）", got, want)
	}
	// 空绑定 → nil（调用方回退网关路由）
	if empty := (ModelBinding{}).Presets(); empty != nil {
		t.Fatalf("空绑定 Presets 应为 nil，got %v", empty)
	}
}

// TestModelBinding_Normalize：NormalizeBinding 去空白、备选去重且剔除与主重名项。
func TestModelBinding_Normalize(t *testing.T) {
	m := NormalizeBinding(ModelBinding{
		Preset:    " pro-model ",
		Fallbacks: []string{" flash-model ", "pro-model", "", "flash-model"},
	})
	if m.Preset != "pro-model" {
		t.Fatalf("主来源应去空白，got %q", m.Preset)
	}
	want := []string{"flash-model"}
	if !reflect.DeepEqual(m.Fallbacks, want) {
		t.Fatalf("备选应去空白/去重/剔主重名 = %v, want %v", m.Fallbacks, want)
	}
}

// TestEmployeePolicy_Fallbacks：ResolvePolicy 透传多来源降级链（M3+ 增强）。
func TestEmployeePolicy_Fallbacks(t *testing.T) {
	e := Employee{
		Name:  "x",
		Model: ModelBinding{Preset: "pro-model", Fallbacks: []string{"flash-model"}},
	}
	p, err := e.ResolvePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if p.ModelPreset != "pro-model" {
		t.Fatalf("preset = %q, want pro-model", p.ModelPreset)
	}
	if !reflect.DeepEqual(p.ModelFallbacks, []string{"flash-model"}) {
		t.Fatalf("fallbacks = %v, want [flash-model]", p.ModelFallbacks)
	}
	// 旧档案（仅 preset，无 fallbacks）向后兼容：fallbacks 为空
	old := Employee{Name: "y", Model: ModelBinding{Preset: "flash-model"}}
	po, err := old.ResolvePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if po.ModelPreset != "flash-model" || po.ModelFallbacks != nil {
		t.Fatalf("旧档案向后兼容: preset=%q fallbacks=%v", po.ModelPreset, po.ModelFallbacks)
	}
}

// TestEmployee_YAMLRoundTripFallbacks：YAML 往返保留 model.fallbacks 字段。
func TestEmployee_YAMLRoundTripFallbacks(t *testing.T) {
	src := Employee{
		Name:  "reviewer",
		Model: ModelBinding{Preset: "pro-model", Fallbacks: []string{"flash-model", "local-model"}},
	}
	data, err := src.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model.Preset != "pro-model" {
		t.Fatalf("往返后 preset = %q", got.Model.Preset)
	}
	if !reflect.DeepEqual(got.Model.Fallbacks, []string{"flash-model", "local-model"}) {
		t.Fatalf("往返后 fallbacks = %v", got.Model.Fallbacks)
	}
}

// TestExampleEmployees_MultiSource：内置 reviewer 演示多来源搭配（主+备）。
func TestExampleEmployees_MultiSource(t *testing.T) {
	for _, e := range ExampleEmployees() {
		if e.Name == "reviewer" {
			b := NormalizeBinding(e.Model)
			if b.Preset != "pro-model" {
				t.Fatalf("reviewer preset = %q, want pro-model", b.Preset)
			}
			if len(b.Fallbacks) == 0 {
				t.Fatal("reviewer 应演示多来源降级链（fallbacks 非空）")
			}
			return
		}
	}
	t.Fatal("内置员工中应有 reviewer")
}
