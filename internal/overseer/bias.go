// Package overseer —— 偏差检测器（BDL / Bias Detector Layer）。
//
// 对照 why-self-verification-is-unreliable.md 的第四层论证（自评失真）：
// 模型自答"全部检查过/测试通过/已完成"属于采样结论冒充全集结论
// （不在上下文 = 不存在）。BDL 是纯规则层（零模型依赖），把模型声明
// 与外部可复现事实（本轮是否真实调用过测试/差异工具）的差距显式化，
// 并把差距转化为监工评分的扣分项与缺陷条目。
//
// 设计原则（原文 §5.4 可迁移规则 1/5/6）：
//   - 无证据断言不拦截输出，但扣分 + 打标（显式化，而不是假装看不见）；
//   - 关键字匹配是探索不是证据：正文提到"测试通过"必须有 run_tests
//     工具证据支撑，否则视为 unverified_pass；
//   - grep 命中行 ≠ 完成证据：本层不做存在性判定，只做声明-证据对齐。
package overseer

import (
	"regexp"
	"strings"
)

// BiasKind 偏差类型。
type BiasKind string

const (
	// BiasNoEvidenceClaim 无证据的全量式声明："全面检查/全部通过/无遗漏/100%"
	// 且本轮没有 run_tests/git_diff 等外部工具证据。
	BiasNoEvidenceClaim BiasKind = "no_evidence_claim"
	// BiasPrematureDone 早产完成声明：[GOAL_DONE] 出现但需求细化未通过
	// （SpecReady=false）——没有合同化细节就宣布达成。
	BiasPrematureDone BiasKind = "premature_done"
	// BiasUnverifiedPass 自答式测试：正文声称"测试通过"，但本轮没有
	// run_tests 工具调用证据。
	BiasUnverifiedPass BiasKind = "unverified_pass"
	// BiasRushedAnswer 草率回答（M11 度量）：复杂任务总轮数 <2 且既无工具
	// 证据也无验证证据——"复杂问题一次思考解决"。默认扣 10。
	BiasRushedAnswer BiasKind = "rushed_answer"
)

// BiasHit 一条偏差命中记录。
type BiasHit struct {
	Kind     BiasKind // 偏差类型
	Desc     string   // 可读说明（注入缺陷条目）
	Penalty  float64  // 评分扣分（0-100 量纲）
	DefectID string   // 缺陷 ID（注入 [GOAL_DEFECTS]）
}

// ToolEvidence 本轮模型实际使用过的外部验证工具证据（由 runtime 从
// trace 记录构造；只认确定性通道，正文口述不算）。
type ToolEvidence struct {
	RanTests bool // 本轮调用过 run_tests / go test（外部测试通道）
	RanDiff  bool // 本轮调用过 git_diff / git diff（外部差异通道）
}

// 全量式声明模式：模型无法执行的"全面检查"类声明（原文：全量检查物理上不可能）。
var totalityClaimRe = regexp.MustCompile(`(?i)(全面检查|全部检查|全量检查|无遗漏|没有遗漏|百分之百|100%完成|彻底检查|全部通过|所有测试都通过|零缺陷|没有其它问题|无其他问题)`)

// 自答式测试模式：声称测试通过（必须有 run_tests 证据）。
var testClaimRe = regexp.MustCompile(`(?i)(测试通过|全部通过|PASS|go test.*(ok|通过)|测试全部|单测通过|所有测试)`)

// BiasAnalyze 对模型响应做确定性偏差检测。
// 参数：
//   - response: 模型本轮响应全文（含协议标记）；
//   - ev: 本轮外部工具证据（nil = 无证据；测试场景可直接传 &ToolEvidence{}）；
//   - specReady: 需求细化是否已通过门槛（goal 模式；非 goal 场景传 true）。
//
// 返回命中列表；空列表 = 无偏差。
func BiasAnalyze(response string, ev *ToolEvidence, specReady bool) []BiasHit {
	if ev == nil {
		ev = &ToolEvidence{}
	}
	// 剥离协议标记后再做文本模式匹配（协议正文不算"声明"）
	body := stripGoalProtocolMarkers(response)
	var hits []BiasHit

	// 1) 无证据的全量式声明
	if totalityClaimRe.MatchString(body) && !ev.RanTests && !ev.RanDiff {
		hits = append(hits, BiasHit{
			Kind:     BiasNoEvidenceClaim,
			Desc:     "无证据的全量式声明：正文声称「全面检查/无遗漏/全部通过」，但本轮没有 run_tests/git_diff 等外部验证证据。按 why-self-verification 一文：采样结论不能冒充全集结论，请改为列出实际检查过的文件/符号清单，或用外部通道验证。",
			Penalty:  20,
			DefectID: "d-bias-totality",
		})
	}

	// 2) 早产完成声明（goal 模式）
	if strings.Contains(response, "[GOAL_DONE]") && !specReady {
		hits = append(hits, BiasHit{
			Kind:     BiasPrematureDone,
			Desc:     "需求细化未通过确定性门槛就宣布 [GOAL_DONE]：必须先输出 [GOAL_SPEC] 通过 SpecGate（目标物指代/显式假设/可测量验收），再谈完成。",
			Penalty:  30,
			DefectID: "d-bias-premature-done",
		})
	}

	// 3) 自答式测试：声称测试通过但无工具证据
	if testClaimRe.MatchString(body) && !ev.RanTests {
		hits = append(hits, BiasHit{
			Kind:     BiasUnverifiedPass,
			Desc:     "自答式测试声明：正文声称「测试通过」，但本轮没有 run_tests 工具调用证据。测试结论必须来自确定性通道（go test 退出码），模型口述不算。",
			Penalty:  15,
			DefectID: "d-bias-unverified-test",
		})
	}

	return hits
}

// BiasRushed 草率回答判据（M11 度量，确定性）：
// complexity=="complex" 且总轮数 <2 且既无工具证据（toolCalls==0）也未送审
// （!verified）→ 命中一条 rushed_answer（-10）。其余一律不命中：simple 不误伤；
// 有工具证据或已送审不命中；轮数达标（>=2）不命中。
// 输入全部来自确定性记录（轮次计数/工具调用/验证事件），模型自报不参与。
// complexity 取值 "simple"/"complex"（与 thinkmgr.Complexity 字符串值对齐）；
// 用 string 传参避免 overseer 依赖 thinkmgr（依赖方向：thinkmgr 不 import overseer）。
func BiasRushed(rounds, toolCalls int, verified bool, complexity string) []BiasHit {
	if complexity != "complex" || rounds >= 2 || toolCalls > 0 || verified {
		return nil
	}
	return []BiasHit{{
		Kind:     BiasRushedAnswer,
		Desc:     "草率回答：复杂任务仅 1 轮（总轮数<2）且无任何工具/验证证据——复杂问题不得一次思考解决，应先探索（补工具证据）或补验证轮再收尾。",
		Penalty:  10,
		DefectID: "d-bias-rushed-answer",
	}}
}

// stripGoalProtocolMarkers 剥离 goal 协议标记（成对与单标记），
// 保留正文用于模式匹配。与 runtime.stripGoalProtocol 的语义一致，
// 但 overseer 不依赖 runtime（避免循环依赖），此处保留本地实现。
func stripGoalProtocolMarkers(s string) string {
	tags := []string{"GOAL_PLAN", "GOAL_STEPS", "GOAL_REFLECT", "GOAL_DEFECTS", "GOAL_LESSON", "GOAL_SPEC"}
	out := s
	for _, tag := range tags {
		for {
			open := "[" + tag + "]"
			closeTag := "[/" + tag + "]"
			i := strings.Index(out, open)
			if i < 0 {
				break
			}
			j := strings.Index(out[i+len(open):], closeTag)
			if j < 0 {
				out = out[:i]
				break
			}
			out = out[:i] + out[i+len(open)+j+len(closeTag):]
		}
	}
	out = strings.ReplaceAll(out, "[GOAL_DONE]", "")
	out = strings.ReplaceAll(out, "[GOAL_POLISH_DONE]", "")
	out = strings.ReplaceAll(out, "[GOAL_BLOCKED_BY_GATE]", "")
	return strings.TrimSpace(out)
}

// TotalPenalty 命中列表的扣分合计（封顶 100，保证评分非负）。
func TotalPenalty(hits []BiasHit) float64 {
	p := 0.0
	for _, h := range hits {
		p += h.Penalty
	}
	if p > 100 {
		p = 100
	}
	return p
}
