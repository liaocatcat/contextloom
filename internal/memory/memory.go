package memory

import (
	"sort"
	"sync"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

type System struct {
	mu          sync.RWMutex
	dir         string
	maxSessions int
	sessions    map[string]*types.Session
	storage     *Storage
}

func New(dir string, maxSessions int) (*System, error) {
	if maxSessions <= 0 {
		maxSessions = 1000
	}
	storage, err := NewStorage(dir)
	if err != nil {
		return nil, err
	}

	m := &System{
		dir:         dir,
		maxSessions: maxSessions,
		sessions:    make(map[string]*types.Session),
		storage:     storage,
	}
	m.loadFromStorage()
	return m, nil
}

func (m *System) loadFromStorage() {
	sessions, err := m.storage.ListSessions()
	if err != nil {
		return
	}
	for _, s := range sessions {
		m.sessions[s.UUID] = s
	}
}

func (m *System) Save(session *types.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.UUID] = session
	if err := m.storage.SaveSession(session); err != nil {
		return err
	}
	_ = m.storage.EvictOldest(m.maxSessions)
	return nil
}

func (m *System) Load(uuid string) (*types.Session, error) {
	m.mu.RLock()
	if session, ok := m.sessions[uuid]; ok {
		m.mu.RUnlock()
		// 返回浅拷贝：缓存指针绝不能直接交出——GUI 并行流回写链路会
		// Load→改（persistCompactionSnapshot 写 CompactedMessages/UpdatedAt）
		// →Save 同一指针对象；若直接返回缓存指针，并行流在【别的会话】上
		// 做的 Save 会原地改写本会话缓存对象，随后的 LoadSession（会话切换）
		// 读到被污染的 CompactedMessages（多任务并行下压缩快照跨会话串扰、
		// 切换后历史错乱/变少的根因之一）。浅拷贝即可：调用方只改顶层字段。
		cp := *session
		return &cp, nil
	}
	m.mu.RUnlock()

	session, err := m.storage.LoadSession(uuid)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.sessions[session.UUID] = session
	m.mu.Unlock()
	return session, nil
}

func (m *System) List() []*types.Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*types.Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		result = append(result, s)
	}
	// 按 UpdatedAt 倒序（最近活跃在前）
	sort.Slice(result, func(i, j int) bool {
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func (m *System) Delete(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, uuid)
	return m.storage.DeleteSession(uuid)
}

func (m *System) Stats() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	count, _ := m.storage.SessionCount()
	return map[string]any{
		"session_count": len(m.sessions),
		"persisted":     count,
		"max_sessions":  m.maxSessions,
	}
}

func (m *System) Close() error {
	return m.storage.Close()
}

// AppendEvent 持久化会话事件到 JSONL 日志
func (m *System) AppendEvent(uuid string, event *types.SessionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.storage.AppendEvent(uuid, event)
}

// LoadEvents 读取会话日志事件，按 seq 升序返回。
func (m *System) LoadEvents(uuid string) ([]*types.SessionEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.storage.LoadEvents(uuid)
}

// LoadEventsAfter 读取 Seq 大于 after 的会话日志事件（增量恢复用），
// 按 seq 升序返回。after<=0 等价于 LoadEvents。
func (m *System) LoadEventsAfter(uuid string, after int64) ([]*types.SessionEvent, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.storage.LoadEventsAfter(uuid, after)
}

// LastEventSeq 返回会话事件日志最后一条事件 Seq（无事件返回 0）。
func (m *System) LastEventSeq(uuid string) (int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.storage.LastEventSeq(uuid)
}
