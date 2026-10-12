package trace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecorder_RecordAndRecent(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	r.Record(Entry{Event: "agent_round", Mode: "task", Round: 1, Tool: "read_file", Ms: 12})
	r.Record(Entry{Event: "tool_call", Tool: "exec_command", Ms: 345, Err: ""})
	r.Record(Entry{Event: "error", Err: "boom"})

	recent := r.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("Recent 应返回 3 条，got %d", len(recent))
	}
	// 倒序：最新在前
	if recent[0].Event != "error" || recent[2].Event != "agent_round" {
		t.Fatalf("Recent 顺序错误: %+v", recent)
	}
	if recent[1].Tool != "exec_command" || recent[1].Ms != 345 {
		t.Fatalf("Recent[1] 内容错误: %+v", recent[1])
	}
}

func TestRecorder_Disabled(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "trace")
	r, err := New(dir, false)
	if err != nil {
		t.Fatalf("New disabled: %v", err)
	}
	r.Record(Entry{Event: "x"})
	r.Close()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled 不应创建目录")
	}
	if got := r.Recent(5); got != nil {
		t.Fatalf("disabled Recent 应为 nil")
	}
}

func TestRecorder_DayRotation(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	// 今天
	r.Record(Entry{Event: "today", TS: time.Now()})
	// 昨天（强制轮转）
	yesterday := time.Now().Add(-24 * time.Hour)
	r.Record(Entry{Event: "yesterday", TS: yesterday})

	files, _ := filepath.Glob(filepath.Join(dir, "trace-*.jsonl"))
	if len(files) < 2 {
		t.Fatalf("应产生 2 个日文件，got %d (%v)", len(files), files)
	}
	recent := r.Recent(10)
	if len(recent) != 2 {
		t.Fatalf("Recent 应跨日返回 2 条，got %d", len(recent))
	}
	// 倒序：today 在前
	if recent[0].Event != "today" || recent[1].Event != "yesterday" {
		t.Fatalf("跨日顺序错误: %+v", recent)
	}
}

func TestRecorder_RecentLimit(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, true)
	defer r.Close()
	for i := 0; i < 10; i++ {
		r.Record(Entry{Event: "e", Round: i})
	}
	if got := r.Recent(3); len(got) != 3 {
		t.Fatalf("Recent(3) 应返回 3 条，got %d", len(got))
	}
}

func TestRecorder_ConcurrentSafe(t *testing.T) {
	dir := t.TempDir()
	r, _ := New(dir, true)
	defer r.Close()
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				r.Record(Entry{Event: "e", Round: j})
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	if got := r.Recent(1000); len(got) != 200 {
		t.Fatalf("并发写入应 200 条，got %d", len(got))
	}
}
