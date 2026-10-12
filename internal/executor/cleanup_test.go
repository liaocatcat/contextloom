package executor

import (
	"os"
	"strings"
	"testing"
)

// resetOffloaded 清空包级临时文件记录，保证测试隔离
func resetOffloaded() {
	offloadedTempFilesMu.Lock()
	defer offloadedTempFilesMu.Unlock()
	for _, f := range offloadedTempFiles {
		os.Remove(f)
	}
	offloadedTempFiles = nil
}

// TestCleanupTempFiles_SessionOnly temp_doc_policy=session_only 时退出清理
func TestCleanupTempFiles_SessionOnly(t *testing.T) {
	resetOffloaded()
	defer resetOffloaded()

	big := strings.Repeat("x", 200000) // 超过 128KB 转存阈值
	out := maybeOffloadToFile(big, "run_tests")
	if !strings.Contains(out, "[已转存:") {
		t.Fatalf("大结果应转存临时文件: %q", out)
	}
	path := extractTempPath(out)
	if path == "" {
		t.Fatal("未提取到临时文件路径")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("临时文件应存在: %v", err)
	}

	CleanupTempFiles("session_only")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("session_only 策略下临时文件应被删除, stat err=%v", err)
	}
}

// TestCleanupTempFiles_Persistent temp_doc_policy=persistent 时退出保留
func TestCleanupTempFiles_Persistent(t *testing.T) {
	resetOffloaded()
	defer resetOffloaded()

	big := strings.Repeat("y", 200000) // 超过 128KB 转存阈值
	out := maybeOffloadToFile(big, "run_tests")
	path := extractTempPath(out)
	if path == "" {
		t.Fatal("未提取到临时文件路径")
	}

	CleanupTempFiles("persistent")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("persistent 策略下临时文件应保留, err=%v", err)
	}
	// 清理现场
	os.Remove(path)
}

// extractTempPath 从转存提示文本中提取临时文件绝对路径
func extractTempPath(s string) string {
	const marker = "[已转存: "
	start := strings.Index(s, marker)
	if start < 0 {
		return ""
	}
	rest := s[start+len(marker):]
	end := strings.Index(rest, "]")
	if end < 0 {
		return ""
	}
	return rest[:end]
}
