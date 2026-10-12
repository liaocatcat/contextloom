package executor

import (
	"context"
	"strings"
	"testing"
)

// TestHandoffRegistry_RegisterGetNames：注册表增删查与名称列表。
func TestHandoffRegistry_RegisterGetNames(t *testing.T) {
	reg := NewHandoffRegistry()
	reg.Register(HandoffTarget{Name: "planner", Desc: "规划", Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
		return "plan:" + reason, nil
	}})
	reg.Register(HandoffTarget{Name: "coder", Desc: "实现", Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
		return "code:" + reason, nil
	}})

	names := reg.Names()
	if len(names) != 2 || names[0] != "coder" || names[1] != "planner" {
		t.Errorf("Names 异常: %v", names)
	}
	if _, ok := reg.Get("coder"); !ok {
		t.Error("Get(coder) 应命中")
	}
	if _, ok := reg.Get("nope"); ok {
		t.Error("Get(nope) 不应命中")
	}
	if d := reg.Descriptions(); !strings.Contains(d, "planner") || !strings.Contains(d, "coder") {
		t.Errorf("Descriptions 不完整: %s", d)
	}
}

// TestHandoffTool_ExecuteAndErrors：工具函数执行、缺目标报错、未知目标报错。
func TestHandoffTool_ExecuteAndErrors(t *testing.T) {
	reg := NewHandoffRegistry()
	reg.Register(HandoffTarget{Name: "verifier", Desc: "验证", Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
		return "verified:" + reason, nil
	}})
	fn := MakeHandoffTool(reg)

	// 正常交接
	out, err := fn(context.Background(), map[string]any{"to": "verifier", "reason": "run tests"})
	if err != nil {
		t.Fatalf("交接执行失败: %v", err)
	}
	m := out.(map[string]any)
	if m["handoff_to"] != "verifier" || m["result"] != "verified:run tests" {
		t.Errorf("交接结果异常: %+v", m)
	}

	// 缺 to
	if _, err := fn(context.Background(), map[string]any{}); err == nil {
		t.Error("缺 to 应报错")
	}
	// 未知目标
	if _, err := fn(context.Background(), map[string]any{"to": "ghost"}); err == nil {
		t.Error("未知目标应报错")
	}
}

// TestHandoffTool_DepthLimit：交接深度超过上限（A→B→A→B 循环）被终止。
func TestHandoffTool_DepthLimit(t *testing.T) {
	reg := NewHandoffRegistry()
	var fn = MakeHandoffTool(reg)
	reg.Register(HandoffTarget{Name: "a", Desc: "A", Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
		// A 把任务交回 A（模拟循环）
		out, err := fn(ctx, map[string]any{"to": "a", "reason": "loop"})
		if err != nil {
			return "", err
		}
		return "a->" + out.(map[string]any)["result"].(string), nil
	}})

	_, err := fn(context.Background(), map[string]any{"to": "a", "reason": "start"})
	if err == nil {
		t.Fatal("循环交接应被深度上限终止")
	}
	if !strings.Contains(err.Error(), "深度超过上限") {
		t.Errorf("错误信息异常: %v", err)
	}
}

// TestHandoffToolSchema_Required：schema 契约（to 必填、三参数齐全）。
func TestHandoffToolSchema_Required(t *testing.T) {
	req, ok := HandoffToolSchema["required"].([]string)
	if !ok || len(req) != 1 || req[0] != "to" {
		t.Errorf("schema required 异常: %v", HandoffToolSchema["required"])
	}
	props, ok := HandoffToolSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema properties 缺失")
	}
	for _, k := range []string{"to", "reason", "payload"} {
		if _, ok := props[k]; !ok {
			t.Errorf("schema 缺参数 %s", k)
		}
	}
}
