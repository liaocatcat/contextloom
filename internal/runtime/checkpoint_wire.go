package runtime

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/checkpoint"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/trace"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// checkpoints 检查点存储（多 Agent 持久化；对标 LangGraph checkpointer 的
// 零依赖 JSONL 落地）。在 runtime.New 中初始化。
func (rt *Runtime) initCheckpoints() {
	st, err := checkpoint.New(filepath.Join(rt.ProjectRoot(), ".contextloom", "checkpoints"))
	if err != nil {
		return // 检查点失败不阻断（非致命设计）
	}
	// 启动路径不做全量扫描/修剪（历史缺陷：启动即 Prune → listLocked 对每个
	// run 文件全量读取取最后一行，巨文件场景卡 12s+ → GUI 切换项目超时）。
	// run 数（run-main/run-agent-N）本身有限（十数级），膨胀主因是单文件
	// 无界追加——已由 Save 内文件大小限流（rolloverLocked）治本。
	//
	// 减容增量：run 文件数收敛用 PruneAged（只读 mtime，不解析内容，
	// 开销与单文件大小无关，启动路径 <1ms）——子任务 runID 无限递增，
	// 长时间运行后 run-*.jsonl 会累积到成百上千个，即使单文件 MB 级
	// 总量也达 GB 级。保留最近 200 个（含主任务/历史子任务各量级），
	// 超出部分按文件修改时间删除最旧的。
	st.PruneAged(maxKeepCheckpointRuns) //nolint:errcheck // 清理失败不阻断
	rt.checkpoints = st
}

// maxKeepCheckpointRuns 启动收敛时保留的 run 文件数（按 mtime 最近）。
const maxKeepCheckpointRuns = 200

// CheckpointStore 返回检查点存储（nil=未初始化）。
func (rt *Runtime) CheckpointStore() *checkpoint.Store {
	return rt.checkpoints
}

// CheckpointsSummary 返回检查点列表摘要（供 /checkpoints 命令展示）。
func (rt *Runtime) CheckpointsSummary() []map[string]any {
	if rt.checkpoints == nil {
		return nil
	}
	metas, err := rt.checkpoints.List()
	if err != nil {
		return nil
	}
	out := make([]map[string]any, 0, len(metas))
	for _, m := range metas {
		out = append(out, map[string]any{
			"run_id":     m.RunID,
			"round":      m.Round,
			"done":       m.Done,
			"mode":       m.Mode,
			"session_id": m.SessionID,
			"updated_at": m.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}
	return out
}

// attachCheckpointSink 给 agent 挂每轮检查点落盘回调（nil 安全）。
// runID 用 agent 的任务 ID（task/goal 主 Agent 恒为 "main"，
// 同一 run 文件内按轮追加快照，LoadLatest 取最新——goal 跨轮可恢复）。
//
// includeMessages=false（子任务）不落 Messages：子任务从不恢复检查点
// （SpawnTask 无 restoreFromCheckpoint 调用），消息体纯属死数据——
// 历史上 run-sub-*.jsonl 每轮全量写对话，是缓存膨胀的隐性主源。
// includeMessages=true（主任务，恢复用）也只保留最近 keepCheckpointMsgCount
// 条消息：跨轮恢复只需最近上下文，全量历史由 memory/session 承担。
func (rt *Runtime) attachCheckpointSink(agent interface {
	SetCheckpointSink(fn func(round int, messages []types.Message, done bool, result string))
}, runID, sessionID, mode string, includeMessages bool) {
	if rt.checkpoints == nil {
		return
	}
	agent.SetCheckpointSink(func(round int, messages []types.Message, done bool, result string) {
		var msgs []byte
		if includeMessages {
			msgs = truncateMessagesForCheckpoint(messages)
		}
		_ = rt.checkpoints.Save(checkpoint.Checkpoint{
			RunID:     runID,
			SessionID: sessionID,
			Mode:      mode,
			Round:     round,
			Messages:  msgs,
			Done:      done,
			Result:    result,
		})
	})
}

// keepCheckpointMsgCount 检查点保存的消息条数上限（最近 N 条）。
// 恢复语义只需最近上下文；全量历史由 memory/session 承担，不必双写。
const keepCheckpointMsgCount = 40

// truncateMessagesForCheckpoint 裁剪消息到最近 keepCheckpointMsgCount 条
// （保留尾部窗口，恢复的是"最新状态"）。空/超限安全，返回 nil 表示不落盘。
func truncateMessagesForCheckpoint(messages []types.Message) []byte {
	if len(messages) == 0 {
		return nil
	}
	msgs := messages
	if len(msgs) > keepCheckpointMsgCount {
		msgs = msgs[len(msgs)-keepCheckpointMsgCount:]
	}
	data, err := json.Marshal(msgs)
	if err != nil {
		return nil
	}
	return data
}

// restoreFromCheckpoint 从检查点恢复 user/assistant 对话消息
// （提取非 system/tool 消息，避免重复注入系统提示词）。
// 返回 (注入 Agent 的历史, 是否实际读取到未完成检查点)。
//
// ⚠️ 回归修复（"压缩只作用于表面、实际发送仍全量"）：
// 旧实现返回 nil 表示"无检查点"，调用方据此【直接采用】内存压缩历史——这
// 是正确的；但 goal/task 模式下一旦存在未完成检查点，就用检查点里的消息
// 【整体覆盖】内存历史。而检查点在 agent 每轮把【构建消息列表 messages】写盘
// （sinkCheckpoint），该列表来自 buildMessages 注入后累积的全量对话——
// 压缩只改了 runtime.conversationHistory，checkpoint 里仍是压缩前的全量！
// 于是压缩后下一轮 launchAgent 恢复 → Agent 拿到全量 → "表面压缩了，实际
// 发送还是全量的"（用户实锤）。
//
// 修复：返回 hasRestore 语义由调用方决策；本函数内部把检查点恢复的消息
// 与内存压缩历史【合并】（检查点消息按 content/role 去重衔接——只追加
// 内存历史中还没有的尾部消息，绝不倒灌被压缩掉的旧消息），并经同一套
// ctxBudget 预算压缩兜底，保证恢复后 token 占用与压缩后一致。
func (rt *Runtime) restoreFromCheckpoint(runID string) ([]types.Message, bool) {
	if rt.checkpoints == nil {
		return nil, false
	}
	cp, err := rt.checkpoints.LoadLatest(runID)
	if err != nil || cp.Done {
		return nil, false // 无检查点或已正常完成 → 不恢复
	}
	var msgs []types.Message
	if err := json.Unmarshal(cp.Messages, &msgs); err != nil {
		return nil, false
	}
	restored := make([]types.Message, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "user" || m.Role == "assistant" {
			restored = append(restored, m)
		}
	}
	if len(restored) == 0 {
		return nil, false
	}
	// 与内存压缩历史合并：检查点消息可能包含压缩已丢弃的旧消息，必须只
	// 衔接"内存历史之后"的增量。去重键：role + content（正文一致即视同
	// 同一条，避免思维链/图片字段差异误判）。
	rt.historyMu.RLock()
	memHist := make([]types.Message, len(rt.conversationHistory))
	copy(memHist, rt.conversationHistory)
	sid := rt.currentSessionID
	rt.historyMu.RUnlock()

	// 内存历史非空时，从检查点里找到与内存历史尾部衔接的起点：跳过与
	// 内存历史重复的消息，只追加新增尾部。衔接用 content 精确匹配（压缩
	// 只截断/丢弃/摘要，不篡改保留消息正文）。
	start := 0
	restored = trimSummaryAndSystem(restored)
	if len(memHist) > 0 {
		// 从恢复列表尾部向前匹配内存历史最后一条（防重复前缀+增量尾部）
		lastMem := lastContentKey(memHist[len(memHist)-1])
		for i := len(restored) - 1; i >= 0; i-- {
			if contentKey(restored[i]) == lastMem {
				start = i + 1
				break
			}
		}
	}
	var merged []types.Message
	if start <= 0 {
		// 无衔接（恢复列表与内存历史完全不同）：以内存历史为基线——
		// 压缩已把旧消息换成了 [历史摘要]，绝不能倒灌回被压缩的原文。
		// 内存历史为空（重启/崩溃后首次）时则采用检查点消息。
		if len(memHist) > 0 {
			merged = memHist
		} else {
			merged = restored
		}
	} else {
		merged = append(append([]types.Message(nil), memHist...), restored[start:]...)
	}
	// 同一套预算压缩兜底：恢复后总量仍必须回到红线内（与 AddToHistory/
	// LoadSession 压缩策略一致），并把被压缩原文入检索索引（可被 AI 找回）
	if rt.ctxBudget != nil && len(merged) > 0 {
		compacted, changed := ctxmgr.CompactToBudgetWithEvict(merged, rt.ctxBudget, func(m types.Message) { rt.indexCompressedEvictedFor(sid, m) })
		if changed && compacted != nil {
			merged = compacted
			if sm := rt.ctxBudget.SummaryMessage(); sm != nil {
				first := ""
				if len(merged) > 0 {
					first, _ = merged[0].Content.(string)
				}
				if !strings.HasPrefix(first, "[历史摘要]") {
					merged = append([]types.Message{*sm}, merged...)
				}
			}
		}
	}
	if len(merged) == 0 {
		return nil, false
	}
	return merged, true
}

// trimSummaryAndSystem 剔除恢复消息里的 [历史摘要] 前缀与 system 残留，
// 避免与内存历史合并时摘要重复注入/系统指令混入对话上下文。
func trimSummaryAndSystem(msgs []types.Message) []types.Message {
	out := msgs[:0]
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		if s, ok := m.Content.(string); ok && strings.HasPrefix(s, "[历史摘要]") {
			continue
		}
		out = append(out, m)
	}
	return out
}

// contentKey 生成消息的去重键（role + 正文）。tool_calls/图片消息正文为空
// 时退化用 role+ToolCallID，仍可稳定区分。
func contentKey(m types.Message) string {
	if s, ok := m.Content.(string); ok && s != "" {
		return m.Role + "|" + s
	}
	if len(m.ToolCalls) > 0 {
		return m.Role + "|tool:" + m.ToolCalls[0].ID
	}
	return m.Role + "|" + m.ToolCallID
}

// lastContentKey 取消息去重键（空键时回退空串，避免全空误配）。
func lastContentKey(m types.Message) string {
	k := contentKey(m)
	if m.Role == "user" && k == "user|" {
		return ""
	}
	return k
}

// ── OTel trace 导出接线 ──

// initTraceExporter 在 trace Recorder 上挂 OTLP 导出器（配置了 endpoint 时）。
func (rt *Runtime) initTraceExporter() {
	if rt.Trace == nil || rt.Config.Trace.ExportEndpoint == "" {
		return
	}
	exp := trace.NewExporter(rt.Config.Trace.ExportEndpoint, rt.Config.Trace.ExportHeaders)
	if exp == nil {
		return
	}
	exp.SetBatch(rt.Config.Trace.ExportBatch)
	exp.SetInterval(rt.Config.Trace.ExportInterval)
	exp.Start()
	rt.Trace.SetSink(exp)
	rt.traceExporter = exp
}

// CloseTraceExport 停止 OTLP 导出器并冲刷剩余（进程收尾时调用）。
func (rt *Runtime) CloseTraceExport() {
	if rt.traceExporter != nil {
		_ = rt.traceExporter.Close()
		rt.traceExporter = nil
	}
}
