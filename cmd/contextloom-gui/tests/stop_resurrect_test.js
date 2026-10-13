// 已停止对话"切回项目后复活"漏洞回归测试
// 运行: node cmd/contextloom-gui/tests/stop_resurrect_test.js
//
// 漏洞背景：多项目并行通道里，非活跃项目的 WS 连接（connectProj 的
// ws.onmessage 中 state.activeDir !== dir 分支）把 agent.stream delta 无条件
// 累积进 conn.background，不检查 state.stoppedStreams/stoppedSessions；
// 切回项目时 switchActiveConn 把 exist.background 合并进全局
// backgroundStreams 也无过滤 → 用户已停止的对话在切回后被恢复成
// "● 生成中"流式 UI（复活/空跑）。
//
// 验证能力：
//   1. 非活跃连接上已停流（stoppedStreams 标记）的残留 delta 不入后台缓冲；
//   2. 仅 stoppedSessions 标记（streamId 未标记）时同样不入缓冲（且镜像
//      handleEvent 把该 streamId 记入 stoppedStreams）；
//   3. 切回项目时 switchActiveConn 合并不复活已停流；
//   4. 未停止的正常后台流仍合并（防回归：既有行为兼容）；
//   5. 已停流的 agent.done 在非活跃路径到达时清理停止标记（镜像 handleEvent）；
//   6. afterProjectSwitch 归档在途流携带 streamId（与 switchSession/closeTab
//      归档一致，切回后可精确标记停止）。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub（与 multi_conn_test.js 同基建） ──
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

__ls.set("contextloom.openChatTabs.v2", JSON.stringify({ "/projA": [{ id: "sess-A", title: "A 会话" }], "/projB": [{ id: "sess-B", title: "B 会话" }] }));

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

// ── WebSocket stub：手动触发，多实例记录 ──
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
const stateEval = (expr) => vm.runInContext(expr, ctx);

async function openWS(ws) { ws.readyState = WebSocketStub.OPEN; ws.onopen(); await sleep(10); }

async function main() {
  console.log("── 前置：双项目连接，A 处于非活跃状态 ──");
  ctx.init();
  await sleep(20);
  const wsA = ctx._ws;
  await openWS(wsA);
  await sleep(20);
  stateEval("switchingProject = false");
  await ctx.switchProject("/projB");
  await sleep(50);
  assert(stateEval("state.activeDir") === "/projB", "活跃项目为 B，A 连接保留为非活跃");
  assert(stateEval("state.conns['/projA']") !== undefined, "A 连接保留在连接池（后台通道）");

  console.log("── 场景 1：非活跃连接已停流的残留 delta 不入后台缓冲 ──");
  stateEval("delete state.conns['/projA'].background");
  stateEval("state.conns['/projA'].activeStreams = 0");
  stateEval("state.stoppedStreams.set(77, 'sess-A')");
  stateEval("state.stoppedSessions.set('sess-A', true)");
  wsA.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.stream", data: { delta: "残留", sessionId: "sess-A", streamId: 77 } }),
  });
  await sleep(5);
  const bg1 = stateEval("state.conns['/projA'].background");
  assert(!(bg1 && bg1["sess-A"]), "已停流 delta 未累积进 A 连接后台缓冲（不复活前提①）");
  assert(stateEval("state.conns['/projA'].activeStreams") === 0, "已停流不递增在途流计数（不影响空闲断开判据）");

  console.log("── 场景 2：仅 stoppedSessions 标记（streamId 未标记）同样不入缓冲 ──");
  stateEval("state.stoppedSessions.set('sess-A2', true)");
  wsA.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.stream", data: { delta: "残留2", sessionId: "sess-A2", streamId: 78 } }),
  });
  await sleep(5);
  const bg2 = stateEval("state.conns['/projA'].background");
  assert(!(bg2 && bg2["sess-A2"]), "停止在途会话的 delta 未累积进后台缓冲（不复活前提②）");
  assert(stateEval("state.stoppedStreams.get(78)") === "sess-A2",
    "镜像 handleEvent：该流的 streamId 已记入 stoppedStreams（后续残留一律丢弃）");

  console.log("── 场景 3：切回项目合并不复活已停流 ──");
  stateEval("state.conns['/projA'].background = { 'sess-A': { buf: 'x', streaming: true, streamId: 77 } }");
  stateEval("delete state.backgroundStreams['sess-A']");
  stateEval("state.stoppedStreams.set(77, 'sess-A')");
  stateEval("state.stoppedSessions.set('sess-A', true)");
  await stateEval("switchActiveConn('/projA')");
  await sleep(20);
  assert(stateEval("state.backgroundStreams['sess-A']") === undefined,
    "已停流未合并回全局后台缓冲（切回不出现 '● 生成中'）");

  console.log("── 场景 4：未停止的正常后台流仍合并（防回归） ──");
  stateEval("state.conns['/projA'].background['sess-C'] = { buf: 'y', streaming: true, streamId: 88 }");
  stateEval("delete state.backgroundStreams['sess-C']");
  await stateEval("switchActiveConn('/projA')");
  await sleep(20);
  const c = stateEval("state.backgroundStreams['sess-C']");
  assert(!!c && c.buf === "y" && c.streaming === true && c.streamId === 88,
    "未停止的后台流照常合并（未停止流的一切路径不变）");

  console.log("── 场景 5：已停流的 agent.done 在非活跃路径到达时清理停止标记 ──");
  // 切回 B（A 再度非活跃），在 A 连接上派发 done
  stateEval("switchingProject = false");
  await ctx.switchProject("/projB");
  await sleep(50);
  stateEval("state.stoppedStreams.set(77, 'sess-A')");
  stateEval("state.stoppedSessions.set('sess-A', true)");
  stateEval("state.conns['/projA'].background = { 'sess-A': { buf: 'x', streaming: true, streamId: 77 } }");
  wsA.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.done", data: { sessionId: "sess-A", streamId: 77 } }),
  });
  await sleep(5);
  assert(!stateEval("state.stoppedStreams.has(77)"), "done 清理 stoppedStreams 中的 77");
  assert(!stateEval("state.stoppedSessions.has('sess-A')"), "done 清理 stoppedSessions 中的 sess-A");
  assert(stateEval("state.conns['/projA'].background['sess-A']") === undefined, "done 同步清掉该流后台缓冲");
  // 兼容分支：仅 stoppedSessions 标记（无 stoppedStreams 记录）也照常清理
  stateEval("state.stoppedSessions.set('sess-D', true)");
  wsA.onmessage({
    data: JSON.stringify({ type: "event", event: "agent.done", data: { sessionId: "sess-D", streamId: 79 } }),
  });
  await sleep(5);
  assert(!stateEval("state.stoppedSessions.has('sess-D')"), "无 streamId 记录的会话 done 也清理 stoppedSessions");

  console.log("── 场景 6：afterProjectSwitch 归档在途流携带 streamId ──");
  // 回到 A 活跃状态，前台有半截流（streamId=55），直接切到 B 触发归档
  stateEval("switchingProject = false");
  await ctx.switchProject("/projA");
  await sleep(50);
  stateEval("state.sessionId = 'sess-A6'");
  stateEval("state.streaming = true");
  stateEval("state.streamBuf = '半截'");
  stateEval("state.streamId = 55");
  stateEval("state.subTasks = new Map()");
  stateEval("delete (state.conns['/projA'].background || {})['sess-A6']");
  await ctx.afterProjectSwitch("/projB");
  await sleep(20);
  const archived = stateEval("state.conns['/projA'].background['sess-A6']");
  assert(!!archived, "在途流已归档到旧项目连接后台缓冲");
  assert(!!archived && archived.streamId === 55, "归档条目携带 streamId=55（切回后可精确标记停止）");
  assert(!!archived && archived.buf === "半截" && archived.streaming === true, "归档条目 buf/streaming 与切前一致");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });
