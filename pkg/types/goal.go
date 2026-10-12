package types

import "time"

// ── 目标循环（Goal Mode）类型 ──
// 借鉴业界同类 Agent 的持久目标循环设计 的持久目标循环结构：
// 目标 + 分阶段(plan/execute/check) + 轮次预算 + 检查点 + continuation 续跑。
// Goal 模式与 task 模式的区别：
//   - task：一次性执行，AI 自主拆子任务并行，执行完即结束
//   - goal：跨轮持久循环，每轮结束 Overseer 评估，未达成自动进入下一轮，
//     直到目标完成 / 达到轮次上限 / 超出 token 预算
//
// v2 自进化扩展（6 大机制）：
//   ① 每轮反思（Reflections）：轮末自我评价，下一轮开局先读
//   ② 缺陷清单（Defects）：监工低分项自动转缺陷条目，逐条修复划掉
//   ③ 验收标准（GoalStep.Acceptance）：步骤 done 需 evidence 非空 + acceptance 对照
//   ④ 收敛检测（StalledRounds）：连续无进展强制换策略
//   ⑤ 打磨轮（Polished）：完成后追加一轮质量加固
//   ⑥ 经验档案（Lessons）：跨轮经验沉淀，同一目标内不重复犯错

// GoalPhase 目标循环阶段。
type GoalPhase string

const (
	GoalPhaseSpec    GoalPhase = "spec"    // 需求细化：模糊输入先产出 [GOAL_SPEC]，经确定性门槛通过后才进入规划
	GoalPhasePlan    GoalPhase = "plan"    // 规划：产出步骤清单
	GoalPhaseExecute GoalPhase = "execute" // 执行：推进步骤
	GoalPhaseCheck   GoalPhase = "check"   // 检查：验证完成度
	GoalPhaseDone    GoalPhase = "done"    // 完成
	GoalPhaseFailed  GoalPhase = "failed"  // 失败（超轮次/超预算/中止）
)

// GoalStepStatus 步骤状态。
type GoalStepStatus string

const (
	GoalStepPending GoalStepStatus = "pending"
	GoalStepRunning GoalStepStatus = "running"
	GoalStepDone    GoalStepStatus = "done"
	GoalStepFailed  GoalStepStatus = "failed"
)

// GoalStep 目标步骤（plan 轮提取，后续轮推进状态）。
type GoalStep struct {
	ID         string `json:"id"`
	Desc       string `json:"desc"`
	Acceptance string `json:"acceptance,omitempty"` // 验收标准（DoD）：本步骤完成的硬指标
	Status     string `json:"status"`               // pending/running/done/failed
	Evidence   string `json:"evidence,omitempty"`   // 完成证据（文件/测试结果摘要）
	// Proofs 物理证据（证据链机制）：done 时可选附带的确定性可复核证据。
	// 非空时经 VerifyGoalProofs 核验，全部通过才计为"有效 done"。
	// 为空时按旧逻辑（evidence 非空即可）——向后兼容。
	Proofs []GoalProof `json:"proofs,omitempty"`
}

// GoalProof 步骤完成的物理证据（证据链机制，对照 why-self-verification 一文
// §5.3「验收必须全量且外部」）。只认三类确定性通道，模型口述不算证据。
type GoalProof struct {
	Kind   string `json:"kind"`           // file / diff / test
	Target string `json:"target"`         // 证据目标：file=相对路径；diff=相对路径；test=包路径（如 ./internal/x）
	Note   string `json:"note,omitempty"` // 补充说明（可读性，不参与判定）
}

// GoalSpec 需求细化产物（[GOAL_SPEC] 协议解析结果，spec 阶段）。
// 对照 why-self-verification 一文的对策：把模型对模糊输入的隐性假设
// 显式化、合同化，经确定性 SpecGate 门槛通过后才进入规划/编码。
type GoalSpec struct {
	GoalRefined   string   `json:"goal_refined"`             // 细化后的目标（须引用可解析的具体目标物）
	Assumptions   []string `json:"assumptions"`              // 显式假设（必须非空）
	Acceptance    []string `json:"acceptance,omitempty"`     // 可测量验收标准（至少一条含可测量谓词）
	Scope         []string `json:"scope,omitempty"`          // 范围内/范围外
	Risks         []string `json:"risks,omitempty"`          // 风险与缓解
	OpenQuestions []string `json:"open_questions,omitempty"` // 开放问题（必须与假设配对兜底）
}

// GoalReflection 每轮反思（机制①：自我评价）。
type GoalReflection struct {
	Round    int       `json:"round"`
	Summary  string    `json:"summary"`   // 本轮做了什么
	Problems string    `json:"problems"`  // 发现什么问题
	NextPlan string    `json:"next_plan"` // 下一步怎么改
	At       time.Time `json:"at"`
}

// GoalDefect 缺陷清单条目（机制②：返工有清单）。
type GoalDefect struct {
	ID         string `json:"id"`
	Desc       string `json:"desc"`
	Status     string `json:"status"`                // open/fixed
	FixedRound int    `json:"fixed_round,omitempty"` // 修复轮次
	Evidence   string `json:"evidence,omitempty"`    // 修复证据
}

// OpenDefects 未修复的缺陷列表。
func (s *GoalSession) OpenDefects() []GoalDefect {
	var out []GoalDefect
	for _, d := range s.Defects {
		if d.Status != "fixed" {
			out = append(out, d)
		}
	}
	return out
}

// GoalCheckpoint 每轮结束的检查点（可恢复、可审计）。
type GoalCheckpoint struct {
	Round   int        `json:"round"`
	Phase   string     `json:"phase"`
	Summary string     `json:"summary"` // 本轮产出摘要（前 200 字）
	Score   float64    `json:"score"`   // Overseer 加权总分
	Passed  bool       `json:"passed"`
	Steps   []GoalStep `json:"steps"` // 轮末步骤快照
	At      time.Time  `json:"at"`
}

// GoalSession 目标会话状态机（跨轮持久，JSON 落盘可恢复）。
type GoalSession struct {
	ID           string           `json:"id"`
	SessionID    string           `json:"session_id,omitempty"` // 所属对话会话 ID（隔离依据：不同会话各自持有独立目标）
	Goal         string           `json:"goal"`
	Phase        GoalPhase        `json:"phase"`
	Round        int              `json:"round"` // 已完成轮数
	MaxRounds    int              `json:"max_rounds"`
	Steps        []GoalStep       `json:"steps"`
	Checkpoints  []GoalCheckpoint `json:"checkpoints"`
	TokenUsed    int64            `json:"token_used"`             // 累计估算 token
	TokenBudget  int64            `json:"token_budget"`           // 0 = 不限制
	Continuation string           `json:"continuation,omitempty"` // 上轮总结 → 下轮指令
	Aborted      bool             `json:"aborted,omitempty"`
	// ── v2 自进化扩展 ──
	Reflections     []GoalReflection `json:"reflections,omitempty"`         // ① 每轮反思（上限 20）
	Defects         []GoalDefect     `json:"defects,omitempty"`             // ② 缺陷清单
	StalledRounds   int              `json:"stalled_rounds,omitempty"`      // ④ 连续无进展轮数
	LastProgressRnd int              `json:"last_progress_round,omitempty"` // ④ 最近实质进展轮次
	Polished        bool             `json:"polished,omitempty"`            // ⑤ 是否已完成打磨轮
	Lessons         []string         `json:"lessons,omitempty"`             // ⑥ 经验档案（上限 10）
	// ── v3 需求细化 + 证据链扩展 ──
	Spec            *GoalSpec `json:"spec,omitempty"`             // 细化需求（[GOAL_SPEC] 解析结果，spec 阶段产物）
	SpecReady       bool      `json:"spec_ready,omitempty"`       // SpecGate 门槛是否通过（false = 仍需细化）
	SpecRounds      int       `json:"spec_rounds,omitempty"`      // spec 阶段已细化轮数（连续失败计数，防死循环）
	RequireEvidence bool      `json:"require_evidence,omitempty"` // 证据链硬模式：done 步骤必须 proofs 全部通过（默认 false = 兼容旧逻辑）
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Done 目标是否已终止（完成/失败/中止）。
func (s *GoalSession) Done() bool {
	return s.Phase == GoalPhaseDone || s.Phase == GoalPhaseFailed || s.Aborted
}

// ActivePhase 当前轮的指令阶段（spec 未就绪优先；plan 轮为 PLAN，
// 后续为 EXECUTE/CHECK 交替推进）。
func (s *GoalSession) ActivePhase() GoalPhase {
	if s.Spec != nil && !s.SpecReady {
		return GoalPhaseSpec
	}
	if s.Phase == GoalPhasePlan || (s.Phase == "" && len(s.Steps) == 0) {
		return GoalPhasePlan
	}
	if s.Phase == GoalPhaseCheck {
		return GoalPhaseCheck
	}
	return GoalPhaseExecute
}

// PendingSteps 未完成的步骤列表。
func (s *GoalSession) PendingSteps() []GoalStep {
	var out []GoalStep
	for _, st := range s.Steps {
		if st.Status != string(GoalStepDone) {
			out = append(out, st)
		}
	}
	return out
}

// DoneSteps 已完成的步骤数。
func (s *GoalSession) DoneSteps() int {
	n := 0
	for _, st := range s.Steps {
		if st.Status == string(GoalStepDone) {
			n++
		}
	}
	return n
}

// AllStepsValidDone 全部步骤是否均为"有效 done"（done 且 evidence 非空，
// 机制③ DoD）。RequireEvidence=true（证据链硬模式）时还要求 proofs 非空，
// 且调用方需先经 VerifyGoalProofs 核验（本方法不触碰 IO，只做结构判断）。
func (s *GoalSession) AllStepsValidDone() bool {
	if len(s.Steps) == 0 {
		return false
	}
	for _, st := range s.Steps {
		if st.Status != string(GoalStepDone) || stringsTrimSpace(st.Evidence) == "" {
			return false
		}
		if s.RequireEvidence && len(st.Proofs) == 0 {
			return false
		}
	}
	return true
}

func stringsTrimSpace(s string) string {
	// 局部工具避免循环依赖：仅用于空判断
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}

// AddLesson 追加经验教训（机制⑥，上限 10，最新在前）。
func (s *GoalSession) AddLesson(lesson string) {
	lesson = stringsTrimSpace(lesson)
	if lesson == "" {
		return
	}
	for _, l := range s.Lessons {
		if l == lesson {
			return
		}
	}
	s.Lessons = append([]string{lesson}, s.Lessons...)
	if len(s.Lessons) > 10 {
		s.Lessons = s.Lessons[:10]
	}
}

// AddReflection 追加本轮反思（机制①，上限 20，最新在前）。
func (s *GoalSession) AddReflection(r GoalReflection) {
	s.Reflections = append([]GoalReflection{r}, s.Reflections...)
	if len(s.Reflections) > 20 {
		s.Reflections = s.Reflections[:20]
	}
}

// AddDefect 追加缺陷条目（机制②，同 desc 去重，open 优先置前）。
func (s *GoalSession) AddDefect(id, desc string) {
	desc = stringsTrimSpace(desc)
	if desc == "" {
		return
	}
	for i := range s.Defects {
		if s.Defects[i].Desc == desc {
			if s.Defects[i].Status != "fixed" {
				return // 已存在且未修复
			}
			s.Defects[i].Status = "open" // 曾修复过又复发：重新打开
			s.Defects[i].FixedRound = 0
			s.Defects[i].Evidence = ""
			return
		}
	}
	s.Defects = append(s.Defects, GoalDefect{ID: id, Desc: desc, Status: "open"})
}
