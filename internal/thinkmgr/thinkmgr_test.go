package thinkmgr

import (
	"strings"
	"testing"
)

// testPolicy 构造测试用任务级策略（complex；verify/nudge 默认关闭）。
func testPolicy(mode string, minRounds, maxExtra int) Policy {
	return Policy{
		Enabled:        mode != "off",
		Mode:           mode,
		Complexity:     ComplexityComplex,
		Score:          70,
		MinRounds:      minRounds,
		MaxExtraRounds: maxExtra,
		VerifyPass:     "off",
		NudgeMax:       2,
		NudgeGap:       2,
		NudgeMinRunes:  200,
		StallRounds:    2,
	}
}

// strictMin 构造 strict + 最少轮数 + 强制验证的策略。
func strictMin(minRounds int) Policy {
	p := testPolicy("strict", minRounds, 3)
	p.RequireVerify = true
	p.VerifyPass = "on"
	return p
}

// TestTracker_KindFlow 类型流转表驱动：explore→execute→verify→refine→execute。
func TestTracker_KindFlow(t *testing.T) {
	tr := NewTracker(testPolicy("strict", 0, 3), 70, ComplexityComplex)
	steps := []struct {
		name string
		run  func() Kind
		want Kind
	}{
		{"第 1 轮（首探）", tr.BeginRound, KindExplore},
		{"第 2 轮（常规推进）", tr.BeginRound, KindExecute},
		{"注入后第 3 轮（验证）", func() Kind {
			tr.Inject(KindVerify, "测试：收尾拦截")
			return tr.BeginRound()
		}, KindVerify},
		{"NEEDS_FIX 末条降级（修订）", func() Kind {
			tr.SettleRound("[THINK_NEEDS_FIX] 修正了关键假设 2 的推导")
			s := tr.Snapshot()
			return s.Rounds[len(s.Rounds)-1].Kind
		}, KindRefine},
		{"复验后第 4 轮（预设只生效一次）", tr.BeginRound, KindExecute},
	}
	for _, st := range steps {
		if got := st.run(); got != st.want {
			t.Fatalf("%s = %q, want %q", st.name, got, st.want)
		}
	}
	if tr.Snapshot().Verified {
		t.Error("NEEDS_FIX 后 Verified 应为 false（保证复验）")
	}
}

// TestTracker_InjectNudge_PresetOnce 软注入同样预设 verify 且只生效一次。
func TestTracker_InjectNudge_PresetOnce(t *testing.T) {
	tr := NewTracker(testPolicy("auto", 0, 3), 70, ComplexityComplex)
	tr.BeginRound() // explore
	tr.InjectNudge("测试：续思提示")
	if k := tr.BeginRound(); k != KindVerify {
		t.Fatalf("nudge 后轮次 = %q, want verify", k)
	}
	if k := tr.BeginRound(); k != KindExecute {
		t.Fatalf("预设只生效一次，got %q, want execute", k)
	}
	if s := tr.Snapshot(); s.Nudges != 1 {
		t.Errorf("Nudges = %d, want 1", s.Nudges)
	}
}

// TestTracker_FinishRound 回填末条 ToolN/HasTools/BodyLen；无轮次时空操作不 panic。
func TestTracker_FinishRound(t *testing.T) {
	tr := NewTracker(testPolicy("auto", 0, 3), 70, ComplexityComplex)
	tr.FinishRound(1, 100) // 无轮次：空操作

	tr.BeginRound()
	tr.FinishRound(3, 456)
	last := tr.Snapshot().Rounds[0]
	if last.Round != 1 || last.ToolN != 3 || !last.HasTools || last.BodyLen != 456 {
		t.Errorf("第 1 轮回填 = %+v", last)
	}

	tr.BeginRound()
	tr.FinishRound(0, 12)
	last = tr.Snapshot().Rounds[1]
	if last.Round != 2 || last.ToolN != 0 || last.HasTools || last.BodyLen != 12 {
		t.Errorf("第 2 轮回填 = %+v", last)
	}
}

// TestTracker_Accessors Policy/Snapshot 只读访问器。
func TestTracker_Accessors(t *testing.T) {
	p := testPolicy("auto", 1, 2)
	tr := NewTracker(p, 42, ComplexityComplex)
	if got := tr.Policy(); got != p {
		t.Errorf("Policy() = %+v, want %+v", got, p)
	}
	s := tr.Snapshot()
	if s.Score != 42 || s.Complexity != ComplexityComplex || len(s.Rounds) != 0 || s.Verified {
		t.Errorf("初始快照 = %+v", s)
	}
}

// TestSettleRound_Marks 标记解析：VERIFIED→通过；NEEDS_FIX→降级 refine 且未验证；verify 轮兜底。
func TestSettleRound_Marks(t *testing.T) {
	newVerifyTracker := func() *Tracker {
		tr := NewTracker(strictMin(0), 70, ComplexityComplex)
		tr.BeginRound() // explore
		tr.Inject(KindVerify, "测试")
		tr.BeginRound() // verify
		return tr
	}

	// ① 含 VERIFIED → Verified=true
	tr := newVerifyTracker()
	tr.SettleRound("验证过程……最终答案 [THINK_VERIFIED]")
	if !tr.Snapshot().Verified {
		t.Error("含 [THINK_VERIFIED] 未置为已验证")
	}

	// ② NEEDS_FIX → 末条降级 refine 且保持未验证（即使此前已验证也要回退，保证复验）
	tr = newVerifyTracker()
	tr.MarkVerified()
	tr.SettleRound("[THINK_NEEDS_FIX] 修正了关键假设")
	s := tr.Snapshot()
	if s.Verified {
		t.Error("NEEDS_FIX 后应保持未验证")
	}
	if s.Rounds[len(s.Rounds)-1].Kind != KindRefine {
		t.Errorf("末条 Kind = %q, want refine", s.Rounds[len(s.Rounds)-1].Kind)
	}

	// ③ verify 轮非空且无标记 → 兜底通过
	tr = newVerifyTracker()
	tr.SettleRound("复核完成，结论不变")
	if !tr.Snapshot().Verified {
		t.Error("verify 轮非空无标记应兜底通过")
	}

	// ④ verify 轮空正文 → 不兜底
	tr = newVerifyTracker()
	tr.SettleRound("   ")
	if tr.Snapshot().Verified {
		t.Error("空正文不应兜底通过")
	}

	// ⑤ 非 verify 轮无标记 → 不兜底
	tr = NewTracker(strictMin(0), 70, ComplexityComplex)
	tr.BeginRound() // explore
	tr.SettleRound("普通回答")
	if tr.Snapshot().Verified {
		t.Error("非 verify 轮不应兜底通过")
	}
}

// TestIntercept_EscapeHatches 收尾拦截逃生通道表驱动。
func TestIntercept_EscapeHatches(t *testing.T) {
	cases := []struct {
		name         string
		policy       Policy
		rounds       int
		verified     bool
		exceed       int // 已注入数（Intercepts+Nudges）
		budget       bool
		wantHit      bool
		wantInReason string
	}{
		{name: "off 不拦截", policy: testPolicy("off", 3, 3), wantHit: false},
		{name: "auto 不拦截（软路由）", policy: testPolicy("auto", 3, 3), wantHit: false},
		{name: "strict+budgetExceeded 不拦截", policy: strictMin(3), budget: true,
			wantHit: false, wantInReason: "红线"},
		{name: "strict+超上限不拦截", policy: strictMin(3), exceed: 3,
			wantHit: false, wantInReason: "上限"},
		{name: "strict+未达下限拦截", policy: strictMin(3), rounds: 1,
			wantHit: true, wantInReason: "轮数 1<3"},
		{name: "strict+min=0 未验证拦截", policy: strictMin(0), rounds: 5,
			wantHit: true, wantInReason: "未完成验证轮"},
		{name: "strict+达标放行", policy: strictMin(2), rounds: 2, verified: true,
			wantHit: false, wantInReason: "已达最少轮数且验证通过"},
		{name: "simple 任务不拦截", policy: Policy{Enabled: true, Mode: "strict",
			Complexity: ComplexitySimple, MaxExtraRounds: 3}, rounds: 1,
			wantHit: false, wantInReason: "验证通过"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := NewTracker(tc.policy, 70, tc.policy.Complexity)
			for i := 0; i < tc.rounds; i++ {
				tr.BeginRound()
			}
			for i := 0; i < tc.exceed; i++ {
				tr.Inject(KindVerify, "测试")
			}
			if tc.verified {
				tr.MarkVerified()
			}
			hit, reason := tr.ShouldInterceptFinal(tc.budget)
			if hit != tc.wantHit {
				t.Fatalf("ShouldInterceptFinal(%v) = %v（%s）, want %v", tc.budget, hit, reason, tc.wantHit)
			}
			if tc.wantInReason != "" && !strings.Contains(reason, tc.wantInReason) {
				t.Errorf("reason = %q, 应包含 %q", reason, tc.wantInReason)
			}
		})
	}
}

// TestShouldNudge_FrequencyCap 频控：上限、间隔、达标豁免、verify_pass=on 首次注入、simple/off 零注入。
func TestShouldNudge_FrequencyCap(t *testing.T) {
	// ① nudge 上限 + 间隔
	p := testPolicy("auto", 0, 5)
	p.NudgeEnabled = true
	tr := NewTracker(p, 70, ComplexityComplex)
	tr.BeginRound()
	if ok, _ := tr.ShouldNudge(1, 10, 0); !ok {
		t.Error("首次 nudge 应注入")
	}
	tr.InjectNudge("第 1 次")
	if ok, reason := tr.ShouldNudge(2, 10, 1); ok {
		t.Errorf("间隔不足仍注入：%s", reason)
	}
	if ok, _ := tr.ShouldNudge(3, 10, 1); !ok {
		t.Error("间隔满足应注入")
	}
	tr.InjectNudge("第 2 次")
	if ok, reason := tr.ShouldNudge(5, 10, 3); ok {
		t.Errorf("nudge_max=2 第 3 次仍注入：%s", reason)
	}

	// ② 达标豁免：轮数>=MinRounds 且 bodyRunes>=NudgeMinRunes
	p2 := testPolicy("auto", 3, 5)
	p2.NudgeEnabled = true
	tr2 := NewTracker(p2, 70, ComplexityComplex)
	tr2.BeginRound()
	if ok, reason := tr2.ShouldNudge(3, 200, 0); ok {
		t.Errorf("轮数达标且回答充分仍注入：%s", reason)
	}
	if ok, _ := tr2.ShouldNudge(3, 199, 0); !ok {
		t.Error("回答不充分应继续注入")
	}

	// ③ verify_pass=on 首次注入（不受 NudgeEnabled 影响），仅一次
	p3 := testPolicy("auto", 0, 5)
	p3.VerifyPass = "on"
	tr3 := NewTracker(p3, 70, ComplexityComplex)
	tr3.BeginRound()
	if ok, reason := tr3.ShouldNudge(1, 10, 0); !ok || !strings.Contains(reason, "verify_pass=on") {
		t.Errorf("verify_pass=on 首次应注入：(%v, %s)", ok, reason)
	}
	tr3.InjectNudge("验证轮")
	if ok, _ := tr3.ShouldNudge(1, 10, 0); ok {
		t.Error("verify_pass=on 只应注入一次")
	}

	// ④ 续轮总上限
	p4 := testPolicy("auto", 0, 1)
	p4.NudgeEnabled = true
	tr4 := NewTracker(p4, 70, ComplexityComplex)
	tr4.BeginRound()
	tr4.Inject(KindVerify, "占满上限")
	if ok, reason := tr4.ShouldNudge(1, 10, 0); ok || !strings.Contains(reason, "上限") {
		t.Errorf("已达续轮上限仍注入：(%v, %s)", ok, reason)
	}

	// ⑤ simple + 默认策略零注入
	ps := BuildPolicy(Strategy{Policy: "auto"}, ComplexitySimple, 10)
	trs := NewTracker(ps, 10, ComplexitySimple)
	trs.BeginRound()
	if ok, _ := trs.ShouldNudge(1, 10, 0); ok {
		t.Error("simple + 默认策略不应注入")
	}

	// ⑥ off 零注入/零拦截
	po := Policy{Enabled: false, Mode: "off", MaxExtraRounds: 3}
	tro := NewTracker(po, 0, ComplexitySimple)
	tro.BeginRound()
	if ok, _ := tro.ShouldNudge(1, 10, 0); ok {
		t.Error("off 不应注入")
	}
	if hit, _ := tro.ShouldInterceptFinal(false); hit {
		t.Error("off 不应拦截")
	}
}
