// Package orchestrator implements the virtual-employee scheduling center
// (M3 milestone, upgrade-plan §4.1): employee profiles, team DAG plans,
// dependency resolution with a live ready queue, and a topological Driver.
//
// The scheduling kernel is self-contained (plain goroutines + sync); models,
// tools and toolchains are injected through the NodeExecutor function
// interface, keeping the package fully decoupled from the rest of the
// program. Only the standard library and gopkg.in/yaml.v3 are used.
package orchestrator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// EmployeeFileName is the canonical profile file name inside an employee
// directory (<employees>/<name>/employee.yaml).
const EmployeeFileName = "employee.yaml"

// Employee is a persistent virtual-employee profile (§4.1.1). It gives an
// agent a stable identity — persona, model binding, tool permissions, memory
// slice, approval preset and resident skills — instead of a per-task
// directive.
//
// ## 深入本体设计（M3+ 增强，本字段组的由来）
// 原始员工档案只有 name/role/persona/tools/approval/skills 几条提示词，
// 过于简单粗暴：无法承载"员工本体能力"——即员工为何擅长其本职（专精领域、
// 行为规范、约束、经验沉淀）以及其可复用的工作区知识库。
//
// 新增的深入本体字段把"提示词描述"升级为"可蒸馏的本体能力库"：
//   - Expertise    —— 专精领域清单（角色能力的结构化声明）
//   - Workflow     —— 结构化工作流步骤（比 persona 一段话更可执行的行为规范）
//   - Constraints  —— 硬性行为约束（必须遵守/必须避免，与 persona 叠加不冲突）
//   - Experience   —— 经验沉淀（跨任务复用：踩过的坑、沉淀出的金规）
//   - OutputFormat —— 产出格式要求（交付物应有的结构/规格）
//   - Ontology     —— 本体能力注入文本：从员工工作区蒸馏出的能力说明书
//     （可由 OntologyFile 指定从 workspace/ONTOLOGY.md 读取；
//     执行时原样注入子 Agent 上下文，即"本体能力蒸馏"）
//   - OntologyFile —— 本体能力说明书的文件路径（相对员工目录，默认 workspace/ONTOLOGY.md）
//   - Workspace    —— 员工专属工作区参考目录（相对员工目录或其相对项目根），
//     存放参考代码/风格指南/过往成果/技能资料，作为能力蒸馏的语料源
type Employee struct {
	Name          string       `yaml:"name"`
	Display       string       `yaml:"display"`
	Role          string       `yaml:"role"`
	Persona       string       `yaml:"persona"`
	Expertise     []string     `yaml:"expertise,omitempty"`
	Workflow      []string     `yaml:"workflow,omitempty"`
	Constraints   []string     `yaml:"constraints,omitempty"`
	Experience    []string     `yaml:"experience,omitempty"`
	OutputFormat  string       `yaml:"output_format,omitempty"`
	Ontology      string       `yaml:"ontology,omitempty"`
	OntologyFile  string       `yaml:"ontology_file,omitempty"`
	Workspace     string       `yaml:"workspace,omitempty"`
	Model         ModelBinding `yaml:"model"`
	ToolAllowlist []string     `yaml:"tool_allowlist"`
	ToolDenylist  []string     `yaml:"tool_denylist"`
	Memory        MemorySlice  `yaml:"memory"`
	Approval      string       `yaml:"approval"`
	Skills        []string     `yaml:"skills"`
}

// DefaultOntologyFileName is the canonical ontology capability doc file name
// inside an employee workspace (<employees>/<name>/workspace/ONTOLOGY.md).
const DefaultOntologyFileName = "ONTOLOGY.md"

// DefaultWorkspaceName is the canonical employee workspace sub-directory name
// (<employees>/<name>/workspace/) holding the ontology distillation corpus.
// Used as the fallback probe path when a profile doesn't declare a workspace.
const DefaultWorkspaceName = "workspace"

// OntologyPath returns the resolved ontology capability doc file path.
// Priority: explicit OntologyFile > default <workspace>/ONTOLOGY.md.
// Returns "" when neither is configured. The returned path is relative to
// the employee directory when it doesn't start with a separator or drive
// letter (treated as workspace-relative automatically by the caller's
// dir resolution).
func (e *Employee) OntologyPath() string {
	if e.OntologyFile != "" {
		return e.OntologyFile
	}
	ws := e.Workspace
	if ws == "" {
		// 未声明 workspace 时按默认约定探测（与 runtime 回退一致）
		ws = DefaultWorkspaceName
	}
	return filepath.Join(ws, DefaultOntologyFileName)
}

// EffectiveOntology merges inline Ontology text with the ontology doc file.
// The caller resolves file content; inline text always takes precedence for
// the trailing synthesis. Returned string is the final capability doc text.
func (e *Employee) EffectiveOntology(fileText string) string {
	if e.Ontology == "" {
		return fileText
	}
	if fileText == "" {
		return e.Ontology
	}
	return e.Ontology + "\n\n" + fileText
}

// buildList renders a section label + newline-joined list, omitting empty.
func buildList(label string, items []string) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(label + ":\n")
	for _, it := range items {
		b.WriteString("• " + it + "\n")
	}
	return b.String()
}

// DeepProfile compiles the employee's deep ontology into a single injectable
// text (§深入本体设计). It assembles role/persona/expertise/workflow/
// constraints/experience/output_format into a structured capability profile
// that is injected into the sub-agent's context before execution — the
// "ontology distillation" that turns a few prompt words into a usable body
// of expertise. ontologyDocText is the optional workspace-distilled doc
// content (already merged via EffectiveOntology by the caller); when given it
// is appended as the authoritative capability reference.
func (e *Employee) DeepProfile(ontologyDocText string) string {
	var b strings.Builder
	b.WriteString("【员工本体 · " + e.Name + "】")
	if e.Display != "" {
		b.WriteString("（" + e.Display + "）")
	}
	b.WriteString("\n")
	if e.Role != "" {
		b.WriteString("岗位职责: " + e.Role + "\n")
	}
	if e.Persona != "" {
		b.WriteString("工作风格: " + e.Persona + "\n")
	}
	if s := buildList("专精领域", e.Expertise); s != "" {
		b.WriteString(s)
	}
	if s := buildList("标准工作流", e.Workflow); s != "" {
		b.WriteString(s)
	}
	if s := buildList("行为约束", e.Constraints); s != "" {
		b.WriteString(s)
	}
	if s := buildList("经验沉淀", e.Experience); s != "" {
		b.WriteString(s)
	}
	if e.OutputFormat != "" {
		b.WriteString("产出格式: " + e.OutputFormat + "\n")
	}
	// 本体能力说明书（工作区蒸馏文档）作为权威能力引用追加
	if ontologyDocText != "" {
		b.WriteString("\n【本体能力说明书（来自员工工作区蒸馏）】\n" + ontologyDocText + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// MergeDeepDefaults fills in missing deep-ontology fields from a builtin
// template, without overwriting any field the user's profile already defines.
// This is the migration path for old profiles (seeded before the M3+ deep
// ontology existed): they keep their user-customized name/role/persona/tools/
// model/approval, but gain the builtin expertise/workflow/constraints/
// experience/output_format/workspace defaults when absent.
//
//   - Only fields that are empty in the target (e) get filled from the template.
//   - Returns a copy; e is not mutated.
//   - When template for the employee's name doesn't exist, returns e unchanged.
func (e *Employee) MergeDeepDefaults() *Employee {
	if e == nil {
		return nil
	}
	var tpl *Employee
	for _, t := range ExampleEmployees() {
		if t.Name == e.Name {
			tt := t
			tpl = &tt
			break
		}
	}
	if tpl == nil {
		return e // 无模板 → 原样返回（用户自定义员工）
	}
	out := *e
	if len(out.Expertise) == 0 {
		out.Expertise = append([]string(nil), tpl.Expertise...)
	}
	if len(out.Workflow) == 0 {
		out.Workflow = append([]string(nil), tpl.Workflow...)
	}
	if len(out.Constraints) == 0 {
		out.Constraints = append([]string(nil), tpl.Constraints...)
	}
	if len(out.Experience) == 0 {
		out.Experience = append([]string(nil), tpl.Experience...)
	}
	if out.OutputFormat == "" {
		out.OutputFormat = tpl.OutputFormat
	}
	if out.Ontology == "" {
		out.Ontology = tpl.Ontology
	}
	if out.OntologyFile == "" {
		out.OntologyFile = tpl.OntologyFile
	}
	if out.Workspace == "" {
		out.Workspace = tpl.Workspace
	}
	return &out
}

// ModelBinding binds an employee to a model preset. An empty Preset means
// "let the model gateway route" (no explicit binding).
type ModelBinding struct {
	Preset string `yaml:"preset"`
}

// MemorySlice points the employee at a persistent memory slice (reused by
// the memory subsystem across sessions).
type MemorySlice struct {
	Slice string `yaml:"slice"`
}

// Approval presets (§4.1.1), reusing the existing three-tier approval
// semantics of the project.
const (
	ApprovalStandard   = "standard"
	ApprovalStrict     = "strict"
	ApprovalPermissive = "permissive"
)

// Parse decodes an employee profile from YAML bytes.
func Parse(data []byte) (*Employee, error) {
	var e Employee
	if err := yaml.Unmarshal(data, &e); err != nil {
		return nil, fmt.Errorf("orchestrator: parse employee: %w", err)
	}
	return &e, nil
}

// Load loads the employee profile from dir/employee.yaml.
func Load(dir string) (*Employee, error) {
	data, err := os.ReadFile(filepath.Join(dir, EmployeeFileName))
	if err != nil {
		return nil, fmt.Errorf("orchestrator: load employee from %s: %w", dir, err)
	}
	return Parse(data)
}

// Marshal serializes the employee profile back to YAML.
func (e *Employee) Marshal() ([]byte, error) {
	data, err := yaml.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("orchestrator: marshal employee: %w", err)
	}
	return data, nil
}

// NormalizeApproval maps an empty approval to the safe default (standard)
// and rejects anything outside the three presets.
func NormalizeApproval(approval string) (string, error) {
	switch approval {
	case "":
		return ApprovalStandard, nil
	case ApprovalStandard, ApprovalStrict, ApprovalPermissive:
		return approval, nil
	default:
		return "", fmt.Errorf("orchestrator: invalid approval %q (want %s|%s|%s)",
			approval, ApprovalStandard, ApprovalStrict, ApprovalPermissive)
	}
}

// FinalTools merges the allowlist and denylist into the effective tool set.
// The denylist wins: any allowed tool listed in the denylist is removed,
// and duplicates are collapsed keeping the first occurrence. An empty
// allowlist means "no explicit restriction" and yields nil — the caller
// grants the full registry in that case.
func FinalTools(allowlist, denylist []string) []string {
	if len(allowlist) == 0 {
		return nil
	}
	blocked := make(map[string]struct{}, len(denylist))
	for _, t := range denylist {
		blocked[t] = struct{}{}
	}
	seen := make(map[string]struct{}, len(allowlist))
	out := make([]string, 0, len(allowlist))
	for _, t := range allowlist {
		if _, bad := blocked[t]; bad {
			continue
		}
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		out = append(out, t)
	}
	return out
}

// EmployeePolicy is the resolved runtime policy of an employee (§4.1/§4.6):
// the model preset (empty = gateway routes), the normalized approval preset
// and the final tool set.
type EmployeePolicy struct {
	ModelPreset string
	Approval    string
	Tools       []string
}

// ResolvePolicy derives the effective runtime policy from the raw profile.
func (e *Employee) ResolvePolicy() (EmployeePolicy, error) {
	approval, err := NormalizeApproval(e.Approval)
	if err != nil {
		return EmployeePolicy{}, err
	}
	return EmployeePolicy{
		ModelPreset: e.Model.Preset,
		Approval:    approval,
		Tools:       FinalTools(e.ToolAllowlist, e.ToolDenylist),
	}, nil
}

// ExampleEmployees returns the six built-in employee templates (M3):
// writer, coder, tester, reviewer, product and translator. Each has a
// distinct role, persona and tool kit; model presets cover both explicit
// binding and gateway-routed (empty) cases; approval covers all three
// presets.
func ExampleEmployees() []Employee {
	return []Employee{
		{
			Name:          "writer",
			Display:       "文案写手小笔",
			Role:          "撰写与润色文档、报告与文案",
			Persona:       "文风灵活：先确认读者与目标再动笔；产出必带标题与要点；用词克制",
			Expertise:     []string{"技术文档", "方案报告", "产品文案", "发布公告"},
			Workflow:      []string{"先确认读者与行文目标", "列提纲再动笔", "产出带标题与要点", "收尾自查是否符合目标读者"},
			Constraints:   []string{"禁止堆砌术语，必要时给出解释", "结论必带依据，不写空泛修饰", "一次只聚焦一个主题"},
			Experience:    []string{"长文档先出目录再展开", "技术说明区分'怎么做'与'为什么'"},
			OutputFormat:  "标题+要点式；结尾给关键结论摘要",
			Workspace:     "workspace",
			Model:         ModelBinding{}, // 空预设：由网关按场景路由
			ToolAllowlist: []string{"read_file", "write_file", "list_files", "search_content"},
			ToolDenylist:  []string{"replace_in_file", "run_tests"},
			Memory:        MemorySlice{Slice: "style:writing"},
			Approval:      ApprovalPermissive,
			Skills:        []string{"doc-gen"},
		},
		{
			Name:          "coder",
			Display:       "开发者小倔",
			Role:          "实现代码与修复缺陷",
			Persona:       "严谨，先看测试再动手，每次改动跑验证",
			Expertise:     []string{"Go", "后端服务", "CLI 工具", "代码重构", "缺陷修复"},
			Workflow:      []string{"先读相关文件与测试理解现状", "确认改动影响面", "动笔前跑前置测试", "实现后补齐/更新测试", "跑全量验证与 git diff 自查"},
			Constraints:   []string{"禁止未跑测试就声称完成", "禁止扩大改动范围做与任务无关的重构", "写出的代码须能在本仓库构建通过"},
			Experience:    []string{"改动越界是返工主因，先圈定影响面", "修复缺陷先写复现用例再改代码"},
			OutputFormat:  "变更清单（文件/新增/修改）+ 验证结果（构建+测试）+ 遗留风险",
			Workspace:     "workspace",
			Model:         ModelBinding{Preset: "flash-model"},
			ToolAllowlist: []string{"read_file", "write_file", "replace_in_file", "run_tests", "git_diff", "list_files", "search_content", "exec_command"},
			ToolDenylist:  []string{"spawn_task", "handoff"},
			Memory:        MemorySlice{Slice: "project:code"},
			Approval:      ApprovalStandard,
			Skills:        []string{"refactor", "code-review"},
		},
		{
			Name:          "tester",
			Display:       "测试员小探",
			Role:          "编写与执行测试、追踪缺陷",
			Persona:       "怀疑一切自报：只认实测输出，失败必复现",
			Expertise:     []string{"单元测试", "回归测试", "缺陷复现", "覆盖率分析"},
			Workflow:      []string{"先定位被测目标与入口", "设计覆盖正常/边界/异常三态", "跑测试拿真实输出", "失败必还原最小复现"},
			Constraints:   []string{"禁止凭代码推断就下'通过'结论", "必须跑真实测试而非口述", "补测优先覆盖主路径与回退点"},
			Experience:    []string{"读到'已测试通过'先查证据，防自我欺骗", "边界条件是最常漏测的维度"},
			OutputFormat:  "通过率/失败项 + 失败原因 + 复现步骤 + 改进建议",
			Workspace:     "workspace",
			Model:         ModelBinding{Preset: "flash-model"},
			ToolAllowlist: []string{"read_file", "run_tests", "exec_command", "git_diff", "search_content"},
			ToolDenylist:  []string{"write_file"},
			Memory:        MemorySlice{Slice: "project:tests"},
			Approval:      ApprovalStandard,
			Skills:        []string{"test-writer", "bug-hunter"},
		},
		{
			Name:          "reviewer",
			Display:       "评审员老严",
			Role:          "审查代码与产出、把关质量",
			Persona:       "只信证据不信口供；挑刺优先；不通过绝不签字",
			Expertise:     []string{"代码审查", "安全扫描", "性能分析", "质量门禁"},
			Workflow:      []string{"先读变更影响面", "逐维度审查（正确性/安全/性能/可维护）", "逐条给证据与修复建议", "输出分级结论"},
			Constraints:   []string{"禁止无证据的批评", "每个问题必须给文件:行定位", "宁严勿宽，不通过直接说明理由"},
			Experience:    []string{"'看上去没 bug'不等于'验证过没 bug'", "并发/边界/资源释放是最常被忽略的点"},
			OutputFormat:  "分级问题清单（🔴严重/🟡建议/⚪风格）+ 总体评分 1-10",
			Workspace:     "workspace",
			Model:         ModelBinding{Preset: "pro-model"},
			ToolAllowlist: []string{"read_file", "git_diff", "search_content", "list_files"},
			ToolDenylist:  []string{"write_file", "replace_in_file"},
			Memory:        MemorySlice{Slice: "project:review"},
			Approval:      ApprovalStrict,
			Skills:        []string{"code-review", "security-scan"},
		},
		{
			Name:          "product",
			Display:       "产品经理小策",
			Role:          "产出 PRD、竞品分析与需求归因",
			Persona:       "先问用户与场景，再写方案；结论必带依据",
			Expertise:     []string{"PRD 撰写", "竞品分析", "需求归因", "用户场景建模"},
			Workflow:      []string{"先确认用户与业务场景", "梳理需求背景与痛点", "给方案与取舍依据", "列出验收标准"},
			Constraints:   []string{"禁止脱离用户场景谈功能", "每条需求必须给归因理由", "方案必须权衡替代项"},
			Experience:    []string{"'想要的功能'背后往往藏着真实业务目标", "验收标准可量化才可落地"},
			OutputFormat:  "背景/用户/目标/需求清单/验收标准，结论附依据",
			Workspace:     "workspace",
			Model:         ModelBinding{Preset: "pro-model"},
			ToolAllowlist: []string{"read_file", "write_file", "search_content", "list_files", "web_fetch"},
			ToolDenylist:  []string{"run_tests", "exec_command"},
			Memory:        MemorySlice{Slice: "project:product"},
			Approval:      ApprovalStandard,
			Skills:        []string{"prd-gen"},
		},
		{
			Name:          "translator",
			Display:       "翻译官小译",
			Role:          "多语言翻译与术语统一",
			Persona:       "忠实原文，术语前后一致，产出附术语表",
			Expertise:     []string{"中英互译", "技术术语", "专利/文档翻译", "术语表维护"},
			Workflow:      []string{"先通读原文定调", "建立/复用术语表", "逐段忠实翻译", "整体回读校对一致性"},
			Constraints:   []string{"禁止意译扭曲原文本意", "专有名词首次出现保留原名", "全文档术语必须统一"},
			Experience:    []string{"术语不一致是翻译返工主要根因", "长文先统一术语表再动笔"},
			OutputFormat:  "译文 + 术语对照表",
			Workspace:     "workspace",
			Model:         ModelBinding{}, // 空预设：由网关路由
			ToolAllowlist: []string{"read_file", "write_file"},
			ToolDenylist:  []string{"exec_command"},
			Memory:        MemorySlice{Slice: "style:translation"},
			Approval:      ApprovalPermissive,
			Skills:        []string{"translate"},
		},
	}
}
