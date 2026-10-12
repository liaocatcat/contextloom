package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestExecCommand_Timeout_Reliable 验证 exec_command 超时可靠返回：
// 命令 spawn 一个 sleep 子进程（shell 派生的"孙子进程"），超时后必须
// 在限定时间内返回（修复前会因孙子进程持有管道而永久挂起）。
func TestExecCommand_Timeout_Reliable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix 进程组语义测试，Windows 跳过")
	}
	ctx := context.Background()
	// sh -c 'sleep 60 & sleep 60' ：shell 派生一个后台 sleep（孙子进程）
	// 再前台 sleep，验证超时后整棵进程树被杀、工具能立即返回
	res, err := ExecCommand(ctx, map[string]any{
		"command":    "sleep 60 & sleep 60",
		"timeout_ms": float64(2000),
	})
	if err != nil {
		t.Fatalf("ExecCommand 不应返回 error: %v", err)
	}
	m := res.(map[string]any)
	stdout := m["stdout"].(string)
	code := m["exit_code"].(int)

	if code != 124 {
		t.Errorf("期望超时退出码 124，实际 %d；输出: %s", code, stdout)
	}
	if !strings.Contains(stdout, "超时") {
		t.Errorf("输出应包含超时提示，实际: %s", stdout)
	}
}

// TestExecCommand_Timeout_QuickReturn 验证超时在限定时间内返回（不挂起）。
func TestExecCommand_Timeout_QuickReturn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix 语义测试，Windows 跳过")
	}
	start := time.Now()
	res, err := ExecCommand(context.Background(), map[string]any{
		"command":    "sleep 120",
		"timeout_ms": float64(1500),
	})
	if err != nil {
		t.Fatalf("ExecCommand 不应返回 error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 10*time.Second {
		t.Fatalf("超时未可靠返回：耗时 %v（应 ≤10s）", elapsed)
	}
	m := res.(map[string]any)
	if m["exit_code"].(int) != 124 {
		t.Errorf("期望 124，实际 %v", m["exit_code"])
	}
}

// TestExecCommand_DefaultTimeout 验证默认超时 20s 生效（不依赖模型传 timeout_ms）。
func TestExecCommand_DefaultTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix 语义测试，Windows 跳过")
	}
	start := time.Now()
	res, err := ExecCommand(context.Background(), map[string]any{
		"command": "sleep 60",
	})
	if err != nil {
		t.Fatalf("ExecCommand 不应返回 error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 30*time.Second {
		t.Fatalf("默认超时未生效：耗时 %v", elapsed)
	}
	if res.(map[string]any)["exit_code"].(int) != 124 {
		t.Errorf("期望 124，实际 %v", res.(map[string]any)["exit_code"])
	}
}

// TestRegistry_UnifiedTimeout 验证 Registry.Execute 统一超时兜底：
// 注册一个永不返回的工具，必须在超时后被强制终止并返回明确错误。
func TestRegistry_UnifiedTimeout(t *testing.T) {
	old := defaultToolTimeout
	defaultToolTimeout = 500 * time.Millisecond
	defer func() { defaultToolTimeout = old }()

	r := NewRegistry()
	// 覆盖 exec_command 为永不返回的实现
	r.tools["exec_command"] = func(ctx context.Context, params map[string]any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	start := time.Now()
	res, err := r.Execute(context.Background(), types.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: types.FunctionCall{
			Name:      "exec_command",
			Arguments: `{"command":"sleep 999"}`,
		},
	})
	if err != nil {
		t.Fatalf("Execute 不应返回 error: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 5*time.Second {
		t.Fatalf("统一超时应快速触发（覆盖后 500ms），实际 %v", elapsed)
	}
	if res.Error == "" || !strings.Contains(res.Error, "超时") {
		t.Fatalf("期望超时错误信息，实际: %q", res.Error)
	}
}

// TestExecCommand_Normal_StillWorks 正常命令不受影响。
func TestExecCommand_Normal_StillWorks(t *testing.T) {
	res, err := ExecCommand(context.Background(), map[string]any{
		"command": "echo hello",
	})
	if err != nil {
		t.Fatalf("正常命令失败: %v", err)
	}
	m := res.(map[string]any)
	if !strings.Contains(m["stdout"].(string), "hello") {
		t.Errorf("期望输出 hello，实际: %v", m["stdout"])
	}
	if m["exit_code"].(int) != 0 {
		t.Errorf("期望退出码 0，实际 %v", m["exit_code"])
	}
}

// TestToolResultJSON 验证超时错误信息能正确序列化为模型可见的 ToolResult。
func TestToolResultJSON(t *testing.T) {
	tr := types.ToolResult{CallID: "c1", Name: "exec_command", Error: "工具执行超时（2m0s 后强制终止）"}
	b, err := json.Marshal(tr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "超时") {
		t.Errorf("JSON 应包含超时信息: %s", b)
	}
}

// TestRunTests_NoOrphan_TempIsolation 验证 go test 失败/超时时：
//  1. 不留孤儿 runtime.test 进程（进程树连坐杀）；
//  2. go-build 中间产物落在工具私有 GOTMPDIR，不污染系统 $TMPDIR/go-build*。
//
// 修复前：RunTests 超时只杀 go 父进程 → 孤儿 runtime.test（PPID=1）继续
// 跑完 10 分钟 test.timeout，期间持续在系统 $TMPDIR 重建 go-build 随机目录，
// 必须 rm -rf $TMPDIR/go-build* 才能清理——本测试锁定该回归。
func TestRunTests_NoOrphan_TempIsolation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix 进程组/孤儿语义测试，Windows 跳过")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	before := listGoBuildDirs(t)
	beforeOrphans := countRuntimeTestProcesses()

	// 故意跑一个超时测试（timeout 3s，go test 自身 10m 上限内会先被 ctx 杀掉）
	res, err := RunTests(ctx, map[string]any{
		"test_target": "./internal/tools",
	})
	if err != nil {
		t.Fatalf("RunTests 不应返回 error: %v", err)
	}
	m := res.(map[string]any)
	code := m["exit_code"].(int)
	if code != 124 && code != 1 {
		t.Errorf("期望超时(124)或失败(1)退出码，实际 %v；输出: %v", code, m["output"])
	}

	after := listGoBuildDirs(t)
	// 系统 $TMPDIR/go-build* 不应新增（即使新增也极少——新 go-build 缓存
	// 在 GOCACHE，run 临时目录在私有 GOTMPDIR）
	if len(after) > len(before)+1 {
		t.Errorf("系统 TMPDIR/go-build* 残留异常：before=%d after=%d（应基本持平）", len(before), len(after))
	}

	// 核心断言：结束后无孤儿 runtime.test / go test 残留（进程树已连坐杀）
	// 等 1 秒让 kill 传播完成
	time.Sleep(1 * time.Second)
	afterOrphans := countRuntimeTestProcesses()
	if afterOrphans > beforeOrphans {
		t.Errorf("存在孤儿 runtime.test 残留：before=%d after=%d（应 ≤ before）", beforeOrphans, afterOrphans)
	}
}

// countRuntimeTestProcesses 统计系统 runtime.test 测试二进制进程数。
func countRuntimeTestProcesses() int {
	out, err := exec.Command("pgrep", "-f", "runtime.test").CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return 0 // pgrep 无匹配
		}
		return -1
	}
	return len(strings.Fields(string(out)))
}

// listGoBuildDirs 列出系统 $TMPDIR 下 go-build* 随机目录。
func listGoBuildDirs(t *testing.T) []string {
	t.Helper()
	pattern := filepath.Join(os.TempDir(), "go-build*")
	dirs, _ := filepath.Glob(pattern)
	return dirs
}
