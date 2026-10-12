// Package goalmgr —— 需求细化（SPEC 阶段）的确定性门槛与模糊度检测。
//
// 对照 why-self-verification-is-unreliable.md 的对策：把模型对模糊输入的
// 隐性假设显式化、合同化。SpecGate 是零模型依赖的确定性判定器，
// 只有通过门槛的 [GOAL_SPEC] 才会让状态机离开 spec 阶段进入规划/编码。
package goalmgr

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// MaxSpecRounds spec 阶段连续细化轮数上限：达到后 continuation 注入
// 用户澄清提示（不强制失败），防止"细化-拒绝"死循环。
const MaxSpecRounds = 3

// ── [GOAL_SPEC] 协议解析 ──

// ExtractGoalSpec 从模型响应中提取需求细化产物（[GOAL_SPEC]<JSON>[/GOAL_SPEC]）。
// 与 extractGoalTaggedJSON 同源（见 runtime/helpers.go）的宽容策略：
// 优先匹配闭合标记；未闭合时取第一个 { 到最后一个 }。
// 解析失败返回 nil（调用方按"未提供 spec"处理，不阻塞旧路径）。
func ExtractGoalSpec(response string) *types.GoalSpec {
	open := "[GOAL_SPEC]"
	closeTag := "[/GOAL_SPEC]"
	i := strings.Index(response, open)
	if i < 0 {
		return nil
	}
	rest := response[i+len(open):]
	jsonStr := ""
	if j := strings.Index(rest, closeTag); j >= 0 {
		jsonStr = strings.TrimSpace(rest[:j])
	} else if a := strings.Index(rest, "{"); a >= 0 {
		if b := strings.LastIndex(rest, "}"); b > a {
			jsonStr = rest[a : b+1]
		}
	}
	if jsonStr == "" {
		return nil
	}
	var raw struct {
		GoalRefined   string   `json:"goal_refined"`
		Assumptions   []string `json:"assumptions"`
		Acceptance    []string `json:"acceptance"`
		Scope         []string `json:"scope"`
		Risks         []string `json:"risks"`
		OpenQuestions []string `json:"open_questions"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &raw); err != nil {
		return nil
	}
	if strings.TrimSpace(raw.GoalRefined) == "" && len(raw.Assumptions) == 0 {
		return nil // 空壳 spec 不算 spec
	}
	return &types.GoalSpec{
		GoalRefined:   strings.TrimSpace(raw.GoalRefined),
		Assumptions:   trimAll(raw.Assumptions),
		Acceptance:    trimAll(raw.Acceptance),
		Scope:         trimAll(raw.Scope),
		Risks:         trimAll(raw.Risks),
		OpenQuestions: trimAll(raw.OpenQuestions),
	}
}

func trimAll(in []string) []string {
	var out []string
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// ── 确定性门槛：SpecGate ──

// 目标物指代模式：文件路径 / 代码符号 / shell 命令 / URL。
// 细化后的目标必须引用至少一个可解析的具体目标物，否则说明细化没有
// 落到"改哪里"的层面（仍是模糊需求）。
var targetRefRe = regexp.MustCompile(`(?:^|[^\w./-])(?:[\w./-]*/[\w.-]+\.(?:go|yaml|yml|json|md|py|js|ts|sh|txt|toml)\b|[\w./-]+\.(?:go|yaml|yml|json|md|py|js|ts|sh|txt|toml)\b|\./[a-z]|/[a-z]|[a-z_]+\(\)|\b(?:git|go|make|docker|curl|python|npm|pnpm)\s+\S+)`)

// 可测量谓词模式：数字 / 百分比 / 退出码 / 命令 / 文件路径 / 时间。
var measurableRe = regexp.MustCompile(`\d+(?:\.\d+)?\s*%|\d+(?:\.\d+)?\s*(?:ms|s|min|行|个|条|次|倍|MB|KB)?\b|\b(?:exit|退出码)\b|\b(?:go|git|make|docker|curl|python|npm|pnpm|pytest|test)\b|\.(?:go|yaml|yml|json|md|py|js|ts)\b`)

// SpecIssue 一条 SpecGate 拒绝原因。
type SpecIssue struct {
	Code string // 规则编号 R1..R5
	Desc string // 可读原因（进入缺陷条目）
}

// SpecGate 需求细化的确定性门槛（零模型依赖）。
// 通过 = 无 issues。所有判定基于正则/结构检查，与模型无关。
type SpecGate struct{}

// Check 判定 spec 是否达到"可进入规划/编码"的门槛。
// 返回 (ok, issues)：ok=true 时 issues 为空。
func (SpecGate) Check(spec *types.GoalSpec) (bool, []SpecIssue) {
	if spec == nil {
		return false, []SpecIssue{{Code: "R1", Desc: "未提供需求细化（缺 [GOAL_SPEC]）"}}
	}
	var issues []SpecIssue

	// R1 目标细化非空
	if strings.TrimSpace(spec.GoalRefined) == "" {
		issues = append(issues, SpecIssue{Code: "R1", Desc: "目标未细化：goal_refined 为空"})
	}

	// R2 目标物指代：goal_refined 或 scope 引用文件/符号/命令/URL
	refText := spec.GoalRefined + "\n" + strings.Join(spec.Scope, "\n")
	if !targetRefRe.MatchString(refText) {
		issues = append(issues, SpecIssue{Code: "R2", Desc: "目标物指代缺失：细化目标未引用任何文件路径/符号/命令（改哪里不明确）"})
	}

	// R3 假设显式
	if len(spec.Assumptions) == 0 {
		issues = append(issues, SpecIssue{Code: "R3", Desc: "假设未显式：assumptions 为空（关键决策没有声明依据）"})
	}

	// R4 验收可测量
	if !acceptanceMeasurable(spec.Acceptance) {
		issues = append(issues, SpecIssue{Code: "R4", Desc: "验收标准不可测量：acceptance 未包含数字/百分比/命令/文件路径等可测量谓词"})
	}

	// R5 开放问题必须与兜底假设配对
	if len(spec.OpenQuestions) > 0 && len(spec.Assumptions) == 0 {
		issues = append(issues, SpecIssue{Code: "R5", Desc: "开放问题无兜底假设：open_questions 存在但 assumptions 为空"})
	}

	return len(issues) == 0, issues
}

// acceptanceMeasurable 验收标准至少一条含可测量谓词。
func acceptanceMeasurable(acceptance []string) bool {
	for _, a := range acceptance {
		if measurableRe.MatchString(a) {
			return true
		}
	}
	return false
}

// String SpecIssue 的可读形式（注入缺陷条目）。
func (i SpecIssue) String() string {
	return fmt.Sprintf("[spec-%s] %s", i.Code, i.Desc)
}

// ── 模糊度检测（提示词侧建议，不硬性拦截）──

// Fuzziness 输入模糊度评估结果。
type Fuzziness struct {
	Fuzzy   bool     // 是否建议触发 SPEC 细化
	Hits    []string // 命中原因
	Suggest bool     // 是否建议模型产出 [GOAL_SPEC]
}

// 弱动词：不含"执行类"动词的输入往往不是可执行指令。
var actionVerbRe = regexp.MustCompile(`(?i)(实现|修复|新增|添加|修改|删除|移除|迁移|重构|优化|支持|集成|部署|生成|编写|补充|调整|升级|扩展|拆分|合并|测试|排查|分析|配置)`)

// DetectFuzziness 检测用户输入是否缺乏细节（供提示词生成侧参考）。
// 规则：无执行动词 / 无可测量验收 / 无目标物指代，命中 ≥2 条 → 建议细化。
// 注意：这是建议信号，绝不硬性拦截用户输入（用户输入永远优先）。
func DetectFuzziness(input string) Fuzziness {
	f := Fuzziness{}
	if !actionVerbRe.MatchString(input) {
		f.Hits = append(f.Hits, "无执行动词（没说做什么操作）")
	}
	if !measurableRe.MatchString(input) {
		f.Hits = append(f.Hits, "无可测量验收（无数字/百分比/命令/文件）")
	}
	if !targetRefRe.MatchString(input) {
		f.Hits = append(f.Hits, "无目标物指代（没指出改哪个文件/模块）")
	}
	f.Fuzzy = len(f.Hits) >= 2
	f.Suggest = len(f.Hits) >= 1
	return f
}

// BuildClarifyContinuation 生成用户澄清提示（spec 连续多轮未过时注入）。
func BuildClarifyContinuation(round int, issues []SpecIssue) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("【需求细化未收敛】已连续 %d 轮细化未通过确定性门槛（%d 条原因）：\n", round, len(issues)))
	for i, it := range issues {
		b.WriteString(fmt.Sprintf("  %d. %s\n", i+1, it.String()))
	}
	b.WriteString("请在下一轮输出 [GOAL_SPEC] 时逐条对照修复；若关键信息确实缺失且无法合理假设，")
	b.WriteString("请把需要用户澄清的问题写入 open_questions 并在 assumptions 中给出兜底假设，")
	b.WriteString("同时在本轮正文用自然语言向用户提出澄清问题。")
	return b.String()
}
