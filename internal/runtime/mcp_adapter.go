package runtime

import (
	"context"
	"fmt"

	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// mcpAdapter 实现 mcpserver.Runtime 接口（只读能力集），
// 将 Runtime 的内部对象转为 MCP Server 可序列化的 map 视图。
// 仅暴露只读操作：检索/列表/加载，不做任何写操作。

// HarnessSearch 在 Harness 索引中检索项目上下文。
// 会话隔离：检索结果过滤为"项目级共享块 + 当前会话私有块"，杜绝
// 同项目不同会话互相检索到对方压缩的对话原文（task/goal 模式串扰根因）。
// 会话归属优先级：ctx 注入的会话（Agent 启动时绑定，GUI 并发多会话精确）
// > 运行时全局当前会话（单个工具调用的兜底）。
func (rt *Runtime) HarnessSearch(ctx context.Context, query string, limit int) []map[string]any {
	if rt.Harness == nil || query == "" {
		// 索引未初始化但有待落库的收集块：清空防泄漏（无索引可写）
		rt.evictPending = nil
		return []map[string]any{}
	}
	// 搜索前冲刷收集区：压缩原文可能还在 evictPending（锁内只收集、
	// 锁外由 flushEvictPending 落库；直接工具调用/测试路径无 flush 环节），
	// 先落库再检索，保证"压缩过就能搜到"语义不变。
	rt.flushEvictPending()
	if limit <= 0 {
		limit = 5
	}
	// 会话 ID：优先 ctx 绑定（并发多会话下精确），回退全局当前会话。
	// 空=未绑定会话，仅看项目级共享块。
	sid := tools.SessionIDFromContext(ctx)
	if sid == "" {
		sid = rt.GetSessionID()
	}
	meta := map[string]string{}
	if sid != "" {
		meta["session_id"] = sid
	}
	blocks := rt.Harness.Search(ctx, query, meta)
	if len(blocks) > limit {
		blocks = blocks[:limit]
	}
	out := make([]map[string]any, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, map[string]any{
			"id":      b.ID,
			"score":   float64(b.Score),
			"content": truncateRunes(b.Content, 2000),
		})
	}
	return out
}

// MemoryList 列出所有历史会话（只读）。
func (rt *Runtime) MemoryList() []map[string]any {
	if rt.Memory == nil {
		return []map[string]any{}
	}
	sessions := rt.Memory.List()
	out := make([]map[string]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, map[string]any{
			"uuid":    s.UUID,
			"title":   s.Title,
			"created": s.CreatedAt.Format("2006-01-02 15:04"),
			"updated": s.UpdatedAt.Format("2006-01-02 15:04"),
		})
	}
	return out
}

// MemoryLoad 加载指定会话的完整历史消息。
func (rt *Runtime) MemoryLoad(uuid string) (map[string]any, error) {
	if rt.Memory == nil {
		return nil, fmt.Errorf("记忆系统未初始化")
	}
	s, err := rt.Memory.Load(uuid)
	if err != nil {
		return nil, err
	}
	events, _ := rt.Memory.LoadEvents(uuid)
	msgs := make([]map[string]any, 0)
	if events != nil {
		for _, ev := range events {
			if ev.Type == "user_msg" || ev.Type == "assistant_msg" {
				content := ""
				if m, ok := ev.Data.(map[string]any); ok {
					content, _ = m["content"].(string)
				}
				msgs = append(msgs, map[string]any{"role": ev.Type, "content": truncateRunes(content, 2000)})
			}
		}
	}
	return map[string]any{
		"uuid":     s.UUID,
		"title":    s.Title,
		"created":  s.CreatedAt.Format("2006-01-02 15:04"),
		"updated":  s.UpdatedAt.Format("2006-01-02 15:04"),
		"messages": msgs,
	}, nil
}

// SkillsList 列出所有可用技能（只读）。
func (rt *Runtime) SkillsList() []map[string]any {
	if rt.skillLoader == nil {
		return []map[string]any{}
	}
	skills := rt.skillLoader.List()
	out := make([]map[string]any, 0, len(skills))
	for _, sk := range skills {
		out = append(out, map[string]any{"name": sk.Name, "description": sk.Description})
	}
	return out
}

var _ = types.Skill{} // 保持依赖引用（Skill 类型经由 skillLoader 使用）
