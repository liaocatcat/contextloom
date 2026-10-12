package goalmgr

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── [GOAL_SPEC] 解析 ──

func TestExtractGoalSpec_Valid(t *testing.T) {
	resp := `分析完成 [GOAL_SPEC]
{"goal_refined":"在 internal/runtime/runtime.go 的 ProcessInput 中新增 spec 阶段分支",
 "assumptions":["goal 模式才会触发","需要新增 GoalPhaseSpec 常量"],
 "acceptance":["go build ./... 通过","go test ./internal/runtime 全部通过"],
 "scope":["只改 runtime.go 与 types","不碰 GUI"],
 "risks":["既有 goal 测试可能受影响，需回归"],
 "open_questions":["用户是否需要可配置开关——假设不需要，后续按需加"]}
[/GOAL_SPEC] 下面开始规划`
	spec := ExtractGoalSpec(resp)
	if spec == nil {
		t.Fatal("应解析出 spec")
	}
	if !strings.Contains(spec.GoalRefined, "ProcessInput") {
		t.Fatalf("GoalRefined 异常: %q", spec.GoalRefined)
	}
	if len(spec.Assumptions) != 2 || len(spec.Acceptance) != 2 {
		t.Fatalf("assumptions=%d acceptance=%d", len(spec.Assumptions), len(spec.Acceptance))
	}
}

func TestExtractGoalSpec_Unclosed(t *testing.T) {
	resp := `[GOAL_SPEC]{"goal_refined":"改 internal/config/config.go","assumptions":["配置键已存在"],"acceptance":["go build 通过"]}`
	spec := ExtractGoalSpec(resp)
	if spec == nil || spec.GoalRefined == "" {
		t.Fatal("未闭合标记也应宽容解析")
	}
}

func TestExtractGoalSpec_Missing(t *testing.T) {
	if spec := ExtractGoalSpec("没有 spec 标记的响应"); spec != nil {
		t.Fatal("无标记应返回 nil")
	}
	if spec := ExtractGoalSpec("[GOAL_SPEC] 非法 JSON [/GOAL_SPEC]"); spec != nil {
		t.Fatal("非法 JSON 应返回 nil")
	}
}

// ── SpecGate 门槛 ──

func TestSpecGate_Pass(t *testing.T) {
	spec := &types.GoalSpec{
		GoalRefined: "在 internal/runtime/runtime.go 的 ProcessInput 中新增 spec 阶段分支",
		Assumptions: []string{"goal 模式才会触发"},
		Acceptance:  []string{"go test ./internal/runtime 全部通过（0 失败）"},
	}
	ok, issues := (SpecGate{}).Check(spec)
	if !ok || len(issues) != 0 {
		t.Fatalf("应通过，issues=%v", issues)
	}
}

func TestSpecGate_RejectAllRules(t *testing.T) {
	cases := []struct {
		name string
		spec *types.GoalSpec
		want string // 期望命中的规则码
	}{
		{"nil", nil, "R1"},
		{"空细化", &types.GoalSpec{}, "R1"},
		{"无目标物", &types.GoalSpec{
			GoalRefined: "把登录做得更好",
			Assumptions: []string{"大概是指现有登录"},
			Acceptance:  []string{"用户体验提升"},
		}, "R2"},
		{"无假设", &types.GoalSpec{
			GoalRefined: "修改 internal/config/config.go 的模型名",
			Acceptance:  []string{"go build 通过"},
		}, "R3"},
		{"验收不可测", &types.GoalSpec{
			GoalRefined: "修改 internal/config/config.go",
			Assumptions: []string{"配置键存在"},
			Acceptance:  []string{"改好了"},
		}, "R4"},
	}
	for _, c := range cases {
		ok, issues := (SpecGate{}).Check(c.spec)
		if ok {
			t.Fatalf("%s: 应不通过", c.name)
		}
		found := false
		for _, it := range issues {
			if it.Code == c.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: 应命中规则 %s，got %v", c.name, c.want, issues)
		}
	}
}

// ── 模糊度检测 ──

func TestDetectFuzziness(t *testing.T) {
	if f := DetectFuzziness("加个登录页"); !f.Fuzzy {
		t.Fatalf("「加个登录页」应判定模糊，hits=%v", f.Hits)
	}
	if f := DetectFuzziness("在 internal/runtime/runtime.go 的 ProcessInput 新增 spec 分支，验收：go test ./internal/runtime 0 失败"); f.Fuzzy {
		t.Fatalf("含动词+目标物+验收的输入不应模糊，hits=%v", f.Hits)
	}
}

// ── SetSpec 状态机 ──

func TestSetSpec_PassThenPlan(t *testing.T) {
	m := New(t.TempDir())
	m.Start("加个登录页", 5, 0)
	spec := &types.GoalSpec{
		GoalRefined: "实现 internal/web/login.go 登录页：表单 + 校验 + 登录接口对接",
		Assumptions: []string{"沿用现有 gin 路由", "登录接口 POST /login 已存在"},
		Acceptance:  []string{"go test ./internal/web 通过", "登录页表单字段 3 个"},
	}
	ok, reasons := m.SetSpec(spec)
	if !ok || len(reasons) != 0 {
		t.Fatalf("应通过，reasons=%v", reasons)
	}
	gs := m.Session()
	if !gs.SpecReady || gs.Spec == nil {
		t.Fatalf("SpecReady 应为 true，got %+v", gs)
	}
	if gs.ActivePhase() != types.GoalPhasePlan {
		t.Fatalf("spec 通过后应进入 plan，got %s", gs.ActivePhase())
	}
}

func TestSetSpec_RejectAddsDefect(t *testing.T) {
	m := New(t.TempDir())
	m.Start("改一下", 5, 0)
	spec := &types.GoalSpec{
		GoalRefined: "改一下", // 无目标物、无假设、验收不可测 → 全拒
		Acceptance:  []string{"改好"},
	}
	ok, reasons := m.SetSpec(spec)
	if ok {
		t.Fatal("应拒绝")
	}
	if len(reasons) == 0 {
		t.Fatal("应有拒绝原因")
	}
	gs := m.Session()
	if gs.SpecReady {
		t.Fatal("SpecReady 应为 false")
	}
	if gs.ActivePhase() != types.GoalPhaseSpec {
		t.Fatalf("拒绝后应停留在 spec 阶段，got %s", gs.ActivePhase())
	}
	if len(gs.OpenDefects()) != 1 || gs.OpenDefects()[0].ID != "d-spec" {
		t.Fatalf("应有 d-spec 缺陷，got %+v", gs.OpenDefects())
	}
	// 第二次细化仍不过 → 缺陷不重复追加
	m.SetSpec(spec)
	if len(gs.OpenDefects()) != 1 {
		t.Fatalf("d-spec 缺陷应去重合并，got %d", len(gs.OpenDefects()))
	}
}

func TestSetSpec_NilCompatible(t *testing.T) {
	m := New(t.TempDir())
	m.Start("目标", 5, 0)
	ok, reasons := m.SetSpec(nil) // 未输出 spec → 兼容通过
	if !ok || len(reasons) != 0 {
		t.Fatalf("nil spec 应兼容通过，reasons=%v", reasons)
	}
	gs := m.Session()
	if gs.ActivePhase() != types.GoalPhasePlan {
		t.Fatalf("nil spec 后应保持旧路径 plan，got %s", gs.ActivePhase())
	}
}
