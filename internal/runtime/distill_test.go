package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 纯函数层：解析/去重/合并（不依赖模型）──

// TestParseDistilledItems 解析模型蒸馏输出：识别 "- 经验：" / "经验：" 行，
// 忽略解释文字，条目去重、限长、限条数。
func TestParseDistilledItems(t *testing.T) {
	msg := `根据任务我提炼出以下经验：
- 经验：改动前先圈定影响面，防止越界返工
这是解释文字，不是条目。
经验：修复缺陷先写复现用例再改代码
- 经验：改动前先圈定影响面，防止越界返工（重复项应被去重）
- 无
`
	items := parseDistilledItems(msg)
	if len(items) != 3 {
		t.Fatalf("应解析出 3 条经验（逐条精确去重，第三条带括号注释视为新条目），got %d: %v", len(items), items)
	}
	if items[0] != "改动前先圈定影响面，防止越界返工" {
		t.Errorf("第 1 条应解析为纯 body，got %q", items[0])
	}
	if items[1] != "修复缺陷先写复现用例再改代码" {
		t.Errorf("第 2 条应为复现用例条目，got %q", items[1])
	}
	if items[2] != "改动前先圈定影响面，防止越界返工（重复项应被去重）" {
		t.Errorf("第 3 条应保留（内容不同），got %q", items[2])
	}
}

// TestParseDistilledItems_ExactDup 同内容条目在同一输出内精确去重。
func TestParseDistilledItems_ExactDup(t *testing.T) {
	msg := "- 经验：先写复现用例再改代码\n- 经验：先写复现用例再改代码\n- 经验：先写复现用例再改代码\n"
	items := parseDistilledItems(msg)
	if len(items) != 1 {
		t.Fatalf("同内容应精确去重为 1 条，got %d: %v", len(items), items)
	}
}

// TestParseDistilledItems_Empty 模型回答"无"或空 → 空列表（调用方跳过沉淀）。
func TestParseDistilledItems_Empty(t *testing.T) {
	if items := parseDistilledItems("无"); len(items) != 0 {
		t.Fatalf("回答'无'应解析为空，got %v", items)
	}
	if items := parseDistilledItems("本次任务无值得沉淀的经验。"); len(items) != 0 {
		t.Fatalf("解释性回答应解析为空，got %v", items)
	}
}

// TestParseDistilledItems_MaxLines 超量条目截断到 distillMaxLines 条。
func TestParseDistilledItems_MaxLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("- 经验：经验条目 " + string(rune('A'+i)) + "\n")
	}
	items := parseDistilledItems(sb.String())
	if len(items) > distillMaxLines {
		t.Fatalf("条目数应 ≤ %d，got %d", distillMaxLines, len(items))
	}
}

// TestAppendDistilledItems_NewSection 无「经验蒸馏」段落 → 文档尾部追加段落 + 条目。
func TestAppendDistilledItems_NewSection(t *testing.T) {
	doc := "# coder 本体能力说明书\n\n## 岗位\n实现代码\n"
	items := []string{"改动前先圈定影响面", "修复缺陷先写复现用例"}
	got := appendDistilledItems(doc, items)
	if !strings.Contains(got, distillSectionTitle) {
		t.Fatalf("应追加段落标题，got:\n%s", got)
	}
	if !strings.Contains(got, "- 经验：改动前先圈定影响面") {
		t.Fatalf("应包含第 1 条经验，got:\n%s", got)
	}
	if !strings.Contains(got, "- 经验：修复缺陷先写复现用例") {
		t.Fatalf("应包含第 2 条经验，got:\n%s", got)
	}
	// 用户原有内容必须保留
	if !strings.Contains(got, "## 岗位\n实现代码") {
		t.Fatalf("用户原有内容不应被破坏，got:\n%s", got)
	}
}

// TestAppendDistilledItems_AppendToExisting 已有段落 → 追加到段落内，不重复。
func TestAppendDistilledItems_AppendToExisting(t *testing.T) {
	doc := "# coder 本体能力说明书\n\n## 岗位\n实现代码\n\n## 经验蒸馏（自动沉淀）\n- 经验：已有经验一\n- 经验：已有经验二\n\n## 风格与偏好\n- \n"
	items := []string{"已有经验一", "新增经验三"}
	got := appendDistilledItems(doc, items)
	// 已有经验一去重：文档中只出现一次
	if strings.Count(got, "已有经验一") != 1 {
		t.Fatalf("重复条目应被去重（只出现 1 次），got %d 次:\n%s", strings.Count(got, "已有经验一"), got)
	}
	if !strings.Contains(got, "- 经验：新增经验三") {
		t.Fatalf("应追加新经验，got:\n%s", got)
	}
	// 后续章节（## 风格与偏好）仍保留
	if !strings.Contains(got, "## 风格与偏好") {
		t.Fatalf("后续章节应保留，got:\n%s", got)
	}
}

// TestAppendDistilledItems_AllDuplicate 全部重复 → 返回原文本（幂等）。
func TestAppendDistilledItems_AllDuplicate(t *testing.T) {
	doc := "## 经验蒸馏（自动沉淀）\n- 经验：已有经验\n"
	if got := appendDistilledItems(doc, []string{"已有经验"}); got != doc {
		t.Fatalf("全部重复应返回原文本，got:\n%s", got)
	}
}

// TestAppendDistilledItems_EmptyItems 空条目 → 原样返回。
func TestAppendDistilledItems_EmptyItems(t *testing.T) {
	doc := "原文"
	if got := appendDistilledItems(doc, nil); got != doc {
		t.Fatalf("空条目应返回原文本，got %q", got)
	}
}

// TestAppendDistilledItems_GrowthBudget 增长超预算 → 整体拒绝（防膨胀）。
func TestAppendDistilledItems_GrowthBudget(t *testing.T) {
	doc := "## 经验蒸馏（自动沉淀）\n"
	big := strings.Repeat("长", distillMaxDocGrowth) // 单条即超预算
	items := []string{big}
	if got := appendDistilledItems(doc, items); got != doc {
		t.Fatalf("超增长预算应整体拒绝（返回原文本），got len=%d", len(got))
	}
}

// ── 集成层：distillEmployee 端到端（真实文件写入）──

// distillBackend 为蒸馏测试返回预设经验条目的模型后端。
type distillBackend struct{ items string }

func (m *distillBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.items, nil, nil
}
func (m *distillBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- m.items
	}()
	return ch, errCh
}
func (m *distillBackend) ModelID() string           { return "mock-distill" }
func (m *distillBackend) MaxContext() int           { return 4096 }
func (m *distillBackend) SupportsVision() bool      { return false }
func (m *distillBackend) SetReasoningEffort(string) {}
func (m *distillBackend) ReasoningEffort() string   { return "" }
func (m *distillBackend) SetTopP(p float64)         {}
func (m *distillBackend) TopP() float64             { return 0 }
func (m *distillBackend) SetTemperature(t float64)  {}
func (m *distillBackend) Temperature() float64      { return 0 }

// TestDistillEmployee_WritesOntology 端到端：子任务完成后蒸馏结果
// 真实追加进员工工作区 ONTOLOGY.md（自动沉淀段落出现）。
func TestDistillEmployee_WritesOntology(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 造一个员工目录 + 空模板 ONTOLOGY.md（与 seed 产物同构）
	empDir := filepath.Join(tmpDir, ".contextloom", "employees", "coder")
	if err := os.MkdirAll(filepath.Join(empDir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedDoc := "# coder 本体能力说明书\n\n## 岗位\n实现代码与修复缺陷\n\n## 专精领域\n- Go\n\n## 能力边界（示例，可编辑）\n- 擅长：\n- 不擅长（转交其他员工）：\n"
	if err := os.WriteFile(filepath.Join(empDir, "workspace", "ONTOLOGY.md"), []byte(seedDoc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 员工档案（可从 ExampleEmployees 取 coder）
	emp := employeeFromExample(t, "coder")

	// 覆盖模型后端：返回蒸馏条目
	rt.Backend = &distillBackend{items: "- 经验：改动前先圈定影响面\n- 经验：修复缺陷先写复现用例再改代码\n"}

	// 触发蒸馏（同步执行，便于断言；生产路径为 goroutine 调用）
	rt.distillEmployee(emp, "实现登录函数", "已实现登录功能，代码已写入 auth.py，测试通过。")

	// 断言 ONTOLOGY.md 已追加自动沉淀段落
	got, err := os.ReadFile(filepath.Join(empDir, "workspace", "ONTOLOGY.md"))
	if err != nil {
		t.Fatalf("读取 ONTOLOGY.md: %v", err)
	}
	s := string(got)
	if !strings.Contains(s, distillSectionTitle) {
		t.Fatalf("应追加经验蒸馏段落，got:\n%s", s)
	}
	if !strings.Contains(s, "- 经验：改动前先圈定影响面") {
		t.Fatalf("应包含蒸馏经验 1，got:\n%s", s)
	}
	if !strings.Contains(s, "- 经验：修复缺陷先写复现用例再改代码") {
		t.Fatalf("应包含蒸馏经验 2，got:\n%s", s)
	}
	// 用户模板占位与岗位内容保留
	if !strings.Contains(s, "## 岗位\n实现代码与修复缺陷") || !strings.Contains(s, "- 擅长：") {
		t.Fatalf("用户原有内容不应被破坏，got:\n%s", s)
	}
}

// TestDistillEmployee_Idempotent 再次蒸馏相同经验 → 不重复追加（幂等）。
func TestDistillEmployee_Idempotent(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	empDir := filepath.Join(tmpDir, ".contextloom", "employees", "coder")
	if err := os.MkdirAll(filepath.Join(empDir, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(empDir, "workspace", "ONTOLOGY.md"), []byte("# coder 本体能力说明书\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	emp := employeeFromExample(t, "coder")
	rt.Backend = &distillBackend{items: "- 经验：改动前先圈定影响面\n"}

	rt.distillEmployee(emp, "任务A", "产出A")
	rt.distillEmployee(emp, "任务B", "产出B")

	got, err := os.ReadFile(filepath.Join(empDir, "workspace", "ONTOLOGY.md"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(got), "改动前先圈定影响面"); n != 1 {
		t.Fatalf("重复蒸馏应幂等（只出现 1 次），got %d 次:\n%s", n, got)
	}
}

// TestDistillEmployee_NoEmployeeFile 工作区文档不存在 → 静默跳过（不重建）。
func TestDistillEmployee_NoEmployeeFile(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	// 删除 seed 落地的员工目录 → 蒸馏时无 ONTOLOGY.md 可写
	if err := os.RemoveAll(filepath.Join(tmpDir, ".contextloom", "employees", "coder")); err != nil {
		t.Fatal(err)
	}
	rt.Backend = &distillBackend{items: "- 经验：某经验\n"}
	rt.distillEmployee(employeeFromExample(t, "coder"), "任务", "产出") // 不应 panic / 不应写文件
	if _, err := os.Stat(filepath.Join(tmpDir, ".contextloom", "employees", "coder")); !os.IsNotExist(err) {
		t.Fatalf("无文档时不应重建员工目录，got err=%v", err)
	}
}

// TestDistillEmployee_NoBackend 无模型后端 → 静默跳过（零配置兜底）。
func TestDistillEmployee_NoBackend(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.Backend = nil // 无后端（selectBackend 回退链全空 → 静默跳过）
	rt.Gateway = nil
	empDir := filepath.Join(tmpDir, ".contextloom", "employees", "coder")
	docPath := filepath.Join(empDir, "workspace", "ONTOLOGY.md")
	before, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("seed 应已创建 ONTOLOGY.md: %v", err)
	}
	rt.distillEmployee(employeeFromExample(t, "coder"), "任务", "产出") // 应静默返回，不 panic
	after, _ := os.ReadFile(docPath)
	if string(after) != string(before) {
		t.Fatalf("无后端时文档不应变化，before=%d after=%d", len(before), len(after))
	}
}

// employeeFromExample 从内置员工模板取一份拷贝（测试助手）。
func employeeFromExample(t *testing.T, name string) *orchestrator.Employee {
	t.Helper()
	for _, e := range orchestrator.ExampleEmployees() {
		if e.Name == name {
			ee := e
			return &ee
		}
	}
	t.Fatalf("内置员工 %q 不存在", name)
	return nil
}

// TestSpawnTask_TriggersDistill 端到端：SpawnTask 命中员工且完成 → 蒸馏钩子触发，
// 经验自动沉淀进员工 ONTOLOGY.md（含子 Agent 输出的真实产物）。
func TestSpawnTask_TriggersDistill(t *testing.T) {
	rt := newSpawnRuntime(t)
	empDir := filepath.Join(rt.Config.Project.Root, ".contextloom", "employees", "coder")
	docPath := filepath.Join(empDir, "workspace", "ONTOLOGY.md")
	before, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("seed 应创建 coder ONTOLOGY.md: %v", err)
	}

	// 蒸馏后端：spawn 后端返回"已实现登录功能"，蒸馏后端返回经验条目
	rt.Backend = &distillBackend{items: "- 经验：实现登录功能前先确认会话存储边界\n"}

	_, err = rt.SpawnTask(context.Background(), "main", tools.TaskSpec{
		Task:      "实现登录函数",
		Directive: "coder",
	})
	if err != nil {
		t.Fatalf("SpawnTask: %v", err)
	}
	// 蒸馏在后台 goroutine 异步执行：轮询等待落盘（最多 2s）
	var after []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		after, _ = os.ReadFile(docPath)
		if string(after) != string(before) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(after) == string(before) {
		t.Fatalf("SpawnTask 命中员工后应触发蒸馏（ONTOLOGY.md 应有自动沉淀段落）")
	}
	if !strings.Contains(string(after), "实现登录功能前先确认会话存储边界") {
		t.Fatalf("应包含蒸馏出的经验，got:\n%s", after)
	}
}
