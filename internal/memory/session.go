package memory

import (
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
	"github.com/google/uuid"
)

func NewSession(title, project string) *types.Session {
	now := time.Now()
	return &types.Session{
		UUID:      uuid.Must(uuid.NewV7()).String(),
		Title:     truncateTitle(title, 50),
		Project:   project,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func truncateTitle(title string, maxLen int) string {
	runes := []rune(title)
	if len(runes) <= maxLen {
		return title
	}
	return string(runes[:maxLen])
}
