package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewAndRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sb")
	fs, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fs.Root() != dir {
		t.Errorf("Root 异常: %s", fs.Root())
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("镜像根目录应已创建")
	}
}

func TestMirrorPath_Absolute(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	real := filepath.Join(t.TempDir(), "proj", "a.go")
	mirror := fs.MirrorPath(real)
	// 镜像路径应在沙箱根下
	if !strings.HasPrefix(mirror, fs.Root()) {
		t.Errorf("镜像路径应在沙箱根下: %s", mirror)
	}
	if mirror == real {
		t.Error("镜像路径不应等于真实路径")
	}
}

func TestResolveWrite_Redirects(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	real := filepath.Join(t.TempDir(), "proj", "sub", "a.go")
	mirror := fs.ResolveWrite(real)
	if _, err := os.Stat(filepath.Dir(mirror)); err != nil {
		t.Error("镜像父目录应自动创建")
	}
	if err := os.WriteFile(mirror, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 真实路径不应被写入
	if _, err := os.Stat(real); err == nil {
		t.Error("真实路径不应存在（写已重定向）")
	}
}

func TestResolveRead_MirrorFirst(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	real := filepath.Join(t.TempDir(), "proj", "b.go")
	// 无镜像：回退真实
	if p, ok := fs.ResolveRead(real); ok || p != real {
		t.Errorf("无镜像应回退真实: %s %v", p, ok)
	}
	// 写镜像后：读镜像
	os.WriteFile(fs.ResolveWrite(real), []byte("mirror"), 0o644)
	p, ok := fs.ResolveRead(real)
	if !ok || p == real {
		t.Errorf("有镜像应读镜像: %s %v", p, ok)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "mirror" {
		t.Error("应读到镜像内容")
	}
}

func TestMerge_WritesBack(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	real := filepath.Join(t.TempDir(), "proj", "c.go")
	os.WriteFile(fs.ResolveWrite(real), []byte("merged"), 0o644)

	count, err := fs.Merge()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("应合并 1 个文件, got %d", count)
	}
	data, err := os.ReadFile(real)
	if err != nil {
		t.Fatal("合并后真实文件应存在")
	}
	if string(data) != "merged" {
		t.Errorf("合并内容异常: %s", data)
	}
}

func TestMerge_Subdirs(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	real := filepath.Join(t.TempDir(), "proj", "deep", "nested", "d.go")
	os.WriteFile(fs.ResolveWrite(real), []byte("x"), 0o644)
	if _, err := fs.Merge(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Error("嵌套目录合并失败")
	}
}

func TestContext_WithAndFrom(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	ctx := WithSandbox(context.Background(), fs)
	if got := FromContext(ctx); got != fs {
		t.Error("FromContext 应返回同一沙箱")
	}
	if FromContext(context.Background()) != nil {
		t.Error("无沙箱 ctx 应返回 nil")
	}
	if FromContext(nil) != nil {
		t.Error("nil ctx 应返回 nil")
	}
}

func TestHas(t *testing.T) {
	fs, _ := New(filepath.Join(t.TempDir(), "sb"))
	real := filepath.Join(t.TempDir(), "proj", "e.go")
	if fs.Has(real) {
		t.Error("初始不应存在")
	}
	os.WriteFile(fs.ResolveWrite(real), []byte("x"), 0o644)
	if !fs.Has(real) {
		t.Error("写镜像后 Has 应为 true")
	}
}
