package gui

// v3 合并面板后端契约测试：fs.* 方法 root 参数（跨项目目录浏览）与写保护。
// 契约见 docs/GUI-CONTRACT.md 附录「v3 项目管理+目录文件合并面板 设计」A 节。

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSafePath_WithRoot 验证 safePath 的 root 基座语义。
func TestSafePath_WithRoot(t *testing.T) {
	projRoot := t.TempDir()
	extRoot := t.TempDir()
	srv := &Server{rt: &fakeRuntime{projectRoot: projRoot}}

	// root 合法：以 root 为基座解析相对路径
	abs, err := srv.safePath("src", extRoot)
	if err != nil {
		t.Fatalf("root 基座解析失败: %v", err)
	}
	want := filepath.Join(extRoot, "src")
	if !strings.EqualFold(abs, want) {
		t.Fatalf("root 基座解析错误: got %q want %q", abs, want)
	}

	// root 下越界：../ 逃逸应拒绝
	if _, err := srv.safePath("../x", extRoot); err == nil {
		t.Fatal("root 基座内 ../ 越界应拒绝")
	}

	// root 非绝对路径应报错
	if _, err := srv.safePath("x", "relative/path"); err == nil {
		t.Fatal("root 非绝对路径应报错")
	}

	// 空 root 兼容：基座 = 当前项目根
	abs2, err := srv.safePath("sub", "")
	if err != nil {
		t.Fatalf("空 root 应兼容旧行为: %v", err)
	}
	if !strings.EqualFold(abs2, filepath.Join(projRoot, "sub")) {
		t.Fatalf("空 root 应解析到当前项目根: %q", abs2)
	}
}

// TestCheckWritableRoot 验证写保护：root=="" 或 ==当前项目根放行，其余拒绝。
func TestCheckWritableRoot(t *testing.T) {
	projRoot := t.TempDir()
	extRoot := t.TempDir()
	srv := &Server{rt: &fakeRuntime{projectRoot: projRoot}}

	if err := srv.checkWritableRoot(""); err != nil {
		t.Fatalf("空 root 应放行: %v", err)
	}
	if err := srv.checkWritableRoot(projRoot); err != nil {
		t.Fatalf("root=当前项目根应放行: %v", err)
	}
	if err := srv.checkWritableRoot(extRoot); err == nil {
		t.Fatal("root=非当前项目应拒绝写")
	}
}

// TestFS_ListWithRoot 验证 fs.list 带 root 可浏览任意绝对目录（合并面板跨项目浏览）。
func TestFS_ListWithRoot(t *testing.T) {
	projRoot := t.TempDir()
	extRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(extRoot, "ext.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(extRoot, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{projectRoot: projRoot}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "fs.list", map[string]any{"path": "", "root": extRoot})
	if !resp.OK {
		t.Fatalf("fs.list root 失败: %v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["root"] != extRoot {
		t.Fatalf("响应应回显 root: %v", m["root"])
	}
	entries := m["entries"].([]any)
	names := map[string]bool{}
	for _, e := range entries {
		em := e.(map[string]any)
		names[em["name"].(string)] = true
		// path 必须相对 root（与无 root 调用语义一致）
		if strings.HasPrefix(em["path"].(string), "/") && filepath.IsAbs(em["path"].(string)) {
			t.Fatalf("entries.path 应为相对路径: %q", em["path"])
		}
	}
	if !names["ext.txt"] || !names["sub"] {
		t.Fatalf("应列出外部目录内容: %v", names)
	}

	// 无 root 调用：仍返回当前项目根（兼容）
	resp2 := sendReq(t, conn, "2", "fs.list", map[string]any{"path": ""})
	if !resp2.OK {
		t.Fatalf("fs.list 无 root 应兼容: %v", resp2.Error)
	}
	if resp2.Result.(map[string]any)["root"] != "" {
		t.Fatalf("无 root 响应 root 应为空串")
	}
}

// TestFS_ReadWithRoot 验证 fs.read 带 root 可读任意项目文件（只读查看）。
func TestFS_ReadWithRoot(t *testing.T) {
	projRoot := t.TempDir()
	extRoot := t.TempDir()
	content := "外部文件内容"
	if err := os.WriteFile(filepath.Join(extRoot, "readme.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &fakeRuntime{projectRoot: projRoot}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "fs.read", map[string]any{"path": "readme.md", "root": extRoot})
	if !resp.OK {
		t.Fatalf("fs.read root 失败: %v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["content"] != content {
		t.Fatalf("读取内容不符: %q", m["content"])
	}
}

// TestFS_WriteProtection 验证 4 个写方法在非当前项目 root 下全部拒绝。
func TestFS_WriteProtection(t *testing.T) {
	projRoot := t.TempDir()
	extRoot := t.TempDir()
	rt := &fakeRuntime{projectRoot: projRoot}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	cases := []struct {
		id     string
		method string
		params map[string]any
	}{
		{"1", "fs.write", map[string]any{"path": "a.txt", "content": "x", "root": extRoot}},
		{"2", "fs.mkdir", map[string]any{"path": "newdir", "root": extRoot}},
		{"3", "fs.rename", map[string]any{"oldPath": "a.txt", "newPath": "b.txt", "root": extRoot}},
		{"4", "fs.delete", map[string]any{"path": "a.txt", "root": extRoot}},
	}
	for _, c := range cases {
		resp := sendReq(t, conn, c.id, c.method, c.params)
		if resp.OK {
			t.Fatalf("%s 带非当前项目 root 应被拒绝", c.method)
		}
		if !strings.Contains(resp.Error.Msg, "只读项目") {
			t.Fatalf("%s 拒绝文案不符: %+v", c.method, resp.Error)
		}
	}

	// 确认外部目录未被写入
	if _, err := os.Stat(filepath.Join(extRoot, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("写保护后外部目录不应产生文件")
	}
	if _, err := os.Stat(filepath.Join(extRoot, "newdir")); !os.IsNotExist(err) {
		t.Fatal("写保护后外部目录不应产生目录")
	}

	// root == 当前项目根时写放行（规范化后相等）
	okResp := sendReq(t, conn, "5", "fs.write", map[string]any{"path": "ok.txt", "content": "y", "root": projRoot})
	if !okResp.OK {
		t.Fatalf("root=当前项目根应放行写: %v", okResp.Error)
	}
	if _, err := os.Stat(filepath.Join(projRoot, "ok.txt")); err != nil {
		t.Fatalf("当前项目根写入应成功: %v", err)
	}
}
