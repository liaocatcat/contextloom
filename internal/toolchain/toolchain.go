// Package toolchain 实现《工具链体系升级方案》§4.3「工具链工厂」：
// 可声明、可校验、可并行的多步骤工具链执行引擎（M2 里程碑核心包体）。
//
// 核心概念：
//
//   - Toolchain：具名多步骤流水线，可由 <catalog>/<name>/toolchain.yaml 反序列化；
//   - Step 三类：tool（调用工具）、llm（调用大模型）、ref（引用上游产物字段或嵌套子链）；
//   - DAG 调度：depends_on 声明依赖（缺省 = 依赖全部前置步骤），无依赖步骤并行执行；
//   - 产物协议：Artifacts 提供 <stepID> 整体产物与 <stepID>.<field> 字段级产物，
//     params/prompt 中可用 "$ref: step.field"、"$step.field"、"{{ts}}"、"{{param.x}}" 引用；
//   - 静态校验：Validate 覆盖环检测、缺失依赖、未知步骤类型、id 重复、未知工具名等；
//   - 重试与嵌套：Step.Retry 配置整步最大尝试次数；ref 类步骤可引用子链
//     （RunOptions.SubChains 注入，键为链名，子链产物以 "<refStepID>.<key>" 前缀并入）。
//
// 解耦设计：本包不 import internal/runtime、internal/model、internal/modelgateway。
// 工具调用与 LLM 调用分别经 RunOptions.Tools（ToolExecutor）与 RunOptions.LLM
// （LLMRunner）注入，由调用方（runtime 用 tools.Registry 与模型网关包装）提供实现。
// 仅依赖标准库与 gopkg.in/yaml.v3，跨平台，无 cgo。
package toolchain

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// 步骤类型常量（Step.Type；缺省时按 tool/prompt/$ref 字段自动推断）。
const (
	StepTypeTool = "tool"
	StepTypeLLM  = "llm"
	StepTypeRef  = "ref"
)

// Toolchain 表示一条具名工具链（对应 <catalog>/<name>/toolchain.yaml）。
type Toolchain struct {
	Name    string  `yaml:"name" json:"name"`
	Version string  `yaml:"version" json:"version,omitempty"`
	Params  []Param `yaml:"params" json:"params,omitempty"`
	Steps   []Step  `yaml:"steps" json:"steps"`
}

// Param 声明链级参数：由调用方经 RunOptions.ParamsRun 注入，
// 步骤参数与提示词中用 "{{param.name}}" 引用。
type Param struct {
	Name     string `yaml:"name" json:"name"`
	Required bool   `yaml:"required" json:"required,omitempty"`
	Desc     string `yaml:"desc" json:"desc,omitempty"`
}

// Depends 声明步骤依赖。语义：
//
//   - nil（字段缺失/null）：缺省 = 依赖链中全部前置步骤；
//   - 非 nil 空数组（depends_on: []）：显式无依赖，可与前置步骤并行；
//   - 非空数组：仅依赖列出的步骤。
//
// 自定义 Marshal/Unmarshal 保证上述 nil/空 区别可经 YAML 往返不丢失
// （omitempty 依赖 IsZeroer：nil 时省略字段，空数组时输出 "depends_on: []"）。
type Depends []string

// IsZero 供 yaml omitempty 判断：nil 视为零值（字段省略），空数组不是零值。
func (d Depends) IsZero() bool { return d == nil }

// MarshalYAML 序列化：nil → 省略（由 IsZero+omitempty 处理，兜底输出 null 亦可被读取回 nil）。
func (d Depends) MarshalYAML() (any, error) {
	if d == nil {
		return nil, nil
	}
	return []string(d), nil
}

// UnmarshalYAML 反序列化：null/缺失 → nil；空数组 → 非 nil 空切片（显式无依赖）。
func (d *Depends) UnmarshalYAML(node *yaml.Node) error {
	if node.Tag == "!!null" || node.Kind == yaml.ScalarNode && node.Value == "" {
		*d = nil
		return nil
	}
	var ss []string
	if err := node.Decode(&ss); err != nil {
		return err
	}
	*d = Depends(ss)
	return nil
}

// Step 表示工具链中的一个步骤。三类步骤（type 可省略，按字段自动推断）：
//
//   - tool：Tool 为工具名，Params 为参数（值支持 "$ref: step.field"、"$step.field"
//     产物引用与 "{{param.x}}"、"{{ts}}" 模板）；
//   - llm ：Prompt 为提示词（同样支持引用与模板），RoleHint 为可选角色提示；
//   - ref ：Ref 为引用目标——"step.field" 引用上游步骤产物字段；"chain-name"（不含点）
//     引用子链（由 RunOptions.SubChains 注入，子链产物以 "<stepID>.<key>" 前缀并入本链）。
//
// DependsOn 语义见 Depends。Retry 为最大尝试次数（<=0 或缺省时按 1 次，即不重试）。
type Step struct {
	ID        string         `yaml:"id" json:"id"`
	Type      string         `yaml:"type" json:"type,omitempty"`
	Tool      string         `yaml:"tool" json:"tool,omitempty"`
	Params    map[string]any `yaml:"params" json:"params,omitempty"`
	Prompt    string         `yaml:"prompt" json:"prompt,omitempty"`
	RoleHint  string         `yaml:"role_hint" json:"role_hint,omitempty"`
	Ref       string         `yaml:"ref" json:"ref,omitempty"`
	DependsOn Depends        `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Retry     int            `yaml:"retry" json:"retry,omitempty"`
}

// UnmarshalYAML 兼容两种 YAML 写法（方案 §4.3.1 官方格式与扁平格式）：
//
//   - 官方格式：{ id: diff, tool: git_diff, args: { path: "..." } }
//     { id: analyze, llm: { role_hint: "...", prompt: "..." } }
//     { id: publish, toolchain: weekly-report-chain }   # 子链（等价 ref）
//   - 扁平格式：{ id: diff, type: tool, tool: git_diff, params: { ... } }
//     { id: analyze, type: llm, role_hint: ..., prompt: ... }
//     { id: publish, type: ref, ref: weekly-report-chain }
//
// 两者可混用；显式 type 与 params/prompt/ref 字段优先于嵌套写法。
func (s *Step) UnmarshalYAML(node *yaml.Node) error {
	var raw struct {
		ID        string         `yaml:"id"`
		Type      string         `yaml:"type"`
		Tool      string         `yaml:"tool"`
		Params    map[string]any `yaml:"params"`
		Args      map[string]any `yaml:"args"`
		Prompt    string         `yaml:"prompt"`
		RoleHint  string         `yaml:"role_hint"`
		Ref       string         `yaml:"ref"`
		Toolchain string         `yaml:"toolchain"`
		LLM       struct {
			RoleHint string `yaml:"role_hint"`
			Prompt   string `yaml:"prompt"`
		} `yaml:"llm"`
		DependsOn Depends `yaml:"depends_on"`
		Retry     int     `yaml:"retry"`
		When      string  `yaml:"when"` // 方案保留字段：当前执行器不解析，仅解析不报错
	}
	if err := node.Decode(&raw); err != nil {
		return err
	}
	s.ID = raw.ID
	s.Type = raw.Type
	s.Tool = raw.Tool
	s.Retry = raw.Retry
	s.DependsOn = raw.DependsOn
	switch {
	case raw.Params != nil:
		s.Params = raw.Params
	case raw.Args != nil:
		s.Params = raw.Args
	}
	if raw.LLM.RoleHint != "" || raw.LLM.Prompt != "" {
		s.RoleHint = raw.LLM.RoleHint
		s.Prompt = raw.LLM.Prompt
	} else {
		s.RoleHint = raw.RoleHint
		s.Prompt = raw.Prompt
	}
	if raw.Ref != "" {
		s.Ref = raw.Ref
	} else {
		s.Ref = raw.Toolchain
	}
	return nil
}

// InferredType 返回步骤有效类型：显式 Type 优先，否则按 Tool/Prompt/Ref 字段推断。
func (s *Step) InferredType() string {
	if s.Type != "" {
		return s.Type
	}
	switch {
	case s.Tool != "":
		return StepTypeTool
	case s.Prompt != "":
		return StepTypeLLM
	case s.Ref != "":
		return StepTypeRef
	}
	return ""
}

// MaxAttempts 返回最大尝试次数（>=1；未配置或非法值时按默认 1）。
func (s *Step) MaxAttempts() int {
	if s.Retry <= 0 {
		return 1
	}
	return s.Retry
}

// ParseYAML 从 YAML 文本反序列化工具链定义。
func ParseYAML(data []byte) (*Toolchain, error) {
	var c Toolchain
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse toolchain yaml: %w", err)
	}
	return &c, nil
}

// Marshal 将工具链定义序列化为 YAML 字节（ParseYAML 的逆操作，保证 YAML 往返）。
func (c *Toolchain) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal toolchain %q: %w", nameOr(c.Name), err)
	}
	return data, nil
}

// Load 从 catalogDir 读取 <catalogDir>/<name>/toolchain.yaml；若定义未写 name 则以目录名回填。
func Load(catalogDir, name string) (*Toolchain, error) {
	data, err := os.ReadFile(filepath.Join(catalogDir, name, "toolchain.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load toolchain %q: %w", name, err)
	}
	c, err := ParseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("load toolchain %q: %w", name, err)
	}
	if strings.TrimSpace(c.Name) == "" {
		c.Name = name
	}
	return c, nil
}

// nameOr 返回链名或占位符。
func nameOr(name string) string {
	if strings.TrimSpace(name) == "" {
		return "<unnamed>"
	}
	return name
}

// ExampleChains 返回内置示例工具链（市场种子数据 + 测试基准）。
// 工具名（git_diff/write_file/git_log）由调用方注入的执行器实现。
func ExampleChains() map[string]Toolchain {
	return map[string]Toolchain{
		"code-review-chain": {
			Name:    "code-review-chain",
			Version: "1.0.0",
			Params:  []Param{{Name: "path", Required: true, Desc: "审查目标路径"}},
			Steps: []Step{
				{ID: "diff", Type: StepTypeTool, Tool: "git_diff", Params: map[string]any{"path": "{{param.path}}"}},
				{
					ID: "review", Type: StepTypeLLM, RoleHint: "资深代码审查专家",
					Prompt: "请审查以下 diff，输出 yaml（report: 审查结论；score: 0-100 打分）：\n$ref: diff",
				},
				{
					ID: "report", Type: StepTypeTool, Tool: "write_file",
					Params: map[string]any{"path": "review-{{ts}}.md", "content": "$ref: review.report"},
				},
			},
		},
		"weekly-report-chain": {
			Name:    "weekly-report-chain",
			Version: "1.0.0",
			Steps: []Step{
				{ID: "collect", Type: StepTypeTool, Tool: "git_log", Params: map[string]any{"since": "7.days.ago"}},
				{
					ID: "write", Type: StepTypeLLM, RoleHint: "文档助手",
					Prompt: "根据以下提交日志写一份周报：\n$ref: collect",
				},
				{
					ID: "deliver", Type: StepTypeTool, Tool: "write_file",
					Params: map[string]any{"path": "weekly-{{ts}}.md", "content": "$ref: write"},
				},
			},
		},
		// 开发流水线：写代码 → 跑测试 → 审查（§4.3 内置 5 条链之三）
		"code-test-review-chain": {
			Name:    "code-test-review-chain",
			Version: "1.0.0",
			Params:  []Param{{Name: "path", Required: true, Desc: "改动目标路径"}},
			Steps: []Step{
				{ID: "diff", Type: StepTypeTool, Tool: "git_diff", Params: map[string]any{"path": "{{param.path}}"}},
				{
					ID: "impl", Type: StepTypeLLM, RoleHint: "资深开发工程师",
					Prompt: "根据以下 diff 完善实现（保持行为正确、边界完备）：\n$ref: diff",
				},
				{ID: "test", Type: StepTypeTool, Tool: "run_tests", Params: map[string]any{"target": "{{param.path}}"}},
				{
					ID: "review", Type: StepTypeLLM, RoleHint: "代码审查专家",
					Prompt: "审查以下改动与测试结果，输出 yaml（report: 结论；score: 0-100）：\n改动:\n$ref: diff\n测试:\n$ref: test",
				},
			},
		},
		// PR 审查流水线（§4.3 内置 5 条链之四）
		"pr-review-chain": {
			Name:    "pr-review-chain",
			Version: "1.0.0",
			Params:  []Param{{Name: "path", Required: true, Desc: "PR 目标路径"}},
			Steps: []Step{
				{ID: "diff", Type: StepTypeTool, Tool: "git_diff", Params: map[string]any{"path": "{{param.path}}"}},
				{
					ID: "review", Type: StepTypeLLM, RoleHint: "PR 审查专家",
					Prompt: "请按以下维度审查该 PR diff 并输出 yaml（summary: 摘要；issues: 问题列表；score: 0-100）：\n正确性/安全性/性能/可维护性\n$ref: diff",
				},
				{
					ID: "report", Type: StepTypeTool, Tool: "write_file",
					Params: map[string]any{"path": "pr-review-{{ts}}.md", "content": "$ref: review.summary"},
				},
			},
		},
		// 缺陷修复流水线：定位 → 修复 → 验证（§4.3 内置 5 条链之五）
		"bug-fix-chain": {
			Name:    "bug-fix-chain",
			Version: "1.0.0",
			Params:  []Param{{Name: "path", Required: true, Desc: "缺陷所在路径"}},
			Steps: []Step{
				{ID: "src", Type: StepTypeTool, Tool: "read_file", Params: map[string]any{"path": "{{param.path}}"}},
				{
					ID: "analyze", Type: StepTypeLLM, RoleHint: "缺陷定位专家",
					Prompt: "分析以下代码找出缺陷根因，输出 yaml（root_cause: 根因；fix_plan: 修复方案）：\n$ref: src",
				},
				{ID: "fix", Type: StepTypeLLM, RoleHint: "修复工程师", Prompt: "按方案修复并输出修复后的完整代码：\n$ref: analyze"},
				{ID: "verify", Type: StepTypeTool, Tool: "run_tests", Params: map[string]any{"target": "{{param.path}}"}},
			},
		},
	}
}
