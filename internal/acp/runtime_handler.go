package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/contextloom/contextloom-agent/internal/executor"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// HandlerDeps RuntimeHandler 依赖（由 main 组装，避免 acp ↔ runtime 循环依赖）。
type HandlerDeps struct {
	// Spawn 创建 agent（runtime.Executor.SpawnAgent）。
	Spawn func(id string, task types.TaskCreate, backend model.Backend) *executor.Agent
	// Tools 工具注册表（能力声明 + 审批桥接）。
	Tools *tools.Registry
	// Backend 模型后端。
	Backend model.Backend
	// PersistencePaths 持久化路径（注入 agent 提示词）。
	PersistencePaths func() map[string]string
	// MaxContext 上下文窗口（capabilities 声明）。
	MaxContext int
	// ModelID 后端模型标识（capabilities 声明）。
	ModelID string
	// Tracer 轻量可观测（runtime.Runtime 实现 executor.Tracer；nil=不记录）。
	Tracer executor.Tracer
}

// RuntimeHandler 把标准 ACP 会话桥接到 ContextLoom runtime：
//   - 每个 ACP 会话持有独立对话历史（互不串扰，与 TUI 会话隔离）；
//   - OnPrompt 创建 task 模式 Agent（复用 runtime 的完整能力：Harness 检索、
//     工具执行、trace、多批次子任务），流式文本转发为
//     session/agent_message（text 内容块），工具结果由 agent 内部执行；
//   - policy=quiet（approval_mode=permissive）自动批准；standard/strict 时
//     敏感工具走标准 ACP 审批流（客户端回 allow/deny）；
//   - 会话历史随 prompt 追加 user/assistant 轮次（保持多轮对话连续性）。
//
// 通过依赖注入而非直接引用 runtime 包，避免 import cycle
// （runtime.go 引用 acp.NewServer）。
type RuntimeHandler struct {
	deps   HandlerDeps
	srv    *Server
	policy string // quiet | standard | strict

	mu        sync.Mutex
	histories map[string][]types.Message
	permChans map[string]chan bool // sessionID/reqID → allow 通道

	installed  bool
	oldConfirm tools.ConfirmFunc
	ca         *tools.CautiousAnalyzer
}

// acpAgentPrefix 会话 Agent ID 前缀（权限桥接识别 ACP 会话）。
const acpAgentPrefix = "acp-"

// NewRuntimeHandler 创建 ACP ↔ runtime 桥接。
// policy: "quiet" | "standard" | "strict"。
// installConfirm=true 时接管 Tools 的确认回调（仅 --acp 独占模式调用）。
func NewRuntimeHandler(deps HandlerDeps, srv *Server, policy string, installConfirm bool) *RuntimeHandler {
	h := &RuntimeHandler{
		deps:      deps,
		srv:       srv,
		policy:    policy,
		histories: make(map[string][]types.Message),
		permChans: make(map[string]chan bool),
	}
	if installConfirm && policy != "quiet" {
		h.installConfirmBridge()
	}
	return h
}

// installConfirmBridge 接管 Tools 确认回调（保存旧值，非 ACP 会话回退旧回调）。
func (h *RuntimeHandler) installConfirmBridge() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.installed {
		return
	}
	h.installed = true
	h.oldConfirm = h.deps.Tools.GetConfirmFunc()
	h.ca = tools.NewCautiousAnalyzer(tools.ModeStandard)
	h.deps.Tools.SetConfirmFunc(h.confirmBridge)
}

// confirmBridge ACP 审批桥接：ACP 会话的敏感工具 → 推送 request_permission，
// 等待客户端 allow/deny；非 ACP 会话回退旧确认回调（或放行）。
func (h *RuntimeHandler) confirmBridge(ctx context.Context, decision *tools.ConfirmDecision) (bool, string) {
	aid := tools.AgentIDFromContext(ctx)
	sessionID := strings.TrimPrefix(aid, acpAgentPrefix)
	if sessionID == aid || sessionID == "" {
		// 非 ACP 会话（如 TUI 并行）：回退旧回调
		if h.oldConfirm != nil {
			return h.oldConfirm(ctx, decision)
		}
		return true, ""
	}
	reqID := uuid.NewString()
	data, _ := json.Marshal(map[string]any{"tool_name": decision.ToolName, "arguments": decision.Params})
	ch := make(chan bool, 1)
	h.mu.Lock()
	h.permChans[sessionID+"/"+reqID] = ch
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.permChans, sessionID+"/"+reqID)
		h.mu.Unlock()
	}()
	_ = h.srv.Push(sessionID, MethodPermissionRequest, PermissionRequestParams{
		SessionID: sessionID,
		PermissionRequest: PermissionRequest{
			ID:      reqID,
			Kind:    PermUseTool,
			Message: fmt.Sprintf("执行工具 %s？原因: %s", decision.ToolName, decision.ReasonsString()),
			Data:    data,
		},
	})
	select {
	case allow := <-ch:
		return allow, ""
	case <-time.After(120 * time.Second):
		return false, "审批超时（120s 未响应，已拒绝）"
	}
}

// OnPermissionResponse 实现 SessionHandler：把客户端 allow/deny 投递到等待通道。
func (h *RuntimeHandler) OnPermissionResponse(sessionID, reqID string, allow bool) error {
	h.mu.Lock()
	ch := h.permChans[sessionID+"/"+reqID]
	h.mu.Unlock()
	if ch != nil {
		select {
		case ch <- allow:
		default:
		}
	}
	return nil
}

// GetCapabilities 返回工具定义与上下文窗口。
func (h *RuntimeHandler) GetCapabilities() Capabilities {
	defs := h.deps.Tools.ToolDefinitions()
	toolDefs := make([]ToolDefinition, 0, len(defs))
	for _, d := range defs {
		// 过滤多 Agent 工具（ACP 单会话驱动场景不需要子 Agent 派发）
		switch d.Function.Name {
		case "spawn_task", "send_message", "check_messages", "handoff":
			continue
		}
		params, _ := json.Marshal(d.Function.Parameters)
		toolDefs = append(toolDefs, ToolDefinition{
			Name:        d.Function.Name,
			Description: d.Function.Description,
			Parameters:  params,
		})
	}
	models := []string{}
	if h.deps.ModelID != "" {
		models = []string{h.deps.ModelID}
	}
	return Capabilities{
		Tools:         toolDefs,
		ContextWindow: h.deps.MaxContext,
		Models:        models,
		PromptCaching: true,
	}
}

// OnNewSession 会话创建（初始化历史）。
func (h *RuntimeHandler) OnNewSession(sessionID string, tools []ToolDefinition, model string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.histories[sessionID]; !ok {
		h.histories[sessionID] = nil
	}
	return nil
}

// OnPrompt 异步执行提示：创建 task Agent 流式执行，文本增量转发为 agent_message。
func (h *RuntimeHandler) OnPrompt(sessionID, prompt string, tools []ToolDefinition, model string) error {
	h.mu.Lock()
	history := h.histories[sessionID]
	h.mu.Unlock()
	go h.runPrompt(sessionID, prompt, history)
	return nil
}

func (h *RuntimeHandler) runPrompt(sessionID, prompt string, history []types.Message) {
	ctx := context.Background()
	task := types.TaskCreate{
		Type:          "task.create",
		ID:            acpAgentPrefix + sessionID,
		Directive:     "ACP",
		Task:          prompt,
		Payload:       map[string]string{"source": "acp"},
		Mode:          "task",
		ContextPolicy: types.PolicyFullContext,
		Priority:      1,
	}
	agent := h.deps.Spawn(task.ID, task, h.deps.Backend)
	agent.SetConversationHistory(history)
	agent.SetTracer(h.deps.Tracer)
	agent.SetPersistencePaths(h.deps.PersistencePaths())
	if h.policy == "quiet" {
		agent.SetAutoApprove(true)
	}

	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		_ = h.srv.Push(sessionID, MethodPromptResponse, ResponseParams{
			Success: false, SessionID: sessionID, Error: err.Error(),
		})
		return
	}

	var out strings.Builder
	finished := false
	for tok := range ch {
		out.WriteString(tok)
		blocks := []ContentBlock{{Type: ContentText, Text: tok}}
		msg, _ := json.Marshal(blocks)
		_ = h.srv.Push(sessionID, MethodAgentMessage, AgentMessageParams{
			SessionID: sessionID,
			Message:   msg,
		})
	}
	streamErr := <-errCh
	if streamErr != nil {
		_ = h.srv.Push(sessionID, MethodPromptResponse, ResponseParams{
			Success: false, SessionID: sessionID, Error: streamErr.Error(),
		})
		finished = true
	}
	if !finished {
		// 流结束（正常/取消）：推送 prompt_response（语义：本次 prompt 处理完成）
		_ = h.srv.Push(sessionID, MethodPromptResponse, ResponseParams{
			Success: true, SessionID: sessionID,
		})
	}

	// 更新会话历史（user prompt + assistant 回复），保持多轮连续性
	updated := append(append([]types.Message(nil), history...),
		types.Message{Role: "user", Content: prompt},
		types.Message{Role: "assistant", Content: out.String()},
	)
	h.mu.Lock()
	h.histories[sessionID] = updated
	h.mu.Unlock()
}

// OnComplete 返回会话摘要（最后一条 assistant 文本前 200 字符）。
func (h *RuntimeHandler) OnComplete(sessionID string) (string, error) {
	h.mu.Lock()
	history := h.histories[sessionID]
	h.mu.Unlock()
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "assistant" {
			if s, ok := history[i].Content.(string); ok {
				return truncateRunes(s, 200), nil
			}
		}
	}
	return "", nil
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// OnCancel 取消当前执行（保留历史，客户端可继续 prompt）。
func (h *RuntimeHandler) OnCancel(sessionID string) error { return nil }

// OnClose 清理会话历史与审批通道。
func (h *RuntimeHandler) OnClose(sessionID string) error {
	h.mu.Lock()
	delete(h.histories, sessionID)
	for k := range h.permChans {
		if strings.HasPrefix(k, sessionID+"/") {
			delete(h.permChans, k)
		}
	}
	h.mu.Unlock()
	return nil
}

// OnUpdate 更新会话工具（当前工具集由 runtime 注册表决定，无操作）。
func (h *RuntimeHandler) OnUpdate(sessionID string, tools []ToolDefinition) error { return nil }

// OnToolCallResponse 客户端工具执行结果（当前工具由 agent 内部执行，无操作）。
func (h *RuntimeHandler) OnToolCallResponse(sessionID, toolCallID string, content []ContentBlock) error {
	return nil
}

// OnAgentMessageResponse 客户端确认（无操作）。
func (h *RuntimeHandler) OnAgentMessageResponse(sessionID string, success bool) error { return nil }
