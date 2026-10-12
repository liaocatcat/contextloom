//go:build !windows

package gui

import "testing"

// requirePTYEnv Unix 下 PTY 无桌面限制，直接通过。
func requirePTYEnv(t *testing.T) {
	t.Helper()
}
