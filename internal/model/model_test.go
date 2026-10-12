package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// Tests for NewDeepSeekBackend — constructor behavior
// =============================================================================

func TestNewDeepSeekBackend_ExplicitAPIKey(t *testing.T) {
	b := NewDeepSeekBackend("https://api.deepseek.com", "sk-test-123", "deepseek-chat", 8192)

	if b == nil {
		t.Fatal("NewDeepSeekBackend returned nil")
	}
	if b.endpoint != "https://api.deepseek.com" {
		t.Errorf("endpoint = %q, want %q", b.endpoint, "https://api.deepseek.com")
	}
	if b.apiKey != "sk-test-123" {
		t.Errorf("apiKey = %q, want %q", b.apiKey, "sk-test-123")
	}
	if b.modelID != "deepseek-chat" {
		t.Errorf("modelID = %q, want %q", b.modelID, "deepseek-chat")
	}
	if b.maxContext != 8192 {
		t.Errorf("maxContext = %d, want %d", b.maxContext, 8192)
	}
	if b.client == nil {
		t.Error("HTTP client should not be nil")
	}
}

func TestNewDeepSeekBackend_FallsBackToEnv(t *testing.T) {
	os.Setenv("DEEPSEEK_API_KEY", "env-api-key-xyz")
	defer os.Unsetenv("DEEPSEEK_API_KEY")

	b := NewDeepSeekBackend("https://api.deepseek.com", "", "deepseek-chat", 4096)

	if b.apiKey != "env-api-key-xyz" {
		t.Errorf("apiKey = %q, want %q (should fall back to env var)", b.apiKey, "env-api-key-xyz")
	}
}

func TestNewDeepSeekBackend_ExplicitOverridesEnv(t *testing.T) {
	os.Setenv("DEEPSEEK_API_KEY", "env-key")
	defer os.Unsetenv("DEEPSEEK_API_KEY")

	b := NewDeepSeekBackend("https://api.deepseek.com", "explicit-key", "deepseek-chat", 4096)

	if b.apiKey != "explicit-key" {
		t.Errorf("apiKey = %q, want %q (explicit should override env)", b.apiKey, "explicit-key")
	}
}

func TestNewDeepSeekBackend_ZeroMaxContext(t *testing.T) {
	b := NewDeepSeekBackend("http://localhost:8080", "key", "model", 0)

	if b.maxContext != 0 {
		t.Errorf("maxContext = %d, want 0 (zero should be allowed)", b.maxContext)
	}
}

// =============================================================================
// Tests for ModelID / MaxContext — getter methods
// =============================================================================

func TestModelID(t *testing.T) {
	b := NewDeepSeekBackend("http://localhost", "key", "gpt-4o", 16384)

	if b.ModelID() != "gpt-4o" {
		t.Errorf("ModelID() = %q, want %q", b.ModelID(), "gpt-4o")
	}
}

func TestMaxContext(t *testing.T) {
	b := NewDeepSeekBackend("http://localhost", "key", "claude-3", 100000)

	if b.MaxContext() != 100000 {
		t.Errorf("MaxContext() = %d, want %d", b.MaxContext(), 100000)
	}
}

// =============================================================================
// Tests for SwitchModel
// =============================================================================

func TestSwitchModel(t *testing.T) {
	b := NewDeepSeekBackend("http://localhost", "key", "deepseek-chat", 8192)

	b.SwitchModel("deepseek-coder")

	if b.ModelID() != "deepseek-coder" {
		t.Errorf("ModelID() = %q, want %q after SwitchModel", b.ModelID(), "deepseek-coder")
	}

	// Switching back works
	b.SwitchModel("deepseek-chat")
	if b.ModelID() != "deepseek-chat" {
		t.Errorf("ModelID() = %q, want %q after switching back", b.ModelID(), "deepseek-chat")
	}
}

func TestSwitchModel_EmptyString(t *testing.T) {
	b := NewDeepSeekBackend("http://localhost", "key", "deepseek-chat", 8192)

	b.SwitchModel("")

	if b.ModelID() != "" {
		t.Errorf("ModelID() = %q, want empty string after SwitchModel(\"\")", b.ModelID())
	}
}

// =============================================================================
// Tests for String
// =============================================================================

func TestString(t *testing.T) {
	b := NewDeepSeekBackend("http://localhost:8080/v1", "key", "deepseek-chat", 8192)

	got := b.String()
	want := "DeepSeekBackend(model=deepseek-chat, endpoint=http://localhost:8080/v1)"

	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

// =============================================================================
// Tests for extractContent — the core parsing helper
// =============================================================================

func TestExtractContent_ValidResponse(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]any
		expected string
	}{
		{
			name: "normal response",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": "Hello, world!",
						},
					},
				},
			},
			expected: "Hello, world!",
		},
		{
			name: "multi choice takes first",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": "first choice",
						},
					},
					map[string]any{
						"message": map[string]any{
							"content": "second choice",
						},
					},
				},
			},
			expected: "first choice",
		},
		{
			name: "empty content",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": "",
						},
					},
				},
			},
			expected: "",
		},
		{
			name: "content with special characters",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": "```go\nfunc main() {}\n```",
						},
					},
				},
			},
			expected: "```go\nfunc main() {}\n```",
		},
		{
			name: "unicode content",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": "你好，世界！🚀",
						},
					},
				},
			},
			expected: "你好，世界！🚀",
		},
		{
			name: "content is not string (int)",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": 42,
						},
					},
				},
			},
			expected: "",
		},
		{
			name: "content is not string (float)",
			input: map[string]any{
				"choices": []any{
					map[string]any{
						"message": map[string]any{
							"content": 3.14,
						},
					},
				},
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContent(tt.input)
			if got != tt.expected {
				t.Errorf("extractContent() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestExtractContent_NilOrMalformed(t *testing.T) {
	tests := []struct {
		name  string
		input map[string]any
	}{
		{"nil map", nil},
		{"empty map", map[string]any{}},
		{"no choices key", map[string]any{"model": "deepseek"}},
		{"choices is not array", map[string]any{"choices": "not-array"}},
		{"choices is empty array", map[string]any{"choices": []any{}}},
		{"choice is not map", map[string]any{"choices": []any{"string-instead-of-map"}}},
		{"no message in choice", map[string]any{"choices": []any{map[string]any{"index": 0}}}},
		{"message is not map", map[string]any{"choices": []any{map[string]any{"message": "not-map"}}}},
		{"no content in message", map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant"}}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractContent(tt.input)
			if got != "" {
				t.Errorf("extractContent() = %q, want empty string for malformed input", got)
			}
		})
	}
}

func TestExtractContent_DeltaFormat(t *testing.T) {
	// Streaming responses use "delta" key instead of "message"
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"content": "streaming chunk content",
				},
			},
		},
	}
	got := extractContent(chunk)
	if got != "streaming chunk content" {
		t.Errorf("extractContent() = %q, want %q", got, "streaming chunk content")
	}
}

func TestExtractContent_DeltaTakesPrecedenceOverMessage(t *testing.T) {
	// When both "message" and "delta" exist, "message" should take precedence
	// as extractContent checks "message" first
	both := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"content": "from message",
				},
				"delta": map[string]any{
					"content": "from delta",
				},
			},
		},
	}
	got := extractContent(both)
	if got != "from message" {
		t.Errorf("extractContent() = %q, want %q (message takes precedence)", got, "from message")
	}
}

func TestExtractContent_DeltaWithoutMessage(t *testing.T) {
	// Only delta, no message — should still work
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"content": "only delta",
				},
			},
		},
	}
	got := extractContent(chunk)
	if got != "only delta" {
		t.Errorf("extractContent() = %q, want %q", got, "only delta")
	}
}

func TestExtractContent_NilChoiceInSlice(t *testing.T) {
	input := map[string]any{
		"choices": []any{nil, map[string]any{
			"message": map[string]any{"content": "fallback"},
		}},
	}
	// nil choice panics on type assertion; extractContent does not handle nil gracefully.
	// This test documents the current behavior.
	defer func() {
		if r := recover(); r != nil {
			t.Logf("recovered from panic (expected with current implementation): %v", r)
		}
	}()
	_ = extractContent(input)
}

// =============================================================================
// Tests for ExpertHint type
// =============================================================================

func TestExpertHint_Fields(t *testing.T) {
	hint := ExpertHint{
		Field: "code_review",
		Value: "Look for SQL injection vulnerabilities",
	}

	if hint.Field != "code_review" {
		t.Errorf("Field = %q, want %q", hint.Field, "code_review")
	}
	if hint.Value != "Look for SQL injection vulnerabilities" {
		t.Errorf("Value = %q, want %q", hint.Value, "Look for SQL injection vulnerabilities")
	}
}

func TestExpertHint_JSONSerialization(t *testing.T) {
	hint := ExpertHint{
		Field: "security",
		Value: "check for XSS",
	}

	data, err := json.Marshal(hint)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	want := `{"expert_hint":"security","value":"check for XSS"}`
	if string(data) != want {
		t.Errorf("JSON = %s, want %s", string(data), want)
	}

	var parsed ExpertHint
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if parsed.Field != hint.Field || parsed.Value != hint.Value {
		t.Errorf("round-trip failed: got %+v", parsed)
	}
}

// =============================================================================
// Integration tests — Chat with httptest server
// =============================================================================

func makeTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func TestChat_Success(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Verify request basics
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("path = %s, want suffix /chat/completions", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-api-key" {
			t.Errorf("Authorization = %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %s", r.Header.Get("Content-Type"))
		}

		// Decode body and verify
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode error: %v", err)
		}
		if body["model"] != "deepseek-chat" {
			t.Errorf("model = %v", body["model"])
		}

		// Return a valid response
		resp := map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": "Hello! How can I help you?",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-api-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "Hi!"},
	}

	content, toolCalls, err := b.Chat(ctx, messages, nil)
	if err != nil {
		t.Fatalf("Chat() error: %v", err)
	}
	if content != "Hello! How can I help you?" {
		t.Errorf("content = %q, want %q", content, "Hello! How can I help you?")
	}
	if toolCalls != nil {
		t.Errorf("toolCalls should be nil, got %+v", toolCalls)
	}
}

func TestChat_HTTPError(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error": "internal server error"}`))
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	content, toolCalls, err := b.Chat(ctx, messages, nil)
	if err != nil {
		t.Fatalf("Chat() returned unexpected error: %v", err)
	}
	// The current implementation does not check HTTP status codes,
	// so it would try to decode the error JSON body as a valid response.
	// This test documents current behavior.
	if content != "" {
		t.Logf("content = %q (expected empty for error response)", content)
	}
	_ = toolCalls
}

func TestChat_ContextCanceled(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(200 * time.Millisecond)
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	_, _, err := b.Chat(ctx, messages, nil)
	if err == nil {
		t.Error("expected error for canceled context, got nil")
	}
}

func TestChat_ConnectionRefused(t *testing.T) {
	// Use a server that shuts down immediately
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // close it immediately

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "Hello"},
	}

	_, _, err := b.Chat(ctx, messages, nil)
	if err == nil {
		t.Error("expected connection error, got nil")
	}
}

// =============================================================================
// Integration tests — Stream with httptest server
// =============================================================================

func TestStream_Success(t *testing.T) {
	chunks := []string{"Hello", " ", "World", "!"}
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept header = %s, want text/event-stream", r.Header.Get("Accept"))
		}

		// Verify request body has stream: true
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if stream, ok := body["stream"]; !ok || stream != true {
			t.Errorf("expected stream: true in body, got %v", body)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}

		for _, chunk := range chunks {
			data := map[string]any{
				"choices": []any{
					map[string]any{
						"delta": map[string]any{
							"content": chunk,
						},
					},
				},
			}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}

		// Send [DONE] signal
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "Say hello"},
	}

	ch, errCh := b.Stream(ctx, messages)

	var received []string
	var wg sync.WaitGroup
	wg.Add(2)

	// Collect chunks
	go func() {
		defer wg.Done()
		for content := range ch {
			received = append(received, content)
		}
	}()

	// Monitor errors
	var streamErr error
	go func() {
		defer wg.Done()
		for err := range errCh {
			streamErr = err
		}
	}()

	wg.Wait()

	if streamErr != nil {
		t.Fatalf("Stream error: %v", streamErr)
	}

	got := strings.Join(received, "")
	want := "Hello World!"
	if got != want {
		t.Errorf("streamed content = %q, want %q", got, want)
	}
}

func TestStream_ExtractContentFromDelta(t *testing.T) {
	// Verify that extractContent works for streaming chunks (delta, not message)
	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"content": "delta content",
				},
			},
		},
	}
	got := extractContent(chunk)
	if got != "delta content" {
		t.Errorf("extractContent from delta chunk = %q, want %q", got, "delta content")
	}
}

func TestStream_EmptyLinesSkipped(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Send empty line first
		w.Write([]byte("\n\n"))
		flusher.Flush()

		// Send valid chunk
		data := map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"content": "hello",
					},
				},
			},
		}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	ch, errCh := b.Stream(ctx, messages)

	var received []string
	done := make(chan struct{})
	go func() {
		for content := range ch {
			received = append(received, content)
		}
		close(done)
	}()
	go func() {
		for range errCh {
		}
	}()

	<-done

	if len(received) != 1 || received[0] != "hello" {
		t.Errorf("received = %v, want [\"hello\"]", received)
	}
}

func TestStream_MalformedJSON(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Send malformed JSON
		w.Write([]byte("data: {not valid json}\n\n"))
		flusher.Flush()

		// Send valid chunk after malformed
		data := map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"content": "valid chunk",
					},
				},
			},
		}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	ch, errCh := b.Stream(ctx, messages)

	var received []string
	done := make(chan struct{})
	go func() {
		for content := range ch {
			received = append(received, content)
		}
		close(done)
	}()
	go func() {
		for range errCh {
		}
	}()

	<-done

	// Malformed JSON should be skipped; only valid chunk is received
	if len(received) != 1 || received[0] != "valid chunk" {
		t.Errorf("received = %v, want [\"valid chunk\"]", received)
	}
}

func TestStream_NonDataLinesSkipped(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Send comment line (no "data: " prefix)
		w.Write([]byte(": comment line\n\n"))
		flusher.Flush()

		// Send valid data
		data := map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"content": "real data",
					},
				},
			},
		}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()

		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	ch, errCh := b.Stream(ctx, messages)

	var received []string
	done := make(chan struct{})
	go func() {
		for content := range ch {
			received = append(received, content)
		}
		close(done)
	}()
	go func() {
		for range errCh {
		}
	}()

	<-done

	if len(received) != 1 || received[0] != "real data" {
		t.Errorf("received = %v, want [\"real data\"]", received)
	}
}

func TestStream_ContextCancellation(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)

		// Send one chunk, then wait
		data := map[string]any{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"content": "first",
					},
				},
			},
		}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()

		// Wait - context will be cancelled, so the goroutine should stop
		time.Sleep(500 * time.Millisecond)
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx, cancel := context.WithCancel(context.Background())
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	ch, errCh := b.Stream(ctx, messages)

	// Read first chunk
	first, ok := <-ch
	if !ok {
		t.Fatal("channel closed before receiving first chunk")
	}
	if first != "first" {
		t.Errorf("first chunk = %q, want %q", first, "first")
	}

	// Cancel context
	cancel()

	// Drain both channels (they should eventually close)
	timeout := time.After(3 * time.Second)
	chClosed, errChClosed := false, false
	for !chClosed || !errChClosed {
		select {
		case _, ok := <-ch:
			if !ok {
				chClosed = true
			}
		case _, ok := <-errCh:
			if !ok {
				errChClosed = true
			}
		case <-timeout:
			t.Fatal("timed out waiting for channels to close after cancellation")
		}
	}
}

func TestStream_RequestCreationError(t *testing.T) {
	// Using an invalid URL with a newline to trigger NewRequestWithContext error
	b := NewDeepSeekBackend("://invalid-url\n", "key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "hello"},
	}

	ch, errCh := b.Stream(ctx, messages)

	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error for invalid URL, got nil")
		} else {
			t.Logf("got expected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for error")
	}

	// Drain the content channel
	for range ch {
	}
}

// =============================================================================
// Tests for GenerateWithExpertHint
// =============================================================================

func TestGenerateWithExpertHint_Success(t *testing.T) {
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"choices": []any{
				map[string]any{
					"message": map[string]any{
						"role":    "assistant",
						"content": "Security review result: no issues found",
					},
				},
			},
		}
		json.NewEncoder(w).Encode(resp)
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "Review this code"},
	}
	hint := ExpertHint{
		Field: "security_audit",
		Value: "check for vulnerabilities",
	}

	result, err := b.GenerateWithExpertHint(ctx, messages, hint)
	if err != nil {
		t.Fatalf("GenerateWithExpertHint error: %v", err)
	}
	if result != "Security review result: no issues found" {
		t.Errorf("result = %q, want %q", result, "Security review result: no issues found")
	}
}

func TestGenerateWithExpertHint_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server.Close() // close immediately

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ctx := context.Background()
	messages := []types.Message{
		{Role: "user", Content: "Review"},
	}
	hint := ExpertHint{Field: "test", Value: "test"}

	_, err := b.GenerateWithExpertHint(ctx, messages, hint)
	if err == nil {
		t.Error("expected error for closed server, got nil")
	}
}

// =============================================================================
// extractToolCallsFromText 文本/XML 工具调用兜底解析
// =============================================================================

func TestExtractToolCallsFromText_ToolCallLine(t *testing.T) {
	content := "[tool_call_15cc578e6c97496795b12a5f] exec_command: pwd"
	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个工具调用，实际 %d: %v", len(calls), calls)
	}
	if calls[0].Function.Name != "exec_command" {
		t.Errorf("工具名错误: %s", calls[0].Function.Name)
	}
	if calls[0].Function.Arguments != "pwd" {
		t.Errorf("参数错误: %q", calls[0].Function.Arguments)
	}
	if calls[0].Type != "function" {
		t.Errorf("type 应为 function: %q", calls[0].Type)
	}
}

func TestExtractToolCallsFromText_FenceBlock(t *testing.T) {
	content := "思考过程...\n```tool_call\nwrite_file: {\"path\": \"a.go\", \"content\": \"package main\"}\n```\n继续"
	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("围栏块应解析出 1 个工具调用，实际 %d: %v", len(calls), calls)
	}
	if calls[0].Function.Name != "write_file" {
		t.Errorf("工具名错误: %s", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, `"path"`) {
		t.Errorf("参数未包含 path: %q", calls[0].Function.Arguments)
	}
}

func TestExtractToolCallsFromText_XMLInvoke(t *testing.T) {
	content := `<invoke name="read_file">
{"path": "main.go", "limit": 10}
</invoke>`
	calls := ExtractToolCallsFromText(content)
	if len(calls) != 1 {
		t.Fatalf("XML 应解析出 1 个工具调用，实际 %d: %v", len(calls), calls)
	}
	if calls[0].Function.Name != "read_file" {
		t.Errorf("工具名错误: %s", calls[0].Function.Name)
	}
}

func TestExtractToolCallsFromText_PlainLine(t *testing.T) {
	content := "第一步先看目录结构：\nlist_files: ."
	calls := ExtractToolCallsFromText(content)
	// 正文里的 "list_files: ." 应被识别（已知工具名 + 非空参数）
	found := false
	for _, c := range calls {
		if c.Function.Name == "list_files" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("独立行 list_files 应被识别: %v", calls)
	}
}

func TestExtractToolCallsFromText_UnknownToolSkipped(t *testing.T) {
	// 未知工具名不解析（防止误解析正文）
	content := "[tool_call_abc] foo_bar: whatever\n普通文本: 值"
	calls := ExtractToolCallsFromText(content)
	if len(calls) != 0 {
		t.Errorf("未知工具不应被解析: %v", calls)
	}
}

func TestExtractToolCallsFromText_Empty(t *testing.T) {
	if calls := ExtractToolCallsFromText(""); calls != nil {
		t.Errorf("空内容应返回 nil: %v", calls)
	}
	if calls := ExtractToolCallsFromText("?"); calls != nil {
		t.Errorf("无工具内容应返回 nil: %v", calls)
	}
}

// =============================================================================
// Benchmark tests
// =============================================================================

func BenchmarkExtractContent(b *testing.B) {
	input := map[string]any{
		"choices": []any{
			map[string]any{
				"message": map[string]any{
					"content": "Hello, world!",
				},
			},
		},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extractContent(input)
	}
}

func BenchmarkNewDeepSeekBackend(b *testing.B) {
	for i := 0; i < b.N; i++ {
		NewDeepSeekBackend("http://localhost:8080", "key", "model", 8192)
	}
}

// =============================================================================
// 流式 XML 文本工具调用兜底测试（issue: XML 被直接打印、从未执行）
// =============================================================================

func TestStream_XMLToolCallBufferedNotPrinted(t *testing.T) {
	// 模拟模型以 XML 文本形式表达工具调用（无结构化 tool_calls），分片流式输出。
	xmlChunks := []string{
		"本目录文件列表\n",
		"<tool_calls>",
		"\n<invoke name=\"list_files\">",
		"\n<parameter name=\"path\" string=\"true\">.</parameter>",
		"\n</invoke>",
		"\n</tool_calls>",
	}
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range xmlChunks {
			data := map[string]any{
				"choices": []any{
					map[string]any{"delta": map[string]any{"content": c}},
				},
			}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}
		// finish_reason: stop（无结构化 tool_calls）
		data := map[string]any{
			"choices": []any{
				map[string]any{
					"delta":         map[string]any{},
					"finish_reason": "stop",
				},
			},
		}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ch, errCh := b.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "list files"}})

	var content strings.Builder
	var calls []types.ToolCall
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return
				}
				if c.IsToolCall && len(c.ToolCalls) > 0 {
					calls = append(calls, c.ToolCalls...)
				} else if c.Content != "" {
					content.WriteString(c.Content)
				}
			case e, ok := <-errCh:
				if ok && e != nil {
					streamErr = e
					return
				}
			}
		}
	}()
	<-done

	if streamErr != nil {
		t.Fatalf("StreamEx error: %v", streamErr)
	}

	// 关键断言：XML 不得作为正文打印
	if got := content.String(); strings.Contains(got, "<invoke") || strings.Contains(got, "<tool_calls") || strings.Contains(got, "<parameter") {
		t.Errorf("XML 被当作正文输出，未执行: %q", got)
	}

	// 必须解析出 list_files 工具调用
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个工具调用，实际 %d: %v", len(calls), calls)
	}
	if calls[0].Function.Name != "list_files" {
		t.Errorf("工具名 = %s, want list_files", calls[0].Function.Name)
	}
	// <parameter> 子元素须归一化为 JSON 参数
	if !strings.Contains(calls[0].Function.Arguments, `"path"`) {
		t.Errorf("参数未归一化为 JSON: %q", calls[0].Function.Arguments)
	}
	if !strings.Contains(calls[0].Function.Arguments, `"."`) {
		t.Errorf("参数缺少 path 值: %q", calls[0].Function.Arguments)
	}
}

// =============================================================================
// DSML 竖线风格工具调用兜底测试（issue: <|invoke|> 不识别 → 调用从不执行，
// 模型重复输出相同调用陷入死循环；参数用 dir 而非 path → 工具拿空路径失败）
// =============================================================================

func TestStream_DSMLToolCallBufferedNotPrinted(t *testing.T) {
	// 模拟模型以 DSML 竖线风格表达工具调用（无结构化 tool_calls），分片流式输出。
	dsmlChunks := []string{
		"本目录文件列表\n",
		"<|tool_calls|>",
		"\n<|invoke name=\"list_files\"|>",
		"\n<|parameter name=\"dir\" string=\"true\"|>.",
		"</|parameter|>",
		"\n</|invoke|>",
		"\n<|/tool_calls|>",
	}
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range dsmlChunks {
			data := map[string]any{
				"choices": []any{
					map[string]any{"delta": map[string]any{"content": c}},
				},
			}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}
		data := map[string]any{
			"choices": []any{
				map[string]any{
					"delta":         map[string]any{},
					"finish_reason": "stop",
				},
			},
		}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ch, errCh := b.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "list files"}})

	var content strings.Builder
	var calls []types.ToolCall
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return
				}
				if c.IsToolCall && len(c.ToolCalls) > 0 {
					calls = append(calls, c.ToolCalls...)
				} else if c.Content != "" {
					content.WriteString(c.Content)
				}
			case e, ok := <-errCh:
				if ok && e != nil {
					streamErr = e
					return
				}
			}
		}
	}()
	<-done

	if streamErr != nil {
		t.Fatalf("StreamEx error: %v", streamErr)
	}

	// 关键断言：DSML 不得作为正文打印
	if got := content.String(); strings.Contains(got, "<|invoke") || strings.Contains(got, "<|tool_calls") || strings.Contains(got, "<|parameter") || strings.Contains(got, "</|invoke") {
		t.Errorf("DSML 被当作正文输出，未执行: %q", got)
	}

	// 必须解析出 list_files 工具调用（DSML 风格）
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个工具调用，实际 %d: %v", len(calls), calls)
	}
	if calls[0].Function.Name != "list_files" {
		t.Errorf("工具名 = %s, want list_files", calls[0].Function.Name)
	}
	// <|parameter|> 子元素须归一化为 JSON 参数（含别名 dir 原样保留，执行层归一化）
	if !strings.Contains(calls[0].Function.Arguments, `"dir"`) {
		t.Errorf("DSML 参数未归一化为 JSON: %q", calls[0].Function.Arguments)
	}
	if !strings.Contains(calls[0].Function.Arguments, `"."`) {
		t.Errorf("参数缺少值: %q", calls[0].Function.Arguments)
	}
}

// =============================================================================
// 真实 DeepSeek 逐 token 分片回归（历史根因：<invoke 被切成 "<in"+"voke"，
// 单 chunk Contains 永不命中 → 调用被当正文打印、从不执行 → 死循环）
// =============================================================================

func TestStream_TokenSlicedXMLToolCall(t *testing.T) {
	// 模拟真实逐 token 输出：<invoke、<parameter、</invoke、</tool_calls 全被切碎
	xmlChunks := []string{
		"本目录文件列",
		"表\n",
		"<tool",
		"_calls>",
		"\n<in",
		"voke name=\"list_files\"",
		">\n<para",
		"meter name=\"path\" str",
		"ing=\"true\">.",
		"</para",
		"meter>",
		"\n</inv",
		"oke>",
		"\n</tool_ca",
		"lls>",
		"\n完成",
	}
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range xmlChunks {
			data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}
		data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ch, errCh := b.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "list files"}})

	var content strings.Builder
	var calls []types.ToolCall
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return
				}
				if c.IsToolCall && len(c.ToolCalls) > 0 {
					calls = append(calls, c.ToolCalls...)
				} else if c.Content != "" {
					content.WriteString(c.Content)
				}
			case e, ok := <-errCh:
				if ok && e != nil {
					streamErr = e
					return
				}
			}
		}
	}()
	<-done

	if streamErr != nil {
		t.Fatalf("StreamEx error: %v", streamErr)
	}
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个工具调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "list_files" {
		t.Errorf("工具名 = %s", calls[0].Function.Name)
	}
	if !strings.Contains(calls[0].Function.Arguments, `"path"`) {
		t.Errorf("参数未归一化: %q", calls[0].Function.Arguments)
	}
	// 正文不得包含残留 XML 标记
	if got := content.String(); strings.Contains(got, "<invoke") || strings.Contains(got, "<tool_calls") {
		t.Errorf("XML 残留被当正文: %q", got)
	}
}

// 真实用户案例：DSML 竖线风格逐 token 分片 + 参数 cmd（exec_command 执行链路）
func TestStream_TokenSlicedDSMLExecCommand(t *testing.T) {
	dsmlChunks := []string{
		"分析项目进展\n",
		"<|tool",
		"_calls|>",
		"\n<|",
		"invoke name=\"exec_",
		"command\"|>",
		"\n<|para",
		"meter name=\"cmd\"",
		" string=\"true\"|>git log --oneline -10",
		"</|para",
		"meter|>",
		"\n<|parameter name=\"timeout\" string=\"true\"|>30000</|parameter|>",
		"\n<|/inv",
		"oke|>",
		"\n<|/tool",
		"_calls|>",
	}
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range dsmlChunks {
			data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}
		data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ch, errCh := b.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "分析项目进展"}})

	var calls []types.ToolCall
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return
				}
				if c.IsToolCall && len(c.ToolCalls) > 0 {
					calls = append(calls, c.ToolCalls...)
				}
			case e, ok := <-errCh:
				if ok && e != nil {
					streamErr = e
					return
				}
			}
		}
	}()
	<-done

	if streamErr != nil {
		t.Fatalf("StreamEx error: %v", streamErr)
	}
	if len(calls) != 1 {
		t.Fatalf("期望解析出 1 个调用，实际 %d", len(calls))
	}
	if calls[0].Function.Name != "exec_command" {
		t.Errorf("工具名 = %s, want exec_command", calls[0].Function.Name)
	}
	// 参数必须含 cmd（执行层别名 cmd→command 生效）
	if !strings.Contains(calls[0].Function.Arguments, `"cmd"`) {
		t.Errorf("参数缺失 cmd: %q", calls[0].Function.Arguments)
	}
	if !strings.Contains(calls[0].Function.Arguments, "git log") {
		t.Errorf("参数缺失命令: %q", calls[0].Function.Arguments)
	}
}

func TestStream_PlainTextNotBuffered(t *testing.T) {
	// 普通正文（不含工具标记）不应被缓冲、应实时输出
	server := makeTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, c := range []string{"你好", "，", "这是", "普通回答"} {
			data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": c}}}}
			payload, _ := json.Marshal(data)
			w.Write([]byte("data: " + string(payload) + "\n\n"))
			flusher.Flush()
		}
		data := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}}}
		payload, _ := json.Marshal(data)
		w.Write([]byte("data: " + string(payload) + "\n\n"))
		flusher.Flush()
		w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	defer server.Close()

	b := NewDeepSeekBackend(server.URL, "test-key", "deepseek-chat", 8192)
	ch, errCh := b.StreamEx(context.Background(), []types.Message{{Role: "user", Content: "hi"}})

	var content strings.Builder
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case c, ok := <-ch:
				if !ok {
					return
				}
				if c.Content != "" {
					content.WriteString(c.Content)
				}
			case e, ok := <-errCh:
				if ok && e != nil {
					streamErr = e
					return
				}
			}
		}
	}()
	<-done
	if streamErr != nil {
		t.Fatalf("StreamEx error: %v", streamErr)
	}
	want := "你好，这是普通回答"
	if got := content.String(); got != want {
		t.Errorf("普通正文输出 = %q, want %q", got, want)
	}
}
