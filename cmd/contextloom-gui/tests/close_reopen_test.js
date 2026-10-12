// 回归测试：点击 X 关闭会话 tab → 再点击会话打开，必须重新加载历史对话。
// 背景（用户报告）：关闭会话再打开，历史对话不显示，只有重启 GUI 才恢复。
// 根因（已修复）：closeTab 关闭当前会话 tab 时不清空 state.sessionId →
// 再打开时 switchSession 的 "uuid === state.sessionId" 短路直接 return，
// 不调 chat.loadSession 也不渲染历史 → 消息区空白。
// 扩展覆盖（本轮复查补充）：
//   8) 流式中关闭会话 tab → 流转入后台缓冲（不中断生成）→ 再打开恢复流式
//   9) deleteSessionPrompt 连带修正：删除当前会话自动切换（isCurrent 在
//      closeTab 清空 sessionId 之前记录，否则自动切换静默失效）
//   10) 删除最后一个会话 → enterEmptyState 空状态（无 tab、sessionId 清空）
// 运行: node cmd/contextloom-gui/tests/close_reopen_test.js
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
    get children() { return this._children; },
    appendChild(c) { if (c) this._children.push(c); return c; },
    remove() {}, querySelector: () => null, querySelectorAll: () => [],
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    focus() {}, scrollTop: 0, scrollHeight: 0, ...extra,
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

let fetchCalls = [];
const ctxErrors = [];
const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console: { log: (...a) => console.log(...a), error: (...a) => { ctxErrors.push(a.map(String).join(" ")); }, warn: () => {} }, setTimeout, clearTimeout, setInterval, clearInterval,
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
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() { ctx._reloaded = true; } },
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
  if (!req) throw new Error("未找到未响应的请求: " + method + "，已发送: " + JSON.stringify(ctx._sent.map(m => m.method)));
  req._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: req.id, ok, result, error: ok ? "" : "mock error" }) });
}
function pushEvent(evt, data) {
  ctx._ws.onmessage({ data: JSON.stringify({ type: "event", event: evt, data }) });
}

async function main() {
  console.log("── 测试：关闭会话 tab 后再打开，必须重新加载历史对话 ──");

  // 1. 启动 → onopen → 会话列表
  ctx.init();
  await sleep(20);
  ctx._ws.readyState = 1;
  ctx._ws.onopen();
  await sleep(10);
  const now = new Date().toISOString();
  respond("chat.listSessions", [
    { uuid: "sess-A", title: "会话 A", updatedAt: now },
  ]);
  await sleep(10);
  assert(getState().sessions.length === 1, "会话列表加载 1 个会话");

  // 2. 打开会话 A → 必须发出 chat.loadSession
  vm.runInContext("openChatTab('sess-A')", ctx);
  await sleep(10);
  assert(ctx._sent.some(m => m.method === "chat.loadSession"), "第一次打开会话 → 发出 chat.loadSession");

  // 3. 响应历史消息（2 条）→ 渲染到消息区
  respond("chat.loadSession", {
    uuid: "sess-A", mode: "chat",
    messages: [
      { role: "user", content: "你好" },
      { role: "assistant", content: "你好！有什么可以帮你？" },
    ],
  });
  await sleep(10);
  await sleep(30);
  const st = getState();
  assert(st.sessionId === "sess-A", "加载后 state.sessionId = sess-A");
  assert(st.tabs.some(t => t.kind === "chat" && t.id === "sess-A"), "会话 tab 已创建");
  const msgList = els.get("#msgList");
  console.log("  [diag] 第一次打开后 children:", (msgList ? msgList._children.map(c => c.className).join(" | ") : "null"));
  console.log("  [diag] streaming:", st.streaming, "backgroundStreams:", JSON.stringify(st.backgroundStreams), "chatCache:", JSON.stringify(st.chatCache));
  assert(msgList && (msgList._children.length >= 2), "历史消息已渲染（≥2 条）");

  // 4. 点击 X 关闭会话 tab
  vm.runInContext("closeTab('sess-A')", ctx);
  await sleep(10);
  const st2 = getState();
  assert(st2.tabs.length === 0, "关闭后 tabs 为空");
  // ⚠️ 核心断言（修复前 FAIL）：sessionId 必须清空，否则再打开会短路
  assert(st2.sessionId === "", "关闭当前会话后 state.sessionId 被清空（修复前残留 → 再打开短路）");

  // 5. 再次点击会话 A 打开 → 【必须再次发出 chat.loadSession】
  vm.runInContext("openChatTab('sess-A')", ctx);
  await sleep(10);
  const loadReqs = ctx._sent.filter(m => m.method === "chat.loadSession");
  assert(loadReqs.length === 2, "再次打开 → 重新发出 chat.loadSession（修复前短路不发出）");

  // 6. 响应历史 → 消息恢复渲染
  respond("chat.loadSession", {
    uuid: "sess-A", mode: "chat",
    messages: [
      { role: "user", content: "你好" },
      { role: "assistant", content: "你好！有什么可以帮你？" },
    ],
  });
  await sleep(30);
  const st3 = getState();
  assert(st3.sessionId === "sess-A", "再次打开后 sessionId 恢复为 sess-A");
  const msgList2 = els.get("#msgList");
  assert(msgList2 && msgList2._children.length >= 2, "再次打开后历史消息重新渲染（≥2 条）");
  if (ctxErrors.length) console.log("  ⚠️ ctx 错误:", JSON.stringify(ctxErrors));

  // 8. 关闭【流式中】的会话 tab → 流转入后台缓冲（不中断生成）→ 再打开恢复流式
  ctx._sent = [];
  pushEvent("agent.stream", { delta: "正在生成第一段", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().streaming === true, "A 流式进行中（streaming=true）");
  vm.runInContext("closeTab('sess-A')", ctx);
  await sleep(10);
  let st8 = getState();
  assert(st8.sessionId === "", "流式中关闭 → sessionId 清空");
  assert(st8.backgroundStreams["sess-A"] && st8.backgroundStreams["sess-A"].streaming === true,
    "关闭时前台流转入后台缓冲（不中断生成）");
  assert(st8.streaming === false, "前台 streaming 已复位（流在后台继续）");
  // 后台继续输出 → 事件按 sessionId 过滤累积到后台缓冲
  pushEvent("agent.stream", { delta: "第二段", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().backgroundStreams["sess-A"].buf === "正在生成第一段第二段",
    "后台缓冲继续累积流内容（不丢半截回复）");
  // 再打开 → switchSession 从后台缓冲恢复流式渲染
  vm.runInContext("openChatTab('sess-A')", ctx);
  await sleep(10);
  respond("chat.loadSession", {
    uuid: "sess-A", mode: "chat",
    messages: [{ role: "user", content: "你好" }],
  });
  await sleep(30);
  st8 = getState();
  assert(st8.sessionId === "sess-A", "流式中关闭再打开 → sessionId 恢复");
  assert(st8.streaming === true, "流式中关闭再打开 → 恢复流式（streaming=true）");
  assert(!st8.backgroundStreams["sess-A"], "后台缓冲已移交前台（backgroundStreams 清空）");
  assert(st8.streamBuf === "正在生成第一段第二段", "半截回复完整恢复（streamBuf 无丢失）");
  // 流结束 → 复位
  pushEvent("agent.done", { text: "正在生成第一段第二段（完整）", sessionId: "sess-A" });
  await sleep(10);
  assert(getState().streaming === false, "流结束后 streaming 复位");

  // 9. deleteSessionPrompt 连带修正：删除【当前会话】→ 自动切换到剩余会话
  //    （isCurrent 在 closeTab 清空 sessionId 之前记录；若仍比对 sessionId
  //    恒不成立 → 自动切换静默失效——本测试在修复前会 FAIL）
  vm.runInContext("confirmDialog = async () => true", ctx);
  ctx._sent = [];
  vm.runInContext(`
    sessionsLoading = false; sessionsReloadQueued = false;
    state.sessions = [
      { uuid: "sess-A", title: "A", updatedAt: "2026-01-01" },
      { uuid: "sess-B", title: "B", updatedAt: "2026-01-02" },
    ];
    state.sessionId = "sess-A";
    state.tabs = [{ kind: "chat", id: "sess-A", title: "A" }];
    state.activeTab = "sess-A";
  `, ctx);
  const dpP = vm.runInContext("deleteSessionPrompt('sess-A')", ctx);
  await sleep(20);
  respond("chat.loadSession", { uuid: "sess-B", mode: "chat", messages: [] }); // 预切换 B
  await sleep(20);
  respond("chat.listSessions", [ // 预切换末尾的列表刷新（switchSession 内部 loadSessions）
    { uuid: "sess-A", title: "A", updatedAt: "2026-01-01" },
    { uuid: "sess-B", title: "B", updatedAt: "2026-01-02" },
  ]);
  await sleep(20);
  respond("chat.deleteSession", { deleted: true });
  await sleep(20);
  respond("chat.listSessions", [
    { uuid: "sess-B", title: "B", updatedAt: "2026-01-02" },
  ]);
  await dpP;
  await sleep(30);
  let st9 = getState();
  assert(ctx._sent.some(m => m.method === "chat.deleteSession" && m.params.uuid === "sess-A"),
    "删除当前会话发出 chat.deleteSession(A)");
  assert(st9.sessionId === "sess-B", "删除当前会话后自动切换到剩余会话 B（isCurrent 判断生效）");
  assert(st9.tabs.length === 1 && st9.tabs[0].id === "sess-B", "tab 列表只剩 B（A 的 tab 已关闭）");

  // 10. 删除【最后一个】会话 → enterEmptyState（无 tab、sessionId 清空）
  ctx._sent = [];
  vm.runInContext(`
    sessionsLoading = false; sessionsReloadQueued = false;
    state.sessions = [{ uuid: "sess-A", title: "A", updatedAt: "2026-01-01" }];
    state.sessionId = "sess-A";
    state.tabs = [{ kind: "chat", id: "sess-A", title: "A" }];
    state.activeTab = "sess-A";
  `, ctx);
  const dpP2 = vm.runInContext("deleteSessionPrompt('sess-A')", ctx);
  await sleep(20);
  respond("chat.deleteSession", { deleted: true });
  await sleep(20);
  respond("chat.listSessions", []);
  await dpP2;
  await sleep(30);
  st9 = getState();
  assert(st9.sessionId === "", "删除最后一个会话 → sessionId 清空");
  assert(st9.tabs.length === 0 && st9.activeTab === null, "删除最后一个会话 → 进入空状态（无 tab）");

  // 7. 关闭后直接发送消息 → 走新建会话分支（sessionId 为空不误发到旧会话）
  vm.runInContext("closeTab('sess-A')", ctx);
  await sleep(10);
  ctx._sent = [];
  const inputEl = els.get("#input") || makeEl("#input");
  els.set("#input", inputEl);
  inputEl.value = "新消息";
  vm.runInContext("sendMessage()", ctx);
  await sleep(30);
  assert(ctx._sent.some(m => m.method === "chat.newSession"), "关闭会话后发消息 → 自动新建会话（不误发到已关闭会话）");

  // ── 竞态回归：关闭当前 tab 自动切相邻（loadSession 慢返回）＋ 用户立即
  //    点击另一会话（loadSession 快返回）→ 后发者必须胜出，旧请求放弃渲染。
  //    修复前（无代际序号）：先发的 loadSession(A) 后返回时覆盖渲染 A，
  //    界面显示错误会话（用户点 C 却看到 A）→ 表现为"关闭再点击无记录/
  //    错乱，重开 GUI 才正常"（重开无并发所以正常）。
  console.log("── 竞态：关闭 tab 自动切相邻 + 立即点击其他会话（代际保护）──");
  ctx._sent = [];
  vm.runInContext(`
    sessionsLoading = false; sessionsReloadQueued = false;
    state.sessions = [
      { uuid: "sess-A", title: "A", updatedAt: "2026-01-01" },
      { uuid: "sess-B", title: "B", updatedAt: "2026-01-02" },
      { uuid: "sess-C", title: "C", updatedAt: "2026-01-03" },
    ];
    state.sessionId = "";
    state.tabs = [];
    state.activeTab = null;
    state.streaming = false; state.streamBuf = ""; state.streamMsg = null;
    state.backgroundStreams = {};
  `, ctx);
  // 打开 A 完成（背景历史）
  const openAP = vm.runInContext("openChatTab('sess-A')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-A", mode: "chat", messages: [{ role: "user", content: "A历史" }] });
  await openAP;
  await sleep(10);
  assert(getState().sessionId === "sess-A", "竞态准备：A 已激活（sessionId=sess-A）");
  // 切到 B（当前激活 tab）
  const openBP = vm.runInContext("openChatTab('sess-B')", ctx);
  await sleep(10);
  respond("chat.loadSession", { uuid: "sess-B", mode: "chat", messages: [{ role: "user", content: "B历史" }] });
  await openBP;
  await sleep(10);
  assert(getState().sessionId === "sess-B", "竞态准备：B 已激活（sessionId=sess-B）");

  // 关闭 B（当前）→ closeTab 自动 activateTab(A) → switchSession(A) 发出 loadSession(A)（慢）
  vm.runInContext("closeTab('sess-B')", ctx);
  await sleep(10);
  assert(getState().tabs.length === 1 && getState().tabs[0].id === "sess-A", "关闭 B 后自动聚焦相邻 A");
  const reqA = ctx._sent.find(m => m.method === "chat.loadSession" && m.params && m.params.uuid === "sess-A" && !m._responded);
  assert(!!reqA, "关闭 B → 自动切 A 发出 loadSession(A)（慢请求）");

  // 用户立即点击 C（不在 tabs 中）→ openChatTab(C) 发出 loadSession(C)（快请求）
  const openCP = vm.runInContext("openChatTab('sess-C')", ctx);
  await sleep(10);
  const reqC = ctx._sent.find(m => m.method === "chat.loadSession" && m.params && m.params.uuid === "sess-C" && !m._responded);
  assert(!!reqC, "用户点击 C → 发出 loadSession(C)（快请求）");

  // 先响应快请求 C（后发者先完成）
  reqC._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: reqC.id, ok: true, result: { uuid: "sess-C", mode: "chat", messages: [{ role: "user", content: "C历史" }] }, error: "" }) });
  await openCP;
  await sleep(30);
  let stRace = getState();
  assert(stRace.sessionId === "sess-C", "C 响应完成 → sessionId=sess-C（用户最后点击的会话）");
  const msgListR = els.get("#msgList");
  assert(msgListR && msgListR._children.length >= 1, "C 的历史已渲染");

  // 再响应慢请求 A（先发的后完成）→ 修复前覆盖渲染 A；修复后代际检查放弃
  reqA._responded = true;
  ctx._ws.onmessage({ data: JSON.stringify({ type: "resp", id: reqA.id, ok: true, result: { uuid: "sess-A", mode: "chat", messages: [{ role: "user", content: "A历史" }] }, error: "" }) });
  await sleep(30);
  stRace = getState();
  assert(stRace.sessionId === "sess-C", "⚠️ 核心：慢请求 A 后返回 → 必须放弃渲染（修复前 sessionId 被覆盖为 sess-A）");
  const msgListR2 = els.get("#msgList");
  assert(msgListR2 && msgListR2._children.length >= 1, "消息区仍是 C 的历史（未被 A 覆盖清空）");

  console.log(`\n结果: ${passed} 通过, ${failed} 失败`);
  process.exit(failed ? 1 : 0);
}

main().catch(e => { console.error("测试崩溃: " + e.stack); process.exit(1); });
