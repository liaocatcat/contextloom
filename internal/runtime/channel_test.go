package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/webhook"
)

// TestWebhook_StartsAndProcessesMessage：webhook 渠道启动 + RunChannelMessage 全链路
// （HTTP 监听就绪 → 消息经 handler → mock 后端输出返回）。
func TestWebhook_StartsAndProcessesMessage(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-channel-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	cfg.Channels.Webhook.Enabled = true
	cfg.Channels.Webhook.Token = "test-token"
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	defer rt.CloseChannels()
	defer rt.CloseMCPServers()

	if rt.webhookSrv == nil {
		t.Fatal("webhook 渠道未启动（Enabled=true 但 webhookSrv 为 nil）")
	}
	if rt.webhookSrv.Token() != "test-token" {
		t.Errorf("token 未透传: got %q want %q", rt.webhookSrv.Token(), "test-token")
	}

	// 替换 mock 后端后执行渠道消息（chat 模式最快路径）
	rt.Backend = &mockBackend{chatResponse: "channel-ok"}
	out, err := rt.RunChannelMessage(context.Background(), webhook.Message{
		Text: "hello channel", Mode: "chat",
	})
	if err != nil {
		t.Fatalf("RunChannelMessage: %v", err)
	}
	if !strings.Contains(out, "channel-ok") {
		t.Errorf("渠道输出异常: got %q", out)
	}

	// 关闭后 HTTP 服务应停止（再 Close 幂等不 panic）
	rt.CloseChannels()
	rt.CloseChannels()
}

// TestCron_RegistersJobs：cron 渠道按配置注册任务并启动调度。
func TestCron_RegistersJobs(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-cron-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	cfg.Channels.Cron.Enabled = true
	cfg.Channels.Cron.Jobs = []config.CronJobConfig{
		{Name: "daily-check", Schedule: "0 9 * * *", Mode: "chat", Prompt: "每日例行检查"},
		{Name: "bad-expr", Schedule: "not-a-cron", Mode: "chat", Prompt: "x"},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	defer rt.CloseChannels()

	if rt.cronSched == nil {
		t.Fatal("cron 调度器未启动（Enabled=true 但 cronSched 为 nil）")
	}
	jobs := rt.cronSched.Jobs()
	if _, ok := jobs["daily-check"]; !ok {
		t.Errorf("合法任务 daily-check 未注册, jobs=%v", jobs)
	}
	if _, ok := jobs["bad-expr"]; ok {
		t.Errorf("非法表达式任务不应注册, jobs=%v", jobs)
	}

	// 关闭调度循环（Stop 幂等）
	rt.CloseChannels()
	rt.CloseChannels()
}

// TestRunChannelMessage_GoalMode：goal 模式渠道消息——无活动目标时新建并执行。
func TestRunChannelMessage_GoalMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-channel-goal-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	rt.Backend = &mockBackend{chatResponse: "goal-created"}

	out, err := rt.RunChannelMessage(context.Background(), webhook.Message{
		Text: "实现一个目标", Mode: "goal",
	})
	if err != nil {
		t.Fatalf("RunChannelMessage(goal): %v", err)
	}
	if out == "" {
		t.Error("goal 模式渠道消息无输出")
	}
	if rt.GetMode() != "goal" {
		t.Errorf("模式未切换为 goal: got %q", rt.GetMode())
	}
	_ = filepath.Join(tmpDir) // 保持 filepath 引用（测试配置使用）
	_ = time.Second
}
