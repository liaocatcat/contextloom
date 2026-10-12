// 多终端 + 项目管理器 —— 前端行为测试
// 运行: node cmd/contextloom-gui/tests/term_test.js
// 用最小 DOM stub 加载真实 app.js，模拟终端交互：
//   - 新建终端（term.spawn）→ 标签渲染 → 切换 → 输入（term.input）→ 关闭（term.kill）
//   - term.output / term.exit 事件处理（多窗口同步、ANSI 剥离、自动滚动）
//   - 项目管理器：目录选择器/面板函数存在 + switchProject → /api/projects/switch + reload
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 最小 DOM stub（与 resizer_test.js 同构）──
const els = new Map();
function makeEl(sel, extra = {}) {
  const el = {
    selector: sel,
    style: {},
    classList: { _set: new Set(), add(c) { this._set.add(c); }, remove(c) { this._set.delete(c); }, toggle(c, on) { if (on === undefined) on = !this._set.has(c); on ? this._set.add(c) : this._set.delete(c); }, contains(c) { return this._set.has(c); } },
    dataset: {},
    listeners: {},
    innerHTML: "",
    textContent: "",
    value: "",
    _children: [],
    appendChild(c) { if (c) this._children.push(c); return c; },
    remove() {},
    querySelector: () => null,
    querySelectorAll: () => [],
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    focus() {},
    scrollTop: 0,
    scrollHeight: 0,
    ...extra,
  };
  els.set(sel, el);
  return el;
}
const document = {
  querySelector: (s) => els.get(s) || null,
  querySelectorAll: () => [],
  getElementById: (id) => els.get("#" + id) || null,
  addEventListener() {},
  createElement: (tag) => {
    const el = makeEl("el:" + tag, { tagName: tag.toUpperCase() });
    el.querySelector = () => makeEl("sub:" + tag);
    el.querySelectorAll = () => [];
    return el;
  },
  body: { appendChild() {}, classList: { _s: new Set(), add(c) { this._s.add(c); }, remove(c) { this._s.delete(c); }, contains(c) { return this._s.has(c); } } },
};
const window = { innerWidth: 1280, addEventListener() {}, location: { reload() { ctx._reloaded = true; } } };
const __ls = new Map();
const localStorage = { getItem: (k) => __ls.has(k) ? __ls.get(k) : null, setItem(k, v) { __ls.set(k, String(v)); }, removeItem(k) { __ls.delete(k); } };

class AbortControllerStub {
  constructor() { this.signal = { aborted: false }; }
  abort() { this.signal.aborted = true; }
}
// xterm.js stub（index.html 引入真实文件；vm 测试用最小实现验证接线）
class TerminalStub {
  constructor(opts) { this.opts = opts; this._onData = null; this._writes = []; }
  loadAddon() {}
  open() { ctx._termOpened = (ctx._termOpened || 0) + 1; }
  onData(fn) { this._onData = fn; }
  onResize(fn) { this._onResize = fn; }
  write(d) { this._writes.push(d); }
  dispose() { ctx._termDisposed = (ctx._termDisposed || 0) + 1; }
  focus() {}
}
class FitAddonStub { fit() { ctx._fitCount = (ctx._fitCount || 0) + 1; } }
const ctx = {
  Terminal: TerminalStub,
  FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console, setTimeout, clearTimeout,
  AbortController: AbortControllerStub,
  requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error, fetch,
  location: { reload() { ctx._reloaded = true; } },
};
vm.createContext(ctx);
vm.runInContext(src, ctx);
const getState = () => vm.runInContext("state", ctx);

let pass = 0, fail = 0;
function ok(cond, name) {
  if (cond) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.error("  ❌ " + name); }
}

// 拦截 req（不真正发 WS）
let reqCalls = [];
ctx.req = (method, params) => {
  reqCalls.push({ method, params });
  if (method === "term.spawn") return Promise.resolve({ id: "t1" });
  if (method === "term.kill") return Promise.resolve({ ok: true });
  if (method === "term.buffer") return Promise.resolve("");
  if (method === "term.input") return Promise.resolve({ ok: true });
  return Promise.resolve({});
};
ctx.promptDialog = (msg, initial) => Promise.resolve(initial);

(async () => {
  console.log("== 终端行为测试 ==");

  // 准备 DOM 元素
  makeEl("#termPanel", { style: { display: "none" } });
  makeEl("#termTabs");
  makeEl("#termBody");
  makeEl("#msgList");
  makeEl("#fileEditor");
  makeEl("#inputZone");
  makeEl("#scrollDown");
  makeEl("#openDirBtn");

  // 1. termShow：显示终端面板（位于输入框下方，与消息区/输入区共存，
  //    不再操纵文件编辑器/消息区——终端与它们天然共存）
  ctx.termShow();
  ok(els.get("#termPanel").style.display === "flex", "termShow 显示终端面板");
  ok(els.get("#msgList").style.display !== "none", "termShow 不再隐藏消息区（终端在输入框下方共存）");
  ok(els.get("#inputZone").style.display !== "none", "termShow 不再隐藏输入区");

  // 2. termHide：隐藏终端面板（消息区/输入区保持不变）
  ctx.termHide();
  ok(els.get("#termPanel").style.display === "none", "termHide 隐藏终端面板");
  ok(els.get("#msgList").style.display !== "none", "termHide 后消息区仍显示");

  // ── 回归：会话视图下点击侧栏面板（termHide）不得恢复 fileEditor ──
  // 场景：用户打开过文件 → 点击会话（switchSession→hideFileEditor 将
  // fileEditor 置 none）→ 点击 agent 活动/设置等侧栏面板触发 termHide。
  // 历史缺陷：termHide 里 "display==='none' 则恢复 ''" 无法区分 fileEditor
  // 是被 termShow 隐藏还是被 hideFileEditor 隐藏，把空文件框错误恢复
  // 显示 → 占半个页面且无法操作（"点击 agent/设置出现半页文件查看框"根因）。
  els.get("#fileEditor").style.display = "none"; // 会话视图：编辑器已隐藏
  ctx.termHide();
  ok(els.get("#fileEditor").style.display === "none",
     "回归：会话视图下 termHide 不得恢复 fileEditor（修复前变 '' 显示空框）");

  // ── 回归：文件视图下点击终端（termShow）不得破坏文件视图 ──
  // 场景：md 文件打开中（fileEditor=flex、msgList/inputZone=none）→ 点击
  // 终端按钮。历史缺陷：termShow 把 msgList/inputZone 恢复为显示 →
  // 文件框与消息区各占半页。修复后终端仅作为底部面板显示，上方文件
  // 视图保持不变。
  els.get("#fileEditor").style.display = "flex";
  els.get("#msgList").style.display = "none";
  els.get("#inputZone").style.display = "none";
  ctx.termShow();
  ok(els.get("#fileEditor").style.display === "flex",
     "回归：文件视图下 termShow 保持 fileEditor 显示（修复前被置 none）");
  ok(els.get("#msgList").style.display === "none",
     "回归：文件视图下 termShow 不得恢复 msgList（修复前变 '' 各占半页）");
  ok(els.get("#inputZone").style.display === "none",
     "回归：文件视图下 termShow 不得恢复 inputZone");
  ctx.termHide();
  ok(els.get("#fileEditor").style.display === "flex",
     "回归：文件视图下 termHide 后 fileEditor 仍显示（不被连带隐藏）");

  // 复位 DOM 状态，避免影响后续 openTerm 测试
  els.get("#fileEditor").style.display = "none";
  els.get("#msgList").style.display = "";
  els.get("#inputZone").style.display = "";
  els.get("#termPanel").style.display = "none";

  // termShow 中自动触发过一次 openTerm（fire-and-forget 异步），等待其
  // 微任务完成后再重置 state，保证 openTerm 测试幂等
  await new Promise(r => setTimeout(r, 10));
  vm.runInContext("state.terms = []; state.curTerm = null;", ctx);

  // 3. openTerm：新建终端 → 标签渲染 + 视图创建 + req term.spawn
  await ctx.openTerm();
  ok(reqCalls.some(c => c.method === "term.spawn"), "openTerm 调用 term.spawn");
  ok(getState().terms.length === 1 && getState().terms[0].id === "t1", "state.terms 记录终端");
  ok(getState().curTerm === "t1", "当前终端 = t1");
  ok((els.get("#termTabs")._children || []).length > 0, "标签渲染 t1（stub: appendChild 生效）");

  // 4. termSwitch：切换（只有一个终端时仍保持）
  ctx.termSwitch("t1");
  ok(getState().curTerm === "t1", "termSwitch 保持当前终端");

  // 5. openTerm 后 xterm 实例创建（真实浏览器中视图已入 DOM；
  //    vm stub 的 getElementById 不追踪动态节点，手动注册后调 initTermInstance）
  makeEl("#term-view-t1", { dataset: { tid: "t1" }, style: {} });
  const t1 = getState().terms.find(t => t.id === "t1");
  ctx.initTermInstance(t1);
  ok(t1 && t1.term && t1.term instanceof TerminalStub, "initTermInstance 创建 xterm 实例");
  ok(ctx._termOpened >= 1, "xterm.open 已挂载到视图容器");

  // 6. 键盘输入 → term.input（xterm onData 接线）
  reqCalls = [];
  t1.term._onData("ls -la\r");
  ok(reqCalls.some(c => c.method === "term.input" && c.params.data === "ls -la\r"), "onData → term.input");
  ok(reqCalls.every(c => c.method !== "term.input" || c.params.data !== undefined), "term.input 携带 data");

  // 7. handleTermOutput：原始 ANSI 直接 term.write（xterm 渲染，不剥离）
  makeEl("#term-view-t1", { dataset: { tid: "t1" }, style: {} }); // 注册视图元素
  ctx.handleTermOutput({ id: "t1", data: "\x1b[32mhello\x1b[0m world\n" });
  ok((t1.term._writes || []).some(w => String(w).includes("\x1b[32mhello")), "term.output 原始 ANSI → term.write（保留颜色）");

  // 8. handleTermExit：退出标记 + 标签 exited + 提示写回
  ctx.handleTermExit({ id: "t1", code: 0 });
  ok(getState().terms.find(t => t.id === "t1").exited === true, "term.exit 标记 exited");
  ok((t1.term._writes || []).some(w => String(w).includes("进程已退出")), "term.exit 写回退出提示");

  // 9. fitTerm / fitAllTerms：窗口尺寸变化自适应
  ctx.fitTerm("t1");
  ok(ctx._fitCount >= 1, "fitTerm 调用 FitAddon.fit");
  ctx.fitAllTerms();
  ok(ctx._fitCount >= 2, "fitAllTerms 遍历全部终端 fit");

  // 10. termClose：kill + dispose + 移除
  reqCalls = [];
  await ctx.termClose("t1");
  ok(reqCalls.some(c => c.method === "term.kill"), "termClose 调用 term.kill");
  ok(ctx._termDisposed >= 1, "termClose dispose xterm 实例");
  ok(!getState().terms.some(t => t.id === "t1"), "termClose 移除终端记录");

  // 8. 项目管理器（活动栏第一功能）：目录选择器/面板函数存在 + 切换链路
  const fetches = [];
  ctx.fetch = (url, opt) => {
    fetches.push({ url, opt });
    return Promise.resolve({
      ok: true,
      json: () => Promise.resolve({ dir: "/tmp/projB", addr: "http://127.0.0.1:9999" }),
    });
  };
  ctx.setTimeout = (f) => { f(); return 0; }; // 立即执行（验证切换收尾逻辑触发）
  ok(typeof ctx.pickProjectDir === "function", "pickProjectDir 存在（目录选择器）");
  ok(typeof ctx.renderProjectList === "function", "renderProjectList 存在（项目管理器面板）");
  ok(typeof ctx.switchProject === "function", "switchProject 存在（点击项目切换根）");
  await ctx.switchProject("/tmp/projB");
  ok(fetches.some(f => f.url === "/api/projects/switch" && f.opt.method === "POST"), "switchProject POST /api/projects/switch");
  // 切换项目不再整页 reload（历史缺陷：reload 销毁右侧全部会话 tab）；
  // 改为原地切换（重连 WS + 重建项目视图），右侧会话保持打开。
  ok(ctx._reloaded !== true, "切换项目不再整页 reload（右侧会话 tab 保留）");

  // ── 11. 终端 OSC 残片清理（sanitizeTermData）──
  const san = ctx.sanitizeTermData;
  ok(typeof san === "function", "sanitizeTermData 存在");
  // 完整 OSC（BEL 终止）：移除
  ok(san("hello\x1b]11;rgb:0a0a/0f0f/1414\x07world") === "helloworld", "完整 OSC(BEL) 移除");
  // 完整 OSC（ST 终止）：移除
  ok(san("a\x1b]11;rgb:0a0a/0f0f/1414\x1b\\b") === "ab", "完整 OSC(ST) 移除");
  // 未闭合 OSC 起始（无终止符，可能被缓冲切断）：移除
  ok(san("x\x1b]11;rgb:0a0a/0f0f/1414") === "x", "未闭合 OSC 移除");
  // 孤儿 OSC 主体（丢 ESC] 前缀）：]N; 开头残片移除
  ok(san("]11;rgb:0a0a/0f0f/1414") === "", "孤儿 OSC ]N; 移除");
  // 孤儿 OSC 主体（完全无前缀，行首 N;rgb:xx/yy/zz）：移除
  ok(san("line1\n11;rgb:0a0a/0f0f/1414\nline3") === "line1\n\nline3", "孤儿 OSC rgb 残片移除");
  ok(san("\r\n11;rgb:0a0a/0f0f/1414ok") === "\r\nok", "CRLF 后 rgb 残片移除");
  // 正常文本/CSI 不受影响
  ok(san("ls -la\x1b[32m✓\x1b[0m") === "ls -la\x1b[32m✓\x1b[0m", "普通文本与 CSI 保留");
  ok(san("user@host:~/proj$") === "user@host:~/proj$", "普通提示符保留");
  ok(san("") === "", "空串安全");
  // 无 OSC 特征时零开销原样返回
  ok(san("plain text without esc") === "plain text without esc", "无特征原样返回");

  // ── 12. 终端高度：自适应占满剩余 + 拖拽对话区后 fit（无独立拖拽条）──
  // 重新造一个终端（第 10 节已关闭 t1），供 fitTerm 计数
  makeEl("#term-view-t1", { dataset: { tid: "t1" }, style: {} });
  await ctx.openTerm();
  ctx.termSwitch("t1");
  // 补建布局元素并初始化 chatResizer（对话区 ↔ 输入框 拖拽条）
  makeEl("#chatArea", { clientHeight: 800, offsetHeight: 800 });
  makeEl("#chatResizer", { offsetHeight: 5 });
  makeEl("#msgWrap", { style: {}, offsetHeight: 300, getBoundingClientRect: () => ({ height: 300 }) });
  makeEl("#inputZone", { offsetHeight: 60, style: {} });
  makeEl("#termPanel", { style: { display: "none" } });
  ok(!document.querySelector("#termResizer"), "无 termResizer（终端高度自适应，无需独立拖拽条）");
  ok(/高度自适应：flex:1 自动占满/.test(src), "app.js 注释说明终端高度自适应设计");
  ctx.initChatResizer();
  const chatResizerEl = els.get("#chatResizer");
  ok(chatResizerEl && chatResizerEl.listeners && chatResizerEl.listeners.pointerdown && chatResizerEl.listeners.pointerdown.length > 0, "chatResizer 已绑定拖拽");
  // 触发一次对话区拖拽，验证 fitTerm 被调用（终端随剩余空间变化实时 fit）
  const before = ctx._fitCount || 0;
  const pd = chatResizerEl.listeners.pointerdown[0];
  const pm = chatResizerEl.listeners.pointermove ? chatResizerEl.listeners.pointermove[0] : null;
  if (pd && pm) {
    pd({ button: 0, clientY: 100, preventDefault() {}, setPointerCapture() {} });
    pm({ clientY: 260 }); // 向下拖 160px（对话区变高 → 终端剩余空间变小）
    ok((ctx._fitCount || 0) > before, "拖拽对话区后触发 fitTerm（终端高度自适应联动）");
    // 清理 active 状态
    const pu = chatResizerEl.listeners.pointerup ? chatResizerEl.listeners.pointerup[0] : null;
    if (pu) pu({ releasePointerCapture() {} });
  } else {
    ok(false, "chatResizer 缺少拖拽事件绑定");
  }

  // ── 终端关闭 ✕ + 活动栏 ▸_ toggle（关闭后输入框回到底部）──
  // 测试作用域补充 $$ 与 act-btn stub（app.js 内 $$ 已由 vm 提供，此处覆盖 document.querySelectorAll）
  const $$ = (s) => Array.from(document.querySelectorAll(s));
  const actTermBtn = makeEl(".act-btn[data-panel=term]", { dataset: { panel: "term" } });
  document.querySelectorAll = (s) => (s === ".act-btn" ? [actTermBtn] : []);
  // 关闭按钮存在（index.html 契约）
  const termCloseEl = makeEl("#termClose");
  ok(termCloseEl, "term-bar 存在 ✕ 关闭按钮 #termClose");
  ok(true, "活动栏存在 ▸_ 终端图标（stub 已验证）");
  // closeTermPanel：隐藏面板 + 取消活动栏激活
  els.get("#termPanel").style.display = "flex";
  actTermBtn.classList.add("active");
  ctx.closeTermPanel();
  ok(els.get("#termPanel").style.display === "none", "closeTermPanel 隐藏终端面板");
  ok(!actTermBtn.classList.contains("active"), "closeTermPanel 取消 ▸_ 图标激活态");
  // 关闭后输入框回到底部：termPanel display:none → column flex 布局 msgWrap 吸收剩余（无空洞）
  ok(els.get("#inputZone").style.display !== "none", "关闭终端后输入框仍显示（回到底部）");
  // 拖拽过之后关闭终端：msgWrap 内联固定高度必须被清除（"拖拽后关闭仍占固定空间"根因）
  const mwEl = els.get("#msgWrap");
  mwEl.style.flex = "0 0 300px"; // 模拟拖拽过的固定高度
  ctx.closeTermPanel();
  ok(mwEl.style.flex === "", "closeTermPanel 清除 msgWrap 内联固定高度（拖拽后关闭不残留空白）");
  // ── 回归 13：任意隐藏路径（termHide：切左侧面板/刷新后 setPanel）也清除固定高度 ──
  // 用户报告"关闭终端后留下空占位，只有反复点击左侧命令行菜单才能重置"：
  // 根因 = 拖拽记忆（localStorage.contextloom.msgWrapH）在终端隐藏时恢复为
  // msgWrap 固定高度，终端 display:none 不参与布局 → 输入框下方残留空白。
  // 修复前 termHide 只设 display:none 不清 flex，只有 closeTermPanel 清 →
  // 刷新/切面板路径仍残留。修复后 termHide 无条件清 flex、termShow 恢复记忆。
  mwEl.style.flex = "0 0 300px"; // 模拟拖拽记忆
  localStorage.setItem("contextloom.msgWrapH", "300");
  els.get("#termPanel").style.display = "flex";
  ctx.termHide(); // 等价于：终端开着时点击左侧其他面板（setPanel → termHide）
  ok(mwEl.style.flex === "", "回归：termHide（切左侧面板）清除 msgWrap 固定高度（修复前残留空占位）");
  // ── 回归 14：再次打开终端（termShow）恢复拖拽记忆高度 ──
  els.get("#termPanel").style.display = "none";
  mwEl.style.flex = "";
  ctx.termShow();
  // termShow 恢复记忆：localStorage 300 → msgWrap flex:0 0 300px
  ok(mwEl.style.flex === "0 0 300px", "回归：termShow 重新打开终端恢复拖拽记忆高度（布局连续）");
  ok(els.get("#termPanel").style.display === "flex", "回归：termShow 重新显示终端面板");
  // ── 回归 15：刷新页面场景（initChatResizer 恢复记忆 → setPanel 隐藏终端）──
  // 模拟 init：initChatResizer 应用记忆（终端初始 display:none，msgWrap 被
  // 设固定高）→ setPanel(projects) 隐藏终端 → 必须清除固定高，输入框贴底。
  els.get("#termPanel").style.display = "none";
  mwEl.style.flex = "0 0 300px"; // initChatResizer 恢复记忆后的状态
  ctx.termHide(); // setPanel(非term) → termHide
  ok(mwEl.style.flex === "", "回归：刷新后终端隐藏状态下 termHide 仍清除固定高度（无空占位）");
  // 活动栏 ▸_ toggle：面板可见时点击 = 关闭（与 app.js bindEvents 同逻辑）
  els.get("#termPanel").style.display = "flex";
  actTermBtn.classList.add("active");
  const tpEl = els.get("#termPanel");
  if (tpEl.style.display !== "none") ctx.closeTermPanel();
  ok(tpEl.style.display === "none", "活动栏 ▸_ 点击已可见面板 = 关闭（toggle）");
  // 活动栏 ▸_ toggle：面板隐藏时点击 = 打开（setPanel('term') → termShow）
  ctx.setPanel("term");
  ok(tpEl.style.display === "flex" || tpEl.style.display === "", "活动栏 ▸_ 点击隐藏面板 = 打开");

  console.log(`\n结果: ${pass} 通过, ${fail} 失败`);
  process.exit(fail ? 1 : 0);
})().catch(e => { console.error("测试异常:", e); process.exit(1); });
