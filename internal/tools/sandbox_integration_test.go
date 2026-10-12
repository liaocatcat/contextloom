package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/sandbox"
)

// newSandboxCtx 创建沙箱上下文（镜像目录在临时目录）。
func newSandboxCtx(t *testing.T) (context.Context, *sandbox.ShadowFS) {
	t.Helper()
	fs, err := sandbox.New(filepath.Join(t.TempDir(), "sb"))
	if err != nil {
		t.Fatal(err)
	}
	return sandbox.WithSandbox(context.Background(), fs), fs
}

func TestWriteFile_SandboxRedirect(t *testing.T) {
	ctx, _ := newSandboxCtx(t)
	real := filepath.Join(t.TempDir(), "proj", "a.go")
	_, err := WriteFile(ctx, map[string]any{"path": real, "content": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	// 真实文件不应存在
	if _, err := os.Stat(real); err == nil {
		t.Error("沙箱模式下真实文件不应被写入")
	}
}

func TestReadFile_SandboxMirrorFirst(t *testing.T) {
	ctx, fs := newSandboxCtx(t)
	real := filepath.Join(t.TempDir(), "proj", "b.go")
	// 写镜像（模拟沙箱内修改）
	os.WriteFile(fs.ResolveWrite(real), []byte("mirror-content"), 0o644)
	// 真实文件存在不同内容
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("real-content"), 0o644)

	out, err := ReadFile(ctx, map[string]any{"path": real})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["content"].(string), "mirror-content") {
		t.Errorf("沙箱模式应读镜像内容: %v", m["content"])
	}
}

func TestReadFile_SandboxFallbackReal(t *testing.T) {
	ctx, _ := newSandboxCtx(t)
	real := filepath.Join(t.TempDir(), "proj", "c.go")
	os.MkdirAll(filepath.Dir(real), 0o755)
	os.WriteFile(real, []byte("real-only"), 0o644)

	out, err := ReadFile(ctx, map[string]any{"path": real})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["content"].(string), "real-only") {
		t.Errorf("无镜像应回退真实: %v", m["content"])
	}
}

func TestReplaceInFile_Sandbox(t *testing.T) {
	ctx, fs := newSandboxCtx(t)
	real := filepath.Join(t.TempDir(), "proj", "d.go")
	// 沙箱内创建文件（走镜像）
	WriteFile(ctx, map[string]any{"path": real, "content": "old text"})
	// 替换
	_, err := ReplaceInFile(ctx, map[string]any{
		"path": real, "old_str": "old", "new_str": "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	// 镜像内容已更新
	data, _ := os.ReadFile(fs.MirrorPath(real))
	if string(data) != "new text" {
		t.Errorf("镜像内容应更新: %q", data)
	}
	// 真实文件仍不存在
	if _, err := os.Stat(real); err == nil {
		t.Error("真实文件不应存在")
	}
}

func TestListFiles_SandboxMerge(t *testing.T) {
	ctx, fs := newSandboxCtx(t)
	dir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "real.txt"), []byte("r"), 0o644)
	// 沙箱内新建文件（镜像）
	WriteFile(ctx, map[string]any{"path": filepath.Join(dir, "sb-new.txt"), "content": "n"})

	out, err := ListFiles(ctx, map[string]any{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	names := out.(map[string]any)["files"].([]string)
	var hasReal, hasNew bool
	for _, n := range names {
		if n == "real.txt" {
			hasReal = true
		}
		if n == "sb-new.txt" {
			hasNew = true
		}
	}
	if !hasReal || !hasNew {
		t.Errorf("列表应合并镜像+真实: %v", names)
	}
	_ = fs
}

func TestExecCommand_SandboxCwd(t *testing.T) {
	ctx, fs := newSandboxCtx(t)
	// 沙箱模式下 pwd 应指向镜像工作区
	out, err := ExecCommand(ctx, map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if ec, _ := m["exit_code"].(int); ec != 0 {
		t.Fatalf("pwd 失败: %v", m)
	}
	pwd := strings.TrimSpace(m["stdout"].(string))
	// bash 的 pwd 输出物理路径（/var → /private/var），root 为字符串路径，
	// 用 EvalSymlinks 归一化后比较
	normRoot := fs.Root()
	if r, err := filepath.EvalSymlinks(fs.Root()); err == nil {
		normRoot = r
	}
	if !strings.HasPrefix(pwd, normRoot) {
		t.Errorf("沙箱模式 pwd 应在镜像内: %q (root=%q)", pwd, normRoot)
	}
}

func TestExecCommand_NoSandbox_Unchanged(t *testing.T) {
	// 无沙箱 ctx：行为不变
	out, err := ExecCommand(context.Background(), map[string]any{"command": "echo ok"})
	if err != nil {
		t.Fatal(err)
	}
	m := out.(map[string]any)
	if !strings.Contains(m["stdout"].(string), "ok") {
		t.Errorf("无沙箱应正常执行: %v", m)
	}
}
