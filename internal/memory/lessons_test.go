package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractLessons(t *testing.T) {
	text := "这次教训是并行子任务改同一文件会冲突。另外记得测试用例要覆盖边界。普通一句话。"
	got := ExtractLessons(text)
	if len(got) != 2 {
		t.Fatalf("应抽取 2 条教训，got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "并行子任务") {
		t.Errorf("第1条应含并行子任务，got %q", got[0])
	}
	if !strings.Contains(got[1], "测试用例") {
		t.Errorf("第2条应含测试用例，got %q", got[1])
	}
}

func TestExtractLessons_NoNoise(t *testing.T) {
	// 无关键词的普通内容不应被抽取
	got := ExtractLessons("今天天气很好，我们去公园散步，阳光明媚，微风习习，非常惬意。")
	if len(got) != 0 {
		t.Fatalf("无关键词文本不应抽取，got %v", got)
	}
	// 过短/过长句子也不抽取
	got = ExtractLessons("注意。")
	if len(got) != 0 {
		t.Fatalf("过短句子不应抽取，got %v", got)
	}
}

func TestLessonsStore_MergeDedup(t *testing.T) {
	dir := t.TempDir()
	store, err := NewLessonsStore(dir)
	if err != nil {
		t.Fatal(err)
	}

	// 第一次合并：新增 2 条
	fresh := []Lesson{
		{Text: "并行子任务改同一文件会冲突", Topic: "并行与多智能体", Source: "sub-a 2026-08-27"},
		{Text: "测试用例要覆盖边界", Topic: "测试与验证", Source: "sub-b 2026-08-27"},
	}
	added, err := store.MergeAndSave(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if added != 2 {
		t.Fatalf("首次应新增 2 条，got %d", added)
	}

	// 第二次合并：同一条（重复）+ 一条新的 → 只新增 1 条
	fresh2 := []Lesson{
		{Text: "并行子任务改同一文件会冲突", Topic: "并行与多智能体", Source: "sub-c 2026-08-28"},
		{Text: "git 提交前先跑测试", Topic: "Git 与版本", Source: "sub-d 2026-08-28"},
	}
	added2, err := store.MergeAndSave(fresh2)
	if err != nil {
		t.Fatal(err)
	}
	if added2 != 1 {
		t.Fatalf("第二次应仅新增 1 条（去重），got %d", added2)
	}

	// 文件内容校验：3 条、含 frontmatter、主题分组
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "---") {
		t.Errorf("应含 YAML frontmatter，got %q", content[:20])
	}
	if !strings.Contains(content, "## 并行与多智能体") || !strings.Contains(content, "## 测试与验证") || !strings.Contains(content, "## Git 与版本") {
		t.Errorf("主题分组缺失，content=%s", content)
	}
	if strings.Count(content, "- 并行子任务改同一文件会冲突") != 1 {
		t.Errorf("重复教训应只保留一条，content=%s", content)
	}
}

func TestLessonsStore_ManualEntriesPreserved(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewLessonsStore(dir)

	// 用户手写 lessons.md（无 frontmatter）
	manual := "## 团队约定\n- 提交信息必须用中文\n- 每周五发周报\n"
	if err := os.WriteFile(store.Path(), []byte(manual), 0644); err != nil {
		t.Fatal(err)
	}

	// /dream 合并新教训，手写条目必须保留
	fresh := []Lesson{{Text: "并行子任务改同一文件会冲突", Topic: "并行与多智能体", Source: "sub-a"}}
	if _, err := store.MergeAndSave(fresh); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(store.Path())
	content := string(data)
	if !strings.Contains(content, "提交信息必须用中文") {
		t.Errorf("用户手工条目被覆盖删除，content=%s", content)
	}
	if !strings.Contains(content, "并行子任务改同一文件会冲突") {
		t.Errorf("新教训未追加，content=%s", content)
	}
	// 手写条目行不应带来源后缀；新教训行应带来源
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "提交信息必须用中文") && strings.Contains(line, "来源:") {
			t.Errorf("手写条目不应带来源后缀，line=%q", line)
		}
		if strings.Contains(line, "并行子任务改同一文件会冲突") && !strings.Contains(line, "来源: sub-a") {
			t.Errorf("新教训应带来源标注，line=%q", line)
		}
	}
}

func TestClassifyTopic(t *testing.T) {
	cases := []struct{ text, want string }{
		{"上下文压缩后 token 超限", "上下文管理"},
		{"测试用例没覆盖边界导致回归", "测试与验证"},
		{"git 分支冲突处理", "Git 与版本"},
		{"exec_command 工具要谨慎", "工具使用"},
		{"并行子任务有冲突风险", "并行与多智能体"},
		{"完全无关的内容", "其他"},
	}
	for _, c := range cases {
		if got := classifyTopic(c.text); got != c.want {
			t.Errorf("classifyTopic(%q)=%q, want %q", c.text, got, c.want)
		}
	}
}

func TestNormalizeLesson(t *testing.T) {
	cases := []struct{ in, want string }{
		{"- 并行子任务改同一文件会冲突", "并行子任务改同一文件会冲突"},
		{"1. 测试用例要覆盖边界。", "测试用例要覆盖边界"},
		{"「记住」不要直接提交", "记住」不要直接提交"},
		{"  多行\n换行折叠  ", "多行 换行折叠"},
	}
	for _, c := range cases {
		if got := normalizeLesson(c.in); got != c.want {
			t.Errorf("normalizeLesson(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestLessonsStore_NoDir(t *testing.T) {
	if _, err := NewLessonsStore(""); err == nil {
		t.Fatal("空目录应报错")
	}
}

var _ = filepath.Join // 保留 import（后续扩展用）
