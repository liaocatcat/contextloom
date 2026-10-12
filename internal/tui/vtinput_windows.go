//go:build windows

package tui

import (
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Windows 控制台模式标志（位定义与 desiredInputMode 见 vtinput_mode.go，
// 供单元测试跨平台复用）。
const (
	// th32csSnapProcess：CreateToolhelp32Snapshot 枚举所有进程。
	th32csSnapProcess = 0x00000002
	// maxPath：进程可执行文件路径缓冲区（MAX_PATH）。
	maxPath = 260
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetStdHandle         = kernel32.NewProc("GetStdHandle")
	procGetConsoleMode       = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode       = kernel32.NewProc("SetConsoleMode")
	procCloseHandle          = kernel32.NewProc("CloseHandle")
	procCreateToolhelp32Snap = kernel32.NewProc("CreateToolhelp32Snapshot")
	procProcess32First       = kernel32.NewProc("Process32FirstW")
	procProcess32Next        = kernel32.NewProc("Process32NextW")
)

// processEntry32 对应 PROCESSENTRY32W（Process32FirstW/NextW 使用）。
type processEntry32 struct {
	Size            uint32
	Usage           uint32
	ProcessID       uint32
	DefaultHeapID   uintptr
	ModuleID        uint32
	Threads         uint32
	ParentProcessID uint32
	PriClassBase    int32
	Flags           uint32
	ExeFile         [maxPath]uint16
}

// setVTInputMode 读取当前控制台模式并应用 desiredInputMode 守护。
// 返回是否成功。
func setVTInputMode() bool {
	// GetStdHandle(STD_INPUT_HANDLE = -10)
	h, _, _ := procGetStdHandle.Call(uintptr(^uintptr(9)))
	if h == 0 || h == ^uintptr(0) {
		return false
	}
	var mode uint32
	r1, _, _ := procGetConsoleMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	if r1 == 0 {
		return false
	}
	want := desiredInputMode(mode)
	if want == mode {
		return true // 已达标，零开销
	}
	r2, _, _ := procSetConsoleMode.Call(h, uintptr(want))
	return r2 != 0
}

// enableVTInput 在 Windows 下守护 bubbletea 所需的控制台输入模式。
//
// 必须在 bubbletea 的 MakeRaw / newInputReader（prepareConsole）完成之后执行
// （prepareConsole 会把模式重建为 ENABLE_MOUSE_INPUT|ENABLE_WINDOW_INPUT|
// ENABLE_EXTENDED_FLAGS，并保存原始模式），因此异步启动并重试数次。
//
// 历史背景（勿回退）：旧实现基于"bubbletea 在 Windows 依赖 VT/SGR 鼠标序列"
// 的错误前提，主动添加 ENABLE_VIRTUAL_TERMINAL_INPUT 并【清除 ENABLE_MOUSE_INPUT】，
// 结果 conhost 不再产生 MouseEventRecord，bubbletea readConInputs 收不到任何
// 鼠标事件 → 点击/滚轮完全失效，焦点机制永远无法激活（"点击历史后 ↑↓/滚轮
// 仍作用于输入框"）。现在只守护必需位，绝不碰 VT input。
func enableVTInput() {
	for i := 0; i < 5; i++ {
		time.Sleep(150 * time.Millisecond)
		if setVTInputMode() {
			return
		}
	}
}

// startVTInputGuard 启动控制台模式守护，按宿主环境选择策略：
//
//   - cmd / Windows PowerShell 5.1：一次性守护即可（enableVTInput）。
//   - PowerShell 7（pwsh）：PSReadLine 在外部程序运行初期会重置控制台
//     模式（可能清除 ENABLE_MOUSE_INPUT 或重新启用 QuickEdit），导致
//     鼠标在 TUI 启动后失效。因此 pwsh 下改用【周期性守护】：每 1s
//     检查并重新应用 desiredInputMode（幂等，已达标时零开销），
//     直到 TUI 退出（进程退出自然终止 goroutine）。
//
// 非 Windows 平台为空实现（vtinput_other.go），零开销。
func startVTInputGuard() {
	if isPowerShell7() {
		go func() {
			for {
				time.Sleep(time.Second)
				setVTInputMode()
			}
		}()
		return
	}
	go enableVTInput()
}

// isPowerShell7 判断当前是否运行在 PowerShell 7（pwsh）中。
// 双重判定（任一命中即 true）：
//  1. 环境变量 PSModulePath 含 \PowerShell\7\（快速路径，
//     PowerShell 7 安装时必然设置，Windows PowerShell 5.1 不含）。
//  2. 父进程链（最多 2 层）可执行文件名含 "pwsh"（准确路径，
//     覆盖通过脚本/包装器间接启动的场景）。
func isPowerShell7() bool {
	if ps := os.Getenv("PSModulePath"); ps != "" {
		if strings.Contains(strings.ToLower(ps), `\powershell\7`) {
			return true
		}
	}
	for depth := 0; depth <= 1; depth++ {
		name := parentProcessName(depth)
		if strings.Contains(strings.ToLower(name), "pwsh") {
			return true
		}
	}
	return false
}

// parentProcessName 返回第 depth 层父进程的可执行文件名（0 = 直接父进程；
// 1 = 祖父进程；以此类推）。找不到时返回空字符串。
func parentProcessName(depth int) string {
	pid := uint32(os.Getppid())
	for i := 0; i <= depth && pid != 0; i++ {
		ppid, name := findProcess(pid)
		if i == depth {
			return name
		}
		pid = ppid
	}
	return ""
}

// findProcess 在进程快照中查找指定 PID，返回其父进程 PID 与可执行文件名。
func findProcess(pid uint32) (uint32, string) {
	snap, _, _ := procCreateToolhelp32Snap.Call(th32csSnapProcess, 0)
	if snap == ^uintptr(0) || snap == 0 {
		return 0, ""
	}
	defer procCloseHandle.Call(snap)

	var entry processEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	r, _, _ := procProcess32First.Call(snap, uintptr(unsafe.Pointer(&entry)))
	for r != 0 {
		if entry.ProcessID == pid {
			return entry.ParentProcessID, syscall.UTF16ToString(entry.ExeFile[:])
		}
		r, _, _ = procProcess32Next.Call(snap, uintptr(unsafe.Pointer(&entry)))
	}
	return 0, ""
}
