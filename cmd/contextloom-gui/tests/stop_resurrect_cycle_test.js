// 已停止/已结束对话"切走→切回→再切走→再切回"复活漏洞回归测试（第二轮）
// 运行: node cmd/contextloom-gui/tests/stop_resurrect_cycle_test.js
//
// 漏洞背景（用户实测："老问题依旧，切换后仍然把已正常停止的其他目录的
// 对话直接激活"）：
//   多项目并行通道里，前台在途流在【切走】时归档到旧项目连接的
//   conn.background；【切回】时 switchActiveConn 把 conn.background 条目
//   拷贝进全局 backgroundStreams 并恢复前台流式渲染——但旧实现只拷贝
//   【引用】、不从 conn.background 删除：
//     ① restoreCurrentSession 把条目从全局移交前台（delete 全局键）后，
//        conn.background 里的陈旧副本不再随流更新（前台 delta 只写
//        streamBuf），却永远保留 {streaming:true} 与旧 buf；
//     ② 该流被用户停止（或自然结束）时，停止标记 stoppedStreams/
//        stoppedSessions 会被 interrupted done 清理；
//     ③ 下一次切回项目时，陈旧副本因标记已清而通过过滤被再次合并 +
//        恢复渲染 → 已正常停止的对话被"直接激活"成 ● 生成中（复活），
//        且 buf 是旧快照（与真实内容错位）。
//
// 修复（本测试断言的契约）：
//   合并/归档一律【移动语义】——任何时刻流缓冲只有一份权威副本：
//   - switchActiveConn 合并后从 conn.background 删除条目（丢弃的也不留）；
//   - afterProjectSwitch 把全局后台缓冲全部移回旧连接缓冲（缓冲跨切换
//     连续），前台流归档用赋值覆盖而非 if(!exists)（不留陈旧副本）；
//   - 已停止的前台流不归档（停止标记被 done 清理后不得复活）。
//
// 验证能力：
//   1. 停止（乐观重置+标记+done 清理）后，切走再切回不得复活（核心）；
//   2. 自然结束（无停止）后，切走切回同样不得复活；
//   3. 未停止的在途流跨两轮切走/切回缓冲连续（P1→P1P2→P1P2P3 不丢）；
//   4. 合并为移动语义：切回后连接缓冲无残留副本（复活前提被切断）。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub（与 stream_identity_test.js 同构） ──
const els = new Map();
function makeEl(sel, extra = {}) {
  const el = {
    selector: sel, style: {},
    classList: { _set: new Set(), add(c) { this._set.add(c); }, remove(c) { this._set.delete(c); }, toggle(c, on) { if (on === undefined) on = !this._set.has(c); on ? this._set.add(c) : this._set.delete(c); }, contains(c) { return this._set.has(c); } },
    dataset: {}, listeners: {}, innerHTML: "", textContent: "", value: "", _children: [], children: [],
    appendChild(c) { if (c) { this._children.push(c); this.children.push(c); } return c; },
    remove() {}, querySelector: () => null, querySelectorAll: () => [],
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    focus() {}, scrollTop: 0, scrollHeight: 0, clientHeight: 0, offsetParent: null,
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
const window = { innerWidth: 1280, addEventListener() {}, location: { reload() {} } };
const __ls = new Map();
const localStorage = {
  getItem: (k) => __ls.has(k) ? __ls.get(k) : null,
  setItem: (k, v) => __ls.set(k, String(v)),
  removeItem: (k) => __ls.delete(k),
};
class AbortControllerStub { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
class TerminalStub { constructor() {} loadAddon() {} open() {} onData() {} write() {} dispose() {} focus() {} }
class FitAddonStub { fit() {} }

// 两个项目各有一个持久化会话 tab（切换后右侧恢复用）
__ls.set("contextloom.openChatTabs.v2", JSON.stringify({
  "/projA": [{ id: "sess-A", title: "A 会话" }],
  "/projB": [{ id: "sess-B", title: "B 会话" }],
}));

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

// ── WebSocket stub：多实例（每个项目一条连接） ──
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
function pushEvent(ws, evt, data) {
  ws.onmessage({ data: JSON.stringify({ type: "event", event: evt, data }) });
}
const bgOf = (dir, sid) => stateEval(`(state.conns['${dir}'].background || {})['${sid}']`);

// switchProjectTo：封装"切项目 + 等 restore 的 loadSession 请求 + 响应"
// （A 项目的连接是复用路径，切换后 restoreCurrentSession 会拉会话历史）
async function switchProjectTo(dir) {
  stateEval("switchingProject = false");
  await ctx.switchProject(dir);
  await sleep(40);
}

async function main() {
  console.log("── 前置：项目 A 活跃，sess-A 前台流式生成中（streamId=100） ──");
  ctx.init();
  await sleep(20);
  const wsA = ctx._ws;
  await openWS(wsA);
  await waitReq(wsA, "chat.listSessions");
  respond(wsA, "chat.listSessions", [{ uuid: "sess-A", title: "A 会话", updatedAt: new Date().toISOString() }]);
  await sleep(20);
  // init 阶段 restoreCurrentSession 的 currentSession 探测：空会话（保持空状态）
  const curReq = await waitReq(wsA, "chat.currentSession", 500).catch(() => null);
  if (curReq) respond(wsA, "chat.currentSession", { uuid: "" });
  await sleep(10);

  // 打开 sess-A 为当前会话 + 前台流（streamId=100）
  stateEval("state.sessionId = 'sess-A'");
  stateEval("state.activeTab = 'sess-A'");
  stateEval("if (!state.tabs.find(t => t.kind === 'chat' && t.id === 'sess-A')) state.tabs.push({ kind: 'chat', id: 'sess-A', title: 'A 会话', projDir: '/projA' })");
  pushEvent(wsA, "agent.stream", { delta: "abc", sessionId: "sess-A", streamId: 100 });
  await sleep(15);
  assert(stateEval("state.streaming === true && state.streamId === 100 && state.streamBuf === 'abc'"),
    "前置：sess-A 前台流式生成中（streamId=100，buf=abc）");

  console.log("── 场景 1：切走→切回→停止→再切走→再切回：已停止的对话不得复活 ──");
  // ① 切到 B：前台流归档到 A 连接缓冲
  await switchProjectTo("/projB");
  await sleep(20);
  const arc = bgOf("/projA", "sess-A");
  assert(!!arc && arc.streaming === true && arc.streamId === 100 && arc.buf === "abc",
    "切走：前台流归档到 A 连接缓冲（streaming=true, streamId=100, buf=abc）");
  // ② 非活跃期间 delta 继续累积（缓冲连续）
  pushEvent(wsA, "agent.stream", { delta: "def", sessionId: "sess-A", streamId: 100 });
  await sleep(5);
  assert(stateEval("(state.conns['/projA'].background || {})['sess-A'] && state.conns['/projA'].background['sess-A'].buf === 'abcdef'"),
    "非活跃期间 delta 继续累积（缓冲连续：abcdef）");
  // ③ 切回 A：合并（移动语义）→ 恢复前台流式
  await switchProjectTo("/projA");
  await waitReq(wsA, "chat.loadSession");
  respond(wsA, "chat.loadSession", { uuid: "sess-A", messages: [] });
  await sleep(40);
  assert(stateEval("state.streaming === true && state.streamBuf === 'abcdef'"),
    "切回：流式恢复且缓冲完整（abcdef）");
  assert(stateEval("state.backgroundStreams['sess-A'] === undefined"),
    "切回后全局缓冲已移交前台（无残留条目）");
  assert(bgOf("/projA", "sess-A") === undefined,
    "合并为移动语义：连接缓冲不留陈旧副本（复活前提被切断）");
  // ④ 用户停止：乐观重置 + 标记；后端 interrupted done 清理标记
  ctx.stopStream("sess-A");
  await sleep(5);
  assert(stateEval("state.streaming === false"), "停止后回到可发送态（已正常停止）");
  assert(stateEval("state.stoppedStreams.has(100) === true"), "停止标记已记录 streamId=100");
  pushEvent(wsA, "agent.done", { sessionId: "sess-A", streamId: 100, interrupted: true });
  await sleep(5);
  assert(stateEval("state.stoppedStreams.has(100) === false && state.stoppedSessions.has('sess-A') === false"),
    "interrupted done 清理停止标记（正常收尾）");
  // ⑤ 再切走 → 再切回：不得复活
  await switchProjectTo("/projB");
  await sleep(20);
  assert(bgOf("/projA", "sess-A") === undefined, "再切走：已停止的流未被归档（连接缓冲无条目）");
  await switchProjectTo("/projA");
  await waitReq(wsA, "chat.loadSession");
  respond(wsA, "chat.loadSession", { uuid: "sess-A", messages: [] });
  await sleep(40);
  assert(stateEval("state.streaming === false"), "★ 切回后不得复活：无 ● 生成中（核心断言）");
  assert(stateEval("state.backgroundStreams['sess-A'] === undefined"), "★ 全局缓冲无复活条目");
  assert(bgOf("/projA", "sess-A") === undefined, "★ 连接缓冲无复活条目");

  console.log("── 场景 2：自然结束（无停止）的对话，切走切回同样不得复活 ──");
  pushEvent(wsA, "agent.stream", { delta: "xyz", sessionId: "sess-A", streamId: 200 });
  await sleep(15);
  assert(stateEval("state.streaming === true && state.streamId === 200"), "前置：sess-A 再次生成中（streamId=200）");
  pushEvent(wsA, "agent.done", { sessionId: "sess-A", streamId: 200 });
  await sleep(15);
  assert(stateEval("state.streaming === false"), "自然结束：前台回到非生成态");
  await switchProjectTo("/projB");
  await sleep(20);
  await switchProjectTo("/projA");
  await waitReq(wsA, "chat.loadSession");
  respond(wsA, "chat.loadSession", { uuid: "sess-A", messages: [] });
  await sleep(40);
  assert(stateEval("state.streaming === false"), "★ 自然结束后切回不复活（无 ● 生成中）");
  assert(stateEval("state.backgroundStreams['sess-A'] === undefined"), "★ 自然结束后无后台缓冲残留");

  console.log("── 场景 3：未停止的在途流跨两轮切走/切回缓冲连续（防回归） ──");
  pushEvent(wsA, "agent.stream", { delta: "P1", sessionId: "sess-A", streamId: 300 });
  await sleep(15);
  assert(stateEval("state.streaming === true && state.streamId === 300 && state.streamBuf === 'P1'"), "前置：sess-A 流式（streamId=300, P1）");
  // 第一轮：切走（归档 P1）→ 非活跃累积 P2 → 切回（恢复前台，缓冲 P1P2）
  await switchProjectTo("/projB");
  await sleep(20);
  pushEvent(wsA, "agent.stream", { delta: "P2", sessionId: "sess-A", streamId: 300 });
  await sleep(5);
  await switchProjectTo("/projA");
  await waitReq(wsA, "chat.loadSession");
  respond(wsA, "chat.loadSession", { uuid: "sess-A", messages: [] });
  await sleep(40);
  assert(stateEval("state.streaming === true && state.streamBuf === 'P1P2'"), "第一轮切回：缓冲连续（P1P2）");
  // 第二轮：再切走（重新归档 P1P2）→ 非活跃累积 P3 → 切回（P1P2P3）
  await switchProjectTo("/projB");
  await sleep(20);
  pushEvent(wsA, "agent.stream", { delta: "P3", sessionId: "sess-A", streamId: 300 });
  await sleep(5);
  await switchProjectTo("/projA");
  await waitReq(wsA, "chat.loadSession");
  respond(wsA, "chat.loadSession", { uuid: "sess-A", messages: [] });
  await sleep(40);
  assert(stateEval("state.streaming === true && state.streamBuf === 'P1P2P3'"),
    "★ 第二轮切回：缓冲仍连续（P1P2P3，半截内容不丢）");
  assert(bgOf("/projA", "sess-A") === undefined, "★ 合并移动语义：两轮后连接缓冲无残留");
  // 收尾：停止该流并清理标记（不影响后续断言）
  ctx.stopStream("sess-A");
  pushEvent(wsA, "agent.done", { sessionId: "sess-A", streamId: 300, interrupted: true });
  await sleep(10);
  assert(stateEval("state.streaming === false"), "收尾：停止后回到可发送态");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });
