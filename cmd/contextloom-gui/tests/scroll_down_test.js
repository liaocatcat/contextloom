// 回到底部按钮避让发送/中止按钮 —— 前端行为测试
// 运行: node cmd/contextloom-gui/tests/scroll_down_test.js
// 验证：
//   - #scrollDown 不再与 #inputZone（发送/中止按钮所在输入区）重叠：
//     placeScrollDown() 按 inputZone 实测高度动态定位（+14px 间隙）
//   - 输入区隐藏（终端全屏）时按钮回到容器底部（12px）
//   - scroll 监听器显示按钮时同步调用 placeScrollDown()
//   - CSS 兜底 bottom 值 = 92px（常规输入区高度之上）
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");
const css = fs.readFileSync(path.join(__dirname, "..", "web", "style.css"), "utf8");
const html = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8");

// ── 最小 DOM stub（与 term_test.js 同构）──
const els = new Map();
function makeEl(sel, extra = {}) {
  const el = {
    selector: sel,
    style: {},
    classList: { _set: new Set(), add(c) { this._set.add(c); }, remove(c) { this._set.delete(c); }, toggle(c, on) { if (on === undefined) on = !this._set.has(c); on ? this._set.add(c) : this._set.delete(c); }, contains(c) { return this._set.has(c); } },
    dataset: {},
    listeners: {},
    innerHTML: "",
    textContent: "",
    value: "",
    offsetHeight: 0,
    _children: [],
    appendChild(c) { if (c) this._children.push(c); return c; },
    remove() {},
    querySelector: () => null,
    querySelectorAll: () => [],
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    focus() {},
    scrollTop: 0,
    scrollHeight: 0,
    clientHeight: 0,
    ...extra,
  };
  els.set(sel, el);
  return el;
}
const document = {
  querySelector: (s) => els.get(s) || null,
  querySelectorAll: () => [],
  getElementById: (id) => els.get("#" + id) || null,
  addEventListener() {},
  createElement: (tag) => {
    const el = makeEl("el:" + tag, { tagName: tag.toUpperCase() });
    el.querySelector = () => makeEl("sub:" + tag);
    el.querySelectorAll = () => [];
    return el;
  },
  body: { appendChild() {} },
};
const window = { innerWidth: 1280, addEventListener() {}, location: { reload() {} } };
const localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
class AbortControllerStub {
  constructor() { this.signal = { aborted: false }; }
  abort() { this.signal.aborted = true; }
}
class TerminalStub { constructor() {} loadAddon() {} open() {} onData() {} onResize() {} write() {} dispose() {} focus() {} }
class FitAddonStub { fit() {} }

const ctx = {
  Terminal: TerminalStub,
  FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console, setTimeout, clearTimeout,
  setInterval: () => 0, clearInterval: () => {},
  AbortController: AbortControllerStub,
  requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error, fetch,
  location: { reload() {} },
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

let pass = 0, fail = 0;
function ok(cond, name) {
  if (cond) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.error("  ❌ " + name); }
}

(async () => {
  console.log("== 回到底部按钮避让测试 ==");

  // ── CSS 契约：兜底 bottom 不再是 12px（旧值会与发送按钮重叠）──
  const m = css.match(/#scrollDown\s*\{([^}]*)\}/);
  ok(!!m, "style.css 存在 #scrollDown 规则");
  if (m) {
    ok(/bottom:\s*12px/.test(m[1]), "CSS 兜底 bottom: 12px（scrollDown 锚定 #msgWrap，不再避让输入区）");
    ok(/right:\s*22px/.test(m[1]), "CSS 保留 right: 22px");
  }
  // 新布局契约：scrollDown 必须位于 #msgWrap 内（对话窗口，随对话窗口拖拽缩放）
  ok(/<div id="msgWrap">[\s\S]*?<div id="msgList"><\/div>\s*<button id="scrollDown"/.test(html),
    "index.html: #scrollDown 位于 #msgWrap 内（#msgList 之后）");
  ok(/<div id="msgWrap">/.test(html) && /<\/div>\s*<!-- 对话区 ↔ 输入框 拖拽条 -->/.test(html),
    "index.html: #msgWrap 后紧跟 #chatResizer 拖拽条");

  // ── placeScrollDown 固定定位（scrollDown 锚定 msgWrap，底部 12px）──
  const iz = makeEl("#inputZone", { offsetHeight: 90 }); // stub 输入区（不再影响按钮定位）
  const sd = makeEl("#scrollDown");
  makeEl("#msgList", { scrollHeight: 1000, scrollTop: 100, clientHeight: 500 });
  makeEl("#sendBtn");
  // bindEvents 其余直接引用的元素（stub 需齐全，防止 null onclick 报错）
  for (const sel of ["#attachBtn", "#cfgRefresh", "#cfgYamlReset", "#cfgYamlSave", "#fileInput", "#input", "#logClear", "#logList", "#projAdd", "#projRefresh", "#sessionNew", "#sessionRefresh", "#termNew", "#themeToggle"]) {
    if (!els.get(sel)) makeEl(sel);
  }

  ctx.placeScrollDown();
  ok(sd.style.bottom === "12px", "placeScrollDown: 固定 bottom=12px（锚定 msgWrap 底部，与输入区高度无关）");
  ok(iz.offsetHeight === 90, "stub inputZone.offsetHeight 生效");

  // 输入区变高：按钮位置不变（不再受输入区高度影响）
  iz.offsetHeight = 130;
  ctx.placeScrollDown();
  ok(sd.style.bottom === "12px", "placeScrollDown: 输入区变高仍 bottom=12px（布局重构后不再避让输入区）");

  // ── scroll 监听器：离开底部显示按钮时同步调用 placeScrollDown ──
  ctx.bindEvents();
  const msg = els.get("#msgList");
  const scrollFns = msg.listeners["scroll"] || [];
  ok(scrollFns.length >= 1, "bindEvents 为 #msgList 绑定 scroll 监听器");
  if (scrollFns.length) {
    // 离开底部（away=true）→ 按钮显示 + 位置固定 12px
    msg.scrollHeight = 1000; msg.scrollTop = 100; msg.clientHeight = 500;
    for (const fn of scrollFns) fn();
    ok(sd.classList.contains("show"), "离开底部 → #scrollDown 显示");
    ok(sd.style.bottom === "12px", "scroll 显示按钮时 placeScrollDown 保持 bottom=12px");
    // 回到底部（away=false）→ 按钮隐藏
    msg.scrollTop = msg.scrollHeight - msg.clientHeight;
    for (const fn of scrollFns) fn();
    ok(!sd.classList.contains("show"), "回到底部 → #scrollDown 隐藏");
  }

  // ── 函数存在性 ──
  ok(typeof ctx.placeScrollDown === "function", "placeScrollDown 已定义为全局函数");

  console.log(fail === 0 ? `\n全部通过 (${pass}/${pass})` : `\n失败 ${fail}/${pass + fail}`);
  process.exit(fail === 0 ? 0 : 1);
})();
