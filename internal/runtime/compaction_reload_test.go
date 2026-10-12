package runtime

import (
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// joinHistory 将历史消息展平为纯文本（测试断言用）。
func joinHistory(hist []types.Message) string {
	var sb strings.Builder
	for _, m := range hist {
		sb.WriteString(m.TextContent())
		sb.WriteString("\n")
	}
	return sb.String()
}

// TestLoadSession_MergesPostCompactionEvents 回归测试（GUI 强制中止断开后再继续
// 对话"不再连续"根因修复）：
//
//  1. 长对话触发压缩 → 快照落盘（CompactedMessages + CompactedSeq）；
//  2. 压缩后继续新对话（快照覆盖点之后的事件）；
//  3. 模拟重连/重启：新 Runtime 在同一存储目录 LoadSession；
//  4. 断言：加载后的历史 = 快照基线 + 压缩后的新对话（后者不得丢失）。
func TestLoadSession_MergesPostCompactionEvents(t *testing.T) {
	tmpDir := t.TempDir()
	rt := newTestRuntimeAt(t, tmpDir)

	// 会话建立 + 第一轮对话
	sess := rt.NewSession("")
	sid := sess.UUID
	rt.AddToHistory("user", "第一问：请帮我分析压缩持久化的设计。")
	rt.AddToHistory("assistant", "第一答：好的，压缩持久化的要点是快照+事件日志双写。")

	// 长对话撑爆预算 → 触发压缩 + 快照持久化
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", strings.Repeat("这是一段足够长的压测用户消息，用来撑爆上下文预算并触发压缩。", 20))
		rt.AddToHistory("assistant", strings.Repeat("这是助手的长回复，包含详细的分析说明与代码片段，用于模拟真实对话中的大块输出。", 20))
	}

	// 压缩后继续新对话（快照覆盖点之后的事件——历史缺陷中"丢失"的部分）
	rt.AddToHistory("user", "压缩后的新问题：GUI 断开重连后对话还连续吗？")
	rt.AddToHistory("assistant", "压缩后的新回答：必须连续，增量事件会与快照合并。")

	// 模拟重连/重启：新 Runtime 同一目录加载会话
	rt2 := newTestRuntimeAt(t, tmpDir)
	if _, _, err := rt2.LoadSession(sid); err != nil {
		t.Fatalf("LoadSession 失败: %v", err)
	}
	hist := rt2.GetConversationHistory()
	joined := joinHistory(hist)

	// 压缩后的新对话必须存在（根因断言：快照覆盖前丢失）
	if !strings.Contains(joined, "压缩后的新问题") {
		t.Errorf("压缩后的 user 消息在重新加载后丢失: %q", joined)
	}
	if !strings.Contains(joined, "压缩后的新回答") {
		t.Errorf("压缩后的 assistant 回复在重新加载后丢失: %q", joined)
	}
	// 压缩前的对话经滚动摘要保留（首轮内容进入摘要）
	if !strings.Contains(joined, "压缩持久化") {
		t.Errorf("压缩前的对话应经滚动摘要保留: %q", joined)
	}
	// 同一消息不得重复（快照覆盖点与增量边界不重叠）
	if strings.Count(joined, "压缩后的新问题") != 1 {
		t.Errorf("增量消息重复合并（count=%d）", strings.Count(joined, "压缩后的新问题"))
	}
}

// TestLoadSession_NoIncrementalDuplication 回归测试：压缩快照 + 增量事件合并
// 不得造成任何消息重复或丢失——事件日志中的每条消息在加载后的历史中
// 恰好出现一次（快照覆盖点边界对齐的语义保证）。
func TestLoadSession_NoIncrementalDuplication(t *testing.T) {
	tmpDir := t.TempDir()
	rt := newTestRuntimeAt(t, tmpDir)

	sess := rt.NewSession("")
	sid := sess.UUID
	// 触发压缩的消息（会成为快照的一部分）+ 压缩后继续的消息（增量）
	for i := 0; i < 8; i++ {
		rt.AddToHistory("user", strings.Repeat("压测用户消息，撑爆预算触发压缩快照。", 20))
		rt.AddToHistory("assistant", strings.Repeat("助手长回复，占据上下文预算。", 20))
	}
	rt.AddToHistory("user", "压缩后的增量问题")
	rt.AddToHistory("assistant", "压缩后的增量回答")

	rt2 := newTestRuntimeAt(t, tmpDir)
	if _, _, err := rt2.LoadSession(sid); err != nil {
		t.Fatalf("LoadSession 失败: %v", err)
	}
	hist := rt2.GetConversationHistory()

	// 以事件日志为真值：快照覆盖点（CompactedSeq）之后的增量消息必须在
	// 加载后历史中恰好出现一次（不丢失）；覆盖点之内的事件若保留则最多
	// 一次（不重复）——压缩丢弃的旧消息允许出现 0 次。
	session, err := rt2.Memory.Load(sid)
	if err != nil {
		t.Fatalf("读取会话元数据失败: %v", err)
	}
	events, err := rt2.Memory.LoadEvents(sid)
	if err != nil {
		t.Fatalf("读取事件日志失败: %v", err)
	}
	counts := map[string]int{}
	for _, m := range hist {
		if c, ok := m.Content.(string); ok && c != "" {
			counts[m.Role+"|"+c]++
		}
	}
	// 日志中同一内容可出现多次（压测消息重复写入），按频次对比：
	// - 快照覆盖点之后的增量事件：日志出现次数 == 历史出现次数（不丢失）
	// - 覆盖点之内的事件：历史出现次数 <= 日志出现次数（压缩只减不增，不重复）
	eventCounts := map[string]int{}
	for _, evt := range events {
		if m := eventToMessage(evt); m != nil {
			if c, ok := m.Content.(string); ok && c != "" {
				eventCounts[m.Role+"|"+c]++
			}
		}
	}
	incrCounts := map[string]int{}
	for _, evt := range events {
		if evt.Seq <= session.CompactedSeq {
			continue
		}
		if m := eventToMessage(evt); m != nil {
			if c, ok := m.Content.(string); ok && c != "" {
				incrCounts[m.Role+"|"+c]++
			}
		}
	}
	for key, incr := range incrCounts {
		if counts[key] != incr {
			t.Errorf("压缩后增量消息历史出现 %d 次（日志 %d 次，必须相等）: %s",
				counts[key], incr, key)
		}
	}
	for key, logN := range eventCounts {
		if counts[key] > logN {
			t.Errorf("快照内消息被重复合并：历史 %d 次 > 日志 %d 次: %.60s",
				counts[key], logN, key)
		}
	}
}

// TestPersistencePaths_ContainsSkillDir 知识索引前置：PersistencePaths 必须
// 声明 skills_dir（供系统提示词知识索引引用）。
func TestPersistencePaths_ContainsSkillDir(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	paths := rt.PersistencePaths()
	if paths["skills_dir"] == "" {
		t.Error("PersistencePaths 缺少 skills_dir")
	}
	if paths["plugins_dir"] == "" {
		t.Error("PersistencePaths 缺少 plugins_dir")
	}
}
