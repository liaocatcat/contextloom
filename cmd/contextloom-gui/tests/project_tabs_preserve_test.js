// 切换/添加项目【右侧旧项目会话 tab 保留在界面上】—— 前端行为回归测试
// 运行: node cmd/contextloom-gui/tests/project_tabs_preserve_test.js
//
// 用户实测缺陷（多轮反馈后的最终诉求）：
//   "每当切换项目目录时，会默认关闭右侧所有对话，并中止正在进行的对话，
//    切换回来也没用了。" → "切换/添加项目时，右侧旧项目的会话 tab 真的
//    不保留界面上。"
//
// 历史两代错误实现（本测试都禁止）：
//   ① location.reload() 整页重载 → 右侧 tab 全被销毁；
//   ② 快照替换 state.tabs = 目标项目快照 → 旧项目 tab 从界面消失。
// 正确语义：chat tab 带 projDir 归属，切换项目时【不清空、不替换】——
// 旧项目 tab 留在栏上（标记 foreign，点击切回），目标项目 tab 追加进来。
//
// 本测试用最小 DOM stub + mock WS/fetch 加载真实 app.js 断言：
//   1. switchProject 不 location.reload()；
//   2. 切到 B 后 A 的 2 个会话 tab 【仍在 state.tabs / 仍在 tabBar DOM 上】；
//   3. 目标项目 B 的持久化 tab 追加进栏（右侧不空）；
//   4. 切回 A 后 A 的 tab 仍在且重新成为本项目的可加载 tab；
//   5. 旧项目在途流事件仍被接收（后台缓冲，不中止对话）；
//   6. 目录规范化（尾斜杠/重复斜杠）不丢 tab。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub ──
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

// 项目 B 的持久化会话 tab（模拟用户此前在 B 项目打开的右侧会话）
__ls.set("contextloom.openChatTabs.v2", JSON.stringify({ "/projB": [{ id: "sess-B1", title: "B 会话 1" }] }));

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

// ── WebSocket stub：手动触发 onopen/onmessage，记录发送的请求 ──
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
// 右侧 tabBar 上真实渲染出的 tab id（DOM 级断言：证明"界面上保留"）
const domTabIds = () => ((els.get("#tabBar") || {})._children || []).map(e => e.dataset && e.dataset.id).filter(Boolean);

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
// 等待指定 method 的请求出现在 WS 上（不阻塞在 await 上——app.js 的
// openChatTab/switchSession 会 await 响应，必须先让它把请求发出去，
// 再从这里响应；旧写法 await openChatTab 后再 respond 会永久死锁）。
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
  respond(ws, "chat.loadSession", { uuid, mode: "chat", messages: [] });
  await p;
  await sleep(5);
}

async function main() {
  console.log("── 场景 1：切换项目时右侧旧项目 tab 保留在界面上 ──");

  ctx.init();
  await sleep(20);
  const ws1 = ctx._ws;
  await openWS(ws1);
  assert(!!ws1, "初始 WS 已建立");

  respond(ws1, "chat.listSessions", [
    { uuid: "sess-A1", title: "A 会话 1", updatedAt: new Date().toISOString() },
    { uuid: "sess-A2", title: "A 会话 2", updatedAt: new Date().toISOString() },
  ]);
  await sleep(20);
  vm.runInContext("state.projectDir = '/projA'", ctx);

  await openChat(ws1, "sess-A1");
  await openChat(ws1, "sess-A2");
  const tabsA = getState().tabs.filter(t => t.kind === "chat").map(t => t.id).sort();
  assert(tabsA.join(",") === "sess-A1,sess-A2", "项目 A 右侧已打开 2 个会话 tab");

  // 切换项目 → B
  reloaded = 0;
  vm.runInContext("switchingProject = false", ctx);
  await ctx.switchProject("/projB");
  await sleep(30);

  assert(reloaded === 0, "切换项目不再整页 reload（右侧 tab 不被销毁）");
  assert(getState().projectDir === "/projB", "当前项目已切换为 B");
  // 核心断言：旧项目 A 的 tab 【仍在内存且在 DOM 上】
  const afterSwitchIds = getState().tabs.filter(t => t.kind === "chat").map(t => t.id);
  assert(afterSwitchIds.includes("sess-A1") && afterSwitchIds.includes("sess-A2"),
    "切到 B 后 A 的 2 个会话 tab 仍在 state.tabs（不被替换/清空）");
  const domIds = domTabIds();
  assert(domIds.includes("sess-A1") && domIds.includes("sess-A2"),
    "切到 B 后 A 的 2 个会话 tab 仍渲染在右侧 tabBar 上（界面上保留）");
  const aTab = getState().tabs.find(t => t.id === "sess-A1");
  assert(aTab && aTab.projDir === "/projA", "A 的 tab 带 projDir=/projA 归属（可点击切回）");

  // 原地切换会重连 WS：新连接应发出会话列表请求
  const ws2 = ctx._ws;
  assert(ws2 && ws2 !== ws1, "切换项目后 WS 已重连（指向新 backend）");
  await openWS(ws2);
  await waitReq(ws2, "chat.listSessions");
  respond(ws2, "chat.listSessions", [
    { uuid: "sess-B1", title: "B 会话 1", updatedAt: new Date().toISOString() },
  ]);
  await sleep(30);
  const allIds = getState().tabs.filter(t => t.kind === "chat").map(t => t.id);
  assert(allIds.includes("sess-B1"), "项目 B 的持久化会话 tab 已追加进栏（右侧不空）");
  assert(allIds.includes("sess-A1") && allIds.includes("sess-A2"),
    "B 的 tab 加入后 A 的 tab 依然保留（跨项目并存）");

  console.log("── 场景 2：切回原项目 → A 的 tab 重新归属当前项目 ──");
  vm.runInContext("switchingProject = false", ctx);
  await ctx.switchProject("/projA");
  await sleep(30);
  const backIds = getState().tabs.filter(t => t.kind === "chat").map(t => t.id);
  assert(backIds.includes("sess-A1") && backIds.includes("sess-A2") && backIds.includes("sess-B1"),
    "切回 A 后 A、B 的 tab 都还在（双向保留）");
  assert(!vm.runInContext("isForeignTab(state.tabs.find(t=>t.id==='sess-A1'))", ctx),
    "切回 A 后 A 的 tab 不再是 foreign（可直接加载会话）");
  assert(vm.runInContext("isForeignTab(state.tabs.find(t=>t.id==='sess-B1'))", ctx),
    "切回 A 后 B 的 tab 标记为 foreign（点击可切回 B）");
  assert(reloaded === 0, "两次切换全程无整页 reload");

  console.log("── 场景 3：切换过程中旧项目的在途流不中止 ──");
  const target = "sess-A1";
  const bufBefore = getState().backgroundStreams[target] ? getState().backgroundStreams[target].buf : "";
  // 多连接并行：切回 A 后活跃连接应仍是【最初那条 A 连接】（复用，未新建）
  // ——这正是"切回秒拉起无缝"的核心：连接保持，backend 在途流不中断。
  const activeWs = vm.runInContext("state.ws", ctx);
  const activeDirNow = vm.runInContext("state.activeDir", ctx);
  assert(activeWs === ws1 && activeDirNow === "/projA",
    "切回 A 复用了最初的 A 连接（无新建无重连，秒拉起）");
  activeWs.onmessage({
    data: JSON.stringify({
      type: "event", event: "agent.stream",
      data: { delta: "仍在生成的内容", sessionId: target },
    }),
  });
  await sleep(5);
  const bs = getState().backgroundStreams[target];
  const got = bs && bs.buf === bufBefore + "仍在生成的内容";
  const foreground = getState().sessionId === target && getState().streamBuf.includes("仍在生成的内容");
  assert(got || foreground,
    "旧项目会话的流事件被接收（后台缓冲/前台渲染）——对话未中止，切回可见");

  console.log("── 场景 4：目录路径规范化（尾部斜杠/重复斜杠）不丢 tab ──");
  const tabsBefore4 = getState().tabs.filter(t => t.kind === "chat").map(t => t.id).sort().join(",");
  vm.runInContext("switchingProject = false", ctx);
  await ctx.switchProject("/projB/");
  await sleep(30);
  assert(getState().projectDir === "/projB", "带尾斜杠的切换被规范化为 /projB");
  vm.runInContext("switchingProject = false", ctx);
  await ctx.switchProject("/projA//");
  await sleep(30);
  assert(getState().projectDir === "/projA", "重复斜杠的切换被规范化为 /projA");
  const tabsAfter4 = getState().tabs.filter(t => t.kind === "chat").map(t => t.id).sort().join(",");
  assert(tabsAfter4 === tabsBefore4, "带斜杠来回切换后右侧会话 tab 完整保留（键一致）");

  console.log("── 场景 5：点击其他项目的 tab → 切回该项目并激活该会话 ──");
  // 当前在 A，B 的 tab（sess-B1）在栏上标记 foreign。点击它应：
  //  ① POST /api/projects/switch 到 B；② 切换后 B 的 tab 变为本项目的 tab。
  fetchCalls.length = 0;
  const tabB1 = getState().tabs.find(t => t.id === "sess-B1");
  assert(!!tabB1 && vm.runInContext("isForeignTab(state.tabs.find(t=>t.id==='sess-B1'))", ctx),
    "起点：当前在 A，B 的 tab 为 foreign");
  const clickRet = ctx.openChatTab("sess-B1");
  await sleep(30);
  await clickRet;
  await sleep(30);
  assert(getState().projectDir === "/projB", "点击 foreign tab 已切回项目 B");
  assert(!vm.runInContext("isForeignTab(state.tabs.find(t=>t.id==='sess-B1'))", ctx),
    "切换后 B 的 tab 不再是 foreign（可直接加载会话）");
  const switched = fetchCalls.some(c => c.url === "/api/projects/switch" && c.body && c.body.path === "/projB");
  assert(switched, "点击 foreign tab 发出了 /api/projects/switch(/projB)");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });
