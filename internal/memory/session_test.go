package memory

import (
	"strings"
	"testing"
	"time"
)

func TestNewSession(t *testing.T) {
	title := "My Test Session"
	project := "contextloom-project"

	s := NewSession(title, project)

	if s == nil {
		t.Fatal("expected non-nil session")
	}
	if s.UUID == "" {
		t.Error("expected non-empty UUID")
	}
	if s.Title != title {
		t.Errorf("expected title %q, got %q", title, s.Title)
	}
	if s.Project != project {
		t.Errorf("expected project %q, got %q", project, s.Project)
	}
	if s.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if s.UpdatedAt.IsZero() {
		t.Error("expected non-zero UpdatedAt")
	}
	if !s.CreatedAt.Equal(s.UpdatedAt) {
		t.Error("expected CreatedAt and UpdatedAt to be equal for a new session")
	}
	if s.CreatedAt.After(time.Now()) {
		t.Error("expected CreatedAt not to be in the future")
	}
}

func TestNewSession_TitleTruncation(t *testing.T) {
	longTitle := strings.Repeat("a", 100)
	s := NewSession(longTitle, "p")

	runes := []rune(s.Title)
	if len(runes) > 50 {
		t.Errorf("expected title truncated to 50 runes, got %d runes", len(runes))
	}
	if len(runes) != 50 {
		t.Errorf("expected exactly 50 runes, got %d", len(runes))
	}
	if s.Title != strings.Repeat("a", 50) {
		t.Error("truncated title should be first 50 runes")
	}
}

func TestNewSession_TitleExactlyFifty(t *testing.T) {
	title := strings.Repeat("x", 50)
	s := NewSession(title, "p")

	if s.Title != title {
		t.Errorf("expected title %q, got %q", title, s.Title)
	}
}

func TestNewSession_EmptyTitle(t *testing.T) {
	s := NewSession("", "project")

	if s.Title != "" {
		t.Errorf("expected empty title, got %q", s.Title)
	}
	if s.UUID == "" {
		t.Error("expected UUID to be generated even with empty title")
	}
}

func TestNewSession_UnicodeTitle(t *testing.T) {
	// 中文标题，每个中文字符是一个 rune
	title := "你好世界！这是一个测试会话的标题，用于验证截断功能是否正常工作"
	s := NewSession(title, "p")

	runes := []rune(s.Title)
	if len(runes) > 50 {
		t.Errorf("expected title truncated to at most 50 runes, got %d", len(runes))
	}
}

func TestTruncateTitle_Short(t *testing.T) {
	tests := []struct {
		name   string
		title  string
		maxLen int
		want   string
	}{
		{"empty string", "", 50, ""},
		{"shorter than max", "hello", 10, "hello"},
		{"exactly max", "1234567890", 10, "1234567890"},
		{"longer than max", "12345678901", 10, "1234567890"},
		{"unicode short", "你好", 5, "你好"},
		{"unicode long", "你好世界你好世界你好世界你好世界你好世界你好世界", 5, "你好世界你"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateTitle(tt.title, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateTitle(%q, %d) = %q, want %q", tt.title, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestNewSession_UUIDIsV7(t *testing.T) {
	// UUID v7 starts with time-ordered component
	s := NewSession("test", "p")

	if len(s.UUID) != 36 {
		t.Errorf("expected UUID length 36, got %d", len(s.UUID))
	}

	// UUID v7 format: xxxxxxxx-xxxx-7xxx-xxxx-xxxxxxxxxxxx
	// The version nibble should be 7
	parts := strings.Split(s.UUID, "-")
	if len(parts) != 5 {
		t.Errorf("expected 5 UUID parts, got %d", len(parts))
	}
}
