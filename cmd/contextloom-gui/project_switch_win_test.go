package main

// Windows 端到端回归：切换项目 → 会话列表必须跟随新项目。
//
// 背景（历史缺陷复现根因）：GUI 切换项目 = 在目标目录重启 backend
// （startBackend 的 cmd.Dir = 项目目录），serve 模式的项目根必须跟随 CWD。
// 但若 exe 同目录 config.yaml 显式配置了【绝对】project.root，旧实现会
// 尊重该配置并把项目根锁死在配置目录 → 记忆/会话/存储全部落回旧项目，
// 表现为"切换项目后会话列表不切换"。修复：serve 模式强制 project.root=CWD
// （TUI 等交互模式仍尊重显式配置）。
//
// 本文件两个测试：
//  1. TestSwitch_ProjectSwitch_SessionList_Windows：完整 GUI 链路（backend
//     restart + WS 动态代理），A 建会话 → 切 B（空列表）→ 切回 A（恢复）。
//     smoke_test.go 的 !windows 版本在 Windows 跳过，此处补 Windows 覆盖。
//  2. TestServeMode_ProjectRootFollowsCWD：精确复现"绝对 root 锁死"场景——
//     exe 旁 config.yaml 配绝对 project.root（指向无关目录），serve 启动
//     于 projA，新建会话必须落在 projA/.contextloom/memory（CWD），绝不能落进
//     配置声明的绝对根目录。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var (
	wSmokeOnce sync.Once
	wSmokeBin  string
	wSmokeErr  error
)

// wBuildSmokeBin 构建 contextloom 到临时目录（整个测试会话只构建一次）。
func wBuildSmokeBin(t *testing.T) string {
	t.Helper()
	wSmokeOnce.Do(func() {
		repoRoot, err := filepath.Abs("../..") // go test 的 CWD 是包目录
		if err != nil {
			wSmokeErr = err
			return
		}
		dir, err := os.MkdirTemp("", "contextloom-switch-bin-*")
		if err != nil {
			wSmokeErr = err
			return
		}
		bin := filepath.Join(dir, "contextloom.exe")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/contextloom")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			wSmokeErr = fmt.Errorf("构建 contextloom 失败: %v\n%s", err, out)
			return
		}
		wSmokeBin = bin
	})
	if wSmokeErr != nil {
		t.Fatalf("前置构建失败: %v", wSmokeErr)
	}
	return wSmokeBin
}

func wSmokeEnv(t *testing.T) func() {
	t.Helper()
	old := os.Getenv("DEEPSEEK_API_KEY")
	if old == "" {
		_ = os.Setenv("DEEPSEEK_API_KEY", "sk-switch-test-not-real")
		return func() { _ = os.Setenv("DEEPSEEK_API_KEY", old) }
	}
	return func() {}
}

// wWinUserCfg 隔离用户配置目录（跨平台）：macOS/Linux 由 HOME 生效、
// Windows 由 APPDATA 生效——确保 projects.json 等用户配置绝不写入
// 真实用户目录（历史教训：旧实现只设 APPDATA/LOCALAPPDATA，在 macOS 上
// 无效，测试临时目录被写入真实 ~/Library/Application Support/contextloom-gui/
// projects.json，导致 GUI 移除项目时自动切换到已不存在的测试目录）。
// 与 smoke_test.go 的 smokeEnvIsolated 等价（此处为跨文件复用独立实现）。
func wWinUserCfg(t *testing.T) func() {
	t.Helper()
	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	oldA := os.Getenv("APPDATA")
	oldL := os.Getenv("LOCALAPPDATA")
	tmp := t.TempDir()
	_ = os.Setenv("HOME", tmp)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))
	_ = os.Setenv("APPDATA", tmp)
	_ = os.Setenv("LOCALAPPDATA", tmp)
	return func() {
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("XDG_CONFIG_HOME", oldXDG)
		_ = os.Setenv("APPDATA", oldA)
		_ = os.Setenv("LOCALAPPDATA", oldL)
	}
}

func wDialWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, _, err := d.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 连接失败 %s: %v", wsURL, err)
	}
	return conn
}

func wNewSession(t *testing.T, conn *websocket.Conn) string {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(map[string]any{
		"id": "ns", "type": "req", "method": "chat.newSession", "params": map[string]any{},
	}); err != nil {
		t.Fatalf("发送 newSession: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	var r struct {
		Type   string         `json:"type"`
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if err := conn.ReadJSON(&r); err != nil {
		t.Fatalf("读取 newSession 响应: %v", err)
	}
	if r.Type != "resp" || !r.OK {
		t.Fatalf("newSession 失败: %v", r.Error)
	}
	uuid, _ := r.Result["uuid"].(string)
	if uuid == "" {
		t.Fatal("newSession 未返回 uuid")
	}
	return uuid
}

func wListSessions(t *testing.T, conn *websocket.Conn) int {
	t.Helper()
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := conn.WriteJSON(map[string]any{
		"id": "ls", "type": "req", "method": "chat.listSessions", "params": map[string]any{},
	}); err != nil {
		t.Fatalf("发送 listSessions: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	var r struct {
		Type   string `json:"type"`
		OK     bool   `json:"ok"`
		Result []any  `json:"result"`
		Error  string `json:"error"`
	}
	if err := conn.ReadJSON(&r); err != nil {
		t.Fatalf("读取 listSessions 响应: %v", err)
	}
	if r.Type != "resp" || !r.OK {
		t.Fatalf("listSessions 失败: %v", r.Error)
	}
	return len(r.Result)
}

// TestSwitch_ProjectSwitch_SessionList_Windows 完整 GUI 链路：
// backendMgr 拉起 A → WS 建会话 → switchProject(B) → 重连 → 空列表 →
// 切回 A → 列表恢复。与 smoke_test.go 的 Unix 版本等价（补 Windows 覆盖）。
func TestSwitch_ProjectSwitch_SessionList_Windows(t *testing.T) {
	bin := wBuildSmokeBin(t)
	restore := wSmokeEnv(t)
	defer restore()
	defer wWinUserCfg(t)()

	projA := t.TempDir()
	projB := t.TempDir()
	_ = os.WriteFile(filepath.Join(projA, "marker-a.txt"), []byte("A"), 0o644)
	_ = os.WriteFile(filepath.Join(projB, "marker-b.txt"), []byte("B"), 0o644)

	b := newBackendMgr()
	t.Cleanup(func() { b.stop() })

	addrA, pidA, doneA, err := startBackend(bin, projA)
	if err != nil {
		t.Fatalf("startBackend(A): %v", err)
	}
	b.set(addrA, pidA, projA, true, doneA)

	proxyMux := http.NewServeMux()
	proxyMux.Handle("/api/ws", wsProxyTo(b))
	mux := http.NewServeMux()
	mux.Handle("/api/ws", proxyMux)
	mux.Handle("/api/projects", projectHandlers(b, bin))
	mux.Handle("/api/projects/", projectHandlers(b, bin))
	ts := httptest.NewServer(mux)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"

	conn := wDialWS(t, wsURL)
	defer conn.Close()
	uuidA := wNewSession(t, conn)
	if got := wListSessions(t, conn); got != 1 {
		t.Fatalf("A 项目 listSessions 期望 1 条，实际 %d", got)
	}
	t.Logf("A 创建会话 %s，列表=1", uuidA)

	// 模拟前端 switchProject：POST /api/projects/switch
	res, err := http.Post(ts.URL+"/api/projects/switch", "application/json",
		strings.NewReader(fmt.Sprintf(`{"path":%q}`, filepath.ToSlash(projB))))
	if err != nil {
		t.Fatalf("POST /api/projects/switch: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("switch 失败: %d", res.StatusCode)
	}

	// 前端 reload 等价：重连 WS（同一动态代理，自动指向新 backend）
	conn.Close()
	conn = wDialWS(t, wsURL)
	defer conn.Close()
	if got := wListSessions(t, conn); got != 0 {
		t.Fatalf("B 项目 listSessions 期望 0 条（跨项目数据隔离），实际 %d", got)
	}
	t.Logf("B 列表为空（跨项目隔离正确）")

	uuidB := wNewSession(t, conn)
	if got := wListSessions(t, conn); got != 1 {
		t.Fatalf("B 新建后期望 1 条，实际 %d", got)
	}
	t.Logf("B 创建会话 %s", uuidB)

	// 切回 A → 列表恢复
	res2, err := http.Post(ts.URL+"/api/projects/switch", "application/json",
		strings.NewReader(fmt.Sprintf(`{"path":%q}`, filepath.ToSlash(projA))))
	if err != nil || res2.StatusCode != 200 {
		t.Fatalf("switch back A 失败: %v %v", err, res2)
	}
	res2.Body.Close()
	conn.Close()
	conn = wDialWS(t, wsURL)
	defer conn.Close()
	if got := wListSessions(t, conn); got != 1 {
		t.Fatalf("切回 A 后期望 1 条，实际 %d", got)
	}
	t.Logf("切回 A 列表恢复=1（uuid %s/%s）", uuidA, uuidB)
}

// TestServeMode_ProjectRootFollowsCWD 精确复现"绝对 project.root 锁死"：
// exe 旁 config.yaml 配置绝对 project.root（指向无关目录 otherRoot），
// 以 CWD=projA 启动 --serve。修复后项目根必须跟随 CWD——新建会话文件
// 落在 projA/.contextloom/memory/sessions/，projA 列表=1；otherRoot 必须为空。
func TestServeMode_ProjectRootFollowsCWD(t *testing.T) {
	bin := wBuildSmokeBin(t)
	restore := wSmokeEnv(t)
	defer restore()
	defer wWinUserCfg(t)()

	projA := t.TempDir()     // backend 实际 CWD（GUI 切换项目后的目标目录）
	otherRoot := t.TempDir() // 配置声明的绝对 project.root（旧实现锁死于此）

	// exe 同目录 config.yaml：绝对 project.root = otherRoot（用户真实场景：
	// contextloom-agent/config.yaml 配了 D:\20260723\contextloom-agent）
	cfg := fmt.Sprintf("model:\n  backend: legacy-model-id-0731\n  endpoint: https://example.invalid/v1\n  api_key: %q\nproject:\n  root: %s\nmemory:\n  directory: .contextloom/memory\n  auto_title: true\n  max_sessions: 1000\n",
		"sk-switch-test-not-real", filepath.ToSlash(otherRoot))
	cfgPath := filepath.Join(filepath.Dir(bin), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o644); err != nil {
		t.Fatalf("写 config.yaml: %v", err)
	}

	b := newBackendMgr()
	t.Cleanup(func() { b.stop() })
	addr, pid, done, err := startBackend(bin, projA) // CWD = projA
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	b.set(addr, pid, projA, true, done)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy := wsProxyTo(b)
		proxy.ServeHTTP(w, r)
	}))
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"

	conn := wDialWS(t, wsURL)
	defer conn.Close()
	uuid := wNewSession(t, conn)
	if got := wListSessions(t, conn); got != 1 {
		t.Fatalf("listSessions 期望 1 条，实际 %d", got)
	}
	t.Logf("新建会话 %s", uuid)

	// 断言 1：会话文件落在 CWD（projA）——项目根跟随 CWD
	sessDirA := filepath.Join(projA, ".contextloom", "memory", "sessions")
	entriesA, err := os.ReadDir(sessDirA)
	if err != nil {
		t.Fatalf("projA 无会话目录 %s（项目根未跟随 CWD，绝对 root 仍锁死）: %v", sessDirA, err)
	}
	if len(entriesA) == 0 {
		t.Fatal("projA/.contextloom/memory/sessions 为空：serve 模式项目根没有跟随 CWD")
	}
	found := false
	for _, e := range entriesA {
		if strings.Contains(e.Name(), strings.Split(uuid, "-")[0]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("会话 %s 未出现在 projA 会话目录（落到了别处）", uuid)
	}
	t.Logf("✅ 会话落在 CWD 项目根 projA/.contextloom/memory/sessions（跟随切换）")

	// 断言 2：配置声明的绝对 root（otherRoot）必须保持空——旧实现会写到这里
	sessDirOther := filepath.Join(otherRoot, ".contextloom", "memory", "sessions")
	if st, err := os.Stat(sessDirOther); err == nil && st.IsDir() {
		if es, _ := os.ReadDir(sessDirOther); len(es) > 0 {
			t.Fatalf("会话被写入配置声明的绝对 root %s（旧缺陷：切换项目不生效）", sessDirOther)
		}
	}
	t.Logf("✅ 绝对 root 目录 %s 无会话（不再锁死项目根）", otherRoot)
}

// TestProjects_RemoveCurrent_SkipsMissingDir 回归：移除当前项目时自动切换
// 必须跳过目录已不存在的残留项（脏数据），切到第一个可达项目。
// 历史 bug：直接切 rest[0]（不存在）→ startBackend chdir 失败 → 500 →
// 前端显示"移除失败"（实际移除已成功，被切换失败掩盖）。
func TestProjects_RemoveCurrent_SkipsMissingDir(t *testing.T) {
	restore := wWinUserCfg(t)
	defer restore()

	projA := t.TempDir()
	projC := t.TempDir()
	missing := filepath.Join(t.TempDir(), "gone") // 从未创建，必不存在

	b := newBackendMgr()
	b.projectsPath = filepath.Join(t.TempDir(), "projects.json") // 测试隔离
	b.projects = []string{projA, missing, projC}
	b.set("http://127.0.0.1:1/?token=t", 12345, projA, true, nil)
	var switchedTo []string
	b.startBackendFn = func(bin, dir string) (string, int, <-chan struct{}, error) {
		switchedTo = append(switchedTo, dir)
		return "http://127.0.0.1:2/?token=t", 12346, nil, nil
	}

	ts := httptest.NewServer(projectHandlers(b, "fake-bin"))
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/projects/remove", "application/json",
		strings.NewReader(fmt.Sprintf(`{"path":%q}`, projA)))
	if err != nil {
		t.Fatalf("POST remove: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("移除应成功（200），实际 %d", res.StatusCode)
	}
	if len(switchedTo) != 1 || switchedTo[0] != projC {
		t.Fatalf("自动切换应跳过 missing 落到 projC，实际 %v", switchedTo)
	}
	if got := b.currentDir(); got != projC {
		t.Fatalf("currentDir 应为 projC，实际 %s", got)
	}
	projs := b.listProjects()
	for _, p := range projs {
		if p == projA {
			t.Fatal("projA 应从列表移除")
		}
	}
	foundMissing := false
	for _, p := range projs {
		if p == missing {
			foundMissing = true
		}
	}
	if !foundMissing {
		t.Fatal("missing 应保留在列表（移除操作不清理其他项）")
	}
}

// TestProjects_RemoveCurrent_AllMissingNo500 回归：移除当前项目后剩余项目
// 全部不可达 → 必须返回 200（移除已成功），绝不能 500；backend 状态清空
// （GUI 回到未连接，等待用户打开/添加新项目）。
func TestProjects_RemoveCurrent_AllMissingNo500(t *testing.T) {
	restore := wWinUserCfg(t)
	defer restore()

	projA := t.TempDir()
	m1 := filepath.Join(t.TempDir(), "gone1")
	m2 := filepath.Join(t.TempDir(), "gone2")

	b := newBackendMgr()
	b.projectsPath = filepath.Join(t.TempDir(), "projects.json")
	b.projects = []string{projA, m1, m2}
	b.set("http://127.0.0.1:1/?token=t", 12345, projA, true, nil)
	b.startBackendFn = func(bin, dir string) (string, int, <-chan struct{}, error) {
		t.Fatalf("不应尝试启动 backend（剩余项目全部不可达），目标 %s", dir)
		return "", 0, nil, nil
	}

	ts := httptest.NewServer(projectHandlers(b, "fake-bin"))
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/projects/remove", "application/json",
		strings.NewReader(fmt.Sprintf(`{"path":%q}`, projA)))
	if err != nil {
		t.Fatalf("POST remove: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("移除应成功（200，即使剩余项目全不可达），实际 %d", res.StatusCode)
	}
	if got := b.currentDir(); got != "" {
		t.Fatalf("currentDir 应清空（backend 已停止），实际 %s", got)
	}
}
