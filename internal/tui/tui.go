package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	bubbletea "github.com/charmbracelet/bubbletea"
	"github.com/contextloom/contextloom-agent/internal/runtime"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 消息类型 ──
// gen 为流代际号：每条流消息携带发起时的 streamGen，Update 只接受
// 与当前代际一致的消息——生成中插入新对话会取消旧流并推进代际，
// 旧流残留的 streamMsg/streamDoneMsg 到达时被丢弃，不会污染新对话
// （"插入对话导致内容重新开始/串台"根因修复）。
type streamMsg struct {
	token string
	gen   int
}
type streamErrMsg struct {
	err string
	gen int
}
type streamDoneMsg struct{ gen int }
type tickMsg struct{}

type inputMode int

const (
	modeNormal inputMode = iota
	modeCommand
	modeChat
	modeSkillSelect
)

type App struct {
	Runtime       *runtime.Runtime
	ConfirmReqCh  <-chan *tools.ConfirmDecision
	ConfirmRespCh chan<- confirmResponse
	model         *model
}

// Run 启动 TUI 程序。退出后（alt-screen 已恢复）可通过 SessionID() 获取会话 ID。
func (a *App) Run() error {
	a.model = initialModel(a.Runtime)
	if a.ConfirmReqCh != nil && a.ConfirmRespCh != nil {
		a.model.SetConfirmChannels(a.ConfirmReqCh, a.ConfirmRespCh)
	}
	// 鼠标捕获：全平台启用（Windows 也可点击展开/折叠思考块、滚轮滚动、
	// 点击历史区切换焦点——焦点机制见 handleMouse/handleKey）。
	// ★ Windows 关键约束（实测根因，勿回退）：bubbletea v1.1.0 在 Windows
	// 下鼠标/键盘事件不走 VT/SGR 序列，而是读取【原生 INPUT_RECORD】
	// （readConInputs → MouseEventRecord）。鼠标事件依赖控制台
	// ENABLE_MOUSE_INPUT 标志——【绝不能清除】！旧实现基于"SGR"错误前提
	// 清除它，导致点击/滚轮事件完全收不到：焦点永远无法激活、↑↓ 永远回填
	// 输入历史。startVTInputGuard() 只守护必需位（MOUSE|WINDOW|EXTENDED）
	// 并清除 QuickEdit，绝不添加 ENABLE_VIRTUAL_TERMINAL_INPUT（原生路径
	// 不需要；添加后 conhost 输出 SGR 文本乱码，三重防御兜底）。
	// 运行期间【绝不能】恢复 QuickEdit（否则点击被拦截为拖选）。
	// 退出 TUI 时 bubbletea 自动 restore 原始控制台模式（QuickEdit 恢复），
	// 原生拖选复制能力自动回来，无需手动干预。
	// 运行期间复制方式：
	//   - Windows Terminal：按住 Shift 拖选复制（推荐，鼠标点击不受影响）
	//   - cmd/conhost：右键 → 标记 后拖选复制（或退出 TUI 后正常复制）
	// 键盘替代仍保留：滚动 PgUp/PgDown，思考块展开/折叠 t 键。
	opts := []bubbletea.ProgramOption{bubbletea.WithAltScreen()}
	// 鼠标捕获全平台启用（含 Windows）：Windows 走原生 MouseEventRecord
	// （依赖 vtinput_windows.go 守护 ENABLE_MOUSE_INPUT）；其他平台走 SGR。
	// 思考块点击展开/折叠、滚轮滚动、点击历史区焦点切换均可用；
	// 键盘兜底（t 键/PgUp/PgDown）保留。
	opts = append(opts, bubbletea.WithMouseCellMotion())
	// Windows 下启用 VT 输入解析（conhost 鼠标支持）；PowerShell 7 下
	// 自动切换为周期守护（PSReadLine 会重置控制台模式）；其他平台空实现零开销。
	startVTInputGuard()
	p := bubbletea.NewProgram(a.model, opts...)
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

// SessionID 返回当前会话 ID，供调用方在退出后打印。
func (a *App) SessionID() string {
	if a.model != nil {
		return a.model.currentSessionID
	}
	return ""
}

// subTaskPanel 子任务面板项（task 模式下展示各 spawn_task 子 Agent 实时状态）
type subTaskPanel struct {
	ID        string  // 子 Agent ID（sub-1）
	Task      string  // 子任务描述
	Directive string  // 指令类型
	Employee  string  // 命中的员工显示名（可空；空时展示 ID）
	Status    string  // running / done / failed
	LastLine  string  // 最新输出行（实时更新）
	Score     float64 // 监工评分（完成后）
}

type model struct {
	rt       *runtime.Runtime
	commands *CommandRegistry
	width    int
	height   int

	mode      inputMode
	input     string
	chatInput string

	messages []ChatMessage
	cursor   int

	streaming   bool
	streamBuf   string
	tokenCount  int
	streamCh    <-chan string
	streamErrCh <-chan error
	streamGen   int // 流代际号：每次发起新流自增；旧流消息按 gen 丢弃（防插入对话串台）

	history          []string
	historyIdx       int
	historyDraft     string // 翻历史前保存的输入草稿，↓ 回到最新时可恢复（防输入丢失）
	currentSessionID string
	completions      []CommandEntry
	complIdx         int
	tick             int
	scrollOffset     int // 滚动偏移：0=底部最新，>0=向上滚动

	modelID         string
	reasoningEffort string
	threshold       float64
	lastKeyTime     time.Time
	pasteSeq        int
	pasteChars      int                // 80ms 窗口内累计到达的字符数（粘贴启发式：按字符而非按键次数判定）
	pasting         bool               // bracketed paste 激活中（msg.Paste 标记）
	inputScroll     int                // 输入区滚动偏移：0=跟随光标，>0=向上滚动查看
	focusHistory    bool               // true=焦点在历史区（↑↓/滚轮滚动历史）
	viewInputTop    int                // 上一帧输入区顶部行号（View 计算，handleMouse 复用）
	cancelFn        context.CancelFunc // Agent 中断取消函数
	interrupted     bool               // 中断标记（二次 Ctrl+C 强制退出）

	// 输入编辑状态
	keyNewline string // 当前推荐的换行键: "ctrl+j" / "alt+enter"（配置或终端检测）
	termHint   string // 终端兼容性提示（检测到占用时非空）
	showHelp   bool   // 帮助面板开关（? 键）
	cursorCol  int    // ↑↓ 多行移动时的目标列（-1=未设置）

	// 鼠标点击映射：屏幕行号 → 消息索引（仅 MsgThinking 的首行）
	clickMap map[int]int

	// 上一帧 View() 计算的布局参数：鼠标事件对应的是屏幕当前显示的内容
	// （即上一帧渲染结果），handleMouse 必须复用这些参数而非自行重算——
	// 自行重算曾导致与 View() 不一致（状态栏实际 2 行/对话框行数未计入、
	// resize 后缓存行数偏差），点击坐标错位 1 行以上，思考块点击失效。
	viewBodyH int // 上一帧消息区高度（View 中 bodyH）
	viewStart int // 上一帧可见窗口起始全局行号（View 中 start）

	// 消息渲染缓存：msgIdx → 渲染行（滚动/重绘时避免重复 lipgloss 全量渲染）
	// 签名含 Content/Collapsed/Streaming/Type/ToolName，变化时自动失效重渲染。
	// 历史消息 append-only，滚动时全部命中缓存，大幅降低 View() 开销。
	renderCache map[int]*msgRenderCache

	// task 模式子任务面板
	subTasks []subTaskPanel

	// 工具谨慎性确认
	confirmDialog *ConfirmDialog
	confirmReqCh  <-chan *tools.ConfirmDecision
	confirmRespCh chan<- confirmResponse

	// 技能选择 UI 状态
	skillSearch   string   // 技能搜索关键词
	skillCursor   int      // 技能列表选中索引
	skillSelected []bool   // 各技能选中状态（按全量排序）
	skillAll      []string // 所有可用技能名（全量缓存）
	skillFiltered []int    // 过滤后的索引列表（指向 skillAll/skillSelected）
}

func initialModel(rt *runtime.Runtime) *model {
	mid, reff, thr := "default-model", "medium", float64(50)
	if rt != nil && rt.Config != nil {
		mid = rt.CurrentModel()
		reff = rt.Config.CLIDefaults.Effort
		thr = rt.Config.Harness.ScoreThreshold
	}
	m := &model{
		rt:              rt,
		commands:        NewCommandRegistry(rt),
		modelID:         mid,
		reasoningEffort: reff,
		threshold:       thr,
		cursorCol:       -1,
		renderCache:     make(map[int]*msgRenderCache),
	}
	// 换行键：配置 > 终端自动检测
	autoKey, autoHint := detectTerminal()
	kn := "auto"
	if rt != nil && rt.Config != nil && rt.Config.CLIDefaults.KeyNewline != "" {
		kn = rt.Config.CLIDefaults.KeyNewline
	}
	if kn == "auto" {
		m.keyNewline = autoKey
		m.termHint = autoHint
	} else {
		m.keyNewline = kn // 用户显式指定（ctrl+j / alt+enter）
		m.termHint = ""   // 显式配置后不再提示
	}
	m.InitConfirmDialog()
	m.mode = modeChat // 默认进入聊天模式，直接可以输入
	m.loadInputHistory()
	if rt != nil {
		// 终端兼容性自检提示（仅检测到占用冲突时，一次性显示）
		if m.termHint != "" {
			m.appendMsg(sysMsg("⌨ " + m.termHint + "（? 查看全部快捷键）"))
		}
		// 如果已有加载的会话（通过 --memory-load），复用其 ID
		// 否则创建新会话
		if rt.GetCurrentSessionID() != "" {
			m.currentSessionID = rt.GetCurrentSessionID()
			// 从记忆系统加载历史消息
			m.loadHistoryMessages()
		} else {
			m.currentSessionID = rt.NewSession("").UUID
		}
	}
	return m
}

// detectTerminal 检测当前终端并返回推荐的换行键与兼容性提示。
// 实测结论（bubbletea v1.1.0）：
//   - VS Code 集成终端：Ctrl+J 被 IDE 占为"切换面板" → 必须用 Alt+Enter
//   - Windows Terminal / 控制台：Alt+Enter 被系统占为"全屏切换" → 必须用 Ctrl+J
//   - 其他 Unix 终端（iTerm2/Terminal.app/xterm）：Ctrl+J 与 Alt+Enter 均可靠
func detectTerminal() (key string, hint string) {
	prog := os.Getenv("TERM_PROGRAM")
	wt := os.Getenv("WT_SESSION")
	term := os.Getenv("TERM")
	switch {
	case prog == "vscode" || prog == "vscode-insiders" || strings.Contains(term, "vscode"):
		return "alt+enter", "检测到 VS Code 终端：Ctrl+J 被 IDE 占用，换行请用 Alt+Enter（macOS: Option+Enter）"
	case wt != "":
		return "ctrl+j", "检测到 Windows Terminal：Alt+Enter 被系统占用（全屏切换），换行请用 Ctrl+J"
	case prog == "iTerm.app" || prog == "Apple_Terminal" || strings.Contains(term, "xterm") || strings.Contains(term, "screen") || strings.Contains(term, "tmux"):
		return "ctrl+j", ""
	case goruntime.GOOS == "windows":
		return "ctrl+j", "Windows 下 Alt+Enter 被系统占用，换行请用 Ctrl+J"
	default:
		return "alt+enter", ""
	}
}

// newlineKeyLabel 返回状态栏展示用的换行键名
func (m *model) newlineKeyLabel() string {
	if m.keyNewline == "ctrl+j" {
		return "Ctrl+J"
	}
	return "Alt+Enter"
}

func (m *model) Init() bubbletea.Cmd {
	return bubbletea.Tick(120*time.Millisecond, func(t time.Time) bubbletea.Msg { return tickMsg{} })
}

// ── Update ──

func (m *model) Update(msg bubbletea.Msg) (bubbletea.Model, bubbletea.Cmd) {
	switch msg := msg.(type) {
	case tickMsg:
		m.CheckConfirmRequest()
		if m.streaming {
			m.tick++
		}
		return m, bubbletea.Tick(120*time.Millisecond, func(t time.Time) bubbletea.Msg { return tickMsg{} })

	case bubbletea.KeyMsg:
		return m.handleKey(msg)
	case bubbletea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case bubbletea.MouseMsg:
		return m.handleMouse(msg)
	case streamMsg:
		// 代际过滤：旧流（已被新对话取代）的残留消息直接丢弃——
		// 否则旧流的 token 会写入新对话的 streamBuf（内容串台/重新开始）。
		if msg.gen != m.streamGen {
			return m, nil
		}
		token := msg.token
		// 子任务面板信号（§ 前缀）— 路由到 task 面板，不进入主消息流
		if strings.HasPrefix(token, "§") {
			m.handleSubTaskSignal(token)
			return m, m.readNextStreamToken()
		}
		// 工具调用/结果消息 — 直接作为独立消息显示
		if strings.HasPrefix(token, "⚡ ") || strings.HasPrefix(token, "✔ ") || strings.HasPrefix(token, "✗ ") {
			// 之前的流式内容先存为消息
			if m.streamBuf != "" {
				body, thinks := splitThinkTags(m.streamBuf)
				for _, t := range thinks {
					if strings.TrimSpace(t) == "" {
						continue
					}
					th := thinkMsg()
					th.Content = t
					th.Collapsed = true
					th.Streaming = false
					m.appendMsg(th)
				}
				if body != "" {
					m.appendMsg(asstMsg(body))
				}
				m.streamBuf = ""
			}
			// 工具消息直接添加
			if strings.HasPrefix(token, "⚡ ") {
				name := strings.TrimPrefix(token, "⚡ ")
				m.appendMsg(toolCall(strings.TrimSpace(name), ""))
			} else if strings.HasPrefix(token, "✔ ") {
				parts := strings.SplitN(token, ": ", 2)
				name := strings.TrimPrefix(parts[0], "✔ ")
				result := ""
				if len(parts) > 1 {
					result = parts[1]
				}
				m.appendMsg(toolOK(name, result))
			} else if strings.HasPrefix(token, "✗ ") {
				m.appendMsg(errMsg(strings.TrimPrefix(token, "✗ ")))
			}
			m.tokenCount++
			return m, m.readNextStreamToken()
		}
		// 普通 token — 累积到 streamBuf
		m.streamBuf += token
		m.tokenCount++
		return m, m.readNextStreamToken()
	case streamErrMsg:
		if msg.gen != m.streamGen {
			return m, nil // 旧流残留错误：丢弃（新对话不受影响）
		}
		m.streaming = false
		// 错误前已产生的部分回复也写入历史（避免整轮丢失导致聊天记录断档）
		if m.streamBuf != "" {
			m.appendMsg(asstMsg(m.streamBuf))
			if m.rt != nil {
				m.rt.AddToHistory("assistant", m.streamBuf)
			}
			m.streamBuf = ""
		}
		m.appendMsg(errMsg(msg.err))
	case streamDoneMsg:
		if msg.gen != m.streamGen {
			return m, nil // 旧流残留完成：丢弃（新对话的流仍在进行）
		}
		m.streaming = false
		m.InitConfirmDialog()
		m.mode = modeChat
		m.scrollOffset = 0 // 新消息到达，重置滚动到底部
		var finalBody string
		if m.streamBuf != "" {
			body, thinks := splitThinkTags(m.streamBuf)
			for _, t := range thinks {
				if strings.TrimSpace(t) == "" {
					continue // 跳过空思考
				}
				th := thinkMsg()
				th.Content = t
				th.Collapsed = true
				th.Streaming = false
				m.appendMsg(th)
			}
			if body != "" {
				m.appendMsg(asstMsg(body))
				finalBody = body
			} else if len(thinks) > 0 {
				// 兜底：正文为空但思考非空——R 系列模型偶发把结论全部写进
				// reasoning_content、content 为空（上游行为）。若不处理，回答
				// 只存在于折叠思考块中，正文区空无一物。取思考尾段提升为正文。
				if promoted, ok := runtime.PromoteThinkBody(thinks); ok {
					m.appendMsg(asstMsg(promoted))
					finalBody = promoted
				}
			} else if len(thinks) == 0 {
				m.appendMsg(asstMsg(m.streamBuf))
				finalBody = m.streamBuf
			}
		}
		m.streamBuf = ""
		// 监工评估闭环：评估结果决定是否纳入对话历史
		overseerPassed := true
		overseerScore := types.Score(0)
		var overseerReport types.OverseerReport
		if m.rt != nil && finalBody != "" && m.rt.HasOverseer() {
			passed, score, report := m.rt.OverseerEvaluate(finalBody)
			overseerPassed = passed
			overseerScore = score
			overseerReport = report
			icon := "✅"
			status := "通过"
			if !passed {
				icon = "⚠"
				status = "未通过 · 已记录"
			}
			m.appendMsg(sysMsg(fmt.Sprintf(
				"🛡 监工评估 %s %s · 评分 %.1f/100（完整:%.0f 测试:%.0f 质量:%.0f 副作用:%.0f）",
				icon, status, score, report.Completeness, report.TestCoverage, report.CodeQuality, report.NoSideEffect)))
		}
		// 无论监工评估是否通过，回复都纳入对话历史：
		// 用户已看到该回复，若不入史则重进会话后聊天记录断档（user 无配对 assistant），
		// 且 AI 不知道自己说过什么，反而破坏上下文连续性。
		// 监工评估结果仅作为界面提示保留（未通过时用户可自行判断是否继续）。
		if m.rt != nil && finalBody != "" {
			m.rt.AddToHistory("assistant", finalBody)
		}
		// goal 模式：持久目标循环 —— 每轮结束更新状态机，未完成自动进入下一轮
		if m.rt != nil && finalBody != "" && m.rt.GetMode() == "goal" && m.rt.GoalSession() != nil && !m.rt.GoalSession().Done() {
			cont, reason := m.rt.GoalOnRoundEnd(finalBody, overseerPassed, overseerScore, overseerReport)
			m.appendMsg(sysMsg("🎯 " + reason))
			if cont {
				// 自动发起下一轮（continuation 驱动，不经过用户输入框）
				cmd := m.startGoalRound()
				if cmd != nil {
					return m, cmd
				}
			}
		}
		// 工作结束提示
		doneMsg := ChatMessage{
			Type:    MsgSystem,
			Content: "── 工作完成 " + time.Now().Format("15:04:05") + " ──",
		}
		m.appendMsg(doneMsg)
	}
	return m, nil
}

// startGoalRound 发起目标循环的下一轮（自动续跑，不经过用户输入框）。
// 输入为 GoalSession.Continuation（上轮总结 + 下一步指令），由 ProcessGoalRound 注入。
func (m *model) startGoalRound() bubbletea.Cmd {
	if m.rt == nil {
		return nil
	}
	gs := m.rt.GoalSession()
	if gs == nil || gs.Done() {
		return nil
	}
	m.streaming = true
	m.streamGen++ // 推进代际：新一轮流（旧流残留消息按 gen 丢弃）
	m.scrollOffset = 0
	m.streamBuf = ""
	m.tick = 0
	m.tokenCount = 0
	m.interrupted = false
	m.subTasks = nil // 清空上一轮子任务面板
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFn = cancel
	ch, errCh, err := m.rt.ProcessGoalRound(ctx)
	if err != nil {
		m.streaming = false
		m.appendMsg(errMsg(err.Error()))
		return nil
	}
	m.streamCh = ch
	m.streamErrCh = errCh
	return m.readNextStreamToken()
}

// handleSubTaskSignal 解析 § 前缀的子任务面板信号，更新 m.subTasks 面板数据。
// 协议：
//
//	§START|<subID>|<directive>|<employee>|<task>  创建/重置面板项（employee 可空=旧格式）
//	§TOK|<subID>|<token>                 累积更新 LastLine（取最后一行）
//	§DONE|<subID>|<score>                标记完成 + 评分
//	§FAIL|<subID>|<err>                  标记失败
func (m *model) handleSubTaskSignal(token string) {
	// 兼容新旧两种 §START 格式：
	//   新: §START|sub-1|CODE_AGENT|coder|实现登录函数   （5 段）
	//   旧: §START|sub-1|CODE_AGENT|实现登录函数         （4 段，SplitN(_,4) → 第 4 段是 task）
	partsHard := strings.SplitN(token, "|", 5)
	event := strings.TrimPrefix(partsHard[0], "§")

	if event == "START" && len(partsHard) == 5 {
		// 新格式：directive=parts[2], employee=parts[3], task=parts[4]
		m.applySubTaskStart(partsHard[1], partsHard[2], partsHard[3], partsHard[4])
		return
	}
	parts := strings.SplitN(token, "|", 4)
	if len(parts) < 2 {
		return
	}
	subID := parts[1]

	switch event {
	case "START":
		if len(parts) < 4 {
			return
		}
		m.applySubTaskStart(subID, parts[2], "", parts[3])

	case "TOK":
		if len(parts) < 3 {
			return
		}
		content := parts[2]
		for i := range m.subTasks {
			if m.subTasks[i].ID == subID {
				m.subTasks[i].LastLine = appendLastLine(m.subTasks[i].LastLine, content)
				return
			}
		}

	case "DONE":
		score := float64(0)
		if len(parts) >= 3 {
			score, _ = strconv.ParseFloat(parts[2], 64)
		}
		for i := range m.subTasks {
			if m.subTasks[i].ID == subID {
				m.subTasks[i].Status = "done"
				m.subTasks[i].Score = score
				if m.subTasks[i].LastLine == "" || m.subTasks[i].LastLine == "启动中…" {
					m.subTasks[i].LastLine = fmt.Sprintf("完成 · 评分 %.0f/100", score)
				}
				return
			}
		}

	case "FAIL":
		errStr := ""
		if len(parts) >= 3 {
			errStr = parts[2]
		}
		for i := range m.subTasks {
			if m.subTasks[i].ID == subID {
				m.subTasks[i].Status = "failed"
				m.subTasks[i].LastLine = "失败: " + truncateRunes(errStr, 40)
				return
			}
		}
	}
}

// applySubTaskStart 创建/重置面板项（§START 的公共处理，兼容新旧格式）。
// employee 为空时保持旧行为（面板显示 subID）；非空时面板以员工名为主显示。
func (m *model) applySubTaskStart(subID, directive, employee, task string) {
	for i := range m.subTasks {
		if m.subTasks[i].ID == subID {
			m.subTasks[i].Status = "running"
			m.subTasks[i].Directive = directive
			m.subTasks[i].Employee = employee
			m.subTasks[i].Task = task
			m.subTasks[i].LastLine = "启动中…"
			m.subTasks[i].Score = 0
			return
		}
	}
	m.subTasks = append(m.subTasks, subTaskPanel{
		ID:        subID,
		Task:      task,
		Directive: directive,
		Employee:  employee,
		Status:    "running",
		LastLine:  "启动中…",
	})
}

// appendLastLine 将新 token 片段追加到 LastLine，仅保留最后一行，超长截断
func appendLastLine(prev, content string) string {
	combined := prev
	if prev == "" || prev == "启动中…" {
		combined = content
	} else {
		combined = prev + content
	}
	// 含换行则取最后一行
	if idx := strings.LastIndex(combined, "\n"); idx >= 0 {
		combined = strings.TrimSpace(combined[idx+1:])
	}
	// 截断到 80 rune
	return truncateRunes(combined, 80)
}

// maxStreamRenderLines 流式渲染时最多渲染的行数。
// 防止模型长输出（数万字）时每 120ms 全量 markdown 渲染导致 TUI 卡顿；
// 完整内容在流结束后存入消息区，可用 PgUp/PgDown 滚动查看。
const maxStreamRenderLines = 300

// tailLines 返回 s 的最后 n 行；不足 n 行时原样返回
func tailLines(s string, n int) string {
	if n <= 0 {
		return ""
	}
	lines := strings.Count(s, "\n")
	if lines < n {
		return s
	}
	idx := 0
	for i := 0; i < lines-n+1; i++ {
		j := strings.IndexByte(s[idx:], '\n')
		if j < 0 {
			return s
		}
		idx += j + 1
	}
	return s[idx:]
}

// truncateRunes 按 rune 截断字符串，超长追加省略号
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ── 按键分发 ──

// sgrMouseSeqRe 匹配 SGR 鼠标序列（可带 ESC 前缀；bubbletea 解析失败时
// ESC 可能被剥离，只剩 "[<65;72;17M" 形态）。用【子串扫描】而非整串
// 匹配：连续鼠标事件会合并成 "[<65;72;17M[<65;82;15M"（中间夹 M），
// 整串 ^...$ 匹配会漏掉；只要字符串中【包含】SGR 序列即判定为鼠标
// 残留整体丢弃（正常用户输入不会含 "[<数字;数字;数字M" 这种形态）。
var sgrMouseSeqRe = regexp.MustCompile(`(?:\x1b)?\[<[0-9]+(?:;[0-9]+)*[Mm]`)

// sgrResidueRe 匹配完整 SGR 鼠标序列残留（用于插入点过滤与输入框整体清除）
var sgrResidueRe = regexp.MustCompile(`(?:\x1b)?\[<[0-9]+(?:;[0-9]+)*[Mm]`)

// sgrPartialRe 匹配【未闭合】的 SGR 前缀（终端逐字节发送、尚未到达 M/m 的
// 累积态，如输入框末尾的 "[<65;72;17"）。正常用户输入不会以 "[<数字;数字"
// 结尾，可安全清除。
var sgrPartialRe = regexp.MustCompile(`\[<[0-9]+(?:;[0-9]+)*$`)

// stripSGRResidue 从字符串中移除所有 SGR 鼠标序列残留（完整形态 + 未闭合
// 前缀）。用于 appInp / Paste 等插入点：SGR 序列被终端逐字节拆分到达时
// handleKey 的整体过滤不命中（单字符正则不匹配），必须在写入输入框前
// 清除，并在写入后对整体再做一次清除（防累积成形）。
func stripSGRResidue(s string) string {
	s = sgrResidueRe.ReplaceAllString(s, "")
	s = sgrPartialRe.ReplaceAllString(s, "")
	return s
}

func (m *model) handleKey(msg bubbletea.KeyMsg) (bubbletea.Model, bubbletea.Cmd) {
	// 防御（最高优先级）：过滤终端透传的 SGR 鼠标序列（\x1b[<...M/m）。
	// 某些终端（conhost + ENABLE_MOUSE_INPUT 残留、远程终端、tmux 等）
	// 会把鼠标事件序列以按键文本形式传入，污染输入框（表现为
	// "[<65;72;17M" 或合并形态 "[<65;72;17M[<65;82;15M" 乱码）。
	// 必须放在 confirm 对话框之前（否则乱码会误触发对话框按键）。
	if s := msg.String(); sgrMouseSeqRe.MatchString(s) {
		return m, nil
	}
	// 确认对话框可见时，所有按键先交给对话框处理
	if m.confirmDialog != nil && m.confirmDialog.IsVisible() {
		m.HandleConfirmKey(msg.String())
		return m, nil
	}
	s := msg.String()

	// 焦点区分（核心）：焦点在历史区时 ↑↓/PgUp/PgDown 滚动消息历史（绝不操作输入框），
	// 其他按键（字符/回车/退格等）把焦点交还输入框后按正常流程处理。
	if m.focusHistory && (m.mode == modeChat || m.mode == modeNormal) {
		switch s {
		case "up", "ctrl+up":
			m.scrollHistory(3)
			return m, nil
		case "down", "ctrl+down":
			m.scrollHistory(-3)
			return m, nil
		case "pgup":
			m.scrollHistory(10)
			return m, nil
		case "pgdown":
			m.scrollHistory(-10)
			return m, nil
		}
		m.focusHistory = false
	}

	if s == "ctrl+c" {
		// Ctrl+C 三级优先级：
		// 1. 先清空输入框（任何模式下输入框有内容，先清空）
		// 2. 再中断对话（输入框为空且流式中）
		// 3. 最后退出（输入框为空且非流式）
		switch m.mode {
		case modeChat:
			if m.chatInput != "" {
				m.chatInput = ""
				m.cursor = 0
				m.inputScroll = 0
				m.appendMsg(sysMsg("⌫ 已清空输入框（再次 Ctrl+C 中断对话/退出）"))
				return m, nil
			}
		case modeCommand:
			if m.input != "" {
				m.mode = modeNormal
				m.input = ""
				m.cursor = 0
				m.completions = nil
				return m, nil
			}
		case modeSkillSelect:
			if m.skillSearch != "" {
				m.skillSearch = ""
				m.skillCursor = 0
				return m, nil
			}
		}
		// 输入框已为空：流式中 → 中断对话
		if m.streaming {
			if m.interrupted {
				return m, bubbletea.Quit // 二次 Ctrl+C 强制退出
			}
			m.interrupted = true
			// 收尾旧流：落内容 + 写历史 + 取消上游 + 推进代际（旧流残留消息作废）
			m.finalizeActiveStream()
			m.appendMsg(sysMsg("⚠ 已中断（再按 Ctrl+C 退出）"))
			return m, nil
		}
		// 输入框为空且非流式：退出
		return m, bubbletea.Quit
	}

	if s == "/" {
		// 斜杠命令只在输入框开头触发：
		// - normal 模式（无输入框）→ 进入命令模式
		// - chat 模式且输入框为空 → 进入命令模式
		// - chat 模式已有输入 / command / skillSelect 模式 → `/` 作为普通字符，不触发
		if m.mode == modeNormal || (m.mode == modeChat && m.chatInput == "") {
			m.mode = modeCommand
			m.input = "/"
			m.cursor = 1
			m.completions = m.commands.MatchPrefixEntries("/")
			m.complIdx = 0
			return m, nil
		}
	}

	// 帮助面板：? 键打开/关闭（toggle；输入框为空或 normal 模式时）
	if s == "?" && m.mode != modeSkillSelect && (m.mode == modeNormal || (m.mode == modeChat && m.chatInput == "")) {
		m.showHelp = !m.showHelp
		return m, nil
	}
	// 帮助面板打开时：任意其他键/ESC 关闭
	if m.showHelp && s != "?" {
		m.showHelp = false
		return m, nil
	}

	if s == "enter" && m.mode == modeNormal {
		m.InitConfirmDialog()
		m.mode = modeChat
		m.chatInput = ""
		m.cursor = 0
		return m, nil
	}

	// 滚动控制：PgUp/PgDown 翻页，↑↓ 交给各自模式处理（历史切换等）
	switch s {
	case "pgup":
		m.scrollOffset += 10
		return m, nil
	case "pgdown":
		m.scrollOffset -= 10
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
		return m, nil
	}

	// 思考块展开/折叠的键盘兜底（Windows 无鼠标捕获时使用）：
	// normal 模式下按 t 切换最近一条已完成的思考消息
	if s == "t" && m.mode == modeNormal && !m.showHelp {
		for i := len(m.messages) - 1; i >= 0; i-- {
			if m.messages[i].Type == MsgThinking && !m.messages[i].Streaming {
				m.messages[i].Collapsed = !m.messages[i].Collapsed
				delete(m.renderCache, i) // 折叠状态变化 → 渲染缓存失效
				return m, nil
			}
		}
	}

	switch m.mode {
	case modeChat:
		// 有输入内容时不滚动
		m.scrollOffset = 0
		return m.handleChat(msg)
	case modeCommand:
		return m.handleCmd(msg)
	case modeSkillSelect:
		return m.handleSkillSelect(msg)
	case modeNormal:
		// 输入框常驻：normal 模式下字符输入直接进入聊天输入框
		// （发送后 / ESC 退出后仍保持输入框可见可用，不再"莫名其妙消失"）
		m.mode = modeChat
		m.scrollOffset = 0
		return m.handleChat(msg)
	}
	return m, nil
}

// ── 鼠标点击 ──

func (m *model) handleMouse(msg bubbletea.MouseMsg) (bubbletea.Model, bubbletea.Cmd) {
	// ── 焦点区分（核心）：左键点击位置决定焦点归属 ──
	// 点击消息历史区（Y < 输入区顶行 viewInputTop）→ 焦点在历史：
	//   ↑↓/PgUp/PgDown/滚轮全部滚动消息历史，绝不触碰输入框；
	// 点击输入区 → 焦点在输入框：↑↓ 回填输入历史、滚轮滚动输入区。
	// 修复 Windows 下"点击 TUI 历史记录后，滚轮/上下键仍作用于输入框"。
	if msg.Button == bubbletea.MouseButtonLeft && msg.Action == bubbletea.MouseActionPress {
		if m.mode == modeChat || m.mode == modeNormal {
			// viewInputTop<=0（极小终端/首帧未渲染）时全屏按输入区处理：
			// msg.Y>=0 恒成立 → 焦点默认在输入框，不会误判为历史区。
			if msg.Y >= m.viewInputTop {
				m.focusHistory = false
			} else {
				m.focusHistory = true
			}
		}
	}

	// 鼠标滚轮 / 触摸板滑动：按焦点路由
	if msg.Button == bubbletea.MouseButtonWheelUp || msg.Button == bubbletea.MouseButtonWheelDown {
		delta := 3
		if msg.Button == bubbletea.MouseButtonWheelDown {
			delta = -3
		}
		// 焦点在历史区 → 一律滚动消息历史（即使光标悬停在输入框上）
		if m.focusHistory {
			m.scrollHistory(delta)
			return m, nil
		}
		// 焦点在输入框 → 按光标位置路由：光标在输入区且输入可滚动 → 滚动输入区；
		// 否则（光标在消息区/状态栏/输入不可滚动）滚动消息历史
		if msg.Y >= m.viewInputTop {
			if m.scrollInputBy(delta) {
				return m, nil
			}
		}
		m.scrollHistory(delta)
		return m, nil
	}
	// 左键点击：展开/折叠思考块
	if msg.Button != bubbletea.MouseButtonLeft || msg.Action != bubbletea.MouseActionPress {
		return m, nil
	}
	// 复用上一帧 View() 计算的布局参数（viewBodyH/viewStart/clickMap）——
	// 鼠标事件对应的是屏幕当前显示的内容，即上一帧渲染结果。
	// 曾自行重算 bodyH（硬编码状态栏 1 行、漏算对话框行数）与 View() 不一致，
	// 导致点击坐标错位 1 行以上：折叠的思考块（单行）点击命中的是上方普通
	// 消息，clickMap 查不到 → "思考完成点击无法展开/折叠"。
	bodyH := m.viewBodyH
	if bodyH < 1 {
		return m, nil
	}
	// msg.Y 是相对于终端顶部的坐标，body 是输出第一段 → 直接对应 body 内行号
	clickLine := msg.Y
	if clickLine < 0 || clickLine >= bodyH {
		return m, nil
	}
	// 全局行号 = 可见窗口起始行 + 点击行（与 View() 完全一致的窗口映射）
	globalLine := m.viewStart + clickLine
	// 查找点击的行是否对应思考消息
	if msgIdx, ok := m.clickMap[globalLine]; ok {
		if msgIdx >= 0 && msgIdx < len(m.messages) {
			if m.messages[msgIdx].Type == MsgThinking && !m.messages[msgIdx].Streaming {
				m.messages[msgIdx].Collapsed = !m.messages[msgIdx].Collapsed
				delete(m.renderCache, msgIdx) // 折叠状态变化 → 渲染缓存失效
			}
		}
	}
	return m, nil
}

// scrollHistory 滚动消息历史：delta>0 向上查看更早内容，delta<0 向下回到底部。
// 焦点在历史区（focusHistory）时 ↑↓/滚轮统一走本函数，保证绝不触碰输入框。
func (m *model) scrollHistory(delta int) {
	maxOffset := len(m.messages) * 5
	if delta > 0 {
		if m.scrollOffset < maxOffset {
			m.scrollOffset += delta
		}
	} else {
		m.scrollOffset += delta
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
	}
}

// scrollInputBy 滚动输入区（仅多行输入超出一屏时可滚动）；返回是否执行了滚动。
// 与 handleChat 的 ctrl+↑/↓ 同一上限/下限逻辑（renderInputArea 同步 clamp）。
func (m *model) scrollInputBy(delta int) bool {
	total := inputTotalLines(m)
	contentMax := m.height - 5
	if contentMax < 3 {
		contentMax = 3
	}
	if total <= contentMax {
		return false
	}
	if delta > 0 {
		if m.inputScroll < total-contentMax {
			m.inputScroll++
			return true
		}
		return false
	}
	if m.inputScroll > 0 {
		m.inputScroll--
		return true
	}
	return false
}

// ── 输入辅助 ──

func (m *model) isPasting() bool {
	// bracketed paste 标记优先：整体粘贴一次性到达，直接判定为粘贴
	if m.pasting {
		return true
	}
	elapsed := time.Since(m.lastKeyTime)
	// 连续字符在 80ms 间隔内到达 → 确认为粘贴（适用于不支持 bracketed paste 的终端）。
	// ★ 必须按"字符数"而非"按键次数"判定：
	//   终端粘贴通常一个 KeyMsg 携带多个字符（如一次到达整行），若按次数计数
	//   （每次调用 +1），粘贴 "abc\n" 只有 2 个 KeyMsg → pasteSeq 永远 <4，
	//   换行会被当作 Enter 触发发送 → 多行粘贴被"逐行发送"。
	//   按字符数累计，3 个字符即命中，换行立刻被识别为粘贴中的换行。
	if elapsed < 80*time.Millisecond {
		return m.pasteChars >= 3
	}
	// 间隔≥80ms → 粘贴突发已结束，重置计数。
	// 之前此处是 >200ms 才重置：一旦计数被快速输入攒高，
	// 停顿 80~200ms 后按回车仍会被误判为"粘贴中"→ 插入换行不发送，
	// 表现为"输入含换行后，回车键无法发送"。改为 ≥80ms 即重置。
	m.pasteChars = 0
	return false
}

// recentTyping：最近 50ms 内刚有字符插入。
// 粘贴流中换行符几乎总是紧跟文本块到达（间隔 <20ms）；而人手输入后按回车
// 通常 >100ms。用于兜底"文本块很短（1-2 字符）+ 换行"的粘贴（如 "a\nb\n"），
// 此时 pasteChars 尚未累计到 3，但换行显然属于粘贴而非用户发送意图。
func (m *model) recentTyping() bool {
	return m.pasteChars >= 1 && time.Since(m.lastKeyTime) < 50*time.Millisecond
}

// isSlashCommandLine 判断当前聊天输入是否为"整行斜杠命令"：
// 单行、以 / 开头、无空白回车。用于粘贴保护放行：用户粘贴 /load <uuid> 等
// 命令整行回车时应执行命令，而不是被当作粘贴换行吞掉（历史缺陷：粘贴命令
// 永不执行、历史对话无法加载到界面）。
func (m *model) isSlashCommandLine() bool {
	s := m.chatInput
	if !strings.HasPrefix(s, "/") || strings.ContainsAny(s, "\n\r") {
		return false
	}
	// 仅放行注册过的命令名（含参数如 "/load <uuid>"）；"/abc 未知命令"不放行，
	// 仍走原粘贴保护逻辑（避免误把多行内容判为命令）。
	trimmed := strings.TrimSpace(s)
	if !strings.HasPrefix(trimmed, "/") {
		return false
	}
	fields := strings.Fields(trimmed)
	if len(fields) == 0 {
		return false
	}
	name := fields[0]
	if m.commands == nil {
		return false
	}
	return m.commands.IsCommand(name)
}

func appInp(cur *string, cursor *int, msg bubbletea.KeyMsg) {
	if len(msg.Runes) > 0 {
		insert := string(msg.Runes)
		// 过滤零宽字符；规范化 \r → \n（防止粘贴中的 CR 被当作字符插入）
		insert = strings.ReplaceAll(insert, "\r\n", "\n")
		insert = strings.ReplaceAll(insert, "\r", "\n")
		insert = strings.Trim(insert, "\u200b\u200c\u200d\ufeff")
		if insert == "" {
			return
		}
		r := []rune(*cur)
		if *cursor >= len(r) {
			*cur += insert
		} else {
			*cur = string(r[:*cursor]) + insert + string(r[*cursor:])
		}
		*cursor += len([]rune(insert))
		// 防御：终端把 SGR 鼠标序列拆成小块/逐字符到达时，handleKey 的整体
		// 正则（需完整形态）不命中，字符会被逐块插入累积成 "[<65;72;17M"
		// 乱码。此处对整体做残留清理（幂等，正常输入不含 "[<数字;数字" 形态）。
		before := len([]rune(*cur))
		*cur = stripSGRResidue(*cur)
		if after := len([]rune(*cur)); after < before {
			if *cursor > after {
				*cursor = after
			}
		}
		return
	}
	ch := msg.String()
	if len(ch) == 1 {
		r := []rune(*cur)
		// 过滤控制字符和零宽字符
		if ch[0] < 0x20 || ch[0] == 0x7f {
			return
		}
		if *cursor >= len(r) {
			*cur += ch
		} else {
			*cur = string(r[:*cursor]) + ch + string(r[*cursor:])
		}
		*cursor++
	}
}

func delBefore(s string, cursor int) (string, int) {
	// 先清除零宽字符
	s = strings.ReplaceAll(s, "\u200b", "")
	s = strings.ReplaceAll(s, "\u200c", "")
	s = strings.ReplaceAll(s, "\u200d", "")
	s = strings.ReplaceAll(s, "\ufeff", "")
	r := []rune(s)
	// cursor 可能在清理零宽字符后超出范围，重新计算
	if cursor > len(r) {
		cursor = len(r)
	}
	if cursor <= 0 {
		if len(r) > 0 {
			// cursor=0 但还有字符 → 删除第一个
			return string(r[1:]), 0
		}
		return "", 0
	}
	cursor--
	return string(r[:cursor]) + string(r[cursor+1:]), cursor
}

// 输入历史持久化（保留1天）

// maxHistoryMessages 限制 TUI 消息列表保留的最大条数。
// 无论加载已有会话（--memory-load）还是新会话，超出部分丢弃最旧消息，
// 避免历史过长导致渲染与滚动开销持续增长。
const maxHistoryMessages = 1000

// resetMessages 清空消息列表并重置所有按消息索引派生的状态：
// renderCache（msgIdx → 渲染行缓存）、clickMap（屏幕行号 → 消息索引）、
// scrollOffset（滚动位置）。
// 必须在会话切换（/session /load /new）时调用——若只清 messages 而保留
// renderCache，新会话消息会以相同 msgIdx 命中旧会话的渲染缓存，显示旧
// 会话内容（"文字顺序/内容错乱"的根因之一）。
func (m *model) resetMessages() {
	m.messages = nil
	m.renderCache = make(map[int]*msgRenderCache)
	m.clickMap = make(map[int]int)
	m.scrollOffset = 0
	m.loadHistoryMessages()
}

// appendMsg 追加一条消息并做上限控制：超过 maxHistoryMessages 时丢弃最旧消息。
// 头部截断会导致消息索引整体偏移，renderCache（按 msgIdx 缓存渲染行）与
// clickMap（屏幕行号 → 消息索引）随之失效，需一并清空重建。
func (m *model) appendMsg(msg ChatMessage) {
	m.messages = append(m.messages, msg)
	if len(m.messages) > maxHistoryMessages {
		drop := len(m.messages) - maxHistoryMessages
		m.messages = m.messages[drop:]
		m.renderCache = make(map[int]*msgRenderCache)
		m.clickMap = make(map[int]int)
	}
}

// loadHistoryMessages 从记忆系统加载历史会话事件并转换为 ChatMessage 列表。
func (m *model) loadHistoryMessages() {
	if m.rt == nil || m.rt.Memory == nil || m.currentSessionID == "" {
		return
	}
	events, err := m.rt.Memory.LoadEvents(m.currentSessionID)
	if err != nil || len(events) == 0 {
		return
	}
	for _, evt := range events {
		switch evt.Type {
		case "user_msg", "user_input":
			if s, ok := evt.Data.(string); ok {
				m.appendMsg(usrMsg(s))
			} else if data, ok := evt.Data.(map[string]any); ok {
				if content, ok := data["content"].(string); ok {
					m.appendMsg(usrMsg(content))
				}
			}
		case "assistant_msg":
			if s, ok := evt.Data.(string); ok {
				m.appendMsg(asstMsg(runtime.StripThinking(s)))
			} else if data, ok := evt.Data.(map[string]any); ok {
				if content, ok := data["content"].(string); ok {
					m.appendMsg(asstMsg(runtime.StripThinking(content)))
				}
			}
		case "tool_call":
			if data, ok := evt.Data.(map[string]any); ok {
				name, _ := data["name"].(string)
				args, _ := data["arguments"].(string)
				m.appendMsg(toolCall(name, args))
			}
		case "tool_result":
			if data, ok := evt.Data.(map[string]any); ok {
				name, _ := data["name"].(string)
				result, _ := data["result"].(string)
				m.appendMsg(toolOK(name, result))
			}
		case "error":
			if s, ok := evt.Data.(string); ok {
				m.appendMsg(errMsg(s))
			}
		}
	}
}

// inputHistoryDir 返回输入历史文件所在目录：
//   - 有运行时上下文时，存项目级 `.contextloom/`（与记忆等数据同目录，跟随项目，不污染用户主目录）；
//   - 无运行时（测试/预览等场景）时返回空串——不读写任何目录。历史缺陷：
//     go test 构造的 model（无 rt）调用 send → saveInputHistory 把测试输入
//     写入用户级 ~/.contextloom/input_history.txt，污染真实用户数据。
func (m *model) inputHistoryDir() string {
	if m.rt != nil && m.rt.Config != nil && m.rt.Config.Project.Root != "" {
		return filepath.Join(m.rt.Config.Project.Root, ".contextloom")
	}
	return ""
}

func (m *model) loadInputHistory() {
	dir := m.inputHistoryDir()
	if dir == "" {
		return
	}
	data, err := os.ReadFile(filepath.Join(dir, "input_history.txt"))
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	cutoff := time.Now().AddDate(0, 0, -1) // 1天前
	for _, l := range lines {
		if l == "" {
			continue
		}
		// 格式: timestamp|content
		parts := strings.SplitN(l, "|", 2)
		if len(parts) == 2 {
			ts, err := time.Parse("2006-01-02T15:04:05", parts[0])
			if err == nil && ts.After(cutoff) {
				m.history = append(m.history, parts[1])
			}
		}
	}
}

func (m *model) saveInputHistory() {
	dir := m.inputHistoryDir()
	if dir == "" {
		return
	}
	os.MkdirAll(dir, 0755)
	path := filepath.Join(dir, "input_history.txt")

	// 先读取现有历史，保留原始时间戳
	existing := map[string]string{} // content → timestamp
	cutoff := time.Now().AddDate(0, 0, -1)
	if data, err := os.ReadFile(path); err == nil {
		for _, l := range strings.Split(string(data), "\n") {
			if l == "" {
				continue
			}
			parts := strings.SplitN(l, "|", 2)
			if len(parts) == 2 {
				ts, err := time.Parse("2006-01-02T15:04:05", parts[0])
				if err == nil && ts.After(cutoff) {
					existing[parts[1]] = parts[0]
				}
			}
		}
	}

	// 合并现有历史 + 新条目（用当前时间戳）
	now := time.Now().Format("2006-01-02T15:04:05")
	seen := map[string]bool{}
	var lines []string
	// 先写新条目（最新在前）
	for _, h := range m.history {
		if seen[h] {
			continue
		}
		seen[h] = true
		ts, ok := existing[h]
		if !ok {
			ts = now
		}
		lines = append(lines, ts+"|"+h)
	}
	// 再写有旧时间戳但不在 m.history 里的条目（清理后保留）
	for content, ts := range existing {
		if !seen[content] {
			lines = append(lines, ts+"|"+content)
		}
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

// ── 聊天模式 ──

func (m *model) handleChat(msg bubbletea.KeyMsg) (bubbletea.Model, bubbletea.Cmd) {
	s := msg.String()

	// 快捷粘贴：bracketed paste 整体到达（Paste:true），一次性插入全部内容
	if msg.Paste && len(msg.Runes) > 0 {
		m.pasting = true
		m.pasteSeq = 0
		m.pasteChars = 0
		insert := string(msg.Runes)
		// 规范化 Windows 剪贴板换行：\r\n / 孤立 \r → \n（否则 \r 会被当作 Enter 误触发发送）
		insert = strings.ReplaceAll(insert, "\r\n", "\n")
		insert = strings.ReplaceAll(insert, "\r", "\n")
		insert = strings.Trim(insert, "\u200b\u200c\u200d\ufeff")
		if insert != "" {
			r := []rune(m.chatInput)
			if m.cursor >= len(r) {
				m.chatInput += insert
			} else {
				m.chatInput = string(r[:m.cursor]) + insert + string(r[m.cursor:])
			}
			m.cursor += len([]rune(insert))
			// 防御：终端把 SGR 鼠标序列作为 Paste 事件整体/合并到达时，
			// handleKey 可能不命中（Paste 事件的 String() 形态特殊），此处
			// 对整体做残留清理（幂等），防止 "[<65;72;17M[<65;82;15M" 乱码。
			before := len([]rune(m.chatInput))
			m.chatInput = stripSGRResidue(m.chatInput)
			if after := len([]rune(m.chatInput)); after < before && m.cursor > after {
				m.cursor = after
			}
		}
		m.inputScroll = 0 // 粘贴后跟随光标
		m.lastKeyTime = time.Now()
		return m, nil
	}

	switch s {
	case "enter", "ctrl+m":
		// 多行粘贴保护：任何粘贴迹象（bracketed paste 标记 / 快速连续输入）下，
		// Enter/CR 一律插入换行、绝不触发发送——否则多行粘贴中的换行会被误判为
		// "发送"，导致输入框（含用户已输入的前文）被整体清空发送，即"粘贴覆盖前文"。
		if (m.isPasting() || msg.Paste || m.recentTyping()) && !m.isSlashCommandLine() {
			r := []rune(m.chatInput)
			if m.cursor >= len(r) {
				m.chatInput += "\n"
			} else {
				m.chatInput = string(r[:m.cursor]) + "\n" + string(r[m.cursor:])
			}
			m.cursor = len([]rune(m.chatInput))
			m.inputScroll = 0
			m.cursorCol = -1
			// 解除粘贴状态：避免 bracketed paste 结束后 pasting 卡死为 true，
			// 导致此后所有回车都被当作换行、永远无法发送。
			// 粘贴内容在 handleChat 顶部的 Paste fast-path 一次性插入，不会走到本分支，
			// 因此这里清零是安全的；真实粘贴流中的换行由上方 isPasting() 的
			// <80ms 连续字符检测继续保护。
			m.pasting = false
			m.pasteSeq = 0
			m.pasteChars = 0
			return m, nil
		}
		// 反斜杠软换行：行尾单个 `\` + Enter → 插入换行不发送（代码块/长文常用）；
		// 行尾 `\\` + Enter → 正常发送（表示字面反斜杠）。
		if strings.HasSuffix(m.chatInput, "\\") && !strings.HasSuffix(m.chatInput, "\\\\") {
			r := []rune(m.chatInput)
			m.chatInput = string(r[:len(r)-1]) + "\n"
			m.cursor = len([]rune(m.chatInput))
			m.inputScroll = 0
			m.cursorCol = -1
			return m, nil
		}
		m.pasting = false
		return m.send()
	case "ctrl+j", "ctrl+enter", "alt+enter", "alt+ctrl+j", "alt+ctrl+m":
		// 在光标处插入换行（光标在末尾则追加）
		// 真实终端实测（bubbletea v1.1.0）：
		//  - Ctrl+J(LF 0x0A)          → "ctrl+j"      ✓ 普通终端(iTerm2/Terminal.app/xterm/Windows Terminal)可靠
		//  - Alt+Enter(ESC+CR 0x1B 0D)→ "alt+enter"   ✓ 所有 Unix 终端可靠；VS Code 集成终端不拦截；macOS=Option+Enter
		//  - Shift+Enter              → 与 Enter 同码(keyCR,"enter")，bubbletea 不解析 → 实际触发发送，不可达
		//  - Ctrl+Enter/Ctrl+M(CR 0D) → 与 Enter 同码(keyCR,"enter")，真实终端无法区分 → 实际触发发送，不会到达本分支
		// 注意：VS Code 集成终端把 Ctrl+J 占为"切换面板"，不会传到应用 → 请用 Alt+Enter(Option+Enter)；
		//       Windows 控制台/Windows Terminal 把 Alt+Enter 占为全屏 → 请用 Ctrl+J。
		// 其余分支为防御性保留（部分终端/键映射可能产生该字符串）。
		r := []rune(m.chatInput)
		if m.cursor >= len(r) {
			m.chatInput += "\n"
		} else {
			m.chatInput = string(r[:m.cursor]) + "\n" + string(r[m.cursor:])
		}
		m.cursor++
		m.inputScroll = 0
		m.cursorCol = -1
		return m, nil
	case "ctrl+up":
		// 输入区向上滚动查看（仅当内容超出一屏时有效）
		total := inputTotalLines(m)
		contentMax := m.height - 5
		if contentMax < 3 {
			contentMax = 3
		}
		if total > contentMax && m.inputScroll < total-contentMax {
			m.inputScroll++
		}
		return m, nil
	case "ctrl+down":
		if m.inputScroll > 0 {
			m.inputScroll--
		}
		return m, nil
	case "esc":
		m.mode = modeNormal
		m.chatInput = ""
		m.cursor = 0
		m.historyIdx = 0
		m.historyDraft = ""
		m.inputScroll = 0
	case "backspace", "ctrl+h":
		// 极简实现：直接按 rune 删除
		if len(m.chatInput) == 0 {
			return m, nil
		}
		r := []rune(m.chatInput)
		if m.cursor >= len(r) {
			// cursor 在末尾 → 删最后一个
			m.chatInput = string(r[:len(r)-1])
			m.cursor = len(r) - 1
		} else if m.cursor > 0 {
			// cursor 在中间 → 删前一个
			m.cursor--
			m.chatInput = string(r[:m.cursor]) + string(r[m.cursor+1:])
		} else {
			// cursor=0 → 删第一个
			m.chatInput = string(r[1:])
			m.cursor = 0
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		m.cursorCol = -1
		m.inputScroll = 0
	case "ctrl+a", "home":
		m.cursor = 0
		m.cursorCol = -1
		m.inputScroll = 0
	case "ctrl+e", "end":
		m.cursor = len([]rune(m.chatInput))
		m.cursorCol = -1
		m.inputScroll = 0
	case "ctrl+u":
		// 删除光标前到行首（多行时仅当前行；readline 习惯）
		r := []rune(m.chatInput)
		lineStart := 0
		for i := m.cursor - 1; i >= 0; i-- {
			if r[i] == '\n' {
				lineStart = i + 1
				break
			}
		}
		m.chatInput = string(r[:lineStart]) + string(r[m.cursor:])
		m.cursor = lineStart
		m.cursorCol = -1
		m.inputScroll = 0
	case "ctrl+k":
		// 删除光标到行尾（多行时仅当前行）
		r := []rune(m.chatInput)
		lineEnd := len(r)
		for i := m.cursor; i < len(r); i++ {
			if r[i] == '\n' {
				lineEnd = i
				break
			}
		}
		m.chatInput = string(r[:m.cursor]) + string(r[lineEnd:])
		m.cursorCol = -1
		m.inputScroll = 0
	case "ctrl+w":
		// 删除光标前一个词
		r := []rune(m.chatInput)
		i := m.cursor
		for i > 0 && (r[i-1] == ' ' || r[i-1] == '\n' || r[i-1] == '\t') {
			i--
		}
		for i > 0 && r[i-1] != ' ' && r[i-1] != '\n' && r[i-1] != '\t' {
			i--
		}
		m.chatInput = string(r[:i]) + string(r[m.cursor:])
		m.cursor = i
		m.cursorCol = -1
		m.inputScroll = 0
	case "left":
		if m.cursor > 0 {
			m.cursor--
		}
		m.cursorCol = -1
		m.inputScroll = 0
	case "right":
		if m.cursor < len([]rune(m.chatInput)) {
			m.cursor++
		}
		m.cursorCol = -1
		m.inputScroll = 0
	case "up", "down":
		// 多行输入：↑↓ 优先在行间移动光标（保持目标列），首行↑/末行↓ 才回退历史
		if strings.Contains(m.chatInput, "\n") {
			d := 1
			if s == "up" {
				d = -1
			}
			if m.moveCursorLine(d) {
				return m, nil
			}
		} else {
			// 单行输入：光标不在边缘时 ↑/↓ 仅移动光标到行首/行尾，
			// 不触发历史翻滚（防止未提交输入被覆盖丢失）
			r := []rune(m.chatInput)
			if s == "up" && m.cursor > 0 {
				m.cursor = 0
				m.cursorCol = -1
				m.inputScroll = 0
				return m, nil
			}
			if s == "down" && m.cursor < len(r) {
				m.cursor = len(r)
				m.cursorCol = -1
				m.inputScroll = 0
				return m, nil
			}
		}
		// 光标已在边缘（单行行首↑/行尾↓，多行首行↑/末行↓）才翻历史
		if len(m.completions) == 0 && len(m.history) > 0 {
			if s == "up" {
				if m.historyIdx == 0 {
					m.historyDraft = m.chatInput // 保存当前输入，↓ 可完整恢复
				}
				m.historyIdx++
				if m.historyIdx > len(m.history) {
					m.historyIdx = 1 // 循环回最早
				}
				m.chatInput = m.history[len(m.history)-m.historyIdx]
				m.cursor = len([]rune(m.chatInput))
			} else if s == "down" {
				if m.historyIdx > 0 {
					m.historyIdx--
					if m.historyIdx == 0 {
						m.chatInput = m.historyDraft // 恢复翻历史前的输入
					} else {
						m.chatInput = m.history[len(m.history)-m.historyIdx]
					}
					m.cursor = len([]rune(m.chatInput))
				}
				// historyIdx==0（未在翻历史）时 ↓ 无操作，不覆盖当前输入
			}
			m.inputScroll = 0
		}
	default:
		appInp(&m.chatInput, &m.cursor, msg)
		// 手动输入时结束粘贴状态
		if !msg.Paste {
			m.pasting = false
		}
		// 编辑即退出历史浏览：当前编辑内容作为新草稿基准
		if m.historyIdx > 0 {
			m.historyIdx = 0
			m.historyDraft = m.chatInput
		}
		// 累计 80ms 窗口内到达的字符数（粘贴启发式按字符判定）
		if n := len(msg.Runes); n > 0 {
			m.pasteChars += n
		}
		m.cursorCol = -1
		m.inputScroll = 0
	}
	m.lastKeyTime = time.Now()
	return m, nil
}

// cursorPos 返回光标所在行号（0 起）与列号（rune 数，行内偏移）
func cursorPos(s string, cursor int) (line, col int) {
	r := []rune(s)
	line, col = 0, 0
	for i := 0; i < cursor && i < len(r); i++ {
		if r[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return
}

// moveCursorLine 在多行输入中把光标向 delta（±1）移动一行，保持目标列。
// 返回是否成功移动（首行↑、末行↓ 返回 false，由调用方回退到历史切换）。
func (m *model) moveCursorLine(delta int) bool {
	r := []rune(m.chatInput)
	if len(r) == 0 {
		return false
	}
	line, col := cursorPos(m.chatInput, m.cursor)
	if m.cursorCol < 0 {
		m.cursorCol = col
	}
	// 计算每行起始偏移
	starts := []int{0}
	for i, ch := range r {
		if ch == '\n' {
			starts = append(starts, i+1)
		}
	}
	target := line + delta
	if target < 0 || target >= len(starts) {
		return false
	}
	end := len(r)
	if target+1 < len(starts) {
		end = starts[target+1] - 1 // 去掉行尾 \n
	}
	newCol := m.cursorCol
	if newCol > end-starts[target] {
		newCol = end - starts[target]
	}
	if newCol < 0 {
		newCol = 0
	}
	m.cursor = starts[target] + newCol
	m.inputScroll = 0
	return true
}

// finalizeActiveStream 收尾正在进行的流：把已产出片段落为消息并写入历史，
// 取消上游 ctx，并推进 streamGen（旧流残留消息随后按代际丢弃）。
// 供"生成中插入新对话"（send）与"中断对话"（Ctrl+C）复用，保证两条路径
// 行为一致——旧回复不丢失、不串台、上游确实被取消。
func (m *model) finalizeActiveStream() {
	if !m.streaming {
		return
	}
	if m.cancelFn != nil {
		m.cancelFn()
	}
	m.streamGen++ // 推进代际：旧流残留消息（token/done/err）全部作废
	m.streaming = false
	m.streamCh = nil
	m.streamErrCh = nil
	m.cancelFn = nil
	if m.streamBuf != "" {
		// 与 streamDoneMsg 同款收尾：思考块 + 正文分离落为消息
		body, thinks := splitThinkTags(m.streamBuf)
		for _, t := range thinks {
			if strings.TrimSpace(t) == "" {
				continue
			}
			th := thinkMsg()
			th.Content = t
			th.Collapsed = true
			th.Streaming = false
			m.appendMsg(th)
		}
		if body != "" {
			m.appendMsg(asstMsg(body))
		}
		// 中断前已产生的回复写入历史（避免整轮丢失导致聊天记录断档）
		if m.rt != nil {
			m.rt.AddToHistory("assistant", m.streamBuf)
		}
		m.streamBuf = ""
	}
}

func (m *model) send() (bubbletea.Model, bubbletea.Cmd) {
	input := strings.TrimSpace(m.chatInput)
	m.chatInput = ""
	m.cursor = 0
	m.pasteSeq = 0
	m.pasteChars = 0
	m.pasting = false
	m.inputScroll = 0
	m.focusHistory = false
	m.mode = modeNormal
	if input == "" {
		return m, nil
	}

	// 聊天输入框整行斜杠命令（常见于粘贴/终端快速送达，如 `/load <uuid>`）：
	// 直接按命令执行，不发给模型。历史缺陷：`/` 进入命令模式只在【单字符击键】
	// 时触发，整行一次性到达时命令落入聊天输入框，随后 Enter 又被粘贴保护
	// （recentTyping/isPasting）当作换行吞掉 → 命令永不执行、历史对话无法加载
	// 到界面。这里在 send 入口统一识别，与 handleCmd 共用 executeCommandInput：
	//   1. 纯单行（无 \n）且 / 开头；多行粘贴保持原行为（插入换行不执行）；
	//   2. /split 仍走异步派发（与命令模式一致）；
	//   3. 未知命令原样回显（与命令模式一致），不再作为聊天消息发给模型。
	if strings.HasPrefix(input, "/") && !strings.Contains(input, "\n") {
		m.input = input
		m.cursor = len([]rune(input))
		m.completions = nil
		// 先恢复确认对话框/模式清理（与 handleCmd 执行前状态一致）
		m.InitConfirmDialog()
		if strings.HasPrefix(input, "/split") {
			return m.sendSplit(input)
		}
		quitCmd, _, _ := m.executeCommandInput()
		if quitCmd {
			return m, bubbletea.Quit
		}
		return m, bubbletea.ClearScreen
	}

	// 生成中插入新对话：先收尾旧流（把已产出片段落为消息 + 写历史 + 取消
	// 旧流并推进代际），再启动新流。历史缺陷：send 无此守卫——直接
	// streamBuf="" 并覆盖 streamCh/cancelFn，旧回复被静默丢弃、旧流无法取消、
	// 残留 token 还会污染新对话（用户实测"对话进行中插入对话 → 所有内容
	// 重新开始"根因）。收尾后旧流的残留消息由 gen 代际过滤丢弃。
	m.finalizeActiveStream()

	m.history = append(m.history, input)
	m.historyIdx = 0
	m.historyDraft = ""
	m.saveInputHistory()

	if len(m.messages) > 0 {
		m.appendMsg(ChatMessage{Type: MsgSystem, Content: "─ " + time.Now().Format("15:04") + " ─"})
	}

	m.appendMsg(usrMsg(input))
	// 不再预创建 thinkMsg — 思考内容由 splitThinkTags 自动从响应中提取
	// 保存用户消息到对话历史
	if m.rt != nil {
		// 首次对话自动更新会话标题和预览（标题取用户提问前 50 字）
		if len(m.rt.GetConversationHistory()) == 0 && m.currentSessionID != "" {
			m.rt.UpdateSessionTitle(m.currentSessionID, input)
		}
		m.rt.AddToHistory("user", input)
	}

	m.streaming = true
	m.streamGen++      // 推进代际：旧流消息（若有）此后按 gen 丢弃
	m.scrollOffset = 0 // 发送时回到底部
	m.streamBuf = ""
	m.tick = 0
	m.tokenCount = 0
	m.interrupted = false
	m.subTasks = nil // 清空上一轮子任务面板

	if m.rt == nil {
		m.streaming = false
		m.appendMsg(asstMsg("[未连接运行时]"))
		return m, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFn = cancel
	ch, errCh, err := m.rt.ProcessInput(ctx, input)
	if err != nil {
		m.streaming = false
		m.appendMsg(errMsg(err.Error()))
		return m, nil
	}
	m.streamCh = ch
	m.streamErrCh = errCh
	// 发送消息时强制清屏重绘一次，消除上一帧可能的终端行残留（含命令列表残留）
	return m, bubbletea.Batch(m.readNextStreamToken(), bubbletea.ClearScreen)
}

// parseSplitArgs 解析 /split 参数为子任务列表（支持中英文逗号/分号分隔）
func parseSplitArgs(args string) []string {
	parts := strings.FieldsFunc(args, func(r rune) bool {
		return r == ',' || r == '，' || r == ';' || r == '；'
	})
	var tasks []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			tasks = append(tasks, p)
		}
	}
	return tasks
}

// sendSplit 处理 /split 命令：绕过模型决策，直接并行派发指定数量的子 Agent。
// 这是唯一 100% 确定性触发子任务面板的途径——不依赖模型是否自主调用 spawn_task。
// 用法: /split 子任务1, 子任务2, 子任务3
func (m *model) sendSplit(input string) (bubbletea.Model, bubbletea.Cmd) {
	// 解析子任务列表（支持中英文逗号/分号分隔）
	args := strings.TrimSpace(strings.TrimPrefix(input, "/split"))
	clearCmd := func() (bubbletea.Model, bubbletea.Cmd) {
		m.input = ""
		m.cursor = 0
		m.InitConfirmDialog()
		m.mode = modeChat
		m.completions = nil
		return m, bubbletea.ClearScreen
	}
	if args == "" {
		m.appendMsg(sysMsg("用法: /split <子任务1>, <子任务2>, ...（每个子任务须有可验证产出：写文件/测试/生成报告）"))
		return clearCmd()
	}
	tasks := parseSplitArgs(args)
	if len(tasks) < 2 {
		m.appendMsg(sysMsg("/split 至少需要 2 个子任务（用逗号分隔；每个子任务须有可验证产出）"))
		return clearCmd()
	}
	if m.rt == nil {
		m.appendMsg(errMsg("[未连接运行时]"))
		return clearCmd()
	}

	// 切 task 模式（SpawnTask 依赖 task 模式环境：监工 + 子 Agent 工具链）
	m.rt.SetMode("task")

	// 记录用户指令为消息
	m.appendMsg(usrMsg(input))
	m.appendMsg(sysMsg(fmt.Sprintf("⚡ 强制派发 %d 个子 Agent 并行执行", len(tasks))))

	// 重置 streaming 状态
	m.streaming = true
	m.scrollOffset = 0
	m.streamBuf = ""
	m.tick = 0
	m.tokenCount = 0
	m.interrupted = false
	m.subTasks = nil
	m.input = ""
	m.cursor = 0
	m.InitConfirmDialog()
	m.mode = modeChat
	m.completions = nil

	// 创建 stream channel + ctx（注入 progressChan 供 SpawnTask 发 § 信号到面板）
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelFn = cancel
	ch := make(chan string, 256)
	errCh := make(chan error, 1)
	m.streamCh = ch
	m.streamErrCh = errCh

	// 注入 progressChan：SpawnTask 内部 EmitTaskStart/Token/Done/Fail 把 § 信号写入 ch
	spawnCtx := tools.WithProgressChan(ctx, ch)

	// 并行派发：每个子任务一个 goroutine 调 SpawnTask（子 Agent 并行执行，共享只读父上下文）
	var wg sync.WaitGroup
	go func() {
		for _, task := range tasks {
			wg.Add(1)
			go func(t string) {
				defer wg.Done()
				_, _ = m.rt.SpawnTask(spawnCtx, "main", tools.TaskSpec{
					Task:      t,
					Directive: "CODE_AGENT",
				})
			}(task)
		}
		wg.Wait()
		// 全部子任务完成 → close 触发 streamDoneMsg 结束 streaming
		close(ch)
		close(errCh)
	}()

	return m, bubbletea.Batch(m.readNextStreamToken(), bubbletea.ClearScreen)
}

func (m *model) readNextStreamToken() bubbletea.Cmd {
	return m.readNextStreamTokenGen(m.streamGen)
}

// readNextStreamTokenGen 与 readNextStreamToken 同语义，但固定代际号 gen：
// 递归续读时保持发起时的代际，防止递归瞬间 m.streamGen 已推进（用户插入
// 新对话）导致旧流的续读消息被误标为新代际而污染新对话。
func (m *model) readNextStreamTokenGen(gen int) bubbletea.Cmd {
	return func() bubbletea.Msg {
		// 错误优先：先非阻塞检查 errCh。agent 在 return 前总是先向 errCh 发送
		// 错误再关闭 ch（defer），因此错误一定先于 ch 关闭到达——这里先查 errCh
		// 就能稳定捕获错误，避免 select 竞态随机选中 ch 关闭（ok=false）→
		// 误判"正常结束"→ 真实错误被吞、显示"工作完成"（对话中止的隐藏路径）。
		select {
		case e, ok := <-m.streamErrCh:
			if ok && e != nil {
				return streamErrMsg{err: e.Error(), gen: gen}
			}
			// errCh 已关闭且无错误：走下方 ch 消费
		default:
			// errCh 暂无就绪：继续
		}
		select {
		case token, ok := <-m.streamCh:
			if !ok {
				// ch 已关闭：再次确认 errCh（错误可能刚到达，ch 与 errCh 同时关闭时
				// 第一次非阻塞检查可能漏掉，这里补查一次）
				select {
				case e, ok := <-m.streamErrCh:
					if ok && e != nil {
						return streamErrMsg{err: e.Error(), gen: gen}
					}
				default:
				}
				return streamDoneMsg{gen: gen}
			}
			return streamMsg{token: token, gen: gen}
		case e, ok := <-m.streamErrCh:
			if ok && e != nil {
				return streamErrMsg{err: e.Error(), gen: gen}
			}
			// errCh 正常关闭（ok=false）不代表流读完：继续消费 ch——
			// ch 已关闭则正常完成；有 token 则继续输出；均未就绪则递归
			// 返回 cmd 等下一轮。不能直接递归返回 cmd：当 ch 与 errCh
			// 同时关闭时 select 可能随机选中 errCh 分支，直接递归会返回
			// "空转 cmd"导致 streamDoneMsg 永不发出（误判为进行中）。
			select {
			case token, ok := <-m.streamCh:
				if !ok {
					return streamDoneMsg{gen: gen}
				}
				return streamMsg{token: token, gen: gen}
			default:
				return m.readNextStreamTokenGen(gen)
			}
		}
	}
}

// ── 命令模式 ──

// executeCommandInput 执行当前命令输入框内容（/xxx ...）并同步 Runtime 状态。
// 返回值：
//   - quit: 命令为 /quit 等退出命令（调用方返回 bubbletea.Quit）
//   - needsClear: 需要无条件强制重绘（命令执行后清残留）
//   - needsBreak: 需要跳出 switch（/skills 等 sentinel 已处理界面状态）
//
// 供 handleCmd（命令模式 Enter）与 handleChat/send（聊天输入框整行粘贴/快速
// 输入斜杠命令）共用，保证两条路径行为一致。
func (m *model) executeCommandInput() (quit bool, needsClear bool, needsBreak bool) {
	prevSession := m.currentSessionID
	result, quitCmd := m.commands.Execute(m.input)
	if result != "" {
		// /skills 命令 sentinel：进入交互式技能选择模式
		if result == "__SKILL_SELECT__" {
			m.initSkillSelect()
			m.appendMsg(sysMsg("> /skills · 进入技能选择模式"))
			m.input = ""
			m.cursor = 0
			m.chatInput = ""
			m.completions = nil
			needsClear = true
			return false, true, true
		}
		m.appendMsg(sysMsg("> " + m.input + "\n" + result))
	}
	// 命令执行后同步 Runtime 状态到 TUI（threshold/model）
	if m.rt != nil {
		m.reasoningEffort = m.rt.Config.CLIDefaults.Effort
		m.threshold = m.rt.Config.Harness.ScoreThreshold
		m.modelID = m.rt.CurrentModel()
		// 会话切换类命令（/session /load /new）后重建消息列表，
		// 立即显示新会话的聊天记录，无需重启
		if newID := m.rt.GetCurrentSessionID(); newID != prevSession {
			m.currentSessionID = newID
			m.resetMessages()
			if len(m.messages) == 0 && m.termHint != "" {
				m.appendMsg(sysMsg("⌨ " + m.termHint + "（? 查看全部快捷键）"))
			}
		}
	}
	if quitCmd {
		return true, true, false
	}
	m.input = ""
	m.cursor = 0
	m.InitConfirmDialog()
	m.mode = modeChat // 执行完命令也切到聊天模式
	m.chatInput = ""
	m.completions = nil
	needsClear = true // 命令执行后无条件强制重绘（用户反馈：发送完命令列表残留）
	return false, true, false
}

func (m *model) handleCmd(msg bubbletea.KeyMsg) (bubbletea.Model, bubbletea.Cmd) {
	s := msg.String()
	// 进入本按键前是否有建议列表：用于判定是否需要强制清屏重绘，
	// 消除 Bubble Tea 在建议列表消失、帧高度骤降时的终端行残留（"命令发送后列表残留"）。
	hadSuggestions := len(m.completions) > 0
	needsClear := false // 各 case 若清空了 completions 则置 true
	switch s {
	case "enter":
		if len(m.completions) > 0 && m.complIdx < len(m.completions) {
			// 第一次 Enter: 选中命令填入输入框
			selected := m.completions[m.complIdx].Name
			m.input = selected + " "
			m.cursor = len([]rune(m.input))
			m.completions = nil // 关闭下拉列表
			needsClear = hadSuggestions
			break
		}
		// /split 特殊拦截：异步并行派发子 Agent，不走同步 Execute
		if strings.HasPrefix(m.input, "/split") {
			return m.sendSplit(m.input)
		}
		if quitCmd, needsClearCmd, needsBreak := m.executeCommandInput(); quitCmd {
			return m, bubbletea.Quit
		} else if needsBreak {
			break
		} else {
			needsClear = needsClearCmd
		}
	case "esc":
		// ESC 彻底退出命令模式
		m.input = ""
		m.cursor = 0
		m.InitConfirmDialog()
		m.mode = modeChat
		m.chatInput = ""
		m.completions = nil
		needsClear = hadSuggestions
	case "backspace", "ctrl+h":
		if len(m.input) == 0 {
			// 空输入退格 → 退出命令模式回到聊天
			m.InitConfirmDialog()
			m.mode = modeChat
			m.completions = nil
			m.chatInput = ""
			needsClear = hadSuggestions
			break
		}
		r2 := []rune(m.input)
		if m.cursor >= len(r2) {
			m.input = string(r2[:len(r2)-1])
			m.cursor = len(r2) - 1
		} else if m.cursor > 0 {
			m.cursor--
			m.input = string(r2[:m.cursor]) + string(r2[m.cursor+1:])
		} else {
			m.input = string(r2[1:])
			m.cursor = 0
		}
		if m.cursor < 0 {
			m.cursor = 0
		}
		// 仅当输入被完全删空时退出命令模式回到聊天（避免退格编辑命令名时被误踢出）。
		// 删到无 / 前缀但不空（如"mode"）时保留在命令模式，用户可继续编辑或再删空退出。
		if m.input == "" {
			m.InitConfirmDialog()
			m.mode = modeChat
			m.completions = nil
			m.chatInput = ""
			needsClear = hadSuggestions
			break
		}
		m.completions = m.commands.MatchPrefixEntries(m.input)
		m.complIdx = 0
	case "tab":
		if len(m.completions) > 0 {
			m.complIdx = (m.complIdx + 1) % len(m.completions)
			m.input = m.completions[m.complIdx].Name
			m.cursor = len([]rune(m.input))
		} else {
			m.input = m.commands.AutoComplete(m.input)
			m.cursor = len([]rune(m.input))
		}
	case "up":
		if len(m.completions) > 0 {
			m.complIdx--
			if m.complIdx < 0 {
				m.complIdx = len(m.completions) - 1
			}
			m.input = m.completions[m.complIdx].Name
			m.cursor = len([]rune(m.input))
		}
	case "down":
		if len(m.completions) > 0 {
			m.complIdx = (m.complIdx + 1) % len(m.completions)
			m.input = m.completions[m.complIdx].Name
			m.cursor = len([]rune(m.input))
		}
	case "left":
		if m.cursor > 0 {
			m.cursor--
		}
	case "right":
		if m.cursor < len([]rune(m.input)) {
			m.cursor++
		}
	default:
		appInp(&m.input, &m.cursor, msg)
		// 仅在仍在输入命令名阶段（无空格分隔参数）时刷新补全列表；
		// 命令已选定并正在输入参数时清空补全列表，避免残留下拉。
		if !strings.Contains(m.input, " ") {
			m.completions = m.commands.MatchPrefixEntries(m.input)
		} else {
			m.completions = nil
			needsClear = hadSuggestions
		}
		m.complIdx = 0
	}
	m.lastKeyTime = time.Now()
	// 建议列表被本按键清空时强制清屏重绘一次，确保终端旧行彻底清除（修复列表残留）
	if needsClear {
		return m, bubbletea.ClearScreen
	}
	return m, nil
}

// ── 技能选择 UI ──

// initSkillSelect 初始化技能选择模式：从 Runtime 加载所有可用技能，重建选中状态。
func (m *model) initSkillSelect() {
	m.mode = modeSkillSelect
	m.skillSearch = ""
	m.skillCursor = 0

	// 从 Runtime 获取所有技能名
	if m.rt != nil {
		skills := m.rt.ListSkills()
		m.skillAll = make([]string, 0, len(skills))
		for _, s := range skills {
			m.skillAll = append(m.skillAll, s.Name)
		}
	} else {
		m.skillAll = nil
	}
	m.skillSelected = make([]bool, len(m.skillAll))

	// 已有激活的技能 → 预选中
	if m.rt != nil {
		active := m.rt.GetActiveSkills()
		activeSet := make(map[string]bool, len(active))
		for _, name := range active {
			activeSet[name] = true
		}
		for i, name := range m.skillAll {
			if activeSet[name] {
				m.skillSelected[i] = true
			}
		}
	}

	m.rebuildFiltered()
}

// fuzzyMatch 模糊匹配：判断 query 是否逐字符顺序匹配 target（不区分大小写）。
// 例如 "dp" 匹配 "deepseek-pro"、"sk" 匹配 "skill-creator"。
