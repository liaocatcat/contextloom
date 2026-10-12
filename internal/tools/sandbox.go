package tools

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/contextloom/contextloom-agent/internal/sandbox"
)

var allowedHosts map[string]bool

// toolProjectRoot 工具路径解析的项目根锚点（相对路径基准）。
// 历史教训（误报写入成功）：此前 SetProjectRoot 被清空、相对路径按进程 CWD
// 解析——GUI 从 Finder/.app 启动时 CWD 是 / 或 $HOME，模型传 "auth.go"
// 会把文件写到 CWD 下（MkdirAll 成功 → 工具返回"写入成功"），项目里却
// 根本没有该文件，形成"误报成功、实际没有落盘"。
var (
	toolProjectRootMu sync.RWMutex
	toolProjectRoot   string
)

// SetProjectRoot 设置工具相对路径解析的项目根锚点。
// 由 runtime.New 在解析出绝对 projRoot 后调用；空串=未设置（回退进程 CWD，兼容测试）。
func SetProjectRoot(root string) {
	toolProjectRootMu.Lock()
	defer toolProjectRootMu.Unlock()
	if root == "" {
		toolProjectRoot = ""
		return
	}
	if abs, err := filepath.Abs(root); err == nil {
		toolProjectRoot = filepath.Clean(abs)
	} else {
		toolProjectRoot = filepath.Clean(root)
	}
}

// projectRootForPaths 返回当前项目根锚点（未设置时回退进程 CWD）。
func projectRootForPaths() string {
	toolProjectRootMu.RLock()
	defer toolProjectRootMu.RUnlock()
	if toolProjectRoot != "" {
		return toolProjectRoot
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// resolveToolPath 把工具传入的路径解析为绝对路径：
//   - 绝对路径：原样返回；
//   - 相对路径（纯文件名 / ./x / ../x）：锚定项目根（而非进程 CWD）。
//     这是"误报写入成功"的修复核心：无论进程从哪里启动，"auth.go" 永远
//     解析到 <项目根>/auth.go，与记忆/技能/插件目录的 projRoot 语义一致。
func resolveToolPath(p string) string {
	if p == "" {
		return p
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(projectRootForPaths(), p)
}

// SetAllowedHosts 设置 web_fetch 允许的域名白名单
func SetAllowedHosts(hosts []string) {
	allowedHosts = make(map[string]bool)
	for _, h := range hosts {
		allowedHosts[h] = true
	}
}

// cleanPath 将路径解析为规范绝对路径。
// 相对路径锚定项目根（resolveToolPath），而非进程 CWD——修复 GUI 从
// Finder/.app 启动（CWD=/ 或 $HOME）时 "auth.go" 写到 CWD 下的误报成功。
// Windows 上跳过 EvalSymlinks（该 API 对普通路径也可能返回 os.ErrPermission），
// 直接使用 filepath.Abs + filepath.Clean 就足够。
func cleanPath(p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = resolveToolPath(p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		return filepath.Clean(abs), nil
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return filepath.Clean(abs), nil
	}
	return real, nil
}

// cleanPathIn 沙箱感知的路径解析：
//   - 相对路径先锚定项目根（resolveToolPath，与 cleanPath 一致——修复
//     GUI CWD ≠ 项目根时相对路径写到进程启动目录的误报成功）；
//   - 沙箱模式：仅 Abs+Clean（不做 EvalSymlinks 物理解析）——镜像映射基于
//     字符串路径，EvalSymlinks 对已存在/不存在路径解析不一致会导致镜像分裂
//     （如 macOS /var 目录解析为 /private/var、不存在的文件不解析）；
//   - 非沙箱：与 cleanPath 一致。
func cleanPathIn(ctx context.Context, p string) (string, error) {
	if !filepath.IsAbs(p) {
		p = resolveToolPath(p)
	}
	if sandbox.FromContext(ctx) != nil {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		return filepath.Clean(abs), nil
	}
	return cleanPath(p)
}

// checkURLAllowed 检查 URL 是否在域名白名单内
// 谨慎性分析器已提供预警机制，此函数保留作为可选的额外保护层
func checkURLAllowed(rawURL string) error {
	if len(allowedHosts) == 0 {
		return nil // 未设置白名单时允许所有
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return os.ErrInvalid
	}
	if !allowedHosts[u.Hostname()] {
		return os.ErrPermission
	}
	return nil
}

// IsAllowedPath 检查路径是否在允许范围内（已废弃，仅保留兼容）
// 从 V2 开始，不再限制路径，改为预警机制。
// 用户可通过 CautiousAnalyzer 自定义风险规则。
func IsAllowedPath(path string) bool {
	return true
}

// IsAllowedCommand 检查命令是否在允许范围内（已废弃，仅保留兼容）
// 从 V2 开始，不再限制命令，改为预警机制。
// 用户可通过 CautiousAnalyzer 自定义风险规则。
func IsAllowedCommand(cmd string) bool {
	return true
}
