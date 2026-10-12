// PlantUML Markdown 预览测试（对标业界主流桌面编码 Agent 的 EUI：plantuml 代码块 → 代理渲染）
// 运行: node cmd/contextloom-gui/tests/plantuml_test.js
// 从生产 app.js 截取 Markdown 渲染函数区（esc/highlightCode/langOf/mdToHTML/
// plantumlPreviewCfg/jsStr），在 vm 沙箱执行并断言契约——与运行时代码一致。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");
// esc 定义于"消息渲染"区（892 行），Markdown 区依赖它——起点取消息渲染区
const start = src.indexOf("/* ═══════════ 消息渲染 ═══════════ */");
const end = src.indexOf("/* ═══════════ 流式 ═══════════ */");
if (start < 0 || end < 0 || end <= start) {
  console.error("FAIL: Markdown 区边界注释缺失（app.js 结构变化？）");
  process.exit(1);
}
const ctx = { state: {} }; // plantumlPreviewCfg 依赖 state.cfgValues
vm.createContext(ctx);
vm.runInContext(src.slice(start, end), ctx);

let pass = 0, fail = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.log("  ❌ " + name + " → got " + g + " want " + w); }
}
function ok(name, cond) { eq(name, !!cond, true); }
function has(name, s, sub) { eq(name, String(s).includes(sub), true); }
function notHas(name, s, sub) { eq(name, String(s).includes(sub), false); }

// ── plantumlPreviewCfg 默认/配置读取 ──
const def = ctx.plantumlPreviewCfg();
eq("默认 server", def.server, "https://www.plantuml.com/plantuml");
eq("默认 enabled", def.enabled, true);
ctx.state.cfgValues = {
  "preview.plantuml_server": "http://127.0.0.1:8080/plantuml",
  "preview.plantuml_enabled": true,
};
const cfg = ctx.plantumlPreviewCfg();
eq("配置 server", cfg.server, "http://127.0.0.1:8080/plantuml");
eq("配置 enabled", cfg.enabled, true);
ctx.state.cfgValues["preview.plantuml_enabled"] = false;
eq("显式 false 禁用", ctx.plantumlPreviewCfg().enabled, false);
ctx.state.cfgValues["preview.plantuml_enabled"] = true;
ctx.state.cfgValues["preview.plantuml_server"] = "https://www.plantuml.com/plantuml";

// ── jsStr 转义 ──
eq("jsStr 引号转义", ctx.jsStr("a'b\"c"), "a\\'b&quot;c");
eq("jsStr 换行转义", ctx.jsStr("a\nb"), "a\\nb");
eq("jsStr 反斜杠转义", ctx.jsStr("a\\b"), "a\\\\b");
eq("jsStr & 转义", ctx.jsStr("a&b"), "a&amp;b");
eq("jsStr 实体形态文本不变异", ctx.jsStr("&lt;"), "&amp;lt;");

// ── mdToHTML：plantuml 代码块 → 代理 img（源码原文编码，不被 esc 污染） ──
const PU = "@startuml\nAlice -> Bob: hello\n@enduml";
const md = "```plantuml\n" + PU + "\n```";
const out = ctx.mdToHTML(md);
has("生成 plantuml-block", out, "plantuml-block");
has("生成代理 img", out, 'src="/api/plantuml/svg?src=');
has("源码 URL 编码（原文 > 不被转义）", out, encodeURIComponent(PU));
has("img 懒加载", out, "loading=\"lazy\"");
has("onerror 兜底绑定", out, "onerror=\"plantumlFallback(this,'");
has("源码折叠 details", out, "plantuml-src");
has("源码折叠含源码", out, "PlantUML 源码");
notHas("不生成普通 pre 代码块", out, 'data-lang="plantuml"');

// ── mdToHTML：puml 别名 ──
const outPuml = ctx.mdToHTML("```puml\nA -> B\n```");
has("puml 别名也渲染", outPuml, "plantuml-block");

// ── mdToHTML：禁用时按普通代码块展示 ──
ctx.state.cfgValues["preview.plantuml_enabled"] = false;
const outDis = ctx.mdToHTML(md);
notHas("禁用不生成 img", outDis, "/api/plantuml/svg");
has("禁用走普通代码块", outDis, "<pre");
ctx.state.cfgValues["preview.plantuml_enabled"] = true;

// ── mdToHTML：普通代码块不受影响 ──
const outGo = ctx.mdToHTML("```go\nfunc main() {}\n```");
notHas("go 代码块不渲染 plantuml", outGo, "plantuml-block");
has("go 代码块正常高亮", outGo, '<span style="color:');
has("go 代码块有 pre", outGo, "<pre");

// ── mdToHTML：plantuml 与正文混排 ──
const mix = ctx.mdToHTML("# 标题\n\n```plantuml\nA -> B\n```\n\n正文段落");
has("混排保留标题", mix, "<h1>标题</h1>");
has("混排保留段落", mix, "<p>正文段落</p>");
has("混排保留图", mix, "plantuml-block");

// ── 裸 @startuml 块（无代码围栏）：流程图类 md 常见写法 ──
// 回归：修复前只识别 ```plantuml 围栏，裸 @startuml...@enduml 被当普通文本
// 输出原文，不渲染图（用户流程图.md 渲染失败根因）。
const mdBare = "管理端流程图\n@startuml\ntitle 示例流程\nstart\n:登录;\nstop\n@enduml";
const outBare = ctx.mdToHTML(mdBare);
ok("裸 @startuml 块生成 plantuml-block", outBare.includes("plantuml-block"));
ok("裸 @startuml 块生成代理 img", outBare.includes("/api/plantuml/svg?src="));
// 源码只允许出现在折叠 details 内（plantuml-src），不得残留为普通文本段：
// 正文段落不含 @startuml，且 @startuml 出现次数 = 1（details 源码区内）。
ok("裸 @startuml 不残留为普通文本段", !/<p>[^<]*@startuml/.test(outBare));
// @startuml 恰好 2 处：details 源码折叠区 1 + onerror 兜底属性 1（均在块内）
eq("裸 @startuml 源码仅存在于块内（details+onerror）",
  (outBare.match(/@startuml/g) || []).length, 2);
ok("裸 @startuml 源码折叠 details 保留源码", outBare.includes("plantuml-src"));
// 混合：围栏 + 裸块 同时存在
const mdMix = "```plantuml\nA -> B\n```\n\n简介\n@startuml\nC -> D\n@enduml";
const outMix = ctx.mdToHTML(mdMix);
eq("围栏+裸块 共生成 2 个 plantuml-block",
  (outMix.match(/plantuml-block/g) || []).length, 2);
// 正文文本不以 @startuml 开头时不受影响（按普通段落渲染）
eq("普通文本不受影响", ctx.mdToHTML("正常段落 @startuml 提到"),
  "<p>正常段落 @startuml 提到</p>");

// ── 未闭合 plantuml 代码块不 panic ──
const unclosed = ctx.mdToHTML("```plantuml\nA -> B\n");
ok("未闭合代码块不抛异常", typeof unclosed === "string");

// ── onerror 属性完整性：源码含双引号（participant "Name"/title "..." 常见）──
// 回归：jsStr 未转义 " 时 onerror="...'participant "A"'..." 属性被裸 " 截断、
// JS 字符串不闭合 → DOM 损坏 + 兜底失效（修复前本组测试 FAIL）。
const mdQuote = "```plantuml\n@startuml\nparticipant \"First Component\" as A\ntitle \"Demo\"\n@enduml\n```";
const outQuote = ctx.mdToHTML(mdQuote);
ok("含双引号源码仍生成 plantuml-block", outQuote.includes("plantuml-block"));
// 属性完整：onerror 属性值中不允许出现裸 "（截断标志）
const oeQuote = outQuote.match(/onerror="([^"]*)"/);
ok("onerror 属性不被截断", !!oeQuote);
eq("onerror 属性完整闭合", oeQuote ? oeQuote[1].endsWith("')") : false, true);
// JS 字符串内含引号形态正确（&quot; 经 HTML 解码后为 "，单引号字符串内无害）
// 源码引号以实体形式嵌入（onerror 属性层 &quot; → JS 字符串层 "）
has("源码引号以实体形式嵌入", outQuote, "&quot;");
const decQuote = (oeQuote ? oeQuote[1] : "").replace(/&quot;/g, '"');
ok("属性内含完整源码（含引号）", decQuote.includes('participant "First Component" as A'));
// 纯文本引号编码不破坏 img src（URL 编码路径）
has("img src 仍为 URL 编码", outQuote, "src=\"/api/plantuml/svg?src=");

// ── jsStr 单测：双引号/单引号/反斜杠/换行 全形态 ──
eq("jsStr 双引号转实体", ctx.jsStr('a"b'), 'a&quot;b');
eq("jsStr 单引号保留转义", ctx.jsStr("a'b"), "a\\'b");
eq("jsStr 混合形态", ctx.jsStr("a\"b'c\\d\ne"), 'a&quot;b\\\'c\\\\d\\ne');

// ── plantumlFallback 兜底源码（esc 已转义全部五字符，details 内安全）──
const fb = ctx.renderPlantUMLBlock("@startuml\nA <-> B: \"x\"\n@enduml");
has("兜底 details 含源码", fb, "plantuml-src");
has("details 内源码转义 <", fb, "&lt;-&gt;");
has("onerror 属性含源码原文（属性值内裸 < 合法）", fb, "A <-> B");
eq("jsStr 混合形态", ctx.jsStr("a\"b'c\\d\ne&f"), 'a&quot;b\\\'c\\\\d\\ne&amp;f');

// ── highlightCode 对 plantuml 源码安全（兜底路径用空语言） ──
const hlSafe = ctx.highlightCode("@startuml\nAlice -> Bob\n@enduml", "");
has("高亮兜底不丢内容", hlSafe, "Alice");
const hlCom = ctx.highlightCode("# 注释\nAlice", "");
has("注释行着色", hlCom, "#5b6287");

console.log(`\n${pass} passed, ${fail} failed`);
process.exit(fail === 0 ? 0 : 1);
