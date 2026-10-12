// Package sandbox 提供轻量 OS 级沙箱：影子文件系统 + 受限执行环境。
//
// 对标前沿（OpenHands Docker / Claude Code sandboxing）的零依赖落地：
//   - 影子文件系统 ShadowFS：沙箱会话内所有文件写操作重定向到镜像目录，
//     读操作镜像优先（镜像有则读镜像，无则读真实），列表合并展示；
//   - 进程隔离：exec 命令在沙箱模式下工作目录进入镜像、环境净化、
//     Unix 资源限制（RLIMIT_AS/NOFILE/CPU）兜底；
//   - 会话结束可选择 Commit（镜像写回真实）或 Discard（丢弃全部改动）。
//
// 纯 Go 实现，全平台可交叉编译（资源限制按 build tag 平台隔离）。
package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ctxKey 沙箱上下文键。
type ctxKey struct{}

// ShadowFS 影子文件系统。
type ShadowFS struct {
	root string // 镜像根目录（绝对路径）
}

// New 创建影子文件系统，root 为镜像根目录（不存在则创建）。
// 注意：统一用 Abs+Clean 字符串路径（不做 EvalSymlinks 物理解析——
// 它对已存在/不存在路径解析结果不一致，会导致镜像路径分裂）。
func New(root string) (*ShadowFS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("沙箱根路径解析失败: %w", err)
	}
	abs = filepath.Clean(abs)
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, fmt.Errorf("创建沙箱镜像目录失败: %w", err)
	}
	return &ShadowFS{root: abs}, nil
}

// Root 返回镜像根目录。
func (s *ShadowFS) Root() string { return s.root }

// MirrorPath 返回真实路径在镜像中的对应路径（不校验存在性）。
// 相对路径基于当前工作目录解析（与真实写路径保持一致）。
// 统一用 Abs+Clean 字符串路径（不做 EvalSymlinks，与 New 一致）。
func (s *ShadowFS) MirrorPath(realPath string) string {
	abs, err := filepath.Abs(realPath)
	if err != nil {
		abs = filepath.Clean(realPath)
	} else {
		abs = filepath.Clean(abs)
	}
	// 镜像结构：root + 去除盘符/根后的绝对路径
	rel := abs
	if vol := filepath.VolumeName(abs); vol != "" {
		rel = strings.TrimPrefix(abs, vol)
	}
	rel = strings.TrimPrefix(rel, string(filepath.Separator))
	return filepath.Join(s.root, rel)
}

// Has 判断镜像中是否存在该路径。
func (s *ShadowFS) Has(realPath string) bool {
	_, err := os.Stat(s.MirrorPath(realPath))
	return err == nil
}

// ResolveWrite 写路径重定向：始终返回镜像路径（写操作不触碰真实文件系统）。
func (s *ShadowFS) ResolveWrite(realPath string) string {
	mirror := s.MirrorPath(realPath)
	if dir := filepath.Dir(mirror); dir != s.root {
		_ = os.MkdirAll(dir, 0o755)
	}
	return mirror
}

// ResolveRead 读路径解析：镜像存在 → 返回镜像路径+true；否则真实路径+false。
func (s *ShadowFS) ResolveRead(realPath string) (string, bool) {
	mirror := s.MirrorPath(realPath)
	if _, err := os.Stat(mirror); err == nil {
		return mirror, true
	}
	return realPath, false
}

// Merge 合并镜像中的文件到真实路径（覆盖）。返回合并的文件数。
// 用于沙箱会话结束选择"保留改动"（Commit 语义）。
func (s *ShadowFS) Merge() (int, error) {
	count := 0
	err := filepath.Walk(s.root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 跳过无法访问的镜像条目
		}
		if info.IsDir() {
			return nil
		}
		// 镜像内文件 → 真实路径（镜像路径 = root + 真实绝对路径，逐层还原）
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return nil
		}
		real := restoreRealPath(rel)
		if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if err := os.WriteFile(real, data, 0o644); err == nil {
			count++
		}
		return nil
	})
	return count, err
}

// restoreRealPath 把镜像相对路径还原为真实绝对路径：
//   - Unix：镜像 rel "Users/x/a.go" → "/Users/x/a.go"；
//   - Windows：镜像 rel "C/foo/a.go" → "C:\foo\a.go"（盘符为第一层）。
func restoreRealPath(rel string) string {
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) >= 2 && len(parts[0]) == 1 && parts[0][0] >= 'A' && parts[0][0] <= 'Z' {
		// Windows 盘符：C/foo → C:/foo
		return parts[0] + ":" + string(filepath.Separator) + filepath.Join(parts[1:]...)
	}
	return string(filepath.Separator) + filepath.Join(parts...)
}

// WithSandbox 把沙箱写入 context（工具层通过 FromContext 感知）。
func WithSandbox(ctx context.Context, fs *ShadowFS) context.Context {
	return context.WithValue(ctx, ctxKey{}, fs)
}

// FromContext 从 context 取沙箱（无则返回 nil）。
func FromContext(ctx context.Context) *ShadowFS {
	if ctx == nil {
		return nil
	}
	fs, _ := ctx.Value(ctxKey{}).(*ShadowFS)
	return fs
}
