// Tab 栏移位（对话区域正上方/模式按钮右侧）+ 同步滚动防回环 + 图片加载补偿
// 回归测试。运行: node cmd/contextloom-gui/tests/tabs_scroll_test.js
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const WEB = path.join(__dirname, "..", "web");
const html = fs.readFileSync(path.join(WEB, "index.html"), "utf8");
const css = fs.readFileSync(path.join(WEB, "style.css"), "utf8");
const src = fs.readFileSync(path.join(WEB, "app.js"), "utf8");

let pass = 0, fail = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.log("  ❌ " + name + " → got " + g + " want " + w); }
}
function ok(name, cond) { eq(name, !!cond, true); }

console.log("── 1. 文件契约：tabBar 位于 topbar 模式按钮右侧（对话区域正上方） ──");
const modeEnd = html.indexOf('data-mode="goal"');
const tabIdx = html.indexOf('<div id="tabBar">');
const topStatusIdx = html.indexOf('<div id="topStatus">');
ok("契约: tabBar 在 modeSwitch(goal) 之后", tabIdx > modeEnd);
ok("契约: tabBar 在 topStatus 之前", tabIdx < topStatusIdx);
const leftColStart = html.indexOf('<div id="leftCol">');
const leftColTab = html.slice(leftColStart, leftColStart + 400).includes("tabBar");
ok("契约: leftCol 内不再包含 tabBar", !leftColTab);
const tabCss = css.slice(css.indexOf("#tabBar {"), css.indexOf("#tabBar::-webkit-scrollbar"));
ok("契约: CSS #tabBar flex:1（占满 topbar 剩余空间）", /\bflex:\s*1\b/.test(tabCss));
ok("契约: CSS #tabBar align-items:flex-end（tab 贴底连接内容区）", /align-items:\s*flex-end/.test(tabCss));
ok("契约: topbar 高度 48px 容纳 tab", css.includes("--topbar-h: 48px;"));
ok("契约: tab 上圆角下直角", /border-radius:\s*6px 6px 0 0/.test(css));
ok("契约: app.js 中 feSetScroll 定义", src.includes("function feSetScroll(el, v)"));
ok("契约: app.js 中 feConsumeExpect 定义", src.includes("function feConsumeExpect(el)"));
ok("契约: ta scroll 监听含 feConsumeExpect(ta)", src.includes("if (feConsumeExpect(ta)) return;"));
ok("契约: pv scroll 监听含 feConsumeExpect", src.includes('if (feConsumeExpect($("#fePreview"))) return;'));
ok("契约: feRenderPreview 调用 feStabilizePreview", src.includes("feStabilizePreview(pv);"));
// 缺陷回归：.fe-preview 必须 position:relative——否则预览内图片的 offsetParent
// 是 body（含 topbar/侧栏偏移），feStabilizePreview 的"图片顶部在视口上方"
// 判断（img.offsetTop <= p.scrollTop+1）坐标系不一致恒不成立，补偿永不触发。
const fePrevCss = css.slice(css.indexOf(".fe-preview {"), css.indexOf(".fe-preview h1"));
ok("契约: .fe-preview 含 position:relative（图片补偿坐标系）", /position:\s*relative/.test(fePrevCss));
// 缺陷回归：feExpect 期望值必须按元素记录（{el,v}）——原文/预览两栏交替
// 程序赋值时互不干扰（单槽位会被另一栏事件误消费/误清）。
ok("契约: feSetScroll 记录 per-element 期望值", /state\.feExpect\s*=\s*\{\s*el,\s*v\s*\}/.test(src));
ok("契约: feConsumeExpect 按元素匹配", src.includes("exp.el === el"));
ok("契约: 本地图占位 min-height", src.includes("min-height:18px;border-radius:8px"));
ok("契约: plantuml 图占位 min-height", src.includes('min-height:18px"'));

console.log("── 2. DOM stub 加载真实 app.js ──");
const els = new Map();
function makeEl(sel, extra = {}) {
  const el = {
    selector: sel,
    style: {},
    dataset: {},
    listeners: {},
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    querySelectorAll() { return []; },
    contains() { return true; },
    ...extra,
  };
  els.set(sel, el);
  return el;
}
function fire(el, type, ev = {}) {
  const fns = (el && el.listeners[type]) || [];
  for (const fn of fns) fn({ preventDefault() {}, key: "", ...ev });
}
const document = {
  querySelector: (s) => els.get(s) || null,
  querySelectorAll: () => [],
  createElement: () => ({ style: {}, dataset: {}, classList: { add() {}, remove() {}, toggle() {}, contains: () => false }, addEventListener() {}, appendChild() {}, querySelector: () => null, innerHTML: "", onclick: null }),
  addEventListener() {},
  body: { appendChild() {}, classList: { add() {}, remove() {} } },
};
const window = { innerWidth: 1280, addEventListener() {}, getComputedStyle: () => ({ lineHeight: "20px" }) };
const localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
const ctx = { document, window, localStorage, console, setTimeout, clearTimeout, requestAnimationFrame: (f) => setTimeout(f, 0), Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error, WebSocket: class { static OPEN = 1; } };
vm.createContext(ctx);
vm.runInContext(src, ctx);

console.log("── 3. 同步滚动防回环（用户滚动同步 / 程序回环吞掉） ──");
const ta = makeEl("#feText", { scrollTop: 0, scrollHeight: 4000, clientHeight: 500, value: "", selectionStart: 0, focus() {} });
const pv = makeEl("#fePreview", { scrollTop: 0, scrollHeight: 2000, clientHeight: 500 });
// 与生产 initFileEditor 一致的 scroll 监听绑定（契约测试保证代码一致）
ta.addEventListener("scroll", () => { if (ctx.state && ctx.state.feSyncing) return; if (ctx.feConsumeExpect(ta)) return; ctx.feSyncPreviewFromEdit(); });
pv.addEventListener("scroll", () => { if (ctx.state && ctx.state.feSyncing) return; if (ctx.feConsumeExpect(pv)) return; ctx.feSyncEditFromPreview(); });

// 用户滚动原文 → 预览按比例同步：1000/(4000-500)*(2000-500)=428.57 → round 429
ta.scrollTop = 1000;
fire(ta, "scroll");
eq("用户滚动原文 → 预览按比例同步", pv.scrollTop, 429);

// 程序赋值触发的 pv scroll 事件异步到达 → 期望值匹配 → 吞掉，原文不再被反向修改
fire(pv, "scroll");
eq("程序回环被吞: 原文 scrollTop 不变", ta.scrollTop, 1000);

// 用户再滚动预览 → 原文同步（正常路径不破坏）
pv.scrollTop = 900;
fire(pv, "scroll");
eq("用户滚动预览 → 原文按比例同步", ta.scrollTop, 2100);

// 程序赋值触发的 ta scroll 事件异步到达 → 吞掉，预览不再被反向修改
fire(ta, "scroll");
eq("程序回环被吞: 预览 scrollTop 不变", pv.scrollTop, 900);

// 连续用户滚动（高频）仍正常同步
ta.scrollTop = 2000;
fire(ta, "scroll");
eq("连续滚动仍同步（1）", pv.scrollTop, 857); // 2000/3500*1500=857.14
pv.scrollTop = 1000;
fire(pv, "scroll");
eq("连续滚动仍同步（2）", ta.scrollTop, 2333); // 1000/1500*3500=2333.33

// 缺陷回归：per-element 期望值——feConsumeExpect 只消费"属于自己元素且
// 值匹配"的期望；其他元素/值不匹配时【不清空】期望（防跨元素误清导致
// 后续程序回环被当作用户滚动，产生一次错误的反向同步/视觉抖动）。
// state 是 app.js 内 const，需在 vm 沙箱内部构造场景断言。
const perElCode = `(function () {
  const ta = { scrollTop: 2333 }, pv = { scrollTop: 857 };
  const out = {};
  state.feExpect = { el: pv, v: 600 };
  out.otherNotConsumed = feConsumeExpect(ta) === false;      // 其他元素：不消费
  out.kept = state.feExpect && state.feExpect.v === 600;     // 期望保留
  pv.scrollTop = 600;
  out.matchedConsumed = feConsumeExpect(pv) === true;        // 匹配：消费
  out.cleared = state.feExpect === null;                     // 消费后清空
  state.feExpect = { el: ta, v: 1600 };
  ta.scrollTop = 1700;
  out.valueMismatch = feConsumeExpect(ta) === false;         // 值不匹配：不消费
  return out;
})()`;
const perEl = vm.runInContext(perElCode, ctx);
eq("per-element: 其他元素的期望不被消费", perEl.otherNotConsumed, true);
eq("per-element: 期望保留（未被误清）", perEl.kept, true);
eq("per-element: 匹配元素且值一致 → 消费", perEl.matchedConsumed, true);
eq("per-element: 消费后期望清空", perEl.cleared, true);
eq("per-element: 值不匹配 → 不消费（当作用户滚动）", perEl.valueMismatch, true);

console.log("── 4. 图片加载高度补偿（视口稳定） ──");
const imgEvents = {};
const img = { complete: false, dataset: {}, offsetHeight: 0, offsetTop: 120, addEventListener(type, fn) { imgEvents[type] = fn; } };
pv.querySelectorAll = () => [img];
pv.scrollTop = 300;
ctx.feStabilizePreview(pv);
img.offsetHeight = 520; // 加载完成高度突变
imgEvents.load();
eq("图片顶部在视口上方 → scrollTop 补偿", pv.scrollTop, 820);

const img2 = { complete: false, dataset: {}, offsetHeight: 0, offsetTop: 600, addEventListener(type, fn) { img2._l = fn; } };
pv.querySelectorAll = () => [img2];
pv.scrollTop = 300;
ctx.feStabilizePreview(pv);
img2.offsetHeight = 520;
img2._l();
eq("图片顶部在视口内 → 不补偿", pv.scrollTop, 300);

let bound = 0;
const img3 = { complete: true, dataset: {}, addEventListener() { bound++; } };
pv.querySelectorAll = () => [img3];
ctx.feStabilizePreview(pv);
eq("已加载图片（缓存命中）跳过绑定", bound, 0);

console.log(`\n结果: ${pass} 通过, ${fail} 失败`);
process.exit(fail > 0 ? 1 : 0);
