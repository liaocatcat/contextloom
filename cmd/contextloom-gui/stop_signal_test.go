//go:build !windows

package main

import (
	"os/exec"
	"runtime"
	"testing"
	"time"
)

// TestBackendMgr_StopReapsProcess：stop() 对真实 backend 进程——
// SIGTERM 后等待退出（done 关闭）且退出状态被回收（无僵尸）。
// 历史 bug：startBackend 成功路径从不 Wait → 每次切换/退出残留僵尸；
// stop() 只发信号不等待 → 卡死 backend 永不清理。
func TestBackendMgr_StopReapsProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	addr, pid, done, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	mgr := newBackendMgr()
	mgr.set(addr, pid, workDir, true, done)

	// stop() 应同步完成（backend 优雅退出毫秒级），且进程退出、回收完毕
	start := time.Now()
	mgr.stop()
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Errorf("stop() 耗时 %v 超过预期（backend 应快速退出）", elapsed)
	}
	// 进程必须已退出（信号已生效）
	if processAlive(pid) {
		t.Fatalf("backend 进程 %d 仍存活（stop() 未清理）", pid)
	}
	// done 已关闭（Wait goroutine 完成回收）
	select {
	case <-done:
	default:
		t.Fatal("done 未关闭：进程退出状态未被回收（僵尸风险）")
	}
	t.Logf("✅ stop() 3.2s 内完成清理（实际 %v）", elapsed)
}

// TestBackendMgr_StopGracefulTimeoutKill：backend 不响应 SIGTERM 时，
// stop() 在超时后强制 Kill，绝不允许进程残留。
func TestBackendMgr_StopGracefulTimeoutKill(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同")
	}
	// 启动一个不处理 SIGTERM 的进程（sh 循环 + trap 忽略 TERM，模拟卡死
	// backend——实测 macOS /bin/sh 下 while 循环版本可靠忽略 TERM）
	cmd := exec.Command("sh", "-c", `trap '' TERM; while :; do sleep 1; done`)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	// 等待 trap 注册完成：Start 后立即发 SIGTERM 会命中默认处理
	// （sh 尚未执行到 trap），进程提前退出 → 测不到超时强杀路径。
	time.Sleep(500 * time.Millisecond)

	mgr := newBackendMgr()
	mgr.set("http://127.0.0.1:9/?token=x", cmd.Process.Pid, t.TempDir(), true, done)

	start := time.Now()
	mgr.stop() // 应等待 3s 超时后 Kill，总计 <6s
	elapsed := time.Since(start)
	if elapsed < 2*time.Second {
		t.Errorf("stop() 应走 3s 超时强杀路径，实际仅 %v（进程可能提前退出了）", elapsed)
	}
	if elapsed > 6*time.Second {
		t.Errorf("stop() 超时强杀路径耗时 %v 超过 6s", elapsed)
	}
	if processAlive(cmd.Process.Pid) {
		t.Fatalf("忽略 SIGTERM 的进程 %d 仍存活（超时强杀未生效）", cmd.Process.Pid)
	}
	t.Logf("✅ 超时强杀生效（%v 内完成，进程已终止）", elapsed)
}
