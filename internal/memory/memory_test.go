package memory

import (
	"os"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// newTestSystem creates a System backed by a temporary directory.
func newTestSystem(t *testing.T, maxSessions int) (*System, func()) {
	t.Helper()
	dir, err := os.MkdirTemp("", "contextloom-system-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	m, err := New(dir, maxSessions)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatalf("New failed: %v", err)
	}
	cleanup := func() {
		os.RemoveAll(dir)
	}
	return m, cleanup
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	m, cleanup := newTestSystem(t, 500)
	defer cleanup()

	if m == nil {
		t.Fatal("expected non-nil System")
	}
	if m.dir == "" {
		t.Error("expected non-empty dir")
	}
	if m.maxSessions != 500 {
		t.Errorf("expected maxSessions 500, got %d", m.maxSessions)
	}
	if m.sessions == nil {
		t.Error("expected non-nil sessions map")
	}
	if m.storage == nil {
		t.Error("expected non-nil storage")
	}
}

func TestNew_DefaultMaxSessions(t *testing.T) {
	dir, err := os.MkdirTemp("", "contextloom-system-default-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	// maxSessions = 0 → default to 1000
	m, err := New(dir, 0)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if m.maxSessions != 1000 {
		t.Errorf("expected default maxSessions 1000, got %d", m.maxSessions)
	}

	// maxSessions = -5 → default to 1000
	m2, err := New(dir+"_neg", -5)
	if err != nil {
		t.Fatalf("New with negative maxSessions failed: %v", err)
	}
	defer os.RemoveAll(dir + "_neg")
	if m2.maxSessions != 1000 {
		t.Errorf("expected default maxSessions 1000 for negative, got %d", m2.maxSessions)
	}
}

func TestNew_InvalidDir(t *testing.T) {
	_, err := New("/dev/null", 100)
	if err == nil {
		t.Error("expected error for invalid base dir")
	}
}

// ---------------------------------------------------------------------------
// Save
// ---------------------------------------------------------------------------

func TestSystem_Save(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	session := testSession("save-1", "Save Test", "proj")

	if err := m.Save(session); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Should be in the in-memory cache
	m.mu.RLock()
	cached, ok := m.sessions["save-1"]
	m.mu.RUnlock()
	if !ok {
		t.Error("expected session to be in memory after Save")
	}
	if cached.Title != "Save Test" {
		t.Errorf("expected title %q, got %q", "Save Test", cached.Title)
	}

	// Should also be persisted to storage
	loaded, err := m.storage.LoadSession("save-1")
	if err != nil {
		t.Fatalf("LoadSession from storage failed: %v", err)
	}
	if loaded.Title != "Save Test" {
		t.Errorf("expected persisted title %q, got %q", "Save Test", loaded.Title)
	}
}

func TestSystem_Save_Overwrite(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	s1 := testSession("over-1", "First", "p")
	s2 := testSession("over-1", "Second", "p")

	if err := m.Save(s1); err != nil {
		t.Fatalf("first Save failed: %v", err)
	}
	if err := m.Save(s2); err != nil {
		t.Fatalf("second Save failed: %v", err)
	}

	loaded, err := m.Load("over-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Title != "Second" {
		t.Errorf("expected overwritten title %q, got %q", "Second", loaded.Title)
	}
}

// ---------------------------------------------------------------------------
// Load
// ---------------------------------------------------------------------------

func TestSystem_Load(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	session := testSession("load-1", "Load Test", "proj")
	if err := m.Save(session); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	loaded, err := m.Load("load-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded == nil {
		t.Fatal("expected non-nil session from Load")
	}
	if loaded.UUID != "load-1" {
		t.Errorf("expected UUID %q, got %q", "load-1", loaded.UUID)
	}
}

func TestSystem_Load_Missing(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	_, err := m.Load("no-such-uuid")
	if err == nil {
		t.Error("expected error loading nonexistent session")
	}
}

func TestSystem_Load_FallsBackToStorage(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	// Save directly to storage (bypass in-memory cache)
	session := testSession("fallback-1", "Fallback Test", "proj")
	if err := m.storage.SaveSession(session); err != nil {
		t.Fatalf("storage.SaveSession failed: %v", err)
	}

	// Load should find it in storage and populate cache
	loaded, err := m.Load("fallback-1")
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if loaded.Title != "Fallback Test" {
		t.Errorf("expected title %q, got %q", "Fallback Test", loaded.Title)
	}

	// It should now be in memory
	m.mu.RLock()
	_, ok := m.sessions["fallback-1"]
	m.mu.RUnlock()
	if !ok {
		t.Error("expected session to be in memory after storage fallback load")
	}
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func TestSystem_List_Empty(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	result := m.List()
	if len(result) != 0 {
		t.Errorf("expected empty list, got %d sessions", len(result))
	}
}

func TestSystem_List(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	for i := 0; i < 3; i++ {
		session := testSession("list-"+string(rune('a'+i)), "Session", "proj")
		if err := m.Save(session); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	result := m.List()
	if len(result) != 3 {
		t.Errorf("expected 3 sessions, got %d", len(result))
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestSystem_Delete(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	session := testSession("del-1", "Delete Me", "proj")
	if err := m.Save(session); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	if err := m.Delete("del-1"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Should be gone from memory
	m.mu.RLock()
	_, ok := m.sessions["del-1"]
	m.mu.RUnlock()
	if ok {
		t.Error("expected session to be removed from memory")
	}

	// Should be gone from storage
	_, err := m.storage.LoadSession("del-1")
	if err == nil {
		t.Error("expected session to be deleted from storage")
	}
}

func TestSystem_Delete_Missing(t *testing.T) {
	m, cleanup := newTestSystem(t, 100)
	defer cleanup()

	err := m.Delete("no-such-uuid")
	if err != nil {
		t.Errorf("Delete on missing session should not error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func TestSystem_Stats(t *testing.T) {
	m, cleanup := newTestSystem(t, 500)
	defer cleanup()

	stats := m.Stats()

	if stats["max_sessions"].(int) != 500 {
		t.Errorf("expected max_sessions 500, got %v", stats["max_sessions"])
	}
	if stats["session_count"].(int) != 0 {
		t.Errorf("expected session_count 0, got %v", stats["session_count"])
	}
	if stats["persisted"].(int) != 0 {
		t.Errorf("expected persisted 0, got %v", stats["persisted"])
	}

	// Add sessions and re-check
	for i := 0; i < 3; i++ {
		session := testSession("stats-"+string(rune('a'+i)), "S", "p")
		_ = m.Save(session)
	}

	stats = m.Stats()
	if stats["session_count"].(int) != 3 {
		t.Errorf("expected session_count 3, got %v", stats["session_count"])
	}
	if stats["persisted"].(int) != 3 {
		t.Errorf("expected persisted 3, got %v", stats["persisted"])
	}
}

// ---------------------------------------------------------------------------
// Eviction via Save
// ---------------------------------------------------------------------------

func TestSystem_Save_Eviction(t *testing.T) {
	m, cleanup := newTestSystem(t, 2) // max 2 sessions
	defer cleanup()

	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		session := &types.Session{
			UUID:      "evict-" + string(rune('a'+i)),
			Title:     "Session " + string(rune('A'+i)),
			Project:   "p",
			CreatedAt: base.Add(time.Duration(i) * time.Hour),
			UpdatedAt: base.Add(time.Duration(i) * time.Hour),
		}
		if err := m.Save(session); err != nil {
			t.Fatalf("Save %d failed: %v", i, err)
		}
	}

	// After saves with maxSessions=2, oldest should be evicted
	count, _ := m.storage.SessionCount()
	if count > 2 {
		t.Errorf("expected at most 2 sessions after eviction, got %d", count)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access safety
// ---------------------------------------------------------------------------

func TestSystem_ConcurrentSave(t *testing.T) {
	m, cleanup := newTestSystem(t, 1000)
	defer cleanup()

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			session := &types.Session{
				UUID:      "concurrent-" + string(rune('a'+id)),
				Title:     "Concurrent",
				Project:   "p",
				CreatedAt: time.Now(),
				UpdatedAt: time.Now(),
			}
			_ = m.Save(session)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	result := m.List()
	if len(result) != 10 {
		t.Errorf("expected 10 sessions after concurrent saves, got %d", len(result))
	}
}

func TestSystem_ConcurrentList(t *testing.T) {
	m, cleanup := newTestSystem(t, 1000)
	defer cleanup()

	// Populate
	for i := 0; i < 5; i++ {
		session := testSession("clist-"+string(rune('a'+i)), "S", "p")
		_ = m.Save(session)
	}

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_ = m.List()
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
	// Test passes if no race detector triggers
}
