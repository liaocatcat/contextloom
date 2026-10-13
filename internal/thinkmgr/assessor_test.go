package thinkmgr

import (
	"strings"
	"testing"
)

// TestAssessor_TableDriven 确定性评分 ≥10 组 + 阈值 60 路由（§6.1 #3）。
func TestAssessor_TableDriven(t *testing.T) {
	long119 := strings.Repeat("测", 119)
	long120 := strings.Repeat("测", 120)
	long400 := strings.Repeat("测", 400)
	long500 := strings.Repeat("测", 500)

	cases := []struct {
		name    string
		sig     Signals
		want    int
		complex bool // 阈值 60 下是否 complex
	}{
		{"空任务", Signals{}, 0, false},
		{"短任务", Signals{TaskText: "帮我写个函数"}, 0, false},
		{"长度119不达线", Signals{TaskText: long119}, 0, false},
		{"长度120达线", Signals{TaskText: long120}, 20, false},
		{"超长400", Signals{TaskText: long400}, 40, false},
		{"超长500不重复计", Signals{TaskText: long500}, 40, false},
		{"模糊1", Signals{FuzzyHits: 1}, 10, false},
		{"模糊2", Signals{FuzzyHits: 2}, 20, false},
		{"模糊3", Signals{FuzzyHits: 3}, 30, false},
		{"模糊4封顶30", Signals{FuzzyHits: 4}, 30, false},
		{"goal模式", Signals{Mode: "goal"}, 10, false},
		{"task多目标词", Signals{Mode: "task", TaskText: "同时完成 A 和 B"}, 10, false},
		{"task无多目标词", Signals{Mode: "task", TaskText: "完成 A"}, 0, false},
		{"图片", Signals{ImageN: 2}, 10, false},
		{"组合恰好60", Signals{TaskText: long120, FuzzyHits: 3, Mode: "goal"}, 60, true},
		{"组合差10分", Signals{TaskText: long120, FuzzyHits: 3}, 50, false},
		{"显式simple覆盖评分", Signals{TaskText: long400, FuzzyHits: 3, ImageN: 1, Explicit: "simple"}, 0, false},
		{"显式complex直通", Signals{Explicit: "complex"}, 100, true},
		{"显式大小写与空白", Signals{Explicit: "  COMPLEX  "}, 100, true},
		{"显式simple直通", Signals{Explicit: "Simple"}, 0, false},
	}
	a := NewAssessor(60)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := a.Score(tc.sig); got != tc.want {
				t.Errorf("Score(%+v) = %d, want %d", tc.sig, got, tc.want)
			}
			wantC := ComplexitySimple
			if tc.complex {
				wantC = ComplexityComplex
			}
			if got := a.Assess(tc.sig); got != wantC {
				t.Errorf("Assess(%+v) = %q, want %q", tc.sig, got, wantC)
			}
		})
	}
}

// TestAssessor_MultiGoalKeywords 多目标词全表 + 模式限定（仅 task 命中）。
func TestAssessor_MultiGoalKeywords(t *testing.T) {
	a := NewAssessor(60)
	for _, kw := range []string{"同时", "分别", "并行", "并且", "以及", "多个", "批量"} {
		if got := a.Score(Signals{Mode: "task", TaskText: "请" + kw + "处理"}); got != 10 {
			t.Errorf("关键词 %q：Score = %d, want 10", kw, got)
		}
	}
	if got := a.Score(Signals{Mode: "chat", TaskText: "同时处理"}); got != 0 {
		t.Errorf("chat 模式不应命中多目标词：Score = %d", got)
	}
	if got := a.Score(Signals{Mode: "goal", TaskText: "同时处理"}); got != 10 {
		t.Errorf("goal 模式仅计 goal 分：Score = %d, want 10", got)
	}
}

// TestAssessor_Threshold 阈值路由：60 恰好 complex；自定义阈值生效；<=0 取默认。
func TestAssessor_Threshold(t *testing.T) {
	long120 := strings.Repeat("测", 120)
	a := NewAssessor(0)
	if a.Threshold != 60 {
		t.Fatalf("NewAssessor(0).Threshold = %d, want 60", a.Threshold)
	}
	sig60 := Signals{TaskText: long120, FuzzyHits: 3, Mode: "goal"} // 20+30+10=60
	if got := a.Assess(sig60); got != ComplexityComplex {
		t.Errorf("60 分 @阈值60 = %q, want complex", got)
	}
	sig50 := Signals{TaskText: long120, FuzzyHits: 3} // 50
	if got := a.Assess(sig50); got != ComplexitySimple {
		t.Errorf("50 分 @阈值60 = %q, want simple", got)
	}
	if got := NewAssessor(30).Assess(sig50); got != ComplexityComplex {
		t.Errorf("50 分 @阈值30 = %q, want complex", got)
	}
	if got := NewAssessor(100).Assess(Signals{Explicit: "complex"}); got != ComplexityComplex {
		t.Errorf("100 分 @阈值100 = %q, want complex", got)
	}
}
