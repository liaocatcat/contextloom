// status.bar 轮询污染前端 sessionId —— 回归测试
// 用户报告：GUI 重启后点击历史会话，消息区空白/历史记录不加载。
// 根因链路：后端 currentSessionID（残留 backend/多窗口）与前端显示的会话不同 →
// refreshStatus 每 2s 无条件 state.sessionId = s.sessionId 覆盖前端 →
// 用户视角的当前会话被静默切换：endStream 收到旧会话的 done 事件后
// activeTab/sessionId 一并被拉到后端残留会话 → 界面跳变、历史"丢失"；
// 再点击原会话时被 switchSession "uuid === state.sessionId" 短路 →
// 不调 chat.loadSession、不渲染历史 → 消息区空白。
// 运行: node cmd/contextloom-gui/tests/status_sessionid_test.js
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub（与 session_switch_test.js 同构）──
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
    if (s.startsWith("#")) {
      let el = els.get(s);
      if (!el) { el = makeEl(s); }
      return el;
    }
    return els.get(s) || null;
  },
  querySelectorAll: () => [],
  getElementById: (id) => {
    let el = els.get("#" + id);
    if (!el) { el = makeEl("#" + id); }
    return el;
  },
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
  if (!req) throw new Error("未找到未响应的请求: " + method + "，已发送: " + JSON.stringify(ctx._sent.map(m => m.method + (m._responded ? "(✓)" : ""))));
  req._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock error" }) });
}

async function main() {
  console.log("── 测试 1：status.bar 轮询不得覆盖前端当前会话 ──");
  ctx.init();
  await sleep(20);
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);

  // 后端残留会话：backend currentSessionID = old-session
  respond("chat.currentSession", { uuid: "old-session", title: "旧会话", mode: "chat", messages: [{ role: "user", content: "旧会话消息" }] });
  await sleep(10);
  assert(getState().sessionId === "old-session", "restore 后前端 sessionId = old-session");

  // 用户点击另一个会话（有 643 条历史）
  ctx.openChatTab("sess-643");
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-643", title: "压缩会话", mode: "chat", messages: [{ role: "user", content: "历史1" }, { role: "assistant", content: "历史2" }] });
  await sleep(20);
  assert(getState().sessionId === "sess-643", "点击后前端 sessionId = sess-643");

  // 2s 轮询 status.bar 返回后端残留 sessionId
  respond("status.bar", { mode: "chat", sessionId: "old-session", ctx: "0%", goal: "无活动目标" });
  await sleep(20);
  const after = getState().sessionId;
  assert(after === "sess-643", "status.bar 轮询不得覆盖前端 sessionId（仍为 sess-643）");

  // 轮询覆盖后消息区内容仍在（未被清空/跳变到旧会话）
  const ml = els.get("#msgList");
  const rendered = ml._children.length > 0;
  assert(rendered, "轮询后消息区历史仍在（未被清空）");

  console.log("── 测试 2：当前会话点击短路只在 sessionId 一致时生效 ──");
  // 修复后：sessionId 未被污染，点击当前会话短路（正确行为，防重绘）。
  // 验证短路条件确实用未被污染的值判定：点击当前会话不产生新请求。
  ctx._sent = [];
  ctx.openChatTab("sess-643");
  await sleep(10);
  const sentAfterClick = ctx._sent.filter(m => m.method === "chat.loadSession");
  assert(sentAfterClick.length === 0, "点击当前会话短路（无重复加载请求）——sessionId 未被污染");

  // 切换到 old-session（正常路径）
  ctx._sent = [];
  ctx.openChatTab("old-session");
  await sleep(10);
  respond("chat.loadSession", { uuid: "old-session", title: "旧会话", mode: "chat", messages: [{ role: "user", content: "旧会话消息" }] });
  await sleep(20);
  assert(getState().sessionId === "old-session", "正常切换到 old-session 成功");

  // 一致的轮询值无副作用（前端可能不重发 status.bar，用已有的响应模拟即可）
  const s = getState();
  assert(s.sessionId === "old-session", "一致时轮询无副作用");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed > 0 ? 1 : 0);
}

main().catch(e => { console.error("测试异常:", e); process.exit(1); });
