package model

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// chainMock 可编程组合测试后端：按配置返回成功/失败，支持流式首块前/后失败，
// 记录收到的参数 setter 调用（验证广播）。
type chainMock struct {
	name string

	chatErr  error
	chatText string

	// streamChunks 先产出这些 chunk，再按 streamErr 结束（nil=正常结束）。
	streamChunks []StreamChunk
	streamErr    error

	mu       sync.Mutex
	temp     float64
	topP     float64
	effort   string
	toolsSet bool
	calls    int
}

func (m *chainMock) Chat(context.Context, []types.Message, []types.ToolDefinition) (string, []types.ToolCall, error) {
	m.mu.Lock()
	m.calls++
	m.mu.Unlock()
	if m.chatErr != nil {
		return "", nil, m.chatErr
	}
	return m.chatText, nil, nil
}

func (m *chainMock) Stream(ctx context.Context, _ []types.Message) (<-chan string, <-chan error) {
	out := make(chan string, 8)
	errC := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errC)
		for _, c := range m.streamChunks {
			select {
			case out <- c.Content:
			case <-ctx.Done():
				return
			}
		}
		if m.streamErr != nil {
			errC <- m.streamErr
		}
	}()
	return out, errC
}

func (m *chainMock) StreamEx(ctx context.Context, _ []types.Message) (<-chan StreamChunk, <-chan error) {
	out := make(chan StreamChunk, 8)
	errC := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errC)
		for _, c := range m.streamChunks {
			select {
			case out <- c:
			case <-ctx.Done():
				return
			}
		}
		if m.streamErr != nil {
			errC <- m.streamErr
		}
	}()
	return out, errC
}

func (m *chainMock) ModelID() string      { return m.name }
func (m *chainMock) MaxContext() int      { return 1000 }
func (m *chainMock) SupportsVision() bool { return false }
func (m *chainMock) SetReasoningEffort(l string) {
	m.mu.Lock()
	m.effort = l
	m.mu.Unlock()
}
func (m *chainMock) ReasoningEffort() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.effort
}
func (m *chainMock) SetTopP(p float64) {
	m.mu.Lock()
	m.topP = p
	m.mu.Unlock()
}
func (m *chainMock) TopP() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.topP
}
func (m *chainMock) SetTemperature(t float64) {
	m.mu.Lock()
	m.temp = t
	m.mu.Unlock()
}
func (m *chainMock) Temperature() float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.temp
}
func (m *chainMock) SetStreamTools([]types.ToolDefinition) {
	m.mu.Lock()
	m.toolsSet = true
	m.mu.Unlock()
}

// visionChainMock 支持多模态的 mock（验证 SupportsVision 并集语义）。
type visionChainMock struct{ chainMock }

func (v *visionChainMock) SupportsVision() bool { return true }

// TestNewChainBackend_FilterNilAndDedup：nil 过滤、重复去重、空返回 nil。
func TestNewChainBackend_FilterNilAndDedup(t *testing.T) {
	a := &chainMock{name: "a"}
	if NewChainBackend(nil, nil) != nil {
		t.Fatal("全 nil 应返回 nil")
	}
	c := NewChainBackend(a, nil, a)
	if c == nil {
		t.Fatal("有效来源应返回非 nil")
	}
	if len(c.Sources()) != 1 {
		t.Fatalf("去重后应剩 1 个来源，got %d", len(c.Sources()))
	}
}

// TestChainBackend_ChatFirstSuccess：Chat 首个成功即返回，不触碰后续来源。
func TestChainBackend_ChatFirstSuccess(t *testing.T) {
	a := &chainMock{name: "a", chatText: "from-a"}
	b := &chainMock{name: "b", chatText: "from-b"}
	c := NewChainBackend(a, b)
	text, _, err := c.Chat(context.Background(), nil, nil)
	if err != nil || text != "from-a" {
		t.Fatalf("应返回首来源结果 from-a，got %q err=%v", text, err)
	}
	if b.calls != 0 {
		t.Fatalf("首来源成功时不应调用次来源，次来源 calls=%d", b.calls)
	}
}

// TestChainBackend_ChatFallback：Chat 首来源失败 → 次来源成功（多来源降级）。
func TestChainBackend_ChatFallback(t *testing.T) {
	a := &chainMock{name: "a", chatErr: errors.New("boom")}
	b := &chainMock{name: "b", chatText: "from-b"}
	c := NewChainBackend(a, b)
	text, _, err := c.Chat(context.Background(), nil, nil)
	if err != nil || text != "from-b" {
		t.Fatalf("首来源失败应降级到次来源 from-b，got %q err=%v", text, err)
	}
}

// TestChainBackend_ChatAllFail：全部失败返回最后一个错误。
func TestChainBackend_ChatAllFail(t *testing.T) {
	a := &chainMock{name: "a", chatErr: errors.New("first")}
	b := &chainMock{name: "b", chatErr: errors.New("second")}
	c := NewChainBackend(a, b)
	_, _, err := c.Chat(context.Background(), nil, nil)
	if err == nil || err.Error() != "second" {
		t.Fatalf("应返回最后一个错误 second，got %v", err)
	}
}

// TestChainBackend_StreamSwitchBeforeFirstChunk：流式在首块【前】失败 → 切换下一来源。
func TestChainBackend_StreamSwitchBeforeFirstChunk(t *testing.T) {
	a := &chainMock{name: "a", streamErr: errors.New("conn refused")}
	b := &chainMock{name: "b", streamChunks: []StreamChunk{{Content: "hello"}}}
	c := NewChainBackend(a, b)
	ch, errCh := c.Stream(context.Background(), nil)
	var got string
	for s := range ch {
		got += s
	}
	for e := range errCh {
		if e != nil {
			t.Fatalf("切换成功后不应有错误，got %v", e)
		}
	}
	if got != "hello" {
		t.Fatalf("应收到次来源内容 hello，got %q", got)
	}
}

// TestChainBackend_StreamNoSwitchAfterContent：已产出内容后失败 → 不切换，上抛错误。
func TestChainBackend_StreamNoSwitchAfterContent(t *testing.T) {
	a := &chainMock{name: "a", streamChunks: []StreamChunk{{Content: "partial"}}, streamErr: errors.New("mid-stream")}
	b := &chainMock{name: "b", streamChunks: []StreamChunk{{Content: "should-not-appear"}}}
	c := NewChainBackend(a, b)
	ch, errCh := c.Stream(context.Background(), nil)
	var got string
	for s := range ch {
		got += s
	}
	var streamErr error
	for e := range errCh {
		streamErr = e
	}
	if got != "partial" {
		t.Fatalf("已产出内容后不应切换来源，got %q", got)
	}
	if streamErr == nil {
		t.Fatal("已产出内容后失败应上抛错误")
	}
}

// TestChainBackend_StreamExSwitch：结构化流式首块前失败 → 切换。
func TestChainBackend_StreamExSwitch(t *testing.T) {
	a := &chainMock{name: "a", streamErr: errors.New("conn refused")}
	b := &chainMock{name: "b", streamChunks: []StreamChunk{{Content: "hi", FinishReason: "stop"}}}
	c := NewChainBackend(a, b)
	ch, errCh := c.StreamEx(context.Background(), nil)
	var got string
	for sc := range ch {
		got += sc.Content
	}
	for e := range errCh {
		if e != nil {
			t.Fatalf("切换成功后不应有错误，got %v", e)
		}
	}
	if got != "hi" {
		t.Fatalf("应收到次来源内容 hi，got %q", got)
	}
}

// TestChainBackend_SetParamsBroadcast：参数 setter 广播到全部来源。
func TestChainBackend_SetParamsBroadcast(t *testing.T) {
	a := &chainMock{name: "a"}
	b := &chainMock{name: "b"}
	c := NewChainBackend(a, b)
	c.SetTemperature(0.7)
	c.SetTopP(0.9)
	c.SetReasoningEffort("high")
	for _, m := range []*chainMock{a, b} {
		if m.temp != 0.7 || m.topP != 0.9 || m.effort != "high" {
			t.Fatalf("来源 %s 未收到广播参数: temp=%v topP=%v effort=%q", m.name, m.temp, m.topP, m.effort)
		}
	}
	if c.Temperature() != 0.7 || c.TopP() != 0.9 || c.ReasoningEffort() != "high" {
		t.Fatalf("组合后端缓存参数不符: temp=%v topP=%v effort=%q", c.Temperature(), c.TopP(), c.ReasoningEffort())
	}
}

// TestChainBackend_VisionUnion：SupportsVision 取并集。
func TestChainBackend_VisionUnion(t *testing.T) {
	a := &chainMock{name: "a"}
	v := &visionChainMock{chainMock{name: "v"}}
	c := NewChainBackend(a, v)
	if !c.SupportsVision() {
		t.Fatal("任一来源支持视觉 → 组合应报告 SupportsVision=true")
	}
	// MaxContext 取正数最小值
	if c.MaxContext() != 1000 {
		t.Fatalf("MaxContext 应为 1000，got %d", c.MaxContext())
	}
}
