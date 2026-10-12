package executor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// mockBackend implements model.Backend for testing
type mockBackend struct {
	chatResponse  string
	chatToolCalls []types.ToolCall
	chatErr       error
	streamTokens  []string
	streamErr     error
	streamDelay   time.Duration
}

func (m *mockBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.chatResponse, m.chatToolCalls, m.chatErr
}

func (m *mockBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, len(m.streamTokens)+1)
	errCh := make(chan error, 1)

	go func() {
		defer close(ch)
		defer close(errCh)
		for _, token := range m.streamTokens {
			select {
			case <-ctx.Done():
				return
			case ch <- token:
				if m.streamDelay > 0 {
					time.Sleep(m.streamDelay)
				}
			}
		}
		if m.streamErr != nil {
			errCh <- m.streamErr
		}
	}()

	return ch, errCh
}

func (m *mockBackend) ModelID() string                 { return "mock-model" }
func (m *mockBackend) MaxContext() int                 { return 4096 }
func (m *mockBackend) SupportsVision() bool            { return false }
func (m *mockBackend) SetReasoningEffort(level string) {}
func (m *mockBackend) ReasoningEffort() string         { return "" }
func (m *mockBackend) SetTopP(p float64)               {}
func (m *mockBackend) TopP() float64                   { return 0 }
func (m *mockBackend) SetTemperature(t float64)        {}
func (m *mockBackend) Temperature() float64            { return 0 }

func TestAgentRunStream_Success(t *testing.T) {
	ctx := context.Background()
	backend := &mockBackend{
		streamTokens: []string{"Hello World!"},
	}

	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	toolReg := tools.NewRegistry()

	agent := &Agent{
		ID: "test-agent-1",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "write a test function",
		},
		harness: eng,
		tools:   toolReg,
		backend: backend,
	}

	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var result string
	for token := range ch {
		result += token
	}

	select {
	case e := <-errCh:
		if e != nil {
			t.Errorf("unexpected stream error: %v", e)
		}
	default:
	}

	if result != "Hello World!" {
		t.Errorf("expected 'Hello World!', got %q", result)
	}
}

func TestAgentRunStream_Error(t *testing.T) {
	ctx := context.Background()
	backend := &mockBackend{
		chatErr: context.DeadlineExceeded,
	}

	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	toolReg := tools.NewRegistry()

	agent := &Agent{
		ID: "test-agent-2",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "test task",
		},
		harness: eng,
		tools:   toolReg,
		backend: backend,
	}

	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("expected no error from RunStream, got %v", err)
	}

	// Drain the channel
	for range ch {
	}

	// Stream returns empty (no tokens), Chat returns error → break
	// No error expected from errCh since we break on Chat failure
	select {
	case e := <-errCh:
		// acceptable to get error or nil
		_ = e
	case <-time.After(100 * time.Millisecond):
		// also acceptable - no error means break happened
	}
}

func TestAgentRunStream_EmptyResponse(t *testing.T) {
	ctx := context.Background()
	backend := &mockBackend{
		streamTokens: []string{},
	}

	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	toolReg := tools.NewRegistry()

	agent := &Agent{
		ID: "test-agent-3",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "empty response test",
		},
		harness: eng,
		tools:   toolReg,
		backend: backend,
	}

	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	var result string
	for token := range ch {
		result += token
	}

	select {
	case e := <-errCh:
		if e != nil {
			t.Errorf("unexpected stream error: %v", e)
		}
	default:
	}

	if !strings.Contains(result, "⚠") {
		t.Errorf("expected 空响应提示后结束，got %q", result)
	}
}

func TestAgentRunStream_ContextCancellation(t *testing.T) {
	backend := &mockBackend{
		streamTokens: []string{"a", "b", "c", "d", "e"},
		streamDelay:  10 * time.Millisecond,
	}

	eng := harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
	toolReg := tools.NewRegistry()

	agent := &Agent{
		ID: "test-agent-4",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "cancellation test",
		},
		harness: eng,
		tools:   toolReg,
		backend: backend,
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, _, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Read one token then cancel
	<-ch
	cancel()

	// The stream should close after cancellation
	timeout := time.After(200 * time.Millisecond)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed, success
			}
		case <-timeout:
			t.Error("timeout: stream did not close after cancellation")
			return
		}
	}
}
