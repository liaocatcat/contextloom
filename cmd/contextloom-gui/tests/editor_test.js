// 编辑器纯函数行为测试（仿业界主流桌面编码 Agent：双栏/同步滚动/行号/高亮/查找替换）
// 运行: node cmd/contextloom-gui/tests/editor_test.js
// 从生产 app.js 截取"纯函数区"（注释边界之间，无 DOM 依赖），在 vm 沙箱中
// 执行并断言行为契约——与运行时代码 100% 一致，杜绝"改前端忘回归"。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");
const start = src.indexOf("// ── 纯函数（可被 Node/Go 契约测试断言） ──");
const end = src.indexOf("// ── 编辑器组件 ──");
if (start < 0 || end < 0 || end <= start) {
  console.error("FAIL: 纯函数边界注释缺失（app.js 结构变化？）");
  process.exit(1);
}
const ctx = {};
vm.createContext(ctx);
vm.runInContext(src.slice(start, end), ctx);

let pass = 0, fail = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.log("  ❌ " + name + " → got " + g + " want " + w); }
}
function ok(name, cond) { eq(name, !!cond, true); }
function has(name, s, sub) { eq(name, s.includes(sub), true); }

// ── feIsMarkdown ──
eq("feIsMarkdown .md", ctx.feIsMarkdown("a.md"), true);
eq("feIsMarkdown .markdown", ctx.feIsMarkdown("docs/README.markdown"), true);
eq("feIsMarkdown .MD 大写", ctx.feIsMarkdown("A.MD"), true);
eq("feIsMarkdown .mdx", ctx.feIsMarkdown("x.mdx"), true);
eq("feIsMarkdown .txt", ctx.feIsMarkdown("a.txt"), false);
eq("feIsMarkdown 空", ctx.feIsMarkdown(""), false);

// ── feLangOf ──
eq("feLangOf go", ctx.feLangOf("main.go"), "go");
eq("feLangOf py", ctx.feLangOf("x.py"), "python");
eq("feLangOf js", ctx.feLangOf("app.js"), "js");
eq("feLangOf json", ctx.feLangOf("cfg.json"), "json");
eq("feLangOf yaml", ctx.feLangOf("c.yaml"), "yaml");
eq("feLangOf md", ctx.feLangOf("README.md"), "markdown");
eq("feLangOf 未知短后缀", ctx.feLangOf("x.xyz"), "xyz");
eq("feLangOf 空", ctx.feLangOf(""), "text");

// ── highlightLine 单行 ──
const hl = ctx.highlightLine;
has("行注释着色", hl("// comment", "go", {}).html, "hl-com");
has("关键字着色", hl("func main() {", "go", {}).html, "hl-kw");
has("括号不着色", hl("func main() {", "go", {}).html, "()");
has("字符串着色", hl('s := "hello"', "go", {}).html, "hl-str");
has("数字着色", hl("x := 42", "go", {}).html, "hl-num");
const b1 = hl("/* start", "go", {});
ok("块注释开启状态", b1.st.block === "com");
has("块注释着色", b1.html, "hl-com");
const b2 = hl("end */", "go", b1.st);
eq("块注释关闭状态", b2.st.block, false);
const t1 = hl("x := `tpl", "go", {});
eq("模板串跨行状态", t1.st.block, "str");
const t2 = hl("more `", "go", t1.st);
eq("模板串闭合状态", t2.st.block, false);
const esc1 = hl("<script>", "js", {});
ok("HTML 转义防注入", !esc1.html.includes("<script>"));
eq("Python 井号注释", hl("# note", "python", {}).html.includes("hl-com"), true);
eq("SQL 行注释", hl("-- note", "sql", {}).html.includes("hl-com"), true);

// ── highlightSource 整文件（跨行状态） ──
const hs = ctx.highlightSource("func a() {\n// note\n}", "go");
ok("整文件含关键字", hs.includes("hl-kw"));
ok("整文件含注释", hs.includes("hl-com"));
const hs2 = ctx.highlightSource("/* a\nb */\nx", "go");
ok("跨行块注释整体着色", hs2.includes('<span class="hl-com">/* a\nb */</span>'));
const hs3 = ctx.highlightSource("s := `a\nb`\nz", "go");
ok("跨行模板串整体着色", hs3.includes('<span class="hl-str">`a\nb`</span>'));

// ── feSyncRatio 同步滚动比例 ──
eq("比例 50%", ctx.feSyncRatio(50, 200, 100, 300, 100), 100);
eq("比例 0", ctx.feSyncRatio(0, 200, 100, 300, 100), 0);
eq("底部 100%", ctx.feSyncRatio(100, 200, 100, 300, 100), 200);
eq("src 不可滚动", ctx.feSyncRatio(10, 100, 100, 300, 100), 0);
eq("dst 不可滚动", ctx.feSyncRatio(50, 200, 100, 100, 100), 0);

// ── feExtractAnchors 标题锚点 ──
const anchors = ctx.feExtractAnchors("# T\n## S\ntext\n### X");
eq("锚点数", anchors.length, 3);
eq("锚点 level", anchors.map(a => a.level), [1, 2, 3]);
eq("锚点 line", anchors.map(a => a.line), [0, 1, 3]);
eq("锚点 title", anchors.map(a => a.title), ["T", "S", "X"]);
eq("非标题不误提取", ctx.feExtractAnchors("a # b\n###\ncode # x").length, 0);

// ── fePosOf 行列 ──
eq("pos 首行", ctx.fePosOf("hello", 3), { line: 1, col: 4 });
eq("pos 二行", ctx.fePosOf("ab\ncd", 4), { line: 2, col: 2 });
eq("pos 行尾", ctx.fePosOf("ab\n", 3), { line: 2, col: 1 });
eq("pos 空串", ctx.fePosOf("", 0), { line: 1, col: 1 });

// ── feFindMatches 查找匹配 ──
eq("匹配 3 个", ctx.feFindMatches("aXaXa", "a").map(m => m.start), [0, 2, 4]);
eq("无匹配", ctx.feFindMatches("abc", "z").length, 0);
eq("空词", ctx.feFindMatches("abc", "").length, 0);
eq("重叠不重复", ctx.feFindMatches("aaa", "aa").length, 1);

// ── 软换行几何（feCharCols/feCharWidth/feEstimateWrap/fePosInLine/feLogicLineAt） ──
eq("charCols ASCII", ctx.feCharCols(65), 1);
eq("charCols CJK", ctx.feCharCols(0x4E2D), 2);       // 中
eq("charCols 全角", ctx.feCharCols(0xFF01), 2);       // ！
eq("charCols emoji", ctx.feCharCols(0x1F600), 2);     // 😀
eq("charCols 制表符", ctx.feCharCols(9), 1);           // tab 基础宽（对齐在 feCharWidth）
eq("charWidth tab 偶数列", ctx.feCharWidth(9, 0), 2);
eq("charWidth tab 奇数列", ctx.feCharWidth(9, 3), 1);  // 对齐到 4
eq("charWidth ASCII", ctx.feCharWidth(97, 0), 1);

// 单行渲染行数：宽 10 列，25 个 ASCII → 3 行（10+10+5）
eq("wrap 纯 ASCII 拆 3 行", ctx.feEstimateWrap("abcdefghijklmnopqrstuvwxy", 10), 3);
eq("wrap 短行 1 行", ctx.feEstimateWrap("abc", 10), 1);
eq("wrap 空行 1 行", ctx.feEstimateWrap("", 10), 1);
// 中文按 2 列：5 个汉字 = 10 列正好一行
eq("wrap CJK 正好一行", ctx.feEstimateWrap("一二三四五", 10), 1);
eq("wrap CJK 超宽拆行", ctx.feEstimateWrap("一二三四五六", 10), 2);
// 混合：4 汉字(8列) + 2 ASCII(2列) = 10 列一行
eq("wrap 混合正好一行", ctx.feEstimateWrap("一二三四ab", 10), 1);
eq("wrap 混合拆行", ctx.feEstimateWrap("一二三四abx", 10), 2);
// 超长单词（不换行单词仍按列拆，textarea soft wrap 亦拆长串）
eq("wrap 超长串拆行", ctx.feEstimateWrap("x".repeat(35), 10), 4);

// fePosInLine：行内偏移 → {渲染行, 列}
eq("posInLine 首段", ctx.fePosInLine("abcdefghijklmnopqrstuvwxy", 0, 8, 10), { row: 0, col: 8 });
eq("posInLine 换行后", ctx.fePosInLine("abcdefghijklmnopqrstuvwxy", 0, 13, 10), { row: 1, col: 3 });
eq("posInLine 第三段", ctx.fePosInLine("abcdefghijklmnopqrstuvwxy", 0, 25, 10), { row: 2, col: 5 });
eq("posInLine CJK", ctx.fePosInLine("一二三四五六", 0, 4, 10), { row: 0, col: 8 });   // 4 汉字 = 8 列
eq("posInLine CJK 换行", ctx.fePosInLine("一二三四五六", 0, 6, 10), { row: 1, col: 2 }); // 第 6 字放不下 → 下一行
eq("posInLine 行内偏移", ctx.fePosInLine("abc\ndef", 4, 2, 10), { row: 0, col: 2 });

// feLogicLineAt：渲染行 → 逻辑行（cum 二分）
const info = { cum: [0, 3, 5, 6] }; // 行0占3渲染行、行1占2、行2占1、行3占1
eq("logicLineAt 0", ctx.feLogicLineAt(info, 0), 0);
eq("logicLineAt 2", ctx.feLogicLineAt(info, 2), 0);
eq("logicLineAt 3", ctx.feLogicLineAt(info, 3), 1);
eq("logicLineAt 4", ctx.feLogicLineAt(info, 4), 1);
eq("logicLineAt 5", ctx.feLogicLineAt(info, 5), 2);
eq("logicLineAt 6", ctx.feLogicLineAt(info, 6), 3);
eq("logicLineAt 超界", ctx.feLogicLineAt(info, 99), 3);
eq("logicLineAt 空", ctx.feLogicLineAt({ cum: [] }, 5), 0);

console.log(`\n编辑器纯函数测试: ${pass} 通过 / ${fail} 失败`);
process.exit(fail ? 1 : 0);
