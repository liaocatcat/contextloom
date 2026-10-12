package overseer

import "testing"

func TestBiasAnalyze_NoEvidenceClaim(t *testing.T) {
	hits := BiasAnalyze(
		"我已经全面检查了所有代码，全部通过，没有遗漏。\n\n[GOAL_STEPS][{\"id\":\"s1\",\"status\":\"done\",\"evidence\":\"完成\"}][/GOAL_STEPS]",
		&ToolEvidence{}, true)
	if len(hits) != 2 {
		t.Fatalf("无证据的全量式声明+测试声明应命中 2 条，got %+v", hits)
	}
	kinds := map[BiasKind]bool{}
	for _, h := range hits {
		kinds[h.Kind] = true
	}
	if !kinds[BiasNoEvidenceClaim] || !kinds[BiasUnverifiedPass] {
		t.Fatalf("应命中 no_evidence_claim + unverified_pass，got %+v", hits)
	}
	for _, h := range hits {
		if h.Kind == BiasNoEvidenceClaim && (h.Penalty != 20 || h.DefectID != "d-bias-totality") {
			t.Fatalf("惩罚/缺陷ID 异常: %+v", h)
		}
	}
}

func TestBiasAnalyze_NoEvidenceClaim_WithToolEvidence(t *testing.T) {
	hits := BiasAnalyze(
		"我已全面检查，全部通过。",
		&ToolEvidence{RanTests: true, RanDiff: true}, true)
	if len(hits) != 0 {
		t.Fatalf("有工具证据时不应命中全量式声明，got %+v", hits)
	}
}

func TestBiasAnalyze_PrematureDone(t *testing.T) {
	hits := BiasAnalyze(
		"做完了。[GOAL_DONE]",
		&ToolEvidence{}, false) // specReady=false
	if len(hits) != 1 || hits[0].Kind != BiasPrematureDone {
		t.Fatalf("spec 未过就 [GOAL_DONE] 应命中，got %+v", hits)
	}
	// specReady=true 时不命中
	if hits := BiasAnalyze("做完了。[GOAL_DONE]", &ToolEvidence{}, true); len(hits) != 0 {
		t.Fatalf("spec 已过不应命中早产完成，got %+v", hits)
	}
}

func TestBiasAnalyze_UnverifiedPass(t *testing.T) {
	hits := BiasAnalyze(
		"测试通过，全部用例 PASS。",
		&ToolEvidence{}, true)
	if len(hits) != 1 || hits[0].Kind != BiasUnverifiedPass {
		t.Fatalf("无 run_tests 证据的测试声明应命中，got %+v", hits)
	}
	// 有 run_tests 证据 → 不命中
	if hits := BiasAnalyze("测试通过。", &ToolEvidence{RanTests: true}, true); len(hits) != 0 {
		t.Fatalf("有测试证据不应命中，got %+v", hits)
	}
}

func TestBiasAnalyze_Clean(t *testing.T) {
	hits := BiasAnalyze(
		"已完成步骤 s1：新增 internal/x/y.go 并补了单元测试。\n\n[GOAL_STEPS][{\"id\":\"s1\",\"status\":\"done\",\"evidence\":\"y.go 创建，go test 通过\",\"proofs\":[{\"kind\":\"file\",\"target\":\"internal/x/y.go\"},{\"kind\":\"test\",\"target\":\"./internal/x\"}]}][/GOAL_STEPS]",
		&ToolEvidence{RanTests: true}, true)
	if len(hits) != 0 {
		t.Fatalf("带证据的干净响应不应命中，got %+v", hits)
	}
}

func TestTotalPenalty(t *testing.T) {
	hits := []BiasHit{
		{Kind: BiasNoEvidenceClaim, Penalty: 20},
		{Kind: BiasPrematureDone, Penalty: 30},
		{Kind: BiasUnverifiedPass, Penalty: 15},
	}
	if p := TotalPenalty(hits); p != 65 {
		t.Fatalf("合计扣分应为 65，got %v", p)
	}
	if p := TotalPenalty(nil); p != 0 {
		t.Fatalf("空命中扣分应为 0，got %v", p)
	}
}
