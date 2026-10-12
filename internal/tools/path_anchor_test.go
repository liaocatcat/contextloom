package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestResolveToolPath_CWDAnchoring 回归测试：修复"误报写入成功"。
// 历史缺陷：SetProjectRoot 被清空、相对路径按进程 CWD 解析——GUI 从
// Finder/.app 启动时 CWD 是 / 或 $HOME，write_file "auth.go" 把文件写到
// CWD 下并返回成功，项目里却没有文件。
// 修复语义：相对路径必须锚定项目根（SetProjectRoot 设置），与进程 CWD 无关。
func TestResolveToolPath_CWDAnchoring(t *testing.T) {
	projRoot := t.TempDir()
	cwd := t.TempDir() // 模拟 GUI 从无关目录启动（CWD ≠ 项目根）

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}

	// 未设置项目根：回退 CWD（测试兜底语义不变）。
	// macOS 上 os.Getwd() 返回物理解析路径（/private/var/...），与 TempDir 的
	// 逻辑路径（/var/...）同目录不同拼写 → 用 EvalSymlinks 归一后比较。
	cwdReal, _ := filepath.EvalSymlinks(cwd)
	if got := resolveToolPath("a.txt"); filepath.Dir(got) != cwdReal {
		t.Fatalf("未设置项目根时应回退 CWD %s，got %q", cwdReal, got)
	}

	// 设置项目根后：相对路径锚定项目根，无视 CWD
	SetProjectRoot(projRoot)
	defer SetProjectRoot("")
	if got := resolveToolPath("auth.go"); got != filepath.Join(projRoot, "auth.go") {
		t.Fatalf("相对路径应锚定项目根 %s，got %q", projRoot, got)
	}
	if got := resolveToolPath("./sub/x.go"); got != filepath.Join(projRoot, "sub", "x.go") {
		t.Fatalf("./x 应锚定项目根，got %q", got)
	}
	if got := resolveToolPath("../y.go"); got != filepath.Clean(filepath.Join(projRoot, "..", "y.go")) {
		t.Fatalf("../x 应基于项目根解析，got %q", got)
	}
	// 绝对路径原样保留
	if got := resolveToolPath(cwd); got != cwd {
		t.Fatalf("绝对路径应原样保留，got %q", got)
	}
}

// TestWriteFile_CWDAnchoredToProjectRoot 端到端：CWD 与项目根分离时，
// write_file 相对路径实际落盘到项目根（而非 CWD），并返回绝对路径。
func TestWriteFile_CWDAnchoredToProjectRoot(t *testing.T) {
	projRoot := t.TempDir()
	cwd := t.TempDir()

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldWd)
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	SetProjectRoot(projRoot)
	defer SetProjectRoot("")

	res, err := WriteFile(context.Background(), map[string]any{
		"path": "docs/note.md", "content": "hello",
	})
	if err != nil {
		t.Fatalf("WriteFile 失败: %v", err)
	}
	m, _ := res.(map[string]any)
	// 项目根下真实存在（而不是 CWD 下）
	if _, err := os.Stat(filepath.Join(projRoot, "docs", "note.md")); err != nil {
		t.Fatalf("文件应落盘到项目根: %v", err)
	}
	// 返回值含绝对路径自证
	if p, _ := m["path"].(string); p != filepath.Join(projRoot, "docs", "note.md") {
		t.Fatalf("返回 path 应为项目根绝对路径，got %q", p)
	}
	// CWD 下不应产生任何文件
	if _, err := os.Stat(filepath.Join(cwd, "docs")); !os.IsNotExist(err) {
		t.Fatalf("CWD 下不应有文件（误报成功的旧路径行为）: err=%v", err)
	}
}

// TestReplaceInFile_BareFilenameAllowed 回归：裸文件名（main.go）此前被
// os.ErrInvalid 拒绝、与 write_file 语义不一致，现应锚定项目根正常替换。
func TestReplaceInFile_BareFilenameAllowed(t *testing.T) {
	projRoot := t.TempDir()
	SetProjectRoot(projRoot)
	defer SetProjectRoot("")

	if err := os.WriteFile(filepath.Join(projRoot, "main.go"), []byte("old line\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReplaceInFile(context.Background(), map[string]any{
		"path": "main.go", "old_str": "old line", "new_str": "new line",
	}); err != nil {
		t.Fatalf("裸文件名应可替换（锚定项目根）: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(projRoot, "main.go"))
	if string(data) != "new line\n" {
		t.Fatalf("替换未生效，got %q", string(data))
	}
}
