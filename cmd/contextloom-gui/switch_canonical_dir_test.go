package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectSwitch_ResponseDirCanonical 回归：/api/projects/switch 与
// /api/projects/open 的响应 dir 必须是 filepath.Abs 规范化后的绝对路径。
//
// 前端以该 dir 作为 tab 快照/localStorage 的键（后端 /api/projects 的
// current 同源），若响应回显请求原始串（带尾部斜杠/重复斜杠/相对路径），
// 前端"存"与"读"的键不一致 → 切回项目时右侧会话 tab 恢复失败，表现为
// 用户实测的"切换回来也没用了"。
func TestProjectSwitch_ResponseDirCanonical(t *testing.T) {
	base := t.TempDir()
	projA := filepath.Join(base, "a")
	projB := filepath.Join(base, "b")
	for _, d := range []string{projA, projB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mgr := newBackendMgr()
	mgr.projectsPath = filepath.Join(t.TempDir(), "projects.json")
	mgr.initProjects(projA)
	mgr.startBackendFn = func(bin, dir string) (string, int, <-chan struct{}, error) {
		return "http://127.0.0.1:1/?token=t", 4242, nil, nil
	}
	srv := httptest.NewServer(projectHandlers(mgr, "/fake/bin"))
	defer srv.Close()

	// 带尾斜杠 + 重复斜杠的请求路径：响应必须回规范化后的绝对路径
	requested := projB + string(filepath.Separator) + string(filepath.Separator) +
		"nested" + string(filepath.Separator) + ".." + string(filepath.Separator)
	if err := os.MkdirAll(filepath.Join(projB, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"/api/projects/switch", "/api/projects/open"} {
		resp, err := http.Post(srv.URL+endpoint, "application/json",
			strings.NewReader(mustPathJSON(t, requested)))
		if err != nil {
			t.Fatalf("POST %s: %v", endpoint, err)
		}
		var out map[string]any
		decodeErr := json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("%s 应 200, got %d", endpoint, resp.StatusCode)
		}
		if decodeErr != nil {
			t.Fatalf("%s 响应解码失败: %v", endpoint, decodeErr)
		}
		got, _ := out["dir"].(string)
		if got != projB {
			t.Fatalf("%s 响应 dir 应为规范化的 %q, got %q（前端以此为 tab 持久化键，不一致会导致切回丢会话）",
				endpoint, projB, got)
		}
		if got != mgr.currentDir() {
			t.Fatalf("%s 响应 dir(%q) 与 currentDir(%q) 不一致", endpoint, got, mgr.currentDir())
		}
	}
}
