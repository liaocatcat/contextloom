package tui

// Windows 控制台输入模式标志（与平台无关的位定义，供 vtinput_windows.go
// 与单元测试共用）。
const (
	// enableVirtualTerminalInput：启用 VT 输入序列解析。
	// 【注意】bubbletea v1.1.0 在 Windows 下不走 VT/SGR 路径——鼠标事件
	// 完全依赖【原生 INPUT_RECORD】（readConInputs → ReadNConsoleInputs），
	// 键盘事件也来自原生 KeyEventRecord。因此本守护【绝不】主动添加该标志：
	// VT input 在位时 conhost 可能把鼠标事件以 SGR 文本输出（"[<65;72;17M"
	// 乱码进输入框，三重防御兜底）；bubbletea 的 newInputReader 已用
	// AddInputModes(0,...) 重建模式（0x0098，无 VT input），保持现状即可。
	enableVirtualTerminalInput = 0x0004
	// enableMouseInput：启用后 conhost 把鼠标事件作为 MouseEventRecord 排入
	// 输入队列——【bubbletea readConInputs 读取鼠标（点击/滚轮）的唯一途径】。
	// ★ 此标志【绝不能清除】：旧实现基于"bubbletea 依赖 SGR 序列"的错误前提
	//   清除它，导致 Windows 下点击/滚轮事件完全收不到——"点击历史记录后
	//   滚轮/上下键仍作用于输入框"（focusHistory 永远 false）的直接根因。
	enableMouseInput = 0x0010
	// enableWindowInput：窗口大小变化事件（bubbletea 的 WindowSizeMsg 依赖）。
	enableWindowInput = 0x0008
	// enableExtendedFlags：允许控制 QuickEdit 等扩展标志。
	enableExtendedFlags = 0x0080
	// enableQuickEditMode：启用后 conhost 把鼠标点击拦截为拖选起点，
	// 与 TUI 鼠标捕获冲突（点击收不到）。bubbletea 已清除它，本守护持续确保。
	enableQuickEditMode = 0x0040
)

// desiredInputMode 返回守护后的控制台输入模式（纯位运算，便于单元测试）：
//   - 确保 bubbletea 原生输入所需位在位：
//     ENABLE_MOUSE_INPUT（鼠标 MouseEventRecord，点击/滚轮必需）
//     | ENABLE_WINDOW_INPUT（WindowSizeMsg）| ENABLE_EXTENDED_FLAGS；
//   - 清除 ENABLE_QUICK_EDIT_MODE（否则鼠标点击被拦截为拖选）；
//   - 【绝不】添加/清除 ENABLE_VIRTUAL_TERMINAL_INPUT——原生路径不需要，
//     且添加后 conhost 可能输出 SGR 鼠标文本乱码；保持终端现状即可。
func desiredInputMode(current uint32) uint32 {
	current |= enableMouseInput | enableWindowInput | enableExtendedFlags
	current &^= enableQuickEditMode
	return current
}
