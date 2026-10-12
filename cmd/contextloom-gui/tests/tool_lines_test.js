// 回归测试：GUI 工具调用内容样式区分。
// 运行: node cmd/contextloom-gui/tests/tool_lines_test.js
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// ── 完善的 DOM stub（支持 appendChild / children / innerHTML / className / querySelector）──
let _elId = 0;
function makeEl(tag, extra = {}) {
  const id = ++_elId;
  const children = [];
  const el = {
    _id: id, _tag: tag, _children: children,
    style: {}, className: "", id: "", innerHTML: "", textContent: "",
    value: "", checked: false, disabled: false,
    dataset: {}, listeners: {},
    _clsSet: new Set(),
    classList: {
      _set: null,
      add(c) { el._clsSet.add(c); el._syncCls(); },
      remove(c) { el._clsSet.delete(c); el._syncCls(); },
      toggle(c, on) {
        if (on === undefined) on = !el._clsSet.has(c);
        on ? el._clsSet.add(c) : el._clsSet.delete(c);
        el._syncCls();
        return on;
      },
      contains(c) { return el._clsSet.has(c); },
    },
    _syncCls() { el.className = Array.from(el._clsSet).join(" "); },
    get children() { return Array.from(children); },
    appendChild(c) { if (c) { children.push(c); c.parentElement = el; } return c; },
    removeChild(c) { const i = children.indexOf(c); if (i >= 0) children.splice(i, 1); },
    insertBefore(c, ref) { const i = ref ? children.indexOf(ref) : -1; if (i >= 0) children.splice(i, 0, c); else children.push(c); },
    remove() { this._removed = true; },
    focus() {}, scrollIntoView() {},
    addEventListener(type, fn) { (this.listeners[type] = this.listeners[type] || []).push(fn); },
    ...extra,
  };
  Object.defineProperty(el, "className", {
    get() { return Array.from(el._clsSet).join(" "); },
    set(v) {
      el._clsSet.clear();
      String(v || "").split(/\s+/).filter(Boolean).forEach(c => el._clsSet.add(c));
    },
  });
  Object.defineProperty(el, "innerHTML", {
    get() { return this._innerHTML || ""; },
    set(v) {
      this._innerHTML = v;
      children.length = 0;
      // 简单解析：span/text/div 提取 textContent
      const m = v.match(/<([a-z0-9-]+)([^>]*)>([\s\S]*?)<\/\1>/g);
      if (m) {
        for (const tagBlock of m) {
          const tm = tagBlock.match(/<([a-z0-9-]+)([^>]*)>([\s\S]*?)<\/\1>/);
          if (tm) {
            const child = makeEl(tm[1]);
            child.className = (tm[2].match(/class="([^"]+)"/) || [,""])[1];
            child.textContent = tm[3].replace(/<[^>]+>/g, "");
            child.parentElement = el;
            children.push(child);
          }
        }
      }
    },
  });
  Object.defineProperty(el, "firstElementChild", { get() { return children[0] || null; } });
  Object.defineProperty(el, "lastElementChild", { get() { return children[children.length - 1] || null; } });
  el.querySelector = (s) => {
    if (s.startsWith(".")) {
      const cls = s.slice(1);
      if (el.className && el.className.split(" ").includes(cls)) return el;
      for (const c of children) {
        const r = c.querySelector ? c.querySelector(s) : null;
        if (r) return r;
      }
      return null;
    }
    if (s.startsWith("#")) { return el.id === s.slice(1) ? el : null; }
    return null;
  };
  el.querySelectorAll = (s) => {
    const res = [];
    // 支持 ".a.b .c" 形态：取最后一个 class 段做匹配，中间段做祖先约束（简化）
    const segs = s.trim().split(/\s+/).map(x => x.replace(/^\./, ""));
    const lastCls = segs[segs.length - 1];
    const matchAll = (node, depth) => {
      const nodeCls = node.className ? node.className.split(" ") : [];
      if (nodeCls.includes(lastCls)) res.push(node);
      for (const c of (node.children || [])) matchAll(c, depth + 1);
    };
    if (s.startsWith(".")) matchAll(el, 0);
    return res;
  };
  el.closest = () => el;
  return el;
}

const els = new Map();
const document = {
  querySelector: (s) => {
    if (s.startsWith("#")) {
      let el = els.get(s);
      if (!el) { el = makeEl("div"); els.set(s, el); }
      return el;
    }
    return null;
  },
  querySelectorAll: () => [],
  getElementById: (id) => {
    let el = els.get("#" + id);
    if (!el) { el = makeEl("div"); els.set("#" + id, el); }
    return el;
  },
  addEventListener() {},
  createElement: (tag) => makeEl(tag),
  createDocumentFragment: () => { const f = makeEl("frag"); f.children = []; f.appendChild = (c) => { f.children.push(c); return c; }; return f; },
  body: { appendChild() {} },
};
const window = { innerWidth: 1280, addEventListener() {}, location: { reload() {} } };
const localStorage = { getItem: () => null, setItem() {}, removeItem() {} };
class AbortControllerStub { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
class TerminalStub { constructor() {} loadAddon() {} open() {} onData() {} write() {} dispose() {} focus() {} }
class FitAddonStub { fit() {} }

const ctxErrors = [];
const ctx = {
  Terminal: TerminalStub, FitAddon: { FitAddon: FitAddonStub },
  document, window, localStorage, console: { log: () => {}, error: (...a) => { ctxErrors.push(a.map(String).join(" ")); }, warn: () => {} }, setTimeout, clearTimeout, setInterval, clearInterval,
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

function callParse(body) {
  return vm.runInContext("parseAssistantBody(" + JSON.stringify(body) + ")", ctx);
}
function callFill(bubble, text) {
  vm.runInContext("fillAssistantBubble", ctx).call(null, bubble, text);
}
function callBuildTool(p) {
  return vm.runInContext("buildToolEl", ctx).call(null, p);
}

function main() {
  process.stdout.write("── 测试 A：parseAssistantBody 提取工具行 ──\n");

  // A1. 纯工具调用行
  let parts = callParse("⚡ 执行工具: read_file");
  assert(parts.length === 1 && parts[0].type === "tool" && parts[0].kind === "call" && parts[0].name === "read_file",
    "A1 纯工具调用行 → {type:tool, kind:call, name:read_file}");

  // A2. 工具结果行
  parts = callParse("✔ read_file: 文件内容 ABC");
  assert(parts.length === 1 && parts[0].type === "tool" && parts[0].kind === "ok" && parts[0].name === "read_file" && parts[0].content === "文件内容 ABC",
    "A2 工具结果行 → {type:tool, kind:ok, name, content}");

  // A3. 工具失败行
  parts = callParse("✗ exec_command: command not found");
  assert(parts.length === 1 && parts[0].type === "tool" && parts[0].kind === "err" && parts[0].name === "exec_command" && parts[0].content === "command not found",
    "A3 工具失败行 → {type:tool, kind:err}");

  // A4. 正文 + 工具行混合
  parts = callParse("我先读取文件。\n⚡ 执行工具: read_file\n✔ read_file: ok\n然后分析。");
  assert(parts.length === 4, "A4 混合内容 → 4 段（text/tool-call/tool-ok/text），实际 " + parts.length);
  assert(parts[0].type === "text" && parts[0].content.includes("我先读取文件") &&
    parts[1].type === "tool" && parts[1].kind === "call" && parts[1].name === "read_file" &&
    parts[2].type === "tool" && parts[2].kind === "ok" && parts[2].name === "read_file" &&
    parts[3].type === "text" && parts[3].content.includes("然后分析"),
    "A4 顺序与类型正确");

  // A5. 思考块内工具文本不误判
  parts = callParse("正文\n<thinking>\n⚡ 执行工具: 内部说明\n</thinking>\n尾部");
  const toolCount = parts.filter(p => p.type === "tool").length;
  assert(toolCount === 0, "A5 思考块内 ⚡ 文本不误判为工具行（tool 段数=" + toolCount + "）");

  // A6. 多行工具结果（后端协议单行发送，第一行是工具行，其余为正文）
  parts = callParse("✔ search: 结果行1\n结果行2\n结果行3");
  assert(parts.length === 2, "A6 多行结果 → 2 段（tool + text），实际 " + parts.length);
  assert(parts[0].type === "tool" && parts[0].kind === "ok" && parts[0].name === "search" && parts[0].content === "结果行1",
    "A6 第一行解析为工具结果行");
  assert(parts[1].type === "text" && parts[1].content === "结果行2\n结果行3",
    "A6 后续行保留为正文");

  // A7. 中文冒号分隔
  parts = callParse("✔ read_file：内容");
  assert(parts.length === 1 && parts[0].type === "tool" && parts[0].kind === "ok" && parts[0].name === "read_file",
    "A7 中文冒号也能解析");

  // A8. 空内容结果行
  parts = callParse("✔ exec_command: ");
  assert(parts.length === 1 && parts[0].type === "tool" && parts[0].kind === "ok" && parts[0].content === "",
    "A8 空结果行可解析（content 为空字符串）");

  // A9. 思考标签正常（回归：不破坏原有思考解析）
  parts = callParse("开始<thinking>内部思考</thinking>结束");
  assert(parts.length === 3, "A9 思考标签 3 段（text/think/text），实际 " + parts.length);
  assert(parts[0].type === "text" && parts[1].type === "think" && parts[1].content === "内部思考" && parts[2].type === "text",
    "A9 思考标签解析不受影响");

  // A10. 无工具行纯正文
  parts = callParse("纯文本消息\n多行");
  assert(parts.length === 1 && parts[0].type === "text" && parts[0].content.includes("纯文本"),
    "A10 纯正文不变");

  // A11. 误判防护：✔ 开头但非协议格式的行 → 内容必须保全不丢失
  parts = callParse("✔ 完成: 今天的优化全部落地");
  const joined11 = parts.map(p => p.type === "tool" ? (p.name + " " + p.content) : p.content).join(" ");
  assert(joined11.includes("完成") && joined11.includes("今天的优化全部落地"),
    "A11 ✔ 开头正文行内容保全（误判为工具卡也不丢内容）");

  // A12. 全量内容保全：任意混合输入解析后无字符丢失（逐字符对比）
  const mixedSrc = "第一段\n⚡ 执行工具: grep\n✔ grep: 命中\n✗ grep: 失败\n第二段<thinking>想</thinking>第三段\n✔ 普通总结: 结束";
  parts = callParse(mixedSrc);
  const rebuilt = parts.map(p => {
    if (p.type === "tool") {
      if (p.kind === "call") return "⚡ 执行工具: " + p.name; // 协议格式：name 后无冒号
      return (p.kind === "err" ? "✗ " : "✔ ") + p.name + ": " + p.content;
    }
    if (p.type === "think") return "<thinking>" + p.content + "</thinking>";
    return p.content;
  }).join("\n");
  const norm = s => s.replace(/\s+/g, ""); // 内容保全：非空白字符逐一等价（空白布局重建不还原，不计）
  assert(norm(rebuilt) === norm(mixedSrc),
    "A12 内容保全：重建文本与原输入逐字符等价（误判不丢内容）");

  process.stdout.write("── 测试 B：buildToolEl 渲染 ──\n");

  // B1. 调用行
  const callEl = callBuildTool({ type: "tool", kind: "call", name: "read_file", content: "" });
  assert(callEl.className === "tool-call", "B1 工具调用行 → .tool-call");
  const callIcon = callEl.querySelector(".tool-call-icon");
  assert(callIcon && callIcon.textContent === "⚡", "B1 ⚡ 图标");
  const callName = callEl.querySelector(".tool-call-name");
  assert(callName && callName.textContent === "read_file", "B1 name 文本");

  // B2. 结果行
  const okEl = callBuildTool({ type: "tool", kind: "ok", name: "read_file", content: "结果" });
  assert(okEl.className === "tool-result", "B2 工具结果行 → .tool-result");
  assert(okEl.querySelector(".tool-head") && okEl.querySelector(".tool-body"), "B2 head + body");
  const okIcon = okEl.querySelector(".tool-result-icon");
  assert(okIcon && okIcon.textContent === "✔", "B2 ✔ 图标");

  // B3. 失败行
  const errEl = callBuildTool({ type: "tool", kind: "err", name: "exec_command", content: "bad" });
  assert(errEl.className.includes("tool-err"), "B3 工具失败行 → .tool-err");
  const errIcon = errEl.querySelector(".tool-result-icon");
  assert(errIcon && errIcon.textContent === "✗", "B3 ✗ 图标");

  // B4. 结果行默认可折叠（初始无 open 类）
  assert(!okEl.classList.contains("open"), "B4 结果行初始无 open（可折叠）");
  okEl.classList.toggle("open");
  assert(okEl.classList.contains("open"), "B4 toggle open 生效");

  process.stdout.write("── 测试 C：fillAssistantBubble 渲染 ──\n");

  // C1. 渲染含工具行的完整内容
  const bubble = makeEl("div");
  callFill(bubble, "先看文件\n⚡ 执行工具: read_file\n✔ read_file: 内容ABC\n完成");
  const kids = bubble.children;
  assert(kids.length === 4, "C1 渲染 4 个子节点，实际 " + kids.length);
  assert(kids[0].className.includes("msg-content"), "C1 第1个：msg-content");
  assert(kids[1].className === "tool-call", "C1 第2个：tool-call");
  assert(kids[2].className === "tool-result", "C1 第3个：tool-result");
  assert(kids[3].className.includes("msg-content"), "C1 第4个：msg-content");

  // C2. 结构同构 → 增量更新（tool 卡片 DOM 节点保留）
  const callNode = kids[1];
  const resultNode = kids[2];
  callFill(bubble, "先看文件\n⚡ 执行工具: read_file\n✔ read_file: 内容ABC-更新\n完成");
  assert(bubble.children[1] === callNode && bubble.children[2] === resultNode,
    "C2 结构同构时 tool 卡片 DOM 节点保留（增量更新）");

  // C3. 工具结果内容已更新（查 .tool-body 子节点）
  const body3 = bubble.children[2].querySelector(".tool-body");
  assert(body3 && body3.textContent.includes("内容ABC-更新"), "C3 工具结果内容已增量更新");

  // C4. 新增工具行 → 结构变化 → 重建
  const b4 = makeEl("div");
  callFill(b4, "⚡ 执行工具: a");
  b4.innerHTML = "";
  callFill(b4, "⚡ 执行工具: a\n✔ a: 结果X\n⚡ 执行工具: b\n✔ b: 结果Y");
  assert(b4.children.length === 4, "C4 重建后 4 节点");
  assert(b4.children[0].className === "tool-call", "C4 第1个 tool-call");
  assert(b4.children[2].className === "tool-call", "C4 第3个 tool-call（第二个工具调用）");

  // C5. 重建后保留已展开的工具结果卡状态
  const b5 = makeEl("div");
  callFill(b5, "⚡ 执行工具: a\n✔ a: 结果X");
  b5.children[1].classList.add("open");
  callFill(b5, "⚡ 执行工具: a\n✔ a: 结果X\n⚡ 执行工具: b\n✔ b: 结果Y");
  const newRs = b5.children.filter(c => c.className.includes("tool-result"));
  assert(newRs.length === 2, "C5 重建后 2 个 tool-result，实际 " + newRs.length);
  assert(newRs[0].classList.contains("open"), "C5 已展开结果卡恢复 open 状态");

  process.stdout.write("\n结果: " + passed + " 通过, " + failed + " 失败\n");
  process.exit(failed > 0 ? 1 : 0);
}
main();