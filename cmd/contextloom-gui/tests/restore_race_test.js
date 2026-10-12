// restoreCurrentSession 代际保护 —— 回归测试
// 场景：onopen 触发 restore（chat.currentSession 慢返回）期间，用户已点击
// 其他会话（switchSession 完成）→ restore 的旧响应不得覆盖新会话。
// 历史缺陷：restore 无代际保护，旧响应覆盖 state.sessionId/activeTab/
// renderMessages → 界面跳回后端残留会话，历史"丢失"。
// 运行: node cmd/contextloom-gui/tests/restore_race_test.js
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

const els = new Map();
function makeEl(sel, extra = {}) {
  const el = {
    selector: sel, style: {},
    classList: { _set: new Set(), add(c) { this._set.add(c); }, remove(c) { this._set.delete(c); }, toggle(c, on) { if (on === undefined) on = !this._set.has(c); on ? this._set.add(c) : this._set.delete(c); }, contains(c) { return this._set.has(c); } },
    dataset: {}, listeners: {}, innerHTML: "", textContent: "", value: "", _children: [], children: [],
    appendChild(c) { if (c) this._children.push(c); return c; },
    remove() {}, querySelector: (s) => makeEl("sub:" + s), querySelectorAll: () => [],
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    focus() {}, setSelectionRange() {}, getBoundingClientRect: () => ({ width: 10, height: 20, top: 0, left: 0, right: 10, bottom: 20 }), scrollTop: 0, scrollHeight: 0, ...extra,
  };
  els.set(sel, el);
  return el;
}
const document = {
  querySelector: (s) => {
    if (s.startsWith("#")) { let el = els.get(s); if (!el) el = makeEl(s); return el; }
    return els.get(s) || null;
  },
  querySelectorAll: () => [],
  getElementById: (id) => { let el = els.get("#" + id); if (!el) el = makeEl("#" + id); return el; },
  addEventListener() {},
  createElement: (tag) => makeEl("el:" + tag, { tagName: tag.toUpperCase(), querySelector: () => makeEl("sub:" + tag), querySelectorAll: () => [] }),
  createDocumentFragment: () => makeEl("frag", { children: [] }),
  body: { appendChild() {} },
};
const window = { innerWidth: 1280, addEventListener() {}, location: { reload() { ctx._reloaded = true; } } };
const localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
class AbortControllerStub { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
class TerminalStub { constructor() {} loadAddon() {} open() {} onData() {} write() {} dispose() {} focus() {} }
class FitAddonStub { fit() {} }

const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console, setTimeout, clearTimeout, setInterval, clearInterval,
  AbortController: AbortControllerStub, requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error,
  fetch: async (url) => {
    if (url === "/api/config") return { ok: true, json: async () => ({ gui: "http://127.0.0.1:18765/" }) };
    if (url === "/api/projects") return { ok: true, json: async () => ({ current: "/projA", projects: ["/projA"] }) };
    return { ok: true, json: async () => ({}) };
  },
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() { ctx._reloaded = true; } },
  getComputedStyle: () => ({ lineHeight: "20px" }),
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

class WebSocketStub {
  constructor(url) { this.url = url; this.readyState = 0; ctx._ws = this; ctx._sent = []; }
  send(d) { ctx._sent.push(JSON.parse(d)); }
  close() {}
}
WebSocketStub.CONNECTING = 0;
WebSocketStub.OPEN = 1;
WebSocketStub.CLOSED = 3;
ctx.WebSocket = WebSocketStub;

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log("  ✅ " + msg); }
  else { failed++; console.log("  ❌ " + msg); }
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const getState = () => vm.runInContext("state", ctx);

function respond(method, result, ok = true) {
  const req = ctx._sent.find(m => m.method === method && !m._responded);
  if (!req) throw new Error("未找到未响应的请求: " + method);
  req._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock error" }) });
}

async function main() {
  console.log("── 测试：restore 慢返回期间用户切换会话 → 旧响应不得覆盖 ──");
  ctx.init();
  await sleep(20);
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);

  // 1. restore 的 chat.currentSession 已发出（慢返回，先不 respond）
  assert(ctx._sent.some(m => m.method === "chat.currentSession" && !m._responded), "restore 已发出 chat.currentSession（待响应）");

  // 2. 用户点击其他会话：switchSession 发出 loadSession 并完成
  ctx.openChatTab("user-clicked");
  await sleep(10);
  respond("chat.loadSession", { uuid: "user-clicked", title: "用户点击的会话", mode: "chat", messages: [{ role: "user", content: "点这里" }] });
  await sleep(20);
  assert(getState().sessionId === "user-clicked", "用户点击的会话已激活");

  // 3. restore 的旧响应此刻才到达（后端残留会话）
  respond("chat.currentSession", { uuid: "stale-session", title: "后端残留", mode: "chat", messages: [] });
  await sleep(30);
  const s = getState();
  assert(s.sessionId === "user-clicked", "restore 旧响应不得覆盖（sessionId 仍为 user-clicked）");
  assert(s.activeTab === "user-clicked", "activeTab 不被旧响应拉走");
  const ml = els.get("#msgList");
  assert(ml._children.length > 0, "消息区仍是用户点击会话的历史（未被清空）");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed > 0 ? 1 : 0);
}

main().catch(e => { console.error("测试异常:", e); process.exit(1); });
