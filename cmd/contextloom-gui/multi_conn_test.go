package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeBackendMgr 构造 backendMgr（注入 fake startBackendFn，不真实拉起进程）。
func fakeBackendMgr(t *testing.T, dirs ...string) *backendMgr {
	t.Helper()
	mgr := newBackendMgr()
	mgr.projectsPath = filepath.Join(t.TempDir(), "projects.json")
	addrSeq := int64(19000)
	mgr.startBackendFn = func(bin, dir string) (string, int, <-chan struct{}, error) {
		n := atomic.AddInt64(&addrSeq, 1)
		done := make(chan struct{})
		close(done)
		return "http://127.0.0.1:" + itoa(int(n)) + "/?token=tok" + itoa(int(n)), int(n), done, nil
	}
	if len(dirs) > 0 {
		mgr.initProjects(dirs[0])
		for _, d := range dirs[1:] {
			mgr.addProject(d)
		}
	}
	return mgr
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// TestBackendAddrs_MultiProject 验证多项目寻址：
// openDir 拉起两项目 backend 后，backendAddrFor 分别命中、backendAddrs
// 返回完整映射（多项目并行通道的寻址基础，前端据此建独立 WS 连接）。
func TestBackendAddrs_MultiProject(t *testing.T) {
	mgr := fakeBackendMgr(t)
	// 拉起 A、B 两个项目的 backend（常驻池登记）
	resA, err := mgr.openDir("bin", "/proj/a")
	if err != nil {
		t.Fatalf("openDir(A): %v", err)
	}
	addrA, _ := resA["addr"].(string)
	if addrA == "" {
		t.Fatalf("openDir(A) 未返回 addr: %v", resA)
	}
	if _, err := mgr.openDir("bin", "/proj/b"); err != nil {
		t.Fatalf("openDir(B): %v", err)
	}
	// backendAddrFor 分别命中
	if got := mgr.backendAddrFor("/proj/a"); got != addrA {
		t.Fatalf("backendAddrFor(/proj/a) = %q, want %q", got, addrA)
	}
	if mgr.backendAddrFor("/proj/nonexistent") != "" {
		t.Fatalf("backendAddrFor(未拉起目录) 应为空串")
	}
	// backendAddrs 返回两项目完整映射
	addrs := mgr.backendAddrs()
	if len(addrs) != 2 {
		t.Fatalf("backendAddrs 应含 2 项, got %d: %v", len(addrs), addrs)
	}
	if addrs["/proj/a"] != addrA {
		t.Fatalf("backendAddrs[/proj/a] = %q, want %q", addrs["/proj/a"], addrA)
	}
}

// TestWsProxyTo_DirRouting 验证 wsProxyTo 按 ?dir= 参数路由到指定项目 backend：
// 请求带 dir 参数 → 路由到该目录 backend 的 /ws（注入其 token）；
// 无 dir 参数 → 回退当前 backend（向后兼容单连接行为）。
func TestWsProxyTo_DirRouting(t *testing.T) {
	mgr := fakeBackendMgr(t)
	// 拉起 A、B，当前指向 B（openDir 顺序：先 A 后 B，B 为当前）
	if _, err := mgr.openDir("bin", "/proj/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.openDir("bin", "/proj/b"); err != nil {
		t.Fatal(err)
	}
	proxy := wsProxyTo(mgr)
	srv := httptest.NewServer(proxy)
	defer srv.Close()

	getURL := func(query string) string {
		// 模拟浏览器 WS 升级请求（登录到 proxy director 判定即可，
		// 不真握手——反向代理把请求转发给 fake backend 地址会失败，
		// 但 Director 已在转发前改写 URL；此处直接断言改写结果）。
		_ = query
		return ""
	}
	_ = getURL

	// 直接验证 Director 改写：构造请求带 dir → 目标应为 /proj/b 的 backend
	req := httptest.NewRequest("GET", "/api/ws?dir=%2Fproj%2Fb", nil)
	director := proxy.Director
	if director == nil {
		t.Fatal("wsProxyTo 未设置 Director")
	}
	director(req)
	// Director 后 URL 应指向 /proj/b backend 的 /ws，且带其 token
	if req.URL.Host == "" || req.URL.Host == "example.com" {
		t.Fatalf("dir 路由后 Host 未被改写为 backend: %q", req.URL.Host)
	}
	if req.URL.Path != "/ws" {
		t.Fatalf("dir 路由后 Path 应为 /ws, got %q", req.URL.Path)
	}
	tok := req.URL.Query().Get("token")
	if tok == "" {
		t.Fatalf("dir 路由后应注入 token")
	}
	// 确认路由到的是 B 的 backend（当前 addr）
	if !strings.Contains(mgr.backendAddrFor("/proj/b"), req.URL.Host) {
		t.Fatalf("dir=proj/b 应路由到 B backend (%s), got host %s", mgr.backendAddrFor("/proj/b"), req.URL.Host)
	}

	// 无 dir 参数 → 回退当前 backend（B）
	req2 := httptest.NewRequest("GET", "/api/ws", nil)
	director(req2)
	cur := mgr.baseURL()
	parsedCur := strings.TrimPrefix(cur, "http://")
	if !strings.Contains(parsedCur, req2.URL.Host) {
		t.Fatalf("无 dir 参数应回退当前 backend (%s), got host %s", cur, req2.URL.Host)
	}

	// dir 参数指向未拉起目录 → 回退当前 backend（不报错）
	req3 := httptest.NewRequest("GET", "/api/ws?dir=%2Fproj%2Fmissing", nil)
	director(req3)
	if !strings.Contains(parsedCur, req3.URL.Host) {
		t.Fatalf("dir 未命中应回退当前 backend, got host %s", req3.URL.Host)
	}
}

// TestProjectsEndpoint_IncludesBackends 验证 /api/projects 附带 backends 映射
// （前端初始化预建多连接的数据源）。
func TestProjectsEndpoint_IncludesBackends(t *testing.T) {
	mgr := fakeBackendMgr(t, "/proj/a", "/proj/b")
	if _, err := mgr.openDir("bin", "/proj/a"); err != nil {
		t.Fatal(err)
	}
	mux := projectHandlers(mgr, "bin")
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/projects")
	if err != nil {
		t.Fatalf("GET /api/projects: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("状态码 %d", resp.StatusCode)
	}
	body := make([]byte, resp.ContentLength)
	_, _ = resp.Body.Read(body)
	s := string(body)
	if !strings.Contains(s, "backends") {
		t.Fatalf("/api/projects 响应应含 backends 字段: %s", s)
	}
	if !strings.Contains(s, "/proj/a") || !strings.Contains(s, "/proj/b") {
		t.Fatalf("/api/projects 响应应含两项目映射: %s", s)
	}
}
