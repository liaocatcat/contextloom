// Package tui 提供终端 UI 组件，包括工具执行的谨慎性确认对话框。
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/contextloom/contextloom-agent/internal/tools"
)

// ---------------------------------------------------------------------------
// 确认响应
// ---------------------------------------------------------------------------

// confirmResponse 用户确认操作的响应
type confirmResponse struct {
	approved bool
	note     string
}

// ---------------------------------------------------------------------------
// 样式
// ---------------------------------------------------------------------------

var (
	confirmBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("#FF6B6B")).
				Padding(1, 2).
				Width(60)

	confirmTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FF6B6B")).
				Bold(true).
				Width(56).
				Align(lipgloss.Center)

	confirmReasonStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#FFA500"))

	confirmLevelLabel = map[tools.ConfirmLevel]string{
		tools.ConfirmNotify: "🔔 通知",
		tools.ConfirmNormal: "⚠ 确认",
		tools.ConfirmStrong: "🚨 强确认",
		tools.ConfirmBlock:  "🛑 拒绝",
	}

	confirmPromptStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#888888")).
				Width(56).
				Align(lipgloss.Left)
)

// ---------------------------------------------------------------------------
// 确认对话框
// ---------------------------------------------------------------------------

// ConfirmDialog 确认对话框组件
type ConfirmDialog struct {
	state     confirmState
	decision  *tools.ConfirmDecision
	userInput string
}

type confirmState int

const (
	confirmStateNone confirmState = iota
	confirmStateWaiting
	confirmStateApproved
	confirmStateRejected
)

// NewConfirmDialog 创建新的确认对话框
func NewConfirmDialog() *ConfirmDialog {
	return &ConfirmDialog{
		state: confirmStateNone,
	}
}

// Show 显示确认对话框
func (d *ConfirmDialog) Show(decision *tools.ConfirmDecision) {
	d.state = confirmStateWaiting
	d.decision = decision
	d.userInput = ""
}

// Hide 隐藏确认对话框
func (d *ConfirmDialog) Hide() {
	d.state = confirmStateNone
	d.decision = nil
}

// IsVisible 检查对话框是否可见
func (d *ConfirmDialog) IsVisible() bool {
	return d.state == confirmStateWaiting
}

// IsFinished 检查确认操作是否已完成
func (d *ConfirmDialog) IsFinished() bool {
	return d.state == confirmStateApproved || d.state == confirmStateRejected
}

// Result 返回确认结果
func (d *ConfirmDialog) Result() (approved bool, note string) {
	approved = d.state == confirmStateApproved
	note = d.userInput
	return
}

// HandleKey 处理按键事件，返回是否已处理
func (d *ConfirmDialog) HandleKey(msg string) bool {
	if d.state != confirmStateWaiting {
		return false
	}

	switch msg {
	case "y", "Y":
		d.state = confirmStateApproved
		return true
	case "n", "N":
		d.state = confirmStateRejected
		return true
	case "a", "A":
		d.state = confirmStateApproved
		d.userInput = "always"
		return true
	case "v", "V":
		d.state = confirmStateApproved
		d.userInput = "edited"
		return true
	default:
		return false
	}
}

// View 返回对话框的渲染内容
func (d *ConfirmDialog) View() string {
	if d.state != confirmStateWaiting || d.decision == nil {
		return ""
	}

	dec := d.decision
	var b strings.Builder

	// 标题
	title := confirmLevelLabel[dec.Level]
	if title == "" {
		title = "⚠ 确认"
	}
	b.WriteString(confirmTitleStyle.Render(title))
	b.WriteString("\n\n")

	// 工具信息
	b.WriteString(fmt.Sprintf("工具: %s\n", dec.ToolName))
	if dec.Params != nil {
		for k, v := range dec.Params {
			val := fmt.Sprintf("%v", v)
			if len(val) > 60 {
				val = val[:57] + "..."
			}
			b.WriteString(fmt.Sprintf("  %s = %s\n", k, val))
		}
	}
	b.WriteString("\n")

	// 风险原因
	if len(dec.Reasons) > 0 {
		b.WriteString("风险原因:\n")
		for _, reason := range dec.Reasons {
			b.WriteString(confirmReasonStyle.Render("  • " + reason))
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// 操作提示
	switch dec.Level {
	case tools.ConfirmBlock:
		b.WriteString(confirmPromptStyle.Render("此操作风险极高，建议拒绝。"))
	case tools.ConfirmStrong:
		b.WriteString(confirmPromptStyle.Render("请确认是否执行此高风险操作？"))
	case tools.ConfirmNormal:
		b.WriteString(confirmPromptStyle.Render("是否执行此操作？"))
	case tools.ConfirmNotify:
		b.WriteString(confirmPromptStyle.Render("即将执行此操作。"))
	}
	b.WriteString("\n\n")

	// 选项
	b.WriteString(confirmPromptStyle.Render(
		"  [y] 确认执行    [n] 拒绝    [a] 总是允许    [v] 修改参数",
	))

	return confirmBorderStyle.Render(b.String())
}

// ---------------------------------------------------------------------------
// 创建确认回调函数
// ---------------------------------------------------------------------------

// MakeConfirmFunc 创建一个确认回调函数，用于 tools.Registry.SetConfirmFunc。
// 返回 ConfirmFunc、决策接收 channel（TUI 从中读取确认请求）和响应发送 channel（TUI 通过它发送结果）。
//
// TUI 集成示例:
//
//	confirmFn, reqCh, respCh := tui.MakeConfirmFunc()
//	registry.SetConfirmFunc(confirmFn)
//	// 在 TUI 的 Update 中监听 reqCh
//	select {
//	case decision := <-reqCh:
//	    dialog.Show(decision)
//	    // 等待用户按键后，通过 respCh 发送响应
//	    respCh <- confirmResponse{approved: true}
//	}
func MakeConfirmFunc() (tools.ConfirmFunc, <-chan *tools.ConfirmDecision, chan<- confirmResponse) {
	reqCh := make(chan *tools.ConfirmDecision, 1)
	respCh := make(chan confirmResponse, 1)

	fn := func(ctx context.Context, decision *tools.ConfirmDecision) (bool, string) {
		// 发送确认请求到 TUI
		select {
		case reqCh <- decision:
		case <-ctx.Done():
			return false, "context cancelled"
		}

		// 等待 TUI 响应
		select {
		case resp := <-respCh:
			return resp.approved, resp.note
		case <-ctx.Done():
			return false, "context cancelled"
		}
	}

	return fn, reqCh, respCh
}
