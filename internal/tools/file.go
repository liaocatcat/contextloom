package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/contextloom/contextloom-agent/internal/sandbox"
)

func ReadFile(ctx context.Context, params map[string]any) (any, error) {
	path, _ := params["path"].(string)
	if path == "" {
		return nil, os.ErrInvalid
	}
	resolved, err := cleanPathIn(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败 %s: 路径解析错误: %w", path, err)
	}
	// 沙箱模式：镜像存在则读镜像（无则回退真实）
	if fs := sandbox.FromContext(ctx); fs != nil {
		resolved, _ = fs.ResolveRead(resolved)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败 %s: %w", path, err)
	}

	content := string(data)
	lines := splitLines(content)
	offset := 0
	limit := len(lines)

	if o, ok := params["offset"].(float64); ok {
		offset = int(o)
	}
	if l, ok := params["limit"].(float64); ok && l > 0 {
		limit = offset + int(l)
	}
	if offset < 0 {
		offset = 0
	}
	if offset > len(lines) {
		offset = len(lines)
	}
	if limit > len(lines) {
		limit = len(lines)
	}
	if limit < offset {
		limit = offset
	}

	result := joinLines(lines[offset:limit])
	return map[string]any{
		"content":     result,
		"total_lines": len(lines),
	}, nil
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	lines := make([]string, 0)
	current := ""
	for _, ch := range s {
		if ch == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	lines = append(lines, current)
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, line := range lines {
		if i > 0 {
			result += "\n"
		}
		result += line
	}
	return result
}

func WriteFile(ctx context.Context, params map[string]any) (any, error) {
	path, _ := params["path"].(string)
	content, _ := params["content"].(string)
	resolved, err := cleanPathIn(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("写入文件失败 %s: 路径解析错误: %w", path, err)
	}
	// 沙箱模式：写操作重定向到影子文件系统（镜像），不触碰真实文件
	if fs := sandbox.FromContext(ctx); fs != nil {
		resolved = fs.ResolveWrite(resolved)
	}
	// 先确保父目录存在（Windows 上 WriteFile 不会自动创建父目录）
	if dir := filepath.Dir(resolved); dir != "." && dir != "/" {
		if fi, err := os.Stat(dir); err != nil {
			if os.IsNotExist(err) {
				os.MkdirAll(dir, 0755)
			} else {
				// Stat 失败但目录可能仍存在（如权限临时不可读），尝试 MkdirAll 并继续
				os.MkdirAll(dir, 0755)
			}
		} else if !fi.IsDir() {
			return nil, fmt.Errorf("路径不是目录: %s", dir)
		}
	}
	// Windows 上预检只读属性并移除（os.WriteFile 在只读文件上会失败）
	if runtime.GOOS == "windows" {
		if fi, err := os.Stat(resolved); err == nil && fi.Mode()&0200 == 0 {
			os.Chmod(resolved, 0666)
		}
	}
	err = os.WriteFile(resolved, []byte(content), 0666)
	if err != nil {
		return nil, fmt.Errorf("写入文件失败 %s: %w", path, err)
	}
	// 返回绝对路径：模型/用户可直接核对落盘位置（防"以为写在项目里、
	// 实际写到别处"的信息不对称）
	return map[string]any{"written": len(content), "path": resolved}, nil
}

func ReplaceInFile(ctx context.Context, params map[string]any) (any, error) {
	path, _ := params["path"].(string)
	oldStr, _ := params["old_str"].(string)
	newStr, _ := params["new_str"].(string)

	if path == "" || oldStr == "" {
		return nil, os.ErrInvalid
	}
	// 纯文件名（无分隔符且非 ./ 开头）同样合法：锚定项目根解析，与 write_file 一致

	resolved, err := cleanPathIn(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("替换文件失败 %s: 路径解析错误: %w", path, err)
	}
	// 沙箱模式：读镜像（有则读镜像）+ 写镜像（重定向到真实路径对应的镜像）
	realPath := resolved
	var mirror string
	if fs := sandbox.FromContext(ctx); fs != nil {
		resolved, _ = fs.ResolveRead(resolved)
		mirror = fs.MirrorPath(realPath)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("读取文件失败 %s: %w", path, err)
	}

	content := string(data)
	if !strings.Contains(content, oldStr) {
		return nil, os.ErrNotExist
	}

	content = strings.Replace(content, oldStr, newStr, 1)
	if mirror != "" {
		resolved = mirror // 写回镜像（沙箱模式）
	}
	if dir := filepath.Dir(resolved); dir != "." && dir != "/" {
		os.MkdirAll(dir, 0755)
	}
	// Windows 上预检只读属性并移除
	if runtime.GOOS == "windows" {
		if fi, err := os.Stat(resolved); err == nil && fi.Mode()&0200 == 0 {
			os.Chmod(resolved, 0666)
		}
	}
	if err := os.WriteFile(resolved, []byte(content), 0666); err != nil {
		return nil, fmt.Errorf("写入文件失败 %s: %w", path, err)
	}
	return map[string]any{"replaced": true, "size": len(content), "path": resolved}, nil
}
