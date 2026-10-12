package tui

import "testing"

// ── 控制台输入模式守护测试 ──
// 背景：bubbletea v1.1.0 在 Windows 下鼠标事件依赖【原生 INPUT_RECORD】
// （MouseEventRecord），其产生需要控制台 ENABLE_MOUSE_INPUT 标志。
// 旧实现（vtinput_windows.go）基于"bubbletea 依赖 SGR 序列"的错误前提
// 【清除 ENABLE_MOUSE_INPUT】→ conhost 不再产生鼠标事件 → bubbletea
// 收不到点击/滚轮 → 点击历史区无法激活焦点（focusHistory 永远 false），
// ↑↓ 永远回填输入历史——"点击历史后滚轮/上下键仍作用于输入框"的直接根因。
// 修复：desiredInputMode 只守护必需位（MOUSE|WINDOW|EXTENDED）、清除
// QuickEdit，绝不添加/清除 ENABLE_VIRTUAL_TERMINAL_INPUT。

// bubbletea newInputReader 实际设定的模式（AddInputModes(0, MOUSE|WINDOW|EXTENDED)）。
const btNativeMode = enableMouseInput | enableWindowInput | enableExtendedFlags // 0x0098

// 已达标模式（bubbletea 设定后）→ 守护幂等，分毫不动。
func TestDesiredInputMode_IdempotentOnNativeMode(t *testing.T) {
	if got := desiredInputMode(btNativeMode); got != btNativeMode {
		t.Fatalf("已达标模式被改动: got=%#x want=%#x", got, btNativeMode)
	}
}

// 关键回归：模式中即使存在 ENABLE_MOUSE_INPUT 也【必须保留】——
// 旧实现曾清除它（鼠标事件从此收不到），这是本次修复的核心。
func TestDesiredInputMode_NeverClearsMouseInput(t *testing.T) {
	// 各种输入模式（含 PSReadLine 可能残留的 VT input / 其他位）
	for _, mode := range []uint32{
		0, btNativeMode, btNativeMode | enableVirtualTerminalInput,
		0x0004, // 仅 VT input（PSReadLine 重置后）
		enableQuickEditMode | enableVirtualTerminalInput,
		0xFFFF,
	} {
		got := desiredInputMode(mode)
		if got&enableMouseInput == 0 {
			t.Fatalf("desiredInputMode(%#x)=%#x 丢失 ENABLE_MOUSE_INPUT——鼠标将完全失效！", mode, got)
		}
		if got&enableWindowInput == 0 {
			t.Fatalf("desiredInputMode(%#x)=%#x 丢失 ENABLE_WINDOW_INPUT", mode, got)
		}
		if got&enableExtendedFlags == 0 {
			t.Fatalf("desiredInputMode(%#x)=%#x 丢失 ENABLE_EXTENDED_FLAGS", mode, got)
		}
	}
}

// QuickEdit 必须清除（否则鼠标点击被拦截为拖选起点）。
func TestDesiredInputMode_ClearsQuickEdit(t *testing.T) {
	got := desiredInputMode(btNativeMode | enableQuickEditMode)
	if got&enableQuickEditMode != 0 {
		t.Fatalf("desiredInputMode 未清除 ENABLE_QUICK_EDIT_MODE: %#x", got)
	}
	// 清除 QuickEdit 的同时其余位保持不变
	if got != btNativeMode {
		t.Fatalf("清除 QuickEdit 不应影响其他位: got=%#x want=%#x", got, btNativeMode)
	}
}

// VT input 标志【既不添加也不清除】——原生路径不需要，添加后 conhost
// 可能输出 SGR 鼠标文本（输入框乱码）；保持终端现状。
func TestDesiredInputMode_LeavesVtInputUntouched(t *testing.T) {
	// 不含 VT input → 守护后仍不含
	if got := desiredInputMode(btNativeMode); got&enableVirtualTerminalInput != 0 {
		t.Fatalf("不应主动添加 ENABLE_VIRTUAL_TERMINAL_INPUT: %#x", got)
	}
	// 已含 VT input（PSReadLine/终端残留）→ 守护后保留（不强行清除，避免与
	// 依赖 VT 输入的宿主环境冲突；SGR 乱码由 handleKey/appInp 三重防御兜底）
	withVt := uint32(btNativeMode | enableVirtualTerminalInput)
	if got := desiredInputMode(withVt); got&enableVirtualTerminalInput == 0 {
		t.Fatalf("不应清除已有的 ENABLE_VIRTUAL_TERMINAL_INPUT: %#x", got)
	}
}

// PSReadLine 重置场景：模式被改为仅 QuickEdit + VT input（无 MOUSE）→ 守护恢复必需位。
func TestDesiredInputMode_PSReadLineResetRestored(t *testing.T) {
	broken := uint32(enableQuickEditMode | enableVirtualTerminalInput)
	got := desiredInputMode(broken)
	want := uint32(btNativeMode | enableVirtualTerminalInput) // 必需位补齐 + VT 原样保留 + QuickEdit 清除
	if got != want {
		t.Fatalf("PSReadLine 重置后守护结果不符: got=%#x want=%#x", got, want)
	}
}
