//go:build !windows

// 该测试经 buildSmokeBin 拉起后端二进制（Unix 命名 contextloom，无 .exe 后缀），
// 辅助函数 buildSmokeBin 位于 smoke_test.go（!windows 标签）；Windows 下整文件排除，
// 避免 Windows 测试包编译失败（vet: undefined: buildSmokeBin）。
package main

import (
	"runtime"
	"testing"
)

// TestProjects_RemoveCurrent_ConstantekCheck 守卫 removeProject 不误杀常驻池：
// 移除【非当前】项目时，其 backend 进程应被停止；【当前+其他常驻】项目保留。
// 重点：removeProject 调用 stopBackend 后，覆盖 stopBackend 对 pid=0 等
// 伪 backend 的防御（kill(0) 会误杀当前进程组——历史崩溃隐患）。
func TestNeverKillZeroPIDBackend(t *testing.T) {
	mgr := newBackendMgr()
	// 登记一个 pid=0 的假 backend（模拟测试 fake：不拉起真实进程）。
	// removeProject 触发 stopBackend 时，若 kill(0) 未防御会误杀当前测试进程组。
	mgr.set("http://127.0.0.1:9/?token=x", 0, "/proj/a", true, nil)
	mgr.addProject("/proj/a")
	mgr.addProject("/proj/b")

	// 移除非当前 /proj/b：应正常完成，不 panic、不误杀任何进程。
	mgr.removeProject("/proj/b")
	if got := len(mgr.listProjects()); got != 1 {
		t.Fatalf("移除后应剩 1 个项目，got %d", got)
	}
	// 再移除当前目录（pid=0 防御路径）：同样应正常完成。
	mgr.removeProject("/proj/a")
	if got := len(mgr.listProjects()); got != 0 {
		t.Fatalf("移除全部后应 0 个项目，got %d", got)
	}
}

// TestSwitchProject_KeepsBackendAlive 验证本修复核心语义：切换项目【不杀】
// 旧 backend 进程（常驻保留），切回时同一 PID 直接复用——右侧会话与在途流
// 随之保留。旧实现 openDir 先 stop() 杀进程再拉起，PID 必变。
// 真实 backend 端到端（与 TestSmoke_ProjectSwitch_SessionListUpdates 同基建）。
func TestSwitchProject_KeepsBackendAlive(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，跳过")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	projA := t.TempDir()
	projB := t.TempDir()

	b := newBackendMgr()
	t.Cleanup(func() { b.stop() })

	// 1. 初始在 projA 拉起 backend（own=true → 进常驻池）
	addrA, pidA, doneA, err := startBackend(bin, projA)
	if err != nil {
		t.Fatalf("startBackend(A): %v", err)
	}
	b.set(addrA, pidA, projA, true, doneA)

	// 2. 切换到 B：openDir 应【复用/拉起 B，但不杀 A】
	if _, err := b.switchProject(bin, projB); err != nil {
		t.Fatalf("switchProject(B): %v", err)
	}
	if !processAlive(pidA) {
		t.Fatalf("切到 B 后 A 的 backend 进程 %d 被杀了——右侧会话/在途流全丢（本修复禁止）", pidA)
	}
	if got := b.currentDir(); got != projB {
		t.Fatalf("当前目录应为 B，got %s", got)
	}

	// 3. 切回 A：必须复用【同一个】backend 进程（PID 不变）
	res, err := b.switchProject(bin, projA)
	if err != nil {
		t.Fatalf("switchProject(A): %v", err)
	}
	if pid, _ := res["pid"].(int); pid != pidA {
		t.Fatalf("切回 A 应复用原进程 %d，实际拉起新进程 %v（会话上下文丢失）", pidA, pid)
	}
	if got := b.currentDir(); got != projA {
		t.Fatalf("当前目录应为 A，got %s", got)
	}

	// 4. stop() 一次性清理全部常驻 backend
	b.stop()
	if processAlive(pidA) {
		t.Fatal("stop() 后 A 进程应被终止")
	}
}

// TestReuseBackend_RegistersPool_NoRelaunchOnSwitch 验证"启动复用"场景：
// GUI 启动时复用其他实例遗留的 backend（set own=false），也必须登记进常驻
// 池——否则切到 B 再切回 A 时池中找不到 A，会重新拉起新 backend 进程，
// 原会话上下文（进程内存态）全部丢失，表现为"切回来也没用了"（用户实测
// 反馈的根因）。同时 stop() 不得误杀复用的 backend（它不属于本进程）。
func TestReuseBackend_RegistersPool_NoRelaunchOnSwitch(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，跳过")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	projA := t.TempDir()
	projB := t.TempDir()

	b := newBackendMgr()
	t.Cleanup(func() { b.stop() })

	// 1. 模拟启动复用：A 的 backend 是别处拉起的（own=false，如残留实例）
	addrA, pidA, doneA, err := startBackend(bin, projA)
	if err != nil {
		t.Fatalf("startBackend(A): %v", err)
	}
	b.set(addrA, pidA, projA, false, doneA) // 复用：own=false

	// 2. 切到 B：拉起 B（own=true 入池），A 保留
	if _, err := b.switchProject(bin, projB); err != nil {
		t.Fatalf("switchProject(B): %v", err)
	}
	if !processAlive(pidA) {
		t.Fatalf("切到 B 后复用的 A backend %d 被杀了（本修复禁止）", pidA)
	}

	// 3. 切回 A：必须复用同一 PID，不得重新拉起（旧实现 A 不在池 → 重启）
	res, err := b.switchProject(bin, projA)
	if err != nil {
		t.Fatalf("switchProject(A): %v", err)
	}
	if pid, _ := res["pid"].(int); pid != pidA {
		t.Fatalf("切回 A 应复用原进程 %d，实际 %v（会话上下文丢失）", pidA, pid)
	}

	// 4. stop() 只杀自己拉起的（B），复用的 A（own=false）不杀——
	//    它属于"其他实例"，本进程退出不应终止它。
	b.stop()
	if !processAlive(pidA) {
		t.Fatal("stop() 不应杀复用的 A backend（它不属于本进程生命周期）")
	}
	// 测试收尾：自己终止 A（模拟原属实例后续清理，防测试残留进程）
	_ = signalTerminate(pidA)
}
