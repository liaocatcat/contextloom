// 跨会话流式 + 追加发送 —— 前端集成测试
// 覆盖用户报告三大缺陷的修复：
//   1) 切换会话再返回丢失聊天记录/状态 → 事件按 sessionId 过滤（旧流不串台）
//   2) 无法停止对话 → endStream 兜底重置 streaming、chat.stop 可停止
//   3) 无法追加发送对话 → 生成中发送 = 先停止再追加
// 运行: node cmd/contextloom-gui/tests/stream_session_test.js
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
    dataset: {}, listeners: {}, innerHTML: "", textContent: "", value: "", _children: [],
    children: [],
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
    if (s.startsWith("#")) {
      let el = els.get(s);
      if (!el) el = makeEl(s);
      return el;
    }
    return els.get(s) || null;
  },
  querySelectorAll: () => [],
  getElementById: (id) => {
    let el = els.get("#" + id);
    if (!el) el = makeEl("#" + id);
    return el;
  },
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

let fetchCalls = [];
const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console, setTimeout, clearTimeout, setInterval, clearInterval,
  AbortController: AbortControllerStub, requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error,
  fetch: async (url, opts) => {
    fetchCalls.push({ url, opts });
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
const sentMethods = () => ctx._sent.map(m => m.method + ":" + (m.params && m.params.sessionId || ""));
function respond(method, result, ok = true) {
  const req = ctx._sent.find(m => m.method === method && !m._responded);
  if (!req) throw new Error("未找到未响应的请求: " + method + "，已发送: " + JSON.stringify(ctx._sent.map(m => m.method)));
  req._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock error" }) });
}
function pushEvent(evt, data) {
  ctx._ws.onmessage({ data: JSON.stringify({ type: "event", event: evt, data }) });
}

async function main() {
  console.log("── 测试 1：跨会话事件过滤（切换会话后旧流事件不串台）──");
  ctx.init();
  await sleep(20);
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);
  respond("chat.listSessions", [{ uuid: "sess-A", title: "A", updatedAt: new Date().toISOString() }]);
  await sleep(10);

  // 打开会话 A（switchSession → loadSession 响应）
  vm.runInContext("state.tabs.push({kind:'chat', id:'sess-A', title:'A'}); state.activeTab='sess-A';", ctx);
  const swP = vm.runInContext("switchSession('sess-A')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-A", title: "A", mode: "chat", messages: [] });
  await swP;
  assert(getState().sessionId === "sess-A", "已切换到会话 A");

  // A 开始流式：agent.stream(sessionId=A) → beginStream
  pushEvent("agent.stream", { delta: "你好", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().streamMsg !== null, "A 的流开始渲染（streamMsg 创建）");
  assert(getState().streamSessionId === "sess-A", "streamSessionId 记录为 A");
  assert(getState().streaming === true, "streaming = true");

  // 切换到会话 B
  vm.runInContext("state.tabs.push({kind:'chat', id:'sess-B', title:'B'}); state.activeTab='sess-B';", ctx);
  const swB = vm.runInContext("switchSession('sess-B')", ctx);
  await sleep(10);
  // 多对话并行：switchSession 绝不发 chat.stop（中断对话 = 历史缺陷，用户要求不中断）
  assert(!sentMethods().some(m => m.startsWith("chat.stop")), "切换会话不发出 chat.stop（不中断对话）");
  respond("chat.loadSession", { uuid: "sess-B", title: "B", mode: "chat", messages: [] });
  await swB;
  assert(getState().sessionId === "sess-B", "已切换到会话 B");
  assert(getState().streamMsg === null, "切换后当前流渲染已清理");
  // A 的流转入后台缓冲（不中断、不丢失）
  assert(getState().backgroundStreams["sess-A"] && getState().backgroundStreams["sess-A"].streaming === true,
    "A 的流已转入后台缓冲（streaming=true）");

  // 旧流事件（sessionId=A）在 B 会话期间到达 → 累积后台缓冲，不渲染到 B
  pushEvent("agent.stream", { delta: "继续输出", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().streamMsg === null, "旧会话 A 的 stream 事件不渲染到 B");
  assert(getState().streamBuf === "", "旧流内容未写入当前 streamBuf");
  assert(getState().backgroundStreams["sess-A"].buf === "你好继续输出", "后台缓冲累积 A 的流内容");

  // 切回 A：恢复流式渲染（并行对话切回可见）
  vm.runInContext("state.tabs.push({kind:'chat', id:'sess-A', title:'A'}); state.activeTab='sess-A';", ctx);
  const swA = vm.runInContext("switchSession('sess-A')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-A", title: "A", mode: "chat", messages: [] });
  await swA;
  assert(getState().sessionId === "sess-A", "已切回会话 A");
  assert(getState().streaming === true, "切回后恢复流式（streaming=true）");
  assert(getState().streamMsg !== null, "切回后恢复流渲染（streamMsg 创建）");
  assert(getState().streamBuf === "你好继续输出", "切回后流缓冲完整恢复");

  // A 继续输出并完成
  pushEvent("agent.stream", { delta: "!", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().streamBuf === "你好继续输出!", "A 的流继续累积");
  pushEvent("agent.done", { text: "你好继续输出!", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().streaming === false, "A 的流结束后 streaming=false");
  assert(!getState().backgroundStreams["sess-A"], "A 的后台缓冲已清理");

  console.log("── 测试 2：追加发送（生成中发送 = 先停止再追加）──");
  // 先切回会话 B（测试 1 结束时停留在 A），B 当前无流
  const swBack = vm.runInContext("switchSession('sess-B')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-B", title: "B", mode: "chat", messages: [] });
  await swBack;
  assert(getState().sessionId === "sess-B", "已切回会话 B（测试 2 前提）");
  // 模拟真实点击会话 tab：activeTab 指向 B（switchSession 只改 sessionId）
  vm.runInContext("state.activeTab='sess-B';", ctx);
  // B 开始流式
  pushEvent("agent.stream", { delta: "B 的回复", sessionId: "sess-B" });
  await sleep(10);
  assert(getState().streaming === true, "B 的流进行中（streaming=true）");

  // 生成中输入新消息并发送 → 应先 chat.stop 再 chat.send
  vm.runInContext("document.querySelector('#input').value = '追加的消息';", ctx);
  const sP = vm.runInContext("sendMessage()", ctx);
  await sleep(50);
  const methods = sentMethods();
  const stopIdx = methods.findIndex(m => m.startsWith("chat.stop:"));
  assert(stopIdx >= 0, "追加发送先发出 chat.stop");
  assert(methods.indexOf("chat.send:") === -1, "chat.send 尚未发出（等待停止完成）");

  // 后端停止完成：agent.done(sessionId=B) 到达 → sendMessage 继续执行
  // → 发出 chat.send 并开启新流（追加发送成功，streaming 重新为 true）
  pushEvent("agent.done", { interrupted: true, sessionId: "sess-B" });
  await sleep(150);
  assert(getState().streaming === true, "追加后新流开始（streaming=true）");
  const methods2 = sentMethods();
  const sendIdx = methods2.findIndex(m => m.startsWith("chat.send:"));
  assert(sendIdx > stopIdx, "停止完成后发出 chat.send（追加发送成功）");
  const sendP = methods2[sendIdx].split(":")[1];
  assert(sendP === "sess-B", "追加消息发送到当前会话 B（sessionId=" + sendP + "）");
  // chat.send 是 await 的（sendMessage 等其响应）；mock 必须响应否则 sP 挂 20s
  respond("chat.send", { accepted: true });
  await sP;

  console.log("── 测试 3：endStream 兜底重置（防 streaming 卡死）──");
  // 手动置 streaming=true 后触发 done，验证必然重置
  vm.runInContext("state.streaming = true; state.streamMsg = null;", ctx);
  pushEvent("agent.done", { interrupted: true, sessionId: "sess-B" });
  await sleep(10);
  assert(getState().streaming === false, "agent.done 到达后 streaming 必然重置为 false");

  console.log("── 测试 4：多轮自动化续跑按钮图标与功能同步（done 后新 stream 切回停止图标）──");
  // 场景：goal 模式多轮自动化——第一轮 agent.done 到达（endStream 把按钮切回
  // "发送箭头"图标），后端紧接着直接推送新一轮 agent.stream（不经过 sendMessage）
  // → beginStream 必须把按钮切回"停止方块"图标；否则图标=发送、功能=停止（streaming=true
  // 时点击执行 chat.stop）不一致（历史缺陷：多轮自动化期间中止按钮显示发送图标）。
  pushEvent("agent.done", { text: "第一轮完成", sessionId: "sess-B" });
  await sleep(10);
  const btn = els.get("#sendBtn");
  assert(!btn.classList.contains("stop"), "第一轮 done 后按钮为发送图标（无 stop class，与 endStream 行为一致）");
  assert(getState().streaming === false, "第一轮 done 后 streaming=false（功能=发送）");
  // 新一轮开始：后端直接推送 agent.stream → beginStream
  pushEvent("agent.stream", { delta: "第二轮", sessionId: "sess-B" });
  await sleep(10);
  assert(getState().streaming === true, "新一轮 stream 到达 streaming=true（功能=停止）");
  assert(btn.classList.contains("stop"), "新一轮 stream 到达后按钮切回停止图标（stop class 存在，图标与功能同步）");
  // 第二轮结束：按钮再切回发送图标（循环闭环）
  pushEvent("agent.done", { text: "第二轮完成", sessionId: "sess-B" });
  await sleep(10);
  assert(!btn.classList.contains("stop"), "第二轮 done 后按钮再次切回发送图标（闭环）");
  assert(getState().streaming === false, "第二轮 done 后 streaming=false");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + (e && e.stack || e)); process.exit(1); });
