package selfcheck

import (
	"fmt"
	"strings"
)

// QualityLevel 质量三档（§4.6.6）：质量默认拉满，速度可自主降级。
type QualityLevel int

const (
	// QualityNone 关门禁（回到旧行为，仅保留监工参考建议）。
	QualityNone QualityLevel = iota
	// QualityFast 关 TEE、保静检（快速档）。
	QualityFast
	// QualityStandard 全开（默认）：静检 + TEE + 门禁。
	QualityStandard
)

// LevelFromString 解析三档（大小写不敏感；standard/fast/none；
// full/on → standard；空或非法 → standard 兜底，防呆默认拉满）。
func LevelFromString(s string) QualityLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "none", "off", "skip":
		return QualityNone
	case "fast", "quick", "static_only":
		return QualityFast
	case "standard", "full", "on", "":
		return QualityStandard
	default:
		return QualityStandard
	}
}

// String 可读名。
func (q QualityLevel) String() string {
	switch q {
	case QualityNone:
		return "none"
	case QualityFast:
		return "fast"
	default:
		return "standard"
	}
}

// TEEEnabled TEE 是否启用（None/Fast 关，Standard 开）。
func (q QualityLevel) TEEEnabled() bool { return q == QualityStandard }

// StaticEnabled 静态核查是否启用（None 关，Fast/Standard 开）。
func (q QualityLevel) StaticEnabled() bool { return q != QualityNone }

// GateEnabled 门禁是否启用（None 关，其余开）。
func (q QualityLevel) GateEnabled() bool { return q != QualityNone }

// Decision 门禁裁决三态（§4.6.3）。
type Decision int

const (
	// DecisionPass 放行外显。
	DecisionPass Decision = iota
	// DecisionFail 缺陷清单 + 禁写 [GOAL_DONE]（非打磨轮）。
	DecisionFail
	// DecisionHumanReview 连续多轮不收敛 → 转人工（换质检员）。
	DecisionHumanReview
)

// String 可读名。
func (d Decision) String() string {
	switch d {
	case DecisionPass:
		return "pass"
	case DecisionHumanReview:
		return "human_review"
	default:
		return "fail"
	}
}

// Defect 一条缺陷（注入下一轮提示词的 [GOAL_DEFECTS]）。
type Defect struct {
	ID       string `yaml:"id"`
	Desc     string `yaml:"desc"`
	Severity string `yaml:"severity"` // high/medium/low
}

// QualityReport 门禁输入：静态核查 + TEE 结果 + 评分维度。
type QualityReport struct {
	Static      StaticReport   // 静态核查报告
	TEE         *TestTEEResult // TEE 结果（文档类可 nil，走 NeedsLogicCheck）
	Score       int            // 综合评分（0-100，监工四维 + TEE 通过率第 5 维）
	PassScore   int            // 通过阈值（默认 90）
	NoWriteDone bool           // 本轮禁写 [GOAL_DONE]（缺陷修复轮强制注入）
}

// Gate 强制质量门禁（门禁有牙齿）。
type Gate struct {
	level           QualityLevel
	passScore       int
	nonConvergences int // 连续不收敛计数
	maxNonConverge  int // 触发换人的阈值（默认 3）
	rounds          int
}

// NewGate 构造门禁（默认 standard、阈值 90、连续 3 轮不收敛转人工）。
func NewGate(level QualityLevel, passScore int) *Gate {
	if passScore <= 0 {
		passScore = 90
	}
	return &Gate{level: level, passScore: passScore, maxNonConverge: 3}
}

// PassScore 返回通过阈值。
func (g *Gate) PassScore() int { return g.passScore }

// Level 返回当前质量档。
func (g *Gate) Level() QualityLevel { return g.level }

// TrackRound 记录一轮是否收敛（converged=true 重置连续计数，false 累加）。
// 返回是否已触发换质检员/转人工（连续 maxNonConverge 轮不收敛）。
func (g *Gate) TrackRound(converged bool) bool {
	g.rounds++
	if converged {
		g.nonConvergences = 0
		return false
	}
	g.nonConvergences++
	return g.nonConvergences >= g.maxNonConverge
}

// NonConvergences 当前连续不收敛轮数（观测/GUI 徽标用）。
func (g *Gate) NonConvergences() int { return g.nonConvergences }

// Admit 门禁裁决（§4.6.3）：
//   - None 档：恒 Pass（门禁关闭）；
//   - 静态核查不通过或评分不达标 → Fail（附缺陷清单 + 禁写标记）；
//   - 连续多轮不收敛 → HumanReview。
func (g *Gate) Admit(q QualityReport) Decision {
	if !g.level.GateEnabled() {
		return DecisionPass
	}
	// 文档类产物：TEE 不适用 → 以静态核查 + 评分裁决，不作为 TEE 失败。
	teePass := true
	if q.TEE != nil && q.TEE.ArtifactType != ArtifactDoc {
		teePass = Verify(*q.TEE) == TEEPassed
	}
	staticPass := q.Static.Pass
	scorePass := q.Score >= g.passScore

	if q.NoWriteDone {
		// 缺陷修复轮自己又被判不通过：视为不收敛
		_ = g.TrackRound(false)
		return DecisionFail
	}

	if staticPass && teePass && scorePass {
		g.TrackRound(true)
		return DecisionPass
	}
	if g.TrackRound(false) {
		return DecisionHumanReview
	}
	return DecisionFail
}

// DefectsFromReport 从 QualityReport 提取缺陷清单（注入 [GOAL_DEFECTS]）。
func (g *Gate) DefectsFromReport(q QualityReport) []Defect {
	var out []Defect
	seq := 0
	nextID := func() string {
		seq++
		return fmt.Sprintf("D%03d", seq)
	}
	for _, it := range q.Static.Issues {
		if it.Severity == "high" || it.Severity == "medium" {
			out = append(out, Defect{ID: nextID(), Desc: fmt.Sprintf("[%s] %s:%d %s", it.Kind, it.File, it.Line, it.Hint), Severity: it.Severity})
		}
	}
	if q.TEE != nil && q.TEE.ArtifactType != ArtifactDoc && Verify(*q.TEE) != TEEPassed {
		out = append(out, Defect{ID: nextID(), Desc: q.TEE.String(), Severity: "high"})
	}
	return out
}

// ForbidDone 是否应禁写 [GOAL_DONE]（Fail/HumanReview 时 true）。
func (d Decision) ForbidDone() bool { return d != DecisionPass }
