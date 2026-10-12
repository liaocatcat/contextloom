package envclean

import (
	"os"
	"strings"
	"testing"
)

// TestStripMallocDebug_RemovesFamily 验证 MallocStackLogging 家族全被清除，
// 其他环境变量原样保留。
func TestStripMallocDebug_RemovesFamily(t *testing.T) {
	// 注入完整家族 + 无关变量
	_ = os.Setenv("MallocStackLogging", "1")
	_ = os.Setenv("MallocStackLoggingLite", "1")
	_ = os.Setenv("MallocStackLoggingNoCompact", "1")
	_ = os.Setenv("MallocStackLoggingDirectory", "/tmp/msl")
	_ = os.Setenv("MallocScribble", "1") // 同类但非栈日志家族，不误删
	_ = os.Setenv("CONTEXTLOOM_KEEP_ME", "keep")
	defer os.Unsetenv("MallocStackLogging")
	defer os.Unsetenv("MallocStackLoggingLite")
	defer os.Unsetenv("MallocStackLoggingNoCompact")
	defer os.Unsetenv("MallocStackLoggingDirectory")
	defer os.Unsetenv("MallocScribble")
	defer os.Unsetenv("CONTEXTLOOM_KEEP_ME")

	StripMallocDebug()

	for _, name := range []string{
		"MallocStackLogging",
		"MallocStackLoggingLite",
		"MallocStackLoggingNoCompact",
		"MallocStackLoggingDirectory",
	} {
		if v, ok := os.LookupEnv(name); ok {
			t.Errorf("%s 应被清除，仍存在=%q", name, v)
		}
	}
	// 无关变量不受影响
	if v, _ := os.LookupEnv("CONTEXTLOOM_KEEP_ME"); v != "keep" {
		t.Errorf("无关变量被误删: %q", v)
	}
	if v, _ := os.LookupEnv("MallocScribble"); v != "1" {
		t.Errorf("非栈日志家族变量被误删: %q", v)
	}
}

// TestStripMallocDebug_NoFamilyNoop 验证无家族变量时零副作用（幂等）。
func TestStripMallocDebug_NoFamilyNoop(t *testing.T) {
	_ = os.Unsetenv("MallocStackLogging")
	before := os.Environ()
	StripMallocDebug()
	after := os.Environ()
	if len(before) != len(after) {
		t.Errorf("无家族变量时环境不应变化: %d → %d", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("环境被意外修改: %q → %q", before[i], after[i])
		}
	}
}

// TestStripMallocDebug_Idempotent 二次调用结果一致。
func TestStripMallocDebug_Idempotent(t *testing.T) {
	_ = os.Setenv("MallocStackLogging", "1")
	defer os.Unsetenv("MallocStackLogging")
	StripMallocDebug()
	first := strings.Join(os.Environ(), "\n")
	StripMallocDebug()
	second := strings.Join(os.Environ(), "\n")
	if first != second {
		t.Errorf("非幂等: 二次调用后环境变化")
	}
}
