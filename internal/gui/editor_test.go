package gui

// 编辑器（仿业界主流桌面编码 Agent：原文/预览双栏 + 同步滚动 + 行号 + 高亮 + 查找替换）
// 前端契约回归测试。
//
// 行为测试见 cmd/contextloom-gui/tests/editor_test.js（Node vm 执行生产 app.js
// 纯函数区，49 项断言）；本测试在 go test 全量中做静态契约断言：
//   - 关键函数/常量存在于 app.js（防止重构误删导致前端运行时 ReferenceError）
//   - index.html 编辑器结构齐全（双栏/gutter/查找框/状态栏）
//   - style.css 关键样式类存在（透明层高亮、查找层、预览排版）
//   - 关键行为实现存在（同步滚动公式、标题锚点、视图切换、保存链路）

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadWebFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "cmd", "contextloom-gui", "web", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("读取 %s: %v", name, err)
	}
	return string(b)
}

func TestEditor_AppJS_FunctionsExist(t *testing.T) {
	src := loadAppJS(t)
	need := []string{
		"feIsMarkdown", "feLangOf", "highlightLine", "highlightSource",
		"feSyncRatio", "feExtractAnchors", "fePosOf", "feFindMatches",
		"curFileTab", "feRenderHighlight", "feRenderGutter", "feRenderPreview",
		"feUpdateStatus", "feApplyView", "feToggleFind", "feFind", "feFindNav",
		"feReplace", "feReplaceAll", "feInsertTab", "initFileEditor",
		"renderFileEditor", "hideFileEditor", "saveFileTab", "feScrollToLine",
		"feSyncPreviewFromEdit", "feSyncEditFromPreview", "markDirty",
	}
	for _, fn := range need {
		if !strings.Contains(src, "function "+fn+"(") {
			t.Errorf("app.js 缺少函数 %s()", fn)
		}
	}
	// 常量
	for _, c := range []string{"HL_KEYWORDS", "HL_COMMENTS"} {
		if !strings.Contains(src, "const "+c) {
			t.Errorf("app.js 缺少常量 %s", c)
		}
	}
	// 纯函数区边界注释（editor_test.js 依赖）
	if !strings.Contains(src, "// ── 纯函数（可被 Node/Go 契约测试断言） ──") {
		t.Error("app.js 缺少纯函数区起始边界注释")
	}
	if !strings.Contains(src, "// ── 编辑器组件 ──") {
		t.Error("app.js 缺少纯函数区结束边界注释")
	}
	// 关键行为实现
	for _, frag := range []string{
		"srcST / (srcSH - srcCH)",                     // 同步滚动比例公式
		`/^(#{1,4})\s+(.+?)\s*#*\s*$/`,                // 标题锚点提取
		"ta.value.length > 200000",                    // 大文件高亮降级
		"total > 5000",                                // 大文件行号可视区渲染
		`e.key === "Tab"`,                             // Tab 缩进
		"(e.metaKey || e.ctrlKey) && e.key === \"f\"", // Cmd/Ctrl+F 查找
		"feViewSplit", "feViewEdit", "feViewPrev",     // 视图切换按钮
		"feFindInput", "feReplaceInput", // 查找/替换输入
		"state.fileViews[tab.id]", // 视图模式 per-file 记忆
		"req(\"fs.write\"",        // 保存走后端 API
	} {
		if !strings.Contains(src, frag) {
			t.Errorf("app.js 缺少关键实现片段: %q", frag)
		}
	}
}

func TestEditor_IndexHTML_Structure(t *testing.T) {
	html := loadWebFile(t, "index.html")
	// 编辑器整体与核心部件
	for _, id := range []string{
		"fileEditor", "feViewGroup", "feFindBox", "feGutter", "feTextWrap",
		"feHighlight", "feFindLayer", "feText", "feEditCol", "fePreviewCol",
		"fePreview", "feToc", "fePos", "feSize", "feLang", "feMode",
		"feSave", "feClose", "feFindInput", "feReplaceInput",
		"feFindPrev", "feFindNext", "feReplaceBtn", "feReplaceAllBtn", "feFindClose",
	} {
		if !strings.Contains(html, `id="`+id+`"`) {
			t.Errorf("index.html 缺少 #%s", id)
		}
	}
	// 关键结构顺序：编辑器位于 msgList 之前（打开文件时替代聊天区）
	ei := strings.Index(html, `id="fileEditor"`)
	mi := strings.Index(html, `id="msgList"`)
	if ei < 0 || mi < 0 || ei > mi {
		t.Errorf("fileEditor 应在 msgList 之前（ei=%d mi=%d）", ei, mi)
	}
}

func TestEditor_StyleCSS_Classes(t *testing.T) {
	css := loadWebFile(t, "style.css")
	for _, cls := range []string{
		".fe-toolbar", ".fe-body", ".fe-edit-col", ".fe-gutter", ".fe-text-wrap",
		".fe-highlight", ".fe-find-layer", ".fe-preview-col", ".fe-preview",
		".fe-status", ".fe-find", ".fe-toc", ".fe-view-btn", ".fe-view-btn.active",
		".hl-kw", ".hl-str", ".hl-com", ".hl-num", ".hl-op",
		".fm.active", ".fe-preview h1", ".fe-preview pre", ".fe-preview blockquote",
		".fe-preview table", ".fe-preview code", ".file-editor.no-hl #feText",
		"#feText", "caret-color",
	} {
		if !strings.Contains(css, cls) {
			t.Errorf("style.css 缺少样式 %s", cls)
		}
	}
	// 透明层高亮方案关键属性（textarea 透明文字 + 可见光标）
	if !strings.Contains(css, "color: transparent;") {
		t.Error("style.css 缺少 textarea 透明文字（高亮层方案核心）")
	}
	if !strings.Contains(css, "caret-color: var(--accent);") {
		t.Error("style.css 缺少 caret-color（透明文字下光标可见）")
	}
	// 预览列默认隐藏（非分栏时）
	if !strings.Contains(css, ".fe-preview-col {") || !strings.Contains(css, "display: none;") {
		t.Error("style.css .fe-preview-col 应默认 display:none")
	}
}
