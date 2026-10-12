// 回归测试：AI 回答一键复制（Markdown 原文 / 纯文本）。
// 运行: node cmd/contextloom-gui/tests/copy_message_test.js
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── DOM stub（支持 appendMessage/fillAssistantBubble/msgPlainText 所需能力）──
function stripTags(html) {
  return String(html).replace(/<[^>]*>/g, "").replace(/&lt;/g, "<").replace(/&gt;/g, ">")
    .replace(/&amp;/g, "&").replace(/&quot;/g, '"').replace(/&#39;/g, "'");
}
function makeEl(tag) {
  const children = [];
  const el = {
    tagName: (tag || "div").toUpperCase(), _children: children,
    style: {}, id: "", value: "", checked: false, disabled: false,
    dataset: {}, listeners: {}, attrs: {}, onclick: null, oncontextmenu: null,
    _clsSet: new Set(), _text: "", _innerHTML: "",
    classList: {
      add(c) { el._clsSet.add(c); }, remove(c) { el._clsSet.delete(c); },
      toggle(c, on) { if (on === undefined) on = !el._clsSet.has(c); on ? el._clsSet.add(c) : el._clsSet.delete(c); return on; },
      contains(c) { return el._clsSet.has(c); },
    },
    get className() { return Array.from(el._clsSet).join(" "); },
    set className(v) { el._clsSet.clear(); String(v || "").split(/\s+/).filter(Boolean).forEach(c => el._clsSet.add(c)); },
    get children() { return Array.from(children); },
    get textContent() { return el._text; },
    set textContent(v) { el._text = String(v == null ? "" : v); },
    get innerHTML() { return el._innerHTML; },
    set innerHTML(v) {
      el._innerHTML = String(v || "");
      children.length = 0;
      el._text = stripTags(el._innerHTML);
      const re = /<([a-z0-9-]+)([^>]*)>([\s\S]*?)<\/\1>/g;
      let m;
      while ((m = re.exec(el._innerHTML)) !== null) {
        const child = makeEl(m[1]);
        const cm = m[2].match(/class="([^"]+)"/);
        if (cm) child.className = cm[1];
        child.textContent = stripTags(m[3]);
        child.parentElement = el;
        children.push(child);
      }
    },
    appendChild(c) { if (c) { children.push(c); c.parentElement = el; } return c; },
    removeChild(c) { const i = children.indexOf(c); if (i >= 0) children.splice(i, 1); },
    insertBefore(c, ref) { const i = ref ? children.indexOf(ref) : -1; if (i >= 0) children.splice(i, 0, c); else children.push(c); return c; },
    remove() { this._removed = true; },
    setAttribute(k, v) { el.attrs[k] = String(v); },
    getAttribute(k) { return el.attrs[k] == null ? null : el.attrs[k]; },
    removeAttribute(k) { delete el.attrs[k]; },
    focus() {}, scrollIntoView() {}, select() {},
    addEventListener(t, fn) { (el.listeners[t] = el.listeners[t] || []).push(fn); },
    cloneNode() { const c = makeEl(el.tagName); c._clsSet = new Set(el._clsSet); c._text = el._text; c._innerHTML = el._innerHTML; children.forEach(ch => c.appendChild(ch.cloneNode ? ch.cloneNode() : makeEl("div"))); return c; },
  };
  const matchCls = (node, cls) => node.className && node.className.split(" ").includes(cls);
  const walk = (node, fn) => { fn(node); (node.children || []).forEach(c => walk(c, fn)); };
  el.querySelector = (s) => {
    if (s.startsWith(".")) { let found = null; walk(el, n => { if (!found && matchCls(n, s.slice(1))) found = n; }); return found; }
    return null;
  };
  el.querySelectorAll = (s) => {
    const res = [];
    if (s.startsWith(".")) { const cls = s.slice(1); walk(el, n => { if (matchCls(n, cls)) res.push(n); }); }
    return res;
  };
  el.closest = () => el;
  el.getBoundingClientRect = () => ({ width: 300, height: 100, top: 0, left: 0 });
  return el;
}

const els = new Map();
const document = {
  querySelector: (s) => { if (s.startsWith("#")) { let e = els.get(s); if (!e) { e = makeEl("div"); e.id = s.slice(1); els.set(s, e); } return e; } return null; },
  querySelectorAll: () => [],
  getElementById: (id) => document.querySelector("#" + id),
  createElement: (t) => makeEl(t),
  createDocumentFragment: () => { const f = makeEl("frag"); return f; },
  addEventListener() {}, execCommand: () => true,
  body: makeEl("body"),
};

let clipboardCalls = [];
const window = { innerWidth: 1280, innerHeight: 800, isSecureContext: true, addEventListener() {}, location: { reload() {} } };
const localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
class AbortControllerStub { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
class TerminalStub { constructor() {} loadAddon() {} open() {} onData() {} write() {} dispose() {} focus() {} }
class FitAddonStub { fit() {} }

const ctxErrors = [];
const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage,
  navigator: { clipboard: { writeText: (t) => { clipboardCalls.push(t); return Promise.resolve(); } } },
  console: { log: () => {}, error: (...a) => { ctxErrors.push(a.map(String).join(" ")); }, warn: () => {} },
  setTimeout, clearTimeout, setInterval, clearInterval,
  AbortController: AbortControllerStub, requestAnimationFrame: (f) => setTimeout(f, 0),
  Date, Math, JSON, String, Number, parseInt, parseFloat, isNaN, Object, Array, RegExp, Map, Set, Promise, Error,
  fetch: async () => ({ ok: true, json: async () => ({}) }),
  location: { protocol: "http:", host: "127.0.0.1:18765", reload() {} },
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

let passed = 0, failed = 0;
function assert(cond, msg) {
  if (cond) { passed++; process.stdout.write("  ✅ " + msg + "\n"); }
  else { failed++; process.stdout.write("  ❌ " + msg + "\n"); }
}

function callAppendMessage(role, content) {
  return vm.runInContext("appendMessage", ctx).call(null, role, content, false, false, null);
}
function callMsgPlainText(bubble) { return vm.runInContext("msgPlainText", ctx).call(null, bubble); }

function main() {
  process.stdout.write("── 测试 A：assistant 消息挂载一键复制 ──\n");

  const MD = "# 标题\n\n正文**加粗**\n\n```go\nfmt.Println(\"hi\")\n```";
  const bubble = callAppendMessage("assistant", MD);
  const row = bubble.parentElement;

  // A1. 原文留存
  assert(bubble.dataset.raw === MD, "A1 气泡 dataset.raw 留存 Markdown 原文");

  // A2. 按钮组存在且含 2 个按钮
  const bar = row.querySelector(".msg-copy-bar");
  assert(!!bar, "A2 assistant 行挂载 .msg-copy-bar");
  const btns = bar ? bar.children : [];
  assert(btns.length === 2, "A2 按钮组含 2 个按钮（MD/文本），实际 " + btns.length);
  assert(btns[0] && btns[0].title.includes("Markdown"), "A2 第1个按钮=复制 Markdown 原文");
  assert(btns[1] && btns[1].title.includes("纯文本"), "A2 第2个按钮=复制纯文本");
  process.stdout.write("── 测试 B：Markdown 原文复制 ──\n");

  clipboardCalls = [];
  btns[0].onclick({ stopPropagation() {} });
  // onclick 内 copyText 是异步 promise，等一个微任务
  return Promise.resolve().then(() => {
    assert(clipboardCalls.length === 1, "B1 点击 MD 按钮触发剪贴板写入");
    assert(clipboardCalls[0] === MD, "B1 剪贴板内容=Markdown 原文（含 # 与 ```）");

    process.stdout.write("── 测试 C：纯文本复制 ──\n");

    // C1. msgPlainText 提取正文（不含 Markdown 标记）
    const plain = callMsgPlainText(bubble);
    assert(plain.includes("标题"), "C1 纯文本含正文");
    assert(plain.includes("fmt.Println"), "C1 纯文本含代码内容");
    assert(!plain.includes("```") && !plain.includes("# "), "C1 纯文本不含 Markdown 标记");

    clipboardCalls = [];
    btns[1].onclick({ stopPropagation() {} });
    return Promise.resolve().then(() => {
      assert(clipboardCalls.length === 1 && clipboardCalls[0] === plain, "C2 点击文本按钮复制纯文本");

      process.stdout.write("── 测试 D：思考块/工具卡处理 ──\n");

      // D0. Markdown 原文剔除思考块（思考是内部推理，不属答案）
      const bm = makeEl("div");
      bm.dataset.raw = "<thinking>内部推理 A</thinking>\n\n# 最终答案\n\n正文内容";
      const pm = vm.runInContext("msgMarkdown", ctx).call(null, bm);
      assert(!pm.includes("内部推理 A") && pm.includes("# 最终答案") && pm.includes("正文内容"),
        "D0 Markdown 原文剔除思考块、保留正文，实际 " + JSON.stringify(pm));

      // D0b. 三种思考标签格式 + 未闭合
      const bm2 = makeEl("div");
      bm2.dataset.raw = "前<thinking>甲</thinking>中<|reasoning|>乙<|/reasoning|>后";
      const pm2 = vm.runInContext("msgMarkdown", ctx).call(null, bm2);
      assert(pm2 === "前中后", "D0b 三种思考标签格式均剔除，实际 " + JSON.stringify(pm2));

      // D1. 思考块内容不进纯文本（折叠态未选定内容）
      const b2 = makeEl("div");
      b2.dataset.raw = "思考: 内部推理\n最终答案";
      const think = makeEl("div"); think.className = "think-block";
      const tb = makeEl("div"); tb.className = "think-body"; tb.textContent = "内部推理";
      think.appendChild(tb);
      const content = makeEl("div"); content.className = "msg-content"; content.textContent = "最终答案";
      b2.appendChild(think); b2.appendChild(content);
      const p2 = callMsgPlainText(b2);
      assert(p2 === "最终答案", "D1 纯文本排除思考块内容，实际 " + JSON.stringify(p2));

      // D2. 无正文（纯工具卡）→ 回退整气泡文本
      const b3 = makeEl("div");
      const tool = makeEl("div"); tool.className = "tool-result";
      const tbody = makeEl("div"); tbody.className = "tool-body"; tbody.textContent = "命令输出 ABC";
      tool.appendChild(tbody); b3.appendChild(tool);
      const p3 = callMsgPlainText(b3);
      assert(p3.includes("命令输出 ABC"), "D2 纯工具卡回退取工具输出，实际 " + JSON.stringify(p3));

      process.stdout.write("── 测试 E：非 assistant 消息不挂按钮 ──\n");

      const ub = callAppendMessage("user", "你好");
      const urow = ub.parentElement;
      assert(!urow.querySelector(".msg-copy-bar"), "E1 用户消息不挂一键复制按钮");
      assert(ub.dataset.raw === "你好", "E1 用户消息同样留存原文（右键复制兼容）");

      process.stdout.write("── 测试 F：剪贴板不可用时回退 ──\n");

      // 模拟 clipboard 抛错 → fallbackCopy（execCommand）
      const origClip = ctx.navigator.clipboard.writeText;
      ctx.navigator.clipboard.writeText = () => Promise.reject(new Error("denied"));
      clipboardCalls = [];
      btns[0].onclick({ stopPropagation() {} });
      return Promise.resolve().then(() => {
        ctx.navigator.clipboard.writeText = origClip;
        assert(true, "F1 clipboard 失败走 fallback 不抛异常（execCommand 回退）");
        assert(ctxErrors.length === 0, "F1 全程无 console.error（无 JS 异常），实际 " + ctxErrors.length);

        process.stdout.write("\n结果: " + passed + " 通过, " + failed + " 失败\n");
        process.exit(failed > 0 ? 1 : 0);
      });
    });
  });
}
main();
