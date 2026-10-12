package checkpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStore_NewCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cp")
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if st.Dir() != dir {
		t.Fatalf("Dir = %q", st.Dir())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("目录未创建: %v", err)
	}
}

func TestStore_SaveLoadLatest(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	msgs := []byte(`[{"role":"user","content":"hi"}]`)

	for i := 1; i <= 3; i++ {
		if err := st.Save(Checkpoint{RunID: "run1", Round: i, Messages: msgs}); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	cp, err := st.LoadLatest("run1")
	if err != nil {
		t.Fatalf("LoadLatest: %v", err)
	}
	if cp.Round != 3 {
		t.Fatalf("Round = %d, want 3", cp.Round)
	}
	if string(cp.Messages) != string(msgs) {
		t.Fatalf("Messages 不一致")
	}
	if cp.ID == "" {
		t.Fatal("ID 未自动生成")
	}
}

func TestStore_Save_KeepsCreatedAt(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	first := time.Now().Add(-time.Hour)
	if err := st.Save(Checkpoint{RunID: "r", Round: 1, CreatedAt: first}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := st.Save(Checkpoint{RunID: "r", Round: 2}); err != nil {
		t.Fatal(err)
	}
	cp, err := st.LoadLatest("r")
	if err != nil {
		t.Fatal(err)
	}
	if !cp.CreatedAt.Equal(first) {
		t.Fatalf("CreatedAt 被覆盖: %v != %v", cp.CreatedAt, first)
	}
	if cp.UpdatedAt.Before(cp.CreatedAt) {
		t.Fatalf("UpdatedAt 早于 CreatedAt")
	}
}

func TestStore_LoadLatest_NotExist(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	_, err := st.LoadLatest("nope")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("错误应为 os.ErrNotExist, got %v", err)
	}
}

func TestStore_LoadLatest_SkipsCorruptLine(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	path := filepath.Join(st.Dir(), "run-r.jsonl")
	os.WriteFile(path, []byte("{corrupt}\n"), 0o644)
	if err := st.Save(Checkpoint{RunID: "r", Round: 2}); err != nil {
		t.Fatal(err)
	}
	cp, err := st.LoadLatest("r")
	if err != nil {
		t.Fatalf("损坏行不应阻断: %v", err)
	}
	if cp.Round != 2 {
		t.Fatalf("Round = %d", cp.Round)
	}
}

func TestStore_List_Order(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	st.Save(Checkpoint{RunID: "a", Round: 1, Mode: "task", SessionID: "s1"})
	time.Sleep(5 * time.Millisecond)
	st.Save(Checkpoint{RunID: "b", Round: 5, Done: true, Mode: "goal", SessionID: "s2"})
	time.Sleep(5 * time.Millisecond)
	st.Save(Checkpoint{RunID: "a", Round: 2}) // 更新 a → a 变最新

	metas, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("len = %d", len(metas))
	}
	if metas[0].RunID != "a" || metas[1].RunID != "b" {
		t.Fatalf("排序错误: %+v", metas)
	}
	if metas[1].Round != 5 || !metas[1].Done || metas[1].Mode != "goal" || metas[1].SessionID != "s2" {
		t.Fatalf("meta 字段错误: %+v", metas[1])
	}
}

func TestStore_Prune(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	for i := 0; i < 5; i++ {
		st.Save(Checkpoint{RunID: string(rune('a' + i)), Round: 1})
		time.Sleep(2 * time.Millisecond)
	}
	removed, err := st.Prune(2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	metas, _ := st.List()
	if len(metas) != 2 {
		t.Fatalf("保留 %d 个, want 2", len(metas))
	}
	// 再 prune 不超过上限 → 不动
	removed, _ = st.Prune(10)
	if removed != 0 {
		t.Fatalf("不应再删, removed=%d", removed)
	}
	// max<=0 不动
	removed, _ = st.Prune(0)
	if removed != 0 {
		t.Fatalf("max<=0 不应删")
	}
}

func TestStore_Delete(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	st.Save(Checkpoint{RunID: "r", Round: 1})
	if err := st.Delete("r"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := st.LoadLatest("r"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("删除后应不存在, got %v", err)
	}
	// 幂等
	if err := st.Delete("r"); err != nil {
		t.Fatalf("重复删除应成功: %v", err)
	}
}

// TestStore_PruneAged 按 mtime 收敛 run 文件数（不解析内容，启动路径安全）。
func TestStore_PruneAged(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	// 造 5 个只写 run 文件（Save 会写内容，PruneAged 只看 mtime）
	for i := 0; i < 5; i++ {
		st.Save(Checkpoint{RunID: fmt.Sprintf("run-%d", i), Round: 1})
		time.Sleep(5 * time.Millisecond) // mtime 递增
	}
	removed, err := st.PruneAged(2)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 3 {
		t.Fatalf("removed = %d, want 3", removed)
	}
	metas, _ := st.List()
	if len(metas) != 2 {
		t.Fatalf("保留 %d 个, want 2", len(metas))
	}
	// 保留的是最新的两个
	if metas[0].RunID != "run-4" || metas[1].RunID != "run-3" {
		t.Fatalf("保留错误: %+v", metas)
	}
	// 不超过上限不动
	removed, _ = st.PruneAged(10)
	if removed != 0 {
		t.Fatalf("不应再删, removed=%d", removed)
	}
	// keep<=0 不动
	removed, _ = st.PruneAged(0)
	if removed != 0 {
		t.Fatalf("keep<=0 不应删")
	}
}

func TestStore_Save_EmptyRunID(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	if err := st.Save(Checkpoint{}); err == nil {
		t.Fatal("空 run_id 应报错")
	}
}

func TestStore_Concurrent(t *testing.T) {
	st, _ := New(filepath.Join(t.TempDir(), "cp"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			runID := string(rune('r' + n))
			for j := 1; j <= 5; j++ {
				_ = st.Save(Checkpoint{RunID: runID, Round: j, SessionID: "s"})
			}
		}(i)
	}
	wg.Wait()
	metas, err := st.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 8 {
		t.Fatalf("并发后 run 数 = %d, want 8", len(metas))
	}
	for _, m := range metas {
		cp, err := st.LoadLatest(m.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if cp.Round != 5 {
			t.Fatalf("run %s Round = %d, want 5", m.RunID, cp.Round)
		}
	}
}

func TestCheckpoint_JSONRoundTrip(t *testing.T) {
	cp := Checkpoint{ID: "id1", RunID: "r", Round: 3, Done: true, Result: "ok",
		Messages: []byte(`[{"role":"assistant","content":"done"}]`)}
	data, err := json.Marshal(cp)
	if err != nil {
		t.Fatal(err)
	}
	var back Checkpoint
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.RunID != "r" || back.Round != 3 || !back.Done || back.Result != "ok" {
		t.Fatalf("round-trip 失败: %+v", back)
	}
	if string(back.Messages) != string(cp.Messages) {
		t.Fatalf("Messages 不一致")
	}
}

// TestStore_LoadLatest_LargeFile_Fast 回归：巨文件（数 GB 级膨胀缺陷产物）
// 下 LoadLatest 必须只读尾部窗口快速返回，不得全量扫描。
// 历史缺陷：run-main.jsonl 932MB → Scanner 全量读取 → 启动卡 12s+。
func TestStore_LoadLatest_LargeFile_Fast(t *testing.T) {
	dir := t.TempDir()
	s := &Store{dir: dir}
	runID := "big-run"
	path := s.fileFor(runID)
	// 构造 ~300MB 前缀（无效行）+ 尾部一条有效快照
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	junk := make([]byte, 256*1024)
	for i := range junk {
		junk[i] = 'x'
	}
	for i := 0; i < 1200; i++ { // 300MB
		if _, err := f.Write(append(junk, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	want := Checkpoint{RunID: runID, Round: 99, Done: true, Result: "ok", SessionID: "s1"}
	data, _ := json.Marshal(want)
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
	f.Close()

	start := time.Now()
	got, err := s.LoadLatest(runID)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("LoadLatest err: %v", err)
	}
	if got.Round != 99 || !got.Done || got.Result != "ok" || got.SessionID != "s1" {
		t.Fatalf("LoadLatest got %+v, want round=99 done=true", got)
	}
	// 300MB 文件若全量扫描需数百 ms~秒级；尾部窗口应 <500ms
	if elapsed > 500*time.Millisecond {
		t.Fatalf("LoadLatest 巨文件耗时 %v，疑似全量扫描（应尾部窗口快速返回）", elapsed)
	}
}

// TestStore_Save_RolloverLargeFile 回归：Save 后文件超限自动滚动重写，
// 只保留最近 keepCheckpointLines 条，LoadLatest 仍返回最新快照。
func TestStore_Save_RolloverLargeFile(t *testing.T) {
	dir := t.TempDir()
	s := &Store{dir: dir}
	runID := "roll-run"
	// 预置超限文件：直接写入超过阈值的字节数（含足够多的有效行）
	path := s.fileFor(runID)
	lines := make([][]byte, 0, 300)
	for i := 0; i < 300; i++ {
		cp := Checkpoint{RunID: runID, Round: i + 1, Done: i == 299, Result: fmt.Sprintf("r%d", i+1)}
		data, _ := json.Marshal(cp)
		lines = append(lines, append(data, '\n'))
	}
	// 用 padding 撑大第一个文件超过阈值
	var sb bytes.Buffer
	padding := bytes.Repeat([]byte{'z'}, 1024*1024) // 1MB 单行 padding（有效行，可解析字段不涉及）
	_ = padding
	// 先写 200 行（内容较小），再额外写入大 payload 行让文件超限
	for i := 0; i < 200; i++ {
		sb.Write(lines[i])
	}
	// 手动写入大 payload 快照（Round=201，含大消息）使文件 > 阈值
	big := Checkpoint{RunID: runID, Round: 201, Messages: bytes.Repeat([]byte("m"), 2*1024*1024)}
	bigData, _ := json.Marshal(big)
	sb.Write(append(bigData, '\n'))
	if err := os.WriteFile(path, sb.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	// 触发 Save（阈值检查在 Save 内）——用小快照触发即可
	cp := Checkpoint{RunID: runID, Round: 202, Done: true, Result: "final"}
	if err := s.Save(cp); err != nil {
		t.Fatal(err)
	}
	// 文件应已被滚动重写为远小于原大小
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxCheckpointFileSize {
		t.Fatalf("滚动重写后文件仍超限: %d bytes", fi.Size())
	}
	// LoadLatest 必须返回最新（Round=202）
	got, err := s.LoadLatest(runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 202 || !got.Done || got.Result != "final" {
		t.Fatalf("LoadLatest got round=%d done=%v, want 202/true", got.Round, got.Done)
	}
}

// TestStore_Save_Normal_NoRollover 回归：小文件 Save 不触发重写（纯追加）。
func TestStore_Save_Normal_NoRollover(t *testing.T) {
	dir := t.TempDir()
	s := &Store{dir: dir}
	runID := "normal-run"
	for i := 0; i < 5; i++ {
		if err := s.Save(Checkpoint{RunID: runID, Round: i + 1}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.LoadLatest(runID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Round != 5 {
		t.Fatalf("got round %d, want 5", got.Round)
	}
}
