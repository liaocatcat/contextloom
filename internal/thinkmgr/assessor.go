package thinkmgr

import (
	"regexp"
	"strings"
)

// ── 复杂度评估（M2）──

// Complexity 复杂度分级：simple=现状单轮路径；complex=触发多轮机制。
type Complexity string

const (
	ComplexitySimple  Complexity = "simple"  // 现状单轮路径
	ComplexityComplex Complexity = "complex" // 触发多轮机制
)

// Signals 评估输入（全部确定性信号）：TaskText 任务文本 / Mode chat|task|goal /
// FuzzyHits DetectFuzziness 命中数（0-3；spec.go:167）/ ImageN 图片数 / Explicit 显式标记（直通）。
type Signals struct {
	TaskText  string // 任务文本（长度与多目标词判定）
	Mode      string // chat / task / goal
	FuzzyHits int    // 模糊度命中数（由 runtime 层复用 goalmgr.DetectFuzziness 提供）
	ImageN    int    // 图片数量
	Explicit  string // 显式标记：complex/simple（大小写不敏感）
}

// Assessor 复杂度评估器（确定性、可解释、阈值可配；默认 60）。
type Assessor struct {
	Threshold int // 分级阈值（score >= Threshold → complex）
}

// NewAssessor 创建评估器；threshold<=0 时取默认 60。
func NewAssessor(threshold int) *Assessor {
	if threshold <= 0 {
		threshold = 60
	}
	return &Assessor{Threshold: threshold}
}

// multiGoalRe 多目标词表（词表集中为常量，便于校准）。
var multiGoalRe = regexp.MustCompile(`(同时|分别|并行|并且|以及|多个|批量)`)

// Score 复杂度评分（0-100；评分表见设计文档 §2.2；Explicit 直通 100/0）。
func (a *Assessor) Score(s Signals) int {
	switch strings.ToLower(strings.TrimSpace(s.Explicit)) {
	case "complex":
		return 100
	case "simple":
		return 0
	}
	score := 0
	if n := len([]rune(s.TaskText)); n >= 120 {
		score += 20
		if n >= 400 {
			score += 20
		}
	}
	if s.FuzzyHits > 0 {
		score += 10 * min(s.FuzzyHits, 3)
	}
	if s.Mode == "goal" {
		score += 10
	}
	if s.Mode == "task" && multiGoalRe.MatchString(s.TaskText) {
		score += 10
	}
	if s.ImageN > 0 {
		score += 10
	}
	if score > 100 {
		score = 100
	}
	return score
}

// Assess 分级：score >= Threshold → complex（否则 simple）。
func (a *Assessor) Assess(s Signals) Complexity {
	if a.Score(s) >= a.Threshold {
		return ComplexityComplex
	}
	return ComplexitySimple
}
