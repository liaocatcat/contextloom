package runtime

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// stallBackend 可编程流式后端：按 interval 依次发送 tokens，发完后可选卡死
// （stall=true 时阻塞直到 ctx 取消——模拟"子任务卡顿无输出"）。
type stallBackend struct {
	tokens   []string
	interval time.Duration
	stall    bool
}

func (b *stallBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return strings.Join(b.tokens, ""), nil, nil
}
func (b *stallBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 16)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, t := range b.tokens {
			select {
			case ch <- t:
			case <-ctx.Done():
				return
			}
			if b.interval > 0 {
				select {
				case <-time.After(b.interval):
				case <-ctx.Done():
					return
				}
			}
		}
		if b.stall {
			<-ctx.Done() // 卡死：无输出，等待外部取消
		}
	}()
	return ch, errCh
}
func (b *stallBackend) ModelID() string           { return "stall" }
func (b *stallBackend) MaxContext() int           { return 4096 }
func (b *stallBackend) SupportsVision() bool      { return false }
func (b *stallBackend) SetReasoningEffort(string) {}
func (b *stallBackend) ReasoningEffort() string   { return "" }
func (b *stallBackend) SetTopP(float64)           {}
func (b *stallBackend) TopP() float64             { return 0 }
func (b *stallBackend) SetTemperature(float64)    {}
func (b *stallBackend) Temperature() float64      { return 0 }

func newSpawnTimeoutRuntime(t *testing.T, backend interface {
	// 占位：直接复用 newSpawnRuntime 的构造，但允许替换 Backend 与超时
}) *Runtime {
	t.Helper()
	tmpDir, _ := os.MkdirTemp("", "contextloom-st-*")
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: tmpDir + "/mem", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: tmpDir + "/plug"},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "task"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: tmpDir, Vocab: tmpDir + "/vocab.json"},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return rt
}

// TestSpawnTask_NoOutputTimeout：子 Agent 卡死无输出 → 超过无输出窗口被终止，
// 错误信息明确（不再受固定 120s 限制）。
func TestSpawnTask_NoOutputTimeout(t *testing.T) {
	rt := newSpawnTimeoutRuntime(t, nil)
	rt.childNoOutputTimeout = 200 * time.Millisecond
	rt.Backend = &stallBackend{tokens: []string{"开始执行"}, stall: true}

	start := time.Now()
	_, err := rt.SpawnTask(context.Background(), "main", tools.TaskSpec{
		Task: "模拟卡死的子任务", Directive: "CODE_AGENT",
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("卡死子任务应被无输出超时终止")
	}
	if !strings.Contains(err.Error(), "无输出超过") {
		t.Errorf("错误信息应为无输出超时，got: %v", err)
	}
	// 应在无输出窗口附近终止（而非 120s 固定超时）
	if elapsed > 5*time.Second {
		t.Errorf("超时应在活动式窗口附近触发，实际耗时 %s", elapsed)
	}
}

// TestSpawnTask_ActiveOutputNoTimeout：子 Agent 持续有输出 → 不设限，正常完成。
func TestSpawnTask_ActiveOutputNoTimeout(t *testing.T) {
	rt := newSpawnTimeoutRuntime(t, nil)
	rt.childNoOutputTimeout = 150 * time.Millisecond // 远小于总时长
	rt.Backend = &stallBackend{
		tokens:   []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"},
		interval: 60 * time.Millisecond, // 每次输出间隔 60ms < 150ms 无输出窗口
	}

	result, err := rt.SpawnTask(context.Background(), "main", tools.TaskSpec{
		Task: "持续输出的子任务", Directive: "CODE_AGENT",
	})
	if err != nil {
		t.Fatalf("持续输出不应超时，got: %v", err)
	}
	for _, tk := range []string{"t1", "t2", "t3", "t4", "t5", "t6", "t7", "t8"} {
		if !strings.Contains(result, tk) {
			t.Errorf("结果应含全部输出 %s，got %q", tk, result)
		}
	}
}

// TestSpawnTask_TimeoutResetsOnOutput：输出会重置计时——
// 每次输出间隔 250ms < 无输出窗口 300ms，但多段输出总时长 ~1s > 窗口：
// 若无"输出重置计时"，t=300ms 时即被误杀；有重置则完整执行到结束。
func TestSpawnTask_TimeoutResetsOnOutput(t *testing.T) {
	rt := newSpawnTimeoutRuntime(t, nil)
	rt.childNoOutputTimeout = 300 * time.Millisecond
	rt.Backend = &stallBackend{
		tokens:   []string{"p1", "p2", "p3", "p4", "p5"},
		interval: 250 * time.Millisecond,
	}

	result, err := rt.SpawnTask(context.Background(), "main", tools.TaskSpec{
		Task: "间歇输出的子任务", Directive: "CODE_AGENT",
	})
	if err != nil {
		t.Fatalf("有输出重置计时不应超时，got: %v", err)
	}
	if !strings.Contains(result, "p5") {
		t.Errorf("结果应含最终输出，got %q", result)
	}
}
