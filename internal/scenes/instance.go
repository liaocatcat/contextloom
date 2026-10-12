package scenes

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Instance 场景实例化产物：Scene 快照 + 触发上下文（Params 模板变量）+ 渲染后的产出路径。
type Instance struct {
	Scene Scene // 场景快照（供 Runner 装配员工/模型/工具链/审批档）
	// Params 模板变量表：date（YYYY-MM-DD）、time（HH:MM:SS）、user_input（触发原话）。
	Params map[string]string
	// OutputPath 为 output.path 渲染 + 安全校验后的相对路径；空串表示场景未配置产出。
	OutputPath string
}

// Instantiate 由场景与触发上下文构造实例：
//
//  1. Params 注入 {{date}}/{{time}}/{{user_input}}；
//  2. output.path 做模板渲染（{{date}} 替换为当日日期等）；
//  3. 渲染后路径过安全校验（见 ValidateOutputPath），非法直接返回可读错误。
func Instantiate(scene Scene, userInput string, now time.Time) (*Instance, error) {
	params := map[string]string{
		"date":       now.Format("2006-01-02"),
		"time":       now.Format("15:04:05"),
		"user_input": userInput,
	}

	inst := &Instance{Scene: scene, Params: params}
	if scene.Output.Path != "" {
		rendered := RenderTemplate(scene.Output.Path, params)
		if err := ValidateOutputPath(rendered); err != nil {
			return nil, fmt.Errorf("scenes: 场景 %q 产出路径非法：%w", scene.Name, err)
		}
		inst.OutputPath = rendered
	}
	return inst, nil
}

// RenderTemplate 将 {{key}} 占位符替换为 params 中对应值；未收录的占位符原样保留
// （方便调用方后续自行二次渲染）。
func RenderTemplate(s string, params map[string]string) string {
	for k, v := range params {
		s = strings.ReplaceAll(s, "{{"+k+"}}", v)
	}
	return s
}

// ValidateOutputPath 校验渲染后的产出路径：
//
//   - 空串合法（表示场景未配置产出文件，不校验）；
//   - 拒绝绝对路径（Unix 与 Windows 盘符形式均拒绝，保证跨平台行为一致）；
//   - 拒绝 `..` 目录逃逸：反斜杠先统一归一为 "/"（与运行时 OS 无关），
//     再按 "/" 清理，清理后仍以 ".." 开头即非法。
//
// 返回可读错误，供调用方直接展示给用户。
func ValidateOutputPath(path string) error {
	if path == "" {
		return nil
	}
	if isAbsPath(path) {
		return fmt.Errorf("产出路径不允许绝对路径（%q）", path)
	}
	// 先统一分隔符（Windows 风格反斜杠在 Unix 上不会被 filepath.Clean 归一，
	// 直接 Clean 会漏掉 "docs/..\..\secret" 这类混合逃逸）。
	norm := strings.ReplaceAll(path, "\\", "/")
	cleaned := filepath.Clean(norm)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("产出路径不允许 .. 逃逸目录（%q）", path)
	}
	return nil
}

// isAbsPath 跨平台绝对路径判定（不依赖运行时 OS，三平台判定一致）。
func isAbsPath(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return true
	}
	// Windows 盘符形式：C:\x / C:/x
	if len(p) >= 2 && p[1] == ':' {
		c := p[0]
		return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	}
	return false
}
