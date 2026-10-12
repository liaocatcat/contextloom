// 静态回归防护：会话切换相关破坏性约束（历史缺陷"点击当前会话清空重置、
// 只能看到新生成的"三层防线）：
//   1. switchSession 对点击当前会话短路；
//   2. openChatTab 双保险短路（不触发 loadSession 重绘）；
//   3. renderMessages 跳过 partial（在途内容由流式恢复接管）；
//   4. beginStream 复用 data-partial 气泡（刷新/历史恢复防重复气泡）。
// 结果写入同目录 result.txt。
const fs = require('fs');
const path = require('path');
const out = [];
const src = fs.readFileSync(path.join(__dirname, '..', 'cmd', 'contextloom-gui', 'web', 'app.js'), 'utf8');

// ── 1. switchSession 短路（必须位于清流/loadSession 之前）──
const start = src.indexOf('async function switchSession(uuid)');
if (start < 0) { out.push('FAIL: switchSession not found'); }
else {
  const body = src.slice(start);
  const guardIdx = body.indexOf('if (uuid === state.sessionId) return true;');
  out.push('switchSession.guardIdx=' + guardIdx);
  if (guardIdx < 0) { out.push('FAIL: switchSession guard missing'); }
  else {
    const clearIdx = body.indexOf('state.streamBuf = ""');
    const loadIdx = body.indexOf('req("chat.loadSession"');
    out.push('switchSession.clearIdx=' + clearIdx + ' loadIdx=' + loadIdx);
    if (clearIdx >= 0 && guardIdx > clearIdx) out.push('FAIL: switchSession guard after clear');
    if (loadIdx >= 0 && guardIdx > loadIdx) out.push('FAIL: switchSession guard after loadSession');
    const backupLine = body.split('\n').find(l => l.includes('state.backgroundStreams[cur]'));
    out.push('switchSession.backup=' + (backupLine ? backupLine.trim() : 'NULL'));
    if (!backupLine || backupLine.includes('cur !== uuid')) out.push('FAIL: switchSession backup condition bad');
  }
}

// ── 2. openChatTab 双保险短路（点击当前会话不触发 loadSession）──
//    注意：短路条件是 `uuid===sessionId && exist`（0a4af0c 引入 exist 约束，
//    搜索串必须带 `&& exist`——只搜 `sessionId)` 会误匹配 switchSession）。
const octStart = src.indexOf('async function openChatTab(uuid)');
if (octStart < 0) { out.push('FAIL: openChatTab not found'); }
else {
  const octBody = src.slice(octStart);
  const octGuard = octBody.indexOf('if (uuid === state.sessionId && exist)');
  const octSwitch = octBody.indexOf('switchSession(uuid)');
  out.push('openChatTab.guardIdx=' + octGuard + ' switchIdx=' + octSwitch);
  if (octGuard < 0) out.push('FAIL: openChatTab guard missing');
  else if (octSwitch >= 0 && octGuard > octSwitch) out.push('FAIL: openChatTab guard after switchSession');
  // 短路分支必须 return true（绝不重绘）
  const guardLine = octBody.split('\n').find(l => l.includes('if (uuid === state.sessionId && exist)'));
  out.push('openChatTab.guardLine=' + (guardLine ? guardLine.trim() : 'NULL'));
}

// ── 3. renderMessages 跳过 partial（有在途流时由流式恢复接管）──
const rmStart = src.indexOf('function renderMessages(msgs)');
if (rmStart < 0) { out.push('FAIL: renderMessages not found'); }
else {
  const rmBody = src.slice(rmStart);
  const partialSkip = rmBody.indexOf('m.partial && (state.streaming || state.backgroundStreams[state.sessionId])');
  out.push('renderMessages.partialSkipIdx=' + partialSkip);
  if (partialSkip < 0) out.push('FAIL: renderMessages partial skip missing');
  const partialArg = rmBody.indexOf('appendMessage(m.role, m.content, false, m.partial)');
  out.push('renderMessages.partialArgIdx=' + partialArg);
  if (partialArg < 0) out.push('FAIL: renderMessages partial arg missing');
}

// ── 4. beginStream 复用 data-partial 气泡（防重复气泡）──
const bsStart = src.indexOf('function beginStream()');
if (bsStart < 0) { out.push('FAIL: beginStream not found'); }
else {
  const bsBody = src.slice(bsStart);
  const reuse = bsBody.indexOf('data-partial');
  out.push('beginStream.reuseIdx=' + reuse);
  if (reuse < 0) out.push('FAIL: beginStream partial reuse missing');
  // 复用分支必须先于 appendMessage 新建分支
  const reuseLine = bsBody.split('\n').find(l => l.includes('existing'));
  out.push('beginStream.reuseLine=' + (reuseLine ? reuseLine.trim() : 'NULL'));
}


// ── 5. 删除最后一个会话 → 进入空状态（不再自动新建新会话）──
//    后端 DeleteSession 已允许删除当前会话（runtime.go），前端据此
//    deleteSessionPrompt 不得再预创建/兜底 newSession。
const dspStart = src.indexOf('async function deleteSessionPrompt');
if (dspStart < 0) { out.push('FAIL: deleteSessionPrompt not found'); }
else {
  const dspEnd = src.indexOf('/* ═══════════ 多 Tab ═══════════ */', dspStart);
  const dspBody = src.slice(dspStart, dspEnd);
  if (dspBody.includes('newSession()')) out.push('FAIL: deleteSessionPrompt still calls newSession()');
  else out.push('OK: deleteSessionPrompt no newSession()');
}
if (!src.includes('function enterEmptyState()')) out.push('FAIL: enterEmptyState missing');
else out.push('OK: enterEmptyState defined');
const delBranch = src.indexOf('else enterEmptyState();');
out.push('delete.enterEmptyStateIdx=' + delBranch);
if (delBranch < 0) out.push('FAIL: delete last session does not enterEmptyState');

// ── 6. 首次对话自动标题：chat.send 后刷新会话列表 ──
const smStart = src.indexOf('async function sendMessage()');
if (smStart < 0) { out.push('FAIL: sendMessage not found'); }
else {
  const smBody = src.slice(smStart);
  const sendReqIdx = smBody.indexOf('req("chat.send"');
  const reloadIdx = smBody.indexOf('lastSessionsSig = "";');
  out.push('sendMessage.sendIdx=' + sendReqIdx + ' reloadIdx=' + reloadIdx);
  if (sendReqIdx < 0 || reloadIdx < 0 || reloadIdx < sendReqIdx) {
    out.push('FAIL: sendMessage missing loadSessions after send (auto-title refresh)');
  } else {
    out.push('OK: sendMessage reloads sessions after send');
  }
}

// ── 7. newSession 不得在 openChatTab 之前设置 sessionId（新建按钮失效防线）──
//     openChatTab/switchSession 对"uuid===当前会话"短路，提前设置 sessionId
//     会把新会话短路跳过 → tab 不创建、消息区不加载（"新建会话按钮失效"）。
const nsStart = src.indexOf('async function newSession()');
if (nsStart < 0) { out.push('FAIL: newSession not found'); }
else {
  const nsEnd = src.indexOf('async function restoreCurrentSession()', nsStart);
  const nsBody = src.slice(nsStart, nsEnd < 0 ? nsStart + 800 : nsEnd);
  const sidSet = nsBody.indexOf('state.sessionId = r.uuid');
  const octCall = nsBody.indexOf('openChatTab(r.uuid)');
  out.push('newSession.sidSetIdx=' + sidSet + ' openChatTabIdx=' + octCall);
  if (sidSet < 0) out.push('FAIL: newSession missing sessionId set');
  else if (octCall < 0) out.push('FAIL: newSession missing openChatTab');
  else if (sidSet < octCall) out.push('FAIL: newSession sets sessionId BEFORE openChatTab (new button breaks)');
  else out.push('OK: newSession sets sessionId after openChatTab');
}

// ── 8. openChatTab 短路必须要求 tab 已存在（exist）──
//     短路条件 `uuid===sessionId && exist`：新建会话（不在 tabs）不得被短路
//     跳过，即使调用方提前设置 sessionId（防再犯的破坏性约束）。
//     注意：octBody 是上方 else 块内的块级 const，此处必须重新切片
//     （历史 bug：直接引用 octBody 抛 ReferenceError，断言 7/8 从未生效）。
const octStart2 = src.indexOf('async function openChatTab(uuid)');
const octBody2 = octStart2 < 0 ? '' : src.slice(octStart2);
const octGuardLine2 = octBody2.split('\n').find(l => l.includes('uuid === state.sessionId && exist'));
out.push('openChatTab.guardHasExist=' + (octGuardLine2 ? 'YES' : 'NO'));
if (!octGuardLine2) out.push('FAIL: openChatTab guard lacks exist check (new session skipped)');
else out.push('OK: openChatTab guard requires tab exist');

out.push('DONE');
fs.writeFileSync(path.join(__dirname, 'result.txt'), out.join('\n'), 'utf8');
