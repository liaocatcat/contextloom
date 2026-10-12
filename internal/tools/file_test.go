package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ── ReadFile Tests ──

func TestReadFile_Success(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}

	gotContent, _ := m["content"].(string)
	totalLines, _ := m["total_lines"].(int)

	// splitLines preserves trailing newline as an extra empty line
	if totalLines != 6 {
		t.Errorf("expected 6 total_lines, got %d", totalLines)
	}
	// Content includes trailing newline from splitLines/joinLines roundtrip
	if gotContent != "line1\nline2\nline3\nline4\nline5\n" {
		t.Errorf("expected content with trailing newline, got %q", gotContent)
	}
}

func TestReadFile_NotFound(t *testing.T) {
	ctx := context.Background()
	_, err := ReadFile(ctx, map[string]any{"path": "/nonexistent/file/path.txt"})
	if err == nil {
		t.Fatal("expected error for non-existent file, got nil")
	}
}

func TestReadFile_EmptyPath(t *testing.T) {
	ctx := context.Background()
	_, err := ReadFile(ctx, map[string]any{"path": ""})
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestReadFile_WithOffset(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_offset_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath, "offset": float64(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	gotContent, _ := m["content"].(string)
	totalLines, _ := m["total_lines"].(int)

	if totalLines != 6 {
		t.Errorf("expected 6 total_lines, got %d", totalLines)
	}
	if gotContent != "line3\nline4\nline5\n" {
		t.Errorf("expected 'line3\\nline4\\nline5\\n', got %q", gotContent)
	}
}

func TestReadFile_WithLimit(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_limit_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath, "limit": float64(3)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	gotContent, _ := m["content"].(string)
	totalLines, _ := m["total_lines"].(int)

	if totalLines != 6 {
		t.Errorf("expected 6 total_lines, got %d", totalLines)
	}
	if gotContent != "line1\nline2\nline3" {
		t.Errorf("expected 'line1\\nline2\\nline3', got %q", gotContent)
	}
}

func TestReadFile_WithOffsetAndLimit(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_offset_limit_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "line1\nline2\nline3\nline4\nline5\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath, "offset": float64(1), "limit": float64(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	gotContent, _ := m["content"].(string)

	if gotContent != "line2\nline3" {
		t.Errorf("expected 'line2\\nline3', got %q", gotContent)
	}
}

func TestReadFile_OffsetOutOfBounds(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_oob_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "line1\nline2\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath, "offset": float64(100)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	gotContent, _ := m["content"].(string)

	if gotContent != "" {
		t.Errorf("expected empty content for out-of-bounds offset, got %q", gotContent)
	}
}

func TestReadFile_NegativeOffset(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_neg_offset_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "line1\nline2\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath, "offset": float64(-5)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	gotContent, _ := m["content"].(string)

	// Negative offset should clamp to 0
	if gotContent != "line1\nline2\n" {
		t.Errorf("expected full content for negative offset, got %q", gotContent)
	}
}

func TestReadFile_EmptyFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "readfile_empty_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(testPath, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReadFile(ctx, map[string]any{"path": testPath})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	gotContent, _ := m["content"].(string)
	totalLines, _ := m["total_lines"].(int)

	if gotContent != "" {
		t.Errorf("expected empty content, got %q", gotContent)
	}
	if totalLines != 0 {
		t.Errorf("expected 0 total_lines, got %d", totalLines)
	}
}

// ── WriteFile Tests ──

func TestWriteFile_Success(t *testing.T) {
	dir, err := os.MkdirTemp("", "writefile_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "output.txt")
	content := "hello world\n"

	ctx := context.Background()
	result, err := WriteFile(ctx, map[string]any{"path": testPath, "content": content})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}

	written, _ := m["written"].(int)
	if written != len(content) {
		t.Errorf("expected written=%d, got %d", len(content), written)
	}

	// Verify file was written correctly
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read back file: %v", err)
	}
	if string(data) != content {
		t.Errorf("expected file content %q, got %q", content, string(data))
	}
}

func TestWriteFile_Overwrite(t *testing.T) {
	dir, err := os.MkdirTemp("", "writefile_overwrite_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "output.txt")
	initialContent := "initial content\n"
	newContent := "updated content\n"

	// Write initial content
	if err := os.WriteFile(testPath, []byte(initialContent), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := WriteFile(ctx, map[string]any{"path": testPath, "content": newContent})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	written, _ := m["written"].(int)
	if written != len(newContent) {
		t.Errorf("expected written=%d, got %d", len(newContent), written)
	}

	// Verify file was overwritten
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read back file: %v", err)
	}
	if string(data) != newContent {
		t.Errorf("expected file content %q, got %q", newContent, string(data))
	}
}

func TestWriteFile_EmptyContent(t *testing.T) {
	dir, err := os.MkdirTemp("", "writefile_empty_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "empty.txt")

	ctx := context.Background()
	result, err := WriteFile(ctx, map[string]any{"path": testPath, "content": ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	written, _ := m["written"].(int)
	if written != 0 {
		t.Errorf("expected written=0, got %d", written)
	}
}

// ── ReplaceInFile Tests ──

func TestReplaceInFile_Success(t *testing.T) {
	dir, err := os.MkdirTemp("", "replaceinfile_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.go")
	content := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	result, err := ReplaceInFile(ctx, map[string]any{
		"path":    testPath,
		"old_str": "hello",
		"new_str": "world",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	replaced, _ := m["replaced"].(bool)
	if !replaced {
		t.Error("expected replaced=true")
	}

	// Verify the replacement
	data, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("failed to read back file: %v", err)
	}
	expected := "package main\n\nfunc main() {\n\tfmt.Println(\"world\")\n}\n"
	if string(data) != expected {
		t.Errorf("expected %q, got %q", expected, string(data))
	}
}

func TestReplaceInFile_NotFound(t *testing.T) {
	dir, err := os.MkdirTemp("", "replaceinfile_notfound_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	content := "hello world\n"
	if err := os.WriteFile(testPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = ReplaceInFile(ctx, map[string]any{
		"path":    testPath,
		"old_str": "nonexistent",
		"new_str": "replacement",
	})
	if err == nil {
		t.Fatal("expected error when old_str not found, got nil")
	}
}

func TestReplaceInFile_EmptyPath(t *testing.T) {
	ctx := context.Background()
	_, err := ReplaceInFile(ctx, map[string]any{
		"path":    "",
		"old_str": "hello",
		"new_str": "world",
	})
	if err == nil {
		t.Fatal("expected error for empty path, got nil")
	}
}

func TestReplaceInFile_EmptyOldStr(t *testing.T) {
	dir, err := os.MkdirTemp("", "replaceinfile_emptyold_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	testPath := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(testPath, []byte("content"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	_, err = ReplaceInFile(ctx, map[string]any{
		"path":    testPath,
		"old_str": "",
		"new_str": "replacement",
	})
	if err == nil {
		t.Fatal("expected error for empty old_str, got nil")
	}
}

func TestReplaceInFile_InvalidPath(t *testing.T) {
	ctx := context.Background()
	_, err := ReplaceInFile(ctx, map[string]any{
		"path":    "justfilename",
		"old_str": "hello",
		"new_str": "world",
	})
	if err == nil {
		t.Fatal("expected error for path without separator, got nil")
	}
}

// ── splitLines / joinLines Tests ──

func TestSplitLines_Empty(t *testing.T) {
	lines := splitLines("")
	if len(lines) != 0 {
		t.Errorf("expected 0 lines for empty string, got %d", len(lines))
	}
}

func TestSplitLines_SingleLine(t *testing.T) {
	lines := splitLines("hello")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	if lines[0] != "hello" {
		t.Errorf("expected 'hello', got %q", lines[0])
	}
}

func TestSplitLines_MultipleLines(t *testing.T) {
	lines := splitLines("a\nb\nc\n")
	// "a\nb\nc\n" should split to ["a", "b", "c", ""]
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %v", len(lines), lines)
	}
	if lines[0] != "a" {
		t.Errorf("expected 'a', got %q", lines[0])
	}
	if lines[1] != "b" {
		t.Errorf("expected 'b', got %q", lines[1])
	}
	if lines[2] != "c" {
		t.Errorf("expected 'c', got %q", lines[2])
	}
	if lines[3] != "" {
		t.Errorf("expected '', got %q", lines[3])
	}
}

func TestJoinLines_Empty(t *testing.T) {
	result := joinLines([]string{})
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

func TestJoinLines_SingleLine(t *testing.T) {
	result := joinLines([]string{"hello"})
	if result != "hello" {
		t.Errorf("expected 'hello', got %q", result)
	}
}

func TestJoinLines_MultipleLines(t *testing.T) {
	result := joinLines([]string{"a", "b", "c"})
	if result != "a\nb\nc" {
		t.Errorf("expected 'a\\nb\\nc', got %q", result)
	}
}

func TestSplitJoinRoundtrip(t *testing.T) {
	original := "line1\nline2\nline3\n"
	lines := splitLines(original)
	joined := joinLines(lines)
	// splitLines("a\nb\nc\n") -> ["a","b","c",""] -> joinLines -> "a\nb\nc\n"
	if joined != original {
		t.Errorf("roundtrip failed: expected %q, got %q", original, joined)
	}
}
