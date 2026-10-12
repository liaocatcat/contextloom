package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/scenes"
	"github.com/contextloom/contextloom-agent/internal/selfcheck"
)

// =============================================================================
// M3 员工中心接线测试（零配置兜底 + 员工命中装配）
// =============================================================================

// TestEmployeeByName_ZeroConfig 无 .contextloom/employees 目录 → 返回 nil（旧行为不变）。
// 注：runtime.New 会落地内置种子（seed.go），故先移除种子目录再验证零配置兜底。
func TestEmployeeByName_ZeroConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.RemoveAll(filepath.Join(tmpDir, ".contextloom", "employees")); err != nil {
		t.Fatal(err)
	}
	if emp := rt.employeeByName("coder"); emp != nil {
		t.Fatalf("零配置下 employeeByName 应返回 nil，got %+v", emp)
	}
}

// TestEmployeeByName_HitsProfile 目录存在 + 档案合法 → 命中返回员工。
func TestEmployeeByName_HitsProfile(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	// 清掉内置种子，只保留本测试手写的档案（验证自定义档案优先于种子）
	if err := os.RemoveAll(filepath.Join(tmpDir, ".contextloom", "employees")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmpDir, ".contextloom", "employees", "coder", "employee.yaml"), `
name: coder
display: 开发者
role: 实现代码
persona: 严谨
model:
  preset: flash-model
approval: standard
`)
	emp := rt.employeeByName("coder")
	if emp == nil || emp.Name != "coder" || emp.Role != "实现代码" {
		t.Fatalf("employeeByName 未命中合法档案: %+v", emp)
	}
	// 未命中名 → nil
	if rt.employeeByName("writer") != nil {
		t.Fatal("尚未定义的员工名不应命中")
	}
}

// TestEmployeeTools_FilterByAllowlist 员工工具表按 allowlist 过滤 + 防递归恒排除。
func TestEmployeeTools_FilterByAllowlist(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	writeFile(t, filepath.Join(tmpDir, ".contextloom", "employees", "writer", "employee.yaml"), `
name: writer
model: {}
tool_allowlist: ["read_file", "write_file"]
`)
	emp := rt.employeeByName("writer")
	if emp == nil {
		t.Fatal("员工档案未加载")
	}
	reg := rt.employeeTools(emp)
	names := map[string]bool{}
	for _, d := range reg.ToolDefinitions() {
		names[d.Function.Name] = true
	}
	if !names["read_file"] || !names["write_file"] {
		t.Fatalf("allowlist 工具未被保留: %v", names)
	}
	if names["exec_command"] || names["spawn_task"] || names["handoff"] {
		t.Fatalf("allowlist 外工具未被过滤 / 防递归工具未被排除: %v", names)
	}
}

// TestEmployeeTools_EmptyAllowlist 空 allowlist → 原注册表（不限制）。
func TestEmployeeTools_EmptyAllowlist(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	writeFile(t, filepath.Join(tmpDir, ".contextloom", "employees", "free", "employee.yaml"), `
name: free
model: {}
`)
	emp := rt.employeeByName("free")
	if emp == nil {
		t.Fatal("员工档案未加载")
	}
	if rt.employeeTools(emp) != rt.Tools {
		t.Fatal("空 allowlist 应返回原注册表（不限制）")
	}
}

// =============================================================================
// M4 场景中心接线测试（零配置兜底 + 规则命中）
// =============================================================================

// TestMatchScene_ZeroConfig 无 .contextloom/scenes 目录 → nil（退回裸 chat）。
// 注：runtime.New 会落地内置场景种子，故先移除种子目录再验证零配置兜底。
func TestMatchScene_ZeroConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.RemoveAll(filepath.Join(tmpDir, ".contextloom", "scenes")); err != nil {
		t.Fatal(err)
	}
	if sc := rt.matchScene("帮我写周报"); sc != nil {
		t.Fatalf("零配置下 matchScene 应返回 nil，got %+v", sc)
	}
}

// TestMatchScene_KeywordHit 场景目录存在 + 关键词命中 → 返回场景。
func TestMatchScene_KeywordHit(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	// 清掉内置场景种子，避免内置 weekly-report 干扰命中断言
	if err := os.RemoveAll(filepath.Join(tmpDir, ".contextloom", "scenes")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(tmpDir, ".contextloom", "scenes", "weekly", "scene.yaml"), `
name: weekly
display: 周报
keywords: ["周报", "周总结"]
`)
	if sc := rt.matchScene("帮我写周报"); sc == nil || sc.Name != "weekly" {
		t.Fatalf("关键词应命中 weekly 场景: %+v", sc)
	}
	if sc := rt.matchScene("来点别的"); sc != nil {
		t.Fatal("未命中应返回 nil")
	}
}

// TestScenePrompt_Assembly 场景提示词拼装完整（触发原话+验收+链产物）。
func TestScenePrompt_Assembly(t *testing.T) {
	sc := scenes.Scene{Name: "weekly", Display: "周报", Verify: "含本周要点"}
	got := scenePrompt(sc, "写周报", "链产物:完成")
	for _, want := range []string{"周报", "写周报", "含本周要点", "链产物:完成"} {
		if !strings.Contains(got, want) {
			t.Fatalf("scenePrompt 缺少 %q: %s", want, got)
		}
	}
}

// =============================================================================
// M5 质量门禁接线测试（零配置兜底 + 裁决 + 收口辅助）
// =============================================================================

// TestQualityGate_ZeroConfig 无 .contextloom/gates 目录 → QualityNone（恒放行）。
func TestQualityGate_ZeroConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if lv := rt.qualityGate(); lv != selfcheck.QualityNone {
		t.Fatalf("零配置门禁应 None，got %v", lv)
	}
	rep, dec := rt.goalGateReport(true, 95)
	if dec != selfcheck.DecisionPass {
		t.Fatalf("None 档应恒 Pass，got %v", dec)
	}
	_ = rep
}

// TestQualityGate_Activated 有 gates 目录 → Standard 档激活。
func TestQualityGate_Activated(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.MkdirAll(filepath.Join(tmpDir, ".contextloom", "gates"), 0755); err != nil {
		t.Fatal(err)
	}
	if lv := rt.qualityGate(); lv != selfcheck.QualityStandard {
		t.Fatalf("gates 目录存在应 Standard，got %v", lv)
	}
	if ps := rt.gatePassScore(); ps != 90 {
		t.Fatalf("无 gate.yaml 阈值应 90，got %d", ps)
	}
}

// TestQualityGate_GateYamlLevels gate.yaml 三档开关接线：level 生效（GUI 面板承诺）。
func TestQualityGate_GateYamlLevels(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	gatesDir := filepath.Join(tmpDir, ".contextloom", "gates")
	if err := os.MkdirAll(gatesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gatesDir, "gate.yaml"), "level: fast\npass_score: 60\n")
	if lv := rt.qualityGate(); lv != selfcheck.QualityFast {
		t.Fatalf("gate.yaml fast 档应生效，got %v", lv)
	}
	if ps := rt.gatePassScore(); ps != 60 {
		t.Fatalf("gate.yaml 阈值 60 应生效，got %d", ps)
	}
	// none 档：目录存在但门禁关闭（与"目录存在即激活"并存，none 可显式关）。
	// 用 Chtimes 强制 mtime 变化 → 同进程内重载（GUI 保存后下一轮 goal 生效语义）。
	p := filepath.Join(gatesDir, "gate.yaml")
	writeFile(t, p, "level: none\n")
	now := time.Now()
	_ = os.Chtimes(p, now, now.Add(2*time.Second))
	if lv := rt.qualityGate(); lv != selfcheck.QualityNone {
		t.Fatalf("gate.yaml none 档应关闭门禁，got %v", lv)
	}
	rep, dec := rt.goalGateReport(true, 95)
	if dec != selfcheck.DecisionPass {
		t.Fatalf("none 档应恒 Pass，got %v", dec)
	}
	_ = rep
}

// TestQualityGate_GateYamlBad 坏 gate.yaml → 回退默认（standard/90），不阻塞。
func TestQualityGate_GateYamlBad(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	gatesDir := filepath.Join(tmpDir, ".contextloom", "gates")
	if err := os.MkdirAll(gatesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gatesDir, "gate.yaml"), "level: [not-a-string]\n")
	if lv := rt.qualityGate(); lv != selfcheck.QualityStandard {
		t.Fatalf("坏 gate.yaml 应回退 Standard，got %v", lv)
	}
	if ps := rt.gatePassScore(); ps != 90 {
		t.Fatalf("坏 gate.yaml 应回退阈值 90，got %d", ps)
	}
}

// TestGatePassScore_Bounds 非法阈值回退默认。
func TestGatePassScore_Bounds(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	gatesDir := filepath.Join(tmpDir, ".contextloom", "gates")
	if err := os.MkdirAll(gatesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gatesDir, "gate.yaml"), "level: standard\npass_score: 999\n")
	if ps := rt.gatePassScore(); ps != 90 {
		t.Fatalf("越界阈值应回退 90，got %d", ps)
	}
}

// TestGoalGateReport_FastSkipsTEE fast 档关 TEE：.go 改动也只做静检（rep.TEE 为 nil）。
func TestGoalGateReport_FastSkipsTEE(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	gatesDir := filepath.Join(tmpDir, ".contextloom", "gates")
	if err := os.MkdirAll(gatesDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(gatesDir, "gate.yaml"), "level: fast\n")
	// 造一个含 .go 改动的工作区（非 git 仓库 → changedFiles 返回 nil → 静检通过路径）
	writeFile(t, filepath.Join(tmpDir, "a.go"), "package main\n\nfunc main() {}\n")
	rep, dec := rt.goalGateReport(true, 95)
	if dec != selfcheck.DecisionPass {
		t.Fatalf("fast 档无改动应 Pass，got %v", dec)
	}
	_ = rep
	// fast 档语义核验：TEEEnabled=false（不做 go test，防慢路径）
	if selfcheck.QualityFast.TEEEnabled() {
		t.Fatal("fast 档 TEE 应关闭")
	}
}

// TestStripGoalDone 剥离/保留语义。
func TestStripGoalDone(t *testing.T) {
	if got := stripGoalDone("完成 [GOAL_DONE]"); strings.Contains(got, "[GOAL_DONE]") {
		t.Fatal("应剥离 [GOAL_DONE]")
	}
	if got := stripGoalDone("[GOAL_POLISH_DONE]"); !strings.Contains(got, "[GOAL_POLISH_DONE]") {
		t.Fatal("不应剥离 [GOAL_POLISH_DONE]")
	}
	if got := stripGoalDone("无标记"); got != "无标记" {
		t.Fatal("无标记应原样返回")
	}
}

// TestSelfcheckGateDefects_IDPrefix 缺陷条目安全 ID 前缀（与监工缺陷并存）。
func TestSelfcheckGateDefects_IDPrefix(t *testing.T) {
	rep := selfcheck.QualityReport{
		Static: selfcheck.StaticReport{
			Pass: false,
			Issues: []selfcheck.Issue{
				{File: "a.go", Line: 1, Kind: "residue", Hint: "TODO", Severity: "high"},
			},
		},
		PassScore: 90,
	}
	defects := selfcheckGateDefects(rep)
	if len(defects) == 0 {
		t.Fatal("high 缺陷应被提取")
	}
	for _, d := range defects {
		if !strings.HasPrefix(d.ID, "d-gate-") {
			t.Fatalf("缺陷 ID 应带 d-gate- 前缀: %s", d.ID)
		}
		if d.Status != "open" {
			t.Fatalf("缺陷状态应为 open: %s", d.Status)
		}
	}
}

// =============================================================================
// M2 工具链接线测试（零配置兜底 + 链产物提取）
// =============================================================================

// TestRunToolchain_ZeroConfig 无 chains 目录 → 可读错误（不静默）。
func TestRunToolchain_ZeroConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	_, err := rt.RunToolchain(context.Background(), "no-such-chain", nil)
	if err == nil || !strings.Contains(err.Error(), "不存在或损坏") {
		t.Fatalf("应返回可读错误，got %v", err)
	}
}

// TestIsTextFile_BinaryFilter 二进制后缀拒绝、文本通过。
func TestIsTextFile_BinaryFilter(t *testing.T) {
	for _, f := range []string{"a.png", "b.zip", "app.exe", "lib.so"} {
		if isTextFile(f) {
			t.Fatalf("%s 应判为二进制", f)
		}
	}
	for _, f := range []string{"a.go", "b.md", "c.yaml", "d.txt"} {
		if !isTextFile(f) {
			t.Fatalf("%s 应判为文本", f)
		}
	}
}

// TestCountTestLines 统计 ok/FAIL 行数。
func TestCountTestLines(t *testing.T) {
	in := []byte("ok  pkg/a  0.1s\nok  pkg/b  0.2s\nFAIL pkg/c 0.1s\n--- FAIL\n")
	if n := countTestLines(in); n != 3 {
		t.Fatalf("countTestLines = %d, want 3", n)
	}
}
