package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSandbox_EnabledCreatesShadowFS：config sandbox.enabled=true 时
// 生产链路真实创建影子文件系统，CloseSandbox 按 discard_on_exit 清理。
func TestSandbox_EnabledCreatesShadowFS(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-sandbox-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir)
	cfg.Sandbox.Enabled = true
	cfg.Sandbox.DiscardOnExit = true
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}

	if rt.sbFS == nil {
		t.Fatal("沙箱未激活（Enabled=true 但 sbFS 为 nil）")
	}
	mirror := rt.sbFS.Root()
	if !strings.Contains(mirror, filepath.Join(".contextloom", "sandbox")) {
		t.Errorf("沙箱镜像路径异常: %s", mirror)
	}
	if _, err := os.Stat(mirror); err != nil {
		t.Errorf("沙箱镜像目录未创建: %v", err)
	}
	if st := rt.SandboxStatus(); !strings.Contains(st, "已启用") {
		t.Errorf("SandboxStatus 异常: %s", st)
	}

	// 退出：discard_on_exit=true → 镜像目录删除、sbFS 置空
	rt.CloseSandbox()
	if rt.sbFS != nil {
		t.Error("CloseSandbox 后 sbFS 未置空")
	}
	if _, err := os.Stat(mirror); !os.IsNotExist(err) {
		t.Errorf("discard_on_exit=true 但镜像目录仍存在: %v", err)
	}
	// 幂等
	rt.CloseSandbox()
}

// TestSandbox_DisabledNoShadowFS：未启用时不创建镜像、命令正常提示。
func TestSandbox_DisabledNoShadowFS(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-sandbox-off-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := testConfig(tmpDir) // Enabled 默认 false
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	if rt.sbFS != nil {
		t.Fatal("未启用沙箱但 sbFS 非 nil")
	}
	if st := rt.SandboxStatus(); !strings.Contains(st, "未启用") {
		t.Errorf("SandboxStatus 异常: %s", st)
	}
	if _, err := rt.SandboxCommit(); err == nil {
		t.Error("未启用时 SandboxCommit 应报错")
	}
	rt.CloseSandbox()
}
