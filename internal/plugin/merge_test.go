package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFromDir_MergeUserAndProject 验证用户级 + 项目级合并扫描语义：
// 项目级同名插件覆盖用户级（后加载覆盖）。
func TestLoadFromDir_MergeUserAndProject(t *testing.T) {
	base := t.TempDir()
	userDir := filepath.Join(base, "user")
	projDir := filepath.Join(base, "project")

	// 用户级：个人插件 common（v1）+ personal（仅用户级）
	writePlugin(t, userDir, "common", "1.0.0")
	writePlugin(t, userDir, "personal", "1.0.0")
	// 项目级：团队插件 common（v2，应覆盖用户级 v1）+ team（仅项目级）
	writePlugin(t, projDir, "common", "2.0.0")
	writePlugin(t, projDir, "team", "1.0.0")

	m := New(userDir)
	if n, err := m.LoadFromDir(userDir); err != nil || n != 2 {
		t.Fatalf("用户级加载: n=%d err=%v, want 2", n, err)
	}
	if n, err := m.LoadFromDir(projDir); err != nil || n != 2 {
		t.Fatalf("项目级加载: n=%d err=%v, want 2", n, err)
	}

	p, ok := m.Get("common")
	if !ok {
		t.Fatal("common 插件应存在")
	}
	if p.Manifest.Version != "2.0.0" {
		t.Errorf("项目级同名插件应覆盖用户级, got version %q want 2.0.0", p.Manifest.Version)
	}
	if p.Dir != filepath.Join(projDir, "common") {
		t.Errorf("Dir 应指向项目级目录, got %q", p.Dir)
	}
	if _, ok := m.Get("personal"); !ok {
		t.Error("用户级 personal 插件应保留")
	}
	if _, ok := m.Get("team"); !ok {
		t.Error("项目级 team 插件应加载")
	}
}

// TestLoadFromDir_NoDirs 目录不存在时静默跳过（不应报错）
func TestLoadFromDir_NoDirs(t *testing.T) {
	base := t.TempDir()
	m := New(filepath.Join(base, "nonexistent"))
	if n, err := m.LoadFromDir(filepath.Join(base, "nonexistent")); err != nil || n != 0 {
		t.Fatalf("目录不存在应静默跳过: n=%d err=%v", n, err)
	}
}

func writePlugin(t *testing.T, dir, name, version string) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	manifest := "name: " + name + "\nversion: " + version + "\n"
	if err := os.WriteFile(filepath.Join(pluginDir, "manifest.yaml"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
}
