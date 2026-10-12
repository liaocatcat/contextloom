package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// testBlock 构造测试块（确定性时间戳）。
func testBlock(id, content string) types.ContextBlock {
	return types.ContextBlock{
		ID:       id,
		Content:  content,
		Score:    42,
		LastUsed: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC),
		Dirty:    true,
	}
}

func addBlocks(idx *HybridIndex, blocks ...types.ContextBlock) {
	for i := range blocks {
		b := blocks[i]
		idx.Add(&b)
	}
}

func TestHybridIndex_SaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.json")

	idx := NewHybridIndex()
	addBlocks(idx,
		testBlock("b1", "func ReadFile 读取文件路径与偏移量"),
		testBlock("b2", "write_file 写入文件内容"),
		testBlock("b3", "list_files 列出目录"),
	)

	if err := idx.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded := NewHybridIndex()
	if err := loaded.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// 内容与元数据完整保留
	for _, id := range []string{"b1", "b2", "b3"} {
		b, ok := loaded.Get(id)
		if !ok {
			t.Fatalf("Get(%s) 未命中", id)
		}
		orig, _ := idx.Get(id)
		if b.Content != orig.Content || b.Score != orig.Score ||
			!b.LastUsed.Equal(orig.LastUsed) || b.Dirty != orig.Dirty {
			t.Errorf("Get(%s) 元数据不一致: got %+v want %+v", id, b, orig)
		}
	}

	// 倒排索引重建：关键词可检索
	got := loaded.Query("ReadFile", nil)
	if len(got) != 1 || got[0].ID != "b1" {
		t.Errorf("Query(ReadFile) = %v, want [b1]", idsOfPtr(got))
	}
	got = loaded.Query("写入", nil)
	if len(got) != 1 || got[0].ID != "b2" {
		t.Errorf("Query(写入) = %v, want [b2]", idsOfPtr(got))
	}

	// 空查询返回全部
	if all := loaded.Query("", nil); len(all) != 3 {
		t.Errorf("Query(\"\") = %d 块, want 3", len(all))
	}
}

func TestHybridIndex_Save_AtomicNoTempResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "idx.json") // 嵌套目录自动创建
	idx := NewHybridIndex()
	addBlocks(idx, testBlock("b1", "内容一"))
	if err := idx.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("索引文件不存在: %v", err)
	}
}

func TestHybridIndex_Load_MissingFile(t *testing.T) {
	idx := NewHybridIndex()
	if err := idx.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("Load 不存在文件应返回 nil, got %v", err)
	}
	if len(idx.blocks) != 0 {
		t.Error("加载后应为空索引")
	}
}

func TestHybridIndex_Load_CorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not-json!!"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewHybridIndex()
	if err := idx.Load(path); err == nil {
		t.Fatal("损坏文件应返回错误")
	}
	// 失败后索引保持可用（空）
	if len(idx.blocks) != 0 {
		t.Error("损坏加载失败后索引应为空")
	}
}

func TestHybridIndex_Load_VersionMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ver.json")
	data := `{"version":999,"saved_at":"2025-01-01T00:00:00Z","blocks":[]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := NewHybridIndex()
	if err := idx.Load(path); err == nil {
		t.Fatal("版本不兼容应返回错误")
	}
}

func TestHybridIndex_SaveLoad_RemoveConsistency(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.json")
	idx := NewHybridIndex()
	addBlocks(idx,
		testBlock("b1", "unique_keyword_alpha"),
		testBlock("b2", "other content"),
	)
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewHybridIndex()
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	loaded.Remove("b1")
	if got := loaded.Query("unique_keyword_alpha", nil); len(got) != 0 {
		t.Errorf("Remove 后 Query 仍命中: %v", idsOfPtr(got))
	}
	if _, ok := loaded.Get("b1"); ok {
		t.Error("Remove 后 Get 仍命中")
	}
	if got := loaded.Query("other", nil); len(got) != 1 {
		t.Errorf("Query(other) = %v, want [b2]", idsOfPtr(got))
	}
}

func TestHybridIndex_Load_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idx.json")
	idx := NewHybridIndex()
	addBlocks(idx, testBlock("old1", "stale content"))
	if err := idx.Save(path); err != nil {
		t.Fatal(err)
	}
	// 用另一批内容覆盖保存
	idx2 := NewHybridIndex()
	addBlocks(idx2, testBlock("new1", "fresh content"))
	if err := idx2.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded := NewHybridIndex()
	if err := loaded.Load(path); err != nil {
		t.Fatal(err)
	}
	if _, ok := loaded.Get("old1"); ok {
		t.Error("旧块不应残留")
	}
	if b, ok := loaded.Get("new1"); !ok || b.Content != "fresh content" {
		t.Errorf("新块加载异常: %+v ok=%v", b, ok)
	}
}

// idsOfPtr 提取查询结果 ID 列表便于断言（Query 返回指针切片）。
func idsOfPtr(blocks []*types.ContextBlock) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.ID
	}
	return out
}

// idsOf 提取值切片查询结果 ID 列表（Engine.Search 返回值类型）。
func idsOf(blocks []types.ContextBlock) []string {
	out := make([]string, len(blocks))
	for i, b := range blocks {
		out[i] = b.ID
	}
	return out
}
