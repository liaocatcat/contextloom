package gui

// 配置文件设置面板（config.get/config.set schema 驱动表单）前端契约测试。
// 行为链路：config.get → 渲染表单 → 收集变更 → config.set（见 settings_test.go 后端测试）。

import (
	"strings"
	"testing"
)

// TestSettings_AppJS_FunctionsExist 验证设置面板关键函数/结构存在（防重构误删）。
func TestSettings_AppJS_FunctionsExist(t *testing.T) {
	src := loadAppJS(t)
	for _, fn := range []string{
		"loadConfigSettings", "renderConfigSettings", "renderCfgField",
		"saveConfigSettings", "resetConfigSettings", "updateCfgSaveState",
	} {
		if !strings.Contains(src, "function "+fn+"(") {
			t.Errorf("app.js 缺少函数 %s()", fn)
		}
	}
	// 状态字段
	for _, f := range []string{"cfgValues", "cfgSchema", "cfgPath"} {
		if !strings.Contains(src, f+":") {
			t.Errorf("app.js 缺少状态字段 %s", f)
		}
	}
	// 关键行为：schema 驱动、password 脱敏跳过、diff 收集
	for _, frag := range []string{
		`req("config.get", {}`,     // 加载
		`req("config.set", patch)`, // 保存
		`f.type === "password"`,    // 密码字段
		`s.includes("****")`,       // 脱敏值跳过
		`f.type === "bool"`,        // 开关
		`f.type === "select"`,      // 下拉
		`data-cfgkey`,              // 控件标识
		`loadConfigSettings();`,    // 面板加载接入
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("app.js 缺少关键实现片段: %q", frag)
		}
	}
}

// TestSettings_IndexHTML_Structure 验证 index.html 设置面板结构。
func TestSettings_IndexHTML_Structure(t *testing.T) {
	html := loadWebFile(t, "index.html")
	for _, id := range []string{"cfgYaml", "cfgYamlForm", "cfgYamlSave", "cfgYamlReset", "cfgYamlHint", "cfgYamlPath"} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html 缺少 #%s", id)
		}
	}
	// 设置区在配置面板内
	if !strings.Contains(html, `<div id="panelConfig"`) || !strings.Contains(html, `id="cfgYaml"`) {
		t.Error("设置表单应位于配置面板 panelConfig 内")
	}
}

// TestSettings_StyleCSS_Classes 验证设置面板样式类存在。
func TestSettings_StyleCSS_Classes(t *testing.T) {
	css := loadWebFile(t, "style.css")
	for _, cls := range []string{".cfg-yaml-group", ".cfg-yaml-row", ".cfg-yaml-k", ".cfg-yaml-ctl",
		".cfg-yaml-desc", ".cfg-switch", ".cfg-switch-slider", ".cfg-yaml-actions", ".cfg-yaml-hint", ".cfg-yaml-path"} {
		if !strings.Contains(css, cls) {
			t.Errorf("style.css 缺少样式类 %s", cls)
		}
	}
}
