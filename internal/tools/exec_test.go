package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/sandbox"
)

func TestSearchContent_GrepSuccess(t *testing.T) {
	dir, err := os.MkdirTemp("", "search_content_grep_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write test files
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "bye.go"), []byte("package main\n\nfunc goodbye() {\n\tfmt.Println(\"goodbye\")\n}\n"), 0644)

	ctx := context.Background()
	result, err := SearchContent(ctx, map[string]any{
		"pattern": "hello",
		"path":    dir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m, ok := result.(map[string]any)
	if !ok {
		t.Fatal("expected map result")
	}

	count, _ := m["count"].(int)
	if count < 1 {
		t.Errorf("expected at least 1 match, got %d", count)
	}

	matches, _ := m["matches"].([]string)
	found := false
	for _, match := range matches {
		if strings.Contains(match, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected matches to contain 'hello', got %v", matches)
	}
}

func TestSearchContent_FallbackToPureGo(t *testing.T) {
	dir, err := os.MkdirTemp("", "search_content_fallback_test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write test files
	os.WriteFile(filepath.Join(dir, "hello.go"), []byte("package main\n\nfunc main() {\n\tfmt.Println(\"hello world\")\n}\n"), 0644)
	os.WriteFile(filepath.Join(dir, "bye.go"), []byte("package main\n\nfunc goodbye() {\n\tfmt.Println(\"goodbye\")\n}\n"), 0644)

	ctx := context.Background()

	// Test the pure Go walk fallback directly
	result, err := searchWithWalk(ctx, "hello", dir)
	if err != nil {
		t.Fatalf("unexpected error from walk fallback: %v", err)
	}

	m := result.(map[string]any)
	count, _ := m["count"].(int)
	if count < 1 {
		t.Errorf("expected at least 1 match from walk, got %d", count)
	}

	matches, _ := m["matches"].([]string)
	if len(matches) < 1 {
		t.Errorf("expected at least 1 match, got %v", matches)
	}

	found := false
	for _, match := range matches {
		if strings.Contains(match, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected matches to contain 'hello', got %v", matches)
	}
}

func TestSearchContent_WalkNoMatches(t *testing.T) {
	dir, err := os.MkdirTemp("", "search_content_walk_nomatch")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	os.WriteFile(filepath.Join(dir, "test.go"), []byte("package main\n\nfunc main() {}\n"), 0644)

	ctx := context.Background()
	result, err := searchWithWalk(ctx, "nonexistent", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	count, _ := m["count"].(int)
	if count != 0 {
		t.Errorf("expected 0 matches, got %d", count)
	}
}

func TestSearchContent_WalkNonexistentDir(t *testing.T) {
	ctx := context.Background()
	result, err := searchWithWalk(ctx, "pattern", "/nonexistent/path/for/testing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	count, _ := m["count"].(int)
	if count != 0 {
		t.Errorf("expected 0 matches for non-existent dir, got %d", count)
	}
}

func TestSearchContent_WalkBinaryFile(t *testing.T) {
	dir, err := os.MkdirTemp("", "search_content_walk_binary")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// Write a binary file (contains null bytes)
	os.WriteFile(filepath.Join(dir, "binary.bin"), []byte{0x00, 0x01, 0x02, 0x03, 0x00, 0xFF}, 0644)
	os.WriteFile(filepath.Join(dir, "text.txt"), []byte("this is a text file\nwith hello in it\n"), 0644)

	ctx := context.Background()
	result, err := searchWithWalk(ctx, "hello", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	m := result.(map[string]any)
	count, _ := m["count"].(int)
	if count < 1 {
		t.Errorf("expected at least 1 match, got %d", count)
	}
	matches, _ := m["matches"].([]string)
	found := false
	for _, match := range matches {
		if strings.Contains(match, "hello") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected to find 'hello' in text file, got %v", matches)
	}
}

// TestExecCommand_SandboxNetDeny 集成测试：沙箱上下文执行 env 命令，
// 输出必须含 HTTP_PROXY=http://127.0.0.1:9（死端口）与 CONTEXTLOOM_SANDBOX_NET=deny，
// 且不含 NO_PROXY（防豁免）——验证 exec.go 沙箱分支的网络隔离接线真实生效。
func TestExecCommand_SandboxNetDeny(t *testing.T) {
	fs, err := sandbox.New(filepath.Join(t.TempDir(), "sb"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := sandbox.WithSandbox(context.Background(), fs)

	res, err := ExecCommand(ctx, map[string]any{"command": "env"})
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("结果类型异常: %T", res)
	}
	stdout, _ := m["stdout"].(string)
	if !strings.Contains(stdout, "HTTP_PROXY=http://127.0.0.1:9") {
		t.Errorf("沙箱环境应含 HTTP_PROXY=http://127.0.0.1:9, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "HTTPS_PROXY=http://127.0.0.1:9") {
		t.Errorf("沙箱环境应含 HTTPS_PROXY=http://127.0.0.1:9, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "CONTEXTLOOM_SANDBOX_NET=deny") {
		t.Errorf("沙箱环境应含 CONTEXTLOOM_SANDBOX_NET=deny, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "NO_PROXY") {
		t.Errorf("沙箱环境不应含 NO_PROXY（防豁免绕过）: %s", stdout)
	}
}

// TestExecCommand_NoSandbox_NoNetDeny 对照：非沙箱上下文不注入网络拒绝
// （网络隔离仅在沙箱会话生效，正常会话不受影响）。
func TestExecCommand_NoSandbox_NoNetDeny(t *testing.T) {
	res, err := ExecCommand(context.Background(), map[string]any{"command": "env"})
	if err != nil {
		t.Fatalf("ExecCommand: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("结果类型异常: %T", res)
	}
	stdout, _ := m["stdout"].(string)
	if strings.Contains(stdout, "CONTEXTLOOM_SANDBOX_NET=deny") {
		t.Errorf("非沙箱上下文不应注入网络拒绝标记: %s", stdout)
	}
}
