package memory

// lessons.go —— 长期记忆整理层（对标业界同类 Agent 的 /dream 记忆整理术
// "记忆主动整理（/dream 式）与学习档案" 低成本落地项）。
//
// 设计原则：
//   - 完全增量：不修改既有会话 JSONL / vocab / checkpoint 任何读写路径；
//   - 主动沉淀：把散落在会话 JSONL 里的"教训/踩坑/约定/经验"类内容抽取为
//     一个可编辑的长期记忆文件（.contextloom/memory/lessons.md），跨会话生效；
//   - 去重合并：以规范化句子为指纹，重复教训只保留一条（最新来源），
//     避免 /dream 反复执行后 lessons.md 无限膨胀；
//   - 用户可编辑：lessons.md 是普通 Markdown（YAML frontmatter + 主题分组），
//     用户可直接修改/删除，下次 /dream 会尊重手工编辑（只增量不覆盖手写内容）。
//
// 文件格式：
//   ---
//   type: lessons
//   updated_at: <RFC3339>
//   count: N
//   ---
//   ## <主题>
//   - 教训内容（带来源会话时间戳，去重后）

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Lesson 单条长期记忆。
type Lesson struct {
	Text      string // 教训正文（规范化后，去重指纹）
	Source    string // 来源（会话 UUID 短码 + 时间，如 "01a04… 2026-08-27"）
	Topic     string // 主题分组（未命中关键词时为 "其他"）
	UpdatedAt time.Time
}

// LessonsStore 长期记忆文件读写（线程安全）。
type LessonsStore struct {
	mu   sync.Mutex
	path string // lessons.md 绝对路径
}

// NewLessonsStore 创建长期记忆存储。dir 为记忆根目录（.contextloom/memory）。
func NewLessonsStore(dir string) (*LessonsStore, error) {
	if dir == "" {
		return nil, fmt.Errorf("lessons: 记忆目录为空")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("lessons: 创建记忆目录失败: %w", err)
	}
	return &LessonsStore{path: filepath.Join(dir, "lessons.md")}, nil
}

// Path 返回 lessons.md 路径（供展示）。
func (s *LessonsStore) Path() string { return s.path }

// topicPatterns 主题关键词表：命中即归入对应主题（按顺序首中即止）。
var topicPatterns = []struct {
	topic string
	re    *regexp.Regexp
}{
	{"上下文管理", regexp.MustCompile(`上下文|token|压缩|上下文窗口|预算|历史记录`)},
	{"测试与验证", regexp.MustCompile(`测试|用例|断言|回归|通过|失败|验证|断言`)},
	{"Git 与版本", regexp.MustCompile(`git|提交|分支|合并|worktree|版本|checkout|commit`)},
	{"工具使用", regexp.MustCompile(`工具|write_file|read_file|exec_command|沙箱|权限|审批`)},
	{"并行与多智能体", regexp.MustCompile(`子任务|spawn|并行|并发|冲突|隔离|sub-`)},
	{"性能与资源", regexp.MustCompile(`性能|内存|泄漏|卡顿|超时|慢|资源|缓存`)},
}

// ClassifyTopic 对教训文本归类主题；未命中返回 "其他"。导出版（供 runtime/TUI 使用）。
func ClassifyTopicPublic(text string) string { return classifyTopic(text) }

// classifyTopic 对教训文本归类主题；未命中返回 "其他"。
func classifyTopic(text string) string {
	for _, tp := range topicPatterns {
		if tp.re.MatchString(text) {
			return tp.topic
		}
	}
	return "其他"
}

// normalizeLesson 规范化教训文本，生成去重指纹：
// 去掉首尾空白/常见引号/编号前缀/行内换行，统一为单行小写。
func normalizeLesson(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "\"'「」『』【】")
	// 去掉 "- "、"1. " 等列表/编号前缀
	re := regexp.MustCompile(`^[-*•]\s+|^\d+[.、)]\s*`)
	text = re.ReplaceAllString(text, "")
	// 行内换行折叠为空格
	text = strings.Join(strings.Fields(text), " ")
	// 尾部句号截掉（便于同义句归一）
	text = strings.TrimRight(text, "。.!！?？")
	return text
}

// 教训候选关键词：出现在 assistant 消息中即视为潜在"经验沉淀"句。
var lessonKeywords = []string{
	"教训", "经验", "踩坑", "坑", "注意", "记住", "记得", "下次",
	"不要", "别", "应该", "必须", "建议", "约定", "惯例",
	"根因", "原因", "修复", "解决",
}

// 句子切分：按中英文句号/问号/感叹号/换行切分，保留长度 12~200 字的候选句。
var sentenceSplitRe = regexp.MustCompile(`[。！？!?\n]+`)

// ExtractLessons 从一段文本中抽取候选教训句。
// 规则：切句 → 含关键词 → 长度在 [12, 200] → 规范化后非空。
// 纯启发式抽取，供 /dream 自动整理；误报可由用户编辑 lessons.md 清除。
func ExtractLessons(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, sent := range sentenceSplitRe.Split(text, -1) {
		sent = strings.TrimSpace(sent)
		if len([]rune(sent)) < 12 || len([]rune(sent)) > 200 {
			continue
		}
		hit := false
		for _, kw := range lessonKeywords {
			if strings.Contains(sent, kw) {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		norm := normalizeLesson(sent)
		if len([]rune(norm)) >= 8 {
			out = append(out, norm)
		}
	}
	return out
}

// stripSourceSuffix 剥离行尾的 "（来源: xxx）" 后缀（渲染时附加的），
// 保证读回解析的指纹与正文一致（否则 /dream 二次执行会误判重复）。
func stripSourceSuffix(text string) string {
	if i := strings.Index(text, "（来源: "); i >= 0 {
		if j := strings.Index(text[i:], "）"); j >= 0 {
			return strings.TrimSpace(text[:i])
		}
	}
	return text
}

// lessons 文件内联解析：返回 (主题 → 教训列表, 是否含 frontmatter, 错误)。
// 手写内容（无 frontmatter）会被整体视为"用户手工条目"，/dream 不覆盖删除。
func parseLessonsFile(content string) (map[string][]string, bool) {
	topics := map[string][]string{}
	hasFront := strings.HasPrefix(strings.TrimSpace(content), "---")
	cur := ""
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			cur = strings.TrimPrefix(line, "## ")
			if cur == "" {
				cur = "其他"
			}
			if _, ok := topics[cur]; !ok {
				topics[cur] = nil
			}
			continue
		}
		if strings.HasPrefix(line, "- ") {
			item := normalizeLesson(stripSourceSuffix(strings.TrimPrefix(line, "- ")))
			if item == "" {
				continue
			}
			if cur == "" {
				cur = "其他"
			}
			topics[cur] = append(topics[cur], item)
		}
	}
	return topics, hasFront
}

// mergeLessons 合并旧教训与新抽取教训（按规范化指纹去重，保留最新来源）。
// 旧教训（含用户手工条目）全部保留；新教训仅在指纹不存在时追加。
func mergeLessons(old map[string][]string, fresh []Lesson) []Lesson {
	// 指纹 → 已存在
	seen := map[string]bool{}
	var out []Lesson
	// 稳定主题顺序（先旧后新，主题按名称排序输出时统一）
	for topic, items := range old {
		for _, it := range items {
			fp := strings.ToLower(it)
			if seen[fp] {
				continue
			}
			seen[fp] = true
			// 旧条目（含用户手写）Source 留空：渲染时不加来源后缀
			out = append(out, Lesson{Text: it, Topic: topic})
		}
	}
	for _, l := range fresh {
		fp := strings.ToLower(l.Text)
		if seen[fp] {
			continue
		}
		seen[fp] = true
		out = append(out, l)
	}
	return out
}

// renderLessons 渲染 lessons.md 文本（frontmatter + 主题分组）。
func renderLessons(lessons []Lesson, updatedAt time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("type: lessons\n")
	b.WriteString(fmt.Sprintf("updated_at: %s\n", updatedAt.Format(time.RFC3339)))
	b.WriteString(fmt.Sprintf("count: %d\n", len(lessons)))
	b.WriteString("---\n\n")
	b.WriteString("> 本文件为长期记忆（/dream 自动整理 + 可手工编辑）。\n")
	b.WriteString("> 修改后再次 /dream 会尊重手写内容，仅追加新教训。\n\n")

	// 按主题分组输出，主题按名称排序
	byTopic := map[string][]Lesson{}
	var topics []string
	for _, l := range lessons {
		t := l.Topic
		if t == "" {
			t = "其他"
		}
		if _, ok := byTopic[t]; !ok {
			topics = append(topics, t)
		}
		byTopic[t] = append(byTopic[t], l)
	}
	sort.Strings(topics)
	for _, t := range topics {
		b.WriteString("## " + t + "\n")
		for _, l := range byTopic[t] {
			line := "- " + l.Text
			if l.Source != "" { // 空来源 = 用户手写条目，不加后缀
				line += "  （来源: " + l.Source + "）"
			}
			b.WriteString(line + "\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// Load 读取当前 lessons.md 内容（无文件返回空 map）。
func (s *LessonsStore) Load() (map[string][]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string][]string{}, nil
		}
		return nil, err
	}
	topics, _ := parseLessonsFile(string(data))
	return topics, nil
}

// MergeAndSave 把新抽取的教训合并进 lessons.md 并落盘。
// 返回本次新增条数（合并后新增）。
func (s *LessonsStore) MergeAndSave(fresh []Lesson) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	old := map[string][]string{}
	if data, err := os.ReadFile(s.path); err == nil {
		old, _ = parseLessonsFile(string(data))
	}
	merged := mergeLessons(old, fresh)
	now := time.Now()
	content := renderLessons(merged, now)
	if err := os.WriteFile(s.path, []byte(content), 0644); err != nil {
		return 0, fmt.Errorf("lessons: 写入失败: %w", err)
	}

	// 新增条数 = 合并后总数 - 旧条数
	oldCount := 0
	for _, items := range old {
		oldCount += len(items)
	}
	added := len(merged) - oldCount
	if added < 0 {
		added = 0
	}
	return added, nil
}
