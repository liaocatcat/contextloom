package thinkmgr

// ── 策略（M1/M2 汇合点）──

// Strategy 轮次面策略输入（runtime 从 config.ThinkingConfig 装配；与配置面 9 字段同构）。
type Strategy struct {
	Policy              string // off/auto/strict
	MinRounds           int    // 最少思考轮数（0=不强制）
	MaxThinkRounds      int    // 续轮注入总上限（0→3）
	VerifyPass          string // off/auto/on
	ComplexityThreshold int    // 0-100（0→60）
	NudgeEnabled        bool   // 续思提示注入开关（M6）
	NudgeMax            int    // nudge 单独上限（0→2）
	RoundEffortPolicy   string // none|escalate
	RoundEffortCap      string // low|medium|high|xhigh|max（""→max）
}

// Normalize 零值兜底（不改变 off 语义：Policy==""→off）。
func (s Strategy) Normalize() Strategy {
	if s.Policy == "" {
		s.Policy = "off"
	}
	if s.MaxThinkRounds <= 0 {
		s.MaxThinkRounds = 3
	}
	if s.ComplexityThreshold <= 0 {
		s.ComplexityThreshold = 60
	}
	if s.NudgeMax <= 0 {
		s.NudgeMax = 2
	}
	if s.RoundEffortPolicy == "" {
		s.RoundEffortPolicy = "none"
	}
	if s.RoundEffortCap == "" {
		s.RoundEffortCap = "max"
	}
	return s
}

// Policy 任务级策略（executor 消费）：MinRounds strict 硬下限/auto 软下限；MaxExtraRounds=拦截+nudge
// 合计上限；NudgeGap=2/NudgeMinRunes=200/StallRounds=2 为常量；VerifyEffort 仅 escalate 时=cap。
type Policy struct {
	Enabled        bool
	Mode           string     // off/auto/strict
	Complexity     Complexity // 任务复杂度分级
	Score          int        // 复杂度评分（展示/度量用）
	MinRounds      int        // 最少思考轮数（strict 硬下限）
	MaxExtraRounds int        // 续轮注入总上限（拦截+nudge）
	RequireVerify  bool       // 是否强制验证轮（strict + verify_pass≠off）
	VerifyPass     string     // off/auto/on
	VerifyEffort   string     // 验证轮 effort 档位（仅 escalate 时=cap）
	NudgeEnabled   bool       // 续思提示开关
	NudgeMax       int        // nudge 单独上限
	NudgeGap       int        // 续思最小间隔轮数
	NudgeMinRunes  int        // 短答阈值（低于此值视为回答不充分）
	StallRounds    int        // 连续无新证据止损轮数
}

// BuildPolicy 生成任务级策略（简单任务全零 → executor 不注入）。
func BuildPolicy(s Strategy, c Complexity, score int) Policy {
	s = s.Normalize()
	p := Policy{Enabled: s.Policy != "off", Mode: s.Policy, Complexity: c, Score: score,
		MaxExtraRounds: s.MaxThinkRounds, VerifyPass: s.VerifyPass, NudgeEnabled: s.NudgeEnabled,
		NudgeMax: s.NudgeMax, NudgeGap: 2, NudgeMinRunes: 200, StallRounds: 2}
	if c != ComplexityComplex {
		return p // 简单任务：零注入、零升档、零送审（M12 条件路由）
	}
	if s.Policy == "strict" {
		p.MinRounds, p.RequireVerify = s.MinRounds, s.VerifyPass != "off"
	}
	if s.RoundEffortPolicy == "escalate" {
		p.VerifyEffort = s.RoundEffortCap
	}
	return p
}
