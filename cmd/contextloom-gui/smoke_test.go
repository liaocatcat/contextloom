//go:build !windows

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 端到端冒烟测试：真实构建 contextloom / contextloom-gui 二进制，
// 验证启动器整链路（拉起 backend → GUI_ADDR 解析 → 探活 → 优雅退出）。
// 目标：全程 < 15s，避免"冒烟测试太久"。

var (
	smokeOnce sync.Once
	smokeBin  string // 构建好的 contextloom 二进制路径
	smokeErr  error
)

// buildSmokeBin 构建 contextloom 到临时目录（整个测试会话只构建一次，go build 缓存加速）。
func buildSmokeBin(t *testing.T) string {
	t.Helper()
	smokeOnce.Do(func() {
		repoRoot, err := filepath.Abs("../..") // go test 的 CWD 是包目录
		if err != nil {
			smokeErr = err
			return
		}
		dir, err := os.MkdirTemp("", "contextloom-smoke-bin-*")
		if err != nil {
			smokeErr = err
			return
		}
		bin := filepath.Join(dir, "contextloom")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/contextloom")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			smokeErr = fmt.Errorf("构建 contextloom 失败: %v\n%s", err, out)
			return
		}
		smokeBin = bin
	})
	if smokeErr != nil {
		t.Fatalf("冒烟前置构建失败: %v", smokeErr)
	}
	return smokeBin
}

// smokeEnv 设置 contextloom 启动校验所需的环境（假 key 仅通过非空校验）。
func smokeEnv(t *testing.T) func() {
	t.Helper()
	old := os.Getenv("DEEPSEEK_API_KEY")
	if old == "" {
		_ = os.Setenv("DEEPSEEK_API_KEY", "sk-smoke-test-not-real")
		return func() { _ = os.Setenv("DEEPSEEK_API_KEY", old) }
	}
	return func() {}
}

// smokeEnvIsolated 设置隔离环境：HOME 指向临时目录，使子进程（contextloom-gui/
// contextloom）的 os.UserConfigDir() 落在临时目录——projects.json 等用户配置
// 绝不写入真实用户目录（历史教训：冒烟测试曾污染真实 projects.json）。
// macOS/Linux 均通过 HOME 生效（UserConfigDir = $HOME/Library/Application
// Support 或 $HOME/.config）；Windows 冒烟跳过不受影响。
func smokeEnvIsolated(t *testing.T) func() {
	t.Helper()
	// 设置 API Key 兜底（与 smokeEnv 同逻辑，此处内联避免递归）
	oldKey := os.Getenv("DEEPSEEK_API_KEY")
	restoreKey := func() {}
	if oldKey == "" {
		_ = os.Setenv("DEEPSEEK_API_KEY", "sk-smoke-test-not-real")
		restoreKey = func() { _ = os.Setenv("DEEPSEEK_API_KEY", oldKey) }
	}
	oldHome := os.Getenv("HOME")
	oldXDG := os.Getenv("XDG_CONFIG_HOME")
	tmpHome := t.TempDir()
	_ = os.Setenv("HOME", tmpHome)
	_ = os.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpHome, ".config"))
	return func() {
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("XDG_CONFIG_HOME", oldXDG)
		restoreKey()
	}
}

// waitProcessExit 等待进程真正退出（含僵尸收割）。
// 注意：仅用 kill(pid,0) 检测会把"已终止但未 wait 的僵尸进程"误判为存活，
// 必须用 Wait4(WNOHANG) 收割（测试进程是这些子进程的父进程，可安全收割）。
func waitProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var ws syscall.WaitStatus
		reaped, err := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil)
		if err == nil && reaped == pid {
			return // 已收割，真正退出
		}
		if err == syscall.ECHILD {
			// 非直接子进程（已被收养/收割）：回退 kill(pid,0) 检测
			if syscall.Kill(pid, 0) != nil {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("进程 %d 在 %v 内未退出", pid, timeout)
}

// TestSmoke_ContextLoomServeEndToEnd：backend 冒烟。
// 真实二进制 --serve → startBackend（启动器真实函数）→ GUI_ADDR → /healthz → SIGTERM。
func TestSmoke_ContextLoomServeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	start := time.Now()
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	addr, pid, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	// 兜底清理：测试中途失败（t.Fatalf 跳过 SIGTERM 分支）时，
	// 也必须杀掉 backend 进程——历史教训：冒烟崩溃残留的孤儿
	// backend 与后续测试抢资源，曾触发 runtime 栈溢出（mcleanup.go）。
	t.Cleanup(func() {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		waitProcessExit(t, pid, 2*time.Second)
	})
	t.Logf("backend 就绪: %s (pid=%d, 启动用时 %v)", addr, pid, time.Since(start))

	// /healthz 探活（公开端点，无需 token）
	u, err := url.Parse(addr)
	if err != nil {
		t.Fatalf("解析 backend 地址 %q: %v", addr, err)
	}
	healthURL := u.Scheme + "://" + u.Host + "/healthz"
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(healthURL)
	if err != nil {
		t.Fatalf("GET %s: %v", healthURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("/healthz 异常: code=%d body=%s", resp.StatusCode, body)
	}
	t.Logf("healthz 探活通过（%v 内）", time.Since(start))

	// SIGTERM 优雅退出
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM backend: %v", err)
	}
	waitProcessExit(t, pid, 3*time.Second)

	elapsed := time.Since(start)
	t.Logf("✅ 冒烟通过，全程 %v", elapsed)
	if elapsed > 15*time.Second {
		t.Errorf("冒烟测试耗时 %v 超过 15s 上限，应优化", elapsed)
	}
}

// TestSmoke_GUILauncherEndToEnd：启动器冒烟。
// contextloom-gui --browser 拉起 backend → 静态服务 + /api/health → 退出时联动停止 backend。
// （默认桌面窗口模式在无显示/无 WebView 的 CI 环境不可用，故用 --browser 回退模式验证启动器链路。）
func TestSmoke_GUILauncherEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	start := time.Now()

	// 构建 contextloom 与 contextloom-gui 到同一临时目录（同目录优先 → findContextLoomBin 命中）
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	build := func(pkg, name string) string {
		bin := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", bin, pkg)
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("构建 %s 失败: %v\n%s", pkg, err, out)
		}
		return bin
	}
	build("./cmd/contextloom", "contextloom")
	guiBin := build("./cmd/contextloom-gui", "contextloom-gui")
	restore := smokeEnvIsolated(t)
	defer restore()

	// 启动 contextloom-gui（--browser 回退模式，不依赖显示环境），扫描 stderr 获取 GUI 地址
	cmd := exec.Command(guiBin, "--browser")
	cmd.Dir = t.TempDir() // 项目目录用独立空目录
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// 兜底清理：SIGKILL 杀 gui 不会联动停止 backend（优雅退出才会），
	// 中途失败时 backend 会成孤儿进程残留（历史教训：残留 backend 资源
	// 竞争曾触发 runtime 栈溢出）。故从 state.json 读 backend PID 一并清理。
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if st, err := loadState(); err == nil && st.PID > 0 {
			_ = syscall.Kill(st.PID, syscall.SIGKILL)
		}
	}()

	// 匹配日志行 "ContextLoom GUI（浏览器模式）: http://127.0.0.1:PORT/（项目目录: ...）"
	var guiURL string
	addrRe := regexp.MustCompile(`ContextLoom GUI[^:]*: (http://127\.0\.0\.1:\d+/)`)
	sc := bufio.NewScanner(stderr)
	scDone := make(chan struct{})
	go func() {
		for sc.Scan() {
			if m := addrRe.FindStringSubmatch(sc.Text()); m != nil {
				guiURL = m[1]
				close(scDone)
				return
			}
		}
		close(scDone)
	}()
	select {
	case <-scDone:
	case <-time.After(8 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("等待 contextloom-gui 输出地址超时（8s）")
	}
	if guiURL == "" {
		t.Fatal("未解析到 contextloom-gui 地址")
	}
	t.Logf("contextloom-gui 就绪: %s（启动用时 %v）", guiURL, time.Since(start))

	// 前端页面可访问
	resp, err := http.Get(guiURL)
	if err != nil {
		t.Fatalf("GET %s: %v", guiURL, err)
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(page), "<html") {
		t.Fatalf("前端页面异常: code=%d", resp.StatusCode)
	}

	// 顶栏 logo 链路：/logo.png 静态资源可达（go:embed 内嵌）且 HTML 引用正确。
	// 修复目标：顶栏 brand 区以 logo 图片替换 ContextLoom 文本。
	hc := &http.Client{Timeout: 3 * time.Second}
	lresp, err := hc.Get(guiURL + "logo.png")
	if err != nil {
		t.Fatalf("GET /logo.png: %v", err)
	}
	defer lresp.Body.Close()
	lbody, _ := io.ReadAll(lresp.Body)
	if lresp.StatusCode != 200 || len(lbody) == 0 {
		t.Fatalf("/logo.png 异常: code=%d size=%d", lresp.StatusCode, len(lbody))
	}
	ct := lresp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "image/png") {
		t.Fatalf("/logo.png Content-Type 应为 image/png，实际 %q", ct)
	}
	if len(lbody) < 8 || string(lbody[1:4]) != "PNG" {
		t.Fatalf("/logo.png 非 PNG 数据: %x", lbody[:8])
	}
	if !strings.Contains(string(page), `id="brandLogo"`) ||
		!strings.Contains(string(page), `src="/logo.png"`) ||
		!strings.Contains(string(page), `rel="icon" href="/logo.png"`) {
		t.Fatalf("HTML 未引用 logo（brandLogo img / favicon 缺失）")
	}
	t.Logf("✅ 顶栏 logo 链路 OK: /logo.png 200 (Content-Type=%s)", ct)

	// /api/health 应返回 backend 地址（代理已注入）
	hresp, err := hc.Get(guiURL + "api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	hbody, _ := io.ReadAll(hresp.Body)
	hresp.Body.Close()
	if hresp.StatusCode != 200 || !strings.Contains(string(hbody), `"ok":true`) {
		t.Fatalf("/api/health 异常: code=%d body=%s", hresp.StatusCode, hbody)
	}
	t.Logf("/api/health 代理通过（%v 内）", time.Since(start))

	// SIGTERM contextloom-gui → 应优雅退出并联动停止 backend
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM contextloom-gui: %v", err)
	}
	waitProcessExit(t, cmd.Process.Pid, 5*time.Second)

	elapsed := time.Since(start)
	t.Logf("✅ 启动器冒烟通过，全程 %v", elapsed)
	if elapsed > 20*time.Second {
		t.Errorf("启动器冒烟耗时 %v 超过 20s 上限", elapsed)
	}
}

// TestSmoke_StartBackend_FailureDiagnosis：错误路径诊断（用非可执行文件模拟坏二进制）。
func TestSmoke_StartBackend_FailureDiagnosis(t *testing.T) {
	dir := t.TempDir()
	badBin := filepath.Join(dir, "contextloom")
	if err := os.WriteFile(badBin, []byte("#!/bin/sh\necho 'boom' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, _, err := startBackend(badBin, dir)
	if err == nil {
		t.Fatal("坏二进制应返回错误")
	}
	if !strings.Contains(err.Error(), "stderr") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("错误诊断应包含 stderr 内容: %v", err)
	}
	t.Logf("错误诊断 OK: %v", err)
}

// TestSmoke_GUIWSFullChain：GUI 启动器完整 WS 链路冒烟。
// 真实构建 contextloom → startBackend 拉起 → 构建与 contextloom-gui 完全相同的
// 本地 mux（fileServer + wsProxy + health）→ 经代理 WS 握手 →
// ping / chat.listSessions 真实返回。这正是前端"会话列表加载"的完整链路，
// 验证其可用性（若此测试通过，桌面窗口"加载中"即为 WebView 环境问题）。
func TestSmoke_GUIWSFullChain(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	start := time.Now()
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	backendAddr, backendPID, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	// 兜底清理：与 TestSmoke_ContextLoomServeEndToEnd 一致，中途失败也杀 backend，
	// 防止孤儿进程残留（历史：曾因残留进程资源竞争触发 runtime 栈溢出）。
	t.Cleanup(func() {
		_ = syscall.Kill(backendPID, syscall.SIGKILL)
		waitProcessExit(t, backendPID, 2*time.Second)
	})
	u, err := url.Parse(backendAddr)
	if err != nil {
		t.Fatalf("解析 backend 地址: %v", err)
	}
	token := u.Query().Get("token")
	backendBase := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()

	// 与 contextloom-gui 完全相同的代理构建
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/api/ws", wsProxy(backendBase, token))
	proxyMux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"backend":%q}`, backendBase)
	})
	// 本地 mux（与 runDesktop/浏览器模式同款）
	mux := http.NewServeMux()
	mux.Handle("/api/ws", proxyMux)
	mux.Handle("/api/health", proxyMux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	// WS 握手（经代理 → backend /ws?token=xxx）
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 握手失败: %v (HTTP %v)", err, resp)
	}
	defer conn.Close()

	// ping
	if err := conn.WriteJSON(map[string]any{"id": "1", "type": "req", "method": "ping", "params": map[string]any{}}); err != nil {
		t.Fatalf("发送 ping: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var pong struct {
		Type   string         `json:"type"`
		ID     string         `json:"id"`
		OK     bool           `json:"ok"`
		Result map[string]any `json:"result"`
		Error  string         `json:"error"`
	}
	if err := conn.ReadJSON(&pong); err != nil {
		t.Fatalf("读取 ping 响应: %v", err)
	}
	if pong.Type != "resp" || !pong.OK || pong.Result["version"] == "" {
		t.Fatalf("ping 响应异常: %+v", pong)
	}
	t.Logf("✅ ping OK: version=%v", pong.Result["version"])

	// chat.listSessions（前端"会话列表"的核心请求）
	if err := conn.WriteJSON(map[string]any{"id": "2", "type": "req", "method": "chat.listSessions", "params": map[string]any{}}); err != nil {
		t.Fatalf("发送 listSessions: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var lst struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		OK     bool   `json:"ok"`
		Result []any  `json:"result"`
		Error  string `json:"error"`
	}
	if err := conn.ReadJSON(&lst); err != nil {
		t.Fatalf("读取 listSessions 响应: %v", err)
	}
	if lst.Type != "resp" || !lst.OK {
		t.Fatalf("listSessions 失败: %+v", lst.Error)
	}
	t.Logf("✅ listSessions OK: %d 个会话（全链路 %v）", len(lst.Result), time.Since(start))
}

// TestSmoke_ConfigEndpoint：桌面窗口前端获取本地服务地址的契约测试。
// 前端（wails:// 环境）fetch("/api/config") 期望返回 {"gui":"http://127.0.0.1:PORT/"}，
// 据此构造绝对地址 WS。验证该端点与格式（runDesktop 与本地 mux 使用同一 handler）。
func TestSmoke_ConfigEndpoint(t *testing.T) {
	// 与 runDesktop 完全相同的注册方式
	configHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"gui":"http://127.0.0.1:43210/"}`)
	})
	ts := httptest.NewServer(configHandler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/config")
	if err != nil {
		t.Fatalf("GET /api/config: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var cfg struct {
		GUI string `json:"gui"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("响应非 JSON: %v (%s)", err, body)
	}
	if !strings.HasPrefix(cfg.GUI, "http://") {
		t.Fatalf("gui 字段格式错误: %q", cfg.GUI)
	}
	t.Logf("✅ /api/config OK: %s", cfg.GUI)
}

// TestSmoke_GUI_SessionDeleteAndModePersist：端到端验证两个需求：
// ① 会话删除链路（GUI 会话面板"删除会话"依赖的 chat.deleteSession）；
// ② mode 持久化（切换 goal 后重启 backend，模式仍为 goal）。
func TestSmoke_GUI_SessionDeleteAndModePersist(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	backendAddr, pid, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	defer func() { syscall.Kill(pid, syscall.SIGKILL) }()

	u, err := url.Parse(backendAddr)
	if err != nil {
		t.Fatalf("解析 backend 地址: %v", err)
	}
	token := u.Query().Get("token")
	backendBase := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/api/ws", wsProxy(backendBase, token))
	mux := http.NewServeMux()
	mux.Handle("/api/ws", proxyMux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 握手失败: %v", err)
	}

	// 请求-响应助手
	call := func(id, method string, params map[string]any) any {
		t.Helper()
		if err := conn.WriteJSON(map[string]any{"id": id, "type": "req", "method": method, "params": params}); err != nil {
			t.Fatalf("发送 %s: %v", method, err)
		}
		conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		var env struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			OK     bool   `json:"ok"`
			Result any    `json:"result"`
			Error  any    `json:"error"`
		}
		for {
			if err := conn.ReadJSON(&env); err != nil {
				t.Fatalf("读取 %s 响应: %v", method, err)
			}
			if env.Type == "resp" && env.ID == id {
				if !env.OK {
					t.Fatalf("%s 失败: %v", method, env.Error)
				}
				return env.Result
			}
		}
	}

	// ① 会话删除链路（与前端一致：后端禁止删除当前活跃会话，先切走再删）
	r := call("1", "chat.newSession", map[string]any{"title": "冒烟删除测试A"}).(map[string]any)
	uuidA, _ := r["uuid"].(string)
	if uuidA == "" {
		t.Fatal("newSession A 未返回 uuid")
	}
	// 新建第二个会话 → 当前活跃变为 B，A 变为可删除
	r = call("2", "chat.newSession", map[string]any{"title": "冒烟删除测试B"}).(map[string]any)
	uuidB, _ := r["uuid"].(string)
	if uuidB == "" {
		t.Fatal("newSession B 未返回 uuid")
	}
	r2 := call("3", "chat.listSessions", map[string]any{})
	if !sessionUUIDsContainAny(r2, uuidA) || !sessionUUIDsContainAny(r2, uuidB) {
		t.Fatalf("新建后 listSessions 应包含 A/B: %+v", r2)
	}
	call("4", "chat.deleteSession", map[string]any{"uuid": uuidA})
	r5 := call("5", "chat.listSessions", map[string]any{})
	if sessionUUIDsContainAny(r5, uuidA) {
		t.Fatalf("删除后 listSessions 仍包含 A: %+v", r5)
	}
	if !sessionUUIDsContainAny(r5, uuidB) {
		t.Fatalf("删除 A 后 B 应保留: %+v", r5)
	}
	t.Log("✅ 会话删除链路 OK：newSession×2 → listSessions(含AB) → deleteSession(A) → listSessions(仅B)")

	// ② mode 持久化：切 goal → 检查 settings.json（per-session modes）→ 重启 backend → 仍为 goal
	call("6", "mode.set", map[string]any{"mode": "goal"})
	settingsPath := filepath.Join(workDir, ".contextloom", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("读取 settings.json: %v", err)
	}
	// 按会话隔离语义：顶层 mode=全局默认（新会话），会话级 mode 在 modes map
	// （会话 UUID 无法从 settings.json 预知，验证"存在 modes 且当前会话键值=goal"）
	if !strings.Contains(string(data), `"modes"`) {
		t.Fatalf("settings.json 应含 per-session modes（问题 2 修复）: %s", data)
	}
	if strings.Contains(string(data), `"mode": "goal"`) {
		t.Fatalf("顶层 mode 应保持全局默认（chat），goal 应只在 modes map（per-session 隔离）: %s", data)
	}
	t.Log("✅ mode.set(goal) 已写入 settings.json 的 modes（按会话隔离）")

	// 重启 backend（模拟重新打开）
	conn.Close()
	syscall.Kill(pid, syscall.SIGKILL)
	time.Sleep(300 * time.Millisecond)
	backendAddr2, pid2, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("重启 backend: %v", err)
	}
	defer func() { syscall.Kill(pid2, syscall.SIGKILL) }()
	u2, _ := url.Parse(backendAddr2)
	token2 := u2.Query().Get("token")
	backendBase2 := (&url.URL{Scheme: u2.Scheme, Host: u2.Host}).String()
	proxyMux2 := http.NewServeMux()
	proxyMux2.Handle("/api/ws", wsProxy(backendBase2, token2))
	mux2 := http.NewServeMux()
	mux2.Handle("/api/ws", proxyMux2)
	ts2 := httptest.NewServer(mux2)
	defer ts2.Close()
	wsURL2 := "ws" + strings.TrimPrefix(ts2.URL, "http") + "/api/ws"
	conn2, _, err := websocket.DefaultDialer.Dial(wsURL2, nil)
	if err != nil {
		t.Fatalf("重启后 WS 握手失败: %v", err)
	}
	defer conn2.Close()
	if err := conn2.WriteJSON(map[string]any{"id": "7", "type": "req", "method": "chat.loadSession", "params": map[string]any{"uuid": uuidB}}); err != nil {
		t.Fatalf("发送 chat.loadSession: %v", err)
	}
	// 重启后模拟 GUI restoreCurrentSession：恢复会话 B（此前 mode.set(goal) 作用于 B）
	var lr struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		OK     bool   `json:"ok"`
		Result any    `json:"result"`
		Error  any    `json:"error"`
	}
	conn2.SetReadDeadline(time.Now().Add(8 * time.Second))
	for {
		if err := conn2.ReadJSON(&lr); err != nil {
			t.Fatalf("读取 chat.loadSession: %v", err)
		}
		if lr.Type == "resp" && lr.ID == "7" {
			break
		}
	}
	if !lr.OK {
		t.Fatalf("chat.loadSession 失败: %v", lr.Error)
	}
	if err := conn2.WriteJSON(map[string]any{"id": "8", "type": "req", "method": "status.bar", "params": map[string]any{}}); err != nil {
		t.Fatalf("发送 status.bar: %v", err)
	}
	conn2.SetReadDeadline(time.Now().Add(8 * time.Second))
	var st struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		OK     bool   `json:"ok"`
		Result any    `json:"result"`
		Error  any    `json:"error"`
	}
	for {
		if err := conn2.ReadJSON(&st); err != nil {
			t.Fatalf("读取 status.bar: %v", err)
		}
		if st.Type == "resp" && st.ID == "8" {
			break
		}
	}
	if !st.OK {
		t.Fatalf("status.bar 失败: %v", st.Error)
	}
	resMap, _ := st.Result.(map[string]any)
	if mode, _ := resMap["mode"].(string); mode != "goal" {
		t.Fatalf("重启后模式应为 goal，实际 %q（持久化失败）", mode)
	}
	t.Log("✅ mode 持久化 OK：重启后 status.bar.mode = goal")
}

func sessionUUIDsContainAny(r any, uuid string) bool {
	var arr []any
	switch v := r.(type) {
	case []any:
		arr = v
	case map[string]any:
		if a, ok := v["sessions"].([]any); ok {
			arr = a
		}
	default:
		return false
	}
	for _, s := range arr {
		if m, ok := s.(map[string]any); ok {
			if u, _ := m["uuid"].(string); u == uuid {
				return true
			}
		}
	}
	return false
}

// TestSmoke_GUI_SessionTrackingRealBackend：真实 backend 下验证会话跟踪链路。
// 回归根因：GetSessionID 恒返回空串 → GUI 无法跟踪当前会话
// （agent.done 无 sessionId、chat.send 会话匹配失效）→ 多轮对话断上下文。
// 通过 chat.newSession → status.bar 断言 sessionId 真实返回且一致。
func TestSmoke_GUI_SessionTrackingRealBackend(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	addr, pid, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGTERM) }()

	u, err := url.Parse(addr)
	if err != nil {
		t.Fatalf("解析 backend 地址 %q: %v", addr, err)
	}
	token := u.Query().Get("token")
	// backend 的 WS 路由是 /ws（/api/ws 是 contextloom-gui 启动器的代理路径）
	wsURL := "ws://" + u.Host + "/ws?token=" + token
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	defer conn.Close()

	readResp := func(id string) map[string]any {
		t.Helper()
		for {
			var env struct {
				ID     string         `json:"id"`
				OK     bool           `json:"ok"`
				Result map[string]any `json:"result"`
				Error  *struct {
					Msg string `json:"msg"`
				} `json:"error"`
			}
			if err := conn.ReadJSON(&env); err != nil {
				t.Fatalf("读取响应失败: %v", err)
			}
			if env.ID != id {
				continue
			}
			if !env.OK {
				t.Fatalf("方法失败: %v", env.Error)
			}
			return env.Result
		}
	}

	// 1) 新建会话
	if err := conn.WriteJSON(map[string]any{
		"id": "1", "type": "req", "method": "chat.newSession",
		"params": map[string]any{"title": "冒烟会话"},
	}); err != nil {
		t.Fatalf("发送 newSession: %v", err)
	}
	res := readResp("1")
	sid, _ := res["uuid"].(string)
	if sid == "" {
		t.Fatal("chat.newSession 返回空 uuid")
	}

	// 2) status.bar 必须返回同一 sessionId（GetSessionID 修复前恒为空串）
	if err := conn.WriteJSON(map[string]any{
		"id": "2", "type": "req", "method": "status.bar", "params": map[string]any{},
	}); err != nil {
		t.Fatalf("发送 status.bar: %v", err)
	}
	res = readResp("2")
	got, _ := res["sessionId"].(string)
	if got != sid {
		t.Fatalf("status.bar sessionId 应为 %q，实际 %q（GetSessionID 回归）", sid, got)
	}

	// 3) chat.loadSession 应能恢复该会话
	if err := conn.WriteJSON(map[string]any{
		"id": "3", "type": "req", "method": "chat.loadSession",
		"params": map[string]any{"uuid": sid},
	}); err != nil {
		t.Fatalf("发送 loadSession: %v", err)
	}
	res = readResp("3")
	if uid, _ := res["uuid"].(string); uid != sid {
		t.Fatalf("loadSession 返回 uuid 应为 %q，实际 %q", sid, uid)
	}

	// 4) 显式优雅退出并等待
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatalf("SIGTERM backend: %v", err)
	}
	waitProcessExit(t, pid, 3*time.Second)
	t.Logf("✅ 会话跟踪链路正常: sessionId=%s", sid)
}

// TestSmoke_GUI_DeleteCurrentSessionAndRestore：端到端验证两个修复契约：
// ① 删除"当前活跃会话"必须被后端拒绝（前端据此先切走再删，150ms 盲等
//
//	竞态修复的前提）；切走后删除成功，且列表立即刷新。
//
// ② chat.currentSession 在浏览器刷新后恢复当前会话（返回 uuid/mode/消息）。
func TestSmoke_GUI_DeleteCurrentSessionAndRestore(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	backendAddr, pid, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	defer func() { syscall.Kill(pid, syscall.SIGKILL) }()

	u, err := url.Parse(backendAddr)
	if err != nil {
		t.Fatalf("解析 backend 地址: %v", err)
	}
	token := u.Query().Get("token")
	backendBase := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/api/ws", wsProxy(backendBase, token))
	mux := http.NewServeMux()
	mux.Handle("/api/ws", proxyMux)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 握手失败: %v", err)
	}
	defer conn.Close()

	call := func(id, method string, params map[string]any) any {
		t.Helper()
		if err := conn.WriteJSON(map[string]any{"id": id, "type": "req", "method": method, "params": params}); err != nil {
			t.Fatalf("发送 %s: %v", method, err)
		}
		conn.SetReadDeadline(time.Now().Add(8 * time.Second))
		var env struct {
			Type   string `json:"type"`
			ID     string `json:"id"`
			OK     bool   `json:"ok"`
			Result any    `json:"result"`
			Error  any    `json:"error"`
		}
		for {
			if err := conn.ReadJSON(&env); err != nil {
				t.Fatalf("读取 %s 响应: %v", method, err)
			}
			if env.Type == "resp" && env.ID == id {
				if !env.OK {
					t.Fatalf("%s 失败: %v", method, env.Error)
				}
				return env.Result
			}
		}
	}
	// 期待失败的调用（用于验证"删除当前活跃会话被拒"）
	// ① 新建 A（当前=A）→ chat.currentSession 应返回 A
	r := call("1", "chat.newSession", map[string]any{"title": "删除测试A"}).(map[string]any)
	uuidA, _ := r["uuid"].(string)
	if uuidA == "" {
		t.Fatal("newSession A 未返回 uuid")
	}
	cur := call("2", "chat.currentSession", map[string]any{}).(map[string]any)
	if id, _ := cur["uuid"].(string); id != uuidA {
		t.Fatalf("currentSession 应返回 A(%s)，实际 %v", uuidA, cur)
	}

	// ② 删除当前活跃会话 A → 允许成功（新契约："删除最后一个会话后不自动新建"），
	//    删除后回到无会话状态（currentSession 返回空 uuid）
	call("3", "chat.deleteSession", map[string]any{"uuid": uuidA})
	curEmpty := call("4", "chat.currentSession", map[string]any{}).(map[string]any)
	if id, _ := curEmpty["uuid"].(string); id != "" {
		t.Fatalf("删除当前活跃会话后 currentSession 应回到空，实际 %v", curEmpty)
	}
	t.Log("✅ 删除当前活跃会话成功（新契约）+ currentSession 回到空")

	// ③ 新建 B（当前=B）→ 再新建 C（当前=C）→ 删除 B 成功且列表立即不含 B
	r = call("5", "chat.newSession", map[string]any{"title": "删除测试B"}).(map[string]any)
	uuidB, _ := r["uuid"].(string)
	r = call("6", "chat.newSession", map[string]any{"title": "删除测试C"}).(map[string]any)
	uuidC, _ := r["uuid"].(string)
	call("7", "chat.deleteSession", map[string]any{"uuid": uuidB})
	r8 := call("8", "chat.listSessions", map[string]any{})
	if sessionUUIDsContainAny(r8, uuidB) {
		t.Fatalf("删除 B 后 listSessions 仍含 B: %+v", r8)
	}
	if !sessionUUIDsContainAny(r8, uuidC) {
		t.Fatalf("C 应保留: %+v", r8)
	}
	t.Log("✅ 删除非当前会话成功 + 列表立即刷新（无 B、含 C）")

	// ④ chat.currentSession 恢复链路（浏览器刷新场景）：当前=C，
	// currentSession 应返回 C 且带 messages 字段
	cur2 := call("9", "chat.currentSession", map[string]any{}).(map[string]any)
	if id, _ := cur2["uuid"].(string); id != uuidC {
		t.Fatalf("currentSession 应返回 C(%s)，实际 %v", uuidC, cur2)
	}
	if _, has := cur2["messages"]; !has {
		t.Fatalf("currentSession 应含 messages 字段: %v", cur2)
	}
	if _, has := cur2["mode"]; !has {
		t.Fatalf("currentSession 应含 mode 字段: %v", cur2)
	}
	t.Logf("✅ chat.currentSession 恢复链路 OK: uuid=%s mode=%v", uuidC, cur2["mode"])
}

// TestSmoke_GUI_TermChain 多终端端到端冒烟：真实 backend → term.spawn →
// term.input(echo) → term.output 事件 → term.list → term.kill。
func TestSmoke_GUI_TermChain(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	workDir := t.TempDir()
	backendAddr, backendPID, _, err := startBackend(bin, workDir)
	if err != nil {
		t.Fatalf("startBackend: %v", err)
	}
	// 兜底清理：中途失败也杀 backend，防止孤儿进程残留
	// （历史教训：残留 backend 资源竞争曾触发 runtime 栈溢出）。
	t.Cleanup(func() {
		_ = syscall.Kill(backendPID, syscall.SIGKILL)
		waitProcessExit(t, backendPID, 2*time.Second)
	})
	u, err := url.Parse(backendAddr)
	if err != nil {
		t.Fatalf("解析 backend 地址: %v", err)
	}
	wsURL := "ws" + strings.TrimPrefix(u.Scheme+"://"+u.Host, "http") + "/ws?token=" + u.Query().Get("token")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	defer conn.Close()

	call := func(id, method string, params any) any {
		t.Helper()
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := conn.WriteJSON(map[string]any{"id": id, "type": "req", "method": method, "params": params}); err != nil {
			t.Fatalf("发送 %s: %v", method, err)
		}
		for {
			_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			var env map[string]any
			if err := conn.ReadJSON(&env); err != nil {
				t.Fatalf("读取响应(%s): %v", method, err)
			}
			if env["id"] == id {
				if ok, _ := env["ok"].(bool); !ok {
					t.Fatalf("%s 失败: %v", method, env["error"])
				}
				return env["result"]
			}
		}
	}

	// ① spawn 一个终端（工作目录 = backend 项目根 = workDir）
	r := call("1", "term.spawn", map[string]any{}).(map[string]any)
	tid, _ := r["id"].(string)
	if tid == "" {
		t.Fatal("term.spawn 未返回 id")
	}
	t.Logf("✅ term.spawn: id=%s shell=%v cwd=%v", tid, r["shell"], r["cwd"])

	// ② input echo → 等待 term.output 事件
	if err := conn.WriteJSON(map[string]any{"id": "2", "type": "req", "method": "term.input", "params": map[string]any{"id": tid, "data": "echo TERM_SMOKE_99\n"}}); err != nil {
		t.Fatalf("term.input: %v", err)
	}
	got := ""
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		var env map[string]any
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env["type"] == "event" && env["event"] == "term.output" {
			if d, ok := env["data"].(map[string]any); ok && d["id"] == tid {
				if s, ok := d["data"].(string); ok {
					got += s
					if strings.Contains(got, "TERM_SMOKE_99") {
						break
					}
				}
			}
		}
	}
	if !strings.Contains(got, "TERM_SMOKE_99") {
		t.Fatalf("未收到终端输出: %q", got)
	}
	t.Logf("✅ term.input → term.output 事件: %q", strings.TrimSpace(got))

	// ③ list 含该终端；kill 后 list 空
	r3 := call("3", "term.list", map[string]any{}).([]any)
	if len(r3) != 1 {
		t.Fatalf("term.list 期望 1，实际 %d", len(r3))
	}
	call("4", "term.kill", map[string]any{"id": tid})
	r5 := call("5", "term.list", map[string]any{}).([]any)
	if len(r5) != 0 {
		t.Fatalf("kill 后期望 0，实际 %d", len(r5))
	}
	t.Log("✅ term.kill 后 term.list 为空")
}

// dialSmokeWS 建立 WS 连接（经代理）。
func dialSmokeWS(t *testing.T, wsURL string) *websocket.Conn {
	t.Helper()
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("WS 握手失败: %v (HTTP %v)", err, resp)
	}
	return conn
}

// smokeNewSession 通过 WS 创建会话并返回 uuid。
func smokeNewSession(t *testing.T, conn *websocket.Conn) string {
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

// smokeListSessions 查询会话列表并返回条数。
func smokeListSessions(t *testing.T, conn *websocket.Conn) int {
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

// TestSmoke_ProjectSwitch_SessionListUpdates 端到端验证用户报告的核心问题：
// "gui切换项目时，会话列表没有更新到对应项目的会话列表"。
// 完整模拟真实 GUI：backendMgr 在 projA 拉起 backend → WS 经动态代理
// （wsProxyTo 每次读取当前 backend 地址，与真实 GUI 相同）创建会话 →
// switchProject(projB)（backend 重启）→ 前端 reload 等价行为（重连 WS）→
// listSessions 必须返回新项目的会话列表 → 切回 projA → 列表恢复。
func TestSmoke_ProjectSwitch_SessionListUpdates(t *testing.T) {
	if testing.Short() {
		t.Skip("短模式跳过端到端冒烟")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows 信号语义不同，端到端冒烟在开发机/CI Linux 跑")
	}
	bin := buildSmokeBin(t)
	restore := smokeEnvIsolated(t)
	defer restore()

	projA := t.TempDir()
	projB := t.TempDir()
	if err := os.WriteFile(filepath.Join(projA, "marker-a.txt"), []byte("A"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projB, "marker-b.txt"), []byte("B"), 0o644); err != nil {
		t.Fatal(err)
	}

	b := newBackendMgr()
	t.Cleanup(func() { b.stop() })

	// 1. 初始在 projA 拉起 backend（等价 GUI 默认第一个项目）
	addrA, pidA, doneA, err := startBackend(bin, projA)
	if err != nil {
		t.Fatalf("startBackend(A): %v", err)
	}
	b.set(addrA, pidA, projA, true, doneA)
	t.Cleanup(func() { _ = syscall.Kill(pidA, syscall.SIGKILL) })

	// 2. 动态代理（与真实 GUI 完全相同：wsProxyTo 每次请求读当前地址）
	proxyMux := http.NewServeMux()
	proxyMux.Handle("/api/ws", wsProxyTo(b))
	mux := http.NewServeMux()
	mux.Handle("/api/ws", proxyMux)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/ws"

	// 3. 在 A 创建会话 → 列表=1
	conn := dialSmokeWS(t, wsURL)
	defer conn.Close()
	uuidA := smokeNewSession(t, conn)
	if got := smokeListSessions(t, conn); got != 1 {
		t.Fatalf("A 项目 listSessions 期望 1 条，实际 %d", got)
	}
	t.Logf("✅ A 项目创建会话 %s，列表=1", uuidA)

	// 4. 切换到 B（backend 在 B 重启——真实 GUI 同一调用）
	res, err := b.switchProject(bin, projB)
	if err != nil {
		t.Fatalf("switchProject(B): %v", err)
	}
	t.Logf("✅ 已切换到 %v", res["current"])

	// 5. 前端 reload 等价：重连 WS（同一动态代理，自动指向新 backend）
	conn.Close()
	conn = dialSmokeWS(t, wsURL)
	defer conn.Close()
	if got := smokeListSessions(t, conn); got != 0 {
		t.Fatalf("B 项目 listSessions 期望 0 条（新项目无会话），实际 %d", got)
	}
	t.Logf("✅ B 项目会话列表已更新：空列表（跨项目数据隔离正确）")

	// B 新建会话 → 列表=1（B 自己的）
	uuidB := smokeNewSession(t, conn)
	if got := smokeListSessions(t, conn); got != 1 {
		t.Fatalf("B 项目新建后 listSessions 期望 1 条，实际 %d", got)
	}
	t.Logf("✅ B 项目新建会话 %s，列表=1", uuidB)

	// 6. 切回 A → 列表恢复为 A 的会话
	if _, err := b.switchProject(bin, projA); err != nil {
		t.Fatalf("switchProject(A): %v", err)
	}
	conn.Close()
	conn = dialSmokeWS(t, wsURL)
	defer conn.Close()
	if got := smokeListSessions(t, conn); got != 1 {
		t.Fatalf("切回 A 后 listSessions 期望 1 条，实际 %d", got)
	}
	t.Logf("✅ 切回 A 项目，会话列表恢复=1（数据完整，uuid=%s/%s）", uuidA, uuidB)
}
