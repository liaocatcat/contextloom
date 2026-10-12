package types

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

// ── Message 测试 ──

func TestMessageTextContent_String(t *testing.T) {
	m := Message{Role: "user", Content: "hello world"}
	got := m.TextContent()
	if got != "hello world" {
		t.Errorf("TextContent() = %q, want %q", got, "hello world")
	}
}

func TestMessageTextContent_ContentParts(t *testing.T) {
	m := Message{Role: "assistant", Content: []ContentPart{
		{Type: "text", Text: "hello "},
		{Type: "text", Text: "world"},
	}}
	got := m.TextContent()
	if got != "hello world" {
		t.Errorf("TextContent() = %q, want %q", got, "hello world")
	}
}

func TestMessageTextContent_NilContent(t *testing.T) {
	m := Message{Role: "user"}
	got := m.TextContent()
	if got != "" {
		t.Errorf("TextContent() = %q, want empty", got)
	}
}

func TestMessageTextContent_IntContent(t *testing.T) {
	m := Message{Role: "user", Content: 42}
	got := m.TextContent()
	if got != "" {
		t.Errorf("TextContent() = %q, want empty", got)
	}
}

func TestMessageSetTextContent(t *testing.T) {
	m := Message{Role: "user"}
	m.SetTextContent("new text")
	got := m.TextContent()
	if got != "new text" {
		t.Errorf("SetTextContent: got %q, want %q", got, "new text")
	}
}

func TestNewTextMessage(t *testing.T) {
	m := NewTextMessage("user", "hello")
	if m.Role != "user" {
		t.Errorf("Role = %q, want user", m.Role)
	}
	if m.TextContent() != "hello" {
		t.Errorf("Content = %q, want hello", m.TextContent())
	}
}

// ── ToolCall 测试 ──

func TestToolCallFuncName(t *testing.T) {
	tc := ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: FunctionCall{
			Name:      "read_file",
			Arguments: `{"path":"/tmp/test"}`,
		},
	}
	if tc.FuncName() != "read_file" {
		t.Errorf("FuncName() = %q, want read_file", tc.FuncName())
	}
}

func TestToolCallFuncArgs(t *testing.T) {
	tc := ToolCall{
		ID:   "call_2",
		Type: "function",
		Function: FunctionCall{
			Name:      "write_file",
			Arguments: `{"path":"/tmp/test","content":"hello"}`,
		},
	}
	if tc.FuncArgs() != `{"path":"/tmp/test","content":"hello"}` {
		t.Errorf("FuncArgs() = %q", tc.FuncArgs())
	}
}

func TestNewToolCall(t *testing.T) {
	tc := NewToolCall("id1", "test_func", `{"key":"value"}`)
	if tc.ID != "id1" {
		t.Errorf("ID = %q, want id1", tc.ID)
	}
	if tc.FuncName() != "test_func" {
		t.Errorf("FuncName() = %q, want test_func", tc.FuncName())
	}
	if tc.FuncArgs() != `{"key":"value"}` {
		t.Errorf("FuncArgs() = %q", tc.FuncArgs())
	}
}

// ── DomainError 测试 ──

func TestDomainError_Error(t *testing.T) {
	de := &DomainError{Code: ErrConfig, Message: "配置加载失败"}
	got := de.Error()
	want := "[CONFIG_ERROR] 配置加载失败"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDomainError_ErrorWithCause(t *testing.T) {
	cause := errors.New("原始错误")
	de := NewDomainError(ErrModelTimeout, "模型超时", cause)
	got := de.Error()
	want := "[MODEL_TIMEOUT] 模型超时: 原始错误"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestDomainError_Unwrap(t *testing.T) {
	cause := errors.New("原始错误")
	de := NewDomainError(ErrToolExec, "工具执行失败", cause)
	unwrapped := de.Unwrap()
	if unwrapped != cause {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, cause)
	}
}

func TestDomainError_UnwrapNil(t *testing.T) {
	de := &DomainError{Code: ErrUnknown, Message: "未知错误"}
	unwrapped := de.Unwrap()
	if unwrapped != nil {
		t.Errorf("Unwrap() = %v, want nil", unwrapped)
	}
}

func TestNewDomainError(t *testing.T) {
	de := NewDomainError(ErrValidation, "验证失败", nil)
	if de.Code != ErrValidation {
		t.Errorf("Code = %q, want VALIDATION_ERROR", de.Code)
	}
	if de.Message != "验证失败" {
		t.Errorf("Message = %q, want 验证失败", de.Message)
	}
}

// ── Result 泛型测试 ──

func TestResultOk(t *testing.T) {
	r := Ok(42)
	if !r.IsOk() {
		t.Error("IsOk() = false, want true")
	}
	if r.IsErr() {
		t.Error("IsErr() = true, want false")
	}
	v, err := r.Unwrap()
	if v != 42 {
		t.Errorf("Unwrap value = %v, want 42", v)
	}
	if err != nil {
		t.Errorf("Unwrap error = %v, want nil", err)
	}
}

func TestResultErr(t *testing.T) {
	e := errors.New("some error")
	r := Err[int](e)
	if r.IsOk() {
		t.Error("IsOk() = true, want false")
	}
	if !r.IsErr() {
		t.Error("IsErr() = false, want true")
	}
	_, err := r.Unwrap()
	if err != e {
		t.Errorf("Unwrap error = %v, want %v", err, e)
	}
}

func TestResultStringOk(t *testing.T) {
	r := Ok("hello")
	if !r.IsOk() {
		t.Error("string result IsOk() = false")
	}
	v, _ := r.Unwrap()
	if v != "hello" {
		t.Errorf("value = %q, want hello", v)
	}
}

func TestResultStringErr(t *testing.T) {
	r := Err[string](errors.New("fail"))
	if !r.IsErr() {
		t.Error("string result IsErr() = false")
	}
	_, err := r.Unwrap()
	if err == nil {
		t.Error("expected error, got nil")
	}
}

func TestResultStructOk(t *testing.T) {
	type Data struct {
		Name string
		Age  int
	}
	r := Ok(Data{Name: "test", Age: 30})
	v, err := r.Unwrap()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if v.Name != "test" || v.Age != 30 {
		t.Errorf("value = %+v, want {Name:test Age:30}", v)
	}
}

// ── ErrorCode 常量验证 ──

func TestErrorCodeConstants(t *testing.T) {
	codes := []ErrorCode{
		ErrUnknown, ErrConfig, ErrModel, ErrModelTimeout,
		ErrModelRateLimit, ErrToolNotFound, ErrToolExec, ErrToolTimeout,
		ErrHarnessSearch, ErrMemoryStore, ErrMemoryLoad, ErrSessionNotFound,
		ErrAgentNotFound, ErrAgentTimeout, ErrOverseerCheat, ErrOverseerFail,
		ErrACPDisconnected, ErrACPSendFail, ErrPluginLoad, ErrSandbox,
		ErrValidation,
	}
	seen := make(map[ErrorCode]bool)
	for _, c := range codes {
		if c == "" {
			t.Error("found empty ErrorCode")
		}
		if seen[c] {
			t.Errorf("duplicate ErrorCode: %q", c)
		}
		seen[c] = true
	}
}

// ── TaskDirective 常量 ──

func TestTaskDirectiveConstants(t *testing.T) {
	if DirectiveCodeAgent != "CODE_AGENT" {
		t.Errorf("DirectiveCodeAgent = %q, want CODE_AGENT", DirectiveCodeAgent)
	}
	if DirectiveReview != "REVIEW" {
		t.Errorf("DirectiveReview = %q, want REVIEW", DirectiveReview)
	}
	if DirectivePlan != "PLAN" {
		t.Errorf("DirectivePlan = %q, want PLAN", DirectivePlan)
	}
	if DirectiveExecute != "EXECUTE" {
		t.Errorf("DirectiveExecute = %q, want EXECUTE", DirectiveExecute)
	}
}

// ── PluginStage 常量 ──

func TestPluginStageConstants(t *testing.T) {
	if StagePre != "pre" {
		t.Errorf("StagePre = %q, want pre", StagePre)
	}
	if StageDuring != "during" {
		t.Errorf("StageDuring = %q, want during", StageDuring)
	}
	if StagePost != "post" {
		t.Errorf("StagePost = %q, want post", StagePost)
	}
}

// ── ContextPolicy 常量 ──

func TestContextPolicyConstants(t *testing.T) {
	policies := []ContextPolicy{PolicyFullContext, PolicyRecentOnly, PolicyMinimal, PolicyNone}
	for _, p := range policies {
		if p == "" {
			t.Error("empty ContextPolicy")
		}
	}
}

// ── TaskStatus 常量 ──

func TestTaskStatusConstants(t *testing.T) {
	statuses := []TaskStatus{TaskPending, TaskRunning, TaskCompleted, TaskFailed, TaskCancelled}
	for _, s := range statuses {
		if s == "" {
			t.Error("empty TaskStatus")
		}
	}
}

// ── AgentStatus 常量 ──

func TestAgentStatusConstants(t *testing.T) {
	statuses := []AgentStatus{AgentIdle, AgentRunning, AgentWaiting, AgentCompleted, AgentError, AgentCancelled}
	for _, s := range statuses {
		if s == "" {
			t.Error("empty AgentStatus")
		}
	}
}

// ── ACP 消息类型常量 ──

func TestACPTypeConstants(t *testing.T) {
	types := []string{
		ACPTypeTaskCreate, ACPTypeTaskAccepted, ACPTypeContextReady,
		ACPTypeContextScoreChg, ACPTypeTaskProgress, ACPTypeTokenStream,
		ACPTypeToolCall, ACPTypeToolResult, ACPTypeCommand,
		ACPTypeSync, ACPTypeAck,
	}
	for _, tp := range types {
		if tp == "" {
			t.Error("empty ACP message type")
		}
	}
}

// ── JSON 序列化测试 ──

func TestMessageJSONRoundtrip(t *testing.T) {
	m := Message{Role: "user", Content: "hello"}
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m2 Message
	if err := json.Unmarshal(data, &m2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m2.Role != "user" || m2.TextContent() != "hello" {
		t.Errorf("roundtrip mismatch: %+v", m2)
	}
}

func TestToolCallJSONRoundtrip(t *testing.T) {
	tc := ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: FunctionCall{
			Name:      "read_file",
			Arguments: `{"path":"/tmp"}`,
		},
	}
	data, err := json.Marshal(tc)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var tc2 ToolCall
	if err := json.Unmarshal(data, &tc2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if tc2.FuncName() != "read_file" {
		t.Errorf("FuncName = %q, want read_file", tc2.FuncName())
	}
}

func TestACPMessageJSONRoundtrip(t *testing.T) {
	payload := json.RawMessage(`{"directive":"CODE_AGENT","task":"BUG_FIX"}`)
	msg := ACPMessage{
		Type:    ACPTypeTaskCreate,
		ID:      "msg_1",
		Seq:     1,
		Payload: payload,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var msg2 ACPMessage
	if err := json.Unmarshal(data, &msg2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if msg2.Type != ACPTypeTaskCreate || msg2.ID != "msg_1" || msg2.Seq != 1 {
		t.Errorf("roundtrip mismatch: %+v", msg2)
	}
}

// ── 并发安全测试 ──

func TestResultConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := Ok("test")
			if !r.IsOk() {
				t.Error("concurrent IsOk failed")
			}
		}()
	}
	wg.Wait()
}

func TestDomainErrorConcurrentCreation(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			de := NewDomainError(ErrConfig, "并发测试", nil)
			if de.Code != ErrConfig {
				t.Errorf("concurrent Code mismatch: %q", de.Code)
			}
		}()
	}
	wg.Wait()
}

// ── WithoutImages（多模态剥离，纯文本模型防 400）──

func TestMessageWithoutImages_StripsAndKeepsText(t *testing.T) {
	msg := Message{Role: "user", Content: []ContentPart{
		{Type: "text", Text: "请看这张图"},
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "data:image/png;base64,QUJD"}},
	}}
	out, changed := msg.WithoutImages()
	if !changed {
		t.Fatal("含图片片段应返回 changed=true")
	}
	got := out.TextContent()
	if got != "请看这张图\n"+ImageStrippedPlaceholder {
		t.Errorf("剥离后文本 = %q", got)
	}
	if _, ok := out.Content.(string); !ok {
		t.Errorf("剥离后 Content 应为纯字符串，实际 %T", out.Content)
	}
	// 原消息不被修改
	if msg.ImagePartCount() != 1 {
		t.Errorf("原消息应保留图片片段")
	}
}

func TestMessageWithoutImages_ImagesOnly(t *testing.T) {
	msg := Message{Role: "user", Content: []ContentPart{
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "data:image/png;base64,QUJD"}},
		{Type: "image_url", ImageURL: &ImageURLPart{URL: "data:image/jpeg;base64,QUJD"}},
	}}
	out, changed := msg.WithoutImages()
	if !changed {
		t.Fatal("纯图片消息应 changed=true")
	}
	if got := out.TextContent(); got != ImageStrippedPlaceholder+"\n"+ImageStrippedPlaceholder {
		t.Errorf("纯图片剥离后 = %q", got)
	}
}

func TestMessageWithoutImages_PlainTextUnchanged(t *testing.T) {
	msg := Message{Role: "user", Content: "普通文本"}
	if out, changed := msg.WithoutImages(); changed || out.Content != "普通文本" {
		t.Errorf("纯文本应 unchanged")
	}
	// []any 形态（JSON 反序列化产物）也支持
	anyMsg := Message{Role: "user", Content: []any{
		map[string]any{"type": "text", "text": "abc"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64,QUJD"}},
	}}
	out, changed := anyMsg.WithoutImages()
	if !changed {
		t.Fatal("[]any 形态多模态应 changed=true")
	}
	if got := out.TextContent(); got != "abc\n"+ImageStrippedPlaceholder {
		t.Errorf("[]any 剥离后 = %q", got)
	}
}
