package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/contextloom/contextloom-agent/internal/tools"
)

// ─────────────────────────────────────────────────────────────
// handoff 原语（对标 OpenAI Agents SDK handoff 的零依赖落地）
//
// handoff = LLM 自主决策的"控制权转移"：主 Agent 在工具循环中调用
// handoff 工具，把当前任务交给目标 Agent（如 planner/coder/verifier）
// 继续执行；目标 Agent 的输出作为交接结果返回主 Agent。
//
// 与 spawn_task 的区别：spawn_task 是"并行派生子任务"（结果回收），
// handoff 是"串行控制权转移"（当前任务整体移交）。
// ─────────────────────────────────────────────────────────────

// HandoffTarget 可交接目标 Agent 定义。
type HandoffTarget struct {
	Name    string // 目标名（handoff 工具的 to 参数）
	Desc    string // 一句话能力描述（注入提示词供模型选择）
	Handler func(ctx context.Context, reason string, payload map[string]any) (string, error)
}

// HandoffRegistry 交接目标注册表。
type HandoffRegistry struct {
	mu      sync.RWMutex
	targets map[string]HandoffTarget
}

// NewHandoffRegistry 创建交接注册表。
func NewHandoffRegistry() *HandoffRegistry {
	return &HandoffRegistry{targets: make(map[string]HandoffTarget)}
}

// Register 注册可交接目标。
func (h *HandoffRegistry) Register(t HandoffTarget) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.targets[t.Name] = t
}

// Get 按名称获取目标。
func (h *HandoffRegistry) Get(name string) (HandoffTarget, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	t, ok := h.targets[name]
	return t, ok
}

// Names 返回全部目标名（排序）。
func (h *HandoffRegistry) Names() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.targets))
	for n := range h.targets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Descriptions 生成提示词注入文本（handoff 工具说明 + 目标清单）。
func (h *HandoffRegistry) Descriptions() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.targets) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("handoff 工具可用，可交接目标：\n")
	names := make([]string, 0, len(h.targets))
	for n := range h.targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "- %s: %s\n", n, h.targets[n].Desc)
	}
	b.WriteString("调用 handoff 后当前任务移交给目标 Agent 执行，其输出作为交接结果返回。")
	return b.String()
}

// handoffDepthKey 交接深度上下文键（防无限转移）。
type handoffDepthKey struct{}

// handoffMaxDepth 交接深度上限（防止 A→B→A→B 死循环）。
const handoffMaxDepth = 3

// MakeHandoffTool 创建 handoff 工具函数（注册到 tools.Registry）。
// 参数：to（目标名，必填）、reason（交接原因）、payload（附加上下文，可选）。
// 目标不存在返回错误；深度超限返回错误（防递归）。
func MakeHandoffTool(reg *HandoffRegistry) tools.ToolFunc {
	return func(ctx context.Context, params map[string]any) (any, error) {
		to, _ := params["to"].(string)
		reason, _ := params["reason"].(string)
		payload, _ := params["payload"].(map[string]any)

		if to == "" {
			return nil, fmt.Errorf("handoff 缺少目标: to 参数必填（可用: %s）", strings.Join(reg.Names(), ", "))
		}
		target, ok := reg.Get(to)
		if !ok {
			return nil, fmt.Errorf("handoff 目标 %q 不存在（可用: %s）", to, strings.Join(reg.Names(), ", "))
		}
		// 深度检查（ctx 传递）
		depth := 0
		if d, ok := ctx.Value(handoffDepthKey{}).(int); ok {
			depth = d
		}
		if depth >= handoffMaxDepth {
			return nil, fmt.Errorf("handoff 交接深度超过上限 %d，已终止（防循环转移）", handoffMaxDepth)
		}
		subCtx := context.WithValue(ctx, handoffDepthKey{}, depth+1)

		// 执行目标 Agent（同步，输出作为交接结果）
		out, err := target.Handler(subCtx, reason, payload)
		if err != nil {
			return nil, fmt.Errorf("handoff 到 %s 失败: %w", to, err)
		}
		return map[string]any{
			"handoff_to":     to,
			"handoff_reason": reason,
			"result":         out,
		}, nil
	}
}

// HandoffToolSchema handoff 工具参数 schema（模型可见）。
var HandoffToolSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"to":      map[string]any{"type": "string", "description": "交接目标 Agent 名称"},
		"reason":  map[string]any{"type": "string", "description": "交接原因（说明为什么移交）"},
		"payload": map[string]any{"type": "object", "description": "附加上下文（可选）"},
	},
	"required": []string{"to"},
}

var _ = json.Marshal // 保留 import（schema 序列化备用）

// ─────────────────────────────────────────────────────────────
// M9 验证子代理（VerifierRequest 复核请求规格）
//
// strict+complex 任务收尾前送审一次：主 Agent 把产出交给只读复核子代理，
// 复核结果以"【复核结果】"回注主线程再走一轮（设计文档 §3.7 复核纪律：
// 只读评审、不写产出、不改主链上下文；成本以"一次上限"兜底）。
// ─────────────────────────────────────────────────────────────

// VerifierKind 复核请求类型（VerifierRequest.Kind 固定值）。
const VerifierKind = "verify"

// VerifierResultPrefix 复核结果回注前缀（结果以本前缀追加到主线程消息）。
const VerifierResultPrefix = "【复核结果】"

// VerifierTaskPrefix 复核子任务文本前缀（runtime 装配与 executor 防递归共用）：
// 复核任务自身不再送审（只读复核的复核无边际收益，且防链式递归）。
const VerifierTaskPrefix = "验证任务（只读复核）"

// BuiltinVerifyChecks 内置复核清单四项（设计文档 §3.7：
// 证据核对/关键数字复算/边界与失败路径/与计划对照）。
var BuiltinVerifyChecks = []string{
	"证据核对：逐条核对产出中的文件/命令/测试结果是否真实存在、可复现",
	"关键数字复算：复算测试通过数/覆盖率/性能等关键数字，识别虚报",
	"边界与失败路径：检查边界条件、错误处理与失败路径是否被覆盖",
	"与计划对照：对照任务目标逐项确认完成度，列出缺口",
}

// VerifierRequest 复核请求规格（M9；复用既有 handoff/spawn 通道）。
type VerifierRequest struct {
	Kind   string   // 固定 "verify"
	Target string   // 被复核产出（正文摘要/文件清单）
	Checks []string // 复核清单（空=内置四项 BuiltinVerifyChecks）
}

// VerifierChecks 返回生效复核清单（空=内置四项；返回副本防调用方篡改）。
func (r *VerifierRequest) VerifierChecks() []string {
	if r == nil || len(r.Checks) == 0 {
		return append([]string(nil), BuiltinVerifyChecks...)
	}
	return append([]string(nil), r.Checks...)
}
