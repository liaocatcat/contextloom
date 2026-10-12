// 多对话并行流身份（streamId）—— 前端集成测试
// 覆盖用户报告：GUI 同时对话多个窗口时状态错乱——某对话明明已停下，
// 却因别的对话仍在激活而"诡异再次触发、难以停下"。
//
// 根因：事件仅带 sessionId，前端无法区分同会话的新旧流/已停止流的延迟事件；
// beginStream 对任何通过会话校验的 delta 都会重建流式气泡 → 已停下的对话
// 被旧流残留 delta 再次"激活"，且旧流 done 会把新流提前结束（难以停下）。
//
// 修复：后端为每条流分配全局唯一 streamId（uid）随事件下发；前端
//   - stoppedStreams：用户显式停止的流，其残留事件一律丢弃；
//   - state.streamId：前台流身份，同会话中 streamId 不匹配的旧流事件丢弃。
// 运行: node cmd/contextloom-gui/tests/stream_identity_test.js
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub（与 stream_session_test.js 同构）──
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
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() {} },
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
function pushEvent(evt, data) {
  ctx._ws.onmessage({ data: JSON.stringify({ type: "event", event: evt, data }) });
}

async function main() {
  console.log("── 测试 1：停止后的旧流残留 delta 不得再次触发（核心场景）──");
  ctx.init();
  await sleep(20);
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);
  respond("chat.listSessions", [{ uuid: "sess-A", title: "A", updatedAt: new Date().toISOString() }]);
  await sleep(10);

  // 打开会话 A
  vm.runInContext("state.tabs.push({kind:'chat', id:'sess-A', title:'A'}); state.activeTab='sess-A';", ctx);
  const swP = vm.runInContext("switchSession('sess-A')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-A", title: "A", mode: "chat", messages: [] });
  await swP;
  assert(getState().sessionId === "sess-A", "已打开会话 A");

  // A 的流开始（streamId=100）
  pushEvent("agent.stream", { delta: "A正在回答", sessionId: "sess-A", streamId: 100 });
  await sleep(10);
  assert(getState().streaming === true && getState().streamId === 100, "A 流开始，streamId=100");
  assert(getState().streamBuf === "A正在回答", "A 流内容已累积");

  // 用户点击停止 → stopStream 乐观重置 + 标记 streamId=100
  const stopP = vm.runInContext("stopStream('sess-A')", ctx);
  await sleep(5);
  assert(getState().streaming === false, "停止后立即回到可发送态（乐观重置）");
  assert(getState().stoppedStreams.has(100), "已停止流的 streamId=100 被标记");
  // 后端确认停止的请求已发出
  const stopReq = ctx._sent.find(m => m.method === "chat.stop");
  assert(stopReq && stopReq.params.streamId === 100, "chat.stop 携带 streamId=100（精确停止）");

  // 停止后旧流残留 delta 到达 → 不得重建流式气泡/重触发
  pushEvent("agent.stream", { delta: "残留片段", sessionId: "sess-A", streamId: 100 });
  await sleep(10);
  assert(getState().streaming === false, "残留 delta 不得让已停止的对话再次进入 streaming");
  assert(getState().streamMsg === null, "残留 delta 不得重建流式气泡");
  assert(getState().streamBuf === "", "残留 delta 不得写入 streamBuf");

  // 旧流 interrupted done 到达 → 仅清理，不驱动渲染
  pushEvent("agent.done", { interrupted: true, sessionId: "sess-A", streamId: 100 });
  await sleep(10);
  assert(getState().streaming === false, "停止后的 done 不得让状态错乱");
  assert(!getState().stoppedStreams.has(100), "done 到达后清理停止标记（防泄漏）");
  respond("chat.stop", { stopped: true });
  await stopP;

  console.log("── 测试 2：同会话旧流（streamId 不匹配）不得打断/覆盖新流 ──");
  // 新流开始（streamId=200）
  pushEvent("agent.stream", { delta: "新回答", sessionId: "sess-A", streamId: 200 });
  await sleep(10);
  assert(getState().streaming === true && getState().streamId === 200, "新流开始，streamId=200");
  // 旧流（streamId=100）的延迟 delta 到达 → 丢弃，不污染新流
  pushEvent("agent.stream", { delta: "旧流残留", sessionId: "sess-A", streamId: 100 });
  await sleep(10);
  assert(getState().streamBuf === "新回答", "旧流 delta 不得写入新流缓冲（got " + getState().streamBuf + "）");
  // 旧流 done 到达 → 不得结束新流
  pushEvent("agent.done", { text: "旧流结束", sessionId: "sess-A", streamId: 100 });
  await sleep(10);
  assert(getState().streaming === true, "旧流 done 不得结束新流（难以停下根因）");
  assert(getState().streamId === 200, "新流身份不变");
  // 新流正常结束
  pushEvent("agent.stream", { delta: "！", sessionId: "sess-A", streamId: 200 });
  await sleep(10);
  assert(getState().streamBuf === "新回答！", "新流继续正常累积");
  pushEvent("agent.done", { text: "新回答！", sessionId: "sess-A", streamId: 200 });
  await sleep(10);
  assert(getState().streaming === false, "新流正常结束");
  assert(getState().streamId === null, "流结束后身份清空");

  console.log("── 测试 3：跨会话后台流的 streamId 隔离（多窗口并行）──");
  // A 的流开始（streamId=300），切到 B → A 流转后台，携带 streamId
  pushEvent("agent.stream", { delta: "A后台", sessionId: "sess-A", streamId: 300 });
  await sleep(10);
  vm.runInContext("state.tabs.push({kind:'chat', id:'sess-B', title:'B'}); state.activeTab='sess-B';", ctx);
  const swB = vm.runInContext("switchSession('sess-B')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-B", title: "B", mode: "chat", messages: [] });
  await swB;
  assert(getState().sessionId === "sess-B", "已切到会话 B");
  assert(getState().backgroundStreams["sess-A"] && getState().backgroundStreams["sess-A"].streamId === 300,
    "A 的后台流携带 streamId=300");
  // 停止 B（B 无流）不得误伤 A 的后台流
  vm.runInContext("stopStream('sess-B')", ctx);
  await sleep(5);
  assert(!getState().stoppedStreams.has(300), "停止 B 不得标记 A 的后台流");
  respond("chat.stop", { stopped: true });
  // A 的后台流继续累积
  pushEvent("agent.stream", { delta: "继续", sessionId: "sess-A", streamId: 300 });
  await sleep(10);
  assert(getState().backgroundStreams["sess-A"].buf === "A后台继续", "A 的后台流继续累积（未受 B 影响）");

  console.log("── 测试 4：sendMessage 生成中追加发送——旧流残留不得打断新流 ──");
  // B 的流开始（streamId=400）
  pushEvent("agent.stream", { delta: "B回答", sessionId: "sess-B", streamId: 400 });
  await sleep(10);
  assert(getState().streaming === true, "B 流进行中");
  // 生成中输入并发送 → stopStream 标记 400，随后等待后端 done 再发新消息
  vm.runInContext("document.querySelector('#input').value = '追加';", ctx);
  const sP = vm.runInContext("sendMessage()", ctx);
  await sleep(20);
  assert(getState().stoppedStreams.has(400), "追加发送先停止旧流（标记 streamId=400）");
  // 旧流残留 delta 到达 → 丢弃（不得重建气泡/污染缓冲）
  pushEvent("agent.stream", { delta: "旧残留", sessionId: "sess-B", streamId: 400 });
  await sleep(10);
  assert(getState().streamBuf === "", "旧流残留 delta 被丢弃");
  // 后端确认停止（done 带 streamId=400）→ 清理标记 → sendMessage 继续发新消息
  pushEvent("agent.done", { interrupted: true, sessionId: "sess-B", streamId: 400 });
  await sleep(80);
  assert(!getState().stoppedStreams.has(400), "旧流 done 清理停止标记");
  assert(ctx._sent.some(m => m.method === "chat.send"), "停止确认后发出新 chat.send");
  respond("chat.send", { accepted: true });
  await sP;
  // 追加后的新流（streamId=500）正常开始
  pushEvent("agent.stream", { delta: "新回答", sessionId: "sess-B", streamId: 500 });
  await sleep(10);
  assert(getState().streaming === true && getState().streamId === 500, "追加后的新流正常开始");
  pushEvent("agent.done", { text: "新回答", sessionId: "sess-B", streamId: 500 });
  await sleep(10);
  assert(getState().streaming === false, "新流正常结束");

  console.log("── 测试 5：停止请求在途时，无 streamId 的残留 delta 也不得重触发 ──");
  // 用户点击发送后立即点停止（首 delta 尚未到达 → 前端不知 streamId）
  pushEvent("agent.stream", { delta: "开头", sessionId: "sess-B", streamId: 600 });
  await sleep(10);
  // 先清掉已知 streamId，模拟"停止时尚未知 streamId"的窗口
  vm.runInContext("state.streamId = null;", ctx);
  vm.runInContext("stopStream('sess-B')", ctx);
  await sleep(5);
  assert(getState().stoppedSessions.has("sess-B"), "停止在途：会话被标记（等待 done）");
  // 停止后到达的 delta（无 streamId，模拟旧格式/晚到）→ 丢弃
  pushEvent("agent.stream", { delta: "残留", sessionId: "sess-B" });
  await sleep(10);
  assert(getState().streamBuf === "开头",
    "停止在途的无 streamId 残留 delta 被丢弃（不追加，got " + getState().streamBuf + "）");
  // done 到达 → 解除标记
  pushEvent("agent.done", { interrupted: true, sessionId: "sess-B" });
  await sleep(10);
  assert(!getState().stoppedSessions.has("sess-B"), "done 到达解除停止在途标记");
  respond("chat.stop", { stopped: true });

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + (e && e.stack || e)); process.exit(1); });
