package plugin

// Manifest 插件清单（manifest.yaml）。
// v2 扩展：hooks.message / hooks.route（脚本形态的动态注入钩子），
// tools 由声明性元数据升级为真实注册（script 字段指向脚本实现）。
type Manifest struct {
	Name    string    `yaml:"name"`
	Version string    `yaml:"version"`
	Stages  []string  `yaml:"stages"`
	Tools   []ToolDef `yaml:"tools"`
	Hooks   HookDef   `yaml:"hooks"`
}

// ToolDef 插件工具定义。
// v2：parameters 升级为完整 JSON Schema（any 而非 map[string]string），
// 新增 script（脚本实现路径，相对插件目录）与 timeout（可选，默认 30s）。
// 声明 tools 的插件会在加载时真实注册进工具 Registry（RegisterExtra），
// 模型可调用；调用时参数 JSON 经 stdin 传入脚本，stdout 为结果。
type ToolDef struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Parameters  map[string]any `yaml:"parameters"`
	Script      string         `yaml:"script"`
	Timeout     int            `yaml:"timeout"` // 秒；0 = 默认 30s
}

// HookDef 钩子定义。
// v2：新增 Message（每轮消息后注入）与 Route（路由决策）。
type HookDef struct {
	Pre     HookStageDef `yaml:"pre"`
	During  HookStageDef `yaml:"during"`
	Post    HookStageDef `yaml:"post"`
	Message MessageHook  `yaml:"message"`
	Route   RouteHook    `yaml:"route"`
}

// MessageHook 每轮消息后注入钩子。
// inject 脚本在每轮用户消息后同步执行：
//
//	stdin  = JSON {input, mode, round, model}
//	stdout = 提示词段（原样注入下一轮请求，随用户消息之后）；空 = 不注入
type MessageHook struct {
	Inject string `yaml:"inject"`
}

// RouteHook 路由决策钩子（对标 dsh 思维模式路由）。
// script 在 agent 构建提示词时执行一次：
//
//	stdin  = JSON {mode, input, model, round}
//	stdout = JSON 决策（至少含 prompt 字段，注入 system 区）；空/失败 = 不注入
type RouteHook struct {
	Script string `yaml:"script"`
}

// HookStageDef 阶段钩子定义（pre/during/post 保持原样）。
type HookStageDef struct {
	PromptInject string `yaml:"prompt_inject"`
	PreTool      string `yaml:"pre_tool"`
	PostTool     string `yaml:"post_tool"`
	Finalize     string `yaml:"finalize"`
}

func (m *Manifest) HasStage(stage string) bool {
	for _, s := range m.Stages {
		if s == stage {
			return true
		}
	}
	return false
}

func (m *Manifest) GetPreInject() string {
	return m.Hooks.Pre.PromptInject
}

// HasMessageHook 是否声明了每轮消息后注入钩子。
func (m *Manifest) HasMessageHook() bool {
	return m.Hooks.Message.Inject != ""
}

// HasRouteHook 是否声明了路由决策钩子。
func (m *Manifest) HasRouteHook() bool {
	return m.Hooks.Route.Script != ""
}

// ToolTimeout 返回工具脚本超时（秒），0 表示使用默认 30s。
func (t *ToolDef) ToolTimeout() int {
	if t.Timeout > 0 {
		return t.Timeout
	}
	return 0
}
