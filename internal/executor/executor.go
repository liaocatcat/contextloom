package executor

import (
	"context"
	"fmt"
	"sync"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Executor struct {
	mu      sync.RWMutex
	harness *harness.Engine
	agents  map[string]*Agent
	tools   *tools.Registry
	bus     *MessageBus // Agent 间消息总线（nil 时 Agent 无通信能力）
}

func New(h *harness.Engine, tr *tools.Registry) *Executor {
	return &Executor{
		harness: h,
		agents:  make(map[string]*Agent),
		tools:   tr,
	}
}

// SetBus 注入消息总线，使 SpawnAgent 创建的 Agent 具备通信能力
func (e *Executor) SetBus(bus *MessageBus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.bus = bus
}

func (e *Executor) SpawnAgent(id string, task types.TaskCreate, backend model.Backend) *Agent {
	e.mu.Lock()
	defer e.mu.Unlock()

	agent := &Agent{
		ID:      id,
		Task:    task,
		harness: e.harness,
		tools:   e.tools,
		backend: backend,
	}
	if e.bus != nil {
		agent.bus = e.bus
		e.bus.Register(id) // 注册收件箱（drain 走 DrainInbox）
	}
	e.agents[id] = agent
	return agent
}

func (e *Executor) Execute(ctx context.Context, agentID string) error {
	e.mu.RLock()
	agent, ok := e.agents[agentID]
	e.mu.RUnlock()

	if !ok {
		return fmt.Errorf("agent not found: %s", agentID)
	}
	return agent.Run(ctx)
}

func (e *Executor) RemoveAgent(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.agents, id)
	// 清理消息总线收件箱，防止 inbox channel 泄漏（SpawnTask 频繁创建/销毁子 Agent）
	if e.bus != nil {
		e.bus.Unregister(id)
	}
}

func (e *Executor) Agents() map[string]*Agent {
	e.mu.RLock()
	defer e.mu.RUnlock()
	cp := make(map[string]*Agent, len(e.agents))
	for k, v := range e.agents {
		cp[k] = v
	}
	return cp
}
