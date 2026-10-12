// 静态回归防护：GUI 多子任务/代理展示（历史缺陷"多子任务没像 TUI 一样显示、
// 只显示一个在活动、TOK 原始垃圾行刷屏、goal 面板静态快照"）：
//   1. handleOrchestration 必须处理 TOK（只更新 LastLine，绝不 prepend 新行）；
//   2. 按 subID 聚合的结构化面板（state.subTasks Map + renderSubTaskPanel 标题计数行）；
//   3. renderGoalPanel 不得清空/写入 #agentTasks（消除双写冲突，#agentTasks 专属子任务面板）；
//   4. Agent 面板打开时 3s 轮询 goal.status（startAgentPolling/stopAgentPolling 注册清除配对）；
//   5. 未知 § 信号不得产生原始垃圾行（默认分支 return）。
// 结果写入同目录 result.txt。
const fs = require('fs');
const path = require('path');
const out = [];
const src = fs.readFileSync(path.join(__dirname, '..', 'cmd', 'contextloom-gui', 'web', 'app.js'), 'utf8');

// ── 1. handleOrchestration：TOK 分支存在且走增量更新（不新增行）──
const hoStart = src.indexOf('function handleOrchestration(data)');
if (hoStart < 0) { out.push('FAIL: handleOrchestration not found'); }
else {
  const hoEnd = src.indexOf('function scheduleStreamRender()', hoStart);
  const hoBody = src.slice(hoStart, hoEnd < 0 ? hoStart + 3000 : hoEnd);
  const tokBranch = hoBody.indexOf('kind === "TOK"');
  out.push('handleOrch.TOKIdx=' + tokBranch);
  if (tokBranch < 0) out.push('FAIL: TOK branch missing (raw token lines flood panel)');
  const tokInc = hoBody.indexOf('st.lastLine = appendLastLine(st.lastLine');
  out.push('handleOrch.TOKIncIdx=' + tokInc);
  if (tokInc < 0) out.push('FAIL: TOK does not update lastLine incrementally');
  // TOK 路径必须 structural=false（走 updateSubTaskLine 而非全量/新增行）
  // 位置约束：在 TOK 分支内部（tokBranch 之后、未知信号分支之前）
  const unknownRetIdx = hoBody.indexOf('return; // 未知信号忽略');
  const tokStructural = hoBody.indexOf('structural = false');
  out.push('handleOrch.TOKStructuralIdx=' + tokStructural);
  if (tokStructural < 0 || tokStructural <= tokBranch || (unknownRetIdx > 0 && tokStructural > unknownRetIdx)) {
    out.push('FAIL: TOK not marked as non-structural');
  }
  const prepend = hoBody.indexOf('tasks.prepend');
  out.push('handleOrch.prependIdx=' + prepend);
  if (prepend >= 0) out.push('FAIL: handleOrchestration still prepends flat rows (old design)');
  // 未知信号默认分支必须 return（不得落入垃圾行）
  const unknownRet = hoBody.indexOf('return; // 未知信号忽略');
  out.push('handleOrch.unknownReturnIdx=' + unknownRet);
  if (unknownRet < 0) out.push('FAIL: unknown signals not ignored (raw garbage rows)');
  // 事件处理按 subID 聚合进 Map
  const mapSet = hoBody.indexOf('state.subTasks.set(subID');
  const mapGet = hoBody.indexOf('state.subTasks.get(subID)');
  out.push('handleOrch.mapSet=' + mapSet + ' mapGet=' + mapGet);
  if (mapSet < 0 || mapGet < 0) out.push('FAIL: no per-subID Map aggregation');
}

// ── 2. renderSubTaskPanel：标题计数行（进行/完成/失败）──
const rspStart = src.indexOf('function renderSubTaskPanel()');
if (rspStart < 0) { out.push('FAIL: renderSubTaskPanel not found'); }
else {
  const rspBody = src.slice(rspStart, rspStart + 1200);
  const title = rspBody.indexOf('at-title');
  out.push('renderSubTask.titleIdx=' + title);
  if (title < 0) out.push('FAIL: no summary title row (running/done/failed counts)');
  const summary = rspBody.indexOf('subTaskSummary()');
  out.push('renderSubTask.summaryIdx=' + summary);
  if (summary < 0) out.push('FAIL: no subTaskSummary call');
  const subid = rspBody.indexOf('data-subid');
  out.push('renderSubTask.subidIdx=' + subid);
  if (subid < 0) out.push('FAIL: no data-subid per-row (DOM reuse broken)');
}
if (!src.includes('function subTaskSummary()')) out.push('FAIL: subTaskSummary missing');
else out.push('OK: subTaskSummary defined');
if (!src.includes('function updateSubTaskLine(subID)')) out.push('FAIL: updateSubTaskLine missing');
else out.push('OK: updateSubTaskLine defined');

// ── 3. renderGoalPanel 不得写 #agentTasks（双写冲突防线）──
const rgpStart = src.indexOf('function renderGoalPanel(g)');
if (rgpStart < 0) { out.push('FAIL: renderGoalPanel not found'); }
else {
  const rgpEnd = src.indexOf('function setAgentStatus', rgpStart);
  const rgpBody = src.slice(rgpStart, rgpEnd < 0 ? rgpStart + 2500 : rgpEnd);
  // 只匹配实际 DOM 写入（取元素），注释提及不算
  const domWrites = rgpBody.match(/\$\(\s*"#agentTasks"\)/g) || [];
  out.push('renderGoal.agentTasksDomWrites=' + domWrites.length);
  if (domWrites.length > 0) out.push('FAIL: renderGoalPanel still writes #agentTasks (double-write conflict)');
  else out.push('OK: renderGoalPanel no longer writes #agentTasks');
  const pendingSteps = rgpBody.indexOf('g.pendingSteps');
  out.push('renderGoal.pendingStepsIdx=' + pendingSteps);
  if (pendingSteps >= 0) out.push('FAIL: renderGoalPanel still renders pendingSteps into task area');
}

// ── 4. 轮询注册/清除配对（setPanel）──
if (!src.includes('function startAgentPolling()')) out.push('FAIL: startAgentPolling missing');
else out.push('OK: startAgentPolling defined');
if (!src.includes('function stopAgentPolling()')) out.push('FAIL: stopAgentPolling missing');
else out.push('OK: stopAgentPolling defined');
const setPanelStart = src.indexOf('function setPanel(panel)');
if (setPanelStart < 0) { out.push('FAIL: setPanel not found'); }
else {
  const setPanelBody = src.slice(setPanelStart, setPanelStart + 1200);
  const agentPoll = setPanelBody.indexOf('startAgentPolling()');
  const agentStop = setPanelBody.indexOf('stopAgentPolling()');
  out.push('setPanel.agentPoll=' + agentPoll + ' stop=' + agentStop);
  if (agentPoll < 0) out.push('FAIL: setPanel agent branch missing startAgentPolling');
  if (agentStop < 0) out.push('FAIL: setPanel missing stopAgentPolling (polling leaks on panel switch)');
  // 轮询间隔在 startAgentPolling 函数体内（3s refreshGoalStatus）
  const sapStart = src.indexOf('function startAgentPolling()');
  const sapBody = sapStart < 0 ? '' : src.slice(sapStart, sapStart + 400);
  const interval = sapBody.indexOf('setInterval(refreshGoalStatus, 3000)');
  out.push('agentPoll.intervalIdx=' + interval);
  if (interval < 0) out.push('FAIL: polling interval not 3s refreshGoalStatus');
}

// ── 5. 后端协议数据源不受影响（server.go § 转发仍在）──
const server = fs.readFileSync(path.join(__dirname, '..', 'internal', 'gui', 'server.go'), 'utf8');
const fwd = server.indexOf('"orchestration.progress"');
out.push('server.orchestrationFwdIdx=' + fwd);
if (fwd < 0) out.push('FAIL: server.go orchestration.progress forwarding lost');

// ── 6. 子任务为中间态：新一轮派发前清空（旧任务不得残留累积）──
if (!src.includes('function clearSubTasks()')) out.push('FAIL: clearSubTasks missing');
else out.push('OK: clearSubTasks defined');
// sendMessage 必须调用 clearSubTasks（对齐 TUI 每轮发送前 m.subTasks = nil）
const smStart = src.indexOf('async function sendMessage()');
const smBody = smStart < 0 ? '' : src.slice(smStart, smStart + 1600);
const smClear = smBody.indexOf('clearSubTasks()');
out.push('sendMessage.clearIdx=' + smClear);
if (smClear < 0) out.push('FAIL: sendMessage does not clear subtask panel (old tasks accumulate)');
// START 分支：上一批全部结束（无 running）且新 subID → 先清空再插入
const ho2Start = src.indexOf('function handleOrchestration(data)');
const ho2Body = ho2Start < 0 ? '' : src.slice(ho2Start, ho2Start + 2200);
const startClear = ho2Body.indexOf('state.subTasks.clear()');
const runCheck = ho2Body.indexOf('runningSubCount() === 0');
out.push('handleOrch.startClearIdx=' + startClear + ' runCheck=' + runCheck);
if (startClear < 0 || runCheck < 0) out.push('FAIL: START does not clear finished batch before new dispatch');
// clearSubTasks 必须重渲染面板（空态清屏）
const cstStart = src.indexOf('function clearSubTasks()');
const cstBody = cstStart < 0 ? '' : src.slice(cstStart, cstStart + 400);
const cstRender = cstBody.indexOf('renderSubTaskPanel()');
out.push('clearSubTasks.renderIdx=' + cstRender);
if (cstRender < 0) out.push('FAIL: clearSubTasks does not re-render (panel not cleared visually)');

out.push('DONE');
fs.writeFileSync(path.join(__dirname, 'result.txt'), out.join('\n'), 'utf8');
