package gui

// 拖拽调整大小 + 配置面板滚动 —— 前端契约回归测试。
//
// 行为测试见 cmd/contextloom-gui/tests/resizer_test.js（Node vm 加载真实
// app.js，模拟 pointer 事件，21 项断言：宽度/高度改变、钳制、持久化、
// 双击重置、resizer 显隐）；本测试在 go test 全量中做静态契约断言：
//   - app.js 存在通用 resizer 与三处初始化函数（防重构误删）
//   - index.html 存在三处分隔条（侧栏/编辑器分栏/Agent 面板）
//   - style.css 存在分隔条样式、body.resizing 光标、配置面板滚动

import (
	"strings"
	"testing"
)

func TestResizer_AppJS_FunctionsExist(t *testing.T) {
	src := loadAppJS(t)
	need := []string{
		"function makeResizer(",      // 通用拖拽工具（pointer capture + 钳制 + 持久化）
		"function initSideResizer(",  // 侧栏 ↔ 消息区 宽度拖拽
		"function initFeResizer(",    // 编辑器原文/预览 分栏拖拽
		"function initAgentResizer(", // Agent 面板任务区高度拖拽
		"sideResizerMax",             // 动态最大宽度（随窗口宽度）
	}
	for _, fn := range need {
		if !strings.Contains(src, fn) {
			t.Errorf("app.js 缺少 %s", fn)
		}
	}
	// 三处初始化必须在 init() 中被调用（否则功能不生效）
	for _, call := range []string{"initSideResizer()", "initFeResizer()", "initAgentResizer()"} {
		if !strings.Contains(src, call) {
			t.Errorf("app.js init() 未调用 %s", call)
		}
	}
}

func TestResizer_IndexHTML_Structure(t *testing.T) {
	html := loadWebFile(t, "index.html")
	for _, id := range []string{
		`id="sideResizer"`,  // 侧栏分隔条（leftCol 与 chatArea 之间）
		`id="feResizer"`,    // 编辑器分栏分隔条（edit-col 与 preview-col 之间）
		`id="agentResizer"`, // Agent 面板分隔条（agentTasks 与 goalPanel 之间）
	} {
		if !strings.Contains(html, id) {
			t.Errorf("index.html 缺少 %s", id)
		}
	}
	// 分隔条必须位于正确结构内（防止插错位置导致布局错乱）
	if !strings.Contains(html, `id="sideResizer" class="v-resizer"`) {
		t.Error("sideResizer 应为 v-resizer（横向拖拽）")
	}
	if !strings.Contains(html, `id="agentResizer" class="h-resizer"`) {
		t.Error("agentResizer 应为 h-resizer（纵向拖拽）")
	}
	if strings.Index(html, `id="leftCol"`) > strings.Index(html, `id="sideResizer"`) {
		t.Error("sideResizer 必须位于 leftCol 之后（chatArea 之前）")
	}
}

func TestResizer_StyleCSS_Classes(t *testing.T) {
	css := loadWebFile(t, "style.css")
	css = strings.ReplaceAll(css, "\r\n", "\n") // 归一化换行：style.css 可能 CRLF（Windows 检出）
	need := []string{
		".v-resizer {",    // 竖向分隔条
		".h-resizer {",    // 水平分隔条
		"body.resizing-x", // 横向拖拽光标
		"body.resizing-y", // 纵向拖拽光标
		"#feResizer.on",   // 分栏模式才显示
		"#panelConfig { padding: 0; overflow-y: auto;",           // 配置面板可滚动（问题1核心）
		"#cfgToolbar {\n  position: sticky; top: 0; z-index: 5;", // cfg toolbar sticky on scroll
	}
	for _, s := range need {
		if !strings.Contains(css, s) {
			t.Errorf("style.css 缺少 %q", s)
		}
	}
}
