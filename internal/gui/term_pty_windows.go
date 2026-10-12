//go:build windows

package gui

// Windows 伪终端实现：ConPTY（Windows Pseudo Console，Win10 1809+）。
//
// 对标业界主流桌面编码 Agent 的 EUI 命令行（node-pty 在 Windows 上即绑定 ConPTY）：
//   - CreatePseudoConsole 创建伪控制台
//   - CreateProcess + STARTUPINFOEX（PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE）
//     让 shell 挂到伪控制台 → 进入真正的交互模式（提示符/回显/行编辑/
//     Ctrl+C/颜色/full-screen 程序）
//   - 输出从 outR 读取（原始字节流含 ANSI），输入写入 inW
//   - ResizePseudoConsole 同步尺寸
//
// 全部通过 kernel32 动态加载 + golang.org/x/sys/windows 调用，零 CGO，
// 保持"全平台可交叉编译"特性（GOOS=windows 交叉编译无需 mingw）。

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32Win      = syscall.NewLazyDLL("kernel32.dll")
	procCreatePseudo = kernel32Win.NewProc("CreatePseudoConsole")
	procResizePseudo = kernel32Win.NewProc("ResizePseudoConsole")
	procClosePseudo  = kernel32Win.NewProc("ClosePseudoConsole")
)

const (
	// PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE（Windows SDK winnt.h）。
	// 注意：不能直接写 22！该宏经 ProcThreadAttributeValue(22, FALSE, TRUE, FALSE)
	// 编码为 22 | (1<<17)（bit17=Input 标志）= 0x20016。若传裸 22，
	// UpdateProcThreadAttribute 会把属性判为无效 → 返回 ERROR_NOT_SUPPORTED
	// （"The request is not supported"），ConPTY 挂载失败，终端无法启动。
	procThreadAttributePseudoConsole = 0x20016
	// EXTENDED_STARTUPINFO_PRESENT（CreateProcess 标志）
	extendedStartupinfoPresent = 0x00080000
	// STARTF_USESTDHANDLES（STARTUPINFO.dwFlags）
	startfUseStdHandles = 0x00000100
	// HANDLE_FLAG_INHERIT（GetHandleInformation/SetHandleInformation）
	handleFlagInherit = 0x1
)

// startupInfoEx 对应 Windows STARTUPINFOEX 结构：
// STARTUPINFO 在前，ProcThreadAttributeList 指针在后。
type startupInfoEx struct {
	windows.StartupInfo
	lpAttributeList *windows.ProcThreadAttributeList
}

// conptyProc Windows 伪终端（ConPTY）。
type conptyProc struct {
	hPC  windows.Handle // HPCON
	inW  windows.Handle // 输入写端（xterm 键盘 → ConPTY stdin）
	outR windows.Handle // 输出读端（ConPTY stdout/stderr → xterm）
	proc windows.Handle // 子进程句柄
	pid  uint32
}

// startPTY 以 ConPTY 伪终端启动交互 shell。
func startPTY(shell, cwd string, cols, rows uint16) (ptyProc, error) {
	// 0. 前置检查：ConPTY 需要 Windows 10 1809+。
	// CreatePseudoConsole 在旧系统 kernel32 中【不存在】导出，直接
	// LazyProc.Call 会 panic（LazyDLL 找不到函数即 panic）。这里先
	// Find 检查，给出友好错误而非崩溃。
	if err := procCreatePseudo.Find(); err != nil {
		return nil, fmt.Errorf("当前系统不支持 ConPTY（需要 Windows 10 1809 或更高版本）: %w", err)
	}

	// 1. 输入/输出管道对（ConPTY 内部持有句柄副本，创建后可关闭本地冗余端）。
	// 【关键】句柄必须可继承（bInheritHandle=TRUE）：CreatePseudoConsole 会把
	// hInput/hOutput 交给内部启动的独立 conhost 进程使用，conhost 只能通过
	// 句柄继承拿到它们。若句柄不可继承（CreatePipe 传 nil 安全属性时默认
	// 不可继承），conhost 无法访问伪终端流 → ConPTY 初始化失败 → 子进程
	// 以 0xC0000142 (STATUS_DLL_INIT_FAILED) 退出，即用户看到的
	// "进程已退出，代码3221225794"。
	inR, inW, outR, outW, err := createPTYPipes()
	if err != nil {
		return nil, err
	}
	if cols == 0 || rows == 0 {
		cols, rows = 80, 24
	}

	// 2. 创建伪控制台。
	// CreatePseudoConsole 签名：CreatePseudoConsole(size, hInput, hOutput, flags, phPC)
	//   hInput  = ConPTY 读取键盘输入的管道【读端】（本端 inR）
	//   hOutput = ConPTY 写出显示输出的管道【写端】（本端 outW）
	// 注意方向不可颠倒：若传 (outR, inW)（输出管道读端/输入管道写端），ConPTY
	// 会去读无人写入的输出管道、往输入管道写端写输出——终端无显示、输入失败。
	hPC, err := createPseudoConsole(cols, rows, inR, outW, 0)
	if err != nil {
		_ = windows.CloseHandle(inR)
		_ = windows.CloseHandle(inW)
		_ = windows.CloseHandle(outR)
		_ = windows.CloseHandle(outW)
		return nil, err
	}
	// ConPTY 已持有副本，关闭本地冗余端（官方示例同样处理）
	_ = windows.CloseHandle(outW)
	_ = windows.CloseHandle(inR)

	// 3. STARTUPINFOEX：把伪控制台属性挂到子进程。
	// 【关键】lpValue 必须传 HPCON 的【值】而非 &hPC 地址！
	// UpdateProcThreadAttribute 的 PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE 属性
	// 直接把 lpValue 当作 HPCON 句柄值使用（微软官方 ConPTY 示例与
	// node-pty conpty.cc 均传 handle->hpc 值、cbSize=sizeof(HPCON)）。若误传
	// &hPC（栈变量地址），内核把栈地址当 HPCON → conhost 连接伪控制台失败
	// → 子进程 0xC0000142（"进程已退出，代码3221225794"）。
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		closePseudoConsole(hPC)
		_ = windows.CloseHandle(inW)
		_ = windows.CloseHandle(outR)
		return nil, fmt.Errorf("创建属性列表失败: %w", err)
	}
	defer attrList.Delete()
	// 把 HPCON（句柄即指针）按位模式转为 unsafe.Pointer【传值】：vet 对
	// unsafe.Pointer(hPC)（uintptr 直转）报警，故用 *(*unsafe.Pointer)(&hPC)
	// 取值；语义与 node-pty 传 handle->hpc 完全一致。
	hpcVal := *(*unsafe.Pointer)(unsafe.Pointer(&hPC))
	if err := attrList.Update(procThreadAttributePseudoConsole, hpcVal, unsafe.Sizeof(hPC)); err != nil {
		closePseudoConsole(hPC)
		_ = windows.CloseHandle(inW)
		_ = windows.CloseHandle(outR)
		return nil, fmt.Errorf("设置伪控制台属性失败（需要 Windows 10 1809+ 的 ConPTY 支持）: %w", err)
	}

	var siEx startupInfoEx
	siEx.Cb = uint32(unsafe.Sizeof(siEx))
	// 【关键】显式设置 STARTF_USESTDHANDLES 且三个标准句柄全为 NULL——
	// 与 node-pty conpty.cc PtyConnect 完全一致：
	//   siEx.StartupInfo.dwFlags |= STARTF_USESTDHANDLES;
	//   siEx.StartupInfo.hStdError = nullptr;
	//   siEx.StartupInfo.hStdInput = nullptr;
	//   siEx.StartupInfo.hStdOutput = nullptr;
	// 若不设置，CreateProcess 会让子进程【继承父进程的标准句柄】：
	//   - GUI 双击启动：父进程（windowsgui 子系统）标准句柄为 NULL →
	//     cmd 的 stdin=NULL → 读输入失败退出；
	//   - 从管道/重定向终端启动（CI、SSH、agent）：父进程 stdin 是 EOF
	//     管道 → cmd 读 stdin 立即 EOF → 【进程已退出，代码0】
	//     （banner 正常显示后瞬间退出）。
	// 显式 NULL 后，cmd 无法依赖继承句柄，只能走 ConPTY 控制台
	// （conhost）的输入输出路径——这正是 ConPTY 的正确用法。
	siEx.Flags |= startfUseStdHandles
	siEx.lpAttributeList = attrList.List()

	// 4. CreateProcess（lpCommandLine 必须可写，Windows 会修改）。
	// bInheritHandles=false 与 node-pty conpty.cc 一致（其注释明确
	// "VERY IMPORTANT that this is false"）：ConPTY 模式下子进程的
	// 标准句柄由 conhost 重定向，无需继承我们的管道；若为 true，
	// cmd.exe 会继承 ConPTY 管道句柄副本，导致伪控制台流管理冲突。
	cmdline, err := windows.UTF16FromString(shell)
	if err != nil {
		closePseudoConsole(hPC)
		_ = windows.CloseHandle(inW)
		_ = windows.CloseHandle(outR)
		return nil, fmt.Errorf("编码命令行失败: %w", err)
	}
	var curDir *uint16
	if cwd != "" {
		curDir, _ = windows.UTF16PtrFromString(cwd)
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		nil, &cmdline[0], nil, nil, false,
		extendedStartupinfoPresent,
		nil, curDir,
		(*windows.StartupInfo)(unsafe.Pointer(&siEx)), &pi,
	)
	if err != nil {
		closePseudoConsole(hPC)
		_ = windows.CloseHandle(inW)
		_ = windows.CloseHandle(outR)
		return nil, fmt.Errorf("启动 shell %q 失败: %w", shell, err)
	}
	_ = windows.CloseHandle(pi.Thread) // 线程句柄无需保留

	return &conptyProc{hPC: hPC, inW: inW, outR: outR, proc: pi.Process, pid: pi.ProcessId}, nil
}

// createPTYPipes 创建 ConPTY 输入/输出两对管道。
//
// 【关键】四个句柄都必须带 HANDLE_FLAG_INHERIT（SECURITY_ATTRIBUTES.
// InheritHandle=TRUE）：CreatePseudoConsole 内部启动的 conhost 是独立进程，
// 它通过【句柄继承】访问 hInput/hOutput 流。若句柄不可继承，conhost 拿不到
// 伪终端流 → ConPTY 初始化失败 → 挂载的子进程以 0xC0000142
// （STATUS_DLL_INIT_FAILED，"进程已退出，代码3221225794"）退出。
// 微软官方 ConPTY 示例同样使用 bInheritHandle=TRUE 的 SECURITY_ATTRIBUTES。
func createPTYPipes() (inR, inW, outR, outW windows.Handle, err error) {
	sa := &windows.SecurityAttributes{
		Length:        uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		InheritHandle: 1, // bInheritHandle = TRUE
	}
	if err = windows.CreatePipe(&inR, &inW, sa, 0); err != nil {
		return 0, 0, 0, 0, fmt.Errorf("创建输入管道失败: %w", err)
	}
	if err = windows.CreatePipe(&outR, &outW, sa, 0); err != nil {
		_ = windows.CloseHandle(inR)
		_ = windows.CloseHandle(inW)
		return 0, 0, 0, 0, fmt.Errorf("创建输出管道失败: %w", err)
	}
	return inR, inW, outR, outW, nil
}

// createPseudoConsole 动态调用 CreatePseudoConsole。
// 返回 S_OK(0) 成功；HRESULT 非 0 失败。
func createPseudoConsole(cols, rows uint16, in, out windows.Handle, flags uint32) (windows.Handle, error) {
	// COORD{SHORT X, SHORT Y}：X=cols（低位），Y=rows（高位）
	size := uint32(rows)<<16 | uint32(cols)
	var hPC windows.Handle
	r, _, _ := procCreatePseudo.Call(
		uintptr(size),
		uintptr(in),
		uintptr(out),
		uintptr(flags),
		uintptr(unsafe.Pointer(&hPC)),
	)
	if r != 0 {
		return 0, fmt.Errorf("CreatePseudoConsole 失败: HRESULT=0x%08x", uint32(r))
	}
	return hPC, nil
}

// Read 从 ConPTY 输出端读取原始字节流。
func (p *conptyProc) Read(buf []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(p.outR, buf, &n, nil)
	if err != nil {
		// 子进程退出/伪控制台关闭时的典型错误 → 视为 EOF
		if err == syscall.ERROR_BROKEN_PIPE ||
			err == windows.ERROR_OPERATION_ABORTED ||
			err == windows.ERROR_INVALID_HANDLE {
			return int(n), ioEOF()
		}
		return int(n), err
	}
	return int(n), nil
}

// Write 向 ConPTY 输入端写入键盘序列/粘贴文本。
func (p *conptyProc) Write(b []byte) (int, error) {
	var n uint32
	if err := windows.WriteFile(p.inW, b, &n, nil); err != nil {
		return int(n), err
	}
	return int(n), nil
}

// Resize 同步伪控制台尺寸（保证 vim/top 等全屏程序布局正确）。
func (p *conptyProc) Resize(cols, rows uint16) error {
	if cols == 0 || rows == 0 {
		return nil
	}
	coord := uint32(rows)<<16 | uint32(cols)
	r, _, _ := procResizePseudo.Call(uintptr(p.hPC), uintptr(coord))
	if r != 0 {
		return fmt.Errorf("ResizePseudoConsole 失败: HRESULT=0x%08x", uint32(r))
	}
	return nil
}

// Close 终止进程树并释放 ConPTY 全部资源。
func (p *conptyProc) Close() error {
	// 先杀进程树（taskkill /T /F，含 shell 派生的全部子进程）
	if p.pid != 0 {
		_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.FormatUint(uint64(p.pid), 10)).Run()
	}
	// 关闭伪控制台（同时终止挂载的子进程）
	closePseudoConsole(p.hPC)
	p.hPC = 0
	if p.inW != 0 {
		_ = windows.CloseHandle(p.inW)
		p.inW = 0
	}
	if p.outR != 0 {
		_ = windows.CloseHandle(p.outR)
		p.outR = 0
	}
	if p.proc != 0 {
		_ = windows.CloseHandle(p.proc)
		p.proc = 0
	}
	return nil
}

// Wait 等待子进程退出并返回退出码。
func (p *conptyProc) Wait() int {
	if p.proc == 0 {
		return -1
	}
	_, err := windows.WaitForSingleObject(p.proc, windows.INFINITE)
	if err != nil {
		return -1
	}
	var code uint32
	if err := windows.GetExitCodeProcess(p.proc, &code); err != nil {
		return -1
	}
	return int(code)
}

func closePseudoConsole(hPC windows.Handle) {
	if hPC != 0 {
		_, _, _ = procClosePseudo.Call(uintptr(hPC))
	}
}

// ioEOF 返回 io.EOF（避免在平台文件里 import io 仅为构造错误）。
func ioEOF() error { return os.ErrClosed }
