package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/checkpoint"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/memory"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestRestoreFromCheckpoint_NoRollbackAfterCompaction 回归：
// 压缩只作用于内存 conversationHistory，checkpoint 里仍是压缩前的全量——
// 旧 restoreFromCheckpoint 用检查点消息整体覆盖内存历史，导致 goal/task
// 模式压缩后下一轮发送全量（"表面压缩了，实际发送还是全量的"实锤根因）。
//
// 修复后：恢复必须与内存压缩历史合并衔接（只追加增量，不倒灌被压缩的
// 旧消息），且返回经预算压缩后的消息——token 占用与压缩后一致。
func TestRestoreFromCheckpoint_NoRollbackAfterCompaction(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("压缩后恢复不倒灌")

	// 1. 写入 10 轮大消息 → 触发渐进压缩，内存历史被压到远小于全量
	big := strings.Repeat("压缩后恢复不倒灌的旧消息内容", 40)
	for i := 0; i < 10; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}
	// 记录压缩后的内存历史（token 与条数）
	rt.historyMu.RLock()
	memAfterCompact := make([]types.Message, len(rt.conversationHistory))
	copy(memAfterCompact, rt.conversationHistory)
	memTokens := ctxmgr.MessagesTokens(rt.conversationHistory)
	rt.historyMu.RUnlock()

	// 2. 手工构造一个"压缩前全量"的检查点（模拟旧行为：agent 每轮把构建的
	//    全量 messages 写盘——压缩只改内存历史，checkpoint 仍旧数据）
	store := rt.CheckpointStore()
	if store == nil {
		t.Fatal("checkpoint store 未初始化")
	}
	fullMsgs := make([]types.Message, 0, 40)
	fullMsgs = append(fullMsgs, types.Message{Role: "user", Content: "第0轮旧问题"})
	fullMsgs = append(fullMsgs, types.Message{Role: "assistant", Content: "第0轮旧回答"})
	for i := 1; i <= 10; i++ {
		fullMsgs = append(fullMsgs, types.Message{Role: "user", Content: big})
		fullMsgs = append(fullMsgs, types.Message{Role: "assistant", Content: big})
	}
	cpData, err := json.Marshal(fullMsgs)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := store.Save(checkpoint.Checkpoint{
		RunID:    "main",
		Round:    3,
		Messages: cpData,
		Done:     false,
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 3. 调用恢复——修复前会整体覆盖为全量（40 条），修复后只保留
	//    内存压缩历史 + 尾部增量，token 应远小于全量
	restored, hasRestore := rt.restoreFromCheckpoint("main")
	if !hasRestore || restored == nil {
		t.Fatalf("应恢复检查点（未完成），got hasRestore=%v restored=%v", hasRestore, restored)
	}

	// 内存历史最后一条（assistant 大消息）应与恢复列表衔接——恢复列表
	// 至少包含内存历史（不倒灌），且 token 不超过内存压缩历史太多
	restoredTokens := ctxmgr.MessagesTokens(restored)

	// 4. 断言：恢复结果 token 不得超过压缩后内存历史的 2 倍（合并了尾部
	//    增量最多翻倍；旧 bug 会把全量 40 条倒灌回来，token 超一个量级）
	if restoredTokens > memTokens*2+1000 {
		t.Fatalf("恢复结果 token %d 远超压缩后内存历史 %d——压缩被检查点倒灌回滚（bug）", restoredTokens, memTokens)
	}
	// 且恢复列表必须覆盖内存历史尾部（衔接成功，不丢压缩后的最新上下文）
	if len(restored) < len(memAfterCompact) {
		t.Fatalf("恢复结果 %d 条 < 内存压缩历史 %d 条——丢失了压缩后的上下文", len(restored), len(memAfterCompact))
	}
	// 检查点里的最旧消息（"第0轮旧问题"）绝不能被倒灌回上下文
	for _, m := range restored {
		if s, ok := m.Content.(string); ok && strings.Contains(s, "第0轮旧问题") {
			t.Fatalf("被压缩丢弃的旧消息被检查点倒灌回恢复结果: %q", s)
		}
	}
}

// TestRestoreFromCheckpoint_NoCheckpointKeepsMemory 回归：无检查点/已完成时
// 必须返回 hasRestore=false，调用方保留内存压缩历史（不回退、不覆盖）。
func TestRestoreFromCheckpoint_NoCheckpointKeepsMemory(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("无检查点")
	rt.AddToHistory("user", "你好")
	rt.AddToHistory("assistant", "你好！")

	restored, hasRestore := rt.restoreFromCheckpoint("main")
	if hasRestore || restored != nil {
		t.Fatalf("无检查点时应返回 hasRestore=false, restored=nil，got %v %v", hasRestore, restored)
	}
	// 内存历史不受影响
	rt.historyMu.RLock()
	n := len(rt.conversationHistory)
	rt.historyMu.RUnlock()
	if n != 2 {
		t.Fatalf("内存历史应保持 2 条，got %d", n)
	}
}

// sentCaptureBackend 捕获每次 Chat/Stream 调用收到的 messages，用于断言
// "实际发送给 AI 的内容"是压缩后量级而非检查点全量。
type sentCaptureBackend struct {
	mockBackend
	sentMessages []types.Message
	sentTokens   int
}

func (c *sentCaptureBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	c.sentMessages = append([]types.Message(nil), messages...)
	c.sentTokens = ctxmgr.MessagesTokens(messages)
	return c.chatResponse, nil, c.chatErr
}

func (c *sentCaptureBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	c.sentMessages = append([]types.Message(nil), messages...)
	c.sentTokens = ctxmgr.MessagesTokens(messages)
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	if c.chatResponse != "" {
		ch <- c.chatResponse
	}
	close(ch)
	close(errCh)
	return ch, errCh
}

// TestLaunchAgent_TaskMode_NoFullRollback 端到端回归：task 模式已有未完成
// 检查点（含压缩前全量）时，launchAgent 注入 Agent 的历史必须是压缩后的
// 内存历史（合并增量、不超过压缩预算），绝不能是检查点里的全量——
// 这直接复现"压缩了，下一轮发送还是全量"的用户实锤场景。
func TestLaunchAgent_TaskMode_NoFullRollback(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(3000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("task 压缩全链路")

	// 1. 写入大量消息触发压缩（内存历史被压到远小于全量）
	big := strings.Repeat("task模式压缩全链路旧消息填充", 40)
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}
	msg := rt.CompressContext()
	if strings.Contains(msg, "无需压缩") {
		t.Fatalf("长历史应能压缩，got %s", msg)
	}
	rt.historyMu.RLock()
	memTokens := ctxmgr.MessagesTokens(rt.conversationHistory)
	rt.historyMu.RUnlock()
	if memTokens == 0 {
		t.Fatal("压缩后内存历史不应为空")
	}

	// 2. 写入一条"压缩后新增"的增量消息（应保留、衔接在恢复结果尾部）
	rt.AddToHistory("user", "压缩之后的新问题")
	rt.AddToHistory("assistant", "压缩之后的新回答")
	rt.historyMu.RLock()
	memTokens = ctxmgr.MessagesTokens(rt.conversationHistory)
	rt.historyMu.RUnlock()

	// 3. 构造"压缩前全量"的未完成检查点（旧行为：agent 每轮写全量 messages）
	store := rt.CheckpointStore()
	if store == nil {
		t.Fatal("checkpoint store 未初始化")
	}
	fullMsgs := []types.Message{{Role: "user", Content: "最古老的旧消息"}}
	for i := 0; i < 12; i++ {
		fullMsgs = append(fullMsgs,
			types.Message{Role: "user", Content: big},
			types.Message{Role: "assistant", Content: big},
		)
	}
	cpData, _ := json.Marshal(fullMsgs)
	if err := store.Save(checkpoint.Checkpoint{RunID: "main", Round: 3, Messages: cpData, Done: false}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// 4. task 模式发起请求（走 launchAgent 完整路径）
	//    ProcessInput 内部按当前模式（task）构造 TaskCreate 并调用 launchAgent；
	//    这里显式设置模式以命中"goal/task 恢复检查点"分支。
	//    注入捕获 backend：抓实际发送给 AI 的 messages 做 token 断言。
	rt.SetMode("task")
	capture := &sentCaptureBackend{}
	capture.chatResponse = "检查完成"
	rt.Backend = capture
	ch, errCh, err := rt.ProcessInput(t.Context(), "请执行检查")
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	// 消费流直到结束（backend mock 直接返回，不会真调用外部）
	done := false
	for !done {
		select {
		case _, ok := <-ch:
			if !ok {
				done = true
			}
		case err, ok := <-errCh:
			if ok && err != nil {
				t.Fatalf("流错误: %v", err)
			}
			if ok {
				done = true
			}
		}
	}

	// 5. 断言：Agent 实际发送的消息 token 与压缩后内存历史同量级，
	//    而非检查点全量（旧 bug 下恢复整体覆盖为全量 → 发送全量）。
	//    修复后发送 = system JSON(固定~3k) + 压缩历史渲染 + payload（~4k），
	//    全量 checkpoint 直接注入会达到 11k+；且压缩块不再自动回灌。
	if capture.sentTokens == 0 {
		t.Fatal("未捕获到实际发送的消息")
	}
	fullTokens := ctxmgr.MessagesTokens(fullMsgs)
	if capture.sentTokens >= fullTokens {
		t.Fatalf("实际发送 token %d ≥ 全量 %d——压缩被检查点回滚", capture.sentTokens, fullTokens)
	}
	// 压缩原文块不得自动注入请求（"压缩≈没压缩"的另一通道）。
	// 内联注入均为 user role；system 的 context_keys 只是引用清单（允许出现）。
	for _, m := range capture.sentMessages {
		if m.Role != "user" {
			continue
		}
		s, ok := m.Content.(string)
		if !ok {
			continue
		}
		if strings.Contains(s, "[压缩原文 ") {
			short := s
			if r := []rune(s); len(r) > 100 {
				short = string(r[:100]) + "..."
			}
			t.Fatalf("压缩原文块被自动回灌进请求(user): %q", short)
		}
	}

	// 5. 断言：Agent 恢复的历史是压缩后的内存历史 + 增量，而非检查点全量。
	//    通过内存历史验证：launchAgent 恢复不改内存 conversationHistory，
	//    且恢复结果 token 上限由预算兜底——直接验证内存历史没有被覆盖成
	//    全量（旧 bug 下 launchAgent 只是把 checkpoint 全量塞给 Agent，
	//    内存历史本身不变；Agent 侧历史通过 ConversationHistory() 暴露）。
	//    这里通过 ProcessInput 后的内存历史 + 恢复函数双重确认：内存历史
	//    仍含"压缩之后的新问题"（增量没丢），且不含"最古老的旧消息"
	//    （没被倒灌）。
	rt.historyMu.RLock()
	found := false
	rollback := false
	for _, m := range rt.conversationHistory {
		if s, ok := m.Content.(string); ok {
			if strings.Contains(s, "压缩之后的新问题") {
				found = true
			}
			if strings.Contains(s, "最古老的旧消息") {
				rollback = true
			}
		}
	}
	rt.historyMu.RUnlock()
	if rollback {
		t.Fatal("被压缩丢弃的旧消息被倒灌——内存历史被检查点回滚")
	}
	if !found {
		t.Fatal("压缩后的新增消息丢失——恢复未衔接增量")
	}
	if memTokens <= 0 {
		t.Fatal("内存压缩历史 token 不应为 0")
	}
}
func TestRestoreFromCheckpoint_DoneCheckpoint(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("已完成检查点")
	cpData, _ := json.Marshal([]types.Message{{Role: "user", Content: "旧"}})
	store := rt.CheckpointStore()
	if store == nil {
		t.Fatal("checkpoint store 未初始化")
	}
	if err := store.Save(checkpoint.Checkpoint{RunID: "main", Round: 1, Messages: cpData, Done: true}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	restored, hasRestore := rt.restoreFromCheckpoint("main")
	if hasRestore || restored != nil {
		t.Fatalf("已完成检查点不应恢复，got hasRestore=%v restored=%v", hasRestore, restored)
	}
}
