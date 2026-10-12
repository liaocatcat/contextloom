package executor

import (
	"fmt"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// MessageBus Agent 间消息总线（多agent实现建议.md §8.1，借鉴业界同类 Agent 的共享消息总线设计）
type MessageBus struct {
	mu       sync.Mutex
	channels map[string]chan types.AgentMessage
}

func NewMessageBus() *MessageBus {
	return &MessageBus{channels: make(map[string]chan types.AgentMessage)}
}

func (mb *MessageBus) Register(agentID string) chan types.AgentMessage {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	ch := make(chan types.AgentMessage, 16)
	mb.channels[agentID] = ch
	return ch
}

func (mb *MessageBus) Send(msg types.AgentMessage) error {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if msg.To == "broadcast" {
		for id, ch := range mb.channels {
			if id == msg.From {
				continue
			}
			select {
			case ch <- msg:
			default:
			}
		}
		return nil
	}
	if ch, ok := mb.channels[msg.To]; ok {
		select {
		case ch <- msg:
			return nil
		default:
			return fmt.Errorf("agent %s inbox full", msg.To)
		}
	}
	return fmt.Errorf("agent %s not found", msg.To)
}

// DrainInbox 非阻塞读取指定 Agent 的全部待处理消息（供 Agent 每轮循环 drain）
func (mb *MessageBus) DrainInbox(agentID string) []types.AgentMessage {
	mb.mu.Lock()
	ch, ok := mb.channels[agentID]
	mb.mu.Unlock()
	if !ok {
		return nil
	}
	var msgs []types.AgentMessage
	for {
		select {
		case m, ok := <-ch:
			if !ok {
				return msgs
			}
			msgs = append(msgs, m)
		default:
			return msgs
		}
	}
}

func (mb *MessageBus) Unregister(agentID string) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	delete(mb.channels, agentID)
}
