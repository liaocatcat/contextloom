package executor

import (
	"context"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// roundRobinBackend 依次返回多轮结果（先工具调用，后最终回答），验证中间检查点。
type roundRobinBackend struct {
	rounds []roundResp
	i      int
}

type roundResp struct {
	resp  string
	calls []types.ToolCall
}

func (m *roundRobinBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	r := m.rounds[m.i%len(m.rounds)]
	m.i++
	return r.resp, r.calls, nil
}

func (m *roundRobinBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string)
	errCh := make(chan error, 1)
	go func() { close(ch); errCh <- nil }()
	return ch, errCh
}

func (m *roundRobinBackend) ModelID() string                 { return "test-model" }
func (m *roundRobinBackend) MaxContext() int                 { return 4096 }
func (m *roundRobinBackend) SupportsVision() bool            { return false }
func (m *roundRobinBackend) SetReasoningEffort(level string) {}
func (m *roundRobinBackend) ReasoningEffort() string         { return "" }
func (m *roundRobinBackend) SetTopP(p float64)               {}
func (m *roundRobinBackend) TopP() float64                   { return 0 }
func (m *roundRobinBackend) SetTemperature(t float64)        {}
func (m *roundRobinBackend) Temperature() float64            { return 0 }

// TestAgentRun_CheckpointSink：每轮消息更新后回调（done=false、round 递增），
// 结束时回调（done=true、result 非空）。
func TestAgentRun_CheckpointSink(t *testing.T) {
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	backend := &roundRobinBackend{rounds: []roundResp{
		{resp: "let me check", calls: []types.ToolCall{{ID: "c1", Type: "function", Function: types.FunctionCall{Name: "read_file", Arguments: "{}"}}}},
		{resp: "final answer"},
	}}
	agent := &Agent{
		ID: "cp-test",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "checkpoint task",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: backend,
	}
	var mu sync.Mutex
	var calls []struct {
		round  int
		done   bool
		result string
		msgs   int
	}
	agent.SetCheckpointSink(func(round int, messages []types.Message, done bool, result string) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, struct {
			round  int
			done   bool
			result string
			msgs   int
		}{round, done, result, len(messages)})
	})

	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) == 0 {
		t.Fatal("检查点回调未被调用")
	}
	last := calls[len(calls)-1]
	if !last.done {
		t.Fatalf("最后回调应 done=true, got %+v", last)
	}
	if last.result != "final answer" {
		t.Fatalf("result = %q, want %q", last.result, "final answer")
	}
	// 中间回调：done=false 且 round 递增
	prevRound := 0
	seenMid := false
	for i, c := range calls {
		if c.round < prevRound {
			t.Fatalf("round 未递增: %d -> %d", prevRound, c.round)
		}
		prevRound = c.round
		if !c.done {
			seenMid = true
			if c.msgs == 0 {
				t.Fatalf("中间回调 messages 为空")
			}
		}
		if i > 0 && c.round == calls[i-1].round && c.done == calls[i-1].done {
			t.Fatalf("重复回调: %+v", c)
		}
	}
	if !seenMid {
		t.Fatal("缺少中间（done=false）回调")
	}
}

// TestAgentRun_CheckpointSink_Nil：nil sink 零开销不 panic（回归）。
func TestAgentRun_CheckpointSink_Nil(t *testing.T) {
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID: "cp-nil",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "nil sink task",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: &testBackend{chatResponse: "done"},
	}
	// 不设置 sink → 不 panic
	if err := agent.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

// TestAgentRunStream_CheckpointSink：流式路径同样触发检查点。
func TestAgentRunStream_CheckpointSink(t *testing.T) {
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	backend := &testBackend{streamTokens: []string{"hello ", "world"}}
	agent := &Agent{
		ID: "cp-stream",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "stream checkpoint",
		},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: backend,
	}
	var mu sync.Mutex
	done := false
	agent.SetCheckpointSink(func(round int, messages []types.Message, isDone bool, result string) {
		mu.Lock()
		defer mu.Unlock()
		if isDone {
			done = true
		}
	})
	ch, errCh, err := agent.RunStream(context.Background())
	if err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	for range ch {
	}
	if e := <-errCh; e != nil {
		t.Fatalf("errCh: %v", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if !done {
		t.Fatal("流式结束应触发 done=true 检查点")
	}
}
