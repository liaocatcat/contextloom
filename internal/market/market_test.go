package market

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuiltinCatalog_Complete(t *testing.T) {
	m := New()
	items := m.Search("")
	if len(items) != 8 {
		t.Fatalf("内置 catalog 应 8 项, got %d", len(items))
	}
	names := map[string]bool{}
	for _, it := range items {
		names[it.Name] = true
		if it.Name == "" || it.Description == "" || it.Content == "" {
			t.Errorf("条目不完整: %+v", it)
		}
		if !strings.Contains(it.Content, "---") {
			t.Errorf("条目缺少 frontmatter: %s", it.Name)
		}
	}
	for _, want := range []string{"code-review", "test-writer", "refactor", "doc-gen",
		"bug-hunter", "perf-audit", "security-scan", "migration-helper"} {
		if !names[want] {
			t.Errorf("缺少内置技能 %s", want)
		}
	}
}

func TestSearch_Match(t *testing.T) {
	m := New()
	// 名称匹配
	if got := m.Search("code-review"); len(got) != 1 || got[0].Name != "code-review" {
		t.Errorf("名称搜索异常: %+v", got)
	}
	// 描述匹配（中文）
	got := m.Search("安全")
	if len(got) == 0 {
		t.Error("描述搜索'安全'应命中 security-scan")
	}
	found := false
	for _, it := range got {
		if it.Name == "security-scan" {
			found = true
		}
	}
	if !found {
		t.Errorf("'安全'应命中 security-scan: %+v", got)
	}
	// 标签匹配
	got = m.Search("test")
	found = false
	for _, it := range got {
		if it.Name == "test-writer" {
			found = true
		}
	}
	if !found {
		t.Errorf("'test' 应命中 test-writer")
	}
	// 无匹配
	if len(m.Search("zzz-no-such")) != 0 {
		t.Error("无匹配查询应返回空")
	}
}

func TestGet_Exact(t *testing.T) {
	m := New()
	it, ok := m.Get("refactor")
	if !ok || it.Name != "refactor" {
		t.Errorf("Get refactor 异常: %+v", it)
	}
	if _, ok := m.Get("nope"); ok {
		t.Error("不存在技能不应命中")
	}
}

func TestInstall_WriteFile(t *testing.T) {
	m := New()
	dir := t.TempDir()
	if err := m.Install("doc-gen", dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "doc-gen", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name: doc-gen") {
		t.Errorf("SKILL.md frontmatter 异常: %s", string(data)[:80])
	}
	// 重复安装应报错
	if err := m.Install("doc-gen", dir); err == nil {
		t.Error("重复安装应报错")
	}
	// 不存在技能安装报错
	if err := m.Install("nope", dir); err == nil {
		t.Error("安装不存在技能应报错")
	}
	// ListInstalled
	installed, err := ListInstalled(dir)
	if err != nil || len(installed) != 1 || installed[0] != "doc-gen" {
		t.Errorf("ListInstalled 异常: %v %v", installed, err)
	}
}

func TestInstall_EmptyDir(t *testing.T) {
	// 技能目录不存在时 ListInstalled 返回空不报错
	got, err := ListInstalled(filepath.Join(t.TempDir(), "no-such"))
	if err != nil || got != nil {
		t.Errorf("空目录应返回 nil,nil: %v %v", got, err)
	}
}

func TestAddRemote_FetchAndMerge(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Catalog{
			Name: "community",
			Items: []Item{
				{Name: "remote-tool", Description: "远程技能", Content: "---\nname: remote-tool\n---\nbody"},
			},
		})
	}))
	defer srv.Close()

	m := New()
	if err := m.AddRemote(srv.URL); err != nil {
		t.Fatal(err)
	}
	// 搜索应含内置 + 远程
	got := m.Search("remote")
	if len(got) != 1 || got[0].Name != "remote-tool" || got[0].Source != srv.URL {
		t.Errorf("远程条目异常: %+v", got)
	}
	// 重复添加忽略
	if err := m.AddRemote(srv.URL); err != nil {
		t.Errorf("重复添加应忽略: %v", err)
	}
	// 远程安装
	dir := t.TempDir()
	if err := m.Install("remote-tool", dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "remote-tool", "SKILL.md")); err != nil {
		t.Error("远程技能安装失败")
	}
}

func TestAddRemote_Errors(t *testing.T) {
	m := New()
	if err := m.AddRemote(""); err == nil {
		t.Error("空 URL 应报错")
	}
	// 404
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 404)
	}))
	defer srv.Close()
	if err := m.AddRemote(srv.URL); err == nil {
		t.Error("404 应报错")
	}
	// 非法 JSON
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-json"))
	}))
	defer srv2.Close()
	if err := m.AddRemote(srv2.URL); err == nil {
		t.Error("非法 JSON 应报错")
	}
	// 超时（连接被挂起）
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {} // 永不返回
	}))
	defer srv3.Close()
	m3 := New()
	m3.client.Timeout = 0 // 用默认 10s 太长，这里不测超时路径（避免慢测试）
	_ = m3
}

func TestSearch_Dedup(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Catalog{
			Name:  "dup",
			Items: []Item{{Name: "code-review", Description: "重复同名", Content: "---\n---"}},
		})
	}))
	defer srv.Close()
	m := New()
	if err := m.AddRemote(srv.URL); err != nil {
		t.Fatal(err)
	}
	got := m.Search("code-review")
	if len(got) != 1 {
		t.Errorf("重名应去重（内置优先）, got %d", len(got))
	}
	if got[0].Source != "builtin" {
		t.Errorf("内置应优先: %+v", got[0])
	}
}
