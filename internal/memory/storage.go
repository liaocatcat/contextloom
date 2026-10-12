package memory

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// Storage 提供基于纯文件的会话持久化：
//   - 会话元数据/对话历史 → sessions/<uuid>.json（完整 Session 序列化）
//   - 会话事件日志       → sessions/<uuid>.jsonl（追加写，每行一个事件）
//
// 索引不再使用 BoltDB（v2.1 遗留）：BoltDB 通过 flock 对 index.bolt 加排他锁，
// 导致同一项目目录无法同时启动多个 contextloom 实例（第二个实例报
// "无法打开 BoltDB: timeout"）。现改为启动时扫描 sessions/ 目录重建索引，
// 天然无锁、支持多实例并行（各自读写不同会话文件互不冲突）。
type Storage struct {
	baseDir string

	// 会话列表缓存：避免 GUI 高频刷新（2s 状态轮询 / 切换会话 / 发送消息）
	// 时每次全量读盘所有会话 JSON。TTL 1s + 写操作主动失效，多实例下
	// 最坏延迟 1s 即自愈，正确性无影响（列表仅用于展示与排序）。
	cacheMu sync.RWMutex
	cache   []*types.Session
	cacheTS time.Time
}

const sessionListCacheTTL = time.Second

func NewStorage(baseDir string) (*Storage, error) {
	sessionsDir := filepath.Join(baseDir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0755); err != nil {
		return nil, err
	}
	// 清理 v2.1 遗留的 BoltDB 索引文件（已废弃，避免误导）
	_ = os.Remove(filepath.Join(baseDir, "index.bolt"))
	return &Storage{baseDir: baseDir}, nil
}

// invalidateSessionCache 使会话列表缓存失效（写操作后调用）。
func (s *Storage) invalidateSessionCache() {
	s.cacheMu.Lock()
	s.cache = nil
	s.cacheTS = time.Time{}
	s.cacheMu.Unlock()
}

// Close 无资源可释放（纯文件存储无锁无句柄），保留接口以兼容旧调用。
func (s *Storage) Close() error {
	return nil
}

func (s *Storage) SaveSession(session *types.Session) error {
	sessionPath := filepath.Join(s.baseDir, "sessions", session.UUID+".json")
	data, err := json.Marshal(session)
	if err != nil {
		return err
	}
	if err := os.WriteFile(sessionPath, data, 0644); err != nil {
		return err
	}
	s.invalidateSessionCache()
	return nil
}

func (s *Storage) LoadSession(uuid string) (*types.Session, error) {
	sessionPath := filepath.Join(s.baseDir, "sessions", uuid+".json")
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil, err
	}
	var session types.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, err
	}
	return &session, nil
}

func (s *Storage) DeleteSession(uuid string) error {
	_ = os.Remove(filepath.Join(s.baseDir, "sessions", uuid+".json"))
	_ = os.Remove(filepath.Join(s.baseDir, "sessions", uuid+".jsonl"))
	s.invalidateSessionCache()
	return nil
}

// ListSessions 返回全部会话（按 UpdatedAt 倒序，最近活跃在前）。
// 1s TTL 缓存：高频轮询（GUI 2s 状态刷新、切换会话、发送消息）时
// 不再每次全量读盘所有会话 JSON，避免会话数量大时 GUI 左侧列表加载缓慢。
// 损坏的 JSON 文件直接跳过，不阻塞整体列表。
func (s *Storage) ListSessions() ([]*types.Session, error) {
	s.cacheMu.RLock()
	if s.cache != nil && time.Since(s.cacheTS) < sessionListCacheTTL {
		out := make([]*types.Session, len(s.cache))
		copy(out, s.cache)
		s.cacheMu.RUnlock()
		return out, nil
	}
	s.cacheMu.RUnlock()

	dir := filepath.Join(s.baseDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var sessions []*types.Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var session types.Session
		if err := json.Unmarshal(data, &session); err != nil {
			continue // 跳过损坏文件
		}
		sessions = append(sessions, &session)
	}
	// 按 UpdatedAt 倒序（最近活跃在前）
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].UpdatedAt.After(sessions[j].UpdatedAt)
	})

	s.cacheMu.Lock()
	s.cache = sessions
	s.cacheTS = time.Now()
	s.cacheMu.Unlock()
	return sessions, nil
}

// AppendEvent 追加会话事件到 JSONL 日志，并同步刷新 JSON 中的 UpdatedAt
// （保持 ListSessions 排序与"最后活跃时间"显示一致）。
// 会话不存在时仅追加日志不报错（与旧行为一致）。
func (s *Storage) AppendEvent(uuid string, event *types.SessionEvent) error {
	logPath := filepath.Join(s.baseDir, "sessions", uuid+".jsonl")
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}

	return s.touchSession(uuid)
}

// touchSession 将会话 JSON 中的 UpdatedAt 刷新为当前时间。
// 文件不存在或损坏时静默忽略（避免追加日志被元数据更新失败拖累）。
func (s *Storage) touchSession(uuid string) error {
	sessionPath := filepath.Join(s.baseDir, "sessions", uuid+".json")
	data, err := os.ReadFile(sessionPath)
	if err != nil {
		return nil
	}
	var session types.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil
	}
	session.UpdatedAt = time.Now()
	out, err := json.Marshal(&session)
	if err != nil {
		return nil
	}
	if err := os.WriteFile(sessionPath, out, 0644); err != nil {
		return err
	}
	s.invalidateSessionCache() // UpdatedAt 变化影响排序，主动失效缓存
	return nil
}

// LoadEvents 读取会话的所有 JSONL 事件日志，按 seq 升序返回。
func (s *Storage) LoadEvents(uuid string) ([]*types.SessionEvent, error) {
	logPath := filepath.Join(s.baseDir, "sessions", uuid+".jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // 无可回溯事件不算错误
		}
		return nil, fmt.Errorf("打开事件日志失败: %w", err)
	}
	defer f.Close()

	var events []*types.SessionEvent
	scanner := bufio.NewScanner(f)
	// 历史缺陷（用户实测："新对话聊了很久，一旦关闭就无法重新加载历史"）：
	// Scanner 默认缓冲区上限 64KB，单条事件（assistant 消息含大段工具输出/
	// 代码/图片 base64）超过即报 "token too long" → LoadEvents 返回错误 →
	// 整个会话历史加载失败（GUI/TUI 关闭后重新打开历史空白/丢失）。
	// 上限放宽到 64MB（单条事件再大也不合理），并保持 Scanner 动态扩容语义。
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var event types.SessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // 跳过损坏行
		}
		events = append(events, &event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取事件日志失败: %w", err)
	}

	sort.Slice(events, func(i, j int) bool {
		return events[i].Seq < events[j].Seq
	})
	return events, nil
}

// LoadEventsAfter 读取 Seq 大于 after 的会话 JSONL 事件日志（增量恢复），
// 按 seq 升序返回。实现为 LoadEvents 后过滤（会话事件量级小，二次读取
// 成本可忽略；保证与 LoadEvents 的排序/容错语义完全一致）。
func (s *Storage) LoadEventsAfter(uuid string, after int64) ([]*types.SessionEvent, error) {
	events, err := s.LoadEvents(uuid)
	if err != nil {
		return nil, err
	}
	if after <= 0 {
		return events, nil
	}
	out := make([]*types.SessionEvent, 0, len(events))
	for _, e := range events {
		if e.Seq > after {
			out = append(out, e)
		}
	}
	return out, nil
}

// LastEventSeq 返回会话事件日志最后一条事件 Seq（无事件/日志不存在返回 0）。
func (s *Storage) LastEventSeq(uuid string) (int64, error) {
	logPath := filepath.Join(s.baseDir, "sessions", uuid+".jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("打开事件日志失败: %w", err)
	}
	defer f.Close()

	var last int64
	scanner := bufio.NewScanner(f)
	// 与 LoadEvents 一致：放宽 Scanner 缓冲区上限（默认 64KB 会被大事件行
	// "token too long" 击穿，导致 Seq 统计失败/历史恢复异常）。
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var event types.SessionEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event.Seq > last {
			last = event.Seq
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("读取事件日志失败: %w", err)
	}
	return last, nil
}

// SessionCount 统计 sessions/ 目录下 JSON 元数据文件数。
func (s *Storage) SessionCount() (int, error) {
	dir := filepath.Join(s.baseDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

// EvictOldest 超出 maxSessions 时按 UpdatedAt 从旧到新淘汰。
func (s *Storage) EvictOldest(maxSessions int) error {
	if maxSessions <= 0 {
		return nil
	}
	sessions, err := s.ListSessions()
	if err != nil {
		return err
	}

	excess := len(sessions) - maxSessions
	for i := 0; i < excess && i < len(sessions); i++ {
		idx := len(sessions) - 1 - i // 列表按 UpdatedAt 倒序，尾部才是最旧
		_ = s.DeleteSession(sessions[idx].UUID)
	}
	return nil
}
