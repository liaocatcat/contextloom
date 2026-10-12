package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestFindContextLoomInDir：同目录查找（纯函数，findContextLoomBin 内部 os.Executable 不可注入）
func TestFindContextLoomInDir(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "contextloom")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := findContextLoomInDir(dir)
	if !ok || got != bin {
		t.Fatalf("期望 %s 命中，得到 %s/%v", bin, got, ok)
	}
	if _, ok := findContextLoomInDir(filepath.Join(dir, "nope")); ok {
		t.Fatal("不存在目录不应命中")
	}
	empty := t.TempDir()
	if _, ok := findContextLoomInDir(empty); ok {
		t.Fatal("无 contextloom 目录不应命中")
	}
	if err := os.WriteFile(filepath.Join(empty, "contextloom.exe"), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := findContextLoomInDir(empty); !ok {
		t.Fatal("contextloom.exe 应命中")
	}
}

// TestFindContextLoomBin_EnvFallback：真实测试二进制目录无 contextloom → CONTEXTLOOM_BIN 兜底
func TestFindContextLoomBin_EnvFallback(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "contextloom-custom")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONTEXTLOOM_BIN", bin)
	got, err := findContextLoomBin()
	if err != nil {
		t.Fatalf("findContextLoomBin 错误: %v", err)
	}
	if got != bin {
		t.Fatalf("期望 %s，得到 %s", bin, got)
	}
}

// TestFindContextLoomBin_Env：CONTEXTLOOM_BIN 环境变量
func TestFindContextLoomBin_Env(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "contextloom-custom")
	if err := os.WriteFile(bin, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := os.Getenv("CONTEXTLOOM_BIN")
	_ = os.Setenv("CONTEXTLOOM_BIN", bin)
	defer os.Setenv("CONTEXTLOOM_BIN", old)

	got, err := findContextLoomBin()
	if err != nil {
		t.Fatalf("findContextLoomBin 错误: %v", err)
	}
	if got != bin {
		t.Fatalf("期望 %s，得到 %s", bin, got)
	}
}

// TestParseGUIAddrLine：GUI_ADDR 行解析
func TestParseGUIAddrLine(t *testing.T) {
	cases := []struct {
		line string
		addr string
		ok   bool
	}{
		{"GUI_ADDR=http://127.0.0.1:12345/?token=abc123", "http://127.0.0.1:12345/?token=abc123", true},
		{"GUI_ADDR=http://127.0.0.1:0/?token=", "http://127.0.0.1:0/?token=", true},
		{"[gui] 服务已启动: http://127.0.0.1:12345/", "", false},
		{"", "", false},
		{"GUI_ADDR=", "http://127.0.0.1:0/?token=", false}, // 空地址视为异常
	}
	for _, c := range cases {
		addr, ok := parseGUIAddrLine(c.line)
		if ok != c.ok || (c.ok && addr != c.addr) {
			t.Errorf("parseGUIAddrLine(%q) = (%q, %v)，期望 (%q, %v)", c.line, addr, ok, c.addr, c.ok)
		}
	}
}

// TestStateSaveLoad：状态持久化往返
func TestStateSaveLoad(t *testing.T) {
	old := os.Getenv("XDG_CACHE_HOME")
	_ = os.Setenv("XDG_CACHE_HOME", t.TempDir())
	defer os.Setenv("XDG_CACHE_HOME", old)

	s := &state{Addr: "http://127.0.0.1:12345/?token=abc", PID: 42, Dir: "/tmp/x"}
	if err := saveState(s); err != nil {
		t.Fatalf("saveState: %v", err)
	}
	got, err := loadState()
	if err != nil {
		t.Fatalf("loadState: %v", err)
	}
	if got.Addr != s.Addr || got.PID != 42 || got.Dir != "/tmp/x" {
		t.Fatalf("往返不一致: %+v", got)
	}
	clearState()
	if _, err := loadState(); err == nil {
		t.Fatal("clearState 后应加载失败")
	}
}

// TestState_WindowSizeRoundTrip 验证窗口状态记忆：WinW/WinH 可持久化并恢复。
func TestState_WindowSizeRoundTrip(t *testing.T) {
	s := &state{Addr: "127.0.0.1:1234", PID: 42, Dir: "/tmp", TS: 1, WinW: 1366, WinH: 768}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got state
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.WinW != 1366 || got.WinH != 768 {
		t.Fatalf("窗口尺寸未正确序列化: %+v", got)
	}
	// 旧版本 state（无窗口字段）反序列化不报错且默认 0
	old := []byte(`{"addr":"a","pid":1,"dir":"d","ts":0}`)
	var o state
	if err := json.Unmarshal(old, &o); err != nil {
		t.Fatalf("旧格式反序列化失败: %v", err)
	}
	if o.WinW != 0 || o.WinH != 0 {
		t.Fatalf("旧格式应得 0,0: %+v", o)
	}
}

// ── shouldReuseBackend：跨项目串联根因回归测试 ──
// 复用 state.json 中记录的运行中 backend 必须满足【目录一致性】：
// 记录的项目目录与当前启动目录不同 → 绝不复用（否则会话写到其他项目）。

func TestShouldReuseBackend_SameDirAndAlive(t *testing.T) {
	st := &state{Addr: "http://127.0.0.1:39991/?token=x", PID: 42, Dir: "/proj/a"}
	alive := func(string) bool { return true }
	if !shouldReuseBackend(st, "/proj/a", alive) {
		t.Fatal("目录一致且探活成功：应复用")
	}
}

func TestShouldReuseBackend_DirDiffers(t *testing.T) {
	// 上次 GUI 在 /proj/b 残留 backend，本次在 /proj/a 启动 → 不复用
	st := &state{Addr: "http://127.0.0.1:39991/?token=x", PID: 42, Dir: "/proj/b"}
	alive := func(string) bool { return true }
	if shouldReuseBackend(st, "/proj/a", alive) {
		t.Fatal("目录不一致：必须不复用（否则跨项目串联）")
	}
}

func TestShouldReuseBackend_ProbeDead(t *testing.T) {
	st := &state{Addr: "http://127.0.0.1:39991/?token=x", PID: 42, Dir: "/proj/a"}
	dead := func(string) bool { return false }
	if shouldReuseBackend(st, "/proj/a", dead) {
		t.Fatal("探活失败：不应复用")
	}
}

func TestShouldReuseBackend_EdgeCases(t *testing.T) {
	alive := func(string) bool { return true }
	cases := []struct {
		name string
		st   *state
		dir  string
		want bool
	}{
		{"nil state", nil, "/proj/a", false},
		{"空 addr", &state{Addr: "", Dir: "/proj/a"}, "/proj/a", false},
		{"空 dir（历史 state 无目录字段）", &state{Addr: "http://x", Dir: ""}, "/proj/a", false},
		{"启动目录为空", &state{Addr: "http://x", Dir: "/proj/a"}, "", false},
		{"路径尾斜杠差异视为不同（保守不复用）", &state{Addr: "http://x", Dir: "/proj/a/"}, "/proj/a", false},
	}
	for _, c := range cases {
		if got := shouldReuseBackend(c.st, c.dir, alive); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}
