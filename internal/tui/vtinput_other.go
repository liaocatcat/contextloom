//go:build !windows

package tui

// enableVTInput 仅 Windows 需要（conhost 需显式启用 VT 输入解析鼠标序列）。
// macOS/Linux 终端原生支持 VT 输入，空实现零开销。
func enableVTInput() {}

// startVTInputGuard 仅 Windows 需要（PowerShell 7 下周期守护 VT 输入标志）。
// 其他平台空实现零开销。
func startVTInputGuard() {}
