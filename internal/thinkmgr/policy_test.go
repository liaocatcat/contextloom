package thinkmgr

import "testing"

// TestStrategy_Normalize 零值兜底：Policy ""→off、MaxThinkRounds<=0→3、ComplexityThreshold<=0→60、
// NudgeMax<=0→2、RoundEffortPolicy ""→none、RoundEffortCap ""→max。
func TestStrategy_Normalize(t *testing.T) {
	got := (Strategy{}).Normalize()
	if got.Policy != "off" {
		t.Errorf("Policy = %q, want off", got.Policy)
	}
	if got.MaxThinkRounds != 3 {
		t.Errorf("MaxThinkRounds = %d, want 3", got.MaxThinkRounds)
	}
	if got.ComplexityThreshold != 60 {
		t.Errorf("ComplexityThreshold = %d, want 60", got.ComplexityThreshold)
	}
	if got.NudgeMax != 2 {
		t.Errorf("NudgeMax = %d, want 2", got.NudgeMax)
	}
	if got.RoundEffortPolicy != "none" {
		t.Errorf("RoundEffortPolicy = %q, want none", got.RoundEffortPolicy)
	}
	if got.RoundEffortCap != "max" {
		t.Errorf("RoundEffortCap = %q, want max", got.RoundEffortCap)
	}
	if got.MinRounds != 0 || got.VerifyPass != "" {
		t.Errorf("MinRounds/VerifyPass 不应被兜底改写：%d/%q", got.MinRounds, got.VerifyPass)
	}

	full := Strategy{Policy: "strict", MinRounds: 2, MaxThinkRounds: 5, VerifyPass: "on",
		ComplexityThreshold: 80, NudgeEnabled: true, NudgeMax: 4,
		RoundEffortPolicy: "escalate", RoundEffortCap: "high"}
	if got := full.Normalize(); got != full {
		t.Errorf("非零值被改写：%+v", got)
	}

	neg := Strategy{MaxThinkRounds: -1, ComplexityThreshold: -5, NudgeMax: -2}.Normalize()
	if neg.MaxThinkRounds != 3 || neg.ComplexityThreshold != 60 || neg.NudgeMax != 2 {
		t.Errorf("负数未兜底：%+v", neg)
	}
}

// TestBuildPolicy_SimpleZeroInject simple 任务返回零注入策略（MinRounds=0/RequireVerify=false/VerifyEffort=""）。
func TestBuildPolicy_SimpleZeroInject(t *testing.T) {
	s := Strategy{Policy: "strict", MinRounds: 3, VerifyPass: "on", NudgeEnabled: true,
		NudgeMax: 2, RoundEffortPolicy: "escalate", RoundEffortCap: "max"}
	p := BuildPolicy(s, ComplexitySimple, 10)
	if !p.Enabled || p.Mode != "strict" {
		t.Errorf("Enabled/Mode = %v/%q, want true/strict", p.Enabled, p.Mode)
	}
	if p.MinRounds != 0 || p.RequireVerify || p.VerifyEffort != "" {
		t.Errorf("simple 应零注入/零升档：MinRounds=%d RequireVerify=%v VerifyEffort=%q",
			p.MinRounds, p.RequireVerify, p.VerifyEffort)
	}
	if p.MaxExtraRounds != 3 || p.NudgeGap != 2 || p.NudgeMinRunes != 200 || p.StallRounds != 2 {
		t.Errorf("常量护栏缺失：%+v", p)
	}
	// simple + 默认策略（nudge/verify 均关闭）→ 不拦截、不注入
	pd := BuildPolicy(Strategy{Policy: "strict"}, ComplexitySimple, 10)
	tr := NewTracker(pd, 10, ComplexitySimple)
	tr.BeginRound()
	if hit, _ := tr.ShouldInterceptFinal(false); hit {
		t.Error("simple 不应收尾拦截")
	}
	if nudge, _ := tr.ShouldNudge(1, 10, 0); nudge {
		t.Error("simple + 默认策略不应续思注入")
	}
}

// TestBuildPolicy_ComplexModes strict 才设 MinRounds/RequireVerify；escalate 才设 VerifyEffort=cap。
func TestBuildPolicy_ComplexModes(t *testing.T) {
	base := Strategy{Policy: "strict", MinRounds: 3, VerifyPass: "on",
		RoundEffortPolicy: "escalate", RoundEffortCap: "max"}

	strict := BuildPolicy(base, ComplexityComplex, 70)
	if strict.MinRounds != 3 || !strict.RequireVerify || strict.VerifyEffort != "max" {
		t.Errorf("strict = %+v", strict)
	}

	offVerify := base
	offVerify.VerifyPass = "off"
	if p := BuildPolicy(offVerify, ComplexityComplex, 70); p.RequireVerify {
		t.Errorf("verify_pass=off 不应 RequireVerify：%+v", p)
	}

	auto := base
	auto.Policy = "auto"
	p := BuildPolicy(auto, ComplexityComplex, 70)
	if p.MinRounds != 0 || p.RequireVerify {
		t.Errorf("auto 不应设硬下限/强制验证：%+v", p)
	}
	if p.VerifyEffort != "max" {
		t.Errorf("escalate 升档不依赖 policy 模式：%+v", p)
	}

	none := base
	none.RoundEffortPolicy = "none"
	if p := BuildPolicy(none, ComplexityComplex, 70); p.VerifyEffort != "" {
		t.Errorf("escalate=none 不应升档：%+v", p)
	}

	off := BuildPolicy(Strategy{}, ComplexityComplex, 70)
	if off.Enabled || off.Mode != "off" || off.MinRounds != 0 || off.RequireVerify {
		t.Errorf("off 策略 = %+v", off)
	}
}
