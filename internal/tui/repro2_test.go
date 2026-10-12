package tui

import (
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/runtime"
)

func mkRealCfg(t *testing.T, dir string) *config.Config {
	return &config.Config{
		ActiveModelName: "mock",
		Models:          []config.ModelPreset{{Name: "mock", Backend: "mock", MaxContext: 4096}},
		Harness:         config.HarnessConfig{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2},
		Memory:          config.MemoryConfig{Directory: ".contextloom/memory", MaxSessions: 10},
		Plugins:         config.PluginsConfig{Directory: ".contextloom/plugins", Enabled: false},
		CLIDefaults:     config.CLIDefaultsConfig{Effort: "medium", Mode: "chat"},
		Overseer:        config.OverseerConfig{PassThreshold: 90},
		Project:         config.ProjectConfig{Root: dir, Vocab: ".contextloom/vocab.json"},
	}
}

// 模拟 TUI 完整命令链路：/session <uuid> 切换后历史消息应上屏。
func TestRepro2_SessionCommandShowsHistory(t *testing.T) {
	tmp, _ := os.MkdirTemp("", "contextloom-r2-*")
	defer os.RemoveAll(tmp)

	rt, err := runtime.New(mkRealCfg(t, tmp))
	if err != nil {
		t.Fatal(err)
	}
	rt.NewSession("会话A")
	rt.AddToHistory("user", "A的问题1")
	rt.AddToHistory("assistant", "A的回答1")
	sidA := rt.GetCurrentSessionID()

	rt.NewSession("会话B")
	rt.AddToHistory("user", "B的问题")
	sidB := rt.GetCurrentSessionID()

	m := initialModel(rt)
	if m.currentSessionID != sidB {
		t.Fatalf("currentSessionID=%q want %q", m.currentSessionID, sidB)
	}

	// 执行 /session <sidA>
	m.mode = modeCommand
	m.input = "/session " + sidA
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.currentSessionID != sidA {
		t.Fatalf("/session 后 currentSessionID=%q want %q", m.currentSessionID, sidA)
	}
	joined := ""
	for _, msg := range m.messages {
		joined += msg.Content + "|"
	}
	if !strings.Contains(joined, "A的问题1") || !strings.Contains(joined, "A的回答1") {
		t.Fatalf("/session 后 A 的历史应显示: %q", joined)
	}
	if strings.Contains(joined, "B的问题") {
		t.Fatalf("/session 后不应再显示 B 的消息: %q", joined)
	}
}
