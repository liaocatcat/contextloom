package tui

import (
	"strings"

	bubbletea "github.com/charmbracelet/bubbletea"
)

func fuzzyMatch(query, target string) bool {
	q := []rune(strings.ToLower(query))
	t := []rune(strings.ToLower(target))
	qi := 0
	for _, tc := range t {
		if qi < len(q) && tc == q[qi] {
			qi++
		}
	}
	return qi == len(q)
}

// rebuildFiltered 按当前 skillSearch 重建过滤索引（使用模糊匹配）。
func (m *model) rebuildFiltered() {
	m.skillFiltered = nil
	if m.skillSearch == "" {
		for i := range m.skillAll {
			m.skillFiltered = append(m.skillFiltered, i)
		}
		return
	}
	for i, name := range m.skillAll {
		if fuzzyMatch(m.skillSearch, name) {
			m.skillFiltered = append(m.skillFiltered, i)
		}
	}
	if m.skillCursor >= len(m.skillFiltered) {
		m.skillCursor = 0
	}
}

// handleSkillSelect 处理技能选择模式的按键交互。
func (m *model) handleSkillSelect(msg bubbletea.KeyMsg) (bubbletea.Model, bubbletea.Cmd) {
	s := msg.String()

	switch s {
	case "esc":
		// ESC 退出技能选择，不保存
		m.skillSearch = ""
		m.skillAll = nil
		m.skillSelected = nil
		m.skillFiltered = nil
		m.InitConfirmDialog()
		m.mode = modeChat
		return m, nil

	case "enter":
		// 保存选中技能并持久化
		var selected []string
		for i, sel := range m.skillSelected {
			if sel && i < len(m.skillAll) {
				selected = append(selected, m.skillAll[i])
			}
		}
		if m.rt != nil {
			m.rt.SetActiveSkills(selected)
		}
		summary := "技能已更新: 未选中技能"
		if len(selected) > 0 {
			summary = "技能已更新: " + strings.Join(selected, ", ")
		}
		m.appendMsg(sysMsg(summary))
		m.skillSearch = ""
		m.skillAll = nil
		m.skillSelected = nil
		m.skillFiltered = nil
		m.InitConfirmDialog()
		m.mode = modeChat
		return m, nil

	case "up":
		if len(m.skillFiltered) > 0 {
			m.skillCursor--
			if m.skillCursor < 0 {
				m.skillCursor = len(m.skillFiltered) - 1
			}
		}

	case "down":
		if len(m.skillFiltered) > 0 {
			m.skillCursor = (m.skillCursor + 1) % len(m.skillFiltered)
		}

	case " ":
		// 空格：切换当前项的选中状态
		if len(m.skillFiltered) > 0 && m.skillCursor < len(m.skillFiltered) {
			idx := m.skillFiltered[m.skillCursor]
			if idx < len(m.skillSelected) {
				m.skillSelected[idx] = !m.skillSelected[idx]
			}
		}

	case "backspace", "ctrl+h":
		r := []rune(m.skillSearch)
		if len(r) > 0 {
			m.skillSearch = string(r[:len(r)-1])
			m.rebuildFiltered()
		}

	default:
		if len(msg.Runes) > 0 {
			m.skillSearch += string(msg.Runes)
			m.rebuildFiltered()
		}
	}

	return m, nil
}

// ── View ──
