package gui

// 多终端 + 打开其他目录 —— 前端契约回归测试。
//
// 行为测试见 cmd/contextloom-gui/tests/term_test.js（Node vm 加载真实 app.js）；
// 本测试在 go test 全量中做静态契约断言：
//   - app.js 存在终端全套函数与打开目录函数（防重构误删）
//   - index.html 存在终端面板结构、活动栏终端按钮、打开目录按钮
//   - style.css 存在终端样式

import (
	"strings"
	"testing"
)

func TestTerm_AppJS_FunctionsExist(t *testing.T) {
	src := loadAppJS(t)
	need := []string{
		"async function openTerm(",          // 新建终端（term.spawn + xterm 实例）
		"function initTermInstance(",        // xterm.js 实例创建（Terminal + FitAddon + onData）
		"function renderTermTabs(",          // 终端标签渲染（多窗口切换）
		"function termSwitch(",              // 切换当前终端（fit + buffer 恢复）
		"async function termClose(",         // 关闭终端（term.kill + dispose）
		"function handleTermOutput(",        // term.output 事件（原始 ANSI → term.write）
		"function handleTermExit(",          // term.exit 事件
		"function fitTerm(",                 // xterm fit 自适应
		"function fitAllTerms(",             // 窗口 resize 时全部终端自适应
		"function termShow(",                // 终端主区域视图显示
		"function termHide(",                // 终端主区域视图隐藏
		"async function renderProjectList(", // 项目管理器面板渲染（活动栏第一功能）
		"function pickProjectDir(",          // 目录选择器（以目录选中的形式引入项目）
		"async function switchProject(",     // 切换项目根（backend 重启 + 文件树跟随）
		"function baseName(",                // 项目路径 → 目录名
	}
	for _, fn := range need {
		if !strings.Contains(src, fn) {
			t.Errorf("app.js 缺少 %s", fn)
		}
	}
	// 事件分发必须接入（否则 term.output/exit 不生效）
	for _, call := range []string{
		`case "term.output":`, `case "term.exit":`,
		`handleTermOutput(env.data);`, `handleTermExit(env.data);`,
	} {
		if !strings.Contains(src, call) {
			t.Errorf("app.js 事件分发缺少 %q", call)
		}
	}
	// setPanel 必须处理 term 面板（主区域视图切换）
	if !strings.Contains(src, `if (panel === "term") {`) {
		t.Error("setPanel 缺少 term 面板分支")
	}
}

func TestTerm_IndexHTML_Structure(t *testing.T) {
	html := loadWebFile(t, "index.html")
	for _, id := range []string{
		`id="termPanel"`,        // 终端面板容器
		`id="termTabs"`,         // 终端标签栏
		`id="termBody"`,         // 终端内容区（xterm 挂载容器）
		`data-panel="term"`,     // 活动栏终端按钮
		`data-panel="projects"`, // 活动栏项目管理器按钮（功能第一）
		`id="panelProjects"`,    // 项目管理器侧栏面板
		`id="projList"`,         // 项目列表容器
		`id="projAdd"`,          // 添加项目（目录选择）按钮
	} {
		if !strings.Contains(html, id) {
			t.Errorf("index.html 缺少 %s", id)
		}
	}
	// 终端面板必须位于 chatArea 内（主区域视图，与消息区/编辑器互斥）
	chatIdx := strings.Index(html, `id="chatArea"`)
	termIdx := strings.Index(html, `id="termPanel"`)
	msgIdx := strings.Index(html, `id="msgList"`)
	if termIdx < 0 || chatIdx < 0 || msgIdx < 0 {
		t.Fatal("chatArea/termPanel/msgList 结构缺失")
	}
	if chatIdx > termIdx {
		t.Error("termPanel 必须位于 chatArea 内")
	}
	if termIdx < msgIdx {
		t.Error("termPanel 必须位于 msgList 之后（msgList 隐藏时终端接管主区域）")
	}
	// xterm.js 资源必须引入（vendor 内嵌，离线可用）
	for _, asset := range []string{
		`/vendor/xterm/xterm.css`,
		`/vendor/xterm/xterm.min.js`,
		`/vendor/xterm/xterm-addon-fit.min.js`,
	} {
		if !strings.Contains(html, asset) {
			t.Errorf("index.html 缺少 xterm 资源 %s", asset)
		}
	}
	// 旧输入行结构应已移除（xterm 接管输入）
	if strings.Contains(html, `id="termInput"`) {
		t.Error("index.html 不应再包含旧输入行 termInput（xterm onData 接管）")
	}
}

func TestTerm_StyleCSS_Classes(t *testing.T) {
	css := loadWebFile(t, "style.css")
	need := []string{
		"#termPanel {",        // 面板容器
		".term-bar {",         // 标签栏
		".term-tab {",         // 标签
		".term-tab.active {",  // 激活标签
		".term-body {",        // 内容区（xterm 挂载，overflow hidden）
		".term-view {",        // 单终端视图（display:none 默认，100% 撑满）
		".term-view .xterm {", // xterm 容器撑满
		"#panelProjects {",    // 项目管理器面板
		".proj-row {",         // 项目行（当前高亮/可点击切换）
		".proj-path {",        // 项目完整路径显示
		".browse-list {",      // 目录选择器列表
	}
	for _, s := range need {
		if !strings.Contains(css, s) {
			t.Errorf("style.css 缺少 %q", s)
		}
	}
}
