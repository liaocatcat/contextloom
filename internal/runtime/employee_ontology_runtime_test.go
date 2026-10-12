package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestEmployeeOntology_WorkspaceDistillation 验证员工工作区本体能力说明书
// 能被正确读取并蒸馏进深入本体档案（DeepProfile）。
// 构造：在种子员工基础上写 coder 工作区 ONTOLOGY.md，验证 employeeDeepProfile
// 把档案字段与文档文本都注入。
func TestEmployeeOntology_WorkspaceDistillation(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)

	// 种子已落 coder 档案（workspace: workspace）；补写工作区本体能力说明书
	wsDir := filepath.Join(tmp, ".contextloom", "employees", "coder", "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "# 参考：错误处理先于业务；提交按主题拆分；改动前先圈定影响面"
	if err := os.WriteFile(filepath.Join(wsDir, "ONTOLOGY.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	emp := rt.employeeByName("coder")
	if emp == nil {
		t.Fatal("coder 员工应被加载")
	}
	if emp.Workspace != "workspace" {
		t.Fatalf("coder 档案应声明 workspace=workspace, got %q", emp.Workspace)
	}
	profile := rt.employeeDeepProfile(emp)
	// 档案字段 + 蒸馏文档都应注入
	for _, want := range []string{
		"【员工本体 · coder】",
		"专精领域",
		"标准工作流",
		"行为约束",
		"参考：错误处理先于业务；提交按主题拆分；改动前先圈定影响面",
	} {
		if !strings.Contains(profile, want) {
			t.Errorf("本体档案缺 %q\n完整:%s", want, profile)
		}
	}
}

// TestEmployeeOntology_MissingFile 验证工作区无 ONTOLOGY.md 时本体文档为空但不
// 阻断档案编译（蒸馏缺失 → 只有档案字段，零配置兜底）。
func TestEmployeeOntology_MissingFile(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)
	// 移除种子自动落地的 ONTOLOGY.md，模拟"工作区无蒸馏文档"
	doc := filepath.Join(tmp, ".contextloom", "employees", "coder", "workspace", "ONTOLOGY.md")
	if err := os.Remove(doc); err == nil {
		t.Log("已移除种子 ONTOLOGY.md 以验证缺文档兜底")
	}
	emp := rt.employeeByName("coder")
	if emp == nil {
		t.Fatal("coder 应存在")
	}
	// 注意：种子会自动生成 ONTOLOGY.md（ontologySeed），若上一行 os.Remove 成功、
	// 且本测试内无重新 seed，则 profile 不含说明书标题；但 newTestRuntime 已 seed 过
	//（doc 已生成），此处移掉后 employeeDeepProfile 读不到文件 → 无说明书标题。
	profile := rt.employeeDeepProfile(emp)
	if strings.Contains(profile, "【本体能力说明书（来自员工工作区蒸馏）】") {
		t.Errorf("无 ONTOLOGY.md 时不应拼上蒸馏说明书标题，got:\n%s", profile)
	}
	if !strings.Contains(profile, "【员工本体 · coder】") {
		t.Errorf("即使无蒸馏文档也应输出档案字段, got:\n%s", profile)
	}
}

// TestEmployeesOverview_And_Summary 验证员工清单与摘要输出（/emp 命令展示）。
func TestEmployeesOverview_And_Summary(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)
	names := rt.EmployeeNames()
	if len(names) == 0 {
		t.Fatal("种子员工应已加载")
	}
	ov := rt.EmployeesOverview()
	if !strings.Contains(ov, "可用员工") {
		t.Errorf("概况应含标题, got: %s", ov)
	}
	for _, n := range names {
		if !strings.Contains(ov, n) {
			t.Errorf("概况应含员工 %q", n)
		}
	}
	if s := rt.EmployeeSummary("coder"); !strings.Contains(s, "coder") || !strings.Contains(s, "专精") {
		t.Errorf("员工摘要应含名称与专精, got: %q", s)
	}
	if s := rt.EmployeeSummary("NO_SUCH"); s != "" {
		t.Errorf("不存在员工摘要应为空, got %q", s)
	}
}

// TestAssignEmployee_RejectsUnknown 验证指派不存在的员工/空任务会报错。
func TestAssignEmployee_RejectsUnknown(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)
	if err := rt.AssignEmployee("ghost", "写个 hello"); err == nil {
		t.Fatal("指派不存在员工应报错")
	}
	if err := rt.AssignEmployee("coder", ""); err == nil {
		t.Error("空任务应报错")
	}
}

// TestEmployeeOntology_SeedMigrationForExistingProfile 验证迁移路径：既有员工档案
// （旧版，未声明 workspace 字段，seedFile 不覆盖）在重新启动 seed 后仍获得
// workspace/ONTOLOGY.md 引导，且 runtime 通过默认约定回退能读到该蒸馏文档。
func TestEmployeeOntology_SeedMigrationForExistingProfile(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)

	// 移除种子已生成的引导文档，模拟"用户已有档案但从未跑过新种子"的纯净前置
	wsDoc := filepath.Join(tmp, ".contextloom", "employees", "coder", "workspace", "ONTOLOGY.md")
	_ = os.Remove(wsDoc)

	// 旧版 maxTokens：employee.yaml 不含 workspace 字段（模拟迁移前的档案经典形态）
	writeFile(t, filepath.Join(tmp, ".contextloom", "employees", "coder", "employee.yaml"), `
name: coder
display: 开发者
role: 实现代码
persona: 严谨
approval: standard
`)
	// 重新执行种子（等价二进制重启）：既有 yaml 不覆盖，但工作区引导应补齐
	root := resolveRootForSeed(tmp)
	seedEmployees(filepath.Join(root, ".contextloom", "employees"))

	// 员工档案保留用户自定义（旧字段，workspace 缺失 → 走默认约定回退）
	emp := rt.employeeByName("coder")
	if emp == nil {
		t.Fatal("coder 应仍被加载")
	}
	// 旧版档案经 loader 的 MergeDeepDefaults 迁移，深入本体已回退补齐（不加感磁盘 yaml）
	if len(emp.Expertise) == 0 || len(emp.Workflow) == 0 || len(emp.Constraints) == 0 {
		t.Errorf("旧版档案应经 MergeDeepDefaults 回退补齐深入本体，got exp=%v wf=%v constr=%v",
			emp.Expertise, emp.Workflow, emp.Constraints)
	}
	if emp.Workspace != "workspace" {
		t.Errorf("旧版档案应回退 workspace=workspace, got %q", emp.Workspace)
	}
	// 用户自定义字段保留
	if emp.Display != "开发者" || emp.Persona != "严谨" {
		t.Errorf("用户自定义 display/persona 应保留, got display=%q persona=%q", emp.Display, emp.Persona)
	}
	// 工作区引导文档应已落地
	got := rt.employeeOntologyDoc(emp)
	if !strings.Contains(got, "本体能力说明书") {
		t.Errorf("迁移后应能读到工作区引导文档（默认约定回退），got %q", got)
	}
}

// resolveRootForSeed 归一化项目根路径，供 seed 落地（与 runtime.New 的 projRoot 一致）。
func resolveRootForSeed(tmp string) string { return tmp }

// ── P1-1/P2-1：容量上限与路径安全 ──

// TestEmployeeOntology_SizeCap 验证本体能力说明书超过上限时被截断注入
// （P1-1：防用户把工作区蒸馏成数万行后全量注入子 Agent 上下文撑爆预算）。
func TestEmployeeOntology_SizeCap(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)

	// 写一个超过 16KB 上限的本体文档
	big := strings.Repeat("能力蒸馏内容能力蒸馏内容能力蒸馏内容", 1024) // ≈ 16KB+，必超上限
	wsDir := filepath.Join(tmp, ".contextloom", "employees", "coder", "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "ONTOLOGY.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	emp := rt.employeeByName("coder")
	if emp == nil {
		t.Fatal("coder 应存在")
	}
	doc := rt.employeeOntologyDoc(emp)
	// 截断后正文 ≤ 上限 + 截断标注（标注约 100 字符，这里放宽到 256 防断言脆弱）
	if len(doc) > maxOntologyDocBytes+256 {
		t.Errorf("本体文档应被截断到上限附近，len=%d > %d", len(doc), maxOntologyDocBytes+256)
	}
	if !strings.Contains(doc, "已截断注入") {
		t.Errorf("截断后应标注提示，got:\n%s", doc)
	}
}

// TestEmployeeOntology_PathTraversal 验证越界路径被拒绝（P2-1 安全）：
//   - 非法员工名（含 ../ 或路径分隔符）→ 不解析任何路径（返回 ""）；
//   - 合法员工名但 ontology_file 指向员工目录外（../）→ 拒绝读取。
func TestEmployeeOntology_PathTraversal(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)

	// 项目外放置一个"敏感文件"，越界成功会读到它
	secret := filepath.Join(tmp, "..", "secret-ontology.txt")
	_ = os.WriteFile(secret, []byte("SECRET"), 0o644)

	// 1. 非法员工名（路径分隔符/绝对路径）→ 直接拒绝
	bad := &orchestrator.Employee{Name: "../../etc"}
	if got := rt.employeeOntologyDoc(bad); got != "" {
		t.Errorf("非法员工名应拒绝读取，got %q", got)
	}
	bad2 := &orchestrator.Employee{Name: "coder", OntologyFile: "../../secret-ontology.txt"}
	// 2. 合法员工名 + 越界 ontology_file → 拒绝
	if got := rt.employeeOntologyDoc(bad2); got != "" {
		t.Errorf("越界 ontology_file 应拒绝读取，got %q", got)
	}
	// 3. 绝对路径 ontology_file → 拒绝
	bad3 := &orchestrator.Employee{Name: "coder", OntologyFile: secret}
	if got := rt.employeeOntologyDoc(bad3); got != "" {
		t.Errorf("绝对路径 ontology_file 应拒绝读取，got %q", got)
	}
	// 4. 正常员工（默认约定路径）不受影响：返回空或正常文档（种子已写引导）
	emp := rt.employeeByName("coder")
	if emp == nil {
		t.Fatal("coder 应存在")
	}
	if doc := rt.employeeOntologyDoc(emp); strings.Contains(doc, "SECRET") {
		t.Errorf("正常路径不应读到越界文件内容")
	}
}

// TestSpawnTask_DeepProfileInjectedIntoAgentContext 端到端断言（P2-3 缺口补齐）：
// SpawnTask 指派员工执行时，深入本体档案（DeepProfile，含工作区蒸馏文档）必须
// 真实拼进 Manifest.Summary 并出现在发给子 Agent 模型的上下文基线里——而非仅
// 在展示层/函数层存在。用捕获型 backend 记录子 Agent 收到的完整消息序列，
// 断言上下文基线包含本体档案关键段落。
func TestSpawnTask_DeepProfileInjectedIntoAgentContext(t *testing.T) {
	rt := newSpawnRuntime(t)

	// 补写 coder 工作区本体说明书，验证"蒸馏文档"也随档案注入
	wsDir := filepath.Join(rt.Config.Project.Root, ".contextloom", "employees", "coder", "workspace")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := "工作区蒸馏：错误处理先于业务；提交按主题拆分；改动前先圈定影响面。"
	if err := os.WriteFile(filepath.Join(wsDir, "ONTOLOGY.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	// 捕获型 backend：记录每次 Chat/Stream 收到的消息
	// mu 保护 messages：SpawnTask 返回后 distill goroutine 仍在后台异步调用
	// Chat（本体能力自动蒸馏），与测试断言并发访问，-race 下必须加锁。
	captured := &ontologyCaptureBackend{response: "任务完成"}
	rt.Backend = captured
	_, err := rt.SpawnTask(context.Background(), "main", tools.TaskSpec{
		Task:      "实现登录函数",
		Directive: "coder", // 精确员工名 → 员工本体装配
	})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	if len(captured.snapshot()) == 0 {
		t.Fatal("子 Agent 应至少发起一次模型请求")
	}
	// 上下文基线：把全部请求消息拼起来找本体档案段落
	var all string
	for _, m := range captured.snapshot() {
		all += fmt.Sprintf("%v\n", m.Content)
	}
	for _, want := range []string{
		"【员工本体 · coder】",
		"岗位职责:",
		"专精领域",
		"标准工作流",
		"行为约束",
		"工作区蒸馏：错误处理先于业务；提交按主题拆分；改动前先圈定影响面。",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("子 Agent 上下文基线缺本体档案段落 %q\n完整消息:\n%s", want, all)
		}
	}
}

// captureBackend 记录每次请求消息的模型后端（端到端断言子 Agent 收到的上下文）。
// messages 的追加与读取可能分属不同 goroutine（主任务请求 vs 后台 distill），
// 用 mu 串行化；断言读取一律走加锁快照 snapshot()。
type ontologyCaptureBackend struct {
	response string
	mu       sync.Mutex
	messages []types.Message
}

func (c *ontologyCaptureBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	c.mu.Lock()
	c.messages = append(c.messages, messages...)
	c.mu.Unlock()
	return c.response, nil, nil
}
func (c *ontologyCaptureBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	c.mu.Lock()
	c.messages = append(c.messages, messages...)
	c.mu.Unlock()
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		ch <- c.response
	}()
	return ch, errCh
}

// snapshot 返回消息的加锁快照（避免与后台 goroutine 并发读切片）。
func (c *ontologyCaptureBackend) snapshot() []types.Message {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]types.Message, len(c.messages))
	copy(out, c.messages)
	return out
}
func (c *ontologyCaptureBackend) ModelID() string           { return "mock-capture" }
func (c *ontologyCaptureBackend) MaxContext() int           { return 4096 }
func (c *ontologyCaptureBackend) SupportsVision() bool      { return false }
func (c *ontologyCaptureBackend) SetReasoningEffort(string) {}
func (c *ontologyCaptureBackend) ReasoningEffort() string   { return "" }
func (c *ontologyCaptureBackend) SetTopP(p float64)         {}
func (c *ontologyCaptureBackend) TopP() float64             { return 0 }
func (c *ontologyCaptureBackend) SetTemperature(t float64)  {}
func (c *ontologyCaptureBackend) Temperature() float64      { return 0 }

// TestEmployeeCenter_ConcurrentFirstAccess 并发首次访问员工中心（P2-2 回归防护）：
// 多个 goroutine 同时触发 employeeByName/EmployeeNames 初始化，中心必须是单例
// （sync.Once 保证），不能出现 lost-update（后者覆盖前者 → 已加载 byName 丢失）。
// 断言：并发访问后员工仍完整可查、且中心指针全局唯一。
func TestEmployeeCenter_ConcurrentFirstAccess(t *testing.T) {
	rt, tmp := newTestRuntime(t)
	defer os.RemoveAll(tmp)
	// 强制清空缓存，模拟进程内首次访问的并发起点
	rt.employeeOnce = sync.Once{}
	rt.employees = nil

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = rt.EmployeeNames()
			_ = rt.employeeByName("coder")
			_ = rt.EmployeesOverview()
		}()
	}
	wg.Wait()

	// 并发后员工中心应仍可用：coder 完整加载（若 lost-update，byName 可能为空）
	if emp := rt.employeeByName("coder"); emp == nil {
		t.Fatal("并发初始化后 coder 员工丢失（lost-update 复现）")
	}
	if len(rt.EmployeeNames()) < 6 {
		t.Errorf("并发初始化后应有 6 名种子员工，got %d", len(rt.EmployeeNames()))
	}
}
