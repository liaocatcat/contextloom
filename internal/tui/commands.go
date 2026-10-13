package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
)

type CommandEntry struct {
	Name string
	Desc string
}

type CommandRegistry struct {
	rt       *runtime.Runtime
	commands map[string]CommandHandler
	descs    map[string]string
}

type CommandHandler func(args []string) string

func NewCommandRegistry(rt *runtime.Runtime) *CommandRegistry {
	cr := &CommandRegistry{
		rt:       rt,
		commands: make(map[string]CommandHandler),
		descs:    make(map[string]string),
	}
	cr.registerBuiltins()
	return cr
}

func (cr *CommandRegistry) registerBuiltins() {
	cr.commands["/new"] = func(args []string) string {
		if cr.rt == nil {
			return "新会话已创建"
		}
		s := cr.rt.NewSession("新会话")
		return "新会话已创建: " + s.UUID[:8] + "..."
	}
	cr.descs["/new"] = "创建新会话"

	cr.commands["/history"] = func(args []string) string {
		if cr.rt == nil {
			return "无历史会话"
		}
		sessions := cr.rt.ListSessions()
		if len(sessions) == 0 {
			return "无历史会话"
		}
		var lines []string
		for _, s := range sessions {
			lines = append(lines, "  "+s.UUID+"  "+truncStr(s.Title, 40))
		}
		return "历史会话:\n" + strings.Join(lines, "\n")
	}
	cr.descs["/history"] = "列出历史会话"

	cr.commands["/load"] = func(args []string) string {
		if len(args) == 0 {
			return "用法: /load <uuid>"
		}
		if cr.rt == nil {
			return "已加载: " + args[0]
		}
		_, t, err := cr.rt.LoadSession(args[0])
		if err != nil {
			return "加载失败: " + err.Error()
		}
		return "已加载: " + t.Title
	}
	cr.descs["/load"] = "加载指定会话 <uuid>"

	cr.commands["/compress"] = func(args []string) string {
		if cr.rt == nil {
			return "已压缩"
		}
		return cr.rt.CompressContext()
	}
	cr.descs["/compress"] = "压缩上下文（衰减未引用块）"

	cr.commands["/ctx"] = func(args []string) string {
		if cr.rt == nil {
			return "上下文: 未初始化"
		}
		if len(args) > 0 && args[0] == "reset" {
			cr.rt.ResetContextSummary()
			return "上下文滚动摘要与压缩审计已重置"
		}
		return cr.rt.ContextStats()
	}
	cr.descs["/ctx"] = "查看上下文用量/阈值/压缩审计 (/ctx reset 重置)"

	cr.commands["/model"] = func(args []string) string {
		if cr.rt == nil {
			return "当前: test"
		}
		if len(args) == 0 {
			names := cr.rt.ModelNames()
			cur := cr.rt.CurrentModel()
			if cur == "" {
				cur = "default-model"
			}
			return "当前: " + cur + "\n可用模型: " + strings.Join(names, " / ") + "\n用法: /model <名称>"
		}
		if err := cr.rt.SwitchModel(args[0]); err != nil {
			return "切换失败: " + err.Error()
		}
		return "已切换至: " + args[0]
	}
	cr.descs["/model"] = "切换模型预设 <名称>（无参列出可用模型）"

	cr.commands["/emp"] = func(args []string) string {
		if cr.rt == nil {
			return "未连接运行时"
		}
		// 无参 → 列出员工清单
		if len(args) == 0 {
			return cr.rt.EmployeesOverview()
		}
		// /emp <name> → 查看员工档案摘要
		if len(args) == 1 {
			if s := cr.rt.EmployeeSummary(args[0]); s != "" {
				return s
			}
			return "员工 " + args[0] + " 不存在（可用: " + strings.Join(cr.rt.EmployeeNames(), " / ") + "）"
		}
		// /emp <name> <task...> → 指定员工执行任务（task 模式装配）
		name := args[0]
		taskText := strings.Join(args[1:], " ")
		if err := cr.rt.AssignEmployee(name, taskText); err != nil {
			return "指派失败: " + err.Error()
		}
		return "已将任务指派给员工 " + name + "（任务已进入执行队列）"
	}
	cr.descs["/emp"] = "员工中心: /emp 列员工 | /emp <名> 看档案 | /emp <名> <任务> 指派员工执行"

	cr.commands["/effort"] = func(args []string) string {
		if len(args) == 0 {
			// 无参：展示当前档位 + 三处长度参数生效值（排查"调了没反应"）。
			am := cr.rt.Config.ActiveModel()
			out := fmt.Sprintf("当前思考深度: %s | 输出上限: %s | 输入预算: %s",
				cr.rt.Config.CLIDefaults.Effort, fmtTokens(am.MaxOutputTokens), fmtTokens(am.MaxInputTokens))
			if am.ThinkingLength != "" {
				out += " | 预设档位: " + am.ThinkingLength
			}
			return out
		}
		level := args[0]
		switch level {
		case "off", "low", "medium", "high", "xhigh", "max":
			cr.rt.SetEffort(level)
			if level == "off" {
				return "已设置: off（关闭思考，不发送 reasoning_effort）"
			}
			return "已设置: " + level
		default:
			return "非法档位: " + level + "（可用: off/low/medium/high/xhigh/max）"
		}
	}
	cr.descs["/effort"] = "设置思考深度 (off/low/medium/high/xhigh/max)；无参查看长度参数"

	// /maxlen：输出/输入长度上限（与上下文窗口解耦，对标 dsh 的 max_tokens 三级优先级）。
	// 走 ApplyConfigPatch 热更新链路（持久化 + 压缩阈值重建），与 GUI 设置面板同一路径，
	// 避免"命令行改了不落盘 / 不生效"。
	cr.commands["/maxlen"] = func(args []string) string {
		if len(args) == 0 {
			am := cr.rt.Config.ActiveModel()
			return fmt.Sprintf("输出上限: %s | 输入预算: %s（0=不限制，自动取最大上下文 %s）",
				fmtTokens(am.MaxOutputTokens), fmtTokens(am.MaxInputTokens), fmtTokens(am.MaxContext))
		}
		if len(args) < 2 {
			return "用法: /maxlen output|input <tokens>（0=不限制）"
		}
		var n int
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil || n < 0 {
			return "非法数值: " + args[1] + "（需为非负整数）"
		}
		key := ""
		switch args[0] {
		case "output", "out":
			key = "model.max_output_tokens"
		case "input", "in":
			key = "model.max_input_tokens"
		default:
			return "非法类型: " + args[0] + "（可用: output / input）"
		}
		if _, err := cr.rt.ApplyConfigPatch(map[string]any{key: n}); err != nil {
			return "设置失败: " + err.Error()
		}
		return fmt.Sprintf("已设置 %s: %s", args[0], fmtTokens(n))
	}
	cr.descs["/maxlen"] = "输出/输入长度上限: /maxlen [output|input] [tokens]"

	// /think：多轮思考策略（轮次面"想几轮/是否验证"，与 /effort 参数面"每轮多长"正交）。
	// policy/verify 走 runtime 会话级覆盖（可选接口断言：runtime 未落盘时优雅降级）；
	// rounds/nudge/effort 走 ApplyConfigPatch 配置级热更新（与 /maxlen 同路径，持久化生效）。
	cr.commands["/think"] = func(args []string) string { return cr.handleThink(args) }
	cr.descs["/think"] = "多轮思考策略: /think [policy|verify|rounds|nudge|effort] [值]"

	cr.commands["/threshold"] = func(args []string) string {
		if len(args) == 0 {
			return fmt.Sprintf("当前: %.0f", cr.rt.Config.Harness.ScoreThreshold)
		}
		var t float64
		fmt.Sscanf(args[0], "%f", &t)
		cr.rt.SetThreshold(t)
		return fmt.Sprintf("已调整: %.0f", t)
	}
	cr.descs["/threshold"] = "调整相关度评分阈值 (0-100)"

	cr.commands["/overseer_threshold"] = func(args []string) string {
		if len(args) == 0 {
			return fmt.Sprintf("当前: %.0f", cr.rt.Config.Overseer.PassThreshold)
		}
		var t float64
		fmt.Sscanf(args[0], "%f", &t)
		cr.rt.Config.Overseer.PassThreshold = t
		return fmt.Sprintf("已调整: %.0f", t)
	}
	cr.descs["/overseer_threshold"] = "调整监工通过阈值 (0-100)"

	cr.commands["/plugin"] = func(args []string) string {
		if len(args) > 0 && args[0] == "list" {
			if cr.rt == nil {
				return "无插件"
			}
			return cr.rt.PluginsList()
		}
		return "用法: /plugin list"
	}
	cr.descs["/plugin"] = "管理插件（list 列出）"

	cr.commands["/memory"] = func(args []string) string {
		if cr.rt == nil {
			return "会话数: 0"
		}
		return cr.rt.GetMemoryStats()
	}
	cr.descs["/memory"] = "查看记忆系统统计"

	cr.commands["/dream"] = func(args []string) string {
		if cr.rt == nil {
			return "记忆系统未初始化"
		}
		return cr.rt.DreamMemory()
	}
	cr.descs["/dream"] = "主动整理长期记忆：扫描历史会话抽取教训/经验（对标业界同类 Agent 的 /dream）"

	cr.commands["/teach"] = func(args []string) string {
		if cr.rt == nil {
			return "记忆系统未初始化"
		}
		return cr.rt.TeachMemory(strings.Join(args, " "))
	}
	cr.descs["/teach"] = "手动沉淀一条长期记忆（写入 .contextloom/memory/lessons.md，可编辑）"

	cr.commands["/acp"] = func(args []string) string {
		if cr.rt == nil {
			return "ACP: 未启用"
		}
		return cr.rt.ACPStatus()
	}
	cr.descs["/acp"] = "查看 ACP 服务状态"

	cr.commands["/quit"] = func(args []string) string { return "quit" }
	cr.descs["/quit"] = "优雅退出并持久化"

	cr.commands["/exit"] = func(args []string) string { return "quit" }
	cr.descs["/exit"] = "退出（同 /quit）"

	cr.commands["/inject"] = func(args []string) string {
		if len(args) == 0 {
			return "用法: /inject <context_id>"
		}
		if cr.rt == nil {
			return "已提升"
		}
		return cr.rt.InjectBlock(args[0])
	}
	cr.descs["/inject"] = "手动提升上下文块至锚定区 <id>"

	cr.commands["/sessions"] = func(args []string) string {
		if cr.rt == nil {
			return "无历史会话"
		}
		sessions := cr.rt.ListSessions()
		if len(sessions) == 0 {
			return "无历史会话"
		}
		var lines []string
		for _, s := range sessions { // ListSessions 已按 UpdatedAt 倒序
			preview := s.Preview
			if preview == "" {
				preview = s.Title
			}
			if len([]rune(preview)) > 40 {
				preview = string([]rune(preview)[:40]) + "…"
			}
			lines = append(lines, fmt.Sprintf("  %s  %s  %s", s.UUID, s.UpdatedAt.Format("01-02 15:04"), preview))
		}
		return "对话历史(最近在前):\n" + strings.Join(lines, "\n")
	}
	cr.descs["/sessions"] = "列出所有对话历史(最近在前)"

	cr.commands["/session"] = func(args []string) string {
		if len(args) == 0 {
			return "用法: /session <id>"
		}
		if cr.rt == nil {
			return "需连接运行时"
		}
		_, sess, err := cr.rt.LoadSession(args[0])
		if err != nil {
			return "加载失败: " + err.Error()
		}
		return "已加载会话: " + sess.Title + " (" + sess.UUID + ")"
	}
	cr.descs["/session"] = "加载历史对话 <id>"

	cr.commands["/help"] = func(args []string) string {
		cmds := cr.Commands()
		return "命令: " + strings.Join(cmds, " ")
	}
	cr.descs["/help"] = "查看所有可用命令"

	cr.commands["/mode"] = func(args []string) string {
		if cr.rt == nil {
			return "当前: chat"
		}
		if len(args) > 0 {
			switch args[0] {
			case "task":
				cr.rt.SetMode("task")
				return "已切换至: 多任务并发模式 (task)"
			case "goal":
				cr.rt.SetMode("goal")
				return "已切换至: 目标驱动模式 (goal) —— 发送目标后自动跨轮循环直到完成/超预算"
			case "chat":
				cr.rt.SetMode("chat")
				return "已切换至: 单对话模式 (chat)"
			}
		}
		mode := cr.rt.GetMode()
		return "当前: " + mode + " 用法: /mode chat | /mode task | /mode goal"
	}
	cr.descs["/mode"] = "切换模式: chat(单对话) | task(多任务并发) | goal(目标持久循环)"

	cr.commands["/goal"] = func(args []string) string {
		if cr.rt == nil {
			return "未连接运行时"
		}
		if len(args) > 0 && args[0] == "abort" {
			return cr.rt.GoalAbort()
		}
		if len(args) > 0 && args[0] == "resume" {
			if len(args) < 2 {
				return "用法: /goal resume <goalID> （/goal list 查看 ID）"
			}
			msg, err := cr.rt.GoalLoad(args[1])
			if err != nil {
				return "恢复失败: " + err.Error()
			}
			return msg + "（发送任意消息继续推进目标）"
		}
		if len(args) > 0 && args[0] == "list" {
			return cr.rt.GoalList()
		}
		return cr.rt.GoalStatus()
	}
	cr.descs["/goal"] = "目标循环: 状态 | abort 中止 | list 历史 | resume <id> 恢复"

	// /split 不注册同步 handler（sendSplit 异步处理），仅注册描述供补全和 /help
	cr.commands["/top_p"] = func(args []string) string {
		if cr.rt == nil {
			return fmt.Sprintf("当前: %.2f", 0.95)
		}
		if len(args) == 0 {
			return fmt.Sprintf("当前: %.2f", cr.rt.TopP())
		}
		var v float64
		fmt.Sscanf(args[0], "%f", &v)
		if v < 0 || v > 1 {
			return "范围必须在 [0, 1]"
		}
		cr.rt.Backend.SetTopP(v)
		rt := cr.rt
		rt.UpdateActivePreset(func(m *config.ModelPreset) { m.TopP = &v })
		rt.SaveSettings()
		return fmt.Sprintf("top_p 已设为: %.2f", v)
	}
	cr.descs["/top_p"] = "设置核采样参数 (0-1, 默认 0.95)"

	cr.commands["/temperature"] = func(args []string) string {
		if cr.rt == nil {
			return fmt.Sprintf("当前: %.2f", 1.0)
		}
		if len(args) == 0 {
			return fmt.Sprintf("当前: %.2f", cr.rt.Temperature())
		}
		var v float64
		fmt.Sscanf(args[0], "%f", &v)
		if v < 0 || v > 2 {
			return "范围必须在 [0, 2]"
		}
		cr.rt.Backend.SetTemperature(v)
		rt := cr.rt
		rt.UpdateActivePreset(func(m *config.ModelPreset) { m.Temperature = &v })
		rt.SaveSettings()
		return fmt.Sprintf("temperature 已设为: %.2f", v)
	}
	cr.descs["/temperature"] = "设置温度参数 (0-2, 默认 1.0)"

	cr.commands["/split"] = func(args []string) string {
		return "提示: /split 在命令模式回车时异步派发，请直接输入 /split 任务1, 任务2"
	}
	cr.descs["/split"] = "强制并行派发子 Agent（绕过模型决策）: /split <任务1>, <任务2>"

	cr.commands["/vocab"] = func(args []string) string {
		if cr.rt == nil {
			return "无运行时"
		}
		if len(args) == 0 || args[0] == "list" {
			proposals := cr.rt.ListPendingVocab()
			if len(proposals) == 0 {
				return "无待确认词汇提案"
			}
			var lines []string
			for _, p := range proposals {
				lines = append(lines, fmt.Sprintf("  %s → %s  (%s)", p.Term, p.Mapping, p.Reason))
			}
			return "待确认词汇:\n" + strings.Join(lines, "\n") +
				"\n\n确认: /vocab accept <term>  拒绝: /vocab reject <term>"
		}
		if args[0] == "accept" && len(args) > 1 {
			if cr.rt.AcceptVocab(args[1]) {
				return "已加入词汇表: " + args[1]
			}
			return "未找到该提案: " + args[1]
		}
		if args[0] == "reject" && len(args) > 1 {
			if cr.rt.RejectVocab(args[1]) {
				return "已拒绝: " + args[1]
			}
			return "未找到该提案: " + args[1]
		}
		return "用法: /vocab [list | accept <term> | reject <term>]"
	}
	cr.descs["/vocab"] = "词汇映射提案管理: /vocab [list|accept <term>|reject <term>]"

	cr.commands["/checkpoints"] = func(args []string) string {
		if cr.rt == nil {
			return "无运行时"
		}
		summary := cr.rt.CheckpointsSummary()
		if len(summary) == 0 {
			return "无检查点（task/goal 执行每轮自动落盘）"
		}
		var lines []string
		for _, m := range summary {
			done := "执行中"
			if m["done"].(bool) {
				done = "已完成"
			}
			lines = append(lines, fmt.Sprintf("%s - %s 轮%d %s (%s)",
				m["run_id"], m["mode"], m["round"], done, m["updated_at"]))
		}
		return "检查点（对标 LangGraph checkpointer；goal 续跑自动恢复最新快照）:\n" + strings.Join(lines, "\n")
	}
	cr.descs["/checkpoints"] = "列出多 Agent 运行检查点"

	cr.commands["/skills"] = func(args []string) string {
		return "__SKILL_SELECT__"
	}
	cr.descs["/skills"] = "交互式技能选择（多选）"

	// 技能市场：/market [查询词] 列出/搜索；/market install <name> 安装到项目技能目录
	cr.commands["/market"] = func(args []string) string {
		if cr.rt == nil || cr.rt.Market == nil {
			return "技能市场未初始化"
		}
		if len(args) > 0 && args[0] == "install" {
			if len(args) < 2 {
				return "用法: /market install <name>（先 /market 查看可用技能）"
			}
			dir, err := cr.rt.InstallSkill(args[1])
			if err != nil {
				return "安装失败: " + err.Error()
			}
			return "已安装技能 " + args[1] + " → " + dir + "/SKILL.md（/skills 选择启用）"
		}
		q := strings.Join(args, " ")
		items := cr.rt.Market.Search(q)
		if len(items) == 0 {
			return "市场无匹配技能（内置 " + strconv.Itoa(len(cr.rt.Market.Search(""))) + " 个；可 AddRemote 扩展远程 catalog）"
		}
		var b strings.Builder
		fmt.Fprintf(&b, "技能市场（%d 项）:\n", len(items))
		for _, it := range items {
			fmt.Fprintf(&b, "  %-24s %s\n", it.Name, it.Description)
		}
		b.WriteString("安装: /market install <name>")
		return b.String()
	}
	cr.descs["/market"] = "技能市场：列出/搜索技能；install <name> 安装"

	// 工具沙箱：/sandbox 状态；commit 合并写回真实文件；discard 丢弃全部改动
	cr.commands["/sandbox"] = func(args []string) string {
		if cr.rt == nil {
			return "需连接运行时"
		}
		if len(args) > 0 {
			switch args[0] {
			case "commit":
				n, err := cr.rt.SandboxCommit()
				if err != nil {
					return "合并失败: " + err.Error()
				}
				return fmt.Sprintf("已把 %d 个沙箱改动合并写回真实文件系统", n)
			case "discard":
				if err := cr.rt.SandboxDiscard(); err != nil {
					return "丢弃失败: " + err.Error()
				}
				return "沙箱镜像已丢弃，后续写操作直接作用于真实文件"
			}
		}
		return cr.rt.SandboxStatus()
	}
	cr.descs["/sandbox"] = "工具沙箱状态；commit 合并写回 / discard 丢弃改动"

	cr.commands["/config"] = func(args []string) string {
		if cr.rt == nil {
			return "需连接运行时"
		}
		return cr.rt.ConfigSummary()
	}
	cr.descs["/config"] = "查看生效配置（来源: 配置文件+默认值）"

	cr.commands["/trace"] = func(args []string) string {
		if cr.rt == nil || cr.rt.Trace == nil {
			return "trace 未启用（config: trace.enabled）"
		}
		n := 30
		if len(args) > 0 {
			if v, err := strconv.Atoi(args[0]); err == nil && v > 0 && v <= 500 {
				n = v
			}
		}
		entries := cr.rt.Trace.Recent(n)
		if len(entries) == 0 {
			return "暂无 trace 记录"
		}
		lines := make([]string, 0, len(entries)+1)
		lines = append(lines, fmt.Sprintf("最近 %d 条 trace（.contextloom/trace/）:", len(entries)))
		for _, e := range entries {
			when := e.TS.Format("15:04:05")
			extra := ""
			if e.Tool != "" {
				extra = " 工具=" + e.Tool
			}
			if e.Ms > 0 {
				extra += fmt.Sprintf(" %dms", e.Ms)
			}
			if e.Err != "" {
				extra += " 错误=" + truncStr(e.Err, 60)
			}
			lines = append(lines, fmt.Sprintf("  %s [%s] %s 轮#%d%s", when, e.Mode, e.Event, e.Round, extra))
		}
		return strings.Join(lines, "\n")
	}
	cr.descs["/trace"] = "查看最近 trace 记录（可带条数，如 /trace 50）"
}

// ── /think 多轮思考策略（设计 §3.8；轮次面与 /effort 参数面正交）──

// thinkRuntime 可选能力接口：runtime 并行任务落盘会话级思考覆盖 API 后自动启用
// （SetThinkingPolicy/SetVerifyPass 仅依赖内建类型，签名稳定、可提前断言）。
// 用类型断言探测而非硬依赖 runtime 包新 API，保证本包在并行任务落盘前独立编译；
// 未落盘时命令回显"运行时未就绪"。
type thinkRuntime interface {
	SetThinkingPolicy(p string) error
	SetVerifyPass(v string) error
}

// thinkSubCmds /think 子命令合法取值（本地校验，非法值不打扰 runtime）。
var thinkSubCmds = map[string][]string{
	"policy": {"off", "auto", "strict", "clear"},
	"verify": {"off", "auto", "on", "clear"},
}

// thinkUsage /think 用法总览（非法子命令/缺参统一回显）。
const thinkUsage = "用法: /think [policy off|auto|strict|clear] [verify off|auto|on|clear] [rounds <0-20>] [nudge on|off] [effort none|escalate]"

// handleThink /think 命令族入口：
//
//	/think                    展示策略全貌（配置值 + 可选实时进度）
//	/think policy <v>         会话级覆盖（runtime API；未落盘回显未就绪）
//	/think verify <v>         会话级覆盖（同上）
//	/think rounds <n>         配置级：最少思考轮数（ApplyConfigPatch）
//	/think nudge <on|off>     配置级：续思提示开关（ApplyConfigPatch）
//	/think effort <v>         配置级：验证轮升档（ApplyConfigPatch）
func (cr *CommandRegistry) handleThink(args []string) string {
	if cr.rt == nil {
		return "未连接运行时"
	}
	if len(args) == 0 {
		return cr.thinkOverview()
	}
	switch args[0] {
	case "policy", "verify":
		return cr.thinkSetOverride(args[0], args[1:])
	case "rounds":
		if len(args) < 2 {
			return "用法: /think rounds <0-20>（最少思考轮数，0=不强制）"
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 0 || n > 20 {
			return "非法数值: " + args[1] + "（需为 0-20 整数）"
		}
		if _, err := cr.rt.ApplyConfigPatch(map[string]any{"thinking.min_think_rounds": n}); err != nil {
			return "设置失败: " + err.Error()
		}
		return fmt.Sprintf("已设置 最少思考轮数: %d（配置级，持久化）", n)
	case "nudge":
		if len(args) < 2 || (args[1] != "on" && args[1] != "off") {
			return "用法: /think nudge <on|off>（复杂任务轮间续思提示开关）"
		}
		v := args[1] == "on"
		if _, err := cr.rt.ApplyConfigPatch(map[string]any{"thinking.nudge_enabled": v}); err != nil {
			return "设置失败: " + err.Error()
		}
		if v {
			return "已设置 nudge: 开（复杂任务轮间注入续思提示）"
		}
		return "已设置 nudge: 关"
	case "effort":
		if len(args) < 2 || (args[1] != "none" && args[1] != "escalate") {
			return "用法: /think effort <none|escalate>（验证轮升档）"
		}
		if _, err := cr.rt.ApplyConfigPatch(map[string]any{"thinking.round_effort_policy": args[1]}); err != nil {
			return "设置失败: " + err.Error()
		}
		if args[1] == "escalate" {
			return "已设置 验证轮升档: escalate（验证轮按封顶档位下发 effort）"
		}
		return "已设置 验证轮升档: none（不升档，保持现状）"
	default:
		return thinkUsage
	}
}

// thinkSetOverride 设置/清除会话级思考覆盖（policy/verify）。
// 校验顺序：本地枚举校验 → 可选接口断言（未落盘回显未就绪）→ 调用 runtime。
func (cr *CommandRegistry) thinkSetOverride(kind string, vals []string) string {
	opts := thinkSubCmds[kind]
	label := "思考策略"
	if kind == "verify" {
		label = "验证轮"
	}
	if len(vals) == 0 {
		return fmt.Sprintf("用法: /think %s <%s>", kind, strings.Join(opts, "|"))
	}
	v := vals[0]
	if !thinkInList(v, opts) {
		return fmt.Sprintf("非法取值: %s（可用: %s）", v, strings.Join(opts, "/"))
	}
	tr, ok := any(cr.rt).(thinkRuntime)
	if !ok {
		return "运行时未就绪（多轮思考接口未加载），当前仅支持配置级设置: /think rounds|nudge|effort"
	}
	var err error
	if kind == "policy" {
		err = tr.SetThinkingPolicy(v)
	} else {
		err = tr.SetVerifyPass(v)
	}
	if err != nil {
		return "设置失败: " + err.Error()
	}
	if v == "clear" {
		return fmt.Sprintf("已清除%s覆盖（恢复配置值）", label)
	}
	out := fmt.Sprintf("已设置 %s: %s（会话覆盖，settings.json 持久化）", label, v)
	if kind == "policy" && v != "off" {
		if hint := thinkOrthogonalHint(cr.rt.Config.CLIDefaults.Effort); hint != "" {
			out += "\n" + hint
		}
	}
	return out
}

// thinkOverview /think 无参：展示策略全貌（配置值 + 可选实时进度）。
// 实时状态经 runtime ModelInfo 的 thinking 可选通道读取（设计 §3.8）；
// 拿不到（runtime 未落盘/任务未运行）时展示配置值——不依赖并行任务进度。
func (cr *CommandRegistry) thinkOverview() string {
	c := cr.rt.Config.Thinking
	cfgPolicy := c.Policy
	if cfgPolicy == "" {
		cfgPolicy = "off"
	}
	verify := c.VerifyPass
	if verify == "" {
		verify = "off"
	}
	live := thinkLiveStatus(cr.rt)
	// 生效策略：实时状态优先（含会话覆盖后的实际值），否则配置值。
	policy := cfgPolicy
	if p := mapStr(live, "policy"); p != "" {
		policy = p
	}
	if v := mapStr(live, "verify"); v != "" {
		verify = v
	}
	// 来源：runtime 提供 source 则按其口径；否则按"生效≠配置 → 会话覆盖"推导。
	src := "配置"
	if s := mapStr(live, "source"); s != "" {
		src = thinkSourceLabel(s)
	} else if policy != cfgPolicy {
		src = "会话覆盖"
	}
	effortSeg := c.RoundEffortPolicy
	if effortSeg == "" {
		effortSeg = "none"
	}
	if effortSeg == "escalate" {
		cap := c.RoundEffortCap
		if cap == "" {
			cap = "max"
		}
		effortSeg = "escalate(封顶" + cap + ")"
	}
	maxRounds := c.MaxThinkRounds
	if maxRounds <= 0 {
		maxRounds = 3 // 运行时 Normalize 同口径
	}
	nudgeSeg := "关"
	if c.NudgeEnabled {
		n := c.NudgeMax
		if n <= 0 {
			n = 2 // 运行时 Normalize 同口径
		}
		nudgeSeg = fmt.Sprintf("开(上限%d)", n)
	}
	out := fmt.Sprintf("思考策略: %s（来源: %s）\n最少轮数: %d | 续轮上限: %d | 验证轮: %s | nudge: %s | 升档: %s",
		policy, src, c.MinThinkRounds, maxRounds, verify, nudgeSeg, effortSeg)
	if r := mapInt(live, "round"); r > 0 {
		mr := mapInt(live, "maxRound")
		if mr <= 0 {
			mr = maxRounds
		}
		line := fmt.Sprintf("\n进度: 轮 %d/%d", r, mr)
		if b, _ := live["verifying"].(bool); b {
			line += " · 验证中"
		}
		out += line
	}
	if policy != "off" {
		if hint := thinkOrthogonalHint(cr.rt.Config.CLIDefaults.Effort); hint != "" {
			out += "\n" + hint
		}
	}
	return out
}

// thinkOrthogonalHint /effort off 与思考策略的矛盾提示（设计 §3.8：effort=off
// 优先、不发思考字段）。effort != off 时返回 ""。
func thinkOrthogonalHint(effort string) string {
	if effort == "off" {
		return "提示: 当前思考档位 off（/effort off），思考不会实际发生；如需启用请先 /effort medium"
	}
	return ""
}

// thinkLiveStatus 读取 runtime 多轮思考实时状态（可选通道，拿不到返回 nil）：
// ModelInfo() 的 "thinking" 字段（设计 §3.8：runtime 侧扩展的稳定 map 通道）。
// 并行任务未落盘/无活动任务时自然缺失，调用方回退配置展示。
func thinkLiveStatus(rt *runtime.Runtime) map[string]any {
	if rt == nil {
		return nil
	}
	mi := rt.ModelInfo()
	if mi == nil {
		return nil
	}
	return toMapAny(mi["thinking"])
}

// toMapAny 把任意 JSON 化值规整为 map（runtime 侧可能放 map[string]any 或
// 结构体；结构体经 JSON 往返兜底，避免与并行任务的类型选择硬耦合）。
func toMapAny(v any) map[string]any {
	if v == nil {
		return nil
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}

// mapStr 从状态 map 安全取字符串（缺失/类型不符返回 ""）。
func mapStr(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// mapInt 从状态 map 安全取整数（兼容 int / int64 / float64 / json.Number）。
func mapInt(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return 0
}

// thinkSourceLabel 来源英文口径 → 中文展示（未知值原样返回）。
func thinkSourceLabel(s string) string {
	switch s {
	case "override", "session":
		return "会话覆盖"
	case "config":
		return "配置"
	case "default", "off":
		return "默认"
	}
	return s
}

// thinkInList 判断值是否命中合法选项清单（/think 本地枚举校验）。
func thinkInList(s string, opts []string) bool {
	for _, opt := range opts {
		if s == opt {
			return true
		}
	}
	return false
}

func (cr *CommandRegistry) Execute(input string) (string, bool) {
	if !strings.HasPrefix(input, "/") {
		return "", false
	}
	parts := strings.Fields(input)
	if len(parts) == 0 {
		return "", false
	}
	cmd := parts[0]
	args := parts[1:]
	if h, ok := cr.commands[cmd]; ok {
		r := h(args)
		if r == "quit" {
			return "已退出", true
		}
		return r, false
	}
	return "未知命令: " + cmd, false
}

func (cr *CommandRegistry) AllEntries() []CommandEntry {
	var entries []CommandEntry
	for cmd := range cr.commands {
		desc := cr.descs[cmd]
		if desc == "" {
			desc = "(无描述)"
		}
		entries = append(entries, CommandEntry{Name: cmd, Desc: desc})
	}
	return entries
}

func (cr *CommandRegistry) Commands() []string {
	cmds := make([]string, 0, len(cr.commands))
	for cmd := range cr.commands {
		cmds = append(cmds, cmd)
	}
	return cmds
}

// IsCommand 判断给定命令名（含 "/" 前缀，如 "/load"）是否为已注册命令。
// 供 TUI 判断聊天输入框整行粘贴/快速输入的斜杠文本是否为真实命令。
func (cr *CommandRegistry) IsCommand(name string) bool {
	if cr == nil {
		return false
	}
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	_, ok := cr.commands[name]
	return ok
}

func (cr *CommandRegistry) MatchPrefix(input string) []string {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	var matches []string
	for cmd := range cr.commands {
		if strings.HasPrefix(cmd, input) {
			matches = append(matches, cmd)
		}
	}
	return matches
}

func (cr *CommandRegistry) MatchPrefixEntries(input string) []CommandEntry {
	if !strings.HasPrefix(input, "/") {
		return nil
	}
	var matches []CommandEntry
	for cmd := range cr.commands {
		if strings.HasPrefix(cmd, input) {
			desc := cr.descs[cmd]
			if desc == "" {
				desc = "(无描述)"
			}
			matches = append(matches, CommandEntry{Name: cmd, Desc: desc})
		}
	}
	return matches
}

func (cr *CommandRegistry) AutoComplete(input string) string {
	if !strings.HasPrefix(input, "/") {
		return input
	}
	matches := cr.MatchPrefix(input)
	if len(matches) == 1 {
		return matches[0] + " "
	}
	if len(matches) > 1 {
		common := matches[0]
		for _, m := range matches[1:] {
			common = commonPrefix(common, m)
		}
		if common != input {
			return common
		}
	}
	return input
}

func commonPrefix(a, b string) string {
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	i := 0
	for i < minLen && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func truncStr(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-3]) + "..."
}

// fmtTokens 把 token 数格式化为人类可读文本（0 显示"不限制"，千位以上用 k 缩写）。
// 用于 /effort 与 /maxlen 的回显，避免用户看到裸数字 393216 难以换算。
func fmtTokens(n int) string {
	if n <= 0 {
		return "不限制"
	}
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n%1000 == 0 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
