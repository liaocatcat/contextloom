package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/scenes"
	"github.com/contextloom/contextloom-agent/internal/scheduler"
)

// =============================================================================
// 场景触发器 → cron 接线测试（registerSceneCronJobs 可注入 fake sched 单测）
// =============================================================================

// TestRegisterSceneCronJobs_RegistersTrigger 目录 + 场景带 cron → 注册 scene:<name>。
// 注意：用独立目录（不经 tmpDir/.contextloom/scenes）——New/testConfig 的种子落地
// 会把内置 weekly-report（带 cron）写入同一目录，污染注册计数，故隔离。
func TestRegisterSceneCronJobs_RegistersTrigger(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	scenesDir := filepath.Join(tmpDir, "test-scenes")
	writeFile(t, filepath.Join(scenesDir, "weekly", "scene.yaml"), `
name: weekly
display: 周报生成
keywords: ["周报", "周总结"]
triggers:
  cron: "0 17 * * 5"
`)
	sched := scheduler.New(0)
	if n := rt.registerSceneCronJobs(sched, scenesDir); n != 1 {
		t.Fatalf("应注册 1 个场景触发器，got %d", n)
	}
	jobs := sched.Jobs()
	if _, ok := jobs["scene:weekly"]; !ok {
		t.Fatalf("scene:weekly 应已注册: %v", jobs)
	}
}

// TestRegisterSceneCronJobs_SkipsNoCron 无 cron 声明的场景跳过。
func TestRegisterSceneCronJobs_SkipsNoCron(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	scenesDir := filepath.Join(tmpDir, "test-scenes")
	writeFile(t, filepath.Join(scenesDir, "prd", "scene.yaml"), `
name: prd
keywords: ["prd"]
`)
	sched := scheduler.New(0)
	if n := rt.registerSceneCronJobs(sched, scenesDir); n != 0 {
		t.Fatalf("无 cron 声明不应注册，got %d", n)
	}
	if len(sched.Jobs()) != 0 {
		t.Fatalf("调度器应为空: %v", sched.Jobs())
	}
}

// TestRegisterSceneCronJobs_ZeroConfigDir 场景目录不存在 → 静默返回 0（零配置兜底）。
func TestRegisterSceneCronJobs_ZeroConfigDir(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	sched := scheduler.New(0)
	if n := rt.registerSceneCronJobs(sched, filepath.Join(tmpDir, "no-such-scenes")); n != 0 {
		t.Fatalf("无场景目录应返回 0，got %d", n)
	}
}

// TestRegisterSceneCronJobs_NilSched cron 通道未启用（sched=nil）→ 返回 0 不 panic。
func TestRegisterSceneCronJobs_NilSched(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	if n := rt.registerSceneCronJobs(nil, filepath.Join(tmpDir, "test-scenes")); n != 0 {
		t.Fatalf("nil sched 应返回 0，got %d", n)
	}
}

// TestRegisterSceneCronJobs_Idempotent 重复注册幂等（重名跳过，不 panic 不重复）。
func TestRegisterSceneCronJobs_Idempotent(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	scenesDir := filepath.Join(tmpDir, "test-scenes")
	writeFile(t, filepath.Join(scenesDir, "weekly", "scene.yaml"), `
name: weekly
keywords: ["周报"]
triggers:
  cron: "0 17 * * 5"
`)
	sched := scheduler.New(0)
	if n := rt.registerSceneCronJobs(sched, scenesDir); n != 1 {
		t.Fatalf("首次注册应 1 个，got %d", n)
	}
	if n := rt.registerSceneCronJobs(sched, scenesDir); n != 0 {
		t.Fatalf("重复注册应幂等为 0，got %d", n)
	}
	if len(sched.Jobs()) != 1 {
		t.Fatalf("重名不应新增任务: %v", sched.Jobs())
	}
}

// TestRegisterSceneCronJobs_InvalidExpr 非法表达式跳过该场景、继续注册其余场景。
func TestRegisterSceneCronJobs_InvalidExpr(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	scenesDir := filepath.Join(tmpDir, "test-scenes")
	writeFile(t, filepath.Join(scenesDir, "bad", "scene.yaml"), `
name: bad
keywords: ["bad"]
triggers:
  cron: "not-a-cron"
`)
	writeFile(t, filepath.Join(scenesDir, "good", "scene.yaml"), `
name: good
keywords: ["good"]
triggers:
  cron: "0 9 * * *"
`)
	sched := scheduler.New(0)
	if n := rt.registerSceneCronJobs(sched, scenesDir); n != 1 {
		t.Fatalf("非法表达式场景应跳过、合法场景应注册：got %d", n)
	}
	jobs := sched.Jobs()
	if _, ok := jobs["scene:bad"]; ok {
		t.Fatalf("非法表达式不应注册: %v", jobs)
	}
	if _, ok := jobs["scene:good"]; !ok {
		t.Fatalf("合法场景应注册: %v", jobs)
	}
}

// TestRegisterSceneCronJobs_TriggerExecutes 手动触发场景任务 → chat 模式经
// RunChannelMessage 输出（用 mock 后端断言链路可达）。
func TestRegisterSceneCronJobs_TriggerExecutes(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.Backend = &mockBackend{chatResponse: "scene-cron-ok"}
	scenesDir := filepath.Join(tmpDir, "test-scenes")
	writeFile(t, filepath.Join(scenesDir, "weekly", "scene.yaml"), `
name: weekly
keywords: ["周报"]
triggers:
  cron: "0 17 * * 5"
`)
	sched := scheduler.New(0)
	if n := rt.registerSceneCronJobs(sched, scenesDir); n != 1 {
		t.Fatalf("注册失败: %d", n)
	}
	if !sched.Trigger("scene:weekly") {
		t.Fatal("Trigger 应命中已注册任务")
	}
	// exec 为异步 goroutine：轮询等待 LastErr 状态刷新
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var runs int
		var lastErr string
		for _, st := range sched.Status() {
			if st.Name == "scene:weekly" {
				runs, lastErr = st.Runs, st.LastErr
			}
		}
		if runs > 0 {
			if lastErr != "" {
				t.Fatalf("场景触发执行失败: %s", lastErr)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("场景任务未在超时内执行")
}

// TestSceneCronPrompt 触发文本优先级：关键词 → 展示名 → 场景名。
func TestSceneCronPrompt(t *testing.T) {
	if got := sceneCronPrompt(scenesScene("weekly", "周报生成", []string{"周报", "周总结"})); got != "周报" {
		t.Fatalf("关键词优先：got %q", got)
	}
	if got := sceneCronPrompt(scenesScene("weekly", "周报生成", []string{"", "  "})); got != "周报生成" {
		t.Fatalf("空关键词回退展示名：got %q", got)
	}
	if got := sceneCronPrompt(scenesScene("weekly", "", nil)); got != "weekly" {
		t.Fatalf("无关键词/展示名回退场景名：got %q", got)
	}
}

// =============================================================================
// 主路径集成：New() 在 cron 通道启用时自动注册场景触发器
// =============================================================================

// TestCronChannel_SceneTriggerIntegrated cron 启用 + 场景目录带 triggers.cron
// → New 后 cronSched 中存在 scene:<name>，且与 config jobs 并存。
func TestCronChannel_SceneTriggerIntegrated(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-scenecron-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	writeFile(t, filepath.Join(tmpDir, ".contextloom", "scenes", "weekly", "scene.yaml"), `
name: weekly
keywords: ["周报"]
triggers:
  cron: "0 17 * * 5"
`)
	cfg := testConfig(tmpDir)
	cfg.Channels.Cron.Enabled = true
	cfg.Channels.Cron.Jobs = []config.CronJobConfig{
		{Name: "daily-check", Schedule: "0 9 * * *", Mode: "chat", Prompt: "每日例行检查"},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	defer rt.CloseChannels()

	if rt.cronSched == nil {
		t.Fatal("cron 调度器未启动")
	}
	jobs := rt.cronSched.Jobs()
	if _, ok := jobs["daily-check"]; !ok {
		t.Errorf("config job daily-check 未注册: %v", jobs)
	}
	if _, ok := jobs["scene:weekly"]; !ok {
		t.Errorf("场景触发器 scene:weekly 未注册: %v", jobs)
	}
}

// scenesScene 测试辅助：构造 scenes.Scene（避免与 scenes 包 API 耦合）。
func scenesScene(name, display string, keywords []string) scenes.Scene {
	return scenes.Scene{Name: name, Display: display, Keywords: keywords}
}
