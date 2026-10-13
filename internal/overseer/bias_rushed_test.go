package overseer

import "testing"

// TestBiasRushed_Deterministic M11 度量判据（表驱动）：
// complex+1 轮+无证据→命中 -10；有工具证据或已送审→不命中；simple 同形不误伤。
func TestBiasRushed_Deterministic(t *testing.T) {
	cases := []struct {
		name       string
		rounds     int
		toolCalls  int
		verified   bool
		complexity string
		wantHit    bool
	}{
		{"complex+1轮+无证据→命中", 1, 0, false, "complex", true},
		{"complex+0轮+无证据→命中（总轮数<2）", 0, 0, false, "complex", true},
		{"complex+1轮+有工具证据→不命中", 1, 3, false, "complex", false},
		{"complex+1轮+已送审→不命中", 1, 0, true, "complex", false},
		{"complex+2轮+无证据→不命中（轮数达标）", 2, 0, false, "complex", false},
		{"simple+1轮+无证据→不误伤", 1, 0, false, "simple", false},
		{"空 complexity→不命中", 1, 0, false, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hits := BiasRushed(c.rounds, c.toolCalls, c.verified, c.complexity)
			if !c.wantHit {
				if len(hits) != 0 {
					t.Fatalf("不应命中，got %+v", hits)
				}
				return
			}
			if len(hits) != 1 {
				t.Fatalf("应命中 1 条，got %+v", hits)
			}
			h := hits[0]
			if h.Kind != BiasRushedAnswer {
				t.Fatalf("Kind 应为 %q，got %q", BiasRushedAnswer, h.Kind)
			}
			if h.Penalty != 10 {
				t.Fatalf("Penalty 应为 10，got %v", h.Penalty)
			}
			if h.Desc == "" || h.DefectID != "d-bias-rushed-answer" {
				t.Fatalf("Desc/DefectID 异常: %+v", h)
			}
			if p := TotalPenalty(hits); p != 10 {
				t.Fatalf("TotalPenalty 应为 10（封顶规则复用），got %v", p)
			}
		})
	}
}
