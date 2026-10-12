package orchestrator

import (
	"strings"
	"testing"
)

// TestDeepProfile_AssemblesAllSections 验证深入本体档案能编译出完整的能力注入文本：
// 岗位/风格/专精/工作流/约束/经验/产出格式全部入档，且本体说明书追加在尾部。
func TestDeepProfile_AssemblesAllSections(t *testing.T) {
	e := Employee{
		Name:         "coder",
		Display:      "开发者小倔",
		Role:         "实现代码与修复缺陷",
		Persona:      "严谨，先看测试再动手",
		Expertise:    []string{"Go", "后端服务"},
		Workflow:     []string{"先读测试", "再动笔", "跑验证"},
		Constraints:  []string{"禁止未跑测试就声称完成"},
		Experience:   []string{"改动越界是返工主因"},
		OutputFormat: "变更清单 + 验证结果",
		Workspace:    "workspace",
	}
	doc := "参考代码风格：错误处理先于业务；提交粒度按主题拆分。"
	profile := e.DeepProfile(doc)

	for _, want := range []string{
		"【员工本体 · coder】（开发者小倔）",
		"岗位职责: 实现代码与修复缺陷",
		"工作风格: 严谨，先看测试再动手",
		"专精领域",
		"• Go",
		"• 后端服务",
		"标准工作流",
		"• 先读测试",
		"行为约束",
		"• 禁止未跑测试就声称完成",
		"经验沉淀",
		"• 改动越界是返工主因",
		"产出格式: 变更清单 + 验证结果",
		"【本体能力说明书（来自员工工作区蒸馏）】",
		"参考代码风格：错误处理先于业务；提交粒度按主题拆分。",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("DeepProfile 缺字段 %q\n完整输出:\n%s", want, profile)
		}
	}
}

// TestDeepProfile_EmptyOntology 验证无本体说明书时仅输出档案字段（不 panic、
// 不追加空说明书标题）。
func TestDeepProfile_EmptyOntology(t *testing.T) {
	e := Employee{Name: "tester", Role: "测试"}
	profile := e.DeepProfile("")
	if strings.Contains(profile, "本体能力说明书") {
		t.Errorf("无本体文档时不应输出说明书标题，got:\n%s", profile)
	}
	if !strings.Contains(profile, "岗位职责: 测试") {
		t.Errorf("应有岗位职责，got:\n%s", profile)
	}
}

// TestEffectiveOntology_Priority 验证内联 Ontology 与文件文本的合并优先级：
// 两者都有 → 内联在前、文件在后拼接；只有其一 → 用其一。
func TestEffectiveOntology_Priority(t *testing.T) {
	e := Employee{Ontology: "inline-capability"}
	if got := e.EffectiveOntology(""); got != "inline-capability" {
		t.Errorf("仅有内联时 should be inline, got %q", got)
	}
	if got := e.EffectiveOntology("file-text"); !strings.Contains(got, "inline-capability") || !strings.Contains(got, "file-text") {
		t.Errorf("两者都有应拼接, got %q", got)
	}
}

// TestEffectiveOntology_FileOnly 验证"仅文件文本"分支（P2-3 缺口补齐）：
// 内联 Ontology 为空、文件文本非空时，完整返回文件文本（不走内联合并）。
func TestEffectiveOntology_FileOnly(t *testing.T) {
	e := Employee{} // 无内联 Ontology
	if got := e.EffectiveOntology("file-only-text"); got != "file-only-text" {
		t.Errorf("仅文件文本时应原样返回文件文本，got %q", got)
	}
}

// TestOntologyPath_Resolution 验证本体路径解析：显式指定优先，否则默认
// workspace/ONTOLOGY.md；无 workspace 时回退默认约定目录。
func TestOntologyPath_Resolution(t *testing.T) {
	e := Employee{Workspace: "workspace"}
	if got := e.OntologyPath(); got != "workspace/ONTOLOGY.md" {
		t.Errorf("默认路径应为 workspace/ONTOLOGY.md, got %q", got)
	}
	e2 := Employee{Workspace: "ws", OntologyFile: "custom/cap.md"}
	if got := e2.OntologyPath(); got != "custom/cap.md" {
		t.Errorf("显式路径应优先, got %q", got)
	}
	e3 := Employee{}
	if got := e3.OntologyPath(); got != "workspace/ONTOLOGY.md" {
		t.Errorf("无工作区应回退默认约定目录, got %q", got)
	}
}

// TestExampleEmployees_HaveDeepOntology 验证内置 6 名员工都已具备深入本体字段
// （专精/工作流/约束/工作区）——种子数据不再只有几条提示词。
func TestExampleEmployees_HaveDeepOntology(t *testing.T) {
	emps := ExampleEmployees()
	if len(emps) < 6 {
		t.Fatalf("内置员工应 ≥6 名, got %d", len(emps))
	}
	for _, e := range emps {
		if len(e.Expertise) == 0 {
			t.Errorf("员工 %q 缺 expertise 专精领域", e.Name)
		}
		if len(e.Workflow) == 0 {
			t.Errorf("员工 %q 缺 workflow 工作流", e.Name)
		}
		if len(e.Constraints) == 0 {
			t.Errorf("员工 %q 缺 constraints 行为约束", e.Name)
		}
		if e.Workspace == "" {
			t.Errorf("员工 %q 缺 workspace 工作区", e.Name)
		}
		profile := e.DeepProfile("")
		if !strings.Contains(profile, "【员工本体 · "+e.Name+"】") {
			t.Errorf("员工 %q 本体档案未编译出头标", e.Name)
		}
	}
}

// TestMergeDeepDefaults_Migration 验证旧版纯净档案（缺深入本体字段）经
// MergeDeepDefaults 回退补齐内置模板字段、且用户自定义字段保留、已有 deep
// 字段不覆盖、无模板自定义员工原样返回——迁移路径完整。
func TestMergeDeepDefaults_Migration(t *testing.T) {
	// 旧版纯净档案：只有基础字段
	emp := &Employee{Name: "coder", Display: "开发者小倔", Role: "实现代码", Persona: "严谨", Approval: ApprovalStrict}
	merged := emp.MergeDeepDefaults()
	if len(merged.Expertise) == 0 {
		t.Error("应回退补齐 expertise")
	}
	if len(merged.Workflow) == 0 {
		t.Error("应回退补齐 workflow")
	}
	if len(merged.Constraints) == 0 {
		t.Error("应回退补齐 constraints")
	}
	if merged.Workspace != "workspace" {
		t.Errorf("应回退补齐 workspace, got %q", merged.Workspace)
	}
	// 用户自定义保留
	if merged.Name != "coder" || merged.Persona != "严谨" {
		t.Errorf("用户自定义字段应保留, got %+v", merged)
	}
	// 已有 deep 字段不覆盖
	emp2 := &Employee{Name: "coder", Expertise: []string{"自定义专精"}}
	merged2 := emp2.MergeDeepDefaults()
	if len(merged2.Expertise) != 1 || merged2.Expertise[0] != "自定义专精" {
		t.Errorf("已有 deep 字段不应被覆盖, got %+v", merged2.Expertise)
	}
	// 无模板自定义员工原样返回（不 panic 不注入；返回同一对象可接受——无变更）
	emp3 := &Employee{Name: "hacker", Role: "自定义"}
	merged3 := emp3.MergeDeepDefaults()
	if len(merged3.Expertise) != 0 {
		t.Errorf("无模板不应回退, got %+v", merged3.Expertise)
	}
	if merged3.Name != "hacker" {
		t.Errorf("无模板员工身份应保留, got %+v", merged3.Name)
	}
}
