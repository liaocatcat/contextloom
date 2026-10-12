// 功能级回归测试：stub DOM 驱动 handleOrchestration/renderSubTaskPanel，
// 验证「多子任务按 subID 聚合显示、TOK 不新增行、计数正确、未知信号无垃圾行」。
// 从 app.js 提取函数源码执行（静态文件不依赖浏览器）。
const fs = require('fs');
const path = require('path');
const src = fs.readFileSync(path.join(__dirname, '..', 'cmd', 'contextloom-gui', 'web', 'app.js'), 'utf8');
const out = [];
let fails = 0;

// ── 最小 DOM stub（支持 innerHTML 写入解析 → children/data-subid/.at-last）──
function unescHtml(s) {
  return String(s).replace(/&gt;/g, '>').replace(/&lt;/g, '<').replace(/&quot;/g, '"').replace(/&amp;/g, '&');
}
function makeEl(tag, cls) {
  const el = {
    tag, className: cls || '', children: [], dataset: {},
    style: {},
    _html: '',
    set innerHTML(v) {
      this._html = String(v);
      this.children = [];
      const re = /<div class="([^"]*)"(?: data-subid="([^"]*)")?>([\s\S]*?)<\/div>/g;
      let m;
      while ((m = re.exec(this._html))) {
        const c = makeEl('div', m[1]);
        if (m[2]) c.dataset.subid = m[2];
        c._html = m[3] || '';
        const lm = /<span class="at-last">([\s\S]*?)<\/span>/.exec(c._html);
        if (lm) c._last = { textContent: unescHtml(lm[1]) };
        this.children.push(c);
      }
    },
    get innerHTML() { return this._html; },
    appendChild(c) {
      this.children.push(c);
      if (c.className === 'at-last') this._last = c; // 真实 DOM 中 append 后可被 querySelector 找到
      return c;
    },
    prepend(c) { this.children.unshift(c); },
    removeChild(c) { const i = this.children.indexOf(c); if (i >= 0) this.children.splice(i, 1); },
    querySelector(sel) { return sel === '.at-last' ? (this._last || null) : null; },
    getBoundingClientRect() { return { height: 100 }; },
  };
  return el;
}
const agentTasksEl = makeEl('div', '');
const els = { agentTasks: agentTasksEl };
function $(sel) {
  if (sel === '#agentTasks') return agentTasksEl;
  return null;
}
function esc(s) { return String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'); }

// ── 提取函数：从 handleOrchestration 到下一个顶层函数 ──
function extract(fnName) {
  const start = src.indexOf('function ' + fnName + '(');
  if (start < 0) throw new Error('function not found: ' + fnName);
  let depth = 0, i = src.indexOf('{', start), end = -1;
  for (; i < src.length; i++) {
    if (src[i] === '{') depth++;
    else if (src[i] === '}') { depth--; if (depth === 0) { end = i + 1; break; } }
  }
  return src.slice(start, end);
}
const funcs = [
  'handleOrchestration', 'appendLastLine', 'runningSubCount', 'subTaskSummary',
  'renderSubTaskPanel', 'updateSubTaskLine', 'clearSubTasks',
].map(extract).join('\n');

const state = {
  subTasks: new Map(),
  agentBusyTimer: 0,
  agentTimer: 0,
};
const setTimeoutFn = setTimeout;
const clearTimeoutFn = clearTimeout;

// 沙箱执行（不依赖浏览器 API）
const sandbox = { state, $, esc, document: { createElement: makeEl } };
const setAgentStatusStub = () => {};
const fn = new Function('state', '$', 'esc', 'document', 'setTimeout', 'clearTimeout', 'setAgentStatus', funcs + '\nreturn {handleOrchestration, renderSubTaskPanel, subTaskSummary, runningSubCount, appendLastLine, clearSubTasks};');
const api = fn(state, $, esc, { createElement: makeEl }, setTimeoutFn, clearTimeoutFn, setAgentStatusStub);

function rowText(row) {
  // 简化：由 innerHTML 直接看文本
  return row.innerHTML;
}

// ── 场景 1：多子任务并行 START + TOK ──
api.handleOrchestration({ msg: '§START|sub-1|CODE_AGENT|任务A：实现登录模块' });
api.handleOrchestration({ msg: '§START|sub-2|TEST_AGENT|任务B：编写单元测试' });
api.handleOrchestration({ msg: '§START|sub-3|REFACTOR|任务C：重构缓存层' });
out.push('S1 rows after 3 START = ' + agentTasksEl.children.length);
if (agentTasksEl.children.length !== 4) { fails++; out.push('FAIL: expect 4 rows (1 title + 3 subtasks), got ' + agentTasksEl.children.length); }
const titleRow = agentTasksEl.children[0].innerHTML;
out.push('S1 title = ' + titleRow);
if (!titleRow.includes('3 进行') || !titleRow.includes('0 完成') || !titleRow.includes('0 失败')) { fails++; out.push('FAIL: title counts wrong'); }

// ── 场景 2：TOK 只更新 lastLine，不新增行 ──
api.handleOrchestration({ msg: '§TOK|sub-1|登录' });
api.handleOrchestration({ msg: '§TOK|sub-1|模块' });
api.handleOrchestration({ msg: '§TOK|sub-2|测试' });
api.handleOrchestration({ msg: '§TOK|sub-2|用例' });
out.push('S2 rows after 4 TOK = ' + agentTasksEl.children.length);
if (agentTasksEl.children.length !== 4) { fails++; out.push('FAIL: TOK added rows (should stay 4)'); }
// sub-1 行 lastLine 累积
const sub1Row = agentTasksEl.children.find(c => c.dataset && c.dataset.subid === 'sub-1');
if (!sub1Row) { fails++; out.push('FAIL: sub-1 row missing'); }
else {
  const last = sub1Row.querySelector('.at-last');
  out.push('S2 sub-1 last = ' + (last ? last.textContent : 'NULL'));
  if (!last || !last.textContent.includes('登录模块')) { fails++; out.push('FAIL: sub-1 lastLine not accumulated (TOK incremental broken)'); }
}

// ── 场景 3：DONE/FAIL 状态与计数 ──
api.handleOrchestration({ msg: '§DONE|sub-1|95' });
api.handleOrchestration({ msg: '§FAIL|sub-3|超时' });
api.handleOrchestration({ msg: '§DONE|sub-2|88' });
out.push('S3 title = ' + api.subTaskSummary());
if (!api.subTaskSummary().includes('0 进行') || !api.subTaskSummary().includes('2 完成') || !api.subTaskSummary().includes('1 失败')) { fails++; out.push('FAIL: final counts wrong'); }
const st1 = state.subTasks.get('sub-1');
if (!st1 || st1.status !== 'done' || st1.score !== '95') { fails++; out.push('FAIL: sub-1 DONE state/score wrong'); }
const st3 = state.subTasks.get('sub-3');
if (!st3 || st3.status !== 'failed' || !st3.err.includes('超时')) { fails++; out.push('FAIL: sub-3 FAIL state/err wrong'); }

// ── 场景 4：未知信号不产生行（旧根因 A 的回归防线） ──
const before = agentTasksEl.children.length;
api.handleOrchestration({ msg: '§FOO|sub-1|未知' });
api.handleOrchestration({ msg: '普通文本（非 § 信号）' });
out.push('S4 rows after unknown = ' + agentTasksEl.children.length);
if (agentTasksEl.children.length !== before) { fails++; out.push('FAIL: unknown signals produced garbage rows'); }

// ── 场景 5：TOK 先于 START（竞态占位） ──
const st0 = state.subTasks.size;
api.handleOrchestration({ msg: '§TOK|sub-9|竞态' });
if (state.subTasks.size !== st0 + 1) { fails++; out.push('FAIL: TOK-before-START not creating placeholder'); }
else {
  const st9 = state.subTasks.get('sub-9');
  out.push('S5 placeholder lastLine = ' + st9.lastLine);
  if (st9.status !== 'running' || !st9.lastLine.includes('竞态')) { fails++; out.push('FAIL: placeholder state wrong'); }
}

// ── 场景 6：上限 100 保护 ──
for (let i = 0; i < 120; i++) api.handleOrchestration({ msg: '§START|bulk-' + i + '|CODE_AGENT|批量' });
out.push('S6 size after 120 bulk = ' + state.subTasks.size);
if (state.subTasks.size > 100) { fails++; out.push('FAIL: subTasks unbounded'); }

// ── 场景 7：renderGoalPanel 不写 #agentTasks（提取并检查无 DOM 写入） ──
const rgpStart = src.indexOf('function renderGoalPanel(g)');
const rgpBody = src.slice(rgpStart, src.indexOf('function setAgentStatus', rgpStart));
const domWrites = (rgpBody.match(/\$\("#agentTasks"\)/g) || []).length;
out.push('S7 renderGoalPanel #agentTasks DOM writes = ' + domWrites);
if (domWrites > 0) { fails++; out.push('FAIL: renderGoalPanel still writes #agentTasks'); }

// ── 场景 8：轮询配对 ──
const spStart = src.indexOf('function setPanel(panel)');
const spBody = src.slice(spStart, spStart + 1200);
out.push('S8 setPanel agentPoll=' + spBody.includes('startAgentPolling()') + ' stop=' + spBody.includes('stopAgentPolling()'));
if (!spBody.includes('startAgentPolling()') || !spBody.includes('stopAgentPolling()')) { fails++; out.push('FAIL: polling register/clear pairing missing'); }

// ── 场景 9：子任务为中间态——新一轮派发前清空旧任务（不残留累积） ──
// 先清空场景 6 遗留的 bulk 任务（clearSubTasks 本身也是被测点）
api.clearSubTasks();
out.push('S9 after clear size=' + state.subTasks.size + ' domRows=' + agentTasksEl.children.length);
if (state.subTasks.size !== 0) { fails++; out.push('FAIL: clearSubTasks did not clear Map'); }
if (agentTasksEl.children.length !== 0) { fails++; out.push('FAIL: clearSubTasks did not clear DOM'); }
// 上一批：2 个任务全部完成
api.handleOrchestration({ msg: '§START|old-1|CODE_AGENT|旧任务1' });
api.handleOrchestration({ msg: '§START|old-2|CODE_AGENT|旧任务2' });
api.handleOrchestration({ msg: '§DONE|old-1|90' });
api.handleOrchestration({ msg: '§DONE|old-2|85' });
// 新一批派发（上一批全 done、无 running）→ 应清空旧任务只显示新任务
api.handleOrchestration({ msg: '§START|new-1|CODE_AGENT|新任务' });
out.push('S9 size after new batch START=' + state.subTasks.size + ' hasOld=' + state.subTasks.has('old-1') + ' hasNew=' + state.subTasks.has('new-1'));
if (state.subTasks.size !== 1 || state.subTasks.has('old-1') || state.subTasks.has('old-2')) { fails++; out.push('FAIL: finished batch not cleared before new dispatch (accumulation)'); }
if (!state.subTasks.has('new-1')) { fails++; out.push('FAIL: new task missing after clear'); }
// DOM 也应只剩 标题 + new-1 两行
out.push('S9 domRows after new START=' + agentTasksEl.children.length);
if (agentTasksEl.children.length !== 2) { fails++; out.push('FAIL: DOM rows not refreshed (title + new task only)'); }

// ── 场景 10：并行批次不清空（有 running 时新 START 追加，不误伤并行） ──
api.clearSubTasks();
api.handleOrchestration({ msg: '§START|p-1|CODE_AGENT|并行1' });
api.handleOrchestration({ msg: '§START|p-2|CODE_AGENT|并行2' }); // p-1 仍在 running → 不清空
out.push('S10 size=' + state.subTasks.size + ' hasP1=' + state.subTasks.has('p-1') + ' hasP2=' + state.subTasks.has('p-2'));
if (state.subTasks.size !== 2 || !state.subTasks.has('p-1') || !state.subTasks.has('p-2')) { fails++; out.push('FAIL: parallel START cleared running batch (must not)'); }

out.push(fails === 0 ? 'ALL PASS' : 'FAILS=' + fails);
fs.writeFileSync(path.join(__dirname, 'result_func.txt'), out.join('\n'), 'utf8');
process.exit(fails === 0 ? 0 : 1);
