package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/memory"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestDreamMemory_ScansSessions 验证 /dream 端到端：
// 写两个会话（各含一条教训类消息）→ DreamMemory → lessons.md 生成且去重。
func TestDreamMemory_ScansSessions(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 会话一：教训消息
	s1 := memory.NewSession("会话一", tmpDir)
	if err := rt.Memory.Save(s1); err != nil {
		t.Fatal(err)
	}
	ev1 := &types.SessionEvent{
		Seq:       1,
		Timestamp: time.Now(),
		Type:      "assistant_msg",
		Data:      "这次教训是并行子任务改同一文件会冲突，下次要用 worktree 隔离。",
	}
	if err := rt.Memory.AppendEvent(s1.UUID, ev1); err != nil {
		t.Fatal(err)
	}

	// 会话二：同样的教训（应去重）+ 一条新教训
	s2 := memory.NewSession("会话二", tmpDir)
	if err := rt.Memory.Save(s2); err != nil {
		t.Fatal(err)
	}
	ev2 := &types.SessionEvent{
		Seq:       1,
		Timestamp: time.Now(),
		Type:      "assistant_msg",
		Data:      "记住：测试用例必须覆盖边界，否则回归会漏。",
	}
	if err := rt.Memory.AppendEvent(s2.UUID, ev2); err != nil {
		t.Fatal(err)
	}
	ev3 := &types.SessionEvent{
		Seq:       2,
		Timestamp: time.Now(),
		Type:      "assistant_msg",
		Data:      "这次教训是并行子任务改同一文件会冲突，下次要用 worktree 隔离。", // 重复
	}
	if err := rt.Memory.AppendEvent(s2.UUID, ev3); err != nil {
		t.Fatal(err)
	}

	out := rt.DreamMemory()
	t.Logf("DreamMemory: %s", out)
	if !strings.Contains(out, "扫描 2 个会话") {
		t.Errorf("应扫描 2 个会话，got %q", out)
	}
	if !strings.Contains(out, "新增沉淀") {
		t.Errorf("应含新增沉淀统计，got %q", out)
	}

	// lessons.md 存在且含去重后的 2 条
	lessonsPath := filepath.Join(tmpDir, "memory", "lessons.md")
	data, err := os.ReadFile(lessonsPath)
	if err != nil {
		t.Fatalf("lessons.md 未生成: %v (out=%s)", err, out)
	}
	content := string(data)
	if strings.Count(content, "并行子任务改同一文件会冲突") != 1 {
		t.Errorf("重复教训应只保留 1 条，content=%s", content)
	}
	if !strings.Contains(content, "测试用例必须覆盖边界") {
		t.Errorf("新教训应写入，content=%s", content)
	}
	if !strings.HasPrefix(content, "---") {
		t.Errorf("应含 frontmatter，content=%q", content[:20])
	}
}

// TestTeachMemory 验证 /teach 手动沉淀：无关键词也能写入（不走启发式过滤）。
func TestTeachMemory(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	out := rt.TeachMemory("每周五发周报")
	if !strings.Contains(out, "已沉淀") {
		t.Fatalf("TeachMemory 应成功，got %q", out)
	}

	data, err := os.ReadFile(filepath.Join(tmpDir, "memory", "lessons.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "每周五发周报") {
		t.Errorf("手写条目未写入，content=%s", data)
	}

	// 空内容应拒绝
	if out := rt.TeachMemory("  "); !strings.Contains(out, "用法") {
		t.Errorf("空内容应提示用法，got %q", out)
	}
}

// TestDreamMemory_TwiceIdempotent 验证 /dream 幂等：二次执行不重复新增。
func TestDreamMemory_TwiceIdempotent(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	s := memory.NewSession("会话", tmpDir)
	if err := rt.Memory.Save(s); err != nil {
		t.Fatal(err)
	}
	ev := &types.SessionEvent{
		Seq:       1,
		Timestamp: time.Now(),
		Type:      "assistant_msg",
		Data:      "经验：写文件前先确认目录存在，否则报错。",
	}
	if err := rt.Memory.AppendEvent(s.UUID, ev); err != nil {
		t.Fatal(err)
	}

	first := rt.DreamMemory()
	second := rt.DreamMemory()
	if !strings.Contains(second, "新增沉淀 0 条") {
		t.Errorf("二次 /dream 应新增 0 条，got %q (first=%q)", second, first)
	}
}
