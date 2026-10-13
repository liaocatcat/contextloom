// /think 状态栏后缀前端冒烟（多轮思考策略 UI 面，设计 §3.8）
// 运行: node cmd/contextloom-gui/tests/think_ui_test.js
// vm 加载生产 app.js（stub document），直接调用 modelInfoSuffix/thinkingSuffix——
// 与运行时代码 100% 一致。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");

const ctx = {
  document: {
    addEventListener: () => {},
    querySelector: () => null,
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

// ── thinkingSuffix / modelInfoSuffix（零回归：缺失/off 不渲染）──
console.log("── thinkingSuffix / modelInfoSuffix（多轮思考策略）──");
eq("字段缺失 → 无后缀（零回归）", ctx.modelInfoSuffix({ effort: "max", maxOutputTokens: 0, thinkingEnabled: true }), "");
eq("thinking=null → 无后缀", ctx.modelInfoSuffix({ effort: "max", thinking: null }), "");
eq("policy=off → 不渲染（零回归）", ctx.modelInfoSuffix({ effort: "max", thinking: { policy: "off", round: 2, maxRound: 3 } }), "");
eq("strict 带轮次", ctx.modelInfoSuffix({ effort: "max", thinking: { policy: "strict", round: 2, maxRound: 3 } }), " · 思考:strict 轮2/3");
eq("auto 无轮次 → 仅策略", ctx.modelInfoSuffix({ effort: "max", thinking: { policy: "auto", round: 0, maxRound: 0 } }), " · 思考:auto");
eq("验证中追加", ctx.modelInfoSuffix({ effort: "max", thinking: { policy: "strict", round: 2, maxRound: 3, verifying: true } }), " · 思考:strict 轮2/3·验证中");
eq("与输出上限组合", ctx.modelInfoSuffix({ effort: "max", maxOutputTokens: 64000, thinking: { policy: "strict", round: 1, maxRound: 3 } }), " · 输出64k · 思考:strict 轮1/3");
eq("与思考关组合（正交）", ctx.modelInfoSuffix({ effort: "off", maxOutputTokens: 0, thinkingEnabled: false, thinking: { policy: "strict", round: 2, maxRound: 3 } }), " · 思考关 · 思考:strict 轮2/3");
eq("thinkingSuffix 非对象安全", ctx.thinkingSuffix("x"), "");
eq("thinkingSuffix 空对象", ctx.thinkingSuffix({}), "");
eq("thinkingSuffix 有轮次无上限 → 轮N", ctx.thinkingSuffix({ policy: "strict", round: 2 }), " · 思考:strict 轮2");
eq("null 安全", ctx.modelInfoSuffix(null), "");

// ── 源码接线断言 ──
console.log("── 源码接线 ──");
has("modelInfoSuffix 调用 thinkingSuffix", src, "s += thinkingSuffix(m && m.thinking);");
has("thinkingSuffix 定义存在", src, "function thinkingSuffix(th) {");
has("status.bar 状态栏渲染经 modelInfoSuffix", src, "modelInfoSuffix(s.model)");
has("model.info 渲染经 modelInfoSuffix", src, "modelInfoSuffix(m)");

console.log(`\nthink UI 冒烟: ${pass} 通过 / ${fail} 失败`);
process.exit(fail ? 1 : 0);
