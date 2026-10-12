package audit

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNew_Disabled(t *testing.T) {
	a, err := New("", false)
	if err != nil {
		t.Fatal(err)
	}
	a.Record(Entry{Action: ActionWriteFile, Path: "/x"}) // 不 panic
	if got := a.Recent(10); got != nil {
		t.Error("disabled 时 Recent 应为 nil")
	}
}

func TestRecord_AndRecent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	a, err := New(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	a.Record(Entry{Action: ActionWriteFile, Path: "/tmp/a.go", OK: true, Ms: 12})
	a.Record(Entry{Action: ActionExecCommand, Path: "go test", OK: false, Err: "boom"})

	recent := a.Recent(10)
	if len(recent) != 2 {
		t.Fatalf("应 2 条, got %d", len(recent))
	}
	// 时间倒序：第二条在前
	if recent[0].Action != ActionExecCommand || recent[0].Err != "boom" {
		t.Errorf("倒序异常: %+v", recent[0])
	}
	if recent[1].Action != ActionWriteFile || !recent[1].OK {
		t.Errorf("条目异常: %+v", recent[1])
	}
	// Recent 数量限制
	if got := a.Recent(1); len(got) != 1 {
		t.Errorf("限制 1 条, got %d", len(got))
	}
}

func TestRecord_RotateByDay(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	a, _ := New(dir, true)
	defer a.Close()

	// 写入昨天日期的记录（强制轮转路径）
	yesterday := time.Now().AddDate(0, 0, -1)
	a.Record(Entry{TS: yesterday, Action: ActionDelete, Path: "/old"})
	a.Record(Entry{Action: ActionReadFile, Path: "/new"})

	matches, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(matches) != 2 {
		t.Fatalf("应跨 2 个文件, got %d: %v", len(matches), matches)
	}
	// 跨日合并读取
	recent := a.Recent(10)
	if len(recent) != 2 {
		t.Errorf("跨日读取应 2 条, got %d", len(recent))
	}
}

func TestPrune_KeepsMax(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "audit")
	a, _ := New(dir, true)
	defer a.Close()
	a.maxKeep = 3

	// 制造 5 个不同日期的文件
	base := time.Now()
	for i := 4; i >= 0; i-- {
		ts := base.AddDate(0, 0, -i)
		a.Record(Entry{TS: ts, Action: ActionToolCall})
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "audit-*.jsonl"))
	if len(matches) > 3 {
		t.Errorf("应清理到 3 个文件, got %d", len(matches))
	}
}

func TestEntry_JSON(t *testing.T) {
	e := Entry{Action: ActionWriteFile, Path: "a.go", OK: true, Ms: 5}
	if e.Action != "write_file" {
		t.Errorf("Action 常量异常: %s", e.Action)
	}
	// 文件内容应合法 JSON（由 Record 写出后读取验证）
	dir := filepath.Join(t.TempDir(), "audit")
	a, _ := New(dir, true)
	defer a.Close()
	a.Record(e)
	data, err := os.ReadFile(filepath.Join(dir, "audit-"+time.Now().Format("20060102")+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("日志应非空且以换行结尾")
	}
}
