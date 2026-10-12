// Package envclean 负责进程启动期的环境变量卫生（env hygiene）。
//
// 背景：macOS 的 libsystem_malloc 会对环境中带 MallocStackLogging 家族
// 变量（MallocStackLogging / Lite / NoCompact / Directory）的进程做栈日志
// 初始化登记；但 Go 程序使用自带内存分配器，从不启用 malloc 栈日志，于是
// 进程退出时 libsystem_malloc 的收尾逻辑会向 stderr 打印噪音：
//
//	contextloom(64165) MallocStackLogging: can't turn off malloc stack logging
//	because it was not enabled.
//
// 该变量常由调试工具（lldb/Xcode/Instruments 会话）或 shell 残留注入。
// TUI/GUI 会话中每个继承该变量的子进程退出都会打印一次，反复污染终端画面。
// 本包在 main 启动最早处清除该变量族：此后所有经 os.Environ() 派生的子进程
// 不再继承，退出噪音从根源消失（子进程自身也省去无谓的栈日志登记开销）。
package envclean

import (
	"os"
	"strings"
)

// mallocStackLoggingVars 会产生退出噪音的 macOS malloc 栈日志环境变量族。
// 统一前缀匹配（含 MallocStackLoggingLite / NoCompact / Directory 变体）。
const mallocStackLoggingPrefix = "MallocStackLogging"

// StripMallocDebug 从当前进程环境移除 macOS malloc 调试变量族。
// 幂等：可安全多次调用；仅在变量存在时执行 unsetenv，零副作用。
func StripMallocDebug() {
	for _, kv := range os.Environ() {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if strings.HasPrefix(name, mallocStackLoggingPrefix) {
			_ = os.Unsetenv(name)
		}
	}
}
