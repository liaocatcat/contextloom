// 配置文件设置面板前端冒烟（config.get/config.set schema 驱动表单）
// 运行: node cmd/contextloom-gui/tests/settings_test.js
// vm 加载生产 app.js（stub document），直接调用 renderCfgField 验证 5 种控件
// 类型的 HTML 生成——与运行时代码 100% 一致。
"use strict";
const fs = require("fs");
const path = require("path");
const vm = require("vm");

const src = fs.readFileSync(path.join(__dirname, "..", "web", "app.js"), "utf8");
const indexHtml = fs.readFileSync(path.join(__dirname, "..", "web", "index.html"), "utf8");
// 顶层仅 DOMContentLoaded 绑定（加载时不执行），stub document 即可安全求值
const ctx = {
  document: { addEventListener: () => {} },
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
function has(name, s, sub) { eq(name, s.includes(sub), true); }

// ── renderCfgField 5 种控件类型 ──
vm.runInContext(`state.cfgValues = {
  "model.backend": "deepseek-v4",
  "model.api_key": "sk-****abcd",
  "model.max_context": 131072,
  "acp.enabled": true,
  "overseer.temp_doc_policy": "session_only",
};`, ctx);

const fText = { key: "model.backend", label: "模型后端", type: "text", group: "模型", desc: "后端模型标识" };
const htmlText = ctx.renderCfgField(fText);
has("text 控件", htmlText, `value="deepseek-v4"`);
has("text data-cfgkey", htmlText, `data-cfgkey="model.backend"`);
has("text 标签", htmlText, "模型后端");
has("text 说明", htmlText, "后端模型标识");

const fPass = { key: "model.api_key", label: "API Key", type: "password", group: "模型", desc: "脱敏" };
const htmlPass = ctx.renderCfgField(fPass);
has("password 控件", htmlPass, `type="password"`);
has("password 脱敏占位", htmlPass, "已设置 sk-****abcd");
has("password 留空提示", htmlPass, "留空不修改");

const fNum = { key: "model.max_context", label: "最大上下文", type: "number", group: "模型", desc: "token 窗口", min: 8192, max: 100000000, step: 1024 };
const htmlNum = ctx.renderCfgField(fNum);
has("number 控件", htmlNum, `type="number"`);
has("number 值", htmlNum, `value="131072"`);
has("number min", htmlNum, `min="8192"`);
has("number step", htmlNum, `step="1024"`);

const fBool = { key: "acp.enabled", label: "启用 ACP", type: "bool", group: "ACP", desc: "ACP 协议" };
const htmlBool = ctx.renderCfgField(fBool);
has("bool 控件", htmlBool, `type="checkbox"`);
has("bool 选中", htmlBool, `checked`);
eq("bool 无明文值", htmlBool.includes(`value=`), false); // checkbox 不应带 value 属性（用 checked）

const fSel = { key: "overseer.temp_doc_policy", label: "临时文档策略", type: "select", group: "监工", enums: ["session_only", "persistent"], desc: "策略" };
const htmlSel = ctx.renderCfgField(fSel);
has("select 控件", htmlSel, `<select`);
has("select 选项1", htmlSel, `session_only`);
has("select 选项2", htmlSel, `persistent`);
has("select 选中态", htmlSel, `session_only" selected`);

// ── saveConfigSettings 依赖的 diff 语义（password 空/脱敏跳过） ──
// 通过函数源码断言关键行为（避免 DOM 依赖）
has("save 调 config.set", src.slice(0, src.indexOf("function saveConfigSettings") + 4000), `req("config.set", patch)`);
has("save 密码跳过", src, `if (!s || s.includes("****")) continue;`);
has("save 无修改提示", src, `没有需要保存的修改`);
has("save 热更新提示", src, `needsRestart`);
has("save 重启生效文案", src, `重启后生效`);
// 保存后状态栏立即同步（不等 2s 轮询）：saveConfigSettings 成功后必须
// 调用 refreshStatus + refreshModelInfo——状态栏显示设置同步更新的核心
const saveSrc = src.slice(src.indexOf("function saveConfigSettings"), src.indexOf("function saveConfigSettings") + 4000);
has("save 后 refreshStatus", saveSrc, `refreshStatus()`);
has("save 后 refreshModelInfo", saveSrc, `refreshModelInfo()`);
// 2s 轮询兜底：refreshStatus 消费 status.bar 的 model 字段更新 #modelInfo
// （任何路径改设置——config.set/model.set 等——状态栏模型信息都自动同步）
const rsSrc = src.slice(src.indexOf("async function refreshStatus"), src.indexOf("async function refreshStatus") + 2200);
has("轮询消费 s.model", rsSrc, `s.model.backend`);
has("轮询更新 modelInfo", rsSrc, `#modelInfo`);
// warn toast 样式契约（保存含重启项时用 🟡 warn 提示，样式类必须存在）
const css = fs.readFileSync(path.join(__dirname, "..", "web", "style.css"), "utf8");
has("warn toast 样式", css, ".toast.warn");
has("render 分组", src, `cfg-yaml-group`);

console.log(`\n设置面板冒烟: ${pass} 通过 / ${fail} 失败`);

// ── 多模型预设下拉（v2.5.0 多模型） ──
// renderConfigSettings 在"模型"分组顶部渲染预设下拉；切换调 model.switch
const rcs = src.slice(src.indexOf("function renderConfigSettings"), src.indexOf("function renderConfigSettings") + 4500);
has("预设下拉渲染", rcs, `cfg-preset-select`);
has("预设切换按钮", rcs, `cfgModelSwitch`);
has("预设切换调 model.switch", rcs, `req("model.switch"`);
has("切换成功 refreshStatus", rcs, `refreshStatus()`);
has("切换后刷新表单", rcs, `loadConfigSettings()`);
has("切换失败 toast", rcs, `切换失败`);
has("下拉 change 即切换", rcs, `sel.onchange`);
has("切换函数 switchModelPreset", rcs, `switchModelPreset`);
const lcs = src.slice(src.indexOf("async function loadConfigSettings"), src.indexOf("async function loadConfigSettings") + 1600);
has("加载预设 model.list", lcs, `req("model.list"`);
const css2 = fs.readFileSync(path.join(__dirname, "..", "web", "style.css"), "utf8");
has("预设下拉样式", css2, ".cfg-preset-select");

// ── 单一模型设置面板（v2.5.x 重构）：GUI 只显示一个模型面板 ──
// 信息卡区块（cfgModelBody）与 renderConfigModel/refreshModelCard/effortSel 已彻底删除，
// 唯一模型设置面板 = 配置文件设置表单"模型"分组（预设下拉 + 该模型参数字段）
const all = src + indexHtml;
eq("无信息卡容器 cfgModelBody", all.includes("cfgModelBody"), false);
eq("无 effortSel 下拉", all.includes("effortSel"), false);
eq("无 renderConfigModel 函数", src.includes("function renderConfigModel"), false);
eq("无 refreshModelCard 函数", src.includes("function refreshModelCard"), false);
const selCount = (src.match(/<select id="cfgModelPreset"/g) || []).length;
eq("唯一模型下拉 = cfgModelPreset", selCount === 1, true);
// 模型参数字段与预设下拉同属"模型"分组（选择哪模型就带出哪模型参数）
const modelGrp = src.slice(src.indexOf(`g.name === "模型"`), src.indexOf(`g.name === "模型"`) + 1200);
has("模型分组含预设下拉", modelGrp, `cfgModelPreset`);
has("模型分组含参数字段渲染", modelGrp, `renderCfgField`);
eq("index 无 cfgModel 区块", indexHtml.includes(`id="cfgModel"`), false);

// ── 运行时分组去重回归（idx 值 0 的 falsy 陷阱，本轮根因）──
// 修复前 renderConfigSettings 用 `if (!idx[f.group])` 判断分组是否存在：
// "模型"是 schema 第一个分组，idx["模型"]=0（falsy）→ 第二个"模型"字段又
// push 一个新分组 → 界面渲染出两个"模型预设"下拉。修复后必须只有一个。
let capturedCfgHTML = "";
const fakeCfgForm = {
  _html: "",
  set innerHTML(v) { this._html = v; },
  get innerHTML() { return this._html; },
  querySelector: () => null,
  querySelectorAll: () => [],
};
ctx.document.querySelector = (sel) => {
  if (sel === "#cfgYamlForm") return fakeCfgForm;
  if (sel === "#cfgYamlPath") return { textContent: "" };
  if (sel === "#cfgYamlHint") return { textContent: "" };
  return null;
};
vm.runInContext(`
state.cfgSchema = [
  { key: "model.endpoint", label: "API 端点", type: "text", group: "模型", desc: "x" },
  { key: "model.api_key", label: "API Key", type: "password", group: "模型", desc: "x" },
  { key: "model.max_context", label: "最大上下文", type: "number", group: "模型", desc: "x" },
  { key: "project.root", label: "项目根目录", type: "text", group: "项目", desc: "x" },
];
state.modelPresets = [{ name: "deepseek-v4" }, { name: "gpt-4o" }];
state.modelCurrent = "deepseek-v4";
state.cfgValues = {};
renderConfigSettings();
`, ctx);
const runtimePresetCount = (fakeCfgForm._html.match(/<select id="cfgModelPreset"/g) || []).length;
eq("运行时：第一个分组'模型'只渲染一个模型预设下拉", runtimePresetCount, 1);
const runtimeModelGroupCount = (fakeCfgForm._html.match(/cfg-yaml-gname">模型/g) || []).length;
eq("运行时：'模型'分组标题只出现一次", runtimeModelGroupCount, 1);

console.log(`\n设置面板冒烟: ${pass} 通过 / ${fail} 失败`);
process.exit(fail ? 1 : 0);
