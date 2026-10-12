//go:build windows

package gui

import (
	"syscall"
	"testing"
	"unsafe"
)

// getHandleFlags 动态调用 kernel32.GetHandleInformation（x/sys 旧版无此导出）。
func getHandleFlags(h syscall.Handle) (uint32, error) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetHandleInformation")
	var flags uint32
	r, _, e := proc.Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	if r == 0 {
		return 0, e
	}
	return flags, nil
}

// TestCreatePTYPipes_HandlesInheritable 验证 ConPTY 管道句柄带继承标志。
//
// 这是 0xC0000142（STATUS_DLL_INIT_FAILED，"进程已退出，代码3221225794"）
// 的回归测试：CreatePseudoConsole 内部 conhost 通过句柄继承访问 hInput/
// hOutput；若句柄不可继承（CreatePipe 传 nil 安全属性），ConPTY 初始化失败，
// 子进程 cmd.exe 以 0xC0000142 退出。四个句柄必须全部带 HANDLE_FLAG_INHERIT。
func TestCreatePTYPipes_HandlesInheritable(t *testing.T) {
	inR, inW, outR, outW, err := createPTYPipes()
	if err != nil {
		t.Fatalf("createPTYPipes 失败: %v", err)
	}
	defer func() {
		_ = syscall.CloseHandle(syscall.Handle(inR))
		_ = syscall.CloseHandle(syscall.Handle(inW))
		_ = syscall.CloseHandle(syscall.Handle(outR))
		_ = syscall.CloseHandle(syscall.Handle(outW))
	}()

	for name, h := range map[string]syscall.Handle{
		"inR":  syscall.Handle(inR),
		"inW":  syscall.Handle(inW),
		"outR": syscall.Handle(outR),
		"outW": syscall.Handle(outW),
	} {
		flags, err := getHandleFlags(h)
		if err != nil {
			t.Fatalf("GetHandleInformation(%s) 失败: %v", name, err)
		}
		if flags&handleFlagInherit == 0 {
			t.Fatalf("句柄 %s (0x%x) 未设置 HANDLE_FLAG_INHERIT——conhost 无法访问伪终端流，子进程将 0xC0000142 退出", name, h)
		}
	}
}

// TestCreatePTYPipes_ValidHandles 管道句柄非零且可读写（基本健全性）。
func TestCreatePTYPipes_ValidHandles(t *testing.T) {
	inR, inW, outR, outW, err := createPTYPipes()
	if err != nil {
		t.Fatalf("createPTYPipes 失败: %v", err)
	}
	defer func() {
		_ = syscall.CloseHandle(syscall.Handle(inR))
		_ = syscall.CloseHandle(syscall.Handle(inW))
		_ = syscall.CloseHandle(syscall.Handle(outR))
		_ = syscall.CloseHandle(syscall.Handle(outW))
	}()

	if inR == 0 || inW == 0 || outR == 0 || outW == 0 {
		t.Fatalf("存在空句柄: inR=%x inW=%x outR=%x outW=%x", inR, inW, outR, outW)
	}
	if inR == inW || outR == outW {
		t.Fatal("管道读写端句柄重复")
	}
}
