package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/contextloom/contextloom-agent/internal/tools"
)

// ---------------------------------------------------------------------------
// 确认对话框集成到 TUI 模型
// ---------------------------------------------------------------------------

// SetConfirmChannels 设置确认通道，供 TUI 模型使用。
// 在 main.go 中调用 MakeConfirmFunc() 获取通道后，通过此方法注入。
func (m *model) SetConfirmChannels(reqCh <-chan *tools.ConfirmDecision, respCh chan<- confirmResponse) {
	m.confirmReqCh = reqCh
	m.confirmRespCh = respCh
	m.confirmDialog = NewConfirmDialog()
}

// InitConfirmDialog 初始化确认对话框（在 initialModel 中调用）
func (m *model) InitConfirmDialog() {
	m.confirmDialog = NewConfirmDialog()
}

// HandleConfirmKey 处理确认对话框的按键事件。
// 当对话框可见时，所有按键先交给对话框处理。
// 返回 true 表示按键已被对话框消费。
func (m *model) HandleConfirmKey(key string) bool {
	if m.confirmDialog == nil || !m.confirmDialog.IsVisible() {
		return false
	}

	handled := m.confirmDialog.HandleKey(key)
	if handled && m.confirmDialog.IsFinished() {
		// 用户完成了确认操作，发送响应
		approved, note := m.confirmDialog.Result()
		if m.confirmRespCh != nil {
			m.confirmRespCh <- confirmResponse{
				approved: approved,
				note:     note,
			}
		}
		m.confirmDialog.Hide()

		// 在消息列表中添加通知
		if approved {
			noteStr := "已批准"
			if note == "always" {
				noteStr = "已批准（总是允许）"
			}
			m.appendMsg(sysMsg("✅ " + noteStr))
		} else {
			m.appendMsg(sysMsg("⛔ 已拒绝执行"))
		}
	}
	return handled
}

// CheckConfirmRequest 检查是否有新的确认请求。
// 在 Update 方法中调用，收到请求后显示对话框。
func (m *model) CheckConfirmRequest() bool {
	if m.confirmReqCh == nil || m.confirmDialog == nil {
		return false
	}

	select {
	case decision := <-m.confirmReqCh:
		m.confirmDialog.Show(decision)
		// 在消息列表中添加通知
		reasons := decision.ReasonsString()
		msg := "⚠ 需要确认: " + decision.ToolName
		if reasons != "无风险" {
			msg += " (" + reasons + ")"
		}
		m.appendMsg(sysMsg(msg))
		return true
	default:
		return false
	}
}

// RenderConfirmDialog 渲染确认对话框（覆盖在主视图上）
func (m *model) RenderConfirmDialog() string {
	if m.confirmDialog == nil || !m.confirmDialog.IsVisible() {
		return ""
	}

	// 使用半透明背景遮罩效果
	dialogContent := m.confirmDialog.View()
	if dialogContent == "" {
		return ""
	}

	// 对话框居中显示
	lines := strings.Split(dialogContent, "\n")
	maxWidth := 0
	for _, line := range lines {
		lineWidth := len([]rune(line))
		if lineWidth > maxWidth {
			maxWidth = lineWidth
		}
	}
	if maxWidth < 60 {
		maxWidth = 60
	}

	// 创建遮罩
	overlay := "\n"
	overlay += lipgloss.NewStyle().
		Width(m.width).
		Foreground(lipgloss.Color("#FF6B6B")).
		Align(lipgloss.Center).
		Render("┌" + strings.Repeat("─", maxWidth-2) + "┐")
	overlay += "\n"
	for _, line := range lines {
		padded := line + strings.Repeat(" ", maxWidth-len([]rune(line)))
		overlay += lipgloss.NewStyle().
			Width(m.width).
			Align(lipgloss.Center).
			Render("│ " + padded + " │")
		overlay += "\n"
	}
	overlay += lipgloss.NewStyle().
		Width(m.width).
		Foreground(lipgloss.Color("#FF6B6B")).
		Align(lipgloss.Center).
		Render("└" + strings.Repeat("─", maxWidth-2) + "┘")
	overlay += "\n"
	overlay += lipgloss.NewStyle().
		Width(m.width).
		Foreground(lipgloss.Color("#888888")).
		Align(lipgloss.Center).
		Render("  [y] 确认执行    [n] 拒绝    [a] 总是允许    [v] 修改参数  ")

	return overlay
}
