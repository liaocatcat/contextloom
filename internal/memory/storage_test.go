package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// newTestStorage creates a Storage backed by a temporary directory.
func newTestStorage(t *testing.T) (*Storage, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "contextloom-memory-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	s, err := NewStorage(dir)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("NewStorage failed: %v", err)
	}
	cleanup := func() {
		s.Close()
		os.RemoveAll(dir)
	}
	return s, cleanup
}

// helper to create a session with known values
func testSession(uuid, title, project string) *types.Session {
	now := time.Now().Truncate(time.Second)
	return &types.Session{
		UUID:      uuid,
		Title:     title,
		Project:   project,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ---------------------------------------------------------------------------
// NewStorage
// ---------------------------------------------------------------------------

func TestNewStorage(t *testing.T) {
	dir, err := os.MkdirTemp("", "contextloom-memory-new-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	s, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage unexpected error: %v", err)
	}
	defer s.Close()

	if s == nil {
		t.Fatal("expected non-nil Storage")
	}
	if s.baseDir != dir {
		t.Errorf("expected baseDir %q, got %q", dir, s.baseDir)
	}

	// sessions directory should exist
	sessionsDir := filepath.Join(dir, "sessions")
	if info, err := os.Stat(sessionsDir); err != nil || !info.IsDir() {
		t.Errorf("expected sessions directory at %s", sessionsDir)
	}

	// v2.1 legacy bolt index file should NOT exist (removed on init)
	boltPath := filepath.Join(dir, "index.bolt")
	if _, err := os.Stat(boltPath); !os.IsNotExist(err) {
		t.Errorf("expected legacy bolt index %s to be removed", boltPath)
	}
}

// TestStorage_MultiInstance_NoLockConflict 验证纯文件存储支持同目录多实例并行：
// 第二个 Storage 打开同一目录不应失败（v2.1 BoltDB flock 排他锁曾导致 timeout）。
func TestStorage_MultiInstance_NoLockConflict(t *testing.T) {
	dir, err := os.MkdirTemp("", "contextloom-memory-multi-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	s1, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage (instance 1) failed: %v", err)
	}
	defer s1.Close()

	// 第二个实例在同一目录打开必须成功（原 BoltDB 在此会 5s 超时失败）
	s2, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage (instance 2) failed: %v", err)
	}
	defer s2.Close()

	// 两个实例各自写不同会话，互相可见且互不冲突
	if err := s1.SaveSession(testSession("multi-1", "From Instance 1", "p")); err != nil {
		t.Fatalf("s1.SaveSession failed: %v", err)
	}
	if err := s2.SaveSession(testSession("multi-2", "From Instance 2", "p")); err != nil {
		t.Fatalf("s2.SaveSession failed: %v", err)
	}

	// s1 能列出 s2 写的会话（目录扫描天然共享）
	sessions, err := s1.ListSessions()
	if err != nil {
		t.Fatalf("s1.ListSessions failed: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("expected 2 sessions visible from instance 1, got %d", len(sessions))
	}

	// 两实例同时向同一会话追加事件：JSONL 追加写原子，不应报错
	evt := &types.SessionEvent{Seq: 1, Timestamp: time.Now(), Type: "test", Data: "x"}
	if err := s1.AppendEvent("multi-1", evt); err != nil {
		t.Errorf("s1.AppendEvent failed: %v", err)
	}
	if err := s2.AppendEvent("multi-1", evt); err != nil {
		t.Errorf("s2.AppendEvent failed: %v", err)
	}

	// 事件应全部落盘
	events, err := s1.LoadEvents("multi-1")
	if err != nil {
		t.Fatalf("LoadEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("expected 2 events after both instances appended, got %d", len(events))
	}
}

func TestNewStorage_InvalidPath(t *testing.T) {
	// /dev/null/sessions should fail (not a directory)
	_, err := NewStorage("/dev/null")
	if err == nil {
		t.Error("expected error for invalid base dir")
	}
}

// ---------------------------------------------------------------------------
// SaveSession / LoadSession
// ---------------------------------------------------------------------------

func TestStorage_SaveAndLoadSession(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	session := testSession("abc-123", "Test Session", "my-project")

	if err := s.SaveSession(session); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	loaded, err := s.LoadSession("abc-123")
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}

	if loaded.UUID != session.UUID {
		t.Errorf("expected UUID %q, got %q", session.UUID, loaded.UUID)
	}
	if loaded.Title != session.Title {
		t.Errorf("expected Title %q, got %q", session.Title, loaded.Title)
	}
	if loaded.Project != session.Project {
		t.Errorf("expected Project %q, got %q", session.Project, loaded.Project)
	}
	if !loaded.CreatedAt.Equal(session.CreatedAt) {
		t.Errorf("expected CreatedAt %v, got %v", session.CreatedAt, loaded.CreatedAt)
	}
	if !loaded.UpdatedAt.Equal(session.UpdatedAt) {
		t.Errorf("expected UpdatedAt %v, got %v", session.UpdatedAt, loaded.UpdatedAt)
	}
}

func TestStorage_LoadSession_Missing(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	_, err := s.LoadSession("nonexistent-uuid")
	if err == nil {
		t.Error("expected error loading nonexistent session")
	}
}

func TestStorage_SaveSession_Overwrite(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	session1 := testSession("xyz-1", "First Title", "proj")
	session2 := testSession("xyz-1", "Second Title", "proj")

	if err := s.SaveSession(session1); err != nil {
		t.Fatalf("first SaveSession failed: %v", err)
	}
	if err := s.SaveSession(session2); err != nil {
		t.Fatalf("second SaveSession failed: %v", err)
	}

	loaded, err := s.LoadSession("xyz-1")
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if loaded.Title != "Second Title" {
		t.Errorf("expected overwritten title %q, got %q", "Second Title", loaded.Title)
	}
}

// ---------------------------------------------------------------------------
// DeleteSession
// ---------------------------------------------------------------------------

func TestStorage_DeleteSession(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	session := testSession("del-1", "To Delete", "proj")
	if err := s.SaveSession(session); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	if err := s.DeleteSession("del-1"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	_, err := s.LoadSession("del-1")
	if err == nil {
		t.Error("expected error loading deleted session")
	}
}

func TestStorage_DeleteSession_Nonexistent(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	// Should not error on deleting a nonexistent session
	err := s.DeleteSession("does-not-exist")
	if err != nil {
		t.Errorf("DeleteSession on nonexistent should not error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ListSessions
// ---------------------------------------------------------------------------

func TestStorage_ListSessions_Empty(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	sessions, err := s.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions, got %d", len(sessions))
	}
}

func TestStorage_ListSessions_Sorted(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 3; i >= 1; i-- {
		ss := &types.Session{
			UUID:      "uuid-" + string(rune('0'+i)),
			Title:     "Session " + string(rune('0'+i)),
			Project:   "p",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := s.SaveSession(ss); err != nil {
			t.Fatalf("SaveSession failed: %v", err)
		}
	}

	sessions, err := s.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions failed: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("expected 3 sessions, got %d", len(sessions))
	}

	// 应按 UpdatedAt 倒序（最近活跃在前）
	for i := 0; i < len(sessions)-1; i++ {
		if sessions[i].UpdatedAt.Before(sessions[i+1].UpdatedAt) {
			t.Errorf("sessions not sorted correctly: session %d UpdatedAt %v before session %d UpdatedAt %v",
				i, sessions[i].UpdatedAt, i+1, sessions[i+1].UpdatedAt)
		}
	}
}

// ---------------------------------------------------------------------------
// SessionCount
// ---------------------------------------------------------------------------

func TestStorage_SessionCount(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	// Initially zero
	count, err := s.SessionCount()
	if err != nil {
		t.Fatalf("SessionCount failed: %v", err)
	}
	if count != 0 {
		t.Errorf("expected count 0, got %d", count)
	}

	// Add 3 sessions
	for i := 0; i < 3; i++ {
		ss := testSession("count-"+string(rune('a'+i)), "S", "p")
		if err := s.SaveSession(ss); err != nil {
			t.Fatalf("SaveSession failed: %v", err)
		}
	}

	count, err = s.SessionCount()
	if err != nil {
		t.Fatalf("SessionCount failed: %v", err)
	}
	if count != 3 {
		t.Errorf("expected count 3, got %d", count)
	}

	// Delete one
	if err := s.DeleteSession("count-a"); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	count, err = s.SessionCount()
	if err != nil {
		t.Fatalf("SessionCount failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected count 2 after delete, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// AppendEvent
// ---------------------------------------------------------------------------

func TestStorage_AppendEvent(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	// Save a session first
	session := testSession("evt-1", "Event Test", "p")
	if err := s.SaveSession(session); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	event := &types.SessionEvent{
		Seq:       1,
		Timestamp: time.Now(),
		Type:      "user_message",
		Data:      "hello",
	}

	if err := s.AppendEvent("evt-1", event); err != nil {
		t.Fatalf("AppendEvent failed: %v", err)
	}

	// Verify JSONL file was created
	logPath := filepath.Join(s.baseDir, "sessions", "evt-1.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading jsonl file failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty jsonl file")
	}
}

func TestStorage_AppendEvent_NonexistentSession(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	event := &types.SessionEvent{
		Seq:       1,
		Timestamp: time.Now(),
		Type:      "test",
		Data:      "data",
	}

	// AppendEvent should succeed even for nonexistent sessions
	// (the event is appended to the JSONL file, but bolt metadata is not updated)
	err := s.AppendEvent("no-such-session", event)
	if err != nil {
		t.Errorf("AppendEvent should not fail for nonexistent session: %v", err)
	}
}

func TestStorage_AppendEvent_MultipleEvents(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	session := testSession("evt-2", "Multi Event", "p")
	if err := s.SaveSession(session); err != nil {
		t.Fatalf("SaveSession failed: %v", err)
	}

	for i := 1; i <= 5; i++ {
		event := &types.SessionEvent{
			Seq:       int64(i),
			Timestamp: time.Now(),
			Type:      "message",
			Data:      "msg-" + string(rune('0'+i)),
		}
		if err := s.AppendEvent("evt-2", event); err != nil {
			t.Fatalf("AppendEvent %d failed: %v", i, err)
		}
	}

	// Verify JSONL has exactly 5 lines
	logPath := filepath.Join(s.baseDir, "sessions", "evt-2.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("reading jsonl file failed: %v", err)
	}
	lines := 0
	for _, b := range data {
		if b == '\n' {
			lines++
		}
	}
	if lines != 5 {
		t.Errorf("expected 5 JSONL lines, got %d", lines)
	}
}

// ---------------------------------------------------------------------------
// EvictOldest
// ---------------------------------------------------------------------------

func TestStorage_EvictOldest_NoEviction(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		ss := &types.Session{
			UUID:      "evict-" + string(rune('a'+i)),
			Title:     "S",
			Project:   "p",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		_ = s.SaveSession(ss)
	}

	// maxSessions = 5, we have 3 — no eviction
	if err := s.EvictOldest(5); err != nil {
		t.Fatalf("EvictOldest failed: %v", err)
	}

	count, _ := s.SessionCount()
	if count != 3 {
		t.Errorf("expected 3 sessions after no-op eviction, got %d", count)
	}
}

func TestStorage_EvictOldest_EvictsExcess(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		ss := &types.Session{
			UUID:      "evict-" + string(rune('a'+i)),
			Title:     "Session " + string(rune('A'+i)),
			Project:   "p",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		_ = s.SaveSession(ss)
	}

	// maxSessions = 2, oldest 3 should be evicted
	if err := s.EvictOldest(2); err != nil {
		t.Fatalf("EvictOldest failed: %v", err)
	}

	count, _ := s.SessionCount()
	if count != 2 {
		t.Errorf("expected 2 sessions after eviction, got %d", count)
	}

	// Oldest sessions (evict-a, evict-b, evict-c) should be gone
	for _, id := range []string{"evict-a", "evict-b", "evict-c"} {
		_, err := s.LoadSession(id)
		if err == nil {
			t.Errorf("expected session %s to be evicted", id)
		}
	}

	// Newest sessions (evict-d, evict-e) should remain
	for _, id := range []string{"evict-d", "evict-e"} {
		_, err := s.LoadSession(id)
		if err != nil {
			t.Errorf("expected session %s to remain, got error: %v", id, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestStorage_Close(t *testing.T) {
	dir, err := os.MkdirTemp("", "contextloom-memory-close-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	s, err := NewStorage(dir)
	if err != nil {
		t.Fatalf("NewStorage failed: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Operations after close should fail or behave predictably
	// (bolt returns "database not open")
	err = s.Close()
	if err == nil {
		t.Log("double Close did not error (acceptable)")
	}
}

// ---------------------------------------------------------------------------
// LoadSession JSON integrity
// ---------------------------------------------------------------------------

func TestStorage_LoadSession_CorruptedJSON(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	// First save a valid session so bolt has the metadata
	_ = s.SaveSession(&types.Session{
		UUID:      "corrupt",
		Title:     "Corrupt",
		Project:   "p",
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	})

	// Then overwrite the JSON file with corrupt content
	sessionPath := filepath.Join(s.baseDir, "sessions", "corrupt.json")
	if err := os.WriteFile(sessionPath, []byte("not valid json"), 0644); err != nil {
		t.Fatalf("failed to write corrupt file: %v", err)
	}

	_, err := s.LoadSession("corrupt")
	if err == nil {
		t.Error("expected error loading session with corrupt JSON")
	}
}

// ---------------------------------------------------------------------------
// 大事件行回归测试（用户实测："新对话聊了很久，一旦关闭就无法重新加载历史"）
// 根因：LoadEvents/LastEventSeq 用 bufio.Scanner 默认 64KB 上限，单条事件
// （assistant 消息含大段工具输出/代码/图片 base64）超过即报 "token too long"，
// 整个会话历史加载失败。修复：scanner.Buffer 上限放宽到 64MB。
// ---------------------------------------------------------------------------

func TestLoadEvents_LargeEventLines(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	uuid := "large-line-session"
	// 模拟超长事件：120KB 文本（远超 Scanner 默认 64KB 上限）
	big := strings.Repeat("这是一个很长的工具输出内容，用于复现历史无法加载的缺陷。", 5000)
	if len(big) < 120*1024 {
		t.Fatalf("测试数据不够大: %d", len(big))
	}
	t.Logf("单条事件长度: %d 字节（默认 Scanner 上限 64KB=%d）", len(big), 64*1024)

	ev1 := &types.SessionEvent{Seq: 1, Type: "user_msg", Data: "第一条短消息"}
	ev2 := &types.SessionEvent{Seq: 2, Type: "assistant_msg", Data: big} // 超限行
	ev3 := &types.SessionEvent{Seq: 3, Type: "user_msg", Data: "超长回复后的追问"}
	if err := s.AppendEvent(uuid, ev1); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(uuid, ev2); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(uuid, ev3); err != nil {
		t.Fatal(err)
	}

	// LoadEvents 必须完整读回（旧实现此处返回 "token too long" 错误）
	events, err := s.LoadEvents(uuid)
	if err != nil {
		t.Fatalf("LoadEvents 失败（历史加载根因未修复）: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("应读回 3 条事件，got %d", len(events))
	}
	if got, _ := events[1].Data.(string); got != big {
		t.Fatalf("超长事件内容未完整读回（长度 %d != %d）", len(got), len(big))
	}
	if events[0].Seq != 1 || events[2].Seq != 3 {
		t.Fatalf("事件排序异常: %+v", events)
	}

	// LastEventSeq 也必须能处理超长行（返回最后 seq 而非报错）
	last, err := s.LastEventSeq(uuid)
	if err != nil {
		t.Fatalf("LastEventSeq 失败（大行击穿）: %v", err)
	}
	if last != 3 {
		t.Fatalf("LastEventSeq = %d, want 3", last)
	}
}

// LoadEventsAfter 复用 LoadEvents 的扫描逻辑：超长行也必须正常增量恢复。
func TestLoadEventsAfter_LargeEventLines(t *testing.T) {
	s, cleanup := newTestStorage(t)
	defer cleanup()

	uuid := "large-line-after-session"
	big := strings.Repeat("X", 100*1024) // 100KB 超限行
	if err := s.AppendEvent(uuid, &types.SessionEvent{Seq: 10, Type: "user_msg", Data: big}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendEvent(uuid, &types.SessionEvent{Seq: 11, Type: "assistant_msg", Data: "回复"}); err != nil {
		t.Fatal(err)
	}
	events, err := s.LoadEventsAfter(uuid, 10)
	if err != nil {
		t.Fatalf("LoadEventsAfter 失败: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 11 {
		t.Fatalf("增量恢复异常: %+v", events)
	}
}
