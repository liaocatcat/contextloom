package tools

import (
	"bytes"
	"context"
	"fmt"
	"github.com/contextloom/contextloom-agent/internal/sandbox"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// envDenylist 子进程环境变量黑名单（借鉴业界同类 Agent，防止注入篡改）
// 注：MallocStackLogging 前缀匹配覆盖 Lite/NoCompact/Directory 变体——
// macOS 下继承该变量族且从未启用栈日志（Go 自带分配器）的进程退出时
// 会打印 "can't turn off malloc stack logging" 噪音污染终端（双保险：
// main 启动期已统一清除，此处兜底防旧进程/外部注入）。
var envDenylist = []string{
	"DYLD_", "LD_", "PYTHONPATH", "NODE_OPTIONS",
	"GIT_ASKPASS", "SSH_ASKPASS", "DEEPSEEK_API_KEY",
	"MallocStackLogging",
}

// shell 返回当前系统的 shell 路径和模式
func shell() (name string, arg0 string, arg1 string) {
	if runtime.GOOS == "windows" {
		// 优先 PowerShell 7 (pwsh)，然后 Windows PowerShell，最后 cmd
		// 使用 Where.exe 而不是 LookPath，因为 LookPath 在某些 Windows 配置下可能找不到 pwsh
		if _, err := os.Stat(`C:\Program Files\PowerShell\7\pwsh.exe`); err == nil {
			return `C:\Program Files\PowerShell\7\pwsh.exe`, "-NoProfile", "-Command"
		}
		if _, err := exec.LookPath("pwsh"); err == nil {
			return "pwsh", "-NoProfile", "-Command"
		}
		if _, err := exec.LookPath("powershell"); err == nil {
			return "powershell", "-NoProfile", "-Command"
		}
		return "cmd", "/C", ""
	}
	return "sh", "-c", ""
}

func ExecCommand(ctx context.Context, params map[string]any) (any, error) {
	command, _ := params["command"].(string)
	// 默认超时 20s（原 30s 偏长）；模型可通过 timeout_ms 覆盖，上限 5 分钟防误传
	timeoutMs := 20000
	if t, ok := params["timeout_ms"].(float64); ok {
		timeoutMs = int(t)
	}
	if timeoutMs <= 0 {
		timeoutMs = 20000
	}
	if timeoutMs > 300000 {
		timeoutMs = 300000
	}

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		shellName, arg0, arg1 := shell()
		if arg1 == "" {
			// cmd /C <command>
			cmd = exec.CommandContext(ctx, shellName, arg0, command)
		} else {
			// pwsh/powershell -NoProfile -Command <command>
			cmd = exec.CommandContext(ctx, shellName, arg0, arg1, command)
		}
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	// 清洗危险环境变量（防止子进程注入篡改）
	env := os.Environ()
	filtered := make([]string, 0, len(env))
	for _, e := range env {
		skip := false
		for _, prefix := range envDenylist {
			if len(e) >= len(prefix) && e[:len(prefix)] == prefix {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, e)
		}
	}
	cmd.Env = filtered

	// 沙箱模式：工作目录进入镜像 + 资源限制（Linux rlimit / 超时兜底）；
	// 非沙箱模式：命令在项目根执行（CWD 锚定项目根，与 write_file 相对路径
	// 语义一致——GUI 从 Finder/.app 启动 CWD=/ 时命令仍作用于项目目录）。
	fs := sandbox.FromContext(ctx)
	if fs != nil {
		cmd.Dir = fs.MirrorPath(cwdForSandbox(ctx))
		_ = os.MkdirAll(cmd.Dir, 0o755)
	} else if projectRoot() != "" {
		cmd.Dir = projectRoot()
	}
	if fs != nil {
		sandbox.ApplyLimits(cmd)
		// 沙箱会话默认网络拒绝：代理级隔离（对标前沿容器网络隔离的零依赖落地）。
		// HTTP(S)_PROXY/ALL_PROXY（大小写双份）全部强制指向死端口
		// http://127.0.0.1:9（连接立即失败），删除 NO_PROXY/no_proxy 防豁免绕过，
		// 并置 CONTEXTLOOM_SANDBOX_NET=deny 标记——沙箱内命令不可外联
		// （防数据泄露/后门下载），仅本机回环可用。
		// envDenylist 不含代理变量；即便宿主注入，NetDenyEnv 后置覆盖保证生效。
		cmd.Env = sandbox.NetDenyEnv(cmd.Env)
	}

	// 可靠超时执行：超时后强制终止整个进程组/进程树，
	// 防止 shell 派生的孙子进程残留并持有 stdout 管道导致永久挂起（"卡半天"）。
	output, timedOut, err := runWithReliableTimeout(ctx, cmd, time.Duration(timeoutMs)*time.Millisecond)
	exitCode := 0
	if err != nil {
		if timedOut {
			output = append(output, []byte(fmt.Sprintf("\n[超时] 命令执行超过 %d 秒，已强制终止整棵进程树", timeoutMs/1000))...)
			exitCode = 124 // 与 timeout 命令一致的超时退出码
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			// 命令未找到或无法启动（如 Windows 上找不到 shell），将错误信息写入 stdout
			output = append(output, []byte("\n[错误] 命令执行失败: "+err.Error())...)
			exitCode = -1
		}
	}

	return map[string]any{
		"stdout":    string(output),
		"exit_code": exitCode,
	}, nil
}

// runWithReliableTimeout 带可靠超时执行命令：
//   - Unix: 子进程设为进程组长（Setpgid），超时后 kill(-pgid, SIGKILL) 杀掉
//     整棵进程树（shell 及其所有孙子进程）——只杀 shell 会让孙子进程残留并
//     持有 stdout 管道，导致输出收集永久阻塞。
//   - Windows: 超时后用 taskkill /T /F 递归终止进程树。
//   - 手动收集输出而非 CombinedOutput：超时后可立即返回，不受管道持有者影响。
//
// 平台差异由 prepareProcess/killProcessTree 封装（exec_unix.go / exec_windows.go，
// 通过 build tag 隔离——不能在 exec.go 直接引用 syscall.SysProcAttr/syscall.Kill，
// 否则 Windows 交叉编译会因字段/符号不存在而编译失败）。
func runWithReliableTimeout(ctx context.Context, cmd *exec.Cmd, timeout time.Duration) (output []byte, timedOut bool, err error) {
	prepareProcess(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Start(); err != nil {
		return nil, false, err
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	select {
	case err := <-done:
		return buf.Bytes(), false, err
	case <-ctx.Done():
		// 超时：强制终止整棵进程树
		killProcessTree(cmd)
		// 等待回收，最多 2s（防 kill 失败导致的二次挂起）
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
		}
		return buf.Bytes(), true, ctx.Err()
	}
}

func SearchContent(ctx context.Context, params map[string]any) (any, error) {
	pattern, _ := params["pattern"].(string)
	dir, _ := params["path"].(string)
	if dir == "" {
		dir = "."
	}

	// Try grep/findstr first
	var output []byte
	var err error
	if runtime.GOOS == "windows" {
		// Windows 用 findstr 替代 grep
		cmd := exec.CommandContext(ctx, "findstr", "/S", "/N", "/M", pattern, filepath.Join(dir, "*"))
		output, err = cmd.CombinedOutput()
	} else {
		cmd := exec.CommandContext(ctx, "grep", "-rn", pattern, dir)
		output, err = cmd.CombinedOutput()
	}

	// If grep fails (binary not found, or exit code 2 — serious error),
	// fall back to pure Go filepath.Walk + strings.Contains
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Exit code 1 means "no matches" — valid result, don't fallback
			if exitErr.ExitCode() == 1 {
				lines := splitLines(string(output))
				return map[string]any{"matches": lines, "count": len(lines)}, nil
			}
			// Exit code >= 2: error — fallback
		}
		// grep not found or other execution error — fallback
		return searchWithWalk(ctx, pattern, dir)
	}

	// grep succeeded (exit code 0)
	lines := splitLines(string(output))
	return map[string]any{"matches": lines, "count": len(lines)}, nil
}

// searchWithWalk is the pure Go fallback when grep is unavailable.
// It walks the directory tree and uses strings.Contains to match each line.
func searchWithWalk(ctx context.Context, pattern, dir string) (any, error) {
	var matches []string

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip files we can't access
		}
		if info.IsDir() {
			return nil
		}

		// Skip binary files by checking for null bytes
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if isBinary(data) {
			return nil
		}

		content := string(data)
		lines := splitLines(content)
		for i, line := range lines {
			if strings.Contains(line, pattern) {
				matches = append(matches, path+":"+strconv.Itoa(i+1)+":"+line)
			}
		}

		return nil
	})

	if err != nil {
		return map[string]any{"matches": []string{}, "count": 0}, nil
	}

	if matches == nil {
		matches = []string{}
	}
	return map[string]any{"matches": matches, "count": len(matches)}, nil
}

// isBinary checks the first 8KB of data for null bytes, indicating a binary file.
func isBinary(data []byte) bool {
	if len(data) > 8192 {
		data = data[:8192]
	}
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func ListFiles(ctx context.Context, params map[string]any) (any, error) {
	path, _ := params["path"].(string)
	resolved, err := cleanPathIn(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败 %s: 路径解析错误: %w", path, err)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, fmt.Errorf("读取目录失败 %s: %w", path, err)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name()
	}
	// 沙箱模式：合并镜像中多出的条目（同名以真实为准——镜像由写操作创建）
	if fs := sandbox.FromContext(ctx); fs != nil {
		seen := make(map[string]bool, len(names))
		for _, n := range names {
			seen[n] = true
		}
		mirrorDir := fs.MirrorPath(resolved)
		if ments, err := os.ReadDir(mirrorDir); err == nil {
			for _, me := range ments {
				if !seen[me.Name()] {
					names = append(names, me.Name())
					seen[me.Name()] = true
				}
			}
		}
	}
	return map[string]any{"files": names}, nil
}

// cwdForSandbox 沙箱模式下命令的工作目录基准（当前进程 CWD）。
func cwdForSandbox(ctx context.Context) string {
	if fs := sandbox.FromContext(ctx); fs != nil {
		if wd, err := os.Getwd(); err == nil {
			return wd
		}
	}
	return "."
}

// projectRoot 返回项目根锚点（未设置返回空串）；供 exec 类工具做 CWD 锚定。
func projectRoot() string {
	toolProjectRootMu.RLock()
	defer toolProjectRootMu.RUnlock()
	return toolProjectRoot
}

func RunTests(ctx context.Context, params map[string]any) (any, error) {
	target, _ := params["test_target"].(string)
	if target == "" {
		target = "./..."
	}

	// 可靠性修复：go test 必须与 exec_command 同级治理——
	//  1) GOTMPDIR 指向本工具创建的私有临时目录：go test 的中间产物
	//     （go-build 随机目录 + runtime.test 等测试二进制）全部落盘于此，
	//     与系统级 $TMPDIR/go-build* 隔离，事后整体清理，不再污染系统缓存区；
	//  2) 进程树治理：测试进程设为进程组长，超时/取消时 kill(-pgid) 连坐
	//     整棵进程树（含 go 派生的 runtime.test 子进程）——修复前超时只杀
	//     go 父进程，孤儿 runtime.test 残留锁住 go-build 目录并持续重建缓存，
	//     必须 rm -rf $TMPDIR/go-build* 才能清理。
	cmd := exec.CommandContext(ctx, "go", "test", target)
	if projectRoot() != "" {
		cmd.Dir = projectRoot() // CWD 锚定项目根（GUI 启动目录 ≠ 项目根时 go test 仍作用于项目）
	}
	tmpDir, mkErr := os.MkdirTemp("", "contextloom-gotest-*")
	if mkErr == nil {
		cmd.Env = append(os.Environ(), "GOTMPDIR="+tmpDir)
		defer os.RemoveAll(tmpDir) // 测试结束后私有临时区整体删除（含残留）
	} else {
		cmd.Env = os.Environ()
	}
	// Unix: Setpgid 进程组长，超时后 kill(-pgid) 连坐 kill 整棵进程树；
	// Windows: taskkill /T /F 递归终止（exec_windows.go 实现）。
	prepareProcess(cmd)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	startErr := cmd.Start()
	if startErr != nil {
		return map[string]any{
			"passed":    false,
			"exit_code": -1,
			"output":    "启动 go test 失败: " + startErr.Error(),
		}, nil
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var output []byte
	passed := true
	exitCode := 0
	select {
	case err := <-done:
		output = buf.Bytes()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
			passed = false
		}
	case <-ctx.Done():
		// 超时（默认工具超时 120s）：连坐杀掉整棵进程树（go + runtime.test 等），
		// 等待回收最多 2s——修复前只杀 go 父进程，孤儿 runtime.test 残留。
		killProcessTree(cmd)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
		}
		output = buf.Bytes()
		output = append(output, fmt.Sprintf("\n[超时] go test 执行超时，已强制终止整棵进程树")...)
		exitCode = 124
		passed = false
	}

	return map[string]any{
		"passed":    passed,
		"exit_code": exitCode,
		"output":    string(output),
	}, nil
}

func GitDiff(ctx context.Context, params map[string]any) (any, error) {
	cmd := exec.CommandContext(ctx, "git", "diff")
	if projectRoot() != "" {
		cmd.Dir = projectRoot() // CWD 锚定项目根（GUI 启动目录 ≠ 项目根时 git diff 仍作用于项目仓库）
	}
	output, _ := cmd.CombinedOutput()
	return map[string]any{"diff": string(output)}, nil
}

func WebFetch(ctx context.Context, params map[string]any) (any, error) {
	url, _ := params["url"].(string)
	maxLen := 4096
	if l, ok := params["max_length"].(float64); ok {
		maxLen = int(l)
	}
	if err := checkURLAllowed(url); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	buf := make([]byte, maxLen)
	n, _ := resp.Body.Read(buf)
	return map[string]any{"content": string(buf[:n]), "fetched": n}, nil
}
