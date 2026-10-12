// Package audit 提供文件操作审计日志（JSONL 按日轮转，零依赖），
// 对标企业治理"审计追踪"的轻量落地（用户排除鉴权，不排除审计）。
//
// 记录点：write_file / replace_in_file / exec_command / delete 等敏感操作，
// 由 tools 层在每次执行后调用（Auditor 注入，nil 安全）。
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Action 审计动作类型。
type Action string

const (
	ActionWriteFile   Action = "write_file"
	ActionReplaceFile Action = "replace_in_file"
	ActionDelete      Action = "delete"
	ActionExecCommand Action = "exec_command"
	ActionReadFile    Action = "read_file"
	ActionListFiles   Action = "list_files"
	ActionToolCall    Action = "tool_call" // 其他工具调用（兜底）
)

// Entry 单条审计记录（JSON 行）。
type Entry struct {
	TS      time.Time `json:"ts"`
	Session string    `json:"session,omitempty"`
	Action  Action    `json:"action"`
	Tool    string    `json:"tool,omitempty"`
	Path    string    `json:"path,omitempty"`
	OK      bool      `json:"ok"`
	Ms      int64     `json:"ms,omitempty"`
	Err     string    `json:"err,omitempty"`
	Detail  string    `json:"detail,omitempty"`
}

// Auditor 文件操作审计器。
type Auditor struct {
	mu      sync.Mutex
	dir     string
	enabled bool
	file    *os.File
	day     string
	maxKeep int // 保留最近 N 个日志文件（0=不清理）
}

// New 创建审计器。enabled=false 时所有操作空操作。
// dir 为审计目录（如 .contextloom/audit/）。
func New(dir string, enabled bool) (*Auditor, error) {
	a := &Auditor{dir: dir, enabled: enabled, maxKeep: 30}
	if !enabled {
		return a, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建审计目录失败: %w", err)
	}
	if err := a.rotateLocked(time.Now()); err != nil {
		return nil, err
	}
	return a, nil
}

// rotateLocked 确保 file 对应当天文件（调用方持锁）。
func (a *Auditor) rotateLocked(now time.Time) error {
	day := now.Format("20060102")
	if a.file != nil && a.day == day {
		return nil
	}
	if a.file != nil {
		a.file.Close()
		a.file = nil
	}
	f, err := os.OpenFile(filepath.Join(a.dir, "audit-"+day+".jsonl"),
		os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	a.file = f
	a.day = day
	a.pruneLocked()
	return nil
}

// pruneLocked 清理超过 maxKeep 的旧日志文件。
func (a *Auditor) pruneLocked() {
	if a.maxKeep <= 0 {
		return
	}
	matches, _ := filepath.Glob(filepath.Join(a.dir, "audit-*.jsonl"))
	if len(matches) <= a.maxKeep {
		return
	}
	sort.Strings(matches)
	for _, f := range matches[:len(matches)-a.maxKeep] {
		os.Remove(f) //nolint:errcheck // 清理失败不阻塞
	}
}

// Record 追加一条审计记录（失败静默，审计不应影响主流程）。
func (a *Auditor) Record(e Entry) {
	if !a.enabled {
		return
	}
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.rotateLocked(e.TS); err != nil {
		return
	}
	a.file.Write(append(data, '\n')) //nolint:errcheck
}

// Recent 读取最近 n 条记录（跨日合并，时间倒序）。
func (a *Auditor) Recent(n int) []Entry {
	if !a.enabled || n <= 0 {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	matches, _ := filepath.Glob(filepath.Join(a.dir, "audit-*.jsonl"))
	sort.Strings(matches)

	var out []Entry
	for i := len(matches) - 1; i >= 0 && len(out) < n; i-- {
		data, err := os.ReadFile(matches[i])
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		for j := len(lines) - 1; j >= 0 && len(out) < n; j-- {
			if strings.TrimSpace(lines[j]) == "" {
				continue
			}
			var e Entry
			if json.Unmarshal([]byte(lines[j]), &e) == nil {
				out = append(out, e)
			}
		}
	}
	return out
}

// Close 关闭当前文件。
func (a *Auditor) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file != nil {
		err := a.file.Close()
		a.file = nil
		return err
	}
	return nil
}
