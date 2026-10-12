package runtime

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/modelgateway"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 员工/子 Agent 自选模型解析测试（model_select.go）──
//
// 核心验收：员工 model.preset 在【未配置 gateway】时也必须真实生效——
// 此前 Preset 只作为网关 Req.Role 透传，无 role=<预设名> 规则即为静默
// 空操作（子 Agent 仍走默认后端），员工模型绑定形同虚设。

// newModelSelectRuntime 构造带多预设的 runtime（未配置 gateway）。
// 预设缓存为 per-Runtime 字段，新 Runtime 天然无串扰（无需重置）。
func newModelSelectRuntime(t *testing.T) *Runtime {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := testConfig(tmpDir)
	cfg.Models = []config.ModelPreset{
		{Name: "mock", Backend: "mock", MaxContext: 4096},
		{Name: "pro-model", Backend: "pro-backend", MaxContext: 8192},
		{Name: "flash-model", Backend: "flash-backend", MaxContext: 4096},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.Backend = &spawnBackend{response: "default-ok"}
	return rt
}

// TestBuildBackendForPreset_Resolves 直接按预设解析后端实例（不依赖网关）。
func TestBuildBackendForPreset_Resolves(t *testing.T) {
	rt := newModelSelectRuntime(t)
	b, err := rt.buildBackendForPreset("pro-model")
	if err != nil {
		t.Fatalf("预设 pro-model 应可解析，err=%v", err)
	}
	if b == nil {
		t.Fatal("解析结果不应为 nil")
	}
	// mock 后端的 ModelID 是 "mock-model"（固定），用 MaxContext 区分预设：
	// pro-model 预设 MaxContext=8192 ≠ 默认 mock 的 4096
	if b.MaxContext() != 8192 {
		t.Fatalf("预设后端 MaxContext = %d, want 8192（应按预设参数建厂，而非默认后端）", b.MaxContext())
	}
	// 大小写不敏感
	b2, err := rt.buildBackendForPreset("PRO-MODEL")
	if err != nil || b2 == nil {
		t.Fatalf("预设名应大小写不敏感，err=%v", err)
	}
}

// TestBuildBackendForPreset_Missing 未知预设返回错误并附可用列表。
func TestBuildBackendForPreset_Missing(t *testing.T) {
	rt := newModelSelectRuntime(t)
	_, err := rt.buildBackendForPreset("no-such-model")
	if err == nil {
		t.Fatal("未知预设应返回错误")
	}
	if !strings.Contains(err.Error(), "mock") {
		t.Fatalf("错误应附可用预设列表（含 mock），got %v", err)
	}
}

// TestEmployeeChainBackend_MultiSource：员工多来源绑定 → ChainBackend。
func TestEmployeeChainBackend_MultiSource(t *testing.T) {
	rt := newModelSelectRuntime(t)
	emp := &orchestrator.Employee{
		Name:  "reviewer",
		Model: orchestrator.ModelBinding{Preset: "pro-model", Fallbacks: []string{"flash-model"}},
	}
	chain, err := rt.employeeChainBackend(emp)
	if err != nil {
		t.Fatalf("员工多来源绑定应可解析，err=%v", err)
	}
	cb, ok := chain.(*model.ChainBackend)
	if !ok {
		t.Fatalf("应返回 *model.ChainBackend，got %T", chain)
	}
	if len(cb.Sources()) != 2 {
		t.Fatalf("组合链应含 2 个来源（主+备），got %d", len(cb.Sources()))
	}
	// 链保守下界 = min(8192, 4096) = 4096
	if chain.MaxContext() != 4096 {
		t.Fatalf("组合链 MaxContext 应取保守下界 4096，got %d", chain.MaxContext())
	}
}

// TestEmployeeChainBackend_Empty 无绑定员工返回 nil（继续网关/默认路径）。
func TestEmployeeChainBackend_Empty(t *testing.T) {
	rt := newModelSelectRuntime(t)
	chain, err := rt.employeeChainBackend(&orchestrator.Employee{Name: "x"})
	if err != nil || chain != nil {
		t.Fatalf("无绑定员工应返回 nil/nil，got (%v, %v)", chain, err)
	}
	chain, err = rt.employeeChainBackend(nil)
	if err != nil || chain != nil {
		t.Fatalf("nil 员工应返回 nil/nil，got (%v, %v)", chain, err)
	}
}

// TestResolveSubAgentBackend_EmployeePresetWithoutGateway 核心回归：
// 员工 model.preset 在未配置 gateway 时真正生效（此前为静默空操作）。
func TestResolveSubAgentBackend_EmployeePresetWithoutGateway(t *testing.T) {
	rt := newModelSelectRuntime(t) // 无 gateway 配置
	emp := &orchestrator.Employee{
		Name:  "reviewer",
		Model: orchestrator.ModelBinding{Preset: "pro-model"},
	}
	b := rt.resolveSubAgentBackend(context.Background(), "", emp, modelgateway.Req{Task: "spawn_task", Role: "reviewer"})
	if b == nil {
		t.Fatal("解析结果不应为 nil")
	}
	if b.MaxContext() != 8192 {
		t.Fatalf("员工 preset 应直接生效（不依赖网关规则）：MaxContext = %d, want 8192。"+
			"若为 4096 说明仍走默认后端（preset 静默空操作未修复）", b.MaxContext())
	}
}

// TestResolveSubAgentBackend_OverrideWins 显式自选模型优先级最高。
func TestResolveSubAgentBackend_OverrideWins(t *testing.T) {
	rt := newModelSelectRuntime(t)
	emp := &orchestrator.Employee{
		Name:  "coder",
		Model: orchestrator.ModelBinding{Preset: "flash-model"},
	}
	b := rt.resolveSubAgentBackend(context.Background(), "pro-model", emp, modelgateway.Req{Task: "spawn_task"})
	if b.MaxContext() != 8192 {
		t.Fatalf("显式 override pro-model 应优先于员工绑定：MaxContext = %d, want 8192", b.MaxContext())
	}
}

// TestResolveSubAgentBackend_OverrideInvalidFallsBack 无效 override 降级员工绑定。
func TestResolveSubAgentBackend_OverrideInvalidFallsBack(t *testing.T) {
	rt := newModelSelectRuntime(t)
	emp := &orchestrator.Employee{
		Name:  "coder",
		Model: orchestrator.ModelBinding{Preset: "flash-model"},
	}
	b := rt.resolveSubAgentBackend(context.Background(), "no-such-model", emp, modelgateway.Req{Task: "spawn_task"})
	if b == nil {
		t.Fatal("无效 override 不应让解析失败")
	}
	if b.MaxContext() != 4096 {
		t.Fatalf("无效 override 应降级到员工绑定 flash-model（MaxContext=4096），got %d", b.MaxContext())
	}
}

// TestResolveSubAgentBackend_DefaultNoEmployee 无员工/无绑定 → 默认后端（零配置兜底）。
func TestResolveSubAgentBackend_DefaultNoEmployee(t *testing.T) {
	rt := newModelSelectRuntime(t)
	b := rt.resolveSubAgentBackend(context.Background(), "", nil, modelgateway.Req{Task: "spawn_task"})
	if b == nil {
		t.Fatal("兜底后端不应为 nil")
	}
	if _, isSpawn := b.(*spawnBackend); !isSpawn {
		t.Fatalf("零配置下应返回默认后端 rt.Backend，got %T", b)
	}
}

// TestResolveSubAgentBackend_PresetCache 缓存生效：两次解析同预设复用实例。
func TestResolveSubAgentBackend_PresetCache(t *testing.T) {
	rt := newModelSelectRuntime(t)
	b1, err := rt.buildBackendForPreset("pro-model")
	if err != nil {
		t.Fatal(err)
	}
	b2, err := rt.buildBackendForPreset("pro-model")
	if err != nil {
		t.Fatal(err)
	}
	if b1 != b2 {
		t.Fatal("同预设二次解析应复用缓存实例")
	}
	// 失效后重建
	rt.InvalidateModelCache()
	b3, err := rt.buildBackendForPreset("pro-model")
	if err != nil {
		t.Fatal(err)
	}
	if b3 == b1 {
		t.Fatal("缓存失效后应重建实例")
	}
}

// TestSpawnTask_ModelOverrideEndToEnd 端到端：spawn_task 传 model 参数 →
// 子 Agent 用该预设的后端执行（自选模型全链路）。
// 注：预设实例缓存按名注入 mock 后端（pro-model 预设指向真实 HTTP 端点，
// 测试环境无服务——注入仅验证"override 预设被真实选中"这一链路）。
func TestSpawnTask_ModelOverrideEndToEnd(t *testing.T) {
	rt := newModelSelectRuntime(t)
	mockBackend := &spawnBackend{response: "已实现登录功能。"}
	presetCacheMu.Lock()
	rt.presetCache = map[string]presetCacheEntry{"pro-model": {backend: mockBackend}}
	presetCacheMu.Unlock()
	rt.overseerMu.Lock()
	rt.currentOverseer = overseer.New("test", map[string]any{}, types.Score(90), nil)
	rt.overseerMu.Unlock()

	result, err := rt.SpawnTask(context.Background(), "main", tools.TaskSpec{
		Task:  "实现函数",
		Model: "pro-model",
	})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	if result == "" {
		t.Fatal("结果不应为空")
	}
	// 链路证据：子 Agent 用的正是 override 预设注入的 mock 实例（ModelID 可查）
	if got := rt.resolveSubAgentBackend(context.Background(), "pro-model", nil, modelgateway.Req{}); got != model.Backend(mockBackend) {
		t.Fatalf("override 预设应解析为注入的 mock 实例，got %T", got)
	}
}

// TestEmployeeSummary_ShowsModelBinding：员工摘要展示模型绑定（主+备选链）。
func TestEmployeeSummary_ShowsModelBinding(t *testing.T) {
	rt := newModelSelectRuntime(t)
	// 种子 coder 员工绑定 flash-model（内置模板）
	if s := rt.EmployeeSummary("coder"); !strings.Contains(s, "flash-model") {
		t.Fatalf("员工摘要应含模型绑定 flash-model，got %q", s)
	}
	// 带 fallbacks 的档案显示备选链
	empDir := rt.Config.Project.Root + "/.contextloom/employees/reviewer"
	writeFile(t, empDir+"/employee.yaml", `name: reviewer
role: 评审
model:
  preset: pro-model
  fallbacks: ["flash-model", "mock"]
`)
	rt.InvalidateModelCache()
	rt.employees = nil // 清员工缓存强制重读
	rt.employeeOnce = sync.Once{}
	if s := rt.EmployeeSummary("reviewer"); !strings.Contains(s, "pro-model（备选: flash-model→mock）") {
		t.Fatalf("摘要应展示多来源绑定链，got %q", s)
	}
}
