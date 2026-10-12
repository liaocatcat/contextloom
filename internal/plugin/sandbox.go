package plugin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"time"
)

type Sandbox struct {
	MemoryLimitMB int
	CPULimit      float64
}

func NewSandbox(memoryLimitMB int, cpuLimit float64) *Sandbox {
	return &Sandbox{
		MemoryLimitMB: memoryLimitMB,
		CPULimit:      cpuLimit,
	}
}

// runScript 可靠超时执行脚本/命令：
// 手动 Start + Wait goroutine + select 超时 + killProcessTree 杀整棵进程树。
// 不用 exec.CommandContext + CombinedOutput —— CommandContext 超时只杀
// 直接子进程，脚本派生的孙子进程会持有 stdout 管道导致 CombinedOutput
// 永久阻塞（Go exec 已知缺陷），表现为"超时了还卡住"。
func (s *Sandbox) runScript(scriptPath string, args []string, input []byte, timeout time.Duration, hookEnv, hookVal string) ([]byte, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cmd := exec.Command(scriptPath, args...)
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("CONTEXTLOOM_SANDBOX_MEMORY_MB=%d", s.MemoryLimitMB),
	)
	if hookEnv != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", hookEnv, hookVal))
	}
	// 平台进程属性：Unix 无条件 Setpgid（进程组隔离，超时杀整棵树的
	// 前提，与内存限制无关）；Windows 隐藏控制台窗口。
	applySysProcAttr(cmd)

	if input != nil {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("stdin: %w", err)
		}
		go func() {
			defer stdin.Close()
			_, _ = stdin.Write(input)
		}()
	}

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var err error
	select {
	case err = <-done:
		// 正常完成（含非零退出码，err 为 ExitError）
	case <-time.After(timeout):
		// 超时：杀整棵进程树（shell + 全部孙子进程），防管道持有者残留
		killProcessTree(cmd)
		select {
		case err = <-done:
		case <-time.After(2 * time.Second):
			// 二次兜底：进程组杀不掉（如权限异常）时强制杀直接子进程
			_ = cmd.Process.Kill()
			err = <-done
		}
		return buf.Bytes(), fmt.Errorf("script timed out after %s (process tree terminated)", timeout)
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return buf.Bytes(), fmt.Errorf("script exited %d: %s", exitErr.ExitCode(), buf.String())
		}
		return buf.Bytes(), fmt.Errorf("script failed: %w", err)
	}
	return buf.Bytes(), nil
}

// Run 执行钩子命令（旧 API，测试覆盖；生产路径走 RunScript/RunScriptTimeout）。
func (s *Sandbox) Run(pluginName string, hook string, input []byte) ([]byte, error) {
	return s.runScript(pluginName, []string{hook}, input, 30*time.Second, "CONTEXTLOOM_HOOK", hook)
}

// RunScript 直接执行脚本文件（拼接完整路径），用于 during/post 阶段钩子。
// 脚本需有可执行权限（Unix: chmod +x），Windows 需 .bat/.cmd/.exe 后缀。
func (s *Sandbox) RunScript(scriptPath string, input []byte) ([]byte, error) {
	return s.RunScriptTimeout(scriptPath, input, 30*time.Second)
}

// RunScriptTimeout 带自定义超时的脚本执行（message/route 钩子与插件工具使用）。
// 超时/失败时返回错误与部分输出，调用方决定降级语义（默认静默跳过）。
// 超时保证终止整棵进程树并返回，绝不永久阻塞（见 runScript 注释）。
func (s *Sandbox) RunScriptTimeout(scriptPath string, input []byte, timeout time.Duration) ([]byte, error) {
	return s.runScript(scriptPath, nil, input, timeout, "", "")
}
