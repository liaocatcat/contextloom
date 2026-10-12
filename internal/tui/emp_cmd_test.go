package tui

import (
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
)

// newEmpRuntime 造一个真实带员工中心种子的 Runtime（复用 goal 测试的构造思路：
// runtime.New 会自动 seed .contextloom/employees/ 内置员工档案，/emp 查询/校验分支
// 可走真实员工中心；指派成功路径不触发模型调用（AssignEmployee 已异步）。
func newEmpRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-tui-emp-*")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plugins"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "task"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
	}
	rt, err := runtime.New(cfg)
	if err != nil {
		t.Fatalf("runtime.New: %v", err)
	}
	return rt
}

// TestCmd_Emp_NoRuntime 验证 /emp 在未连接运行时下优雅提示。
func TestCmd_Emp_NoRuntime(t *testing.T) {
	cr := NewCommandRegistry(nil)
	result, _ := cr.Execute("/emp")
	if !strings.Contains(result, "未连接") {
		t.Errorf("expected 未连接运行时, got %q", result)
	}
}

// TestCmd_Emp_Registered 验证 /emp 已注册进命令表（可被补全/帮助发现）。
func TestCmd_Emp_Registered(t *testing.T) {
	cr := NewCommandRegistry(nil)
	if !cr.IsCommand("/emp") {
		t.Errorf("/emp 应已注册，命令表: %v", cr.Commands())
	}
}

// TestCmd_Emp_EmptyRuntime 验证带种子的真实 Runtime 无参 /emp 返回员工清单（种子已落盘），
// 不 panic。
func TestCmd_Emp_EmptyRuntime(t *testing.T) {
	rt := newEmpRuntime(t)
	cr := NewCommandRegistry(rt)
	result, _ := cr.Execute("/emp")
	if !strings.Contains(result, "可用员工") {
		t.Errorf("expected 员工清单, got %q", result)
	}
}

// TestCmd_Emp_Assign_Validation 验证 /emp <名> <任务> 指派分支（P2-3 缺口补齐）：
//   - 空 Runtime → 未连接运行时（不 panic，不走指派）；
//   - 指派不存在的员工 → 报错且文案含"不存在"（同步校验路径）；
//   - /emp <name> → 档案摘要分支；
//   - 空任务校验（AssignEmployee 拒绝空任务）由 runtime 层单测
//     TestAssignEmployee_RejectsUnknown 覆盖（TUI Execute 经 strings.Fields
//     拆参会吸收尾部空格，空任务分支在 TUI 层不可达，只能靠 runtime 层校验兜底）。
//
// 注：真实指派（存在员工 + 非空任务）会触发 SpawnTask 模型执行，TUI 单测
// 不落模型调用；该成功路径由 runtime 包 AssignEmployee 单测覆盖。
func TestCmd_Emp_Assign_Validation(t *testing.T) {
	// 空 Runtime：未连接 → 优雅提示
	crNil := NewCommandRegistry(nil)
	result, _ := crNil.Execute("/emp coder 写个函数")
	if !strings.Contains(result, "未连接") {
		t.Errorf("空 Runtime 指派应提示未连接，got %q", result)
	}
	// 真实 Runtime（种子员工已落盘）：指派不存在员工 → 报错
	rt := newEmpRuntime(t)
	cr := NewCommandRegistry(rt)
	result, _ = cr.Execute("/emp ghost 写个函数")
	if !strings.Contains(result, "不存在") {
		t.Errorf("指派不存在员工应报错，got %q", result)
	}
	// 无参 → 员工清单（种子员工已落盘）
	result, _ = cr.Execute("/emp")
	if !strings.Contains(result, "可用员工") {
		t.Errorf("无参应列出员工清单，got %q", result)
	}
	// /emp <name> → 档案摘要
	result, _ = cr.Execute("/emp coder")
	if !strings.Contains(result, "coder") {
		t.Errorf("看档案应含员工名，got %q", result)
	}
}
