package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestFindContextLoomInDir_PlatformPriority 同目录多平台产物并存时优先当前平台产物。
func TestFindContextLoomInDir_PlatformPriority(t *testing.T) {
	plat := runtime.GOOS + "-" + runtime.GOARCH
	if _, _, ok := splitPlatformSuffix("contextloom-" + plat); !ok {
		t.Skip("当前平台不在打包白名单内")
	}
	other := "contextloom-linux-amd64"
	if runtime.GOOS == "linux" && runtime.GOARCH == "amd64" {
		other = "contextloom-darwin-arm64"
	}
	dir := t.TempDir()
	for _, name := range []string{other, "contextloom-" + plat} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := findContextLoomInDir(dir)
	want := filepath.Join(dir, "contextloom-"+plat)
	if !ok || got != want {
		t.Fatalf("同目录多平台产物应优先当前平台 %s，got %s/%v", want, got, ok)
	}
}

// TestFindContextLoomInDir_FallbackAnyPlatform 目录里只有其他平台产物（当前平台无
// 精确产物）时仍应兜底命中——"包里带了 backend 就能拉起"，交由 backend 自身报架构错。
func TestFindContextLoomInDir_FallbackAnyPlatform(t *testing.T) {
	other := "contextloom-darwin-arm64"
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
		other = "contextloom-linux-amd64"
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, other), []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := findContextLoomInDir(dir)
	if !ok || filepath.Base(got) != other {
		t.Fatalf("应兜底命中 %s，got %s/%v", other, got, ok)
	}
}
