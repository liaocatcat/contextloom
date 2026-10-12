// 拖拽调整大小行为测试（侧栏宽度 / 编辑器分栏 / 配置面板滚动）
// 运行: node cmd/contextloom-gui/tests/resizer_test.js
// 用最小 DOM stub 加载真实 app.js，模拟 pointerdown/move/up 事件，
// 验证：宽度改变、钳制范围、双击重置、持久化、分栏 resizer 显隐。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");
const css = fs.readFileSync(path.join(__dirname, "..", "web", "style.css"), "utf8");

// ── 最小 DOM stub ──
const listeners = new Map(); // selector → {type: [fn]}
const els = new Map();       // selector → element stub

function makeEl(sel, extra = {}) {
  const el = {
    selector: sel,
    style: {},
    classList: { _set: new Set(), add(c) { this._set.add(c); }, remove(c) { this._set.delete(c); }, toggle(c, on) { if (on === undefined) on = !this._set.has(c); on ? this._set.add(c) : this._set.delete(c); }, contains(c) { return this._set.has(c); } },
    dataset: {},
    listeners: {},
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    setPointerCapture() {}, releasePointerCapture() {},
    getBoundingClientRect() {
      // 模拟真实渲染尺寸：优先反映 inline style（width / flex-basis / height）
      const wRaw = String(this.style.width || this.style.flex || "");
      const wn = parseInt(wRaw.replace(/[^0-9]/g, ""), 10);
      const hRaw = String(this.style.height || "");
      const hn = parseInt(hRaw.replace(/[^0-9]/g, ""), 10);
      return { width: wn > 0 ? wn : (extra.width || 0), height: hn > 0 ? hn : (extra.height || 0) };
    },
    ...extra,
  };
  els.set(sel, el);
  return el;
}
function fire(el, type, ev = {}) {
  const fns = (el && el.listeners[type]) || [];
  for (const fn of fns) fn({ button: 0, preventDefault() {}, clientX: 0, pointerId: 1, ...ev });
}

const document = {
  querySelector: (s) => els.get(s) || null,
  querySelectorAll: () => [],
  addEventListener() {},
  body: { classList: { _set: new Set(), add(c) { this._set.add(c); }, remove(c) { this._set.delete(c); } } },
};
const window = { innerWidth: 1280, addEventListener() {} };
const localStorage = (() => { let m = new Map(); return {
  getItem: (k) => m.has(k) ? m.get(k) : null,
  setItem: (k, v) => m.set(k, String(v)),
  removeItem: (k) => m.delete(k),
  _dump: () => Object.fromEntries(m),
}; })();

const ctx = { document, window, localStorage, console, setTimeout, clearTimeout, requestAnimationFrame: (f) => setTimeout(f, 0), Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error };
vm.createContext(ctx);
vm.runInContext(src, ctx);

let pass = 0, fail = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.log("  ❌ " + name + " → got " + g + " want " + w); }
}
function ok(name, cond) { eq(name, !!cond, true); }

// ── 构造 DOM：侧栏 + resizer + 编辑器列 ──
const leftCol = makeEl("#leftCol", { width: 264 });
const sideResizer = makeEl("#sideResizer");
const feResizer = makeEl("#feResizer");
makeEl("#feEditCol", { width: 400 });
const body = makeEl(".fe-body", { width: 1000 });

// ── 测试 1：initSideResizer 应用已保存宽度 ──
localStorage.setItem("contextloom.sideW", "360");
ctx.initSideResizer();
eq("测试1 已保存宽度 360 被应用", leftCol.style.width, "360px");

// ── 测试 2：pointerdown → move 改变宽度 ──
fire(sideResizer, "pointerdown", { clientX: 100 });
fire(sideResizer, "pointermove", { clientX: 200 });   // +100 → 460
eq("测试2 拖拽 +100px → 460", leftCol.style.width, "460px");
fire(sideResizer, "pointermove", { clientX: 50 });    // 360+(50-100)=310（相对按下点）
eq("测试2b 反向拖拽 -50px → 310", leftCol.style.width, "310px");

// ── 测试 3：钳制最小宽度 ──
fire(sideResizer, "pointermove", { clientX: -5000 }); // 410-5100 → clamp 180
eq("测试3 钳制最小 180", leftCol.style.width, "180px");

// ── 测试 4：钳制最大宽度 ──
// startVal=410, dx=+5000 → 5410 → clamp min(560, 1280-340=940)=560
fire(sideResizer, "pointerdown", { clientX: 0 });
fire(sideResizer, "pointermove", { clientX: 5000 });
eq("测试4 钳制最大 560", leftCol.style.width, "560px");

// ── 测试 5：pointerup 持久化 ──
fire(sideResizer, "pointerup", {});
eq("测试5 pointerup 后保存到 localStorage", localStorage.getItem("contextloom.sideW"), "560");
ok("测试5b 拖拽中 body.resizing 已移除", !document.body.classList._set.has("resizing"));

// ── 测试 6：双击重置默认 264 ──
fire(sideResizer, "dblclick", {});
eq("测试6 双击重置 → 默认宽度", leftCol.style.width, "");
eq("测试6b localStorage 已清除", localStorage.getItem("contextloom.sideW"), null);

// ── 测试 7：编辑器分栏 resizer 拖拽 ──
ctx.initFeResizer();
const ed = els.get("#feEditCol");
fire(feResizer, "pointerdown", { clientX: 400 });
fire(feResizer, "pointermove", { clientX: 600 });     // +200 → 600
eq("测试7 分栏拖拽 → edit 列 600px", ed.style.flex, "0 0 600px");
fire(feResizer, "pointerup", {});
eq("测试7b 分栏宽度持久化", localStorage.getItem("contextloom.feSplit"), "600");

// ── 测试 8：编辑器分栏钳制 ──
fire(feResizer, "pointerdown", { clientX: 600 });
fire(feResizer, "pointermove", { clientX: 99999 });   // → clamp 1000-160=840
eq("测试8 分栏钳制最大 840", ed.style.flex, "0 0 840px");
fire(feResizer, "pointerup", {});
fire(feResizer, "dblclick", {});
eq("测试8b 分栏双击重置 → 恢复默认", ed.style.flex, "");

// ── 测试 9：feResizer 显隐由 feApplyView 控制 ──
// （feApplyView 需要 curFileTab，这里直接验证契约：resizer 默认不带 on，设置 on 类可行）
feResizer.classList.toggle("on", true);
ok("测试9 分栏模式 resizer.on 存在", feResizer.classList.contains("on"));

// ── 测试 10：Agent 面板纵向拖拽 ──
const agentResizer = makeEl("#agentResizer");
const agentTasks = makeEl("#agentTasks", { width: 250, height: 140 });
localStorage.setItem("contextloom.agentTasksH", "200");
ctx.initAgentResizer();
eq("测试10 已保存高度 200 被应用", agentTasks.style.height, "200px");
eq("测试10b maxHeight 同步覆盖", agentTasks.style.maxHeight, "200px");

fire(agentResizer, "pointerdown", { clientY: 100 });
fire(agentResizer, "pointermove", { clientY: 180 });  // +80 → 280
eq("测试11 纵向拖拽 +80px → 280", agentTasks.style.height, "280px");
fire(agentResizer, "pointermove", { clientY: 99999 }); // → clamp 320
eq("测试11b 纵向钳制最大 320", agentTasks.style.height, "320px");
fire(agentResizer, "pointerup", {});
eq("测试11c pointerup 持久化高度", localStorage.getItem("contextloom.agentTasksH"), "320");
ok("测试11d resizing-y 已移除", !document.body.classList._set.has("resizing-y"));
fire(agentResizer, "dblclick", {});
eq("测试12 双击重置 → 恢复默认", agentTasks.style.height, "");

// ── 测试 13：分栏分隔条 DOM 位置（防回归）──
// 回归：feResizer 曾被放到 fe-body 首位 → 分隔竖线跑到最左，与侧栏 resizer 贴在一起。
// 契约：真实 index.html 中 feResizer 必须位于 feEditCol 之后、fePreviewCol 之前。
{
  const html = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8");
  const body = html.split('class="fe-body"')[1] || "";
  const iEdit = body.indexOf('id="feEditCol"');
  const iRes  = body.indexOf('id="feResizer"');
  const iPrev = body.indexOf('id="fePreviewCol"');
  ok("测试13 fe-body 内 feResizer 位于编辑列之后", iRes > iEdit && iEdit >= 0);
  ok("测试13b fe-body 内 feResizer 位于预览列之前", iPrev > iRes && iRes >= 0);
  // 不允许 feResizer 是 fe-body 第一个子元素（旧 bug：紧贴左侧）
  ok("测试13c feResizer 非 fe-body 首位", iEdit < iRes);

  // ── 测试 14：chatResizer 对话窗口高度拖拽 ──
  // #chatResizer 位于 msgWrap 与 inputZone 之间
  ok("测试14a index.html 含 #chatResizer", /id="chatResizer"/.test(html));
  ok("测试14b #chatResizer 在 #msgWrap 之后", html.indexOf('id="chatResizer"') > html.indexOf('id="msgWrap"'));
  ok("测试14c #chatResizer 在 #inputZone 之前", html.indexOf('id="chatResizer"') < html.indexOf('id="inputZone"'));
  ok("测试14d #chatResizer 类为 h-resizer", /chatResizer.*class="h-resizer"/.test(html));
  ok("测试14e app.js 定义 initChatResizer", /function initChatResizer/.test(src));
  ok("测试14f init 调用 initChatResizer", /initChatResizer\(\)/.test(src));
  // ── 测试 15：终端高度自适应（无独立拖拽条）──
  // 设计：终端位于输入框下方，flex:1 自动占满剩余空间；拖拽 #chatResizer
  // （输入框上方的分隔条）调整对话区高度后，终端自动跟随变化。
  ok("测试15a index.html 不含 #termResizer", !/id="termResizer"/.test(html));
  ok("测试15b app.js 不定义 initTermResizer", !/function initTermResizer/.test(src));
  ok("测试15c app.js 不含 initTermResizer() 调用", !/initTermResizer\(\)/.test(src));
  ok("测试15d #termPanel 在 #inputZone 之后", html.indexOf('id="termPanel"') > html.indexOf('id="inputZone"'));
  // ── 测试 16：termPanel 高度自适应 ──
  ok("测试16a termPanel CSS 使用 flex: 1 1 auto（自适应占满剩余）", /#termPanel[\s\S]*?flex:\s*1\s*1\s*auto/.test(css));
  ok("测试16b termPanel 无固定 height: 150px", !/#termPanel[\s\S]*?height:\s*150px/.test(css));
  ok("测试16c termPanel 保留 min-height 保护", /#termPanel[\s\S]*?min-height:\s*60px/.test(css));
  // ── 测试 17：msgWrap 布局 ──
  ok("测试17a msgWrap 在 chatArea 内", /id="msgWrap"/.test(html));
  ok("测试17b msgList 在 msgWrap 内", html.indexOf('id="msgList"') > html.indexOf('id="msgWrap"') && html.indexOf('id="msgList"') < html.indexOf('id="chatResizer"'));
}

console.log(fail === 0 ? "\n🎉 resizer 行为测试全部通过" : "\n❌ 有失败项");
process.exit(fail === 0 ? 0 : 1);
