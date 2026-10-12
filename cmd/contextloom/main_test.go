package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
)

// TestParseFlags 覆盖各参数解析与默认值
func TestParseFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want options
	}{
		{
			name: "无参数使用默认值",
			args: []string{},
			want: options{acpPort: 20250},
		},
		{
			name: "model 与 resume",
			args: []string{"--model", "flash-model", "--resume"},
			want: options{modelID: "flash-model", resume: true, acpPort: 20250},
		},
		{
			name: "memory 系列参数",
			args: []string{"--memory-load", "abc-123", "--memory-list", "--memory-delete", "def-456"},
			want: options{memoryLoad: "abc-123", memoryList: true, memoryDelete: "def-456", acpPort: 20250},
		},
		{
			name: "acp 模式与端口覆盖",
			args: []string{"--acp", "--acp-port", "30000"},
			want: options{acpOnly: true, acpPort: 30000},
		},
		{
			name: "mcp-server 模式",
			args: []string{"--mcp-server"},
			want: options{mcpServer: true, acpPort: 20250},
		},
		{
			name: "config 路径与 version",
			args: []string{"--config", "config.local.yaml", "--version"},
			want: options{configPath: "config.local.yaml", showVersion: true, acpPort: 20250},
		},
		{
			name: "短横线单参数形式",
			args: []string{"-acp", "-acp-port=8080"},
			want: options{acpOnly: true, acpPort: 8080},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseFlags(tt.args)
			if err != nil {
				t.Fatalf("parseFlags(%v) 返回错误: %v", tt.args, err)
			}
			if *got != tt.want {
				t.Errorf("parseFlags(%v) = %+v, 期望 %+v", tt.args, *got, tt.want)
			}
		})
	}
}

// TestParseFlagsErrors 非法参数应返回错误而非 panic
func TestParseFlagsErrors(t *testing.T) {
	bad := [][]string{
		{"--unknown-flag"},
		{"--acp-port", "not-a-number"},
		{"--model"}, // 缺值
	}
	for _, args := range bad {
		if _, err := parseFlags(args); err == nil {
			t.Errorf("parseFlags(%v) 期望报错，实际无错误", args)
		}
	}
}

// TestParseFlagsHelp 内置 -h 不应报错（flag 包语义：打印 usage 返回 ErrHelp）
func TestParseFlagsHelp(t *testing.T) {
	_, err := parseFlags([]string{"-h"})
	if err == nil {
		t.Fatal("期望 -h 返回 ErrHelp")
	}
	if !strings.Contains(err.Error(), "help") {
		t.Errorf("期望 ErrHelp 包含 help，实际: %v", err)
	}
}

// TestOverrideProjectRootFromCWD：配置指定显式 project.root 时，
// overrideProjectRootFromCWD 一律锚定到启动 CWD（bug3 根因：二进制目录
// config 指定项目目录导致 TUI/CLI 记忆命令加载对话失败）。
func TestOverrideProjectRootFromCWD(t *testing.T) {
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	proj := t.TempDir()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}
	// 构造一个显式 root 指向别处的配置（模拟二进制目录 config 指定项目目录）
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "config.yaml")
	other := t.TempDir()
	if err := os.WriteFile(cfgPath, []byte("project:\n  root: "+other+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Project.Root != other {
		t.Fatalf("前置：配置显式 root 应被尊重, got %q want %q", cfg.Project.Root, other)
	}
	overrideProjectRootFromCWD(cfg)
	want, _ := filepath.Abs(proj)
	if real, err := filepath.EvalSymlinks(want); err == nil {
		want = real
	}
	if cfg.Project.Root != want {
		t.Errorf("override 后项目根应锚定 CWD %q, got %q", want, cfg.Project.Root)
	}
}
