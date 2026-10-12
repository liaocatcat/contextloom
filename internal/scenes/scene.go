// Package scenes 实现《工具链体系升级方案》§4.4「场景中心」：
// 意图解析（两级漏斗）+ 场景实例化 + 一键执行（M4 里程碑核心包体）。
//
// 核心概念：
//
//   - Scene：具名场景定义，可由 .contextloom/scenes/<name>/scene.yaml 反序列化，
//     字段覆盖 keywords/intent_prompt（意图）、team/models/toolchain/approval（装配）、
//     output（产出交付）、verify（验收）、triggers（定时触发）；
//   - Match：两级漏斗意图解析——规则层（keywords 大小写不敏感包含匹配，
//     确定性优先）→ 模型层（IntentClassifier 注入，逐场景调用 intent_prompt）；
//     全未命中返回 nil，调用方兜底退回 chat 普通模式，绝不阻塞；
//   - Instantiate：场景 + 触发上下文 → Instance，Params 携带 {{date}}/{{time}}/
//     {{user_input}}/{{scene}} 模板变量；output.path 渲染后经路径安全校验
//     （拒绝绝对路径与 .. 逃逸）；
//   - Run：一键执行，Runner 由调用方注入（runtime 用 goalmgr/overseer 包装），
//     ctx 取消返回可读错误且 Status=canceled；
//   - LoadAll：扫描场景目录（每个子目录 = 一个场景，scene.yaml 为入口）。
//
// 解耦设计：本包不 import internal/runtime、internal/goalmgr、internal/executor、
// internal/orchestrator、internal/modelgateway、internal/toolchain、internal/preprocessor。
// 意图分类与执行动作均经函数类型注入，包内只做解析/匹配/实例化/编排语义。
// 仅依赖标准库与 gopkg.in/yaml.v3，跨平台，无 cgo。
package scenes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Scene 表示一个场景定义（对应 .contextloom/scenes/<name>/scene.yaml）。
// 字段语义见《工具链体系升级方案》§4.4.1：
//
//   - Name：唯一场景名（必填），同时作为目录名；
//   - Display：人性化展示名（GUI 场景卡片标题）；
//   - Keywords：意图匹配规则层关键词表（大小写不敏感，命中即返回，确定性优先）；
//   - IntentPrompt：意图匹配模型层提示词（规则层未命中时才经 IntentClassifier 调用）；
//   - Team：装配的员工/团队（§4.1 orchestrator 档案名）；
//   - Models.Policy：模型策略（§4.2，临时路由规则，优先级低于 gateway.rules）；
//   - Toolchain：工具链名（§4.3，一条链 = 一段流水线）；
//   - Approval：审批安全档位（复用 standard/permissive/strict 三档预设）；
//   - Output：产出交付（路径支持 {{date}}/{{time}}/{{user_input}} 等模板变量；
//     AutoLoad 为 true 时产出完成后由 GUI 自动加载）；
//   - Verify：验收器名（默认 overseer 监工评分）；
//   - Triggers.Cron：可选 cron 表达式（复用现有 cron 通道自动触发）。
type Scene struct {
	Name         string       `yaml:"name" json:"name"`
	Display      string       `yaml:"display" json:"display,omitempty"`
	Keywords     []string     `yaml:"keywords" json:"keywords,omitempty"`
	IntentPrompt string       `yaml:"intent_prompt" json:"intent_prompt,omitempty"`
	Team         string       `yaml:"team" json:"team,omitempty"`
	Models       ModelsPolicy `yaml:"models" json:"models,omitempty"`
	Toolchain    string       `yaml:"toolchain" json:"toolchain,omitempty"`
	Approval     string       `yaml:"approval" json:"approval,omitempty"`
	Output       OutputSpec   `yaml:"output" json:"output,omitempty"`
	Verify       string       `yaml:"verify" json:"verify,omitempty"`
	Triggers     TriggersSpec `yaml:"triggers" json:"triggers,omitempty"`
}

// ModelsPolicy 场景级模型策略（对应 scene.yaml 的 models 节）。
type ModelsPolicy struct {
	// Policy 路由策略表达式，如 "budget=low"、"role=product"。
	Policy string `yaml:"policy" json:"policy,omitempty"`
}

// OutputSpec 场景产出交付配置（对应 scene.yaml 的 output 节）。
type OutputSpec struct {
	// Path 产出文件路径（相对工作目录），支持 {{date}}/{{time}}/{{user_input}} 等模板变量。
	Path string `yaml:"path" json:"path,omitempty"`
	// AutoLoad 为 true 时产出完成后由 GUI 自动加载展示。
	AutoLoad bool `yaml:"autoload" json:"autoload,omitempty"`
}

// TriggersSpec 场景自动触发配置（对应 scene.yaml 的 triggers 节）。
type TriggersSpec struct {
	// Cron 标准 5 段 cron 表达式（分 时 日 月 周），如 "0 17 * * 5"（每周五 17:00）。
	Cron string `yaml:"cron" json:"cron,omitempty"`
}

// Parse 从 YAML 字节反序列化场景定义，并做基础校验（name 必填）。
func Parse(data []byte) (*Scene, error) {
	var s Scene
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("scenes: 解析场景定义失败：%w", err)
	}
	if strings.TrimSpace(s.Name) == "" {
		return nil, fmt.Errorf("scenes: 场景定义缺少必填字段 name")
	}
	return &s, nil
}

// Load 从目录或文件加载场景定义：
//
//   - path 为目录时加载 <path>/scene.yaml（对应 .contextloom/scenes/<name>/ 布局）；
//   - path 为文件时直接加载该文件。
func Load(path string) (*Scene, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("scenes: 读取场景 %q 失败：%w", path, err)
	}
	if info.IsDir() {
		path = filepath.Join(path, "scene.yaml")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scenes: 读取场景文件 %q 失败：%w", path, err)
	}
	return Parse(data)
}

// Marshal 将场景定义序列化为 YAML 字节（Parse 的逆操作，保证 YAML 往返）。
func (s Scene) Marshal() ([]byte, error) {
	return yaml.Marshal(s)
}
