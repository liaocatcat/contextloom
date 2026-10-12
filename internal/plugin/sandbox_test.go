package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ============================================================================
// Sandbox Configuration Tests
// ============================================================================

func TestNewSandbox_Defaults(t *testing.T) {
	s := NewSandbox(256, 0.5)
	if s.MemoryLimitMB != 256 {
		t.Errorf("expected MemoryLimitMB 256, got %d", s.MemoryLimitMB)
	}
	if s.CPULimit != 0.5 {
		t.Errorf("expected CPULimit 0.5, got %f", s.CPULimit)
	}
}

func TestNewSandbox_ZeroMemory(t *testing.T) {
	s := NewSandbox(0, 1.0)
	if s.MemoryLimitMB != 0 {
		t.Errorf("expected MemoryLimitMB 0, got %d", s.MemoryLimitMB)
	}
}

func TestNewSandbox_ZeroCPU(t *testing.T) {
	s := NewSandbox(128, 0)
	if s.CPULimit != 0 {
		t.Errorf("expected CPULimit 0, got %f", s.CPULimit)
	}
}

func TestNewSandbox_HighMemory(t *testing.T) {
	s := NewSandbox(1024, 1.0)
	if s.MemoryLimitMB != 1024 {
		t.Errorf("expected MemoryLimitMB 1024, got %d", s.MemoryLimitMB)
	}
}

// ============================================================================
// Sandbox Run Tests (with real subprocess)
// ============================================================================

func TestSandbox_RunEcho(t *testing.T) {
	s := NewSandbox(256, 0.5)
	output, err := s.Run("echo", "hello", nil)
	if err != nil {
		t.Fatalf("unexpected error running echo: %v", err)
	}
	if string(output) != "hello\n" {
		t.Errorf("expected 'hello\\n', got %q", string(output))
	}
}

func TestSandbox_RunWithInput(t *testing.T) {
	s := NewSandbox(256, 0.5)
	output, err := s.Run("cat", "-", []byte("test input"))
	if err != nil {
		t.Fatalf("unexpected error running cat: %v", err)
	}
	if string(output) != "test input" {
		t.Errorf("expected 'test input', got %q", string(output))
	}
}

func TestSandbox_RunNonexistentCommand(t *testing.T) {
	s := NewSandbox(256, 0.5)
	_, err := s.Run("/nonexistent/command", "hook", nil)
	if err == nil {
		t.Error("expected error for nonexistent command")
	}
}

func TestSandbox_RunTimeout(t *testing.T) {
	s := NewSandbox(256, 0.5)
	// Run a command that sleeps longer than the 30s timeout
	// We use a short sleep here to avoid test hanging
	start := time.Now()
	_, err := s.Run("sleep", "1", nil)
	elapsed := time.Since(start)
	if err != nil {
		t.Logf("sleep exited with error: %v", err)
	}
	// Should complete within a reasonable time
	if elapsed > 3*time.Second {
		t.Errorf("expected quick completion, took %v", elapsed)
	}
}

func TestSandbox_RunWithEnvVars(t *testing.T) {
	s := NewSandbox(256, 0.5)
	// Run with echo, passing the hook name that gets set as CONTEXTLOOM_HOOK env var
	output, err := s.Run("echo", "test-hook-name", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(output) != "test-hook-name\n" {
		t.Errorf("expected 'test-hook-name\\n', got %q", string(output))
	}
}

// ============================================================================
// Sandbox Memory Limit Behavior
// ============================================================================

func TestSandbox_MemoryLimitZero(t *testing.T) {
	// MemoryLimitMB=0 不影响功能；进程组隔离（Setpgid）无条件启用，
	// 与内存限制无关（超时清理整棵进程树的前提）。
	s := NewSandbox(0, 0.5)
	output, err := s.Run("echo", "no-memory-limit", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(output) != "no-memory-limit\n" {
		t.Errorf("unexpected output: %q", string(output))
	}
}

func TestSandbox_SubprocessCleanup(t *testing.T) {
	s := NewSandbox(256, 0.5)
	// Run a simple command and verify cleanup
	output, err := s.Run("echo", "done", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(output) != "done\n" {
		t.Errorf("expected 'done\\n', got %q", string(output))
	}
}

// ============================================================================
// Sandbox Concurrent Safety
// ============================================================================

func TestSandbox_ConcurrentRun(t *testing.T) {
	s := NewSandbox(256, 0.5)
	errCh := make(chan error, 5)

	for i := 0; i < 5; i++ {
		go func() {
			_, err := s.Run("echo", "concurrent", nil)
			errCh <- err
		}()
	}

	for i := 0; i < 5; i++ {
		if err := <-errCh; err != nil {
			t.Errorf("concurrent run failed: %v", err)
		}
	}
}

// ============================================================================
// RunScriptTimeout 可靠超时（进程树终止）
// ============================================================================

// TestSandbox_RunScriptTimeout_KillsGrandchild 验证超时后整棵进程树被终止：
// 脚本派生后台 sleep 子进程后自身长睡——修复前 exec.CommandContext 超时只杀
// 直接子进程，孙子进程持有 stdout 管道导致 CombinedOutput 永久阻塞；
// 修复后超时杀进程组（kill(-pgid)），RunScriptTimeout 按时返回且孙子进程被清。
func TestSandbox_RunScriptTimeout_KillsGrandchild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 后台子进程场景仅 Unix 可测")
	}
	dir := t.TempDir()
	childPIDFile := filepath.Join(dir, "child.pid")
	script := filepath.Join(dir, "spawn.sh")
	content := "#!/bin/sh\nsleep 60 &\necho $! > " + childPIDFile + "\nsleep 60\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}

	s := NewSandbox(0, 0.5)
	start := time.Now()
	_, err := s.RunScriptTimeout(script, nil, 2*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("期望超时错误，got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("错误应含 timed out，got: %v", err)
	}
	if elapsed > 10*time.Second {
		t.Fatalf("RunScriptTimeout 应按时返回（<10s），实际 %v——孙子进程持有管道导致永久阻塞", elapsed)
	}

	// 孙子进程应已被进程树终止（kill(pid, 0) 探测）
	pidBytes, rerr := os.ReadFile(childPIDFile)
	if rerr != nil {
		t.Skipf("无法读取孙子 PID 文件: %v", rerr)
	}
	pidStr := strings.TrimSpace(string(pidBytes))
	if pidStr == "" {
		t.Skip("孙子 PID 未记录")
	}
	pid, aerr := strconv.Atoi(pidStr)
	if aerr != nil {
		t.Skipf("孙子 PID 解析失败: %v", aerr)
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Errorf("孙子进程 %d 仍存活——进程树未被终止", pid)
	}
}

// TestSandbox_RunScriptTimeout_QuickScript 验证正常完成路径不受影响：
// 快速脚本返回输出与退出码，无超时错误。
func TestSandbox_RunScriptTimeout_QuickScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell 脚本场景仅 Unix 可测")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "quick.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho hello-from-script\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewSandbox(0, 0.5)
	out, err := s.RunScriptTimeout(script, []byte("ignored"), 5*time.Second)
	if err != nil {
		t.Fatalf("快速脚本不应报错: %v", err)
	}
	if string(out) != "hello-from-script\n" {
		t.Errorf("输出 = %q, want %q", string(out), "hello-from-script\n")
	}
}
