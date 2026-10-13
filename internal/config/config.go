package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/modelgateway"
)

type Config struct {
	// ActiveModelName 当前激活的模型预设名（models 列表中的 name）。
	// 模型配置唯一定义在 models 列表；顶层只存"激活名"指针，不存任何模型字段。
	ActiveModelName string                     `yaml:"active_model"`
	Models          []ModelPreset              `yaml:"models"`
	Gateway         modelgateway.GatewayConfig `yaml:"gateway"`
	Harness         HarnessConfig              `yaml:"harness"`
	Project         ProjectConfig              `yaml:"project"`
	ACP             ACPConfig                  `yaml:"acp"`
	Plugins         PluginsConfig              `yaml:"plugins"`
	Overseer        OverseerConfig             `yaml:"overseer"`
	Memory          MemoryConfig               `yaml:"memory"`
	Context         ContextConfig              `yaml:"context"`
	CLIDefaults     CLIDefaultsConfig          `yaml:"cli_defaults"`
	Tools           ToolsConfig                `yaml:"tools"`
	Trace           TraceConfig                `yaml:"trace"`
	MCP             MCPConfig                  `yaml:"mcp"`
	Channels        ChannelsConfig             `yaml:"channels"`
	Sandbox         SandboxConfig              `yaml:"sandbox"`
	Preview         PreviewConfig              `yaml:"preview"`
	Thinking        ThinkingConfig             `yaml:"thinking"`

	// ConfigPath 实际加载的配置文件路径（运行时字段，不入 yaml）。
	// 可能位于可执行文件目录（部署）或启动目录（开发），供 /config 与提示词展示。
	ConfigPath string `yaml:"-"`
}

// MCPConfig MCP Client 配置：mcp.servers.<name> 启动外部 MCP server 子进程，
// 其工具动态注册为 "mcp.<name>.<tool>" 供模型调用（消费外部 MCP 生态）。
type MCPConfig struct {
	Servers map[string]MCPServerConfig `yaml:"servers"`
}

// MCPServerConfig 单个 MCP server 定义。
type MCPServerConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []string `yaml:"env,omitempty"` // 追加子进程环境变量（KEY=VALUE）
	Enabled *bool    `yaml:"enabled"`       // nil=默认 true
}

// ChannelsConfig 外部消息渠道（对标业界同类 Agent 的消息渠道，零依赖落地）：
// webhook=HTTP 接收外部消息触发 goal/task/chat；cron=定时触发。
type ChannelsConfig struct {
	Webhook WebhookConfig `yaml:"webhook"`
	Cron    CronConfig    `yaml:"cron"`
}

// WebhookConfig HTTP 消息渠道配置。
type WebhookConfig struct {
	Enabled bool   `yaml:"enabled"`         // 启动时监听 127.0.0.1 随机端口
	Token   string `yaml:"token,omitempty"` // 鉴权 token；空=启动时自动生成并打印
}

// CronConfig 定时触发渠道配置。
type CronConfig struct {
	Enabled bool            `yaml:"enabled"`
	Jobs    []CronJobConfig `yaml:"jobs"`
}

// CronJobConfig 单个定时任务。
type CronJobConfig struct {
	Name     string `yaml:"name"`     // 任务名（唯一）
	Schedule string `yaml:"schedule"` // 5 字段 cron：分 时 日 月 周
	Mode     string `yaml:"mode"`     // goal / task / chat
	Prompt   string `yaml:"prompt"`   // 触发内容
}

// SandboxConfig 工具沙箱（对标 OpenHands/Claude Code 沙箱的零依赖落地）：
// 开启后所有文件写操作重定向到影子文件系统（.contextloom/sandbox/），
// 读操作镜像优先、列表合并展示；exec 工作目录进入镜像 + 资源限制。
// 会话结束可选择 commit（镜像写回真实）或 discard（丢弃全部改动）。
type SandboxConfig struct {
	Enabled       bool `yaml:"enabled"`         // 工具写操作进入影子文件系统
	DiscardOnExit bool `yaml:"discard_on_exit"` // 退出时丢弃镜像（默认 true）；false=保留供下次合并
}

// TraceConfig 轻量可观测（JSONL trace + OTLP 导出）。
type TraceConfig struct {
	Enabled        *bool             `yaml:"enabled"`         // 是否启用 trace 记录（nil=默认 true）
	ExportEndpoint string            `yaml:"export_endpoint"` // OTLP/HTTP 接收端（如 http://localhost:4318/v1/logs）；空=不导出
	ExportHeaders  map[string]string `yaml:"export_headers"`  // 附加 HTTP 头（Authorization 等）
	ExportBatch    int               `yaml:"export_batch"`    // 批量条数（默认 32）
	ExportInterval int               `yaml:"export_interval"` // 定时冲刷秒（默认 5）
}

// ContextConfig 上下文预算控制（token 预算驱动的渐进式压缩）。
// 所有阈值都是用量比例 [0,1]，并自动保证 compact < hard < safety 单调。
type ContextConfig struct {
	CompactRatio float64 `yaml:"compact_ratio"` // 达到该用量开始前缀截断（默认 0.70）
	HardRatio    float64 `yaml:"hard_ratio"`    // 达到该用量强制丢弃最旧（默认 0.85）
	SafetyRatio  float64 `yaml:"safety_ratio"`  // 发送前兜底红线（默认 0.92）
	KeepRecent   int     `yaml:"keep_recent"`   // 至少保留的完整消息条数（默认 8）
}

// legacyModelConfig 仅用于加载时迁移旧版 model: 配置节（v2.5.1 之前）。
// 新配置彻底移除 model: 节——模型配置唯一定义在 models 列表，
// 顶层 active_model 仅保存"当前激活预设名"指针，杜绝双份配置漂移。
type legacyModelConfig struct {
	Backend        string   `yaml:"backend"`
	Endpoint       string   `yaml:"endpoint"`
	APIKey         string   `yaml:"api_key"`
	MaxContext     int      `yaml:"max_context"`
	TopP           *float64 `yaml:"top_p"`
	Temperature    *float64 `yaml:"temperature"`
	VisionModel    string   `yaml:"vision_model"`
	SupportsVision bool     `yaml:"supports_vision"`
	SendEffort     *bool    `yaml:"send_effort"`

	// 生成参数随迁移携带（与 ModelPreset 同 yaml 映射）
	MaxOutputTokens int    `yaml:"max_output_tokens"`
	MaxInputTokens  int    `yaml:"max_input_tokens"`
	ThinkingLength  string `yaml:"thinking_length"`
	ThinkingEnabled *bool  `yaml:"thinking_enabled"`
}

// ActiveModel 返回当前生效的模型配置（唯一事实来源）。
//
// 设计约定：models 列表是模型配置的唯一定义处；顶层 active_model
// 仅保存"当前激活预设名"指针，其余字段（backend/endpoint/api_key/
// max_context/top_p/temperature/vision_model/supports_vision/send_effort）
// 一律从激活预设读取，避免双份配置漂移。
//
// 查找规则：按激活名（大小写不敏感）查找 → 找不到回退第一个预设 →
// 无预设时返回内置默认（default-model，与 applyDefaults 一致）。
func (c *Config) ActiveModel() ModelPreset {
	if len(c.Models) > 0 {
		name := strings.TrimSpace(c.ActiveModelName)
		for i := range c.Models {
			if strings.EqualFold(strings.TrimSpace(c.Models[i].Name), name) {
				return c.Models[i]
			}
		}
		return c.Models[0]
	}
	// 无预设（异常兜底）：返回内置默认，与 applyDefaults 保持一致
	se := true
	return ModelPreset{
		Name:           "default-model",
		Backend:        "openai-compatible",
		Endpoint:       "",
		APIKey:         "",
		MaxContext:     131072,
		TopP:           f64p(0.95),
		Temperature:    f64p(1.0),
		VisionModel:    "",
		SupportsVision: false,
		SendEffort:     &se,
	}
}

// GatewayModelConfig 把 gateway: 节桥接为网关配置（方案 §4.2.2 语义落地）：
// pools.<池名> 优先按 models: 预设补全连接参数（backend/endpoint/api_key/
// model/max_context，预设是唯一事实来源），池级治理参数（限流/熔断/重试）
// 仍取自 gateway.pools.<池名>。未配置 gateway 节时返回零配置（网关默认池
// =注入后端 = 当前激活预设）。rules 原样透传（yaml 反序列化时已解析谓词）。
func (c *Config) GatewayModelConfig() modelgateway.GatewayConfig {
	out := modelgateway.GatewayConfig{Default: c.Gateway.Default, Rules: c.Gateway.Rules}
	out.Pools = make(map[string]modelgateway.PoolConfig, len(c.Gateway.Pools))
	for name, pc := range c.Gateway.Pools {
		out.Pools[name] = c.bridgeGatewayPool(name, pc)
	}
	return out
}

// bridgeGatewayPool 单池桥接：同名 models 预设补全连接参数。
func (c *Config) bridgeGatewayPool(name string, pc modelgateway.PoolConfig) modelgateway.PoolConfig {
	if preset := c.presetByName(name); preset != nil {
		if pc.Backend == "" {
			pc.Backend = preset.Backend
		}
		if pc.Endpoint == "" {
			pc.Endpoint = preset.Endpoint
		}
		if pc.APIKey == "" && len(pc.Keys) == 0 {
			pc.APIKey = preset.APIKey
		}
		if pc.Model == "" {
			pc.Model = preset.Backend // 与 runtime 建后端语义一致：modelID 取 backend 名
		}
		if pc.MaxContext == 0 {
			pc.MaxContext = preset.MaxContext
		}
		if pc.SendEffort == false && preset.SendEffort != nil {
			pc.SendEffort = *preset.SendEffort
		}
		// 多模态识图能力：池未显式声明时按同名预设补齐。若缺失，网关路径
		// （配置了 gateway: 节时所有请求都经此）会以默认 false 建实例，
		// 导致 supports_vision=true 的预设仍被误判为纯文本模型而降级。
		if !pc.SupportsVision {
			pc.SupportsVision = preset.SupportsVision
		}
	}
	return pc
}

// presetByName 按名称查找模型预设（大小写不敏感）。
func (c *Config) presetByName(name string) *ModelPreset {
	for i := range c.Models {
		if strings.EqualFold(strings.TrimSpace(c.Models[i].Name), name) {
			return &c.Models[i]
		}
	}
	return nil
}

// ModelPreset 多模型预设：models 列表中的一项。通过 /model <name> 或
// GUI 设置面板一键切换（切换时按本预设的 backend/endpoint/api_key 等重建后端）。
type ModelPreset struct {
	Name           string   `yaml:"name"`
	Backend        string   `yaml:"backend"`
	Endpoint       string   `yaml:"endpoint"`
	APIKey         string   `yaml:"api_key"`
	MaxContext     int      `yaml:"max_context"`
	TopP           *float64 `yaml:"top_p"`
	Temperature    *float64 `yaml:"temperature"`
	VisionModel    string   `yaml:"vision_model"`
	SupportsVision bool     `yaml:"supports_vision"`
	SendEffort     *bool    `yaml:"send_effort"` // nil=默认 true

	// 生成参数（对标 DeepSeek Harness：max_tokens 与 contextWindow 解耦）：
	// 输出/输入上限独立于 MaxContext，思考长度与思考开关独立控制思考行为。
	MaxOutputTokens int    `yaml:"max_output_tokens"` // 0=未设置（回退 MaxContext，保持现状语义）；>0=单次模型输出上限（思考过程计入该预算）
	MaxInputTokens  int    `yaml:"max_input_tokens"`  // 0=未设置（回退 MaxContext）；>0=单次请求输入预算上限
	ThinkingLength  string `yaml:"thinking_length"`   // ""=继承 cli_defaults.effort；可选 off/low/medium/high/xhigh/max
	ThinkingEnabled *bool  `yaml:"thinking_enabled"`  // nil=不显式发送 thinking 字段（保持现状）；false=关闭思考
}

// thinkingLengthOptions 思考长度合法档位（首项 ""=继承 cli_defaults.effort）。
// ConfigSchema 枚举 / Apply 写入校验 / Validate 软校验共用同一清单，杜绝三处漂移。
var thinkingLengthOptions = []string{"", "off", "low", "medium", "high", "xhigh", "max"}

// validThinkingLength 判断思考长度档位是否合法。
func validThinkingLength(s string) bool {
	for _, opt := range thinkingLengthOptions {
		if s == opt {
			return true
		}
	}
	return false
}

type HarnessConfig struct {
	ScoreThreshold float64 `yaml:"score_threshold"`
	DecayFactor    float64 `yaml:"decay_factor"`
	AnchorRatio    float64 `yaml:"anchor_ratio"`
}

type ProjectConfig struct {
	Root  string `yaml:"root"`
	Vocab string `yaml:"vocab"`
}

type ACPConfig struct {
	Enabled bool `yaml:"enabled"`
	Port    int  `yaml:"port"`
}

type PluginsConfig struct {
	Enabled   bool           `yaml:"enabled"`
	Directory string         `yaml:"directory"`
	Sandbox   PluginsSandbox `yaml:"sandbox"`
}

type PluginsSandbox struct {
	MemoryLimitMB int     `yaml:"memory_limit_mb"`
	CPULimit      float64 `yaml:"cpu_limit"`
}

type OverseerConfig struct {
	PassThreshold float64 `yaml:"pass_threshold"`
	NodeAutoPlan  bool    `yaml:"node_auto_plan"`
	TestTEE       bool    `yaml:"test_tee"`
	TempDocPolicy string  `yaml:"temp_doc_policy"`
}

type MemoryConfig struct {
	Directory   string `yaml:"directory"`
	AutoTitle   bool   `yaml:"auto_title"`
	MaxSessions int    `yaml:"max_sessions"`
}

// ToolsConfig 工具行为配置。
// ApprovalMode 审批预设（解决"权限审批模型复杂、无一键预设"的共性缺点）：
//   - "permissive"：全自动批准（task 模式/无人值守）
//   - "standard" （默认）：标准谨慎模式（敏感写操作/命令需确认，chat 模式默认）
//   - "strict"：严格模式（所有写操作和命令都需确认，高风险场景）
type ToolsConfig struct {
	ApprovalMode string `yaml:"approval_mode"`
}

// PreviewConfig 预览渲染配置（GUI Markdown 预览）。
// PlantUML 图（```plantuml 代码块）通过本地代理渲染为 SVG——
// 同源请求天然规避跨域限制（响应亦附加 CORS 头双保险），server 可指向
// 公网默认服务或内网自建 plantuml-server。
type PreviewConfig struct {
	PlantUMLServer  string `yaml:"plantuml_server"`  // plantuml-server 根地址（默认 https://www.plantuml.com/plantuml）
	PlantUMLEnabled *bool  `yaml:"plantuml_enabled"` // 是否渲染 plantuml 图（nil=默认 true；false=按普通代码块展示）
}

type CLIDefaultsConfig struct {
	Model             string  `yaml:"model"`
	Effort            string  `yaml:"effort"`
	Mode              string  `yaml:"mode"`
	Threshold         float64 `yaml:"threshold"`
	OverseerThreshold float64 `yaml:"overseer_threshold"`
	// KeyNewline 输入框换行键：auto（按终端自动检测）| ctrl+j | alt+enter
	//  - VS Code 集成终端：Ctrl+J 被 IDE 占用 → 建议 alt+enter
	//  - Windows 终端：Alt+Enter 被系统占用（全屏） → 建议 ctrl+j
	//  - 其他 Unix 终端（iTerm2/Terminal.app/xterm）：两者皆可
	KeyNewline string `yaml:"key_newline"`
}

// ThinkingConfig 多轮思考触发策略（顶层 thinking: 分组；轮次面"想几轮/是否验证"）。
// 与参数面 model.thinking_length（"每轮想多长"）正交：本组管轮次结构与收尾拦截，
// Group 名"思考策略"与"模型"分组区隔。零值=off/现状（零回归）：空串归一
// （""→off/off/none/max）与 0 值语义（0→运行时取 3/60/2）由运行时 Normalize 负责。
type ThinkingConfig struct {
	Policy              string `yaml:"policy"`               // ""|off|auto|strict：off=不干预（现状）；auto=复杂任务软触发；strict=强制验证轮并拦截收尾
	MinThinkRounds      int    `yaml:"min_think_rounds"`     // 0-20（0=不强制）：复杂任务最少思考轮数（strict 硬下限 / auto 软下限）
	MaxThinkRounds      int    `yaml:"max_think_rounds"`     // 0-20（0→运行时取 3）：续轮注入总上限（拦截+nudge；工具执行轮不受限）
	VerifyPass          string `yaml:"verify_pass"`          // ""|off|auto|on：off=无验证轮；auto=复杂触发；on=收尾必验（含 simple）
	ComplexityThreshold int    `yaml:"complexity_threshold"` // 0-100（0→运行时取 60）：复杂度评分阈值（≥阈值=复杂）
	NudgeEnabled        bool   `yaml:"nudge_enabled"`        // 续思提示注入开关（M6）
	NudgeMax            int    `yaml:"nudge_max"`            // 0-10（0→运行时取 2）：nudge 单独上限
	RoundEffortPolicy   string `yaml:"round_effort_policy"`  // ""|none|escalate：none=保持（现状）；escalate=验证轮升档
	RoundEffortCap      string `yaml:"round_effort_cap"`     // ""|low|medium|high|xhigh|max：升档封顶（仅 escalate 生效）
}

// thinking.* 选项清单（ConfigSchema / Sanitized / applyOne / Validate 共用，杜绝多处漂移）。
var (
	thinkingPolicyOptions    = []string{"off", "auto", "strict"}
	verifyPassOptions        = []string{"off", "auto", "on"}
	roundEffortPolicyOptions = []string{"none", "escalate"}
	roundEffortCapOptions    = []string{"low", "medium", "high", "xhigh", "max"}
)

// inList 判断字符串是否命中合法选项清单（枚举校验共用）。
func inList(s string, opts []string) bool {
	for _, opt := range opts {
		if s == opt {
			return true
		}
	}
	return false
}

// thinkingNorm 空值归一：""→默认档（Sanitized 输出与运行时 Normalize 同口径）。
func thinkingNorm(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// DefaultConfigPath 返回默认配置文件路径（完整查找链，从高到低）：
//
//  1. CONTEXTLOOM_CONFIG 环境变量（显式指定，等价于 --config 参数）
//  2. 真实二进制目录：解析可执行文件符号链接后的本体所在目录
//     （全局命令 /usr/local/bin/contextloom → 真实本体目录场景，配置跟随二进制打包部署）
//  3. 原始可执行文件目录（os.Executable 原样返回；无符号链接时的本体目录）
//  4. 当前工作目录 CWD 的 config.yaml（go run / 开发场景）
//  5. ~/.contextloom/config.yaml（用户级兜底配置）
//  6. "config.yaml"（兜底路径：Load 内 os.IsNotExist → 内置默认配置）
//
// 历史缺陷：macOS/Linux 下经 PATH 符号链接执行时，os.Executable() 返回链接路径
// 而非本体路径——若不解析符号链接，"二进制本体所在目录"永远查不到，从而静默
// 回退 CWD。EvalSymlinks 解析后优先探测本体目录解决此问题。
func DefaultConfigPath() string {
	if env := os.Getenv("CONTEXTLOOM_CONFIG"); env != "" {
		return env
	}
	exe, _ := os.Executable()
	return defaultConfigPathFromExe(exe)
}

// defaultConfigPathFromExe 纯函数：由可执行文件路径推导默认配置路径（便于单测）。
// exe 为空 = 跳过 exe 目录探测（等价于 os.Executable 失败场景）。
func defaultConfigPathFromExe(exe string) string {
	if exe != "" {
		// 1. 真实本体目录（解析符号链接）——"全局命令"最常见形态
		if real, err := filepath.EvalSymlinks(exe); err == nil && real != exe {
			if p := filepath.Join(filepath.Dir(real), "config.yaml"); fileExists(p) {
				return p
			}
		}
		// 2. 原始 exe 目录（无链接或解析失败时）
		if p := filepath.Join(filepath.Dir(exe), "config.yaml"); fileExists(p) {
			return p
		}
	}
	// 3. 当前工作目录（go run / 开发场景）
	if fileExists("config.yaml") {
		return "config.yaml"
	}
	// 4. 用户级兜底配置
	if home, err := os.UserHomeDir(); err == nil {
		if p := filepath.Join(home, ".contextloom", "config.yaml"); fileExists(p) {
			return p
		}
	}
	// 5. 兜底：Load 内 os.IsNotExist → 内置默认配置
	return "config.yaml"
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(absPath)
	cfg := &Config{}
	if err != nil {
		// 配置文件不存在（全新部署/项目目录无配置）：使用内置默认配置继续。
		// 设计语义：config.yaml 跟随可执行文件目录，项目目录可以没有配置文件；
		// 进一步地，全新部署（exe 目录也没有）也能启动——api_key 等关键项
		// 由 Validate() 兜底校验（无 key 且无 DEEPSEEK_API_KEY 时启动报错提示）。
		// 注意：路径存在但内容非法（yaml 解析错误）仍会报错，不静默吞掉。
		if os.IsNotExist(err) {
			cfg.applyDefaults()
			cfg.resolveProjectRoot()
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	// 旧配置迁移（v2.5.1 之前）：检测已废弃的 model: 节。
	// 新配置彻底移除 model: 节——若旧配置仅用 model 节、无 active_model/models，
	// 自动迁移为 active_model + models 预设，零手工迁移。
	cfg.migrateLegacyModel(data)
	cfg.ConfigPath = absPath
	cfg.applyDefaults()
	cfg.resolveProjectRoot()
	return cfg, nil
}

// resolveProjectRoot 推导项目根目录：
//   - 若配置显式指定 root（非空且非 "."），尊重用户配置；
//   - 否则以"启动 contextloom 时所在的工作目录（CWD）"为项目根。
//
// 设计语义：
//   - 项目目录 = 命令启动时所在目录，该目录下存放 .contextloom/ 数据（记忆/技能等）；
//   - 项目目录可以没有 config.yaml —— 配置文件默认跟随可执行文件所在目录（见 DefaultConfigPath），
//     与项目根解耦：同一份配置可服务于多个项目，数据目录始终落在各自的启动目录。
func (c *Config) resolveProjectRoot() {
	if c.Project.Root != "" && c.Project.Root != "." {
		return
	}
	if wd, err := os.Getwd(); err == nil {
		c.Project.Root = wd
	}
}

// Validate 检查配置是否有效，返回错误列表
func (c *Config) Validate() error {
	am := c.ActiveModel()
	if strings.TrimSpace(am.Backend) == "" {
		return fmt.Errorf("models 激活预设 %q 缺少 backend", am.Name)
	}
	if am.Endpoint == "" && !model.LocalBackendNames()[strings.ToLower(am.Backend)] {
		return fmt.Errorf("model.endpoint 不能为空（models 激活预设 %q）", am.Name)
	}
	// API Key 缺失时启动即报错，避免运行到请求阶段才 401。
	// 本地/OpenAI 兼容后端（llama/ollama/local 等）api_key 可为空。
	if !model.LocalBackendNames()[strings.ToLower(am.Backend)] {
		if am.APIKey == "" && os.Getenv("DEEPSEEK_API_KEY") == "" {
			return fmt.Errorf("model.api_key 未配置：请在 config.yaml 的 models 列表中填写 api_key（或设置环境变量 DEEPSEEK_API_KEY）")
		}
	}
	if c.Harness.DecayFactor <= 0 || c.Harness.DecayFactor > 1 {
		return fmt.Errorf("harness.decay_factor 必须在 (0, 1] 范围内")
	}
	if c.Harness.AnchorRatio < 0 || c.Harness.AnchorRatio > 1 {
		return fmt.Errorf("harness.anchor_ratio 必须在 [0, 1] 范围内")
	}
	if am.TopP != nil && (*am.TopP < 0 || *am.TopP > 1) {
		return fmt.Errorf("model.top_p 必须在 [0, 1] 范围内")
	}
	if am.Temperature != nil && (*am.Temperature < 0 || *am.Temperature > 2) {
		return fmt.Errorf("model.temperature 必须在 [0, 2] 范围内")
	}
	// 生成参数软校验：只拦明显误配（负值/非法档位/单值超窗口 4 倍）。
	// 输出+输入之和超窗口不报错——模型窗口差异大（128K~1M），动态预算由运行时
	// 按实际用量钳制；配置层硬拒会误伤"给大上限、运行时按需收敛"的用法。
	if am.MaxOutputTokens < 0 {
		return fmt.Errorf("model.max_output_tokens 不能为负（0=不限制）")
	}
	if am.MaxInputTokens < 0 {
		return fmt.Errorf("model.max_input_tokens 不能为负（0=不限制）")
	}
	if am.ThinkingLength != "" && !validThinkingLength(am.ThinkingLength) {
		return fmt.Errorf("model.thinking_length 非法取值 %q：合法取值 %v", am.ThinkingLength, thinkingLengthOptions)
	}
	if am.MaxContext > 0 {
		if am.MaxOutputTokens > am.MaxContext*4 {
			return fmt.Errorf("model.max_output_tokens=%d 超过最大上下文 %d 的 4 倍，疑似误配", am.MaxOutputTokens, am.MaxContext)
		}
		if am.MaxInputTokens > am.MaxContext*4 {
			return fmt.Errorf("model.max_input_tokens=%d 超过最大上下文 %d 的 4 倍，疑似误配", am.MaxInputTokens, am.MaxContext)
		}
	}
	// thinking.* 多轮思考触发策略软校验：枚举 + 范围 + 交叉校验（min>max 且均>0 报错）。
	// policy=off（含空值归一）时其余字段不做硬拒绝——软裁决：运行时 policy 优先，
	// GUI 提示"不生效"（对齐参数面互斥软裁决现状）。
	if c.Thinking.Policy != "" && !inList(c.Thinking.Policy, thinkingPolicyOptions) {
		return fmt.Errorf("thinking.policy 非法取值 %q：合法取值 %v", c.Thinking.Policy, thinkingPolicyOptions)
	}
	if thinkingNorm(c.Thinking.Policy, "off") != "off" {
		if c.Thinking.VerifyPass != "" && !inList(c.Thinking.VerifyPass, verifyPassOptions) {
			return fmt.Errorf("thinking.verify_pass 非法取值 %q：合法取值 %v", c.Thinking.VerifyPass, verifyPassOptions)
		}
		if c.Thinking.RoundEffortPolicy != "" && !inList(c.Thinking.RoundEffortPolicy, roundEffortPolicyOptions) {
			return fmt.Errorf("thinking.round_effort_policy 非法取值 %q：合法取值 %v", c.Thinking.RoundEffortPolicy, roundEffortPolicyOptions)
		}
		if c.Thinking.RoundEffortCap != "" && !inList(c.Thinking.RoundEffortCap, roundEffortCapOptions) {
			return fmt.Errorf("thinking.round_effort_cap 非法取值 %q：合法取值 %v", c.Thinking.RoundEffortCap, roundEffortCapOptions)
		}
		if c.Thinking.MinThinkRounds < 0 || c.Thinking.MinThinkRounds > 20 {
			return fmt.Errorf("thinking.min_think_rounds=%d 必须在 [0, 20] 范围内", c.Thinking.MinThinkRounds)
		}
		if c.Thinking.MaxThinkRounds < 0 || c.Thinking.MaxThinkRounds > 20 {
			return fmt.Errorf("thinking.max_think_rounds=%d 必须在 [0, 20] 范围内", c.Thinking.MaxThinkRounds)
		}
		if c.Thinking.ComplexityThreshold < 0 || c.Thinking.ComplexityThreshold > 100 {
			return fmt.Errorf("thinking.complexity_threshold=%d 必须在 [0, 100] 范围内", c.Thinking.ComplexityThreshold)
		}
		if c.Thinking.NudgeMax < 0 || c.Thinking.NudgeMax > 10 {
			return fmt.Errorf("thinking.nudge_max=%d 必须在 [0, 10] 范围内", c.Thinking.NudgeMax)
		}
		if c.Thinking.MinThinkRounds > 0 && c.Thinking.MaxThinkRounds > 0 && c.Thinking.MinThinkRounds > c.Thinking.MaxThinkRounds {
			return fmt.Errorf("thinking.min_think_rounds=%d 不能大于 thinking.max_think_rounds=%d", c.Thinking.MinThinkRounds, c.Thinking.MaxThinkRounds)
		}
	}
	return nil
}

// Save 将配置序列化写回文件（GUI 设置面板保存链路）。
// 注意：yaml 序列化会丢失原文件注释，因此先自动备份原文件为 config.yaml.bak
// （保留注释版便于用户回滚），再写新文件。
func (c *Config) Save(path string) error {
	if path == "" {
		path = DefaultConfigPath()
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if data, err := os.ReadFile(abs); err == nil {
		_ = os.WriteFile(abs+".bak", data, 0o644) // 备份原文件（含注释），失败不阻塞保存
	}
	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置: %w", err)
	}
	if dir := filepath.Dir(abs); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	if err := os.WriteFile(abs, out, 0o644); err != nil {
		return fmt.Errorf("写入配置: %w", err)
	}
	c.ConfigPath = abs
	return nil
}

// FieldSchema 描述一个可配置字段（GUI 设置面板 schema 驱动渲染，前后端不重复维护清单）。
type FieldSchema struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // text|password|number|bool|select
	Default any      `json:"default,omitempty"`
	Desc    string   `json:"desc,omitempty"`
	Group   string   `json:"group"`
	Enums   []string `json:"enums,omitempty"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
}

func f64p(v float64) *float64 { return &v }

// ConfigSchema 返回设置面板可编辑字段清单（与 config.example.yaml 一一对应）。
func ConfigSchema() []FieldSchema {
	return []FieldSchema{
		{Key: "model.endpoint", Label: "API 端点", Type: "text", Group: "模型", Desc: "OpenAI 兼容 API 地址（写回当前激活预设）"},
		{Key: "model.api_key", Label: "API Key", Type: "password", Group: "模型", Desc: "显示脱敏；保存时留空/不改则不覆盖真实 Key（也可用环境变量 DEEPSEEK_API_KEY）"},
		{Key: "model.max_context", Label: "最大上下文 (tokens)", Type: "number", Group: "模型", Default: 131072, Desc: "token 窗口上限", Min: f64p(8192), Max: f64p(100000000), Step: 1024},
		{Key: "model.max_output_tokens", Label: "最大输出 (tokens)", Type: "number", Group: "模型", Default: 0, Desc: "0=不限制（自动取最大上下文）；>0=单次模型输出 token 上限（与最大上下文解耦，思考过程计入该预算）", Min: f64p(0), Max: f64p(100000000), Step: 1024},
		{Key: "model.max_input_tokens", Label: "最大输入 (tokens)", Type: "number", Group: "模型", Default: 0, Desc: "0=不限制（取最大上下文）；>0=单次请求输入 token 预算上限", Min: f64p(0), Max: f64p(100000000), Step: 1024},
		{Key: "model.top_p", Label: "Top P", Type: "number", Group: "模型", Default: 0.95, Desc: "核采样 [0,1]", Min: f64p(0), Max: f64p(1), Step: 0.05},
		{Key: "model.temperature", Label: "温度", Type: "number", Group: "模型", Default: 1.0, Desc: "采样温度 [0,2]", Min: f64p(0), Max: f64p(2), Step: 0.1},
		{Key: "model.vision_model", Label: "识图模型", Type: "text", Group: "模型", Default: "", Desc: "输入含图片时本轮自动切换（空=沿用主模型）"},
		{Key: "model.supports_vision", Label: "支持多模态识图", Type: "bool", Group: "模型", Default: false, Desc: "OpenAI 兼容视觉端点才开启（DeepSeek 官方为纯文本，发图片会 400；关闭时图片降级为路径文本）"},
		{Key: "model.send_effort", Label: "发送思考强度参数", Type: "bool", Group: "模型", Default: true, Desc: "是否向 API 发送 reasoning_effort（默认打开；个别 OpenAI 兼容服务严格校验未知字段时关闭）"},
		{Key: "model.thinking_length", Label: "思考长度", Type: "select", Group: "模型", Default: "", Enums: append([]string(nil), thinkingLengthOptions...), Desc: "思考长度档位：off 关闭思考；空=沿用 CLI 默认思考深度（cli_defaults.effort）"},
		{Key: "model.thinking_enabled", Label: "启用模型思考", Type: "bool", Group: "模型", Default: true, Desc: "是否启用模型思考（false 时向 API 发送 thinking:{type:disabled}；仅 DeepSeek 官方通道生效）"},
		{Key: "harness.score_threshold", Label: "检索分数阈值", Type: "number", Group: "Harness 检索", Default: 50.0, Desc: "低于该分的块不返回", Min: f64p(0), Max: f64p(100), Step: 1},
		{Key: "harness.decay_factor", Label: "索引衰减因子", Type: "number", Group: "Harness 检索", Default: 0.9, Desc: "/compress 时旧块权重衰减", Min: f64p(0.01), Max: f64p(1), Step: 0.05},
		{Key: "harness.anchor_ratio", Label: "锚点比例", Type: "number", Group: "Harness 检索", Default: 0.2, Desc: "锚点块占比", Min: f64p(0), Max: f64p(1), Step: 0.05},
		{Key: "project.root", Label: "项目根目录", Type: "text", Group: "项目", Desc: "为空或 . 时 = 启动目录；.contextloom/ 数据落于此"},
		{Key: "project.vocab", Label: "词汇表路径", Type: "text", Group: "项目", Desc: "自定义词汇映射文件"},
		{Key: "acp.enabled", Label: "启用 ACP", Type: "bool", Group: "ACP", Default: true, Desc: "Agent 通信协议"},
		{Key: "acp.port", Label: "ACP 端口", Type: "number", Group: "ACP", Default: 20250, Desc: "监听端口", Min: f64p(1), Max: f64p(65535), Step: 1},
		{Key: "plugins.enabled", Label: "启用插件", Type: "bool", Group: "插件", Default: true, Desc: "加载用户级+项目级插件"},
		{Key: "plugins.directory", Label: "插件目录", Type: "text", Group: "插件", Default: "~/.contextloom/plugins", Desc: "用户级插件目录（~ 展开）"},
		{Key: "plugins.sandbox.memory_limit_mb", Label: "沙箱内存上限 (MB)", Type: "number", Group: "插件", Default: 256, Desc: "插件运行内存限制", Min: f64p(16), Max: f64p(65536), Step: 16},
		{Key: "plugins.sandbox.cpu_limit", Label: "沙箱 CPU 上限", Type: "number", Group: "插件", Default: 0.5, Desc: "CPU 配额 [0,1]", Min: f64p(0.01), Max: f64p(1), Step: 0.05},
		{Key: "overseer.pass_threshold", Label: "监工通过阈值", Type: "number", Group: "监工", Default: 90.0, Desc: "低于该分界面提示⚠（回复仍完整入史）", Min: f64p(0), Max: f64p(100), Step: 1},
		{Key: "overseer.node_auto_plan", Label: "自动计划节点", Type: "bool", Group: "监工", Default: true, Desc: "task 模式按输入拆分主子任务"},
		{Key: "overseer.test_tee", Label: "测试透明代理", Type: "bool", Group: "监工", Default: true, Desc: "防自我欺骗：false 时跳过沙箱测试对比"},
		{Key: "overseer.temp_doc_policy", Label: "临时文档策略", Type: "select", Group: "监工", Default: "session_only", Enums: []string{"session_only", "persistent"}, Desc: "会话结束清理 / 保留"},
		{Key: "memory.directory", Label: "记忆目录", Type: "text", Group: "记忆", Default: ".contextloom/memory", Desc: "会话存储位置"},
		{Key: "memory.auto_title", Label: "自动标题", Type: "bool", Group: "记忆", Default: true, Desc: "会话自动生成标题"},
		{Key: "memory.max_sessions", Label: "最大会话数", Type: "number", Group: "记忆", Default: 1000, Desc: "超出时淘汰最旧", Min: f64p(10), Max: f64p(100000), Step: 10},
		{Key: "channels.webhook.enabled", Label: "Webhook 渠道", Type: "bool", Group: "渠道", Default: false, Desc: "HTTP 接收外部消息触发 goal/task/chat"},
		{Key: "trace.export_endpoint", Label: "OTLP 导出端点", Type: "text", Group: "可观测性", Desc: "OTLP/HTTP+JSON 接收端（如 http://localhost:4318/v1/logs）；留空=仅本地 JSONL"},
		{Key: "trace.export_batch", Label: "OTLP 批量条数", Type: "number", Group: "可观测性", Default: 32, Desc: "满批量即导出", Min: f64p(1), Max: f64p(1024), Step: 1},
		{Key: "trace.export_interval", Label: "OTLP 冲刷间隔 (秒)", Type: "number", Group: "可观测性", Default: 5, Desc: "定时导出周期", Min: f64p(1), Max: f64p(3600), Step: 1},
		{Key: "channels.webhook.token", Label: "Webhook Token", Type: "text", Group: "渠道", Desc: "鉴权 token；留空=启动时自动生成并打印"},
		{Key: "channels.cron.enabled", Label: "Cron 定时调度", Type: "bool", Group: "渠道", Default: false, Desc: "按 cron 表达式定时触发（jobs 在 YAML 中配置）"},
		{Key: "sandbox.enabled", Label: "工具沙箱", Type: "bool", Group: "沙箱", Default: false, Desc: "文件写操作重定向到影子文件系统，exec 进入镜像"},
		{Key: "sandbox.discard_on_exit", Label: "退出丢弃镜像", Type: "bool", Group: "沙箱", Default: true, Desc: "退出时丢弃沙箱改动；false=保留供下次合并"},
		{Key: "context.compact_ratio", Label: "压缩阈值", Type: "number", Group: "上下文预算", Default: 0.70, Desc: "用量达 70% 开始前缀截断", Min: f64p(0.1), Max: f64p(0.99), Step: 0.05},
		{Key: "context.hard_ratio", Label: "强制丢弃阈值", Type: "number", Group: "上下文预算", Default: 0.85, Desc: "用量达 85% 丢弃最旧（累积摘要）", Min: f64p(0.1), Max: f64p(0.99), Step: 0.05},
		{Key: "context.safety_ratio", Label: "发送前红线", Type: "number", Group: "上下文预算", Default: 0.92, Desc: "超限自动压缩/强制收尾", Min: f64p(0.1), Max: f64p(0.99), Step: 0.05},
		{Key: "context.keep_recent", Label: "保留完整消息数", Type: "number", Group: "上下文预算", Default: 8, Desc: "永不丢弃的最近消息条数", Min: f64p(1), Max: f64p(100), Step: 1},
		{Key: "cli_defaults.model", Label: "默认模型", Type: "text", Group: "CLI 默认", Default: "default-model", Desc: "CLI 默认后端"},
		{Key: "cli_defaults.effort", Label: "思考深度", Type: "select", Group: "CLI 默认", Default: "max", Enums: []string{"low", "medium", "high", "xhigh", "max"}, Desc: "max 最高推理强度（low 透传 low 低档，medium/high→high，xhigh/max→max）"},
		{Key: "cli_defaults.mode", Label: "默认模式", Type: "select", Group: "CLI 默认", Default: "chat", Enums: []string{"chat", "task", "goal"}, Desc: "chat 对话 / task 多任务 / goal 目标循环"},
		{Key: "cli_defaults.threshold", Label: "CLI 检索阈值", Type: "number", Group: "CLI 默认", Default: 50.0, Desc: "命令行检索分数阈值", Min: f64p(0), Max: f64p(100), Step: 1},
		{Key: "cli_defaults.overseer_threshold", Label: "CLI 监工阈值", Type: "number", Group: "CLI 默认", Default: 90.0, Desc: "命令行监工通过分", Min: f64p(0), Max: f64p(100), Step: 1},
		{Key: "cli_defaults.key_newline", Label: "换行键", Type: "select", Group: "CLI 默认", Default: "auto", Enums: []string{"auto", "ctrl+j", "alt+enter"}, Desc: "输入框换行快捷键（终端冲突时可改）"},
		{Key: "tools.approval_mode", Label: "审批预设", Type: "select", Group: "工具", Default: "standard", Enums: []string{"standard", "permissive", "strict"}, Desc: "standard 谨慎 / permissive 全自动 / strict 全确认"},
		{Key: "preview.plantuml_server", Label: "PlantUML 服务器", Type: "text", Group: "预览", Default: "https://www.plantuml.com/plantuml", Desc: "plantuml-server 根地址（可配内网自建服务，如 http://127.0.0.1:8080/plantuml）；Markdown 预览中的 plantuml 代码块经本地代理渲染为 SVG"},
		{Key: "preview.plantuml_enabled", Label: "PlantUML 渲染", Type: "bool", Group: "预览", Default: true, Desc: "true=plantuml 代码块渲染为图（失败兜底显示源码）；false=按普通代码块展示"},
		// ── 思考策略（多轮思考触发 M1-M12 轮次面；与"模型"分组 thinking_length 参数面正交）──
		{Key: "thinking.policy", Label: "思考策略", Type: "select", Group: "思考策略", Default: "off", Enums: append([]string(nil), thinkingPolicyOptions...), Desc: "off=不干预（现状）；auto=复杂任务软触发；strict=强制验证轮并拦截收尾"},
		{Key: "thinking.min_think_rounds", Label: "最少思考轮数", Type: "number", Group: "思考策略", Default: 0, Min: f64p(0), Max: f64p(20), Step: 1, Desc: "复杂任务最少思考轮数（0=不强制；建议 3=首探+执行+验证）"},
		{Key: "thinking.max_think_rounds", Label: "思考轮上限", Type: "number", Group: "思考策略", Default: 3, Min: f64p(1), Max: f64p(20), Step: 1, Desc: "续轮注入总上限（拦截+nudge；工具执行轮不受限；0=未设置→运行时取 3）"},
		{Key: "thinking.verify_pass", Label: "验证轮策略", Type: "select", Group: "思考策略", Default: "off", Enums: append([]string(nil), verifyPassOptions...), Desc: "off=无验证轮；auto=复杂任务触发；on=收尾必验（含 simple）"},
		{Key: "thinking.complexity_threshold", Label: "复杂度阈值", Type: "number", Group: "思考策略", Default: 60, Min: f64p(0), Max: f64p(100), Step: 1, Desc: "复杂度评分阈值 0-100（≥阈值=复杂；0=未设置→运行时取 60）"},
		{Key: "thinking.nudge_enabled", Label: "续思提示", Type: "bool", Group: "思考策略", Default: false, Desc: "复杂任务续思提示注入（结构性催轮，不等模型自觉）"},
		{Key: "thinking.nudge_max", Label: "续思提示上限", Type: "number", Group: "思考策略", Default: 2, Min: f64p(0), Max: f64p(10), Step: 1, Desc: "nudge 单独上限（0=未设置→运行时取 2）"},
		{Key: "thinking.round_effort_policy", Label: "轮次档位策略", Type: "select", Group: "思考策略", Default: "none", Enums: append([]string(nil), roundEffortPolicyOptions...), Desc: "none=保持（现状）；escalate=验证轮升档"},
		{Key: "thinking.round_effort_cap", Label: "升档封顶", Type: "select", Group: "思考策略", Default: "max", Enums: append([]string(nil), roundEffortCapOptions...), Desc: "验证轮升档封顶（仅 escalate 生效）"},
	}
}

// Sanitized 返回脱敏后的扁平配置视图（key 与 schema 一致，api_key 打码）。
// model.* 字段一律从激活预设 ActiveModel 读取（models 为唯一事实来源）。
func (c *Config) Sanitized() map[string]any {
	m := map[string]any{}
	am := c.ActiveModel()
	m["model.endpoint"] = am.Endpoint
	m["model.api_key"] = maskKey(am.APIKey)
	m["model.max_context"] = am.MaxContext
	if am.TopP != nil {
		m["model.top_p"] = *am.TopP
	}
	if am.Temperature != nil {
		m["model.temperature"] = *am.Temperature
	}
	m["model.vision_model"] = am.VisionModel
	m["model.supports_vision"] = am.SupportsVision
	m["model.send_effort"] = true
	if am.SendEffort != nil {
		m["model.send_effort"] = *am.SendEffort
	}
	m["model.max_output_tokens"] = am.MaxOutputTokens
	m["model.max_input_tokens"] = am.MaxInputTokens
	m["model.thinking_length"] = am.ThinkingLength
	// ThinkingEnabled 为 nil（未显式配置）时输出 true，与 schema Default 一致
	m["model.thinking_enabled"] = true
	if am.ThinkingEnabled != nil {
		m["model.thinking_enabled"] = *am.ThinkingEnabled
	}
	// thinking.* 多轮思考触发策略（空串归一为运行时默认档；数值 0=未设置原样输出，
	// 归一由运行时 Normalize 负责——配置层不做非零默认写入，保证零回归）
	m["thinking.policy"] = thinkingNorm(c.Thinking.Policy, "off")
	m["thinking.min_think_rounds"] = c.Thinking.MinThinkRounds
	m["thinking.max_think_rounds"] = c.Thinking.MaxThinkRounds
	m["thinking.verify_pass"] = thinkingNorm(c.Thinking.VerifyPass, "off")
	m["thinking.complexity_threshold"] = c.Thinking.ComplexityThreshold
	m["thinking.nudge_enabled"] = c.Thinking.NudgeEnabled
	m["thinking.nudge_max"] = c.Thinking.NudgeMax
	m["thinking.round_effort_policy"] = thinkingNorm(c.Thinking.RoundEffortPolicy, "none")
	m["thinking.round_effort_cap"] = thinkingNorm(c.Thinking.RoundEffortCap, "max")
	m["harness.score_threshold"] = c.Harness.ScoreThreshold
	m["harness.decay_factor"] = c.Harness.DecayFactor
	m["harness.anchor_ratio"] = c.Harness.AnchorRatio
	m["project.root"] = c.Project.Root
	m["project.vocab"] = c.Project.Vocab
	m["acp.enabled"] = c.ACP.Enabled
	m["acp.port"] = c.ACP.Port
	m["plugins.enabled"] = c.Plugins.Enabled
	m["plugins.directory"] = c.Plugins.Directory
	m["plugins.sandbox.memory_limit_mb"] = c.Plugins.Sandbox.MemoryLimitMB
	m["plugins.sandbox.cpu_limit"] = c.Plugins.Sandbox.CPULimit
	m["overseer.pass_threshold"] = c.Overseer.PassThreshold
	m["overseer.node_auto_plan"] = c.Overseer.NodeAutoPlan
	m["overseer.test_tee"] = c.Overseer.TestTEE
	m["overseer.temp_doc_policy"] = c.Overseer.TempDocPolicy
	m["memory.directory"] = c.Memory.Directory
	m["memory.auto_title"] = c.Memory.AutoTitle
	m["memory.max_sessions"] = c.Memory.MaxSessions
	m["channels.webhook.enabled"] = c.Channels.Webhook.Enabled
	m["trace.export_endpoint"] = c.Trace.ExportEndpoint
	m["trace.export_batch"] = c.Trace.ExportBatch
	m["trace.export_interval"] = c.Trace.ExportInterval
	m["channels.webhook.token"] = c.Channels.Webhook.Token
	m["channels.cron.enabled"] = c.Channels.Cron.Enabled
	m["sandbox.enabled"] = c.Sandbox.Enabled
	m["sandbox.discard_on_exit"] = c.Sandbox.DiscardOnExit
	m["context.compact_ratio"] = c.Context.CompactRatio
	m["context.hard_ratio"] = c.Context.HardRatio
	m["context.safety_ratio"] = c.Context.SafetyRatio
	m["context.keep_recent"] = c.Context.KeepRecent
	m["cli_defaults.model"] = c.CLIDefaults.Model
	m["cli_defaults.effort"] = c.CLIDefaults.Effort
	m["cli_defaults.mode"] = c.CLIDefaults.Mode
	m["cli_defaults.threshold"] = c.CLIDefaults.Threshold
	m["cli_defaults.overseer_threshold"] = c.CLIDefaults.OverseerThreshold
	m["cli_defaults.key_newline"] = c.CLIDefaults.KeyNewline
	m["tools.approval_mode"] = c.Tools.ApprovalMode
	m["preview.plantuml_server"] = c.Preview.PlantUMLServer
	if c.Preview.PlantUMLEnabled != nil {
		m["preview.plantuml_enabled"] = *c.Preview.PlantUMLEnabled
	}
	return m
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 8 {
		return "****"
	}
	return k[:6] + "****" + k[len(k)-4:]
}

// ApplyPatch 应用扁平 key 配置补丁（GUI config.set 链路）。
// 特殊语义：
//   - model.api_key 传入脱敏值（含 "****"）时跳过，避免覆盖真实 Key；
//   - 未知 key 返回错误（防止前端/调用方拼写错误静默失败）。
func (c *Config) ApplyPatch(patch map[string]any) error {
	for key, val := range patch {
		if err := c.applyOne(key, val); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) applyOne(key string, val any) error {
	// model.* 字段写透到激活预设（models 为唯一事实来源；model 节仅存激活名）。
	// 无预设（旧配置防御）时直接写 model 节，ActiveModel 兜底读取。
	am := c.ActiveModel()
	writePreset := func(mutate func(*ModelPreset)) {
		name := strings.TrimSpace(c.ActiveModelName)
		for i := range c.Models {
			if strings.EqualFold(strings.TrimSpace(c.Models[i].Name), name) {
				mutate(&c.Models[i])
				return
			}
		}
		if len(c.Models) > 0 {
			mutate(&c.Models[0])
			return
		}
		// 防御：无预设时忽略写入（applyDefaults 保证 models 非空，不可达）
		_ = am
	}
	switch key {
	case "model.active":
		// 激活名只读：切换必须走模型预设下拉（model.switch，触发后端重建），
		// 直接改字段只会改指针、不重建后端（旧"第一个无反应"的根源）。
		return fmt.Errorf("model.active 为只读：请使用模型预设下拉切换（model.switch）")
	case "model.endpoint":
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.Endpoint, _ = val.(string)
			} // 无预设：忽略（applyDefaults 保证非空）
		})
		return nil
	case "model.api_key":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("%s 需要字符串", key)
		}
		if strings.Contains(s, "****") {
			return nil // 脱敏值 → 不覆盖
		}
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.APIKey = s
			} // 无预设：忽略
		})
		return nil
	case "model.max_context":
		return setIntPreset(c, key, val, func(m *ModelPreset, n int) {
			if m != nil {
				m.MaxContext = n
			} // 无预设：忽略
		})
	case "model.top_p":
		return setF64PtrPreset(c, key, val, func(m *ModelPreset, f *float64) {
			if m != nil {
				m.TopP = f
			} // 无预设：忽略
		})
	case "model.temperature":
		return setF64PtrPreset(c, key, val, func(m *ModelPreset, f *float64) {
			if m != nil {
				m.Temperature = f
			} // 无预设：忽略
		})
	case "model.vision_model":
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.VisionModel, _ = val.(string)
			} // 无预设：忽略
		})
		return nil
	case "model.supports_vision":
		b, _ := val.(bool)
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.SupportsVision = b
			} // 无预设：忽略
		})
		return nil
	case "model.send_effort":
		var v bool
		if err := setBool(&v, key, val); err != nil {
			return err
		}
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.SendEffort = &v
			} // 无预设：忽略
		})
		return nil
	case "model.max_output_tokens":
		return setIntPreset(c, key, val, func(m *ModelPreset, n int) {
			if m != nil {
				m.MaxOutputTokens = n
			} // 无预设：忽略
		})
	case "model.max_input_tokens":
		return setIntPreset(c, key, val, func(m *ModelPreset, n int) {
			if m != nil {
				m.MaxInputTokens = n
			} // 无预设：忽略
		})
	case "model.thinking_length":
		s, ok := val.(string)
		if !ok {
			return fmt.Errorf("%s 需要字符串", key)
		}
		if !validThinkingLength(s) {
			return fmt.Errorf("%s 非法取值 %q：合法取值 %v", key, s, thinkingLengthOptions)
		}
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.ThinkingLength = s
			} // 无预设：忽略
		})
		return nil
	case "model.thinking_enabled":
		var v bool
		if err := setBool(&v, key, val); err != nil {
			return err
		}
		writePreset(func(m *ModelPreset) {
			if m != nil {
				m.ThinkingEnabled = &v
			} // 无预设：忽略
		})
		return nil
	// thinking.* 多轮思考触发策略（枚举走 setEnum 校验；数值 setInt；布尔 setBool）
	case "thinking.policy":
		return setEnum(&c.Thinking.Policy, key, val, thinkingPolicyOptions)
	case "thinking.min_think_rounds":
		return setInt(&c.Thinking.MinThinkRounds, key, val)
	case "thinking.max_think_rounds":
		return setInt(&c.Thinking.MaxThinkRounds, key, val)
	case "thinking.verify_pass":
		return setEnum(&c.Thinking.VerifyPass, key, val, verifyPassOptions)
	case "thinking.complexity_threshold":
		return setInt(&c.Thinking.ComplexityThreshold, key, val)
	case "thinking.nudge_enabled":
		return setBool(&c.Thinking.NudgeEnabled, key, val)
	case "thinking.nudge_max":
		return setInt(&c.Thinking.NudgeMax, key, val)
	case "thinking.round_effort_policy":
		return setEnum(&c.Thinking.RoundEffortPolicy, key, val, roundEffortPolicyOptions)
	case "thinking.round_effort_cap":
		return setEnum(&c.Thinking.RoundEffortCap, key, val, roundEffortCapOptions)
	case "harness.score_threshold":
		return setF64(&c.Harness.ScoreThreshold, key, val)
	case "harness.decay_factor":
		return setF64(&c.Harness.DecayFactor, key, val)
	case "harness.anchor_ratio":
		return setF64(&c.Harness.AnchorRatio, key, val)
	case "project.root":
		return setStr(&c.Project.Root, key, val)
	case "project.vocab":
		return setStr(&c.Project.Vocab, key, val)
	case "acp.enabled":
		return setBool(&c.ACP.Enabled, key, val)
	case "acp.port":
		return setInt(&c.ACP.Port, key, val)
	case "plugins.enabled":
		return setBool(&c.Plugins.Enabled, key, val)
	case "plugins.directory":
		return setStr(&c.Plugins.Directory, key, val)
	case "plugins.sandbox.memory_limit_mb":
		return setInt(&c.Plugins.Sandbox.MemoryLimitMB, key, val)
	case "plugins.sandbox.cpu_limit":
		return setF64(&c.Plugins.Sandbox.CPULimit, key, val)
	case "overseer.pass_threshold":
		return setF64(&c.Overseer.PassThreshold, key, val)
	case "overseer.node_auto_plan":
		return setBool(&c.Overseer.NodeAutoPlan, key, val)
	case "overseer.test_tee":
		return setBool(&c.Overseer.TestTEE, key, val)
	case "overseer.temp_doc_policy":
		return setStr(&c.Overseer.TempDocPolicy, key, val)
	case "memory.directory":
		return setStr(&c.Memory.Directory, key, val)
	case "memory.auto_title":
		return setBool(&c.Memory.AutoTitle, key, val)
	case "memory.max_sessions":
		return setInt(&c.Memory.MaxSessions, key, val)
	case "channels.webhook.enabled":
		return setBool(&c.Channels.Webhook.Enabled, key, val)
	case "trace.export_endpoint":
		return setStr(&c.Trace.ExportEndpoint, key, val)
	case "trace.export_batch":
		return setInt(&c.Trace.ExportBatch, key, val)
	case "trace.export_interval":
		return setInt(&c.Trace.ExportInterval, key, val)
	case "channels.webhook.token":
		return setStr(&c.Channels.Webhook.Token, key, val)
	case "channels.cron.enabled":
		return setBool(&c.Channels.Cron.Enabled, key, val)
	case "sandbox.enabled":
		return setBool(&c.Sandbox.Enabled, key, val)
	case "sandbox.discard_on_exit":
		return setBool(&c.Sandbox.DiscardOnExit, key, val)
	case "context.compact_ratio":
		return setF64(&c.Context.CompactRatio, key, val)
	case "context.hard_ratio":
		return setF64(&c.Context.HardRatio, key, val)
	case "context.safety_ratio":
		return setF64(&c.Context.SafetyRatio, key, val)
	case "context.keep_recent":
		return setInt(&c.Context.KeepRecent, key, val)
	case "cli_defaults.model":
		return setStr(&c.CLIDefaults.Model, key, val)
	case "cli_defaults.effort":
		return setStr(&c.CLIDefaults.Effort, key, val)
	case "cli_defaults.mode":
		return setStr(&c.CLIDefaults.Mode, key, val)
	case "cli_defaults.threshold":
		return setF64(&c.CLIDefaults.Threshold, key, val)
	case "cli_defaults.overseer_threshold":
		return setF64(&c.CLIDefaults.OverseerThreshold, key, val)
	case "cli_defaults.key_newline":
		return setStr(&c.CLIDefaults.KeyNewline, key, val)
	case "tools.approval_mode":
		return setStr(&c.Tools.ApprovalMode, key, val)
	case "preview.plantuml_server":
		return setStr(&c.Preview.PlantUMLServer, key, val)
	case "preview.plantuml_enabled":
		b, ok := val.(bool)
		if !ok {
			return fmt.Errorf("%s 需要布尔值", key)
		}
		c.Preview.PlantUMLEnabled = &b
		return nil
	default:
		return fmt.Errorf("未知配置项: %s", key)
	}
}

func setStr(dst *string, key string, val any) error {
	s, ok := val.(string)
	if !ok {
		return fmt.Errorf("%s 需要字符串", key)
	}
	*dst = s
	return nil
}

func setBool(dst *bool, key string, val any) error {
	b, ok := val.(bool)
	if !ok {
		return fmt.Errorf("%s 需要布尔值", key)
	}
	*dst = b
	return nil
}

func setInt(dst *int, key string, val any) error {
	var n int
	switch v := val.(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		i, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s 需要整数", key)
		}
		n = i
	default:
		return fmt.Errorf("%s 需要整数", key)
	}
	*dst = n
	return nil
}

// setEnum 枚举写入：命中合法选项清单才写入（错误信息含全部合法取值）。
func setEnum(dst *string, key string, val any, opts []string) error {
	s, ok := val.(string)
	if !ok {
		return fmt.Errorf("%s 需要字符串", key)
	}
	if !inList(s, opts) {
		return fmt.Errorf("%s 非法取值 %q：合法取值 %v", key, s, opts)
	}
	*dst = s
	return nil
}

func setF64(dst *float64, key string, val any) error {
	f, err := toF64(val)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = f
	return nil
}

func setF64Ptr(dst **float64, key string, val any) error {
	f, err := toF64(val)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = &f
	return nil
}

// setIntPreset：解析 int 后写入预设字段（写透激活预设）。
func setIntPreset(c *Config, key string, val any, apply func(*ModelPreset, int)) error {
	var n int
	switch v := val.(type) {
	case float64:
		n = int(v)
	case int:
		n = v
	case string:
		i, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("%s 需要整数", key)
		}
		n = i
	default:
		return fmt.Errorf("%s 需要整数", key)
	}
	preset := c.activePresetPtr()
	apply(preset, n)
	return nil
}

// setF64PtrPreset：解析 *float64 后写入预设字段（写透激活预设）。
func setF64PtrPreset(c *Config, key string, val any, apply func(*ModelPreset, *float64)) error {
	f, err := toF64(val)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	preset := c.activePresetPtr()
	apply(preset, &f)
	return nil
}

// activePresetPtr 返回激活预设的可变指针（无预设时返回 nil，由调用方回退）。
func (c *Config) activePresetPtr() *ModelPreset {
	name := strings.TrimSpace(c.ActiveModelName)
	for i := range c.Models {
		if strings.EqualFold(strings.TrimSpace(c.Models[i].Name), name) {
			return &c.Models[i]
		}
	}
	if len(c.Models) > 0 {
		return &c.Models[0]
	}
	return nil
}

func toF64(val any) (float64, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("需要数字")
		}
		return f, nil
	default:
		return 0, fmt.Errorf("需要数字")
	}
}

// ModelNames 返回全部模型预设名（用于 /model 列出、--model 校验、GUI 下拉）。
func (c *Config) ModelNames() []string {
	names := make([]string, 0, len(c.Models))
	for _, m := range c.Models {
		if strings.TrimSpace(m.Name) != "" {
			names = append(names, m.Name)
		}
	}
	if len(names) == 0 {
		n := strings.TrimSpace(c.ActiveModelName)
		if n == "" {
			n = "default-model"
		}
		names = append(names, n)
	}
	return names
}

// migrateLegacyModel 迁移 v2.5.1 之前的旧 model: 配置节到新结构。
//
// 新配置彻底移除 model: 节（模型配置唯一定义在 models 列表，顶层
// active_model 仅存激活名）。若旧配置只有 model: 节（无 active_model、
// 无 models），自动迁移：激活名=model.backend，预设=model 节全部字段。
// 幂等：已迁移/新格式配置不执行任何操作。
func (c *Config) migrateLegacyModel(data []byte) {
	if c.ActiveModelName != "" || len(c.Models) > 0 {
		return // 已是新格式或已迁移
	}
	var legacy struct {
		Model *legacyModelConfig `yaml:"model"`
	}
	if err := yaml.Unmarshal(data, &legacy); err != nil || legacy.Model == nil {
		return
	}
	m := legacy.Model
	if strings.TrimSpace(m.Backend) == "" {
		return
	}
	c.ActiveModelName = m.Backend
	c.Models = []ModelPreset{{
		Name:           m.Backend,
		Backend:        m.Backend,
		Endpoint:       m.Endpoint,
		APIKey:         m.APIKey,
		MaxContext:     m.MaxContext,
		TopP:           m.TopP,
		Temperature:    m.Temperature,
		VisionModel:    m.VisionModel,
		SupportsVision: m.SupportsVision,
		SendEffort:     m.SendEffort,

		// 生成参数随迁移携带（避免旧配置升级后丢失输出/输入/思考长度设置）
		MaxOutputTokens: m.MaxOutputTokens,
		MaxInputTokens:  m.MaxInputTokens,
		ThinkingLength:  m.ThinkingLength,
		ThinkingEnabled: m.ThinkingEnabled,
	}}
}

func (c *Config) applyDefaults() {
	if c.ActiveModelName == "" {
		c.ActiveModelName = "default-model"
	}
	// models 预设列表为空时，内置默认预设（default-model）。
	// 保证 /model 与 GUI 下拉始终有可切换项；模型配置唯一定义在 models 列表。
	if len(c.Models) == 0 {
		se := true
		c.Models = []ModelPreset{{
			Name:           "default-model",
			Backend:        "openai-compatible",
			Endpoint:       "",
			APIKey:         "",
			MaxContext:     131072,
			TopP:           f64p(0.95),
			Temperature:    f64p(1.0),
			VisionModel:    "",
			SupportsVision: false,
			SendEffort:     &se,
		}}
	}
	// 激活名不在列表中时，回退到第一个预设（防配置漂移）
	found := false
	for i := range c.Models {
		if strings.EqualFold(strings.TrimSpace(c.Models[i].Name), strings.TrimSpace(c.ActiveModelName)) {
			found = true
			break
		}
	}
	if !found {
		c.ActiveModelName = c.Models[0].Name
	}
	// 补齐预设默认值（旧 model: 节迁移/用户省略字段场景）：
	// MaxContext 缺省 131072；TopP/Temperature/SendEffort/ThinkingEnabled 缺省指针。
	// MaxOutputTokens/MaxInputTokens 缺省 0（不限制，回退 MaxContext）、ThinkingLength
	// 缺省 ""（继承 cli_defaults.effort）——不做非零默认，保证行为零回归。
	for i := range c.Models {
		if c.Models[i].MaxContext == 0 {
			c.Models[i].MaxContext = 131072
		}
		if c.Models[i].TopP == nil {
			c.Models[i].TopP = f64p(0.95)
		}
		if c.Models[i].Temperature == nil {
			c.Models[i].Temperature = f64p(1.0)
		}
		if c.Models[i].SendEffort == nil {
			v := true
			c.Models[i].SendEffort = &v
		}
		if c.Models[i].ThinkingEnabled == nil {
			// nil=不显式发送 thinking 字段；缺省指针 true 与 schema Default 一致，
			// 显式 false（关闭思考）不被默认值覆盖。
			v := true
			c.Models[i].ThinkingEnabled = &v
		}
	}
	if c.Harness.ScoreThreshold == 0 {
		c.Harness.ScoreThreshold = 50
	}
	if c.Harness.DecayFactor == 0 {
		c.Harness.DecayFactor = 0.9
	}
	if c.Harness.AnchorRatio == 0 {
		c.Harness.AnchorRatio = 0.2
	}
	if c.ACP.Port == 0 {
		c.ACP.Port = 20250
	}
	if c.Overseer.PassThreshold == 0 {
		c.Overseer.PassThreshold = 90
	}
	if c.Memory.MaxSessions == 0 {
		c.Memory.MaxSessions = 1000
	}
	if c.Memory.Directory == "" {
		c.Memory.Directory = ".contextloom/memory"
	}
	if c.Plugins.Directory == "" {
		c.Plugins.Directory = "~/.contextloom/plugins"
	}
	// 布尔/零值默认对齐 config.example.yaml（GUI 设置面板展示值与 example 一致）
	if !c.Plugins.Enabled {
		c.Plugins.Enabled = true
	}
	if c.Plugins.Sandbox.MemoryLimitMB == 0 {
		c.Plugins.Sandbox.MemoryLimitMB = 256
	}
	if c.Preview.PlantUMLServer == "" {
		c.Preview.PlantUMLServer = "https://www.plantuml.com/plantuml"
	}
	if c.Preview.PlantUMLEnabled == nil {
		v := true
		c.Preview.PlantUMLEnabled = &v
	}
	if c.Plugins.Sandbox.CPULimit == 0 {
		c.Plugins.Sandbox.CPULimit = 0.5
	}
	if c.Overseer.TempDocPolicy == "" {
		c.Overseer.TempDocPolicy = "session_only"
	}
	if !c.Memory.AutoTitle {
		c.Memory.AutoTitle = true
	}
	if !c.ACP.Enabled {
		c.ACP.Enabled = true
	}
	// trace 导出默认（与 config.example.yaml 一致）
	if c.Trace.ExportBatch <= 0 {
		c.Trace.ExportBatch = 32
	}
	if c.Trace.ExportInterval <= 0 {
		c.Trace.ExportInterval = 5
	}
	if c.CLIDefaults.KeyNewline == "" {
		c.CLIDefaults.KeyNewline = "auto"
	}
	// 思考深度默认 max：未配置时使用最高推理强度（low/medium/high 均 remap 为 high，xhigh/max 为 max）
	if c.CLIDefaults.Effort == "" {
		c.CLIDefaults.Effort = "max"
	}
	// CLI 默认补齐（与 config.example.yaml 一致；仅在空值时填充，不覆盖显式配置）
	if c.CLIDefaults.Model == "" {
		c.CLIDefaults.Model = "default-model"
	}
	if c.CLIDefaults.Mode == "" {
		c.CLIDefaults.Mode = "chat"
	}
	if c.CLIDefaults.Threshold == 0 {
		c.CLIDefaults.Threshold = 50
	}
	if c.CLIDefaults.OverseerThreshold == 0 {
		c.CLIDefaults.OverseerThreshold = 90
	}
	// 审批预设默认 standard（chat 模式标准谨慎；task 模式自动批准独立于该设置）
	if c.Tools.ApprovalMode == "" {
		c.Tools.ApprovalMode = "standard"
	}
	if c.Context.CompactRatio == 0 {
		c.Context.CompactRatio = 0.70
	}
	if c.Context.HardRatio == 0 {
		c.Context.HardRatio = 0.85
	}
	if c.Context.SafetyRatio == 0 {
		c.Context.SafetyRatio = 0.92
	}
	if c.Context.KeepRecent == 0 {
		c.Context.KeepRecent = 8
	}
	// 保证阈值单调：compact < hard < safety
	if c.Context.CompactRatio >= c.Context.HardRatio {
		c.Context.HardRatio = c.Context.CompactRatio + (1-c.Context.CompactRatio)/2
	}
	if c.Context.HardRatio >= c.Context.SafetyRatio {
		c.Context.SafetyRatio = c.Context.HardRatio + (1-c.Context.HardRatio)/2
	}
	// thinking.* 不做默认写入：零值=off/现状（空串归一由运行时 Normalize 负责，保证零回归）。
}
