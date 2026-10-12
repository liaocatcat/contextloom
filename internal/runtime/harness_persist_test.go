package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// harness_search 工具注册与执行
// =============================================================================

func TestRuntime_HarnessSearchTool_Registered(t *testing.T) {
	rt, _ := newTestRuntime(t)
	t.Cleanup(rt.CloseHarness)

	names := rt.Tools.List()
	found := false
	for _, n := range names {
		if n == "harness_search" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("harness_search 未注册到本机 Agent 工具表, got %v", names)
	}

	// 定义必须携带完整 schema（required query）——缺失时模型无法构造合法调用
	var def *types.ToolDefinition
	for _, d := range rt.Tools.ToolDefinitions() {
		if d.Function.Name == "harness_search" {
			def = &d
			break
		}
	}
	if def == nil {
		t.Fatal("ToolDefinitions 中无 harness_search")
	}
	params, ok := def.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("harness_search schema 缺 properties: %v", def.Function.Parameters)
	}
	if _, ok := params["query"]; !ok {
		t.Error("schema 缺 query 参数")
	}
	req, ok := def.Function.Parameters["required"].([]string)
	if !ok || len(req) == 0 {
		t.Error("schema 缺 required（query 必填）")
	}
}

func TestRuntime_HarnessSearchTool_Executes(t *testing.T) {
	rt, _ := newTestRuntime(t)
	t.Cleanup(rt.CloseHarness)

	rt.Harness.Index(context.Background(), []types.ContextBlock{
		{ID: "c1", Content: "[压缩原文 assistant] 修复了 token 估算偏差问题", Score: 50},
		{ID: "c2", Content: "项目配置说明文档", Score: 50},
	})

	res, err := rt.Tools.Execute(context.Background(), types.ToolCall{
		ID:   "t1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "harness_search",
			Arguments: `{"query":"token 估算","limit":5}`,
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("Execute 返回错误: %s", res.Error)
	}
	items, ok := res.Result.([]map[string]any)
	if !ok {
		t.Fatalf("Result 类型 = %T, want []map[string]any", res.Result)
	}
	if len(items) == 0 {
		t.Fatal("检索无命中——压缩原文应可找回")
	}
	if items[0]["id"] != "c1" {
		t.Errorf("命中 id = %v, want c1", items[0]["id"])
	}
	if content, _ := items[0]["content"].(string); content == "" {
		t.Error("命中内容为空")
	}
}

func TestRuntime_HarnessSearchTool_EmptyQueryRejected(t *testing.T) {
	rt, _ := newTestRuntime(t)
	t.Cleanup(rt.CloseHarness)

	res, err := rt.Tools.Execute(context.Background(), types.ToolCall{
		ID:   "t2",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "harness_search",
			Arguments: `{"query":"  "}`,
		},
	})
	if err != nil {
		t.Fatalf("Execute 不应返回 Go error（错误走 ToolResult.Error）: %v", err)
	}
	if res.Error == "" {
		t.Fatal("空 query 应返回错误")
	}
}

// =============================================================================
// 索引持久化闭环：跨进程重启找回
// =============================================================================

func TestRuntime_HarnessPersist_AcrossRestart(t *testing.T) {
	// 手动目录：进程内两个 Runtime 的 audit/trace 句柄可能阻止 Windows 删除，
	// 自动清理失败会误报 FAIL——这里忽略清理错误，专注断言本身。
	tmpDir, err := os.MkdirTemp("", "contextloom-persist-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir) // 忽略占用导致的清理失败

	// 第一进程：写入检索块并保存
	rt1 := newTestRuntimeAt(t, tmpDir)
	rt1.Harness.Index(context.Background(), []types.ContextBlock{
		{ID: "persist-1", Content: "[压缩原文 user] 被压缩掉的完整需求描述", Score: 50},
	})
	if err := rt1.SaveHarnessIndex(); err != nil {
		t.Fatalf("SaveHarnessIndex: %v", err)
	}
	// 释放 audit JSONL 句柄（runtime 未提供统一关闭，测试内直接关；
	// 否则 Windows 下 TempDir 清理因文件占用失败）
	if rt1.auditor != nil {
		_ = rt1.auditor.Close()
	}
	rt1.CloseHarness()

	// 第二进程：重新构造 Runtime（New 自动 LoadIndex），检索必须命中
	rt2 := newTestRuntimeAt(t, tmpDir)
	defer func() {
		if rt2.auditor != nil {
			_ = rt2.auditor.Close()
		}
		rt2.CloseHarness()
	}()

	got := rt2.Harness.Search(context.Background(), "需求描述", nil)
	if len(got) == 0 {
		t.Fatal("重启后索引未恢复——压缩原文丢失")
	}
	if got[0].ID != "persist-1" {
		t.Errorf("命中 id = %v, want persist-1", got[0].ID)
	}
}

func TestRuntime_CloseHarness_WritesIndexFile(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	rt.Harness.Index(context.Background(), []types.ContextBlock{
		{ID: "close-1", Content: "退出前写入的内容", Score: 50},
	})
	rt.CloseHarness()

	path := filepath.Join(tmpDir, ".contextloom", "harness", "index.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("CloseHarness 后索引文件不存在: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("索引文件为空")
	}

	// 文件可被新引擎加载
	e := harness.New(harness.Config{ScoreThreshold: 50})
	if err := e.LoadIndex(path); err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if got := e.Search(context.Background(), "退出前", nil); len(got) != 1 {
		t.Errorf("重载后检索 = %d 条, want 1", len(got))
	}
}

func TestRuntime_CloseHarness_Idempotent(t *testing.T) {
	rt, _ := newTestRuntime(t)
	rt.CloseHarness()
	rt.CloseHarness() // 二次调用不 panic、不卡死
}

// TestRuntime_HarnessAutoSave_NoChangeNoWrite 验证无变更时自动保存不产生写盘
// （NeedsSave 门控；直接验证 SaveHarnessIndex 在无变更时仍可安全调用）。
func TestRuntime_HarnessAutoSave_NoChangeNoWrite(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer rt.CloseHarness()
	if err := rt.SaveHarnessIndex(); err != nil {
		t.Fatalf("空索引保存不应报错: %v", err)
	}
	path := filepath.Join(tmpDir, ".contextloom", "harness", "index.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("空索引也应生成文件（幂等落盘）: %v", err)
	}
}

// ── 会话隔离：同项目不同会话 harness_search 不串扰（task/goal 串对话根因回归）──

func TestRuntime_HarnessSearch_SessionIsolation(t *testing.T) {
	rt, _ := newTestRuntime(t)
	t.Cleanup(rt.CloseHarness)

	// 会话 A：压缩产生的私有原文块（经 indexCompressedEvictedFor 写入，带 SessionID）
	rt.NewSession("会话A")
	sidA := rt.GetSessionID()
	if sidA == "" {
		t.Fatal("会话 A 未绑定 UUID")
	}
	rt.indexCompressedEvictedFor(sidA, types.Message{Role: "assistant", Content: "A会话的压缩原文：登录功能采用JWT方案"})

	// 会话 B：切换到新会话（goal/task 模式典型场景）
	sessB := rt.NewSession("会话B")
	sidB := sessB.UUID
	if sidB == sidA {
		t.Fatal("两个新会话 UUID 相同，测试无效")
	}
	// 会话 B 不能搜到 A 的压缩原文（即便 query 完全命中）
	res := rt.HarnessSearch(context.Background(), "JWT 登录", 10)
	for _, it := range res {
		if c, _ := it["content"].(string); containsAny(c, "A会话的压缩原文") {
			t.Fatalf("会话 B 检索到了会话 A 的压缩原文: %v", it)
		}
	}

	// 会话 B 自己的压缩原文可检索
	rt.indexCompressedEvictedFor(sidB, types.Message{Role: "assistant", Content: "B会话的压缩原文：支付回调幂等"})
	resB := rt.HarnessSearch(context.Background(), "支付回调幂等", 5)
	foundB := false
	for _, it := range resB {
		if c, _ := it["content"].(string); containsAny(c, "B会话的压缩原文") {
			foundB = true
		}
	}
	if !foundB {
		t.Error("会话 B 应能检索到自己的压缩原文")
	}

	// 切回会话 A：A 的原文仍在其名下，B 的原文不可见
	if _, _, err := rt.LoadSession(sidA); err != nil {
		t.Fatalf("切回会话 A 失败: %v", err)
	}
	resA := rt.HarnessSearch(context.Background(), "JWT 登录", 10)
	foundA := false
	for _, it := range resA {
		if c, _ := it["content"].(string); containsAny(c, "A会话的压缩原文") {
			foundA = true
		}
		if c, _ := it["content"].(string); containsAny(c, "B会话的压缩原文") {
			t.Fatalf("会话 A 检索到了会话 B 的原文: %v", it)
		}
	}
	if !foundA {
		t.Error("切回会话 A 后应能检索到 A 的压缩原文")
	}
}

func TestRuntime_HarnessSearch_ProjectSharedVisible(t *testing.T) {
	rt, _ := newTestRuntime(t)
	t.Cleanup(rt.CloseHarness)

	rt.NewSession("会话X")
	// 项目级共享块（无 SessionID）：所有会话可见
	rt.Harness.Index(context.Background(), []types.ContextBlock{
		{ID: "shared", Content: "项目架构文档：三层结构说明", Score: 80},
	})
	res := rt.HarnessSearch(context.Background(), "三层结构架构", 10)
	found := false
	for _, it := range res {
		if c, _ := it["content"].(string); containsAny(c, "项目架构文档") {
			found = true
		}
	}
	if !found {
		t.Error("项目级共享块应对会话检索可见")
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && len(sub) <= len(s) && indexOf(s, sub) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
