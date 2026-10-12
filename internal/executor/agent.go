package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Agent struct {
	ID                  string
	Task                types.TaskCreate
	harness             *harness.Engine
	tools               *tools.Registry
	backend             model.Backend
	conversationHistory []types.Message
	depth               int       // 嵌套深度，主 Agent=0，子 Agent=1（防递归与隔离）
	bus                 tools.Bus // 消息总线（nil 时跳过通信，drain 走 DrainInbox）
	// §3.5 动态提示词缓存：连续相似任务的 sysPrompt 哈希，命中时精简 examples
	lastSysPromptHash string
	lastDirective     string
	// persistencePaths 持久化文件路径（由 Runtime 注入，供系统提示词告知模型各文件位置）
	persistencePaths map[string]string
	// pluginInject 插件 pre 阶段注入的提示片段（system role，注入到消息列表 system 之后）
	pluginInject []types.Message
	// pluginMessageHook 插件每轮消息后注入钩子（v2 hooks.message.inject）：
	// 每轮构建消息时调用，返回提示词段（user role，随用户消息之后）；空=不注入。
	// 对标 dsh 的"每轮近距离引导"——脚本侧按 round/input 动态决策。
	pluginMessageHook func(input string, round int) string
	// pluginRouteHook 插件路由决策钩子（v2 hooks.route.script）：
	// 构建提示词时调用，返回引导段注入 system 区（动态路由决策，可随输入/轮次变化）；
	// 空=不注入。对标 dsh 的"思维模式路由"（spec/react/mixed 行为带选择）。
	pluginRouteHook func(mode, input string, round int) string
	// toolScope 工具范围约束（由 pluginRouteMessages 解析 route 钩子 JSON 时设置）：
	// 空=无约束；"core"=仅核心工具；"read"=仅读取工具；"full"=全部工具。
	// 对标 dsh 极简模式的"首轮锁定最少工具，锚定后释放全部"。
	toolScope string
	// runRound 当前执行轮次（Run/RunStream 循环递增，供钩子感知轮次）
	runRound int
	// skillContents 技能内容片段（选中 skill 的正文，注入系统提示词）
	skillContents []string
	// autoApprove 自动批准标记：task 模式开启后跳过权限确认弹窗。
	autoApprove bool
	// visionModel 识图模型：本轮任务携带图片时自动切换的模型 ID（空=不切换）。
	// 由 runtime 从 config.model.vision_model 注入；切换作用域仅限本轮 Agent
	// 运行（prepareVision 返回恢复函数，运行结束恢复原模型）。
	visionModel string
	// evictSeq 被压缩原文入索引的 ID 计数器（保证索引块 ID 唯一）
	evictSeq int64
	// sessionID 归属会话（由 Runtime 注入，主/子 Agent 通用）。
	// 用于检索与会话私有压缩原文隔离：非空时 Search 只返回
	// "项目级共享块 + 本会话私有块"，indexCompressedEvicted 写入块带本会话。
	sessionID string
	// batchNo 已派发子任务批次计数（多批次拆分：一批完成后可继续派发新一批）
	batchNo int64
	// tracer 轻量可观测记录器（nil=不记录）
	tracer Tracer
	// checkpointSink 每轮运行检查点回调（nil=零开销）。
	// 每轮消息更新后调用 (round, msgs, false, "")；运行结束（正常/错误/强制收尾）
	// 前调用 (round, msgs, true, result)。由 runtime 注入 checkpoint.Store 的落盘实现，
	// 对标 LangGraph checkpointer：异常重启/goal 续跑可从最新快照恢复消息上下文。
	checkpointSink func(round int, messages []types.Message, done bool, result string)
}

// SetCheckpointSink 注入每轮检查点回调（nil 安全；nil 时主循环零开销）。
func (a *Agent) SetCheckpointSink(fn func(round int, messages []types.Message, done bool, result string)) {
	a.checkpointSink = fn
}

// sinkCheckpoint 调用检查点回调（nil 跳过）。
func (a *Agent) sinkCheckpoint(round int, messages []types.Message, done bool, result string) {
	if a.checkpointSink != nil {
		a.checkpointSink(round, messages, done, result)
	}
}

// hasSpawnTask 本轮工具调用是否含 spawn_task（用于批次续派提示）。
func hasSpawnTask(calls []types.ToolCall) bool {
	for _, c := range calls {
		if c.Function.Name == "spawn_task" {
			return true
		}
	}
	return false
}

// batchNudge 一批 spawn_task 完成后注入动态续批提示：
// 修正"模型派发一批子任务后不再拆分、主线程干活最多"的行为——
// 提示模型评估本批结果，若仍有可并行的独立工作则继续派发新一批。
func (a *Agent) batchNudge(calls []types.ToolCall, messages []types.Message) []types.Message {
	if !hasSpawnTask(calls) {
		return messages
	}
	no := atomic.AddInt64(&a.batchNo, 1)
	messages = append(messages, types.Message{
		Role: "user",
		Content: fmt.Sprintf(
			"【批次提示】第 %d 批子任务已全部完成并回收结果。请评估：若仍有可并行的独立工作（新文件/新测试/新报告等有可验证产出的子步骤），"+
				"请继续调用 spawn_task 派发第 %d 批（子任务可分批反复派发，直到无可并行工作）；若无并行空间则继续串行推进。",
			no, no+1),
	})
	return messages
}

// Tracer 轻量可观测接口（由 runtime 注入 trace.Recorder 实现；nil 时跳过）。
// 用接口而非直接依赖 trace 包，保持 executor 解耦。
type Tracer interface {
	TraceRound(mode string, round int, tool string, ms int64, errMsg string)
}

// SetTracer 注入 trace 记录器（nil 安全）。
func (a *Agent) SetTracer(t Tracer) {
	a.tracer = t
}

func (a *Agent) SetBackend(b model.Backend) {
	a.backend = b
}

func (a *Agent) SetConversationHistory(history []types.Message) {
	a.conversationHistory = history
}

// SetSessionID 注入 Agent 归属会话（进程内单例 Runtime 下多会话隔离的关键：
// 检索/压缩入索引均以会话为界，防止 task/goal 子 Agent 搜到其他会话内容）。
func (a *Agent) SetSessionID(sid string) {
	a.sessionID = sid
}

// SessionID 返回 Agent 归属会话（可能为空=未绑定，视为仅项目级共享）。
func (a *Agent) SessionID() string {
	return a.sessionID
}

// ConversationHistory 返回注入的对话历史副本（供 ACP/恢复链路读取）。
func (a *Agent) ConversationHistory() []types.Message {
	return append([]types.Message(nil), a.conversationHistory...)
}

// SetTools 设置工具注册表（子 Agent 用过滤后的 registry 防递归）
func (a *Agent) SetTools(r *tools.Registry) {
	a.tools = r
}

// SetDepth 设置嵌套深度（子 Agent=1）
func (a *Agent) SetDepth(d int) {
	a.depth = d
}

// SetPersistencePaths 注入持久化文件路径（由 Runtime 构建，供系统提示词告知模型）
func (a *Agent) SetPersistencePaths(paths map[string]string) {
	a.persistencePaths = paths
}

// knowledgeIndex 构建轻量知识索引（插件/技能目录及其规范），注入系统提示词。
// 只声明目录位置与各自规范（加载契约），不展开全部内容——模型需要细节时用
// read_file/list_files 工具按需读取对应 manifest.yaml / SKILL.md，控制提示词
// token 占用（全量内联会占用过量 token）。
func (a *Agent) knowledgeIndex() map[string]any {
	pluginsDir := ".contextloom/plugins（项目级）或 ~/.contextloom/plugins（用户级）"
	skillsDir := ".contextloom/skills"
	if a.persistencePaths != nil {
		if d := a.persistencePaths["plugins_dir"]; d != "" {
			pluginsDir = d
		}
		if d := a.persistencePaths["skills_dir"]; d != "" {
			skillsDir = d
		}
	}
	return map[string]any{
		"plugins_dir": pluginsDir,
		"plugins_规范":  "每个子目录=一个插件；manifest.yaml 声明 name/version/stages(pre/during/post)/tools/hooks(message/route)。需要插件能力细节时用 read_file 读对应 manifest.yaml",
		"skills_dir":  skillsDir,
		"skills_规范":   "每个子目录=一个技能；SKILL.md = YAML frontmatter(name/description) + Markdown 正文。需要技能规范细节时用 read_file 读对应 SKILL.md",
		"usage":       "以上仅为索引：不要假设或编造目录内容，需要具体规范时用 read_file/list_files 按需查阅",
	}
}

// chatSystemPrompt 构建 chat 模式系统提示词（基础指令 + 知识索引）。
// 知识索引以 JSON 块追加，仅占极少 token（目录路径 + 规范一句话），
// 详细内容按需经工具读取。
func (a *Agent) chatSystemPrompt() string {
	base := "你是ContextLoom中文编码助手。可用工具:read_file,exec_command,search_content,run_tests,write_file,list_files,git_diff,web_fetch,replace_in_file。需要实时数据时调用工具。中文回答，简洁。任务完成后直接给出最终答案并停止，不要反复调用工具。"
	if a.persistencePaths == nil {
		return base
	}
	idx, err := json.Marshal(a.knowledgeIndex())
	if err != nil {
		return base
	}
	return base + "\n[知识索引]" + string(idx)
}

// SetPluginInject 注入插件 pre 阶段提示片段（由 Runtime 从插件 Manager 获取）
func (a *Agent) SetPluginInject(msgs []types.Message) {
	a.pluginInject = msgs
}

// SetPluginMessageHook 注入插件每轮消息后钩子（由 Runtime 包装插件 Manager 注入）。
// 钩子在每次 buildMessages 时调用：input=本轮用户输入，round=当前执行轮次，
// 返回非空字符串时以 user role 追加在用户消息之后（近距离引导语义）。
func (a *Agent) SetPluginMessageHook(fn func(input string, round int) string) {
	a.pluginMessageHook = fn
}

// SetPluginRouteHook 注入插件路由决策钩子（由 Runtime 包装插件 Manager 注入）。
// 钩子在每次 buildMessages 时调用：mode=chat/task/goal，input=任务描述，round=轮次，
// 返回非空字符串时以 system role 注入（动态路由决策，随输入/轮次变化）。
func (a *Agent) SetPluginRouteHook(fn func(mode, input string, round int) string) {
	a.pluginRouteHook = fn
}

// pluginRouteMessages 返回路由决策钩子的 system 注入消息（空输出=不注入）。
// 钩子返回值可以是纯文本提示词，也可以是 JSON 格式 {"prompt":"...","tool_scope":"core"}。
// tool_scope 控制工具范围约束（对标 dsh 极简模式"首轮锁定最少工具"）。
func (a *Agent) pluginRouteMessages(task types.TaskCreate) []types.Message {
	a.toolScope = "" // 默认无限制
	if a.pluginRouteHook == nil {
		return nil
	}
	s := a.pluginRouteHook(task.Mode, task.Task, a.runRound)
	if s == "" {
		return nil
	}
	// 尝试解析 JSON 格式 {"prompt":"...","tool_scope":"..."}
	var parsed struct {
		Prompt    string `json:"prompt"`
		ToolScope string `json:"tool_scope"`
	}
	if err := json.Unmarshal([]byte(s), &parsed); err == nil && parsed.Prompt != "" {
		a.toolScope = parsed.ToolScope
		return []types.Message{{Role: "system", Content: parsed.Prompt}}
	}
	// 纯文本提示词（无 tool_scope）
	return []types.Message{{Role: "system", Content: s}}
}

// pluginMessageMessages 返回每轮消息后钩子的 user 注入消息（空输出=不注入）。
func (a *Agent) pluginMessageMessages(task types.TaskCreate) []types.Message {
	if a.pluginMessageHook == nil {
		return nil
	}
	if s := a.pluginMessageHook(task.Task, a.runRound); s != "" {
		return []types.Message{{Role: "user", Content: s}}
	}
	return nil
}

// SetSkillContents 注入技能内容片段（选中 skill 的正文，由 Runtime 在选中/切换技能时更新）
func (a *Agent) SetSkillContents(contents []string) {
	a.skillContents = contents
}

// SetAutoApprove 设置自动批准标记。
// task 模式（主 Agent 与子 Agent）开启：工具执行跳过权限确认弹窗，
// 实现全自动执行。
func (a *Agent) SetAutoApprove(enabled bool) {
	a.autoApprove = enabled
}

// SetVisionModel 注入识图模型（空=不切换，沿用主模型）。
func (a *Agent) SetVisionModel(modelID string) {
	a.visionModel = strings.TrimSpace(modelID)
}

// prepareVision 识图决策：本轮任务携带图片时决定模型切换或降级。
// 返回 (恢复函数, 是否降级)。
//
// 决策矩阵：
//   - 无图片 → (nil, false) 无操作；
//   - 后端不支持多模态（SupportsVision()==false，如 DeepSeek 官方纯文本
//     模型，发 image_url 会 400）→ 清空 Task.Images + 降级提示，(nil, true)。
//     此时图片内容以路径文本形式保留在用户输入中，模型仍知道图片位置；
//   - 后端支持多模态且配置了 vision_model → 切换模型并返回恢复函数；
//   - 后端支持多模态但未配置 vision_model → 主模型直接收图，(nil, false)。
func (a *Agent) prepareVision() (func(), bool) {
	n := len(a.Task.Images)
	if n == 0 {
		return nil, false
	}
	if !a.backendSupportsVision() {
		// 目标模型纯文本：绝不能让 image_url 进入请求（否则 API 400
		// unknown variant 'image_url', expected 'text'）。清空图片片段，
		// 提示随流输出；用户输入的图片路径文本仍在消息中。
		a.Task.Images = nil
		return nil, true
	}
	if a.visionModel == "" {
		return nil, false
	}
	ds, ok := a.backend.(*model.DeepSeekBackend)
	if !ok {
		return nil, false
	}
	prev := ds.ModelID()
	if prev == "" || prev == a.visionModel {
		return nil, false
	}
	ds.SwitchModel(a.visionModel)
	return func() { ds.SwitchModel(prev) }, false
}

// backendSupportsVision 报告后端是否支持 image_url 多模态输入。
// 未知后端类型（自定义实现旧接口）按不支持处理——宁可降级为文本，
// 也绝不把 image_url 发给纯文本模型触发 400。
func (a *Agent) backendSupportsVision() bool {
	type visionCapable interface{ SupportsVision() bool }
	if vc, ok := a.backend.(visionCapable); ok {
		return vc.SupportsVision()
	}
	return false
}

// visionNotice 生成识图提示文案（随流输出，TUI/GUI/CLI 统一可见）。
func visionNotice(imageCount int, visionModel string) string {
	if visionModel == "" {
		return fmt.Sprintf("🖼 识图模式：已加载 %d 张图片\n", imageCount)
	}
	return fmt.Sprintf("🖼 识图模式：已加载 %d 张图片，自动切换识图模型 %s\n", imageCount, visionModel)
}

// visionDegradedNotice 生成模型不支持识图时的降级提示文案。
// 明确告知用户原因（DeepSeek 官方纯文本）与解决路径（配置视觉模型），
// 避免"图片发了模型却没看到"的静默困惑。
func visionDegradedNotice(imageCount int) string {
	return fmt.Sprintf(
		"⚠ 检测到 %d 张图片，但当前模型不支持视觉输入（DeepSeek 官方 API 为纯文本模型，发送图片会报 400）。\n"+
			"   图片将按路径文本提示发送。如需识图，请在 config.yaml 配置：\n"+
			"   model:\n     supports_vision: true   # 接入 OpenAI 兼容视觉模型端点\n     vision_model: <视觉模型ID>\n",
		imageCount)
}

// toolCtx 返回注入自动批准标记的工具执行 context。
// chat 模式保持原样（用户在场，保留确认弹窗）。
func (a *Agent) toolCtx(ctx context.Context) context.Context {
	// 注入归属会话：检索类工具（harness_search）按 Agent 所属会话隔离，
	// 而非依赖运行时全局当前会话（GUI 并发多会话下全局可能已被切换）
	if a.sessionID != "" {
		ctx = tools.WithSessionID(ctx, a.sessionID)
	}
	if a.autoApprove {
		return tools.WithAutoApprove(ctx, true)
	}
	return ctx
}

func (a *Agent) Run(ctx context.Context) error {
	// 识图：切换视觉模型 / 纯文本模型降级。Run 为同步执行路径（子 Agent），
	// 无流式通道——降级提示输出到 stderr（CLI 可见，不污染请求消息）。
	n := len(a.Task.Images)
	restore, degraded := a.prepareVision()
	if restore != nil {
		defer restore()
	} else if degraded {
		fmt.Fprintln(os.Stderr, visionDegradedNotice(n))
	}
	query := buildQuery(a.Task)
	contextBlocks := a.harness.Search(ctx, query, a.searchMeta())

	messages := a.buildMessages(a.Task, contextBlocks)

	toolList := a.tools.ToolDefinitions()
	var loopHistory []toolCallSignature
	round := 0
	// 收尾检查点：函数任何返回路径（正常/错误/强制收尾）都落最终快照
	defer func() {
		result := ""
		if p, ok := a.Task.Payload.(map[string]string); ok {
			result = p["result"]
		}
		a.sinkCheckpoint(round, messages, true, truncate(result, 200))
	}()
	for {
		round++
		a.runRound = round
		// token 预算兜底：上下文接近上限时【先按预算压缩】释放空间，压缩后仍超红线才强制收尾。
		// 关键：不能拿"未压缩的 token 数"直接判红线——deepseek.go 发送前兜底本就会压缩，
		// 未压缩超限时压缩后通常空间充足，直接强制收尾会"莫名其妙中止任务"。
		// 压缩后仍超红线才是真正会 API 400 的硬约束，此时才提示总结（措辞温和，允许说明剩余步骤）。
		if ds, ok := a.backend.(*model.DeepSeekBackend); ok && ds.MaxContext() > 0 {
			b := ctxmgr.NewTokenBudget(ds.MaxContext())
			if b.SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
				if compacted, changed := ctxmgr.CompactToBudgetWithEvict(messages, b, a.indexCompressedEvicted); changed && compacted != nil {
					messages = compacted
				}
				if b.SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
					messages = append(messages, types.Message{
						Role:    "user",
						Content: "【上下文提示】上下文空间接近上限，请停止调用新工具，直接总结当前进展与结论；未完成部分请说明剩余步骤。",
					})
					response, _, err := a.backend.Chat(ctx, normalizeSystemFirst(messages), nil)
					if err != nil {
						return fmt.Errorf("模型调用失败: %w", err)
					}
					a.Task.Payload = map[string]string{"result": response}
					break
				}
			}
		}
		response, toolCalls, err := a.backend.Chat(ctx, normalizeSystemFirst(messages), toolList)
		if err != nil {
			return fmt.Errorf("模型调用失败: %w", err)
		}

		if len(toolCalls) == 0 {
			a.Task.Payload = map[string]string{"result": response}
			break
		}

		// 循环检测：同工具同参数连续 3 次 → 提示模型换方法
		for _, call := range toolCalls {
			loopHistory = append(loopHistory, toolCallSignature{name: call.Function.Name, args: call.Function.Arguments})
		}
		if blocked, reason := detectLoop(loopHistory); blocked {
			messages = append(messages, types.Message{
				Role:    "assistant",
				Content: response,
			})
			messages = append(messages, types.Message{
				Role:    "user",
				Content: "⚠ " + reason + "，请换一种方法或直接给出回答。",
			})
			continue
		}

		// 正确格式：assistant 消息包含 tool_calls（ID 规范化：文本兜底解析的
		// text-* 标记 ID 非法/重复，会导致下一轮 API 400 → 对话中止）
		toolCalls = normalizeToolCallIDs(toolCalls)
		messages = append(messages, types.Message{
			Role:      "assistant",
			Content:   response,
			ToolCalls: toolCalls,
		})

		for _, call := range toolCalls {
			tStart := time.Now()
			result, toolErr := a.tools.Execute(a.toolCtx(ctx), call)
			if a.tracer != nil {
				errMsg := ""
				if toolErr != nil {
					errMsg = toolErr.Error()
				}
				a.tracer.TraceRound(a.Task.Mode, round, call.Function.Name, time.Since(tStart).Milliseconds(), errMsg)
			}
			resultStr := ""
			if toolErr != nil {
				resultStr = fmt.Sprintf("错误: %v", toolErr)
			} else if result != nil {
				resultStr = formatToolResult(result)
			}
			resultStr = maybeOffloadToFile(resultStr, call.Function.Name)
			// 工具结果完整进入消息，不做截断（模型需要看到完整输出；
			// 超限保护由发送前的 ctxmgr token 预算兜底）
			messages = append(messages, types.Message{
				Role:       "tool",
				ToolCallID: call.ID,
				Content:    resultStr,
			})
		}
		// 多批次续派：一批 spawn_task 完成后动态提示评估并决定是否派发新一批
		messages = a.batchNudge(toolCalls, messages)

		// 每轮检查点：本轮消息更新完成后的状态快照
		a.sinkCheckpoint(round, messages, false, "")
	}

	for i := range contextBlocks {
		contextBlocks[i].LastUsed = time.Now()
	}

	return nil
}

// buildScope 从上下文块构建作用域范围（§3.3 scope 字段）
func (a *Agent) buildScope(context []types.ContextBlock) []string {
	var scope []string
	for _, b := range context {
		scope = append(scope, "ctx:"+b.ID)
	}
	if len(scope) == 0 {
		scope = []string{"global"}
	}
	return scope
}

// toolNames 返回当前工具表名称列表（动态，反映过滤后的实际可用工具）。
// 当 toolScope 被 route 钩子设置时，按范围约束过滤工具列表（对标 dsh 极简模式：
// 首轮只暴露核心工具，锚定后释放全部）。
func (a *Agent) toolNames() []string {
	all := a.tools.List()
	if a.toolScope == "" {
		return all
	}
	core := map[string]bool{
		"read_file": true, "write_file": true, "replace_in_file": true,
		"exec_command": true, "harness_search": true, "git_diff": true,
		"spawn_task": true, "send_message": true, "check_messages": true, "handoff": true,
		"propose_vocab": true, "run_tests": true, "list_files": true, "search_content": true,
	}
	read := map[string]bool{
		"read_file": true, "harness_search": true, "git_diff": true,
		"list_files": true, "search_content": true, "web_fetch": true,
	}
	var filtered []string
	for _, name := range all {
		switch a.toolScope {
		case "core":
			if core[name] {
				filtered = append(filtered, name)
			}
		case "read":
			if read[name] {
				filtered = append(filtered, name)
			}
		default: // "full" 或未知范围：全部放行
			filtered = append(filtered, name)
		}
	}
	// 过滤后为空时回退完整列表（防空工具集导致模型无法操作）
	if len(filtered) == 0 {
		return all
	}
	return filtered
}

// buildExamples 构建 few-shot 示例，缓存命中时精简为 1 个（§3.5 动态提示词缓存）
func (a *Agent) buildExamples() []map[string]any {
	hash := a.sysPromptHash()
	if hash == a.lastSysPromptHash && a.lastDirective != "" {
		// 缓存命中：连续相似任务，精简 examples 省 token
		return []map[string]any{
			{"in": a.lastDirective, "out": map[string]any{"tool": "read_file", "params": map[string]string{"path": "target.go"}}},
		}
	}
	a.lastSysPromptHash = hash
	a.lastDirective = a.Task.Directive
	return []map[string]any{
		{"in": "FIX_AUTH_NPE", "out": map[string]any{"tool": "read_file", "params": map[string]string{"path": "auth.go"}}},
		{"in": "TYPING_UTILS_PUB", "out": map[string]any{"tool": "write_file", "params": map[string]string{"path": "utils.go"}}},
	}
}

// sysPromptHash 计算当前任务指令的哈希（用于 §3.5 缓存命中判断）
func (a *Agent) sysPromptHash() string {
	return fmt.Sprintf("%x", len([]rune(a.Task.Directive+a.Task.Task)))
}

func buildQuery(task types.TaskCreate) string {
	return task.Directive + " " + task.Task
}

// messageWithImages 构造 user 消息：无图片时 Content 为纯文本；有图片时组装为
// OpenAI 兼容多模态片段列表（先 text 后 image_url，字符串与数组不能混用）。
func messageWithImages(text string, images []types.ContentPart) types.Message {
	if len(images) == 0 {
		return types.Message{Role: "user", Content: text}
	}
	parts := []types.ContentPart{{Type: "text", Text: text}}
	return types.Message{Role: "user", Content: append(parts, images...)}
}

// lastUserHasImages 判断历史中最近一条 user 消息是否已携带图片片段。
// chat 模式图片随对话历史透传（多轮追问仍可见图）；该检查防止图片在同一请求
// 中被编码两份（历史一份 + 本轮消息一份）造成 token 翻倍。
func lastUserHasImages(hist []types.Message) bool {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Role != "user" {
			continue
		}
		return hist[i].ImagePartCount() > 0
	}
	return false
}

// GetMessages 公开方法，供 Runtime 并发派发时调用
func (a *Agent) GetMessages(task types.TaskCreate, context []types.ContextBlock) []types.Message {
	return a.buildMessages(task, context)
}

func (a *Agent) buildMessages(task types.TaskCreate, context []types.ContextBlock) []types.Message {
	// 提示词模式由 Mode 字段决定（"task"=JSON极简 | "chat"=自然语言），
	// 与 ContextPolicy（上下文加载策略 full/recent/minimal/none）分离
	if task.Mode == "task" {
		// JSON 极简模式（长任务结构化状态设计，§3.3）
		// 彻底摒弃自然语言系统提示，所有指令以精简 JSON 表达
		sysPrompt := map[string]any{
			"directive": task.Directive,
			"task":      task.Task,
			"scope":     a.buildScope(context),
			"constraints": map[string]any{
				"lang":             "auto",
				"no_comment_bloat": true,
			},
			// task 模式多 Agent 策略：主动引导模型在可拆分任务上调用 spawn_task 并行派发
			// 派发铁律：子任务必须产出可验证成果；无产出的查询/分析/评估/检查类由主 Agent 自己执行，禁止派发
			"strategy": "task 模式优先拆分并行：若任务含 2 个以上独立子步骤（如功能实现+测试、多文件改动），必须调用 spawn_task 工具为每个独立子步骤派发子 Agent 并行执行，而非自己串行完成。简单单步任务无需拆分。⚠️派发铁律：只有『有可验证产出』的独立子步骤才可派发（产出=新增/修改文件、测试运行结果、生成的报告/文档等可见成果）；无产出的步骤（查询、分析、评估、检查、检索、总结类）由主 Agent 自己执行，禁止派发子任务。🔁多批次拆分：子任务可分批反复派发——第 1 批完成后评估回收的结果，若仍有可并行的独立工作（新文件/新测试/新报告等），继续派发第 2 批、第 3 批……直到目标完成或无可并行工作；不要因为已派发过一批就不再派发，主 Agent 应聚焦串行依赖与收尾工作，而非包办可并行部分。",
			// 词汇学习：模型发现新专有术语时主动提交映射提案，用户确认后写入词汇表
			"vocab_learning": "当任务中出现项目专有术语、技术缩写、领域名词且不在现有词汇表中时，调用 propose_vocab 工具提交映射提案（term=原始术语, mapping=缩写代号, reason=理由）。提案需用户确认后才生效，不要自行假设已加入。",
			// 持久化信息路径：告知模型配置/记忆/词汇表等文件位置，供 proprioception 自主定位
			"persistence": a.persistencePaths,
			// 知识索引：插件/技能目录与各自规范（轻量索引，详情按需用工具读，不全量内联）
			"knowledge": a.knowledgeIndex(),
		}
		ctxBlocks := contextBlocksForInject(context)
		if len(ctxBlocks) > 0 {
			keys := make([]string, len(ctxBlocks))
			for i, b := range ctxBlocks {
				keys[i] = b.ID
			}
			sysPrompt["context_keys"] = keys
		}
		// 工具表动态生成（子 Agent 过滤了 spawn_task，主 Agent 含多 Agent 协作工具）
		sysPrompt["tools_available"] = a.toolNames()
		// 技能注入（选中的 skill 正文，辅助模型遵循项目惯例和领域知识）
		if len(a.skillContents) > 0 {
			sysPrompt["skills"] = strings.Join(a.skillContents, "\n---\n")
		}
		// 多 Agent 协作接口规范（仅主 Agent depth=0 注入；§5.1 AI 自主编排）
		if a.depth == 0 {
			sysPrompt["multi_agent"] = map[string]any{
				"spawn_task": map[string]any{
					"desc": "自主决策启动子 Agent 执行独立子任务，结果同步返回。子任务必须产出可验证成果（新增/修改文件、测试结果、生成文档等）",
					"params": map[string]string{
						"task":      "自包含子任务描述（必填），必须是有明确产出的任务",
						"directive": "指令: CODE_AGENT/TEST/REFACTOR（可选）",
						"context":   "传递给子任务的上下文摘要（可选）",
					},
					"when": "仅当子步骤有明确可验证产出且可独立并行时派发；无产出的查询/分析/评估/检查由主 Agent 自己执行，不派发。可反复调用：一批子任务结果回收后，若仍有独立可并行工作，继续派发新一批（系统会在每批完成后给出【批次提示】）",
				},
				"handoff": map[string]any{
					"desc": "串行控制权转移：把当前任务整体移交给目标 Agent（planner/coder/verifier）执行，其输出作为交接结果返回。与 spawn_task 并行派发的区别：handoff 移交后当前任务暂停，适合'先规划→再实现→后验证'的串行流水线",
					"params": map[string]string{
						"to":      "目标名: planner/coder/verifier（必填）",
						"reason":  "交接原因（说明为什么移交、期望产出）",
						"payload": "附加上下文（可选）",
					},
					"when": "任务含明确串行阶段（规划→实现→验证）且当前阶段已完成时移交；可并行的子步骤用 spawn_task 而非 handoff",
				},
				"send_message": map[string]any{
					"desc":   "向其他 Agent 投递消息",
					"params": map[string]string{"to": "main/agentID/broadcast", "content": "消息内容"},
				},
				"check_messages": map[string]any{
					"desc": "读取自己的收件箱消息",
				},
			}
		}
		// Few-shot 示例压缩（§3.4）：总长度控制在 200 token 以内，缓存命中时精简（§3.5）
		sysPrompt["examples"] = a.buildExamples()
		sysJSON, _ := json.Marshal(sysPrompt)
		messages := []types.Message{
			{Role: "system", Content: string(sysJSON)},
		}
		// 插件 pre 阶段注入（system role，追加在主 system 之后）
		messages = append(messages, a.pluginInject...)
		messages = append(messages, a.pluginRouteMessages(task)...)
		// 技能注入（选中的 skill 正文，每条单独 as system role）
		if len(a.skillContents) > 0 {
			messages = append(messages, types.Message{
				Role:    "system",
				Content: "以下是本次对话加载的技能指南（SKILL）：\n" + strings.Join(a.skillContents, "\n---\n"),
			})
		}
		// 注入对话历史（task 模式继承主会话上下文，避免质检/子任务丢失 chat 中已讨论的背景）
		// 用 user role（对话背景，非系统指令；避免中间 system 触发 llama.cpp Jinja 校验）
		if hist := a.compactHistoryForTask(); len(hist) > 0 {
			messages = append(messages, types.Message{
				Role:    "user",
				Content: "[对话历史]（主会话背景，供参考理解任务上下文；其中已完成步骤请勿重复执行）:\n" + renderConversationHistory(hist),
			})
		}
		// 上下文块以 context_keys 引用，不内联全文
		// （compressed: 压缩原文块不自动注入，只经 harness_search 工具按需检索）
		for _, block := range ctxBlocks {
			messages = append(messages, types.Message{
				Role:    "user",
				Content: fmt.Sprintf("[%s] %s", block.ID, truncate(block.Content, 2000)),
			})
		}
		if task.Payload != nil {
			payloadJSON, _ := json.Marshal(task.Payload)
			// 识图：task 模式历史被展平为纯文本（见上 [对话历史] 注入），
			// 本轮图片必须显式附到 user 消息（OpenAI 兼容 image_url），
			// 否则视觉模型看不到图。
			messages = append(messages, messageWithImages(string(payloadJSON), task.Images))
		}
		// 插件每轮消息后注入（v2 hooks.message.inject，user role 随用户消息之后）
		messages = append(messages, a.pluginMessageMessages(task)...)
		return messages
	}

	// goal 模式：持久目标循环（借鉴业界同类 Agent 的持久目标循环结构）
	// 与 task 的区别：跨轮状态机驱动（plan → execute → check），每轮结束 Overseer
	// 评估，未达成自动进入下一轮（continuation 续跑），直到 [GOAL_DONE] / 轮次上限 / token 预算
	if task.Mode == "goal" {
		gs, _ := task.Payload.(*types.GoalSession)
		sysPrompt := map[string]any{
			"directive": task.Directive,
			"goal":      task.Task,
			"phase":     gs.ActivePhase(),
			"round":     fmt.Sprintf("%d/%d", gs.Round, gs.MaxRounds),
			"constraints": map[string]any{
				"lang":             "auto",
				"no_comment_bloat": true,
			},
			"strategy":          "goal 模式：跨轮持久目标循环，每轮聚焦当前 phase 推进目标，不要重复已完成工作。可调用 spawn_task 拆子任务并行执行——仅限有可验证产出的独立子步骤（写文件/测试/生成报告），无产出的查询/分析/评估由自己执行，禁止派发。🔁多批次拆分：子任务可分批反复派发——一批完成后评估回收的结果，若仍有可并行的独立工作继续派发新一批（第 2、3……批），直到目标完成或无可并行工作；主 Agent 聚焦串行依赖与收尾，不包办可并行部分。",
			"protocol":          "plan 阶段输出 [GOAL_PLAN]<JSON 步骤数组，元素 {id,desc,acceptance}>[/GOAL_PLAN] 声明步骤清单（acceptance 为该步骤验收标准）；每轮结束前必须输出 [GOAL_REFLECT]<JSON {summary,problems,next_plan}>[/GOAL_REFLECT] 反思本轮（做了什么/发现什么问题/下一步怎么改）；用 [GOAL_STEPS]<JSON，元素 {id,status,evidence}>[/GOAL_STEPS] 汇报步骤状态（done 必须带 evidence 证据）；修复的缺陷用 [GOAL_DEFECTS]<JSON，元素 {id,status,evidence}>[/GOAL_DEFECTS] 回报（status=fixed 表示已修复）；可沉淀经验用 [GOAL_LESSON]<JSON {lesson}>[/GOAL_LESSON] 记录（避免后续重犯）；全部完成时输出 [GOAL_DONE] 声明目标达成（触发打磨轮），打磨轮完成后输出 [GOAL_POLISH_DONE] 正式收尾。",
			"spec_protocol":     "spec 阶段（directive=SPEC，需求模糊或缺乏细节时）：先输出 [GOAL_SPEC]<JSON {goal_refined,assumptions,acceptance,scope,risks,open_questions}>[/GOAL_SPEC] 细化需求。确定性门槛（SpecGate）要求：goal_refined 必须引用具体目标物（文件路径/符号/命令）；assumptions 非空（显式列出假设）；acceptance 至少一条可测量（数字/百分比/命令/文件）；open_questions 必须与 assumptions 配对兜底。门槛通过前禁止输出 [GOAL_PLAN] 进入规划、禁止 [GOAL_DONE]。",
			"evidence_protocol": "步骤 done 的 evidence 必须是可独立复核的物理证据，不能只是口述。可用 [GOAL_STEPS] 元素的 proofs 字段附带确定性证据：[{kind:'file'|'diff'|'test', target:'相对路径或包路径'}]——file=文件存在、diff=git 工作区改动命中、test=go test 实测通过。系统会独立核验 proofs：失败则步骤降级 pending 并追加缺陷。声称'测试通过/全部检查/无遗漏'而没有对应工具调用（run_tests/git_diff）会被偏差检测器扣分并打标：把'我检查完了'改成'我实际检查了这些文件/符号'，用工具结果说话，不要用口述替工具背书。",
			// 知识索引：插件/技能目录与各自规范（轻量索引，详情按需用工具读，不全量内联）
			"knowledge": a.knowledgeIndex(),
		}
		// 工具表动态生成（与 task 分支一致）：明确告知模型可用工具，
		// 特别是多 Agent 协作工具 spawn_task/send_message/check_messages 的参数契约
		sysPrompt["tools_available"] = a.toolNames()
		if gs.Continuation != "" {
			sysPrompt["continuation"] = gs.Continuation
		}
		if gs.TokenBudget > 0 {
			sysPrompt["token_budget"] = fmt.Sprintf("已用 %d / 预算 %d", gs.TokenUsed, gs.TokenBudget)
		}
		pending := gs.PendingSteps()
		if len(pending) > 0 {
			sysPrompt["pending_steps"] = pending
		}
		// 多 Agent 协作接口规范（仅主 Agent depth=0 注入；goal 模式同样支持
		// spawn_task 拆子任务——子任务只读共享、结果同步返回、必须产出可验证成果）
		if a.depth == 0 {
			sysPrompt["multi_agent"] = map[string]any{
				"spawn_task": map[string]any{
					"desc": "自主决策启动子 Agent 执行独立子任务，结果同步返回。子任务必须产出可验证成果（新增/修改文件、测试结果、生成文档等）",
					"params": map[string]string{
						"task":      "自包含子任务描述（必填），必须是有明确产出的任务",
						"directive": "指令: CODE_AGENT/TEST/REFACTOR（可选）",
						"context":   "传递给子任务的上下文摘要（可选）",
					},
					"when": "仅当子步骤有明确可验证产出且可独立并行时派发；无产出的查询/分析/评估/检查由主 Agent 自己执行，不派发。可反复调用：一批子任务结果回收后，若仍有独立可并行工作，继续派发新一批（系统会在每批完成后给出【批次提示】）",
				},
				"handoff": map[string]any{
					"desc": "串行控制权转移：把当前任务整体移交给目标 Agent（planner/coder/verifier）执行，其输出作为交接结果返回。与 spawn_task 并行派发的区别：handoff 移交后当前任务暂停，适合'先规划→再实现→后验证'的串行流水线",
					"params": map[string]string{
						"to":      "目标名: planner/coder/verifier（必填）",
						"reason":  "交接原因（说明为什么移交、期望产出）",
						"payload": "附加上下文（可选）",
					},
					"when": "任务含明确串行阶段（规划→实现→验证）且当前阶段已完成时移交；可并行的子步骤用 spawn_task 而非 handoff",
				},
				"send_message": map[string]any{
					"desc":   "向其他 Agent 投递消息",
					"params": map[string]string{"to": "main/agentID/broadcast", "content": "消息内容"},
				},
				"check_messages": map[string]any{
					"desc": "读取自己的收件箱消息",
				},
			}
		}
		sysPrompt["examples"] = a.buildExamples()
		sysJSON, _ := json.Marshal(sysPrompt)
		messages := []types.Message{
			{Role: "system", Content: string(sysJSON)},
		}
		// 插件 pre 阶段注入（system role，追加在主 system 之后）
		messages = append(messages, a.pluginInject...)
		messages = append(messages, a.pluginRouteMessages(task)...)
		// 技能注入（选中的 skill 正文，每条单独 as system role）
		if len(a.skillContents) > 0 {
			messages = append(messages, types.Message{
				Role:    "system",
				Content: "以下是本次对话加载的技能指南（SKILL）：\n" + strings.Join(a.skillContents, "\n---\n"),
			})
		}
		// 注入对话历史（goal 模式继承主会话上下文，token 预算压缩保护）
		// 用 user role（对话背景，非系统指令；避免中间 system 触发 llama.cpp Jinja 校验）
		if hist := a.compactHistoryForTask(); len(hist) > 0 {
			messages = append(messages, types.Message{
				Role:    "user",
				Content: "[对话历史]（主会话背景，供参考理解任务上下文；其中已完成步骤请勿重复执行）:\n" + renderConversationHistory(hist),
			})
		}
		// 上下文块以 context_keys 引用，不内联全文
		// （compressed: 压缩原文块不自动注入，只经 harness_search 工具按需检索）
		for _, block := range contextBlocksForInject(context) {
			messages = append(messages, types.Message{
				Role:    "user",
				Content: fmt.Sprintf("[%s] %s", block.ID, truncate(block.Content, 2000)),
			})
		}
		// 识图：goal 模式历史被展平为纯文本（见上 [对话历史] 注入），
		// 本轮输入检测到的图片必须显式附到 user 消息（goal 文本在 system
		// 提示词中，无需重复；Content 仅为图片片段列表）。
		if len(task.Images) > 0 {
			messages = append(messages, types.Message{Role: "user", Content: task.Images})
		}
		// 插件每轮消息后注入（v2 hooks.message.inject，user role 随用户消息之后）
		messages = append(messages, a.pluginMessageMessages(task)...)
		return messages
	}

	// chat 模式：自然语言对话
	messages := []types.Message{
		{Role: "system",
			Content: a.chatSystemPrompt()},
	}
	// 插件 pre 阶段注入（system role，追加在主 system 之后）
	messages = append(messages, a.pluginInject...)
	messages = append(messages, a.pluginRouteMessages(task)...)

	// 注入对话历史（多轮连续性）
	for _, h := range a.conversationHistory {
		messages = append(messages, h)
	}

	// 上下文块注入（用 user role，OpenAI API 不支持 context role，会导致 400）
	// （compressed: 压缩原文块不自动注入，只经 harness_search 工具按需检索）
	for _, block := range contextBlocksForInject(context) {
		messages = append(messages, types.Message{
			Role:    "user",
			Content: fmt.Sprintf("[%s] %s", block.ID, truncate(block.Content, 2000)),
		})
	}
	// KEY_REMIND 由 harness/window.go Slots() 统一处理，此处不再重复

	if task.Payload != nil {
		content := fmt.Sprintf("%v", task.Payload)
		// 优先 JSON 序列化：%v 打印结构体指针会生成 &{Field1 val1 Field2 val2} 的
		// 不可读转储，浪费 token 且可能被模型复述污染输出
		if b, err := json.Marshal(task.Payload); err == nil {
			content = string(b)
		}
		// 识图：chat 模式图片随对话历史（多模态消息）透传——TUI/GUI 发送前
		// 已通过 AddToHistory 写入。仅当历史中尚无图片（如 webhook/ACP 渠道
		// 不经 AddToHistory 直连 ProcessInput）才把图片附到本轮消息，
		// 避免同一请求中图片编码两份。
		images := task.Images
		if lastUserHasImages(a.conversationHistory) {
			images = nil
		}
		messages = append(messages, messageWithImages(content, images))
	}
	// 插件每轮消息后注入（v2 hooks.message.inject，user role 随用户消息之后）
	messages = append(messages, a.pluginMessageMessages(task)...)

	return messages
}

// contextBlocksForInject 过滤自动注入请求的上下文块。
// ⚠️ 压缩原文回灌修复（"压缩只作用于表面、实际发送仍全量"根因之二）：
// 压缩把被移除的原文写入 Harness 索引（ID 前缀 "compressed:"），本意是供
// harness_search 工具【按需检索】找回——但 buildMessages 每轮自动
// harness.Search 会把该会话全部压缩块捞回，再以 user 消息内联全文发送，
// 等于把刚压缩掉的内容原样塞回请求（发送量甚至超过压缩前）。
// 因此自动注入必须跳过 compressed: 块：压缩原文只经 harness_search 工具
// 按模型主动查询返回，绝不自动回灌请求。
func contextBlocksForInject(blocks []types.ContextBlock) []types.ContextBlock {
	if len(blocks) == 0 {
		return blocks
	}
	out := make([]types.ContextBlock, 0, len(blocks))
	for _, b := range blocks {
		if strings.HasPrefix(b.ID, "compressed:") || strings.HasPrefix(b.ID, "KEY_REMIND_compressed:") {
			continue
		}
		out = append(out, b)
	}
	return out
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}

// compactHistoryForTask 返回注入 task 模式提示词的对话历史（受 token 预算保护）。
// task 模式（主 Agent + 子 Agent）需要继承主会话上下文，但不能撑爆上下文窗口：
// 超过 MaxContext/2 预算时用 ctxmgr 渐进压缩（截断→丢弃→摘要），保留最近消息。
// 被压缩移除的完整原文通过 indexCompressedEvicted 写入检索索引，
// 子 Agent 可通过 harness_search 找回被压缩的上下文。
func (a *Agent) compactHistoryForTask() []types.Message {
	if len(a.conversationHistory) == 0 {
		return nil
	}
	maxTokens := 8000 // 无后端信息时的默认预算
	if ds, ok := a.backend.(*model.DeepSeekBackend); ok && ds.MaxContext() > 0 {
		maxTokens = ds.MaxContext() / 2
	}
	budget := ctxmgr.NewTokenBudget(maxTokens)
	compacted, changed := ctxmgr.CompactToBudgetWithEvict(a.conversationHistory, budget, a.indexCompressedEvicted)
	if changed && compacted != nil {
		return compacted
	}
	return a.conversationHistory
}

// indexCompressedEvicted 将被压缩移除的完整原文写入 Harness 检索索引，
// 使 AI 之后可通过 harness_search 找回被截断/丢弃的内容（压缩≠永久丢失）。
// 带 role 与 [压缩原文] 标记便于识别来源；ID 用时间戳+计数器保证唯一。
// 块带归属会话（a.sessionID）：仅该会话可检索到，防止跨会话串扰。
func (a *Agent) indexCompressedEvicted(m types.Message) {
	if a.harness == nil {
		return
	}
	s, ok := m.Content.(string)
	if !ok || s == "" || strings.HasPrefix(s, "[历史摘要]") {
		return
	}
	a.evictSeq++
	block := types.ContextBlock{
		ID:        fmt.Sprintf("compressed:%d:%d", time.Now().UnixNano(), a.evictSeq),
		Content:   "[压缩原文 " + m.Role + "] " + s,
		Score:     50,
		LastUsed:  time.Now(),
		SessionID: a.sessionID,
	}
	a.harness.Index(context.Background(), []types.ContextBlock{block})
}

// searchMeta 返回本 Agent 的检索过滤元数据。
// 会话隔离：绑定了会话时仅检索"项目级共享块 + 本会话私有块"，
// 防止 task/goal 子 Agent 搜到其他会话的压缩原文/工具结果（串对话根因）。
// 未绑定（sessionID 为空）时不过滤，兼容历史行为与无会话上下文场景。
func (a *Agent) searchMeta() map[string]string {
	if a.sessionID == "" {
		return nil
	}
	return map[string]string{"session_id": a.sessionID}
}

// renderConversationHistory 将消息列表渲染为纯文本历史（task 模式注入用）。
func renderConversationHistory(hist []types.Message) string {
	var sb strings.Builder
	for _, m := range hist {
		content := m.TextContent()
		// 多模态消息：文本缺失时用图片占位标注（图片本体被展平丢弃，
		// task/goal 模式只注入纯文本历史）
		if n := m.ImagePartCount(); n > 0 {
			if content == "" {
				content = fmt.Sprintf("[图片×%d]", n)
			} else {
				content = fmt.Sprintf("[图片×%d] %s", n, content)
			}
		}
		if content == "" {
			continue
		}
		role := m.Role
		if role == "" {
			role = "unknown"
		}
		// 控制单条长度，防止单条消息过大
		content = truncate(content, 1000)
		sb.WriteString(role + ": " + content + "\n")
	}
	return sb.String()
}

// formatToolResult 格式化工具结果为字符串（借鉴业界开源 Agent 框架的精简输出策略）
func formatToolResult(result *types.ToolResult) string {
	if result == nil {
		return ""
	}
	// 工具执行失败：错误信息在 Error 字段（Registry.Execute 将 err 封装进
	// ToolResult.Error 且外层 error 为 nil），必须先于 Result 处理。
	// 否则 Result 为 nil 时 json.Marshal(nil) 会输出 "null"，真实错误被吞掉。
	if result.Error != "" {
		return "错误: " + result.Error
	}
	resultMap, ok := result.Result.(map[string]any)
	if !ok {
		resultBytes, _ := json.Marshal(result.Result)
		return string(resultBytes)
	}
	// 按优先级提取关键字段，避免完整 JSON 浪费 token
	if stdout, exists := resultMap["stdout"]; exists {
		return fmt.Sprintf("%v", stdout)
	}
	if content, exists := resultMap["content"]; exists {
		return fmt.Sprintf("%v", content)
	}
	if diff, exists := resultMap["diff"]; exists {
		return fmt.Sprintf("%v", diff)
	}
	if matches, exists := resultMap["matches"]; exists {
		return fmt.Sprintf("%v", matches)
	}
	if files, exists := resultMap["files"]; exists {
		return fmt.Sprintf("%v", files)
	}
	// 兜底：JSON 序列化但截断
	resultBytes, _ := json.Marshal(resultMap)
	return string(resultBytes)
}

// RunStream runs the agent with streaming responses. It returns channels for
// tokens and errors, allowing the caller (e.g. TUI) to consume tokens as they arrive.
func (a *Agent) RunStream(ctx context.Context) (<-chan string, <-chan error, error) {
	query := buildQuery(a.Task)
	contextBlocks := a.harness.Search(ctx, query, a.searchMeta())
	messages := a.buildMessages(a.Task, contextBlocks)
	toolList := a.tools.ToolDefinitions()

	ch := make(chan string, 256)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(errCh)

		// 注入 Agent ID 到 context，供 spawn_task/send_message 等任务工具识别调用者
		ctx = tools.WithAgentID(ctx, a.ID)

		// 识图：切换视觉模型 / 纯文本模型降级（运行结束含异常退出自动恢复）；
		// 提示随流输出，TUI/GUI/CLI 统一可见
		imgCount := len(a.Task.Images)
		restore, degraded := a.prepareVision()
		if restore != nil {
			defer restore()
			ch <- visionNotice(imgCount, a.visionModel)
		} else if degraded {
			ch <- visionDegradedNotice(imgCount)
		} else if imgCount > 0 {
			ch <- visionNotice(imgCount, a.visionModel)
		}

		var loopHistory []toolCallSignature
		var noToolRounds int  // 连续无工具调用轮次
		brokenToolRounds := 0 // 回复含工具调用痕迹但未解析出完整调用的连续轮次
		forcedFinal := false  // token 预算兜底：强制收尾
		round := 0            // 检查点轮次计数
		// 收尾检查点：任何退出路径（正常/错误/强制收尾/ctx 取消）都落最终快照
		defer func() {
			result := ""
			if p, ok := a.Task.Payload.(map[string]string); ok {
				result = p["result"]
			}
			a.sinkCheckpoint(round, messages, true, truncate(result, 200))
		}()

		// 工具执行无轮次上限：任务该做多久做多久，直到完成、用户中断（ctx.Done）、
		// token 预算红线（压缩后仍超限）、死循环检测（detectLoop）或模型主动停手。
		// 防失控不靠"轮次硬上限"——那会莫名其妙中止长任务，而靠以下真实终止条件：
		//   - ctx.Done()        用户 Ctrl+C / 取消
		//   - token 预算红线    压缩后仍超安全红线（会 API 400 的硬约束）
		//   - detectLoop        同工具同参数连续调用 ≥3 次
		//   - noToolRounds      模型连续空输出（无有效内容）≥2 次
		//   - 模型不再调工具    自然结束
		for {
			round++
			a.runRound = round
			// token 预算兜底：先按预算压缩释放空间（压缩后通常可继续执行），
			// 仅当压缩后仍超安全红线才强制收尾——避免"未压缩 token 超限"误判导致莫名其妙中止。
			if !forcedFinal {
				if ds, ok := a.backend.(*model.DeepSeekBackend); ok && ds.MaxContext() > 0 {
					b := ctxmgr.NewTokenBudget(ds.MaxContext())
					if b.SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
						if compacted, changed := ctxmgr.CompactToBudgetWithEvict(messages, b, a.indexCompressedEvicted); changed && compacted != nil {
							messages = compacted
							ch <- "🧹 上下文接近上限，已自动压缩旧消息释放空间，继续执行\n"
						}
						if b.SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
							forcedFinal = true
							ch <- "⚠ 上下文空间已满（压缩后仍超红线，唯一硬约束）：请收尾当前步骤并总结进展\n"
							messages = append(messages, types.Message{
								Role:    "user",
								Content: "【上下文提示】上下文空间接近上限，请停止调用新工具，直接总结当前进展与结论；未完成部分请说明剩余步骤。",
							})
						}
					}
				}
			}
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			// 每轮循环 drain 收件箱：把待处理消息注入对话，支持 Agent 间实时通信
			if a.bus != nil {
				for _, m := range a.bus.DrainInbox(a.ID) {
					messages = append(messages, types.Message{
						Role:    "user",
						Content: fmt.Sprintf("[来自 %s 的消息] %s", m.From, m.Content),
					})
				}
			}

			// 设置流式工具（预算兜底时禁用工具，强制模型直接总结）。
			// 用 StreamExBackend 接口断言而非 *model.DeepSeekBackend：
			// config 启用 gateway 后 backend 是 *modelgateway.handle（包装类型），
			// 旧断言失败 → tools 永不发送 → 模型输出 XML 文本 → 依赖文本兜底。
			if st, ok := a.backend.(model.StreamExBackend); ok {
				if forcedFinal {
					st.SetStreamTools(nil)
				} else {
					st.SetStreamTools(toolList)
				}
			}

			// 单次流式请求：流式输出 content + 解析 tool_calls。
			// 变量在 if/else 之前声明：else 分支（非 DeepSeekBackend）也要写入。
			var responseBuf strings.Builder
			var toolCalls []types.ToolCall
			var streamCh <-chan model.StreamChunk
			var streamErrCh <-chan error
			streaming := true // StreamEx 路径走流循环；纯文本兜底分支置 false 跳过
			if se, ok := a.backend.(model.StreamExBackend); ok {
				// StreamEx 接口（DeepSeekBackend 原生 / modelgateway handle 转发）：
				// system 前置归一化（llama.cpp 等后端要求 system 必须在开头）
				streamCh, streamErrCh = se.StreamEx(ctx, normalizeSystemFirst(messages))
			} else {
				// 无 StreamEx 的纯 model.Backend 后端（仅文本流，无结构化工具）：
				// 收集全文 → 文本兜底解析工具调用（XML/DSML/行/围栏，含截断补全）
				// → 交给主流程执行，与 StreamEx 路径走同一套 toolCalls==0/执行
				// 工具逻辑。旧逻辑在这里直接 break：不解析工具、不 append
				// assistant → 对话一轮中止、工具从不执行。
				oldCh, oldErrCh := a.backend.Stream(ctx, normalizeSystemFirst(messages))
				for token := range oldCh {
					responseBuf.WriteString(token)
					ch <- token
				}
				if e := <-oldErrCh; e != nil {
					errCh <- e
				}
				toolCalls = model.ExtractToolCallsFromText(responseBuf.String())
				// 跳过下方 for streaming 流循环（streamCh 为 nil 会永久阻塞直到
				// ctx 超时）：全文已收集完毕，直接走统一收尾
				streaming = false
			}

			var streamErr error
			var finishReason string // 流结束原因：stop/tool_calls/length/eof

			// drainStreamCh 非阻塞排空 streamCh 缓冲中已就绪的 chunk。
			// 竞争根因：StreamEx 在连接中断时先 flushPending 发 ToolCalls chunk
			// 再发 errCh；主循环 select 中 streamErr 分支命中即退出时，streamCh
			// 缓冲里已就绪的 ToolCalls chunk 可能未被消费 → toolCalls 为空 →
			// 走"半句正文重试"分支，工具从不执行（间歇性失败/flaky 测试根因）。
			// 修复：错误退出前非阻塞排空缓冲，保证 ToolCalls chunk 不丢。
			drainStreamCh := func() {
				for {
					select {
					case chunk, ok := <-streamCh:
						if !ok {
							return
						}
						if chunk.FinishReason != "" {
							finishReason = chunk.FinishReason
						}
						if chunk.IsToolCall && len(chunk.ToolCalls) > 0 {
							toolCalls = append(toolCalls, chunk.ToolCalls...)
						}
						if chunk.Content != "" {
							responseBuf.WriteString(chunk.Content)
							ch <- chunk.Content
						}
					default:
						return
					}
				}
			}

			for streaming {
				select {
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				case chunk, ok := <-streamCh:
					if !ok {
						streaming = false
						break
					}
					if chunk.FinishReason != "" {
						finishReason = chunk.FinishReason
					}
					if chunk.IsToolCall && len(chunk.ToolCalls) > 0 {
						toolCalls = append(toolCalls, chunk.ToolCalls...)
					}
					if chunk.Content != "" {
						responseBuf.WriteString(chunk.Content)
						ch <- chunk.Content
					}
				case e, ok := <-streamErrCh:
					// 仅在真实错误时结束；errCh 正常关闭（ok=false）不代表流已读完，
					// 必须继续消费 streamCh 直到其关闭，否则会丢失尾部 chunk
					if ok && e != nil {
						streamErr = e
						drainStreamCh() // 错误退出前排空缓冲，防止 ToolCalls chunk 丢失
						streaming = false
					}
				}
			}

			if streamErr != nil {
				// 连接中断但已解析出工具调用（StreamEx flushPending 已发出
				// ToolCalls chunk）→ 不丢弃，继续执行工具——这是真实场景
				// "模型输出 XML 时连接中断"的直接路径，旧逻辑在这里 return
				// 导致工具从不执行、对话中止（检查点无 assistant 消息）。
				if len(toolCalls) > 0 {
					streamErr = nil
					streaming = false
					// 注意：不能 continue 重试——工具调用已经拿到，直接执行
				} else if responseBuf.Len() > 0 {
					// 只收到半句正文（工具调用还没开始就断流）：把已收到的
					// 内容按"截断"处理，提示模型继续，不中止对话。
					brokenToolRounds++
					if brokenToolRounds >= 3 {
						ch <- "⚠ 连接多次中断，本轮到此结束\n"
						body, _ := splitThinkTagsAgent(responseBuf.String())
						messages = append(messages, types.Message{
							Role:    "assistant",
							Content: body,
						})
						break
					}
					body, _ := splitThinkTagsAgent(responseBuf.String())
					messages = append(messages, types.Message{
						Role:    "assistant",
						Content: body,
					})
					ch <- "⚠ 连接中断（已收到部分输出），请继续\n"
					messages = append(messages, types.Message{
						Role:    "user",
						Content: "上一条回复因连接中断不完整。若需要调用工具，请重新输出完整闭合的 <invoke> 调用块；否则请直接简短回答。",
					})
					continue
				}
				// 完全无输出（API 400/网络失败）→ 真实错误上报
				if streamErr != nil {
					errCh <- fmt.Errorf("模型调用失败: %w", streamErr)
					return
				}
			}

			response := responseBuf.String()

			// 将回复加入 messages
			body, _ := splitThinkTagsAgent(response)
			messages = append(messages, types.Message{
				Role:    "assistant",
				Content: body,
			})

			// 预算兜底收尾：无论模型是否调工具，直接结束
			if forcedFinal {
				break
			}

			if len(toolCalls) == 0 {
				// 模型没有调用工具。
				// 当 body 为空（只有思考没有输出）时，不结束——提示模型继续
				if strings.TrimSpace(body) == "" {
					noToolRounds++
					if noToolRounds >= 2 {
						break
					}
					ch <- "⚠ 模型思考后未输出有效内容，请继续\n"
					messages = append(messages, types.Message{
						Role:    "user",
						Content: "你已在上面的思考中得出结论，但回答正文（content）为空。请把最终结论直接作为回答正文输出；若需要执行操作请调用工具。不要只思考不行动。",
					})
					continue
				}
				// 回复含工具调用痕迹但未解析出完整调用（调用块被截断/格式异常）：
				// 不中止对话，提示模型重新输出完整调用（有重试上限防死循环）。
				// 否则模型"想调工具没调成"被当"已回答"收尾 → 对话中止、工具不执行。
				if hasToolCallTrace(body) {
					brokenToolRounds++
					if brokenToolRounds >= 3 {
						// 连续多次无法识别（模型持续输出截断 XML）：放弃，防死循环
						ch <- "⚠ 工具调用多次无法识别，本轮到此结束\n"
						break
					}
					ch <- "⚠ 检测到不完整的工具调用，请重新输出\n"
					messages = append(messages, types.Message{
						Role:    "user",
						Content: "你上一条回复中的工具调用不完整或格式无法识别（标签未闭合/参数缺失/被截断）。请重新输出完整闭合的工具调用，格式：<invoke name=\"工具名\"><parameter name=\"参数名\">值</parameter></invoke>；或直接给出回答。",
					})
					continue
				}
				// 输出被截断（finish_reason=length：max_tokens 耗尽，典型为长思考
				// 耗尽预算后工具调用/回答被腰斩；eof：连接中断提前断流）：
				// 模型根本没说完，不能当最终回答收尾 → 提示继续。
				// 这是"对话总是突然中止"的最后一条隐藏路径：模型只输出半句
				// 开场白（工具调用还没开始）就被截断，旧逻辑直接 break 收尾。
				if finishReason == "length" || finishReason == "eof" {
					brokenToolRounds++
					if brokenToolRounds >= 3 {
						// 连续多次截断（mock/代理可能从不发 finish_reason，或模型
						// 持续超长思考）：放弃重试，直接收尾，防止无限循环。
						ch <- "⚠ 输出多次被截断，本轮到此结束\n"
						break
					}
					ch <- "⚠ 输出被截断，请继续\n"
					messages = append(messages, types.Message{
						Role:    "user",
						Content: "你的上一条回复输出被截断（超出长度限制或连接中断）。请直接输出工具调用或简短回答，不要长篇思考；若需要调用工具请立即输出 <invoke> 调用块。",
					})
					continue
				}
				// 模型已给出实质回答且未调用工具 → 任务结束，直接收尾。
				// 不再注入"任务尚未完成，请继续"催促（它会诱导模型反复调工具，是死循环源头之一）。
				break
			}

			// 有工具调用 → 重置无工具计数，更新 assistant 消息
			noToolRounds = 0
			ch <- "⚡ 执行工具: " + toolCalls[0].Function.Name + "\n"
			// 工具调用 ID 规范化（关键）：文本兜底解析（XML/DSML/行格式）产出的
			// ID 是 "text-*" 格式标记——多调用时重复且非 API 接受的 id 格式，
			// 工具执行后第二轮请求携带非法/重复 tool_call_id → API 400 →
			// 模型永远拿不到工具结果 → 对话突然中止、没有最终回答。
			// 这里统一重生成合法唯一 ID（API 结构化返回的合法 ID 保留）。
			toolCalls = normalizeToolCallIDs(toolCalls)
			messages[len(messages)-1].ToolCalls = toolCalls

			// 循环检测
			for _, call := range toolCalls {
				loopHistory = append(loopHistory, toolCallSignature{name: call.Function.Name, args: call.Function.Arguments})
			}
			if blocked, reason := detectLoop(loopHistory); blocked {
				ch <- "⚠ " + reason + "\n"
				messages = append(messages, types.Message{
					Role:    "user",
					Content: "检测到重复调用同一工具同一参数，请换一种方法或直接给出回答。",
				})
				continue
			}

			// 执行工具
			toolResults := a.executeToolsConcurrent(ctx, toolCalls, ch)
			toolResults = deduplicateResults(toolCalls, toolResults)
			for i, call := range toolCalls {
				messages = append(messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    toolResults[i],
				})
				// 索引到 Harness（归属本会话，防止其他会话检索到本会话的工具结果）
				if len(toolResults[i]) > 0 {
					block := types.ContextBlock{
						ID:        "tool_" + call.ID,
						Content:   call.Function.Name + ": " + truncate(toolResults[i], 500),
						Score:     60,
						SessionID: a.sessionID,
					}
					a.harness.Index(ctx, []types.ContextBlock{block})
				}
			}
			messages = sanitizeMessages(messages)

			// 每轮检查点：本轮消息更新完成后的状态快照
			a.sinkCheckpoint(round, messages, false, "")
		}

		for i := range contextBlocks {
			contextBlocks[i].LastUsed = time.Now()
		}
	}()

	return ch, errCh, nil
}

func (a *Agent) CallTool(ctx context.Context, call types.ToolCall) (*types.ToolResult, error) {
	return a.tools.Execute(a.toolCtx(ctx), call)
}

// normalizeSystemFirst 将所有 system 消息归一化到消息列表最前（保持相对顺序），
// 其余消息（user/assistant/tool）顺序不变。
// 背景：DeepSeek 容忍 system 出现在列表中间，但 llama.cpp 等 OpenAI 兼容后端的
// Jinja 模板强制要求 "System message must be at the beginning"（中间 system 会 500）。
// 作为双保险：即使运行时注入或对话历史恢复混入了中间 system，发送前也会被纠正。
func normalizeSystemFirst(messages []types.Message) []types.Message {
	// 快速路径：前导 system 连续段之后无 system，直接返回原切片
	i := 0
	for i < len(messages) && messages[i].Role == "system" {
		i++
	}
	for j := i; j < len(messages); j++ {
		if messages[j].Role == "system" {
			goto rearrange
		}
	}
	return messages

rearrange:
	systems := make([]types.Message, 0, i+1)
	rest := make([]types.Message, 0, len(messages)-i)
	for _, m := range messages {
		if m.Role == "system" {
			systems = append(systems, m)
		} else {
			rest = append(rest, m)
		}
	}
	return append(systems, rest...)
}

// sanitizeMessages 消息结构自修复（借鉴业界主流桌面编码 Agent 的工程韧性设计）
// 1. 清理空 assistant 消息（content 为空且无 tool_calls → 会导致 API 400）
// 2. 确保每个带 tool_calls 的 assistant 消息后有对应 tool 响应
func sanitizeMessages(messages []types.Message) []types.Message {
	var result []types.Message
	pendingToolCalls := make(map[string]bool) // 待响应的 tool_call_id
	for _, msg := range messages {
		// 跳过空 assistant 消息（content 为空且无 tool_calls）
		if msg.Role == "assistant" {
			content := ""
			if s, ok := msg.Content.(string); ok {
				content = s
			}
			if content == "" && len(msg.ToolCalls) == 0 {
				continue // 清理空消息
			}
			// 记录待响应的 tool_call
			for _, tc := range msg.ToolCalls {
				pendingToolCalls[tc.ID] = true
			}
		}
		// tool 消息清除待响应标记
		if msg.Role == "tool" && msg.ToolCallID != "" {
			delete(pendingToolCalls, msg.ToolCallID)
		}
		result = append(result, msg)
	}
	// 补全缺失的 tool 响应（防止 API 400）
	for callID := range pendingToolCalls {
		result = append(result, types.Message{
			Role:       "tool",
			ToolCallID: callID,
			Content:    "(工具执行中断，结果不可用)",
		})
	}
	return result
}

// readOnlyTools 只读工具集合（可安全并行执行，借鉴业界开源 Agent 框架的三分法）
var readOnlyTools = map[string]bool{
	"read_file":      true,
	"list_files":     true,
	"search_content": true,
	"git_diff":       true,
	"web_fetch":      true,
}

// executeToolsConcurrent 并发执行工具调用（只读工具并行，spawn_task 并行，其他写工具串行）
func (a *Agent) executeToolsConcurrent(ctx context.Context, calls []types.ToolCall, ch chan<- string) []string {
	results := make([]string, len(calls))

	// 分离工具：只读 / spawn_task / 其他写工具
	var readOnlyIdx []int
	var spawnIdx []int
	var writeIdx []int
	for i, call := range calls {
		switch {
		case readOnlyTools[call.Function.Name]:
			readOnlyIdx = append(readOnlyIdx, i)
		case call.Function.Name == "spawn_task":
			// spawn_task 各子 Agent 并行执行（继承父上下文，只读共享；单 flight 已处理重复合并）
			spawnIdx = append(spawnIdx, i)
		default:
			writeIdx = append(writeIdx, i)
		}
	}

	var wg sync.WaitGroup
	// 只读工具并发执行
	for _, idx := range readOnlyIdx {
		wg.Add(1)
		go func(i int, call types.ToolCall) {
			defer wg.Done()
			results[i] = a.executeSingleTool(ctx, call, ch)
		}(idx, calls[idx])
	}

	// spawn_task 并发执行（子任务并行 + 面板并行展示）
	for _, idx := range spawnIdx {
		wg.Add(1)
		go func(i int, call types.ToolCall) {
			defer wg.Done()
			results[i] = a.executeSingleTool(ctx, call, ch)
		}(idx, calls[idx])
	}

	// 其他写工具串行执行
	for _, idx := range writeIdx {
		results[idx] = a.executeSingleTool(ctx, calls[idx], ch)
	}

	wg.Wait()
	return results
}

// executeSingleTool 执行单个工具并返回格式化结果字符串
func (a *Agent) executeSingleTool(ctx context.Context, call types.ToolCall, ch chan<- string) string {
	// 注入 TUI 进度 channel，供 spawn_task 等工具发送子 Agent 状态到主消息流
	ctx = tools.WithProgressChan(ctx, ch)
	result, toolErr := a.tools.Execute(a.toolCtx(ctx), call)
	resultStr := ""
	if toolErr != nil {
		resultStr = fmt.Sprintf("错误: %v", toolErr)
	} else if result != nil {
		resultStr = formatToolResult(result)
	}
	// 工具结果文件引用（仅超大结果转存临时文件，常规输出完整保留）
	resultStr = maybeOffloadToFile(resultStr, call.Function.Name)
	ch <- "✔ " + call.Function.Name + ": " + truncate(resultStr, 200) + "\n"
	// 完整返回工具结果，不做 2000 字符截断（模型需要完整输出）
	return resultStr
}

// maybeOffloadToFile 超大工具结果转存临时文件，消息中只保留引用。
// 阈值 128KB：绝大多数工具输出（文件读取、搜索、测试日志）完整进入上下文；
// 仅超大输出转存，避免单条消息撑爆窗口（信息不丢，模型可按引用 read_file 读取全文）。
// offloadedTempFiles 记录已创建的临时文件（用于退出时清理）
var offloadedTempFiles []string
var offloadedTempFilesMu sync.Mutex

// CleanupTempFiles 清理所有临时文件（应在退出时调用）。
// policy 为 "persistent" 时保留临时文件（仅清空记录，对应 overseer.temp_doc_policy），
// 其余值（含 "session_only"）都会删除。
func CleanupTempFiles(policy string) {
	offloadedTempFilesMu.Lock()
	defer offloadedTempFilesMu.Unlock()
	if policy != "persistent" {
		for _, f := range offloadedTempFiles {
			os.Remove(f)
		}
	}
	offloadedTempFiles = nil
}

func maybeOffloadToFile(resultStr, toolName string) string {
	if len([]rune(resultStr)) <= 131072 { // 128KB 以下完整保留，不截断
		return resultStr
	}
	tmpFile, err := os.CreateTemp("", "contextloom-tool-*.txt")
	if err != nil {
		return resultStr
	}
	tmpFile.WriteString(resultStr)
	tmpFile.Close()
	// 记录以便退出时清理
	offloadedTempFilesMu.Lock()
	offloadedTempFiles = append(offloadedTempFiles, tmpFile.Name())
	// 超过 50 个文件时清理最早的
	if len(offloadedTempFiles) > 50 {
		for i := 0; i < len(offloadedTempFiles)-50; i++ {
			os.Remove(offloadedTempFiles[i])
		}
		offloadedTempFiles = offloadedTempFiles[len(offloadedTempFiles)-50:]
	}
	offloadedTempFilesMu.Unlock()
	preview := truncate(resultStr, 800)
	return fmt.Sprintf("[已转存: %s] 预览:\n%s\n...(完整结果见临时文件)", tmpFile.Name(), preview)
}

// toolCallSignature 工具调用签名（用于去重和循环检测）
type toolCallSignature struct {
	name string
	args string
}

// detectLoop 护栏循环检测（借鉴业界开源 Agent 框架的护栏控制器）
// 同工具同参数连续失败 → 告警；同工具连续失败 → 阻止
// hasToolCallTrace 判断文本是否含工具调用痕迹（XML/DSML 标记、[tool_call] 行、
// tool_call 围栏），但调用未被成功解析。用于 agent 层兜底：模型想调工具但块
// 不完整/格式异常时，提示重新输出而非静默收尾（避免"对话中止、工具不执行"）。
func hasToolCallTrace(s string) bool {
	for _, m := range []string{
		"<invoke", "<|invoke", "<|DSML|invoke",
		"<tool_calls", "<|tool_calls", "<|DSML|tool_calls",
		"<parameter", "<|parameter", "<|DSML|parameter",
		"[tool_call_", "```tool_call",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func detectLoop(history []toolCallSignature) (bool, string) {
	if len(history) < 2 {
		return false, ""
	}
	// 同工具同参数连续调用检测（真正死循环）
	last := history[len(history)-1]
	consecutiveSame := 1
	for i := len(history) - 2; i >= 0; i-- {
		if history[i].name == last.name && history[i].args == last.args {
			consecutiveSame++
		} else {
			break
		}
	}
	// 同工具同参数连续 3 次 → 死循环，阻止
	if consecutiveSame >= 3 {
		return true, "检测到死循环: " + last.name + " 同参数连续调用 " + fmt.Sprintf("%d", consecutiveSame) + " 次，阻止继续"
	}
	// 不再阻止同工具不同参数的连续调用（模型可能在分步处理多个文件）
	return false, ""
}

// deduplicateResults 工具结果去重合并（借鉴业界开源 Agent 框架的结果去重器）
// normalizeToolCallIDs 为工具调用生成合法且唯一的 API tool_call_id。
// 背景：文本兜底解析（XML/DSML/行/围栏格式）产出的 ToolCall.ID 是 "text-*"
// 格式标记——多调用时重复、且非 OpenAI/DeepSeek 兼容 API 接受的 id 格式。
// 工具执行后下一轮请求携带 assistant.tool_calls[].id 与 tool 消息的
// tool_call_id（两者来自同一 toolCalls，ID 必须非空、唯一、可互相匹配），
// 否则 API 400 → 模型永远拿不到工具结果 → 对话突然中止、没有最终回答。
// API 结构化返回的合法 ID（call_ 前缀等）原样保留。
func normalizeToolCallIDs(calls []types.ToolCall) []types.ToolCall {
	used := make(map[string]bool, len(calls))
	for i := range calls {
		id := calls[i].ID
		if id == "" || strings.HasPrefix(id, "text-") || used[id] {
			calls[i].ID = fmt.Sprintf("call_%s_%d", calls[i].Function.Name, i+1)
		}
		used[calls[i].ID] = true
	}
	return calls
}

func deduplicateResults(calls []types.ToolCall, results []string) []string {
	seen := make(map[string]int) // signature -> first index
	for i, call := range calls {
		sig := call.Function.Name + ":" + call.Function.Arguments
		if firstIdx, exists := seen[sig]; exists {
			// 与之前结果相同则合并
			if results[i] == results[firstIdx] {
				results[i] = "[结果与第 " + fmt.Sprintf("%d", firstIdx+1) + " 次调用相同，已合并]"
			}
		} else {
			seen[sig] = i
		}
	}
	return results
}

// buildMessagesWithManifest 带 ContextManifest 的消息构建（§4.2）
// 子 Agent 基于 Manifest 构建消息（§5.1 入口契约 + §6 预算模式），
// 并注入父对话上下文（由 Runtime.SpawnTask 通过 SetConversationHistory 提供，预算压缩保护）。
// 注入项目文件树摘要 + harness 检索结果，解决子 Agent 上下文贫乏（之前输出打招呼的根因）。
func (a *Agent) buildMessagesWithManifest(task types.TaskCreate) []types.Message {
	manifest := task.Manifest
	if manifest == nil {
		return a.buildMessages(task, nil)
	}

	// 第一条 system 消息：明确编码助手角色 + 中文输出 + 禁止无关闲聊
	// 产出契约：子任务必须产出可验证成果，禁止只输出分析/评估性结论
	messages := []types.Message{
		{Role: "system",
			Content: "你是ContextLoom中文编码助手，负责在指定项目中完成具体编码/分析任务。" +
				"\n\n可用工具:read_file,exec_command,search_content,run_tests,write_file,list_files,git_diff,web_fetch,replace_in_file。" +
				"\n\n要求：1) 中文回答，简洁直接 2) 必须基于下方提供的项目上下文和工具执行结果完成任务 3) 禁止输出与任务无关的寒暄/自我介绍 4) 若上下文不足，先用 list_files/read_file 探索项目再回答" +
				" 5) ⚠️产出契约：你的子任务必须产出可验证成果——新增/修改文件（write_file/replace_in_file）、测试运行结果（run_tests）、生成的报告/文档等可见产出；禁止只输出分析/评估性结论就结束" +
				" 6) 结束时在结果中明确列出产出物清单（文件路径/测试结果），无产出即视为任务未完成。"},
	}

	// 技能注入（§7.2）：必须在 user 消息之前（system 区），
	// llama.cpp 要求 system 位于列表开头
	messages = injectSkill(messages, task.Directive)

	// 第二条 user 消息: 入口契约（工作目录+项目文件树+上下文基线+harness 检索结果）
	var contract strings.Builder
	contract.WriteString(fmt.Sprintf("工作目录: %s\n项目根目录: %s\n",
		manifest.WorkDir, manifest.ProjectRoot))

	// 项目文件树摘要（Manifect.FilePaths 为空时自动列出顶层结构）
	contract.WriteString("\n项目结构摘要:\n")
	if len(manifest.FilePaths) > 0 {
		contract.WriteString(strings.Join(manifest.FilePaths, "\n"))
	} else if manifest.ProjectRoot != "" {
		contract.WriteString(listTopLevelFiles(manifest.ProjectRoot))
	}

	if manifest.Summary != "" {
		contract.WriteString("\n\n上下文基线: " + manifest.Summary)
	}
	if manifest.ContextFile != "" {
		contract.WriteString("\n\n详细上下文已转存至: " + manifest.ContextFile)
		contract.WriteString("\n请先用 read_file 读取该文件获取完整背景。")
	}

	// 持久化信息路径：告知模型配置/记忆/词汇表等文件位置
	if len(a.persistencePaths) > 0 {
		contract.WriteString("\n\n持久化信息路径:")
		for name, path := range a.persistencePaths {
			contract.WriteString(fmt.Sprintf("\n  %s: %s", name, path))
		}
	}

	// harness 检索结果内联（§3.1 上下文供应），解决子 Agent 无上下文 starving
	// 用 task 描述作为 query 检索相关上下文块，内联进入口契约
	if a.harness != nil {
		query := buildQuery(task)
		blocks := a.harness.Search(context.Background(), query, a.searchMeta())
		if len(blocks) > 0 {
			contract.WriteString("\n\n相关上下文（检索自工作区）:\n")
			for i, b := range blocks {
				if i >= 5 { // 最多 5 块，防 token 爆炸
					break
				}
				contract.WriteString(fmt.Sprintf("--- %s ---\n%s\n\n",
					b.ID, truncate(b.Content, 400)))
			}
		}
	}

	messages = append(messages, types.Message{
		Role:    "user",
		Content: contract.String(),
	})

	// 第三条 user 消息: 具体任务指令
	messages = append(messages, types.Message{
		Role:    "user",
		Content: task.Task,
	})

	// 注入父对话上下文：子 Agent 继承主会话历史（预算压缩保护），
	// 使质检/子任务能理解 chat 中已讨论的背景，而非仅凭代码仓库盲干。
	// （由 Runtime.SpawnTask 注入父历史，见 SetConversationHistory）
	// 用 user role（对话背景，非系统指令；且避免中间 system 触发 llama.cpp Jinja 校验）
	if hist := a.compactHistoryForTask(); len(hist) > 0 {
		messages = append(messages, types.Message{
			Role:    "user",
			Content: "[父对话上下文]（主会话讨论背景，供参考；其中已完成步骤请勿重复执行）:\n" + renderConversationHistory(hist),
		})
	}
	return messages
}

// listTopLevelFiles 列出项目根目录顶层结构（最多 2 层，每层最多 30 项），
// 作为子 Agent 的项目上下文摘要，解决"无项目信息"导致的模型输出无关内容。
func listTopLevelFiles(root string) string {
	if root == "" {
		return "(未提供项目根目录)"
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return fmt.Sprintf("(无法读取项目目录: %v)", err)
	}
	var lines []string
	count := 0
	for _, e := range entries {
		if count >= 30 {
			lines = append(lines, "  ...")
			break
		}
		// 跳过隐藏目录和常见无关项
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "target" {
			continue
		}
		if e.IsDir() {
			lines = append(lines, name+"/")
			// 子目录再列一层
			sub, _ := os.ReadDir(filepath.Join(root, name))
			subCount := 0
			for _, se := range sub {
				if subCount >= 10 {
					break
				}
				if strings.HasPrefix(se.Name(), ".") {
					continue
				}
				suffix := ""
				if se.IsDir() {
					suffix = "/"
				}
				lines = append(lines, "  "+se.Name()+suffix)
				subCount++
			}
		} else {
			lines = append(lines, name)
		}
		count++
	}
	return strings.Join(lines, "\n")
}

// RunStreamWithBudget 带迭代预算的流式执行（§6.2）
func (a *Agent) RunStreamWithBudget(ctx context.Context, maxIter int) (<-chan string, <-chan error, error) {
	// maxIter 参数保留（兼容调用方），但不再作为硬上限——
	// 工具执行无轮次上限，任务该做多久做多久。
	// 终止条件：ctx.Done()（用户中断）、token 预算红线（压缩后仍超限）、
	// detectLoop（同参 3 次）、模型不再调用工具（自然结束）。
	_ = maxIter

	// 如果有 Manifest，用 buildMessagesWithManifest
	var messages []types.Message
	if a.Task.Manifest != nil {
		messages = a.buildMessagesWithManifest(a.Task)
	} else {
		query := buildQuery(a.Task)
		contextBlocks := a.harness.Search(ctx, query, a.searchMeta())
		messages = a.buildMessages(a.Task, contextBlocks)
	}

	toolList := a.tools.ToolDefinitions()

	ch := make(chan string, 256)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(errCh)

		// 注入 Agent ID 到 context，供 spawn_task/send_message/check_messages 等
		// 任务工具识别调用者身份（子 Agent 必须用自己的 ID 寻址收件箱，
		// 否则 AgentIDFromContext 回退 "main"，消息错投/误读主 Agent 收件箱）。
		ctx = tools.WithAgentID(ctx, a.ID)

		// 识图：切换视觉模型 / 纯文本模型降级（运行结束含异常退出自动恢复）；
		// 提示随流输出
		imgCount := len(a.Task.Images)
		restore, degraded := a.prepareVision()
		if restore != nil {
			defer restore()
			ch <- visionNotice(imgCount, a.visionModel)
		} else if degraded {
			ch <- visionDegradedNotice(imgCount)
		} else if imgCount > 0 {
			ch <- visionNotice(imgCount, a.visionModel)
		}

		var lastToolCalls []types.ToolCall
		var lastToolResults []string
		var loopHistory []toolCallSignature
		forcedFinal := false  // token 预算兜底：强制收尾
		brokenToolRounds := 0 // 回复含工具调用痕迹但未解析出完整调用的连续轮次（上限防死循环）

		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				errCh <- ctx.Err()
				return
			default:
			}

			// 每轮循环 drain 收件箱：把待处理消息注入对话，支持 Agent 间实时通信。
			// 与主 Agent 路径（RunStream）保持一致——子 Agent 同样能收到
			// 其他 Agent 发来的消息（send_message/check_messages 的接收端）。
			if a.bus != nil {
				for _, m := range a.bus.DrainInbox(a.ID) {
					messages = append(messages, types.Message{
						Role:    "user",
						Content: fmt.Sprintf("[来自 %s 的消息] %s", m.From, m.Content),
					})
				}
			}

			// token 预算兜底：先按预算压缩释放空间（压缩后通常可继续执行），
			// 仅当压缩后仍超安全红线才强制收尾——避免"未压缩 token 超限"误判导致莫名其妙中止。
			if !forcedFinal {
				if ds, ok := a.backend.(*model.DeepSeekBackend); ok && ds.MaxContext() > 0 {
					b := ctxmgr.NewTokenBudget(ds.MaxContext())
					if b.SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
						if compacted, changed := ctxmgr.CompactToBudgetWithEvict(messages, b, a.indexCompressedEvicted); changed && compacted != nil {
							messages = compacted
							ch <- "🧹 上下文接近上限，已自动压缩旧消息释放空间，继续执行\n"
						}
						if b.SafetyExceeded(ctxmgr.MessagesTokens(messages)) {
							forcedFinal = true
							ch <- "⚠ 上下文空间已满（压缩后仍超红线，唯一硬约束）：请收尾当前步骤并总结进展\n"
							messages = append(messages, types.Message{
								Role:    "user",
								Content: "【上下文提示】上下文空间接近上限，请停止调用新工具，直接总结当前进展与结论；未完成部分请说明剩余步骤。",
							})
						}
					}
				}
			}

			// 每 2 轮注入验证指令（§6.1）
			// 用 user role 而非 system（llama.cpp 等后端要求 system 位于列表开头）
			if i > 0 && i%2 == 0 {
				messages = append(messages, types.Message{
					Role: "user",
					Content: "【验证指令】" +
						"1. 用 exec_command 运行 pwd 确认当前工作目录 " +
						"2. 确认目标文件存在 " +
						"3. 如果目录或文件不存在，立即停止并报告",
				})
			}

			// 全流式：优先 StreamEx（一次流式拿全 content + 结构化 tool_calls），
			// 无 StreamEx 的后端退回纯文本 Stream + 文本兜底解析工具调用。
			// 旧实现在流式结束后又调一次非流式 Chat 拿 tool_calls——每轮 2 次请求
			// （流式一遍、非流式再一遍），既浪费 token，又让第二次请求无法流式显示。
			if st, ok := a.backend.(model.StreamExBackend); ok {
				if forcedFinal {
					st.SetStreamTools(nil)
				} else {
					st.SetStreamTools(toolList)
				}
			}
			var responseBuf strings.Builder
			var toolCalls []types.ToolCall
			var streamCh <-chan model.StreamChunk
			var streamErrCh <-chan error
			var streamErr error
			streaming := true
			if se, ok := a.backend.(model.StreamExBackend); ok {
				streamCh, streamErrCh = se.StreamEx(ctx, normalizeSystemFirst(messages))
			} else {
				// 该后端无结构化流式能力（未实现 StreamEx）：流式仅用于实时显示，
				// 结构化 tool_calls 拿不到，需再走一次非流式 Chat 获取。
				// 这是能力必需的兜底，非主路径——DeepSeek 原生后端与 modelgateway
				// handle 均实现 StreamEx，生产主路径全流式、每轮仅 1 次请求。
				oldCh, oldErrCh := a.backend.Stream(ctx, normalizeSystemFirst(messages))
				draining := true
				for draining {
					select {
					case <-ctx.Done():
						errCh <- ctx.Err()
						return
					case token, ok := <-oldCh:
						if !ok {
							draining = false
							break
						}
						responseBuf.WriteString(token)
						ch <- token
					case e, ok := <-oldErrCh:
						if ok && e != nil {
							streamErr = e
						}
					}
				}
				streaming = false
				if !forcedFinal && streamErr == nil {
					if r2, tc, e := a.backend.Chat(ctx, normalizeSystemFirst(messages), toolList); e == nil {
						responseBuf.Reset()
						responseBuf.WriteString(r2)
						toolCalls = tc
					}
				}
			}

			for streaming {
				select {
				case <-ctx.Done():
					errCh <- ctx.Err()
					return
				case chunk, ok := <-streamCh:
					if !ok {
						streaming = false
						break
					}
					if chunk.IsToolCall && len(chunk.ToolCalls) > 0 {
						toolCalls = append(toolCalls, chunk.ToolCalls...)
					}
					if chunk.Content != "" {
						responseBuf.WriteString(chunk.Content)
						ch <- chunk.Content
					}
				case e, ok := <-streamErrCh:
					// 仅在真实错误时结束；errCh 正常关闭（ok=false）不代表流已读完，
					// 必须继续消费 streamCh 直到其关闭，否则会丢失尾部 chunk。
					// 与主循环同一竞争根因：错误退出前必须非阻塞排空 streamCh，
					// 否则缓冲里已就绪的 ToolCalls chunk 可能未被消费 → 子 Agent
					// 误判"截断无工具调用"→ 提示重发 → 工具从不执行。
					if ok && e != nil {
						streamErr = e
					drainLoop:
						for {
							select {
							case chunk, ok := <-streamCh:
								if !ok {
									break drainLoop
								}
								if chunk.IsToolCall && len(chunk.ToolCalls) > 0 {
									toolCalls = append(toolCalls, chunk.ToolCalls...)
								}
								if chunk.Content != "" {
									responseBuf.WriteString(chunk.Content)
									ch <- chunk.Content
								}
							default:
								break drainLoop
							}
						}
						streaming = false
					}
				}
			}

			if streamErr != nil {
				errCh <- fmt.Errorf("模型调用失败: %w", streamErr)
				return
			}

			response := responseBuf.String()
			// 预算兜底收尾：不再调用工具，直接结束
			if forcedFinal {
				break
			}
			if len(toolCalls) == 0 {
				// 回复含工具调用痕迹但未解析成功（截断/格式异常）：提示重发，
				// 避免子任务"想调工具没调成"被当完成静默中止。
				// 有上限：连续多次仍无法识别时强制结束，防止模型持续输出
				// 截断 XML 导致子 Agent 无限循环（每轮还额外多一次 API 调用）。
				if hasToolCallTrace(response) {
					brokenToolRounds++
					if brokenToolRounds >= 3 {
						ch <- "⚠ 工具调用多次无法识别，直接结束子任务\n"
						break
					}
					ch <- "⚠ 检测到不完整的工具调用，请重新输出\n"
					messages = append(messages, types.Message{
						Role:    "user",
						Content: "你上一条回复中的工具调用不完整或格式无法识别（标签未闭合/参数缺失/被截断）。请重新输出完整闭合的工具调用，或直接给出回答。",
					})
					continue
				}
				break
			}
			ch <- "⚡ 执行工具: " + toolCalls[0].Function.Name + "\n"
			messages = append(messages, types.Message{
				Role:      "assistant",
				Content:   response,
				ToolCalls: toolCalls,
			})

			for _, call := range toolCalls {
				loopHistory = append(loopHistory, toolCallSignature{name: call.Function.Name, args: call.Function.Arguments})
			}
			if blocked, reason := detectLoop(loopHistory); blocked {
				ch <- "⚠ " + reason + "\n"
				messages = append(messages, types.Message{
					Role:    "user",
					Content: "检测到重复调用同一工具同一参数，请换一种方法或直接给出回答。",
				})
				continue
			}
			toolResults := a.executeToolsConcurrent(ctx, toolCalls, ch)
			toolResults = deduplicateResults(toolCalls, toolResults)
			for i, call := range toolCalls {
				messages = append(messages, types.Message{
					Role:       "tool",
					ToolCallID: call.ID,
					Content:    toolResults[i],
				})
			}
			// 多批次续派：一批 spawn_task 完成后动态提示评估并决定是否派发新一批
			messages = a.batchNudge(toolCalls, messages)
			lastToolCalls = toolCalls
			lastToolResults = toolResults
			_ = lastToolCalls
			_ = lastToolResults
		}
	}()

	return ch, errCh, nil
}

// splitThinkTagsAgent 剥离思考标签，返回 (body, thinks)
// 支持模型层标记 <|reasoning|>/<|/reasoning|>（deepseek.go 输出）和尖括号 <think>/<thinking>
func splitThinkTagsAgent(s string) (string, string) {
	var body, think strings.Builder
	for {
		// 找最早出现的开标签
		start := -1
		openTag := ""
		for _, tag := range []string{"<|reasoning|>", "<thinking>", "<think>"} {
			if idx := strings.Index(s, tag); idx != -1 && (start == -1 || idx < start) {
				start = idx
				openTag = tag
			}
		}
		if start < 0 {
			body.WriteString(s)
			break
		}
		body.WriteString(s[:start])
		rest := s[start+len(openTag):]
		// 找结束标签
		end := -1
		closeTag := ""
		for _, tag := range []string{"<|/reasoning|>", "</thinking>", "</think>"} {
			if idx := strings.Index(rest, tag); idx != -1 && (end == -1 || idx < end) {
				end = idx
				closeTag = tag
			}
		}
		if end < 0 {
			think.WriteString(rest)
			break
		}
		think.WriteString(rest[:end])
		s = rest[end+len(closeTag):]
	}
	return strings.TrimSpace(body.String()), think.String()
}
