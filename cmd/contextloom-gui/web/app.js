/* ============================================================
   ContextLoom GUI v2 — 交互逻辑（契约 CONTRACT.md 第一/二/四节）
   对标业界主流桌面编码 Agent 的 EUI：多 Tab / 文件树 / Observer / 配置 / 日志
   ============================================================ */
"use strict";

const $ = (s) => document.querySelector(s);
const $$ = (s) => Array.from(document.querySelectorAll(s));

// 回到底部按钮：位于 #msgWrap 内部（对话窗口），锚点是 msgWrap——
// msgWrap 底部即对话区底部（下方是 chatResizer 分隔条），固定 bottom 12px
// 即可，不再需要避让输入区（旧布局 scrollDown 锚定 chatArea 需避让
// inputZone 高度；布局重构后 scrollDown 随对话窗口一起缩放）。
function placeScrollDown() {
  const btn = $("#scrollDown");
  if (!btn) return;
  btn.style.bottom = "12px";
}

const state = {
  ws: null,
  reqId: 0,
  // pending 兜底：多连接下请求挂活跃连接的 conn.pending（req 用 activeConn()），
  // 此全局 Map 仅在无活跃连接（启动极早期）时回退使用，正常路径为空。
  pending: new Map(),
  // 多项目并行通道（用户诉求"高并发多线程并行，不同项目不同对话互不干扰"）：
  // conns: dir → {dir, ws, gen, pending, reconnectCount, lastActive, activeStreams, closed}
  // 每个打开过的项目各保持一条独立 WS 连接；state.ws 始终指向【当前活跃项目】
  // 的连接（所有既有读取处兼容）。切换项目【不关闭】旧项目连接——其 backend
  // 在途流继续经该连接推送（非活跃连接事件只累积后台缓冲，不渲染，切回即见）。
  // 空闲连接（非活跃 + 无在途流 + 10 分钟无活动）由 sweepIdleConns 自动断开。
  conns: {},
  activeDir: "",       // 当前活跃项目目录（conns 键）
  connSweeper: 0,      // 空闲连接扫描定时器（startConnSweeper 每 30s 巡检）
  sessionId: "",
  sessions: [],
  mode: "chat",
  streaming: false,
  streamBuf: "",
  streamMsg: null,
  streamSessionId: null, // 当前进行中流所属会话（跨会话事件过滤）
  streamId: null,        // 当前前台流唯一标识（后端 streamId=uid）：区分同会话的新旧流，
                         // 旧流延迟事件按此丢弃，杜绝"停下的对话被别的流再次触发"
  stoppedStreams: new Map(), // streamId → 所属会话：已被用户显式停止的流，其残留事件一律丢弃（防重触发）
  stoppedSessions: new Map(), // sessionId → true：停止请求在途（尚未收到该会话的 done），期间该会话事件一律丢弃
  chatCache: {},         // sessionId → {len, scrollTop}（切回时恢复滚动位置）
  sessionSeq: 0,         // 会话切换代际序号：并发 loadSession 时旧请求放弃渲染
  backgroundStreams: {}, // sid -> {buf,streaming}: 非当前会话后台流缓冲（多对话并行，切回恢复渲染）
  subTasks: new Map(),   // subID → {id,directive,employee,task,status,score,lastLine,err}：§ 事件按 subID 聚合（与 TUI handleSubTaskSignal 同构）
  subTasksByDir: {},     // dir → Map：每个项目独立的子任务视图（后台连接聚合到该项目视图，切回恢复显示，跨项目互不干扰）
  agentTimer: 0,         // Agent 面板 goal.status 轮询 timer（3s，打开面板注册/切换清除）
  agentBusyTimer: 0,     // 状态栏 busy 文案延迟清除 timer
  // 配置文件设置面板（schema 驱动）
  cfgValues: {},   // key → 当前值（脱敏）
  cfgSchema: [],   // FieldSchema 清单
  cfgPath: "",     // config.yaml 路径
  lastThink: null, // 保留字段（历史兼容），展开状态恢复改用文本指纹
  cmdPanel: null,
  cmdSel: 0,
  // ── 文件编辑器状态（仿业界主流桌面编码 Agent） ──
  feInit: false,          // 编辑器事件是否已初始化
  fileViews: {},          // path → view 记忆（split/edit/prev）
  feCharWCache: 0,        // 等宽字符宽度缓存
  feAnchors: [],          // 预览标题锚点 [{level,title,line}]
  fePrevTimer: null,      // 预览刷新 debounce
  feSyncing: false,       // 同步滚动防回环标志（次要防线）
  feExpect: null,         // 程序设置滚动位置的期望值（防双向同步回环主防线）
  feGutterFull: true,     // gutter 全量渲染模式
  findQ: "",              // 查找词
  findMatches: [],        // [{start,end}]
  findIdx: 0,             // 当前匹配索引
  userScrolledUp: false,
  rafPending: false,
  lastStreamRenderT: 0,
  sr: null, // 流式增量渲染状态 {parts}（与 streamMsg 同生命周期；null=下帧全量重建）
  reconnectCount: 0,
  statusTimer: 0,
  // v2
  // 右侧 tab：chat tab 带 projDir（所属项目）。切换项目【不清空】——旧项目
  // 的会话 tab 留在栏上（标记为"其他项目"，点击切回），目标项目的 tab 追加
  // 进来。这是用户明确要求："切换/添加项目时，右侧旧项目的会话tab要保留在界面上"。
  tabs: [],            // {kind:"chat"|"file", id, title, projDir, dirty, content, origContent}
  pendingRestoreTab: null, // 切换项目后待恢复的激活 tab（onopen 恢复时优先）
  restoreRetry: 0,     // 切换后会话恢复失败重试计数（backend 冷启动慢时递增，成功时清零）
  projectDir: "",      // 当前项目根（/api/projects 缓存，tab 持久化按项目隔离）
  projectGen: 0,       // /api/projects 响应代际（防旧项目响应覆盖新项目目录）
  activeTab: null,     // tab id
  attachments: [],     // [{name,size}]
  fileCache: {},       // path → {content, mtime}
  treeCache: {},       // "root|path" → entries（v3 合并面板跨项目隔离，杜绝路径串扰）
  sessionStart: 0,
  timerTick: 0,
  theme: "dark",
  logTimer: 0,
  activePanel: "files",
};

const REQ_TIMEOUT = 20000; // 请求超时 20s（backend 慢启动/大项目首次请求较慢）
const MAX_RECONNECT = 60;

/* ═══════════ Toast / 横幅 ═══════════ */
function toast(text, kind = "ok") {
  let wrap = $("#toastWrap");
  if (!wrap) { wrap = document.createElement("div"); wrap.id = "toastWrap";
               document.body.appendChild(wrap); }
  const t = document.createElement("div");
  t.className = "toast " + kind;
  t.innerHTML = `<span class="t-ico">${kind === "err" ? "⚠️" : kind === "warn" ? "🟡" : "✅"}</span><span>${esc(text)}</span>`;
  wrap.appendChild(t);
  setTimeout(() => { t.classList.add("hide");
    setTimeout(() => t.remove(), 320); }, 2600);
}

/* ═══════════ 自定义确认对话框 ═══════════
   替代原生 confirm()：Wails WKWebView（macOS 桌面壳）未实现
   runJavaScriptConfirmPanel 委托，原生 confirm 静默返回 false 且不显示
   任何对话框 → 删除会话等操作"点击无反应"。自绘模态框跨
   WKWebView / WebView2 / 浏览器全平台行为一致。 */
let _confirmEl = null;
function confirmDialog(message, { danger = false, okText = "确定", cancelText = "取消" } = {}) {
  return new Promise((resolve) => {
    if (_confirmEl) { _confirmEl.remove(); _confirmEl = null; }
    const wrap = document.createElement("div");
    wrap.className = "dlg-mask";
    const dlg = document.createElement("div");
    dlg.className = "dlg-box" + (danger ? " danger" : "");
    dlg.innerHTML = `
      <div class="dlg-msg">${esc(message)}</div>
      <div class="dlg-actions">
        <button class="dlg-btn cancel">${esc(cancelText)}</button>
        <button class="dlg-btn ok">${esc(okText)}</button>
      </div>`;
    wrap.appendChild(dlg);
    document.body.appendChild(wrap);
    const done = (v) => {
      document.removeEventListener("keydown", onKey);
      wrap.remove();
      _confirmEl = null;
      resolve(v);
    };
    const onKey = (e) => {
      if (e.key === "Escape") done(false);
      else if (e.key === "Enter") done(true);
    };
    dlg.querySelector(".cancel").onclick = (e) => { e.stopPropagation(); done(false); };
    dlg.querySelector(".ok").onclick = (e) => { e.stopPropagation(); done(true); };
    wrap.onclick = (e) => { if (e.target === wrap) done(false); }; // 点遮罩 = 取消
    document.addEventListener("keydown", onKey);
    dlg.querySelector(".ok").focus();
    _confirmEl = wrap;
  });
}

function showFatal(text) {
  const el = $("#fatalBanner");
  el.style.display = "block";
  el.textContent = "⚠ " + text;
}

function hideFatal() {
  const el = $("#fatalBanner");
  if (el) el.style.display = "none";
}

/* ═══════════ WS 连接 ═══════════ */
let connGen = 0; // 连接代际：防止旧连接回调（onopen/onclose）干扰新连接
let switchingProject = false; // 项目切换进行中：防连点（切换耗时长，重复点击会连环切换）

async function resolveWSBase() {
  if (location.protocol.startsWith("http")) {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    return `${proto}://${location.host}`;
  }
  try {
    const r = await fetch("/api/config", { cache: "no-store" });
    if (!r.ok) throw new Error("HTTP " + r.status);
    const j = await r.json();
    if (j.gui) return j.gui.replace(/^http/, "ws");
  } catch (e) {
    console.error("无法获取 GUI 本地服务地址:", e);
  }
  return null;
}

// activeConn 当前活跃项目的连接对象（state.ws 是其 ws 引用）。
function activeConn() {
  return state.conns[state.activeDir] || null;
}

// connLastActive 触摸指定连接的活动时间（任何请求/消息/事件都算活动——
// 空闲断开判定依据；活跃连接不参与空闲断开）。
function touchConn(dir) {
  const c = state.conns[dir];
  if (c) c.lastActive = Date.now();
}

// startConnSweeper 启动空闲连接扫描（幂等）：每 30s 扫描一次，关闭并移除
// 满足全部条件的连接——非活跃项目 + 无在途流 + 超过 IDLE_CONN_TIMEOUT
// （10 分钟）无任何活动。活跃连接与有在途流的连接永不自动断开（用户诉求：
// "线程不活跃十分钟自动断开即可，需要再重连"——断开的是空闲连接，正在
// 生成/有请求的连接保持，保证不同项目不同对话互不干扰）。
const IDLE_CONN_TIMEOUT = 10 * 60 * 1000; // 10 分钟
const IDLE_CONN_SWEEP_MS = 30 * 1000;     // 扫描周期 30s
// sweepIdleConns 单次空闲扫描（独立具名函数：startConnSweeper 定时调用，
// 测试可手动触发验证超时判定）。
function sweepIdleConns() {
  const now = Date.now();
  for (const dir of Object.keys(state.conns)) {
    const c = state.conns[dir];
    if (!c) continue;
    if (dir === state.activeDir) continue;            // 活跃连接不断
    if (c.activeStreams > 0) continue;                // 有在途流不断
    if (now - (c.lastActive || 0) < IDLE_CONN_TIMEOUT) continue;
    // 空闲超时：关闭并移除（后端进程常驻不受影响，切回时 connectProj 秒建）
    if (c.ws) { try { c.ws.close(); } catch (_) {} }
    c.closed = true;
    delete state.conns[dir];
  }
}
function startConnSweeper() {
  if (state.connSweeper) return;
  state.connSweeper = setInterval(sweepIdleConns, IDLE_CONN_SWEEP_MS);
}

// connOnOpenUI 连接就绪后的 UI 驱动（复用路径与 connectProj.onopen 共用）：
// 拉会话列表/状态栏/模型信息 + 恢复当前会话 + 补载活动面板数据。
// 复用已有连接时由 switchActiveConn 直接调用——不等 onopen（连接一直是
// OPEN 的，只会触发一次），界面即刻可交互。
function connOnOpenUI(conn) {
  if (!conn) return;
  loadSessions();
  refreshStatus();
  refreshModelInfo();
  ping();
  // 浏览器手动刷新后恢复当前会话：前端 state.sessionId 重置为空，
  // 但后端 currentSessionID 仍是刷新前的会话——若不恢复，发消息会
  // 新建会话、删除时 isCurrent 判断全错（功能加载异常）。
  restoreCurrentSession();
  // 补载当前活动面板：连接恢复后重试此前失败的面板数据
  // （根因修复：init 时文件树等在 WS 就绪前加载失败，必须在此重试）
  if (state.activePanel === "sessions") renderSessionListPanel();
  else if (state.activePanel === "projects") renderProjectList();
  else if (state.activePanel === "config") loadConfigPanel();
  else if (state.activePanel === "agent") { refreshGoalStatus(); startAgentPolling(); }
  else if (state.activePanel === "logs") refreshLogs();
  if (!state.statusTimer) state.statusTimer = setInterval(refreshStatus, 2000);
}

// connectProj 为指定项目目录建立独立 WS 连接（多项目并行通道）：
//   - 连接 URL 带 ?dir=<enc>，contextloom-gui 反向代理按 dir 路由到该项目
//     backend（常驻池内秒路由；未拉起则由前端切换动作先拉起再连）。
//   - 已存在连接且未关闭 → 直接复用（切回该项目"秒拉起"的关键：不重连、
//     不等待 onopen，目标 backend 在途流/会话状态全程保留）。
//   - 返回后 state.ws 被设置为【新/复用连接的 ws】——调用方（切换项目/
//     首次连接）把该连接设为当前活跃连接。
async function connectProj(dir) {
  dir = normDir(dir); // 键统一规范化（与 tab 持久化/switchActiveConn 一致）
  const base = await resolveWSBase();
  if (!base) {
    if (dir === state.activeDir) {
      setConn(false);
      showFatal("无法获取本地服务地址（/api/config 失败），请重新启动 contextloom-gui。");
    }
    return null;
  }
  state.envInfo = base.replace(/^ws/, "http");
  const envEl = $("#envInfo");
  if (envEl) envEl.textContent = state.envInfo;

  // 复用未关闭连接（秒切核心）：连接还在 → 直接复用，不新建、不等待
  const exist = state.conns[dir];
  if (exist && !exist.closed && exist.ws &&
      (exist.ws.readyState === WebSocket.OPEN || exist.ws.readyState === WebSocket.CONNECTING)) {
    touchConn(dir);
    return exist;
  }
  // 旧连接已死：清理残留再新建
  if (exist && exist.ws) { try { exist.ws.close(); } catch (_) {} }
  delete state.conns[dir];

  const conn = { dir, ws: null, gen: ++connGen, pending: new Map(),
                 reconnectCount: 0, lastActive: Date.now(), activeStreams: 0, closed: false,
                 // 子任务按项目独立视图：非活跃连接把 § 信号聚合到本项目 Map，
                 // 切回该项目时恢复面板显示（跨项目互不干扰）。
                 // 复用已有 Map（空闲连接被 sweep 后重建时保留旧数据）。
                 subTasks: state.subTasksByDir[dir] || new Map() };
  state.conns[dir] = conn;
  state.subTasksByDir[dir] = conn.subTasks;
  // 带 dir 参数的连接 URL：代理按项目路由（多项目并行）
  const sep = base.includes("?") ? base.endsWith("?") || base.endsWith("&") ? "" : "&" : "?";
  const ws = new WebSocket(`${base}/api/ws${sep}dir=${encodeURIComponent(dir)}`);
  conn.ws = ws;

  ws.onopen = () => {
    if (state.conns[dir] !== conn || conn.closed) return; // 已被新连接取代/移除
    touchConn(dir);
    // 仅当该连接是当前活跃连接时才驱动 UI（会话列表/状态栏/面板补载）——
    // 非活跃连接（后台常驻）不触碰界面，避免串台。
    if (state.activeDir === dir) {
      if (state.ws !== ws) state.ws = ws; // 确保活跃指针指向本连接
      setConn(true);
      hideFatal();
      conn.reconnectCount = 0;
      state.reconnectCount = 0;
      connOnOpenUI(conn);
    }
  };
  ws.onmessage = (e) => {
    if (state.conns[dir] !== conn || conn.closed) return; // 旧连接消息丢弃
    touchConn(dir);
    let env;
    try { env = JSON.parse(e.data); } catch { return; }
    if (env.type === "resp") {
      // 本连接自己的 pending（请求都发在当前活跃连接上；非活跃连接无请求）
      const p = conn.pending.get(env.id);
      if (p) { conn.pending.delete(env.id); env.ok ? p.resolve(env.result) : p.reject(env.error); }
      return;
    }
    if (env.type !== "event") return;
    // 事件按"来源连接的项目"归属：非活跃项目的事件只累积该连接的后台
    // 流缓冲（不渲染、不碰当前界面状态），切回该项目时恢复渲染——
    // 不同项目不同对话互不干扰（用户核心诉求）。
    if (state.activeDir !== dir) {
      const sid = env.data && env.data.sessionId;
      if (env.event === "agent.stream") {
        const streamId = env.data && env.data.streamId;
        // 已显式停止的流：残留 delta 一律不入后台缓冲（否则切回该项目时
        // switchActiveConn 合并 + 恢复渲染 → "停下的对话被重新激活"）
        if ((streamId != null && state.stoppedStreams.has(streamId)) ||
            (sid && state.stoppedSessions.has(sid))) {
          if (streamId != null) state.stoppedStreams.set(streamId, sid || "");
        } else {
          conn.activeStreams++;
          const bs = conn.background || (conn.background = {});
          const sidKey = sid || "";
          const b = bs[sidKey] || (bs[sidKey] = { buf: "", streaming: true, streamId });
          // 归档条目补全流标识：停止标记按 streamId 精确匹配，缺失则只能
          // 靠会话级标记（10s 过期）过滤——补全后残余事件可长期精确丢弃
          if (b.streamId == null && streamId != null) b.streamId = streamId;
          b.buf += env.data.delta;
        }
      } else if (env.event === "agent.done") {
        // 镜像 handleEvent 的停止标记清理：已停止流的 done 仅用于解除
        // 停止标记（sendMessage 等待收尾），不驱动任何渲染。
        const streamId = env.data && env.data.streamId;
        if (streamId != null && state.stoppedStreams.has(streamId)) {
          state.stoppedStreams.delete(streamId);
          if (sid) state.stoppedSessions.delete(sid);
        } else if (sid && state.stoppedSessions.has(sid)) {
          state.stoppedSessions.delete(sid);
        }
        const bs = conn.background;
        if (bs) delete bs[sid || ""];
        if (conn.activeStreams > 0) conn.activeStreams--;
      } else if (env.event === "orchestration.progress") {
        // 非活跃项目的子任务信号：聚合到该项目独立视图，切回时恢复面板
        handleOrchestrationTo(conn.subTasks, env.data);
      } else if (env.event === "ctx.usage" || env.event === "ctx.compressed") {
        // 状态/压缩事件：仅更新连接活动时间（保活），不操作渲染
      }
      return;
    }
    // 活跃连接事件：既有 handleEvent 全量处理（按会话 sid 分流前台/后台缓冲）
    handleEvent(env);
  };
  ws.onclose = () => {
    if (state.conns[dir] !== conn) return; // 旧连接关闭不影响当前连接状态
    const wasActive = state.activeDir === dir;
    conn.closed = true;
    // 清本连接自己的 pending（活跃连接断开时即既有收敛逻辑）
    for (const [id, p] of conn.pending) {
      p.reject(new Error("连接已断开，请求已取消"));
    }
    conn.pending.clear();
    if (wasActive) {
      if (state.ws === ws) state.ws = null;
      setConn(false);
      state.reconnectCount = ++conn.reconnectCount;
      // 断线瞬间收敛所有在途状态——否则 UI 假死（历史缺陷根因之一）：
      // ① pending 请求立即失败（不等 20s 超时），正在等待的 Promise 立刻
      //    收到 reject，界面不会"点了没反应"；
      // ② 正在生成的流重置为可发送态——后端连接断开时已把已产片段写回
      //    会话历史（runAgentRound 的 ctx.Done() 分支），重连后
      //    restoreCurrentSession 恢复展示即完整，无需前端保留半截流。
      //    不重置则 streaming=true 永久卡死（按钮显示"停止生成"却无法发消息）。
      // ③ 后台流缓冲一并清空（后端在途流已随连接取消）。
      if (state.streaming) {
        state.streaming = false;
        state.streamMsg = null;
        state.streamBuf = "";
        state.streamSessionId = null;
        state.streamId = null;
        setSendBtn(false);
        setAgentStatus("", false);
      }
      state.backgroundStreams = {};
      // 根因修复：超过 MAX_RECONNECT 后【继续】每 5s 重试——
      // 旧实现直接 return 停止调度，后端（--serve）若中途恢复也无法重连，只能重启 GUI。
      // GUI 侧保持致命横幅提示（服务不可用），同时永不放弃重连（配合后端 WS 心跳
      // 探活 + 读超时，半开连接可在秒级被感知）。
      if (state.reconnectCount > MAX_RECONNECT) {
        showFatal("无法连接 ContextLoom 服务（已持续重试）。请确认 contextloom-gui 与 contextloom 主程序在同一目录。");
        // 注意：不再 return——横幅常驻但自动重连继续
      }
      setTimeout(() => { connectProj(dir); }, 2000);
    } else {
      // 非活跃连接断开：不触碰界面/不自动重连（切回该项目时 connectProj 重建）
      delete state.conns[dir];
    }
  };
  ws.onerror = () => ws.close();
  startConnSweeper();
  return conn;
}

// connect 兼容入口：连接当前活跃项目（首次启动/显式重连当前项目）。
// 启动早期 state.projectDir 可能尚未从 /api/projects 就绪（updateCurProjectLabel
// 依赖 WS 建立后的 loadSessions），这里先兜底拉一次 current，避免建空目录连接。
async function connect() {
  let dir = state.activeDir || curProjectDir() || "";
  if (!dir) {
    try {
      const r = await fetch("/api/projects", { cache: "no-store" });
      const j = await r.json();
      dir = normDir((j && j.current) || "");
    } catch (_) {}
  }
  if (!dir) return;
  state.activeDir = dir;
  state.projectDir = dir;
  const conn = await connectProj(dir);
  if (conn && conn.ws) state.ws = conn.ws;
}

// 等待 WS 就绪（最多 waitMs 毫秒）：连接中排队等待，就绪返回 true；
// 关闭/失败/超时返回 false。杜绝 init 阶段"连接未就绪"竞态错误。
// 注意：state.ws 为 null（connect() 尚未执行到 new WebSocket，如浏览器
// 刷新后 init→setPanel 立即触发请求）时【继续等待】而非立即失败——
// 旧实现 !w 直接 resolve(false)，刷新后马上操作必然报"连接未就绪"。
function waitForWS(waitMs = 20000) {
  return new Promise((resolve) => {
    const deadline = Date.now() + waitMs;
    const tick = () => {
      const w = state.ws;
      if (w && w.readyState === WebSocket.OPEN) return resolve(true);
      if (w && w.readyState === WebSocket.CLOSED) return resolve(false);
      if (Date.now() >= deadline) return resolve(false);
      setTimeout(tick, 100);
    };
    tick();
  });
}

function ping() {
  req("ping", {}).then(() => {}).catch(() => {});
}

// req 发送请求到【当前活跃项目】的 backend（多连接池中仅活跃连接收发请求，
// 非活跃连接只收后台流事件——不同项目不同对话互不干扰）。
// pending 挂活跃连接的 conn.pending（与 connectProj.onmessage 的 resp 处理
// 一致；切换项目后旧活跃连接的 pending 由 onclose 拒绝，不会误 resolve）。
function req(method, params = {}) {
  return new Promise((resolve, reject) => {
    const id = String(++state.reqId);
    const conn = activeConn();
    const pendingMap = conn ? conn.pending : state.pending;
    const timer = setTimeout(() => {
      pendingMap.delete(id);
      reject(new Error(`请求 ${method} 超时（${REQ_TIMEOUT / 1000}s）`));
    }, REQ_TIMEOUT);
    pendingMap.set(id, {
      resolve: (v) => { clearTimeout(timer); resolve(v); },
      reject: (e) => { clearTimeout(timer); reject(e); },
    });
    const send = () => {
      const w = state.ws;
      if (w && w.readyState === WebSocket.OPEN) {
        w.send(JSON.stringify({ id, type: "req", method, params }));
      } else {
        clearTimeout(timer);
        pendingMap.delete(id);
        reject(new Error("连接未就绪，请稍候重试"));
      }
    };
    // 已就绪直接发送；连接中则排队等待（最多 6s），避免启动竞态报错
    if (state.ws && state.ws.readyState === WebSocket.OPEN) {
      send();
    } else {
      waitForWS(20000).then(ok => {
        if (ok) send();
        else {
          clearTimeout(timer);
          pendingMap.delete(id);
          reject(new Error("连接未就绪，请稍候重试"));
        }
      });
    }
  });
}

function msgOf(e) { return (e && e.msg) || String(e); }

/* ═══════════ 事件 ═══════════ */
// curSession 当前激活的聊天会话 ID（事件流归属判断：agent.stream/done 的
// sessionId 与之比较；无激活聊天 tab 时退回 state.sessionId）。
function curSession() {
  const tab = state.tabs.find(t => t.kind === "chat" && t.id === state.activeTab);
  return tab ? tab.id : state.sessionId;
}

function handleEvent(env) {
  switch (env.event) {
    case "agent.stream": {
      // 多对话并行：事件带 sessionId 且不属于当前会话 → 只累积后台缓冲，
      // 不渲染（切回该会话时由 switchSession 恢复渲染）；属于当前会话 →
      // 正常流式渲染。后台流【不中断】（后端 loadSession 不再取消其他会话流）
      const sid = env.data && env.data.sessionId;
      const streamId = env.data && env.data.streamId;
      // ① 已显式停止的流：残留 delta 一律丢弃（"停下后诡异再次触发"根因）。
      if (streamId != null && state.stoppedStreams.has(streamId)) break;
      // ①b 停止请求在途的会话（尚未收到 done）：此时不会有"合法新流"
      // （sendMessage 会等旧流 done 后才发新消息），到达的 stream 必是旧流
      // 残留——记录其 streamId 并丢弃，杜绝"停下的对话再次触发"。
      if (sid && state.stoppedSessions.has(sid)) {
        if (streamId != null) state.stoppedStreams.set(streamId, sid);
        break;
      }
      if (sid && sid !== curSession()) {
        const bs = state.backgroundStreams[sid] || (state.backgroundStreams[sid] = { buf: "", streaming: true, streamId });
        // 同会话新流取代旧后台流：以最新 streamId 为准，旧流缓冲丢弃
        if (bs.streamId != null && streamId != null && bs.streamId !== streamId) {
          bs.buf = "";
        }
        if (streamId != null) bs.streamId = streamId;
        bs.streaming = true;
        bs.buf += env.data.delta;
        break;
      }
      // ② 前台流归属校验：当前会话已有进行中的流，且事件 streamId 与
      // 当前前台流不一致 → 属于同会话的旧流/另一条流，丢弃（防串台重触发）。
      if (state.streaming && state.streamId != null && streamId != null && streamId !== state.streamId) {
        break;
      }
      if (!state.streamMsg) beginStream(streamId);
      else if (streamId != null) state.streamId = streamId;
      state.streamBuf += env.data.delta;
      scheduleStreamRender();
      break;
    }
    case "agent.done": {
      // 非当前会话的 done：只清理该会话的后台缓冲（不碰当前会话状态，
      // 防 endStream 按 data.sessionId 把当前会话拉回旧会话——历史 bug）
      const sid = env.data && env.data.sessionId;
      const streamId = env.data && env.data.streamId;
      // 已显式停止的流：done 仅用于清理，不驱动任何渲染/状态迁移。
      if (streamId != null && state.stoppedStreams.has(streamId)) {
        state.stoppedStreams.delete(streamId);
        if (sid) state.stoppedSessions.delete(sid);
        if (sid && sid !== curSession()) delete state.backgroundStreams[sid];
        break;
      }
      // 兼容无 streamId 的旧格式 done：若该会话有被停止的流，视为其收尾
      // （清理停止标记，使 sendMessage 的等待结束；不驱动渲染）。
      if (streamId == null && sid) {
        for (const [id, s] of state.stoppedStreams) {
          if (s === sid) state.stoppedStreams.delete(id);
        }
      }
      // 停止请求在途的会话：done 到达即清除待停止标记（该会话已确认收尾）。
      if (sid && state.stoppedSessions.has(sid)) {
        state.stoppedSessions.delete(sid);
        if (sid !== curSession()) { delete state.backgroundStreams[sid]; break; }
        // 当前会话：继续走 endStream 兜底收尾（保留原有结束语义）
      }
      if (sid && sid !== curSession()) {
        delete state.backgroundStreams[sid];
        break;
      }
      // 前台流归属校验：done 的 streamId 与当前前台流不一致（同会话旧流）
      // → 不结束当前流（"别的对话让停下的对话再次触发/难以停下"根因）。
      if (state.streaming && state.streamId != null && streamId != null && streamId !== state.streamId) {
        break;
      }
      endStream(env.data);
      break;
    }
    case "ctx.usage":
      applyCtxUsage(env.data);
      break;
    case "ctx.compressed": {
      // 压缩事件（手动 /compact + 阈值自动压缩共用）：
      // 压缩只作用于 AI 上下文（后端 conversationHistory），用户可见历史
      // （事件日志）从不删减——所以这里【不】再按事件携带的消息列表渲染
      // 压缩态（旧实现会把消息区重绘成"摘要+保留消息"，看起来历史变少，
      // 需切走再切回才恢复，正是用户反馈的缺陷）。
      // 现在收到事件后：① 追加一条系统提示（压缩结果文案，auto=true 时
      // 说明是自动压缩）；② 主动调 chat.history 重拉该会话完整历史重绘
      // 消息区（与切走再切回等效，但无需用户操作）；③ 刷新状态栏占比。
      // 后端事件携带的 messages（完整历史）仅作兜底：拉取失败时用它渲染，
      // 避免界面停留在旧状态。
      const d = env.data || {};
      const sid = d.sessionId;
      const isAuto = !!d.auto;
      // 非当前会话：不重绘当前消息区（避免串台），仅刷新状态栏。
      // 该会话若切回，switchSession 走 loadSession 完整历史，无显示问题。
      if (sid && sid !== curSession()) { if (d.usage) applyCtxString(d.usage); break; }
      if (isAuto) {
        appendSystem(`📦 ${d.message || "上下文已自动压缩"}`);
      } else if (d.message) {
        appendSystem(`📦 ${d.message}`);
      }
      if (d.usage) applyCtxString(d.usage);
      // 压缩后自动刷新：重拉完整历史（事件日志重放），消息区保持完整显示
      // （代际保护：重绘前确认用户没有切走会话）。
      const target = sid || state.sessionId;
      if (target) {
        req("chat.history", { uuid: target }).then(r => {
          if ((sid || state.sessionId) !== curSession()) return;
          if (Array.isArray(r && r.messages)) renderMessages(r.messages);
        }).catch(() => {
          // 拉取失败兜底：用事件携带的完整历史渲染（保证刷新语义不丢）
          if (Array.isArray(d.messages)) {
            const msgs = d.messages.filter(m => !(m.partial && (state.streaming || state.backgroundStreams[target])));
            renderMessages(msgs);
          }
        });
      }
      break;
    }
    case "orchestration.progress":
      handleOrchestration(env.data);
      break;
    case "goal.round": {
      // goal 模式轮次结果（每轮监工评估 + 状态机推进；自动续跑时每轮一条）
      const d = env.data || {};
      if (d.reason) appendSystem(`🎯 ${d.reason}`);
      break;
    }
    case "term.output":
      handleTermOutput(env.data);
      break;
    case "term.exit":
      handleTermExit(env.data);
      break;
  }
}

// handleOrchestration 处理子 Agent 活动信号（§ 前缀，与 TUI 面板同构）：
//   §START|<subID>|<directive>|<employee>|<task>  子任务启动（upsert → running，employee 可空=旧格式）
//   §TOK|<subID>|<token>                子任务增量 token（只更新 LastLine，绝不新增行）
//   §DONE|<subID>|<score>               子任务完成 + 评分
//   §FAIL|<subID>|<err>                 子任务失败
// 按 subID 聚合到 state.subTasks（Map），渲染结构化面板 #agentTasks：
// 标题行「⚡ 子任务 · N 进行 · M 完成 · K 失败」+ 每行一个子任务
// （DOM 按 data-subid 定位复用；TOK 高频走增量更新，不重建 DOM）。
function handleOrchestration(data) {
  const res = handleOrchestrationTo(state.subTasks, data);
  if (!res) return; // 未知信号/无效：忽略
  // 面板渲染：结构性变化（START/DONE/FAIL）→ 全量重绘；TOK → 增量更新
  if (res.structural) renderSubTaskPanel();
  else updateSubTaskLineFromMap(state.subTasks, res.subID);
  // 状态栏实时活动计数
  const n = runningSubCount();
  setAgentStatus(n > 0 ? `${n} 个 Agent 进行中` : subTaskSummary(), n > 0);
  clearTimeout(state.agentBusyTimer);
  state.agentBusyTimer = setTimeout(() => {
    const m = runningSubCount();
    if (m > 0) setAgentStatus(`${m} 个 Agent 进行中`, true);
    else if (state.subTasks.size > 0) setAgentStatus(subTaskSummary(), false);
    else setAgentStatus("", false);
  }, 2000);
}

// handleOrchestrationTo 纯函数：把 § 信号写入指定 Map。
// 返回 {structural bool, subID string}（有效信号）或 null（未知/无效信号）。
// 独立于 state.subTasks：非活跃连接的子任务信号聚合到 conn.subTasks，
// 切回时并入全局 state.subTasks 渲染面板。
function handleOrchestrationTo(target, data) {
  const raw = (data && data.msg) || "";
  if (!raw) return null;
  const parts = raw.split("|");
  const kind = parts[0].replace(/^§/, "").trim();
  const subID = parts[1] || "";
  let structural = true;
  if (kind === "START" && parts.length >= 4) {
    const isNew = parts.length >= 5;
    const directive = parts[2] || "";
    const employee = isNew ? (parts[3] || "") : "";
    const task = isNew ? parts.slice(4).join("|") : parts.slice(3).join("|");
    if (!target.has(subID) && target.size > 0 && runningSubCountIn(target) === 0) {
      target.clear();
    }
    const st = target.get(subID);
    if (st) {
      st.status = "running"; st.directive = directive; st.employee = employee; st.task = task;
      st.lastLine = ""; st.score = ""; st.err = "";
    } else {
      target.set(subID, { id: subID, directive: directive, employee: employee, task: task, status: "running", score: "", lastLine: "", err: "" });
    }
  } else if (kind === "TOK" && parts.length >= 3) {
    const st = target.get(subID);
    if (st) {
      st.lastLine = appendLastLine(st.lastLine, parts.slice(2).join("|"));
      structural = false;
    } else {
      target.set(subID, { id: subID, directive: "", task: "", status: "running", score: "", lastLine: appendLastLine("", parts.slice(2).join("|")), err: "" });
    }
  } else if (kind === "DONE" && parts.length >= 3) {
    const score = parts[2] || "—";
    const st = target.get(subID);
    if (st) {
      st.status = "done"; st.score = score;
      if (!st.lastLine) st.lastLine = "完成 · 评分 " + score + "/100";
    } else {
      target.set(subID, { id: subID, directive: "", task: "", status: "done", score: score, lastLine: "完成 · 评分 " + score + "/100", err: "" });
    }
  } else if (kind === "FAIL" && parts.length >= 3) {
    const err = parts.slice(2).join("|");
    const st = target.get(subID);
    if (st) { st.status = "failed"; st.err = err; }
    else target.set(subID, { id: subID, directive: "", task: "", status: "failed", score: "", lastLine: "失败: " + err, err: err });
  } else {
    return null; // 未知信号忽略
  }
  // 上限保护：保留最近 100 个子任务
  while (target.size > 100) {
    const first = target.keys().next().value;
    target.delete(first);
  }
  return { structural, subID };
}

// updateSubTaskLineFromMap 增量更新子任务面板：按 target Map 中最新的那条记录更新 DOM
function updateSubTaskLineFromMap(target, subID) {
  const tasks = $("#agentTasks");
  if (!tasks) return;
  let row = null;
  for (const el of tasks.children) {
    if (el.dataset && el.dataset.subid === subID) { row = el; break; }
  }
  const st = target.get(subID);
  if (!row || !st) { renderSubTaskPanel(); return; }
  let last = row.querySelector(".at-last");
  if (!last) {
    last = document.createElement("span");
    last.className = "at-last";
    row.appendChild(last);
  }
  last.textContent = " → " + st.lastLine.slice(0, 80);
}

// runningSubCountIn 统计指定 Map 中的进行中子任务数
function runningSubCountIn(target) {
  let n = 0;
  target.forEach(s => { if (s.status === "running") n++; });
  return n;
}

// runningSubCount 当前项目进行中的子任务数（状态栏实时活动计数）
function runningSubCount() {
  return runningSubCountIn(state.subTasks);
}

// clearSubTasks 清空子任务面板：子任务是中间态——TUI 在每次发送前 m.subTasks = nil
// （新一轮对话/新一轮任务派发前清空，防止旧任务残留导致新任务不断累加）。
// 调用点：sendMessage 新一轮对话开始；handleOrchestration START 分支上一批全结束。
function clearSubTasks() {
  if (state.subTasks.size === 0) return;
  state.subTasks.clear();
  renderSubTaskPanel();
  setAgentStatus("", false);
  clearTimeout(state.agentBusyTimer);
}

// appendLastLine 与 TUI appendLastLine 同语义：追加 token 片段，仅保留最后一行，超长截断
function appendLastLine(prev, content) {
  let combined = (prev && prev !== "启动中…") ? prev + content : content;
  const idx = combined.lastIndexOf("\n");
  if (idx >= 0) combined = combined.slice(idx + 1).trim();
  return combined.slice(-120);
}

// runningSubCount 进行中的子任务数（状态栏实时活动计数）
function runningSubCount() {
  let n = 0;
  state.subTasks.forEach(s => { if (s.status === "running") n++; });
  return n;
}

// subTaskSummary 标题计数文本（与 TUI「⚡ 子任务 · N 进行 · M 完成 · K 失败」对齐）
function subTaskSummary() {
  let running = 0, done = 0, failed = 0;
  state.subTasks.forEach(s => {
    if (s.status === "running") running++;
    else if (s.status === "done") done++;
    else if (s.status === "failed") failed++;
  });
  return `⚡ 子任务 · ${running} 进行 · ${done} 完成 · ${failed} 失败`;
}

// renderSubTaskPanel 全量渲染结构化子任务面板：标题计数行 + 每行一个子任务
function renderSubTaskPanel() {
  const tasks = $("#agentTasks");
  if (!tasks) return;
  if (state.subTasks.size === 0) { tasks.innerHTML = ""; return; }
  let html = `<div class="agent-task at-title"><span class="at-status running">⚡</span> ${esc(subTaskSummary())}</div>`;
  state.subTasks.forEach(st => {
    const ico = st.status === "running" ? "⏳" : st.status === "done" ? "✅" : "❌";
    const cls = st.status === "running" ? "running" : st.status === "done" ? "done" : "failed";
    const label = st.directive ? `[${esc(st.directive)}] ` : "";
    // 身份显示：员工命中（M3）显示员工名（带工牌 icon）；未命中回退 subID
    const who = st.employee ? "🧑💻" + st.employee : st.id;
    const summary = (st.task || st.err || "").slice(0, 60);
    const tail = st.status === "done" && st.score ? ` ${esc(String(st.score))}` : "";
    const last = st.lastLine ? `<span class="at-last"> → ${esc(st.lastLine).slice(0, 80)}</span>` : "";
    html += `<div class="agent-task" data-subid="${esc(st.id)}"><span class="at-status ${cls}">${ico}</span> ${esc(who)} ${label}${esc(summary)}${tail}${last}</div>`;
  });
  tasks.innerHTML = html;
}

// updateSubTaskLine TOK 高频增量更新：只刷新对应子任务行的 lastLine，不重建 DOM
function updateSubTaskLine(subID) {
  const tasks = $("#agentTasks");
  if (!tasks) return;
  let row = null;
  for (const el of tasks.children) {
    if (el.dataset && el.dataset.subid === subID) { row = el; break; }
  }
  const st = state.subTasks.get(subID);
  if (!row || !st) { renderSubTaskPanel(); return; } // 行不存在（面板被重建）→ 全量重绘兜底
  let last = row.querySelector(".at-last");
  if (!last) {
    last = document.createElement("span");
    last.className = "at-last";
    row.appendChild(last);
  }
  last.textContent = " → " + st.lastLine.slice(0, 80);
}

function scheduleStreamRender() {
  if (state.rafPending) return;
  state.rafPending = true;
  requestAnimationFrame(() => {
    state.rafPending = false;
    renderStream();
  });
}

/* ═══════════ 会话 ═══════════ */
let sessionsLoading = false;
let sessionsReloadQueued = false; // 在途加载期间再次请求 → 排队补刷（防丢刷新）
let lastSessionsSig = "";
let sessionsReloadTimer = 0;

async function loadSessions() {
  if (sessionsLoading) { sessionsReloadQueued = true; return; }
  sessionsLoading = true;
  try {
    const list = await req("chat.listSessions");
    state.sessions = list || [];
    const sig = JSON.stringify(state.sessions.map(s => s.uuid + "|" + s.updatedAt));
    if (sig !== lastSessionsSig) {
      lastSessionsSig = sig;
      if (state.activePanel === "sessions") renderSessionListPanel();
      await updateCurProjectLabel();
      // 首次加载（刷新/切换项目后）恢复当前项目持久化的右侧会话 tab——
      // "切换项目右侧会话不关闭"的前端配套（会话由 backend 常驻保留，
      // tab 视图按项目持久化于此处恢复）。需先 updateCurProjectLabel
      // 拿到 state.projectDir（tab 按项目隔离）再恢复。
      await restoreSavedChatTabs();
    }
  } catch (e) {
    toast("加载会话失败: " + msgOf(e), "err");
  } finally {
    sessionsLoading = false;
    // 在途期间有新的刷新请求（如删除/新建后）：补跑一次，保证列表立即更新
    if (sessionsReloadQueued) {
      sessionsReloadQueued = false;
      loadSessions();
    }
  }
}

// 恢复当前项目持久化的会话 tab：把 savedOpenChatTabs() 中仍存在于后端
// 会话列表的 tab 补回（已有 tab 去重；已删除的会话从持久化清除）。
// 不激活任何 tab（保持当前会话），仅恢复右侧可见性。
async function restoreSavedChatTabs() {
  const pdir = curProjectDir();
  const saved = savedOpenChatTabs();
  if (!saved.length) return;
  const valid = new Set(state.sessions.map(s => s.uuid));
  const dead = [];
  let added = 0;
  for (const it of saved) {
    if (!valid.has(it.id)) { dead.push(it.id); continue; }
    if (state.tabs.find(t => t.kind === "chat" && t.id === it.id)) continue;
    const sess = state.sessions.find(s => s.uuid === it.id);
    state.tabs.push({ kind: "chat", id: it.id, title: (sess && sess.title) || it.title || "会话", projDir: pdir });
    added++;
  }
  for (const id of dead) forgetDeletedChatTab(id);
  // 修复：旧实现判断条件在 push 之后恒为 false（tab 已存在）→ 恢复的 tab
  // 从不重绘，只有等到别的路径调 renderTabs 才显示（用户观感=右侧会话没恢复）。
  if (added > 0) renderTabs();
  // 目标项目恢复出 tab 但当前无激活项（首次访问该项目）时，激活该项目记忆的
  // tab 或第一个——否则右侧 tab 栏有内容但消息区空白。
  // 已有 pendingRestoreTab（afterProjectSwitch 已选定激活项）时不重复加载：
  // restoreCurrentSession 会处理它，此处再发一次 loadSession 属冗余请求。
  if (added > 0 && !state.pendingRestoreTab) {
    const curActive = state.tabs.find(t => t.id === state.activeTab);
    if (!curActive || tabProjDir(curActive) !== pdir) {
      const savedActive = savedActiveChatTabFor(pdir);
      const mine = state.tabs.filter(t => t.kind === "chat" && tabProjDir(t) === pdir);
      const first = mine.find(t => t.id === savedActive) || mine[0];
      if (first) {
        state.activeTab = first.id;
        renderTabs();
        if (state.sessionId !== first.id) switchSession(first.id);
      }
    }
  }
}

// updateCurProjectLabel 会话面板显示当前项目名：切换项目后列表跟随该项目，
// 标签让用户直观确认"列表属于哪个项目"（防误以为列表未更新）。
async function updateCurProjectLabel() {
  const el = $("#curProjectLabel");
  if (!el) return;
  const myGen = ++state.projectGen;
  try {
    const r = await fetch("/api/projects", { cache: "no-store" });
    const j = await r.json();
    // 代际保护：切换项目时旧项目的 /api/projects 请求可能后返回，若直接
    // 覆盖 state.projectDir 会把 tab 归属标到错误项目（跨项目串台）。
    if (myGen !== state.projectGen) return;
    const cur = (j && j.current) || "";
    state.projectDir = normDir(cur); // 供持久化 tab 按项目隔离（curProjectDir()）
    el.textContent = cur ? "📁 " + baseName(cur) : "";
    el.title = cur || "未指定项目";
    // projectDir 就绪后补存一次：onopen 阶段 restoreCurrentSession 先于
    // updateCurProjectLabel 完成时，openChatTab 的 saveOpenChatTabs 会错存
    // 到空键 ""；此处把当前 tab 重存到真实目录键并清掉空键脏数据，保证
    // 切换项目后按目录键能读到（"切换项目右侧会话不关闭"前端闭环关键）。
    saveOpenChatTabs();
    try {
      const all = readTabsStore();
      if (all[""] !== undefined) { delete all[""]; localStorage.setItem(OPEN_TABS_KEY, JSON.stringify(all)); }
    } catch (_) {}
  } catch (e) { /* 忽略：标签为可选增强 */ }
}

function scheduleSessionsReload() {
  clearTimeout(sessionsReloadTimer);
  sessionsReloadTimer = setTimeout(() => { lastSessionsSig = ""; loadSessions(); }, 800);
}

function renameSessionPrompt(uuid) {
  const s = state.sessions.find(x => x.uuid === uuid);
  promptDialog("新标题:", s ? s.title : "", "重命名").then(title => {
    if (title == null || !title.trim()) return;
    req("chat.renameSession", { uuid, title: title.trim() }).then(() => {
      toast("会话已重命名");
      lastSessionsSig = "";
      loadSessions();
      // 更新 tab
      const tab = state.tabs.find(t => t.kind === "chat" && t.id === uuid);
      if (tab) { tab.title = title.trim(); renderTabs(); }
    }).catch(e => toast("重命名失败: " + msgOf(e), "err"));
  });
}

/* 输入框版确认对话框（替代原生 prompt——WKWebView 未实现
   runJavaScriptTextInputPanel，原生 prompt 静默返回 null） */
function promptDialog(message, initial = "", okText = "确定") {
  return new Promise((resolve) => {
    if (_confirmEl) { _confirmEl.remove(); _confirmEl = null; }
    const wrap = document.createElement("div");
    wrap.className = "dlg-mask";
    const dlg = document.createElement("div");
    dlg.className = "dlg-box";
    dlg.innerHTML = `
      <div class="dlg-msg">${esc(message)}</div>
      <input class="dlg-input" type="text" value="${esc(initial)}" spellcheck="false" />
      <div class="dlg-actions">
        <button class="dlg-btn cancel">取消</button>
        <button class="dlg-btn ok">${esc(okText)}</button>
      </div>`;
    wrap.appendChild(dlg);
    document.body.appendChild(wrap);
    const input = dlg.querySelector(".dlg-input");
    const done = (v) => {
      document.removeEventListener("keydown", onKey);
      wrap.remove();
      _confirmEl = null;
      resolve(v);
    };
    const onKey = (e) => {
      if (e.key === "Escape") done(null);
      else if (e.key === "Enter") done(input.value);
    };
    dlg.querySelector(".cancel").onclick = (e) => { e.stopPropagation(); done(null); };
    dlg.querySelector(".ok").onclick = (e) => { e.stopPropagation(); done(input.value); };
    wrap.onclick = (e) => { if (e.target === wrap) done(null); };
    document.addEventListener("keydown", onKey);
    input.focus();
    input.select();
    _confirmEl = wrap;
  });
}

// 新建模型预设（傻瓜式）：只填核心字段（名称/后端/endpoint/api_key/max_context），
// 其余用默认值；提交后调用 model.create 创建并自动激活，刷新下拉。
function openModelCreateDialog() {
  return new Promise((resolve) => {
    if (_confirmEl) { _confirmEl.remove(); _confirmEl = null; }
    const wrap = document.createElement("div");
    wrap.className = "dlg-mask";
    const dlg = document.createElement("div");
    dlg.className = "dlg-box";
    dlg.innerHTML = `
      <div class="dlg-msg">新建模型预设（其余参数用默认，新建后可微调）</div>
      <div class="dlg-field"><label>名称 *</label><input class="dlg-input" id="mdl-name" type="text" placeholder="如 my-model" spellcheck="false" /></div>
      <div class="dlg-field"><label>后端</label>
        <select class="dlg-input" id="mdl-backend">
          <option value="deepseek" selected>deepseek</option>
          <option value="openai">openai</option>
          <option value="anthropic">anthropic</option>
          <option value="openai-compatible">openai-compatible</option>
        </select></div>
      <div class="dlg-field"><label>API 端点</label><input class="dlg-input" id="mdl-endpoint" type="text" placeholder="留空=官方默认" spellcheck="false" /></div>
      <div class="dlg-field"><label>API Key</label><input class="dlg-input" id="mdl-apikey" type="password" placeholder="留空=环境变量/现有 Key" spellcheck="false" autocomplete="off" /></div>
      <div class="dlg-field"><label>最大上下文</label><input class="dlg-input" id="mdl-ctx" type="number" value="131072" /></div>
      <div class="dlg-actions">
        <button class="dlg-btn cancel">取消</button>
        <button class="dlg-btn ok">创建并激活</button>
      </div>`;
    wrap.appendChild(dlg);
    document.body.appendChild(wrap);
    const nameEl = dlg.querySelector("#mdl-name");
    const done = (v) => {
      document.removeEventListener("keydown", onKey);
      wrap.remove();
      _confirmEl = null;
      resolve(v);
    };
    const onKey = (e) => {
      if (e.key === "Escape") done(null);
      else if (e.key === "Enter") submit();
    };
    async function submit() {
      const name = nameEl.value.trim();
      if (!name) { toast("请填写模型名称", "err"); return; }
      const btn = dlg.querySelector(".dlg-btn.ok");
      btn.disabled = true;
      try {
        const payload = {
          name,
          backend: dlg.querySelector("#mdl-backend").value,
          endpoint: dlg.querySelector("#mdl-endpoint").value.trim(),
          api_key: dlg.querySelector("#mdl-apikey").value,
          max_context: parseInt(dlg.querySelector("#mdl-ctx").value, 10) || 0,
        };
        await req("model.create", payload);
        toast(`已创建并激活模型: ${name}`, "ok");
        done(true);
        refreshStatus();
        refreshModelInfo();
        await loadConfigSettings();
      } catch (e) {
        toast(`创建失败: ${msgOf(e)}`, "err");
        btn.disabled = false;
      }
    }
    dlg.querySelector(".cancel").onclick = (e) => { e.stopPropagation(); done(null); };
    dlg.querySelector(".ok").onclick = (e) => { e.stopPropagation(); submit(); };
    wrap.onclick = (e) => { if (e.target === wrap) done(null); };
    document.addEventListener("keydown", onKey);
    nameEl.focus();
    _confirmEl = wrap;
  });
}

async function deleteSessionPrompt(uuid) {
  if (!await confirmDialog("确定删除该会话？此操作不可恢复。", { danger: true, okText: "删除" })) return;
  const isCurrent = uuid === state.sessionId;
  const doDelete = async (retried) => {
    try {
      await req("chat.deleteSession", { uuid });
      toast("会话已删除");
      forgetDeletedChatTab(uuid); // 持久化 tab 清理（防死 tab 恢复）
      lastSessionsSig = "";
      await loadSessions();
      await closeTab(uuid);
      // 当前会话被删除：自动切换到剩余会话第一个；
      // 删除【最后一个】会话 → 进入空状态（不再自动新建——用户要求，
      // 发消息时后端/前端会自动创建新会话）。
      // ⚠️ 用删除前记录的 isCurrent 判断：closeTab 关闭当前会话 tab 时
      // 已把 state.sessionId 清空（"关闭再打开需重新加载历史"修复），
      // 此处再比对 sessionId 恒不成立 → 自动切换会静默失效。
      if (isCurrent) {
        const next = state.sessions.find(s => s.uuid !== uuid);
        if (next) openChatTab(next.uuid);
        else enterEmptyState();
      }
    } catch (e) {
      // 兜底：后端仍认为该会话是当前活跃（前端状态与后端不一致时，
      // 如刷新后 restore 失败）→ 自动切换后重试一次，而不是直接报错
      if (!retried && /当前活跃|请先切换/.test(msgOf(e))) {
        const next = state.sessions.find(s => s.uuid !== uuid);
        if (next) {
          const ok = await openChatTab(next.uuid);
          if (ok) { await doDelete(true); return; }
        }
      }
      toast("删除失败: " + msgOf(e), "err");
    }
  };
  if (isCurrent) {
    // 后端禁止删除当前活跃会话（防状态悬空）：先【等待切换完成】再删。
    // 旧实现用 setTimeout(150ms) 盲等：switchSession 是异步 await req，
    // 网络/后端稍慢即超时，后端 currentSessionID 未更新 → 删除被拒。
    const next = state.sessions.find(s => s.uuid !== uuid);
    if (next) {
      const ok = await openChatTab(next.uuid);
      if (!ok) { toast("切换到其他会话失败，已取消删除", "warn"); return; }
    }
    // 无其他会话（删除最后一个）：后端 DeleteSession 已允许删除当前会话，
    // 删除后回到无会话状态，前端进入空状态——不预创建新会话。
  }
  await doDelete(false);
}

// 删除最后一个会话后进入空状态（无会话）：发消息时前端/后端自动新建。
// 注意：只清【当前项目】的 chat tab——其他项目的 tab 仍在栏上（跨项目保留），
// 连同文件 tab 一并保留（旧实现清空全部 tabs 会连带抹掉其他项目 tab）。
function enterEmptyState() {
  state.sessionId = "";
  state.activeTab = null;
  const cur = curProjectDir();
  state.tabs = state.tabs.filter(t => t.kind === "file" || (cur && tabProjDir(t) !== cur));
  state.streaming = false;
  state.streamMsg = null;
  state.streamBuf = "";
  state.streamSessionId = null;
  state.streamId = null;
  state.backgroundStreams = {};
  saveOpenChatTabs(); // 会话全部删除：当前项目持久化 tab 同步清空
  hideFileEditor();
  renderMessages([]);
  renderTabs();
  setSendBtn(false);
  setAgentStatus("", false);
}

/* ═══════════ 多 Tab ═══════════ */

// 右侧会话 tab 按【所属项目】持久化（localStorage）——切换项目/刷新后右侧
// 已打开的会话 tab 原样回到界面上（"切换/添加项目时右侧旧项目会话 tab 保留"
// 的前端配套：会话本身由 backend 常驻保留，tab 视图由本持久化恢复）。
// 值 = { dir: [{id, title}] }；chat tab 的 projDir 字段决定归属项目，
// 因此【跨项目并存】：切到 B 后 A 的 tab 仍在，切回 A 原样可见。
const OPEN_TABS_KEY = "contextloom.openChatTabs.v2"; // { dir: [{id,title}] }
// 当前激活的 chat tab 按项目记忆（切回该项目时恢复原激活项）
const ACTIVE_TAB_KEY = "contextloom.activeChatTab.v1"; // { dir: tabId }
function readTabsStore() {
  try { return JSON.parse(localStorage.getItem(OPEN_TABS_KEY) || "{}") || {}; } catch (_) { return {}; }
}
function saveOpenChatTabs() {
  try {
    const cur = curProjectDir();
    // 以内存中的 tabs 为准重建每个项目的条目：栏上可能同时存在多个项目
    // 的 tab（跨项目并存），按 tab.projDir 分组落盘。
    const grouped = {};
    for (const t of state.tabs) {
      if (t.kind !== "chat") continue;
      // 补归属：早于 projectDir 就绪时创建的 tab（onopen 阶段）无 projDir
      if (!t.projDir && cur) t.projDir = cur;
      const d = normDir(t.projDir || cur);
      if (!d) continue;
      (grouped[d] = grouped[d] || []).push({ id: t.id, title: t.title || "会话" });
    }
    const all = readTabsStore();
    for (const d of Object.keys(grouped)) all[d] = grouped[d];
    // 当前项目：内存中无 chat tab 即落盘为空（关闭全部 tab 场景）。
    // 其他项目保留磁盘条目——其 tab 未加载进内存（本次会话未访问该项目），
    // 不代表被关闭，清掉会复活丢失问题。
    if (cur) all[cur] = grouped[cur] || [];
    localStorage.setItem(OPEN_TABS_KEY, JSON.stringify(all));
    saveActiveChatTab();
  } catch (_) {}
}
// 记忆当前项目下的激活 chat tab（切回项目时恢复原视图）。
function saveActiveChatTab() {
  try {
    const d = curProjectDir();
    if (!d) return;
    const cur = state.tabs.find(t => t.kind === "chat" && t.id === state.activeTab);
    if (!cur || tabProjDir(cur) !== d) return; // 激活的是其他项目 tab：不覆盖
    const all = JSON.parse(localStorage.getItem(ACTIVE_TAB_KEY) || "{}");
    all[d] = cur.id;
    localStorage.setItem(ACTIVE_TAB_KEY, JSON.stringify(all));
  } catch (_) {}
}
function savedActiveChatTabFor(dir) {
  try { return (JSON.parse(localStorage.getItem(ACTIVE_TAB_KEY) || "{}"))[normDir(dir)] || ""; } catch (_) { return ""; }
}
// 移除已删除会话在持久化中的记录（防残留死 tab 恢复）。
function forgetDeletedChatTab(uuid) {
  try {
    const all = readTabsStore();
    let changed = false;
    for (const dir of Object.keys(all)) {
      const arr = (all[dir] || []).filter(x => (typeof x === "string" ? x : x.id) !== uuid);
      if (arr.length !== (all[dir] || []).length) { all[dir] = arr; changed = true; }
    }
    if (changed) localStorage.setItem(OPEN_TABS_KEY, JSON.stringify(all));
  } catch (_) {}
}
// 读取当前项目应恢复的会话 tab 列表。
function savedOpenChatTabs() {
  return savedChatTabsFor(curProjectDir());
}
// 读取指定项目持久化的会话 tab 条目（切换项目时按【目标目录】读取——
// 此刻 state.projectDir 尚未更新，不能用 curProjectDir）。
// 返回 [{id,title}]（兼容 v1 的纯 id 数组，旧数据自动升级）。
function savedChatTabsFor(dir) {
  const all = readTabsStore();
  const arr = all[normDir(dir)] || [];
  return arr.map(x => typeof x === "string" ? { id: x, title: "会话" } : x);
}
// normDir 规范化项目根路径（tab 持久化的键必须与后端 canonical 形式
// 一致）：统一分隔符、折叠重复斜杠、去尾部斜杠（保留根 "/"）。
// 后端 decodePath 用 filepath.Abs 规范化后返回，而前端传入的 dir 可能带
// 尾部斜杠/重复斜杠（用户手输路径、browse 回填）——键不一致会导致
// 持久化按不同键存与读，表现为"切回来右侧会话没了"（用户实测缺陷残留路径）。
function normDir(d) {
  let s = String(d == null ? "" : d).trim().replace(/\\/g, "/").replace(/\/{2,}/g, "/");
  if (s.length > 1) s = s.replace(/\/+$/, "");
  return s;
}

// 当前项目根（/api/projects 缓存；无则空串——不匹配任何持久化键）。
function curProjectDir() {
  return normDir(state.projectDir);
}

// connSubTasksFor 返回指定目录连接的独立子任务视图（无则新建空 Map）。
// 与 state.subTasksByDir 映射对齐，保证切换项目时子任务面板按项目隔离。
function connSubTasksFor(dir) {
  const ndir = normDir(dir);
  if (state.subTasksByDir[ndir]) return state.subTasksByDir[ndir];
  const newMap = new Map();
  state.subTasksByDir[ndir] = newMap;
  return newMap;
}

// ═══ 项目切换（不刷新页面）═══
// 历史做法（两代）：
//   ① location.reload() 整页重载 → 右侧 tab 全被销毁；
//   ② 快照替换（state.tabs = 目标项目快照）→ 旧项目 tab 从界面上消失。
// 两者都违背用户要求："切换/添加项目时，右侧旧项目的会话 tab 要保留在界面上"。
// 现在改为【tab 跨项目并存】：chat tab 带 projDir 归属，切换项目只改
// state.projectDir + 把目标项目的持久化 tab 补入栏中，旧项目 tab 原样留在
// 栏上（标记为"其他项目"，点击即切回该项目）。仅重连 WS（反向代理在握手时
// 绑定 backend，必须重连才能指向新项目）。
// tab 所属项目（缺省视为当前项目——兼容历史数据）。
function tabProjDir(tab) {
  return normDir(tab && tab.projDir ? tab.projDir : curProjectDir());
}
// 是否为"其他项目"的 tab（当前 backend 无法加载其会话，点击需先切项目）。
function isForeignTab(tab) {
  return !!tab && tab.kind === "chat" && !!curProjectDir() && tabProjDir(tab) !== curProjectDir();
}
// reconnectWS 强制重连当前活跃项目的连接（仅连接异常时用）：先摘除
// state.ws 再 close 旧连接，然后为当前活跃项目重建连接。
// 多连接改造后，切换项目【不再走这里】（见 switchActiveConn——复用已有
// 连接，不重连不等待，秒拉起）。
function reconnectWS() {
  const dir = state.activeDir || curProjectDir() || "";
  const old = state.ws;
  state.ws = null;
  if (old) { try { old.close(); } catch (_) {} }
  if (state.conns[dir]) { state.conns[dir].closed = true; delete state.conns[dir]; }
  state.reconnectCount = 0;
  connectProj(dir).then(conn => {
    if (conn && conn.ws) state.ws = conn.ws;
    if (state.activeDir === dir) {
      setConn(true); // 放下秒回：onopen 会再次驱动；此处避免等待间隙变灰
    }
  });
}

// switchActiveConn 切换当前活跃项目的 WS 连接（多项目并行通道核心）：
//   - 目标项目连接【已存在且可用】→ 直接复用（state.ws 指向它）——
//     不关闭、不重连、不等 onopen，目标项目的 backend 会话/在途流立即可见
//     （"切换项目对话状态秒拉起、无缝"的直接实现）；
//   - 目标项目连接【不存在/已断开】→ connectProj 新建（其 backend 进程
//     已在常驻池，代理按 dir 参数秒路由，无需再等 backend 启动）。
// 旧项目的连接【保留】在 conns 池中：它的 backend 在途流继续经该连接
// 推送后台缓冲，切回时恢复渲染——不同项目不同对话互不干扰。
async function switchActiveConn(dir) {
  const ndir = normDir(dir);
  state.activeDir = ndir;
  const exist = state.conns[ndir];
  // 该项目连接在"非活跃期间"累积的后台流缓冲（切走时该项目的在途流继续
  // 经此连接推送）：切回时合并进全局 backgroundStreams，供 switchSession/
  // restoreCurrentSession 恢复渲染——并行对话切回立即可见（不依赖重新
  // loadSession，后台流已实时累积，无需等待后端）。
  if (exist && exist.background) {
    for (const sid of Object.keys(exist.background)) {
      const b = exist.background[sid];
      // ⚠️ 移动语义（"切回后已停止对话被直接激活"根因）：无论合并还是丢弃，
      // 一律从连接缓冲移除。旧实现只拷贝【引用】、不从连接缓冲删除——
      // restoreCurrentSession 把条目移交前台（delete 全局键）后，连接缓冲的
      // 陈旧副本不再随流更新却保留 streaming:true；等停止标记被 done 清理后，
      // 下次切回又被合并 + 恢复渲染 → 已停止/已结束的对话复活成"● 生成中"。
      delete exist.background[sid];
      if (state.backgroundStreams[sid]) continue; // 全局已有条目（更新缓冲）：丢弃旧副本
      // 用户已显式停止的流不合并回全局（防止切回项目时被恢复成
      // "● 生成中"——复活已停止的对话）
      if ((b.streamId != null && state.stoppedStreams.has(b.streamId)) ||
          (sid && state.stoppedSessions.has(sid))) {
        continue;
      }
      state.backgroundStreams[sid] = b;
    }
  }
  // 子任务视图恢复：连接在其非活跃期间持续聚合的 § 信号（conn.subTasks）
  // 以及切换时归档的旧视图，切回时合并进全局面板，子代理活动立即可见。
  if (exist && exist.subTasks && exist.subTasks.size > 0) {
    // 直接使用连接自身的子任务视图（同引用，后续非活跃期间新增的信号
    // 自动聚合到该 Map，切回时立即可见，无需拷贝避免"一帧延迟"）。
    state.subTasks = exist.subTasks;
    state.subTasksByDir[ndir] = exist.subTasks;
    renderSubTaskPanel();
  }
  if (exist && !exist.closed && exist.ws &&
      (exist.ws.readyState === WebSocket.OPEN || exist.ws.readyState === WebSocket.CONNECTING)) {
    touchConn(ndir);
    state.ws = exist.ws;
    // 复用路径（秒切）：连接一直在，无需等待 onopen 驱动 UI——
    // 直接按新项目重拉会话列表/状态栏/恢复会话，界面即刻可交互。
    if (exist.ws.readyState === WebSocket.OPEN) {
      connOnOpenUI(exist);
    }
    return exist;
  }
  // 目标连接不存在（从未打开/已空闲断开）：新建（backend 常驻池已拉起）
  const conn = await connectProj(ndir);
  if (conn && conn.ws) {
    state.ws = conn.ws;
    if (conn.ws.readyState === WebSocket.OPEN) connOnOpenUI(conn);
  }
  return conn;
}

// afterProjectSwitch 切换项目后的前端收尾（不整页刷新）：
//  1. 旧项目 tab 【原样保留】在右侧栏上（不替换、不清空——用户核心诉求）；
//  2. 项目作用域缓存清空（文件树/文件内容）；
//  3. 目标项目的持久化会话 tab 补入栏中（首次访问该项目时右侧不空白）；
//  4. 重连 WS 指向新 backend，并加载新项目会话列表。
async function afterProjectSwitch(dir) {
  const prevDir = curProjectDir();
  // 规范化目录（后端 decodePath 用 filepath.Abs 规范化后返回；前端传入的
  // 原始路径可能带尾部斜杠/重复斜杠，甚至相对路径）——持久化键必须与后端
  // 一致，否则"存"与"读"用不同键，切回来右侧会话就丢了。
  const newDir = normDir(dir);
  // 切换前：给当前栏上"无归属"的 tab 打上旧项目标签并落盘（跨项目并存
  // 的持久化前提——每个 tab 必须知道自己的项目，切回来才能读回）。
  if (prevDir) {
    for (const t of state.tabs) {
      if (t.kind === "chat" && !t.projDir) t.projDir = prevDir;
    }
    saveOpenChatTabs();
  }
  state.projectDir = newDir;
  state.projectGen++; // 作废在途的 /api/projects 响应（防旧目录覆盖新目录）
  state.treeCache = {};
  state.fileCache = {};
  // 终端属于旧项目的 backend 进程（term id 在新 backend 无效）：销毁实例并
  // 清空，切回旧项目时用户按需重开（避免显示陈旧终端/输入报错）。
  for (const t of state.terms) { if (t.term) { try { t.term.dispose(); } catch (_) {} } }
  state.terms = [];
  state.curTerm = null;
  const termTabs = $("#termTabs"); if (termTabs) termTabs.innerHTML = "";
  // ── 归档旧项目运行态到其连接（多项目并行保留） ──
  // 切换前把当前项目的在途流/子任务归档到该项目的 conn，切回时恢复：
  //   - 前台流（streamBuf/streaming）→ conn.background[sessionId] 后台缓冲
  //   - 子任务面板（subTasks）→ conn.subTasks
  //   - 归档后 state 重置为新项目视图，旧项目的运行时状态随连接保留。
  const oldConn = state.conns[prevDir ? normDir(prevDir) : ""];
  if (oldConn) {
    const bs = oldConn.background || (oldConn.background = {});
    const sid = state.sessionId;
    // 用户已显式停止的前台流不归档：停止标记被 done 清理后，归档条目会在
    // 下次切回时把已停止的对话重新激活（复活路径之一）
    const frontStopped = !!sid && (state.stoppedSessions.has(sid) ||
      (state.streamId != null && state.stoppedStreams.has(state.streamId)));
    if (sid && state.streaming && state.streamBuf && !frontStopped) {
      // 归档携带 streamId（与 switchSession/closeTab 归档一致）：切回后
      // 该流再被用户停止时可精确标记 stoppedStreams（否则停止标记对不上）。
      // 赋值覆盖而非 if(!bs[sid])：前台状态永远是最新缓冲，旧残留副本不得保留。
      bs[sid] = { buf: state.streamBuf, streaming: true, streamId: state.streamId };
    }
    // 本项目其他会话的后台在途缓冲【移动】回连接缓冲：切换期间 delta 从
    // 原缓冲继续累积（跨切换连续），且全局不再保留副本——停止标记被 done
    // 清理后不会在下次切回时复活已停止/已结束的流（与 switchActiveConn 的
    // 移动语义配对：任何时刻流缓冲只有一份权威副本）。
    for (const ksid of Object.keys(state.backgroundStreams)) {
      bs[ksid] = state.backgroundStreams[ksid];
    }
    if (state.subTasks.size > 0) {
      oldConn.subTasks = state.subTasks;
    }
  }
  // 目标项目的持久化 tab 补入栏中（去重；旧项目 tab 保留不动）。
  // 首次访问该项目时这是唯一的 tab 来源——否则切过去右侧空白。
  const saved = savedChatTabsFor(newDir);
  let added = 0;
  for (const it of saved) {
    if (state.tabs.find(t => t.kind === "chat" && t.id === it.id)) continue;
    state.tabs.push({ kind: "chat", id: it.id, title: it.title || "会话", projDir: newDir });
    added++;
  }
  // 会话列表/签名重置：切到新 backend 后必须重新拉取（否则两个项目会话
  // 列表相同签名时 restoreSavedChatTabs 不触发，右侧 tab 不恢复）。
  // ⚠️ sessionsLoading 是全局锁：旧项目在途的 loadSessions（如 switchSession
  // 尾部触发、backend 慢响应）若一直不结束会卡 true，新项目 loadSessions
  // 被短路不刷新——多连接并行下旧连接保留（pending 不被 reject 释放），
  // 必须在此显式重置，让新项目的加载不被旧项目阻塞（互不干扰）。
  state.sessions = [];
  lastSessionsSig = "";
  sessionsLoading = false;
  sessionsReloadQueued = false;
  // 会话运行态重置（旧项目在途流由后端常驻继续跑，切回时从历史恢复）
  state.sessionId = "";
  state.restoreRetry = 0; // 新切换：会话恢复重试计数从 0 开始（最多 5 次）
  state.streamMsg = null;
  state.streamBuf = "";
  state.streaming = false;
  state.streamSessionId = null;
  state.streamId = null;
  state.backgroundStreams = {};
  setSendBtn(false);
  setAgentStatus("", false);
  // 子任务面板切换到目标项目的独立视图（无则空 Map）——旧项目视图已归档
  // 到其连接（上方归档逻辑），切回时经 switchActiveConn 恢复合并。
  state.subTasks = connSubTasksFor(newDir);
  // 激活项优先级：
  //   ① pendingRestoreTab 若属于新项目（点击"其他项目 tab"切回来的场景
  //      —— 必须激活用户点的那个 tab，而非该项目记忆的激活项）；
  //   ② 该项目记忆的激活 tab；③ 该项目第一个 chat tab；
  //   ④ 都没有 → activeTab 置空（绝不把其他项目 tab 标成激活项：消息区
  //      空白却高亮一个 tab 语义矛盾；用户点它才切回该项目）。
  const savedActive = savedActiveChatTabFor(newDir);
  const projTabs = state.tabs.filter(t => t.kind === "chat" && tabProjDir(t) === newDir);
  const pending = state.pendingRestoreTab;
  const pendingTab = pending ? projTabs.find(t => t.id === pending) : null;
  const want = pendingTab || projTabs.find(t => t.id === savedActive) || projTabs[0] || null;
  state.activeTab = want ? want.id : null;
  renderTabs();
  if (want) {
    // 让 onopen 的 restoreCurrentSession 优先恢复我们选中的 tab，
    // 而不是后端残留的 currentSession（否则会覆盖用户视图）
    state.pendingRestoreTab = want.id;
  } else {
    // 该项目无会话 tab：消息区清空（栏上旧项目 tab 仍在，点击可切回）
    state.pendingRestoreTab = null;
    hideFileEditor();
    renderMessages([]);
  }
  saveOpenChatTabs();
  // 切换活跃 WS 连接：目标项目连接已存在（之前打开过）→ 直接复用秒拉起；
  // 不存在 → 新建（backend 常驻池已就绪，代理按 dir 秒路由）。旧项目连接
  // 保留在池中（在途流继续后台推送，切回恢复渲染）——不重连不打断。
  switchActiveConn(newDir);
  if (state.activePanel === "projects") renderProjectList();
  if (state.activePanel === "sessions") renderSessionListPanel();
}

async function openChatTab(uuid) {
  const exist = state.tabs.find(t => t.kind === "chat" && t.id === uuid);
  // 其他项目的 tab（切换项目后旧项目 tab 仍留在栏上）：当前 backend 加载不了
  // 它的会话——点击语义 = 切回该项目并激活该会话（用户要求 tab 保留且可用）。
  if (exist && isForeignTab(exist)) return switchToTabProject(exist);
  // 点击/激活【当前会话】时：只确保 tab 显示激活，【绝不】触发 loadSession
  // 重绘——生成中后端历史缺在途 assistant，重绘会把"正在生成的半截回复"
  // 从界面清掉（"点击本会话标题后清空重置、只能看到新生成的"根因）。
  // 消息区 DOM 无需重绘（历史 + 在途流仍在）；若当前激活的是文件 tab，
  // 仅切换显示（activeTab + 隐藏编辑器），不动消息列表。
  // ⚠️ 破坏性约束：短路必须同时满足【tab 已存在】——新建会话（uuid 不在
  // tabs 中）即使调用方提前设置了 state.sessionId 也必须走完整分支创建
  // tab 并加载历史，否则"新建会话"静默失效（历史缺陷，防再犯）。
  if (uuid === state.sessionId && exist) {
    if (state.activeTab !== uuid) {
      state.activeTab = uuid;
      hideFileEditor();
      renderTabs();
    }
    return true;
  }
  // 已打开则激活
  if (exist) { state.activeTab = uuid; renderTabs(); return switchSession(uuid); }
  if (state.tabs.length >= 12) { toast("Tab 数量已达上限（12）", "warn"); return false; }
  const sess = state.sessions.find(s => s.uuid === uuid);
  // projDir 记录 tab 归属项目（跨项目并存的关键：切走后 tab 仍在栏上，
  // 切回来能按归属恢复激活项；点击其他项目 tab 时据此切回）。
  state.tabs.push({ kind: "chat", id: uuid, title: sess ? sess.title : "会话", projDir: curProjectDir() });
  state.activeTab = uuid;
  renderTabs();
  saveOpenChatTabs();
  return switchSession(uuid);
}

// switchToTabProject 点击"其他项目"的 tab：切回该 tab 所属项目并激活它。
// 项目切换是异步的（后端 backend 复用/拉起 + WS 重连 + loadSession），
// 切换完成后由 pendingRestoreTab 激活目标会话——这里只负责发起切换。
async function switchToTabProject(tab) {
  const dir = tabProjDir(tab);
  if (!dir) return false;
  if (switchingProject) { toast("正在切换项目，请稍候…", "warn"); return false; }
  switchingProject = true;
  state.pendingRestoreTab = tab.id;
  try {
    const r = await fetch("/api/projects/switch", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: dir }),
    });
    if (!r.ok) { switchingProject = false; state.pendingRestoreTab = null; toast("切换项目失败: " + await r.text(), "err"); return false; }
    let canon = dir;
    try { const j = await r.json(); if (j && j.dir) canon = j.dir; } catch (_) {}
    await afterProjectSwitch(canon);
    switchingProject = false;
    return true;
  } catch (e) {
    switchingProject = false;
    state.pendingRestoreTab = null; // 切换失败：不留下悬空的恢复目标
    toast("切换项目失败: " + msgOf(e), "err");
    return false;
  }
}

function openFileTab(path, content, mtime, root, readonly) {
  const exist = state.tabs.find(t => t.kind === "file" && t.id === path);
  if (exist) {
    state.activeTab = path;
    renderTabs();
    renderFileEditor();
    return;
  }
  if (state.tabs.length >= 12) { toast("Tab 数量已达上限（12）", "warn"); return; }
  const name = path.split("/").pop() || path;
  // v3 合并面板：非当前项目文件以只读打开（🔒，禁用保存）；root 透传供保存定位。
  // projDir 记录归属项目：切换项目后文件 tab 同样保留在栏上（用户诉求
  // "右侧 tab 不因切换项目消失"），点击仍可查看（内容已在内存）。
  state.tabs.push({ kind: "file", id: path, title: (readonly ? "🔒 " : "") + name, dirty: false, content: content, origContent: content, mtime: mtime, root: root || "", readonly: !!readonly, projDir: curProjectDir() });
  state.activeTab = path;
  renderTabs();
  renderFileEditor();
}

async function closeTab(id) {
  const idx = state.tabs.findIndex(t => t.id === id);
  if (idx < 0) return;
  const tab = state.tabs[idx];
  if (tab.kind === "file" && tab.dirty) {
    if (!await confirmDialog(`文件 ${tab.title} 有未保存修改，确定关闭？`)) return;
  }
  state.tabs.splice(idx, 1);
  if (tab.kind === "chat") {
    // 关闭会话 tab：从持久化中移除（刷新/切项目后不再自动恢复该 tab）。
    // 注意用 forgetDeletedChatTab（跨项目全表删除该 id）——只调
    // saveOpenChatTabs 的话，若该 tab 属于"当前内存无其他 tab 的项目"，
    // 磁盘旧条目会残留并在此后切回时复活已关闭的 tab。
    forgetDeletedChatTab(id);
    saveOpenChatTabs();
  }
  // ⚠️ 关闭【当前会话】tab 时必须重置会话状态：state.sessionId 若不清空，
  // 再次点击该会话时 switchSession 的 "uuid === state.sessionId" 短路直接
  // return，不调 chat.loadSession 也不渲染历史 → 消息区空白，只有重启 GUI
  // 才能恢复（"点击 X 关闭会话再打开，历史对话不加载"根因，历史缺陷）。
  // 前台流转入后台缓冲（与 switchSession 语义一致：不中断生成，切回时
  // 由 switchSession 的 backgroundStreams 恢复逻辑接管渲染）。
  if (tab.kind === "chat" && state.sessionId === id) {
    if (state.streaming || state.streamMsg) {
      state.backgroundStreams[id] = { buf: state.streamBuf, streaming: true, streamId: state.streamId };
    }
    state.sessionId = "";
    state.streamMsg = null;
    state.streamBuf = "";
    state.streaming = false;
    state.streamSessionId = null;
    state.streamId = null;
    setSendBtn(false);
    setAgentStatus("", false);
  }
  if (state.activeTab === id) {
    // 聚焦相邻：只在本项目 tab（含文件 tab）内找——绝不自动切到其他项目的
    // tab（那会连带重连 backend，用户没要求切项目就不该切）。本项目无剩余
    // tab 时进入空状态（其他项目 tab 仍留在栏上）。
    const myDir = tabProjDir(tab);
    // 保持原始顺序，取关闭位置【左侧最近】的同项目 tab（无则右侧最近）
    const mine = state.tabs
      .map((t, i) => ({ t, i }))
      .filter(x => x.t.kind === "file" || tabProjDir(x.t) === myDir);
    const left = mine.filter(x => x.i < idx).pop();
    const right = mine.find(x => x.i > idx);
    const next = (left && left.t) || (right && right.t) || null;
    if (next) {
      state.activeTab = next.id;
      activateTab(next);
    } else {
      state.activeTab = null;
      hideFileEditor();
      renderMessages([]);
    }
  }
  renderTabs();
}

function renderTabs() {
  const bar = $("#tabBar");
  bar.innerHTML = "";
  // tabs（只渲染打开的会话/文件 tab；会话的完整管理在左侧会话面板，
  // 不再放 ☰ 下拉与 ＋ 新建按钮——与面板重复，且挤压 tab 显示空间）
  for (const tab of state.tabs) {
    const foreign = isForeignTab(tab); // 其他项目的会话 tab：保留在栏上，点击切回
    const el = document.createElement("div");
    el.className = "tab-item" + (tab.id === state.activeTab ? " active" : "") + (foreign ? " foreign" : "");
    el.dataset.id = tab.id;
    if (foreign) el.title = "属于项目 " + tabProjDir(tab) + "（点击切回该项目）";
    const icon = tab.kind === "chat" ? (foreign ? "🗂" : "💬") : "📄";
    const dirty = tab.kind === "file" && tab.dirty ? `<span class="tab-dirty">●</span>` : "";
    el.innerHTML = `<span>${icon}</span><span class="tab-title">${esc(tab.title)}</span>${dirty}<button class="tab-close">✕</button>`;
    el.onclick = () => {
      if (foreign) { switchToTabProject(tab); return; }
      if (state.activeTab !== tab.id) {
        state.activeTab = tab.id;
        renderTabs();
        activateTab(tab);
      } else if (tab.kind === "chat" && state.sessionId !== tab.id) {
        // 激活 tab 已是本 tab，但内容未加载（切换项目后恢复 tab 的
        // loadSession 失败/被竞态放弃时，state.activeTab 指向该 tab 而
        // state.sessionId 为空）→ 点击必须强制加载，否则消息区永远空白
        // 且无响应（用户实测"切换项目后中间对话记录消失、点不开"根因）。
        activateTab(tab);
      }
    };
    el.querySelector(".tab-close").onclick = (e) => {
      e.stopPropagation();
      closeTab(tab.id);
    };
    bar.appendChild(el);
  }
}

function activateTab(tab) {
  if (isForeignTab(tab)) { switchToTabProject(tab); return; }
  if (tab.kind === "chat") {
    hideFileEditor();
    // 强制加载语义：即使该 tab 已是 activeTab，只要内容未加载
    // （sessionId ≠ tab.id，如切换项目后恢复失败）也必须走完整加载
    // 路径——switchSession 的 "uuid === sessionId" 短路只放过真正已加载
    // 的当前会话（曾因恢复失败 activeTab 指向空内容，点击 tab 无响应）。
    switchSession(tab.id);
  } else {
    renderFileEditor();
  }
}

/* ═══════════ 会话切换 ═══════════ */
async function switchSession(uuid) {
  // ⚠️ 所有"点击会话"路径的汇聚点：无论来自顶栏会话 tab、左侧会话列表
  // （openChatTab 已存在分支直接调本函数）还是自动切换，都必须先收起文件
  // 编辑器并恢复消息区/输入区显示——否则会残留"md 预览窗口空占上方、
  // 下方聊天区被 display:none 隐藏、无法点击"的布局状态（历史缺陷：
  // renderFileEditor 会隐藏 msgList/inputZone，openChatTab 曾不恢复）。
  // 幂等：activateTab(chat) 已调用时重复执行无副作用。
  // 其他项目的会话 tab：当前 backend 加载不了 → 切回该项目（由
  // pendingRestoreTab 在切换完成后激活该会话）。
  const foreignTab = state.tabs.find(t => t.kind === "chat" && t.id === uuid);
  if (foreignTab && isForeignTab(foreignTab)) return switchToTabProject(foreignTab);
  hideFileEditor();
  // 点击/激活【当前会话】时短路：不做任何重置、清流或重绘。否则正在流式
  // 生成的半截回复会被清空（streamBuf=""、streaming=false），并被后端历史
  // 重绘覆盖——而 streamChat 只在流【结束】时才把 assistant 写入历史，
  // 生成中后端历史只有 user 消息 → 界面变成空白/仅剩用户消息，
  // 表现为"点击会话导致记录重置清空、没有任何对话记录"（历史缺陷）。
  if (uuid === state.sessionId) return true;
  // 会话切换代际序号（防并发竞态）：关闭会话 tab 会自动切相邻会话（异步
  // loadSession），用户紧接着点击另一会话 → 两个 loadSession 并发执行。
  // 先发出的请求若后返回，会把界面覆盖成错误会话（"关闭会话 tab 再点击
  // 会话无记录/显示错乱、重开 GUI 才正常"根因——重开 GUI 无并发所以正常）。
  // 修复：每次切换递增序号，await 返回后序号不匹配（已被更新切换取代）
  // 则放弃本次渲染（不设置 sessionId、不渲染历史、不恢复后台流）。
  const mySeq = ++state.sessionSeq;
  // 切换前保存当前会话滚动位置（切回时恢复，防"丢失状态"）
  const msgList = $("#msgList");
  const cur = state.sessionId;
  if (cur) {
    state.chatCache[cur] = { len: msgList.children.length, scrollTop: msgList.scrollTop };
  }
  // 多对话并行：切换会话【不中断】当前会话正在进行的流——把当前流状态
  // 转入后台缓冲（backgroundStreams），后台继续生成；切回时恢复渲染。
  // 绝不调用 chat.stop（那会中断对话——历史缺陷，用户明确要求不中断）。
  // 注意：到此处 cur 必不等于 uuid（相等已短路），无条件备份当前流状态，
  // 防止任何路径下丢失正在生成的半截回复（streamBuf）。
  if (cur && (state.streaming || state.streamMsg)) {
    state.backgroundStreams[cur] = { buf: state.streamBuf, streaming: true, streamId: state.streamId };
  }
  // 清空当前流式渲染状态（流已转入后台缓冲；防止残留渲染到新会话）
  state.streamMsg = null;
  state.streamBuf = "";
  state.streaming = false;
  state.streamSessionId = null;
  state.streamId = null;
  setSendBtn(false);
  setAgentStatus("", false);
  try {
    const r = await req("chat.loadSession", { uuid });
    // ⚠️ 代际检查：期间发生了更新的会话切换（用户又点了别的会话/自动切换
    // 已推进）→ 本请求已被取代，放弃渲染（界面保持新请求的结果）。
    if (mySeq !== state.sessionSeq) return true;
    state.sessionId = r.uuid;
    state.mode = r.mode || "chat";
    setModeUI(state.mode);
    renderMessages(r.messages || []);
    // 目标会话有后台流在途 → 恢复流式渲染（并行对话切回可见，继续累积）
    const bs = state.backgroundStreams[uuid];
    if (bs && bs.streaming) {
      state.streaming = true;
      state.streamBuf = bs.buf;
      state.streamId = bs.streamId != null ? bs.streamId : null;
      delete state.backgroundStreams[uuid]; // 流已回前台，后台缓冲移交 streamBuf
      appendMessage("assistant", "", true); // 内部设置 state.streamMsg
      state.sr = null; // 切换会话恢复：首帧增量渲染自动收编/重建子节点
      if (state.streamMsg) fillAssistantBubble(state.streamMsg, bs.buf);
      state.streamSessionId = uuid;
      setSendBtn(true);
      setAgentStatus("● 生成中", true);
    }
    // 切回会话：消息条数未变时恢复原滚动位置（聊天记录/滚动状态不丢失）
    const cache = state.chatCache[uuid];
    if (cache && cache.len === (r.messages || []).length && cache.scrollTop > 0) {
      requestAnimationFrame(() => { msgList.scrollTop = cache.scrollTop; });
    }
    state.sessionStart = Date.now();
    updateTimer();
    lastSessionsSig = "";
    loadSessions();
    $("#input").focus();
    return true;
  } catch (e) { toast("切换会话失败: " + msgOf(e), "err"); return false; }
}

async function newSession() {
  try {
    const r = await req("chat.newSession", { title: "新会话" });
    // ⚠️ 破坏性约束：绝不能在 openChatTab 之前设置 state.sessionId——
    // openChatTab/switchSession 对"uuid===当前会话"短路（点击当前会话防
    // 重绘），提前设置会把新会话短路跳过：tab 不创建、消息区不加载、
    // 界面无任何变化（"新建会话按钮失效"根因，历史缺陷）。
    // 必须先走完整分支（push tab + loadSession 渲染空态，switchSession
    // 内部已把 state.sessionId 设为 r.uuid），此处赋值仅幂等兜底。
    await loadSessions();            // 侧边栏立即出现新会话（标题/列表一致）
    const ok = await openChatTab(r.uuid);
    if (!ok) { toast("打开新会话失败", "warn"); return; }
    state.sessionId = r.uuid;        // 幂等（switchSession 已设置）
    state.sessionStart = Date.now();
    updateTimer();
    toast("已新建会话");
  } catch (e) { toast("新建会话失败: " + msgOf(e), "err"); }
}

// 浏览器手动刷新后恢复当前会话（依赖后端 chat.currentSession）。
// 刷新后前端 state.sessionId 为空，但后端 currentSessionID 保留旧值：
// 若不恢复，发消息会新建会话、删除会话时 isCurrent 判断错误、
// 消息列表空白——表现为"会话等功能加载异常"。onopen 时调用本函数，
// 无当前会话时静默跳过（保持空状态）。
async function restoreCurrentSession() {
  // 代际保护：与 switchSession 同款 sessionSeq 防并发竞态。
  // 旧实现无保护：onopen 的 restore 与用户已发生的会话切换（或随后
  // 的 switchSession）并发时，先发出的 restore 若后返回，会把界面
  // 覆盖回后端残留会话（历史记录丢失/错乱）。响应到达时代际不匹配
  // 则放弃渲染。
  const mySeq = ++state.sessionSeq;
  try {
    // 切换项目后的 tab 恢复优先：pendingRestoreTab 是 afterProjectSwitch
    // 选中的右侧 tab，绝不能被后端残留的 currentSession 覆盖（否则用户
    // 切回项目时看到的不是原来的会话）。
    const want = state.pendingRestoreTab;
    if (want) {
      state.pendingRestoreTab = null;
      const isChat = state.tabs.find(t => t.kind === "chat" && t.id === want);
      if (isChat) {
        try {
          const r = await req("chat.loadSession", { uuid: want });
          if (mySeq !== state.sessionSeq) return;
          state.restoreRetry = 0; // 恢复成功：重置重试计数
          state.sessionId = r.uuid;
          state.mode = r.mode || "chat";
          setModeUI(state.mode);
          state.activeTab = r.uuid;
          renderTabs();
          saveOpenChatTabs();
          renderMessages(r.messages || []);
          // 切回项目恢复流式渲染：切换期间后台继续生成的半截回复（归档在
          // backgroundStreams / 连接后台缓冲）必须接回流式状态，界面显示
          // "生成中"且继续累积 delta——否则切回项目时界面空白、直到流结束
          // 才一次性补全（"切回来不能重新续上"根因）。
          const bs = state.backgroundStreams[want];
          if (bs && bs.streaming) {
            state.streaming = true;
            state.streamBuf = bs.buf;
            state.streamId = bs.streamId != null ? bs.streamId : null;
            delete state.backgroundStreams[want]; // 流已回前台，后台缓冲移交 streamBuf
            appendMessage("assistant", "", true); // 内部设置 state.streamMsg
            state.sr = null; // 切换会话恢复：首帧增量渲染自动收编/重建子节点
            if (state.streamMsg) fillAssistantBubble(state.streamMsg, bs.buf);
            state.streamSessionId = want;
            setSendBtn(true);
            setAgentStatus("● 生成中", true);
          }
          state.sessionStart = Date.now();
          updateTimer();
          lastSessionsSig = "";
          return;
        } catch (e) {
          // 恢复失败（目标项目 backend 冷启动/切换刚完成未就绪时 loadSession
          // 会超时或报错）：保留待恢复标记并调度一次重试，避免该 tab 内容
          // 永远空白且点击无响应（用户实测"切换项目后中间对话记录消失"根因）。
          // 重试间隔 1s、最多 5 次（backend 拉起通常 ≤3s；超过则放弃，等待
          // 用户手动点击 tab——renderTabs 已支持"激活但未加载"点击强制加载）。
          state.pendingRestoreTab = want;
          if (state.restoreRetry < 5) {
            state.restoreRetry++;
            setTimeout(() => {
              // 仅当该 tab 仍是激活项且内容仍未加载时重试（用户已手动切换
              // 到其他会话/其他 tab 则不打扰）
              const t = state.tabs.find(x => x.kind === "chat" && x.id === want);
              if (t && state.activeTab === want && state.sessionId !== want) {
                restoreCurrentSession();
              }
            }, 1000);
          }
          console.warn("恢复切换后的会话失败（稍后重试）:", e);
          return;
        }
      }
      // 待恢复的是文件 tab：文件内容已在项目快照中，直接保持（不拉会话，
      // 否则会把用户视图从文件抢到后端残留会话）。
      if (state.tabs.find(t => t.kind === "file" && t.id === want)) {
        state.activeTab = want;
        renderTabs();
        renderFileEditor();
        return;
      }
    }
    const r = await req("chat.currentSession");
    if (mySeq !== state.sessionSeq) return; // 已被更新的切换取代
    state.restoreRetry = 0; // 恢复成功：重置重试计数
    if (!r || !r.uuid) return; // 后端无当前会话，保持空状态
    state.sessionId = r.uuid;
    state.mode = r.mode || "chat";
    setModeUI(state.mode);
    // 恢复会话 tab（chat 标签，复用现有会话标题）
    if (!state.tabs.find(t => t.kind === "chat" && t.id === r.uuid)) {
      const sess = state.sessions.find(s => s.uuid === r.uuid);
      state.tabs.push({ kind: "chat", id: r.uuid, title: sess ? sess.title : (r.title || "会话"), projDir: curProjectDir() });
    }
    state.activeTab = r.uuid;
    renderTabs();
    saveOpenChatTabs(); // 恢复的当前会话 tab 也持久化（否则刷新/切换后该 tab 丢失）
    renderMessages(r.messages || []);
    // 切回项目/刷新恢复后，正在生成的流（归档在 backgroundStreams）必须
    // 接回流式渲染——否则切回项目时界面空白、直到流结束才一次性补全。
    const uuid = r.uuid;
    const bs = uuid ? state.backgroundStreams[uuid] : null;
    if (bs && bs.streaming) {
      state.streaming = true;
      state.streamBuf = bs.buf;
      state.streamId = bs.streamId != null ? bs.streamId : null;
      delete state.backgroundStreams[uuid];
      appendMessage("assistant", "", true);
      state.sr = null;
      if (state.streamMsg) fillAssistantBubble(state.streamMsg, bs.buf);
      state.streamSessionId = uuid;
      setSendBtn(true);
      setAgentStatus("● 生成中", true);
    }
    state.sessionStart = Date.now();
    updateTimer();
  } catch (e) {
    console.warn("恢复当前会话失败:", e);
  }
}

/* ═══════════ 消息渲染 ═══════════ */
function renderMessages(msgs) {
  const list = $("#msgList");
  list.innerHTML = "";
  if (!msgs.length) { list.appendChild(emptyHTML()); return; }
  for (const m of msgs) {
    // 生成中的在途内容（后端合并的 partial assistant）：若该会话有在途流
    // （前台 streaming 或后台缓冲）→ 跳过渲染，由流式恢复逻辑接管
    // （switchSession 的 backgroundStreams 恢复 / agent.stream 的 beginStream），
    // 避免与流式气泡重复（"点击会话后只能看到新生成的"根治；无在途流时
    // partial 正常渲染为普通消息兜底，防竞态窗口丢内容）。
    if (m.partial && (state.streaming || state.backgroundStreams[state.sessionId])) continue;
    appendMessage(m.role, m.content, false, m.partial, m.images);
  }
  scrollBottom(true);
  // 记录消息快照信息（切回会话时恢复滚动位置用）
  if (state.sessionId) {
    state.chatCache[state.sessionId] = { len: msgs.length, scrollTop: list.scrollTop };
  }
}

function emptyHTML() {
  const d = document.createElement("div");
  d.id = "emptyState"; // appendMessage 依赖此 id 在首条消息时移除空状态页
  d.className = "empty-state";
  d.innerHTML = `<div class="empty-logo">🧠</div>
    <h1>ContextLoom <span class="grad">智能编码助手</span></h1>
    <p>上下文感知 · 记忆持久 · 多 Agent 协作</p>
    <div class="empty-hints"><span>直接输入问题开始对话</span><span><kbd>/</kbd> 输入命令</span><span>切换模式：Chat / Task / Goal</span></div>`;
  return d;
}

function esc(s) {
  return String(s == null ? "" : s).replace(/[&<>"']/g, c =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
}

/* 解析 assistant 文本为结构化段落序列：
 *   [{type:"text", content}, {type:"think", content, closed}, ...]
 * 关键改进（修复"思考中只显示光标"）：
 *   思考标签【未闭合】（流式中间态）也生成思考块——正则闭合分支带 |$ ，
 *   未闭合时惰性匹配到末尾，思考块内容随流式逐帧增长（实时可见）；
 *   真正的闭合标签到达后自然闭合（closed=true）——等效"先补闭合再删重复"，
 *   无需手动补标签。半截标签（<think 未完整到达）从尾部剔除防转义文本露出。
 */
function parseAssistantBody(body) {
  const parts = [];
  const src = String(body == null ? "" : body);
  // 思考标签三种格式（与后端 StripThinking 支持格式对齐）：
  //    thinking / <thinking> / <|reasoning|>
  const THINK_RE = new RegExp("<(?:think|thinking|\\|reasoning\\|)>([\\s\\S]*?)(?:<(?:/(?:think|thinking)|\\|/reasoning\\|)>|$)", "g");
  // 工具调用/结果行（后端流式协议格式，见 agent.go）：
  //   "⚡ 执行工具: name"    → 工具调用行
  //   "✔ name: result"      → 工具结果行
  //   "✗ name: error"       → 工具失败行
  const TOOL_RE = /^[ \t]*(?:⚡[ \t]*执行工具[ \t]*[:：][ \t]*(.+?)[ \t]*$|✔[ \t]*([^:\n]{1,60}?)[ \t]*[:：][ \t]*(.*)$|✗[ \t]*([^:\n]{1,60}?)[ \t]*[:：][ \t]*(.*)$)/;
  // 工具行只能在思考块外识别：行内不含思考标签前缀（简单字符串判定，非
  // THINK_RE.test —— g 标志会推进 lastIndex 导致非幂等）
  const isThinkLine = ln => /<(?:think|thinking|\|reasoning\|)/.test(ln);

  // 逐行扫描：工具行 → 独立 tool 段；其他行 → 累积 text 段
  let inThink = false;
  const textBuf = [];
  const flushText = () => {
    if (textBuf.length) {
      const joined = textBuf.join("\n").trim();
      textBuf.length = 0;
      if (joined) parts.push({ type: "text", content: joined });
    }
  };
  for (const ln of src.split("\n")) {
    if (isThinkLine(ln)) inThink = true;
    if (inThink) {
      textBuf.push(ln);
      if (/<\/(?:think|thinking)>|<\|\/reasoning\|>/.test(ln)) inThink = false;
      continue;
    }
    const m = TOOL_RE.exec(ln);
    if (m) {
      flushText();
      if (m[1] != null) parts.push({ type: "tool", kind: "call", name: m[1].trim(), content: "" });
      else if (m[2] != null) parts.push({ type: "tool", kind: "ok", name: m[2].trim(), content: m[3].trim() });
      else parts.push({ type: "tool", kind: "err", name: m[4].trim(), content: m[5].trim() });
    } else {
      textBuf.push(ln);
    }
  }
  flushText();

  // 思考标签解析（只处理 text 段）
  const out = [];
  for (const p of parts) {
    if (p.type !== "text") { out.push(p); continue; }
    let last = 0, m;
    while ((m = THINK_RE.exec(p.content)) !== null) {
      const seg = p.content.slice(last, m.index).trim();
      if (seg) out.push({ type: "text", content: seg });
      out.push({
        type: "think",
        content: m[1],
        closed: /(?:<\/(?:think|thinking)>|<\|\/reasoning\|>)$/.test(m[0]),
      });
      last = m.index + m[0].length;
    }
    let tail = p.content.slice(last);
    const li = tail.lastIndexOf("<");
    if (li >= 0) {
      const frag = tail.slice(li);
      const targets = ["think", "thinking", "|reasoning|"];
      if (frag === "<" || targets.some(t => frag.length <= t.length + 1 && t.startsWith(frag.slice(1)))) tail = tail.slice(0, li);
    }
    if (tail.trim()) out.push({ type: "text", content: tail.trim() });
  }
  return out;
}

/* ── AI 回答一键复制 ──
 * 两种模式（用户诉求）：
 *   ① 复制 Markdown 原文：取 bubble.dataset.raw（后端下发/历史加载的原始文本），
 *      保留标题、列表、代码围栏、表格等 Markdown 语法，粘贴到编辑器即原样可用；
 *      剔除 <thinking> 思考块（模型内部推理，不属答案正文）。
 *   ② 复制纯文本：取渲染后 DOM 的 textContent（代码高亮标签已剥离、HTML 实体已解码），
 *      适合粘贴到聊天框/文档，不含任何标记符号。
 * 悬挂方式：assistant 行右上角悬浮按钮组（hover 才显示，避免干扰阅读），
 * 同时右键菜单也提供这两项（入口互补）。 */
function attachCopyActions(row, bubble) {
  const bar = document.createElement("div");
  bar.className = "msg-copy-bar";
  const mkBtn = (label, title, fn) => {
    const b = document.createElement("button");
    b.className = "msg-copy-btn";
    b.type = "button";
    b.textContent = label;
    b.title = title;
    b.onclick = (e) => { e.stopPropagation(); fn(b); };
    return b;
  };
  bar.appendChild(mkBtn("📄 MD", "复制 Markdown 原文（保留排版，不含思考过程）", (b) => copyText(msgMarkdown(bubble), b)));
  bar.appendChild(mkBtn("📋 文本", "复制纯文本（不含 Markdown 标记）", (b) => copyText(msgPlainText(bubble), b)));
  row.appendChild(bar);
}

// msgMarkdown 取 Markdown 原文并剔除思考块（与 parseAssistantBody 的 THINK_RE 对齐）。
function msgMarkdown(bubble) {
  const raw = bubble.dataset.raw || "";
  return raw
    .replace(/<(?:think|thinking|\|reasoning\|)>[\s\S]*?(?:<\/(?:think|thinking)>|<\|\/reasoning\|>|$)/g, "")
    .trim();
}

// msgPlainText 从渲染后 DOM 提取纯文本：优先正文段（.msg-content，HTML 实体已解码、
// 代码高亮标签已剥离），不含思考过程（.think-block，折叠态属"未选定内容"）；
// 无正文（纯工具卡）时取工具输出；再兜底整气泡文本。
function msgPlainText(bubble) {
  const segs = [];
  bubble.querySelectorAll(".msg-content").forEach(el => {
    const t = el.textContent.trim();
    if (t) segs.push(t);
  });
  if (segs.length) return segs.join("\n\n");
  bubble.querySelectorAll(".tool-body").forEach(el => {
    const t = el.textContent.trim();
    if (t) segs.push(t);
  });
  if (segs.length) return segs.join("\n");
  const clone = bubble.cloneNode(true);
  clone.querySelectorAll(".think-block, .cursor, .msg-copy-bar").forEach(el => el.remove());
  return (clone.textContent || "").trim();
}

/* 统一渲染 assistant 气泡内容（思考块默认折叠，点击展开，可查看实时思考）：
 *   - 流式逐帧调用：与上一帧【结构同构】时只更新内容（保留 DOM 节点、onclick
 *     与展开状态，不闪烁不破坏焦点——修复"点击展开/折叠受输出闪烁影响"）；
 *     结构变化（思考块闭合后出现正文 / 新增思考块）才重建，重建时恢复已展开块。
 *   - 思考中（未闭合）：思考块实时渲染（内容逐帧增长），点击可查看实时思考。
 * 注意：增量更新/重建后光标由调用方补回（renderStream）。 */
function fillAssistantBubble(bubble, text) {
  const parts = parseAssistantBody(String(text == null ? "" : text));
  const kids = Array.from(bubble.children).filter(el => !el.classList.contains("cursor"));
  // 结构同构 → 增量更新（只改内容，DOM 节点与点击状态原样保留，无闪烁）
  const same = kids.length === parts.length && kids.every((el, i) => {
    const t = parts[i].type;
    if (t === "think") return el.classList.contains("think-block");
    if (t === "tool") return el.classList.contains("tool-call") || el.classList.contains("tool-result");
    return el.classList.contains("msg-content");
  });
  if (same) {
    parts.forEach((p, i) => {
      const el = kids[i];
      if (p.type === "think") {
        const bodyEl = el.querySelector(".think-body");
        if (bodyEl.textContent !== p.content) bodyEl.textContent = p.content;
        el.classList.toggle("done", p.closed);
      } else if (p.type === "tool") {
        // 增量更新：只更新内容，保留折叠状态与点击绑定
        if (p.kind === "call") {
          const nameEl = el.querySelector(".tool-call-name");
          if (nameEl && nameEl.textContent !== p.name) nameEl.textContent = p.name;
        } else {
          const bodyEl = el.querySelector(".tool-body");
          if (bodyEl && bodyEl.textContent !== p.content) bodyEl.textContent = p.content;
          // 状态标记（成功/失败）变化时同步 class
          el.classList.toggle("tool-err", p.kind === "err");
        }
      } else {
        const html = mdToHTML(p.content);
        if (el.innerHTML !== html) el.innerHTML = html;
      }
    });
    return;
  }
  // 结构变化 → 重建；重建前记录已展开思考块/工具结果卡的正文指纹，
  // 重建后恢复展开状态
  const opened = [];
  bubble.querySelectorAll(".think-block.open .think-body")
    .forEach(el => opened.push(el.textContent));
  const openedTool = [];
  bubble.querySelectorAll(".tool-result.open .tool-body")
    .forEach(el => openedTool.push(el.textContent));
  bubble.innerHTML = "";
  for (const p of parts) {
    if (p.type === "think") {
      const tb = document.createElement("div");
      tb.className = "think-block" + (p.closed ? " done" : "");
      tb.innerHTML = `<div class="think-head"><span class="think-arrow">▶</span>
        <span class="think-label">思考过程</span></div>
        <div class="think-body"></div>`;
      tb.querySelector(".think-head").onclick = () => {
        tb.classList.toggle("open");
        // 点击思考块 = 主动交互：暂停流式自动下拉，避免视口跳动打断阅读/点击
        state.userScrolledUp = true;
        const btn = $("#scrollDown");
        if (btn) btn.classList.add("show");
        placeScrollDown();
      };
      tb.querySelector(".think-body").textContent = p.content;
      bubble.appendChild(tb);
    } else if (p.type === "tool") {
      bubble.appendChild(buildToolEl(p));
    } else {
      const c = document.createElement("div");
      c.className = "msg-content";
      c.innerHTML = mdToHTML(p.content);
      bubble.appendChild(c);
    }
  }
  if (opened.length > 0) {
    bubble.querySelectorAll(".think-block .think-body").forEach(el => {
      const t = el.textContent;
      if (opened.some(o => o === t || t.startsWith(o))) el.parentElement.classList.add("open");
    });
  }
  if (openedTool.length > 0) {
    bubble.querySelectorAll(".tool-result .tool-body").forEach(el => {
      const t = el.textContent;
      if (openedTool.some(o => o === t || t.startsWith(o))) el.parentElement.classList.add("open");
    });
  }
}

/* 构建工具调用/结果 DOM 元素（紧凑工具卡片）：
 *   - 工具调用行：.tool-call —— 行内样式（灰暗背景、等宽、青色强调），默认展开
 *   - 工具结果行：.tool-result —— 可折叠卡片（工具名标题 + 折叠内容），默认可折叠
 *   - 失败结果（✗）：加 .tool-err 红色强调
 * 供 fillAssistantBubble（重建路径）与 appendMessage（独立 tool 消息）共用。 */
function buildToolEl(p) {
  const name = p.name || "tool";
  if (p.kind === "call") {
    const div = document.createElement("div");
    div.className = "tool-call";
    const icon = document.createElement("span");
    icon.className = "tool-call-icon";
    icon.textContent = "⚡";
    const nameSpan = document.createElement("span");
    nameSpan.className = "tool-call-name";
    nameSpan.textContent = name;
    div.appendChild(icon);
    div.appendChild(nameSpan);
    return div;
  }
  const div = document.createElement("div");
  div.className = "tool-result" + (p.kind === "err" ? " tool-err" : "");
  const head = document.createElement("div");
  head.className = "tool-head";
  const arrow = document.createElement("span");
  arrow.className = "t-arrow";
  arrow.textContent = "▶";
  const icon = document.createElement("span");
  icon.className = "tool-result-icon";
  icon.textContent = p.kind === "err" ? "✗" : "✔";
  const nameSpan = document.createElement("span");
  nameSpan.className = "tool-result-name";
  nameSpan.textContent = name;
  head.appendChild(arrow);
  head.appendChild(icon);
  head.appendChild(nameSpan);
  head.onclick = () => div.classList.toggle("open");
  const body = document.createElement("div");
  body.className = "tool-body";
  body.textContent = p.content ? p.content : "（无输出）";
  div.appendChild(head);
  div.appendChild(body);
  return div;
}

function appendMessage(role, content, isStream, markPartial, images) {
  const list = $("#msgList");
  const empty = $("#emptyState");
  if (empty) empty.remove();

  const row = document.createElement("div");
  row.className = "msg-row " + role;
  const avatar = document.createElement("div");
  avatar.className = "msg-avatar";
  avatar.textContent = role === "user" ? "🧑" : role === "tool" ? "🛠" : "✦";
  const bubble = document.createElement("div");
  bubble.className = "msg-bubble";

  const body = content || "";
  // 原始文本留存（AI 回答一键复制用）：流式/历史/重绘三条路径都经此，
  // 复制 Markdown 原文时直接取 dataset.raw，无需从渲染后 DOM 反推。
  bubble.dataset.raw = body;

  if (role === "system") {
    // 系统提示样式（压缩摘要/系统事件）：虚线提示条，无头像气泡
    row.classList.add("sys");
    const c = document.createElement("div");
    c.className = "msg-system";
    c.textContent = body;
    bubble.appendChild(c);
  } else if (role === "assistant") {
    // 思考块默认折叠 + 点击展开（统一渲染：流式/最终/历史加载共用）
    fillAssistantBubble(bubble, body);
  } else if (role === "tool" || body.startsWith("[TOOL]")) {
    // 工具消息（历史加载/独立 tool 消息）：解析 ⚡/✔/✗ 行渲染为工具卡片
    // （与流式 assistant 内的工具行同一套 buildToolEl 渲染，样式统一）
    const tbody = role === "tool" ? body : body.replace(/^\[TOOL\]\s*/, "");
    const toolParts = parseAssistantBody(tbody).filter(p => p.type === "tool");
    if (toolParts.length > 0) {
      for (const tp of toolParts) bubble.appendChild(buildToolEl(tp));
      // 非工具行残留文本（如旧格式纯文本）追加为正文
      const textOnly = parseAssistantBody(tbody).filter(p => p.type === "text" || p.type === "think");
      for (const tp of textOnly) {
        const c = document.createElement("div");
        c.className = "msg-content";
        c.innerHTML = tp.type === "think"
          ? `<div class="think-block done"><div class="think-head"><span class="think-arrow">▶</span><span class="think-label">思考过程</span></div><div class="think-body"></div></div>`
          : mdToHTML(tp.content);
        if (tp.type === "think") c.querySelector(".think-body").textContent = tp.content;
        bubble.appendChild(c);
      }
    } else {
      // 无工具行（历史旧格式）：回退为 tool-result 卡片（与流式工具结果卡同款样式）
      const nameMatch = tbody.match(/^([^\s:：]{1,40})[:：]/);
      const name = nameMatch ? nameMatch[1] : "tool";
      const card = document.createElement("div");
      card.className = "tool-result open";
      card.innerHTML = `<div class="tool-head"><span class="t-arrow">▶</span><span class="tool-result-icon">🛠</span><span class="tool-result-name"></span></div><div class="tool-body"></div>`;
      card.querySelector(".tool-result-name").textContent = name;
      card.querySelector(".tool-body").textContent = tbody;
      card.querySelector(".tool-head").onclick = () => card.classList.toggle("open");
      bubble.appendChild(card);
    }
  } else {
    const c = document.createElement("div");
    c.className = "msg-content";
    c.innerHTML = mdToHTML(body);
    bubble.appendChild(c);
  }

  // 对话记录图片预览（后端 messagesToJSON 输出的 images: [dataURI,...]）：
  // 历史消息重绘/历史加载时渲染小图预览；点击放大查看原图。
  if (Array.isArray(images) && images.length && !isStream) {
    const pv = document.createElement("div");
    pv.className = "msg-imgs";
    for (const url of images) {
      if (!url) continue;
      const img = document.createElement("img");
      img.className = "msg-img-pv";
      img.src = url;
      img.loading = "lazy";
      img.alt = "对话图片";
      img.onclick = () => showImagePreview(url);
      pv.appendChild(img);
    }
    if (pv.children.length) bubble.appendChild(pv);
  }

  row.appendChild(avatar);
  row.appendChild(bubble);
  // AI 回答一键复制：两种模式（Markdown 原文 / 纯文本），右上角悬浮按钮。
  // 仅 assistant 消息（含工具卡/思考块）挂载；用户/系统消息不挂。
  if (role === "assistant") attachCopyActions(row, bubble);
  // 右键：复制/清空
  row.oncontextmenu = (e) => {
    e.preventDefault();
    const text = row.querySelector(".msg-content, .tool-body")?.textContent || body;
    const items = [];
    if (role === "assistant") {
      items.push({ label: "📄 复制 Markdown 原文", fn: () => copyText(msgMarkdown(bubble)) });
      items.push({ label: "📋 复制纯文本", fn: () => copyText(msgPlainText(bubble)) });
    } else {
      items.push({ label: "📋 复制文本", fn: () => copyText(text) });
    }
    items.push({ label: "🗑 清空会话显示", danger: true, fn: () => { renderMessages([]); appendSystem("已清空显示"); } });
    showContextMenu(e.clientX, e.clientY, items);
  };
  list.appendChild(row);

  // partial 标记：重绘时渲染的在途内容（生成中 loadSession 合并返回），
  // 后续 agent.stream 到达时由 beginStream 复用该气泡继续流式填充
  // （刷新/历史恢复场景防重复气泡；无后续流时保持为普通消息，内容不丢）
  if (markPartial) bubble.setAttribute("data-partial", "1");

  if (isStream) {
    row.classList.add("streaming");
    state.streamMsg = bubble;
    const cur = document.createElement("span");
    cur.className = "cursor";
    bubble.appendChild(cur);
  }
  return bubble;
}

function appendSystem(text) {
  const list = $("#msgList");
  const empty = $("#emptyState");
  if (empty) empty.remove();
  const d = document.createElement("div");
  d.className = "msg-system";
  d.textContent = text;
  list.appendChild(d);
  // 非强制：用户上翻阅读历史时不打断位置
  scrollBottom();
}

/* ═══════════ Markdown ═══════════ */
const HIGHLIGHT = {
  go: [
    [/\b(func|package|import|return|if|else|for|range|go|defer|select|case|switch|struct|interface|map|chan|type|var|const|break|continue|fallthrough|default)\b/g, "kw"],
    [/\b(int|int64|int32|string|bool|byte|rune|float64|float32|uint|error|any|nil|true|false|len|cap|make|new|append|copy|delete|close)\b/g, "type"],
    [/\/\/[^\n]*/g, "cmt"], [/"(\\.|[^"\\])*"|`[^`]*`|'(\\.|[^'\\])*'/g, "str"],
  ],
  python: [
    [/\b(def|class|import|from|return|if|elif|else|for|while|try|except|finally|with|as|lambda|pass|break|continue|yield|global|nonlocal|raise|assert|async|await|in|is|not|and|or)\b/g, "kw"],
    [/\b(True|False|None|self|print|len|range|str|int|float|list|dict|set|tuple)\b/g, "type"],
    [/#[^\n]*/g, "cmt"], [/"""[\s\S]*?"""|'''[\s\S]*?'''|"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'/g, "str"],
  ],
  js: [
    [/\b(const|let|var|function|return|if|else|for|while|do|switch|case|break|continue|new|typeof|instanceof|class|extends|super|import|export|from|async|await|try|catch|finally|throw|yield|delete|in|of)\b/g, "kw"],
    [/\b(true|false|null|undefined|this|NaN|Infinity|console|Math|JSON|Promise|window|document)\b/g, "type"],
    [/\/\/[^\n]*|\/\*[\s\S]*?\*\//g, "cmt"], [/"(\\.|[^"\\])*"|'(\\.|[^'\\])*'|`[^`]*`/g, "str"],
  ],
  bash: [
    [/\b(if|then|else|fi|for|while|do|done|case|esac|function|return|export|local|echo|cd|ls|mkdir|rm|cp|mv|grep|sed|awk|cat|touch|chmod|sudo|git|go|npm|pip)\b/g, "kw"],
    [/#[^\n]*/g, "cmt"], [/"[^"\n]*"|'[^'\n]*'/g, "str"],
  ],
  json: [
    [/"(\\.|[^"\\])*"(?=\s*:)/g, "key"], [/"(\\.|[^"\\])*"(?!\s*:)/g, "str"],
    [/\b(true|false|null)\b/g, "kw"], [/-?\b\d+(\.\d+)?([eE][+-]?\d+)?\b/g, "num"],
  ],
  yaml: [
    [/^[ \t]*[A-Za-z_][\w-]*:/gm, "key"], [/#[^\n]*/g, "cmt"],
    [/"(\\.|[^"\\])*"|'[^'\n]*'/g, "str"], [/\b(true|false|null|yes|no|on|off)\b/g, "kw"],
  ],
  diff: [
    [/^\+[^\n]*/gm, "add"], [/^-[^\n]*/gm, "del"], [/^@@[^\n]*/gm, "meta"], [/^diff[^\n]*/gm, "meta"],
  ],
  default: [
    [/\/\/[^\n]*|#[^\n]*/g, "cmt"], [/"(\\.|[^"\\])*"|'(\\.|[^'\\])*'|`[^`]*`/g, "str"],
  ],
};
const HL_COLOR = {
  kw: "#c792ea", type: "#82aaff", str: "#c3e88d", cmt: "#5b6287",
  num: "#f78c6c", key: "#89ddff", add: "#c3e88d", del: "#f07178", meta: "#ffcb6b",
};

function highlightCode(code, lang) {
  const rules = HIGHLIGHT[lang] || HIGHLIGHT.default;
  let html = esc(code);
  for (const [re, cls] of rules) {
    html = html.replace(re, (m) => {
      if (m.includes("<span")) return m;
      return `<span style="color:${HL_COLOR[cls]}">${m}</span>`;
    });
  }
  return html;
}

function langOf(fence) {
  const m = String(fence || "").toLowerCase().trim();
  if (!m) return "";
  if (m.includes("go")) return "go";
  if (m.includes("python") || m.includes("py")) return "python";
  if (m.includes("javascript") || m.includes("js")) return "js";
  if (m.includes("typescript") || m.includes("ts")) return "js";
  if (m.includes("bash") || m.includes("sh") || m.includes("shell")) return "bash";
  if (m.includes("json")) return "json";
  if (m.includes("yaml") || m.includes("yml")) return "yaml";
  if (m.includes("diff")) return "diff";
  return m.split(/\s+/)[0] || "";
}

// PlantUML 预览配置（从 config.get 缓存读取；未加载时用与后端一致的默认值）
function plantumlPreviewCfg() {
  const v = state.cfgValues || {};
  const server = v["preview.plantuml_server"] || "https://www.plantuml.com/plantuml";
  const enabled = v["preview.plantuml_enabled"] !== false; // 显式 false 才禁用
  return { server, enabled };
}

// JS 字符串转义（嵌入 onerror 参数用）
// 双层编码各司其职：
//   &  → &amp;   （首位：HTML 实体层，防源码含实体形态文本时被解码变异；
//                  replace 一次性遍历，产物 &amp;/&quot; 中的 & 不再被处理）
//   "  → &quot;  （onerror 属性用双引号包裹，裸 " 会截断属性 + JS 不闭合；
//                  PlantUML 中 participant "Name"/title "..." 极常见）
//   '  → \'      （JS 字符串用单引号包裹）
//   \r/\n → 字面转义（JS 字符串内换行非法）
// 实体经 HTML 解码回到原字符，在 JS 单引号字符串内无害。
function jsStr(s) {
  return String(s).replace(/&/g, "&amp;").replace(/\\/g, "\\\\").replace(/'/g, "\\'").replace(/"/g, "&quot;").replace(/\r/g, "\\r").replace(/\n/g, "\\n");
}

// PlantUML 渲染失败兜底：img 替换为错误提示 + 可折叠源码（不阻断阅读）
function plantumlFallback(img, code) {
  const d = document.createElement("div");
  d.className = "plantuml-fallback";
  const msg = document.createElement("div");
  msg.innerHTML = "⚠ PlantUML 渲染失败：请检查 <code>preview.plantuml_server</code> 配置（⚙ 配置面板 → 预览）或服务器可用性";
  d.appendChild(msg);
  const det = document.createElement("details");
  det.innerHTML = `<summary>查看源码</summary><pre><code>${esc(code)}</code></pre>`;
  d.appendChild(det);
  img.replaceWith(d);
}

// PlantUML 渲染块（占位符回填用）：源码用原文渲染——
// 若先 esc 再编码，> " 等字符会被转义污染 plantuml 语法，后端渲染错误图。
function renderPlantUMLBlock(code) {
  const pv = plantumlPreviewCfg();
  const trimmed = code.replace(/\n$/, "");
  if (!pv.enabled) {
    return `<pre data-lang="plantuml"><button class="copy-btn" onclick="copyCode(this)">复制</button><code>${highlightCode(trimmed, "")}</code></pre>`;
  }
  const src = encodeURIComponent(code);
  return `<div class="plantuml-block"><img class="plantuml-img" src="/api/plantuml/svg?src=${src}" loading="lazy" alt="PlantUML 图" style="min-height:18px" onerror="plantumlFallback(this,'${jsStr(trimmed)}')"><details class="plantuml-src"><summary>PlantUML 源码</summary><pre><code>${highlightCode(trimmed, "")}</code></pre></details></div>`;
}

function mdToHTML(text) {
  // PlantUML 代码块先用原文提取为占位符（跳过 esc，防止 > " 等被转义污染
  // 源码）；其余 Markdown 处理完成后回填。占位符用不可见字符，esc 不转义，
  // 段落包裹后两种形态（裸 / 被 <p> 包裹）统一回填。
  const puBlocks = [];
  let t = String(text == null ? "" : text).replace(/```(?:plantuml|puml)\s*\n([\s\S]*?)```/g, (_m, code) => {
    const ph = "\u0000PU" + puBlocks.length + "\u0000";
    puBlocks.push(code);
    return ph;
  });
  // 裸 PlantUML 块（无 ```plantuml 围栏，行首 @startuml 至 @enduml）：
  // 流程图类 md 常见写法（如"管理端流程图\n@startuml ... @enduml"），
  // 同样提取为占位符走渲染路径。围栏内的 @startuml 已被上面替换为
  // 占位符，此处不会重复匹配。
  t = t.replace(/(^|\n)@startuml[\s\S]*?@enduml/g, (_m) => {
    const ph = "\u0000PU" + puBlocks.length + "\u0000";
    puBlocks.push(_m.trim());
    return ph;
  });
  t = esc(t);
  t = t.replace(/```([\w+-]*)\n([\s\S]*?)```/g, (_m, lang, code) => {
    const l = langOf(lang);
    const trimmed = code.replace(/\n$/, "");
    return `<pre data-lang="${esc(l)}"><button class="copy-btn" onclick="copyCode(this)">复制</button><code>${highlightCode(trimmed, l)}</code></pre>`;
  });
  t = t.replace(/`([^`\n]+)`/g, "<code>$1</code>");
  t = t.replace(/\*\*([^*\n]+)\*\*/g, "<strong>$1</strong>");
  t = t.replace(/((?:^\|.*\|\s*$\n?)+)/gm, (block) => {
    const rows = block.trim().split("\n").filter(Boolean);
    if (rows.length < 2) return block;
    const html = rows.map((r, i) => {
      const cells = r.replace(/^\||\|$/g, "").split("|").map(c => c.trim());
      if (i === 1 && cells.every(c => /^:?-{3,}:?$/.test(c))) return "";
      const tag = i === 0 ? "th" : "td";
      return `<tr>${cells.map(c => `<${tag}>${c}</${tag}>`).join("")}</tr>`;
    }).filter(Boolean).join("");
    return `<table>${html}</table>`;
  });
  t = t.replace(/^[\s]*[-*] (.+)$/gm, "<li>$1</li>");
  t = t.replace(/((?:<li>.*<\/li>\n?)+)/g, (b) => `<ul>${b}</ul>`);
  t = t.replace(/^\s*(\d+)\. (.+)$/gm, "<li>$2</li>");
  t = t.replace(/^### (.+)$/gm, "<h3>$1</h3>");
  t = t.replace(/^## (.+)$/gm, "<h2>$1</h2>");
  t = t.replace(/^# (.+)$/gm, "<h1>$1</h1>");
  t = t.replace(/^&gt; (.+)$/gm, (m, c) => `<blockquote>${c}</blockquote>`);
  t = t.replace(/^---+$/gm, "<hr>");
  // 图片渲染：本地相对路径 → /api/fs/img?path= 占位（后端无路由时 alt 兜底），
  // 远程 http(s) 图片直接加载。
  t = t.replace(/!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)/g, (m, alt, src) => {
    const a = esc(alt || src);
    if (/^https?:\/\//i.test(src)) {
      return `<img src="${esc(src)}" alt="${a}" loading="lazy" style="max-width:100%;min-height:18px;border-radius:8px;margin:4px 0;">`;
    }
    return `<img src="/api/fs/img?path=${encodeURIComponent(src)}" alt="${a}" loading="lazy" style="max-width:100%;min-height:18px;border-radius:8px;margin:4px 0;" onerror="this.remove()">`;
  });
  t = t.replace(/\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)/g,
    '<a href="$2" target="_blank" rel="noopener">$1</a>');
  t = t.split(/\n{2,}/).map(p => {
    const s = p.trim();
    if (!s) return "";
    if (/^(<[hul]|<table|<\/table|<pre|<blockquote|<hr|<div)/.test(s)) return s;
    return `<p>${s.replace(/\n/g, "<br>")}</p>`;
  }).join("");
  // 回填 plantuml 块（原文渲染）：三种形态统一处理——
  //   ① 占位符前有文本被段落包裹：<p>文本<br>\u0000PU0\u0000</p>
  //   ② 裸占位符：\u0000PU0\u0000
  //   ③ 占位符单独成段：<p>\u0000PU0\u0000</p>
  // ①② 拆出 p 包裹（div 不能嵌在 p 内，浏览器会错误重组）。
  t = t.replace(/<p>(.*?)<br>\u0000PU(\d+)\u0000<\/p>/g, (_m, prefix, i) => `<p>${prefix}</p>${renderPlantUMLBlock(puBlocks[+i])}`);
  t = t.replace(/<p>\u0000PU(\d+)\u0000<\/p>|\u0000PU(\d+)\u0000/g,
    (_m, a, b) => renderPlantUMLBlock(puBlocks[+(a != null ? a : b)]));
  return t;
}

function copyCode(btn) {
  const code = btn.parentElement.querySelector("code").textContent;
  copyText(code, btn);
}

function copyText(text, btn) {
  const ok = () => {
    if (btn) {
      const orig = btn.dataset.origLabel || btn.textContent;
      btn.dataset.origLabel = orig;
      btn.textContent = "已复制";
      btn.classList.add("copied");
      setTimeout(() => { btn.textContent = orig; btn.classList.remove("copied"); }, 1600);
    } else {
      toast("已复制");
    }
  };
  const fail = () => toast("复制失败", "err");
  // 剪贴板 API 在非安全上下文/未聚焦时可能不可用 → 回退 execCommand
  if (navigator.clipboard && window.isSecureContext !== false) {
    navigator.clipboard.writeText(text).then(ok).catch(() => fallbackCopy(text) ? ok() : fail());
  } else {
    fallbackCopy(text) ? ok() : fail();
  }
}

// fallbackCopy 兼容回退：临时 textarea + execCommand('copy')
function fallbackCopy(text) {
  try {
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    const ok = document.execCommand("copy");
    ta.remove();
    return ok;
  } catch (_) { return false; }
}

/* ═══════════ 流式 ═══════════ */
function beginStream(streamId) {
  // 记录流归属会话：endStream 时按此过滤旧会话的 done 事件
  state.streamSessionId = curSession();
  // 记录流唯一标识：同会话的新旧流据此区分，旧流残留事件被丢弃
  if (streamId != null) state.streamId = streamId;
  // 增量渲染状态重置：新流/复用 partial 气泡都从干净状态开始（首帧收编或重建）
  state.sr = null;
  // 幂等置位生成中状态（即使 chat.send 响应丢失/时序异常，只要有流事件
  // 到达即进入 streaming，endStream 必然重置——防"无法停止/无法追加"卡死）
  state.streaming = true;
  // 重绘已渲染在途内容（data-partial 气泡：刷新/历史恢复等场景）→ 复用该
  // 气泡继续流式填充，避免与新建流式气泡重复（"点击会话后只能看到新生成
  // 的"根治；无 partial 时正常新建气泡）
  const list = $("#msgList");
  const last = list.lastElementChild;
  const existing = last && last.querySelector(".msg-bubble[data-partial]");
  if (existing) {
    state.streamMsg = existing;
    existing.removeAttribute("data-partial");
    const row = existing.parentElement;
    if (row) row.classList.add("streaming");
  } else {
    appendMessage("assistant", "", true);
  }
  // 图标与功能同步：beginStream 不一定由 sendMessage 发起（多轮自动化续跑
  // 由后端直接推送 agent.stream 触发），上一轮 agent.done 的 endStream 已把
  // 按钮切回"发送箭头"，这里必须切回"停止方块"——否则图标=发送、功能=停止
  // （streaming=true 时点击执行 chat.stop）不一致（历史缺陷）。
  setSendBtn(true);
  setAgentStatus("● 生成中", true);
}

function renderStream() {
  if (!state.streamMsg) return;
  // 时间节流（RAF 之上）：流式增量最多 ~10fps。旧实现每帧(60fps)全量重建，
  // 长回复时主线程被打满 → 整窗卡死（按钮/中断全部无响应）。endStream 有
  // 最终全量渲染兜底，节流不会丢内容。
  const now = Date.now();
  if (now - state.lastStreamRenderT < 100) return;
  state.lastStreamRenderT = now;
  renderStreamIncremental();
  // 同步 Markdown 原文（生成中也能一键复制当前已输出内容）
  state.streamMsg.dataset.raw = state.streamBuf;
  // 增量路径不清空 DOM：先移除旧光标再补回（endStream 须清全部）
  state.streamMsg.querySelectorAll(".cursor").forEach(c => c.remove());
  const cur = document.createElement("span");
  cur.className = "cursor";
  state.streamMsg.appendChild(cur);
  scrollBottom();
}

/* 流式增量渲染（GUI 卡死根治）：
 * 旧路径每帧 fillAssistantBubble 全量重建 —— bubble.innerHTML="" + 全量
 * parseAssistantBody + mdToHTML + 全部 DOM 销毁重建，成本 O(全文累计长度)；
 * 长回复（几 KB~几十 KB markdown/工具卡）× 60fps RAF → 布局与重绘风暴，
 * 主线程饱和 → 整窗冻结（"卡死一直不响应也不中断"主根因）。
 * 增量策略：流是 append-only，parse 出的 parts 除最后一段外全部不可变——
 *   - 已定稿段：只渲染一次，后续帧零成本；
 *   - 活动尾段：原位更新（不复用旧节点内容指纹，内容未变直接跳过）；
 *   - 异常防御：parts 收缩/结构不齐时回退全量重建（自愈）。
 * sr 与 streamMsg 同生命周期：endStream 置 null；切换会话恢复场景
 * （fillAssistantBubble 预渲染后接续流）首帧自动收编已有子节点。 */
function renderStreamIncremental() {
  const bubble = state.streamMsg;
  if (!bubble) return;
  const parts = parseAssistantBody(state.streamBuf);
  let sr = state.sr;
  if (!sr) {
    // 首帧：气泡已有同构子节点（partial 恢复/切换会话）→ 收编，避免重渲染
    const existing = [...bubble.children].filter(el => !el.classList.contains("cursor") && !el.classList.contains("msg-error"));
    if (parts.length > 0 && existing.length === parts.length) {
      sr = state.sr = { parts, final: parts.length - 1, nodes: existing, fills: parts.map(srPartFP) };
      // 尾段强制刷新一次：已有节点可能落后最新 delta（partial 恢复/切换会话
      // 场景先按旧缓冲渲染，随后 delta 到达才走增量）——重置尾段指纹强制重绘
      sr.fills[parts.length - 1] = null;
      srFillNode(sr, parts.length - 1);
      return;
    }
    // 回退全量重建：先清掉旧渲染子节点（段数不匹配时直接追加会产生重复内容
    // ——partial 恢复后缓冲又增长的场景），一次性成本可接受，此后转增量
    for (const el of existing) el.remove();
    sr = state.sr = { parts: [], final: 0, nodes: [], fills: [] };
  }
  if (parts.length < sr.parts.length) {
    // append-only 被破坏（缓冲重置等）→ 防御性全量重建
    state.sr = null;
    fillAssistantBubble(bubble, state.streamBuf);
    return;
  }
  sr.parts = parts;
  // 定稿：除最后一段外全部落定（内容不可变，渲染一次）
  while (sr.final < parts.length - 1) {
    srEnsureNode(sr, sr.final, bubble);
    srFillNode(sr, sr.final);
    sr.final++;
  }
  // 活动尾段：每帧原位更新（指纹未变则跳过，保留用户折叠/展开状态）
  if (parts.length > 0) {
    srEnsureNode(sr, parts.length - 1, bubble);
    srFillNode(sr, parts.length - 1);
  }
}

function srPartFP(p) {
  return p.type + "|" + (p.name || "") + "|" + (p.closed ? "c" : "o") + "|" + (p.content || "").length + "|" + (p.content || "").slice(-64);
}

function srEnsureNode(sr, i, bubble) {
  if (sr.nodes[i]) return;
  const p = sr.parts[i];
  let node;
  if (p.type === "text") {
    node = document.createElement("div");
    node.className = "msg-content";
  } else if (p.type === "think") {
    node = document.createElement("div");
    node.className = "think-block" + (p.closed ? " done" : "");
    node.innerHTML = `<div class="think-head"><span class="think-arrow">▶</span>
        <span class="think-label">思考过程</span></div>
        <div class="think-body"></div>`;
    node.querySelector(".think-head").onclick = () => {
      node.classList.toggle("open");
      // 点击思考块 = 主动交互：暂停流式自动下拉（与全量路径行为一致）
      state.userScrolledUp = true;
      const btn = $("#scrollDown");
      if (btn) btn.classList.add("show");
      placeScrollDown();
    };
  } else {
    node = buildToolEl(p);
  }
  bubble.appendChild(node);
  sr.nodes[i] = node;
}

function srFillNode(sr, i) {
  const p = sr.parts[i];
  const fp = srPartFP(p);
  if (sr.fills[i] === fp) return; // 指纹未变：跳过（保折叠态，零成本）
  sr.fills[i] = fp;
  if (p.type === "tool") {
    const fresh = buildToolEl(p);
    sr.nodes[i].replaceWith(fresh);
    sr.nodes[i] = fresh;
    return;
  }
  const node = sr.nodes[i];
  if (p.type === "text") {
    node.innerHTML = mdToHTML(p.content);
  } else {
    // think：只同步 done 类，不动 open（用户手动展开不被流式刷新冲掉）
    node.classList.toggle("done", !!p.closed);
    node.querySelector(".think-body").textContent = p.content;
  }
}

function endStream(data) {
  if (state.streamMsg) {
    const row = state.streamMsg.parentElement;
    if (row) row.classList.remove("streaming");
    // 清全部光标（增量更新可能残留多个，只删第一个会漏）
    state.streamMsg.querySelectorAll(".cursor").forEach(c => c.remove());
    state.sr = null; // 增量渲染状态随流生命周期结束（最终渲染走全量路径）
    if (data.text != null) fillAssistantBubble(state.streamMsg, data.text);
    // 同步一键复制的 Markdown 原文：done 带最终文本用 data.text，interrupted
    // 等无 text 的收尾用当前流缓冲（半截内容也能复制，不丢）
    state.streamMsg.dataset.raw = (data.text != null ? data.text : state.streamBuf) || state.streamMsg.dataset.raw || "";
    if (data.error) {
      const err = document.createElement("div");
      err.className = "msg-error";
      err.textContent = "错误: " + data.error;
      state.streamMsg.parentElement.appendChild(err);
      toast("生成出错", "err");
    }
    state.streamMsg = null;
    state.streamBuf = "";
    state.streamSessionId = null;
    state.streamId = null;
  }
  // 兜底重置：无论流以何种方式结束（done/error/interrupted），都必须
  // 恢复可发送状态（历史 bug：异常路径漏重置 → streaming 卡死，用户无法
  // 停止对话、无法追加发送）
  state.streaming = false;
  state.streamSessionId = null;
  state.streamId = null;
  setSendBtn(false);
  setAgentStatus("", false);
  if (data && data.sessionId) {
    state.sessionId = data.sessionId;
    // 确保会话 tab 存在
    if (!state.tabs.find(t => t.kind === "chat" && t.id === data.sessionId)) {
      const sess = state.sessions.find(s => s.uuid === data.sessionId);
      state.tabs.push({ kind: "chat", id: data.sessionId, title: sess ? sess.title : "会话", projDir: curProjectDir() });
    }
    state.activeTab = data.sessionId;
    renderTabs();
  }
  scheduleSessionsReload();
  refreshStatus();
  refreshGoalStatus();
  // 流结束不再强制下拉：用户上翻阅读历史时保持位置（仅用户位于底部才跟随）
  scrollBottom();
}

/* ═══════════ 发送 ═══════════ */
// stopStream 停止当前会话正在进行的流：记录其 streamId 到 stoppedStreams
// （残留事件一律丢弃，杜绝"停下后诡异再次触发"），乐观重置前端流状态，
// 再请求后端取消。后端随后到达的 interrupted done 只做清理，不再驱动渲染。
function stopStream(sid) {
  const targetSid = sid || state.sessionId;
  const bs = targetSid ? state.backgroundStreams[targetSid] : null;
  const hasFront = state.streaming && (!targetSid || targetSid === state.sessionId);
  const hasBg = !!(bs && bs.streaming);
  // 停止请求在途标记（仅当该会话确有在途流）：在收到该会话 done 之前，
  // 其残留事件一律丢弃（覆盖"首 delta 未到就按停止"的竞态窗口）。
  // 会话本无在途流时不标记——否则会永久屏蔽该会话后续新流。
  if (targetSid && (hasFront || hasBg)) {
    state.stoppedSessions.set(targetSid, true);
    // 兜底：done 若丢失，10s 后自动解除，避免该会话被永久屏蔽
    setTimeout(() => state.stoppedSessions.delete(targetSid), 10000);
  }
  // 记录前台流标识（若存在），供事件层丢弃其残留 delta/done
  const marked = [];
  const frontStreamId = state.streamId;
  if (frontStreamId != null) { state.stoppedStreams.set(frontStreamId, targetSid); marked.push(frontStreamId); }
  // 同会话后台缓冲的流也一并标记（用户可能停在后台流所属会话）
  if (bs && bs.streamId != null) { state.stoppedStreams.set(bs.streamId, targetSid); marked.push(bs.streamId); }
  // 乐观重置：UI 立即回到可发送态（不等后端 done，避免"难以停下"）。
  // 仅当成功标记了旧流（有 streamId 可精确过滤）时才乐观重置；无 streamId
  // 的旧格式流无法区分新旧事件，保留 streaming 由 done 收尾（兼容语义）。
  if ((!targetSid || targetSid === state.sessionId) && marked.length > 0) {
    state.streaming = false;
    state.streamMsg = null;
    state.streamBuf = "";
    state.streamSessionId = null;
    state.streamId = null;
    state.sr = null;
    setSendBtn(false);
    setAgentStatus("", false);
  }
  if (bs) delete state.backgroundStreams[targetSid];
  req("chat.stop", { sessionId: targetSid, streamId: frontStreamId != null ? frontStreamId : undefined }).catch(() => {});
  // 标记防无限增长：60s 后清理本次标记（后端 interrupted done 通常秒级到达，
  // 届时事件层已自行删除对应标记；此兜底只兜"done 丢失"的极端场景）。
  if (marked.length) {
    setTimeout(() => { for (const id of marked) state.stoppedStreams.delete(id); }, 60000);
  }
}

async function sendMessage() {
  const input = $("#input");
  const text = input.value.trim();
  // 纯文本为空但有附件（如只传图片）也允许发送——图片进对话记录的前置条件；
  // 后端 chat.send 同样支持空 input + 附件
  if (!text && state.attachments.length === 0) return;

  // 生成中发送 = "停止当前生成并追加发送"（历史缺陷修复：此前直接 return，
  // 用户无法在对话进行中追加消息；停止后已产出片段由后端写回会话历史）
  if (state.streaming) {
    const oldStreamId = state.streamId;
    // stopStream 乐观重置 UI + 标记旧 streamId（其残留事件被丢弃，不会把
    // 刚发出的新流再次打断/重触发）
    stopStream(state.sessionId);
    // 等待后端确认停止（旧流 done 到达 → stoppedStreams 标记被清理）：
    // 后端在发 done 前会把已产出的半截回复写入本会话历史，必须等它完成，
    // 否则新 user 消息会先于旧 assistant 片段写入，历史顺序错乱。
    // 最多 5s 兜底（后端异常时不卡死输入）。
    if (oldStreamId != null) {
      const t0 = Date.now();
      while (state.stoppedStreams.has(oldStreamId) && Date.now() - t0 < 5000) {
        await new Promise(r => setTimeout(r, 60));
      }
    } else {
      // 兼容无 streamId 的旧格式：按原语义等待 done 把 streaming 置 false
      const t0 = Date.now();
      while (state.streaming && Date.now() - t0 < 5000) {
        await new Promise(r => setTimeout(r, 60));
      }
      if (state.streaming) {
        state.streaming = false;
        state.streamMsg = null;
        state.streamSessionId = null;
        state.streamId = null;
        setSendBtn(false);
        setAgentStatus("", false);
      }
    }
  }

  if (text.startsWith("/")) {
    if (await handleCommand(text)) { input.value = ""; autoGrow(); return; }
  }

  // 无当前会话：先新建（后端也兜底自动创建，但前端主动建可拿到有效
  // sessionId 用于 tab/列表跟踪——修复多轮对话"第二次找不到用户消息"）
  if (!state.sessionId) await newSession();

  // 图片附件同步预览：发送瞬间用户气泡直接显示图片
  // （后端 chat.send 会以 [图片×N] 计数 + messagesToJSON images 输出，
  //   但那是后端写历史后的回读；此处即时渲染，无需等回读）
  const sendImgs = state.attachments.filter(a => a.content && String(a.content).startsWith("data:image/")).map(a => a.content);
  appendMessage("user", text, false, false, sendImgs);
  // 新一轮对话开始：清空上一轮子任务面板（子任务为中间态，与 TUI 发送前
  // m.subTasks = nil 同语义——防止旧任务残留导致新任务不断累加）
  clearSubTasks();
  // 附件信息（真实内容；超大文件 content 为空，仅传 name）
  const atts = state.attachments.map(a => ({ name: a.name, content: a.content || "" }));
  input.value = "";
  autoGrow();
  clearAttachments();

  state.streaming = true;
  setSendBtn(true);
  try {
    await req("chat.send", { input: text, sessionId: state.sessionId, attachments: atts });
    // 首次对话后端自动更新会话标题（取第一个问题）→ 立即刷新会话列表显示
    lastSessionsSig = "";
    loadSessions();
  } catch (e) {
    toast("发送失败: " + msgOf(e), "err");
    state.streaming = false;
    setSendBtn(false);
    setAgentStatus("", false);
  }
}

const CMD_LIST = [
  { name: "/mode <chat|task|goal>", desc: "切换工作模式" },
  { name: "/new", desc: "新建会话" },
  { name: "/clear", desc: "清空当前显示" },
  { name: "/stop", desc: "停止生成" },
  { name: "/ctx", desc: "查看上下文占用" },
  { name: "/compact", desc: "压缩上下文" },
  { name: "/config", desc: "查看配置摘要" },
  { name: "/paths", desc: "查看持久化路径" },
  { name: "/help", desc: "显示帮助" },
  { name: "/history", desc: "打开历史会话列表" },
  { name: "/skills", desc: "技能管理（打开配置面板）" },
  { name: "/load <uuid>", desc: "加载指定会话" },
  { name: "/rename <uuid> <标题>", desc: "重命名会话" },
  { name: "/delete <uuid>", desc: "删除会话" },
  { name: "/fs", desc: "打开文件浏览器" },
  { name: "/logs", desc: "打开日志面板" },
  { name: "/agent", desc: "打开 Agent 观察面板" },
  { name: "/status", desc: "显示状态信息" },
  { name: "/theme", desc: "切换主题" },
  { name: "/effort <level>", desc: "设置思考深度 (off/low/medium/high/xhigh/max)；off=关闭思考" },
  { name: "/maxlen <output|input> <tokens>", desc: "设置输出/输入长度上限（0=不限制，与上下文解耦）" },
  { name: "/attach", desc: "选择附件文件" },
  { name: "/about", desc: "关于 ContextLoom" },
  { name: "/mcp", desc: "查看 MCP 状态" },
  { name: "/exit", desc: "提示关闭 GUI 窗口" },
];

async function handleCommand(cmd) {
  const [name, ...rest] = cmd.slice(1).split(/\s+/);
  const arg = rest.join(" ");
  switch (name) {
    case "new": await newSession(); return true;
    case "mode":
      if (["chat", "task", "goal"].includes(arg)) {
        await req("mode.set", { mode: arg });
        state.mode = arg; setModeUI(arg);
        appendSystem(`已切换模式: ${arg}`);
        return true;
      }
      toast("用法: /mode chat|task|goal", "warn");
      return true;
    case "clear":
      renderMessages([]);
      appendSystem("已清空显示");
      return true;
    case "help":
      appendSystem("命令: /mode /new /clear /stop /ctx /compact /config /paths /history /skills /load /rename /delete /fs /logs /agent /status /theme /effort /maxlen /attach /about /mcp /exit");
      return true;
    case "stop":
      stopStream(state.sessionId);
      appendSystem("已请求停止");
      return true;
    case "ctx":
      try { const r = await req("context.stats", {}); appendSystem("上下文: " + r.usage + (r.stats ? "\n" + r.stats : "")); }
      catch (e) { appendSystem("获取失败: " + msgOf(e)); }
      return true;
    case "config":
      try { const r = await req("config.get", {}); appendSystem("配置: " + (r.summary || "")); }
      catch (e) { appendSystem("获取失败: " + msgOf(e)); }
      return true;
    case "compact":
      // 与快捷按钮一致：直接调后端 context.compress（等价 TUI /compress）。
      // 压缩成功后后端推送 ctx.compressed 事件重绘消息区；这里把返回文案
      // 直接显示出来——短对话/已压缩过时后端返回"无需压缩"而非 0 释放，
      // 用户点击后立刻看到结果，不再是"毫无反应"。
      try {
        const r = await req("context.compress", {});
        if (r.usage) applyCtxString(r.usage);
        appendSystem(r.message || "上下文已压缩");
      } catch (e) { toast("压缩失败: " + msgOf(e), "err"); }
      return true;
    case "paths":
      try { const r = await req("paths", {}); appendSystem("路径: " + JSON.stringify(r)); }
      catch (e) { appendSystem("获取路径失败: " + msgOf(e)); }
      return true;
    case "history":
      // 会话菜单已移除（会话管理在左侧会话面板）——/history 直接切到会话面板
      setPanel("sessions");
      lastSessionsSig = "";
      loadSessions();
      return true;
    case "skills":
      setPanel("config");
      loadConfigPanel();
      return true;
    case "load":
      if (!arg) { toast("用法: /load <uuid>", "warn"); return true; }
      openChatTab(arg);
      return true;
    case "rename": {
      const parts = arg.split(/\s+/);
      if (parts.length < 2) { toast("用法: /rename <uuid> <标题>", "warn"); return true; }
      const uuid = parts[0];
      const title = parts.slice(1).join(" ");
      try { await req("chat.renameSession", { uuid, title }); appendSystem("已重命名"); loadSessions(); }
      catch (e) { appendSystem("重命名失败: " + msgOf(e)); }
      return true;
    }
    case "delete":
      if (!arg) { toast("用法: /delete <uuid>", "warn"); return true; }
      deleteSessionPrompt(arg);
      return true;
    case "fs":
      setPanel("projects");
      return true;
    case "logs":
      setPanel("logs");
      refreshLogs();
      return true;
    case "agent":
      setPanel("agent");
      refreshGoalStatus();
      return true;
    case "status":
      try {
        const r = await req("status.bar", {});
        appendSystem("状态: " + JSON.stringify({ mode: r.mode, ctx: r.ctx, goal: r.goal }));
      } catch (e) { appendSystem("获取状态失败: " + msgOf(e)); }
      return true;
    case "theme":
      toggleTheme();
      return true;
    case "effort":
      if (!["off", "low", "medium", "high", "xhigh", "max"].includes(arg)) {
        toast("用法: /effort off/low/medium/high/xhigh/max", "warn");
        return true;
      }
      try {
        await req("model.set", { effort: arg });
        appendSystem(arg === "off" ? "已关闭思考（不发送 reasoning_effort）" : `思考深度已设为 ${arg}`);
        refreshModelInfo();
      } catch (e) { appendSystem("设置失败: " + msgOf(e)); }
      return true;
    case "maxlen": {
      // 长度上限（与上下文解耦，0=不限制）：无参显示当前值；output|input <tokens> 设置。
      // 走 config.set 同一链路（自动持久化 + 热更新），成功后同步本地缓存与状态栏。
      const parts = arg ? arg.trim().split(/\s+/) : [];
      if (!parts.length) {
        let v = state.cfgValues || {};
        // 缓存未加载（设置面板从未打开）时补拉一次 config.get
        if (v["model.max_output_tokens"] === undefined && v["model.max_input_tokens"] === undefined) {
          try {
            const r = await req("config.get", {});
            v = (r && r.values) || {};
            state.cfgValues = v;
          } catch (_) {}
        }
        const out = Number(v["model.max_output_tokens"]) || 0;
        const inp = Number(v["model.max_input_tokens"]) || 0;
        appendSystem(`当前长度上限: 输出 ${fmtLimitTokens(out)} / 输入 ${fmtLimitTokens(inp)}（0=不限制）`);
        return true;
      }
      if (parts.length !== 2 || (parts[0] !== "output" && parts[0] !== "input")) {
        toast("用法: /maxlen output|input <tokens>（0=不限制）", "warn");
        return true;
      }
      if (!/^\d+$/.test(parts[1])) {
        toast("tokens 必须是非负整数（0=不限制）", "warn");
        return true;
      }
      const n = parseInt(parts[1], 10);
      const isOut = parts[0] === "output";
      const key = isOut ? "model.max_output_tokens" : "model.max_input_tokens";
      const label = isOut ? "输出" : "输入";
      try {
        await req("config.set", { [key]: n });
        // 本地缓存 + 设置面板字段同步（面板 diff 以 state.cfgValues 为基准，避免旧值回写）
        if (state.cfgValues) state.cfgValues[key] = n;
        const cfgForm = $("#cfgYamlForm");
        const cfgEl = cfgForm ? cfgForm.querySelector(`[data-cfgkey="${key}"]`) : null;
        if (cfgEl) { cfgEl.value = String(n); cfgEl.dataset.dirty = ""; updateCfgSaveState(); }
        if (n === 0) appendSystem(`${label}上限已设为 0（不限制，自动取最大上下文）`);
        else if (n >= 1000) appendSystem(`${label}上限已设为 ${n}（${fmtLimitTokens(n)}）`);
        else appendSystem(`${label}上限已设为 ${n}`);
        refreshModelInfo();
      } catch (e) { toast("设置失败: " + msgOf(e), "err"); }
      return true;
    }
    case "attach":
      $("#attachBtn").click();
      return true;
    case "about":
      appendSystem("ContextLoom — 智能编码助手 · GUI v2 · 上下文感知 · 记忆持久 · 多 Agent 协作");
      return true;
    case "mcp":
      appendSystem("MCP Server 模式: contextloom --mcp-server 启动；GUI 本身不消费 MCP 工具。");
      return true;
    case "exit":
      appendSystem("关闭本窗口即可退出 GUI（后台 contextloom 服务会随 GUI 退出自动停止）");
      return true;
    default:
      return false;
  }
}

/* ═══════════ 命令面板 ═══════════ */
function showCmdPanel() {
  closeCmdPanel();
  const panel = document.createElement("div");
  panel.className = "cmd-panel";
  panel.id = "cmdPanel";
  panel.innerHTML = CMD_LIST.map((c, i) =>
    `<div class="cmd-item${i === 0 ? " sel" : ""}" data-i="${i}">
       <span class="cmd-name">${c.name}</span><span class="cmd-desc">${c.desc}</span>
     </div>`).join("");
  const inputZone = $("#inputZone");
  inputZone.appendChild(panel);
  state.cmdPanel = panel;
  state.cmdSel = 0;
  panel.querySelectorAll(".cmd-item").forEach(el => el.onclick = () => {
    const c = CMD_LIST[+el.dataset.i];
    const ta = $("#input");
    ta.value = c.name.split(" ")[0] + " ";
    ta.focus(); closeCmdPanel();
  });
}

function closeCmdPanel() {
  if (state.cmdPanel) { state.cmdPanel.remove(); state.cmdPanel = null; }
}

function moveCmdSel(d) {
  if (!state.cmdPanel) return;
  const items = state.cmdPanel.querySelectorAll(".cmd-item");
  state.cmdSel = (state.cmdSel + d + items.length) % items.length;
  items.forEach((el, i) => el.classList.toggle("sel", i === state.cmdSel));
}

function filterCmdPanel() {
  if (!state.cmdPanel) return;
  const q = $("#input").value.trim().slice(1).toLowerCase();
  const items = state.cmdPanel.querySelectorAll(".cmd-item");
  items.forEach((el, i) => {
    const c = CMD_LIST[i];
    const hit = !q || c.name.toLowerCase().includes(q) || c.desc.toLowerCase().includes(q);
    el.style.display = hit ? "" : "none";
    if (hit) el.classList.remove("sel");
  });
  const first = Array.from(items).find(el => el.style.display !== "none");
  if (first) first.classList.add("sel");
  state.cmdSel = 0;
}

/* ═══════════ 快捷命令 ═══════════ */
function renderQuickCmds() {
  const wrap = $("#quickCmds");
  const cmds = [
    { c: "/new", t: "新建" },
    { c: "/mode", t: "模式" },
    { c: "/stop", t: "停止" },
    { c: "/ctx", t: "上下文" },
    { c: "/compact", t: "压缩" },
    { c: "/skills", t: "技能" },
    { c: "/config", t: "配置" },
    { c: "/help", t: "帮助" },
  ];
  wrap.innerHTML = "";
  for (const { c, t } of cmds) {
    const b = document.createElement("button");
    b.className = "qcmd";
    b.textContent = c;
    b.title = t;
    b.onclick = async () => {
      if (c === "/mode") {
        const next = state.mode === "chat" ? "task" : state.mode === "task" ? "goal" : "chat";
        try {
          await req("mode.set", { mode: next });
          state.mode = next; setModeUI(next);
          appendSystem(`已切换模式: ${next}`);
        } catch (e) { toast("切换模式失败: " + msgOf(e), "err"); }
      } else if (c === "/new") {
        newSession();
      } else if (c === "/stop") {
        stopStream(state.sessionId);
        appendSystem("已请求停止");
      } else if (c === "/ctx") {
        try { const r = await req("context.stats", {}); appendSystem("上下文: " + r.usage + (r.stats ? "\n" + r.stats : "")); }
        catch (e) { appendSystem("获取失败: " + msgOf(e)); }
      } else if (c === "/compact") {
        // GUI 手动深度压缩：直接调后端 context.compress（等价 TUI /compress）。
        // 压缩成功后后端会推送 ctx.compressed 事件（重绘消息区为压缩态 +
        // 摘要提示）；这里把返回文案直接显示，压缩/无需压缩都有明确反馈。
        try {
          const r = await req("context.compress", {});
          if (r.usage) applyCtxString(r.usage);
          appendSystem(r.message || "上下文已压缩");
        } catch (e) { toast("压缩失败: " + msgOf(e), "err"); }
      } else if (c === "/skills") {
        setPanel("config"); loadConfigPanel();
      } else if (c === "/config") {
        try { const r = await req("config.get", {}); appendSystem("配置: " + (r.summary || "")); }
        catch (e) { appendSystem("获取失败: " + msgOf(e)); }
      } else if (c === "/help") {
        showCmdPanel();
      }
    };
    wrap.appendChild(b);
  }
}

/* ═══════════ 状态栏 / 顶栏 ═══════════ */
async function refreshStatus() {
  if (!state.ws || state.ws.readyState !== 1) return;
  try {
    const s = await req("status.bar");
    if (s.mode) { state.mode = s.mode; setModeUI(s.mode); }
    // ⚠️ 不得无条件覆盖 state.sessionId：status.bar 返回的是【后端】
    // currentSessionID，多窗口/残留 backend 场景下它可能与前端用户正在
    // 查看的会话不同。旧实现每 2s 轮询无条件覆盖 → 用户点击的会话被
    // switchSession "uuid === state.sessionId" 短路 → 不调 loadSession、
    // 不渲染历史 → 消息区空白（"GUI 重启后历史对话记录不加载"根因）。
    // 前端 sessionId 是"用户视角的当前会话"，只由 switchSession/
    // endStream/closeTab 等前端动作更新；后端未在前端动作下切换会话时
    // 轮询值必须被忽略。
    if (s.sessionId && !state.sessionId) {
      // 仅当前端尚无会话（未恢复/清空态）时采纳后端值兜底同步
      state.sessionId = s.sessionId;
    }
    // 流式生成中跳过轮询覆盖：ctx.usage 推送已实时更新（含在途 streaming），
    // 轮询的 ctx 只含历史值（不含在途回复），2s 覆盖会造成进度条回退抖动、
    // 观感"不实时"。流结束后有 pushCtxUsage(conn,0) 校准推送兜底，轮询仅
    // 服务无流稳态（多会话/刷新后恢复）。
    if (!state.streaming) applyCtxString(s.ctx || "");
    if (s.goal && s.goal !== "无活动目标") {
      $("#goalPhase").textContent = s.goal;
    }
    // 模型信息随轮询同步（设置面板保存 config.set 后立即生效，
    // 无需依赖 onopen/model.set 等单一触发点）
    if (s.model && (s.model.backend || s.model.effort)) {
      const el = $("#modelInfo");
      if (el) el.textContent = `${s.model.backend || "deepseek"} · ${s.model.effort || "max"}${modelInfoSuffix(s.model)}`;
    }
    updateModeBadge();
  } catch (_) {}
}

function applyCtxString(str) {
  // 支持小数百分比（后端 ContextUsage 现输出 "ctx 0.6% (800/131k)"，
  // 旧正则只匹配整数导致 0.6% 被 parse 成 0——小增量看不到变化）
  const m = String(str || "").match(/(\d+(?:\.\d+)?)%/);
  if (m) {
    const pct = parseFloat(m[1]);
    // 提取括号内 token 明细（800/131k），避免与百分比重复显示
    const dm = String(str).match(/\(([^)]+)\)/);
    const detail = dm ? dm[1] : String(str).replace(/^ctx\s+/, "");
    setCtxUI(pct, detail);
  }
}

function applyCtxUsage(data) {
  if (!data) return;
  const used = data.used ?? 0, max = data.max ?? 0;
  if (max <= 0) return;
  const pct = (used / max) * 100;
  setCtxUI(pct, fmtTokens(used) + "/" + fmtTokens(max));
  if (data.action) toast(`[上下文] ${data.action}`, "warn");
}

// fmtTokens 格式化 token 数：<1000 真实数值、<10000 1 位小数 k、否则整数 k
// （与后端 fmtCtxTokens 完全一致：整数 k 为截断语义 12500→12k，非四舍五入；
// 避免 "800" 被截断显示成 "0k"）
function fmtTokens(n) {
  n = Number(n) || 0;
  if (n < 1000) return String(n);
  if (n < 10000) return (n / 1000).toFixed(1) + "k";
  return Math.floor(n / 1000) + "k";
}

// fmtLimitTokens 格式化长度上限（/maxlen 与状态栏"输出xxk"展示用）：
// 0=不限制显示"不限"；<1000 原值；>=1000 整除显示 Nk，否则 N.Nk（65536→65.5k）。
// 不复用 fmtTokens：后者 >=10000 为截断语义（与后端 ctx 展示一致），上限需精确 N.Nk。
function fmtLimitTokens(n) {
  n = Number(n) || 0;
  if (n <= 0) return "不限";
  if (n < 1000) return String(n);
  return n % 1000 === 0 ? (n / 1000) + "k" : (n / 1000).toFixed(1) + "k";
}

// 状态栏模型信息后缀：输出上限（>0 时展示）+ 思考关闭提示（effort=off 或
// thinking_enabled=false 均表示思考被关闭，让用户一眼看出）
function modelInfoSuffix(m) {
  let s = "";
  const mo = Number(m && m.maxOutputTokens) || 0;
  if (mo > 0) s += ` · 输出${fmtLimitTokens(mo)}`;
  if (m && (m.effort === "off" || m.thinkingEnabled === false)) s += " · 思考关";
  s += thinkingSuffix(m && m.thinking);
  return s;
}

// thinkingSuffix 多轮思考策略后缀（设计 §3.8）：policy≠off 时返回
// " · 思考:strict 轮2/3"（验证轮进行中追加 ·验证中）；字段缺失或 policy=off
// 返回 ""（零回归：旧后端不下发该字段时不渲染）。
function thinkingSuffix(th) {
  if (!th || typeof th !== "object") return "";
  const p = String(th.policy || "");
  if (!p || p === "off") return "";
  let s = ` · 思考:${p}`;
  const r = Number(th.round) || 0, mr = Number(th.maxRound) || 0;
  if (r > 0 && mr > 0) s += ` 轮${r}/${mr}`;
  else if (r > 0) s += ` 轮${r}`;
  if (th.verifying) s += "·验证中";
  return s;
}

function setCtxUI(pct, detail) {
  pct = Math.min(99, Math.max(0, Number(pct) || 0));
  const fill = $("#ctxFill"), num = $("#ctxNum");
  if (fill) {
    fill.style.width = pct + "%";
    fill.className = "ctx-fill" + (pct >= 90 ? " danger" : pct >= 70 ? " warn" : "");
  }
  // 百分比显示精度：<1% 保留 2 位小数（0.42%，大预算下小占位可见），
  // <10% 保留 1 位小数（0.6%），≥10% 整数（42%）
  const label = pct < 1 ? pct.toFixed(2) : pct < 10 ? pct.toFixed(1) : Math.round(pct) + "";
  if (num) num.textContent = label + "%";
  const num2 = $("#ctxNum2");
  if (num2) {
    num2.textContent = label + "%";
    num2.className = pct >= 90 ? "danger" : pct >= 70 ? "warn" : "";
  }
  const det = $("#ctxDetail");
  if (det) det.textContent = detail ? `(${detail})` : "";
}

async function refreshModelInfo() {
  if (!state.ws || state.ws.readyState !== 1) return;
  try {
    const m = await req("model.info");
    const el = $("#modelInfo");
    if (el) el.textContent = `${m.backend} · ${m.effort}${modelInfoSuffix(m)}`;
  } catch (_) {}
}

function updateModeBadge() {
  const icons = { chat: "💬 chat", task: "⚡ task", goal: "🎯 goal" };
  const el = $("#modeBadge");
  if (el) el.textContent = icons[state.mode] || "💬 chat";
}

function updateTimer() {
  const el = $("#timerBlock");
  if (!el) return;
  if (!state.sessionStart) { el.textContent = ""; return; }
  const sec = Math.floor((Date.now() - state.sessionStart) / 1000);
  const mm = String(Math.floor(sec / 60)).padStart(2, "0");
  const ss = String(sec % 60).padStart(2, "0");
  el.textContent = `⏱ ${mm}:${ss}`;
}

function setModeUI(mode) {
  $$(".mode-btn").forEach(b => b.classList.toggle("active", b.dataset.mode === mode));
  updateModeBadge();
}

function setConn(on) {
  const dot = $("#connDot");
  if (dot) dot.classList.toggle("on", on);
}

/* ═══════════ 活动栏 / 面板切换 ═══════════ */
function setPanel(panel) {
  state.activePanel = panel;
  $$(".act-btn").forEach(b => b.classList.toggle("active", b.dataset.panel === panel));
  const panels = {
    projects: "#panelProjects",
    sessions: "#panelSessions",
    agent: "#panelAgent",
    config: "#panelConfig",
    logs: "#panelLogs",
  };
  for (const [k, sel] of Object.entries(panels)) {
    const el = $(sel);
    if (el) el.style.display = k === panel ? "flex" : "none";
  }
  // 终端位于输入框下方、与消息区/文件编辑器天然共存（仅由
  // renderFileEditor/hideFileEditor 管理彼此互斥）；termShow/termHide
  // 只显隐终端面板本身与分隔条，绝不操纵 fileEditor/msgList/inputZone
  // （历史"显示终端隐藏消息区/编辑器"为全屏时代残留，已废除）。
  if (panel === "term") {
    termShow();
  } else {
    termHide();
  }
  if (panel === "projects") renderProjectList();
  if (panel === "sessions") renderSessionListPanel();
  if (panel === "agent") { refreshGoalStatus(); startAgentPolling(); }
  else stopAgentPolling();
  if (panel === "config") loadConfigPanel();
  if (panel === "logs") { refreshLogs(); startLogPolling(); }
  else stopLogPolling();
}

/* ═══════════ 终端（xterm.js 全功能，仿 VS Code / 业界主流桌面编码 Agent）═══════════ */
// state.terms: [{id, exited, term, fit, loaded}]; state.curTerm: 当前终端 id
state.terms = state.terms || [];
state.curTerm = state.curTerm || null;

// xterm.js 主题（克制科技风：深蓝黑底 + 单一青色强调）
const XTERM_THEME = {
  background: "#0a0f14",
  foreground: "#d6e2ee",
  cursor: "#4fd1ff",
  cursorAccent: "#0a0f14",
  selectionBackground: "#1e3a4d",
  black: "#0a0f14", red: "#e5484d", green: "#46a758", yellow: "#f5a524",
  blue: "#4fd1ff", magenta: "#d889f0", cyan: "#22d3ee", white: "#d6e2ee",
  brightBlack: "#4a5568", brightRed: "#ff6369", brightGreen: "#59e08a",
  brightYellow: "#ffc24d", brightBlue: "#7dd8ff", brightMagenta: "#e8a8ff",
  brightCyan: "#5ce8ff", brightWhite: "#ffffff",
};

// initTermInstance 创建 xterm.js 实例并挂载到终端视图容器。
function initTermInstance(t) {
  const body = document.getElementById("term-view-" + t.id);
  if (!body || t.term) return;
  t.term = new Terminal({
    convertEol: true,          // LF -> CRLF（shell 输出显示正常）
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'Menlo, Consolas, "Courier New", "Noto Sans Mono CJK SC", monospace',
    scrollback: 5000,          // 5k 行回滚
    theme: XTERM_THEME,
    allowProposedApi: true,
  });
  t.fit = new FitAddon.FitAddon();
  t.term.loadAddon(t.fit);
  t.term.open(body);
  // 键盘输入 → 后端 shell stdin（Ctrl+C/方向键/粘贴全部原生支持）
  t.term.onData(data => {
    req("term.input", { id: t.id, data }).catch(err => {
      toast("终端输入失败: " + msgOf(err), "err");
    });
  });
  // 尺寸同步：fit/窗口变化后把行列数上报后端 PTY（Unix TIOCSWINSZ /
  // Windows ResizePseudoConsole），保证 vim/top 等全屏程序布局正确。
  t.term.onResize(({ cols, rows }) => {
    req("term.resize", { id: t.id, cols, rows }).catch(() => {});
  });
  try { t.fit.fit(); } catch (e) { /* 容器暂不可见时忽略 */ }
}

function fitTerm(id) {
  const t = state.terms.find(x => x.id === id);
  if (t && t.fit) { try { t.fit.fit(); } catch (e) {} }
}

function focusTerm(id) {
  const t = state.terms.find(x => x.id === id);
  if (t && t.term) t.term.focus();
}

// termShow/termHide：终端面板显示/隐藏。
// 布局重构后终端位于输入框下方（与消息区、输入框共存，非全屏替代）——
// 仅与文件编辑器互斥（同时显示文件编辑器会挤占对话窗口）。
function termShow() {
  const tp = $("#termPanel");
  if (!tp) return;
  tp.style.display = "flex";
  // ⚠️ 不再操纵 fileEditor/msgList/inputZone：终端位于输入框下方，与
  // 文件编辑器/消息区天然共存；它们的互斥只由 renderFileEditor/
  // hideFileEditor 负责。历史逻辑"termShow 隐藏 fileEditor + 恢复消息区"
  // 是终端全屏时代的残留——它会破坏"文件视图"（msgList 被错误恢复 →
  // 文件框与消息区各占半页）并留下状态错位（见 termHide 同款缺陷）。
  // 恢复对话窗口拖拽记忆：终端可见时 #msgWrap 固定高度才有意义（配合
  // 终端 flex:1 分摊剩余空间）。termHide 隐藏终端时不恢复、也不清除
  // localStorage——用户从侧栏面板切回终端时，对话窗口高度回到上次
  // 拖拽值，体验连续。
  const mw = $("#msgWrap");
  if (mw && !mw.style.flex) {
    const saved = parseInt(localStorage.getItem("contextloom.msgWrapH") || "", 10);
    if (saved && saved >= MSG_WRAP_H_MIN) applyMsgWrapH(saved);
  }
  if (!state.terms.length) openTerm();
  fitTerm(state.curTerm);
  focusTerm(state.curTerm);
  placeScrollDown();
}

function termHide() {
  const tp = $("#termPanel");
  if (tp) tp.style.display = "none";
  // ⚠️ 绝不恢复 fileEditor：历史逻辑"if (ed.style.display==='none')
  // ed.style.display=''"无法区分 fileEditor 是被 termShow 隐藏还是被
  // hideFileEditor（点击会话）隐藏——点击会话后 fileEditor 为 none，
  // 再点击 agent 活动/设置等侧栏面板触发本函数会把空文件框错误恢复
  // 显示，占半个页面且无法操作（"会话后点其他面板出现半页文件查看框"
  // 根因）。fileEditor 的显示状态只由当前激活 tab 决定。
  // 终端隐藏时 #msgWrap 的固定高度（拖拽 #chatResizer 产生）必须无条件
  // 清除：终端 display:none 后不再吸收剩余空间，固定高度会在输入框
  // 下方留下一块空占位（"关闭终端/切走面板后空白阻碍输入框回底部，
  // 反复点击左侧命令行菜单才能重置"根因）。高度记忆保留在
  // localStorage，termShow 重新打开终端时恢复，布局连续。
  const mw = $("#msgWrap");
  if (mw) mw.style.flex = "";
  placeScrollDown();
}

// closeTermPanel 关闭终端：隐藏面板 + 取消活动栏 ▸_ 激活态。
// 关闭后 #termPanel display:none → #chatArea 的 column flex 布局中
// #msgWrap(flex:1) 自动吸收全部剩余空间 → 输入框回到底部，无空洞。
// （msgWrap 固定高度清除统一由 termHide 负责——覆盖 ✕ 关闭、切左侧
// 面板、刷新后 setPanel 全部隐藏路径，见 termHide 注释）
function closeTermPanel() {
  termHide();
  $$(".act-btn").forEach(b => {
    if (b.dataset.panel === "term") b.classList.remove("active");
  });
}

// openTerm 新建终端：term.spawn → xterm 实例 → 标签页 → 切换。
async function openTerm() {
  let t;
  try {
    const r = await req("term.spawn", {});
    t = { id: r.id, exited: false, term: null, fit: null, loaded: false };
  } catch (err) {
    toast("新建终端失败: " + msgOf(err), "err");
    return;
  }
  state.terms.push(t);
  const body = document.createElement("div");
  body.className = "term-view";
  body.id = "term-view-" + t.id;
  body.dataset.tid = t.id;
  $("#termBody").appendChild(body);
  initTermInstance(t);
  renderTermTabs();
  termSwitch(t.id);
  toast("终端已创建: " + t.id);
}

// renderTermTabs 渲染终端标签（含关闭按钮）。
function renderTermTabs() {
  const bar = $("#termTabs");
  if (!bar) return;
  bar.innerHTML = "";
  for (const t of state.terms) {
    const tab = document.createElement("span");
    tab.className = "term-tab" + (t.id === state.curTerm ? " active" : "") + (t.exited ? " exited" : "");
    tab.dataset.tid = t.id;
    tab.innerHTML = `<span class="term-tab-name">${esc(t.id)}</span>` +
      `<button class="term-tab-x" data-tid="${t.id}" title="关闭终端">✕</button>`;
    tab.querySelector(".term-tab-name").onclick = () => termSwitch(t.id);
    tab.querySelector(".term-tab-x").onclick = (e) => { e.stopPropagation(); termClose(t.id); };
    bar.appendChild(tab);
  }
}

// termSwitch 切换当前终端（多窗口随时切换；切换时 fit + 拉取历史缓冲恢复画面）。
function termSwitch(id) {
  state.curTerm = id;
  $$("#termBody .term-view").forEach(v => {
    v.style.display = v.dataset.tid === id ? "block" : "none";
  });
  renderTermTabs();
  const t = state.terms.find(x => x.id === id);
  if (t) {
    if (!t.term) initTermInstance(t);
    if (!t.loaded) {
      t.loaded = true;
      req("term.buffer", { id }).then(b => {
        if (b && t.term) t.term.write(sanitizeTermData(b));
      }).catch(() => {});
    }
    fitTerm(id);
    focusTerm(id);
  }
}

// termClose 关闭终端（kill + dispose + 移除视图 + 切到下一个）。
async function termClose(id) {
  try { await req("term.kill", { id }); } catch (e) { /* 已退出则忽略 */ }
  const t = state.terms.find(x => x.id === id);
  if (t && t.term) { try { t.term.dispose(); } catch (e) {} }
  state.terms = state.terms.filter(x => x.id !== id);
  const v = document.getElementById("term-view-" + id);
  if (v) v.remove();
  if (state.curTerm === id) {
    state.curTerm = state.terms.length ? state.terms[0].id : null;
    if (state.curTerm) termSwitch(state.curTerm);
  }
  renderTermTabs();
}

// sanitizeTermData 清理终端输出中的 OSC 残片（防"11;rgb:0a0a/0f0f/1414"
// 类字面文本泄漏）：
//   - 完整 OSC（ESC ] ... BEL|ESC\）：移除——终端背景/前景由 xterm.js 主题
//     （XTERM_THEME）控制，动态 OSC 颜色设置无意义且部分场景渲染异常
//   - 未闭合 OSC 起始（ESC ] 后无终止符，可能跨块/被 64KB 环形缓冲切断）：
//     移除，防其主体泄漏为文本
//   - 孤儿 OSC 主体（前缀 ESC] 丢失的残片，如 "]11;rgb:..." 或换行后的
//     "11;rgb:0a0a/0f0f/1414"）：移除——这正是用户报告的"空行出现
//     rgb 内容"现象（PTY/缓冲边界切断或终端对 OSC 11 支持不完整所致）
// 仅对"含 OSC 特征"的块做处理，正常文本/CSI（\x1b[）完全不动。
function sanitizeTermData(s) {
  if (!s || typeof s !== "string") return s;
  const hasOSC = s.indexOf("\x1b]") >= 0 || /(^|[\r\n])\d+;rgb:[0-9a-fA-F]+\/[0-9a-fA-F]+\/[0-9a-fA-F]+/.test(s) || /^\]\d+;/.test(s);
  if (!hasOSC) return s;
  // 1) 完整 OSC：ESC ] ... (BEL 或 ESC \)
  s = s.replace(/\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g, "");
  // 2) 未闭合 OSC 起始（到块尾无终止符）
  s = s.replace(/\x1b\][^\x07\x1b]*$/g, "");
  // 3) 孤儿 OSC 主体：前缀丢失的 ]N; 开头残片
  s = s.replace(/\]\d+;[^\r\n\x1b]*/g, "");
  // 4) 孤儿 OSC 主体：完全无前缀的 "N;rgb:xx/yy/zz"（仅行首，防误删正文）
  s = s.replace(/(^|[\r\n])\d+;rgb:[0-9a-fA-F]+\/[0-9a-fA-F]+\/[0-9a-fA-F]+/g, "$1");
  return s;
}

// handleTermOutput 终端输出事件（broadcast，任何窗口都接收；原始 ANSI 交 xterm 渲染）。
function handleTermOutput(data) {
  if (!data || !data.id) return;
  let t = state.terms.find(x => x.id === data.id);
  if (!t) {
    // 未知终端（其他窗口创建）：自动补建视图，保持多窗口同步
    t = { id: data.id, exited: false, term: null, fit: null, loaded: false };
    state.terms.push(t);
    const body = document.createElement("div");
    body.className = "term-view";
    body.id = "term-view-" + data.id;
    body.dataset.tid = data.id;
    $("#termBody").appendChild(body);
    renderTermTabs();
  }
  if (!t.term) initTermInstance(t);
  if (t.term && data.data) t.term.write(sanitizeTermData(data.data));
}

// handleTermExit 终端退出事件。
function handleTermExit(data) {
  if (!data || !data.id) return;
  const t = state.terms.find(x => x.id === data.id);
  if (t) {
    t.exited = true;
    if (t.term) t.term.write(`\x1b[2m[进程已退出，代码 ${data.code ?? "?"}]\x1b[0m\r\n`);
  }
  renderTermTabs();
}

// fitAllTerms 窗口尺寸变化时自适应全部终端（xterm fit）。
function fitAllTerms() {
  for (const t of state.terms) fitTerm(t.id);
}

/* ═══════════ 项目管理器（多项目目录）═══════════ */
/* ═══════════ 项目管理器（功能第一：管理项目集合，区别于目录管理器） ═══════════ */
// renderProjectList 渲染项目管理器面板：项目列表（当前高亮）+ 点击切换 +
// 移除。切换项目时 backend 在目标目录重启，目录管理器（文件树）/会话/终端
// 等所有目录相关内容随 WS 重连实时切换到新项目根（config.yaml 除外）。
function baseName(p) {
  const s = String(p).replace(/[\\/]+$/, "");
  if (!s) return String(p); // 根路径（/ 或 C:\）保持原样
  const i = Math.max(s.lastIndexOf("/"), s.lastIndexOf("\\"));
  return i >= 0 ? s.slice(i + 1) : s;
}

// v3 合并面板：项目管理器 = 项目行 + 每个项目内嵌的完整文件树。
// 项目行点击=展开/折叠该项目文件树（懒加载 fs.list with root=项目路径）；
// 当前项目默认展开且可编辑，其他项目只读浏览（后端写保护兜底）。
async function renderProjectList() {
  const list = $("#projList");
  if (!list) return;
  let data;
  try {
    data = await fetch("/api/projects").then(r => r.json());
  } catch (e) {
    list.innerHTML = `<div class="cfg-empty">加载失败</div>`;
    return;
  }
  const cur = data.current || "";
  const projects = data.projects || [];
  if (!projects.length) {
    list.innerHTML = `<div class="cfg-empty">尚未添加项目<br><span class="proj-empty-sub">点击上方 ＋ 添加（目录选择）</span></div>`;
    return;
  }
  if (!state.projExpanded) state.projExpanded = {};
  const frag = document.createDocumentFragment();
  for (const p of projects) {
    const isCur = p === cur;
    const expanded = state.projExpanded[p] !== undefined ? state.projExpanded[p] : isCur; // 当前项目默认展开
    const wrap = document.createElement("div");
    wrap.className = "proj-wrap";
    // ── 项目行 ──
    const row = document.createElement("div");
    row.className = "proj-row" + (isCur ? " cur" : " clickable");
    row.title = p;
    const arrow = document.createElement("span");
    arrow.className = "tree-arrow" + (expanded ? " open" : "");
    arrow.textContent = expanded ? "▼" : "▶";
    const ico = document.createElement("span");
    ico.className = "proj-ico";
    ico.textContent = isCur ? "📌" : "📁";
    const info = document.createElement("div");
    info.className = "proj-info";
    const name = document.createElement("div");
    name.className = "proj-name";
    name.textContent = baseName(p);
    const path = document.createElement("div");
    path.className = "proj-path";
    path.textContent = p;
    info.appendChild(name);
    info.appendChild(path);
    row.appendChild(arrow);
    row.appendChild(ico);
    row.appendChild(info);
    if (isCur) {
      const tag = document.createElement("span");
      tag.className = "proj-tag";
      tag.textContent = "当前";
      row.appendChild(tag);
    } else {
      const sw = document.createElement("button");
      sw.className = "proj-sw";
      sw.textContent = "切换";
      sw.title = "切换项目根（backend 重启，会话/终端等跟随）";
      sw.onclick = (e) => { e.stopPropagation(); switchProject(p); };
      row.appendChild(sw);
    }
    const rm = document.createElement("button");
    rm.className = "proj-rm";
    rm.textContent = "✕";
    rm.title = "从项目管理器移除";
    rm.onclick = async (e) => {
      e.stopPropagation();
      if (!await confirmDialog(`从项目管理器移除 ${baseName(p)}？`, { danger: true, okText: "移除" })) return;
      try {
        const r = await fetch("/api/projects/remove", {
          method: "POST", headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ path: p }),
        });
        if (!r.ok) { toast("移除失败: " + await r.text(), "err"); return; }
        let removedMsg = "已从列表移除";
        try { const j = await r.json(); if (j && j.notExists) removedMsg = "目录不存在，已从列表移除"; } catch (_) {}
        delete state.projExpanded[p];
        if (p === cur) {
          // 移除的是当前项目：后端已自动切到剩余首个可达项目。原地刷新
          // （不整页重载——右侧会话 tab 保留），随后按新当前项目重建视图。
          toast(removedMsg + "，已自动切换项目");
          let newCur = "";
          try { newCur = (await fetch("/api/projects", { cache: "no-store" }).then(x => x.json())).current || ""; } catch (_) {}
          await afterProjectSwitch(newCur);
        } else {
          toast(removedMsg);
          renderProjectList();
        }
      } catch (err) { toast("移除失败: " + msgOf(err), "err"); }
    };
    row.appendChild(rm);
    // 行点击 = 展开/折叠该项目文件树（不再直接切换）
    row.onclick = () => {
      state.projExpanded[p] = !state.projExpanded[p];
      renderProjectList();
    };
    wrap.appendChild(row);
    // ── 项目文件树（展开时懒加载；当前项目默认展开）──
    if (expanded) {
      const tree = document.createElement("div");
      tree.className = "proj-tree";
      // body 必须先于工具栏声明：内嵌工具栏的"⟳ 刷新"回调要引用它。
      // loadProjTree 的 container 必须是 tree-body——若误传整个 tree，
      // force 刷新时 container.innerHTML='加载中…' 会把工具栏按钮
      // （⟳ ＋文件 ＋目录）整排清掉（历史 bug：刷新后按钮消失）。
      const body = document.createElement("div");
      body.className = "proj-tree-body";
      if (isCur) {
        // 当前项目：内嵌工具栏（刷新/新建），全功能树
        const tb = document.createElement("div");
        tb.className = "proj-tree-toolbar";
        const btn = (label, title, fn) => {
          const b = document.createElement("button");
          b.textContent = label;
          b.title = title;
          b.onclick = (e) => { e.stopPropagation(); fn(); };
          return b;
        };
        tb.appendChild(btn("⟳", "刷新当前项目文件树", () => loadProjTree(p, body, isCur, true)));
        tb.appendChild(btn("＋文件", "在当前项目根新建文件", () => newFileIn("", p)));
        tb.appendChild(btn("＋目录", "在当前项目根新建目录", () => newDirIn("", p)));
        tree.appendChild(tb);
      } else {
        tree.classList.add("readonly");
      }
      tree.appendChild(body);
      wrap.appendChild(tree);
      loadProjTree(p, body, isCur, false);
    }
    frag.appendChild(wrap);
  }
  list.innerHTML = "";
  list.appendChild(frag);
}

// loadProjTree 懒加载项目根第一层；root=项目绝对路径（空=当前项目根）。
async function loadProjTree(root, container, writable, force) {
  const cacheKey = root + "|";
  if (!force && state.treeCache[cacheKey]) {
    renderProjEntries(root, container, state.treeCache[cacheKey], writable);
    return;
  }
  container.innerHTML = '<div class="cfg-empty" style="padding:6px">加载中…</div>';
  try {
    const r = await req("fs.list", { path: "", root });
    state.treeCache[cacheKey] = r.entries || [];
    renderProjEntries(root, container, r.entries || [], writable);
  } catch (e) {
    container.innerHTML = `<div class="cfg-empty" style="padding:6px">⚠ ${esc(msgOf(e))}</div>`;
  }
}

function renderProjEntries(root, container, entries, writable) {
  container.innerHTML = "";
  const frag = document.createDocumentFragment();
  for (const e of entries) {
    frag.appendChild(buildTreeNode("", e, root, writable));
  }
  container.appendChild(frag);
}

// buildTreeNode 构建树节点（v3：透传 root；writable=false 时只读——目录可
// 展开浏览、文件只读打开、右键菜单仅复制路径）。
function buildTreeNode(parentPath, e, root, writable) {
  const path = e.path;
  const div = document.createElement("div");
  div.className = "tree-node";
  const row = document.createElement("div");
  row.className = "tree-row" + (writable ? "" : " ro");
  row.dataset.path = path;
  row.dataset.isDir = e.isDir ? "1" : "0";
  const arrow = document.createElement("span");
  arrow.className = "tree-arrow" + (e.isDir ? "" : " leaf");
  arrow.textContent = "▶";
  const ico = document.createElement("span");
  ico.className = "tree-ico";
  ico.textContent = e.isDir ? "📁" : "📄";
  const name = document.createElement("span");
  name.className = "tree-name " + (e.isDir ? "dir" : "file");
  name.textContent = truncName(e.name);
  name.title = path;
  row.appendChild(arrow);
  row.appendChild(ico);
  row.appendChild(name);

  if (e.isDir) {
    // 懒加载：目录默认折叠，点击箭头/行时才请求子目录（root 透传）
    const children = document.createElement("div");
    children.className = "tree-children";
    children.style.display = "none";
    const loadCh = async () => {
      if (children.dataset.loaded) return;
      try {
        const r = await req("fs.list", { path, root });
        state.treeCache[root + "|" + path] = r.entries || [];
        children.innerHTML = "";
        const frag = document.createDocumentFragment();
        for (const c of r.entries || []) frag.appendChild(buildTreeNode(path, c, root, writable));
        children.appendChild(frag);
        children.dataset.loaded = "1";
      } catch (err) {
        children.innerHTML = `<div class="cfg-empty">⚠ ${esc(msgOf(err))}</div>`;
      }
    };
    const toggle = () => {
      arrow.classList.toggle("open");
      const open = arrow.classList.contains("open");
      children.style.display = open ? "" : "none";
      if (open) loadCh();
    };
    arrow.onclick = (ev) => {
      ev.stopPropagation();
      toggle();
    };
    row.onclick = toggle;
    div.appendChild(row);
    div.appendChild(children);
  } else {
    row.onclick = async () => {
      try {
        const r = await req("fs.read", { path, root });
        openFileTab(path, r.content, r.mtime, root, !writable);
      } catch (err) {
        toast("打开失败: " + msgOf(err), "err");
      }
    };
    div.appendChild(row);
  }

  // 右键菜单：只读树仅保留复制路径；可写树全功能（新建/重命名/删除）
  row.oncontextmenu = (ev) => {
    ev.preventDefault();
    ev.stopPropagation();
    row.classList.add("sel");
    const items = [];
    if (writable) {
      items.push({ label: e.isDir ? "📁 新建文件" : "📄 新建文件", fn: () => newFileIn(path, root) });
      items.push({ label: "📁 新建目录", fn: () => newDirIn(path, root) });
      items.push({ label: "✏️ 重命名", fn: () => renamePath(path, root) });
    }
    items.push({ label: "📋 复制路径", fn: () => copyText(root ? root + "/" + path : path) });
    if (writable) {
      items.push({ label: e.isDir ? "🗑 删除目录" : "🗑 删除文件", danger: true, fn: () => deletePath(path, e.isDir, root) });
    }
    showContextMenu(ev.clientX, ev.clientY, items, () => row.classList.remove("sel"));
  };
  return div;
}

// switchProject 切换项目根：backend 常驻复用（目标目录进程保留，切回立即可
// 见原项目会话与在途流），重载后自动打开文件树（目录管理器跟随新项目根），
// 所有目录相关内容实时刷新。右侧已打开的会话 tab 按项目持久化，重载后
// loadSessions→restoreSavedChatTabs 自动恢复。
async function switchProject(dir) {
  if (switchingProject) { toast("正在切换项目，请稍候…", "warn"); return; }
  switchingProject = true;
  toast("正在切换到 " + dir + " …");
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), 45000); // 防 fetch 永久挂起
  try {
    const r = await fetch("/api/projects/switch", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ path: dir }),
      signal: ctrl.signal,
    });
    clearTimeout(timer);
    if (!r.ok) { toast("切换失败: " + await r.text(), "err"); switchingProject = false; return; }
    // 以后端返回的规范化目录为准（decodePath 用 filepath.Abs）——前端若按
    // 原始输入路径建键，与后端 current 不一致，tab 快照/持久化就会错位。
    let canon = dir;
    try { const j = await r.json(); if (j && j.dir) canon = j.dir; } catch (_) {}
    try { localStorage.setItem("dm-gui-after-switch", "projects"); } catch (_) {}
    toast("已切换到 " + canon);
    // 原地切换（不 location.reload）：右侧会话 tab 保留，仅重连 WS 指向新项目
    await afterProjectSwitch(canon);
    switchingProject = false;
  } catch (err) {
    clearTimeout(timer);
    switchingProject = false;
    toast("切换失败: " + msgOf(err) + (ctrl.signal.aborted ? "（后端切换超时，请稍后重试）" : ""), "err");
  }
}

/* 目录选择器：以"目录选中"的形式引入项目（项目管理器核心交互）。
 * 支持路径输入 + 文件系统目录浏览（/api/fs/browse：任意绝对路径只读列
 * 子目录，Windows 根为盘符列表）。选中 → /api/projects/open = 添加 +
 * 激活（backend 常驻切换，项目出现在列表首位并成为当前项目）。 */
function pickProjectDir() {
  let wrap = null, pathEl = null, listEl = null;
  const close = () => { if (wrap) { wrap.remove(); wrap = null; } };
  const browse = async (path) => {
    let data;
    try {
      data = await fetch("/api/fs/browse?path=" + encodeURIComponent(path || "")).then(r => r.json());
    } catch (e) {
      listEl.innerHTML = `<div class="dlg-msg" style="text-align:center">浏览失败: ${msgOf(e)}</div>`;
      return;
    }
    if (data.path !== undefined && data.path !== "") pathEl.value = data.path;
    const dirs = data.dirs || [];
    const frag = document.createDocumentFragment();
    if (data.parent) {
      const up = document.createElement("div");
      up.className = "browse-row up";
      up.textContent = "↑ 上级目录";
      up.onclick = () => browse(data.parent);
      frag.appendChild(up);
    }
    if (!dirs.length && !data.parent) {
      const empty = document.createElement("div");
      empty.className = "dlg-msg";
      empty.style.textAlign = "center";
      empty.textContent = "（无子目录）";
      frag.appendChild(empty);
    }
    for (const d of dirs) {
      const row = document.createElement("div");
      row.className = "browse-row";
      row.textContent = "📁 " + d.name;
      row.title = d.path;
      row.onclick = () => browse(d.path);
      frag.appendChild(row);
    }
    listEl.innerHTML = "";
    listEl.appendChild(frag);
  };

  wrap = document.createElement("div");
  wrap.className = "dlg-mask";
  const dlg = document.createElement("div");
  dlg.className = "dlg-box proj-mgr";
  dlg.innerHTML = `
    <div class="dlg-msg" style="font-weight:600;margin-bottom:4px">选择项目目录</div>
    <div class="browse-bar">
      <input class="dlg-input" placeholder="输入目录绝对路径，或浏览下方目录选择" />
      <button class="dlg-btn">前往</button>
    </div>
    <div class="browse-list"></div>
    <div class="dlg-actions">
      <button class="dlg-btn ok">选择此目录并打开</button>
      <button class="dlg-btn cancel">取消</button>
    </div>`;
  wrap.appendChild(dlg);
  document.body.appendChild(wrap);
  pathEl = dlg.querySelector("input");
  listEl = dlg.querySelector(".browse-list");
  dlg.querySelector(".browse-bar button").onclick = (e) => {
    e.stopPropagation();
    const p = (pathEl.value || "").trim();
    if (!p) { toast("请输入目录路径", "warn"); return; }
    browse(p);
  };
  pathEl.addEventListener("keydown", (e) => {
    if (e.key === "Enter") {
      e.preventDefault();
      const p = (pathEl.value || "").trim();
      if (p) browse(p);
    }
  });
  dlg.querySelector(".cancel").onclick = (e) => { e.stopPropagation(); close(); };
  wrap.onclick = (e) => { if (e.target === wrap) close(); };
  dlg.querySelector(".ok").onclick = async (e) => {
    e.stopPropagation();
    const target = (pathEl.value || "").trim();
    if (!target) { toast("请先选择或输入目录", "warn"); return; }
    if (switchingProject) { toast("正在切换项目，请稍候…", "warn"); return; }
    switchingProject = true;
    // 打开 = 添加 + 激活（backend 切换重启；config.yaml 仍跟随可执行文件目录）
    const ctrl = new AbortController();
    const timer = setTimeout(() => ctrl.abort(), 45000);
    try {
      const r = await fetch("/api/projects/open", {
        method: "POST", headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ path: target }),
        signal: ctrl.signal,
      });
      clearTimeout(timer);
      if (!r.ok) { toast("打开失败: " + await r.text(), "err"); switchingProject = false; return; }
      close();
      let canon = target;
      try { const j = await r.json(); if (j && j.dir) canon = j.dir; } catch (_) {}
      try { localStorage.setItem("dm-gui-after-switch", "projects"); } catch (_) {}
      toast("已打开项目并激活");
      // 原地切换（不 location.reload）：右侧会话 tab 保留
      await afterProjectSwitch(canon);
      switchingProject = false;
    } catch (err) {
      clearTimeout(timer);
      switchingProject = false;
      toast("打开失败: " + msgOf(err) + (ctrl.signal.aborted ? "（后端切换超时，请稍后重试）" : ""), "err");
    }
  };
  document.addEventListener("keydown", function esc(e) {
    if (e.key === "Escape") { close(); document.removeEventListener("keydown", esc); }
  });
  browse("");
  pathEl.focus();
}

/* ═══════════ 会话列表面板 ═══════════ */
function renderSessionListPanel() {
  const list = $("#sessionList");
  if (!list) return;
  if (!state.sessions.length) {
    // WS 未就绪或首次加载中：显示"加载中"而非"暂无会话"（避免浏览器
    // 刷新后 init→setPanel 先于 connect 渲染时误导用户以为会话丢失）
    const connecting = !state.ws || state.ws.readyState !== 1;
    list.innerHTML = connecting
      ? `<div class="cfg-empty" style="padding:12px;text-align:center">加载中…</div>`
      : `<div class="cfg-empty" style="padding:12px;text-align:center">暂无会话<br><span style="font-size:12px;opacity:.6">点击上方 ＋ 新建</span></div>`;
    return;
  }
  const frag = document.createDocumentFragment();
  for (const s of state.sessions) {
    const item = document.createElement("div");
    item.className = "sl-item" + (s.uuid === state.sessionId ? " active" : "");
    item.title = s.title || "未命名会话";
    const t = s.updatedAt ? new Date(s.updatedAt).toLocaleString("zh-CN", {
      month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit",
    }) : "";
    item.innerHTML = `
      <div class="sl-main" data-uuid="${s.uuid}">
        <div class="sl-title">${esc(s.title || "未命名会话")}</div>
        <div class="sl-time">${t}</div>
      </div>
      <div class="sl-actions">
        <button class="sl-del" data-uuid="${s.uuid}" title="删除会话">🗑</button>
      </div>`;
    item.querySelector(".sl-main").onclick = () => { openChatTab(s.uuid); };
    item.querySelector(".sl-del").onclick = (e) => { e.stopPropagation(); deleteSessionPrompt(s.uuid); };
    frag.appendChild(item);
  }
  list.innerHTML = "";
  list.appendChild(frag);
}

/* ═══════════ 文件操作（v3：root 透传；写操作仅当前项目树可用） ═══════════ */
async function newFileIn(parentPath, root) {
  const name = await promptDialog("文件名:", "", "创建");
  if (!name || !name.trim()) return;
  const path = parentPath ? parentPath + "/" + name.trim() : name.trim();
  try {
    await req("fs.write", { path, content: "", root });
    toast("已创建 " + name.trim());
    refreshProjTree(root, path);
  } catch (e) { toast("创建失败: " + msgOf(e), "err"); }
}

async function newDirIn(parentPath, root) {
  const name = await promptDialog("目录名:", "", "创建");
  if (!name || !name.trim()) return;
  const path = parentPath ? parentPath + "/" + name.trim() : name.trim();
  try {
    await req("fs.mkdir", { path, root });
    toast("已创建目录 " + name.trim());
    refreshProjTree(root, path);
  } catch (e) { toast("创建失败: " + msgOf(e), "err"); }
}

async function renamePath(path, root) {
  const oldName = path.split("/").pop();
  const name = await promptDialog("新名称:", oldName, "重命名");
  if (!name || !name.trim() || name.trim() === oldName) return;
  const parent = path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "";
  const newPath = parent ? parent + "/" + name.trim() : name.trim();
  try {
    await req("fs.rename", { oldPath: path, newPath, root });
    toast("已重命名");
    // 若文件 tab 打开则同步更新
    const tab = state.tabs.find(t => t.id === path);
    if (tab) { tab.id = newPath; tab.title = name.trim(); renderTabs(); }
    refreshProjTree(root, parent);
  } catch (e) { toast("重命名失败: " + msgOf(e), "err"); }
}

async function deletePath(path, isDir, root) {
  if (!await confirmDialog(`确定删除${isDir ? "目录" : "文件"} ${path}？`, { danger: true, okText: "删除" })) return;
  try {
    await req("fs.delete", { path, root });
    toast("已删除");
    closeTab(path);
    const parent = path.includes("/") ? path.slice(0, path.lastIndexOf("/")) : "";
    refreshProjTree(root, parent);
  } catch (e) { toast("删除失败: " + msgOf(e), "err"); }
}

// refreshProjTree 写操作后刷新受影响的项目树（root 为空 = 当前项目）。
function refreshProjTree(root, parentPath) {
  const cacheKey = (root || "") + "|" + (parentPath || "");
  delete state.treeCache[cacheKey];
  const list = $("#projList");
  if (!list) return;
  // 重渲染项目列表（保持展开状态）
  renderProjectList();
}

function truncName(name) {
  if (name.length <= 20) return name;
  return name.slice(0, 10) + "…" + name.slice(-10);
}

/* ═══════════ 文件编辑 Tab（仿业界主流桌面编码 Agent：双栏原文/预览 + 同步滚动 + 行号 + 高亮 + 查找替换） ═══════════ */

// ── 纯函数（可被 Node/Go 契约测试断言） ──

// Markdown 判定
function feIsMarkdown(path) {
  return /\.(md|markdown|mdown|mkd|mdx)$/i.test(path || "");
}

// 语言推断（按扩展名）
function feLangOf(path) {
  const n = (path || "").toLowerCase();
  const m = n.split(".").pop();
  if (feIsMarkdown(n)) return "markdown";
  if (m === "go") return "go";
  if (m === "py") return "python";
  if (m === "js" || m === "mjs" || m === "cjs" || m === "jsx") return "js";
  if (m === "ts" || m === "tsx") return "js";
  if (m === "json") return "json";
  if (m === "yaml" || m === "yml" || m === "toml") return "yaml";
  if (m === "sh" || m === "bash" || m === "zsh") return "bash";
  if (m === "html" || m === "htm" || m === "vue" || m === "xml") return "html";
  if (m === "css" || m === "scss" || m === "less") return "css";
  if (m === "c" || m === "h") return "c";
  if (m === "cpp" || m === "cc" || m === "hpp") return "cpp";
  if (m === "java") return "java";
  if (m === "rs") return "rust";
  if (m === "sql") return "sql";
  if (m === "txt") return "text";
  return m && m.length <= 8 ? m : "text";
}

// 关键字集（按语言）
const HL_KEYWORDS = {
  go: ["func","package","import","return","if","else","for","range","var","const","type","struct","interface","map","chan","go","defer","select","switch","case","default","break","continue","nil","true","false","len","make","new","string","int","bool","error","byte","rune","float64","float32","uint","int64","uint64","int32","uint32","uint8","int8","append","cap","close","copy","delete","panic","print","println","real","recover"],
  js: ["function","const","let","var","return","if","else","for","while","do","switch","case","break","continue","new","class","extends","import","export","from","async","await","try","catch","finally","throw","typeof","instanceof","in","of","true","false","null","undefined","this","void","delete","yield"],
  python: ["def","class","return","if","elif","else","for","while","import","from","as","try","except","finally","with","lambda","pass","break","continue","not","and","or","is","in","None","True","False","yield","global","nonlocal","assert","raise","del"],
  bash: ["if","then","else","elif","fi","for","while","do","done","case","esac","function","return","local","export","echo","exit","cd","sudo","set","unset","read","printf","source"],
  json: ["true","false","null"],
  yaml: ["true","false","null","yes","no","on","off","~"],
  html: ["div","span","html","head","body","title","meta","link","script","style","table","tr","td","th","ul","ol","li","a","img","p","h1","h2","h3","h4","h5","h6","button","input","form","section","aside","header","footer","nav","main","select","option","textarea","pre","code","strong","em","br","hr","iframe","video","audio","canvas","svg"],
  css: ["@media","@import","@keyframes","@font-face","@supports","!important"],
  c: ["int","char","float","double","void","long","short","unsigned","signed","struct","union","enum","typedef","static","extern","const","volatile","register","sizeof","return","if","else","for","while","do","switch","case","default","break","continue","goto","NULL","true","false"],
  cpp: ["int","char","float","double","void","long","short","unsigned","signed","struct","class","union","enum","typedef","static","extern","const","volatile","register","sizeof","return","if","else","for","while","do","switch","case","default","break","continue","namespace","using","template","typename","public","private","protected","virtual","override","new","delete","this","nullptr","true","false"],
  java: ["public","private","protected","class","interface","extends","implements","static","final","void","int","long","float","double","boolean","char","byte","short","String","return","if","else","for","while","do","switch","case","default","break","continue","new","this","super","try","catch","finally","throw","throws","import","package","true","false","null","instanceof","enum","abstract","synchronized","volatile","transient"],
  rust: ["fn","let","mut","const","static","struct","enum","trait","impl","mod","use","pub","crate","self","Self","match","if","else","for","while","loop","return","break","continue","true","false","None","Some","Ok","Err","String","Vec","Option","Result","Box","Rc","Arc","unsafe","async","await","move","ref","type","where","in","as","dyn"],
  sql: ["SELECT","FROM","WHERE","INSERT","INTO","VALUES","UPDATE","SET","DELETE","CREATE","TABLE","ALTER","DROP","INDEX","JOIN","LEFT","RIGHT","INNER","OUTER","ON","GROUP","BY","ORDER","HAVING","LIMIT","OFFSET","AND","OR","NOT","NULL","PRIMARY","KEY","FOREIGN","REFERENCES","UNIQUE","DEFAULT","CASE","WHEN","THEN","ELSE","END","AS","DISTINCT","COUNT","SUM","AVG","MIN","MAX","BEGIN","COMMIT","ROLLBACK","TRANSACTION"]
};
const HL_COMMENTS = {
  go: ["//", "/*", "*/"], js: ["//", "/*", "*/"], python: ["#", '"""'],
  bash: ["#"], json: ["//"], yaml: ["#", "/*", "*/"], html: ["<!--", "-->"],
  css: ["/*", "*/"], c: ["//", "/*", "*/"], cpp: ["//", "/*", "*/"],
  java: ["//", "/*", "*/"], rust: ["//", "/*", "*/"], sql: ["--", "/*", "*/"],
  text: []
};

function hlEscape(s) {
  return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

// 单行语法高亮（带跨行状态：st.block=false | "com" 块注释 | "str" 模板串），返回 {html, st}
function highlightLine(line, lang, st) {
  st = st || { block: false };
  const kws = HL_KEYWORDS[lang] || [];
  const kwSet = {};
  for (const k of kws) kwSet[k] = 1;
  const com = HL_COMMENTS[lang] || [];
  const lineCom = com[0] === "//" || com[0] === "#" || com[0] === "--" ? com[0] : null;
  const blockOpen = com.includes("/*") ? "/*" : (com.includes("<!--") ? "<!--" : null);
  const blockClose = com.includes("*/") ? "*/" : (com.includes("-->") ? "-->" : null);
  const esc = hlEscape;
  let out = "", i = 0, n = line.length;
  while (i < n) {
    const c = line[i];
    if (st.block) {
      if (st.block === "com" && blockClose && line.startsWith(blockClose, i)) {
        out += esc(blockClose) + "</span>"; i += blockClose.length; st.block = false; continue;
      }
      if (st.block === "str" && c === "`") {
        out += esc(c) + "</span>"; i++; st.block = false; continue;
      }
      out += esc(c); i++; continue;
    }
    // 行注释
    if (lineCom && line.startsWith(lineCom, i)) {
      out += '<span class="hl-com">' + esc(line.slice(i)) + "</span>";
      break;
    }
    // 字符串
    if (c === '"' || c === "'" || c === "`") {
      let j = i + 1, s = c;
      while (j < n) {
        if (line[j] === "\\") { s += line[j] + (line[j + 1] || ""); j += 2; continue; }
        if (line[j] === c) { s += c; j++; break; }
        s += line[j]; j++;
      }
      if (j >= n && s[s.length - 1] !== c) {
        // 未闭合：模板串跨行保持着色状态
        if (c === "`") { out += '<span class="hl-str">' + esc(s); st.block = "str"; i = j; continue; }
      }
      out += '<span class="hl-str">' + esc(s) + "</span>";
      i = j; continue;
    }
    // 块注释开始
    if (blockOpen && line.startsWith(blockOpen, i)) {
      const closeAt = line.indexOf(blockClose, i + blockOpen.length);
      if (closeAt >= 0) {
        out += '<span class="hl-com">' + esc(line.slice(i, closeAt + blockClose.length)) + "</span>";
        i = closeAt + blockClose.length; continue;
      }
      // 跨行：span 保持未闭合，由后续行在闭合处收尾
      out += '<span class="hl-com">' + esc(line.slice(i));
      st.block = "com"; break;
    }
    // 数字
    if (/[0-9]/.test(c) && (i === 0 || !/[A-Za-z0-9_]/.test(line[i - 1]))) {
      let j = i;
      while (j < n && /[0-9a-fA-FxXoObB._eE+-]/.test(line[j])) {
        if (line[j] === "e" || line[j] === "E") { j++; if (line[j] === "+" || line[j] === "-") j++; continue; }
        j++;
      }
      out += '<span class="hl-num">' + esc(line.slice(i, j)) + "</span>";
      i = j; continue;
    }
    // 标识符/关键字
    if (/[A-Za-z_]/.test(c)) {
      let j = i;
      while (j < n && /[A-Za-z0-9_]/.test(line[j])) j++;
      const w = line.slice(i, j);
      out += kwSet[w] ? '<span class="hl-kw">' + esc(w) + "</span>" : esc(w);
      i = j; continue;
    }
    // 运算符
    if (/[+\-*/%=<>!&|^~?:]/.test(c)) {
      out += '<span class="hl-op">' + esc(c) + "</span>";
      i++; continue;
    }
    out += esc(c); i++;
  }
  return { html: out, st };
}

// 整文件高亮（按行，携带跨行状态）
function highlightSource(text, lang) {
  const lines = String(text).split("\n");
  const out = [];
  let st = { block: false };
  for (const ln of lines) {
    const r = highlightLine(ln, lang, st);
    st = r.st;
    out.push(r.html);
  }
  return out.join("\n");
}

// 滚动比例映射（同步滚动核心，纯函数）：src 滚动位置 → dst 应处位置
function feSyncRatio(srcST, srcSH, srcCH, dstSH, dstCH) {
  if (srcSH <= srcCH) return 0;
  return srcST / (srcSH - srcCH) * Math.max(0, dstSH - dstCH);
}

// 提取 Markdown 标题锚点 [{level,title,line}]（预览 TOC 与双向定位用）
function feExtractAnchors(text) {
  const lines = String(text).split("\n");
  const out = [];
  for (let i = 0; i < lines.length; i++) {
    const m = /^(#{1,4})\s+(.+?)\s*#*\s*$/.exec(lines[i]);
    if (m) out.push({ level: m[1].length, title: m[2].trim(), line: i });
  }
  return out;
}

// 偏移 → 行/列（1-based，纯函数）
function fePosOf(text, offset) {
  const pre = String(text).slice(0, offset);
  const line = (pre.match(/\n/g) || []).length + 1;
  const nl = pre.lastIndexOf("\n");
  return { line: line, col: pre.length - nl };
}

// 全文查找匹配（纯函数）
function feFindMatches(text, q) {
  if (!q) return [];
  const out = [];
  let i = 0;
  while ((i = String(text).indexOf(q, i)) >= 0) {
    out.push({ start: i, end: i + q.length });
    i += Math.max(q.length, 1);
  }
  return out;
}

// ── 软换行（自动换行适应窗口）纯计算函数 ──
// 等宽字体字符列模型：ASCII=1 列、CJK/全角/emoji=2 列、tab 按 tab-size:2 对齐，
// 行宽 = 内容区宽 / 字符宽（wrapCols）。供 gutter 行号 / 查找层 / TOC 跳转按"渲染行"对齐。

// 码点 → 字符列宽（CJK/全角/emoji 记 2 列）
function feCharCols(cp) {
  if (cp >= 0x1100 && (cp <= 0x115F || cp === 0x2329 || cp === 0x232A ||
      (cp >= 0x2E80 && cp <= 0xA4CF && cp !== 0x303F) ||
      (cp >= 0xAC00 && cp <= 0xD7A3) || (cp >= 0xF900 && cp <= 0xFAFF) ||
      (cp >= 0xFE10 && cp <= 0xFE19) || (cp >= 0xFE30 && cp <= 0xFE6F) ||
      (cp >= 0xFF00 && cp <= 0xFF60) || (cp >= 0xFFE0 && cp <= 0xFFE6) ||
      (cp >= 0x1F300 && cp <= 0x1F64F) || (cp >= 0x1F900 && cp <= 0x1F9FF))) return 2;
  return 1;
}
// 当前列 col 处再放一个码点 cp 所占列宽（tab 对齐到 tabSize=2 边界）
function feCharWidth(cp, col) {
  if (cp === 9) return 2 - (col % 2);
  return feCharCols(cp);
}
// 文本 [lineStart, lineStart+charOff) 在行内软换行后的位置 {row 渲染行, col 列}
function fePosInLine(text, lineStart, charOff, wrapCols) {
  let col = 0, row = 0;
  const end = lineStart + charOff;
  for (let i = lineStart; i < end;) {
    const cp = text.codePointAt(i);
    i += cp > 0xFFFF ? 2 : 1;
    const w = feCharWidth(cp, col);
    if (col + w > wrapCols) { row++; col = 0; }
    col += w;
  }
  return { row, col };
}
// 单行文本软换行后的渲染行数
function feEstimateWrap(line, wrapCols) {
  if (!line) return 1;
  let col = 0, rows = 1;
  for (let i = 0; i < line.length;) {
    const cp = line.codePointAt(i);
    i += cp > 0xFFFF ? 2 : 1;
    const w = feCharWidth(cp, col);
    if (col + w > wrapCols) { rows++; col = 0; }
    col += w;
  }
  return rows;
}
// 渲染行 → 逻辑行（cum 二分）
function feLogicLineAt(info, row) {
  const c = info.cum;
  let lo = 0, hi = c.length - 1;
  if (hi < 0) return 0;
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1;
    if (c[mid] <= row) lo = mid; else hi = mid - 1;
  }
  return lo;
}

// ── 编辑器组件 ──

function curFileTab() {
  return state.tabs.find(t => t.kind === "file" && t.id === state.activeTab);
}

function feLineH() {
  const ta = $("#feText");
  return parseFloat(getComputedStyle(ta).lineHeight) || 20;
}

function feCharW() {
  if (state.feCharWCache) return state.feCharWCache;
  const s = document.createElement("span");
  s.textContent = "0";
  s.style.cssText = "font-family:var(--mono);font-size:12.5px;position:absolute;visibility:hidden;";
  document.body.appendChild(s);
  state.feCharWCache = s.getBoundingClientRect().width || 7.5;
  s.remove();
  return state.feCharWCache;
}

// ── 软换行（自动换行适应窗口）几何支持 ──
// 编辑列软换行后，长逻辑行在屏幕上占据多"渲染行"。高亮层用 CSS pre-wrap 自动对齐；
// 行号 gutter / 查找层 / TOC 跳转需按"渲染行"计算坐标，统一用等宽字体字符列模型估算
// （纯计算函数见纯函数区 feCharCols/feCharWidth/fePosInLine/feEstimateWrap/feLogicLineAt）。
// 全文换行几何缓存：{wrapCols, wrapped[], cum[], total}（value 引用变化时重建）
function feWrapCache() {
  const ta = $("#feText");
  const v = ta.value;
  if (state.feWrapKey === v && state.feWrapInfo) return state.feWrapInfo;
  state.feWrapKey = v;
  const wrapCols = Math.max(8, Math.floor((ta.clientWidth - 28) / feCharW()));
  const lines = v.split("\n");
  const wrapped = new Array(lines.length);
  const cum = new Array(lines.length);
  let total = 0;
  for (let i = 0; i < lines.length; i++) {
    cum[i] = total;
    const n = feEstimateWrap(lines[i], wrapCols);
    wrapped[i] = n;
    total += n;
  }
  state.feWrapInfo = { wrapCols, wrapped, cum, total };
  return state.feWrapInfo;
}

function markDirty() {
  const t = curFileTab();
  if (t && !t.dirty) { t.dirty = true; renderTabs(); }
  const d = document.querySelector("#fileEditor .fe-dirty");
  if (d) d.textContent = "● 未保存";
}

function feRenderHighlight() {
  const tab = curFileTab(); if (!tab) return;
  const hl = $("#feHighlight"); if (!hl) return;
  const ta = $("#feText");
  const big = ta.value.length > 200000;
  $("#fileEditor").classList.toggle("no-hl", big);
  if (big) { hl.innerHTML = ""; return; } // 大文件降级：关闭高亮（透明层方案失效，textarea 恢复实体文字）
  hl.innerHTML = highlightSource(ta.value, feLangOf(tab.id));
}

function feRenderGutter() {
  const g = $("#feGutter"); if (!g) return;
  const ta = $("#feText");
  const total = ta.value.split("\n").length;
  const lineH = feLineH();
  const info = feWrapCache();
  const viewport = total > 5000;
  state.feGutterFull = !viewport;
  let start = 0, end = total;
  if (viewport) {
    // 软换行后按"渲染行"反推逻辑行区间：当前视口顶部渲染行 ± 余量
    const sRow = Math.max(0, Math.floor(ta.scrollTop / lineH) - 20);
    const eRow = sRow + Math.ceil(ta.clientHeight / lineH) + 40;
    start = feLogicLineAt(info, sRow);
    let acc = info.cum[start], e = start;
    while (e < total && acc + info.wrapped[e] <= eRow) { acc += info.wrapped[e]; e++; }
    end = Math.min(total, e + 1);
  }
  const cur = fePosOf(ta.value, ta.selectionStart).line;
  let html = "";
  for (let i = start; i < end; i++) {
    // 行号高度 = 该逻辑行软换行后的渲染行数 × 行高（与原文行对齐）
    const h = (info.wrapped[i] * lineH).toFixed(1);
    html += `<span class="ln${i + 1 === cur ? " cur" : ""}" style="height:${h}px">${i + 1}</span>\n`;
  }
  g.innerHTML = html;
  // 视口模式：渲染区间起点即视口顶部（行高已是真实渲染高度），gutter 从顶部显示；
  // 全量模式：gutter 高度与内容一致，直接同步滚动位置。
  g.scrollTop = viewport ? 0 : ta.scrollTop;
}

function feRenderPreview() {
  const tab = curFileTab(); if (!tab) return;
  const pv = $("#fePreview"); if (!pv) return;
  if (!feIsMarkdown(tab.id) || tab.view === "edit") return;
  // 编辑中（dirty）用实时内容渲染，保证预览跟随原文
  const src = tab.dirty ? $("#feText").value : tab.content;
  const anchors = feExtractAnchors(src);
  state.feAnchors = anchors;
  pv.innerHTML = mdToHTML(src);
  feStabilizePreview(pv); // 懒加载图片加载完成后的视口稳定补偿（防内容漂移）
  // TOC
  const toc = $("#feToc");
  if (toc) {
    toc.innerHTML = anchors.slice(0, 30).map(a =>
      `<button data-line="${a.line}" title="跳到原文行 ${a.line + 1}">${"#".repeat(a.level)} ${hlEscape(a.title)}</button>`
    ).join("");
    toc.querySelectorAll("button").forEach(b => {
      b.onclick = () => feScrollToLine(parseInt(b.dataset.line, 10) || 0);
    });
  }
  // 预览内标题点击 → 原文定位（标题顺序与锚点顺序一致）
  const hs = pv.querySelectorAll("h1,h2,h3,h4");
  hs.forEach((h, i) => {
    const a = anchors[i];
    if (a) { h.title = "跳到原文行 " + (a.line + 1); h.onclick = () => feScrollToLine(a.line); }
  });
}

function feSchedulePreview() {
  clearTimeout(state.fePrevTimer);
  state.fePrevTimer = setTimeout(() => feRenderPreview(), 300);
}

// 预览内懒加载图片（本地图/远程图/PlantUML 代理图）加载完成后高度从 0 突变，
// 导致预览 scrollHeight 变化——若不处理，已滚动位置的视口内容会被"推走"
// （内容缓慢向上/向下漂移，历史缺陷：md 分栏查看时内容缓慢返回）。
// 加载完成后若图片顶部位于视口上方，补偿 scrollTop 保持视口内容位置稳定。
function feStabilizePreview(pv) {
  pv.querySelectorAll("img").forEach(img => {
    if (img.complete) return; // 已加载（缓存命中）不会再变化
    img.addEventListener("load", () => {
      const p = $("#fePreview");
      if (!p || !p.contains(img)) return;
      const h = img.offsetHeight - (parseFloat(img.dataset.ph || "0"));
      img.dataset.ph = String(img.offsetHeight);
      if (Math.abs(h) < 0.5) return;
      // 图片顶部在视口上方：其高度变化把视口内容下推 → 补偿滚动位置
      if (img.offsetTop <= p.scrollTop + 1) {
        p.scrollTop += h;
      }
    });
  });
}

function feUpdateStatus() {
  const tab = curFileTab(); if (!tab) return;
  const ta = $("#feText");
  const pos = fePosOf(ta.value, ta.selectionStart);
  $("#fePos").textContent = `行 ${pos.line}, 列 ${pos.col}`;
  const lines = ta.value.split("\n").length;
  const size = ta.value.length;
  $("#feSize").textContent = `${lines} 行 · ${size.toLocaleString()} 字符`;
  $("#feLang").textContent = feLangOf(tab.id);
  $("#feMode").textContent = tab.view === "split" ? "分栏" : (tab.view === "prev" ? "预览" : "原文");
}

// 视图切换（分栏/原文/预览）
function feApplyView(mode) {
  const tab = curFileTab(); if (!tab) return;
  if ((mode === "split" || mode === "prev") && !feIsMarkdown(tab.id)) {
    toast("仅 Markdown 文件支持预览", "err");
    return;
  }
  tab.view = mode;
  state.fileViews[tab.id] = mode;
  const ed = $("#feEditCol"), pv = $("#fePreviewCol");
  const feRes = $("#feResizer");
  const btns = { split: $("#feViewSplit"), edit: $("#feViewEdit"), prev: $("#feViewPrev") };
  Object.keys(btns).forEach(k => btns[k].classList.toggle("active", k === mode));
  if (mode === "edit") {
    ed.style.display = "flex"; pv.style.display = "none";
    // 原文独占：清除分栏拖拽留下的 inline flex（flex:0 0 Xpx），占满窗口宽度
    // （否则编辑列仍锁在保存的分栏宽度，出现"只显示一半在左边"）
    ed.style.flex = "";
  }
  else if (mode === "prev") { ed.style.display = "none"; pv.style.display = "flex"; }
  else {
    ed.style.display = "flex"; pv.style.display = "flex";
    // 分栏：恢复保存的分栏比例（若有）
    const saved = parseInt(localStorage.getItem("contextloom.feSplit") || "", 10);
    if (saved && saved >= 160) ed.style.flex = "0 0 " + saved + "px";
    else if (!ed.style.flex) ed.style.flex = "1 1 50%";
  }
  // 分栏分隔条：仅分栏模式可见（可拖拽调整原文/预览比例）
  if (feRes) feRes.classList.toggle("on", mode === "split");
  if (mode !== "edit") feRenderPreview();
  feUpdateStatus();
}

// 程序设置滚动位置：记录"期望值"（per-element）——scroll 事件异步派发，
// 到达时若与期望值一致 → 是程序赋值触发的回环，直接吞掉（不同步）；
// 不一致 → 用户又滚动了，按用户滚动正常同步。彻底消除双向同步循环
// （旧 feSyncing 同步置/清挡不住异步事件：md 分栏查看时内容缓慢漂移/跳
// 动的根因之一）。期望值按元素记录（{el,v}）：原文/预览两栏交替程序赋值
// 时互不干扰——ta 的 scroll 事件只消费 ta 的期望，不会误吞/误清 pv 的。
function feSetScroll(el, v) {
  v = Math.max(0, Math.round(v));
  if (Math.abs(el.scrollTop - v) < 1) return; // 无实质变化：不赋值不触发事件
  state.feExpect = { el, v }; // 记录期望值（程序赋值）
  el.scrollTop = v;
}
function feConsumeExpect(el) {
  const exp = state.feExpect;
  if (!exp) return false;
  if (exp.el === el && Math.abs(el.scrollTop - exp.v) < 1.5) {
    state.feExpect = null; // 匹配→程序回环，吞掉
    return true;
  }
  // 期望值属于另一元素（其赋值事件尚未到达）：保留，等待其事件消费
  return false;
}

// 原文滚动 → 预览滚动（比例同步）
function feSyncPreviewFromEdit() {
  const pv = $("#fePreview"); const ta = $("#feText");
  if (!pv || pv.style.display === "none" || state.feSyncing) return;
  state.feSyncing = true;
  feSetScroll(pv, feSyncRatio(ta.scrollTop, ta.scrollHeight, ta.clientHeight, pv.scrollHeight, pv.clientHeight));
  state.feSyncing = false;
}

// 预览滚动 → 原文滚动（比例同步，双向）
function feSyncEditFromPreview() {
  const pv = $("#fePreview"); const ta = $("#feText");
  if (!ta || state.feSyncing) return;
  state.feSyncing = true;
  feSetScroll(ta, feSyncRatio(pv.scrollTop, pv.scrollHeight, pv.clientHeight, ta.scrollHeight, ta.clientHeight));
  state.feSyncing = false;
}

// 原文滚动到指定行（预览 TOC/标题点击定位；软换行后按累计渲染行计算）
function feScrollToLine(line) {
  const ta = $("#feText");
  const lineH = feLineH();
  const info = feWrapCache();
  const row = line >= 0 && line < info.cum.length ? info.cum[line] : line * 1;
  feSetScroll(ta, Math.max(0, row * lineH + 12 - ta.clientHeight / 2));
  const pv = $("#fePreview");
  if (pv && pv.style.display !== "none" && !state.feSyncing) {
    state.feSyncing = true;
    feSetScroll(pv, feSyncRatio(row * lineH, ta.scrollHeight, ta.clientHeight, pv.scrollHeight, pv.clientHeight));
    state.feSyncing = false;
  }
  ta.focus();
}

// ── 查找 / 替换 ──
function feToggleFind(show, replaceMode) {
  const box = $("#feFindBox"); const ta = $("#feText");
  if (!box) return;
  box.style.display = show ? "flex" : "none";
  if (show) {
    const fi = $("#feFindInput"), ri = $("#feReplaceInput");
    const rb = $("#feReplaceBtn"), ra = $("#feReplaceAllBtn");
    const sel = ta.value.slice(ta.selectionStart, ta.selectionEnd);
    if (sel && !replaceMode) fi.value = sel;
    ri.style.display = replaceMode ? "" : "none";
    rb.style.display = replaceMode ? "" : "none";
    ra.style.display = replaceMode ? "" : "none";
    feFind(fi.value);
    fi.focus(); fi.select();
  } else {
    state.findQ = ""; state.findMatches = []; state.findIdx = 0;
    feRenderFindLayer();
    $("#feFindCount").textContent = "";
  }
}

function feFind(q) {
  const ta = $("#feText");
  state.findQ = q || "";
  state.findMatches = feFindMatches(ta.value, state.findQ);
  state.findIdx = state.findIdx >= state.findMatches.length ? 0 : state.findIdx;
  feRenderFindLayer();
  feUpdateFindCount();
}

function feUpdateFindCount() {
  const c = $("#feFindCount");
  if (c) c.textContent = state.findQ ? `${state.findMatches.length ? state.findIdx + 1 : 0}/${state.findMatches.length}` : "";
}

function feRenderFindLayer() {
  const layer = $("#feFindLayer"); const ta = $("#feText");
  if (!layer) return;
  layer.innerHTML = "";
  if (!state.findQ || !state.findMatches.length) return;
  const lines = ta.value.split("\n");
  const offs = []; let acc = 0;
  for (const l of lines) { offs.push(acc); acc += l.length + 1; }
  const lineH = feLineH(), charW = feCharW();
  const info = feWrapCache();
  const padL = 14, padT = 12;
  const frag = document.createDocumentFragment();
  const shown = state.findMatches.slice(0, 500); // 匹配过多时限制渲染，避免卡顿
  shown.forEach((m, i) => {
    let lo = 0, hi = offs.length - 1;
    while (lo < hi) { const mid = (lo + hi + 1) >> 1; if (offs[mid] <= m.start) lo = mid; else hi = mid - 1; }
    // 软换行后匹配可能跨渲染行：拆成多个矩形段精确定位
    const segs = feMatchSegments(ta.value, lo, offs[lo], m.start - offs[lo], m.end - m.start, info, charW, padL, padT, lineH);
    segs.forEach((s, si) => {
      const sp = document.createElement("span");
      sp.className = "fm" + (i === state.findIdx && si === 0 ? " active" : "");
      sp.style.left = s.left + "px";
      sp.style.top = s.top + "px";
      sp.style.width = s.width + "px";
      sp.style.height = lineH + "px";
      frag.appendChild(sp);
    });
  });
  layer.appendChild(frag);
}

// 匹配矩形段：逻辑行 lo 内 [colStart, colStart+len) 字符在软换行后的屏幕矩形列表
function feMatchSegments(text, lo, lineStart, colStart, len, info, charW, padL, padT, lineH) {
  const wrapCols = info.wrapCols;
  const s = fePosInLine(text, lineStart, colStart, wrapCols);
  const e = fePosInLine(text, lineStart, colStart + len, wrapCols);
  const base = info.cum[lo];
  const segs = [];
  let row = s.row, col = s.col;
  for (;;) {
    const top = padT + (base + row) * lineH;
    const left = padL + col * charW;
    if (row === e.row) {
      segs.push({ top, left, width: Math.max(2, (e.col - col) * charW) });
      break;
    }
    segs.push({ top, left, width: Math.max(2, (wrapCols - col) * charW) });
    row++; col = 0;
  }
  return segs;
}

function feFindNav(dir) {
  const ms = state.findMatches;
  if (!ms.length) return;
  state.findIdx = (state.findIdx + dir + ms.length) % ms.length;
  const m = ms[state.findIdx];
  const ta = $("#feText");
  ta.setSelectionRange(m.start, m.end);
  const line = (ta.value.slice(0, m.start).match(/\n/g) || []).length;
  feScrollToLine(line);
  feRenderFindLayer();
  feUpdateFindCount();
}

function feReplace() {
  const ta = $("#feText");
  const m = state.findMatches[state.findIdx];
  if (!m || !state.findQ) return;
  const rp = $("#feReplaceInput").value;
  ta.value = ta.value.slice(0, m.start) + rp + ta.value.slice(m.end);
  ta.setSelectionRange(m.start, m.start + rp.length);
  markDirty();
  feFind(state.findQ);
  feFindNav(0);
  feRenderHighlight(); feRenderGutter(); feUpdateStatus();
}

function feReplaceAll() {
  const ta = $("#feText");
  if (!state.findQ) return;
  const rp = $("#feReplaceInput").value;
  const count = state.findMatches.length;
  if (!count) return;
  ta.value = ta.value.split(state.findQ).join(rp);
  markDirty();
  feFind(state.findQ);
  feRenderHighlight(); feRenderGutter(); feUpdateStatus();
  toast(`已替换 ${count} 处`);
}

// Tab 缩进（多行选择整体缩进/反缩进）
function feInsertTab(shift) {
  const ta = $("#feText");
  const s = ta.selectionStart, e = ta.selectionEnd;
  const v = ta.value, ind = "  ";
  if (s === e) {
    if (shift) return;
    ta.value = v.slice(0, s) + ind + v.slice(e);
    ta.setSelectionRange(s + ind.length, s + ind.length);
  } else {
    const ls = v.lastIndexOf("\n", s - 1) + 1;
    const le = v.indexOf("\n", e);
    const end = le < 0 ? v.length : le;
    const block = v.slice(ls, end);
    const nlines = block.split("\n").map(l => shift ? (l.startsWith(ind) ? l.slice(ind.length) : l) : ind + l);
    const nv = nlines.join("\n");
    ta.value = v.slice(0, ls) + nv + v.slice(end);
    ta.setSelectionRange(ls, ls + nv.length);
  }
  markDirty();
  feRenderHighlight(); feRenderGutter(); feUpdateStatus();
}

// 编辑器一次性初始化（事件绑定）
function initFileEditor() {
  if (state.feInit) return;
  state.feInit = true;
  const ta = $("#feText");
  $("#feSave").onclick = saveFileTab;
  $("#feClose").onclick = () => closeTab(state.activeTab);
  $("#feViewSplit").onclick = () => feApplyView("split");
  $("#feViewEdit").onclick = () => feApplyView("edit");
  $("#feViewPrev").onclick = () => feApplyView("prev");
  $("#feFindClose").onclick = () => { feToggleFind(false); ta.focus(); };
  $("#feFindNext").onclick = () => feFindNav(1);
  $("#feFindPrev").onclick = () => feFindNav(-1);
  $("#feReplaceBtn").onclick = () => feReplace();
  $("#feReplaceAllBtn").onclick = () => feReplaceAll();
  const fi = $("#feFindInput"), ri = $("#feReplaceInput");
  fi.addEventListener("input", () => feFind(fi.value));
  fi.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); feFindNav(e.shiftKey ? -1 : 1); }
    else if (e.key === "Escape") { e.preventDefault(); feToggleFind(false); ta.focus(); }
  });
  ri.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); feReplace(); }
    else if (e.key === "Escape") { e.preventDefault(); feToggleFind(false); ta.focus(); }
  });
  ta.addEventListener("input", () => {
    markDirty();
    feRenderHighlight();
    feRenderGutter();
    feSchedulePreview();
    feUpdateStatus();
    if (state.findQ) feFind(state.findQ);
  });
  ta.addEventListener("scroll", () => {
    if (state.feSyncing) return;
    if (feConsumeExpect(ta)) return; // 程序赋值回环：吞掉，不同步
    const hl = $("#feHighlight"), g = $("#feGutter"), fl = $("#feFindLayer");
    hl.scrollTop = ta.scrollTop; hl.scrollLeft = ta.scrollLeft;
    fl.scrollTop = ta.scrollTop; fl.scrollLeft = ta.scrollLeft;
    if (state.feGutterFull) g.scrollTop = ta.scrollTop; else feRenderGutter();
    feSyncPreviewFromEdit();
  });
  ta.addEventListener("select", feUpdateStatus);
  ta.addEventListener("click", feUpdateStatus);
  ta.addEventListener("keyup", feUpdateStatus);
  ta.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "s") { e.preventDefault(); saveFileTab(); }
    else if ((e.metaKey || e.ctrlKey) && e.key === "f") { e.preventDefault(); feToggleFind(true, false); }
    else if ((e.metaKey || e.ctrlKey) && e.key === "h") { e.preventDefault(); feToggleFind(true, true); }
    else if (e.key === "Tab") { e.preventDefault(); feInsertTab(e.shiftKey); }
    else if (e.key === "Escape" && !e.metaKey && $("#feFindBox").style.display !== "none") { e.preventDefault(); feToggleFind(false); }
  });
  $("#fePreview").addEventListener("scroll", () => {
    if (state.feSyncing) return;
    if (feConsumeExpect($("#fePreview"))) return; // 程序赋值回环：吞掉，不同步
    feSyncEditFromPreview();
  });
}

function feRenderAll() {
  feRenderHighlight();
  feRenderGutter();
  feUpdateStatus();
  if (state.findQ) feRenderFindLayer();
  const tab = curFileTab();
  if (tab && tab.view !== "edit" && feIsMarkdown(tab.id)) feRenderPreview();
}

function renderFileEditor() {
  const tab = curFileTab();
  if (!tab) { hideFileEditor(); return; }
  initFileEditor();
  const ed = $("#fileEditor");
  ed.style.display = "flex";
  $("#msgList").style.display = "none";
  $("#inputZone").style.display = "none";
  $("#msgWrap").style.display = "none";
  ed.querySelector(".fe-path").textContent = (tab.readonly ? "🔒 " : "") + tab.id;
  ed.querySelector(".fe-dirty").textContent = tab.dirty ? "● 未保存" : "";
  // 只读 tab（非当前项目文件）：禁用保存按钮与编辑（textarea readonly）
  const saveBtn = $("#feSave");
  if (saveBtn) saveBtn.disabled = !!tab.readonly;
  const ta = $("#feText");
  if (ta) ta.readOnly = !!tab.readonly;
  // 视图模式每文件记忆：默认 md=分栏、其他=原文
  if (!tab.view) {
    tab.view = state.fileViews[tab.id] || (feIsMarkdown(tab.id) ? "split" : "edit");
  }
  feApplyView(tab.view);
  if (!tab.dirty && ta.value !== tab.content) { ta.value = tab.content; ta.setSelectionRange(0, 0); }
  // 切换文件后查找状态按新内容重算，防止旧匹配错位渲染
  if (state.findQ) feFind(state.findQ);
  feRenderAll();
  ta.focus();
}

function hideFileEditor() {
  const editor = $("#fileEditor");
  if (editor) editor.style.display = "none";
  clearTimeout(state.fePrevTimer); // 关闭时取消挂起的预览渲染
  const mw = $("#msgWrap");
  const msg = $("#msgList");
  const iz = $("#inputZone");
  if (mw) mw.style.display = "";
  if (msg) msg.style.display = "";
  if (iz) iz.style.display = "";
  // ✅ 修复：恢复 #msgWrap 显示（此前只恢复了 msgList/inputZone，漏掉了外层 msgWrap——
  // 导致文件编辑器 flex:1 与 msgWrap flex:1 各占一半，下半部分被 msgWrap 挡住）
}

async function saveFileTab() {
  const tab = curFileTab();
  if (!tab) return;
  if (tab.readonly) {
    toast("只读项目：请先切换到该项目再修改", "err");
    return;
  }
  const ta = $("#feText");
  if (!ta) return;
  try {
    const r = await req("fs.write", { path: tab.id, content: ta.value, root: tab.root });
    tab.content = ta.value;
    tab.origContent = ta.value;
    tab.dirty = false;
    renderTabs();
    const d = document.querySelector("#fileEditor .fe-dirty");
    if (d) d.textContent = "";
    feRenderPreview();
    toast("已保存 " + tab.title + (r.size != null ? `（${r.size}B）` : ""));
  } catch (e) {
    toast("保存失败: " + msgOf(e), "err");
  }
}

/* ═══════════ Agent 观察面板 ═══════════ */
async function refreshGoalStatus() {
  if (!state.ws || state.ws.readyState !== 1) return;
  try {
    const g = await req("goal.status");
    renderGoalPanel(g);
  } catch (_) {}
}

// startAgentPolling Agent 面板打开时 3s 轮询 goal.status（阶段/轮次/步骤实时，
// 修复旧缺陷根因 E：goal 面板原本是打开瞬间的静态快照）。断连期间
// refreshGoalStatus 内部有 ws.readyState 守卫直接返回，重连后下一 tick 自动恢复。
function startAgentPolling() {
  stopAgentPolling();
  state.agentTimer = setInterval(refreshGoalStatus, 3000);
}
function stopAgentPolling() {
  if (state.agentTimer) { clearInterval(state.agentTimer); state.agentTimer = 0; }
}

function renderGoalPanel(g) {
  const panel = $("#goalPanel");
  if (!panel) return;
  if (!g || !g.active) {
    // 目标未激活但子任务可能仍在运行（普通 chat 模式 spawn_task 并行）：
    // 提示指向上方任务区，避免与"无 Agent 活动"冲突造成误解
    const n = runningSubCount();
    panel.innerHTML = n > 0
      ? `<div class="cfg-empty">目标模式未激活 · ${n} 个子任务运行中（见上方任务区）</div>`
      : `<div class="cfg-empty">当前无 Agent 活动</div>`;
    return;
  }
  $("#goalPhase").textContent = `阶段: ${g.phase || "—"}`;
  $("#goalRound").textContent = `轮次: ${g.round}${g.maxRounds ? "/" + g.maxRounds : ""}${g.goal ? " · " + esc(g.goal).slice(0, 40) : ""}`;
  const total = g.totalSteps || 0;
  const done = g.doneSteps || 0;
  const bar = $("#goalBar");
  if (bar) bar.style.width = total ? Math.round(done / total * 100) + "%" : "0%";
  const steps = $("#goalSteps");
  steps.innerHTML = "";
  const list = g.steps || [];
  for (const st of list) {
    const d = document.createElement("div");
    d.className = "goal-step" + (st.status === "done" ? " done" : "");
    const ico = st.status === "done" ? "✅" : st.status === "running" ? "⏳" : st.status === "failed" ? "❌" : "○";
    d.innerHTML = `<span class="gs-ico">${ico}</span><span>${esc(st.desc || st.id)}</span>`;
    steps.appendChild(d);
  }
  const score = $("#goalScore");
  if (score) {
    if (g.score > 0) {
      score.innerHTML = `监工评分: <span class="${g.passed ? "pass" : "fail"}">${Math.round(g.score)}/100 ${g.passed ? "✅ 通过" : "⚠ 未通过"}</span>`;
    } else {
      score.textContent = "";
    }
  }
  // v2 自进化扩展：缺陷清单 / 收敛告警 / 最近反思（机制②①④）
  const defects = $("#goalDefects");
  if (defects) {
    const list = g.defects || [];
    const stalled = g.stalledRounds >= 2;
    let html = "";
    if (list.length > 0) {
      html += `<div class="goal-sub">🔧 待修复缺陷 (${list.length})</div>`;
      for (const d of list) html += `<div class="goal-defect">⚠ ${esc(d.desc || d.id)}</div>`;
    }
    if (stalled) html += `<div class="goal-sub warn">⚠ 已连续 ${g.stalledRounds} 轮无实质进展（将强制换策略）</div>`;
    const refs = g.reflections || [];
    if (refs.length > 0 && refs[0].summary) {
      html += `<div class="goal-sub">🧠 最近反思 (轮 ${refs[0].round}): ${esc(String(refs[0].summary).slice(0, 60))}</div>`;
      if (refs[0].problems) html += `<div class="goal-defect">❓ ${esc(String(refs[0].problems).slice(0, 80))}</div>`;
    }
    defects.innerHTML = html;
  }
  // 注意：#agentTasks 专属于子任务实时面板（handleOrchestration 聚合），
  // 此处【绝不】清空/写入——goal 计划步骤已由上方 #goalSteps 渲染
  // （旧缺陷根因 C/D：renderGoalPanel 清空 #agentTasks 会抹掉子任务实时行，
  //  且 pendingSteps 是计划步骤而非实际运行的子 Agent，导致"只显示一个在活动"）。
}

function setAgentStatus(text, busy) {
  const el = $("#agentStatus");
  if (!el) return;
  el.textContent = text;
  el.classList.toggle("busy", busy);
}

/* ═══════════ 配置面板 ═══════════ */
async function loadConfigPanel() {
  if (!state.ws || state.ws.readyState !== 1) return;
  try {
    const [skills, plugins, paths] = await Promise.all([
      req("skill.list").catch(() => null),
      req("plugin.list").catch(() => null),
      req("paths").catch(() => null),
    ]);
    renderConfigSkills(skills);
    renderConfigPlugins(plugins);
    renderConfigPaths(paths);
    loadConfigSettings(); // 配置文件设置表单（独立加载，失败不阻塞信息区）
    wsInit(); // 工作台配置（员工/工具链/场景/门禁 GUI 全配置化）
  } catch (e) {
    toast("加载配置失败: " + msgOf(e), "err");
  }
}

/* ── 配置文件设置（schema 驱动表单，config.get/config.set 链路） ── */

// 加载 schema + 当前值，渲染表单
async function loadConfigSettings() {
  try {
    const r = await req("config.get", {});
    if (!r || !Array.isArray(r.schema)) throw new Error("config.get 返回结构异常");
    state.cfgValues = r.values || {};
    state.cfgSchema = r.schema;
    state.cfgPath = r.path || "";
    // 多模型预设（下拉切换；失败不阻塞表单）
    try {
      const mr = await req("model.list", {});
      state.modelPresets = (mr && Array.isArray(mr.presets)) ? mr.presets : [];
      state.modelCurrent = (mr && mr.current) || "";
    } catch (_) { state.modelPresets = []; state.modelCurrent = ""; }
    renderConfigSettings();
  } catch (e) {
    const form = $("#cfgYamlForm");
    if (form) form.innerHTML = `<div class="cfg-empty">加载失败: ${esc(msgOf(e))}</div>`;
  }
}

// 按分组渲染字段表单
function renderConfigSettings() {
  const form = $("#cfgYamlForm");
  if (!form) return;
  const pathEl = $("#cfgYamlPath");
  if (pathEl) pathEl.textContent = state.cfgPath ? state.cfgPath : "（未找到 config.yaml，保存时写入默认路径）";
  const groups = [];
  const idx = {};
  for (const f of state.cfgSchema) {
    // 注意：必须用 `in` 判断 key 是否存在——不能写 `if (!idx[f.group])`，
    // 因为 idx 值为 0（第一个分组，如"模型"）是 falsy，会导致该分组被
    // 重复创建两次、渲染出两个"模型预设"下拉。
    if (!(f.group in idx)) { idx[f.group] = groups.length; groups.push({ name: f.group, fields: [] }); }
    groups[idx[f.group]].fields.push(f);
  }
  let html = "";
  for (const g of groups) {
    html += `<div class="cfg-yaml-group"><div class="cfg-yaml-gname">${esc(g.name)}</div>`;
    // 模型预设下拉：置于"模型"分组顶部（多模型一键切换，保存即生效）
    // 布局：标签一行 / 下拉整行 / 切换按钮+当前模型一行——避免默认侧栏宽度截断
    if (g.name === "模型" && Array.isArray(state.modelPresets) && state.modelPresets.length > 0) {
      html += `<div class="cfg-preset-row">
        <span class="cfg-preset-label">模型预设</span>
        <select id="cfgModelPreset" class="cfg-preset-select">
          ${state.modelPresets.map(p =>
            `<option value="${esc(p.name)}" ${p.name === state.modelCurrent ? "selected" : ""}>${esc(p.name)}</option>`
          ).join("")}
        </select>
        <span class="cfg-preset-actions">
          <button type="button" id="cfgModelSwitch" class="ghost-btn small">切换</button>
          <button type="button" id="cfgModelNew" class="ghost-btn small">＋新建</button>
          <button type="button" id="cfgModelDel" class="ghost-btn small danger">删除</button>
          <span class="cfg-preset-hint">当前: ${esc(state.modelCurrent || "—")}</span>
        </span></div>`;
    }
    for (const f of g.fields) html += renderCfgField(f);
    html += `</div>`;
  }
  form.innerHTML = html;
  // 模型预设：下拉 change 即切换（选择即带出该模型参数）+ 切换按钮兜底
  // 模型设置面板 = "模型"分组：下拉（唯一选择入口）+ 下方该模型全部参数字段
  async function switchModelPreset(sel, btn) {
    const name = sel ? sel.value : "";
    if (!name) return;
    if (btn) btn.disabled = true;
    try {
      await req("model.switch", { name });
      toast(`已切换模型预设: ${name}`, "ok");
      refreshStatus();
      refreshModelInfo(); // 状态栏 #modelInfo（backend · effort）立即同步，不等 2s 轮询
      await loadConfigSettings(); // 重新拉取 config.get → 表单刷新为该模型参数值
    } catch (e) {
      toast(`切换失败: ${msgOf(e)}`, "err");
    } finally {
      if (btn) btn.disabled = false;
    }
  }
  const sel = form.querySelector("#cfgModelPreset");
  if (sel) sel.onchange = () => switchModelPreset(sel, form.querySelector("#cfgModelSwitch"));
  const sw = form.querySelector("#cfgModelSwitch");
  if (sw) sw.onclick = () => switchModelPreset(form.querySelector("#cfgModelPreset"), sw);
  // 模型预设"新建"：弹出傻瓜表单，提交后创建并自动切换激活
  const nw = form.querySelector("#cfgModelNew");
  if (nw) nw.onclick = async () => { await openModelCreateDialog(); };
  // 模型预设"删除"：删除当前下拉选中的预设（删激活项后端自动切换并回传 current）
  const del = form.querySelector("#cfgModelDel");
  if (del) del.onclick = async () => {
    const selEl = form.querySelector("#cfgModelPreset");
    const name = selEl ? selEl.value : "";
    if (!name) { toast("请先选择要删除的模型", "err"); return; }
    if (!await confirmDialog(`确定删除模型配置「${name}」？此操作会写回 config.yaml，不可撤销。`, { danger: true, okText: "删除" })) return;
    del.disabled = true;
    try {
      await req("model.delete", { name });
      toast(`已删除模型: ${name}`, "ok");
      refreshStatus();
      refreshModelInfo();
      await loadConfigSettings(); // 重新拉取预设下拉 + 该模型参数字段
    } catch (e) {
      toast(`删除失败: ${msgOf(e)}`, "err");
    } finally {
      del.disabled = false;
    }
  };
  // 绑定控件
  for (const f of state.cfgSchema) {
    const el = form.querySelector(`[data-cfgkey="${f.key}"]`);
    if (!el) continue;
    if (f.type === "number") {
      el.addEventListener("change", () => {
        const v = parseFloat(el.value);
        if (!isNaN(v)) el.dataset.dirty = "1";
        else el.dataset.dirty = "";
        updateCfgSaveState();
      });
    } else if (f.type === "bool") {
      el.addEventListener("change", () => { el.dataset.dirty = "1"; updateCfgSaveState(); });
    } else {
      el.addEventListener("input", () => { el.dataset.dirty = "1"; updateCfgSaveState(); });
    }
  }
  updateCfgSaveState();
}

function renderCfgField(f) {
  const v = state.cfgValues[f.key];
  const val = v === undefined || v === null ? "" : v;
  let ctl = "";
  if (f.type === "bool") {
    ctl = `<label class="cfg-switch" title="${esc(f.desc || "")}">
      <input type="checkbox" data-cfgkey="${f.key}" ${val ? "checked" : ""}><span class="cfg-switch-slider"></span></label>`;
  } else if (f.type === "select") {
    // 枚举首项为空字符串（model.thinking_length：""=继承 CLI 默认）：仅显示
    // 文案"（继承默认）"，value 仍为空串——提交值语义不变。
    ctl = `<select data-cfgkey="${f.key}" title="${esc(f.desc || "")}">
      ${(f.enums || []).map(e => `<option value="${esc(e)}"${String(e) === String(val) ? " selected" : ""}>${e === "" ? "（继承默认）" : esc(e)}</option>`).join("")}</select>`;
  } else if (f.type === "password") {
    ctl = `<input type="password" data-cfgkey="${f.key}" spellcheck="false" autocomplete="off"
      placeholder="${val ? "已设置 " + esc(String(val)) + "（留空不修改）" : "未设置"}" title="${esc(f.desc || "")}">`;
  } else if (f.type === "number") {
    const min = f.min !== undefined && f.min !== null ? ` min="${f.min}"` : "";
    const max = f.max !== undefined && f.max !== null ? ` max="${f.max}"` : "";
    const step = f.step ? ` step="${f.step}"` : "";
    ctl = `<input type="number" data-cfgkey="${f.key}" value="${esc(String(val))}"${min}${max}${step} title="${esc(f.desc || "")}">`;
  } else {
    ctl = `<input type="text" data-cfgkey="${f.key}" value="${esc(String(val))}" spellcheck="false" title="${esc(f.desc || "")}">`;
  }
  return `<div class="cfg-yaml-row" title="${esc(f.desc || "")}">
    <span class="cfg-yaml-k">${esc(f.label)}</span>
    <span class="cfg-yaml-ctl">${ctl}</span>
    <span class="cfg-yaml-desc">${esc(f.desc || "")}</span>
  </div>`;
}

// 保存：diff 收集变更字段 → config.set（api_key 输入为空不提交）
async function saveConfigSettings() {
  const form = $("#cfgYamlForm");
  if (!form) return;
  const patch = {};
  for (const f of state.cfgSchema) {
    const el = form.querySelector(`[data-cfgkey="${f.key}"]`);
    if (!el) continue;
    if (f.type === "password") {
      const s = el.value.trim();
      if (!s || s.includes("****")) continue; // 留空/脱敏 → 不修改
      patch[f.key] = s;
      continue;
    }
    let cur;
    if (f.type === "bool") cur = el.checked;
    else if (f.type === "number") cur = parseFloat(el.value);
    else cur = el.value;
    const orig = state.cfgValues[f.key];
    if (String(cur) !== String(orig === undefined || orig === null ? "" : orig)) patch[f.key] = cur;
  }
  const keys = Object.keys(patch);
  if (!keys.length) { toast("没有需要保存的修改"); return; }
  try {
    const r = await req("config.set", patch);
    const nr = (r && Array.isArray(r.needsRestart) && r.needsRestart.length) ? r.needsRestart : [];
    if (nr.length) {
      toast(`已保存 ${keys.length} 项设置，${nr.length} 项重启后生效（${nr.join("、")}）`, "warn");
    } else {
      toast("已保存 " + keys.length + " 项设置 → " + (state.cfgPath || "config.yaml") + "（原文件已备份 .bak）");
    }
    loadConfigSettings(); // 重新加载（含后端修正后的值）
    refreshStatus();      // 状态栏立即同步（mode/ctx/模型信息），不等 2s 轮询
    refreshModelInfo();   // #modelInfo 双保险即时刷新（backend · effort）
  } catch (e) {
    toast("保存失败: " + msgOf(e), "err");
  }
}

// 重置：重新加载丢弃本地修改
async function resetConfigSettings() {
  toast("已放弃未保存的修改");
  await loadConfigSettings();
}

function updateCfgSaveState() {
  const form = $("#cfgYamlForm");
  if (!form) return;
  let dirty = 0;
  form.querySelectorAll("[data-cfgkey]").forEach(el => { if (el.dataset.dirty) dirty++; });
  const hint = $("#cfgYamlHint");
  if (hint) hint.textContent = dirty ? `有 ${dirty} 项未保存修改` : "";
}

function renderConfigSkills(sk) {
  const body = $("#cfgSkillsBody");
  if (!body) return;
  if (!sk || !sk.skills || !sk.skills.length) {
    body.innerHTML = '<div class="cfg-empty">无技能</div>';
    return;
  }
  body.innerHTML = "";
  for (const s of sk.skills) {
    const row = document.createElement("div");
    row.className = "cfg-row";
    row.innerHTML = `<span class="k">${esc(s.name)}</span>
      <label class="v" title="${esc(s.desc || "")}">
        <input type="checkbox" class="skill-toggle" ${s.active ? "checked" : ""}>
      </label>`;
    const cb = row.querySelector("input");
    cb.onchange = async () => {
      try { await req("skill.toggle", { name: s.name, active: cb.checked }); toast(`${s.name} ${cb.checked ? "已启用" : "已停用"}`); }
      catch (e) { cb.checked = !cb.checked; toast("设置失败: " + msgOf(e), "err"); }
    };
    body.appendChild(row);
  }
}

function renderConfigPlugins(pl) {
  const body = $("#cfgPluginsBody");
  if (!body) return;
  if (!pl || !pl.plugins) { body.innerHTML = '<div class="cfg-empty">无插件</div>'; return; }
  body.innerHTML = `<pre>${esc(pl.plugins)}</pre>`;
}

function renderConfigPaths(paths) {
  const body = $("#cfgPathsBody");
  if (!body) return;
  if (!paths) { body.innerHTML = '<div class="cfg-empty">无法获取</div>'; return; }
  body.innerHTML = "";
  for (const [k, v] of Object.entries(paths)) {
    const row = document.createElement("div");
    row.className = "cfg-row";
    row.innerHTML = `<span class="k">${esc(k)}</span><span class="v" title="${esc(v)}">${esc(v)}</span>`;
    body.appendChild(row);
  }
}

/* ═══════════ 日志面板 ═══════════ */
async function refreshLogs() {
  const list = $("#logList");
  if (!list || list.style.display === "none") return;
  try {
    const r = await req("log.tail");
    const lines = r.lines || [];
    if (!lines.length) {
      if (!list.dataset.hasContent) {
        list.innerHTML = '<div class="log-empty">暂无日志</div>';
        list.dataset.hasContent = "1";
      }
      return;
    }
    list.dataset.hasContent = "1";
    const frag = document.createDocumentFragment();
    for (const l of lines) {
      const d = document.createElement("div");
      d.className = "log-line";
      d.innerHTML = `<span class="lt">[${esc(l.ts)}]</span> <span class="lv-${esc(l.level)}">[${esc(l.level)}]</span> ${esc(l.msg)}`;
      frag.appendChild(d);
    }
    list.innerHTML = "";
    list.appendChild(frag);
  } catch (_) {}
}

function startLogPolling() {
  if (state.logTimer) return;
  state.logTimer = setInterval(() => {
    if (state.activePanel === "logs") refreshLogs();
  }, 5000);
}

function stopLogPolling() {
  if (state.logTimer) { clearInterval(state.logTimer); state.logTimer = 0; }
}

/* ═══════════ 右键菜单 ═══════════ */
function showContextMenu(x, y, items, onClose) {
  const menu = $("#contextMenu");
  menu.innerHTML = "";
  for (const it of items) {
    if (it.sep) {
      const s = document.createElement("div");
      s.className = "cm-sep";
      menu.appendChild(s);
      continue;
    }
    const d = document.createElement("div");
    d.className = "cm-item" + (it.danger ? " danger" : "");
    d.textContent = it.label;
    d.onclick = () => {
      hideContextMenu();
      it.fn && it.fn();
    };
    menu.appendChild(d);
  }
  menu.style.display = "block";
  const mw = 170, mh = menu.offsetHeight || items.length * 30;
  menu.style.left = Math.min(x, window.innerWidth - mw - 8) + "px";
  menu.style.top = Math.min(y, window.innerHeight - mh - 8) + "px";
  state.ctxMenuClose = onClose || null;
}

function hideContextMenu() {
  const menu = $("#contextMenu");
  if (menu) menu.style.display = "none";
  if (state.ctxMenuClose) { state.ctxMenuClose(); state.ctxMenuClose = null; }
}

/* ═══════════ 附件 ═══════════ */
const IMG_EXTS = [".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp"];
function isImageFile(name) {
  const lower = name.toLowerCase();
  return IMG_EXTS.some(e => lower.endsWith(e));
}

function addAttachment(file) {
  if (state.attachments.length >= 8) { toast("附件最多 8 个", "warn"); return; }
  const att = { name: file.name, size: file.size, content: "" };
  state.attachments.push(att);
  renderAttachments();
  // 图片附件：readAsText 会把二进制读成乱码；改为 readAsDataURL（base64），
  // 后端解码写临时文件 → 走 vision 识图链路（[图片: 绝对路径] → 多模态 →
  // 自动切换识图模型）。大小不限——base64 由后端解码，不进上下文文本。
  if (isImageFile(file.name)) {
    const reader = new FileReader();
    reader.onload = () => {
      att.content = String(reader.result || ""); // data:image/png;base64,...
      renderAttachments();
    };
    reader.onerror = () => {
      att.content = "";
      toast("读取图片失败: " + file.name, "err");
    };
    reader.readAsDataURL(file);
    toast(`图片附件 ${file.name}：将自动走识图模式`, "ok");
    return;
  }
  // 非图片文件 >200KB：仅记录文件名，不读取内容（防止超大附件撑爆上下文）
  if (file.size > 200 * 1024) {
    toast(`附件过大（${(file.size / 1024).toFixed(0)}KB），仅记录文件名`, "warn");
    return;
  }
  // 真实读取文件内容（文本），供模型参考
  const reader = new FileReader();
  reader.onload = () => {
    att.content = String(reader.result || "");
    renderAttachments();
  };
  reader.onerror = () => {
    att.content = "";
    toast("读取附件失败: " + file.name, "err");
  };
  reader.readAsText(file);
}

function clearAttachments() {
  state.attachments = [];
  renderAttachments();
}

/* 对话图片点击放大（lightbox）：全屏遮罩 + 居中原图，点击任意处/Esc 关闭。
 * 图片源是 data URI（后端 messagesToJSON 输出），无需网络请求。 */
function showImagePreview(url) {
  let wrap = document.getElementById("imgLightbox");
  if (!wrap) {
    wrap = document.createElement("div");
    wrap.id = "imgLightbox";
    wrap.className = "img-lightbox";
    document.body.appendChild(wrap);
  }
  wrap.innerHTML = "";
  const img = document.createElement("img");
  img.src = url;
  wrap.appendChild(img);
  wrap.classList.add("open");
  const close = () => {
    wrap.classList.remove("open");
    wrap.innerHTML = "";
  };
  wrap.onclick = close;
  document.onkeydown = (e) => { if (e.key === "Escape") { close(); document.onkeydown = null; } };
}

function renderAttachments() {
  const wrap = $("#attachments");
  if (!wrap) return;
  wrap.innerHTML = "";
  for (const a of state.attachments) {
    const tag = document.createElement("div");
    tag.className = "att-tag";
    const size = a.size > 1024 * 1024 ? (a.size / 1024 / 1024).toFixed(1) + "MB" : Math.max(1, Math.round(a.size / 1024)) + "KB";
    tag.innerHTML = `📎 ${esc(a.name)} (${size})<button class="att-x">✕</button>`;
    tag.querySelector(".att-x").onclick = () => {
      state.attachments = state.attachments.filter(x => x.name !== a.name);
      renderAttachments();
    };
    wrap.appendChild(tag);
  }
}

/* ═══════════ 主题 ═══════════ */
function toggleTheme() {
  state.theme = state.theme === "dark" ? "light" : "dark";
  document.body.classList.toggle("theme-light", state.theme === "light");
  try { localStorage.setItem("contextloom-gui-theme", state.theme); } catch (_) {}
}

function initTheme() {
  try {
    const saved = localStorage.getItem("contextloom-gui-theme");
    if (saved) {
      state.theme = saved;
      document.body.classList.toggle("theme-light", saved === "light");
    }
  } catch (_) {}
}

/* ═══════════ 输入框 ═══════════ */
function autoGrow() {
  const ta = $("#input");
  ta.style.height = "auto";
  ta.style.height = Math.min(ta.scrollHeight, 180) + "px";
}

function setSendBtn(streaming) {
  const btn = $("#sendBtn");
  if (!btn) return;
  btn.classList.toggle("stop", streaming);
  btn.disabled = false;
  btn.title = streaming ? "停止生成" : "发送";
  btn.innerHTML = streaming
    ? '<svg viewBox="0 0 24 24" width="14" height="14" fill="currentColor"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>'
    : '<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 2L11 13"/><path d="M22 2l-7 20-4-9-9-4 20-7z"/></svg>';
}

/* ═══════════ 滚动 ═══════════ */
function scrollBottom(force) {
  const list = $("#msgList");
  const nearBottom = list.scrollHeight - list.scrollTop - list.clientHeight < 4;
  if (force || nearBottom || !state.userScrolledUp) {
    requestAnimationFrame(() => { list.scrollTop = list.scrollHeight; });
  }
}

/* ═══════════ 滚轮守卫（Windows/WebView2 滚动冲突修复） ═══════════ */
// 背景：WebView2（Chromium）中，鼠标滚轮默认滚动"光标下元素的最近可滚动
// 祖先"；但光标位于【不可滚动区域】（输入区空白、按钮、消息区间隙、
// 回到底部按钮、顶栏等）时，Chromium 会把滚轮交给【当前获得焦点的可滚动
// 元素】——即 #input 多行输入框（autoGrow 后自身可滚动），于是用户滚动
// 消息历史时输入框内容跟着滚（Windows 特有；macOS 无此行为）。
// 修复：全局滚轮守卫（passive:false）——光标下无任何可滚动祖先时，
// preventDefault 并把滚动转发到当前主滚动容器（消息列表/终端输出/编辑器
// 原文区）；光标下存在可滚动祖先则完全交给原生滚动，不干预既有交互。
function initWheelGuard() {
  // 当前主滚动容器：终端显示 → termBody；文件编辑器显示 → 原文区
  // （原文隐藏的纯预览模式 → 预览区）；否则 → 消息列表。
  function mainScroll() {
    const tp = $("#termPanel");
    if (tp && tp.offsetParent !== null) return $("#termBody");
    const ed = $("#fileEditor");
    if (ed && ed.offsetParent !== null) {
      const ta = $("#feText");
      if (ta && ta.offsetParent !== null) return ta;
      return $("#fePreview");
    }
    return $("#msgList");
  }
  const isScrollable = (el) =>
    el.scrollHeight > el.clientHeight + 1 || el.scrollWidth > el.clientWidth + 1;
  const nearestScrollable = (el) => {
    for (let n = el; n && n !== document.documentElement; n = n.parentElement) {
      if (isScrollable(n)) return n;
    }
    return null;
  };
  document.addEventListener("wheel", (e) => {
    // 1) 光标下存在可滚动元素/容器（消息列表、文件树、命令面板、编辑器、
    //    终端输出、侧栏、输入框自身等）：原生滚动，绝不干预
    if (e.target && nearestScrollable(e.target)) return;
    // 2) 全局覆盖层：不干预
    if (e.target && e.target.closest && e.target.closest("#contextMenu, #fatalBanner, #toastWrap")) return;
    // 3) 其余不可滚动区域：阻止默认行为（防聚焦输入框"劫持"滚轮），
    //    显式滚动当前主容器
    e.preventDefault();
    const sc = mainScroll();
    if (!sc) return;
    let dy = e.deltaY;
    if (e.deltaMode === 1) dy *= 16;       // DOM_DELTA_LINE → 像素
    else if (e.deltaMode === 2) dy *= 100; // DOM_DELTA_PAGE → 像素
    sc.scrollTop += dy;
  }, { passive: false });
}

/* ═══════════ 拖拽调整大小（通用 resizer） ═══════════ */
// 通用分隔条拖拽：pointerdown 捕获 → pointermove 计算 → pointerup 结束
// opts: { axis:"x"|"y"(默认 x), getValue, min, max(可为函数), invert, onStart, onResize, onEnd, onReset }
function makeResizer(handle, opts) {
  if (!handle || !opts) return;
  const isY = opts.axis === "y";
  let active = false, startPos = 0, startVal = 0;
  const clamp = (v) => {
    let lo = opts.min, hi = opts.max;
    if (typeof lo === "function") lo = lo();
    if (typeof hi === "function") hi = hi();
    if (lo != null) v = Math.max(lo, v);
    if (hi != null) v = Math.min(hi, v);
    return v;
  };
  handle.addEventListener("pointerdown", (e) => {
    if (e.button !== 0) return;
    e.preventDefault();
    active = true;
    startPos = isY ? e.clientY : e.clientX;
    startVal = opts.getValue ? opts.getValue() : 0;
    handle.classList.add("active");
    document.body.classList.add(isY ? "resizing-y" : "resizing-x");
    document.body.classList.add("resizing");
    if (opts.onStart) opts.onStart();
    try { handle.setPointerCapture(e.pointerId); } catch (_) {}
  });
  handle.addEventListener("pointermove", (e) => {
    if (!active) return;
    const pos = isY ? e.clientY : e.clientX;
    const d = pos - startPos;
    const v = clamp(startVal + (opts.invert ? -d : d));
    if (opts.onResize) opts.onResize(v);
  });
  const finish = (e) => {
    if (!active) return;
    active = false;
    handle.classList.remove("active");
    document.body.classList.remove("resizing");
    document.body.classList.remove("resizing-y");
    document.body.classList.remove("resizing-x");
    if (opts.onEnd) opts.onEnd();
    try { handle.releasePointerCapture(e.pointerId); } catch (_) {}
  };
  handle.addEventListener("pointerup", finish);
  handle.addEventListener("pointercancel", finish);
  handle.addEventListener("dblclick", () => { if (opts.onReset) opts.onReset(); });
}

const SIDE_W_MIN = 180, SIDE_W_MAX = 560, SIDE_W_DEFAULT = 264;
function applySideWidth(w, inline) {
  const leftCol = $("#leftCol");
  if (!leftCol) return;
  if (inline) { leftCol.style.width = w + "px"; leftCol.style.minWidth = w + "px"; }
  else { leftCol.style.width = ""; leftCol.style.minWidth = ""; }
}
function sideResizerMax() {
  return Math.max(SIDE_W_MIN, Math.min(SIDE_W_MAX, window.innerWidth - 340));
}
function initSideResizer() {
  const handle = $("#sideResizer");
  if (!handle) return;
  const saved = parseInt(localStorage.getItem("contextloom.sideW") || "", 10);
  if (saved && saved >= SIDE_W_MIN && saved <= SIDE_W_MAX) applySideWidth(saved, true);
  makeResizer(handle, {
    getValue: () => $("#leftCol") ? $("#leftCol").getBoundingClientRect().width : SIDE_W_DEFAULT,
    min: SIDE_W_MIN,
    max: sideResizerMax,
    onStart: () => { const l = $("#leftCol"); if (l) l.style.transition = "none"; },
    onResize: (w) => applySideWidth(w, true),
    onEnd: () => {
      const l = $("#leftCol");
      if (l) {
        l.style.transition = "";
        localStorage.setItem("contextloom.sideW", String(Math.round(l.getBoundingClientRect().width)));
      }
    },
    onReset: () => {
      localStorage.removeItem("contextloom.sideW");
      applySideWidth(SIDE_W_DEFAULT, false);
    },
  });
}

const AGENT_H_MIN = 60, AGENT_H_MAX = 320; // AGENT_H_MAX 仅为兜底（面板不可测时），实际上限由 agentTasksMax 动态计算
const AGENT_GOAL_MIN = 60; // goalPanel 保留最小高度（px）
// 动态上限：任务区可拉满至「面板可用高度 - 头部 - 分隔条 - goalPanel 最小高」，
// 修复固定 320px 导致「只能拖到一半 / 下方空白区无法利用」的问题。
function agentTasksMax() {
  const p = $("#panelAgent");
  if (!p || p.clientHeight <= 0) return AGENT_H_MAX;
  const hd = $("#agentHeader");
  const used = (hd ? hd.offsetHeight : 0) + 8 /* 分隔条命中区 */ + AGENT_GOAL_MIN;
  return Math.max(AGENT_H_MIN, Math.min(2000, p.clientHeight - used));
}
// 统一设置任务区高度（夹紧到 [MIN, 动态MAX]），供恢复/拖拽/窗口 resize 复用
function applyAgentH(h) {
  const tasks = $("#agentTasks");
  if (!tasks) return 0;
  const hh = Math.max(AGENT_H_MIN, Math.min(agentTasksMax(), h));
  tasks.style.height = hh + "px";
  tasks.style.maxHeight = hh + "px";
  return hh;
}
function initAgentResizer() {
  const handle = $("#agentResizer");
  const tasks = $("#agentTasks");
  if (!handle || !tasks) return;
  const saved = parseInt(localStorage.getItem("contextloom.agentTasksH") || "", 10);
  if (saved && saved >= AGENT_H_MIN) applyAgentH(saved);
  makeResizer(handle, {
    axis: "y",
    getValue: () => tasks.getBoundingClientRect().height,
    min: AGENT_H_MIN,
    max: agentTasksMax,
    onResize: (h) => applyAgentH(h),
    onEnd: () => localStorage.setItem("contextloom.agentTasksH", String(Math.round(tasks.getBoundingClientRect().height))),
    onReset: () => {
      localStorage.removeItem("contextloom.agentTasksH");
      tasks.style.height = "";
      tasks.style.maxHeight = "";
    },
  });
  // 窗口尺寸变化后按新可用空间重新夹紧（防止保存值在新窗口下溢出/无法拉满）
  window.addEventListener("resize", () => {
    if (tasks.style.height) applyAgentH(parseFloat(tasks.style.height));
  });
}

function initFeResizer() {
  const handle = $("#feResizer");
  if (!handle) return;
  const ed = $("#feEditCol");
  const feBody = () => document.querySelector(".fe-body");
  const saved = parseInt(localStorage.getItem("contextloom.feSplit") || "", 10);
  if (saved && saved >= 160) { if (ed) ed.style.flex = "0 0 " + saved + "px"; }
  makeResizer(handle, {
    getValue: () => (ed ? ed.getBoundingClientRect().width : 0),
    min: 160,
    max: () => Math.max(320, (feBody() ? feBody().getBoundingClientRect().width : 800) - 160),
    onResize: (w) => { if (ed) ed.style.flex = "0 0 " + w + "px"; },
    onEnd: () => {
      if (ed) localStorage.setItem("contextloom.feSplit", String(Math.round(ed.getBoundingClientRect().width)));
    },
    onReset: () => {
      localStorage.removeItem("contextloom.feSplit");
      if (ed) ed.style.flex = "";
    },
  });
}

/* ═══════════ 对话窗口/终端 高度拖拽（布局重构：终端在输入框下方） ═══════════ */
// 对话窗口（#msgWrap）高度拖拽：
//   - 范围：min 80px；max = chatArea 高度 - 输入区 - 终端最小高度（动态）
//   - 双击重置：flex:1 自适应（清除内联高度）
//   - localStorage 记忆（contextloom.msgWrapH）
const MSG_WRAP_H_MIN = 80;
// 终端高度自适应：flex:1 自动占满输入区下方剩余空间，无需独立拖拽条。
// 拖拽 #chatResizer（输入框上方的分隔条）调整对话区高度后，终端随剩余
// 空间自动变化（initChatResizer 的 onResize/onReset 已联动 fitTerm）。
const TERM_H_MIN = 60;
function msgWrapMax() {
  const ca = $("#chatArea");
  if (!ca || ca.clientHeight <= 0) return 100000;
  const iz = $("#inputZone");
  const izH = (iz && iz.style.display !== "none") ? iz.offsetHeight : 0;
  // 对话窗口最大高度 = chatArea - 输入区 - 终端最小高度（若终端可见）
  let max = ca.clientHeight - izH - 60;
  const tp = $("#termPanel");
  if (tp && tp.style.display !== "none") max -= TERM_H_MIN;
  return Math.max(MSG_WRAP_H_MIN, max - 40); // 额外留白，保证输入区/终端可见
}
function applyMsgWrapH(h) {
  const mw = $("#msgWrap");
  if (!mw) return 0;
  const hh = Math.max(MSG_WRAP_H_MIN, Math.min(msgWrapMax(), h));
  mw.style.flex = "0 0 " + hh + "px";
  return hh;
}
function initChatResizer() {
  const handle = $("#chatResizer");
  const mw = $("#msgWrap");
  if (!handle || !mw) return;
  const saved = parseInt(localStorage.getItem("contextloom.msgWrapH") || "", 10);
  if (saved && saved >= MSG_WRAP_H_MIN) applyMsgWrapH(saved);
  makeResizer(handle, {
    axis: "y",
    getValue: () => mw.getBoundingClientRect().height,
    min: MSG_WRAP_H_MIN,
    max: msgWrapMax,
    onResize: (h) => {
      applyMsgWrapH(h);
      // 对话区高度变化 → 终端（flex:1 自适应）占满剩余空间变化 →
      // 立即重新 fit 让 xterm 行数随新高度实时更新
      fitTerm(state.curTerm);
    },
    onEnd: () => localStorage.setItem("contextloom.msgWrapH", String(Math.round(mw.getBoundingClientRect().height))),
    onReset: () => {
      localStorage.removeItem("contextloom.msgWrapH");
      mw.style.flex = "";
      fitTerm(state.curTerm);
    },
  });
  window.addEventListener("resize", () => {
    if (mw.style.flex && mw.style.flex !== "1") {
      const cur = parseFloat(mw.style.flex);
      applyMsgWrapH(cur); // 重新夹紧到新窗口下的合法范围
    }
  });
}
/* ═══════════ 事件绑定 ═══════════ */
function bindEvents() {
  // 活动栏
  $$(".act-btn").forEach(b => b.onclick = () => {
    // 终端图标 toggle：面板可见时再次点击 = 关闭（输入框回到底部），
    // 隐藏时点击 = 打开；其余面板保持切换语义。
    if (b.dataset.panel === "term") {
      const tp = $("#termPanel");
      if (tp && tp.style.display !== "none") { closeTermPanel(); return; }
    }
    setPanel(b.dataset.panel);
  });
  // 终端关闭按钮：✕ 关闭终端面板，输入框回到底部；
  // 关闭后点击左侧 ▸_ 图标重新打开。
  const termCloseBtn = $("#termClose");
  if (termCloseBtn) termCloseBtn.onclick = closeTermPanel;

  // 模式切换
  $$(".mode-btn").forEach(b => b.onclick = async () => {
    const mode = b.dataset.mode;
    try { await req("mode.set", { mode }); state.mode = mode; setModeUI(mode); }
    catch (e) { toast("切换模式失败: " + msgOf(e), "err"); }
  });

  // 主题
  $("#themeToggle").onclick = toggleTheme;

  // 会话工具栏
  $("#sessionNew").onclick = newSession;
  $("#sessionRefresh").onclick = () => { lastSessionsSig = ""; loadSessions(); };

  // 项目管理器（功能第一）：添加（目录选择）/ 刷新
  const projAdd = $("#projAdd");
  if (projAdd) projAdd.onclick = pickProjectDir;
  const projRefresh = $("#projRefresh");
  if (projRefresh) projRefresh.onclick = renderProjectList;

  // 终端：新建（xterm.js 实例在 openTerm/首次输出时创建）；窗口尺寸变化自适应
  const tn = $("#termNew");
  if (tn) tn.onclick = openTerm;
  window.addEventListener("resize", fitAllTerms);

  $("#cfgRefresh").onclick = loadConfigPanel;
  const yamlSave = $("#cfgYamlSave");
  if (yamlSave) yamlSave.onclick = saveConfigSettings;
  const yamlReset = $("#cfgYamlReset");
  if (yamlReset) yamlReset.onclick = resetConfigSettings;
  $("#logClear").onclick = async () => {
    try { await req("log.clear", {}); $("#logList").innerHTML = ""; $("#logList").dataset.hasContent = ""; toast("日志已清空"); }
    catch (e) { toast("清空失败: " + msgOf(e), "err"); }
  };

  // 附件
  $("#attachBtn").onclick = () => {
    let inp = $("#fileInput");
    if (!inp) {
      inp = document.createElement("input");
      inp.id = "fileInput";
      inp.type = "file";
      inp.multiple = true;
      inp.style.display = "none";
      document.body.appendChild(inp);
      inp.onchange = () => {
        for (const f of inp.files) addAttachment(f);
        inp.value = "";
      };
    }
    inp.click();
  };

  // 输入框
  const ta = $("#input");
  ta.addEventListener("input", () => { autoGrow(); filterCmdPanel(); });
  ta.addEventListener("keydown", (e) => {
    if (e.key === "ArrowDown" && state.cmdPanel) { e.preventDefault(); moveCmdSel(1); return; }
    if (e.key === "ArrowUp" && state.cmdPanel) { e.preventDefault(); moveCmdSel(-1); return; }
    if (e.key === "Tab" && state.cmdPanel) {
      e.preventDefault();
      const item = state.cmdPanel.querySelector(".cmd-item.sel");
      if (item) { const c = CMD_LIST[+item.dataset.i];
        ta.value = c.name.split(" ")[0] + " "; autoGrow(); closeCmdPanel(); }
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      if (state.cmdPanel) { e.preventDefault(); closeCmdPanel(); return; }
      e.preventDefault(); sendMessage();
    }
    if (e.key === "Enter" && e.ctrlKey) { e.preventDefault(); sendMessage(); }
    if (e.key === "Escape") { closeCmdPanel(); }
    if ((e.ctrlKey || e.metaKey) && e.key === "l") {
      e.preventDefault();
      renderMessages([]);
      appendSystem("已清屏 (Ctrl+L)");
    }
    if ((e.ctrlKey || e.metaKey) && e.key === "s") {
      // 在文件编辑器中由编辑器处理；此处防浏览器保存弹窗
      e.preventDefault();
    }
  });
  ta.addEventListener("keyup", (e) => {
    if (e.key === "/" && ta.value === "/") { showCmdPanel(); }
    if (ta.value !== "/" && state.cmdPanel) filterCmdPanel();
  });

  // 用户手动上翻暂停自动滚动；离开底部时显示"回到底部"按钮
  // 阈值 4px（严格跟随）：用户哪怕上翻 1 行（≈20px）也立即暂停自动跟随，
  // 只有主动回到底部（或点击回到底部按钮）才恢复——满足"离开底部不强制下滑"。
  const list = $("#msgList");
  list.addEventListener("scroll", () => {
    const away = list.scrollHeight - list.scrollTop - list.clientHeight > 4;
    state.userScrolledUp = away;
    const btn = $("#scrollDown");
    if (btn) btn.classList.toggle("show", away && list.scrollHeight > list.clientHeight);
    if (away) placeScrollDown();
  });

  // 回到底部：点击后恢复自动跟随（流式输出不再强制下拉，用户主动回底才续跟）
  const scrollDownBtn = $("#scrollDown");
  if (scrollDownBtn) {
    scrollDownBtn.onclick = () => {
      const l = $("#msgList");
      l.scrollTop = l.scrollHeight;
      state.userScrolledUp = false;
      scrollDownBtn.classList.remove("show");
      l.focus();
    };
  }

  // 发送按钮：生成中 = 停止（stopStream 乐观重置 + 标记 streamId，残留事件丢弃）
  $("#sendBtn").onclick = () => {
    if (state.streaming) {
      stopStream(state.sessionId);
      setTimeout(() => {
        if (state.streaming) {
          state.streaming = false;
          state.streamMsg = null;
          state.streamSessionId = null;
          state.streamId = null;
          setSendBtn(false);
          setAgentStatus("", false);
          toast("已停止（后端未确认，状态已重置）", "warn");
        }
      }, 5000);
    } else sendMessage();
  };

  // 拖拽上传
  const zone = $("#inputZone");
  zone.addEventListener("dragover", (e) => { e.preventDefault(); });
  zone.addEventListener("drop", (e) => {
    e.preventDefault();
    const files = e.dataTransfer.files;
    if (files) for (const f of files) addAttachment(f);
  });

  // 全局关闭右键菜单 / 命令面板
  // 注意：命令面板的关闭条件必须排除整个 #inputZone（含快捷命令按钮区）——
  // 点击快捷按钮（如 /help）时，按钮 onclick 先创建面板，同一次点击冒泡到
  // document 后若命中关闭条件，面板会"刚创建就被删除"（表现为无响应）。
  document.addEventListener("click", (e) => {
    if (!e.target.closest("#contextMenu")) hideContextMenu();
    if (!e.target.closest("#cmdPanel") && !e.target.closest("#inputZone")) closeCmdPanel();
  });
  document.addEventListener("contextmenu", (e) => {
    // 面板外右键：默认菜单
    if (!e.target.closest(".tree-row") && !e.target.closest(".msg-row")) {
      hideContextMenu();
    }
  });
  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape") { hideContextMenu(); closeCmdPanel(); }
  });

  // 计时器
  setInterval(updateTimer, 30000);
}

/* ═══════════ 启动 ═══════════ */
function init() {
  initTheme();
  renderQuickCmds();
  renderTabs();
  setSendBtn(false);
  bindEvents();
  // 输入区尺寸变化（多行输入/附件/快捷命令行换行）时，动态保持回到底部
  // 按钮与发送/中止按钮不重叠；同时初始定位一次（ResizeObserver 不触发首帧）
  if (typeof ResizeObserver !== "undefined") {
    const izEl = $("#inputZone");
    if (izEl) new ResizeObserver(placeScrollDown).observe(izEl);
  }
  placeScrollDown();
  initSideResizer();
  initWheelGuard();
  initFeResizer();
  initAgentResizer();
  initChatResizer();
  // 默认打开项目管理器（功能第一，区别于会话管理器）；切换项目后的
  // 重载自动打开文件树（目录管理器跟随新项目根）。
  let startPanel = "projects";
  try {
    const after = localStorage.getItem("dm-gui-after-switch");
    if (after) {
      localStorage.removeItem("dm-gui-after-switch");
      startPanel = after;
    }
  } catch (_) {}
  setPanel(startPanel);
  // tab 栏滚轮横滚：tab 溢出时，滚轮上下滚动 → 横向滚动 tab 列表
  // （配合 CSS 细滚动条，解决"tab 多时只显示几个、其余被裁剪"的问题）
  const tabBarEl = $("#tabBar");
  if (tabBarEl) {
    tabBarEl.addEventListener("wheel", (e) => {
      if (tabBarEl.scrollWidth > tabBarEl.clientWidth) {
        tabBarEl.scrollLeft += (e.deltaY || e.deltaX);
        e.preventDefault();
      }
    }, { passive: false });
  }
  connect();
}
document.addEventListener("DOMContentLoaded", init);

/* ═══════════ 工作台配置（GUI 全配置化 §4.7：员工/工具链/场景/门禁） ═══════════
 * 后端方法（internal/gui/workspace.go）：
 *   employee.list/get/save/delete、scene.*、toolchain.*、gate.list/save
 * 前端形态：四 tab（员工/工具链/场景/门禁）→ 列表 → 新建向导（表单）/
 * 编辑（YAML 编辑器）→ 保存 → 删除。全程鼠标，YAML 只是收据。
 */

const WS_KINDS = {
  employees: { api: "employee", file: "employee.yaml", label: "员工", icon: "👷" },
  chains:    { api: "toolchain", file: "toolchain.yaml", label: "工具链", icon: "🔗" },
  scenes:    { api: "scene", file: "scene.yaml", label: "场景", icon: "🎬" },
  gates:     { api: "gate", file: "gate.yaml", label: "门禁", icon: "🚦" },
};

// 内置种子模板（新建向导默认内容，与 internal 各包 Example* 对齐）
const WS_TEMPLATES = {
  employees: `# 员工档案（§4.1.1）
name: my-employee
display: "我的新员工"
role: 一句话分工（如：撰写周报与会议纪要）
persona: 人设（如：严谨、先看测试再动手）
model:
  preset: ""
tool_allowlist: [read_file, write_file, list_files, search_content]
tool_denylist: []
approval: standard
skills: []
`,
  chains: `# 工具链定义（§4.3.1）
name: my-chain
version: "1.0.0"
steps:
  - id: step1
    tool: git_diff
    params: { path: "." }
  - id: step2
    llm:
      role_hint: "助手"
      prompt: "总结以下内容：\n$ref: step1"
`,
  scenes: `# 场景定义（§4.4.1）
name: my-scene
display: "我的场景"
keywords: [关键词1, 关键词2]
intent_prompt: 判断用户是否触发此场景
toolchain: my-chain
approval: standard
verify: overseer
`,
};

function wsActive() { return state.wsTab || "employees"; }

// 工作台 tab 切换
function wsSwitch(kind) {
  state.wsTab = kind;
  $$("#wsTabs .ws-tab").forEach(b => b.classList.toggle("active", b.dataset.ws === kind));
  wsRender();
}

// 工作台总入口：按 tab 渲染
function wsRender() {
  const kind = wsActive();
  const body = $("#wsBody");
  if (!body) return;
  if (kind === "gates") { wsRenderGate(body); return; }
  wsRenderList(body, kind);
}

// 渲染员工/工具链/场景列表（含新建按钮）
async function wsRenderList(body, kind) {
  const info = WS_KINDS[kind];
  body.innerHTML = `<div class="cfg-empty">加载中…</div>`;
  let r;
  try { r = await req(info.api + ".list", {}); }
  catch (e) { body.innerHTML = `<div class="cfg-empty">加载失败: ${esc(msgOf(e))}</div>`; return; }
  const items = (r && r.items) || [];
  const warnings = (r && r.warnings) || [];
  let html = `<button class="ghost-btn small ws-new-btn" id="wsNew">＋ 新建${info.label}</button>`;
  if (items.length === 0) {
    html += `<div class="ws-empty">暂无${info.label}。点击上方按钮新建，或首次启动已自动内置种子。</div>`;
  } else {
    html += `<div class="ws-list">`;
    for (const it of items) {
      const stepsN = typeof it.steps === "number" ? it.steps : (Array.isArray(it.steps) ? it.steps.length : 0);
      const desc = it.display || it.role || (Array.isArray(it.keywords) ? it.keywords.join(",") : "") || (stepsN ? `${stepsN} 步` : "") || "";
      html += `<div class="ws-item" data-name="${esc(it.name)}">
        <span class="ws-name">${esc(it.name)}</span>
        <span class="ws-desc">${esc(desc)}</span>
        <span class="ws-act" data-act="edit">编辑</span>
        <span class="ws-act" data-act="del">✕</span>
      </div>`;
    }
    html += `</div>`;
  }
  if (warnings.length > 0) html += `<div class="ws-hint">⚠ ${warnings.length} 个文件解析失败：${esc(warnings[0])}</div>`;
  body.innerHTML = html;
  const nb = $("#wsNew");
  if (nb) nb.onclick = () => wsRenderNew(body, kind);
  $$("#wsBody .ws-item").forEach(el => {
    const name = el.dataset.name;
    el.onclick = (ev) => {
      const act = ev.target && ev.target.dataset ? ev.target.dataset.act : "";
      if (act === "del") { wsConfirmDelete(kind, name); }
      else if (act === "edit") { wsRenderEdit(body, kind, name); }
      else { wsRenderEdit(body, kind, name); }
    };
  });
}

// 新建表单（向导式）
function wsRenderNew(body, kind) {
  const info = WS_KINDS[kind];
  const tpl = WS_TEMPLATES[kind] || "";
  body.innerHTML = `
    <button class="ghost-btn small ws-back" id="wsBack">← 返回列表</button>
    <div class="ws-form">
      <label>名称（目录名，小写连字符）
        <input id="wsNewName" placeholder="my-employee" />
      </label>
      <label>${info.label} YAML（保存后可随时回来编辑）
        <textarea id="wsNewYaml">${esc(tpl)}</textarea>
      </label>
      <div class="ws-actions">
        <button class="fe-btn" id="wsSaveNew">💾 保存</button>
        <button class="ghost-btn small" id="wsCancel">取消</button>
      </div>
      <div class="ws-hint">保存即写入 .contextloom/${esc(kind)}/&lt;名称&gt;/${esc(info.file)}；重名不会覆盖，需先删除。</div>
    </div>`;
  $("#wsBack").onclick = () => wsRenderList(body, kind);
  $("#wsCancel").onclick = () => wsRenderList(body, kind);
  $("#wsSaveNew").onclick = async () => {
    const name = $("#wsNewName").value.trim();
    const yaml = $("#wsNewYaml").value;
    if (!name) { toast("请填写名称", "err"); return; }
    try {
      await req(info.api + ".save", { name, yaml });
      toast(`${info.label}「${name}」已创建`, "ok");
      wsRenderList(body, kind);
    } catch (e) { toast("保存失败: " + msgOf(e), "err"); }
  };
}

// 编辑现有对象（YAML 编辑器 + 保存 + 删除）
async function wsRenderEdit(body, kind, name) {
  const info = WS_KINDS[kind];
  body.innerHTML = `<div class="cfg-empty">加载中…</div>`;
  let r;
  try { r = await req(info.api + ".get", { name }); }
  catch (e) { body.innerHTML = `<div class="cfg-empty">加载失败: ${esc(msgOf(e))}</div>`; return; }
  body.innerHTML = `
    <button class="ghost-btn small ws-back" id="wsBack">← 返回列表</button>
    <div class="ws-form">
      <label>${info.label}：<b>${esc(name)}</b></label>
      <textarea id="wsEditYaml">${esc(r.yaml || "")}</textarea>
      <div class="ws-actions">
        <button class="fe-btn" id="wsSaveEdit">💾 保存</button>
        <button class="ghost-btn small" id="wsBack2">取消</button>
      </div>
      <div class="ws-hint">路径：${esc(r.path || "")}</div>
    </div>`;
  const back = () => wsRenderList(body, kind);
  $("#wsBack").onclick = back;
  $("#wsBack2").onclick = back;
  $("#wsSaveEdit").onclick = async () => {
    const yaml = $("#wsEditYaml").value;
    try {
      await req(info.api + ".save", { name, yaml, force: true });
      toast(`${info.label}「${name}」已保存`, "ok");
      wsRenderList(body, kind);
    } catch (e) { toast("保存失败: " + msgOf(e), "err"); }
  };
}

// 删除确认
async function wsConfirmDelete(kind, name) {
  const info = WS_KINDS[kind];
  confirmDialog(`确定删除${info.label}「${name}」？`, { danger: true, okText: "删除" }).then(async (ok) => {
    if (!ok) return;
    try {
      await req(info.api + ".delete", { name });
      toast(`${info.label}「${name}」已删除`, "ok");
      wsRender();
    } catch (e) { toast("删除失败: " + msgOf(e), "err"); }
  });
}

// 门禁 tab：三档开关 + 通过阈值 + 质量账本只读面板
async function wsRenderGate(body) {
  body.innerHTML = `<div class="cfg-empty">加载中…</div>`;
  let r;
  try { r = await req("gate.list", {}); }
  catch (e) { body.innerHTML = `<div class="cfg-empty">加载失败: ${esc(msgOf(e))}</div>`; return; }
  const enabled = !!(r && r.enabled);
  const level = (r && r.level) || "standard";
  const passScore = (r && r.pass_score != null) ? r.pass_score : 90;
  const ledger = (r && r.ledger_yaml) || "";
  let html = `
    <div class="ws-gate-row">
      <span class="ws-badge ${enabled ? "on" : "off"}">${enabled ? "🟢 已启用" : "⚪ 未启用"}</span>
      <span class="ws-hint">${enabled ? "门禁有牙齿：未过不外显、禁写 [GOAL_DONE]" : "gates 目录不存在 = 门禁关闭（零配置兜底）"}</span>
    </div>
    <div class="ws-form" style="margin-top:8px">
      <label>质量档（standard 全开 / fast 关 TEE 保静检 / none 关门禁）
        <select id="wsGateLevel">
          <option value="standard" ${level === "standard" ? "selected" : ""}>standard — 全开（默认）</option>
          <option value="fast" ${level === "fast" ? "selected" : ""}>fast — 快速（关 TEE，保静检）</option>
          <option value="none" ${level === "none" ? "selected" : ""}>none — 关闭（回旧行为）</option>
        </select>
      </label>
      <label>通过阈值（0-100）
        <input id="wsGateScore" type="number" min="0" max="100" value="${esc(String(passScore))}" />
      </label>
      <div class="ws-actions">
        <button class="fe-btn" id="wsGateSave">💾 保存</button>
      </div>
      <div class="ws-hint">保存即写入 .contextloom/gates/gate.yaml；gates 目录一经创建即激活门禁（重启/下轮 goal 生效）。</div>
    </div>`;
  if (ledger) {
    html += `<div class="ws-hint" style="margin-top:8px">📒 质量账本 quality_ledger.yaml（只读，持久怀疑记录）</div>
      <div class="ws-ledger">${esc(ledger)}</div>`;
  }
  body.innerHTML = html;
  $("#wsGateSave").onclick = async () => {
    const lv = $("#wsGateLevel").value;
    const sc = parseInt($("#wsGateScore").value, 10);
    try {
      await req("gate.save", { level: lv, pass_score: sc });
      toast(`门禁已保存：${lv} / ${sc} 分`, "ok");
      wsRenderGate(body);
    } catch (e) { toast("保存失败: " + msgOf(e), "err"); }
  };
}

// 挂载到 config 面板加载链：panelConfig 显示时初始化工作台
function wsInit() {
  const tabs = $("#wsTabs");
  if (tabs && !tabs.dataset.bound) {
    tabs.dataset.bound = "1";
    $$("#wsTabs .ws-tab").forEach(b => { b.onclick = () => wsSwitch(b.dataset.ws); });
  }
  wsRender();
}
