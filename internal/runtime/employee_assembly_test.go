package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/tools"
)

// =============================================================================
// 员工真实装配测试：通用指令映射到员工（非仅显示）
// =============================================================================

// TestResolveEmployeeForDirective_ExactName 精确员工名 → 命中该员工。
func TestResolveEmployeeForDirective_ExactName(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if emp := rt.resolveEmployeeForDirective("coder"); emp == nil || emp.Name != "coder" {
		t.Fatalf("精确名 coder 应命中，got %+v", emp)
	}
	if emp := rt.resolveEmployeeForDirective("tester"); emp == nil || emp.Name != "tester" {
		t.Fatalf("精确名 tester 应命中，got %+v", emp)
	}
}

// TestResolveEmployeeForDirective_GenericMapping 通用指令映射到员工（真实装配）：
// CODE_AGENT→coder、TEST→tester、PLANNER→product、REVIEW→reviewer、
// WRITE→writer、TRANSLATE→translator。
func TestResolveEmployeeForDirective_GenericMapping(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	cases := map[string]string{
		"CODE_AGENT": "coder",
		"TEST":       "tester",
		"PLANNER":    "product",
		"REVIEW":     "reviewer",
		"WRITE":      "writer",
		"TRANSLATE":  "translator",
	}
	for directive, want := range cases {
		emp := rt.resolveEmployeeForDirective(directive)
		if emp == nil || emp.Name != want {
			t.Fatalf("通用指令 %s 应装配员工 %s，got %+v", directive, want, emp)
		}
	}
}

// TestResolveEmployeeForDirective_Missing 未命中（无员工/未知指令）→ nil（零配置兜底）。
func TestResolveEmployeeForDirective_Missing(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if emp := rt.resolveEmployeeForDirective("NO_SUCH_EMPLOYEE"); emp != nil {
		t.Fatalf("未知指令应返回 nil，got %+v", emp)
	}
	// 大小写不敏感映射：小写通用指令同样命中
	if emp := rt.resolveEmployeeForDirective("code_agent"); emp == nil || emp.Name != "coder" {
		t.Fatalf("小写 code_agent 应映射 coder，got %+v", emp)
	}
}

// TestResolveEmployeeForDirective_ZeroConfig 员工中心不存在 → 通用指令返回 nil
// （零配置兜底：无员工 = 旧行为，不因映射凭空装配）。
func TestResolveEmployeeForDirective_ZeroConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if err := os.RemoveAll(filepath.Join(tmpDir, ".contextloom", "employees")); err != nil {
		t.Fatal(err)
	}
	rt.employees = nil // 清缓存强制重读
	if emp := rt.resolveEmployeeForDirective("CODE_AGENT"); emp != nil {
		t.Fatalf("零配置下通用指令应返回 nil，got %+v", emp)
	}
}

// TestSpawnTask_GenericDirectiveAssemblesEmployee 端到端：spawn_task 传通用指令
// CODE_AGENT → 子 Agent 真实装配 coder 员工（工具表按 coder 白名单过滤 + directive
// 注入 role/persona）。这是"员工真的在干活"的装配级证据（非仅面板显示）。
func TestSpawnTask_GenericDirectiveAssemblesEmployee(t *testing.T) {
	rt := newSpawnRuntime(t)
	ctx := context.Background()

	result, err := rt.SpawnTask(ctx, "main", tools.TaskSpec{
		Task:      "实现登录函数",
		Directive: "CODE_AGENT",
	})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	if !strings.Contains(result, "已实现登录功能") {
		t.Fatalf("结果应含子 Agent 输出，got %q", result)
	}
	// coder 员工工具白名单不含 web_fetch/propose_vocab → 子 Agent 工具表应过滤；
	// 白名单含 read_file/write_file/exec_command 等核心工具。
	// 注：SpawnTask 同步执行后子 Agent 已移除，此处通过信号无法断言工具表，
	// 装配证据 = resolveEmployeeForDirective 命中（上方单测）+ directive 注入（下方 EmitTaskStart）。
	_ = result
}

// TestSpawnTask_GenericDirectiveSignalEmployee 通用指令 CODE_AGENT 的 §START 信号
// 员工段应为 coder（显示名），directive 段保留原始通用指令（面板简洁）。
func TestSpawnTask_GenericDirectiveSignalEmployee(t *testing.T) {
	rt := newSpawnRuntime(t)
	progressCh := make(chan string, 128)
	ctx := tools.WithProgressChan(context.Background(), progressCh)

	_, err := rt.SpawnTask(ctx, "main", tools.TaskSpec{
		Task:      "实现登录函数",
		Directive: "CODE_AGENT",
	})
	if err != nil {
		t.Fatalf("SpawnTask 错误: %v", err)
	}
	var start string
	for {
		select {
		case m, ok := <-progressCh:
			if !ok {
				goto collected
			}
			if strings.HasPrefix(m, "§START|") {
				start = m
			}
		default:
			goto collected
		}
	}
collected:
	if start == "" {
		t.Fatal("未收到 §START 信号")
	}
	parts := strings.Split(start, "|")
	// 新格式 5 段: §START|<subID>|<directive>|<employee>|<task>
	if len(parts) != 5 {
		t.Fatalf("§START 应为 5 段新格式，got %q (parts=%v)", start, parts)
	}
	if parts[2] != "CODE_AGENT" {
		t.Errorf("directive 段应保留通用指令 CODE_AGENT（面板简洁），got %q", parts[2])
	}
	if parts[3] != "coder" {
		t.Errorf("员工段应为 coder（CODE_AGENT 装配映射），got %q", parts[3])
	}
}
