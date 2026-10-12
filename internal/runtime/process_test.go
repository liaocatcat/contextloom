package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// mockBackend implements model.Backend for runtime tests
type mockBackend struct {
	chatResponse string
	chatErr      error
	effort       string
}

func (m *mockBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.chatResponse, nil, m.chatErr
}

func (m *mockBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		if m.chatResponse != "" {
			ch <- m.chatResponse
		}
		if m.chatErr != nil {
			errCh <- m.chatErr
		}
	}()
	return ch, errCh
}

func (m *mockBackend) ModelID() string                 { return "mock-model" }
func (m *mockBackend) MaxContext() int                 { return 4096 }
func (m *mockBackend) SupportsVision() bool            { return false }
func (m *mockBackend) SetReasoningEffort(level string) { m.effort = level }
func (m *mockBackend) ReasoningEffort() string         { return m.effort }
func (m *mockBackend) SetTopP(p float64)               {}
func (m *mockBackend) TopP() float64                   { return 0 }
func (m *mockBackend) SetTemperature(t float64)        {}
func (m *mockBackend) Temperature() float64            { return 0 }

func testConfig(tmpDir string) *config.Config {
	return &config.Config{
		ActiveModelName: "mock",
		Models: []config.ModelPreset{{
			Name:       "mock",
			Backend:    "mock",
			MaxContext: 4096,
		}},
		Harness: config.HarnessConfig{
			ScoreThreshold: 50,
			DecayFactor:    0.9,
			AnchorRatio:    0.2,
		},
		Memory: config.MemoryConfig{
			Directory:   filepath.Join(tmpDir, "memory"),
			MaxSessions: 10,
		},
		Plugins: config.PluginsConfig{
			Directory: filepath.Join(tmpDir, "plugins"),
		},
		// 显式设置项目根，避免 runtime 在测试目录（CWD）生成 .contextloom/ 污染源码
		Project: config.ProjectConfig{
			Root:  tmpDir,
			Vocab: filepath.Join(tmpDir, "vocab.json"),
		},
		CLIDefaults: config.CLIDefaultsConfig{
			Effort: "medium",
		},
	}
}

func TestProcessInput_Streaming(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	// Replace with mock backend
	mock := &mockBackend{
		chatResponse: "Hello from mock!",
	}
	rt.Backend = mock

	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "write a test")
	if err != nil {
		t.Fatalf("ProcessInput returned error: %v", err)
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

	if result != "Hello from mock!" {
		t.Errorf("expected 'Hello from mock!', got %q", result)
	}
}

func TestProcessInput_EmptyInput(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	mock := &mockBackend{
		chatResponse: "",
	}
	rt.Backend = mock

	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "")
	if err != nil {
		t.Fatalf("ProcessInput returned error: %v", err)
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

func TestProcessInput_StreamError(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	mock := &mockBackend{
		chatResponse: "partial",
		chatErr:      context.DeadlineExceeded,
	}
	rt.Backend = mock

	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "test error handling")
	if err != nil {
		t.Fatalf("ProcessInput returned error: %v", err)
	}

	// Drain the channel
	for range ch {
	}

	// Stream returns empty, Chat returns error → break, no error
	select {
	case <-errCh:
		// acceptable
	case <-time.After(200 * time.Millisecond):
		// also acceptable
	}
}

func TestProcessInput_ContextCancellation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	mock := &mockBackend{
		chatResponse: "ae",
	}
	rt.Backend = mock

	ctx, cancel := context.WithCancel(context.Background())
	ch, _, err := rt.ProcessInput(ctx, "cancellation test")
	if err != nil {
		t.Fatalf("ProcessInput returned error: %v", err)
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
