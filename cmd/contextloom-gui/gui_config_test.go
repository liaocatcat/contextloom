package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGUIConfigPathFromExe_ExeDir：config.yaml 与可执行文件同目录 → 命中 exe 同目录。
func TestGUIConfigPathFromExe_ExeDir(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("model:\n  backend: deepseek\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "contextloom-gui")
	got := guiConfigPathFromExe(exe)
	if got != cfg {
		t.Fatalf("期望 %s，得到 %s", cfg, got)
	}
}

// TestGUIConfigPathFromExe_AppBundleSibling：.app 打包场景，config.yaml 放在
// ContextLoom.app 同级目录 → 上溯 3 级后命中应用包同级。
func TestGUIConfigPathFromExe_AppBundleSibling(t *testing.T) {
	root := t.TempDir()
	appParent := root // ContextLoom.app 放在 root 下
	appDir := filepath.Join(appParent, "ContextLoom.app")
	macOSDir := filepath.Join(appDir, "Contents", "MacOS")
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(appParent, "config.yaml")
	if err := os.WriteFile(cfg, []byte("model:\n  backend: deepseek\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(macOSDir, "contextloom-gui")
	got := guiConfigPathFromExe(exe)
	if got != cfg {
		t.Fatalf("期望 %s，得到 %s", cfg, got)
	}
}

// TestGUIConfigPathFromExe_AppBundleInternal：config.yaml 放在 Contents/MacOS 内
// （exe 同目录）→ 命中 exe 同目录（优先级高于 .app 同级）。
func TestGUIConfigPathFromExe_AppBundleInternal(t *testing.T) {
	root := t.TempDir()
	macOSDir := filepath.Join(root, "ContextLoom.app", "Contents", "MacOS")
	if err := os.MkdirAll(macOSDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(macOSDir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("x: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(macOSDir, "contextloom-gui")
	got := guiConfigPathFromExe(exe)
	if got != cfg {
		t.Fatalf("期望 %s，得到 %s", cfg, got)
	}
}

// TestGUIConfigPathFromExe_NoConfig：各处均无 config.yaml → 返回空。
func TestGUIConfigPathFromExe_NoConfig(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "contextloom-gui")
	if got := guiConfigPathFromExe(exe); got != "" {
		t.Fatalf("期望空，得到 %s", got)
	}
}

// normGUIPath 归一化路径（EvalSymlinks 失败时原样返回），消除 macOS 上
// /var→/private/var、/tmp→/private/tmp 符号链接差异。
func normGUIPath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return real
}

// TestFindGUIConfigPathFromExe_SymlinkResolution：GUI 经符号链接启动（全局命令），
// 真实本体目录有 config.yaml → 命中本体目录而非链接目录/CWD。
// 历史缺陷场景：os.Executable() 返回链接路径，本体旁配置被无视。
func TestFindGUIConfigPathFromExe_SymlinkResolution(t *testing.T) {
	realDir := t.TempDir()
	linkDir := t.TempDir()
	cfg := filepath.Join(realDir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("model:\n  backend: deepseek\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	realExe := filepath.Join(realDir, "contextloom-gui")
	if err := os.WriteFile(realExe, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	linkExe := filepath.Join(linkDir, "contextloom-gui")
	if err := os.Symlink(realExe, linkExe); err != nil {
		t.Skipf("当前平台不支持符号链接: %v", err)
	}
	got := findGUIConfigPathFromExe(linkExe)
	if normGUIPath(t, got) != normGUIPath(t, cfg) {
		t.Fatalf("符号链接启动应命中真实本体目录配置, want %q got %q", cfg, got)
	}
}

// TestFindGUIConfigPathFromExe_NoConfigAnywhere：exe 目录与 CWD 均无配置 →
// 返回 ""（调用方走内置默认配置）。
func TestFindGUIConfigPathFromExe_NoConfigAnywhere(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	emptyDir := t.TempDir()
	if err := os.Chdir(emptyDir); err != nil {
		t.Fatal(err)
	}
	got := findGUIConfigPathFromExe(filepath.Join(t.TempDir(), "no-such-dir", "contextloom-gui"))
	if got != "" {
		t.Fatalf("无配置时应返回空字符串, got %q", got)
	}
}
