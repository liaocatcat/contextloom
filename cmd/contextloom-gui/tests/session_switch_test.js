// 切换项目 → 会话列表更新 —— 前端集成测试（用户报告：gui切换项目时，
// 会话列表没有更新到对应项目的会话列表）。
// 运行: node cmd/contextloom-gui/tests/session_switch_test.js
// 用最小 DOM stub + mock WebSocket/fetch 加载真实 app.js，完整模拟浏览器：
//   - init() → connect() → ws.onopen → loadSessions()（会话列表请求）
//   - 模拟后端返回 A 项目会话 → 会话面板渲染 2 条
//   - 模拟"切换项目后 reload"（全新页面状态）→ onopen → 后端返回空列表
//     → 会话面板渲染"暂无会话"
//   - 全程断言无运行时异常（undefined 函数/引用错误是历史主要故障源）
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub（与 term_test.js 同构）──
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
  // 懒创建：任何 #id 访问自动创建元素（init/bindEvents 依赖大量面板元素）
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

let fetchCalls = [];
const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console, setTimeout, clearTimeout, setInterval, clearInterval,
  AbortController: AbortControllerStub, requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error,
  fetch: async (url, opts) => {
    fetchCalls.push({ url, opts });
    if (url === "/api/config") {
      return { ok: true, json: async () => ({ gui: "http://127.0.0.1:18765/" }) };
    }
    if (url === "/api/projects") {
      return { ok: true, json: async () => ({ current: "/projA", projects: ["/projA", "/projB"] }) };
    }
    return { ok: true, json: async () => ({}) };
  },
  // 浏览器模式：protocol http → 同源 WS（resolveWSBase 走 http 分支）
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() { ctx._reloaded = true; } },
  getComputedStyle: () => ({ lineHeight: "20px" }),
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

// ── WebSocket stub：手动触发 onopen/onmessage，记录发送的请求 ──
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

// 模拟后端响应指定 method 的请求（按发送顺序匹配第一个未响应者）
function respond(method, result, ok = true) {
  const req = ctx._sent.find(m => m.method === method && !m._responded);
  if (!req) throw new Error("未找到未响应的请求: " + method + "，已发送: " + JSON.stringify(ctx._sent.map(m => m.method)));
  req._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock error" }) });
}

async function main() {
  console.log("── 测试 1：切换项目后会话列表更新（前端完整 onopen 流程）──");

  // 1. 启动（DOMContentLoaded → init → connect）
  // init 由 app.js 末尾的 addEventListener("DOMContentLoaded", init) 触发，
  // 但 vm 中 document.addEventListener 是 no-op → 手动调用 init
  ctx._reloaded = false;
  ctx.init();
  await sleep(20); // 等 connect() 的 resolveWSBase await
  if (!ctx._ws) throw new Error("connect() 未创建 WebSocket");
  assert(/\/api\/ws(\?|$)/.test(ctx._ws.url), "WS 连接到 /api/ws（代理路径，可带 ?dir= 项目路由参数）");

  // 2. 触发 onopen → 应发出 chat.listSessions 请求
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);
  assert(ctx._sent.some(m => m.method === "chat.listSessions"), "onopen 发出 chat.listSessions（会话列表请求）");

  // 3. 后端返回 A 项目的 2 个会话
  const now = new Date().toISOString();
  respond("chat.listSessions", [
    { uuid: "sess-A1", title: "A 会话 1", updatedAt: now },
    { uuid: "sess-A2", title: "A 会话 2", updatedAt: now },
  ]);
  await sleep(10);
  assert(getState().sessions.length === 2, "state.sessions 更新为 A 的 2 个会话");

  // 4. 打开会话面板 → 渲染 2 条
  const sl = makeEl("#sessionList");
  els.set("#sessionList", sl);
  ctx.setPanel("sessions");
  await sleep(10);
  // renderSessionListPanel 用 DocumentFragment 批量渲染：list.appendChild(frag)，
  // 实际条目在 frag 的子元素里
  const fragEl = sl._children.length ? sl._children[0] : null;
  const itemCount = fragEl && fragEl._children ? fragEl._children.length : 0;
  assert(getState().sessions.length === 2 && itemCount === 2, "会话面板渲染 A 的 2 条会话");

  // 5. 模拟"切换项目后 reload"：全新页面状态 → 重新 connect → onopen
  vm.runInContext("state.sessions = []; state.activePanel = 'projects'; state.ws = null;", ctx);
  ctx._sent = [];
  ctx.connect();
  await sleep(20);
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);
  assert(ctx._sent.some(m => m.method === "chat.listSessions"), "切换后 reload → onopen 再次发出 chat.listSessions");

  // 6. 后端返回 B 项目（空列表）→ 打开会话面板 → "暂无会话"
  respond("chat.listSessions", []);
  await sleep(10);
  assert(getState().sessions.length === 0, "state.sessions 更新为 B 的空列表（跨项目数据隔离）");
  ctx.setPanel("sessions");
  await sleep(10);
  assert(sl.innerHTML.includes("暂无会话"), "会话面板渲染'暂无会话'（不再显示 A 的旧列表）");

  // ── 测试 2：已打开 md 文件时点击会话 → 文件编辑器必须收起、聊天区恢复 ──
  console.log("── 测试 2：点击会话收起 md 编辑器（布局防残留回归）──");

  // 2.1 新建会话 A（openChatTab 新建分支 → switchSession → loadSession）
  ctx.openChatTab("sess-A1");
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-A1", mode: "chat", messages: [] });
  await sleep(20);
  assert(getState().sessionId === "sess-A1", "会话 A1 已激活");

  // 2.1.5 新建会话 B 并激活（制造"当前会话=B、A 为非当前已打开会话"）
  ctx.openChatTab("sess-B1");
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-B1", mode: "chat", messages: [] });
  await sleep(20);
  assert(getState().sessionId === "sess-B1", "会话 B1 已激活（当前会话）");

  // 2.2 打开 md 文件（renderFileEditor：fileEditor 显示、聊天区隐藏）
  ctx.openFileTab("/projA/readme.md", "# 标题\n正文", Date.now(), "/projA", false);
  await sleep(10);
  const fe = els.get("#fileEditor");
  const ml = els.get("#msgList");
  const iz = els.get("#inputZone");
  assert(fe.style.display === "flex", "打开 md 后文件编辑器显示（flex）");
  assert(ml.style.display === "none" && iz.style.display === "none", "打开 md 后消息区/输入区隐藏");

  // 2.3 点击【非当前】已打开的会话 tab A（openChatTab exist 分支 → switchSession）
  //     历史缺陷：该路径不调 hideFileEditor → fileEditor 空占上方、聊天区隐藏无法点击
  ctx.openChatTab("sess-A1");
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-A1", mode: "chat", messages: [] });
  await sleep(20);
  assert(fe.style.display === "none", "点击非当前会话后文件编辑器收起（display:none）——修复后无残留");
  assert(ml.style.display === "" && iz.style.display === "", "点击非当前会话后消息区/输入区恢复显示（可点击）");
  assert(getState().sessionId === "sess-A1", "已切换到会话 A1（非短路路径）");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });
