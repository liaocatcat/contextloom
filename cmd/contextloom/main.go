package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/acp"
	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/envclean"
	"github.com/contextloom/contextloom-agent/internal/eval"
	"github.com/contextloom/contextloom-agent/internal/executor"
	"github.com/contextloom/contextloom-agent/internal/gui"
	"github.com/contextloom/contextloom-agent/internal/mcpserver"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/runtime"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/internal/trace"
	"github.com/contextloom/contextloom-agent/internal/tui"
	"github.com/contextloom/contextloom-agent/pkg/types"
	"github.com/contextloom/contextloom-agent/pkg/version"
)

func main() {
	// 环境卫生：macOS 下清除 MallocStackLogging 家族变量，杜绝 Go 进程
	// 退出时 libsystem_malloc 打印 "can't turn off malloc stack logging"
	// 噪音反复污染 TUI/GUI 终端画面（详见 internal/envclean 包注释）。
	// 必须最早执行：此后所有 os.Environ() 派生的子进程都不会再继承该变量。
	envclean.StripMallocDebug()

	// 子命令分发：contextloom eval / contextloom trace（独立 FlagSet，不影响主参数解析与测试）
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "eval":
			os.Exit(runEvalCmd(os.Args[2:]))
		case "eval-retrieval":
			os.Exit(runRetrievalEvalCmd(os.Args[2:]))
		case "trace":
			os.Exit(runTraceCmd(os.Args[2:]))
		}
	}

	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		// -h/--help：usage 已在 parseFlags 内打印，正常退出
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "参数解析失败: %v\n", err)
		os.Exit(2)
	}

	if opts.showVersion {
		fmt.Printf("contextloom v%s\n", version.Version)
		return
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "无法加载配置: %v\n", err)
		os.Exit(1)
	}
	// 配置来源可见性：stderr 打印实际使用的配置文件路径（配置错位一眼可见；
	// GUI_ADDR 等 stdout 契约不受影响）。TUI 模式下该行打印于 TUI 初始化之前。
	if cfg.ConfigPath != "" {
		fmt.Fprintf(os.Stderr, "[config] 使用配置文件: %s\n", cfg.ConfigPath)
	} else {
		fmt.Fprintf(os.Stderr, "[config] 未找到配置文件，使用内置默认配置\n")
	}
	if err := cfg.Validate(); err != nil {
		// --serve 模式（GUI 启动器拉起的 headless 服务）降级为警告继续启动：
		// GUI 必须先能打开，才能引导用户配置；TUI/MCP 等交互模式保持严格校验。
		if opts.guiServe {
			fmt.Fprintf(os.Stderr, "警告: 配置校验未通过（GUI 服务仍启动，请配置 config.yaml 后重启）: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "配置无效: %v\n", err)
			os.Exit(1)
		}
	}

	if opts.modelID != "" {
		// --model 按预设名激活（大小写不敏感；找不到报错提示可用列表）
		found := false
		for i := range cfg.Models {
			if strings.EqualFold(strings.TrimSpace(cfg.Models[i].Name), strings.TrimSpace(opts.modelID)) {
				cfg.ActiveModelName = cfg.Models[i].Name
				found = true
				break
			}
		}
		if !found {
			fmt.Fprintf(os.Stderr, "--model %q 未找到，可用预设: %s\n", opts.modelID, cfg.ModelNames())
			os.Exit(1)
		}
	}

	// 项目根锚定到启动工作目录（CWD），全部模式统一。
	// 背景：config.yaml 跟随可执行文件目录（默认），其中的显式 project.root
	// 会把 .contextloom/ 记忆/会话锚定到配置目录——而用户的实际项目是
	// 【启动时所在目录】。
	// 历史缺陷（用户实测 bug3 复现）：二进制目录的 config.yaml 指定
	// project.root 时，serve 模式早已强制 CWD（GUI 切换项目契约），但
	// TUI/CLI 记忆命令（--memory-load/list/delete、TUI resume）仍尊重
	// 配置 root → 从项目目录运行却读配置目录的记忆/会话 → 加载历史会话
	// 报 "no such file or directory"（"加载对话失败"）。
	// 设计语义（README）：项目目录 = 命令启动时所在目录，.contextloom/ 数据
	// 落于此；config.yaml 仅提供模型/工具等全局设置，同一份配置服务多个
	// 项目。故此处对【所有模式】强制 project.root = CWD。
	// 注：仅当 CWD 可用且非根目录 "/" 时覆盖（"/" 不是项目，mkdir /.contextloom
	// 会因只读失败——用户实测 "加载对话失败" 的另一触发）。
	overrideProjectRootFromCWD(cfg)

	switch {
	case opts.acpOnly:
		runACPServer(cfg, opts.acpPort)
	case opts.mcpServer:
		runMCPServer(cfg)
	case opts.guiServe:
		runGUIServer(cfg, opts.guiPort)
	case opts.memoryList:
		runMemoryList(cfg)
	case opts.memoryLoad != "":
		runMemoryLoad(cfg, opts.memoryLoad)
	case opts.memoryDelete != "":
		runMemoryDelete(cfg, opts.memoryDelete)
	default:
		runTUI(cfg, opts.resume, opts.memoryLoad)
	}
}

// runGUIServer 以 headless 服务模式启动（供 contextloom-gui 启动器拉起）：
//   - 无 TUI，仅启动 Runtime + internal/gui HTTP/WS 服务（127.0.0.1:port + token）
//   - TUI 启动方式不受影响，二者可并行（存储为纯文件无锁，天然支持并发）
//   - 启动后打印访问地址（GUI 启动器解析 stdout 获取端口与 token）
func overrideProjectRootFromCWD(cfg *config.Config) {
	wd, err := os.Getwd()
	if err != nil {
		return
	}
	// 防御：根目录 "/" 不是项目（mkdir /.contextloom 只读失败）——不覆盖，
	// 保留配置 root（若同样无效由 runtime 初始化报错，但至少不静默写根目录）。
	if filepath.Clean(wd) == string(filepath.Separator) {
		return
	}
	cfg.Project.Root = wd
}

func runGUIServer(cfg *config.Config, port int) {
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		rt.CloseMCPServers()
		rt.CloseChannels()
		rt.CloseSandbox()
		rt.CloseHarness()     // Harness 检索索引最后落盘（压缩原文跨进程找回）
		rt.CloseTraceExport() // OTLP 导出器收尾冲刷（先于 JSONL Close，flusher 双保险）
		if rt.Trace != nil {
			rt.Trace.Close()
		}
	}()
	srv := gui.NewServer(rt, port, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// 异步启动：监听就绪后打印访问地址供启动器解析
	// （格式固定：GUI_ADDR=http://127.0.0.1:PORT/?token=TOKEN）
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	select {
	case <-srv.Ready():
		fmt.Printf("GUI_ADDR=http://127.0.0.1:%d/?token=%s\n", srv.Port(), srv.Token())
	case err := <-done:
		fmt.Fprintf(os.Stderr, "GUI 服务启动失败: %v\n", err)
		os.Exit(1)
	}
	if err := <-done; err != nil {
		fmt.Fprintf(os.Stderr, "GUI 服务异常: %v\n", err)
		os.Exit(1)
	}
}

// runMCPServer 以 MCP Server 模式启动（stdio JSON-RPC，只读暴露
// Harness 检索/记忆/技能）。供 Claude Code/Cursor 等外部 AI 工具调用
// ContextLoom 的上下文服务——"Server 优先，Client 后置"（竞品分析结论）。
func runMCPServer(cfg *config.Config) {
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	srv := mcpserver.NewServer(rt)
	if err := srv.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "MCP Server 异常: %v\n", err)
		os.Exit(1)
	}
}

// options 保存全部命令行参数
type options struct {
	modelID      string
	resume       bool
	memoryLoad   string
	memoryList   bool
	memoryDelete string
	acpOnly      bool
	acpPort      int
	mcpServer    bool
	guiServe     bool
	guiPort      int
	configPath   string
	showVersion  bool
}

// parseFlags 解析命令行参数，使用独立 FlagSet 以便单元测试
func parseFlags(args []string) (*options, error) {
	opts := &options{acpPort: 20250}
	fs := flag.NewFlagSet("contextloom", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // 错误由调用方处理
	fs.StringVar(&opts.modelID, "model", "", "指定启动时使用的模型")
	fs.BoolVar(&opts.resume, "resume", false, "自动加载上次未完成的会话")
	fs.StringVar(&opts.memoryLoad, "memory-load", "", "加载指定历史记忆并启动交互")
	fs.BoolVar(&opts.memoryList, "memory-list", false, "打印所有历史记忆摘要并退出")
	fs.StringVar(&opts.memoryDelete, "memory-delete", "", "删除指定记忆")
	fs.BoolVar(&opts.acpOnly, "acp", false, "仅启动 ACP 服务（无 TUI）")
	fs.IntVar(&opts.acpPort, "acp-port", 20250, "指定 ACP 端口")
	fs.BoolVar(&opts.mcpServer, "mcp-server", false, "以 MCP Server 模式启动（stdio JSON-RPC，只读暴露 Harness/记忆/技能）")
	fs.BoolVar(&opts.guiServe, "serve", false, "以 headless 服务模式启动（供 contextloom-gui 启动器拉起，无 TUI）")
	fs.IntVar(&opts.guiPort, "serve-port", 0, "GUI 服务端口（0=自动选择空闲端口）")
	fs.StringVar(&opts.configPath, "config", "", "指定配置文件路径")
	fs.BoolVar(&opts.showVersion, "version", false, "显示版本信息")
	// 自定义 usage：SetOutput(io.Discard) 会吞掉 flag 包默认输出，需显式打印到 stdout
	fs.Usage = printUsage
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return opts, nil
}

// printUsage 打印 CLI 用法说明（README 文档中的命令行参数）
func printUsage() {
	fmt.Fprintf(os.Stdout, `用法: contextloom [选项]

选项:
  --model <id>           指定启动时使用的模型
  --resume               自动加载上次未完成的会话
  --memory-load <uuid>   加载指定历史记忆并启动交互
  --memory-list          打印所有历史记忆摘要并退出
  --memory-delete <uuid> 删除指定记忆
  --acp                  仅启动 ACP 服务（无 TUI）
  --acp-port <port>      指定 ACP 端口（默认 20250）
  --mcp-server           以 MCP Server 模式启动（stdio JSON-RPC，只读暴露 Harness/记忆/技能）
  --serve                以 headless 服务模式启动（供 contextloom-gui 拉起，无 TUI）
  --serve-port <port>    指定 GUI 服务端口（0=自动选择，默认 0）
  --config <path>        指定配置文件路径
  --version              显示版本信息
  -h, --help             显示本帮助并退出

子命令:
  eval [--file eval.yaml] [--json out.json] [--dry-run|--real]  离线可复现评估
  eval-retrieval [--file retrieval.yaml] [--json out.json]      检索质量评测（Recall@K/MRR）
  trace [--tail N] [--dir 项目目录]                             查看轻量 trace 记录
`)
}

func runACPServer(cfg *config.Config, port int) {
	if port != 0 {
		cfg.ACP.Port = port
	}
	cfg.ACP.Enabled = true

	// 初始化 runtime（标准 ACP 会话桥接到完整 ContextLoom 能力）
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ACP 服务初始化失败: %v\n", err)
		os.Exit(1)
	}
	acpSrv := acp.NewServer(cfg.ACP.Port)
	// 审批策略映射：permissive → quiet（自动批准）/ standard / strict
	policy := cfg.Tools.ApprovalMode
	switch policy {
	case "permissive":
		policy = "quiet"
	case "", "standard":
		policy = "standard"
	case "strict":
		policy = "strict"
	}
	h := acp.NewRuntimeHandler(acp.HandlerDeps{
		Spawn:            rt.Executor.SpawnAgent,
		Tools:            rt.Tools,
		Backend:          rt.Backend,
		PersistencePaths: rt.PersistencePaths,
		MaxContext:       cfg.ActiveModel().MaxContext,
		ModelID:          cfg.ActiveModel().Backend,
		Tracer:           rt,
	}, acpSrv, policy, true)
	acpSrv.SetHandler(h)
	fmt.Printf("ACP 服务（agent-client-protocol 1.0）启动于端口 %d，审批策略: %s\n", cfg.ACP.Port, policy)
	if err := acpSrv.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "ACP 服务异常: %v\n", err)
		os.Exit(1)
	}
	rt.CloseHarness() // 退出兜底：Harness 检索索引最后落盘
}

func runMemoryList(cfg *config.Config) {
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	sessions := rt.ListSessions()
	if len(sessions) == 0 {
		fmt.Println("无历史会话")
		return
	}
	// ListSessions 已按 UpdatedAt 倒序返回（最近活跃在前）
	for _, s := range sessions {
		preview := s.Preview
		if preview == "" {
			preview = s.Title
		}
		if len([]rune(preview)) > 60 {
			preview = string([]rune(preview)[:60]) + "…"
		}
		fmt.Printf("%s  %s  %s\n", s.UUID, s.UpdatedAt.Format("2006-01-02 15:04"), preview)
	}
}

func runMemoryLoad(cfg *config.Config, uuid string) {
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	_, session, err := rt.LoadSession(uuid)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载会话失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("已加载会话: %s (%s)\n", session.Title, session.UUID)

	// 加载后进入 TUI 交互
	// 初始化工具谨慎性分析器（按审批预设映射）
	//   permissive → quiet（自动执行）
	//   standard  → standard（默认，敏感操作确认）
	//   strict    → strict（所有写操作和命令确认）
	caMode := tools.ModeStandard
	switch cfg.Tools.ApprovalMode {
	case "permissive":
		caMode = tools.ModeQuiet
	case "strict":
		caMode = tools.ModeStrict
	}
	ca := tools.NewCautiousAnalyzer(caMode)
	confirmFn, reqCh, respCh := tui.MakeConfirmFunc()
	rt.Tools.SetCautious(ca)
	rt.Tools.SetConfirmFunc(confirmFn)

	app := &tui.App{
		Runtime:       rt,
		ConfirmReqCh:  reqCh,
		ConfirmRespCh: respCh,
	}
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "TUI 异常退出: %v\n", err)
		os.Exit(1)
	}
	// 按 overseer.temp_doc_policy 清理大结果转存的临时文件（session_only 删除 / persistent 保留）
	executor.CleanupTempFiles(cfg.Overseer.TempDocPolicy)
	if sid := app.SessionID(); sid != "" {
		fmt.Printf("会话 ID: %s\n", sid)
		fmt.Printf("恢复命令: contextloom --memory-load %s\n", sid)
	}
}

func runMemoryDelete(cfg *config.Config, uuid string) {
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}
	if err := rt.Memory.Delete(uuid); err != nil {
		fmt.Fprintf(os.Stderr, "删除失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("已删除会话: %s\n", uuid)
}

func runTUI(cfg *config.Config, resume bool, memoryLoad string) {
	rt, err := runtime.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化失败: %v\n", err)
		os.Exit(1)
	}

	if memoryLoad != "" {
		// 必须走 LoadSession：恢复 currentSessionID 和 conversationHistory，
		// 否则 TUI 会误判为无当前会话而新建空会话，导致 AI 丢失全部上下文
		if _, _, err := rt.LoadSession(memoryLoad); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 加载记忆失败: %v\n", err)
		} else if sess, err := rt.Memory.Load(memoryLoad); err == nil {
			fmt.Printf("已恢复会话: %s (%s)\n", sess.Title, sess.UUID)
		}
	} else if resume {
		sessions := rt.ListSessions() // 按 UpdatedAt 倒序，最近活跃在前
		if len(sessions) > 0 {
			latest := sessions[0]
			if _, _, err := rt.LoadSession(latest.UUID); err != nil {
				fmt.Fprintf(os.Stderr, "警告: 恢复会话失败: %v\n", err)
			} else {
				fmt.Printf("已恢复上次会话: %s (%s)\n", latest.Title, latest.UUID)
			}
		}
	}

	if cfg.ACP.Enabled {
		go func() {
			acpSrv := acp.NewServer(cfg.ACP.Port)
			// TUI 并行：不接管确认回调（敏感工具弹 TUI 窗口，用户在场景中）
			h := acp.NewRuntimeHandler(acp.HandlerDeps{
				Spawn:            rt.Executor.SpawnAgent,
				Tools:            rt.Tools,
				Backend:          rt.Backend,
				PersistencePaths: rt.PersistencePaths,
				MaxContext:       cfg.ActiveModel().MaxContext,
				ModelID:          cfg.ActiveModel().Backend,
				Tracer:           rt,
			}, acpSrv, "standard", false)
			acpSrv.SetHandler(h)
			if err := acpSrv.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "ACP 启动失败: %v\n", err)
			}
		}()
	}

	// 初始化工具谨慎性分析器
	ca := tools.NewCautiousAnalyzer(tools.ModeStandard)
	confirmFn, reqCh, respCh := tui.MakeConfirmFunc()
	rt.Tools.SetCautious(ca)
	rt.Tools.SetConfirmFunc(confirmFn)

	app := &tui.App{
		Runtime:       rt,
		ConfirmReqCh:  reqCh,
		ConfirmRespCh: respCh,
	}
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "运行错误: %v\n", err)
		os.Exit(1)
	}
	// 关闭 trace 记录器（flush 当日 JSONL）、OTLP 导出器、MCP server 客户端与外部渠道
	rt.CloseTraceExport()
	if rt.Trace != nil {
		rt.Trace.Close()
	}
	rt.CloseMCPServers()
	rt.CloseChannels()
	rt.CloseSandbox()
	rt.CloseHarness()
	// 按 overseer.temp_doc_policy 清理大结果转存的临时文件（session_only 删除 / persistent 保留）
	executor.CleanupTempFiles(cfg.Overseer.TempDocPolicy)
	// 退出后在标准输出打印会话 ID，方便后续恢复（alt-screen 已恢复，此处可见）
	if sid := app.SessionID(); sid != "" {
		fmt.Printf("会话 ID: %s\n", sid)
		fmt.Printf("恢复命令: contextloom --memory-load %s\n", sid)
	}
}

// runTraceCmd contextloom trace 子命令：查看轻量可观测记录（JSONL 按日轮转）。
func runTraceCmd(args []string) int {
	fs := flag.NewFlagSet("contextloom trace", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tail := fs.Int("tail", 50, "显示最近 N 条")
	dir := fs.String("dir", "", "项目目录（.contextloom/trace 所在目录；默认当前目录）")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "参数解析失败: %v\n", err)
		return 2
	}
	projDir := *dir
	if projDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "获取当前目录失败: %v\n", err)
			return 1
		}
		projDir = wd
	}
	rec, err := trace.New(filepath.Join(projDir, ".contextloom", "trace"), true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开 trace 失败: %v\n", err)
		return 1
	}
	defer rec.Close()
	entries := rec.Recent(*tail)
	if len(entries) == 0 {
		fmt.Println("暂无 trace 记录")
		return 0
	}
	fmt.Printf("最近 %d 条 trace（%s/.contextloom/trace/）:\n", len(entries), projDir)
	for _, e := range entries {
		extra := ""
		if e.Tool != "" {
			extra = " 工具=" + e.Tool
		}
		if e.Ms > 0 {
			extra += fmt.Sprintf(" %dms", e.Ms)
		}
		if e.Err != "" {
			extra += " 错误=" + e.Err
		}
		fmt.Printf("  %s [%s] %s 轮#%d%s\n", e.TS.Format("2006-01-02 15:04:05"), e.Mode, e.Event, e.Round, extra)
	}
	return 0
}

// runEvalCmd contextloom eval 子命令：离线可复现评估 harness。
// runRetrievalEvalCmd 检索质量评测命令（Recall@K / MRR）。
// 纯离线零 API 依赖：yaml 定义语料库 + 查询 + gold，输出 R@K/MRR 基线报告。
func runRetrievalEvalCmd(args []string) int {
	fs := flag.NewFlagSet("contextloom eval-retrieval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "retrieval.yaml", "retrieval.yaml 路径")
	jsonOut := fs.String("json", "", "输出 JSON 报告路径（可选）")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "参数解析失败: %v\n", err)
		return 2
	}
	rep, err := eval.RunRetrieval(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检索评测失败: %v\n", err)
		return 1
	}
	fmt.Print(rep.RenderText())
	for _, d := range rep.Details {
		rankStr := ""
		for i, r := range d.Ranks {
			if i > 0 {
				rankStr += ","
			}
			rankStr += fmt.Sprint(r)
		}
		fmt.Printf("  %-6s R=%.2f MRR=%.2f gold排名=[%s]\n", truncate(d.Query, 24), d.Recall, d.MRR, rankStr)
	}
	if *jsonOut != "" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err == nil && os.WriteFile(*jsonOut, data, 0o644) == nil {
			fmt.Printf("JSON 报告: %s\n", *jsonOut)
		}
	}
	return 0
}

// truncate 截断字符串（runes 保留，超出加省略号）。
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

//   - 默认 dry-run：只验证解析/文件拷贝/检查器，不调用模型（CI 可复跑）；
//   - --real 时用当前配置的模型后端执行任务（需已配置 API Key）；
//   - --direct 时用轻量直接调用模式（仅加载 config + model 后端，不走完整
//     runtime/TUI/插件链），适合大规模数据集快速跑分（GSM8K/HumanEval 等）。
func runEvalCmd(args []string) int {
	fs := flag.NewFlagSet("contextloom eval", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	file := fs.String("file", "eval.yaml", "eval.yaml 路径")
	jsonOut := fs.String("json", "", "输出 JSON 报告路径（可选）")
	dryRun := fs.Bool("dry-run", true, "dry-run 模式（默认）：不调用模型，只验证解析与检查器")
	real := fs.Bool("real", false, "真实模式：调用模型执行任务（需 API Key）")
	direct := fs.Bool("direct", false, "轻量直接调用模式：仅加载配置与模型后端，不走完整 runtime（大数据集快速跑分）")
	modelName := fs.String("model", "", "指定评测使用的模型预设名（默认用 active_model）")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "参数解析失败: %v\n", err)
		return 2
	}
	opts := eval.Options{DryRun: *dryRun && !*real && !*direct}

	if *direct {
		// 轻量直接模式：只加载配置 + model 后端，不初始化 runtime。
		// 适用：大规模标准数据集快速跑分（GSM8K/HumanEval/Terminal-Bench 等）。
		cfg, err := config.Load("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
			return 1
		}
		if *modelName != "" {
			found := false
			for i := range cfg.Models {
				if strings.EqualFold(strings.TrimSpace(cfg.Models[i].Name), strings.TrimSpace(*modelName)) {
					cfg.ActiveModelName = cfg.Models[i].Name
					found = true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "--model %q 未找到，可用预设: %s\n", *modelName, cfg.ModelNames())
				return 1
			}
		}
		m := cfg.ActiveModel()
		sendEffort := true
		if m.SendEffort != nil {
			sendEffort = *m.SendEffort
		}
		backend := model.NewDeepSeekBackendWithEffort(m.Endpoint, m.APIKey, m.Backend, m.MaxContext, sendEffort)
		if m.TopP != nil && *m.TopP > 0 {
			backend.SetTopP(*m.TopP)
		}
		if m.Temperature != nil && *m.Temperature >= 0 {
			backend.SetTemperature(*m.Temperature)
		}
		backend.SetReasoningEffort("low")
		opts.RunTask = func(ctx context.Context, workDir, prompt string) (string, error) {
			// 单轮直接对话（无工具/无 runtime），返回最终文本
			out, _, err := backend.Chat(ctx, []types.Message{
				{Role: "user", Content: prompt},
			}, nil)
			return out, err
		}
	} else if !opts.DryRun {
		// 真实模式：加载配置并构造 runtime（复用既有 agent 执行链路）
		cfg, err := config.Load("")
		if err != nil {
			fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
			return 1
		}
		rt, err := runtime.New(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "初始化 runtime 失败: %v\n", err)
			return 1
		}
		opts.RunTask = func(ctx context.Context, workDir, prompt string) (string, error) {
			// 在工作目录执行 task 模式（stateless 单轮）
			// 复用 ProcessInput 的 task 执行链，输出为最终响应文本
			ch, errCh, err := rt.ProcessInput(ctx, "[task 模式] "+prompt)
			if err != nil {
				return "", err
			}
			var sb strings.Builder
			for {
				select {
				case tok, ok := <-ch:
					if !ok {
						ch = nil
						continue
					}
					sb.WriteString(tok)
				case err, ok := <-errCh:
					if ok && err != nil {
						return sb.String(), err
					}
				case <-ctx.Done():
					return sb.String(), ctx.Err()
				}
				if ch == nil && errCh == nil {
					break
				}
			}
			return sb.String(), nil
		}
	}

	rep, err := eval.Run(context.Background(), *file, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "评估失败: %v\n", err)
		return 1
	}

	// 终端表格
	fmt.Printf("评估集: %s（%s，%d/%d 通过，通过率 %.1f%%）\n",
		rep.Name, map[bool]string{true: "dry-run", false: "真实模式"}[rep.DryRun],
		rep.Passed, rep.Total, rep.PassRate*100)
	for _, tr := range rep.Tasks {
		mark := "✅"
		if !tr.Pass {
			mark = "❌"
		}
		line := fmt.Sprintf("  %s %-16s %5dms", mark, tr.ID, tr.Ms)
		if tr.Err != "" {
			line += "  " + tr.Err
		} else if tr.Output != "" {
			line += "  输出: " + tr.Output
		}
		fmt.Println(line)
	}

	if *jsonOut != "" {
		data, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(*jsonOut, data, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "写 JSON 报告失败: %v\n", err)
			return 1
		}
		fmt.Printf("JSON 报告已写入: %s\n", *jsonOut)
	}
	return 0
}
