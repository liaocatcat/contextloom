package selfcheck

import (
	"testing"
)

// ① 静态核查：残留/未决/未用符号三类检出 + Pass 判定
func TestStaticChecker_ResidueUnresolvedUnused(t *testing.T) {
	sc := NewStaticChecker(StaticOptions{
		KnownSymbols:    map[string]bool{"UsedHelper": true},
		DeclaredSymbols: map[string]bool{"NewHelper": true, "UsedHelper": true},
	})
	files := []FileChange{
		{Path: "a.go", Content: "func UsedHelper() {}\nfunc NewHelper() {}\n// TODO 后续优化\nvar x = 1\n"},
		{Path: "b.go", Content: "这个设计[?]还不确定\n"},
		{Path: "c.go", Content: "func Clean() {}\n"},
	}
	rep := sc.Check(files)
	if rep.Pass {
		t.Fatal("有 high(未决)/medium(残留未用) 问题应不通过")
	}
	kinds := map[string]bool{}
	for _, it := range rep.Issues {
		kinds[it.Kind] = true
	}
	if !kinds["residue"] {
		t.Error("应检出残留标记 TODO")
	}
	if !kinds["unresolved"] {
		t.Error("应检出未决标记 [?]")
	}
	if !kinds["unused"] {
		t.Error("应检出未用符号 NewHelper")
	}
	// 行号核对
	for _, it := range rep.Issues {
		if it.Kind == "unresolved" && it.Line != 1 {
			t.Errorf("未决标记应定位第 1 行，got %d", it.Line)
		}
		if it.Kind == "residue" && it.Line != 3 {
			t.Errorf("TODO 应定位第 3 行，got %d", it.Line)
		}
	}
}

func TestStaticChecker_CleanPasses(t *testing.T) {
	sc := NewStaticChecker(StaticOptions{})
	rep := sc.Check([]FileChange{{Path: "a.go", Content: "func Clean() {}\n"}})
	if !rep.Pass {
		t.Fatalf("干净文件应通过，issues=%+v", rep.Issues)
	}
}

func TestStaticChecker_ScanToggle(t *testing.T) {
	off := false
	sc := NewStaticChecker(StaticOptions{ScanResidue: &off, ScanUnresolved: &off})
	rep := sc.Check([]FileChange{{Path: "a.go", Content: "// TODO x\nvar [?]\n"}})
	if len(rep.Issues) != 0 {
		t.Fatalf("关闭扫描应无 issues，got %d", len(rep.Issues))
	}
}

// ② 门禁：Pass / Fail 禁写 / 连续 3 轮不收敛 → HumanReview
func TestGate_PassFailForbid(t *testing.T) {
	g := NewGate(QualityStandard, 90)
	// 通过
	d := g.Admit(QualityReport{
		Static: StaticReport{Pass: true},
		TEE:    &TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: true, TestCount: 2},
		Score:  95,
	})
	if d != DecisionPass {
		t.Fatalf("应 pass，got %s", d)
	}
	// 静态核查不过 → Fail + 禁写
	d = g.Admit(QualityReport{
		Static: StaticReport{Pass: false, Issues: []Issue{{Kind: "unresolved", Severity: "high", File: "a.md", Hint: "[?]"}}},
		TEE:    &TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: true},
		Score:  95,
	})
	if d != DecisionFail {
		t.Fatalf("静态不过应 Fail，got %s", d)
	}
	if !d.ForbidDone() {
		t.Fatal("Fail 应禁写 [GOAL_DONE]")
	}
	// 收敛一轮重置计数，隔离后续用例
	g.TrackRound(true)

	// TEE 不通过 → Fail
	d = g.Admit(QualityReport{
		Static: StaticReport{Pass: true},
		TEE:    &TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: false},
		Score:  95,
	})
	if d != DecisionFail {
		t.Fatalf("TEE 不过应 Fail，got %s", d)
	}
	// 评分不达标 → Fail（独立 Gate 实例，避免不收敛计数串扰）
	g2 := NewGate(QualityStandard, 90)
	d = g2.Admit(QualityReport{
		Static: StaticReport{Pass: true},
		TEE:    &TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: true},
		Score:  50,
	})
	if d != DecisionFail {
		t.Fatalf("评分应 Fail，got %s", d)
	}
}

func TestGate_ThreeRoundNonConvergence(t *testing.T) {
	g := NewGate(QualityStandard, 90)
	bad := QualityReport{
		Static: StaticReport{Pass: false, Issues: []Issue{{Kind: "unresolved", Severity: "high"}}},
		Score:  60,
	}
	// 3 个缺陷修复轮 + 1 个开场轮（共 4 轮不收敛）→ 第 3 轮不收敛即 HumanReview
	d := g.Admit(bad)
	if d != DecisionFail {
		t.Fatalf("第 1 轮应 Fail，got %s", d)
	}
	if g.NonConvergences() != 1 {
		t.Fatalf("连续不收敛应 1，got %d", g.NonConvergences())
	}
	d = g.Admit(bad)
	if d != DecisionFail {
		t.Fatalf("第 2 轮应 Fail，got %s", d)
	}
	d = g.Admit(bad)
	if d != DecisionHumanReview {
		t.Fatalf("连续 3 轮不收敛应 HumanReview，got %s", d)
	}
	// 换质检员后收敛 → 重置
	if g.TrackRound(true) {
		t.Fatal("收敛轮不应触发换人")
	}
	if g.NonConvergences() != 0 {
		t.Fatalf("收敛后计数应重置 0，got %d", g.NonConvergences())
	}
	// 再次不收敛：从头计数不立即换人
	if d := g.Admit(bad); d != DecisionFail {
		t.Fatalf("重置后第 1 轮应 Fail，got %s", d)
	}
}

// ③ TEE 按产物类型判定
func TestTEE_ArtifactVerdicts(t *testing.T) {
	if v := Verify(TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: true, TestCount: 3}); v != TEEPassed {
		t.Fatalf("GoCode 编译+测试过应 Passed，got %s", v)
	}
	if v := Verify(TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: false, TestPassed: true}); v != TEEFailed {
		t.Fatalf("GoCode 未编译应 Failed，got %s", v)
	}
	if v := Verify(TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: false}); v != TEEFailed {
		t.Fatalf("GoCode 测试未过应 Failed，got %s", v)
	}
	if v := Verify(TestTEEResult{ArtifactType: ArtifactNonGoCode, TestPassed: true}); v != TEEPassed {
		t.Fatalf("NonGoCode 测试过应 Passed，got %s", v)
	}
	if v := Verify(TestTEEResult{ArtifactType: ArtifactDoc, Compiled: false, TestPassed: false}); v != TEENeedsLogicCheck {
		t.Fatalf("Doc 应 NeedsLogicCheck，got %s", v)
	}
	// 门禁视角：Doc 类不因 TEE 未跑而失败
	g := NewGate(QualityStandard, 90)
	d := g.Admit(QualityReport{
		Static: StaticReport{Pass: true},
		TEE:    &TestTEEResult{ArtifactType: ArtifactDoc, TestPassed: false},
		Score:  95,
	})
	if d != DecisionPass {
		t.Fatalf("Doc 类 TEE 不适用应 Pass，got %s", d)
	}
	// 发证
	if _, ok := NewCertificate(TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: true, TestPassed: true}); !ok {
		t.Fatal("通过应可发证")
	}
	if _, ok := NewCertificate(TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: false}); ok {
		t.Fatal("未通过不应发证")
	}
}

// ④ Ledger 累计与 TopSuspects
func TestLedger_RecordAndTopSuspects(t *testing.T) {
	l := NewLedger()
	l.Record("改完不跑测试")
	l.Record("改完不跑测试")
	l.Record("改完不跑测试")
	l.Record("未决标记残留")
	l.Record("未决标记残留")
	l.Record("文档引用失效")
	top := l.TopSuspects(2)
	if len(top) != 2 {
		t.Fatalf("应返回 2 个，got %d", len(top))
	}
	if top[0].Signature != "改完不跑测试" || top[0].Count != 3 {
		t.Fatalf("首位应为 改完不跑测试×3，got %+v", top[0])
	}
	if top[1].Signature != "未决标记残留" || top[1].Count != 2 {
		t.Fatalf("次位应为 未决标记残留×2，got %+v", top[1])
	}
	if l.Count("改完不跑测试") != 3 {
		t.Fatal("Count 错误")
	}
}

// ⑤ yaml 往返（Ledger）
func TestLedger_YAMLRoundTrip(t *testing.T) {
	l := NewLedger()
	l.Record("改完不跑测试")
	l.Record("Mock 残留")
	data, err := l.MarshalYAML()
	if err != nil {
		t.Fatal(err)
	}
	l2 := NewLedger()
	if err := l2.UnmarshalYAML(data); err != nil {
		t.Fatal(err)
	}
	if len(l2.DefectHistory) != 2 || l2.Count("改完不跑测试") != 1 {
		t.Fatalf("往返不符：%+v", l2.DefectHistory)
	}
}

// ⑥ 三档质量解析与边界
func TestQualityLevel_ParseAndSemantics(t *testing.T) {
	if LevelFromString("") != QualityStandard {
		t.Fatal("空应 Standard")
	}
	if LevelFromString("STANDARD") != QualityStandard {
		t.Fatal("STANDARD 应 Standard")
	}
	if LevelFromString("fast") != QualityFast {
		t.Fatal("fast 应 Fast")
	}
	if LevelFromString("none") != QualityNone {
		t.Fatal("none 应 None")
	}
	if LevelFromString("垃圾值") != QualityStandard {
		t.Fatal("非法值应兜底 Standard")
	}
	if QualityStandard.TEEEnabled() != true || QualityFast.TEEEnabled() != false || QualityNone.TEEEnabled() != false {
		t.Fatal("TEE 启用语义错误")
	}
	if QualityStandard.StaticEnabled() != true || QualityFast.StaticEnabled() != true || QualityNone.StaticEnabled() != false {
		t.Fatal("静检启用语义错误")
	}
	if QualityStandard.GateEnabled() != true || QualityNone.GateEnabled() != false {
		t.Fatal("门禁启用语义错误")
	}
	// None 档：恒 Pass
	g := NewGate(QualityNone, 90)
	if d := g.Admit(QualityReport{Score: 0}); d != DecisionPass {
		t.Fatalf("None 档应 Pass，got %s", d)
	}
}

// ⑦ 缺陷清单提取
func TestGate_DefectsFromReport(t *testing.T) {
	g := NewGate(QualityStandard, 90)
	defects := g.DefectsFromReport(QualityReport{
		Static: StaticReport{Issues: []Issue{
			{Kind: "unresolved", Severity: "high", File: "a.md", Line: 2, Hint: "[?]"},
			{Kind: "residue", Severity: "low", File: "b.go", Line: 1, Hint: "TODO"},
		}},
		TEE: &TestTEEResult{ArtifactType: ArtifactGoCode, Compiled: false, TestPassed: false, Output: "build fail"},
	})
	if len(defects) != 2 {
		t.Fatalf("应 2 条缺陷（high 静态 + TEE），got %d: %+v", len(defects), defects)
	}
	if defects[0].ID == defects[len(defects)-1].ID {
		t.Fatal("缺陷 ID 应唯一")
	}
}

// ⑧ 禁写标记：HumanReview 也禁写
func TestDecision_ForbidDone(t *testing.T) {
	if DecisionPass.ForbidDone() {
		t.Fatal("Pass 不应禁写")
	}
	if !DecisionFail.ForbidDone() || !DecisionHumanReview.ForbidDone() {
		t.Fatal("Fail/HumanReview 应禁写")
	}
}
