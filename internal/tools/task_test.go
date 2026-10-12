package tools

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// mockBus 测试用消息总线
type mockBus struct {
	mu      sync.Mutex
	inboxes map[string][]types.AgentMessage
	sent    []types.AgentMessage
}

func newMockBus() *mockBus {
	return &mockBus{inboxes: map[string][]types.AgentMessage{}}
}

func (m *mockBus) Send(msg types.AgentMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	if msg.To == "broadcast" {
		return nil
	}
	m.inboxes[msg.To] = append(m.inboxes[msg.To], msg)
	return nil
}

func (m *mockBus) DrainInbox(agentID string) []types.AgentMessage {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.inboxes[agentID]
	m.inboxes[agentID] = nil
	return msgs
}

// mockSpawner 测试用 TaskSpawner
type mockSpawner struct {
	calls   int32
	results map[string]string
}

func newMockSpawner() *mockSpawner {
	return &mockSpawner{results: map[string]string{}}
}

func (m *mockSpawner) SpawnTask(ctx context.Context, parentID string, spec TaskSpec) (string, error) {
	atomic.AddInt32(&m.calls, 1)
	return fmt.Sprintf("结果[%s]: %s", parentID, spec.Task), nil
}

func TestSpawnTaskTool_ParamsAndAgentID(t *testing.T) {
	spawner := newMockSpawner()
	fn := MakeSpawnTaskTool(spawner)

	ctx := WithAgentID(context.Background(), "main")
	result, err := fn(ctx, map[string]any{
		"task":      "修复登录bug",
		"directive": "CODE_AGENT",
		"context":   "auth.py 第30行",
	})
	if err != nil {
		t.Fatalf("spawn_task 错误: %v", err)
	}
	if atomic.LoadInt32(&spawner.calls) != 1 {
		t.Errorf("应调用 SpawnTask 1 次，got %d", spawner.calls)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("返回类型错误: %T", result)
	}
	if m["status"] != "completed" {
		t.Errorf("status 应 completed，got %v", m["status"])
	}
	if !contains(m["result"].(string), "main") {
		t.Errorf("结果应含 parentID=main，got %v", m["result"])
	}
}

func TestSpawnTaskTool_MissingTask(t *testing.T) {
	fn := MakeSpawnTaskTool(newMockSpawner())
	_, err := fn(context.Background(), map[string]any{"directive": "X"})
	if err == nil {
		t.Error("缺少 task 应报错")
	}
}

func TestSendMessageTool(t *testing.T) {
	bus := newMockBus()
	fn := MakeSendMessageTool(bus)

	ctx := WithAgentID(context.Background(), "sub-1")
	_, err := fn(ctx, map[string]any{"to": "main", "content": "需要auth.py内容"})
	if err != nil {
		t.Fatalf("send_message 错误: %v", err)
	}
	if len(bus.sent) != 1 {
		t.Fatalf("应发送 1 条，got %d", len(bus.sent))
	}
	if bus.sent[0].From != "sub-1" || bus.sent[0].To != "main" {
		t.Errorf("消息路由错误: from=%s to=%s", bus.sent[0].From, bus.sent[0].To)
	}
}

func TestCheckMessagesTool(t *testing.T) {
	bus := newMockBus()
	bus.inboxes["sub-1"] = []types.AgentMessage{
		{From: "main", Type: "reply", Content: "auth.py 内容如下..."},
	}

	fn := MakeCheckMessagesTool(bus)
	ctx := WithAgentID(context.Background(), "sub-1")
	result, err := fn(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("check_messages 错误: %v", err)
	}
	m := result.(map[string]any)
	if m["count"].(int) != 1 {
		t.Errorf("应 1 条消息，got %v", m["count"])
	}
	// drain 后应清空
	result2, _ := fn(ctx, map[string]any{})
	if result2.(map[string]any)["count"].(int) != 0 {
		t.Error("drain 后应无消息")
	}
}

func TestSingleflight_DeduplicatesConcurrentSpawn(t *testing.T) {
	var calls int32
	started := make(chan struct{})
	spawner := &mockSpawnerResult{calls: &calls, started: started}
	spec := TaskSpec{Task: "相同任务"}

	var wg sync.WaitGroup
	results := make([]string, 5)
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = singleflightSpawn(spawner, context.Background(), "main", spec)
		}(i)
	}
	// 等待 goroutine 进入 singleflight，释放阻塞让 SpawnTask 完成
	close(started)
	wg.Wait()

	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("singleflight 应只调用 1 次，got %d", calls)
	}
	for i, r := range results {
		if errs[i] != nil {
			t.Errorf("goroutine %d 错误: %v", i, errs[i])
		}
		if r == "" {
			t.Errorf("goroutine %d 结果为空", i)
		}
	}
}

type mockSpawnerResult struct {
	calls   *int32
	started chan struct{}
}

func (m *mockSpawnerResult) SpawnTask(ctx context.Context, parentID string, spec TaskSpec) (string, error) {
	atomic.AddInt32(m.calls, 1)
	// 注：singleflight 合并"进行中"的请求；SpawnTask 阻塞一小段时间确保所有并发调用都落在合并窗口内
	time.Sleep(50 * time.Millisecond)
	return "共享结果", nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
