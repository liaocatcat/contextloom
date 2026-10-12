package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/memory"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestCompressContext_FlushesEvictPending 回归：CompressContext 锁内只收集
// 压缩原文（不触碰 Harness），解锁后 flushEvictPending 必须清空收集区——
// 否则下一次压缩/检索会把旧原文重复入索引（索引膨胀 + 检索污染）。
// 同时验证压缩产生的原文已可被检索到（"压缩过就能搜到"语义不变）。
func TestCompressContext_FlushesEvictPending(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("压缩冲刷测试")
	// 长历史触发压缩
	big := strings.Repeat("压缩冲刷测试内容触发压缩", 30)
	for i := 0; i < 10; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}
	msg := rt.CompressContext()
	if strings.Contains(msg, "无需压缩") {
		t.Fatalf("长历史压缩应产生释放，got %s", msg)
	}

	// 收集区必须已清空（锁外 flush 已执行）
	rt.historyMu.RLock()
	pending := len(rt.evictPending)
	rt.historyMu.RUnlock()
	if pending != 0 {
		t.Fatalf("CompressContext 后 evictPending 应清空（锁外已 flush），got %d", pending)
	}

	// 压缩原文应已可检索（flush 已落库）
	res := rt.HarnessSearch(context.Background(), "压缩冲刷测试", 5)
	if len(res) == 0 {
		t.Fatal("压缩后应能检索到压缩原文（flush 未落库）")
	}
}

// TestAddToHistory_FlushesEvictPending 回归：AddToHistory 渐进压缩收集的
// 原文在解锁后必须落库（否则压缩过的内容永远检索不到）。
func TestAddToHistory_FlushesEvictPending(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(1500)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("增量压缩冲刷测试")
	big := strings.Repeat("增量压缩冲刷触发压缩", 30)
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}

	rt.historyMu.RLock()
	pending := len(rt.evictPending)
	rt.historyMu.RUnlock()
	if pending != 0 {
		t.Fatalf("AddToHistory 后 evictPending 应清空，got %d", pending)
	}
	res := rt.HarnessSearch(context.Background(), "增量压缩冲刷", 5)
	if len(res) == 0 {
		t.Fatal("增量压缩的原文应可检索到（flush 未落库）")
	}
}

// TestCompactionSnapshot_NoImageDataURI 回归：压缩快照落盘（CompactedMessages）
// 不得携带图片 dataURI——否则 session.json 膨胀到几十 MB，之后每次
// LoadSession 读盘+解析巨文件卡死（"压缩后历史记录不能加载"根因之一）。
// 图片在事件日志已有 [图片: 路径] 锚点，重载时按路径重建，快照无需带图。
func TestCompactionSnapshot_NoImageDataURI(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	mem, err := memory.New(tmpDir, 10)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	rt.NewSession("快照图片降级测试")
	// 混入含图消息：历史里同时有文本消息与带 dataURI 的图片消息
	imgMsg := types.Message{Role: "user", Content: []types.ContentPart{
		{Type: "text", Text: "看图"},
		{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}},
	}}
	rt.historyMu.Lock()
	rt.conversationHistory = append(rt.conversationHistory, imgMsg)
	rt.historyMu.Unlock()
	big := strings.Repeat("触发压缩的快照降级测试内容", 30)
	for i := 0; i < 8; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}
	rt.CompressContext()

	session, err := rt.Memory.Load(rt.GetSessionID())
	if err != nil {
		t.Fatalf("加载会话: %v", err)
	}
	if len(session.CompactedMessages) == 0 {
		t.Fatal("CompactedMessages 不应为空——压缩后未持久化")
	}
	for i, m := range session.CompactedMessages {
		if m.ImagePartCount() > 0 {
			t.Fatalf("快照消息[%d] 不应携带图片 dataURI（session.json 膨胀根因）: %+v", i, m.Content)
		}
		if c := m.TextContent(); strings.Contains(c, "data:image/") {
			t.Fatalf("快照消息[%d] 文本不应含 dataURI: %q", i, c)
		}
	}
}
