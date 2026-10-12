// GUI 状态栏 ctx 同步前端回归（状态栏"没有同步改变"修复）
// 运行: node cmd/contextloom-gui/tests/ctx_status_test.js
// 覆盖：小数百分比解析（applyCtxString）、流式事件应用（applyCtxUsage）、
// 精度显示（setCtxUI/fmtTokens）——旧实现 int 截断 + /1000 截断导致
// 131k 预算下几百 token 增量显示为 0%，状态栏纹丝不动。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

// 假元素：setCtxUI 需要 style/className/textContent；querySelector 按 id 返回
function fakeEl(id) {
  return {
    id,
    style: {},
    className: "",
    textContent: "",
    offsetHeight: 0,
    addEventListener: () => {},
    appendChild: () => {},
  };
}
const els = {
  ctxFill: fakeEl("ctxFill"),
  ctxNum: fakeEl("ctxNum"),
  ctxNum2: fakeEl("ctxNum2"),
  ctxDetail: fakeEl("ctxDetail"),
};
const ctx = {
  document: {
    addEventListener: () => {},
    querySelector: (sel) => {
      const id = sel.replace(/^#/, "");
      return els[id] || null;
    },
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

console.log("── fmtTokens 精度（<1000 真实数值、<10000 1位小数 k、否则整数 k）──");
eq("800 → 800", ctx.fmtTokens(800), "800");
eq("12500 → 12k（截断语义，与后端一致）", ctx.fmtTokens(12500), "12k");
eq("9600 → 9.6k（<10000 1位小数）", ctx.fmtTokens(9600), "9.6k");
eq("131072 → 131k", ctx.fmtTokens(131072), "131k");
eq("0 → 0", ctx.fmtTokens(0), "0");

console.log("── applyCtxString 小数百分比解析（后端新格式）──");
ctx.applyCtxString("ctx 0.6% (800/131k)");
eq("pct 0.6 显示小数", els.ctxNum.textContent, "0.6%");
eq("进度条 0.6", els.ctxFill.style.width, "0.6%");
eq("detail 提取括号内 token 明细", els.ctxDetail.textContent, "(800/131k)");
ctx.applyCtxString("ctx 42.0% (55k/131k)");
eq("42.0% → 42% 显示整数", els.ctxNum.textContent, "42%");
eq("进度条 42", els.ctxFill.style.width, "42%");
eq("detail 55k/131k", els.ctxDetail.textContent, "(55k/131k)");
ctx.applyCtxString("");
eq("空串不更新（保持 42%）", els.ctxNum.textContent, "42%");

console.log("── applyCtxUsage 流式事件（后端 ctx.usage 推送）──");
ctx.applyCtxUsage({ used: 800, max: 131072, streaming: 200, action: "" });
eq("used 800 → 0.6%", els.ctxNum.textContent, "0.6%");
eq("detail 800/131k", els.ctxDetail.textContent, "(800/131k)");
ctx.applyCtxUsage({ used: 15000, max: 131072, streaming: 0, action: "" });
eq("used 15000 → 11.4%", els.ctxNum.textContent, "11%");
eq("detail 15k/131k", els.ctxDetail.textContent, "(15k/131k)");
ctx.applyCtxUsage(null);
eq("null 不更新", els.ctxNum.textContent, "11%");
ctx.applyCtxUsage({ used: 0, max: 0 });
eq("max=0 不更新（防除零）", els.ctxNum.textContent, "11%");

console.log("── setCtxUI 边界（钳制 0~99）──");
ctx.setCtxUI(-5, "x");
eq("负值钳制 0", els.ctxNum.textContent, "0.0%");
ctx.setCtxUI(105, "x");
eq("超 99 钳制", els.ctxNum.textContent, "99%");
ctx.setCtxUI(95, "x");
eq("95% danger 类", els.ctxFill.className.includes("danger"), true);
ctx.setCtxUI(75, "x");
eq("75% warn 类", els.ctxFill.className.includes("warn"), true);
ctx.setCtxUI(5, "x");
eq("5% 无危险类", els.ctxFill.className.includes("danger") || els.ctxFill.className.includes("warn"), false);

console.log(`\n结果: ${pass} passed, ${fail} failed`);
process.exit(fail ? 1 : 0);
