package executor

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ---------------------------------------------------------------------------
// mockBackend (shared with agent_stream_test.go patterns)
// ---------------------------------------------------------------------------

type testBackend struct {
	chatResponse  string
	chatToolCalls []types.ToolCall
	chatErr       error

	streamTokens []string
	streamErr    error
}

func (m *testBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return m.chatResponse, m.chatToolCalls, m.chatErr
}

func (m *testBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
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
			}
		}
		if m.streamErr != nil {
			errCh <- m.streamErr
		}
	}()

	return ch, errCh
}

func (m *testBackend) ModelID() string                 { return "test-model" }
func (m *testBackend) MaxContext() int                 { return 4096 }
func (m *testBackend) SupportsVision() bool            { return false }
func (m *testBackend) SetReasoningEffort(level string) {}
func (m *testBackend) ReasoningEffort() string         { return "" }
func (m *testBackend) SetTopP(p float64)               {}
func (m *testBackend) TopP() float64                   { return 0 }
func (m *testBackend) SetTemperature(t float64)        {}
func (m *testBackend) Temperature() float64            { return 0 }

// newTestHarness creates a harness.Engine with default test config.
func newTestHarness() *harness.Engine {
	return harness.New(harness.Config{
		ScoreThreshold: 50,
		DecayFactor:    0.9,
		AnchorRatio:    0.2,
	})
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew_InitializesEmptyAgentsMap(t *testing.T) {
	h := newTestHarness()
	tr := tools.NewRegistry()
	exec := New(h, tr)

	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.harness != h {
		t.Error("executor harness does not match input")
	}
	if exec.tools != tr {
		t.Error("executor tools does not match input")
	}
	if exec.agents == nil {
		t.Error("expected non-nil agents map")
	}
	if len(exec.agents) != 0 {
		t.Errorf("expected empty agents map, got %d entries", len(exec.agents))
	}
}

// ---------------------------------------------------------------------------
// SpawnAgent
// ---------------------------------------------------------------------------

func TestSpawnAgent_CreatesAndStoresAgent(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{
		ID:        "task-1",
		Directive: "CODE_AGENT",
		Task:      "write unit tests",
	}
	backend := &testBackend{chatResponse: "ok"}

	agent := exec.SpawnAgent("agent-1", task, backend)

	// Verify returned agent fields
	if agent.ID != "agent-1" {
		t.Errorf("expected agent ID 'agent-1', got %q", agent.ID)
	}
	if agent.Task.ID != "task-1" {
		t.Errorf("expected task ID 'task-1', got %q", agent.Task.ID)
	}
	if agent.Task.Directive != "CODE_AGENT" {
		t.Errorf("expected directive 'CODE_AGENT', got %q", agent.Task.Directive)
	}
	if agent.backend != backend {
		t.Error("agent backend does not match input")
	}
	if agent.harness != exec.harness {
		t.Error("agent harness does not match executor harness")
	}
	if agent.tools != exec.tools {
		t.Error("agent tools does not match executor tools")
	}

	// Verify agent is stored internally
	stored, ok := exec.agents["agent-1"]
	if !ok {
		t.Fatal("agent not found in internal map")
	}
	if stored != agent {
		t.Error("stored agent is not the same pointer as returned agent")
	}
}

func TestSpawnAgent_OverwritesExistingAgent(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task1 := types.TaskCreate{ID: "task-1", Directive: "CODE_AGENT", Task: "first"}
	task2 := types.TaskCreate{ID: "task-2", Directive: "CODE_AGENT", Task: "second"}
	backend1 := &testBackend{chatResponse: "first"}
	backend2 := &testBackend{chatResponse: "second"}

	first := exec.SpawnAgent("agent-1", task1, backend1)
	second := exec.SpawnAgent("agent-1", task2, backend2)

	// Should return a different agent
	if first == second {
		t.Error("expected a new agent instance on overwrite")
	}
	if second.Task.ID != "task-2" {
		t.Errorf("expected task ID 'task-2', got %q", second.Task.ID)
	}
	if second.backend != backend2 {
		t.Error("expected new backend on overwritten agent")
	}

	// Map should contain exactly one entry
	if len(exec.agents) != 1 {
		t.Errorf("expected 1 agent in map, got %d", len(exec.agents))
	}
}

func TestSpawnAgent_MultipleDistinctAgents(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	a1 := exec.SpawnAgent("agent-1", task, backend)
	a2 := exec.SpawnAgent("agent-2", task, backend)
	a3 := exec.SpawnAgent("agent-3", task, backend)

	if len(exec.agents) != 3 {
		t.Errorf("expected 3 agents, got %d", len(exec.agents))
	}
	if a1.ID == a2.ID || a2.ID == a3.ID || a1.ID == a3.ID {
		t.Error("agents should have distinct IDs")
	}
}

// ---------------------------------------------------------------------------
// Execute
// ---------------------------------------------------------------------------

func TestExecute_AgentNotFound(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	ctx := context.Background()

	err := exec.Execute(ctx, "nonexistent-agent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
	expected := "agent not found: nonexistent-agent"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestExecute_Success_NoToolCalls(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	ctx := context.Background()
	backend := &testBackend{
		chatResponse:  "test result",
		chatToolCalls: nil, // no tool calls → loop breaks after first iteration
	}
	task := types.TaskCreate{
		ID:        "task-1",
		Directive: "CODE_AGENT",
		Task:      "simple task",
	}

	exec.SpawnAgent("agent-1", task, backend)
	err := exec.Execute(ctx, "agent-1")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Verify payload was set by agent.Run
	agent := exec.agents["agent-1"]
	payload, ok := agent.Task.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", agent.Task.Payload)
	}
	if payload["result"] != "test result" {
		t.Errorf("expected payload result 'test result', got %q", payload["result"])
	}
}

func TestExecute_BackendError(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	ctx := context.Background()
	backend := &testBackend{
		chatErr: errors.New("backend unavailable"),
	}
	task := types.TaskCreate{
		ID:        "task-1",
		Directive: "CODE_AGENT",
		Task:      "failing task",
	}

	exec.SpawnAgent("agent-1", task, backend)
	err := exec.Execute(ctx, "agent-1")

	if err == nil {
		t.Fatal("expected error from backend failure")
	}
}

func TestExecute_WithToolCalls(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	ctx := context.Background()

	// Simulate: first call returns a tool call, second call returns final result
	callCount := 0
	backend := &countingBackend{
		responses: []backendResponse{
			{content: "I'll search that", toolCalls: []types.ToolCall{{ID: "call-1", Type: "function", Function: types.FunctionCall{Name: "read_file", Arguments: `{"path":"/test.txt"}`}}}},
			{content: "Found the file content", toolCalls: nil},
		},
		callCount: &callCount,
	}
	task := types.TaskCreate{
		ID:        "task-1",
		Directive: "CODE_AGENT",
		Task:      "read a file",
	}

	exec.SpawnAgent("agent-1", task, backend)
	err := exec.Execute(ctx, "agent-1")

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	agent := exec.agents["agent-1"]
	payload, ok := agent.Task.Payload.(map[string]string)
	if !ok {
		t.Fatalf("expected map[string]string payload, got %T", agent.Task.Payload)
	}
	if payload["result"] != "Found the file content" {
		t.Errorf("expected payload 'Found the file content', got %q", payload["result"])
	}
}

// countingBackend is a backend that cycles through pre-configured responses.
type countingBackend struct {
	responses []backendResponse
	callCount *int
}

type backendResponse struct {
	content   string
	toolCalls []types.ToolCall
}

func (b *countingBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	idx := *b.callCount
	*b.callCount++
	if idx >= len(b.responses) {
		return "fallback", nil, nil
	}
	r := b.responses[idx]
	return r.content, r.toolCalls, nil
}

func (b *countingBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	ch := make(chan string)
	errCh := make(chan error)
	close(ch)
	close(errCh)
	return ch, errCh
}

func (b *countingBackend) ModelID() string                 { return "counting-backend" }
func (b *countingBackend) MaxContext() int                 { return 4096 }
func (b *countingBackend) SupportsVision() bool            { return false }
func (b *countingBackend) SetReasoningEffort(level string) {}
func (b *countingBackend) ReasoningEffort() string         { return "" }
func (b *countingBackend) SetTopP(p float64)               {}
func (b *countingBackend) TopP() float64                   { return 0 }
func (b *countingBackend) SetTemperature(t float64)        {}
func (b *countingBackend) Temperature() float64            { return 0 }

// ---------------------------------------------------------------------------
// RemoveAgent
// ---------------------------------------------------------------------------

func TestRemoveAgent_RemovesExisting(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	exec.SpawnAgent("agent-1", task, backend)
	if len(exec.agents) != 1 {
		t.Fatal("expected 1 agent before removal")
	}

	exec.RemoveAgent("agent-1")

	if len(exec.agents) != 0 {
		t.Errorf("expected 0 agents after removal, got %d", len(exec.agents))
	}
	_, exists := exec.agents["agent-1"]
	if exists {
		t.Error("agent-1 should not exist after removal")
	}
}

func TestRemoveAgent_NoOpOnNonExistent(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	exec.SpawnAgent("agent-1", task, backend)

	// Remove a non-existent agent should not panic or error
	exec.RemoveAgent("nonexistent")

	if len(exec.agents) != 1 {
		t.Errorf("expected 1 agent after no-op removal, got %d", len(exec.agents))
	}
	_, exists := exec.agents["agent-1"]
	if !exists {
		t.Error("agent-1 should still exist")
	}
}

func TestRemoveAgent_ThenRespawn(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	exec.SpawnAgent("agent-1", task, backend)
	exec.RemoveAgent("agent-1")
	newAgent := exec.SpawnAgent("agent-1", task, backend)

	if newAgent == nil {
		t.Fatal("expected non-nil agent after respawn")
	}
	if len(exec.agents) != 1 {
		t.Errorf("expected 1 agent after respawn, got %d", len(exec.agents))
	}
}

// ---------------------------------------------------------------------------
// Agents
// ---------------------------------------------------------------------------

func TestAgents_ReturnsEmptyMap(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())

	result := exec.Agents()

	if result == nil {
		t.Fatal("expected non-nil map")
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %d entries", len(result))
	}
}

func TestAgents_ReturnsCopyNotReference(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	exec.SpawnAgent("agent-1", task, backend)

	// Get the map and modify it
	result := exec.Agents()
	result["agent-1"] = nil      // mutate the returned map
	result["agent-2"] = &Agent{} // add a new entry

	// Internal map should be unchanged
	internal := exec.agents
	if internal["agent-1"] == nil {
		t.Error("internal map should not be affected by external mutation (nil assignment)")
	}
	if _, exists := internal["agent-2"]; exists {
		t.Error("internal map should not contain externally added entries")
	}
	if len(internal) != 1 {
		t.Errorf("expected 1 internal agent, got %d", len(internal))
	}
}

func TestAgents_ReturnsAllAgents(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	exec.SpawnAgent("agent-1", task, backend)
	exec.SpawnAgent("agent-2", task, backend)
	exec.SpawnAgent("agent-3", task, backend)

	result := exec.Agents()

	if len(result) != 3 {
		t.Errorf("expected 3 agents, got %d", len(result))
	}
	for _, id := range []string{"agent-1", "agent-2", "agent-3"} {
		if _, ok := result[id]; !ok {
			t.Errorf("expected agent %q in result", id)
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestExecutor_ConcurrentSpawnAndRead(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	const numGoroutines = 50
	var wg sync.WaitGroup

	// Spawn agents concurrently
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			id := "agent-" + string(rune('A'+idx%26)) + string(rune('a'+idx/26))
			exec.SpawnAgent(id, task, backend)
		}(i)
	}

	// Concurrently read agents while spawning
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = exec.Agents()
		}()
	}

	wg.Wait()

	// Should not panic and should have agents
	agents := exec.Agents()
	if len(agents) == 0 {
		t.Error("expected at least some agents after concurrent spawns")
	}
}

func TestExecutor_ConcurrentRemoveAndRead(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}
	backend := &testBackend{}

	// Pre-populate agents
	for i := 0; i < 20; i++ {
		id := "agent-" + string(rune('0'+i%10)) + string(rune('a'+i/10))
		exec.SpawnAgent(id, task, backend)
	}

	var wg sync.WaitGroup
	const numGoroutines = 30

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Some goroutines remove, some read
			if idx%2 == 0 {
				id := "agent-" + string(rune('0'+idx%10)) + string(rune('a'+(idx%20)/10))
				exec.RemoveAgent(id)
			} else {
				_ = exec.Agents()
			}
		}(i)
	}

	wg.Wait()
	// Should not panic
}

func TestExecutor_ConcurrentExecuteAndRemove(t *testing.T) {
	exec := New(newTestHarness(), tools.NewRegistry())
	ctx := context.Background()
	backend := &testBackend{chatResponse: "result"}
	task := types.TaskCreate{Directive: "CODE_AGENT", Task: "test"}

	exec.SpawnAgent("agent-1", task, backend)

	var wg sync.WaitGroup
	wg.Add(2)

	// Execute and remove concurrently
	go func() {
		defer wg.Done()
		_ = exec.Execute(ctx, "agent-1")
	}()

	go func() {
		defer wg.Done()
		exec.RemoveAgent("agent-1")
	}()

	wg.Wait()
	// Should not panic regardless of execution order
}
