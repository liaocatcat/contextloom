package executor

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// raceBackend 精确复现 select 竞争：streamCh 缓冲先塞满（含 ToolCalls chunk），
// errCh 再发错误——agent 主循环 select 命中错误分支时，若不排空缓冲则
// ToolCalls 丢失 → 工具从不执行（TestAgentRunStream_Real1626 的 flaky 根因）。
type raceBackend struct {
	model.Backend // 内嵌 nil 接口：RunStream 主路径仅走 StreamEx 断言
	mu            sync.Mutex
	reqCount      int
}

func (b *raceBackend) ModelID() string { return "race-model" }
func (b *raceBackend) MaxContext() int { return 131072 }
func (b *raceBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return "", nil, nil
}
func (b *raceBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	close(ch)
	close(errCh)
	return ch, errCh
}

func (b *raceBackend) SetStreamTools(tools []types.ToolDefinition) {}

func (b *raceBackend) StreamEx(ctx context.Context, messages []types.Message) (<-chan model.StreamChunk, <-chan error) {
	// 带缓冲：ToolCalls 与错误在生产者侧同批就绪，模拟真实时序
	// （StreamEx flushPending 发 ToolCalls → 紧接 scanner.Err() 发 errCh）。
	ch := make(chan model.StreamChunk, 2)
	errCh := make(chan error, 1)
	b.mu.Lock()
	b.reqCount++
	first := b.reqCount == 1
	b.mu.Unlock()
	if first {
		// 第一轮：正文 + 工具调用 + 连接中断（竞争场景）
		ch <- model.StreamChunk{Content: "先查看目录"}
		ch <- model.StreamChunk{IsToolCall: true, ToolCalls: []types.ToolCall{{
			ID:   "call_t0",
			Type: "function",
			Function: types.FunctionCall{
				Name:      "list_files",
				Arguments: `{"path":"."}`,
			},
		}}}
		errCh <- context.DeadlineExceeded // 模拟连接中断
	} else {
		// 工具结果已回传后的后续轮次：正常回答收尾
		ch <- model.StreamChunk{Content: "目录已列出，任务完成"}
		errCh <- nil
	}
	close(ch)
	close(errCh)
	return ch, errCh
}

// TestAgentRunStream_ErrRaceDrainsToolCalls：错误与 ToolCalls chunk 竞争时，
// 修复后的 drain 逻辑保证 ToolCalls 被消费并执行工具（回归钉子）。
func TestAgentRunStream_ErrRaceDrainsToolCalls(t *testing.T) {
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "race-drain",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "列出目录", Mode: "task"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: &raceBackend{},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	if !strings.Contains(got, "执行工具: list_files") {
		t.Fatalf("错误分支命中后缓冲中的 ToolCalls 应被排空并执行工具，实际输出: %q", got)
	}
}
