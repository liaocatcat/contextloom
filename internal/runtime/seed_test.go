// seed_test.go —— 内置种子数据落地测试：三类目录生成、内容可解析、二次幂等、失败静默容忍。
package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/market"
	"github.com/contextloom/contextloom-agent/internal/orchestrator"
	"github.com/contextloom/contextloom-agent/internal/scenes"
	"github.com/contextloom/contextloom-agent/internal/toolchain"
)

// TestSeedBuiltins_CreatesAllDirs 验证首次调用把四类种子全部落地到 .contextloom/ 下，
// 且落地的 YAML/SKILL.md 能被对应包 Load/识别（Marshal 往返正确性）。
func TestSeedBuiltins_CreatesAllDirs(t *testing.T) {
	tmp := t.TempDir()
	seedBuiltins(tmp)
	dm := filepath.Join(tmp, ".contextloom")

	// 员工：6 份档案，可被 orchestrator.Load 解析
	if got := len(orchestrator.ExampleEmployees()); got != 6 {
		t.Fatalf("内置员工应 6 名，实际 %d（种子断言基线变化，请同步更新）", got)
	}
	for _, e := range orchestrator.ExampleEmployees() {
		p := filepath.Join(dm, seedEmployeesDir, e.Name, orchestrator.EmployeeFileName)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("员工档案未落地 %s: %v", p, err)
		}
		loaded, err := orchestrator.Load(filepath.Join(dm, seedEmployeesDir, e.Name))
		if err != nil {
			t.Fatalf("员工档案 %s 无法解析: %v", p, err)
		}
		if loaded == nil || loaded.Name != e.Name {
			t.Fatalf("员工档案 %s 解析结果异常: %+v", p, loaded)
		}
	}

	// 场景：4 份，可被 scenes.Load 解析
	if got := len(scenes.DefaultBuiltinScenes()); got != 4 {
		t.Fatalf("内置场景应 4 个，实际 %d（种子断言基线变化，请同步更新）", got)
	}
	for _, s := range scenes.DefaultBuiltinScenes() {
		p := filepath.Join(dm, seedScenesDir, s.Name, sceneFileName)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("场景未落地 %s: %v", p, err)
		}
		loaded, err := scenes.Load(filepath.Join(dm, seedScenesDir, s.Name))
		if err != nil {
			t.Fatalf("场景 %s 无法解析: %v", p, err)
		}
		if loaded == nil || loaded.Name != s.Name {
			t.Fatalf("场景 %s 解析结果异常: %+v", p, loaded)
		}
	}

	// 工具链：5 条（§4.3 内置 5 条链：code-review / weekly-report / code-test-review / pr-review / bug-fix），
	// 可被 toolchain.Load 解析（覆盖 Toolchain.Marshal）
	if got := len(toolchain.ExampleChains()); got != 5 {
		t.Fatalf("内置工具链应 5 条，实际 %d（种子断言基线变化，请同步更新）", got)
	}
	for name := range toolchain.ExampleChains() {
		p := filepath.Join(dm, seedChainsDir, name, toolchainFileName)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("工具链未落地 %s: %v", p, err)
		}
		loaded, err := toolchain.Load(filepath.Join(dm, seedChainsDir), name)
		if err != nil {
			t.Fatalf("工具链 %s 无法解析: %v", p, err)
		}
		if loaded == nil || loaded.Name != name {
			t.Fatalf("工具链 %s 解析结果异常: %+v", p, loaded)
		}
	}

	// 技能：全部 builtin catalog 落地为 <name>/SKILL.md
	builtinCount := 0
	for _, it := range market.New().Search("") {
		if it.Source == "builtin" {
			builtinCount++
		}
	}
	if builtinCount == 0 {
		t.Fatal("market builtin catalog 为空（基线异常）")
	}
	installed, err := market.ListInstalled(filepath.Join(dm, seedSkillsDir))
	if err != nil {
		t.Fatalf("列出技能失败: %v", err)
	}
	if len(installed) != builtinCount {
		t.Fatalf("内置技能应 %d 个，实际 %d: %v", builtinCount, len(installed), installed)
	}
}

// TestSeedBuiltins_Idempotent 验证二次调用幂等：已存在文件（用户手改档案）不被覆盖，
// 且重复调用不产生额外条目。
func TestSeedBuiltins_Idempotent(t *testing.T) {
	tmp := t.TempDir()
	seedBuiltins(tmp)
	dm := filepath.Join(tmp, ".contextloom")

	// 模拟用户/GUI 手改的员工档案
	p := filepath.Join(dm, seedEmployeesDir, "coder", orchestrator.EmployeeFileName)
	custom := []byte("# 用户手改档案（模拟 GUI/人工编辑）\nname: coder\ndisplay: 手改版\n")
	if err := os.WriteFile(p, custom, 0o644); err != nil {
		t.Fatal(err)
	}

	seedBuiltins(tmp) // 二次调用

	got, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatalf("二次调用覆盖了已有档案：\n got=%q\nwant=%q", got, custom)
	}

	// 目录条目数不变（不产生重复/多余条目）
	entries, err := os.ReadDir(filepath.Join(dm, seedEmployeesDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(orchestrator.ExampleEmployees()) {
		t.Fatalf("员工目录数 %d，期望 %d", len(entries), len(orchestrator.ExampleEmployees()))
	}
}

// TestSeedBuiltins_FailureTolerance 验证失败路径静默容忍：projRoot 不可写
// （projRoot 为普通文件，无法在其下建目录）时 seedBuiltins 不 panic、不返回错误。
func TestSeedBuiltins_FailureTolerance(t *testing.T) {
	tmp := t.TempDir()
	blocker := filepath.Join(tmp, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Run("non-dir projRoot", func(t *testing.T) {
		seedBuiltins(blocker) // 不应 panic（内部打印 stderr 并跳过）
	})
	t.Run("readonly parent", func(t *testing.T) {
		if os.Getuid() == 0 {
			t.Skip("root 不受只读权限约束，跳过")
		}
		ro := filepath.Join(tmp, "ro")
		if err := os.MkdirAll(ro, 0o555); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(ro, 0o755)
		seedBuiltins(filepath.Join(ro, "proj")) // MkdirAll 失败路径，不应 panic
	})
}
