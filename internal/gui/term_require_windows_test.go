//go:build windows

package gui

import (
	"syscall"
	"testing"
	"unsafe"
)

// requirePTYEnv 检测当前会话是否有交互桌面（WinSta0\Default）。
//
// 系统 ConPTY（CreatePseudoConsole + conhost.exe）依赖交互桌面会话：
// 在无桌面的会话（SSH/agent/任务计划/部分远程会话）中，CreatePseudoConsole
// 会"假成功"但 conhost 无法激活——子进程以 0xC0000142 (STATUS_DLL_INIT_FAILED)
// 退出、输出管道永远无数据。此时真实 PTY 测试必然失败，故跳过
// （用户双击 GUI 的交互桌面不受影响，测试保持有效性）。
func requirePTYEnv(t *testing.T) {
	t.Helper()
	if !hasInteractiveDesktop() {
		t.Skip("当前会话无交互桌面（WinSta0\\Default），系统 ConPTY 不可用，跳过真实 PTY 测试")
	}
}

// hasInteractiveDesktop 返回当前线程是否连接在 WinSta0\Default。
func hasInteractiveDesktop() bool {
	user32 := syscall.NewLazyDLL("user32.dll")
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	procGetWS := user32.NewProc("GetProcessWindowStation")
	procGetTID := kernel32.NewProc("GetCurrentThreadId")
	procGetDesk := user32.NewProc("GetThreadDesktop")
	procGetObjInfo := user32.NewProc("GetUserObjectInformationW")

	hWS, _, _ := procGetWS.Call()
	// 【注意】GetThreadDesktop 必须传【当前线程 ID】——传 0 会返回无效
	// 句柄，导致桌面名取不到而误判"无交互桌面"（真实交互桌面被跳过，
	// ConPTY 回归测试全部白跑）。
	tid, _, _ := procGetTID.Call()
	hDesk, _, _ := procGetDesk.Call(tid)
	name := func(h uintptr) string {
		var needed uint32
		procGetObjInfo.Call(h, 2, 0, 0, uintptr(unsafe.Pointer(&needed)))
		if needed == 0 {
			return ""
		}
		buf := make([]uint16, needed/2+2)
		procGetObjInfo.Call(h, 2, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
		return syscall.UTF16ToString(buf)
	}
	return name(hWS) == "WinSta0" && name(hDesk) == "Default"
}
