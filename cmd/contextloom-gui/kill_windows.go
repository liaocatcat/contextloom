//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Windows 平台 backend 进程终止。
//
// 关键约束：Windows 上 os.Process.Signal 仅支持 Kill，【不支持 SIGTERM】
// （返回 "not supported by windows"）——若用 Process.Signal(SIGTERM)，
// backend 不会被终止，stop() 只能等 3 秒超时后强杀，每次切换项目/退出
// 都卡顿 3 秒（历史"打开新项目卡顿"根因之一）。
//
// 因此 Windows 一律用 taskkill /T /F 递归终止整棵进程树（backend 是
// contextloom --serve，其下可能有子进程，必须 /T 连带清理）。
func signalTerminate(pid int) error {
	return taskkillTree(pid)
}

func signalKill(pid int) error {
	return taskkillTree(pid)
}

func taskkillTree(pid int) error {
	cmd := exec.Command("taskkill", "/PID", strconv.Itoa(pid), "/T", "/F")
	// GUI 子系统父进程（无控制台）下，console 子进程会新建控制台窗口
	// （退出/切换项目时闪烁黑窗）——CREATE_NO_WINDOW 静默执行。
	cmd.SysProcAttr = hiddenProcAttr()
	// 已退出的 PID：taskkill 报错（返回码 128），无害——调用方按
	// "尽力清理"语义忽略错误。
	return cmd.Run()
}

// processAlive 检查进程是否存活（测试用）：tasklist 按 PID 过滤。
func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), strconv.Itoa(pid))
}
