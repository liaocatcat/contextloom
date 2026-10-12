// 回归测试：删除会话确认链路（原生 confirm 在 WKWebView 静默失败 → 自定义 confirmDialog）
// 运行: node cmd/contextloom-gui/tests/delete_confirm_test.js
// GUI 删除会话链路冒烟测试：验证自定义 confirmDialog 替代原生 confirm 后，
// 点击 🗑 → 对话框弹出 → 点"删除" → 发出 chat.deleteSession 请求
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

// ─── 最小 DOM stub ───
class El {
  constructor(tag) {
    this.tagName = tag.toUpperCase();
    this.children = [];
    this.style = {};
    this.dataset = {};
    this.classList = { _s: new Set(), add: c => this.classList._s.add(c), remove: c => this.classList._s.delete(c), toggle: (c, f) => f === undefined ? (this.classList._s.has(c) ? this.classList._s.delete(c) : this.classList._s.add(c)) : (f ? this.classList._s.add(c) : this.classList._s.delete(c)), contains: c => this.classList._s.has(c) };
    this._listeners = {};
    this.value = "";
    this.id = "";
    this.className = "";
    this.disabled = false;
  }
  appendChild(c) { this.children.push(c); c.parent = this; return c; }
  remove() { if (this.parent) { const i = this.parent.children.indexOf(this); if (i >= 0) this.parent.children.splice(i, 1); } }
  querySelector(sel) { return query(this, sel); }
  querySelectorAll(sel) { return queryAll(this, sel); }
  addEventListener(t, fn) { (this._listeners[t] = this._listeners[t] || []).push(fn); }
  removeEventListener(t, fn) { if (this._listeners[t]) this._listeners[t] = this._listeners[t].filter(f => f !== fn); }
  set onclick(fn) { if (fn) this._listeners.click = [fn]; }
  get onclick() { return (this._listeners.click || [])[0]; }
  dispatch(t, ev = {}) { (this._listeners[t] || []).forEach(fn => fn(Object.assign({ target: this, stopPropagation() {}, preventDefault() {} }, ev))); }
  click() { this.dispatch("click", { target: this }); }
  focus() {} select() {}
  set innerHTML(v) {
    this._html = v;
    this.children = [];
    // 简单解析：提取 class="..." 的元素及其直接文本（测试用 stub）
    const re = /<([a-z]+)\s+class="([^"]*)"[^>]*>([^<]*)<\/\1>/g;
    let m;
    while ((m = re.exec(v))) {
      const el = new El(m[1]);
      el.className = m[2];
      el.textContent = m[3];
      this.children.push(el);
    }
  }
  get innerHTML() { return this._html || ""; }
  set textContent(v) { this._text = String(v); }
  get textContent() { return this._text || ""; }
  closest() { return null; }
  getBoundingClientRect() { return { bottom: 100 }; }
}

const doc = {
  body: new El("body"),
  _listeners: {},
  createElement: t => new El(t),
  querySelector: sel => query(doc.body, sel),
  addEventListener(t, fn) { (this._listeners[t] = this._listeners[t] || []).push(fn); },
  removeEventListener(t, fn) { if (this._listeners[t]) this._listeners[t] = this._listeners[t].filter(f => f !== fn); },
};
function match(el, sel) {
  if (!el || !el.className) return false;
  const cls = String(el.className).split(/\s+/);
  const parts = sel.replace(/^\./, "").split(".");
  return parts.every(p => cls.includes(p));
}
function query(root, sel) {
  if (match(root, sel)) return root;
  for (const c of root.children || []) { const r = query(c, sel); if (r) return r; }
  return null;
}
function queryAll(root, sel) {
  const out = [];
  (function walk(el) { if (match(el, sel) && el !== root) out.push(el); (el.children || []).forEach(walk); })(root);
  return out;
}

// ─── 执行环境 ───
const sandbox = {
  document: doc,
  window: { innerHeight: 800, innerWidth: 1200, location: { protocol: "http:", host: "127.0.0.1:1" }, addEventListener() {}, setTimeout, clearTimeout, setInterval: () => 0 },
  location: { protocol: "http:", host: "127.0.0.1:1" },
  navigator: { platform: "MacIntel" },
  console, setTimeout, clearTimeout, setInterval: () => 0, clearInterval: () => {},
  URLSearchParams, fetch: async () => ({ ok: true, json: async () => ({}) }),
  WebSocket: function () { this.readyState = 0; this.send = () => {}; this.close = () => {}; },
};
sandbox.globalThis = sandbox;
vm.createContext(sandbox);
const code = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");
vm.runInContext(code, sandbox, { filename: "app.js" });

(async () => {
  let pass = 0, fail = 0;
  const ok = (cond, name) => { if (cond) { pass++; console.log("  ✅ " + name); } else { fail++; console.log("  ❌ " + name); } };

  // 1. confirmDialog 弹出（替代原生 confirm 静默 false）
  const p = sandbox.confirmDialog("确定删除该会话？", { danger: true, okText: "删除" });
  const mask = query(doc.body, ".dlg-mask");
  ok(!!mask, "confirmDialog 创建遮罩层（原生 confirm 在 WKWebView 下不显示任何内容）");
  const box = query(doc.body, ".dlg-box");
  ok(!!box && String(box.className).includes("danger"), "对话框带 danger 样式");
  const msg = query(doc.body, ".dlg-msg");
  ok(msg && msg.textContent === "确定删除该会话？", "对话框显示确认文案: " + (msg && msg.textContent));

  // 2. 点"取消" → resolve(false)
  const p2 = sandbox.confirmDialog("测试取消", { okText: "删除" });
  const cancelBtn = query(doc.body, ".dlg-btn.cancel");
  cancelBtn.click();
  const v2 = await p2;
  ok(v2 === false, "点取消 resolve(false) → 删除不执行");
  ok(!query(doc.body, ".dlg-mask"), "取消后遮罩移除");

  // 3. 点"删除" → resolve(true)
  const p3 = sandbox.confirmDialog("确定删除该会话？此操作不可恢复。", { okText: "删除" });
  const okBtn = query(doc.body, ".dlg-btn.ok");
  ok(!!okBtn && okBtn.textContent === "删除", "确定按钮文案为'删除'");
  okBtn.click();
  const v3 = await p3;
  ok(v3 === true, "点删除 resolve(true) → 继续删除流程");
  ok(!query(doc.body, ".dlg-mask"), "确认后遮罩移除");

  // 4. Escape 取消
  const p4 = sandbox.confirmDialog("Escape 测试");
  const mask4 = query(doc.body, ".dlg-mask");
  const keyFn = doc._listeners.keydown && doc._listeners.keydown[doc._listeners.keydown.length - 1];
  ok(!!keyFn, "注册了 keydown 监听（Escape/Enter 支持）");
  if (keyFn) keyFn({ key: "Escape" });
  const v4 = await p4;
  ok(v4 === false, "Escape 取消 resolve(false)");

  // 5. deleteSessionPrompt 完整链路：模拟点击 🗑 → 确认 → req 发出
  sandbox.__reqCalls = [];
  vm.runInContext(`
    req = (m, p) => {
      globalThis.__reqCalls.push([m, p]);
      if (m === "chat.listSessions") return Promise.resolve([]);
      return Promise.resolve({});
    };
    openChatTab = async () => true;
    newSession = () => {};
  `, sandbox);
  const st = vm.runInContext("state", sandbox);
  st.sessionId = "aaa";
  st.sessions = [{ uuid: "aaa", title: "A", updatedAt: "2026-01-01" }, { uuid: "bbb", title: "B", updatedAt: "2026-01-02" }];
  const dp = vm.runInContext("deleteSessionPrompt", sandbox)("bbb"); // 非当前会话
  const mask5 = query(doc.body, ".dlg-mask");
  ok(!!mask5, "点击删除按钮后弹出确认框（不再静默无反应）");
  query(doc.body, ".dlg-btn.ok").click();
  await dp;
  await new Promise(r => setTimeout(r, 30));
  ok(sandbox.__reqCalls.some(c => c[0] === "chat.deleteSession" && c[1].uuid === "bbb"), "确认后发出 chat.deleteSession 请求: " + JSON.stringify(sandbox.__reqCalls));

  // 6. 删除当前活跃会话：先切换再删
  sandbox.__reqCalls = [];
  st.sessionId = "aaa";
  const dp2 = vm.runInContext("deleteSessionPrompt", sandbox)("aaa");
  query(doc.body, ".dlg-btn.ok").click();
  await dp2;
  await new Promise(r => setTimeout(r, 30));
  ok(sandbox.__reqCalls.some(c => c[0] === "chat.deleteSession" && c[1].uuid === "aaa"), "当前会话删除：切换后发出删除请求");

  console.log(`\n结果: ${pass} 通过, ${fail} 失败`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error("测试异常:", e); process.exit(1); });
