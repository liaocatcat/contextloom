package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// registerPlanNodes 实现 overseer.node_auto_plan 配置：把用户输入拆分为
// 子任务并注册为监工的计划节点（§3.2 计划节点追踪），供 AI 编排和 TUI 面板参考。
// 拆分规则：优先按换行拆；单行时按分号（；/;）拆；过滤过短片段。
// 复杂度按片段长度估算（>100 字=3，>50 字=2，否则 1）。
func (rt *Runtime) registerPlanNodes(ov *overseer.Overseer, input string) {
	tasks := splitPlanTasks(input)
	if len(tasks) == 0 {
		return
	}
	specs := make([]overseer.TaskSpec, 0, len(tasks))
	for _, t := range tasks {
		specs = append(specs, overseer.TaskSpec{
			Directive:  "task",
			Task:       t,
			Complexity: estimateComplexity(t),
		})
	}
	for _, node := range overseer.PlanNodesStructured(specs, 1) {
		ov.RegisterNode(node.ID, node.Prompt)
	}
}

// splitPlanTasks 把输入拆分为子任务列表
func splitPlanTasks(input string) []string {
	lines := strings.Split(input, "\n")
	var tasks []string
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || len([]rune(ln)) < 4 {
			continue
		}
		// 单行含分号时按分号再拆
		if len(lines) == 1 && strings.ContainsAny(ln, "；;") {
			for _, seg := range strings.FieldsFunc(ln, func(r rune) bool { return r == '；' || r == ';' }) {
				seg = strings.TrimSpace(seg)
				if len([]rune(seg)) >= 4 {
					tasks = append(tasks, seg)
				}
			}
			continue
		}
		tasks = append(tasks, ln)
	}
	return tasks
}

// estimateComplexity 按片段长度估算任务复杂度（1=简单 2=中等 3=复杂）
func estimateComplexity(t string) int {
	switch n := len([]rune(t)); {
	case n > 100:
		return 3
	case n > 50:
		return 2
	default:
		return 1
	}
}

// isLangIdentifier 判断字符串是否为代码块语言标识（纯字母/+/#/-）
func isLangIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '+', r == '#', r == '-':
		default:
			return false
		}
	}
	return true
}

// HasOverseer 是否有监工

// verifyTestExecution 实现 §9.6 防自我欺骗的透明代理测试：
// 受 overseer.test_tee 配置开关控制（默认 true）。
// 仅当子 Agent 声称测试通过时触发——监工独立在沙箱执行测试获取真实结果（ActualResult），
// 供 VerifyExecution 对比。子 Agent 未涉及测试则返回 nil（不触发防欺骗）。
func verifyTestExecution(ctx context.Context, agentOutput string, enabled bool) (claimed, actual *types.ToolResult) {
	// 透明代理开关关闭时不做防欺骗验证
	if !enabled {
		return nil, nil
	}
	// 仅当子 Agent 声称测试通过时才触发透明代理验证（避免无谓的 go test 执行）
	if !claimsTestPassed(agentOutput) {
		return nil, nil
	}
	// 提取声称的测试通过数
	claimedPassed, claimedFailed := extractClaimedTestCounts(agentOutput)
	claimed = &types.ToolResult{
		CallID: "anti_cheat_claimed",
		Name:   "run_tests",
		Result: map[string]any{"passed": claimedPassed, "failed": claimedFailed},
	}
	// 监工独立执行测试（透明代理），设 15s 超时防止卡死
	testCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	testResult, _ := tools.RunTests(testCtx, map[string]any{"test_target": "./..."})
	actual = &types.ToolResult{
		CallID: "anti_cheat_actual",
		Name:   "run_tests",
		Result: testResult,
	}
	return claimed, actual
}

// claimsTestPassed 检测子 Agent 是否声称测试通过（仅此情况触发防欺骗验证）
func claimsTestPassed(s string) bool {
	for _, kw := range []string{"测试通过", "PASS", "全部通过", "测试成功"} {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

// extractClaimedTestCounts 从输出文本启发式提取声称的测试通过/失败数
func extractClaimedTestCounts(s string) (passed, failed int) {
	// 简化实现：检测关键词给默认值，完整解析可后续增强
	if strings.Contains(s, "全部通过") || strings.Contains(s, "PASS") {
		passed = 10 // 声称通过
	}
	if strings.Contains(s, "FAIL") || strings.Contains(s, "测试失败") {
		failed = 1 // 声称失败
	}
	if passed == 0 && failed == 0 {
		passed = 5 // 中性默认
	}
	return
}

// splitSubTasks 检测多行输入中的子任务（§5.1）
// resolvePath 将配置中的路径解析为绝对路径。
// 如果路径是绝对路径或以 ~ 开头，直接展开返回；
// 否则视为相对于 projRoot 的相对路径。
func resolvePath(p, projRoot string) string {
	if p == "" {
		return projRoot
	}
	if strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projRoot, p)
}

// StripThinking 剥离思考标签内容（支持模型层标记 <|reasoning|>/<|/reasoning|> 和尖括号 <think>/<thinking>）
// 以及旧版 thinking...response 格式（thinking/response 处于行首或空白后）
func StripThinking(s string) string {
	// 处理模型层标记
	for {
		start := strings.Index(s, "<|reasoning|>")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "<|/reasoning|>")
		if end < 0 {
			// 未闭合：删除从开标签到末尾
			s = s[:start]
			break
		}
		s = s[:start] + s[start+end+len("<|/reasoning|>"):]
	}
	// 处理尖括号格式
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</think>")
		if end < 0 {
			break
		}
		s = s[:start] + s[end+len("</think>"):]
	}
	// <thinking> 变体（与注释声明一致；<thinking> 不含 <think> 完整标签，
	// 上面的循环匹配不到，需单独处理）
	for {
		start := strings.Index(s, "<thinking>")
		if start < 0 {
			break
		}
		end := strings.Index(s, "</thinking>")
		if end < 0 {
			break
		}
		s = s[:start] + s[end+len("</thinking>"):]
	}
	// 兼容历史数据：旧版 thinking...response 格式（thinking/response 处于行首或空白后）
	// 注意：正文中英文单词 "thinking"/"response" 可能误匹配，但历史数据为旧模型输出，
	// 且此处仅影响写入历史的内容；新输出已统一使用 <|reasoning|> 标记。
	// 未闭合的旧格式不剥离（避免 "response 在 thinking 之前" 的正文被误删）
	for {
		start := indexWord(s, "thinking")
		if start < 0 {
			break
		}
		rest := s[start+len("thinking"):]
		end := -1
		for from := 0; ; {
			idx := indexWord(rest[from:], "response")
			if idx < 0 {
				break
			}
			idx += from
			// 跨行时要求 thinking 与 response 都独占一行（行首），
			// 避免正文中 "thinking 环节 ... response 处理" 等英文单词被误删
			if idx > 0 && rest[idx-1] != '\n' && strings.Contains(rest[:idx], "\n") {
				from = idx + 1
				continue
			}
			end = idx
			break
		}
		if end < 0 {
			// 未闭合：无法可靠区分正文与思考，保留原文
			break
		}
		// 跨行时要求 thinking 与 response 都独占一行（行首），避免误删正文
		if strings.Contains(rest[:end], "\n") {
			thinkLineStart := start == 0 || s[start-1] == '\n'
			respLineStart := end == 0 || rest[end-1] == '\n'
			if !(thinkLineStart && respLineStart) {
				break
			}
		}
		// 删除范围：thinking（含其前导空白）到 response 结束
		prefix := s[:start]
		for len(prefix) > 0 {
			last := prefix[len(prefix)-1]
			if last == ' ' || last == '\t' || last == '\n' || last == '\r' {
				prefix = prefix[:len(prefix)-1]
			} else {
				break
			}
		}
		s = prefix + rest[end+len("response"):]
	}
	return s
}

// indexWord 查找词 token 的位置，要求前面是行首或空白（\n\r\t 空格），
// 避免把路径中的 think/thinking 子串（如 /home/thinking_project）误判为思考标签。
func indexWord(s, token string) int {
	for from := 0; ; {
		idx := strings.Index(s[from:], token)
		if idx < 0 {
			return -1
		}
		idx += from
		if idx > 0 {
			prev := s[idx-1]
			if prev != ' ' && prev != '\n' && prev != '\t' && prev != '\r' {
				from = idx + 1
				continue
			}
		}
		return idx
	}
}

// PromoteThinkBody 正文为空、回答滞留思考内的兜底提升。
//
// 背景（用户症状"正文为空，回答出现在思考内"）：R 系列思考模型偶发把最终
// 结论全部写进 reasoning_content、content 为空（长思考耗尽 max_tokens 或
// 未切回正文阶段的上游行为）。此时若只展示折叠思考块，正文区空无一物。
// 兜底策略：取最后一段非空思考的【尾段】（结论通常在末尾），加来源标记后
// 作为可见正文返回。ok=false 表示没有可提升内容。
func PromoteThinkBody(thinks []string) (string, bool) {
	last := ""
	for i := len(thinks) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(thinks[i]); t != "" {
			last = t
			break
		}
	}
	if last == "" {
		return "", false
	}
	const maxRunes = 1600
	if r := []rune(last); len(r) > maxRunes {
		last = "…（思考前段略，仅取结论尾段）" + string(r[len(r)-maxRunes:])
	}
	return "〔回答取自思考：模型未输出正文，以下为思考结论〕\n\n" + last, true
}

// SplitReasoning 从含思考标签的完整 assistant 输出中分离【正文】与【思维链】。
//
// 背景：deepseek go 的流式/非流式输出把思维链以 <|reasoning|>…<|/reasoning|>
// 标签与正文拼接（TUI/GUI 实时展示用）。保存历史时正文进 Content、思维链进
// ReasoningContent；DeepSeek thinking 模式要求 assistant 消息原样回传
// reasoning_content，故两段都必须持久化（不能再像旧版 StripThinking 直接丢弃）。
//
// 支持标签：<|reasoning|>/<|/reasoning|>、<thinking>/</thinking>、
// " thinking /" response（旧模型格式）。无标签时 body=原文、reasoning="".
// 未闭合标签：视为正文（与 StripThinking 对旧格式的保守策略一致）。
func SplitReasoning(s string) (body, reasoning string) {
	// 模型层标记（deepseek.go 输出）：<|reasoning|>…<|/reasoning|>
	// 宽松识别开标签：<|reasoning|> 或 <|reasoning|（缺尾部 > 的变体，如
	// 压缩/截断场景），闭合必须 <|/reasoning|>——避免开标签不完整导致思维链泄漏正文。
	for {
		start := strings.Index(s, "<|reasoning|")
		if start < 0 {
			break
		}
		openLen := len("<|reasoning|")
		if start+openLen < len(s) && s[start+openLen] == '>' {
			openLen++
		}
		rest := s[start+openLen:]
		end := strings.Index(rest, "<|/reasoning|>")
		if end < 0 {
			// 未闭合：去掉开标签之前的正文，去掉开标签本身与剩余内容
			body += s[:start]
			reasoning += rest
			return body, reasoning
		}
		body += s[:start]
		reasoning += rest[:end]
		s = rest[end+len("<|/reasoning|>"):]
	}
	// 尖括号格式：<think>…</think>（注：<thinking> 的第 7 字符是 'i' 非 '>',
	// 两个标签互不为对方前缀子串，下面的循环不会误吃 <thinking> 开标签）
	for {
		start := strings.Index(s, "<think>")
		if start < 0 {
			break
		}
		rest := s[start+len("<think>"):]
		end := strings.Index(rest, "</think>")
		if end < 0 {
			body += s[:start]
			reasoning += rest
			return body, reasoning
		}
		body += s[:start]
		reasoning += rest[:end]
		s = rest[end+len("</think>"):]
	}
	// 尖括号格式：<thinking>…</thinking>
	for {
		start := strings.Index(s, "<thinking>")
		if start < 0 {
			break
		}
		rest := s[start+len("<thinking>"):]
		end := strings.Index(rest, "</thinking>")
		if end < 0 {
			body += s[:start]
			reasoning += rest
			return body, reasoning
		}
		body += s[:start]
		reasoning += rest[:end]
		s = rest[end+len("</thinking>"):]
	}
	// 旧格式：" thinking … response"（行首或空白后，与 StripThinking 一致）
	for {
		start := indexWord(s, "thinking")
		if start < 0 {
			break
		}
		rest := s[start+len("thinking"):]
		end := indexWord(rest, "response")
		if end < 0 {
			// 未闭合旧格式：保守保留为正文（与 StripThinking 一致）
			break
		}
		// 前导空白归属思维块（与 TUI splitThinkTags 对齐）
		lead := start
		for lead > 0 && (s[lead-1] == ' ' || s[lead-1] == '\t') {
			lead--
		}
		if lead > 0 && s[lead-1] == '\n' {
			lead--
		}
		body += s[:lead]
		// indexWord 要求 response 前有空白，该空白是分隔符不属于思维链——
		// 裁掉（与 body 侧 lead 回退对称，否则 reasoning 尾部残留空格）
		reasoning += strings.TrimRight(rest[:end], " \t\r\n")
		s = rest[end+len("response"):]
	}
	body += s
	return body, reasoning
}

// ── Goal 协议解析（[GOAL_PLAN] / [GOAL_STEPS] / [GOAL_DONE]）──

// extractGoalPlan 从模型响应中提取 plan 轮步骤清单（[GOAL_PLAN]<JSON>[/GOAL_PLAN]）。
// JSON 元素格式: [{"id":"s1","desc":"...","acceptance":"..."}]。宽松解析：容忍标记未闭合、JSON 前有杂文本。
func extractGoalPlan(response string) []types.GoalStep {
	jsonStr := extractGoalTaggedJSON(response, "GOAL_PLAN")
	if jsonStr == "" {
		return nil
	}
	var raw []struct {
		ID         string `json:"id"`
		Desc       string `json:"desc"`
		Acceptance string `json:"acceptance"`
	}
	if err := jsonUnmarshal(jsonStr, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	steps := make([]types.GoalStep, 0, len(raw))
	for i, r := range raw {
		id := r.ID
		if id == "" {
			id = fmt.Sprintf("s%d", i+1)
		}
		steps = append(steps, types.GoalStep{ID: id, Desc: r.Desc, Acceptance: r.Acceptance, Status: string(types.GoalStepPending)})
	}
	return steps
}

// extractGoalSteps 从模型响应中提取每轮步骤状态汇报（[GOAL_STEPS]<JSON>[/GOAL_STEPS]）。
// JSON 元素格式: [{"id":"s1","status":"done","evidence":"..."}]。
// v3 证据链扩展：元素可带 "proofs":[{"kind":"file|diff|test","target":"..."}]，
// 解析后随步骤返回，由 goalmgr 的确定性核验器验证。
func extractGoalSteps(response string) []types.GoalStep {
	jsonStr := extractGoalTaggedJSON(response, "GOAL_STEPS")
	if jsonStr == "" {
		return nil
	}
	var raw []struct {
		ID       string `json:"id"`
		Desc     string `json:"desc"`
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
		Proofs   []struct {
			Kind   string `json:"kind"`
			Target string `json:"target"`
			Note   string `json:"note"`
		} `json:"proofs"`
	}
	if err := jsonUnmarshal(jsonStr, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	steps := make([]types.GoalStep, 0, len(raw))
	for _, r := range raw {
		if r.ID == "" {
			continue
		}
		status := r.Status
		switch status {
		case string(types.GoalStepDone), string(types.GoalStepFailed), string(types.GoalStepRunning):
		case "completed", "complete", "ok", "yes":
			status = string(types.GoalStepDone)
		default:
			status = string(types.GoalStepPending)
		}
		proofs := make([]types.GoalProof, 0, len(r.Proofs))
		for _, p := range r.Proofs {
			if p.Kind != "" && p.Target != "" {
				proofs = append(proofs, types.GoalProof{Kind: p.Kind, Target: p.Target, Note: p.Note})
			}
		}
		steps = append(steps, types.GoalStep{ID: r.ID, Desc: r.Desc, Status: status, Evidence: r.Evidence, Proofs: proofs})
	}
	return steps
}

// extractGoalReflect 从模型响应中提取本轮反思（[GOAL_REFLECT]<JSON>[/GOAL_REFLECT]）。
// JSON 对象格式: {"summary":"做了什么","problems":"发现什么问题","next_plan":"下一步怎么改"}。
func extractGoalReflect(response string) types.GoalReflection {
	jsonStr := extractGoalTaggedJSON(response, "GOAL_REFLECT")
	if jsonStr == "" {
		return types.GoalReflection{}
	}
	var raw struct {
		Summary  string `json:"summary"`
		Problems string `json:"problems"`
		NextPlan string `json:"next_plan"`
	}
	if err := jsonUnmarshal(jsonStr, &raw); err != nil {
		return types.GoalReflection{}
	}
	return types.GoalReflection{Summary: raw.Summary, Problems: raw.Problems, NextPlan: raw.NextPlan}
}

// extractGoalDefects 从模型响应中提取缺陷修复回报（[GOAL_DEFECTS]<JSON>[/GOAL_DEFECTS]）。
// JSON 元素格式: [{"id":"d1","status":"fixed","evidence":"..."}]。
func extractGoalDefects(response string) []types.GoalDefect {
	jsonStr := extractGoalTaggedJSON(response, "GOAL_DEFECTS")
	if jsonStr == "" {
		return nil
	}
	var raw []struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Evidence string `json:"evidence"`
	}
	if err := jsonUnmarshal(jsonStr, &raw); err != nil || len(raw) == 0 {
		return nil
	}
	defects := make([]types.GoalDefect, 0, len(raw))
	for _, r := range raw {
		if r.ID == "" {
			continue
		}
		status := r.Status
		switch status {
		case "fixed", "done", "ok", "resolved":
			status = "fixed"
		default:
			status = "open"
		}
		defects = append(defects, types.GoalDefect{ID: r.ID, Desc: "", Status: status, Evidence: r.Evidence})
	}
	return defects
}

// extractGoalLessons 从模型响应中提取经验教训（[GOAL_LESSON]<JSON>[/GOAL_LESSON]）。
// JSON 支持两种形态: {"lesson":"..."} 或 [{"lesson":"..."}]。
func extractGoalLessons(response string) []string {
	jsonStr := extractGoalTaggedJSON(response, "GOAL_LESSON")
	if jsonStr == "" {
		return nil
	}
	var single struct {
		Lesson string `json:"lesson"`
	}
	if err := jsonUnmarshal(jsonStr, &single); err == nil && strings.TrimSpace(single.Lesson) != "" {
		return []string{single.Lesson}
	}
	var multi []struct {
		Lesson string `json:"lesson"`
	}
	if err := jsonUnmarshal(jsonStr, &multi); err == nil {
		var out []string
		for _, m := range multi {
			if strings.TrimSpace(m.Lesson) != "" {
				out = append(out, m.Lesson)
			}
		}
		return out
	}
	return nil
}

// hasGoalPolishDone 检测打磨轮完成标记（[GOAL_POLISH_DONE]）。
func hasGoalPolishDone(response string) bool {
	return strings.Contains(response, "[GOAL_POLISH_DONE]")
}

// stripGoalPlan 从 response 剥离 [GOAL_PLAN] 成对标记及其内容
// （spec 门槛未通过时禁入规划：plan 清单必须等细化通过后再生效）。
func stripGoalPlan(response string) string {
	for {
		open := "[GOAL_PLAN]"
		closeTag := "[/GOAL_PLAN]"
		i := strings.Index(response, open)
		if i < 0 {
			break
		}
		j := strings.Index(response[i+len(open):], closeTag)
		if j < 0 {
			response = response[:i]
			break
		}
		response = response[:i] + response[i+len(open)+j+len(closeTag):]
	}
	return response
}

// extractGoalTaggedJSON 提取 [TAG] 与 [/TAG] 之间的 JSON 数组文本。
// 宽容策略：优先匹配闭合标记；未闭合时取 TAG 后第一个 [ 到最后一个 ]。
func extractGoalTaggedJSON(response, tag string) string {
	open := "[" + tag + "]"
	closeTag := "[/" + tag + "]"
	if i := strings.Index(response, open); i >= 0 {
		rest := response[i+len(open):]
		if j := strings.Index(rest, closeTag); j >= 0 {
			return strings.TrimSpace(rest[:j])
		}
		// 未闭合：取第一个 [ 到最后一个 ]
		if a := strings.Index(rest, "["); a >= 0 {
			if b := strings.LastIndex(rest, "]"); b > a {
				return rest[a : b+1]
			}
		}
	}
	return ""
}

func jsonUnmarshal(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}
