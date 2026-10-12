package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 目录浏览器端点：项目管理器"以目录选中的形式引入项目"的文件系统浏览。
// mustPathJSON builds a {"path":...} JSON body.
// On Windows temp dirs contain backslashes which break manually-concatenated JSON
// (invalid \U escapes) causing backend 400 - always marshal via encoding/json.
func mustPathJSON(t *testing.T, dir string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{"path": dir})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestFSBrowse_RootAndDirs(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// 文件不应出现在目录列表
	f := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := newBackendMgr()
	srv := httptest.NewServer(projectHandlers(mgr, "/fake/bin"))
	defer srv.Close()

	get := func(u string) (map[string]any, int) {
		resp, err := http.Get(srv.URL + u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&m)
		return m, resp.StatusCode
	}

	// 具体目录：只列子目录，不含文件
	m, code := get("/api/fs/browse?path=" + url.QueryEscape(dir))
	if code != 200 {
		t.Fatalf("browse 目录应 200, got %d", code)
	}
	if m["path"] != dir {
		t.Fatalf("path 应为 %s, got %v", dir, m["path"])
	}
	if m["parent"] == "" {
		t.Fatal("parent 不应为空")
	}
	dirs, _ := m["dirs"].([]any)
	found := false
	for _, d := range dirs {
		dm, _ := d.(map[string]any)
		if dm["name"] == "subdir" {
			found = true
		}
		if dm["name"] == "a.txt" {
			t.Fatal("文件不应出现在目录列表")
		}
	}
	if !found {
		t.Fatal("子目录 subdir 应出现在列表中")
	}

	// 根（Unix）：path=/ 且含系统目录（跨平台宽松断言）
	if runtime.GOOS != "windows" {
		m, code = get("/api/fs/browse?path=")
		if code != 200 || m["path"] != "/" {
			t.Fatalf("根浏览应返回 /, got %v code=%d", m["path"], code)
		}
	}

	// 无效路径 → 400
	_, code = get("/api/fs/browse?path=" + url.QueryEscape(filepath.Join(dir, "not-exist")))
	if code != 400 {
		t.Fatalf("无效路径应 400, got %d", code)
	}
}

// 打开项目 = 添加 + 激活：出现在列表首位、成为当前目录、backend 在目标目录重启。
func TestProjectHandlers_OpenAddsAndActivates(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mgr := newBackendMgr()
	mgr.projectsPath = filepath.Join(t.TempDir(), "projects.json")
	mgr.initProjects(dirA)
	var opened []string
	mgr.startBackendFn = func(bin, dir string) (string, int, <-chan struct{}, error) {
		opened = append(opened, dir)
		return "http://127.0.0.1:1/?token=t", 4242, nil, nil
	}
	srv := httptest.NewServer(projectHandlers(mgr, "/fake/bin"))
	defer srv.Close()

	post := func(u, body string) int {
		resp, err := http.Post(srv.URL+u, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	// 打开项目 B：应添加 + 激活（backend 切换到 B）
	if code := post("/api/projects/open", mustPathJSON(t, dirB)); code != 200 {
		t.Fatalf("open 应 200, got %d", code)
	}
	if len(opened) != 1 || opened[0] != dirB {
		t.Fatalf("backend 应切到 B, got %v", opened)
	}
	list := mgr.listProjects()
	if len(list) != 2 || list[0] != dirB {
		t.Fatalf("打开后 B 应在列表首位, got %v", list)
	}
	if snap := mgr.snapshot(); snap["dir"] != dirB {
		t.Fatalf("当前目录应为 B, got %v", snap["dir"])
	}

	// 重复打开已存在目录：不重复添加，仅激活（前移首位）
	if code := post("/api/projects/open", mustPathJSON(t, dirA)); code != 200 {
		t.Fatalf("再次 open 应 200, got %d", code)
	}
	if got := mgr.listProjects(); len(got) != 2 || got[0] != dirA {
		t.Fatalf("重复打开应去重且 A 前移首位, got %v", got)
	}

	// 不存在的目录 → 400（不加入列表）
	if code := post("/api/projects/open", `{"path":"/no/such/dir"}`); code != 400 {
		t.Fatalf("无效目录应 400, got %d", code)
	}
	if got := mgr.listProjects(); len(got) != 2 {
		t.Fatalf("无效目录不应加入列表, got %v", got)
	}
}
