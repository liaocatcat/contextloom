// 项目管理器 v3 合并面板行为测试（项目行展开=文件树，合并目录管理器）
// 运行: node cmd/contextloom-gui/tests/projects_panel_test.js
// 用最小 DOM stub 加载真实 app.js，mock fetch + mock WS 验证：
//   1. baseName 路径提取（Unix/Windows/尾部斜杠/根）
//   2. renderProjectList 合并树渲染（当前高亮/展开箭头/切换/移除按钮）
//   3. 点击项目行 = 展开/折叠（懒加载 fs.list with root）；当前项目默认展开
//   4. 切换按钮 → switch API + localStorage 标记 + 原地切换（不整页 reload，
//      右侧会话 tab 保留——回归"切换项目默认关闭右侧所有对话"）
//   5. 目录选择器：browse 列表渲染、点击子目录再浏览、确认 → open API
//   6. 只读树：非当前项目右键菜单仅复制路径、文件只读打开（root 透传）
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub ──
function makeNode(tag) {
  const node = {
    tag, children: [], parentNode: null, _text: "", _html: "",
    style: {}, dataset: {}, _title: "", value: "", placeholder: "", disabled: false, readOnly: false,
    classList: {
      _set: new Set(),
      add(c) { this._set.add(c); },
      remove(c) { this._set.delete(c); },
      toggle(c, on) { if (on === undefined) on = !this._set.has(c); on ? this._set.add(c) : this._set.delete(c); },
      contains(c) { return this._set.has(c); },
    },
    listeners: {},
    addEventListener(t, fn) { (this.listeners[t] = this.listeners[t] || []).push(fn); },
    appendChild(c) {
      if (c.tag === "#frag") {
        for (const child of c.children) this.appendChild(child);
        return c;
      }
      this.children.push(c); c.parentNode = this; return c;
    },
    remove() { if (this.parentNode) { const i = this.parentNode.children.indexOf(this); if (i >= 0) this.parentNode.children.splice(i, 1); } },
    set innerHTML(v) {
      this._html = String(v);
      this.children = [];
      const parsed = parseHTML(this._html);
      for (const c of parsed.children) this.appendChild(c);
    },
    get innerHTML() { return this._html; },
    set textContent(v) { this._text = String(v); },
    get textContent() { return this._text; },
    set className(v) { this.classList._set = new Set(String(v).split(/\s+/).filter(Boolean)); },
    get className() { return [...this.classList._set].join(" "); },
    set title(v) { this._title = String(v); },
    get title() { return this._title; },
    querySelector(sel) {
      if (matches(this, sel)) return this;
      for (const c of this.children) { const r = c.querySelector(sel); if (r) return r; }
      return null;
    },
    querySelectorAll(sel) {
      const out = [];
      if (matches(this, sel)) out.push(this);
      for (const c of this.children) out.push(...c.querySelectorAll(sel));
      return out;
    },
    focus() {}, click() {},
  };
  return node;
}
function matches(node, sel) {
  const s = String(sel).trim();
  if (s.includes(" ")) {
    const [anc, desc] = s.split(/\s+/, 2);
    return matches(node, desc) && !!node.parentNode && matches(node.parentNode, anc);
  }
  if (s.startsWith(".")) return node.classList.contains(s.slice(1));
  if (s.startsWith("#")) return node._id === s.slice(1);
  return node.tag === s;
}
function parseHTML(html) {
  const root = makeNode("#root");
  const stack = [root];
  const tagRe = /<(\/?)([a-z]+)((?:\s+[a-z-]+="[^"]*")*)\s*(\/?)>/g;
  let last = 0, m;
  while ((m = tagRe.exec(html)) !== null) {
    const text = html.slice(last, m.index);
    if (text && text.trim()) stack[stack.length - 1]._text += text.replace(/\s+/g, " ");
    last = tagRe.lastIndex;
    const [, close, tag, attrsStr, selfClose] = m;
    if (close) { if (stack.length > 1) stack.pop(); continue; }
    const node = makeNode(tag);
    const attrRe = /([a-z-]+)="([^"]*)"/g;
    let am;
    while ((am = attrRe.exec(attrsStr)) !== null) {
      if (am[1] === "class") for (const c of am[2].split(/\s+/)) node.classList.add(c);
      else if (am[1] === "id") node._id = am[2];
      else if (am[1] === "placeholder") node.placeholder = am[2];
      else node[am[1]] = am[2];
    }
    stack[stack.length - 1].appendChild(node);
    if (!selfClose && tag !== "input") stack.push(node);
  }
  return root;
}
function makeEl(sel) {
  const node = makeNode(sel);
  if (sel.startsWith("#")) node._id = sel.slice(1);
  return node;
}

// ── vm 环境 ──
let reloaded = 0;
const fetchCalls = [];
const fetchMock = async (url, opts) => {
  fetchCalls.push({ url, body: opts && opts.body ? JSON.parse(opts.body) : null });
  if (url === "/api/projects") {
    return { ok: true, json: async () => ({ projects: ["/proj/alpha", "/proj/beta"], current: "/proj/alpha" }) };
  }
  if (url.startsWith("/api/fs/browse")) {
    if (url.includes("path=%2Ftmp")) {
      return { ok: true, json: async () => ({ path: "/tmp", parent: "/", dirs: [{ name: "sub1", path: "/tmp/sub1" }, { name: "sub2", path: "/tmp/sub2" }] }) };
    }
    return { ok: true, json: async () => ({ path: "/", parent: "", dirs: [{ name: "tmp", path: "/tmp" }, { name: "usr", path: "/usr" }] }) };
  }
  if (url === "/api/projects/switch" || url === "/api/projects/open") {
    return { ok: true, json: async () => ({ ok: true }) };
  }
  // 原地切换会重连 WS → resolveWSBase 读 /api/config（不整页 reload）
  if (url === "/api/config") {
    return { ok: true, json: async () => ({ gui: "http://127.0.0.1:18765/" }) };
  }
  throw new Error("unexpected fetch: " + url);
};

// ── mock WebSocket：捕获 fs.* 请求并按 root 返回 ──
const wsCalls = [];
function wsSend(data) {
  const msg = JSON.parse(data);
  wsCalls.push(msg);
  const params = msg.params || {};
  // 响应模拟：fs.list 返回构造 entries；fs.read 返回内容
  let result;
  if (msg.method === "fs.list") {
    const root = params.root || "/proj/alpha";
    result = { path: params.path, root, entries: [
      { name: "src", path: "src", isDir: true, size: 0, mtime: "" },
      { name: "README.md", path: "README.md", isDir: false, size: 10, mtime: "" },
      { name: "go.mod", path: "go.mod", isDir: false, size: 8, mtime: "" },
    ] };
  } else if (msg.method === "fs.read") {
    result = { path: params.path, root: params.root || "", content: "# " + params.path, size: 5, mtime: "" };
  } else {
    result = { ok: true };
  }
  // 异步回包（req 是 Promise）；state 在 vm 上下文，需经 runInContext 读取
  setTimeout(() => {
    const st = vm.runInContext("state", ctx);
    const cb = st.pending && st.pending.get(msg.id);
    if (cb) { cb.resolve(result); st.pending.delete(msg.id); }
  }, 0);
}
class WebSocketStub {
  constructor(url) { this.url = url; this.readyState = 1; ctx._ws = this; }
  send(d) { __wsSend(d); }
  close() {}
}
WebSocketStub.OPEN = 1;
WebSocketStub.CONNECTING = 0;
WebSocketStub.CLOSED = 3;

const els = new Map();
const document = {
  querySelector: (s) => els.get(s) || null,
  querySelectorAll: () => [],
  createElement: (tag) => makeNode(tag),
  createDocumentFragment: () => makeNode("#frag"),
  addEventListener() {}, removeEventListener() {},
  body: makeNode("body"),
};
const localStorage = (() => { let m = new Map(); return {
  getItem: (k) => m.has(k) ? m.get(k) : null,
  setItem: (k, v) => m.set(k, String(v)),
  removeItem: (k) => m.delete(k),
  _dump: () => Object.fromEntries(m),
}; })();
class AbortControllerStub {
  constructor() { this.signal = { aborted: false }; }
  abort() { this.signal.aborted = true; }
}
const ctx = {
  document, window: { innerWidth: 1280, addEventListener() {} }, localStorage,
  AbortController: AbortControllerStub,
  fetch: fetchMock, console, Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN,
  Object, Array, RegExp, Map, Set, Promise, Error,
  location: { protocol: "http:", host: "127.0.0.1:18765", reload: () => { reloaded++; } },
  // 保持原生异步 setTimeout：req 的超时计时器与 wsSend 回包都依赖事件循环
  // （立即执行会破坏 req 的 pending/resolve 时序，导致所有请求同步超时）
  setTimeout: (fn, ms) => setTimeout(fn, ms || 0), clearTimeout: (id) => clearTimeout(id),
  requestAnimationFrame: (f) => f(),
  confirmDialog: async () => true, toast() {}, msgOf: (e) => String(e && e.message || e),
};
vm.createContext(ctx);
vm.runInContext(src, ctx);
// 注入已就绪的 WS（合并树懒加载依赖 req → state.ws）
// 注入已就绪的 WS。原地切换项目会重连（state.ws 置空 → connect() 新建
// WebSocketStub，但 stub 不会自动触发 onopen）——测试里用本函数恢复就绪态，
// 等价于浏览器中 WS 重连成功。
const restoreWS = () => vm.runInContext(
  `state.ws = { readyState: 1, send: (d) => __wsSend(d) };`,
  Object.assign(ctx, { __wsSend: wsSend, WebSocket: WebSocketStub }),
);
restoreWS();

let pass = 0, fail = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.log("  ❌ " + name + " → got " + g + " want " + w); }
}
function ok(name, cond) { eq(name, !!cond, true); }
async function flush(n = 10) { for (let i = 0; i < n; i++) await Promise.resolve(); }

(async function main() {
  // ── 测试 1：baseName 路径提取 ──
  eq("T1a Unix 路径", ctx.baseName("/Users/me/code/myapp"), "myapp");
  eq("T1b Windows 路径", ctx.baseName("D:\\work\\proj\\app"), "app");
  eq("T1c 尾部斜杠", ctx.baseName("/a/b/c/"), "c");
  eq("T1d 根目录", ctx.baseName("/"), "/");

  // ── 测试 2：renderProjectList 合并树渲染 ──
  const projList = makeEl("#projList");
  els.set("#projList", projList);
  await ctx.renderProjectList();
  await flush();
  eq("T2a 渲染 2 个项目 wrap", projList.children.length, 2);
  const wrapA = projList.children[0];
  const rowA = wrapA.children[0];
  eq("T2b 当前项目行 cur 高亮", rowA.classList.contains("cur"), true);
  ok("T2c 行首为展开箭头", rowA.children[0].className.includes("tree-arrow"));
  eq("T2d 当前项目含 📌 图标", rowA.children[1].textContent, "📌");
  eq("T2e 当前项目含 当前 标签", rowA.children[3] && rowA.children[3].textContent, "当前");
  // 当前项目默认展开 → wrap 含 tree 容器
  eq("T2f 当前项目默认展开（含树容器）", wrapA.children.length, 2);
  ok("T2g 当前项目树可写（非 readonly）", !wrapA.children[1].classList.contains("readonly"));
  // 树已懒加载第一层（fs.list 带 root=当前项目）
  const fsListCalls = wsCalls.filter(c => c.method === "fs.list");
  ok("T2h fs.list 已发起（懒加载）", fsListCalls.length >= 1);
  eq("T2h2 首层 fs.list 带 root=当前项目", fsListCalls[0].params.root, "/proj/alpha");
  // 树 body 内有 3 个节点（src 目录 + 2 文件）
  const treeBody = wrapA.children[1].querySelector(".proj-tree-body");
  ok("T2i 当前项目树已渲染条目", treeBody.children.length, 3);

  const wrapB = projList.children[1];
  const rowB = wrapB.children[0];
  ok("T2j 非当前行 clickable", rowB.classList.contains("clickable"));
  eq("T2k 非当前行含切换按钮", rowB.children[3] && rowB.children[3].textContent, "切换");
  eq("T2l 非当前项目未展开", wrapB.children.length, 1);
  // 非当前项目树只读标记（点击行展开后）
  vm.runInContext("state.projExpanded['/proj/beta'] = true", ctx);
  await ctx.renderProjectList();
  await flush();
  const wrapB2 = projList.children[1];
  ok("T2m 非当前项目展开后树 readonly", wrapB2.children[1].classList.contains("readonly"));
  // 非当前项目展开时 fs.list 带其 root
  const betaCall = wsCalls.filter(c => c.method === "fs.list" && c.params.root === "/proj/beta");
  ok("T2n 非当前项目 fs.list 带 root=项目路径", betaCall.length >= 1);
  // 只读树无内嵌工具栏
  ok("T2o 只读树无工具栏", !wrapB2.children[1].querySelector(".proj-tree-toolbar"));

  // ── 测试 3：点击项目行 = 展开/折叠（不切换）──
  fetchCalls.length = 0;
  vm.runInContext("state.projExpanded['/proj/beta'] = false", ctx);
  await ctx.renderProjectList();
  const rowB3 = projList.children[1].children[0];
  await rowB3.onclick(); // 展开
  await flush();
  ok("T3a 点击行触发展开（wrap 含树）", projList.children[1].children.length, 2);
  eq("T3b 点击行未调用 switch API", fetchCalls.some(c => c.url.includes("/switch")), false);
  await rowB3.onclick(); // 折叠
  await flush();
  eq("T3c 再次点击折叠", projList.children[1].children.length, 1);

  // ── 测试 4：切换按钮 → switch API ──
  reloaded = 0; fetchCalls.length = 0;
  vm.runInContext("switchingProject = false", ctx);
  await ctx.renderProjectList();
  fetchCalls.length = 0; // 清掉 renderProjectList 的 /api/projects 探测
  const swBtn = projList.children[1].children[0].children[3];
  await swBtn.onclick({ stopPropagation() {} });
  await new Promise(r => setTimeout(r, 30)); // 等待 switchProject 的 await fetch + setItem 完成
  const swCall = fetchCalls.find(c => c.url.includes("/switch"));
  ok("T4a switch API 被调用", !!swCall);
  eq("T4b 切换目标为 /proj/beta", swCall && swCall.body && swCall.body.path, "/proj/beta");
  eq("T4c 重载标记为 projects（合并面板）", localStorage.getItem("dm-gui-after-switch"), "projects");
  await new Promise(r => setTimeout(r, 30)); // 放行宏任务（原地切换后的树/会话重载）
  ok("T4d 切换不再整页 reload（右侧会话 tab 保留）", reloaded === 0);
  restoreWS(); // 原地切换重连 WS：恢复就绪态（浏览器中 onopen 自动发生）
  eq("T4e 当前项目已就地更新", vm.runInContext("state.projectDir", ctx), "/proj/beta");

  // ── 测试 5：目录选择器（以目录选中的形式引入项目） ──
  document.body.children.length = 0;
  ctx.pickProjectDir();
  await flush();
  const dlg = document.body.children[0];
  ok("T5a 选择器对话框已创建", !!dlg);
  const browseList = dlg.querySelector(".browse-list");
  eq("T5b 根目录浏览列表已渲染", browseList.children.length, 2);
  eq("T5c 路径输入框已更新为浏览路径", dlg.querySelector("input").value, "/");
  const rowTmp = browseList.children[0];
  eq("T5d 首行为 /tmp 目录", rowTmp.title, "/tmp");
  rowTmp.onclick();
  await flush();
  eq("T5e 点击目录行后路径更新", dlg.querySelector("input").value, "/tmp");
  eq("T5f 子目录列表已刷新（含上级行）", browseList.children.length, 3);
  eq("T5g 首行为上级目录", browseList.children[0].textContent, "↑ 上级目录");
  eq("T5g2 子目录名 sub1", browseList.children[1].textContent, "📁 sub1");
  vm.runInContext("switchingProject = false", ctx);
  fetchCalls.length = 0; reloaded = 0;
  await dlg.querySelector(".ok").onclick({ stopPropagation() {} });
  eq("T5h open API 被调用", fetchCalls.length, 1);
  eq("T5i 打开目标为 /tmp", fetchCalls[0].body && fetchCalls[0].body.path, "/tmp");
  eq("T5j 打开后重载标记 projects", localStorage.getItem("dm-gui-after-switch"), "projects");
  await new Promise(r => setTimeout(r, 30)); // 放行宏任务（同 T4d）
  ok("T5k 打开后不整页 reload（右侧会话 tab 保留）", reloaded === 0);
  restoreWS(); // 同上：重连后恢复就绪态

  // ── 测试 6：init() 默认打开项目管理器 ──
  const initSrc = src.slice(src.indexOf("function init()"), src.indexOf("function init()") + 1600);
  ok("T6a init 默认面板为 projects", /let startPanel = "projects"/.test(initSrc));
  ok("T6b init 使用 startPanel 变量", /setPanel\(startPanel\)/.test(initSrc));
  ok("T6c init 不再硬编码 sessions", !/setPanel\("sessions"\)/.test(initSrc));
  ok("T6d 移除响应读取 notExists 提示", /j\.notExists/.test(src));
  ok("T6e 存在'目录不存在，已从列表移除'文案", /目录不存在，已从列表移除/.test(src));

  // ── 测试 7：只读树行为（右键菜单仅复制路径 + 文件只读打开 root 透传） ──
  wsCalls.length = 0;
  vm.runInContext("state.projExpanded['/proj/beta'] = true", ctx);
  await ctx.renderProjectList();
  await new Promise(r => setTimeout(r, 30)); // 树懒加载走 req（宏任务回包）
  // 调试：输出渲染结构
  console.log("  [debug] projList.children=", projList.children.length,
    "wrapB.children=", projList.children[1] && projList.children[1].children.length,
    "wrapB[1].className=", projList.children[1] && projList.children[1].children[1] && projList.children[1].children[1].className,
    "treeCache beta=", !!vm.runInContext("state.treeCache['/proj/beta|']", ctx));
  const betaTree = projList.children[1].children[1];
  const betaRows = betaTree.querySelectorAll(".tree-row");
  ok("T7a 只读树含节点", betaRows.length >= 2);
  // 文件行（README.md）点击 → fs.read 带 root + 只读 tab
  const fileRow = betaRows.find(r => r.dataset.path === "README.md");
  ok("T7b 只读树文件行存在", !!fileRow);
  await fileRow.onclick();
  await flush();
  const readCalls = wsCalls.filter(c => c.method === "fs.read");
  eq("T7c fs.read 带 root=非当前项目", readCalls[0].params.root, "/proj/beta");
  const fileTab = vm.runInContext("state.tabs[state.tabs.length-1]", ctx);
  eq("T7d 文件 tab 标记只读", fileTab.readonly, true);
  eq("T7e 文件 tab 标记 root", fileTab.root, "/proj/beta");
  eq("T7f 只读 tab 标题含 🔒", fileTab.title.includes("🔒"), true);
  // 只读树右键菜单：仅复制路径（无写操作）
  let ctxMenuItems = null;
  vm.runInContext(`__capture = (items) => { ctxMenuItems = items; };`, Object.assign(ctx, {
    get ctxMenuItems() { return ctxMenuItems; }, set ctxMenuItems(v) { ctxMenuItems = v; },
  }));
  // 通过源码契约断言：只读分支只 push 复制路径（buildTreeNode 全文）
  const btbSrc = src.slice(src.indexOf("function buildTreeNode"), src.indexOf("function truncName"));
  ok("T7g 右键菜单 writable 分支含新建/重命名/删除", /if \(writable\)/.test(btbSrc) && /items\.push\(\{ label: "✏️ 重命名"/.test(btbSrc));
  ok("T7h 只读树复制路径含 root 前缀", /copyText\(root \? root \+ "\/" \+ path : path\)/.test(btbSrc));
  // 文件行右键模拟（覆写 showContextMenu 捕获 items）
  vm.runInContext(`showContextMenu = (x, y, items, cb) => { __ctxItems = items; cb(); };`, ctx);
  fileRow.oncontextmenu({ preventDefault() {}, stopPropagation() {}, clientX: 0, clientY: 0 });
  await flush();
  const gotItems = vm.runInContext("__ctxItems", ctx);
  eq("T7i 只读树右键仅 1 项（复制路径）", gotItems.length, 1);

  // ── 测试 8：内嵌工具栏"⟳ 刷新"按钮不消失（回归：loadProjTree 容器误传整棵树） ──
  vm.runInContext("state.projExpanded['/proj/alpha'] = true", ctx);
  await ctx.renderProjectList();
  await flush();
  const wrapA8 = projList.children[0];
  const tree8 = wrapA8.children[1];
  const tb8 = tree8.querySelector(".proj-tree-toolbar");
  ok("T8a 当前项目树含内嵌工具栏", !!tb8);
  const tbBtns8 = tb8.querySelectorAll("button");
  eq("T8b 工具栏 3 个按钮（⟳ ＋文件 ＋目录）", tbBtns8.length, 3);
  const listCallsBefore = wsCalls.filter(c => c.method === "fs.list").length;
  // 点击"⟳ 刷新"（第一个按钮）——修复前会把整个 tree（含工具栏）清成"加载中…"。
  // 注意：btn 工厂的 onclick 是块体箭头函数（返回 undefined），不能 await；
  // loadProjTree 的 req 依赖 setTimeout 宏任务回包，必须用真实宏任务等待。
  tbBtns8[0].onclick({ stopPropagation() {} });
  await new Promise(r => setTimeout(r, 30));
  const tb8After = tree8.querySelector(".proj-tree-toolbar");
  ok("T8c 刷新后工具栏仍在（不消失）", !!tb8After);
  eq("T8d 刷新后仍 3 个按钮", tb8After.querySelectorAll("button").length, 3);
  const listCallsAfter = wsCalls.filter(c => c.method === "fs.list").length;
  ok("T8e 刷新触发重新加载 fs.list", listCallsAfter > listCallsBefore);
  const body8 = tree8.querySelector(".proj-tree-body");
  ok("T8f 刷新后树条目已渲染（非'加载中'残留）", body8.children.length >= 3);

  console.log(`\n${pass} passed, ${fail} failed`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error("TEST ERROR:", e); process.exit(2); });
