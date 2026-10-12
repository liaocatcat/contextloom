// 多项目并行 WS 通道（用户诉求："为什么不允许 gui 与 backend 之间高并发
// 多线程并行？切换不同项目不同对话互不干扰，线程不活跃十分钟时自动断开
// 即可，需要再重连"）—— 前端行为回归测试
// 运行: node cmd/contextloom-gui/tests/multi_conn_test.js
//
// 验证的能力：
//   1. 每个项目一条独立 WS 连接（连接 URL 带 ?dir=<proj>，代理按项目路由）；
//   2. 切换项目【复用】已有连接（不重连不等 onopen）→ 切换即秒拉起；旧项目
//      连接保留（其 backend 在途流不中断，继续经该连接推送后台缓冲）；
//   3. 非活跃项目连接【10 分钟无活动】自动断开（startConnSweeper）；
//   4. 有在途流的连接【不】自动断开（互不干扰保底）；
//   5. 事件按项目归属隔离：非活跃项目事件只累积该连接后台缓冲，不污染
//      活跃界面；切回时缓冲合并恢复渲染。
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
  querySelector: (s) => { if (s.startsWith("#")) { let el = els.get(s); if (!el) el = makeEl(s); return el; } return els.get(s) || null; },
  querySelectorAll: () => [], getElementById: (id) => { let el = els.get("#" + id); if (!el) el = makeEl("#" + id); return el; },
  addEventListener() {}, createElement: (tag) => makeEl("el:" + tag, { tagName: tag.toUpperCase(), querySelector: () => makeEl("sub:" + tag), querySelectorAll: () => [] }),
  createDocumentFragment: () => makeEl("frag", { children: [] }), body: { appendChild() {} },
};
const window = { innerWidth: 1280, addEventListener() {}, location: {} };
const __ls = new Map();
const localStorage = {
  getItem: (k) => __ls.has(k) ? __ls.get(k) : null,
  setItem: (k, v) => __ls.set(k, String(v)),
  removeItem: (k) => __ls.delete(k),
};
class AbortControllerStub { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
function TerminalStub() {}
TerminalStub.prototype.loadAddon = function () {}; TerminalStub.prototype.open = function () {};
TerminalStub.prototype.onData = function () {}; TerminalStub.prototype.onResize = function () {};
TerminalStub.prototype.write = function () {}; TerminalStub.prototype.dispose = function () {}; TerminalStub.prototype.focus = function () {};
function FitAddonStub() {}

__ls.set("contextloom.openChatTabs.v2", JSON.stringify({ "/projA": [{ id: "sess-A1", title: "A 会话 1" }], "/projB": [{ id: "sess-B1", title: "B 会话 1" }] }));

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
      return { ok: true, json: async () => ({ current: cur, projects: ["/projA", "/projB"], backends: { "/projA": "http://127.0.0.1:19001/?token=a", "/projB": "http://127.0.0.1:19002/?token=b" } }) };
    }
    if (url === "/api/projects/switch" || url === "/api/projects/open") {
      const body = opts && opts.body ? JSON.parse(opts.body) : {};
      return { ok: true, json: async () => ({ dir: body.path, pid: 1 }) };
    }
    return { ok: true, json: async () => ({}) };
  },
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() {} },
  getComputedStyle: () => ({ lineHeight: "20px" }),
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

// ── WebSocket stub：手动触发，记录 url 与发送 ──
const wsInstances = [];
class WebSocketStub {
  constructor(url) { this.url = url; this.readyState = 0; this.sent = []; this.closed = false; wsInstances.push(this); ctx._ws = this; }
  send(d) { this.sent.push(JSON.parse(d)); }
  close() { this.readyState = WebSocketStub.CLOSED; this.closed = true; }
}
WebSocketStub.CONNECTING = 0; WebSocketStub.OPEN = 1; WebSocketStub.CLOSED = 3;
ctx.WebSocket = WebSocketStub;

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; console.log("  ✅ " + msg); }
  else { failed++; console.log("  ❌ " + msg); }
}
const sleep = (ms) => new Promise(r => setTimeout(r, ms));
const getState = () => vm.runInContext("state", ctx);
const stateEval = (expr) => vm.runInContext(expr, ctx);

async function openWS(ws) { ws.readyState = WebSocketStub.OPEN; ws.onopen(); await sleep(10); }
function respond(ws, method, result, ok = true) {
  const req = ws.sent.find(m => m.method === method && !m._responded);
  if (!req) throw new Error("未找到未响应请求: " + method);
  req._responded = true;
  ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock" }) });
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
let sweepTimerReal = null; // 记录真实 setInterval id（供手动触发扫描器）

async function main() {
  console.log("── 场景 1：每个项目独立 WS 连接 + 切换复用（秒拉起） ──");
  ctx.init();
  await sleep(20);
  const wsA = ctx._ws;
  await openWS(wsA);
  await waitReq(wsA, "chat.listSessions");
  respond(wsA, "chat.listSessions", [
    { uuid: "sess-A1", title: "A 会话 1", updatedAt: new Date().toISOString() },
  ]);
  await sleep(20);
  assert(stateEval("state.activeDir") === "/projA", "初始活跃项目为 A");
  assert(wsA.url.includes("dir=" + encodeURIComponent("/projA")), "A 连接 URL 带项目路由参数 ?dir=/projA（代理按项目分发）");
  assert(Object.keys(stateEval("state.conns")).length === 1, "初始仅 1 条连接（项目 A）");

  // 切到 B：应新建 B 连接（A 连接保留）
  stateEval("switchingProject = false");
  await ctx.switchProject("/projB");
  await sleep(50);
  const wsB = ctx._ws;
  assert(wsB !== wsA, "切到 B 新建了 B 连接");
  assert(wsB.url.includes("dir=" + encodeURIComponent("/projB")), "B 连接 URL 带项目路由参数 ?dir=/projB");
  assert(Object.keys(stateEval("state.conns")).length === 2, "连接池保留 A + B 两条连接（并行不互杀）");
  assert(stateEval("state.activeDir") === "/projB", "活跃项目已切到 B");

  // 切回 A：应复用 wsA（不新建、不重连）——"秒拉起无缝"核心
  const wsCountBefore = wsInstances.length;
  stateEval("switchingProject = false");
  await ctx.switchProject("/projA");
  await sleep(50);
  assert(wsInstances.length === wsCountBefore, "切回 A 未新建连接（复用已有 A 连接）");
  assert(stateEval("state.ws") === wsA, "切回 A 后活跃连接就是最初的 A 连接");
  assert(stateEval("state.activeDir") === "/projA", "活跃目录切回 A");

  console.log("── 场景 2：非活跃项目在途流不中断（后台缓冲隔离） ──");
  // 当前 A 活跃；B 非活跃。向 B 连接推 agent.stream（模拟 B 项目后台对话
  // 继续生成）→ 应累积到 B 连接的后台缓冲，不污染 A 界面。
  wsB.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.stream", data: { delta: "B 项目正在生成", sessionId: "sess-B1" } }),
  });
  await sleep(5);
  const bBuff = stateEval("state.conns['/projB'].background") ;
  assert(bBuff && bBuff["sess-B1"] && bBuff["sess-B1"].buf === "B 项目正在生成",
    "非活跃项目 B 的流累积在 B 连接后台缓冲（不串到 A）");
  assert(stateEval("state.backgroundStreams['sess-B1']") === undefined,
    "B 的流未污染 A 的全局后台缓冲（不同项目互不干扰）");
  // B 的流计数 > 0：该连接"有在途流"→ 不应被空闲扫描断开
  assert(stateEval("state.conns['/projB'].activeStreams") === 1, "B 连接在途流计数 = 1");
  // B 流结束 → 后台缓冲清除、计数归零
  wsB.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.done", data: { sessionId: "sess-B1" } }),
  });
  await sleep(5);
  assert(stateEval("state.conns['/projB'].activeStreams") === 0, "B 流结束后计数归零");

  console.log("── 场景 3：非活跃连接 10 分钟无活动自动断开（空闲清理） ──");
  assert(stateEval("state.connSweeper") !== 0, "空闲连接扫描器已启动（每 30s 巡检）");
  // 把 B 连接 lastActive 拨回 11 分钟前 → 手动触发扫描 → B 应被关闭移除
  stateEval("state.conns['/projB'].lastActive = Date.now() - 11*60*1000");
  stateEval("sweepIdleConns()");
  assert(stateEval("state.conns['/projB']") === undefined, "B 连接超 10 分钟无活动被自动断开移除");
  assert(wsB.closed === true, "B 连接已 close（需要再重连时由切回动作重建）");
  // 活跃连接 A 不受影响
  assert(stateEval("state.conns['/projA']") !== undefined, "活跃连接 A 保留不受扫描影响");

  console.log("── 场景 4：活跃连接 + 有在途流的连接永不自动断开 ──");
  // 活跃连接(A) lastActive 极旧也不断（切回必须秒拉起）
  stateEval("state.conns['/projA'].lastActive = Date.now() - 30*60*1000");
  stateEval("sweepIdleConns()");
  assert(stateEval("state.conns['/projA']") !== undefined, "活跃连接(A)即使空闲很久也保留");
  // 重建 B 连接（模拟切回 B 后再次切走），B 有在途流 → 不因空闲断开
  stateEval("switchingProject = false");
  await ctx.switchProject("/projB");
  await sleep(50);
  const wsB2 = ctx._ws;
  assert(wsB2 !== wsB, "切到 B 新建了 B 连接（此前已断开，需重连）——符合'需要再重连'");
  stateEval("state.conns['/projB'].activeStreams = 1");
  stateEval("state.conns['/projB'].lastActive = Date.now() - 30*60*1000");
  stateEval("sweepIdleConns()");
  assert(stateEval("state.conns['/projB']") !== undefined, "有在途流的非活跃连接(B)不自动断开（互不干扰保底）");

  console.log("── 场景 5：非活跃项目事件隔离不污染活跃界面 ──");
  // 当前 B 活跃，A 非活跃；确保 A 连接在池中（场景 4 的扫描可能已按规则
  // 断开"非活跃+无流+超时"的 A——这里按"需要再重连"语义切回重建再切走）
  if (stateEval("state.conns['/projA']") === undefined) {
    stateEval("switchingProject = false");
    await ctx.switchProject("/projA");
    await sleep(50);
    stateEval("switchingProject = false");
    await ctx.switchProject("/projB");
    await sleep(50);
  }
  const wsBActive = stateEval("state.ws");
  // 向 A 连接推 A 会话的 stream → 只累积 A 连接缓冲
  // （取当前池中的 A 连接 ws——场景 4 的扫描可能已断开旧的 wsA）
  const wsACur = stateEval("state.conns['/projA'].ws");
  assert(!!wsACur, "A 连接存在且可接收事件");
  wsACur.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.stream", data: { delta: "A 项目后台生成", sessionId: "sess-A1" } }),
  });
  await sleep(5);
  const aBuff = stateEval("state.conns['/projA'].background");
  assert(!!aBuff && aBuff["sess-A1"] && aBuff["sess-A1"].buf === "A 项目后台生成",
    "A 项目流累积在 A 连接后台缓冲（项目间隔离）");
  assert(stateEval("state.streamBuf") === "", "活跃 B 界面的流缓冲未被 A 事件污染");
  // B 流 done 事件（活跃连接）正常处理：活跃连接流计数由 state.streaming
  // 跟踪（非活跃连接才用 conn.activeStreams 判空闲断开），done 走
  // handleEvent.endStream 正常收尾——断言不抛错、连接保留、无残留缓冲。
  const bbgBefore = stateEval("state.conns['/projB'].background");
  wsBActive.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.done", data: { sessionId: "sess-B1" } }),
  });
  await sleep(5);
  assert(stateEval("state.conns['/projB']") !== undefined, "活跃连接 B 在 done 后仍保留");
  assert(stateEval("state.streaming") === false, "活跃 B 无残留前台流状态（done 正常收尾）");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });