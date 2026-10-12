package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ToolFunc 工具函数签名
type ToolFunc func(ctx context.Context, params map[string]any) (any, error)

// ConfirmFunc 用户确认回调。
// 当 CautiousAnalyzer 判定需要确认时，系统调用此函数等待用户决策。
// 返回 true 表示用户批准执行，false 表示拒绝。
type ConfirmFunc func(ctx context.Context, decision *ConfirmDecision) (approved bool, userNote string)

// Registry 工具注册表
type Registry struct {
	mu              sync.RWMutex
	tools           map[string]ToolFunc
	extraDesc       map[string]string         // 动态工具描述（RegisterExtra）
	extraSchemas    map[string]map[string]any // 动态工具参数 schema（RegisterExtra）
	cautious        *CautiousAnalyzer         // 谨慎性分析器（可选）
	confirmFn       ConfirmFunc               // 用户确认回调（可选）
	confirmRequired bool                      // 是否需要执行确认流程
	hooks           []Hook                    // 工具生命周期钩子链（Pre/Post）
}

// NewRegistry 创建新的工具注册表
func NewRegistry() *Registry {
	r := &Registry{
		tools:        make(map[string]ToolFunc),
		extraDesc:    make(map[string]string),
		extraSchemas: make(map[string]map[string]any),
	}
	r.registerBuiltins()
	return r
}

// normalizeParamAliases 工具参数别名归一化：把模型输出的 schema 未声明参数名
// 映射到声明名。历史教训：DSML/文本兜底解析的调用用 dir 而非 path（或 file 而非
// path），导致工具拿到空 path 静默失败/误执行——这里统一纠偏。
// 声明名已存在时优先保留声明名（别名不覆盖）。
func normalizeParamAliases(tool string, params map[string]any) map[string]any {
	if params == nil {
		return params
	}
	aliases, ok := paramAliases[tool]
	if !ok {
		return params
	}
	for from, to := range aliases {
		v, exists := params[from]
		if !exists {
			continue
		}
		if _, declared := params[to]; !declared {
			params[to] = v
		}
		delete(params, from)
	}
	return params
}

// normalizeNumericStrings 把数字型参数从字符串转为 float64（模型/文本兜底常输出
// "30000" 而非 30000，工具按 float64 断言失败导致参数丢失）。
// 仅对 schema 中声明为 integer/number 的参数生效，避免误转。
func normalizeNumericStrings(tool string, params map[string]any) map[string]any {
	if params == nil {
		return params
	}
	schema, ok := toolSchemas[tool]
	if !ok {
		return params
	}
	props, _ := schema["properties"].(map[string]any)
	for key, v := range params {
		s, ok := v.(string)
		if !ok || s == "" {
			continue
		}
		prop, _ := props[key].(map[string]any)
		typ, _ := prop["type"].(string)
		if typ != "integer" && typ != "number" {
			continue
		}
		if num, err := strconv.ParseFloat(s, 64); err == nil {
			params[key] = num
		}
	}
	return params
}

// paramAliases 各工具的参数别名表（from=模型常见误写，to=schema 声明名）。
var paramAliases = map[string]map[string]string{
	"exec_command":    {"cmd": "command", "timeout": "timeout_ms", "commandline": "command", "shell_cmd": "command"},
	"list_files":      {"dir": "path", "folder": "path", "directory": "path"},
	"search_content":  {"dir": "path", "folder": "path", "directory": "path"},
	"read_file":       {"file": "path", "filename": "path"},
	"write_file":      {"file": "path", "filename": "path"},
	"replace_in_file": {"file": "path", "filename": "path"},
	"run_tests":       {"target": "test_target"},
	"web_fetch":       {"max_length": "max_length"},
}

// SetCautious 设置谨慎性分析器。
// 设置后，每次工具执行前会进行风险分析，需要确认的操作会调用 ConfirmFunc。
func (r *Registry) SetCautious(ca *CautiousAnalyzer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cautious = ca
	r.confirmRequired = ca != nil
}

// AddHook 注册工具生命周期钩子（按注册顺序执行）。
// Pre 拒绝时工具不执行，reason 返回给模型。
func (r *Registry) AddHook(h Hook) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hooks = append(r.hooks, h)
}

// SetConfirmFunc 设置用户确认回调函数。
// 当谨慎性分析器判定需要确认时，会调用此函数等待用户决策。
// 通常由 TUI 层注入，用于弹出确认对话框。
func (r *Registry) SetConfirmFunc(fn ConfirmFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.confirmFn = fn
}

// GetConfirmFunc 返回当前确认回调（nil=未设置）。供 ACP 桥接保存/恢复旧值。
func (r *Registry) GetConfirmFunc() ConfirmFunc {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.confirmFn
}

// CautiousAnalyzer 返回当前的谨慎性分析器
func (r *Registry) CautiousAnalyzer() *CautiousAnalyzer {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.cautious
}

// Register 注册工具
func (r *Registry) Register(name string, fn ToolFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = fn
}

// RegisterExtra 注册带完整 schema 与描述的工具（MCP 动态工具等）。
// 与 Register 的区别：额外维护 desc/schema，ToolDefinitions() 返回时携带，
// 避免模型收到空 schema 而无法生成参数（goal 模式 spawn_task 曾因空 schema 从不触发）。
func (r *Registry) RegisterExtra(name, desc string, schema map[string]any, fn ToolFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[name] = fn
	if r.extraDesc == nil {
		r.extraDesc = make(map[string]string)
	}
	if r.extraSchemas == nil {
		r.extraSchemas = make(map[string]map[string]any)
	}
	r.extraDesc[name] = desc
	r.extraSchemas[name] = schema
}

// Execute 执行工具调用。
// 如果设置了 CautiousAnalyzer，会在执行前进行风险分析；
// 需要确认的操作会调用 ConfirmFunc 等待用户决策。
// defaultToolTimeout 工具统一执行超时兜底（120s）。
// 包级变量以便测试覆盖为更小值。
var defaultToolTimeout = 120 * time.Second

// toolTimeoutOverride 工具统一超时豁免表：0 = 不套用 defaultToolTimeout，
// 由工具内部自管理终止（如 spawn_task 采用"连续无输出 10 分钟"活动式超时，
// 子 Agent 持续输出时不设限——固定 120s 会误杀长任务）。
var toolTimeoutOverride = map[string]time.Duration{
	"spawn_task": 0,
}

func (r *Registry) Execute(ctx context.Context, call types.ToolCall) (*types.ToolResult, error) {
	r.mu.RLock()
	fn, ok := r.tools[call.Function.Name]
	ca := r.cautious
	confirmFn := r.confirmFn
	hooks := make([]Hook, len(r.hooks))
	copy(hooks, r.hooks)
	r.mu.RUnlock()

	if !ok {
		return &types.ToolResult{
			CallID: call.ID,
			Name:   call.Function.Name,
			Error:  fmt.Sprintf("未知工具: %s", call.Function.Name),
		}, nil
	}

	// 双向 hooks：PreToolUse 链（可修改参数/拒绝执行）
	for _, h := range hooks {
		modified, allow, reason := h.PreToolUse(ctx, call)
		if !allow {
			return &types.ToolResult{
				CallID: call.ID,
				Name:   call.Function.Name,
				Error:  "工具调用被拦截: " + reason,
			}, nil
		}
		call = modified
	}

	// 解析 JSON 参数
	var params map[string]any
	if call.Function.Arguments != "" {
		if err := json.Unmarshal([]byte(call.Function.Arguments), &params); err != nil {
			// JSON 解析失败时，将原始字符串作为 raw 参数传入
			params = map[string]any{"raw": call.Function.Arguments}
		}
	}
	// 参数别名归一化：模型可能输出 schema 未声明的参数名（历史教训：
	// DSML 文本兜底解析出的调用曾用 dir 而非 path → 工具收到空 path 静默失败）。
	params = normalizeParamAliases(call.Function.Name, params)
	// 类型归一化：模型常把数字参数写成字符串（如 timeout:"30000"、offset:"10"），
	// 工具按 float64/int 断言会失败 → 参数"丢失"。统一把纯数字字符串转成 float64。
	params = normalizeNumericStrings(call.Function.Name, params)

	// 谨慎性分析：检查是否需要用户确认。
	// task 模式（autoApprove）跳过确认弹窗，避免打断自动化执行；
	// chat 模式（用户在场）保留确认弹窗。
	if ca != nil && confirmFn != nil && !IsAutoApprove(ctx) {
		decision := ca.Analyze(call.Function.Name, params)
		if decision.Level >= ConfirmNotify {
			approved, note := confirmFn(ctx, decision)
			if !approved {
				// 用户拒绝执行
				return &types.ToolResult{
					CallID: call.ID,
					Name:   call.Function.Name,
					Result: map[string]any{
						"skipped":  true,
						"reason":   "用户拒绝执行",
						"userNote": note,
					},
				}, nil
			}
			// 用户批准，记录决策
			decision.Approved = true
			decision.UserNote = note

			// 如果用户选择"总是允许"，记录
			if note == "always" {
				ca.AddAlwaysAllowed(call.Function.Name, params)
			}
		}
	}

	// 统一工具超时兜底：防止任何工具（exec_command/run_tests 等）无限卡住。
	// exec_command 内部有更短超时（默认 20s），这里 120s 是最后防线。
	// spawn_task 例外（toolTimeoutOverride）：子任务执行采用"活动式超时"——
	// 子 Agent 持续输出时不设限；仅当连续 10 分钟无任何输出才终止（输出重置计时）。
	execCtx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	if d, ok := toolTimeoutOverride[call.Function.Name]; ok && d == 0 {
		execCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()

	// 执行工具（带审计：成功/失败/超时全路径记录，nil 审计器时零开销）
	start := time.Now()
	result, err := fn(execCtx, params)
	ms := time.Since(start).Milliseconds()
	recordAudit(auditActionFor(call.Function.Name), call.Function.Name,
		auditPath(params), err == nil, ms, errString(err))

	var res *types.ToolResult
	if err != nil {
		if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
			res = &types.ToolResult{
				CallID: call.ID,
				Name:   call.Function.Name,
				Error:  fmt.Sprintf("工具执行超时（%s 后强制终止）", defaultToolTimeout),
			}
		} else {
			res = &types.ToolResult{
				CallID: call.ID,
				Name:   call.Function.Name,
				Error:  err.Error(),
			}
		}
	} else {
		res = &types.ToolResult{
			CallID: call.ID,
			Name:   call.Function.Name,
			Result: result,
		}
	}

	// PostToolUse 链：工具执行后回调（可修改结果/记录审计）
	for i := len(hooks) - 1; i >= 0; i-- {
		res = hooks[i].PostToolUse(ctx, call, res)
	}
	return res, nil
}

// ToolDefinitions 返回工具定义列表（用于模型调用）
func (r *Registry) ToolDefinitions() []types.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var defs []types.ToolDefinition
	for name := range r.tools {
		desc := toolDescriptions[name]
		if desc == "" && r.extraDesc != nil {
			desc = r.extraDesc[name]
		}
		if desc == "" {
			desc = name + " 工具"
		}
		params := toolSchemas[name]
		if params == nil && r.extraSchemas != nil {
			params = r.extraSchemas[name]
		}
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, types.ToolDefinition{
			Type: "function",
			Function: types.FunctionDefinition{
				Name:        name,
				Description: desc,
				Parameters:  params,
			},
		})
	}

	return defs
}

// toolDescriptions 工具描述（用于模型提示词）
var toolDescriptions = map[string]string{
	"read_file":       "读取文件",
	"write_file":      "写入文件",
	"replace_in_file": "替换文件内容",
	"exec_command":    "执行命令",
	"search_content":  "搜索代码",
	"list_files":      "列出目录",
	"run_tests":       "运行测试",
	"git_diff":        "查看变更",
	"web_fetch":       "获取网页",
	"spawn_task":      "创建子任务",
	"send_message":    "发送消息",
	"check_messages":  "检查消息",
	"propose_vocab":   "提交词汇映射提案",
}

// toolSchemas 工具参数 JSON Schema（用于模型调用）
var toolSchemas = map[string]map[string]any{
	"read_file": {
		"type": "object",
		"properties": map[string]any{
			"path":   map[string]any{"type": "string", "description": "路径"},
			"offset": map[string]any{"type": "integer", "description": "起始行"},
			"limit":  map[string]any{"type": "integer", "description": "行数"},
		},
		"required": []string{"path"},
	},
	"write_file": {
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "路径"},
			"content": map[string]any{"type": "string", "description": "内容"},
		},
		"required": []string{"path", "content"},
	},
	"replace_in_file": {
		"type": "object",
		"properties": map[string]any{
			"path":    map[string]any{"type": "string", "description": "路径"},
			"old_str": map[string]any{"type": "string", "description": "旧文本"},
			"new_str": map[string]any{"type": "string", "description": "新文本"},
		},
		"required": []string{"path", "old_str", "new_str"},
	},
	"exec_command": {
		"type": "object",
		"properties": map[string]any{
			"command":    map[string]any{"type": "string", "description": "命令"},
			"timeout_ms": map[string]any{"type": "integer", "description": "超时ms"},
		},
		"required": []string{"command"},
	},
	"search_content": {
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{"type": "string", "description": "模式"},
			"path":    map[string]any{"type": "string", "description": "目录"},
		},
		"required": []string{"pattern"},
	},
	"list_files": {
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{"type": "string", "description": "目录"},
		},
		"required": []string{"path"},
	},
	"run_tests": {
		"type": "object",
		"properties": map[string]any{
			"test_target": map[string]any{"type": "string", "description": "目标"},
		},
	},
	"git_diff": {
		"type":       "object",
		"properties": map[string]any{},
	},
	// 多 Agent 协作工具 schema（必须完整定义——缺失时模型收到的 parameters 为空对象，
	// 严格按 schema 生成调用参数的工具模型将无法构造合法调用，导致 spawn_task 从不触发。
	// 历史教训：goal 模式"从未自动触发过 task 子任务"的根因就是这三个工具缺 schema）
	"spawn_task": {
		"type": "object",
		"properties": map[string]any{
			"task":      map[string]any{"type": "string", "description": "自包含子任务描述（必填），必须是有明确产出的任务（新增/修改文件、测试结果、生成文档等）。子任务执行无固定超时：持续输出时不设限，仅当连续 10 分钟无任何输出才终止（有输出即重置计时）"},
			"directive": map[string]any{"type": "string", "description": "指令或员工名: 可直接传员工名（coder/tester/product/reviewer/writer/translator，按员工档案装配工具/人设/模型），或通用指令（CODE_AGENT/TEST/REFACTOR/PLANNER/REVIEW/WRITE/TRANSLATE，自动映射到对应员工）"},
			"context":   map[string]any{"type": "string", "description": "传递给子任务的上下文摘要（可选）"},
			"model":     map[string]any{"type": "string", "description": "子任务自选模型（可选）: models 列表中的模型预设名，子 Agent 用该模型执行任务（多来源搭配组合）；空 = 按员工档案 model.preset/fallbacks 绑定，或网关路由"},
		},
		"required": []string{"task"},
	},
	"send_message": {
		"type": "object",
		"properties": map[string]any{
			"to":      map[string]any{"type": "string", "description": "接收方: main/agentID/broadcast"},
			"content": map[string]any{"type": "string", "description": "消息内容"},
		},
		"required": []string{"to", "content"},
	},
	"check_messages": {
		"type":       "object",
		"properties": map[string]any{},
	},
	"propose_vocab": {
		"type":       "object",
		"properties": map[string]any{},
	},
	"web_fetch": {
		"type": "object",
		"properties": map[string]any{
			"url":        map[string]any{"type": "string", "description": "URL"},
			"max_length": map[string]any{"type": "integer", "description": "最大长度"},
		},
		"required": []string{"url"},
	},
}

// List 返回所有已注册的工具名列表
func (r *Registry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// registerBuiltins 注册内置工具
func (r *Registry) registerBuiltins() {
	r.tools["read_file"] = ReadFile
	r.tools["write_file"] = WriteFile
	r.tools["replace_in_file"] = ReplaceInFile
	r.tools["exec_command"] = ExecCommand
	r.tools["search_content"] = SearchContent
	r.tools["list_files"] = ListFiles
	r.tools["run_tests"] = RunTests
	r.tools["git_diff"] = GitDiff
	r.tools["web_fetch"] = WebFetch
}

// CloneWithout 返回排除指定工具的注册表副本（子 Agent 过滤 spawn_task 防递归）
func (r *Registry) CloneWithout(exclude ...string) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	clone := &Registry{
		tools:        make(map[string]ToolFunc, len(r.tools)),
		extraDesc:    make(map[string]string, len(r.extraDesc)),
		extraSchemas: make(map[string]map[string]any, len(r.extraSchemas)),
	}
	skip := make(map[string]bool, len(exclude))
	for _, name := range exclude {
		skip[name] = true
	}
	for name, fn := range r.tools {
		if !skip[name] {
			clone.tools[name] = fn
		}
	}
	for k, v := range r.extraDesc {
		clone.extraDesc[k] = v
	}
	for k, v := range r.extraSchemas {
		clone.extraSchemas[k] = v
	}
	return clone
}

// RiskLevel 风险等级（借鉴业界主流桌面编码 Agent 的四级权限模型）
type RiskLevel int

const (
	RiskSafe   RiskLevel = iota // 只读，无需确认
	RiskLow                     // 轻微影响
	RiskMedium                  // 写操作，需确认
	RiskHigh                    // 命令执行，强制确认
)

// toolRiskLevels 工具风险分级
var toolRiskLevels = map[string]RiskLevel{
	"read_file":       RiskSafe,
	"list_files":      RiskSafe,
	"search_content":  RiskSafe,
	"git_diff":        RiskSafe,
	"web_fetch":       RiskLow,
	"write_file":      RiskMedium,
	"replace_in_file": RiskMedium,
	"run_tests":       RiskMedium,
	"exec_command":    RiskHigh,
}

// GetRiskLevel 获取工具的风险等级
func (r *Registry) GetRiskLevel(toolName string) RiskLevel {
	if level, ok := toolRiskLevels[toolName]; ok {
		return level
	}
	return RiskHigh // 未知工具默认高风险
}

// NeedsConfirmation 判断工具是否需要用户确认（基于旧的风险等级系统）
func (r *Registry) NeedsConfirmation(toolName string) bool {
	level := r.GetRiskLevel(toolName)
	return level >= RiskMedium
}
