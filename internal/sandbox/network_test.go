package sandbox

import (
	"strings"
	"testing"
)

// lookupEnv 从 env 切片中取变量值（不存在返回 ""）。
func lookupEnv(env []string, key string) string {
	for _, e := range env {
		if name, val, ok := strings.Cut(e, "="); ok && name == key {
			return val
		}
	}
	return ""
}

// countEnv 统计 env 中同名变量出现次数。
func countEnv(env []string, key string) int {
	n := 0
	for _, e := range env {
		if name, _, ok := strings.Cut(e, "="); ok && name == key {
			n++
		}
	}
	return n
}

func equalEnv(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNetDenyEnv_AllProxiesPointToDeadPort(t *testing.T) {
	// 宿主环境只有部分代理变量（模拟未配置代理的机器）
	env := []string{
		"HTTP_PROXY=http://proxy.example.com:8080",
		"https_proxy=https://proxy.example.com:8443",
		"PATH=/usr/bin:/bin",
		"HOME=/home/user",
	}
	out := NetDenyEnv(env)
	// 6 个代理变量全部指向死端口（含宿主缺失的也补齐）
	for _, v := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "ALL_PROXY", "all_proxy"} {
		if got := lookupEnv(out, v); got != denyProxyAddr {
			t.Errorf("%s = %q, 应指向死端口 %q", v, got, denyProxyAddr)
		}
	}
	// deny 标记
	if lookupEnv(out, "CONTEXTLOOM_SANDBOX_NET") != "deny" {
		t.Error("应追加 CONTEXTLOOM_SANDBOX_NET=deny")
	}
	if countEnv(out, "CONTEXTLOOM_SANDBOX_NET") != 1 {
		t.Errorf("CONTEXTLOOM_SANDBOX_NET 应唯一, got %d", countEnv(out, "CONTEXTLOOM_SANDBOX_NET"))
	}
	// 非代理变量保留
	if lookupEnv(out, "PATH") != "/usr/bin:/bin" || lookupEnv(out, "HOME") != "/home/user" {
		t.Error("非代理变量应原样保留")
	}
}

func TestNetDenyEnv_RemovesNoProxy(t *testing.T) {
	env := []string{"NO_PROXY=localhost,127.0.0.1", "no_proxy=internal.example.com", "PATH=/bin"}
	out := NetDenyEnv(env)
	if lookupEnv(out, "NO_PROXY") != "" || lookupEnv(out, "no_proxy") != "" {
		t.Error("NO_PROXY/no_proxy 应被删除（防豁免绕过）")
	}
	if lookupEnv(out, "PATH") != "/bin" {
		t.Error("非代理变量应保留")
	}
}

func TestNetDenyEnv_Idempotent(t *testing.T) {
	env := []string{"HTTP_PROXY=http://x:1", "NO_PROXY=localhost", "PATH=/bin", "CONTEXTLOOM_SANDBOX_NET=allow"}
	once := NetDenyEnv(env)
	twice := NetDenyEnv(once)
	if !equalEnv(once, twice) {
		t.Errorf("幂等失败:\n once =%v\n twice=%v", once, twice)
	}
}

func TestNetDenyEnv_DoesNotMutateOriginal(t *testing.T) {
	orig := []string{"HTTP_PROXY=http://x:1", "NO_PROXY=localhost", "PATH=/bin"}
	cp := append([]string(nil), orig...)
	_ = NetDenyEnv(orig)
	if !equalEnv(orig, cp) {
		t.Error("NetDenyEnv 不应修改原切片")
	}
}

func TestNetDenyEnv_OverridesExistingDeny(t *testing.T) {
	env := []string{"CONTEXTLOOM_SANDBOX_NET=allow", "HTTP_PROXY=http://x:1"}
	out := NetDenyEnv(env)
	if lookupEnv(out, "CONTEXTLOOM_SANDBOX_NET") != "deny" {
		t.Error("CONTEXTLOOM_SANDBOX_NET 应覆盖为 deny")
	}
	if countEnv(out, "CONTEXTLOOM_SANDBOX_NET") != 1 {
		t.Errorf("CONTEXTLOOM_SANDBOX_NET 应唯一, got %d", countEnv(out, "CONTEXTLOOM_SANDBOX_NET"))
	}
}
