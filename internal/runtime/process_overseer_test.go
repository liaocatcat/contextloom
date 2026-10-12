package runtime

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// multiTokenBackend 模拟模型后端，流式返回多个 token
type multiTokenBackend struct {
	tokens []string
}

func (m *multiTokenBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return strings.Join(m.tokens, ""), nil, nil
}

func (m *multiTokenBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, len(m.tokens)+1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, t := range m.tokens {
			select {
			case <-ctx.Done():
				return
			case ch <- t:
			}
		}
	}()
	return ch, errCh
}

func (m *multiTokenBackend) ModelID() string           { return "mock-multi" }
func (m *multiTokenBackend) MaxContext() int           { return 4096 }
func (m *multiTokenBackend) SupportsVision() bool      { return false }
func (m *multiTokenBackend) SetReasoningEffort(string) {}
func (m *multiTokenBackend) ReasoningEffort() string   { return "" }
func (m *multiTokenBackend) SetTopP(p float64)         {}
func (m *multiTokenBackend) TopP() float64             { return 0 }
func (m *multiTokenBackend) SetTemperature(t float64)  {}
func (m *multiTokenBackend) Temperature() float64      { return 0 }

// TestProcessInput_TaskMode_StreamIntegrity 回归测试：
// task 模式下 ProcessInput 必须保证 ch 只有 TUI 一个消费者。
// 修复前：监工 goroutine 用 range ch 与 TUI 竞争，随机吞掉约一半 token，
// 导致 TUI 输出语义破碎、内容丢失。
func TestProcessInput_TaskMode_StreamIntegrity(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-overseer-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rt, err := New(testConfig(tmpDir))
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	// 20 个 token，拼接成一句连贯的话
	var tokens []string
	for _, w := range []string{
		"你好", "！很高", "兴为您", "服务", "。我可", "以帮你", "编写", "代码", "，包括",
		"：修复", "bug", "、分析", "需求", "、编写", "测试", "、生成", "文档", "等", "。",
	} {
		tokens = append(tokens, w)
	}
	want := strings.Join(tokens, "")
	rt.Backend = &multiTokenBackend{tokens: tokens}

	// task 模式：触发监工创建（修复前这会启动偷 token 的 goroutine）
	// 直接改配置字段而非 SetMode，避免 saveSettings 污染工作区文件
	rt.Config.CLIDefaults.Mode = "task"

	// 循环多轮，任何一轮 token 不完整即失败（修复前随机吞 token，必然失败）
	for round := 0; round < 10; round++ {
		ctx := context.Background()
		ch, errCh, err := rt.ProcessInput(ctx, "单个任务不拆分")
		if err != nil {
			t.Fatalf("round %d: ProcessInput error: %v", round, err)
		}

		var got string
		for token := range ch {
			got += token
		}
		if e := <-errCh; e != nil {
			t.Fatalf("round %d: stream error: %v", round, e)
		}

		if got != want {
			t.Fatalf("round %d: token 丢失/乱序（存在第二消费者偷 token）:\nwant: %q\n got: %q", round, want, got)
		}
	}
}

// TestProcessInput_ChatMode_ClearsResidualOverseer 回归测试：
// task 请求后的 chat 请求必须清除残留监工，避免监工状态泄漏。
func TestProcessInput_ChatMode_ClearsResidualOverseer(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-overseer-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	rt, err := New(testConfig(tmpDir))
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	rt.Backend = &multiTokenBackend{tokens: []string{"ok"}}

	ctx := context.Background()

	// 第一次：task 模式，创建监工
	rt.Config.CLIDefaults.Mode = "task"
	if _, _, err := rt.ProcessInput(ctx, "单任务"); err != nil {
		t.Fatalf("task ProcessInput error: %v", err)
	}
	if !rt.HasOverseer() {
		t.Fatal("task 模式应创建监工")
	}

	// 第二次：chat 模式，必须清除残留监工
	rt.Config.CLIDefaults.Mode = "chat"
	ch, errCh, err := rt.ProcessInput(ctx, "你好")
	if err != nil {
		t.Fatalf("chat ProcessInput error: %v", err)
	}
	for range ch {
	}
	<-errCh

	if rt.HasOverseer() {
		t.Fatal("chat 模式必须清除残留监工，避免状态泄漏")
	}
}
