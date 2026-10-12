// Package runtime —— 工具链体系升级方案（v1.8）「待接续」里程碑的接线层。
//
// 本文件把 M2~M5 四个已落地包体（orchestrator/scenes/selfcheck/toolchain）
// 以「零配置兜底」方式挂载进 Runtime：
//
//   - 员工中心（M3）：.contextloom/employees/<name>/employee.yaml 存在才生效，
//     SpawnTask 的 directive 命中员工（精确名或通用指令映射 CODE_AGENT→coder 等）
//     时按员工档案约束子 Agent；
//     无 employees 目录/未命中 → 完全走旧路径（CODE_AGENT 默认）。
//   - 场景中心（M4）：.contextloom/scenes/ 存在且关键词命中才接管，
//     未命中 → 退回裸 chat（与旧版完全一致）。
//   - 工具链工厂（M2）：.contextloom/chains/<name>/toolchain.yaml 存在才可运行；
//     RunToolchain 是纯增量 API，不改变任何既有执行路径。
//   - 质量门禁（M5）：.contextloom/gates/ 目录存在才激活静态门禁（默认档 standard），
//     否则 QualityNone 恒放行——行为与旧版完全一致。
//
// 全部实现只用标准库 + 已有包体；go.mod 零新增；跨平台（无 cgo）。
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/internal/modelgateway"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/scenes"
	"github.com/contextloom/contextloom-agent/internal/selfcheck"
	"github.com/contextloom/contextloom-agent/internal/toolchain"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
	"gopkg.in/yaml.v3"
)

// ── 目录约定（与 skills/plugins 一致：.contextloom/ 下按品类分目录）──

const (
	employeesDir = "employees" // .contextloom/employees/<name>/employee.yaml（M3）
	scenesDir    = "scenes"    // .contextloom/scenes/<name>/scene.yaml（M4）
	chainsDir    = "chains"    // .contextloom/chains/<name>/toolchain.yaml（M2）
	gatesDir     = "gates"     // .contextloom/gates/（目录存在即启用质量门禁，M5）
	gateFileName = "gate.yaml" // .contextloom/gates/gate.yaml：三档 + 阈值（GUI 门禁面板写入）
)

// employeeCenter 惰性加载的员工中心（M3）。
type employeeCenter struct {
	mu     sync.RWMutex
	loaded bool
	byName map[string]*orchestrator.Employee
}

func (c *employeeCenter) get(name string) *orchestrator.Employee {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.byName[name]
}

// loadEmployees 从 .contextloom/employees/ 加载全部员工档案；目录不存在时
// 返回空 map（零配置兜底：无员工 = 旧行为）。
func (rt *Runtime) loadEmployees() map[string]*orchestrator.Employee {
	if rt.Config == nil {
		return nil
	}
	dir := filepath.Join(rt.Config.Project.Root, ".contextloom", employeesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make(map[string]*orchestrator.Employee)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		emp, err := orchestrator.Load(filepath.Join(dir, e.Name()))
		if err != nil || emp.Name == "" {
			continue // 坏档案静默跳过（不阻塞启动，审计靠静态门禁发现）
		}
		// 深入本体迁移：旧版档案（缺新字段）按内置模板回退补齐，其余字段用户自定义保留。
		out[emp.Name] = emp.MergeDeepDefaults()
	}
	return out
}

// employeeByName 惰性读取员工档案（带缓存；目录删除后缓存不清——进程内一致即可）。
// 精确匹配员工名（历史语义）：directive 恰好等于员工名才命中并装配员工约束。
// 通用指令（CODE_AGENT/TEST）不在此命中，避免意外改变子任务装配行为；
// 需要展示员工名时用 displayEmployeeForDirective（纯显示映射，不装配）。
func (rt *Runtime) employeeByName(name string) *orchestrator.Employee {
	rt.ensureEmployeeCenter()
	rt.employees.mu.Lock()
	defer rt.employees.mu.Unlock()
	if !rt.employees.loaded {
		rt.employees.byName = rt.loadEmployees()
		rt.employees.loaded = true
	}
	return rt.employees.byName[name]
}

// ensureEmployeeCenter 单例初始化员工中心（P2-2 竞态修复）：
// 并发首次访问时多个 goroutine 同时执行 `if rt.employees == nil` 会创建多个
// employeeCenter，后者覆盖前者（已加载 byName 短暂丢失 → 重复读盘 + race 报告）。
// 用 sync.Once 保证只初始化一次；中心内部的加载仍由 mu 串行化。
func (rt *Runtime) ensureEmployeeCenter() {
	rt.employeeOnce.Do(func() {
		rt.employees = &employeeCenter{}
	})
}

// candidateEmployeeForDirective 返回通用指令对应的员工候选名（不校验存在性）。
// 与 schema 描述（spawn_task 的 directive 参数）保持一致：模型可传员工名，
// 也可传通用指令；通用指令按角色映射到员工。
func (rt *Runtime) candidateEmployeeForDirective(directive string) string {
	switch strings.ToUpper(strings.TrimSpace(directive)) {
	case "CODE_AGENT", "TASK", "TASK_AGENT", "REFACTOR", "REFACTOR_AGENT", "IMPLEMENT", "FIX":
		return "coder"
	case "TEST", "TEST_AGENT", "VERIFY", "VERIFIER", "RUN_TESTS":
		return "tester"
	case "PLANNER", "SPEC", "PRODUCT", "DESIGN":
		return "product"
	case "REVIEW", "REVIEW_AGENT", "CODE_REVIEW":
		return "reviewer"
	case "WRITE", "WRITER", "COPYWRITING", "COPY":
		return "writer"
	case "TRANSLATE", "TRANSLATOR":
		return "translator"
	default:
		return ""
	}
}

// resolveEmployeeForDirective 按指令解析应装配的员工（真实装配，非仅显示）：
//  1. 精确命中员工名（coder/tester/…）→ 该员工（历史语义，显式指定）；
//  2. 通用指令映射（CODE_AGENT→coder、TEST→tester、PLANNER→product、
//     REVIEW→reviewer、WRITE→writer、TRANSLATE→translator）→ 对应员工。
//
// 仅当员工中心真实存在该员工时才返回；零配置/未命中 → nil（调用方走旧路径，
// 完全保持零配置兜底语义）。
func (rt *Runtime) resolveEmployeeForDirective(directive string) *orchestrator.Employee {
	if emp := rt.employeeByName(directive); emp != nil {
		return emp
	}
	cand := rt.candidateEmployeeForDirective(directive)
	if cand == "" {
		return nil
	}
	return rt.employeeByName(cand)
}

// displayEmployeeForDirective 为通用指令提供纯显示映射（不改变装配行为）：
// goal/task 模式模型常传 CODE_AGENT/TEST 等通用指令，映射到对应员工名仅供
// TUI 子任务面板展示；仅当员工中心真实存在该员工时才返回（避免显示不存在的员工）。
func (rt *Runtime) displayEmployeeForDirective(directive string) string {
	cand := rt.candidateEmployeeForDirective(directive)
	if cand == "" {
		return ""
	}
	if rt.employeeByName(cand) != nil {
		return cand
	}
	return ""
}

// employeeTools 员工工具约束：allowlist 为空 = 不限制（全注册表）；否则
// 用 CloneWithout 移除不在允许清单内的工具（exclude = 全内置 - 允许集）。
// 保留原注册表的 cautions/hooks 语义由调用方在新注册表上另行设置。
func (rt *Runtime) employeeTools(emp *orchestrator.Employee) *tools.Registry {
	policy, err := emp.ResolvePolicy()
	if err != nil || len(policy.Tools) == 0 {
		return rt.Tools
	}
	allowed := make(map[string]bool, len(policy.Tools))
	for _, t := range policy.Tools {
		allowed[t] = true
	}
	var exclude []string
	for _, def := range rt.Tools.ToolDefinitions() {
		if !allowed[def.Function.Name] {
			exclude = append(exclude, def.Function.Name)
		}
	}
	// 防递归工具对子 Agent 恒排除（SpawnTask 既有语义保持）
	exclude = append(exclude, "spawn_task", "handoff")
	return rt.Tools.CloneWithout(exclude...)
}

// maxOntologyDocBytes 员工工作区本体能力说明书的最大注入字节数。
// 超过上限截断并标注，防止用户把本体蒸馏成数万行参考代码/风格指南后
// 全量注入每个指派该员工的子 Agent 上下文基线撑爆预算
// （P1-1：宁可截断标注，不先注入再压缩浪费且丢失）。
const maxOntologyDocBytes = 16 * 1024 // 16KB

// employeeSafeNameRe 员工目录名白名单（P2-1 路径校验）：仅允许字母/数字/
// 下划线/连字符，且不含路径分隔符——杜绝 emp.Name 从 yaml 带入 ../ 或绝对
// 路径越界读取员工目录/项目外任意文件并注入模型上下文（敏感信息外泄面）。
var employeeSafeNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// employeeOntologyDoc 读取员工工作区本体能力说明书（workspace/ONTOLOGY.md
// 或档案显式指定的 ontology_file）的文本内容。文件不存在/读取失败 →
// 返回 ""（能力蒸馏缺失不阻断装配，零配置兜底语义保持）。
// 工作区路径解析规则：employee.yaml 里的 workspace 字段是相对员工目录的
// 子目录名（默认约定 workspace）；ontology_file 若相对则同样相对员工目录。
//
// 安全与容量约束（P2-1/P1-1）：
//   - 员工名必须是白名单安全名（非法 → 返回 ""，不解析任何路径）；
//   - ontology_file 解析后必须落在员工目录内（filepath.Clean + HasPrefix
//     双重校验，防 ../ 或绝对路径越界）；
//   - 文档内容超过 maxOntologyDocBytes 时截断并标注，防上下文撑爆。
func (rt *Runtime) employeeOntologyDoc(emp *orchestrator.Employee) string {
	if emp == nil || rt.Config == nil {
		return ""
	}
	if !employeeSafeNameRe.MatchString(emp.Name) {
		return "" // 非法员工名：拒绝解析任何路径（P2-1）
	}
	dir := filepath.Join(rt.Config.Project.Root, ".contextloom", employeesDir, emp.Name)
	// OntologyPath 恒返回一个相对路径（显式 ontology_file 或默认 workspace/ONTOLOGY.md）
	rel := emp.OntologyPath()
	p := rel
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, rel)
	}
	// P2-1：清理后必须仍落在员工目录内（filepath.Join 会把 ../ 归一化，
	// 这里显式 Clean + HasPrefix 校验，防越界读任意文件）
	clean := filepath.Clean(p)
	dirClean := filepath.Clean(dir)
	if clean != dirClean && !strings.HasPrefix(clean, dirClean+string(filepath.Separator)) {
		return "" // 越界路径：拒绝读取（P2-1）
	}
	data, err := os.ReadFile(clean)
	if err != nil {
		return ""
	}
	doc := strings.TrimSpace(string(data))
	// P1-1：容量上限截断（宁可截断标注，不撑爆子 Agent 上下文）
	if len(doc) > maxOntologyDocBytes {
		doc = doc[:maxOntologyDocBytes]
		doc += "\n\n（注：本体能力说明书超过 " + strconv.Itoa(maxOntologyDocBytes) + " 字节，已截断注入——请精简工作区文档以保留完整能力蒸馏）"
	}
	return doc
}

// employeeDeepProfile 编译员工深入本体档案（DeepProfile + 工作区蒸馏文档），
// 返回可注入子 Agent 上下文的完整本体能力文本。员工缺失/无深入字段 →
// 返回 ""（旧行为：只有 role/persona 注入）。
func (rt *Runtime) employeeDeepProfile(emp *orchestrator.Employee) string {
	if emp == nil {
		return ""
	}
	doc := rt.employeeOntologyDoc(emp)
	return emp.DeepProfile(doc)
}

// EmployeeNames 返回员工中心已加载的全部员工名（进程内一次惰性加载）。
// 无 employees 目录/无档案 → 空列表（零配置兜底）。
func (rt *Runtime) EmployeeNames() []string {
	rt.ensureEmployeeCenter()
	byName := func() map[string]*orchestrator.Employee {
		rt.employees.mu.Lock()
		defer rt.employees.mu.Unlock()
		if !rt.employees.loaded {
			rt.employees.byName = rt.loadEmployees()
			rt.employees.loaded = true
		}
		return rt.employees.byName
	}()
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// EmployeeSummary 返回单名员工的可读摘要（名称/岗位/专精/工作区），供
// /emp 命令与 GUI 展示。员工不存在 → ""。
func (rt *Runtime) EmployeeSummary(name string) string {
	emp := rt.employeeByName(name)
	if emp == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s", emp.Name))
	if emp.Display != "" {
		b.WriteString("（" + emp.Display + "）")
	}
	b.WriteString(" · " + emp.Role)
	if len(emp.Expertise) > 0 {
		b.WriteString(" · 专精: " + strings.Join(emp.Expertise, "/"))
	}
	if emp.Workspace != "" {
		b.WriteString(" · 工作区: " + emp.Workspace)
	}
	return b.String()
}

// EmployeesOverview 供 /emp 无参展示全部员工清单。
func (rt *Runtime) EmployeesOverview() string {
	names := rt.EmployeeNames()
	if len(names) == 0 {
		return "暂无员工档案（.contextloom/employees/ 下创建 <name>/employee.yaml 即生效）"
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("可用员工 %d 名:\n", len(names)))
	for _, n := range names {
		if s := rt.EmployeeSummary(n); s != "" {
			b.WriteString("  " + s + "\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// AssignEmployee 把任务显式指派给指定员工执行（/emp <name> <task> 入口，
// 用户可直接指定员工干活——而非仅由模型在 spawn_task 里隐式选择）。
// 执行走标准子任务管线（SpawnTask 同路径，员工本体/工具表/模型绑定全装配）。
//
// 同步阻塞语义（P1-2 修复前）：原实现 context.Background() 同步等待 SpawnTask
// 完成，而 TUI 命令执行是同步的——指派长任务时整个界面阻塞直到子任务结束，
// 返回文案却写"已进入执行队列"，与同步行为不符，易误导用户。
//
// 异步语义（P1-2 修复后）：先在当前 goroutine 做轻量同步校验（员工是否存在、
// 任务是否为空），通过后**后台 goroutine** 执行 SpawnTask，函数立即返回
// （TUI 命令即刻响应，不阻塞界面）。SpawnTask 的内部 § 进度信号仍照常走
// 其 ctx 携带的 progressChan（由调用方注入）呈现到子任务面板。
// SpawnTask 的最终执行错误经内部日志记录（调用方拿不到同步结果；
// 如需等待完成请用 SPAWN 专用通道或后续状态查询——当前 /emp 为即发即忘）。
func (rt *Runtime) AssignEmployee(name, task string) error {
	if emp := rt.employeeByName(name); emp == nil {
		avail := rt.EmployeeNames()
		if len(avail) == 0 {
			return fmt.Errorf("员工 %q 不存在且无任何员工档案（.contextloom/employees/ 下创建）", name)
		}
		return fmt.Errorf("员工 %q 不存在，可用: %s", name, strings.Join(avail, " / "))
	}
	if task == "" {
		return fmt.Errorf("任务描述为空（用法: /emp <员工名> <任务描述>）")
	}
	// 同步校验通过 → 后台异步执行（P1-2：不阻塞 TUI）。复用 SpawnTask：
	// directive=员工名 → resolveEmployeeForDirective 精确命中，本体能力/工具表/
	// 模型绑定全部按员工档案装配；任务上下文标注指派来源。
	spec := tools.TaskSpec{Directive: name, Task: task, Context: "用户显式指派给员工 " + name + " 执行"}
	go func() {
		ctx := context.Background()
		_, err := rt.SpawnTask(ctx, "main", spec)
		if err != nil {
			log.Printf("[emp] 员工 %q 执行任务失败: %v", name, err)
		}
	}()
	return nil
}

// executeToolByName 经主注册表执行工具（工具链 ToolExecutor 转发壳）。
func (rt *Runtime) executeToolByName(ctx context.Context, name string, params map[string]any) (any, error) {
	argsJSON, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("工具 %q 参数序列化失败: %w", name, err)
	}
	call := types.ToolCall{
		ID:   fmt.Sprintf("call-%d", timeNow().UnixNano()),
		Type: "function",
		Function: types.FunctionCall{
			Name:      name,
			Arguments: string(argsJSON),
		},
	}
	res, err := rt.Tools.Execute(ctx, call)
	if err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, fmt.Errorf("%s", res.Error)
	}
	return res.Result, nil
}

// ── 场景中心（M4）──

// matchScene 在 chat 入口做零成本规则漏斗：目录不存在/无关键词命中 → nil。
// 模型层分类器默认关闭（零 token 成本、绝不阻塞用户输入），仅在
// .contextloom/scenes/ 存在且用户显式开启时可用（见 §4.4 语义：规则优先）。
func (rt *Runtime) matchScene(input string) *scenes.Scene {
	dir := filepath.Join(rt.Config.Project.Root, ".contextloom", scenesDir)
	if _, err := os.Stat(dir); err != nil {
		return nil
	}
	catalog, err := scenes.LoadAll(dir)
	if err != nil || len(catalog) == 0 {
		return nil
	}
	// 规则层命中即接管；模型层分类器置 nil（意图解析永不失败、永不阻塞）
	return scenes.Match(catalog, input, nil)
}

// launchScene 场景一键执行（M4）：组装场景任务交标准管线（launchAgent），
// 复用监工/会话隔离/上下文/交付全链路——不另起旁路。
//
// 装配语义：
//   - 触发原话 + 场景可达信息（工具链预跑产物）拼入 Task 提示词；
//   - mode 用 "task"：场景即任务，有监工把关质量（低门槛 + 不裸奔）；
//   - 产出交付：scenes.Run 之后（launchAgent 返回）调用方拿到的是输出流，
//     交付写文件由 sendSceneOutput 就地完成，不阻塞既有管线段落。
func (rt *Runtime) launchScene(ctx context.Context, userInput string, sc scenes.Scene) (<-chan string, <-chan error, error) {
	// 场景级工具链预跑（同步、失败降级不阻塞场景启动）
	var chainOut string
	if sc.Toolchain != "" {
		res, cerr := rt.RunToolchain(ctx, sc.Toolchain, map[string]string{"user_input": userInput})
		if cerr != nil {
			chainOut = "（工具链 " + sc.Toolchain + " 执行失败，已降级普通执行：" + cerr.Error() + "）"
		} else {
			chainOut = chainOutput(res)
		}
	}
	prompt := scenePrompt(sc, userInput, chainOut)
	task := types.TaskCreate{
		Type:          "task.create",
		ID:            "main",
		Directive:     string(types.DirectiveCodeAgent),
		Task:          prompt,
		Payload:       nil,
		Mode:          "task", // 场景即任务：监工全程把关
		ContextPolicy: types.PolicyFullContext,
		Priority:      1,
	}
	return rt.launchAgent(ctx, task, userInput, "task")
}

// scenePrompt 组装场景执行提示词：场景触发原话 + 工具链产物 + 产出路径约束。
func scenePrompt(sc scenes.Scene, userInput, chainOut string) string {
	var b strings.Builder
	b.WriteString("场景「" + sc.Display + "」已触发。用户原话：" + userInput)
	if sc.Verify != "" {
		b.WriteString("\n验收标准：" + sc.Verify)
	}
	if chainOut != "" {
		b.WriteString("\n工具链产物（据此整理交付）：\n" + chainOut)
	}
	return b.String()
}

// ── 工具链工厂（M2）──

// RunToolchain 按名加载并执行一条工具链（纯增量 API：scene/team/模型可引用）。
// 工具执行注入主注册表；LLM 步骤注入模型网关（走池路由/熔断/降级）。
// 链不存在时返回可读错误（不静默）。
func (rt *Runtime) RunToolchain(ctx context.Context, name string, params map[string]string) (*toolchain.Result, error) {
	dir := filepath.Join(rt.Config.Project.Root, ".contextloom", chainsDir)
	chain, err := toolchain.Load(dir, name)
	if err != nil {
		return nil, fmt.Errorf("工具链 %q 不存在或损坏（%s）：%w", name, dir, err)
	}
	if problems := toolchain.Validate(*chain, toolchain.ValidateOptions{
		KnownTools: rt.toolNameSet(),
	}); len(problems) > 0 {
		return nil, fmt.Errorf("工具链 %q 校验失败：%s", name, strings.Join(problems, "；"))
	}
	// 预加载同目录下全部子链（ref 步骤按名查找；找不到时 run 时报可读错误）
	subs := map[string]*toolchain.Toolchain{}
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			base := e.Name()
			if !strings.HasSuffix(base, ".yaml") && !strings.HasSuffix(base, ".yml") {
				continue
			}
			subName := strings.TrimSuffix(strings.TrimSuffix(base, ".yaml"), ".yml")
			if subName == name {
				continue
			}
			if sub, err := toolchain.Load(dir, subName); err == nil {
				subs[subName] = sub
			}
		}
	}
	opts := toolchain.RunOptions{
		ParamsRun: params,
		SubChains: subs,
		Tools: func(toolName string, p map[string]any) (string, error) {
			raw, err := rt.executeToolByName(ctx, toolName, p)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("%v", raw), nil
		},
		LLM: func(pctx context.Context, prompt string, artifacts map[string]string) (string, error) {
			backend := rt.selectBackend(pctx, modelgateway.Req{Task: "toolchain:" + name})
			msg, _, err := backend.Chat(pctx, []types.Message{{Role: "user", Content: prompt}}, nil)
			return msg, err
		},
	}
	return toolchain.Run(ctx, *chain, opts)
}

// toolNameSet 注册表全部工具名集合（供工具链静态校验）。
func (rt *Runtime) toolNameSet() map[string]bool {
	out := make(map[string]bool)
	for _, def := range rt.Tools.ToolDefinitions() {
		out[def.Function.Name] = true
	}
	return out
}

// chainOutput 从工具链结果提取可读产物文本（链级产物 → 各步骤产物 → 日志尾）。
func chainOutput(res *toolchain.Result) string {
	if res == nil {
		return ""
	}
	if len(res.Artifacts) > 0 {
		// 优先 "output"/"result" 约定键，否则取最后一个产物
		var keys []string
		for k := range res.Artifacts {
			keys = append(keys, k)
		}
		for _, preferred := range []string{"output", "result", "final"} {
			if v, ok := res.Artifacts[preferred]; ok {
				return v
			}
		}
		if len(keys) > 0 {
			return res.Artifacts[keys[len(keys)-1]]
		}
	}
	if len(res.Log) > 0 {
		return res.Log[len(res.Log)-1].Summary
	}
	return ""
}

// ── 质量门禁（M5）──

// gateState 进程级门禁状态（惰性初始化，目录存在才激活）。
// gate.yaml 修改后按 mtime 自动重载——GUI 保存三档/阈值后下一轮 goal 即生效，
// 无需重启（与 GUI 门禁面板"下轮 goal 生效"的承诺一致）。
type gateState struct {
	mu        sync.Mutex
	init      bool
	level     selfcheck.QualityLevel
	passScore int
	mtime     time.Time // gate.yaml 修改时间戳（变化即重载）
}

// ── 质量账本持久化（M5：quality_ledger.yaml 落盘）──

// ledgerState 质量账本落盘状态（进程级单例，惰性初始化）。
//
// 零配置兜底：.contextloom/gates/ 目录不存在 → ready=false，不做任何账本操作
// （不读不写），行为与旧版完全一致。目录存在时：
//   - 首次访问从 quality_ledger.yaml 反序列化累计（损坏/缺失 → 空账本重新开始）；
//   - 每轮门禁 Fail/HumanReview 裁决把缺陷签名 Record 进账本并当轮落盘，
//     供后续轮次（同一进程或重启后）优先核查历史高频缺陷。
type ledgerState struct {
	mu     sync.Mutex
	init   bool
	ready  bool   // .contextloom/gates/ 目录存在（false = 零操作）
	path   string // <root>/.contextloom/gates/quality_ledger.yaml
	ledger *selfcheck.Ledger
}

// gateLedger 惰性初始化质量账本状态（进程内缓存；含磁盘加载与目录探测）。
// ready=false 时每次访问重新探测目录（GUI 门禁面板保存 gate.yaml 会创建
// gates 目录，同进程内下一轮 goal 即可恢复账本能力，无需重启）。
func (rt *Runtime) gateLedger() *ledgerState {
	if rt.gateLedgerState == nil {
		rt.gateLedgerState = &ledgerState{
			path: filepath.Join(rt.Config.Project.Root, ".contextloom", gatesDir, "quality_ledger.yaml"),
		}
	}
	ls := rt.gateLedgerState
	ls.mu.Lock()
	defer ls.mu.Unlock()
	if ls.init && ls.ready {
		return ls
	}
	ls.init = true
	// 目录存在才启用；同时尝试恢复磁盘上已有的账本（重启后累计不丢失）
	if _, err := os.Stat(filepath.Dir(ls.path)); err == nil {
		ls.ready = true
		if data, err := os.ReadFile(ls.path); err == nil {
			if l := selfcheck.NewLedger(); l.UnmarshalYAML(data) == nil {
				ls.ledger = l
			}
		}
	}
	if ls.ledger == nil {
		ls.ledger = selfcheck.NewLedger()
	}
	return ls
}

// recordGateVerdict 每轮门禁裁决后累计质量账本并落盘（接线点：GoalOnRoundEnd）：
//   - Pass：无缺陷可记，直接返回（零磁盘写入）；
//   - Fail/HumanReview：把 high/medium 静检缺陷签名（Kind: Hint，去掉文件行号
//     以便跨轮聚合）+ TEE 失败签名累计进账本，当轮写回 quality_ledger.yaml；
//   - 零配置兜底：.contextloom/gates/ 不存在 → ready=false，不做任何操作；
//   - 落盘失败只记日志，绝不阻塞主流程。
func (rt *Runtime) recordGateVerdict(rep selfcheck.QualityReport, dec selfcheck.Decision) {
	if dec == selfcheck.DecisionPass {
		return
	}
	ls := rt.gateLedger()
	if !ls.ready {
		return
	}
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for _, it := range rep.Static.Issues {
		if it.Severity != "high" && it.Severity != "medium" {
			continue
		}
		ls.ledger.Record(it.Kind + ": " + it.Hint)
	}
	// TEE 失败签名（与 Gate.Admit 语义一致：文档类产物 TEE 不适用，不计缺陷）
	if rep.TEE != nil && rep.TEE.ArtifactType != selfcheck.ArtifactDoc && selfcheck.Verify(*rep.TEE) != selfcheck.TEEPassed {
		ls.ledger.Record("TEE失败: " + rep.TEE.String())
	}
	ls.save()
}

// save 把账本落盘到 quality_ledger.yaml（幂等；失败仅记日志不阻塞）。
func (ls *ledgerState) save() {
	data, err := ls.ledger.MarshalYAML()
	if err != nil {
		log.Printf("[gate] 质量账本序列化失败: %v", err)
		return
	}
	if err := os.WriteFile(ls.path, data, 0o644); err != nil {
		log.Printf("[gate] 质量账本落盘失败: %v", err)
	}
}

// qualityGate 返回当前质量档（gate.yaml 三档开关接线，与 GUI 门禁面板对齐）：
//   - .contextloom/gates/ 目录不存在 → none（门禁关闭 = 旧行为，零配置兜底）；
//   - 目录存在但无 gate.yaml → standard + 阈值 90（默认拉满）；
//   - gate.yaml 存在 → 读 level（standard/fast/none）与 pass_score（0-100 校验）；
//   - gate.yaml 被 GUI 修改后（mtime 变化）→ 自动重载，下一轮 goal 生效；
//   - gate.yaml 解析失败/字段非法 → 回退默认（standard/90），不阻塞启动。
func (rt *Runtime) qualityGate() selfcheck.QualityLevel {
	rt.loadGateState()
	return rt.gates.level
}

// gatePassScore 返回门禁通过阈值（gate.yaml 的 pass_score；默认 90）。
func (rt *Runtime) gatePassScore() int {
	rt.loadGateState()
	if rt.gates.passScore <= 0 || rt.gates.passScore > 100 {
		return 90
	}
	return rt.gates.passScore
}

// loadGateState 惰性加载/重载门禁状态（内部持有 rt.gates.mu）。
func (rt *Runtime) loadGateState() {
	if rt.gates == nil {
		rt.gates = &gateState{}
	}
	g := rt.gates
	g.mu.Lock()
	defer g.mu.Unlock()

	gatesDir := filepath.Join(rt.Config.Project.Root, ".contextloom", gatesDir)
	st, err := os.Stat(gatesDir)
	if err != nil || !st.IsDir() {
		// 目录不存在（含被删除）→ 门禁关闭，重置缓存
		g.init = true
		g.level = selfcheck.QualityNone
		g.passScore = 90
		g.mtime = time.Time{}
		return
	}

	// 目录存在：读取 gate.yaml 的 mtime（不存在 → 默认档）
	var mtime time.Time
	if fi, ferr := os.Stat(filepath.Join(gatesDir, gateFileName)); ferr == nil {
		mtime = fi.ModTime()
	}
	if g.init && mtime.Equal(g.mtime) {
		return // 未变化：沿用缓存（GUI 保存后 mtime 变化才会重载）
	}
	g.init = true
	g.mtime = mtime
	g.level = selfcheck.QualityStandard // 默认拉满
	g.passScore = 90

	data, err := os.ReadFile(filepath.Join(gatesDir, gateFileName))
	if err != nil {
		return
	}
	var raw struct {
		Level     string `yaml:"level"`
		PassScore int    `yaml:"pass_score"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return // 坏 gate.yaml：回退默认，不阻塞
	}
	if lv := strings.TrimSpace(raw.Level); lv != "" {
		g.level = selfcheck.LevelFromString(lv)
	}
	if raw.PassScore > 0 && raw.PassScore <= 100 {
		g.passScore = raw.PassScore
	}
}

// goalGateReport 目标轮结束时构造门禁输入：本轮 git 改动文件 → 静态核查 →
// 评分维度（监工分数加权 + 静检通过率）。返回 QualityReport 与裁决。
//
// 零配置兜底：门禁档为 none 时不扫描、不裁决（Admit 恒 Pass）。
// 三档语义（gate.yaml）：standard 全开（静检+TEE）；fast 关 TEE 保静检；
// none 关门禁。阈值取 gate.yaml 的 pass_score（默认 90）。
func (rt *Runtime) goalGateReport(passed bool, score float64) (selfcheck.QualityReport, selfcheck.Decision) {
	level := rt.qualityGate()
	passScore := rt.gatePassScore()
	if !level.StaticEnabled() {
		return selfcheck.QualityReport{}, selfcheck.DecisionPass
	}
	// 本轮改动文件（git diff --name-only；非 git 仓库/无改动 → 空清单）
	files := rt.changedFiles()
	var changes []selfcheck.FileChange
	for _, f := range files {
		// 只扫文本类文件（代码/文档/配置）；二进制跳过
		if !isTextFile(f) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rt.Config.Project.Root, f))
		if err != nil {
			continue
		}
		changes = append(changes, selfcheck.FileChange{Path: f, Content: string(data)})
	}
	if len(changes) == 0 {
		// 无文件改动（纯对话轮）→ 静检自然通过
		return selfcheck.QualityReport{Static: selfcheck.StaticReport{Pass: true}, Score: int(score), PassScore: passScore}, selfcheck.DecisionPass
	}
	checker := selfcheck.NewStaticChecker(selfcheck.StaticOptions{
		KnownSymbols: rt.knownGoSymbols(),
	})
	static := checker.Check(changes)
	// 代码类目标（改动含 .go 文件）且 standard 档 → 附加测试 TEE 实测维度；
	// fast 档关 TEE（只静检），none 档已在前面返回。
	var tee *selfcheck.TestTEEResult
	if level.TEEEnabled() {
		for _, f := range files {
			if strings.HasSuffix(f, ".go") {
				tee = rt.goalTEEPassed()
				break
			}
		}
	}
	// 综合评分：监工分 70% + 静检通过率 30%
	finalScore := int(score*0.7 + (1.0-staticPenalty(static))*30)
	rep := selfcheck.QualityReport{
		Static:    static,
		TEE:       tee,
		Score:     finalScore,
		PassScore: passScore,
	}
	return rep, selfcheck.NewGate(level, passScore).Admit(rep)
}

// staticPenalty 静检扣分：每个 high 扣 20、medium 扣 10（封顶 1.0）。
func staticPenalty(rep selfcheck.StaticReport) float64 {
	p := 0.0
	for _, it := range rep.Issues {
		switch it.Severity {
		case "high":
			p += 0.2
		case "medium":
			p += 0.1
		}
	}
	if p > 1.0 {
		p = 1.0
	}
	return p
}

// changedFiles 返回 git 工作区变更文件（相对路径；非 git 仓库 → nil）。
func (rt *Runtime) changedFiles() []string {
	cmd := exec.Command("git", "diff", "--name-only")
	cmd.Dir = rt.Config.Project.Root
	data, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		names = append(names, line)
	}
	return names
}

// knownGoSymbols 扫描项目 .go 文件提取声明符号集（供 unused 闭锁核查）。
// 无 .go 文件/读取失败 → 空集（跳过该核查，不阻塞）。
func (rt *Runtime) knownGoSymbols() map[string]bool {
	syms := map[string]bool{}
	root := rt.Config.Project.Root
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			name := info.Name()
			if name == ".git" || name == ".contextloom" {
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, m := range goDeclRe.FindAllSubmatch(data, -1) {
			syms[string(m[1])] = true
		}
		return nil
	})
	return syms
}

var goDeclRe = regexp.MustCompile(`(?m)^\s*(?:var|const|type|func)\s+([A-Za-z_][A-Za-z0-9_]*)`)

// isTextFile 按后缀粗判文本类文件（零依赖；未知后缀按文本处理，宁多勿漏）。
func isTextFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".ico", ".bmp", ".webp",
		".zip", ".gz", ".tar", ".exe", ".bin", ".so", ".dylib", ".dll", ".pdf":
		return false
	}
	return true
}

// ── 测试 TEE 轻量钩子（M5：代码类目标的门禁实测维度）──
//
// goalTEEPassed 在门禁激活时执行 `go test ./...`（仅代码类目标），
// 结果注入 QualityReport.TEE。零配置：门禁 none 时绝不执行。
func (rt *Runtime) goalTEEPassed() *selfcheck.TestTEEResult {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = rt.Config.Project.Root
	output, err := cmd.CombinedOutput()
	passed := err == nil
	return &selfcheck.TestTEEResult{
		ArtifactType: selfcheck.ArtifactGoCode,
		Compiled:     passed,
		TestPassed:   passed,
		TestCount:    countTestLines(output),
		Output:       tailOf(string(output), 500),
	}
}

// countTestLines 粗略统计 `ok  …  <dur>` 行数（零依赖，失败时也尽力统计）。
func countTestLines(output []byte) int {
	n := 0
	for _, line := range bytes.Split(output, []byte("\n")) {
		s := strings.TrimSpace(string(line))
		if strings.HasPrefix(s, "ok") || strings.HasPrefix(s, "FAIL") {
			n++
		}
	}
	return n
}

func tailOf(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[len(s)-max:]
}

// timeNow 可注入时钟（便于测试；正式路径直接用 time.Now）。
func timeNow() time.Time { return time.Now() }

// ── 门禁收口辅助（GoalOnRoundEnd 内联调用）──

// stripGoalDone 从 response 剥离 [GOAL_DONE] 标记（门禁 Fail 时禁写外显）。
// [GOAL_POLISH_DONE] 不剥离（打磨轮门禁不拦，见 GoalOnRoundEnd 条件）。
func stripGoalDone(response string) string {
	if !strings.Contains(response, "[GOAL_DONE]") {
		return response
	}
	return strings.ReplaceAll(response, "[GOAL_DONE]", "[GOAL_BLOCKED_BY_GATE]")
}

// selfcheckGateDefects 把门禁报告转为 goalmgr 缺陷条目（注入下一轮修复清单）。
// 复用安全 ID 前缀 d-gate-，与监工缺陷（d- 前缀族）并存不冲突。
func selfcheckGateDefects(rep selfcheck.QualityReport) []types.GoalDefect {
	gate := selfcheck.NewGate(selfcheck.QualityStandard, rep.PassScore)
	defects := gate.DefectsFromReport(rep)
	out := make([]types.GoalDefect, 0, len(defects))
	for i, d := range defects {
		out = append(out, types.GoalDefect{
			ID:     fmt.Sprintf("d-gate-%d-%s", i, d.ID),
			Desc:   "质量门禁拦截：" + d.Desc,
			Status: "open",
		})
	}
	return out
}

// ── 打磨轮外部验收（证据链硬化，对照 why-self-verification 一文 §5.3）──
//
// verifyGoalPolish 是 [GOAL_POLISH_DONE] 的外部验收通道：
// 模型自答"打磨完成"不算数，必须由确定性进程（go test / 静态核查）确认。
//
// 判定规则：
//   - 本轮有 Go 代码改动 → 必须 go test ./... 通过（TEE 实测）；
//   - 本轮有其他文本改动 → 静态核查无 high/medium 残留；
//   - 无文件改动（纯对话轮）→ 通过（无产物可验）。
//
// 返回 (通过, 失败原因列表)。失败原因注入 d-polish 缺陷，下一轮强制修复。
func (rt *Runtime) verifyGoalPolish(s *types.GoalSession) (bool, []string) {
	files := rt.changedFiles()
	var reasons []string
	hasGo := false
	for _, f := range files {
		if strings.HasSuffix(f, ".go") {
			hasGo = true
			break
		}
	}
	if hasGo {
		// 代码类目标：go test ./... 全量实测（外部通道，不受上下文窗口限制）
		tee := rt.goalTEEPassed()
		if selfcheck.Verify(*tee) != selfcheck.TEEPassed {
			reasons = append(reasons, tee.String())
		}
	}
	// 静态核查（所有文本改动：残留标记 / 未决标记 [?]）
	var changes []selfcheck.FileChange
	for _, f := range files {
		if !isTextFile(f) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(rt.Config.Project.Root, f))
		if err != nil {
			continue
		}
		changes = append(changes, selfcheck.FileChange{Path: f, Content: string(data)})
	}
	if len(changes) > 0 {
		static := selfcheck.NewStaticChecker(selfcheck.StaticOptions{}).Check(changes)
		if !static.Pass {
			for _, it := range static.Issues {
				if it.Severity == "high" || it.Severity == "medium" {
					reasons = append(reasons, fmt.Sprintf("[%s] %s:%d %s", it.Kind, it.File, it.Line, it.Hint))
				}
			}
		}
	}
	return len(reasons) == 0, reasons
}
