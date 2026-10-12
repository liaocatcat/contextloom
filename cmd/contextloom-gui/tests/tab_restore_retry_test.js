// 切换项目后的会话恢复容错 —— 回归测试
// 用户实测："切换项目时右侧tab对话页面还是消失；恢复老是显示恢复失败，
// 但是能继续下去，只是中间对话记录消失。"
//
// 根因（本测试覆盖的两个断点）：
//   1. afterProjectSwitch 恢复 tab 后，onopen 的 restoreCurrentSession 用
//      pendingRestoreTab loadSession —— 目标项目 backend 冷启动（首次访问
//      拉起进程需 1~3s）时该请求必然失败/超时 → catch 只 console.warn，
//      tab 显示激活但消息区永远空白（"中间对话记录消失"）。
//      → 修复：失败保留 pendingRestoreTab 并 1s×5 自动重试。
//   2. 恢复失败后 state.activeTab 指向空内容 tab，用户点击该 tab 时
//      "activeTab !== tab.id" 为 false 不触发加载 → 点击无响应。
//      → 修复：renderTabs onclick 对"激活但未加载"（sessionId ≠ tab.id）
//        的 chat tab 强制走 activateTab → switchSession 加载历史。
//
// 运行: node cmd/contextloom-gui/tests/tab_restore_retry_test.js
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
    focus() {}, setSelectionRange() {}, scrollTop: 0, scrollHeight: 0,
    getBoundingClientRect: () => ({ width: 10, height: 20, top: 0, left: 0, right: 10, bottom: 20 }),
    ...extra,
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
let reloaded = 0;
const window = { innerWidth: 1280, addEventListener() {}, location: { reload() { reloaded++; } } };
const __ls = new Map();
const localStorage = {
  getItem: (k) => __ls.has(k) ? __ls.get(k) : null,
  setItem: (k, v) => __ls.set(k, String(v)),
  removeItem: (k) => __ls.delete(k),
};
class AbortControllerStub { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
class TerminalStub { constructor() {} loadAddon() {} open() {} onData() {} onResize() {} write() {} dispose() {} focus() {} }
class FitAddonStub { fit() {} }

const fetchCalls = [];
const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console, setTimeout, clearTimeout, setInterval, clearInterval,
  AbortController: AbortControllerStub, requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error,
  fetch: async (url, opts) => {
    fetchCalls.push({ url, body: opts && opts.body ? JSON.parse(opts.body) : null });
    if (url === "/api/config") return { ok: true, json: async () => ({ gui: "http://127.0.0.1:18765/" }) };
    if (url === "/api/projects") {
      const cur = vm.runInContext("state.projectDir", ctx) || "/projA";
      return { ok: true, json: async () => ({ current: cur, projects: ["/projA", "/projB"] }) };
    }
    if (url === "/api/projects/switch" || url === "/api/projects/open") {
      const body = opts && opts.body ? JSON.parse(opts.body) : {};
      return { ok: true, json: async () => ({ dir: body.path, pid: 1 }) };
    }
    return { ok: true, json: async () => ({}) };
  },
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() { reloaded++; } },
  getComputedStyle: () => ({ lineHeight: "20px" }),
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

const wsInstances = [];
class WebSocketStub {
  constructor(url) { this.url = url; this.readyState = 0; this.sent = []; wsInstances.push(this); ctx._ws = this; }
  send(d) { this.sent.push(JSON.parse(d)); }
  close() { this.readyState = WebSocketStub.CLOSED; }
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

async function openWS(ws) {
  ws.readyState = WebSocketStub.OPEN;
  ws.onopen();
  await sleep(10);
}
function respond(ws, method, result, ok = true) {
  const req = ws.sent.find(m => m.method === method && !m._responded);
  if (!req) throw new Error("未找到未响应请求: " + method + "（已发送: " + ws.sent.map(m => m.method).join(",") + "）");
  req._responded = true;
  ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock error" }) });
}
async function waitReq(ws, method, timeoutMs = 2000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const r = ws.sent.find(m => m.method === method && !m._responded);
    if (r) return r;
    await sleep(5);
  }
  throw new Error("等待请求超时: " + method + "（已发送: " + ws.sent.map(m => m.method).join(",") + "）");
}
async function openChat(ws, uuid) {
  const p = ctx.openChatTab(uuid);
  await waitReq(ws, "chat.loadSession");
  respond(ws, "chat.loadSession", { uuid, mode: "chat", messages: [{ role: "user", content: "hi" }] });
  await p;
  await sleep(5);
}

// 从 stub DOM 中取右侧 tab 元素（renderTabs 渲染到 #tabBar 的 _children）
function findTabEl(id) {
  const bar = els.get("#tabBar");
  const kids = bar && bar._children ? bar._children : [];
  return kids.find(e => e.dataset && e.dataset.id === id);
}

async function main() {
  console.log("── 场景 1：切换项目后恢复 loadSession 失败 → 自动重试成功 ──");
  ctx.init();
  await sleep(20);
  const ws1 = ctx._ws;
  await openWS(ws1);
  respond(ws1, "chat.listSessions", [
    { uuid: "sess-A1", title: "A 会话 1", updatedAt: new Date().toISOString() },
  ]);
  await sleep(20);
  vm.runInContext("state.projectDir = '/projA'", ctx);
  await openChat(ws1, "sess-A1");
  assert(getState().sessionId === "sess-A1", "A1 会话内容已加载");

  // 切到 B（首次访问：backend 冷启动，loadSession 会失败一次再重试成功）
  vm.runInContext("switchingProject = false", ctx);
  await ctx.switchProject("/projB");
  await sleep(30);

  const ws2 = ctx._ws;
  await openWS(ws2);
  // B 无持久化 tab；B 会话列表返回空。注意：A 的 tab 【必须保留】在栏上
  // （用户明确要求"切换/添加项目时右侧旧项目的会话 tab 要保留在界面上"），
  // 只是标记为 foreign、点击需切回 A。
  await waitReq(ws2, "chat.listSessions");
  respond(ws2, "chat.listSessions", []);
  await sleep(30);
  const bIds = getState().tabs.filter(t => t.kind === "chat").map(t => t.id);
  assert(bIds.includes("sess-A1"),
    "切到 B（无会话）：A 的会话 tab 仍保留在右侧栏上（跨项目保留）");
  assert(vm.runInContext("isForeignTab(state.tabs.find(t=>t.id==='sess-A1'))", ctx),
    "A 的 tab 标记为 foreign（点击可切回 A）");

  // 切回 A：此时模拟 backend 未就绪 → loadSession 第一次失败
  vm.runInContext("switchingProject = false", ctx);
  await ctx.switchProject("/projA");
  await sleep(30);
  // 多连接并行：切回 A 复用最初的 A 连接（不新建）——loadSession 走活跃连接
  const wsAConn = vm.runInContext("state.ws", ctx);
  assert(wsAConn === ws1 || vm.runInContext("state.activeDir", ctx) === "/projA",
    "切回 A 复用 A 连接（秒拉起，不新建等待）");
  assert(getState().pendingRestoreTab === "sess-A1" || getState().activeTab === "sess-A1",
    "切回 A 后恢复 tab 指向 A1");

  // 第一次 loadSession 失败（respond ok=false）→ 必须保留 pendingRestoreTab 并重试
  await waitReq(wsAConn, "chat.loadSession");
  respond(wsAConn, "chat.loadSession", null, false);
  await sleep(30);
  assert(getState().pendingRestoreTab === "sess-A1", "loadSession 失败后保留待恢复标记（等待重试）");

  // 重试：1s 后自动再发 loadSession（backend 就绪）→ 成功恢复内容
  await sleep(1100);
  await waitReq(wsAConn, "chat.loadSession");
  respond(wsAConn, "chat.loadSession", { uuid: "sess-A1", mode: "chat", messages: [{ role: "user", content: "hi" }, { role: "assistant", content: "hello" }] });
  await sleep(30);
  assert(getState().sessionId === "sess-A1", "重试成功后会话内容已恢复（sessionId=A1）");
  assert(getState().activeTab === "sess-A1", "恢复后 activeTab=A1");

  console.log("── 场景 2：恢复失败后点击激活但未加载的 tab → 强制加载 ──");
  // 直接模拟"activeTab 指向 A1 但 sessionId 为空"（恢复失败的中间态）
  vm.runInContext(`
    state.activeTab = "sess-A1";
    state.sessionId = "";
    renderTabs();
  `, ctx);
  // 从 stub DOM 拿到 tab 元素并点击 → 应触发 switchSession 加载（activeTab===tab.id 但 sessionId 不匹配）
  const tabElA1 = findTabEl("sess-A1");
  assert(!!tabElA1, "tabBar 渲染了 A1 tab 元素");
  tabElA1.onclick();
  await waitReq(wsAConn, "chat.loadSession");
  respond(wsAConn, "chat.loadSession", { uuid: "sess-A1", mode: "chat", messages: [{ role: "user", content: "hi" }] });
  await sleep(30);
  assert(getState().sessionId === "sess-A1",
    "点击'激活但未加载'的 tab 强制走 switchSession 加载（不再点击无响应）");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });