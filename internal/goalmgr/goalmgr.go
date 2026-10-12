// Package goalmgr 实现 Goal 模式（持久目标循环）的状态机与持久化。
//
// 借鉴业界同类 Agent 的持久目标循环结构：
//   - 持久目标循环：规划 → 执行 → 检查 → 下一轮（跨轮状态由 GoalSession 承载）
//   - 预算限制：轮次上限 MaxRounds + 累计 token 预算 TokenBudget
//   - continuation：每轮结束后生成"上轮总结 + 下一步指令"，注入下一轮提示词
//   - 检查点：每轮结束写入 Checkpoint，崩溃/中断后可恢复续跑
//
// 完成判定（ApplyRoundResult）：
//  1. 模型输出 [GOAL_DONE] 标记 → done
//  2. 所有步骤 done 且 Overseer 通过 → done
//  3. 轮次 >= MaxRounds → 软提示继续（不强制失败，仅注入收敛提示）
//  4. TokenUsed >= TokenBudget（>0 时）→ failed（超出 token 预算）
//  5. 否则生成 continuation → 继续下一轮
package goalmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

const (
	// DefaultMaxRounds 默认 0 = 轮次无上限：目标一直执行直到
	// [GOAL_DONE] / token 预算 / 用户手动中断，不做轮次中止。
	// 显式配置 MaxRounds>0 时仅注入中性进度提示，仍不强制终止。
	DefaultMaxRounds = 0
	// DefaultTokenMultiplier 默认 token 预算 = MaxContext × 该倍数（0 = 不限制）。
	DefaultTokenMultiplier = 2
)

// Manager 管理各对话会话的目标状态机。
//
// 隔离设计：Goal 状态机按【对话会话】隔离（bySID[sessionID]），而不是全局单例。
// 每个会话各自持有独立的活动目标：
//   - 新建/切换会话（runtime.NewSession/LoadSession 调用 SetCurrent）→ 目标绑定随之切换
//   - 会话 B 发消息时，即使会话 A 有活动目标，也只会看到自己的目标（或自动新建），互不串扰
//   - 切回会话 A → SetCurrent(A) → 恢复 A 自己的目标，继续推进，不丢进度
//
// 向后兼容：current 默认为 ""（TUI 等未显式设置会话 ID 的场景），行为与旧版单例一致。
type Manager struct {
	mu      sync.Mutex
	current string                        // 当前绑定的会话 ID（"" = 默认/未绑定）
	bySID   map[string]*types.GoalSession // 每个会话各自的当前目标
	dir     string                        // .contextloom/goals
	// polishVerifier 打磨轮外部验收（nil = 关闭，旧行为）。
	// 对照 why-self-verification 一文：验收必须走外部通道，
	// 模型自答"打磨完成"不算数。runtime 接线时注入 go test ./... + 静态核查。
	polishVerifier func(s *types.GoalSession) (bool, []string)
	// evidenceVerifier 步骤证据链核验（nil = 关闭，旧行为）。
	// runtime 接线时注入 goalmgr.VerifyGoalProofs + DefaultProofIO：
	// done 步骤的 proofs 未全部通过 → 降级 pending + 追加缺陷。
	evidenceVerifier func(steps []types.GoalStep) []ProofFailure
}

// New 创建目标管理器。dir 为持久化目录（项目级 .contextloom/goals）。
func New(dir string) *Manager {
	return &Manager{dir: dir, bySID: map[string]*types.GoalSession{}}
}

// SetCurrent 切换当前绑定的对话会话（runtime.NewSession/LoadSession 时调用）。
// 目标状态随之切换：每个会话各自的活动目标互不干扰。
func (m *Manager) SetCurrent(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.current = sessionID
}

// Start 启动新目标会话（绑定到当前会话）。maxRounds<=0 用默认值；tokenBudget<=0 不限制。
func (m *Manager) Start(goal string, maxRounds int, tokenBudget int64) *types.GoalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxRounds <= 0 {
		maxRounds = DefaultMaxRounds
	}
	now := time.Now()
	s := &types.GoalSession{
		ID:          fmt.Sprintf("goal-%d", now.UnixNano()),
		SessionID:   m.current,
		Goal:        goal,
		Phase:       types.GoalPhasePlan,
		Round:       0,
		MaxRounds:   maxRounds,
		TokenBudget: tokenBudget,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	m.bySID[m.current] = s
	_ = m.persistLocked(s)
	return s
}

// Session 返回当前会话的活动目标（可能为 nil）。
func (m *Manager) Session() *types.GoalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bySID[m.current]
}

// SessionFor 返回指定会话的活动目标（可能为 nil）。
func (m *Manager) SessionFor(sessionID string) *types.GoalSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bySID[sessionID]
}

// Reset 清空当前会话的活动目标（mode 切回 chat/task 时调用；该会话目标保留在磁盘可 /goal resume）。
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bySID, m.current)
}

// ResetFor 清空指定会话的活动目标。
func (m *Manager) ResetFor(sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bySID, sessionID)
}

// Abort 中止当前目标循环（保留会话与检查点，可审计）。
func (m *Manager) Abort() {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.bySID[m.current]
	if s == nil {
		return
	}
	s.Aborted = true
	s.UpdatedAt = time.Now()
	_ = m.persistLocked(s)
}

// SetPlan 记录 plan 轮提取的步骤清单。
func (m *Manager) SetPlan(steps []types.GoalStep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.bySID[m.current]
	if s == nil {
		return
	}
	s.Steps = steps
	if len(steps) > 0 {
		s.Phase = types.GoalPhaseExecute
	}
	s.UpdatedAt = time.Now()
	_ = m.persistLocked(s)
}

// SetSpec 受理 spec 阶段的细化产物（[GOAL_SPEC]）。
// 经 SpecGate 确定性门槛判定：
//   - 通过 → SpecReady=true，Phase 推进到 plan（进入规划/编码）；
//   - 不通过 → Spec 保留（供下一轮对照细化），SpecRounds++，
//     并追加一条 open 缺陷（d-spec），返回拒绝原因列表。
//
// spec 为 nil（模型未输出 [GOAL_SPEC]）→ 直接通过（兼容旧路径：
// 不输出 spec 的模型不受影响，行为与旧版一致）。
func (m *Manager) SetSpec(spec *types.GoalSpec) (bool, []string) {
	if spec == nil {
		return true, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.bySID[m.current]
	if s == nil {
		return true, nil
	}
	ok, issues := (SpecGate{}).Check(spec)
	s.Spec = spec
	s.SpecRounds++
	s.UpdatedAt = time.Now()
	if ok {
		s.SpecReady = true
		// 划掉 d-spec 缺陷（细化已通过门槛）
		for i := range s.Defects {
			if s.Defects[i].ID == "d-spec" {
				s.Defects[i].Status = "fixed"
				s.Defects[i].FixedRound = s.Round + 1
				s.Defects[i].Evidence = "SpecGate 通过"
			}
		}
		if s.Phase == types.GoalPhasePlan || s.Phase == "" {
			s.Phase = types.GoalPhasePlan
		}
		_ = m.persistLocked(s)
		return true, nil
	}
	var reasons []string
	for _, it := range issues {
		reasons = append(reasons, it.String())
	}
	// 缺陷去重：同 spec 阶段的拒绝合并为一条（多次细化累计原因太长）
	merged := strings.Join(reasons, "；")
	for i := range s.Defects {
		if s.Defects[i].ID == "d-spec" {
			s.Defects[i].Status = "open"
			s.Defects[i].Desc = "需求细化未通过确定性门槛：" + merged
			s.UpdatedAt = time.Now()
			_ = m.persistLocked(s)
			return false, reasons
		}
	}
	s.AddDefect("d-spec", "需求细化未通过确定性门槛："+merged)
	_ = m.persistLocked(s)
	return false, reasons
}

// SetPolishVerifier 注入打磨轮外部验收函数（nil = 关闭，旧行为）。
// 判定函数返回 (通过, 失败原因列表)；不通过时 [GOAL_POLISH_DONE] 被拒绝，
// 追加缺陷 d-polish 并继续下一轮（对照 why-self-verification 一文：
// 验收必须走外部通道，模型自答"打磨完成"不算数）。
func (m *Manager) SetPolishVerifier(fn func(s *types.GoalSession) (bool, []string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.polishVerifier = fn
}

// SetEvidenceVerifier 注入步骤证据链核验函数（nil = 关闭，旧行为）。
// done 步骤携带 proofs 时由核验器做确定性验证（file/diff/test 三类物理通道），
// 失败 → 步骤降级 pending 并追加缺陷 d-evidence-<sid>。
func (m *Manager) SetEvidenceVerifier(fn func(steps []types.GoalStep) []ProofFailure) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evidenceVerifier = fn
}

// ApplyRoundResult 每轮结束调用：更新状态机并返回是否应继续下一轮。
// （兼容旧签名：不含反思/缺陷/经验等扩展数据，等价于 ApplyRoundResultEx 传空扩展。）
//
// 参数：
//   - summary: 本轮响应摘要（模型正文）
//   - passed: Overseer 是否通过
//   - score: Overseer 加权总分
//   - tokens: 本轮估算消耗 token（<=0 时按响应长度估算）
//   - steps: 本轮模型回报的步骤状态（可能为 nil，表示未变更）
//
// 返回 (shouldContinue, reason)。reason 描述继续/停止原因，可作为
// continuation 的组成部分由调用方展示。
func (m *Manager) ApplyRoundResult(summary string, passed bool, score float64, tokens int64, steps []types.GoalStep) (bool, string) {
	return m.ApplyRoundResultEx(summary, passed, score, tokens, steps, types.GoalReflection{}, nil, nil, nil)
}

// ApplyRoundResultEx 每轮结束调用（v2 自进化版）。
// 除旧签名参数外，扩展：
//   - refl: 本轮反思（[GOAL_REFLECT] 解析结果，机制①）
//   - autoDefects: 监工低分维度自动生成的缺陷条目（机制②，未通过时非空）
//   - fixReports: 模型回报的缺陷修复状态（[GOAL_DEFECTS] 解析结果，机制②）
//   - lessons: 模型沉淀的经验教训（[GOAL_LESSON] 解析结果，机制⑥）
func (m *Manager) ApplyRoundResultEx(summary string, passed bool, score float64, tokens int64, steps []types.GoalStep, refl types.GoalReflection, autoDefects []types.GoalDefect, fixReports []types.GoalDefect, lessons []string) (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.bySID[m.current]
	if s == nil {
		return false, "无活动目标"
	}
	s.Round++
	s.UpdatedAt = time.Now()

	// token 消耗累计（未提供时按摘要估算）
	if tokens <= 0 {
		tokens = int64(len([]rune(summary)) / 2) // 粗略：CJK 约 1 token/字，折半保守
	}
	s.TokenUsed += tokens

	// ④ 收敛检测：记录本轮前的进展签名（步骤状态集 + 分数 5 分桶）
	prevScore := 0.0
	if n := len(s.Checkpoints); n > 0 {
		prevScore = s.Checkpoints[n-1].Score
	}
	prevSig := progressSignature(s.Steps, prevScore)

	// ① 反思记录（机制①：每轮必反思）
	if refl.Summary != "" || refl.Problems != "" || refl.NextPlan != "" {
		refl.Round = s.Round
		refl.At = time.Now()
		s.AddReflection(refl)
	}
	// ⑥ 经验档案（机制⑥：跨轮经验沉淀）
	for _, l := range lessons {
		s.AddLesson(l)
	}

	// 步骤状态更新：模型回报的步骤覆盖同名步骤
	if len(steps) > 0 {
		byID := map[string]types.GoalStep{}
		for _, st := range s.Steps {
			byID[st.ID] = st
		}
		for _, st := range steps {
			if st.ID != "" {
				byID[st.ID] = st
			}
		}
		// 保持原始顺序
		merged := make([]types.GoalStep, 0, len(byID))
		for _, st := range s.Steps {
			if v, ok := byID[st.ID]; ok {
				merged = append(merged, v)
				delete(byID, st.ID)
			}
		}
		for _, st := range byID { // 新增步骤（模型自主补充）
			merged = append(merged, st)
		}
		s.Steps = merged
	}

	// ③+ 证据链核验（对照 why-self-verification 一文：验收走外部物理通道）。
	// done 步骤携带 proofs 时由注入的核验器做确定性验证（file/diff/test），
	// 失败 → 步骤降级 pending（evidence 保留供参考）并追加缺陷条目。
	if m.evidenceVerifier != nil && len(s.Steps) > 0 {
		failures := m.evidenceVerifier(s.Steps)
		if len(failures) > 0 {
			failedIDs := map[string]bool{}
			for _, f := range failures {
				failedIDs[f.StepID] = true
			}
			for i := range s.Steps {
				if failedIDs[s.Steps[i].ID] && s.Steps[i].Status == string(types.GoalStepDone) {
					s.Steps[i].Status = string(types.GoalStepPending)
				}
			}
			for _, f := range failures {
				s.AddDefect("d-evidence-"+f.StepID,
					fmt.Sprintf("证据核验未通过（步骤 %s，%s 证据 %s）：%s", f.StepID, f.Kind, f.Target, f.Reason))
			}
		}
	}

	// ② 缺陷清单（机制②：返工有清单）
	if !passed {
		for _, d := range autoDefects {
			s.AddDefect(d.ID, d.Desc)
		}
		if len(autoDefects) == 0 {
			s.AddDefect("d-overseer", fmt.Sprintf("监工评估未通过（%.0f/100），请对照验收标准复查产出质量（测试/完整性/副作用）并修复", score))
		}
	}
	// 模型回报的缺陷修复状态
	for _, d := range fixReports {
		for i := range s.Defects {
			if s.Defects[i].ID == d.ID {
				s.Defects[i].Status = d.Status
				if d.Status == "fixed" {
					s.Defects[i].FixedRound = s.Round
					if d.Evidence != "" {
						s.Defects[i].Evidence = truncateRunes(d.Evidence, 200)
					}
				}
				break
			}
		}
	}

	// ④ 收敛检测（机制④：无进展换策略；打磨轮期间不参与，避免误报）
	if s.Phase != types.GoalPhaseCheck {
		newSig := progressSignature(s.Steps, score)
		if newSig != prevSig {
			s.StalledRounds = 0
			s.LastProgressRnd = s.Round
		} else {
			s.StalledRounds++
		}
	}

	// 写入检查点
	cp := types.GoalCheckpoint{
		Round:   s.Round,
		Phase:   string(s.Phase),
		Summary: truncateRunes(summary, 200),
		Score:   score,
		Passed:  passed,
		Steps:   append([]types.GoalStep(nil), s.Steps...),
		At:      time.Now(),
	}
	s.Checkpoints = append(s.Checkpoints, cp)
	if len(s.Checkpoints) > 20 { // 检查点上限，防无限膨胀
		s.Checkpoints = s.Checkpoints[len(s.Checkpoints)-20:]
	}

	// ── 完成判定（v2：先打磨再完成）──
	// 0. 打磨轮完成标记 → 外部验收（证据链硬化）→ 真正完成。
	//    对照 why-self-verification 一文：模型自答"打磨完成"不算数，
	//    验收必须走外部物理通道（polishVerifier：go test/静态核查）。
	//    未注入 verifier 时按旧行为（标记即完成，向后兼容）。
	if strings.Contains(summary, "[GOAL_POLISH_DONE]") {
		if m.polishVerifier != nil {
			ok, reasons := m.polishVerifier(s)
			if ok {
				s.Phase = types.GoalPhaseDone
				s.Polished = true
				_ = m.persistLocked(s)
				return false, fmt.Sprintf("目标完成（打磨轮外部验收通过，共 %d 轮）", s.Round)
			}
			// 外部验收不通过：拒绝标记，追加缺陷，继续下一轮修复
			s.AddDefect("d-polish", "打磨轮外部验收未通过："+strings.Join(reasons, "；"))
			s.Phase = types.GoalPhaseCheck
			s.Continuation = buildPolishContinuation(s) +
				"\n【外部验收失败】" + strings.Join(reasons, "；") +
				"\n请修复后再次输出 [GOAL_POLISH_DONE]（自答不算数，必须让外部通道通过）。"
			_ = m.persistLocked(s)
			return true, fmt.Sprintf("打磨轮外部验收未通过（%s），继续修复", strings.Join(reasons, "；"))
		}
		s.Phase = types.GoalPhaseDone
		s.Polished = true
		_ = m.persistLocked(s)
		return false, fmt.Sprintf("目标完成（打磨轮通过，共 %d 轮）", s.Round)
	}
	// 1. 模型显式声明完成 → 首次进入打磨轮，不直接结束
	if strings.Contains(summary, "[GOAL_DONE]") && !s.Polished {
		s.Phase = types.GoalPhaseCheck
		s.Continuation = buildPolishContinuation(s)
		_ = m.persistLocked(s)
		return true, "目标步骤已全部完成，进入打磨轮（质量加固后收尾）"
	}
	// 1b. 打磨轮期间再次声明完成（未输出 POLISH_DONE）→ 防死循环兜底，视为完成
	if s.Phase == types.GoalPhaseCheck && strings.Contains(summary, "[GOAL_DONE]") {
		s.Phase = types.GoalPhaseDone
		_ = m.persistLocked(s)
		return false, "目标完成（打磨轮确认）"
	}
	// 2. 全部步骤有效 done（evidence 非空，DoD）+ 监工通过 → 首次进入打磨轮
	if s.AllStepsValidDone() && passed && !s.Polished {
		s.Phase = types.GoalPhaseCheck
		s.Continuation = buildPolishContinuation(s)
		_ = m.persistLocked(s)
		return true, fmt.Sprintf("步骤 %d/%d 全部有效完成，进入打磨轮（质量加固后收尾）", s.DoneSteps(), len(s.Steps))
	}
	// 2b. 打磨轮结束 + 全部步骤有效 done + 通过 → 完成
	if s.AllStepsValidDone() && passed && s.Polished {
		s.Phase = types.GoalPhaseDone
		_ = m.persistLocked(s)
		return false, fmt.Sprintf("目标完成（%d/%d 步骤 done，监工通过 %.0f 分，打磨完成）", s.DoneSteps(), len(s.Steps), score)
	}
	// 3. 轮次提示（不强制失败、不中止）：MaxRounds<=0 表示无上限（默认），
	//    目标一直执行直到 [GOAL_DONE] / token 预算 / 用户手动中断。
	//    仅当显式配置了 MaxRounds>0 时，超轮后注入中性进度提示（不出现
	//    "上限/停止/尽快"等收尾引导词，避免模型误判而提前中止）。
	if s.MaxRounds > 0 && s.Round >= s.MaxRounds {
		s.Phase = types.GoalPhaseExecute
		s.Continuation = buildContinuation(s, summary+"\n【轮次提示】已执行 "+fmt.Sprint(s.Round)+" 轮，当前进度正常推进；若已完成目标请输出 [GOAL_DONE] 结束，否则可继续执行剩余步骤。", passed, score)
		_ = m.persistLocked(s)
		return true, fmt.Sprintf("第 %d 轮评估 %.0f 分（继续执行）", s.Round, score)
	}
	// 4. token 预算
	if s.TokenBudget > 0 && s.TokenUsed >= s.TokenBudget {
		s.Phase = types.GoalPhaseFailed
		_ = m.persistLocked(s)
		return false, fmt.Sprintf("超出 token 预算 %d（已用 %d），目标未完成", s.TokenBudget, s.TokenUsed)
	}

	// 5. 生成 continuation → 继续下一轮
	s.Phase = types.GoalPhaseExecute
	s.Continuation = buildContinuation(s, summary, passed, score)
	_ = m.persistLocked(s)
	return true, fmt.Sprintf("第 %d 轮评估 %.0f 分%s，剩余 %d 步，自动进入下一轮", s.Round, score, passNote(passed), len(s.PendingSteps()))
}

// progressSignature 生成进展签名：步骤 (id:status) 序列 + 分数 5 分桶。
// 签名变化 = 实质进展；不变 = 原地打转（收敛检测依据，机制④）。
func progressSignature(steps []types.GoalStep, score float64) string {
	var b strings.Builder
	for _, st := range steps {
		b.WriteString(st.ID)
		b.WriteString(":")
		b.WriteString(st.Status)
		b.WriteString(";")
	}
	b.WriteString(fmt.Sprintf("score=%.0f", score/5*5))
	return b.String()
}

// Persist 手动持久化（恢复后修改等场景）。
func (m *Manager) Persist() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.bySID[m.current]; s != nil {
		_ = m.persistLocked(s)
	}
}

func (m *Manager) persistLocked(s *types.GoalSession) error {
	if s == nil || m.dir == "" {
		return nil
	}
	if err := os.MkdirAll(m.dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(m.dir, s.ID+".json"), data, 0644)
}

// RestoreFor 从磁盘恢复指定会话最近的活动目标（backend 重启后自动挂回内存）。
// 规则：归属（SessionID）匹配该会话、未完成（!Done）的目标中按 UpdatedAt 取最新。
// 返回恢复的目标；无匹配返回 nil。用于 runtime.LoadSession 挂载，修复
// "重启后 bySID 全空 → 发消息静默新建目标、旧目标失联"的缺陷。
func (m *Manager) RestoreFor(sessionID string) *types.GoalSession {
	if m.dir == "" || sessionID == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var best *types.GoalSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var s types.GoalSession
		if json.Unmarshal(data, &s) != nil {
			continue
		}
		if s.SessionID != sessionID || s.Done() {
			continue
		}
		if best == nil || s.UpdatedAt.After(best.UpdatedAt) {
			cp := s
			best = &cp
		}
	}
	if best != nil {
		m.bySID[sessionID] = best
	}
	return best
}

// Load 从磁盘加载指定目标会话并设为当前会话的活动目标（用于 /goal resume）。
// 归属校验：目标已归属【其他】会话 → 拒绝加载并报错（防止跨会话改绑造成
// "目标串会话"）；SessionID 为空（旧数据/未绑定）→ 兼容绑定到当前会话。
func (m *Manager) Load(id string) (*types.GoalSession, error) {
	if m.dir == "" {
		return nil, fmt.Errorf("目标目录未配置")
	}
	data, err := os.ReadFile(filepath.Join(m.dir, id+".json"))
	if err != nil {
		return nil, fmt.Errorf("加载目标会话失败: %w", err)
	}
	var s types.GoalSession
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("解析目标会话失败: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.SessionID != "" && s.SessionID != m.current {
		return nil, fmt.Errorf("目标 %q 属于会话 %s，无法在会话 %s 恢复（请先切回原会话再 /goal resume）",
			truncateGoal(s.Goal), s.SessionID, m.current)
	}
	s.SessionID = m.current // 同会话 resume 或旧数据（无归属）→ 归一绑定
	m.bySID[m.current] = &s
	return &s, nil
}

// truncateGoal 截断目标文本用于错误提示（避免超长目标刷屏）。
func truncateGoal(g string) string {
	if len(g) <= 60 {
		return g
	}
	return g[:57] + "..."
}

// List 列出已持久化的目标会话（按更新时间倒序）。
func (m *Manager) List() []types.GoalSession {
	if m.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil
	}
	var out []types.GoalSession
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(m.dir, e.Name()))
		if err != nil {
			continue
		}
		var s types.GoalSession
		if json.Unmarshal(data, &s) == nil {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// buildContinuation 生成下一轮指令：上轮结论 + 经验档案 + 反思 + 缺陷清单 +
// 收敛告警 + 未完成步骤（含验收标准）。summary 非空时追加为"上轮执行摘要"。
func buildContinuation(s *types.GoalSession, summary string, passed bool, score float64) string {
	var b strings.Builder
	b.WriteString("【目标循环自动续跑】上一轮（第 ")
	b.WriteString(fmt.Sprintf("%d", s.Round))
	b.WriteString(" 轮）已结束，评估 ")
	b.WriteString(fmt.Sprintf("%.0f", score))
	b.WriteString("/100 ")
	if passed {
		b.WriteString("（通过）")
	} else {
		b.WriteString("（未通过，需改进后重试）")
	}
	b.WriteString("。")
	if strings.TrimSpace(summary) != "" {
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(summary))
	}
	// ⑥ 经验档案（机制⑥：开局先读，同一目标内不重复犯错）
	if len(s.Lessons) > 0 {
		b.WriteString("\n【经验档案】本目标已沉淀的教训（勿重犯）：\n")
		for i, l := range s.Lessons {
			b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, l))
		}
	}
	// ① 本轮反思摘要（机制①：最新一条，供下一轮参考）
	if len(s.Reflections) > 0 {
		r := s.Reflections[0]
		b.WriteString("\n【上轮反思】")
		if r.Summary != "" {
			b.WriteString("做了什么: " + truncateRunes(r.Summary, 120) + "；")
		}
		if r.Problems != "" {
			b.WriteString("发现问题: " + truncateRunes(r.Problems, 120) + "；")
		}
		if r.NextPlan != "" {
			b.WriteString("下一步: " + truncateRunes(r.NextPlan, 120))
		}
		b.WriteString("\n")
	}
	// ② 缺陷清单（机制②：返工有清单，逐条修复划掉）
	open := s.OpenDefects()
	if len(open) > 0 {
		b.WriteString(fmt.Sprintf("【缺陷清单】%d 项待修复（逐条修复后在 [GOAL_DEFECTS] 回报 fixed，全部 fixed 才能完成）：\n", len(open)))
		for i, d := range open {
			b.WriteString(fmt.Sprintf("  %d. [%s] %s\n", i+1, d.ID, d.Desc))
		}
	}
	// ③+ spec 未收敛（需求细化连续多轮未过门槛）：注入用户澄清兜底
	// （防"细化-拒绝"死循环；关键信息缺失时模型必须向用户提出澄清问题）。
	if s.Spec != nil && !s.SpecReady && s.SpecRounds >= MaxSpecRounds {
		b.WriteString("\n【需求细化未收敛】已连续 " + fmt.Sprint(s.SpecRounds) +
			" 轮细化未通过确定性门槛。若关键信息确实缺失且无法合理假设：\n" +
			"  1) 把需要用户澄清的问题写入 open_questions 并给出兜底假设；\n" +
			"  2) 在本轮正文中直接向用户提出澄清问题，等待回复后再继续。\n")
	}
	// ④ 收敛告警（机制④：无进展强制换策略）
	if s.StalledRounds >= 2 {
		b.WriteString(fmt.Sprintf("【无进展告警】已连续 %d 轮无实质进展，禁止再用老办法硬磨，必须更换策略：\n  1) 拆子任务并行（spawn_task，仅限有可验证产出的步骤）\n  2) 用 harness_search 检索项目资料/历史方案\n  3) 若需求不明确，先向用户请求澄清再继续\n", s.StalledRounds))
	}
	b.WriteString("请不要重复已完成的工作，聚焦以下未完成步骤：\n")
	pending := s.PendingSteps()
	if specPending := s.Spec != nil && !s.SpecReady; specPending {
		b.WriteString("- 需求细化尚未通过确定性门槛：请对照上方 d-spec 缺陷原因修正后重新输出 [GOAL_SPEC]\n")
	} else if len(pending) == 0 {
		b.WriteString("- 全部步骤已 done，但监工未通过：请逐条修复【缺陷清单】中的缺陷，并在 [GOAL_DEFECTS] 中回报修复状态\n")
	} else {
		for i, st := range pending {
			b.WriteString(fmt.Sprintf("  %d. [%s] %s（当前: %s）", i+1, st.ID, st.Desc, st.Status))
			if st.Acceptance != "" {
				b.WriteString(fmt.Sprintf(" 验收: %s", truncateRunes(st.Acceptance, 80)))
			}
			b.WriteString("\n")
		}
	}
	// spec 已过但尚无步骤清单 → 下一轮请产出计划
	if s.SpecReady && len(s.Steps) == 0 {
		b.WriteString("- 需求已细化，请输出 [GOAL_PLAN] 声明步骤清单（含每个步骤的验收标准）\n")
	}
	b.WriteString("每轮结束必须输出 [GOAL_REFLECT]<JSON {summary,problems,next_plan}> 反思本轮；修复的缺陷在 [GOAL_DEFECTS]<JSON [{id,status,evidence}]> 中回报。")
	b.WriteString("完成全部步骤后，请输出 [GOAL_DONE] 标记声明目标达成。")
	return b.String()
}

// buildPolishContinuation 生成打磨轮指令（机制⑤：完成后再打磨一轮）。
// 只做质量加固检查，发现问题直接修复；打磨完成后输出 [GOAL_POLISH_DONE]。
func buildPolishContinuation(s *types.GoalSession) string {
	return fmt.Sprintf("【打磨轮】目标步骤已全部完成（第 %d 轮）。在最终收尾前，请执行一轮质量加固——只做以下检查，发现问题直接修复：\n  1) 边界情况与异常路径是否处理\n  2) 测试是否覆盖核心路径（缺则补）\n  3) 死代码/临时文件/调试残留是否清理\n  4) 文档（README/注释）是否同步\n  5) 检查副作用与资源泄漏\n打磨完成后输出 [GOAL_POLISH_DONE] 声明打磨结束；如发现问题并修复，在 [GOAL_STEPS] 中更新证据。", s.Round)
}

func passNote(passed bool) string {
	if passed {
		return ""
	}
	return "· 未通过"
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}
