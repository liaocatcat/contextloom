// /effort off + /maxlen 命令与状态栏后缀前端冒烟（思考长度/输出上限/输入预算接线）
// 运行: node cmd/contextloom-gui/tests/maxlen_effort_test.js
// vm 加载生产 app.js（stub document），直接调用 handleCommand/CMD_LIST 与
// fmtLimitTokens/modelInfoSuffix/renderCfgField——与运行时代码 100% 一致。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// 假元素：#modelInfo（状态栏）——refreshStatus/refreshModelInfo 渲染断言用
const els = { modelInfo: { textContent: "" } };
const ctx = {
  document: {
    addEventListener: () => {},
    querySelector: (sel) => (sel === "#modelInfo" ? els.modelInfo : null),
    querySelectorAll: () => [],
  },
  window: {},
  WebSocket: class {},
  localStorage: { getItem: () => null, setItem: () => {} },
  console,
};
vm.createContext(ctx);
vm.runInContext(src, ctx);

let pass = 0, fail = 0;
function eq(name, got, want) {
  const g = JSON.stringify(got), w = JSON.stringify(want);
  if (g === w) { pass++; console.log("  ✅ " + name); }
  else { fail++; console.log("  ❌ " + name + " → got " + g + " want " + w); }
}
function has(name, s, sub) { eq(name, String(s).includes(sub), true); }

// ── 命令表 / 帮助清单 ──
console.log("── CMD_LIST / 帮助清单 ──");
const cmdList = vm.runInContext("CMD_LIST", ctx);
const effItem = cmdList.find(c => c.name.indexOf("/effort") === 0);
has("CMD_LIST /effort 描述含 off 枚举", effItem.desc, "(off/low/medium/high/xhigh/max)");
has("CMD_LIST /effort 描述 off=关闭思考", effItem.desc, "off=关闭思考");
const mlItem = cmdList.find(c => c.name.indexOf("/maxlen") === 0);
eq("CMD_LIST 含 /maxlen 条目", !!mlItem, true);
has("CMD_LIST /maxlen 描述 0=不限制", mlItem.desc, "0=不限制");
has("CMD_LIST /maxlen 描述与上下文解耦", mlItem.desc, "与上下文解耦");
const helpSrc = src.slice(src.indexOf('case "help"'), src.indexOf('case "help"') + 400);
has("help 命令清单含 /effort /maxlen", helpSrc, "/effort /maxlen");
has("源码含 maxlen case", src, 'case "maxlen": {');
has("maxlen 走 config.set", src, 'await req("config.set", { [key]: n });');

// ── fmtLimitTokens ──
console.log("── fmtLimitTokens（0=不限；<1000 原值；整除 Nk；否则 N.Nk）──");
eq("0 → 不限", ctx.fmtLimitTokens(0), "不限");
eq("999 → 999", ctx.fmtLimitTokens(999), "999");
eq("1000 → 1k", ctx.fmtLimitTokens(1000), "1k");
eq("1500 → 1.5k", ctx.fmtLimitTokens(1500), "1.5k");
eq("64000 → 64k", ctx.fmtLimitTokens(64000), "64k");
eq("65536 → 65.5k", ctx.fmtLimitTokens(65536), "65.5k");
eq("131072 → 131.1k", ctx.fmtLimitTokens(131072), "131.1k");

// ── modelInfoSuffix ──
console.log("── modelInfoSuffix（状态栏：输出上限 + 思考关）──");
eq("默认无后缀", ctx.modelInfoSuffix({ backend: "deepseek", effort: "max", maxOutputTokens: 0, thinkingEnabled: true }), "");
eq("输出上限后缀", ctx.modelInfoSuffix({ effort: "max", maxOutputTokens: 65536, thinkingEnabled: true }), " · 输出65.5k");
eq("effort=off → 思考关", ctx.modelInfoSuffix({ effort: "off", maxOutputTokens: 0, thinkingEnabled: false }), " · 思考关");
eq("thinking_enabled=false → 思考关", ctx.modelInfoSuffix({ effort: "max", maxOutputTokens: 0, thinkingEnabled: false }), " · 思考关");
eq("输出上限+思考关组合", ctx.modelInfoSuffix({ effort: "off", maxOutputTokens: 64000, thinkingEnabled: false }), " · 输出64k · 思考关");
eq("null 安全", ctx.modelInfoSuffix(null), "");

// ── 设置面板 select 空枚举显示 ──
console.log("── 设置面板 select 空枚举（继承默认）──");
vm.runInContext('state.cfgValues = { "model.thinking_length": "" };', ctx);
const htmlSel = ctx.renderCfgField({ key: "model.thinking_length", label: "思考长度", type: "select", group: "模型", enums: ["", "off", "low"], desc: "x" });
has("空枚举显示（继承默认）", htmlSel, "（继承默认）");
has("空枚举 value 仍为空串且选中", htmlSel, 'value="" selected');
has("非空枚举原样显示", htmlSel, ">off</option>");
vm.runInContext('state.cfgValues = { "model.thinking_length": "high" };', ctx);
const htmlSel2 = ctx.renderCfgField({ key: "model.thinking_length", label: "思考长度", type: "select", group: "模型", enums: ["", "off", "high"], desc: "x" });
has("选中 high 而非空项", htmlSel2, 'value="high" selected');
eq("空项未选中", htmlSel2.includes('value="" selected'), false);

// ── 运行时：状态栏渲染 + handleCommand ──
(async () => {
  console.log("── refreshStatus / refreshModelInfo（状态栏渲染）──");
  const calls = [];
  ctx.req = async (method, params) => {
    calls.push({ method, params });
    if (method === "status.bar") {
      return { mode: "chat", sessionId: "s1", ctx: "1%", goal: "无活动目标",
        model: { backend: "deepseek", effort: "max", maxOutputTokens: 65536, maxInputTokens: 0, thinkingEnabled: true } };
    }
    if (method === "model.info") {
      return { backend: "deepseek", effort: "off", maxOutputTokens: 0, maxInputTokens: 0, thinkingEnabled: false };
    }
    return {};
  };
  ctx.appendSystem = (t) => { calls.push({ sys: t }); };
  ctx.toast = (t, k) => { calls.push({ toast: t, kind: k }); };
  ctx.updateCfgSaveState = () => { calls.push({ cfgState: true }); };
  function reset() { calls.length = 0; }
  function findCall(m) { return calls.find(c => c.method === m); }
  function lastSys() { for (let i = calls.length - 1; i >= 0; i--) if (calls[i].sys !== undefined) return calls[i].sys; return ""; }
  function lastToast() { for (let i = calls.length - 1; i >= 0; i--) if (calls[i].toast !== undefined) return calls[i]; return null; }

  vm.runInContext('state.ws = { readyState: 1 }; state.sessionId = "s1";', ctx);
  await vm.runInContext("refreshStatus()", ctx);
  eq("轮询渲染含输出上限", els.modelInfo.textContent, "deepseek · max · 输出65.5k");
  await vm.runInContext("refreshModelInfo()", ctx);
  eq("model.info 渲染含思考关", els.modelInfo.textContent, "deepseek · off · 思考关");

  console.log("── handleCommand：/maxlen 显示与设置 ──");
  // 1) 无参：缓存命中，不发 config.get
  reset();
  vm.runInContext('state.cfgValues = { "model.max_output_tokens": 65536, "model.max_input_tokens": 0 };', ctx);
  await vm.runInContext('handleCommand("/maxlen")', ctx);
  eq("无参显示缓存值", lastSys(), "当前长度上限: 输出 65.5k / 输入 不限（0=不限制）");
  eq("缓存命中不补拉 config.get", !!findCall("config.get"), false);

  // 2) 无参：缓存未加载 → 补拉 config.get
  reset();
  vm.runInContext("state.cfgValues = {};", ctx);
  await vm.runInContext('handleCommand("/maxlen")', ctx);
  eq("缓存空时补拉 config.get", !!findCall("config.get"), true);
  eq("补拉后回退显示 0=不限", lastSys(), "当前长度上限: 输出 不限 / 输入 不限（0=不限制）");

  // 3) output 设置成功 → config.set + 缓存同步 + 状态栏刷新
  reset();
  vm.runInContext("state.cfgValues = {};", ctx);
  await vm.runInContext('handleCommand("/maxlen output 65536")', ctx);
  const cs = findCall("config.set");
  eq("output 调 config.set", !!cs, true);
  eq("output 提交键值", cs.params, { "model.max_output_tokens": 65536 });
  eq("output 成功提示", lastSys(), "输出上限已设为 65536（65.5k）");
  eq("成功后刷新状态栏（model.info）", calls.some(c => c.method === "model.info"), true);
  eq("本地缓存同步", vm.runInContext('state.cfgValues["model.max_output_tokens"]', ctx), 65536);

  // 4) input 0 → 不限制文案
  reset();
  vm.runInContext("state.cfgValues = {};", ctx);
  await vm.runInContext('handleCommand("/maxlen input 0")', ctx);
  const cs4 = findCall("config.set");
  eq("input 提交键", cs4.params, { "model.max_input_tokens": 0 });
  eq("input 0 提示", lastSys(), "输入上限已设为 0（不限制，自动取最大上下文）");

  // 5) 非法参数（未知类型/负数/非数字/小数）→ warn 且不提交
  reset();
  await vm.runInContext('handleCommand("/maxlen foo")', ctx);
  eq("未知类型 warn", lastToast().kind, "warn");
  has("未知类型用法提示", lastToast().toast, "用法: /maxlen output|input <tokens>");
  reset();
  await vm.runInContext('handleCommand("/maxlen output -1")', ctx);
  eq("负数 warn", lastToast().kind, "warn");
  has("负数提示", lastToast().toast, "非负整数");
  reset();
  await vm.runInContext('handleCommand("/maxlen output abc")', ctx);
  eq("非数字 warn", lastToast().kind, "warn");
  reset();
  await vm.runInContext('handleCommand("/maxlen output 1.5")', ctx);
  eq("小数 warn", lastToast().kind, "warn");
  eq("非法参数不调 config.set", !!findCall("config.set"), false);

  // 6) /effort off 与非法值
  console.log("── handleCommand：/effort off ──");
  reset();
  await vm.runInContext('handleCommand("/effort off")', ctx);
  eq("off 调 model.set", findCall("model.set").params, { effort: "off" });
  eq("off 提示文案", lastSys(), "已关闭思考（不发送 reasoning_effort）");
  eq("off 后刷新状态栏（model.info）", calls.some(c => c.method === "model.info"), true);
  reset();
  await vm.runInContext('handleCommand("/effort ultra")', ctx);
  eq("非法 effort warn", lastToast().kind, "warn");
  has("effort 用法含 off", lastToast().toast, "off/low/medium/high/xhigh/max");
  eq("非法 effort 不调 model.set", !!findCall("model.set"), false);

  console.log(`\nmaxlen/effort 冒烟: ${pass} 通过 / ${fail} 失败`);
  process.exit(fail ? 1 : 0);
})().catch((e) => { console.error("测试异常: " + ((e && e.stack) || e)); process.exit(1); });
