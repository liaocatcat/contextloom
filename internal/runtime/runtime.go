package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/contextloom/contextloom-agent/internal/acp"
	"github.com/contextloom/contextloom-agent/internal/audit"
	"github.com/contextloom/contextloom-agent/internal/checkpoint"
	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/executor"
	"github.com/contextloom/contextloom-agent/internal/goalmgr"
	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/market"
	"github.com/contextloom/contextloom-agent/internal/mcpclient"
	"github.com/contextloom/contextloom-agent/internal/memory"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/modelgateway"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/internal/plugin"
	"github.com/contextloom/contextloom-agent/internal/preprocessor"
	sbox "github.com/contextloom/contextloom-agent/internal/sandbox"
	"github.com/contextloom/contextloom-agent/internal/scheduler"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/internal/trace"
	"github.com/contextloom/contextloom-agent/internal/vision"
	"github.com/contextloom/contextloom-agent/internal/webhook"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Runtime struct {
	Config   *config.Config
	Harness  *harness.Engine
	Memory   *memory.System
	Plugins  *plugin.Manager
	Tools    *tools.Registry
	Backend  model.Backend
	Gateway  *modelgateway.Gateway // 模型网关（§4.2）：default 池指向 Backend，旧路径零改动
	Executor *executor.Executor
	ACP      *acp.Server
	Preproc  *preprocessor.Preprocessor
	sandbox  *plugin.Sandbox
	running  bool

	// 对话连续性：累积所有轮次的 user/assistant 消息
	conversationHistory []types.Message
	historyMu           sync.RWMutex

	// 待消费图片暂存：GUI 上传图片（base64 data URL）由 chat.send 先解码暂存，
	// 随本次 ProcessInput 消费为多模态片段（与视频/文档等结构化工件同路径）。
	// 受 historyMu 保护——与 ProcessInput 取用/AddToHistory 同锁，避免并发读写竞态。
	stagedImages []vision.Image

	// 上下文预算控制器（token 预算驱动，替代旧版按条数断崖截断）
	ctxBudget *ctxmgr.TokenBudget

	// Harness 检索索引持久化：压缩产生的检索块跨进程落盘（.contextloom/harness/index.json），
	// 进程重启后 Agent 仍可通过 harness_search 找回被压缩的对话原文。
	harnessIndexPath string
	harnessSaveStop  chan struct{} // 关闭时停止自动保存 goroutine
	harnessSaveDone  chan struct{} // 自动保存 goroutine 退出信号

	// 目标循环管理器（goal 模式状态机 + 持久化）
	goalMgr *goalmgr.Manager

	// 文件操作审计器（企业治理审计追踪，JSONL 按日轮转）
	auditor *audit.Auditor

	// 多 Agent 检查点存储（对标 LangGraph checkpointer 的零依赖 JSONL 落地）
	checkpoints *checkpoint.Store
	// OTLP trace 导出器（配置 trace.export_endpoint 时启用）
	traceExporter *trace.Exporter

	// handoff 交接注册表（对标 OpenAI Agents SDK handoff：串行控制权转移）
	handoffReg *executor.HandoffRegistry

	// 外部消息渠道（对标业界同类 Agent 的消息渠道）：webhook=HTTP 触发，cron=定时触发
	webhookSrv *webhook.Server
	cronSched  *scheduler.Scheduler

	// 技能/插件市场（对标业界同类 Agent 的插件/技能生态）：内置 catalog + 远程 URL + 搜索/安装
	Market *market.Market

	// 工具沙箱（config sandbox.enabled）：影子文件系统隔离写操作
	sbFS *sbox.ShadowFS

	// 子任务活动式超时（默认 10 分钟无输出终止；持续输出不设限）
	childNoOutputTimeout time.Duration

	// 监工 Agent（§9 — task 模式时创建）
	currentOverseer *overseer.Overseer
	lastAgentID     string // 最近一次请求的 Agent ID，供监工评估关联
	overseerMu      sync.RWMutex

	// 偏差检测（对照 why-self-verification 一文）：最近一轮 OverseerEvaluate
	// 的偏差命中记录，GoalOnRoundEnd 消费后转缺陷条目并清空。
	lastBiasHits []overseer.BiasHit
	biasMu       sync.Mutex

	// roundEvidence 当前轮的外部验证工具证据累加器（偏差检测器输入）。
	// TraceRound 每记一次工具调用便累计；GoalOnRoundEnd 消费并清空进入下一轮。
	// 与 trace 开关解耦（纯内存），trace 关闭时偏差检测依然可取证。
	evMu          sync.Mutex
	roundEvidence overseer.ToolEvidence

	// 多 Agent 协作（§5.1）
	bus          *executor.MessageBus // Agent 间消息总线
	childCounter int64                // 子 Agent ID 计数器（childMu 保护）
	childMu      sync.Mutex

	// 员工中心（M3 接线）：惰性加载的员工档案缓存 + 门禁状态（M5 接线）
	employees    *employeeCenter
	employeeOnce sync.Once // 员工中心单例初始化（P2-2：并发首次访问 lost-update 竞态修复）
	gates        *gateState
	// 质量账本（M5 持久化接线）：.contextloom/gates/quality_ledger.yaml 惰性落盘状态
	// 字段名后缀 State 以与方法 gateLedger() 区分（避免 Go 字段/方法同名冲突）。
	gateLedgerState *ledgerState

	// 技能加载器
	skillLoader *SkillLoader
	// 当前会话选中的技能名列表（会话持久化）
	activeSkills []string
	// 当前会话 ID（由 NewSession 或 LoadSession 设置，供 TUI 使用）
	currentSessionID string
	// modeBySID 按会话隔离的工作模式（问题 2 修复：GUI 多会话下切换模式
	// 不再影响其他会话）。键=会话 UUID，值为 chat/task/goal；未设置的会话
	// 回退全局默认 Config.CLIDefaults.Mode（该默认值持久化于 settings.json 的 mode 字段）。
	modeBySID map[string]string
	// evictSeq 被压缩原文入索引的 ID 计数器（保证索引块 ID 唯一）
	evictSeq int64
	// evictPending 锁内收集的压缩原文（historyMu 写锁期间不得调用 Harness.Index——
	// 那会长时间持 Harness 全局锁并触发全量倒排重建，导致压缩期间所有 WS
	// 请求（status.bar 轮询/loadSession/点击）在 historyMu 上排队，界面表现为
	// "无反应+状态栏不刷+历史不加载"（真实用户反馈根因）。改为锁内只收集、
	// 锁外由 flushEvictPending 批量入索引，锁持有时间降到 O(1)。
	evictPending []types.ContextBlock
	// Trace 轻量可观测记录器（JSONL，按日轮转；trace.enabled=false 时为空操作）
	Trace *trace.Recorder
	// MCPClients 外部 MCP server 客户端（mcp.servers 配置启动）
	MCPClients map[string]*mcpclient.Client
	mcpMu      sync.Mutex
}

// startMCPServers 按配置启动外部 MCP server 并动态注册其工具
// （工具名 "mcp.<server>.<tool>"，执行时转发 tools/call）。
// 连接失败的 server 只警告跳过，不阻断 runtime 启动。
func (rt *Runtime) startMCPServers() {
	if rt.Config.MCP.Servers == nil || len(rt.Config.MCP.Servers) == 0 {
		return
	}
	for name, sc := range rt.Config.MCP.Servers {
		if sc.Enabled != nil && !*sc.Enabled {
			continue
		}
		if sc.Command == "" {
			fmt.Fprintf(os.Stderr, "警告: MCP server %s 未配置 command，已跳过\n", name)
			continue
		}
		client := mcpclient.New(name, mcpclient.ServerConfig{Command: sc.Command, Args: sc.Args, Env: sc.Env})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := client.Connect(ctx); err != nil {
			cancel()
			fmt.Fprintf(os.Stderr, "警告: MCP server %s 连接失败: %v（已跳过）\n", name, err)
			continue
		}
		cancel()
		defs, err := client.ListTools(context.Background())
		if err != nil {
			fmt.Fprintf(os.Stderr, "警告: MCP server %s tools/list 失败: %v（已断开）\n", name, err)
			client.Close()
			continue
		}
		registered := 0
		for _, def := range defs {
			// 内部名 mcp.<server>.<tool>（点分）仅用于拆分出 server/tool；
			// 注册名必须符合 OpenAI 兼容 API 的工具名约束 ^[a-zA-Z0-9_-]+$
			// （含点号会导致 API 400 "Invalid 'tools[].name'"）。
			server, tool := splitMCPTool(def.Function.Name)
			if server == "" {
				fmt.Fprintf(os.Stderr, "警告: MCP server %s 工具名非法 %q，已跳过\n", name, def.Function.Name)
				continue
			}
			toolName := "mcp_" + sanitizeToolName(server) + "_" + sanitizeToolName(tool)
			fn := func(ctx context.Context, params map[string]any) (any, error) {
				out, err := rt.callMCPTool(server, tool, params)
				if err != nil {
					return nil, err
				}
				return out, nil
			}
			rt.Tools.RegisterExtra(toolName, def.Function.Description, def.Function.Parameters, fn)
			registered++
		}
		rt.mcpMu.Lock()
		if rt.MCPClients == nil {
			rt.MCPClients = make(map[string]*mcpclient.Client)
		}
		rt.MCPClients[name] = client
		rt.mcpMu.Unlock()
		fmt.Fprintf(os.Stderr, "已连接 MCP server %s（注册 %d 个工具）\n", name, registered)
	}
}

// callMCPTool 转发工具调用到对应 MCP server。
func (rt *Runtime) callMCPTool(server, tool string, params map[string]any) (string, error) {
	rt.mcpMu.Lock()
	client := rt.MCPClients[server]
	rt.mcpMu.Unlock()
	if client == nil {
		return "", fmt.Errorf("MCP server %s 未连接", server)
	}
	return client.CallTool(context.Background(), tool, params)
}

// CloseMCPServers 关闭全部 MCP server 客户端（退出时调用）。
func (rt *Runtime) CloseMCPServers() {
	rt.mcpMu.Lock()
	defer rt.mcpMu.Unlock()
	for name, client := range rt.MCPClients {
		client.Close()
		delete(rt.MCPClients, name)
		_ = name
	}
}

// splitMCPTool 从内部名 "mcp.<server>.<tool>"（点分，mcpclient 返回的原始名）拆出 server 与 tool 名。
// 注意：注册到模型工具表的对外名已转换为 "mcp_<server>_<tool>"（OpenAI 兼容 API
// 要求工具名匹配 ^[a-zA-Z0-9_-]+$，含点号会触发 400），拆分仅用于内部转发。
func splitMCPTool(full string) (string, string) {
	parts := strings.SplitN(full, ".", 3)
	if len(parts) != 3 {
		return "", full
	}
	return parts[1], parts[2]
}

// sanitizeToolName 将工具名转换为 OpenAI 兼容 API 允许的字符集 ^[a-zA-Z0-9_-]+$：
// 非字母数字下划线连字符的字符统一替换为下划线。
// 插件工具与 MCP 工具名（可能含点号/空格/中文）不经过此转换直接发送，
// DeepSeek 等严格校验的 API 会返回 400 "Invalid 'tools[].name'".
func sanitizeToolName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	return b.String()
}

// TraceRound 实现 executor.Tracer：记录 agent 每轮工具调用（runtime 作为 tracer 注入 Agent）。
// 同时累计本轮外部验证工具证据（纯内存，与 trace 开关解耦）：
// run_tests → RanTests；git_diff → RanDiff。GoalOnRoundEnd 消费后清空。
func (rt *Runtime) TraceRound(mode string, round int, tool string, ms int64, errMsg string) {
	if rt.Trace != nil {
		rt.Trace.Record(trace.Entry{Event: "tool_call", Mode: mode, Round: round, Tool: tool, Ms: ms, Err: errMsg})
	}
	rt.evMu.Lock()
	switch tool {
	case "run_tests":
		rt.roundEvidence.RanTests = true
	case "git_diff":
		rt.roundEvidence.RanDiff = true
	}
	rt.evMu.Unlock()
}

// takeRoundEvidence 取出并清空本轮工具证据（GoalOnRoundEnd 每轮消费一次）。
func (rt *Runtime) takeRoundEvidence() overseer.ToolEvidence {
	rt.evMu.Lock()
	defer rt.evMu.Unlock()
	ev := rt.roundEvidence
	rt.roundEvidence = overseer.ToolEvidence{}
	return ev
}

func New(cfg *config.Config) (*Runtime, error) {
	// 将项目根目录解析为绝对路径（支持 "." 和相对路径），
	// 后续所有 .contextloom/ 子目录均以此为基准，确保配置中的相对路径正确定位到启动目录。
	projRoot, err := filepath.Abs(cfg.Project.Root)
	if err != nil {
		return nil, fmt.Errorf("解析项目根目录失败: %w", err)
	}
	cfg.Project.Root = projRoot

	harnessCfg := harness.Config{
		ScoreThreshold: cfg.Harness.ScoreThreshold,
		DecayFactor:    cfg.Harness.DecayFactor,
		AnchorRatio:    cfg.Harness.AnchorRatio,
	}
	harnessEngine := harness.New(harnessCfg)
	// 加载持久化检索索引（压缩原文找回的跨进程底座）。
	// 无文件=首次运行（空索引）；损坏/版本不兼容仅警告不阻塞——检索缺失不影响主流程。
	harnessIndexPath := filepath.Join(projRoot, ".contextloom", "harness", "index.json")
	if err := harnessEngine.LoadIndex(harnessIndexPath); err != nil {
		fmt.Fprintf(os.Stderr, "警告: Harness 索引加载失败(%s): %v\n", harnessIndexPath, err)
	}

	memoryDir := resolvePath(cfg.Memory.Directory, projRoot)
	memSystem, err := memory.New(memoryDir, cfg.Memory.MaxSessions)
	if err != nil {
		return nil, fmt.Errorf("记忆系统初始化失败: %w", err)
	}

	// 插件：用户级 + 项目级合并扫描。
	// 用户级（默认 ~/.contextloom/plugins）放个人通用插件，跨项目共享；
	// 项目级（.contextloom/plugins/）放团队共享插件，随项目提交分发；
	// 同名插件项目级优先（后加载覆盖）。
	pluginDir := resolvePath(cfg.Plugins.Directory, projRoot)
	pluginMgr := plugin.New(pluginDir)
	if cfg.Plugins.Enabled {
		if n, err := pluginMgr.LoadFromDir(pluginDir); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 用户级插件加载失败(%s): %v\n", pluginDir, err)
		} else if n > 0 {
			fmt.Fprintf(os.Stderr, "已加载 %d 个用户级插件\n", n)
		}
		projectPluginDir := filepath.Join(projRoot, ".contextloom", "plugins")
		// 防御：用户把 plugins.directory 显式配成项目级目录时，避免同一目录重复扫描
		if filepath.Clean(projectPluginDir) != filepath.Clean(pluginDir) {
			if n, err := pluginMgr.LoadFromDir(projectPluginDir); err != nil {
				fmt.Fprintf(os.Stderr, "警告: 项目级插件加载失败(%s): %v\n", projectPluginDir, err)
			} else if n > 0 {
				fmt.Fprintf(os.Stderr, "已加载 %d 个项目级插件\n", n)
			}
		}
	}

	toolReg := tools.NewRegistry()
	tools.SetProjectRoot(projRoot)

	am := cfg.ActiveModel()
	sendEffort := true
	if am.SendEffort != nil {
		sendEffort = *am.SendEffort
	}
	backend, err := model.NewBackend(
		am.Backend,
		am.Endpoint,
		am.APIKey,
		am.Backend,
		am.MaxContext,
		sendEffort,
	)
	if err != nil {
		return nil, err
	}
	if am.TopP != nil {
		backend.SetTopP(*am.TopP)
	}
	if am.Temperature != nil {
		backend.SetTemperature(*am.Temperature)
	}
	// 多模态能力标志注入：默认 false（DeepSeek 官方纯文本模型，发 image_url
	// 直接 400）；仅当用户显式配置 supports_vision=true（OpenAI 兼容视觉端点）开启。
	// Agent 据此决策识图切换/降级，模型层据此做发送前剥离兜底。
	if ds, ok := backend.(*model.DeepSeekBackend); ok {
		ds.SetSupportsVision(am.SupportsVision)
	}

	exec := executor.New(harnessEngine, toolReg)

	// 模型网关（方案 §4.2）：从 config.yaml 的 gateway: 节构建实例池与路由规则。
	// 桥接语义（§4.2.2）：gateway.pools.<池名> 优先解析为 models: 预设名——按预设
	// 的 backend/endpoint/api_key/max_context/send_effort 建实例（多 key 池化），
	// 池内限流/熔断/重试参数取自 gateway.pools.<池名>；找不到同名预设时退化
	// 为 pool 内的裸连接参数。
	// default 池 = 当前激活预设 Backend（旧路径零改动：未配置 gateway 时 Select
	// 返回的句柄直接代理 rt.Backend）。降级事件接入标准输出提示（用户可见）。
	//
	// 铁律：gateway 节完全未配置（default/rules/pools 全空）→ 网关不启用
	//（rt.Gateway = nil），所有调用点回退 rt.Backend——零配置零行为变化，
	// 也保证运行期替换 rt.Backend（如测试注入 mock）的既有语义不被网关缓存破坏。
	gwCfg := cfg.GatewayModelConfig()
	gatewayEnabled := cfg.Gateway.Default != "" || len(cfg.Gateway.Rules) > 0 || len(cfg.Gateway.Pools) > 0
	var gateway *modelgateway.Gateway
	if gatewayEnabled {
		gateway, err = modelgateway.New(gwCfg, backend)
		if err != nil {
			return nil, fmt.Errorf("构建模型网关失败: %w", err)
		}
		gateway.SetOnDegrade(func(notice string) {
			fmt.Fprintf(os.Stderr, "⚡ 模型网关降级：%s\n", notice)
		})
	}

	acpSrv := acp.NewServer(cfg.ACP.Port)

	// 词汇表路径：相对路径基于项目根（projRoot），支持 ~ 展开，避免 CWD 依赖
	cfg.Project.Vocab = resolvePath(cfg.Project.Vocab, projRoot)
	preproc := preprocessor.New(cfg.Project.Vocab)

	sandbox := plugin.NewSandbox(
		cfg.Plugins.Sandbox.MemoryLimitMB,
		cfg.Plugins.Sandbox.CPULimit,
	)

	// 创建 Agent 间消息总线（§5.1）并注入 Executor
	bus := executor.NewMessageBus()
	exec.SetBus(bus)

	// 注册 task 模式任务工具：spawn_task / send_message / check_messages
	// runtime 实现 tools.TaskSpawner，注入闭包；模型自主决策调用
	toolReg.Register("spawn_task", tools.MakeSpawnTaskTool(nil)) // 占位，下方用 rt 替换
	toolReg.Register("send_message", tools.MakeSendMessageTool(bus))
	toolReg.Register("check_messages", tools.MakeCheckMessagesTool(bus))
	// propose_vocab 占位，下方用 rt 替换（rt 需实现 VocabProposer）
	toolReg.Register("propose_vocab", tools.MakeProposeVocabTool(nil))

	rt := &Runtime{
		Config:   cfg,
		Harness:  harnessEngine,
		Memory:   memSystem,
		Plugins:  pluginMgr,
		Tools:    toolReg,
		Backend:  backend,
		Gateway:  gateway,
		Executor: exec,
		ACP:      acpSrv,
		Preproc:  preproc,
		sandbox:  sandbox,
		bus:      bus,
	}
	// 初始化轻量 trace（可观测性：agent 轮次/工具调用/耗时/错误 JSONL）
	traceEnabled := true
	if cfg.Trace.Enabled != nil {
		traceEnabled = *cfg.Trace.Enabled
	}
	if rec, err := trace.New(filepath.Join(projRoot, ".contextloom", "trace"), traceEnabled); err == nil {
		rt.Trace = rec
	}
	// OTLP 导出接线（trace.export_endpoint 配置时：批量+定时冲刷到 OTLP 接收端）
	rt.initTraceExporter()
	// 多 Agent 检查点（每轮 JSONL 快照，goal 续跑可恢复；保留最新 100 run）
	rt.initCheckpoints()
	// 初始化文件操作审计（企业治理审计追踪；与 trace 分离：审计=敏感操作，trace=运行事件）
	if aud, err := audit.New(filepath.Join(projRoot, ".contextloom", "audit"), traceEnabled); err == nil {
		tools.SetAuditor(aud)
		tools.SetAuditSessionProvider(func() string { return rt.GetSessionID() })
		rt.auditor = aud
	}
	// 记录 settings.json 加载前的激活模型（config.yaml 默认），用于检测持久化切换
	initialActiveModel := rt.Config.ActiveModelName
	// 按会话隔离的模式表必须在 loadSettings 之前初始化（loadSettings 恢复 modes 字段）
	rt.modeBySID = map[string]string{}
	rt.loadSettings()
	// 思考深度统一下发到后端：
	//   - 配置/持久化设置（settings.json）中的 effort 在此生效（此前仅在 /effort 命令时下发，启动时从不生效）；
	//   - 未配置时默认 "max"（最高推理强度）。
	if rt.Config.CLIDefaults.Effort == "" {
		rt.Config.CLIDefaults.Effort = "max"
	}
	rt.Backend.SetReasoningEffort(rt.Config.CLIDefaults.Effort)
	// 初始化上下文预算控制器（token 预算驱动，接线激活预设的 max_context）
	rt.ctxBudget = ctxmgr.NewTokenBudget(cfg.ActiveModel().MaxContext)
	rt.ctxBudget.ApplyConfig(
		cfg.Context.CompactRatio,
		cfg.Context.HardRatio,
		cfg.Context.SafetyRatio,
		cfg.Context.KeepRecent,
	)
	// 恢复 settings.json 持久化的激活模型（/model 与 GUI 模型预设切换均经 saveSettings 落盘）：
	// loadSettings 只恢复配置指针，此处对比默认模型——若不同则重建后端+预算，
	// 使启动时即用上次切换的模型（endpoint/api_key/max_context/send_effort 全部生效）。
	// 调用点在 ctxBudget 创建之后，applyLiveConfig 依赖的组件均已就绪。
	if rt.Config.ActiveModelName != initialActiveModel && rt.Config.ActiveModelName != "" {
		rt.applyLiveConfig(nil)
	}
	// 用真正的 rt 实现 TaskSpawner 替换占位
	toolReg.Register("spawn_task", tools.MakeSpawnTaskTool(rt))
	// 用真正的 rt 实现 VocabProposer 替换占位
	toolReg.Register("propose_vocab", tools.MakeProposeVocabTool(rt))
	// 本机 Agent 的 harness_search：检索被压缩/丢弃的对话原文与项目上下文（只读）。
	// 与 MCP 客户端暴露的 harness_search 同一数据源（rt.HarnessSearch）——此前该能力
	// 仅外部 MCP 客户端可用，本机 Agent 压缩后"找回"闭环断裂；注册后 Agent 在
	// 对话被压缩、需要旧内容时可直接调用，不消耗对话 token 预算。
	// 数据底座：indexCompressedEvicted 把压缩原文写入 Harness 索引，并跨进程持久化
	// （.contextloom/harness/index.json，见 startHarnessAutoSave/CloseHarness）。
	toolReg.RegisterExtra("harness_search",
		"在 ContextLoom 检索索引中搜索被压缩/丢弃的对话原文与项目上下文（只读，不消耗对话 token 预算）。对话历史被压缩后可用来找回旧内容。",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "description": "搜索关键词"},
				"limit": map[string]any{"type": "integer", "description": "返回条数上限（默认5）"},
			},
			"required": []string{"query"},
		},
		func(ctx context.Context, params map[string]any) (any, error) {
			query, _ := params["query"].(string)
			if strings.TrimSpace(query) == "" {
				return nil, fmt.Errorf("harness_search: query 不能为空")
			}
			limit := 5
			if l, ok := params["limit"].(float64); ok && int(l) > 0 {
				limit = int(l)
			}
			return rt.HarnessSearch(ctx, query, limit), nil
		},
	)
	// 接线检索索引持久化：自动保存（30s 节流） + 记录落盘路径（退出时 CloseHarness 兜底保存）。
	rt.harnessIndexPath = harnessIndexPath
	rt.startHarnessAutoSave()
	// handoff 交接原语（对标 OpenAI Agents SDK handoff）：
	// 串行控制权转移——主 Agent 把当前任务整体移交给目标 Agent（planner/coder/verifier），
	// 目标 Agent 输出作为交接结果返回；区别于 spawn_task 的并行派发。
	// 交接目标内部复用 rt.SpawnTask 创建子 Agent（自动批准 + 父上下文继承）。
	rt.handoffReg = executor.NewHandoffRegistry()
	payloadText := func(p map[string]any) string {
		if len(p) == 0 {
			return ""
		}
		if b, err := json.Marshal(p); err == nil {
			return " " + string(b)
		}
		return ""
	}
	rt.handoffReg.Register(executor.HandoffTarget{
		Name: "planner",
		Desc: "规划专家：把任务拆解为分步计划（计划文档/步骤清单/验收标准）",
		Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
			return rt.SpawnTask(ctx, tools.AgentIDFromContext(ctx), tools.TaskSpec{
				Directive: "PLANNER",
				Task:      "规划任务：" + reason + payloadText(payload),
			})
		},
	})
	rt.handoffReg.Register(executor.HandoffTarget{
		Name: "coder",
		Desc: "实现专家：按计划/描述完成代码实现（写文件、改代码）",
		Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
			return rt.SpawnTask(ctx, tools.AgentIDFromContext(ctx), tools.TaskSpec{
				Directive: "CODE_AGENT",
				Task:      "实现任务：" + reason + payloadText(payload),
			})
		},
	})
	rt.handoffReg.Register(executor.HandoffTarget{
		Name: "verifier",
		Desc: "验证专家：运行测试与检查，核实实现是否符合要求",
		Handler: func(ctx context.Context, reason string, payload map[string]any) (string, error) {
			return rt.SpawnTask(ctx, tools.AgentIDFromContext(ctx), tools.TaskSpec{
				Directive: "TEST",
				Task:      "验证任务：" + reason + payloadText(payload),
			})
		},
	})
	toolReg.RegisterExtra("handoff",
		"把当前任务移交给目标 Agent 执行（串行控制权转移，结果返回；区别于 spawn_task 的并行派发）",
		executor.HandoffToolSchema, executor.MakeHandoffTool(rt.handoffReg))
	// run_toolchain 工具（§4.3.2 技能联动）：按名执行 .contextloom/chains/<name>/toolchain.yaml，
	// 返回产物文本。SKILL.md frontmatter 的 toolchain: 字段（技能强制执行链）依赖本工具
	// 供模型调用；也可由模型自主组合多条链。
	toolReg.RegisterExtra("run_toolchain",
		"按名执行一条已安装的工具链（.contextloom/chains/<name>/toolchain.yaml），返回各步骤产物；可用 chains 参数一次传入多条链名顺序执行",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"name":   map[string]any{"type": "string", "description": "工具链名（如 code-review-chain）"},
				"params": map[string]any{"type": "object", "description": "链级参数（如 path），按链定义注入"},
			},
			"required": []string{"name"},
		}, func(ctx context.Context, params map[string]any) (any, error) {
			name, _ := params["name"].(string)
			if strings.TrimSpace(name) == "" {
				return nil, fmt.Errorf("run_toolchain 缺少工具链名 name")
			}
			pm := map[string]string{}
			if raw, ok := params["params"].(map[string]any); ok {
				for k, v := range raw {
					if s, ok := v.(string); ok {
						pm[k] = s
					} else {
						b, err := json.Marshal(v)
						if err == nil {
							pm[k] = string(b)
						}
					}
				}
			}
			res, err := rt.RunToolchain(ctx, name, pm)
			if err != nil {
				return nil, err
			}
			return map[string]any{
				"chain":       name,
				"failed_step": res.FailedStepID,
				"skipped":     res.Skipped,
				"output":      chainOutput(res),
			}, nil
		})
	// 初始化目标循环管理器（goal 模式状态机，持久化到 .contextloom/goals/）
	rt.goalMgr = goalmgr.New(filepath.Join(projRoot, ".contextloom", "goals"))
	// 证据链核验接线（对照 why-self-verification 一文：验收走外部物理通道）。
	// done 步骤携带 proofs 时做确定性核验（file/diff/test），失败 → 降级 + 缺陷。
	rt.goalMgr.SetEvidenceVerifier(func(steps []types.GoalStep) []goalmgr.ProofFailure {
		return goalmgr.VerifyGoalProofs(steps, goalmgr.DefaultProofIO(projRoot))
	})
	// 打磨轮外部验收接线：代码类目标 go test ./... 通过 + 静态核查无 high/medium
	// 残留，才受理 [GOAL_POLISH_DONE]（模型自答"打磨完成"不算数）。
	rt.goalMgr.SetPolishVerifier(func(s *types.GoalSession) (bool, []string) {
		return rt.verifyGoalPolish(s)
	})
	// 子任务活动式超时：持续输出不设限；连续 10 分钟无输出才终止（有输出重置计时）
	rt.childNoOutputTimeout = 10 * time.Minute
	// 初始化技能加载器（扫描项目 .contextloom/skills/）
	skillDir := filepath.Join(projRoot, ".contextloom", "skills")
	rt.skillLoader = NewSkillLoader(skillDir)
	_ = rt.skillLoader.Load()
	// 内置种子数据落地（员工/场景/工具链/技能，幂等开箱即用；失败仅 stderr 告警，绝不阻断启动）
	seedBuiltins(projRoot)
	_ = rt.skillLoader.Load() // 重扫技能目录，纳入刚落地的内置技能
	// 从 settings 恢复上次选中的技能
	rt.loadActiveSkills()
	// 技能市场（内置 catalog 开箱即用；远程 URL 可经 Market.AddRemote 扩展）
	rt.Market = market.New()
	// 工具沙箱（config sandbox.enabled）：所有文件写操作重定向到影子文件系统，
	// 读操作镜像优先、列表合并；exec 进入镜像 + 资源限制。退出按 discard_on_exit 处理。
	if cfg.Sandbox.Enabled {
		if fs, err := sbox.New(filepath.Join(projRoot, ".contextloom", "sandbox")); err == nil {
			rt.sbFS = fs
			log.Printf("工具沙箱已启用: %s（写操作进入镜像；退出: %s）",
				fs.Root(), map[bool]string{true: "丢弃改动", false: "保留镜像"}[cfg.Sandbox.DiscardOnExit])
		} else {
			log.Printf("工具沙箱初始化失败: %v", err)
		}
	}
	// 启动外部 MCP server 并注册其工具（mcp.<server>.<tool>）
	rt.startMCPServers()
	// v2 插件体系：插件 tools 真实注册（manifest 声明 + script 脚本实现）。
	// 与 MCP 同机制（RegisterExtra 完整 schema 透传），工具名直接用插件声明的
	// name（同名覆盖语义与插件加载一致：项目级覆盖用户级）。调用时参数 JSON
	// 经 stdin 传入脚本，stdout 为结果；失败返回错误字符串给模型（不 panic）。
	for _, reg := range pluginMgr.ToolRegistrations() {
		reg := reg
		toolName := "plugin_" + sanitizeToolName(reg.Name)
		rt.Tools.RegisterExtra(toolName, reg.Description, reg.Schema, func(ctx context.Context, params map[string]any) (any, error) {
			out, err := rt.runPluginTool(reg, params)
			if err != nil {
				return nil, fmt.Errorf("插件工具 %s 执行失败: %w", reg.Name, err)
			}
			return string(out), nil
		})
		fmt.Fprintf(os.Stderr, "插件工具已注册: %s（来自插件 %s）\n", toolName, reg.PluginName)
	}
	// 外部消息渠道（对标业界同类 Agent 的消息渠道）：
	//   webhook=HTTP 接收外部消息触发 goal/task/chat（随机端口 + token 鉴权）
	//   cron=按 5 字段 cron 表达式定时触发（调度精度 30s 内）
	if cfg.Channels.Webhook.Enabled {
		wb := webhook.New(cfg.Channels.Webhook.Token, func(ctx context.Context, m webhook.Message) (string, error) {
			return rt.RunChannelMessage(ctx, m)
		})
		if addr, err := wb.Start(); err == nil {
			rt.webhookSrv = wb
			log.Printf("webhook 渠道已启动: %s/hook/%s (POST {message, mode: goal/task/chat})", addr, wb.Token())
		} else {
			log.Printf("webhook 渠道启动失败: %v", err)
		}
	}
	if cfg.Channels.Cron.Enabled {
		sched := scheduler.New(0)
		sched.SetErrorHandler(func(name string, err error) {
			log.Printf("[cron] 任务 %s 执行失败: %v", name, err)
		})
		for _, j := range cfg.Channels.Cron.Jobs {
			job := j
			if job.Mode == "" {
				job.Mode = "goal"
			}
			if err := sched.AddJob(job.Name, job.Schedule, func(ctx context.Context) error {
				_, err := rt.RunChannelMessage(ctx, webhook.Message{Text: job.Prompt, Mode: job.Mode, Meta: "cron:" + job.Name})
				return err
			}); err != nil {
				log.Printf("[cron] 任务 %s 注册失败: %v", job.Name, err)
				continue
			}
			log.Printf("[cron] 任务 %s 已注册: %q (mode=%s)", job.Name, job.Schedule, job.Mode)
		}
		// 场景触发器接线（M4.4.3 triggers.cron）：.contextloom/scenes/<name>/scene.yaml
		// 声明 triggers.cron 的场景自动注册为 scene:<name> 定时任务（幂等，重名忽略；
		// 无场景目录/无 cron 声明 → 零开销，见 scene_cron.go）。
		if added := rt.registerSceneCronJobs(sched, filepath.Join(projRoot, ".contextloom", scenesDir)); added > 0 {
			log.Printf("[cron] 已注册 %d 个场景触发器", added)
		}
		if len(sched.Jobs()) > 0 {
			go sched.Start(context.Background()) // Start 为阻塞循环：后台运行，退出由 CloseChannels→Stop 停止
			rt.cronSched = sched
		}
	}
	return rt, nil
}

// RunChannelMessage 执行渠道消息（webhook/cron 共用）：
//   - goal：无活动目标→新建目标会话并执行；有活动目标→推进目标循环
//   - task/chat：按模式执行输入
//
// 返回完整输出文本（供 webhook 响应 / cron 日志）。
func (rt *Runtime) RunChannelMessage(ctx context.Context, msg webhook.Message) (string, error) {
	if msg.Mode == "" {
		msg.Mode = "goal"
	}
	rt.SetMode(msg.Mode)
	var ch <-chan string
	var errCh <-chan error
	var err error
	switch msg.Mode {
	case "goal":
		gs := rt.goalMgr.Session()
		if gs == nil || gs.Done() {
			ch, errCh, err = rt.ProcessInput(ctx, msg.Text)
		} else {
			ch, errCh, err = rt.ProcessGoalRound(ctx)
		}
	default: // task / chat
		ch, errCh, err = rt.ProcessInput(ctx, msg.Text)
	}
	if err != nil {
		if rt.Trace != nil {
			rt.Trace.Record(trace.Entry{Event: "channel", Mode: msg.Mode, Err: err.Error(), Detail: msg.Meta})
		}
		return "", err
	}
	var b strings.Builder
	for tok := range ch {
		b.WriteString(tok)
	}
	if e := <-errCh; e != nil {
		if rt.Trace != nil {
			rt.Trace.Record(trace.Entry{Event: "channel", Mode: msg.Mode, Err: e.Error(), Detail: msg.Meta})
		}
		return "", e
	}
	if rt.Trace != nil {
		rt.Trace.Record(trace.Entry{Event: "channel", Mode: msg.Mode, Tokens: int(harness.EstimateTokens(b.String())), Detail: msg.Meta})
	}
	return b.String(), nil
}

// CloseChannels 关闭外部消息渠道（webhook HTTP 服务 + cron 调度循环）。
func (rt *Runtime) CloseChannels() {
	if rt.cronSched != nil {
		rt.cronSched.Stop()
		rt.cronSched = nil
	}
	if rt.webhookSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = rt.webhookSrv.Stop(ctx)
		rt.webhookSrv = nil
	}
}

// SandboxStatus 返回工具沙箱当前状态文本。
func (rt *Runtime) SandboxStatus() string {
	if rt.sbFS == nil {
		return "工具沙箱未启用（config: sandbox.enabled）"
	}
	return fmt.Sprintf("工具沙箱已启用: %s（写操作进入镜像；退出: %s；/sandbox commit 合并写回）",
		rt.sbFS.Root(), map[bool]string{true: "丢弃改动", false: "保留镜像"}[rt.Config.Sandbox.DiscardOnExit])
}

// SandboxCommit 把镜像改动合并写回真实文件系统，返回合并文件数。
func (rt *Runtime) SandboxCommit() (int, error) {
	if rt.sbFS == nil {
		return 0, fmt.Errorf("工具沙箱未启用")
	}
	return rt.sbFS.Merge()
}

// SandboxDiscard 丢弃镜像全部改动（清空镜像目录）。
func (rt *Runtime) SandboxDiscard() error {
	if rt.sbFS == nil {
		return fmt.Errorf("工具沙箱未启用")
	}
	if err := os.RemoveAll(rt.sbFS.Root()); err != nil {
		return err
	}
	rt.sbFS = nil
	return nil
}

// CloseSandbox 退出时按 discard_on_exit 处理沙箱镜像（丢弃或保留）。
func (rt *Runtime) CloseSandbox() {
	if rt.sbFS == nil {
		return
	}
	if rt.Config.Sandbox.DiscardOnExit {
		_ = os.RemoveAll(rt.sbFS.Root())
	}
	rt.sbFS = nil
}

func (rt *Runtime) NewSession(input string) *types.Session {
	title := input
	if len([]rune(title)) > 50 {
		title = string([]rune(title)[:50])
	}
	if title == "" {
		title = "新会话"
	}
	session := memory.NewSession(title, rt.Config.Project.Root)
	_ = rt.Memory.Save(session)
	// 会话 ID 与历史同锁保护（GUI 并发场景：status.bar 轮询、流式 goroutine
	// 与 chat.send 可能并发读写 currentSessionID，需防 data race）
	rt.historyMu.Lock()
	rt.currentSessionID = session.UUID
	// 新会话：清空历史与滚动摘要
	rt.conversationHistory = nil
	rt.historyMu.Unlock()
	// 目标状态机按会话隔离：切换到新会话（各自独立的活动目标，互不串扰）
	rt.goalMgr.SetCurrent(session.UUID)
	rt.ResetContextSummary()
	return session
}

// UpdateSessionTitle 更新会话标题和预览（首次用户输入时自动调用）。
// 标题截断前 50 字，预览取首条用户消息前 80 字。
// 受 memory.auto_title 开关控制：关闭时不改写标题（保留"新会话"），预览仍更新。
func (rt *Runtime) UpdateSessionTitle(uuid, userInput string) {
	if session, err := rt.Memory.Load(uuid); err == nil {
		if rt.Config.Memory.AutoTitle {
			if len([]rune(userInput)) > 50 {
				session.Title = string([]rune(userInput)[:50])
			} else {
				session.Title = userInput
			}
		}
		if len([]rune(userInput)) > 80 {
			session.Preview = string([]rune(userInput)[:80])
		} else {
			session.Preview = userInput
		}
		session.UpdatedAt = time.Now()
		_ = rt.Memory.Save(session)
	}
}

// GetSessionID 获取当前会话ID（historyMu 保护，GUI 并发读取安全）
func (rt *Runtime) GetSessionID() string {
	rt.historyMu.RLock()
	defer rt.historyMu.RUnlock()
	return rt.currentSessionID
}

// agentRouteReq 构造网关路由请求：任务携带图片时 Task="vision"（§4.2.2
// 视觉路由规则可命中多模态池）；goal/task/chat 模式作为默认 Task 谓词。
func (rt *Runtime) agentRouteReq(task types.TaskCreate, mode string) modelgateway.Req {
	req := modelgateway.Req{Task: mode}
	if len(task.Images) > 0 {
		req.Task = "vision"
	}
	return req
}

// selectBackend 经模型网关选择 per-agent 后端句柄（方案 §4.2.3）。
// 网关闭用（nil）或 Select 失败时回退 rt.Backend，保证零破坏。
// 视觉路由（§4.2.3）：req.Task=vision 且配置了 vision_model 预设时按规则
// 命中多模态池；否则沿用既有 SetVisionModel 补丁逻辑（agent 层仍兼容）。
func (rt *Runtime) selectBackend(ctx context.Context, req modelgateway.Req) model.Backend {
	if rt.Gateway == nil {
		return rt.Backend
	}
	b, err := rt.Gateway.Select(ctx, req)
	if err != nil {
		// 网关全灭（含 default/falback）：回退既有后端，不让自动化层阻断人工兜底
		return rt.Backend
	}
	return b
}

func (rt *Runtime) CreateTask(directive, task string, payload any, priority int) (*executor.Agent, error) {
	id := fmt.Sprintf("agent-%d", len(rt.Executor.Agents())+1)
	tc := types.TaskCreate{
		Type:          "task.create",
		ID:            id,
		Directive:     directive,
		Task:          task,
		Payload:       payload,
		ContextPolicy: "auto_score",
		Priority:      priority,
	}
	agent := rt.Executor.SpawnAgent(id, tc, rt.selectBackend(context.Background(), modelgateway.Req{Task: "task"}))
	agent.SetSessionID(rt.GetSessionID())
	agent.SetTracer(rt)
	return agent, nil
}

func (rt *Runtime) RunAgent(ctx context.Context, agent *executor.Agent) error {
	err := agent.Run(ctx)
	if err != nil && rt.Trace != nil {
		// 可观测性：错误事件（多轮失败归因）
		rt.Trace.Record(trace.Entry{Event: "error", Mode: rt.GetMode(), Err: err.Error()})
	}
	return err
}

// SwitchModel 按名称切换多模型预设（models 列表中的一项）：
// 找到预设 → 将其 backend/endpoint/api_key/max_context/top_p/temperature/
// supports_vision/vision_model/send_effort 应用到激活配置 → 热重建后端与
// 上下文预算（applyLiveConfig）→ 持久化设置。未找到返回错误并附可用列表。
func (rt *Runtime) SwitchModel(name string) error {
	if name == "" {
		return fmt.Errorf("模型名不能为空")
	}
	preset := rt.findModelPreset(name)
	if preset == nil {
		avail := rt.ModelNames()
		if len(avail) == 0 {
			return fmt.Errorf("未找到模型 %q（配置中无任何模型预设）", name)
		}
		return fmt.Errorf("未找到模型 %q，可用: %s", name, strings.Join(avail, " / "))
	}
	// 应用预设：顶层 active_model 仅存激活名指针，
	// 其余字段由 ActiveModel() 从 models 列表读取（唯一事实来源，避免双份漂移）。
	rt.Config.ActiveModelName = preset.Name
	// 热重建后端 + 上下文预算（与 config.set 保存即生效同链路）
	rt.applyLiveConfig(nil)
	rt.saveSettings()
	return nil
}

// AddModelPreset 新建一个模型预设并写入 models 列表（傻瓜式新建能力）。
// 只接收傻瓜表单的核心字段（name/backend/endpoint/api_key/max_context），
// 其余参数（top_p/temperature/supports_vision/send_effort 等）用默认值，
// 用户可在新建后于"模型"分组表单里进一步微调。
// 默认同步激活该预设（GUI 新建后立即生效），持久化配置。
func (rt *Runtime) AddModelPreset(p config.ModelPreset) error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return fmt.Errorf("模型名称不能为空")
	}
	if rt.findModelPreset(name) != nil {
		return fmt.Errorf("模型预设 %q 已存在，请换一个名称", name)
	}
	if p.Backend == "" {
		p.Backend = "openai-compatible"
	}
	if p.MaxContext == 0 {
		p.MaxContext = 131072
	}
	se := true
	if p.SendEffort == nil {
		p.SendEffort = &se
	}
	if p.TopP == nil {
		p.TopP = rt.f64ptr(0.95)
	}
	if p.Temperature == nil {
		p.Temperature = rt.f64ptr(1.0)
	}
	rt.Config.Models = append(rt.Config.Models, p)
	rt.Config.ActiveModelName = p.Name
	rt.applyLiveConfig(nil)
	rt.saveSettings()
	return nil
}

func (rt *Runtime) f64ptr(v float64) *float64 { return &v }

// RemoveModelPreset 删除一个模型预设（GUI 设置面板"删除模型配置"能力）。
// 语义：
//   - 空名/未找到 → 报错；
//   - 仅剩一个预设时拒绝删除（否则无可用模型，运行时无法重建后端）；
//   - 删除的是当前激活预设 → 自动切换到剩余第一个并热重建后端；
//   - 删除后写回 config.yaml（含 .bak 备份）并持久化 settings。
func (rt *Runtime) RemoveModelPreset(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("模型名称不能为空")
	}
	idx := -1
	for i := range rt.Config.Models {
		if rt.Config.Models[i].Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		lower := strings.ToLower(name)
		for i := range rt.Config.Models {
			if strings.ToLower(strings.TrimSpace(rt.Config.Models[i].Name)) == lower {
				idx = i
				break
			}
		}
	}
	if idx == -1 {
		avail := rt.ModelNames()
		return fmt.Errorf("未找到模型 %q，可用: %s", name, strings.Join(avail, " / "))
	}
	if len(rt.Config.Models) <= 1 {
		return fmt.Errorf("至少保留一个模型预设，无法删除最后一个 %q", name)
	}
	deletedActive := strings.EqualFold(strings.TrimSpace(rt.Config.ActiveModelName), name)
	rt.Config.Models = append(rt.Config.Models[:idx], rt.Config.Models[idx+1:]...)
	if deletedActive {
		// 删除激活预设：切到剩余第一个（Config.ActiveModel 亦有回退，显式切换避免漂移）
		rt.Config.ActiveModelName = rt.Config.Models[0].Name
		rt.applyLiveConfig(nil)
	}
	// 写回 config.yaml（ApplyConfigPatch 之外的直改路径需自行持久化；
	// 复用 Config.Save 的 .bak 备份语义）
	path := rt.Config.ConfigPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	if err := rt.Config.Save(path); err != nil {
		return err
	}
	rt.saveSettings()
	return nil
}

// findModelPreset 按名称查找模型预设（大小写不敏感；优先精确匹配，
// 其次大小写不敏感匹配）。
func (rt *Runtime) findModelPreset(name string) *config.ModelPreset {
	for i := range rt.Config.Models {
		if rt.Config.Models[i].Name == name {
			return &rt.Config.Models[i]
		}
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	for i := range rt.Config.Models {
		if strings.ToLower(strings.TrimSpace(rt.Config.Models[i].Name)) == lower {
			return &rt.Config.Models[i]
		}
	}
	return nil
}

// ModelNames 返回全部模型预设名称（/model 与 GUI 下拉展示）。
// models 为空时返回当前 model 节后端名（至少一个可切换项，保证 GUI 下拉有内容）。
func (rt *Runtime) ModelNames() []string {
	names := make([]string, 0, len(rt.Config.Models))
	for _, m := range rt.Config.Models {
		if m.Name != "" {
			names = append(names, m.Name)
		}
	}
	if len(names) == 0 {
		n := strings.TrimSpace(rt.Config.ActiveModelName)
		if n == "" {
			n = "default-model"
		}
		names = append(names, n)
	}
	return names
}

// CurrentModel 返回当前激活的模型预设名（顶层 active_model 指针）。
func (rt *Runtime) CurrentModel() string {
	return rt.Config.ActiveModelName
}

func (rt *Runtime) SetEffort(effort string) {
	rt.Config.CLIDefaults.Effort = effort
	rt.Backend.SetReasoningEffort(effort)
	rt.saveSettings()
}

// UpdateActivePreset 修改当前激活预设的字段（写透到 models 列表对应项）。
// model 节只保留激活名指针，字段级修改必须落在预设上，否则切换后丢失。
// 注意：applyDefaults 保证 models 至少含一个默认预设，故无 models 分支
// 仅为防御（此时字段修改写不透，属旧配置兼容路径，调用方先 Load 即可）。
func (rt *Runtime) UpdateActivePreset(mutate func(*config.ModelPreset)) {
	name := strings.TrimSpace(rt.Config.ActiveModelName)
	for i := range rt.Config.Models {
		if strings.EqualFold(strings.TrimSpace(rt.Config.Models[i].Name), name) {
			mutate(&rt.Config.Models[i])
			return
		}
	}
	if len(rt.Config.Models) > 0 {
		mutate(&rt.Config.Models[0])
	}
}

// SetMode 设置【当前会话】的工作模式 (chat/task/goal) 并持久化。
// 按会话隔离（问题 2 修复）：GUI 多会话下在会话 A 切 goal，会话 B 的模式
// 不受影响（B 未设置时回退全局默认）；切回 A 自动恢复 A 的模式。
func (rt *Runtime) SetMode(mode string) {
	rt.historyMu.Lock()
	sid := rt.currentSessionID
	if sid != "" {
		if rt.modeBySID == nil {
			rt.modeBySID = map[string]string{}
		}
		rt.modeBySID[sid] = mode
	} else {
		// 无会话上下文（TUI 未加载会话/开发场景）：写全局默认
		rt.Config.CLIDefaults.Mode = mode
	}
	rt.historyMu.Unlock()
	rt.saveSettings() // 内部 RLock 快照 modeBySID（此处已解锁，无死锁）
	// 离开 goal 模式：停用当前目标循环（会话保留在 .contextloom/goals/ 可 /goal resume）
	if mode != "goal" {
		rt.goalMgr.Reset()
	}
}

// GetMode 获取【当前会话】的工作模式：优先会话级设置，未设置回退全局默认。
func (rt *Runtime) GetMode() string {
	rt.historyMu.RLock()
	sid := rt.currentSessionID
	var m string
	if sid != "" && rt.modeBySID != nil {
		m = rt.modeBySID[sid]
	}
	rt.historyMu.RUnlock()
	if m == "" {
		m = rt.Config.CLIDefaults.Mode
	}
	if m != "chat" && m != "task" && m != "goal" {
		return "chat"
	}
	return m
}

// PersistencePaths 构建持久化文件路径 map，注入系统提示词告知模型各文件位置。
// 参考 README.md 的存储结构说明，模型可据此自主定位配置/记忆/词汇表等。
func (rt *Runtime) PersistencePaths() map[string]string {
	projRoot := rt.Config.Project.Root
	paths := map[string]string{
		"settings":      filepath.Join(projRoot, ".contextloom", "settings.json"),
		"memory_dir":    resolvePath(rt.Config.Memory.Directory, projRoot),
		"vocab":         rt.Config.Project.Vocab,
		"plugins_dir":   resolvePath(rt.Config.Plugins.Directory, projRoot),
		"skills_dir":    filepath.Join(projRoot, ".contextloom", "skills"),
		"config_file":   rt.Config.ConfigPath,
		"harness_index": filepath.Join(projRoot, ".contextloom", "harness", "index.json"),
	}
	paths["acp_port"] = fmt.Sprintf("%d", rt.Config.ACP.Port)
	return paths
}

// ProposeVocab 实现 tools.VocabProposer（模型调用 propose_vocab 工具时触发）
func (rt *Runtime) ProposeVocab(term, mapping, reason string) error {
	rt.Preproc.Vocab().Propose(term, mapping, reason)
	return nil
}

// ListPendingVocab 返回待确认的词汇提案（供 TUI /vocab 命令调用）
func (rt *Runtime) ListPendingVocab() []preprocessor.VocabProposal {
	return rt.Preproc.Vocab().ListPending()
}

// AcceptVocab 确认词汇提案并写入词表
func (rt *Runtime) AcceptVocab(term string) bool {
	return rt.Preproc.Vocab().Accept(term)
}

// RejectVocab 拒绝词汇提案
func (rt *Runtime) RejectVocab(term string) bool {
	return rt.Preproc.Vocab().Reject(term)
}

func (rt *Runtime) SetThreshold(t float64) {
	rt.Harness.SetThreshold(types.Score(t))
	rt.Config.Harness.ScoreThreshold = t
}

func (rt *Runtime) GetMemoryStats() string {
	stats := rt.Memory.Stats()
	return fmt.Sprintf("会话数: %v", stats["session_count"])
}

// DreamMemory 主动整理长期记忆（对标业界同类 Agent 的 /dream 记忆整理术
// "记忆主动整理（/dream 式）与学习档案" 落地项）。
//
// 做法：扫描全部历史会话的 JSONL 事件日志，从 assistant/user 消息文本中
// 抽取含"教训/经验/踩坑/注意/约定"等关键词的句段，去重合并写入
// .contextloom/memory/lessons.md（可手工编辑，手写内容永不覆盖）。
// 返回统计信息：扫描会话数、抽取候选条数、本次新增条数。
func (rt *Runtime) DreamMemory() string {
	if rt.Memory == nil {
		return "记忆系统未初始化"
	}
	dir := rt.MemoryDir()
	lessons, err := memory.NewLessonsStore(dir)
	if err != nil {
		return "创建长期记忆失败: " + err.Error()
	}

	sessions := rt.Memory.List()
	scanned, candidates := 0, 0
	var fresh []memory.Lesson
	for _, s := range sessions {
		events, eerr := rt.Memory.LoadEvents(s.UUID)
		if eerr != nil {
			continue
		}
		scanned++
		short := s.UUID
		if len(short) > 8 {
			short = short[:8]
		}
		for _, ev := range events {
			if ev.Type != "user_msg" && ev.Type != "assistant_msg" {
				continue
			}
			text, ok := ev.Data.(string)
			if !ok {
				continue
			}
			for _, l := range memory.ExtractLessons(text) {
				candidates++
				fresh = append(fresh, memory.Lesson{
					Text:      l,
					Topic:     memory.ClassifyTopicPublic(l),
					Source:    short + " " + ev.Timestamp.Format("2006-01-02"),
					UpdatedAt: ev.Timestamp,
				})
			}
		}
	}
	added, werr := lessons.MergeAndSave(fresh)
	if werr != nil {
		return "写入长期记忆失败: " + werr.Error()
	}
	return fmt.Sprintf("记忆整理完成：扫描 %d 个会话 / 抽取候选 %d 条 / 新增沉淀 %d 条\n长期记忆文件: %s",
		scanned, candidates, added, lessons.Path())
}

// TeachMemory 手动沉淀一条长期记忆（对标 /teach-me 学习档案）。
// 直接把用户输入的教训写入 lessons.md（规范化 + 主题归类），不做关键词过滤。
func (rt *Runtime) TeachMemory(text string) string {
	if strings.TrimSpace(text) == "" {
		return "用法: /teach <内容>"
	}
	if rt.Memory == nil {
		return "记忆系统未初始化"
	}
	lessons, err := memory.NewLessonsStore(rt.MemoryDir())
	if err != nil {
		return "创建长期记忆失败: " + err.Error()
	}
	fresh := []memory.Lesson{{
		Text:  strings.TrimSpace(text),
		Topic: memory.ClassifyTopicPublic(text),
	}}
	added, werr := lessons.MergeAndSave(fresh)
	if werr != nil {
		return "写入长期记忆失败: " + werr.Error()
	}
	return fmt.Sprintf("已沉淀 %d 条长期记忆：%s", added, strings.TrimSpace(text))
}

// MemoryDir 返回记忆根目录（与 memory.System 同一 resolved 目录，保证 lessons.md 与
// sessions/ 并列）。相对路径按项目根解析，绝对路径原样返回。
func (rt *Runtime) MemoryDir() string {
	if rt.Config == nil || rt.Config.Project.Root == "" {
		return "./.contextloom/memory"
	}
	return resolvePath(rt.Config.Memory.Directory, rt.Config.Project.Root)
}

// ConfigSummary 返回生效配置摘要（供 /config 命令展示）。
// 来源: 配置文件 + 默认值（applyDefaults 已合并），解决"配置来源分散无可视化"问题。
func (rt *Runtime) ConfigSummary() string {
	c := rt.Config
	if c == nil {
		return "配置: 未加载"
	}
	am := c.ActiveModel()
	lines := []string{
		"=== 生效配置 ===",
		fmt.Sprintf("配置文件: %s", c.ConfigPath),
		fmt.Sprintf("模型: %s (max_context=%d)", am.Name, am.MaxContext),
		fmt.Sprintf("项目根: %s (启动目录)", c.Project.Root),
		fmt.Sprintf("记忆目录: %s | 插件目录: %s", c.Memory.Directory, c.Plugins.Directory),
		fmt.Sprintf("审批模式: %s", c.Tools.ApprovalMode),
		fmt.Sprintf("上下文阈值: compact %.0f%% / hard %.0f%% / safety %.0f%% (keep_recent=%d)",
			c.Context.CompactRatio*100, c.Context.HardRatio*100, c.Context.SafetyRatio*100, c.Context.KeepRecent),
		fmt.Sprintf("Overseer: 通过线 %.0f | 自动计划节点: %v | 测试TEE: %v | 临时文档: %s",
			c.Overseer.PassThreshold, c.Overseer.NodeAutoPlan, c.Overseer.TestTEE, c.Overseer.TempDocPolicy),
		fmt.Sprintf("思考深度: %s | 模式: %s | 阈值: %.0f", c.CLIDefaults.Effort, c.CLIDefaults.Mode, c.CLIDefaults.Threshold),
	}
	return strings.Join(lines, "\n")
}

// ConfigSnapshot 返回脱敏配置视图（GUI config.get：schema + values + path）。
func (rt *Runtime) ConfigSnapshot() map[string]any {
	c := rt.Config
	if c == nil {
		return map[string]any{"values": map[string]any{}, "schema": []config.FieldSchema{}, "path": ""}
	}
	snap := map[string]any{
		"values": c.Sanitized(),
		"schema": config.ConfigSchema(),
		"path":   c.ConfigPath,
	}
	// 升级方案目录约定（§4.7 GUI 全配置化支撑）：暴露四个可选目录，
	// GUI 员工向导/工具链画布/场景向导/门禁面板据此读写 YAML
	// （目录不存在 = 对应能力未启用，GUI 可引导一键创建）。
	root := c.Project.Root
	if root == "" {
		root = "."
	}
	snap["employees_dir"] = filepath.Join(root, ".contextloom", employeesDir)
	snap["scenes_dir"] = filepath.Join(root, ".contextloom", scenesDir)
	snap["chains_dir"] = filepath.Join(root, ".contextloom", chainsDir)
	snap["gates_dir"] = filepath.Join(root, ".contextloom", gatesDir)
	return snap
}

// ApplyConfigPatch 应用配置补丁并持久化（GUI config.set 链路）。
// 语义：
//   - 扁平 key 补丁（如 {"model.max_context": 262144}），未知 key 报错；
//   - api_key 传脱敏值自动跳过；
//   - 写回 ConfigPath（无路径时回退默认查找路径），原文件自动备份 .bak；
//   - 保存前 Validate（api_key 未配置时允许保存——可能正要用环境变量，不阻塞 GUI 保存）；
//   - 保存成功后热同步运行中的组件（上下文预算/模型后端/检索参数），
//     返回 needsRestart：本次补丁中无法热更新、需重启进程生效的字段清单
//     （MCP 客户端/插件/ACP/webhook/cron/沙箱/项目根等启动期创建的重型组件），
//     GUI 据此提示用户"部分设置重启后生效"。
func (rt *Runtime) ApplyConfigPatch(patch map[string]any) ([]string, error) {
	c := rt.Config
	if c == nil {
		return nil, fmt.Errorf("配置未加载")
	}
	if err := c.ApplyPatch(patch); err != nil {
		return nil, err
	}
	// 阈值单调性自动修正（compact < hard < safety），与 Load 行为一致
	if c.Context.CompactRatio >= c.Context.HardRatio {
		c.Context.HardRatio = c.Context.CompactRatio + 0.01
	}
	if c.Context.HardRatio >= c.Context.SafetyRatio {
		c.Context.SafetyRatio = c.Context.HardRatio + 0.01
	}
	if c.Context.SafetyRatio > 1 {
		c.Context.SafetyRatio = 1
	}
	if err := c.Validate(); err != nil && !strings.Contains(err.Error(), "api_key") {
		return nil, err
	}
	path := c.ConfigPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	if err := c.Save(path); err != nil {
		return nil, err
	}
	return rt.applyLiveConfig(patch), nil
}

// applyLiveConfig 把已保存的配置热同步到运行中的组件，返回本次补丁中
// 需重启进程生效的字段清单（无热重建链路的启动期组件）。
// 热更新范围（进程内无缓存、改配置立即生效的组件）：
//   - 上下文预算：model.max_context + context.* → 重建 TokenBudget（historyMu
//     保护，与 ProcessInput/压缩/统计的读取互斥；滚动摘要跨重建保留）；
//   - 模型后端：effort / top_p / temperature / 模型 ID / 多模态标志 / endpoint /
//     api_key / max_context → DeepSeekBackend 热更新 setter（modelMu 保护）；
//   - 检索参数：harness.* → Engine.ApplyConfig（mu 保护）。
//
// 重型组件（MCP 客户端、插件、ACP、webhook、cron、沙箱、项目根/记忆目录等）
// 在启动时创建且无热重建链路，列入 needsRestart 提示"重启生效"。
func (rt *Runtime) applyLiveConfig(patch map[string]any) []string {
	// 1) 上下文预算热重建（读取点均在 historyMu 保护下；保留滚动摘要防失忆）
	am := rt.Config.ActiveModel()
	rt.historyMu.Lock()
	old := rt.ctxBudget
	rt.ctxBudget = ctxmgr.NewTokenBudget(am.MaxContext)
	rt.ctxBudget.ApplyConfig(
		rt.Config.Context.CompactRatio,
		rt.Config.Context.HardRatio,
		rt.Config.Context.SafetyRatio,
		rt.Config.Context.KeepRecent,
	)
	if old != nil {
		rt.ctxBudget.Summary = old.Summary
	}
	rt.historyMu.Unlock()

	// 2) 模型后端热更新（字段一律取自激活预设 ActiveModel，单一事实来源）
	if rt.Backend != nil {
		rt.Backend.SetReasoningEffort(rt.Config.CLIDefaults.Effort)
		if am.TopP != nil {
			rt.Backend.SetTopP(*am.TopP)
		}
		if am.Temperature != nil {
			rt.Backend.SetTemperature(*am.Temperature)
		}
		if ds, ok := rt.Backend.(*model.DeepSeekBackend); ok {
			ds.SwitchModel(am.Backend)
			ds.SetSupportsVision(am.SupportsVision)
			ds.SetEndpoint(am.Endpoint)
			ds.SetAPIKey(am.APIKey)
			ds.SetMaxContext(am.MaxContext)
			se := true
			if am.SendEffort != nil {
				se = *am.SendEffort
			}
			ds.SetSendEffort(se)
		}
	}

	// 3) 检索参数热更新
	if rt.Harness != nil {
		rt.Harness.ApplyConfig(harness.Config{
			ScoreThreshold: rt.Config.Harness.ScoreThreshold,
			DecayFactor:    rt.Config.Harness.DecayFactor,
			AnchorRatio:    rt.Config.Harness.AnchorRatio,
		})
	}

	// 4) 本次补丁中需重启生效的字段清单（启动期创建、无热重建链路的组件）
	// 注：cli_defaults.* 不在此列——effort 已热下发到后端；mode/key_newline 每次
	// 使用现读 Config（即时生效）；model/threshold/overseer_threshold 无运行期
	// 消费者（仅默认值填充），重启与否都不改变运行行为，列为重启项会误导用户。
	restartKeys := map[string]bool{
		"project.root": true, "project.vocab": true,
		"acp.enabled": true, "acp.port": true,
		"plugins.enabled": true, "plugins.directory": true,
		"plugins.sandbox.memory_limit_mb": true, "plugins.sandbox.cpu_limit": true,
		"memory.directory": true, "memory.max_sessions": true,
		"channels.webhook.enabled": true, "channels.webhook.token": true,
		"channels.cron.enabled": true,
		"trace.export_endpoint": true, "trace.export_batch": true, "trace.export_interval": true,
		"sandbox.enabled": true, "sandbox.discard_on_exit": true,
		"mcp.servers": true,
	}
	var needsRestart []string
	for k := range patch {
		if restartKeys[k] {
			needsRestart = append(needsRestart, k)
		}
	}
	return needsRestart
}

func (rt *Runtime) ListSessions() []*types.Session {
	return rt.Memory.List()
}

func (rt *Runtime) LoadSession(uuid string) (*types.Session, *types.Session, error) {
	if rt.Memory == nil {
		return nil, nil, fmt.Errorf("记忆系统未初始化")
	}
	target, err := rt.Memory.Load(uuid)
	if err != nil {
		return nil, nil, err
	}
	events, eventsErr := rt.Memory.LoadEvents(uuid)
	// currentSessionID 与历史同锁保护（GUI 并发读写安全）
	rt.historyMu.Lock()
	rt.currentSessionID = target.UUID
	// 目标状态机按会话隔离：切到该会话的目标（若该会话此前有活动目标则自动恢复）
	rt.goalMgr.SetCurrent(target.UUID)
	// 重启后自动恢复该会话的磁盘目标（缺陷 A：此前 bySID 全空，发消息会静默
	// 新建目标、旧目标失联；RestoreFor 按 SessionID 匹配最近未完成目标挂回内存）
	rt.goalMgr.RestoreFor(target.UUID)

	// 从事件日志恢复 conversationHistory，若存在压缩持久化快照（CompactedMessages），
	// 以快照为压缩基线，并把 Seq 大于快照覆盖点（CompactedSeq）的增量事件追加合并：
	// 快照只在压缩发生时写入，压缩后的新对话在快照之后——若不合并，重连/切换会话
	// 加载时快照会覆盖进程内完整历史，丢失压缩后的对话（GUI 强制中止断开后再继续
	// 对话"不再连续"的历史缺陷根因）。合并后按预算重压缩，保持 ctx 占比不膨胀。
	rt.conversationHistory = nil
	// 滚动摘要是【会话级】数据：加载会话必须按当前会话重建。
	// ctxBudget.Summary 是 Runtime 级单例——GUI/CLI 直接 LoadSession 切换会话时
	// 不经过 NewSession 的 ResetContextSummary，若不清空会把上一个会话的摘要
	// 注入当前会话上下文（跨会话串扰 → 答非所问/严重跑偏）。先清空，快照路径
	// 再按需恢复本会话自己的摘要；事件重放路径由本次压缩累积生成本会话摘要。
	if rt.ctxBudget != nil {
		rt.ctxBudget.ResetSummary()
	}
	if len(target.CompactedMessages) > 0 {
		// 快照恢复：采用压缩后的消息列表（含 [历史摘要] 注入）作为基线
		rt.conversationHistory = make([]types.Message, len(target.CompactedMessages))
		copy(rt.conversationHistory, target.CompactedMessages)
		// 恢复滚动摘要（防后续再压缩时摘要丢失；CompactedSummary 为空则保持已清空）
		if rt.ctxBudget != nil && target.CompactedSummary != "" {
			rt.ctxBudget.Summary = target.CompactedSummary
		}
		// 增量合并：快照覆盖点之后写入事件日志的消息（压缩后的新对话）。
		// 无法读取增量时静默降级为纯快照（不阻断会话加载）。
		if after, err := rt.Memory.LoadEventsAfter(target.UUID, target.CompactedSeq); err == nil {
			for _, evt := range after {
				if msg := eventToMessage(evt); msg != nil {
					rt.conversationHistory = append(rt.conversationHistory, *msg)
				}
			}
		}
		// 合并后重压缩：增量可能使总量超出预算，必须回到安全红线内，
		// 并把新压缩结果持久化（CompactedSeq 推进到日志最新 Seq）
		if rt.ctxBudget != nil {
			sid := rt.currentSessionID
			compacted, changed := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(sid, m) })
			if changed {
				rt.conversationHistory = compacted
				if sm := rt.ctxBudget.SummaryMessage(); sm != nil {
					first := ""
					if len(rt.conversationHistory) > 0 {
						first, _ = rt.conversationHistory[0].Content.(string)
					}
					if !strings.HasPrefix(first, "[历史摘要]") {
						rt.conversationHistory = append([]types.Message{*sm}, rt.conversationHistory...)
					}
				}
				// 摘要注入后二次压缩，确保恢复后第一轮请求不超限
				if compacted2, _ := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(sid, m) }); compacted2 != nil {
					rt.conversationHistory = compacted2
				}
			}
		}
	} else if eventsErr == nil {
		for _, evt := range events {
			msg := eventToMessage(evt)
			if msg != nil {
				rt.conversationHistory = append(rt.conversationHistory, *msg)
			}
		}
		// 预算压缩 + 滚动摘要注入（复用 AddToHistory 同一套策略）
		if rt.ctxBudget != nil {
			sid := rt.currentSessionID
			compacted, changed := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(sid, m) })
			if changed {
				rt.conversationHistory = compacted
				if sm := rt.ctxBudget.SummaryMessage(); sm != nil {
					first := ""
					if len(rt.conversationHistory) > 0 {
						first, _ = rt.conversationHistory[0].Content.(string)
					}
					if !strings.HasPrefix(first, "[历史摘要]") {
						rt.conversationHistory = append([]types.Message{*sm}, rt.conversationHistory...)
					}
				}
				// 摘要注入后二次压缩，确保恢复后第一轮请求不超限
				if compacted2, _ := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(sid, m) }); compacted2 != nil {
					rt.conversationHistory = compacted2
				}
			}
		}
	}
	rt.historyMu.Unlock()
	// 锁外批量写入压缩原文索引（LoadSession 恢复历史时可能触发重压缩）
	rt.flushEvictPending()

	return nil, target, nil
}

// eventToMessage 将 SessionEvent 转换为 Message，用于恢复对话历史。
func eventToMessage(evt *types.SessionEvent) *types.Message {
	switch evt.Type {
	case "user_msg", "user_input":
		if s, ok := evt.Data.(string); ok {
			// 识图：事件日志存纯文本（图片以 [图片: 路径] 标记），
			// 恢复时按路径重建 image_url 片段（文件丢失则保留失效提示文本）
			m := vision.MessageFromEventText(s)
			return &m
		}
		if data, ok := evt.Data.(map[string]any); ok {
			if c, ok := data["content"].(string); ok {
				return &types.Message{Role: "user", Content: c}
			}
		}
	case "assistant_msg":
		if s, ok := evt.Data.(string); ok {
			return &types.Message{Role: "assistant", Content: s}
		}
		if data, ok := evt.Data.(map[string]any); ok {
			if c, ok := data["content"].(string); ok {
				return &types.Message{Role: "assistant", Content: c}
			}
		}
	}
	return nil
}

func (rt *Runtime) CompressContext() string {
	if rt.Harness == nil {
		return "上下文引擎未初始化"
	}
	// 1) Harness 检索索引衰减（降低旧块的检索权重）
	rt.Harness.ApplyDecay()

	// 2) 真正压缩对话历史：手动 /compress 强制渐进压缩（截断→丢弃→滚动摘要），
	//    与 AddToHistory 同一套策略——这样状态栏 ctx 占比会实时下降。
	before := 0
	after := 0
	truncated, dropped := 0, 0
	rt.historyMu.Lock()
	before = ctxmgr.MessagesTokens(rt.conversationHistory)
	sidCompress := rt.currentSessionID
	if rt.ctxBudget != nil {
		compacted, changed := rt.ctxBudget.ForceCompactWithEvict(rt.conversationHistory, func(m types.Message) { rt.indexCompressedEvictedFor(sidCompress, m) })
		if changed {
			// 统计动作（审计日志已由 CompactToBudget 内部记录）
			for _, a := range rt.ctxBudget.AuditLog() {
				if strings.Contains(a, "丢弃") {
					dropped++
				} else if strings.Contains(a, "截断") {
					truncated++
				}
			}
			rt.conversationHistory = compacted
			// 滚动摘要注入（受保护，不会被再次压缩丢弃）
			if sm := rt.ctxBudget.SummaryMessage(); sm != nil {
				first := ""
				if len(rt.conversationHistory) > 0 {
					first, _ = rt.conversationHistory[0].Content.(string)
				}
				if !strings.HasPrefix(first, "[历史摘要]") {
					rt.conversationHistory = append([]types.Message{*sm}, rt.conversationHistory...)
				}
			}
			// 摘要注入后二次压缩兜底
			if compacted2, _ := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(sidCompress, m) }); compacted2 != nil {
				rt.conversationHistory = compacted2
			}
		}
	}
	after = ctxmgr.MessagesTokens(rt.conversationHistory)
	sid := rt.currentSessionID
	rt.historyMu.Unlock()
	// 锁外批量写入压缩原文索引（锁内只收集，见 indexCompressedEvictedFor 注释）
	rt.flushEvictPending()
	// 手动压缩同样持久化快照（/compress 与 GUI /compact 后重开会话也保持压缩状态）
	if rt.ctxBudget != nil && sid != "" {
		rt.persistCompactionSnapshot(sid, 0)
	}

	released := before - after
	if released < 0 {
		released = 0
	}
	// 占比用高精度（%.2f%%）：max_context 大（如 100w）时几千 token 只占
	// 0.0x%——旧版 pctOf 取整后显示 "0% → 0%"，用户看到状态栏"毫无变化"
	// （GUI 压缩提示 + 状态栏百分比均来自本字符串语义）。
	//
	// 无变化短路：系统提示（含任务指令）恒保留 + 消息条数 ≤ keepRecent 且
	// 未超安全红线时 ForceCompact 不产生任何动作——旧版文案仍显示
	// "释放 0 tokens（0.00% → 0.00%）"，用户会误以为压缩已执行但界面
	// 没反应（GUI 紧凑态重绘还没发生）。此时如实说明"无需压缩"，并给出
	// 当前实际用量，让用户在 TUI/GUI 都能看清发生了什么。
	//
	// ⚠️ 压无可压也必须走短路：ForceCompact 只要条数 > keepRecent 就强制
	// 进入压缩流程（预算压到当前用量的 60%），即使所有消息都已到截断下限
	// （无可释放 token），truncated/dropped 审计计数仍会累加——若短路条件
	// 再要求两者为 0，则返回"已压缩：截断 N/丢弃 N，释放 0 tokens（x% → x%）"
	// 的自相矛盾文案：占比没变却宣称"已压缩"，用户连点多次看到"释放 0"
	// 感知为"压缩只能生效一次/按钮失灵"（真实用户反馈）。
	// 因此只要 released == 0（无论是否有截断/丢弃动作记录）都返回"无需压缩"。
	if released == 0 {
		why := "对话历史较短或已压缩过"
		if n := len(rt.conversationHistory); n > 0 && rt.ctxBudget != nil && rt.ctxBudget.KeepRecent >= n {
			why = fmt.Sprintf("消息仅 %d 条（≤ 保留 %d 条）", n, rt.ctxBudget.KeepRecent)
		} else if truncated > 0 || dropped > 0 {
			why = "上下文已压至精简态（无可再释放）"
		}
		return fmt.Sprintf("无需压缩：%s，当前占用 %.2f%%（%d tokens/%d）", why, pctOf2(before, rt.ctxBudget), before, rt.ctxBudget.MaxTokens)
	}
	return fmt.Sprintf("上下文已压缩：截断 %d 条 / 丢弃 %d 条，释放 %d tokens（%.2f%% → %.2f%%）",
		truncated, dropped, released, pctOf2(before, rt.ctxBudget), pctOf2(after, rt.ctxBudget))
}

// pctOf2 高精度占比（百分比，0.00 表示低于 0.005%）：供压缩提示文案与
// 状态栏展示使用——大预算（100w）场景下取整会把小占比抹成 0%。
func pctOf2(tokens int, b *ctxmgr.TokenBudget) float64 {
	if b == nil || b.MaxTokens <= 0 {
		return 0
	}
	return b.Usage(tokens) * 100
}

// indexCompressedEvicted 将被压缩移除的完整原文写入 Harness 检索索引，
// 使 AI 之后可通过 harness_search 找回被截断/丢弃的内容（压缩≠永久丢失）。
// 带 role 与 [压缩原文] 标记便于识别来源；ID 用时间戳+计数器保证唯一。
// 压缩原文属于当前会话私有：块带 SessionID，仅该会话可检索到——
// 防止同项目不同会话（task/goal 模式）harness_search 搜回他会话对话导致串扰。
//
// ⚠️ 调用方持 historyMu 锁时请用 indexCompressedEvictedFor(sid, m)：
// 它【只把块加入 evictPending 收集区，不触碰 Harness】——锁外由
// flushEvictPending 统一批量入索引。原因：Harness.Index 持 Harness 全局锁
// 并做倒排重建+缓存失效，若在 historyMu 写锁内逐条调用，压缩一条大历史
// （几十条消息×KB 级原文）会让 historyMu 被占用数十毫秒~数百毫秒，期间
// 所有 GUI WS 请求（status.bar 2s 轮询 / chat.loadSession / 点击会话）全部
// 排队 → 界面"无反应、状态栏不刷、历史不加载"（真实用户反馈三症状同源）。
func (rt *Runtime) indexCompressedEvicted(m types.Message) {
	rt.historyMu.RLock()
	sid := rt.currentSessionID
	rt.historyMu.RUnlock()
	rt.indexCompressedEvictedFor(sid, m)
	// 锁外调用（工具/测试等非持锁路径）：立即批量落库，保持"调用即可检索"语义
	rt.flushEvictPending()
}

// indexCompressedEvictedFor 与 indexCompressedEvicted 同逻辑，但显式指定
// 会话 ID（调用方已在锁内读取 sid 时使用，避免 RWMutex 重入）。
// 锁内调用时只做内存收集（O(1)），真正的 Harness 索引在锁外 flush。
func (rt *Runtime) indexCompressedEvictedFor(sid string, m types.Message) {
	if rt.Harness == nil {
		return
	}
	s, ok := m.Content.(string)
	if !ok || s == "" || strings.HasPrefix(s, "[历史摘要]") {
		return
	}
	rt.evictSeq++
	block := types.ContextBlock{
		ID:        fmt.Sprintf("compressed:%d:%d", time.Now().UnixNano(), rt.evictSeq),
		Content:   "[压缩原文 " + m.Role + "] " + s,
		Score:     50,
		LastUsed:  time.Now(),
		SessionID: sid,
	}
	rt.evictPending = append(rt.evictPending, block)
}

// flushEvictPending 把锁内收集的压缩原文批量写入 Harness 索引（锁外调用）。
// 必须在 historyMu 已解锁、sid 上下文明确时调用（从锁内提取后调用）。
// 空收集直接返回（零开销）；Harness 关闭时清空收集区。
func (rt *Runtime) flushEvictPending() {
	if len(rt.evictPending) == 0 {
		return
	}
	blocks := rt.evictPending
	rt.evictPending = nil
	if rt.Harness == nil {
		return
	}
	rt.Harness.Index(context.Background(), blocks)
}

// SaveHarnessIndex 将 Harness 检索索引落盘（原子写，损坏不丢旧索引）。
// 供 CloseHarness（退出兜底）、自动保存 goroutine 与外部显式调用使用。
func (rt *Runtime) SaveHarnessIndex() error {
	if rt.Harness == nil || rt.harnessIndexPath == "" {
		return nil
	}
	if err := rt.Harness.SaveIndex(rt.harnessIndexPath); err != nil {
		return fmt.Errorf("保存 Harness 索引失败: %w", err)
	}
	return nil
}

// CloseHarness 停止自动保存 goroutine 并做最后一次落盘。
// main 的退出路径（TUI/GUI/--serve）统一调用；幂等，可重复调用。
func (rt *Runtime) CloseHarness() {
	if rt.harnessSaveStop != nil {
		close(rt.harnessSaveStop)
		<-rt.harnessSaveDone
		rt.harnessSaveStop = nil
		rt.harnessSaveDone = nil
	}
	if err := rt.SaveHarnessIndex(); err != nil {
		fmt.Fprintf(os.Stderr, "警告: %v\n", err)
	}
}

// startHarnessAutoSave 启动索引自动保存（30s 节流轮询）。
// 压缩/检索块写入后不立即写盘（避免高频 IO），由 NeedsSave 变更计数驱动：
// 有未落盘变更才保存；进程正常退出由 CloseHarness 兜底。
func (rt *Runtime) startHarnessAutoSave() {
	rt.harnessSaveStop = make(chan struct{})
	rt.harnessSaveDone = make(chan struct{})
	go func() {
		defer close(rt.harnessSaveDone)
		// 防御：自动保存 goroutine 意外 panic 不拖垮主进程
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "警告: Harness 自动保存 panic: %v\n", r)
			}
		}()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if rt.Harness != nil && rt.Harness.NeedsSave() {
					if err := rt.SaveHarnessIndex(); err != nil {
						fmt.Fprintf(os.Stderr, "警告: %v\n", err)
					}
				}
			case <-rt.harnessSaveStop:
				return
			}
		}
	}()
}

// pctOf 计算 tokens 占预算的百分比（无预算时返回 0）。
func pctOf(tokens int, b *ctxmgr.TokenBudget) int {
	if b == nil || b.MaxTokens <= 0 {
		return 0
	}
	return int(b.Usage(tokens) * 100)
}

func (rt *Runtime) ACPStatus() string {
	return fmt.Sprintf("ACP 端口 %d (已启用: %v)", rt.Config.ACP.Port, rt.Config.ACP.Enabled)
}

// ListSkills 返回所有可用技能（SKILL.md 加载结果），供 /skills 命令展示。
func (rt *Runtime) ListSkills() []*types.Skill {
	if rt.skillLoader == nil {
		return nil
	}
	return rt.skillLoader.List()
}

// InstallSkill 从市场安装技能到项目技能目录并刷新加载器。
// 已存在同名技能时返回错误（不覆盖，防误伤）。
func (rt *Runtime) InstallSkill(name string) (string, error) {
	if rt.Market == nil || rt.skillLoader == nil {
		return "", fmt.Errorf("技能市场未初始化")
	}
	dir := rt.skillLoader.Dir()
	if err := rt.Market.Install(name, dir); err != nil {
		return "", err
	}
	if err := rt.skillLoader.Load(); err != nil {
		return "", fmt.Errorf("技能已安装但刷新失败: %w", err)
	}
	return dir, nil
}

// SetActiveSkills 设置当前会话选中的技能，并持久化到 settings。
func (rt *Runtime) SetActiveSkills(names []string) {
	if rt.skillLoader == nil {
		return
	}
	rt.activeSkills = make([]string, 0, len(names))
	for _, name := range names {
		if s, ok := rt.skillLoader.Get(name); ok && s != nil {
			rt.activeSkills = append(rt.activeSkills, name)
		}
	}
	rt.saveSettings()
}

// GetActiveSkills 返回当前会话选中的技能名列表。
func (rt *Runtime) GetActiveSkills() []string {
	return rt.activeSkills
}

// GetActiveSkillContents 返回当前选中技能的正文内容，供注入系统提示词。
func (rt *Runtime) GetActiveSkillContents() []*types.Skill {
	if rt.skillLoader == nil {
		return nil
	}
	var result []*types.Skill
	for _, name := range rt.activeSkills {
		if s, ok := rt.skillLoader.Get(name); ok && s != nil {
			result = append(result, s)
		}
	}
	return result
}

func (rt *Runtime) PluginsList() string {
	plugins := rt.Plugins.List()
	if len(plugins) == 0 {
		return "无已加载插件"
	}
	names := make([]string, len(plugins))
	for i, p := range plugins {
		names[i] = p.String()
	}
	return strings.Join(names, "\n")
}

func (rt *Runtime) InjectBlock(blockID string) string {
	rt.Harness.Rerank(context.Background(), blockID)
	return fmt.Sprintf("上下文块 %s 已提升至锚定区", blockID)
}

// ProcessInput processes user input through the full pipeline:
//  1. Preprocessor: intent parsing + vocab mapping
//  2. Harness: context search
//  3. Executor: spawn agent
//  4. Agent: stream response from model
//
// It returns channels for streaming tokens and errors, suitable for TUI consumption.
func (rt *Runtime) ProcessInput(ctx context.Context, input string) (<-chan string, <-chan error, error) {
	// Step 1: Preprocess input
	prompt, err := rt.Preproc.Process(input)
	if err != nil {
		return nil, nil, fmt.Errorf("预处理失败: %w", err)
	}

	// Step 2: Create task
	mode := rt.GetMode()
	// 场景中心（M4 接线，零配置兜底）：chat 模式下规则层关键词命中才接管；
	// 未命中/无 scenes 目录 → 完全走旧路径（裸 chat 行为不变）。
	if mode == "chat" {
		if sc := rt.matchScene(input); sc != nil {
			return rt.launchScene(ctx, input, *sc)
		}
	}
	// task/goal 模式主 Agent ID 固定为 "main"，便于子 Agent 通过消息总线寻址（§5.1）
	agentID := fmt.Sprintf("agent-%d", len(rt.Executor.Agents())+1)
	if mode == "task" || mode == "goal" {
		agentID = "main"
	}
	task := types.TaskCreate{
		Type:          "task.create",
		ID:            agentID,
		Directive:     prompt.Directive,
		Task:          prompt.Task,
		Payload:       prompt,
		Mode:          mode,                    // 提示词模式: "chat" | "task" | "goal"
		ContextPolicy: types.PolicyFullContext, // 上下文策略（与提示词模式分离）
		Priority:      1,
	}

	// 识图检测：输入含图片路径时读取并编码为 image_url 片段（OpenAI 兼容格式，
	// 按路径与后缀判断，不传文件）。TUI 与 GUI 共用此入口；agent 据此切换
	// vision_model 并把图片附到本轮消息。
	if imgs, err := rt.detectTaskImages(input); err != nil {
		return nil, nil, fmt.Errorf("识图失败: %w", err)
	} else {
		task.Images = imgs
	}

	// goal 模式：绑定目标会话状态机（跨轮持久循环，按对话会话隔离）
	// 规则：无会话/已终止/目标归属其他会话 → 输入作为本会话的新目标；
	//       本会话已有活动目标 → 输入作为推进指令
	// （任务始终指向本会话的目标，用户输入作为对话上下文，避免跨会话串扰）
	if mode == "goal" {
		curSID := rt.GetSessionID()
		gs := rt.goalMgr.Session()
		if gs == nil || gs.Done() || (gs.SessionID != "" && gs.SessionID != curSID) {
			gs = rt.goalMgr.Start(input, 0, int64(rt.Config.ActiveModel().MaxContext)*goalmgr.DefaultTokenMultiplier)
		}
		task.Task = gs.Goal // 任务描述恒为目标文本
		if gs.ActivePhase() == types.GoalPhaseSpec {
			task.Directive = string(types.DirectiveSpec)
		} else if gs.ActivePhase() == types.GoalPhasePlan {
			task.Directive = string(types.DirectivePlan)
		}
		task.Payload = gs
	}

	return rt.launchAgent(ctx, task, input, mode)
}

// launchAgent 构造 Agent 并启动流式执行（chat/task/goal 共用骨架）。
func (rt *Runtime) launchAgent(ctx context.Context, task types.TaskCreate, input, mode string) (<-chan string, <-chan error, error) {
	// 沙箱模式（config sandbox.enabled）：向工具执行注入影子文件系统。
	// 子 Agent（spawn_task/handoff）同步运行于父 ctx 内，自动继承沙箱。
	if rt.sbFS != nil {
		ctx = sbox.WithSandbox(ctx, rt.sbFS)
	}
	// task/goal 模式：创建监工 Agent（§9.3），多 Agent 编排交由 AI 自主决策
	// goal 模式的监工评估驱动目标循环（未通过自动续跑）
	// AI 通过 spawn_task 工具决定启动多少子 Agent，contextloom 只提供接口和规范
	if mode == "task" || mode == "goal" {
		rt.overseerMu.Lock()
		ov := overseer.New(
			input,
			map[string]any{
				"min_test_coverage": 60.0,
				"min_code_quality":  70.0,
			},
			types.Score(rt.Config.Overseer.PassThreshold),
			nil,
		)
		// node_auto_plan：自动把输入拆分为计划节点注册到监工（§3.2 计划节点追踪）
		if rt.Config.Overseer.NodeAutoPlan {
			rt.registerPlanNodes(ov, input)
		}
		rt.currentOverseer = ov
		rt.overseerMu.Unlock()
	} else {
		// chat 模式：清除上一次 task 请求残留的监工，避免监工状态泄漏到后续请求
		rt.overseerMu.Lock()
		rt.currentOverseer = nil
		rt.overseerMu.Unlock()
	}

	agent := rt.Executor.SpawnAgent(task.ID, task, rt.selectBackend(ctx, rt.agentRouteReq(task, mode)))
	// 会话隔离：Agent 所有检索/压缩入索引以当前会话为界（防同项目跨会话串扰）
	agent.SetSessionID(rt.GetSessionID())
	// 识图：注入 vision_model（agent 仅在任务携带图片时切换，结束后自动恢复）
	if rt.Config != nil {
		agent.SetVisionModel(rt.Config.ActiveModel().VisionModel)
	}
	convHistory := rt.GetConversationHistory()
	// goal/task 模式：从检查点恢复未完成任务的上文（崩溃/中断后续跑不丢上下文）。
	// ⚠️ 压缩回归修复：旧实现用检查点消息【整体覆盖】内存历史，而检查点写盘的是
	// agent 构建的全量 messages（压缩只改 conversationHistory，不写 checkpoint）——
	// 导致压缩后下一轮恢复全量，"表面压缩、实际发送全量"。restoreFromCheckpoint
	// 现改为与内存压缩历史合并衔接（只追加增量，不倒灌被压缩的旧消息）并经预算
	// 压缩兜底；hasRestore=false 表示无检查点/已完成，保持原历史。
	if mode == "goal" || mode == "task" {
		if restored, hasRestore := rt.restoreFromCheckpoint(task.ID); hasRestore && restored != nil {
			convHistory = restored
		}
	}
	agent.SetConversationHistory(convHistory)
	agent.SetTracer(rt)

	// 每轮检查点落盘（对标 LangGraph checkpointer：异常中断后可从最新快照恢复）
	// 主任务含消息体（恢复用，内部裁剪到最近 40 条）；子任务不含（从不恢复）
	rt.attachCheckpointSink(agent, task.ID, rt.GetSessionID(), mode, true)

	// task/goal 模式开启自动批准：跳过权限确认弹窗，实现全自动执行。
	if mode == "task" || mode == "goal" {
		agent.SetAutoApprove(true)
	}

	// 注入持久化路径（供系统提示词告知模型配置/记忆/词汇表等文件位置）
	agent.SetPersistencePaths(rt.PersistencePaths())

	// 注入插件 pre 阶段提示片段（system role，影响模型行为）
	agent.SetPluginInject(rt.Plugins.RunPreHooks())
	// v2 插件动态钩子：message（每轮消息后注入）/ route（路由决策）。
	// 钩子闭包直接引用 Manager 的脚本引用列表——脚本执行经沙箱（sandbox.RunScriptTimeout），
	// 失败/超时/空输出静默跳过（插件绝不阻断主流程）。
	if rt.sandbox != nil {
		msgRefs := rt.Plugins.MessageInjects()
		if len(msgRefs) > 0 {
			refs := msgRefs
			agent.SetPluginMessageHook(func(input string, round int) string {
				return rt.runPluginMessageHooks(refs, input, round)
			})
		}
		routeRefs := rt.Plugins.RouteScripts()
		if len(routeRefs) > 0 {
			refs := routeRefs
			agent.SetPluginRouteHook(func(mode, input string, round int) string {
				return rt.runPluginRouteHooks(refs, mode, input, round)
			})
		}
	}
	// 注入技能内容（选中 skill 的正文，注入系统提示词供模型参考）
	skills := rt.GetActiveSkillContents()
	if len(skills) > 0 {
		contents := make([]string, len(skills))
		for i, s := range skills {
			if s.Toolchain != "" {
				// 技能关联工具链（§4.3.2）：提示模型经 run_toolchain 工具强制执行该链，
				// 让技能"不只建议，还能强制执行链"。
				contents[i] = s.Content + "\n\n> 本技能已关联工具链 `" + s.Toolchain +
					"`：处理此类任务时请调用 run_toolchain 工具（name=\"" + s.Toolchain +
					"\"）执行该链，并基于链产物完成交付。"
			} else {
				contents[i] = s.Content
			}
		}
		agent.SetSkillContents(contents)
	}

	// chat 模式过滤多 Agent 工具（spawn_task/send_message/check_messages/handoff），
	// 保持单对话语义——chat 模式不应有子 Agent 生成和消息总线通信能力
	// （task/goal 模式保留多 Agent 能力：可拆子任务、消息总线、目标拆解、串行交接）
	if mode != "task" && mode != "goal" {
		agent.SetTools(rt.Tools.CloneWithout("spawn_task", "send_message", "check_messages", "handoff"))
	}

	// 记录 Agent ID，供流结束后监工评估关联
	rt.overseerMu.Lock()
	rt.lastAgentID = task.ID
	rt.overseerMu.Unlock()

	// Step 3: Run agent with streaming
	ch, errCh, runErr := agent.RunStream(ctx)
	if runErr != nil {
		return nil, nil, fmt.Errorf("agent启动失败: %w", runErr)
	}

	// Step 4: Apply harness decay after processing
	rt.Harness.ApplyDecay()

	// Step 5: 执行插件 during/post 阶段脚本
	// during 脚本（pre_tool/post_tool）在 agent 流启动后异步执行
	for _, ref := range rt.Plugins.RunDuringScripts() {
		if rt.sandbox != nil {
			go func(r plugin.ScriptRef) {
				_, _ = rt.sandbox.RunScript(r.ScriptPath, []byte(input))
			}(ref)
		}
	}
	// post 脚本（finalize）同步执行（会话收尾）
	for _, ref := range rt.Plugins.RunPostScripts() {
		if rt.sandbox != nil {
			_, _ = rt.sandbox.RunScript(ref.ScriptPath, []byte(input))
		}
	}

	return ch, errCh, nil
}

// ── v2 插件动态钩子执行（message 每轮注入 / route 路由决策 / 插件工具）──

// runPluginMessageHooks 执行所有插件的 message 钩子脚本，拼接返回注入段。
// 脚本 stdin = JSON {input, round, mode}，stdout = 提示词段（原样注入 user 消息后）；
// 失败/超时/空输出静默跳过（插件绝不阻断主流程）。
// 注意：钩子每轮构建消息时执行，脚本应保持轻量（<100ms 量级）。
func (rt *Runtime) runPluginMessageHooks(refs []plugin.MessageInjectRef, input string, round int) string {
	if rt.sandbox == nil || len(refs) == 0 {
		return ""
	}
	var parts []string
	for _, ref := range refs {
		payload, _ := json.Marshal(map[string]any{
			"input": input,
			"round": round,
			"mode":  rt.GetMode(),
		})
		out, err := rt.sandbox.RunScriptTimeout(ref.ScriptPath, payload, 30*time.Second)
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			continue
		}
		parts = append(parts, strings.TrimSpace(string(out)))
	}
	return strings.Join(parts, "\n")
}

// runPluginRouteHooks 执行所有插件的 route 钩子脚本，拼接返回路由引导段（注入 system 区）。
// 脚本 stdin = JSON {mode, input, round}，stdout = 路由引导文本（动态决策，可随输入/轮次变化）。
func (rt *Runtime) runPluginRouteHooks(refs []plugin.RouteRef, mode, input string, round int) string {
	if rt.sandbox == nil || len(refs) == 0 {
		return ""
	}
	var parts []string
	for _, ref := range refs {
		payload, _ := json.Marshal(map[string]any{
			"mode":  mode,
			"input": input,
			"round": round,
		})
		out, err := rt.sandbox.RunScriptTimeout(ref.ScriptPath, payload, 30*time.Second)
		if err != nil || len(strings.TrimSpace(string(out))) == 0 {
			continue
		}
		parts = append(parts, strings.TrimSpace(string(out)))
	}
	return strings.Join(parts, "\n")
}

// runPluginTool 执行插件工具脚本：参数 JSON 经 stdin 传入，stdout 为结果。
// 超时使用工具声明的 timeout（0=默认 30s）。
func (rt *Runtime) runPluginTool(reg plugin.ToolRegistration, params map[string]any) ([]byte, error) {
	if rt.sandbox == nil {
		return nil, fmt.Errorf("工具沙箱未初始化")
	}
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("参数序列化失败: %w", err)
	}
	timeout := 30 * time.Second
	if reg.TimeoutSec > 0 {
		timeout = time.Duration(reg.TimeoutSec) * time.Second
	}
	out, err := rt.sandbox.RunScriptTimeout(reg.ScriptPath, payload, timeout)
	if err != nil {
		return out, err
	}
	return out, nil
}

// AddToHistory 保存消息到对话历史（多轮连续性）
// 采用滑动窗口+旧消息摘要压缩策略（需求 §13.2）
// 借鉴业界开源 Agent 框架：思维链(reasoning_content)不写回历史，只保留正文
func (rt *Runtime) AddToHistory(role, content string) {
	// 思维链不回传问题修复（DeepSeek thinking 模式 API 400）：
	// 不再整体 StripThinking 丢弃思维链——由 buildHistoryMessage 用 SplitReasoning
	// 分离：正文进 Content、思维链进 ReasoningContent（历史持久化 + API 原样回传），
	// 事件日志纯文本只保留正文（展示层不显示思维链）。
	// 识图：user 消息检测图片路径并组装多模态（多轮追问图片仍在上下文中），
	// eventText 是事件日志持久化用纯文本（图片以 [图片: 路径] 标记）
	msg, eventText := rt.buildHistoryMessage(role, content)
	rt.historyMu.Lock()
	rt.conversationHistory = append(rt.conversationHistory, msg)

	// token 预算驱动的渐进式压缩（ctxmgr）：
	// 完整保留 → 前缀截断 → 丢弃最旧（累积滚动摘要）→ 二次截断兜底。
	// 替代旧版"按条数断崖截断（>92% 砍到剩 3 条）"。
	compacted := false
	// 压缩入索引的原文归属当前会话（锁内读取 sid，避免 RWMutex 重入）
	addSid := rt.currentSessionID
	if rt.ctxBudget != nil {
		compactedList, changed := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(addSid, m) })
		if changed {
			rt.conversationHistory = compactedList
			compacted = true
			// 注入滚动摘要（防断崖式失忆），避免重复注入
			if sm := rt.ctxBudget.SummaryMessage(); sm != nil {
				first := ""
				if len(rt.conversationHistory) > 0 {
					first, _ = rt.conversationHistory[0].Content.(string)
				}
				if !strings.HasPrefix(first, "[历史摘要]") {
					rt.conversationHistory = append([]types.Message{*sm}, rt.conversationHistory...)
				}
			}
			// 摘要注入后二次压缩，确保总量回到安全红线内
			if compacted2, _ := ctxmgr.CompactToBudgetWithEvict(rt.conversationHistory, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(addSid, m) }); compacted2 != nil {
				rt.conversationHistory = compacted2
			}
		}
	}
	// 压缩/截断等操作结束后，锁内取出会话 ID（与写端同锁，防 data race），
	// 随即解锁。注意顺序：事件日志【先】落盘、压缩快照【后】写——快照记录
	// 覆盖到日志最新 Seq（persistCompactionSnapshot 内部读 LastEventSeq），
	// 只有先写事件再写快照，快照覆盖点才包含本消息；否则重连加载时本消息
	// 会以增量身份与快照重复合并（历史缺陷：压缩后新对话在重连加载时丢失或错乱）。
	sid := rt.currentSessionID
	rt.historyMu.Unlock()
	// 锁外批量写入压缩原文索引（AddToHistory 渐进压缩可能收集了多条）
	rt.flushEvictPending()
	eventSeq := int64(0)
	if rt.Memory != nil && sid != "" {
		eventType := "user_msg"
		if role == "assistant" {
			eventType = "assistant_msg"
		}
		event := &types.SessionEvent{
			Seq:       time.Now().UnixNano(),
			Timestamp: time.Now(),
			Type:      eventType,
			Data:      eventText,
		}
		if err := rt.Memory.AppendEvent(sid, event); err == nil {
			eventSeq = event.Seq
		}
	}
	if compacted && sid != "" {
		rt.persistCompactionSnapshot(sid, eventSeq)
	}
}

// imageBaseDir 返回图片相对路径的解析基准目录：优先项目根（config.project.root），
// 空时由 vision 包回退当前工作目录。与 tools.SetProjectRoot 的项目根保持一致。
func (rt *Runtime) imageBaseDir() string {
	if rt.Config != nil && rt.Config.Project.Root != "" {
		return rt.Config.Project.Root
	}
	return ""
}

// StageImages 暂存 GUI 上传的图片（base64 data URL 已由 GUI 层解码落盘为
// vision.Image），供紧接着的一次 ProcessInput 消费为多模态片段。
// 与 TUI 按路径识图的区别：浏览器 File API 拿不到本地绝对路径，只能
// dataURL 上传——图片以结构化工件（OpenAI 多模态 content 片段）直传，
// 不再拼 "[图片: 路径]" 文本标记绕行（主流识图接口）。
func (rt *Runtime) StageImages(images []vision.Image) {
	if len(images) == 0 {
		return
	}
	rt.historyMu.Lock()
	rt.stagedImages = append(rt.stagedImages, images...)
	rt.historyMu.Unlock()
}

// ConsumeStagedImages 取走并清空暂存图片（ProcessInput 消费；未消费的
// 暂存图片随下一次消费自然废弃，避免跨轮错位）。
func (rt *Runtime) ConsumeStagedImages() []vision.Image {
	rt.historyMu.Lock()
	defer rt.historyMu.Unlock()
	imgs := rt.stagedImages
	rt.stagedImages = nil
	return imgs
}

// PeekStagedImages 返回暂存图片副本【不消费】。用途：AddToHistory 写历史
// 多模态消息（多轮追问图片仍在上下文）时借用副本，暂存本体留给紧接着的
// ProcessInput（ConsumeStagedImages）消费以触发识图模型切换。
func (rt *Runtime) PeekStagedImages() []vision.Image {
	rt.historyMu.RLock()
	defer rt.historyMu.RUnlock()
	return append([]vision.Image(nil), rt.stagedImages...)
}

// detectTaskImages 检测输入中的图片路径并构造多模态片段（无图片返回 nil）。
// 图片来源两条通道，合并去重：
//
//	a) 文本路径扫描（TUI 粘贴路径 / 命令行参数）：先做轻量文本扫描
//	   （HasImagePath，零文件 IO），命中才读文件；
//	b) 暂存图片（GUI 上传：StageImages 已解码落盘）——结构化直传，
//	   不依赖文本标记，与主流多模态接口一致。
func (rt *Runtime) detectTaskImages(input string) ([]types.ContentPart, error) {
	// 图片来源两条通道，合并去重（同图只能出现一份 image_url）：
	//   a) 文本路径扫描（TUI 粘贴路径 / 命令行参数）：先做轻量文本扫描
	//      （HasImagePath，零文件 IO），命中才读文件；
	//   b) 暂存图片（GUI 上传：StageImages 已解码落盘）——结构化直传，
	//      不依赖文本标记，与主流多模态接口一致。
	// 去重必要性（用户实锤"原图和压缩图一起发给 AI，识别出两个图片"）：
	// GUI 上传同一张图 + 用户文本里又提到该路径（或历史恢复重读同一文件）
	// 时，两条通道会各产出一份 image_url——暂存与文本扫描合并必须去重，
	// 且 staged 优先（其 DataURI 已是压缩版）。
	var staged []vision.Image
	if staged = rt.ConsumeStagedImages(); len(staged) > 0 {
		// 暂存图本身可能重复（同一文件多次上传）→ 先自行去重
		staged = dedupeImages(staged)
	}
	var detected []vision.Image
	if vision.HasImagePath(input) {
		imgs, err := vision.DetectImages(input, rt.imageBaseDir())
		if err != nil {
			return nil, err
		}
		if len(imgs) > 0 {
			detected = imgs
		}
	}
	merged := mergeImages(detected, staged)
	if len(merged) == 0 {
		return nil, nil
	}
	return vision.BuildImageParts(merged), nil
}

// dedupeImages 按图片身份（绝对路径 Clean 或 DataURI）去重，保留首次出现者。
// 同一张图可能经多次上传/历史恢复产生多个副本，身份相同只留一份——
// 否则同一请求中出现两个 image_url（用户实锤"识别出两个图片"）。
func dedupeImages(imgs []vision.Image) []vision.Image {
	if len(imgs) <= 1 {
		return imgs
	}
	seen := make(map[string]bool, len(imgs)*2)
	out := imgs[:0:0]
	for _, img := range imgs {
		key := imgKey(img)
		if key != "" {
			if seen[key] {
				continue
			}
			seen[key] = true
		}
		out = append(out, img)
	}
	return out
}

// mergeImages 合并文本路径扫描结果与暂存图片：同一张图（同绝对路径或同
// DataURI）只保留一份，staged 优先（压缩版 DataURI，避免原图/压缩图双发）。
func mergeImages(detected, staged []vision.Image) []vision.Image {
	if len(detected) == 0 {
		return dedupeImages(staged)
	}
	if len(staged) == 0 {
		return dedupeImages(detected)
	}
	// 先登记 staged 身份，再过滤 detected 中重复者。
	out := dedupeImages(staged)
	seen := make(map[string]bool, len(out)*2)
	for _, img := range out {
		if k := imgKey(img); k != "" {
			seen[k] = true
		}
	}
	for _, d := range detected {
		k := imgKey(d)
		if k != "" && seen[k] {
			continue // 同路径/同 URI 已被暂存图占用 → 丢弃文本扫描副本
		}
		if k != "" {
			seen[k] = true
		}
		out = append(out, d)
	}
	return out
}

// imgKey 生成图片身份键：优先绝对路径（Clean），无路径时回退 DataURI。
func imgKey(img vision.Image) string {
	if img.Path != "" {
		return "p:" + filepath.Clean(img.Path)
	}
	if img.DataURI != "" {
		return "u:" + img.DataURI
	}
	return ""
}

// buildHistoryMessage 构造历史消息：user 消息检测图片路径并组装多模态（识图），
// 返回 (消息, 事件日志纯文本)。检测失败/无图时回退纯文本，不中断主流程。
// 目标模型不支持多模态时（DeepSeek 官方纯文本）历史直接存纯文本——绝不把
// image_url 写进对话历史（否则每轮透传触发 API 400）；模型层另有发送前兜底。
//
// assistant 消息：用 SplitReasoning 分离思维链——正文进 Content、思维链进
// ReasoningContent。DeepSeek thinking 模式要求 assistant 消息把 reasoning_content
// 原样回传（否则 API 400 "reasoning_content ... must be passed back"），
// 而事件日志纯文本只保留正文（思维链不入日志，展示层实时展示即可）。
func (rt *Runtime) buildHistoryMessage(role, content string) (types.Message, string) {
	if role == "assistant" {
		body, reasoning := SplitReasoning(content)
		return types.Message{Role: role, Content: body, ReasoningContent: reasoning}, body
	}
	if role != "user" || !rt.supportsVision() {
		return types.Message{Role: role, Content: content}, content
	}
	// 图片来源：文本路径扫描 + 暂存图片（GUI 上传结构化直传）。两者都取，
	// 但同一张图（同路径/同 DataURI）只保留一份——staged 优先（压缩版），
	// 否则同图两份（原图 + 压缩图）会同时进历史与请求（用户实锤双图）。
	var imgs []vision.Image
	if vision.HasImagePath(content) {
		detected, err := vision.DetectImages(content, rt.imageBaseDir())
		if err == nil {
			imgs = detected
		}
	}
	imgs = mergeImages(imgs, rt.PeekStagedImages())
	if len(imgs) == 0 {
		return types.Message{Role: role, Content: content}, content
	}
	return vision.BuildUserMessage(content, imgs), vision.EventText(content, imgs)
}

// supportsVision 报告当前配置是否启用多模态视觉输入（model.supports_vision）。
func (rt *Runtime) supportsVision() bool {
	return rt.Config != nil && rt.Config.ActiveModel().SupportsVision
}

// AddToHistoryForSession 把消息写入【指定会话】的历史（GUI 跨会话流式修复）。
// 背景：GUI 的 streamChat 是后台 goroutine，用户在生成期间切换会话后，
// AddToHistory 内部取 currentSessionID 会把回复写到【新】会话的事件日志
// （原会话丢失回复——"切换会话再返回丢聊天记录"根因之一）。
// 本方法绑定发起流时的会话 ID：
//   - sid 为空或 == 当前会话：走 AddToHistory（内存历史 + 事件日志，行为不变）；
//   - sid != 当前会话：只写事件日志（内存 conversationHistory 属于当前会话，
//     不能污染），用户切回 sid 时 LoadSession 会从事件日志恢复完整历史。
func (rt *Runtime) AddToHistoryForSession(sid, role, content string) {
	if sid == "" {
		rt.AddToHistory(role, content)
		return
	}
	rt.historyMu.RLock()
	cur := rt.currentSessionID
	rt.historyMu.RUnlock()
	if sid == cur {
		rt.AddToHistory(role, content)
		return
	}
	// 非当前会话：只持久化事件，不动内存历史
	content = StripThinking(content)
	_, eventText := rt.buildHistoryMessage(role, content)
	if rt.Memory != nil {
		eventType := "user_msg"
		if role == "assistant" {
			eventType = "assistant_msg"
		}
		_ = rt.Memory.AppendEvent(sid, &types.SessionEvent{
			Seq:       time.Now().UnixNano(),
			Timestamp: time.Now(),
			Type:      eventType,
			Data:      eventText,
		})
	}
}

// ContextUsage 返回当前对话上下文用量摘要（供 TUI/GUI 状态栏展示）。
// 格式: "ctx 0.6% (800/131k)"——精度 1 位小数、千 token 以下显示真实数值：
// 此前 int 截断在 131k 预算下几百 token 的增量恒为 0%、used/1000 恒为 0k，
// 导致对话/流式期间状态栏"纹丝不动"（GUI 状态栏不同步的根因之一）。
// 无预算时不显示。
// 注意：ctxBudget 指针可能在配置热更新（applyLiveConfig）时被整体替换，
// 必须在锁内取快照，解锁后只使用快照，避免与写者形成 data race。
func (rt *Runtime) ContextUsage() string {
	used, max := rt.ContextUsageInfo()
	if max <= 0 {
		return ""
	}
	pct := float64(used) / float64(max) * 100
	if pct > 99 {
		pct = 99
	}
	// 精度 2 位小数（%.2f）：大预算（如 max_context=1000000）下几千 token
	// 仅占 0.0x%——旧版 %.1f 会显示 "ctx 0.0%"，压缩前后无差异，用户
	// 看到状态栏"毫无变化"（本轮修复核心）。
	return fmt.Sprintf("ctx %.2f%% (%s/%s)", pct, fmtCtxTokens(used), fmtCtxTokens(max))
}

// ContextUsageInfo 返回上下文原始占用 (已用 token, 总预算)——GUI 状态栏
// 轮询（status.bar ctxRaw）与流式期间实时推送（ctx.usage 事件）共用，
// 前端据此精确渲染进度条与 token 数，不再依赖字符串解析。
func (rt *Runtime) ContextUsageInfo() (used, max int) {
	rt.historyMu.RLock()
	used = ctxmgr.MessagesTokens(rt.conversationHistory)
	budget := rt.ctxBudget
	if budget != nil {
		max = budget.MaxTokens
	}
	rt.historyMu.RUnlock()
	return used, max
}

// fmtCtxTokens 格式化 token 数：<1000 显示真实数值（800）；<10000 显示
// 1 位小数 k（12.5k）；否则整数 k（131k）。避免小增量被 k 截断成 0。
func fmtCtxTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%dk", n/1000)
	}
}

// ContextStats 返回上下文状态详情（供 /ctx 命令展示）。
// 含压缩审计日志（压缩了什么），解决"压缩信息丢失不可审计"问题。
func (rt *Runtime) ContextStats() string {
	rt.historyMu.RLock()
	n := len(rt.conversationHistory)
	used := ctxmgr.MessagesTokens(rt.conversationHistory)
	budget := rt.ctxBudget // 锁内快照：配置热更新会替换该指针，解锁后不得再直接读字段
	max := 0
	summary := ""
	var audit []string
	if budget != nil {
		max = budget.MaxTokens
		summary = budget.Summary
		audit = budget.AuditLog()
	}
	rt.historyMu.RUnlock()
	compact, hard, safety, keep, pct := 0.0, 0.0, 0.0, 0, 0.0
	if budget != nil {
		compact, hard, safety = budget.CompactRatio*100, budget.HardRatio*100, budget.SafetyRatio*100
		keep = budget.KeepRecent
		pct = budget.Usage(used) * 100
	}
	lines := []string{
		fmt.Sprintf("消息数: %d", n),
		fmt.Sprintf("估算用量: %d / %d tokens (%.0f%%)", used, max, pct),
		fmt.Sprintf("阈值: compact %.0f%% / hard %.0f%% / safety %.0f%%", compact, hard, safety),
		fmt.Sprintf("保留完整消息: %d 条", keep),
	}
	if summary != "" {
		lines = append(lines, "滚动摘要: "+truncateRunes(summary, 200))
	}
	if len(audit) > 0 {
		lines = append(lines, "--- 压缩审计（最近）---")
		for _, a := range audit {
			lines = append(lines, "• "+a)
		}
	} else {
		lines = append(lines, "压缩审计: 暂无（尚未触发压缩）")
	}
	return strings.Join(lines, "\n")
}

// persistCompactionSnapshot 将压缩后的对话历史快照写入会话 JSON，使重新加载时
// ctx 占比立即等于压缩后的状态，不再回退到未压缩的完整用量。
// 被压缩丢弃的原文完整保留在 JSONL 事件日志中，可通过 harness_search 检索找回。
// coverSeq 是本次快照内容已覆盖到的事件日志 Seq（由调用方传入刚写入的事件 Seq；
// 为 0 时自动读日志最后一条 Seq 兜底）——重新加载会话时按此 Seq 合并增量事件，
// 快照覆盖点之后的新对话不会丢失。
// 必须在 historyMu 已解锁、sid 非空时调用（已从锁内提取）。
func (rt *Runtime) persistCompactionSnapshot(sid string, coverSeq int64) {
	if rt.Memory == nil || sid == "" {
		return
	}
	session, err := rt.Memory.Load(sid)
	if err != nil {
		return
	}
	rt.historyMu.RLock()
	// 快照：压缩后的消息列表（深拷贝，避免与内存共享底层数组）
	// ⚠️ 图片 dataURI 防爆：单张可达 MB 级，几十张图会让 session.json 膨胀
	// 到几十 MB，之后每次 LoadSession 读盘+解析巨文件卡死（"压缩后历史
	// 记录不能加载"真实根因之一）。快照只用于恢复 AI 上下文，图片在
	// 事件日志已有 [图片: 路径] 锚点（重载时按路径重建），因此快照内
	// 图片统一降级为文本占位，不携带 dataURI。
	snapshot := make([]types.Message, len(rt.conversationHistory))
	for i, m := range rt.conversationHistory {
		if m.ImagePartCount() > 0 {
			if deg, ok := ctxmgr.StripImageDataURI(m); ok {
				snapshot[i] = deg
				continue
			}
		}
		snapshot[i] = m
	}
	session.CompactedMessages = snapshot
	if rt.ctxBudget != nil {
		session.CompactedSummary = rt.ctxBudget.Summary
	} else {
		session.CompactedSummary = ""
	}
	rt.historyMu.RUnlock()
	if coverSeq <= 0 {
		if seq, err := rt.Memory.LastEventSeq(sid); err == nil && seq > 0 {
			coverSeq = seq
		}
	}
	if coverSeq > session.CompactedSeq {
		session.CompactedSeq = coverSeq
	}
	_ = rt.Memory.Save(session)
}

// ResetContextSummary 清空滚动摘要（新会话时调用）
// 持锁保护：ctxBudget 指针可能被配置热更新整体替换，必须与写者互斥。
func (rt *Runtime) ResetContextSummary() {
	rt.historyMu.Lock()
	defer rt.historyMu.Unlock()
	if rt.ctxBudget != nil {
		rt.ctxBudget.ResetSummary()
	}
}

// GetConversationHistory 线程安全读取对话历史
func (rt *Runtime) GetConversationHistory() []types.Message {
	rt.historyMu.RLock()
	defer rt.historyMu.RUnlock()
	return rt.conversationHistory
}

// GetDisplayHistory 从 JSONL 事件日志加载会话完整历史用于展示（user/assistant），
// 与 AI 上下文 conversationHistory 解耦：后者在压缩持久化后是压缩快照
// （只含保留消息 + [历史摘要]），不适合作为 GUI 显示历史；事件日志始终完整。
// 行为与 TUI loadHistoryMessages 一致（识图消息经 eventToMessage 还原）。
func (rt *Runtime) GetDisplayHistory(sid string) []types.Message {
	if rt.Memory == nil || sid == "" {
		return nil
	}
	events, err := rt.Memory.LoadEvents(sid)
	if err != nil {
		return nil
	}
	var out []types.Message
	for _, evt := range events {
		if m := eventToMessage(evt); m != nil {
			out = append(out, *m)
		}
	}
	return out
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}

// ── Goal 模式（持久目标循环，借鉴业界同类 Agent 的持久目标循环）──

// GoalSession 返回当前目标会话（nil 表示无活动目标）。
func (rt *Runtime) GoalSession() *types.GoalSession {
	return rt.goalMgr.Session()
}

// GoalOnRoundEnd 每轮目标循环结束调用（TUI streamDoneMsg 时）。
// 提取 [GOAL_PLAN]/[GOAL_STEPS]/[GOAL_REFLECT]/[GOAL_DEFECTS]/[GOAL_LESSON]
// 协议标记，更新状态机并返回 (是否继续, 原因)。v2 自进化：反思/缺陷/经验/收敛/打磨。
func (rt *Runtime) GoalOnRoundEnd(response string, passed bool, score types.Score, report types.OverseerReport) (bool, string) {
	gs := rt.goalMgr.Session()
	if gs == nil || gs.Done() {
		return false, "无活动目标"
	}
	steps := extractGoalSteps(response)
	// spec 细化（spec 未就绪且尚无步骤清单）：每轮优先提取 [GOAL_SPEC] 跑确定性
	// SpecGate。未输出 spec → 走旧路径（不阻塞）；输出了但不通过 → 缺陷 + 继续
	// 细化（SetSpec 内部自动追加 d-spec 缺陷，continuation 由 goalmgr 生成）。
	specRejected := false
	if !gs.SpecReady && len(gs.Steps) == 0 {
		if spec := goalmgr.ExtractGoalSpec(response); spec != nil {
			ok, _ := rt.goalMgr.SetSpec(spec)
			if !ok {
				// spec 门槛不通过：剥离 [GOAL_PLAN]/[GOAL_DONE]，强制下一轮细化
				response = stripGoalPlan(response)
				response = stripGoalDone(response)
				passed = false // 细化未通过不允许算通过
				specRejected = true
			}
		}
	}
	// plan 轮（尚未有步骤）：提取步骤清单（[GOAL_PLAN] 标记）
	if gs.Round == 0 && len(gs.Steps) == 0 {
		if plan := extractGoalPlan(response); len(plan) > 0 {
			rt.goalMgr.SetPlan(plan)
		}
	}
	// v2 扩展协议解析
	refl := extractGoalReflect(response)
	fixReports := extractGoalDefects(response)
	lessons := extractGoalLessons(response)
	// 监工低分维度 → 自动缺陷条目（机制②）。spec 细化被拒的轮次跳过监工维度
	// 缺陷（细化拒绝本身就是缺陷，避免"功能完整性不足"与 d-spec 双重误报），
	// 改把 session 里已有的 d-spec 缺陷作为本轮缺陷传递（同 desc 自动去重，
	// 同时防止 goalmgr 的"监工评估未通过"兜底缺陷误报）。
	autoDefects := defectsFromReport(report, passed)
	if specRejected {
		autoDefects = nil
		for _, d := range gs.Defects {
			if d.ID == "d-spec" {
				autoDefects = append(autoDefects, types.GoalDefect{ID: d.ID, Desc: d.Desc, Status: "open"})
			}
		}
	}
	// 偏差检测命中 → 缺陷条目（对照 why-self-verification 一文：声明与证据对齐）。
	// OverseerEvaluate 已按偏差扣分；此处把命中原因转成可修复的缺陷清单，
	// 让下一轮"列出实际检查过的东西 / 补上外部验证证据"。
	rt.biasMu.Lock()
	biasHits := rt.lastBiasHits
	rt.lastBiasHits = nil
	rt.biasMu.Unlock()
	for _, h := range biasHits {
		autoDefects = append(autoDefects, types.GoalDefect{ID: h.DefectID, Desc: h.Desc, Status: "open"})
	}
	// 本轮工具证据清空（若 OverseerEvaluate 未调用，证据不能跨轮残留）
	rt.takeRoundEvidence()
	// 质量门禁（M5 接线，零配置兜底）：.contextloom/gates/ 存在才激活。
	// 门禁有牙齿：Fail/HumanReview → 本轮视为未通过（defects 强制入清单）、
	// 剥离 [GOAL_DONE]（不过不外显）——模型必须修复缺陷后再交付。
	if gs.ActivePhase() != types.GoalPhaseCheck { // 打磨轮本身即质量加固，门禁不重复拦
		gateRep, decision := rt.goalGateReport(passed, float64(score))
		// 质量账本（M5 持久化接线）：每轮裁决后把缺陷签名累计进
		// .contextloom/gates/quality_ledger.yaml 并当轮落盘（Pass 无缺陷可记 → 零磁盘写入；
		// 无 gates 目录 → 零操作，见 wiring.go recordGateVerdict）。
		rt.recordGateVerdict(gateRep, decision)
		if decision.ForbidDone() {
			passed = false // 门禁否决 → 参与缺陷清单与完成判定（禁止交付）
			response = stripGoalDone(response)
			for _, d := range selfcheckGateDefects(gateRep) {
				autoDefects = append(autoDefects, types.GoalDefect{ID: d.ID, Desc: d.Desc, Status: "open"})
			}
		}
	}
	tokens := int64(harness.EstimateTokens(response))
	// 可观测性：goal 轮次事件（评估分/是否通过/轮次号/停滞轮数），供离线归因
	if rt.Trace != nil {
		rt.Trace.Record(trace.Entry{
			Event:  "goal_round",
			Mode:   "goal",
			Round:  gs.Round + 1,
			Tokens: int(tokens),
			Detail: fmt.Sprintf("passed=%v score=%.0f stalled=%d defects=%d", passed, float64(score), gs.StalledRounds, len(gs.OpenDefects())),
		})
	}
	return rt.goalMgr.ApplyRoundResultEx(response, passed, float64(score), tokens, steps, refl, autoDefects, fixReports, lessons)
}

// defectsFromReport 监工四维低分（<80）自动转化为缺陷条目（机制②）。
// 已通过时不生成（无缺陷）；未通过时低分维度逐一转缺陷，供下一轮逐条修复。
func defectsFromReport(report types.OverseerReport, passed bool) []types.GoalDefect {
	if passed {
		return nil
	}
	var out []types.GoalDefect
	dimDefects := []struct {
		id    string
		dim   string
		score float64
		desc  string
	}{
		{"d-completeness", "功能完整性", report.Completeness, "功能完整性不足（" + fmt.Sprintf("%.0f", report.Completeness) + "/100）：产出未覆盖目标要求的全部功能点，请对照验收标准补全"},
		{"d-test", "测试覆盖", report.TestCoverage, "测试覆盖不足（" + fmt.Sprintf("%.0f", report.TestCoverage) + "/100）：代码任务缺少测试或未验证测试通过，请补充核心路径测试"},
		{"d-quality", "代码质量", report.CodeQuality, "代码质量不足（" + fmt.Sprintf("%.0f", report.CodeQuality) + "/100）：请检查代码健壮性、命名、结构并改进"},
		{"d-side-effect", "副作用", report.NoSideEffect, "存在副作用风险（" + fmt.Sprintf("%.0f", report.NoSideEffect) + "/100）：请检查对现有功能/数据/文档的影响并消除"},
	}
	for _, d := range dimDefects {
		if d.score < 80 {
			out = append(out, types.GoalDefect{ID: d.id, Desc: d.desc, Status: "open"})
		}
	}
	return out
}

// ProcessGoalRound 发起目标循环的下一轮（continuation 驱动，自动续跑）。
// 与 ProcessInput 的区别：不经过用户输入框，直接注入 [目标循环自动续跑] 指令。
func (rt *Runtime) ProcessGoalRound(ctx context.Context) (<-chan string, <-chan error, error) {
	gs := rt.goalMgr.Session()
	if gs == nil {
		return nil, nil, fmt.Errorf("无活动目标")
	}
	if gs.Done() {
		return nil, nil, fmt.Errorf("目标已终止（%s）", gs.Phase)
	}
	// 注入续跑指令到对话历史（模型可见的下一轮指令）
	rt.AddToHistory("user", gs.Continuation)
	// spec 未就绪的续跑轮仍用 SPEC directive（细化未过 → 继续产出 [GOAL_SPEC] 修复）
	directive := string(types.DirectiveCodeAgent)
	if gs.ActivePhase() == types.GoalPhaseSpec {
		directive = string(types.DirectiveSpec)
	}
	task := types.TaskCreate{
		Type:          "task.create",
		ID:            "main",
		Directive:     directive,
		Task:          gs.Goal,
		Payload:       gs,
		Mode:          "goal",
		ContextPolicy: types.PolicyFullContext,
		Priority:      1,
	}
	return rt.launchAgent(ctx, task, gs.Goal, "goal")
}

// GoalAbort 中止当前目标循环（保留会话与检查点，可审计）。
func (rt *Runtime) GoalAbort() string {
	gs := rt.goalMgr.Session()
	if gs == nil {
		return "无活动目标"
	}
	rt.goalMgr.Abort()
	return "目标循环已中止（检查点已保存，可用 /goal resume " + gs.ID + " 恢复）"
}

// GoalList 列出历史目标会话（按更新时间倒序）。
func (rt *Runtime) GoalList() string {
	sessions := rt.goalMgr.List()
	if len(sessions) == 0 {
		return "暂无目标历史"
	}
	var b strings.Builder
	for i, s := range sessions {
		status := string(s.Phase)
		if s.Aborted {
			status = "aborted"
		}
		fmt.Fprintf(&b, "%d. %s [%s] 轮%d/%d 步骤%d/%d %s\n",
			i+1, s.ID, status, s.Round, s.MaxRounds, s.DoneSteps(), len(s.Steps), truncateRunes(s.Goal, 40))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// GoalLoad 加载历史目标会话并切回 goal 模式（/goal resume <id>）。
func (rt *Runtime) GoalLoad(id string) (string, error) {
	s, err := rt.goalMgr.Load(id)
	if err != nil {
		return "", err
	}
	rt.SetMode("goal")
	return fmt.Sprintf("已恢复目标 %q（%s，阶段 %s，轮 %d/%d，%d/%d 步骤）",
		s.Goal, s.ID, s.Phase, s.Round, s.MaxRounds, s.DoneSteps(), len(s.Steps)), nil
}

// GoalStatus 格式化当前目标状态（/goal 命令展示）。
func (rt *Runtime) GoalStatus() string {
	gs := rt.goalMgr.Session()
	if gs == nil {
		return "当前无活动目标（/mode goal 后发送目标，或用 /goal resume <id> 恢复）"
	}
	var b strings.Builder
	status := "进行中"
	if gs.Done() {
		status = string(gs.Phase)
	}
	fmt.Fprintf(&b, "目标: %s\n状态: %s（阶段 %s，轮 %d/%d）\n",
		truncateRunes(gs.Goal, 80), status, gs.ActivePhase(), gs.Round, gs.MaxRounds)
	if gs.TokenBudget > 0 {
		fmt.Fprintf(&b, "token 预算: 已用 %d / %d\n", gs.TokenUsed, gs.TokenBudget)
	}
	b.WriteString("步骤:\n")
	if len(gs.Steps) == 0 {
		b.WriteString("  （尚未规划，等待 plan 轮产出步骤清单）\n")
	}
	for _, st := range gs.Steps {
		fmt.Fprintf(&b, "  [%s] %s (%s)\n", st.ID, truncateRunes(st.Desc, 60), st.Status)
	}
	// v2 自进化扩展展示（反思/缺陷/收敛/经验）
	if len(gs.Reflections) > 0 {
		r := gs.Reflections[0]
		fmt.Fprintf(&b, "最近反思(轮%d): %s\n", r.Round, truncateRunes(r.Summary, 60))
		if r.Problems != "" {
			fmt.Fprintf(&b, "  发现问题: %s\n", truncateRunes(r.Problems, 60))
		}
	}
	if open := gs.OpenDefects(); len(open) > 0 {
		b.WriteString("待修复缺陷:\n")
		for _, d := range open {
			fmt.Fprintf(&b, "  [%s] %s\n", d.ID, truncateRunes(d.Desc, 60))
		}
	}
	if gs.StalledRounds >= 2 {
		fmt.Fprintf(&b, "⚠ 已连续 %d 轮无实质进展（将强制换策略）\n", gs.StalledRounds)
	}
	if len(gs.Lessons) > 0 {
		fmt.Fprintf(&b, "经验档案: %d 条\n", len(gs.Lessons))
	}
	if len(gs.Checkpoints) > 0 {
		last := gs.Checkpoints[len(gs.Checkpoints)-1]
		note := "通过"
		if !last.Passed {
			note = "未通过"
		}
		fmt.Fprintf(&b, "最近检查点: 轮%d %s 评分%.0f %s\n", last.Round, last.Phase, last.Score, note)
	}
	if gs.Continuation != "" {
		fmt.Fprintf(&b, "续跑指令: %s\n", truncateRunes(gs.Continuation, 100))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// OverseerEvaluate 监工评估闭环（由 TUI 在 streamDoneMsg 时调用）。
// 基于真实响应构造四维评估报告（量纲 0-100，与 pass_threshold 一致），
// 返回 (是否通过, 加权总分, 完整报告) 供 TUI 展示。
// 注意：依赖 currentOverseer/lastAgentID 共享状态，TUI 串行请求安全；
// 并发请求（如 ACP 多连接）间会互相覆盖，属已知限制，需改为按请求隔离。
func (rt *Runtime) OverseerEvaluate(response string) (bool, types.Score, types.OverseerReport) {
	rt.overseerMu.RLock()
	ov := rt.currentOverseer
	agentID := rt.lastAgentID
	rt.overseerMu.RUnlock()
	if ov == nil {
		return true, types.Score(100), types.OverseerReport{}
	}
	report := buildOverseerReport(response)
	passed := ov.Evaluate(context.Background(), agentID, report)
	score := ov.CalculateScore(report)
	report.Score = score
	report.Passed = passed
	// 偏差检测（对照 why-self-verification 一文：模型声明 vs 外部证据对齐）。
	// goal 模式且 spec 未过门槛时早产 [GOAL_DONE] 被判定为偏差；
	// 正文声称"测试通过/全面检查"但本轮无工具证据 → 扣分。
	// 偏差命中记录进 rt 供 GoalOnRoundEnd 追加缺陷条目。
	if hits := rt.biasHits(response); len(hits) > 0 {
		penalty := overseer.TotalPenalty(hits)
		score = types.Score(float64(score) - penalty)
		if score < 0 {
			score = 0
		}
		report.Score = score
		if passed && penalty > 0 && float64(score) < float64(rt.Config.Overseer.PassThreshold) {
			passed = false
			report.Passed = false
		}
		rt.biasMu.Lock()
		rt.lastBiasHits = hits
		rt.biasMu.Unlock()
	} else {
		rt.biasMu.Lock()
		rt.lastBiasHits = nil
		rt.biasMu.Unlock()
	}
	return passed, score, report
}

// biasHits 对响应做偏差检测（goal 模式用 spec 门槛状态，其余模式 specReady=true）。
// 偏差检测只作用于 goal 模式（对照 why-self-verification 一文的目标链路），
// chat/task 模式沿用旧监工评分语义（不因"口述测试通过"额外扣分）。
// 工具证据来自本轮内存累加器（与 trace 开关解耦），取用后即清空。
func (rt *Runtime) biasHits(response string) []overseer.BiasHit {
	if rt.GetMode() != "goal" {
		return nil
	}
	ev := rt.takeRoundEvidence()
	specReady := true
	if gs := rt.goalMgr.Session(); gs != nil {
		specReady = gs.SpecReady || gs.Spec == nil
	}
	return overseer.BiasAnalyze(response, &ev, specReady)
}

// buildOverseerReport 基于模型响应构造四维评估报告（量纲 0-100）。
// 评分维度按任务类型动态调整：非代码任务（无代码块）不要求测试覆盖，
// 代码任务无测试才严重扣分。避免"删文件/配置"等正常任务因测试维度被误判。
func buildOverseerReport(response string) types.OverseerReport {
	// 防协议自报污染（机制②配套）：评估前剥离 Goal 协议标记与思考标记，
	// 评分只看真实正文产出——模型在 [GOAL_REFLECT]/[GOAL_DEFECTS] 里写
	// "单元测试"等字样不应算作测试覆盖证据，否则会被自报内容刷分。
	response = stripGoalProtocol(response)

	// 功能完整性：有实质内容（>30 字）即给高分，让代码质量成为区分维度
	completeness := 0.0
	switch n := len([]rune(strings.TrimSpace(response))); {
	case n > 30:
		completeness = 95
	case n > 0:
		completeness = 70
	}

	// 检测响应是否包含代码块（区分代码任务 vs 非代码任务）
	hasCode := strings.TrimSpace(extractCodeBlocks(response)) != ""

	// 测试覆盖：按任务类型动态评分
	// - 代码任务 + 提到测试通过 → 95（优秀）
	// - 代码任务 + 未提测试 → 55（代码任务应有测试，严重扣分）
	// - 非代码任务（删文件/配置/文档） → 95（不要求测试，不扣分）
	testCoverage := 95.0
	mentionsTest := false
	for _, kw := range []string{"测试通过", "PASS", "全部通过", "go test", "覆盖率", "单元测试"} {
		if strings.Contains(response, kw) {
			mentionsTest = true
			break
		}
	}
	if hasCode && !mentionsTest {
		testCoverage = 55 // 代码任务缺测试，严重扣分
	}

	// 代码质量：非代码任务给中性高分（不扣分），代码任务用 CodeQualityChecker 实测
	codeQuality := 90.0
	if hasCode {
		codeQuality = overseer.NewCodeQualityChecker().Analyze(extractCodeBlocks(response)).Score * 100
	}

	return types.OverseerReport{
		NodeName:     "main",
		Completeness: completeness,
		TestCoverage: testCoverage,
		CodeQuality:  codeQuality,
		NoSideEffect: 92,
		Summary:      truncateRunes(response, 100),
	}
}

// extractCodeBlocks 提取响应中所有 ``` 代码块内容并合并，剥离首行语言标识（如 go/python）
// stripGoalProtocol 从响应中剥离 Goal 模式协议标记（[GOAL_PLAN]/[GOAL_STEPS]/
// [GOAL_REFLECT]/[GOAL_DEFECTS]/[GOAL_LESSON] 成对标记 + [GOAL_DONE]/
// [GOAL_POLISH_DONE] 单标记），供监工评分使用——评分只看真实正文，
// 不受模型自报的协议内容影响（机制②配套：防自报刷分）。
func stripGoalProtocol(s string) string {
	tags := []string{"GOAL_PLAN", "GOAL_STEPS", "GOAL_REFLECT", "GOAL_DEFECTS", "GOAL_LESSON"}
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
	return strings.TrimSpace(out)
}

func extractCodeBlocks(s string) string {
	var blocks []string
	rest := s
	for {
		start := strings.Index(rest, "```")
		if start < 0 {
			break
		}
		rest = rest[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			break
		}
		block := rest[:end]
		// 剥离首行语言标识（纯字母/+/#/- 组成，如 "go"、"c++"、"c#"）
		if nl := strings.IndexByte(block, '\n'); nl >= 0 {
			if isLangIdentifier(strings.TrimSpace(block[:nl])) {
				block = block[nl+1:]
			}
		} else if isLangIdentifier(strings.TrimSpace(block)) {
			block = ""
		}
		if strings.TrimSpace(block) != "" {
			blocks = append(blocks, block)
		}
		rest = rest[end+3:]
	}
	return strings.Join(blocks, "\n")
}
func (rt *Runtime) HasOverseer() bool {
	rt.overseerMu.RLock()
	defer rt.overseerMu.RUnlock()
	return rt.currentOverseer != nil
}

// SpawnTask 实现 tools.TaskSpawner：模型自主调用 spawn_task 时触发，
// 创建子 Agent（继承父对话上下文 + 自动批准，§9.4 上下文继承），
// 注入 ContextManifest（§5.1 入口契约），同步执行后将结果与监工评分返回给主 Agent。
func (rt *Runtime) SpawnTask(ctx context.Context, parentID string, spec tools.TaskSpec) (string, error) {
	rt.childMu.Lock()
	rt.childCounter++
	num := rt.childCounter
	rt.childMu.Unlock()
	childID := fmt.Sprintf("sub-%d", num)

	// 子任务默认指令；员工命中时（M3 接线）用员工档案约束
	directive := spec.Directive
	if directive == "" {
		directive = "CODE_AGENT"
	}
	// 员工中心接线（零配置兜底）：directive 命中员工（精确名或通用指令映射，
	// 如 CODE_AGENT→coder、TEST→tester、PLANNER→product）时，
	// 按员工 role/persona 定 directive，并按其工具权限与模型绑定装配子 Agent。
	// 注：schema 教模型传通用指令，历史上只有精确员工名才装配——通用指令从未
	// 触发员工约束（面板显示员工≠员工真的干活）；此处用 resolveEmployeeForDirective
	// 让通用指令同样装配到对应员工（仅当员工中心真实存在该员工，零配置兜底不变）。
	var emp *orchestrator.Employee
	// 员工深入本体装配（M3+）：职务角色进入 directive（行为约束），
	// 深入本体能力（专精/工作流/约束/经验/产出格式/工作区蒸馏文档）
	// 追加进 Manifest.Summary（注入子 Agent 上下文基线）——把"几个提示词"
	// 升级为"可蒸馏的本体能力库"。
	var deepProfile string
	if emp = rt.resolveEmployeeForDirective(directive); emp != nil {
		directive = emp.Role
		if emp.Persona != "" {
			directive = directive + "（人设：" + emp.Persona + "）"
		}
		deepProfile = rt.employeeDeepProfile(emp)
	}

	task := types.TaskCreate{
		Type:          "task.create",
		ID:            childID,
		Directive:     directive,
		Task:          spec.Task,
		ContextPolicy: types.PolicyMinimal, // 子 Agent 最小上下文，靠 manifest 补充
		Priority:      1,
		ParentGoal:    spec.Task,
		Manifest: &types.ContextManifest{
			WorkDir:       rt.Config.Project.Root,
			ProjectRoot:   rt.Config.Project.Root,
			Summary:       spec.Context,
			MaxIterations: 15, // 兼容字段：已无硬上限，仅保留配置结构（任务执行由完成/中断/token红线/死循环检测决定）
			MaxTokens:     8000,
		},
	}

	// 乙方本体能力注入：准确渲染进 Manifest.Summary 的上下文基线尾部，
	// 确保注入发生在 sub-agent 入口契约（§上下文基线）组装之前。
	if deepProfile != "" {
		base := task.Manifest.Summary
		if base != "" {
			base += "\n\n"
		}
		task.Manifest.Summary = base + deepProfile
	}

	// 子 Agent 用过滤后的工具表（移除 spawn_task/handoff 防递归）；
	// 员工命中时进一步按其 allow/deny 过滤（M3：无限制员工 = 默认全表）
	filteredTools := rt.Tools.CloneWithout("spawn_task", "handoff")
	backendReq := modelgateway.Req{Task: "spawn_task", Role: spec.Directive}
	if emp != nil {
		policy, perr := emp.ResolvePolicy()
		if perr == nil {
			filteredTools = rt.employeeTools(emp)
			if policy.ModelPreset != "" {
				backendReq.Task = "spawn_task:" + emp.Name
				backendReq.Role = policy.ModelPreset
			}
		}
	}
	agent := rt.Executor.SpawnAgent(childID, task, rt.selectBackend(ctx, backendReq))
	agent.SetTools(filteredTools)
	agent.SetTracer(rt)
	agent.SetDepth(1)
	// 子 Agent 归属父会话：检索/压缩入索引按会话隔离，防跨会话串扰
	agent.SetSessionID(rt.GetSessionID())
	agent.SetPersistencePaths(rt.PersistencePaths()) // 子 Agent 也注入持久化路径
	// 子 Agent 继承父会话上下文（预算压缩由 buildMessagesWithManifest 的 compactHistoryForTask 保护），
	// 使质检/子任务能理解 chat 中已讨论的背景，而非仅凭代码仓库盲干
	agent.SetConversationHistory(rt.GetConversationHistory())
	// 子 Agent 检查点（runID=子任务 ID，独立 run 文件）。
	// 减容：子任务从不恢复检查点（restoreFromCheckpoint 仅主任务调用），
	// 因此不落 Messages（消息体是纯死数据，历史膨胀主源）；任务完成后
	// 在下文 SpawnTask 尾部 Delete 该 run 文件，避免 run-sub-* 无限积累。
	rt.attachCheckpointSink(agent, childID, rt.GetSessionID(), "task", false)
	// task 子 Agent 开启自动批准：跳过权限确认弹窗，实现全自动执行。
	agent.SetAutoApprove(true)

	// ── 活动式超时（替代固定超时）──
	// 子 Agent 持续输出（token 到达）时不设限；仅当连续 noOut 时长无任何输出
	// 才开始计时，无输出超时则终止；期间一旦有输出，计时作废重置。
	// 固定超时会误杀长任务（如长时间跑测试/下载），活动式只在"卡死"时兜底。
	noOut := rt.childNoOutputTimeout
	if noOut <= 0 {
		noOut = 10 * time.Minute
	}
	actCtx, actCancel := context.WithCancel(ctx)
	defer actCancel()
	var timeoutFired atomic.Bool
	timer := time.AfterFunc(noOut, func() {
		if timeoutFired.CompareAndSwap(false, true) {
			actCancel() // 终止子 Agent（RunStreamWithBudget 感知 ctx.Done 停止）
		}
	})
	defer timer.Stop()

	// 用 RunStreamWithBudget：工具执行无轮次硬上限（参数仅保留兼容），
	// 终止由 ctx 中断（含活动式超时）/ token 预算红线（压缩后仍超限）/ detectLoop（同参3次）
	// / 模型主动停手（无工具调用）决定——任务该做多久做多久。
	maxIter := task.Manifest.MaxIterations
	if maxIter <= 0 {
		maxIter = 15
	}
	ch, errCh, err := agent.RunStreamWithBudget(actCtx, maxIter)
	if err != nil {
		rt.Executor.RemoveAgent(childID)
		tools.EmitTaskFail(ctx, childID, err)
		return "", fmt.Errorf("子 Agent 启动失败: %w", err)
	}

	// 发送子任务启动信号（TUI 创建面板项）
	// 显示名优先级：真实命中的员工名 > 通用指令纯显示映射（CODE_AGENT→coder 等）
	// > 空串（TUI 回退显示 sub-1）。
	// 注：装配（role/persona/工具表/模型绑定）由上方 resolveEmployeeForDirective
	// 完成——通用指令也会真实装配到员工（非仅显示），本处只决定面板怎么显示。
	empName := ""
	if emp != nil {
		empName = emp.Name
	} else {
		empName = rt.displayEmployeeForDirective(directive)
	}
	// §START 的 directive 段显示原始通用指令（CODE_AGENT 等，面板保持简洁）；
	// 装配替换后的 role+persona 只进 TaskCreate.Directive（模型侧行为约束），不进面板。
	displayDir := spec.Directive
	if displayDir == "" {
		displayDir = "CODE_AGENT"
	}
	tools.EmitTaskStart(ctx, childID, displayDir, empName, spec.Task)

	var output strings.Builder
	for token := range ch {
		output.WriteString(token)
		// 有输出 → 活动式超时计时作废重置（Stop+Reset：若已触发则忽略，边界 10 分钟粒度可接受）
		timer.Stop()
		timer.Reset(noOut)
		// 转发子 Agent token 到 TUI 面板（非阻塞，channel 满则丢弃）
		tools.EmitTaskToken(ctx, childID, token)
	}
	if e := <-errCh; e != nil {
		rt.Executor.RemoveAgent(childID)
		// 减容：子任务检查点只写不读（SpawnTask 从不恢复），失败即删，
		// 防止 run-sub-* 无限累积成缓存膨胀源
		if rt.checkpoints != nil {
			_ = rt.checkpoints.Delete(childID)
		}
		if timeoutFired.Load() {
			msg := fmt.Sprintf("子任务无输出超过 %s，已超时终止（子任务持续输出时不设限，有输出即重置计时）", noOut)
			tools.EmitTaskFail(ctx, childID, fmt.Errorf("%s", msg))
			return "", fmt.Errorf("%s", msg)
		}
		tools.EmitTaskFail(ctx, childID, e)
		return "", fmt.Errorf("子 Agent 执行失败: %w", e)
	}
	rt.Executor.RemoveAgent(childID)

	// 减容：子任务检查点只写不读（SpawnTask 从不恢复），完成即删，
	// 防止 run-sub-* 无限累积成缓存膨胀源（历史 runID 无限递增）
	if rt.checkpoints != nil {
		_ = rt.checkpoints.Delete(childID)
	}

	// 监工评估子任务结果（§9.4 四维评分 + §9.6 防自我欺骗）
	// 直接用 childID 关联，绕过 OverseerEvaluate 的共享 lastAgentID（后者始终指向 main）
	result := output.String()
	rt.overseerMu.RLock()
	ov := rt.currentOverseer
	rt.overseerMu.RUnlock()
	if ov != nil {
		report := buildOverseerReport(result)
		// §9.6 防自我欺骗：监工独立沙箱执行测试，对比子 Agent 声称的结果
		// （受 overseer.test_tee 开关控制）
		claimed, actual := verifyTestExecution(ctx, result, rt.Config.Overseer.TestTEE)
		if claimed != nil || actual != nil {
			report.ClaimedResult = claimed
			report.ActualResult = actual
		}
		// §9.4 复盘节点：注册并更新节点状态
		ov.RegisterNode(childID, spec.Task)
		passed := ov.Evaluate(ctx, childID, report)
		score := ov.CalculateScore(report)
		ov.UpdateNodeStatus(childID, passed)
		if passed {
			result += fmt.Sprintf("\n\n✅ 监工评估通过 (评分 %.0f/100)", score)
		} else {
			result += fmt.Sprintf("\n\n⚠ 监工评估未通过 (评分 %.0f/100)，结果仅供参考，建议换方式重试", score)
		}
		// 发送完成信号（TUI 更新面板状态为 done + 评分）
		tools.EmitTaskDone(ctx, childID, float64(score))
	} else {
		// 无监工：仍发送完成信号（评分 0，表示未评估）
		tools.EmitTaskDone(ctx, childID, 0)
	}
	// 本体能力自动蒸馏（M3+ 自动化闭环）：
	// 子任务命中员工 → 后台把本次产出蒸馏成经验条目沉淀进该员工
	// workspace/ONTOLOGY.md，下一次指派该员工时随本体注入复用。
	// 异步 + 失败静默降级：绝不影响子任务返回，绝不阻塞主流程。
	if emp != nil {
		empCopy := *emp
		distillTask := spec.Task
		distillOut := result
		go func() {
			rt.distillEmployee(&empCopy, distillTask, distillOut)
		}()
	}
	return result, nil
}

// ────────────────────────────────────────────────────────────────
// GUI 服务层支持方法（供 internal/gui 的 WS 方法契约调用）
// ────────────────────────────────────────────────────────────────

// DeleteSession 删除指定会话（GUI chat.deleteSession）。
// 允许删除当前活跃会话（"删除最后一个会话后不自动新建"的用户需求）：
// 删除成功后若被删的是当前会话，回到无会话状态——currentSessionID 清空、
// 内存历史一并清空（该内存历史本就属于被删会话）。前端据此在删除最后一个
// 会话后进入空状态；GUI 发送消息时若发现无当前会话会自动新建（chat.send）。
func (rt *Runtime) DeleteSession(uuid string) error {
	if uuid == "" {
		return fmt.Errorf("uuid 不能为空")
	}
	if err := rt.Memory.Delete(uuid); err != nil {
		return err
	}
	rt.historyMu.Lock()
	if rt.currentSessionID == uuid {
		rt.currentSessionID = ""
		rt.conversationHistory = nil
		// 目标状态机同步解绑（被删会话的活动目标保留磁盘，可 /goal resume）
		rt.goalMgr.SetCurrent("")
	}
	// 清理被删会话的模式记录（防 map 泄漏）
	if rt.modeBySID != nil {
		delete(rt.modeBySID, uuid)
	}
	rt.historyMu.Unlock()
	return nil
}

// RenameSession 重命名会话标题（GUI chat.renameSession）。
func (rt *Runtime) RenameSession(uuid, title string) error {
	if uuid == "" {
		return fmt.Errorf("uuid 不能为空")
	}
	sess, err := rt.Memory.Load(uuid)
	if err != nil {
		return fmt.Errorf("会话不存在: %w", err)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("标题不能为空")
	}
	sess.Title = title
	sess.UpdatedAt = time.Now()
	return rt.Memory.Save(sess)
}

// ProjectRoot 返回项目根目录绝对路径（GUI 文件系统操作的安全边界）。
func (rt *Runtime) ProjectRoot() string {
	if rt.Config == nil || rt.Config.Project.Root == "" {
		return "."
	}
	return rt.Config.Project.Root
}

// ModelInfo 返回当前模型结构化信息（GUI model.info）。
func (rt *Runtime) ModelInfo() map[string]any {
	// backend 返回当前激活预设名（用户可辨识的模型名），而非 provider 类型；
	// 实际 provider 类型经 ActiveModel().Backend 透传，避免切换后状态栏不变。
	backend := rt.Config.ActiveModelName
	if backend == "" {
		backend = "default-model"
	}
	am := rt.Config.ActiveModel()
	modelID := am.Backend
	if rt.Backend != nil && rt.Backend.ModelID() != "" {
		modelID = rt.Backend.ModelID()
	}
	effort := rt.Config.CLIDefaults.Effort
	if effort == "" {
		effort = "max"
	}
	maxCtx := rt.Config.ActiveModel().MaxContext
	if rt.Backend != nil && rt.Backend.MaxContext() > 0 {
		maxCtx = rt.Backend.MaxContext()
	}
	return map[string]any{
		"backend":     backend,
		"provider":    am.Backend,
		"model":       modelID,
		"visionModel": rt.Config.ActiveModel().VisionModel, // 识图模型（空=不切换）
		"effort":      effort,
		"maxContext":  maxCtx,
	}
}

// GoalStatusDetail 返回结构化目标状态（GUI goal.status）。
func (rt *Runtime) GoalStatusDetail() map[string]any {
	gs := rt.GoalSession()
	if gs == nil || gs.Done() {
		return map[string]any{
			"active": false, "phase": "", "round": 0,
			"totalSteps": 0, "doneSteps": 0, "pendingSteps": []string{},
			"score": 0.0, "passed": false,
		}
	}
	steps := make([]map[string]any, 0, len(gs.Steps))
	for _, st := range gs.Steps {
		steps = append(steps, map[string]any{
			"id": st.ID, "desc": st.Desc, "status": st.Status, "evidence": st.Evidence,
		})
	}
	pending := make([]string, 0)
	for _, st := range gs.PendingSteps() {
		pending = append(pending, st.Desc)
	}
	score := 0.0
	passed := false
	if len(gs.Checkpoints) > 0 {
		cp := gs.Checkpoints[len(gs.Checkpoints)-1]
		score = cp.Score
		passed = cp.Passed
	}
	// v2 自进化扩展（GUI goal 面板展示）
	defects := make([]map[string]any, 0)
	for _, d := range gs.OpenDefects() {
		defects = append(defects, map[string]any{"id": d.ID, "desc": d.Desc})
	}
	reflections := make([]map[string]any, 0, min(len(gs.Reflections), 3))
	for _, r := range gs.Reflections {
		if len(reflections) >= 3 {
			break
		}
		reflections = append(reflections, map[string]any{"round": r.Round, "summary": r.Summary, "problems": r.Problems, "next_plan": r.NextPlan})
	}
	return map[string]any{
		"active": true, "phase": string(gs.ActivePhase()), "round": gs.Round,
		"maxRounds": gs.MaxRounds, "totalSteps": len(gs.Steps),
		"doneSteps": gs.DoneSteps(), "pendingSteps": pending,
		"steps": steps, "score": score, "passed": passed,
		"goal":    gs.Goal,
		"defects": defects, "reflections": reflections,
		"stalledRounds": gs.StalledRounds, "lessons": gs.Lessons,
	}
}
