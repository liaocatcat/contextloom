package tools

import (
	"context"
	"fmt"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 任务工具依赖注入接口 ──
// tools 包不直接 import executor（避免循环依赖），通过接口注入能力。

// agentIDKey 是 context 中携带当前 Agent ID 的 key
type agentIDKey struct{}

// progressChanKey 是 context 中携带 TUI 进度 channel 的 key
type progressChanKey struct{}

// WithAgentID 将 Agent ID 注入 context，供任务工具识别调用者身份
func WithAgentID(ctx context.Context, agentID string) context.Context {
	return context.WithValue(ctx, agentIDKey{}, agentID)
}

// WithProgressChan 注入 TUI 进度 channel，供 spawn_task 向主消息流发送子 Agent 状态
func WithProgressChan(ctx context.Context, ch chan<- string) context.Context {
	return context.WithValue(ctx, progressChanKey{}, ch)
}

// EmitProgress 向 TUI 发送进度消息（若 ctx 未注入 channel 则静默跳过）
func EmitProgress(ctx context.Context, msg string) {
	if ch, ok := ctx.Value(progressChanKey{}).(chan<- string); ok && ch != nil {
		select {
		case ch <- msg:
		default: // channel 满，跳过进度消息不阻塞工具执行
		}
	}
}

// ── 子任务面板信号（§ 前缀，TUI 解析后路由到 task 面板而非主消息流）──
// 协议格式：
//   §START|<subID>|<directive>|<employee>|<task>   子任务启动（TUI 创建面板项）
//   §TOK|<subID>|<token>                 子 Agent 流式 token（TUI 更新面板 LastLine）
//   §DONE|<subID>|<score>                子任务完成（TUI 更新状态+评分）
//   §FAIL|<subID>|<err>                  子任务失败
// 注：§START 的 employee 段在 unix 员工命中时填写（如 coder/tester），
// 未命中/旧格式为三段（<subID>|<directive>|<task>），TUI 解析为空串——向后兼容。

// EmitTaskStart 发送子任务启动信号（TUI 创建面板项）。
// employee 为命中的员工名（可空串；空串时发三段旧格式，与历史信号兼容）。
func EmitTaskStart(ctx context.Context, subID, directive, employee, task string) {
	if employee == "" {
		EmitProgress(ctx, fmt.Sprintf("§START|%s|%s|%s", subID, directive, task))
		return
	}
	EmitProgress(ctx, fmt.Sprintf("§START|%s|%s|%s|%s", subID, directive, employee, task))
}

// EmitTaskToken 转发子 Agent 流式 token 到 TUI（面板实时更新 LastLine）
func EmitTaskToken(ctx context.Context, subID, token string) {
	ch, ok := ctx.Value(progressChanKey{}).(chan<- string)
	if !ok || ch == nil {
		return
	}
	select {
	case ch <- "§TOK|" + subID + "|" + token:
	default: // channel 满，丢弃该 token 片段不阻塞子 Agent
	}
}

// EmitTaskDone 发送子任务完成信号（TUI 更新面板状态为 done + 评分）
func EmitTaskDone(ctx context.Context, subID string, score float64) {
	EmitProgress(ctx, fmt.Sprintf("§DONE|%s|%.0f", subID, score))
}

// EmitTaskFail 发送子任务失败信号（TUI 更新面板状态为 failed）
func EmitTaskFail(ctx context.Context, subID string, err error) {
	EmitProgress(ctx, fmt.Sprintf("§FAIL|%s|%v", subID, err))
}

// VocabProposer 词汇映射提案器（由 runtime 注入实现，桥接 preprocessor.VocabManager）
type VocabProposer interface {
	ProposeVocab(term, mapping, reason string) error
}

// MakeProposeVocabTool 创建 propose_vocab 工具函数。
// 模型在任务中发现新的项目专有术语时调用，提交映射提案。
// 提案不立即生效——通过 TUI § 信号通知用户，用户用 /vocab accept 确认后写入词汇表。
func MakeProposeVocabTool(proposer VocabProposer) ToolFunc {
	return func(ctx context.Context, params map[string]any) (any, error) {
		term, _ := params["term"].(string)
		mapping, _ := params["mapping"].(string)
		reason, _ := params["reason"].(string)
		if term == "" || mapping == "" {
			return nil, fmt.Errorf("缺少必需参数 term 或 mapping")
		}
		if proposer == nil {
			return nil, fmt.Errorf("词汇提案器未注入")
		}
		if err := proposer.ProposeVocab(term, mapping, reason); err != nil {
			return nil, err
		}
		// 通知 TUI：模型发现新词汇，待用户确认
		EmitProgress(ctx, fmt.Sprintf("💡 词汇提案: %s→%s（%s）— 输入 /vocab accept %s 确认\n",
			term, mapping, reason, term))
		return map[string]any{
			"status": "proposed",
			"hint":   "提案已提交，等待用户用 /vocab accept 确认",
		}, nil
	}
}

// AgentIDFromContext 从 context 提取 Agent ID
func AgentIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(agentIDKey{}).(string); ok {
		return v
	}
	return "main"
}

// Bus 是消息总线抽象（由 runtime 注入 executor.MessageBus 实现）
type Bus interface {
	Send(msg types.AgentMessage) error
	DrainInbox(agentID string) []types.AgentMessage
}

// TaskSpawner 是子任务生成抽象（由 runtime 注入实现）
// SpawnTask 同步执行子任务并返回结果文本，singleflight 合并相同 task 描述的并发请求。
type TaskSpawner interface {
	SpawnTask(ctx context.Context, parentID string, spec TaskSpec) (string, error)
}

// TaskSpec 是 spawn_task 工具传入的子任务规格
type TaskSpec struct {
	Task      string // 自包含的子任务描述
	Directive string // 指令类型（可选）
	Context   string // 传递给子任务的上下文摘要（可选）
	// Model 子任务自选模型（可选）：models 列表中的预设名。子 Agent 用该
	// 预设的后端执行任务，实现"自选模型"；空 = 按员工档案绑定/网关路由。
	Model string
}

// ── singleflight：合并相同 task 描述的并发 spawn（需求 §5.1）──

type inFlightKey struct {
	parentID string
	task     string
}

var (
	inFlightMu sync.Mutex
	inFlight   = make(map[inFlightKey]*inFlightCall)
)

type inFlightCall struct {
	done   chan struct{}
	result string
	err    error
}

// singleflightSpawn 确保同一 parent 对同一 task 的并发 spawn 只执行一次，复用结果
func singleflightSpawn(spawner TaskSpawner, ctx context.Context, parentID string, spec TaskSpec) (string, error) {
	key := inFlightKey{parentID: parentID, task: spec.Task}
	inFlightMu.Lock()
	if call, ok := inFlight[key]; ok {
		inFlightMu.Unlock()
		select {
		case <-call.done:
			return call.result, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	call := &inFlightCall{done: make(chan struct{})}
	inFlight[key] = call
	inFlightMu.Unlock()

	result, err := spawner.SpawnTask(ctx, parentID, spec)

	call.result = result
	call.err = err
	close(call.done)

	inFlightMu.Lock()
	delete(inFlight, key)
	inFlightMu.Unlock()

	return result, err
}

// ── 工具工厂：由 runtime 注入 Bus + TaskSpawner 后注册 ──

// MakeSpawnTaskTool 创建 spawn_task 工具函数
func MakeSpawnTaskTool(spawner TaskSpawner) ToolFunc {
	return func(ctx context.Context, params map[string]any) (any, error) {
		parentID := AgentIDFromContext(ctx)
		task, _ := params["task"].(string)
		if task == "" {
			return nil, fmt.Errorf("缺少必需参数 task")
		}
		spec := TaskSpec{Task: task}
		if d, ok := params["directive"].(string); ok {
			spec.Directive = d
		}
		if c, ok := params["context"].(string); ok {
			spec.Context = c
		}
		if m, ok := params["model"].(string); ok {
			spec.Model = m
		}

		// 子任务启动/完成/失败的 § 信号由 SpawnTask 统一发送（SpawnTask 知道 childID）
		result, err := singleflightSpawn(spawner, ctx, parentID, spec)
		if err != nil {
			return nil, err
		}
		return map[string]any{"result": result, "status": "completed"}, nil
	}
}

// MakeSendMessageTool 创建 send_message 工具函数
func MakeSendMessageTool(bus Bus) ToolFunc {
	return func(ctx context.Context, params map[string]any) (any, error) {
		from := AgentIDFromContext(ctx)
		to, _ := params["to"].(string)
		content, _ := params["content"].(string)
		if to == "" {
			return nil, fmt.Errorf("缺少必需参数 to")
		}
		err := bus.Send(types.AgentMessage{
			From:    from,
			To:      to,
			Type:    "notify",
			Content: content,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": "sent"}, nil
	}
}

// MakeCheckMessagesTool 创建 check_messages 工具函数
func MakeCheckMessagesTool(bus Bus) ToolFunc {
	return func(ctx context.Context, params map[string]any) (any, error) {
		agentID := AgentIDFromContext(ctx)
		msgs := bus.DrainInbox(agentID)
		if len(msgs) == 0 {
			return map[string]any{"messages": []any{}, "count": 0}, nil
		}
		out := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			out = append(out, map[string]any{
				"from":    m.From,
				"type":    m.Type,
				"content": m.Content,
			})
		}
		return map[string]any{"messages": out, "count": len(out)}, nil
	}
}
