// Package gui 提供 ContextLoom GUI 的服务层：headless HTTP + WebSocket 服务。
//
// 部署形态（用户需求）：
//   - GUI 是单独的可执行文件（contextloom-gui），双击即可启动；
//   - 启动时自动拉起 contextloom 主程序（contextloom serve，headless 服务模式）；
//   - contextloom 的 TUI 启动方式不受影响，可并行使用（存储为纯文件、无锁，
//     多实例天然支持并发——见 internal/memory/storage.go 设计说明）。
//
// 本包只实现"服务端"：鉴权、消息信封、方法路由、流式事件推送。
// 渲染层（Wails/WebView/浏览器）由 cmd/contextloom-gui 负责，二者通过
// 本包的 WS 协议通信（协议定义见 GUI实现设计文档.md 第五章）。
package gui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/contextloom/contextloom-agent/internal/config"
	devruntime "github.com/contextloom/contextloom-agent/internal/runtime"
	"github.com/contextloom/contextloom-agent/internal/vision"
	"github.com/contextloom/contextloom-agent/pkg/types"
	"github.com/contextloom/contextloom-agent/pkg/version"
	"github.com/gorilla/websocket"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Runtime 是 GUI 服务需要的最小能力集（由 runtime.Runtime 实现），
// 便于单元测试用桩替换。
type Runtime interface {
	ListSessions() []*types.Session
	LoadSession(uuid string) (*types.Session, *types.Session, error)
	NewSession(input string) *types.Session
	GetSessionID() string
	ProcessInput(ctx context.Context, input string) (<-chan string, <-chan error, error)
	ConfigSummary() string
	GetMode() string
	SetMode(mode string)
	ContextUsage() string
	ContextUsageInfo() (used, max int)
	GoalStatus() string
	PersistencePaths() map[string]string
	GetConversationHistory() []types.Message
	// GetDisplayHistory 从 JSONL 事件日志加载完整历史用于展示（与 AI 上下文 conversationHistory 解耦），
	// 压缩持久化后 conversationHistory 可能为压缩快照，但展示历史应始终完整。
	// 返回值与 TUI loadHistoryMessages 行为一致：user/assistant 消息。
	GetDisplayHistory(sid string) []types.Message
	AddToHistory(role, content string)
	// UpdateSessionTitle 首次对话时自动更新会话标题（与 TUI 行为对齐，
	// 标题取第一个问题前 50 字，受 memory.auto_title 开关控制）
	UpdateSessionTitle(uuid, userInput string)
	// AddToHistoryForSession 把消息写入指定会话历史（sid != 当前会话时仅写
	// 事件日志，不污染内存历史；跨会话流式修复，见 runtime 实现注释）
	AddToHistoryForSession(sid, role, content string)

	// v2 扩展（契约 CONTRACT.md 第一节）
	DeleteSession(uuid string) error
	RenameSession(uuid, title string) error
	ProjectRoot() string
	ModelInfo() map[string]any
	GoalStatusDetail() map[string]any
	ListSkills() []*types.Skill
	GetActiveSkills() []string
	SetActiveSkills(names []string)
	PluginsList() string
	SetEffort(effort string)
	ContextStats() string
	// CompressContext 手动强制深度压缩（等价 TUI /compress）：
	// 强制渐进压缩（截断→丢弃→滚动摘要）+ Harness 索引衰减，
	// 返回压缩结果文案（截断/丢弃条数、释放 tokens、占比变化）。
	CompressContext() string

	// v3 配置管理（GUI 设置面板）
	ConfigSnapshot() map[string]any
	// ApplyConfigPatch 应用并保存配置补丁，返回需重启进程生效的字段清单
	// （其余字段已热同步到运行中组件，保存即生效）。
	ApplyConfigPatch(patch map[string]any) ([]string, error)

	// v4 目标循环（goal 模式 GUI 自动续跑，与 TUI 全链路一致）
	GoalSession() *types.GoalSession
	HasOverseer() bool
	OverseerEvaluate(response string) (bool, types.Score, types.OverseerReport)
	GoalOnRoundEnd(response string, passed bool, score types.Score, report types.OverseerReport) (bool, string)
	ProcessGoalRound(ctx context.Context) (<-chan string, <-chan error, error)

	// v5 多模型预设（/model 与 GUI 下拉切换）
	ModelNames() []string
	CurrentModel() string
	SwitchModel(name string) error
	AddModelPreset(p config.ModelPreset) error
	RemoveModelPreset(name string) error

	// v6 图片附件结构化直传：GUI 上传图片（dataURL 解码后）先暂存，
	// 紧接着一次 ProcessInput 消费为多模态片段（OpenAI 标准 image_url）。
	// 与 TUI 按路径识图等价，但不需要图片位于项目根/本机可读路径。
	StageImages(images []vision.Image)
}

// Envelope 是 WS 消息信封（设计文档 5.2）。
type Envelope struct {
	ID     string          `json:"id,omitempty"`
	Type   string          `json:"type"` // req / resp / event
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	OK     bool            `json:"ok,omitempty"`
	Result any             `json:"result,omitempty"`
	Error  *ErrObj         `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
	Data   any             `json:"data,omitempty"`
}

// ctxKeyStreamCancel 流式 cancel 注入 context 的 key（chat.send dispatch 取用）。
type ctxKey int

const ctxKeyStreamCancel ctxKey = iota

// ErrObj 错误对象。
type ErrObj struct {
	Code string `json:"code"`
	Msg  string `json:"msg"`
}

// activeStream 流式请求注册项：cancel 用于停止，gen 用于代际防误删，
// sid 为发起流时绑定的真实会话 UUID（chat.stop 按此匹配）。
// uid 是全局唯一的"流标识"：同一会话可以先后/并发存在多条流，前端仅凭
// sessionId 无法区分新旧流——旧流的延迟 delta/done 到达时会结束或重触发
// 新流（"停下的对话因别的对话仍在激活而诡异再次触发、难以停下"根因）。
// uid 随 agent.stream/done 下发（streamId 字段），前端据此判定事件归属。
type activeStream struct {
	cancel  context.CancelFunc
	gen     uint64
	uid     uint64
	sid     string
	stopped bool // 用户已显式 chat.stop：此后不再下发该流的 delta（残留事件抑制）
}

// Server 是 GUI 服务端。
type Server struct {
	rt       Runtime
	port     int
	token    string
	upgrader websocket.Upgrader
	mu       sync.Mutex
	writeMu  sync.Mutex // 串行化 WS 写（gorilla 不允许并发写）
	conns    map[*websocket.Conn]bool
	// active 会话 ID → 流式 cancel（代际管理，防误删）
	// 历史 bug：chat.send 的 handleReq 返回时 defer 即删除注册，而流还在后台跑
	// → chat.stop / 切换会话取消全部失效（"无法停止对话"根因）。
	// 修复：注册/注销全部在 streamChat 生命周期内完成（gen 匹配防误删新注册）。
	active    map[string]*activeStream
	activeGen uint64 // 流式注册代际计数器
	closed    chan struct{}
	ready     chan struct{} // Start 监听成功时关闭（供启动器获取实际端口）

	// pending 会话 ID → 在途流已生成内容（未写历史前）。
	// 背景：streamChat 只在流【结束】时才把 assistant 写入历史，生成期间
	// chat.loadSession 返回的历史缺本次回复——前端任何重绘（点击会话标题/
	// 切换会话/恢复）都会把"正在生成的半截回复"从界面清掉，只剩 user 消息
	// （"点击本会话标题后清空重置、只能看到新生成的"根因）。
	// 修复：streamChat 每收到一个 delta 实时记录到 pending[sid]，流结束清理；
	// loadSession/currentSession/history 返回消息时把 pending 合并为
	// partial assistant 追加在历史末尾，前端据此保留在途内容（partial 标记）。
	pendingMu sync.Mutex
	pending   map[string]string

	// 活动日志环形缓冲（log.tail/log.clear，最多 200 条）
	logMu   sync.Mutex
	logBuf  []LogLine
	logSeq  int64
	lastLog time.Time

	// 多终端管理器（term.* API，term.go）
	terms *TermManager
}

// LogLine 活动日志行（契约 log.tail 返回结构）。
type LogLine struct {
	TS    string `json:"ts"`    // HH:MM:SS
	Level string `json:"level"` // info/warn/err/ok
	Msg   string `json:"msg"`
}

// NewServer 创建 GUI 服务端。port=0 时自动选择空闲端口；
// token 为空时自动生成 32 字节随机 token。
func NewServer(rt Runtime, port int, token string) *Server {
	if token == "" {
		token = randomToken()
	}
	s := &Server{
		rt:    rt,
		port:  port,
		token: token,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true }, // 仅 127.0.0.1 + token 鉴权
		},
		conns:   map[*websocket.Conn]bool{},
		active:  map[string]*activeStream{},
		closed:  make(chan struct{}),
		ready:   make(chan struct{}),
		pending: map[string]string{},
	}
	s.terms = newTermManager(s)
	return s
}

// Ready 返回监听就绪通知 channel（Start 后可用）。
func (s *Server) Ready() <-chan struct{} { return s.ready }

// log 记录活动日志到环形缓冲（log.tail 读取）。
func (s *Server) log(level, format string, args ...any) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.logBuf == nil {
		s.logBuf = make([]LogLine, 0, 200)
	}
	s.logSeq++
	now := time.Now()
	s.lastLog = now
	msg := format
	if len(args) > 0 {
		msg = fmt.Sprintf(format, args...)
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	s.logBuf = append(s.logBuf, LogLine{
		TS:    now.Format("15:04:05"),
		Level: level,
		Msg:   msg,
	})
	if len(s.logBuf) > 200 {
		s.logBuf = s.logBuf[len(s.logBuf)-200:]
	}
}

// logLines 返回日志快照副本（最新在前）。
func (s *Server) logLines() []LogLine {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	out := make([]LogLine, len(s.logBuf))
	copy(out, s.logBuf)
	// 倒序：最新在前
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// recordPending 记录在途流已生成内容（按发起流时的会话 ID；sid 为空忽略）。
func (s *Server) recordPending(sid, delta string) {
	if sid == "" || delta == "" {
		return
	}
	s.pendingMu.Lock()
	s.pending[sid] += delta
	s.pendingMu.Unlock()
}

// pendingSnapshot 返回指定会话在途流内容快照（无则空串）。
func (s *Server) pendingSnapshot(sid string) string {
	if sid == "" {
		return ""
	}
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.pending[sid]
}

// clearPending 清理在途流记录（流结束/中断后历史已写入，不再需要）。
func (s *Server) clearPending(sid string) {
	if sid == "" {
		return
	}
	s.pendingMu.Lock()
	delete(s.pending, sid)
	s.pendingMu.Unlock()
}

// messagesWithPending 返回会话历史 + 在途流内容（合并为 partial assistant）。
// 生成中 loadSession/currentSession/history 必须包含在途内容，否则前端重绘
// 会把"正在生成的半截回复"从界面清掉（历史缺陷）。
// 历史来源为 JSONL 事件日志（GetDisplayHistory）而非内存 conversationHistory：
// 压缩持久化后 conversationHistory 是压缩快照（只含保留消息+[历史摘要]），
// 若用它展示 GUI 历史会残缺（明显短于 TUI）；事件日志始终完整。
func (s *Server) messagesWithPending(sid string) []map[string]any {
	msgs := s.messagesToJSON(s.rt.GetDisplayHistory(sid))
	if pend := s.pendingSnapshot(sid); pend != "" {
		msgs = append(msgs, map[string]any{"role": "assistant", "content": pend, "partial": true})
	}
	return msgs
}

// Token 返回服务 token（供 GUI 启动器注入 /?token=）。
func (s *Server) Token() string { return s.token }

// Port 返回实际监听端口（ListenAndServe 后有效）。
func (s *Server) Port() int { return s.port }

// Start 启动 HTTP 服务（127.0.0.1:port），阻塞直到 ctx 取消。
func (s *Server) Start(ctx context.Context) error {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(s.port))
	if err != nil {
		return fmt.Errorf("监听 127.0.0.1:%d 失败: %w", s.port, err)
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	close(s.ready)

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"ok":true,"version":"%s"}`, version.Version)
	})
	mux.HandleFunc("/", s.handleIndex) // 内嵌前端 / 启动提示

	srv := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
		s.closeAll()
	}()
	select {
	case <-ctx.Done():
		_ = srv.Close()
		return nil
	default:
	}
	log.Printf("[gui] 服务已启动: http://127.0.0.1:%d/?token=%s", s.port, s.token)
	err = srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// handleIndex 返回启动提示页（完整 GUI 前端由 contextloom-gui 提供并内嵌，
// 本服务仅 headless 提供 API；直接访问 / 给出手动访问指引）。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	token := r.URL.Query().Get("token")
	if token != "" && token != s.token {
		http.Error(w, "无效 token", http.StatusForbidden)
		return
	}
	fmt.Fprintf(w, indexFallbackHTML(s.port, s.token))
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token != s.token {
		http.Error(w, "未授权", http.StatusForbidden)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.mu.Lock()
	s.conns[conn] = true
	s.mu.Unlock()

	// ── WS 心跳检测：服务器侧 ping 探测半开连接 ──
	// 历史缺陷：无 ping/pong 机制，ReadMessage 在 TCP 半开时无限阻塞，
	// 连接泄漏无法回收，导致 GUI 隔夜/空闲后红罩重连失败。
	done := make(chan struct{})
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	// 心跳 goroutine：保持 writeMu 短持有（WriteControl 微秒级完成），
	// gorilla 要求 WriteControl 与 WriteJSON 等写方法外部串行化。
	// 旧实现：send() 无写超时长持 writeMu → ping 拿不到锁 → 30s 无 pong →
	// 60s 读超时 → 连接被掐断（"对话中途中断"根因）。
	// 修复：send() 加 15s 写超时释放锁，保证 ping 最多等 15s 即可发出。
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.writeMu.Lock()
				err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second))
				s.writeMu.Unlock()
				if err != nil {
					// ping 失败：主动关闭连接，让 defer 回收资源
					_ = conn.Close()
					return
				}
			case <-done:
				return
			}
		}
	}()

	defer func() {
		close(done)
		s.mu.Lock()
		delete(s.conns, conn)
		// 连接断开【不取消】在途流（历史缺陷根因，用户实测："切换项目时
		// 正在进行的对话被中止，切回来也没用了"）。
		// 旧实现：连接断开即 cancel 全部 active 流——GUI 切换项目会
		// location.reload()，旧连接断开 → 后端把正在生成的流全部掐死。
		// backend 常驻池让进程活下来了，但对话仍被这一步杀掉，进程常驻
		// 救不了它。
		// 新语义：流是"后端自己的任务"，连接只是观察者。断连后流继续生成、
		// 继续写回会话历史（AddToHistoryForSession），切回该项目重连时
		// loadSession/currentSession 从事件日志拿到完整回复。
		// 流的取消只由三件事控制：chat.stop（用户主动停止）、同会话新消息
		// 覆盖、服务进程关闭（closeAll）。
		s.mu.Unlock()
		_ = conn.Close()
	}()

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		// 收到任何消息都重置读超时（心跳保护 + 正常消息双保险）
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		var env Envelope
		if err := json.Unmarshal(data, &env); err != nil {
			_ = s.send(conn, Envelope{Type: "resp", OK: false, Error: &ErrObj{Code: "E_BADJSON", Msg: "非法 JSON: " + err.Error()}})
			continue
		}
		if env.Type != "req" {
			continue
		}
		// 每个请求独立 goroutine，支持并发；流式请求持有 cancel
		go s.handleReq(conn, env)
	}
}

func (s *Server) handleReq(conn *websocket.Conn, env Envelope) {
	isStream := env.Method == "chat.send"
	var (
		ctx    context.Context
		cancel context.CancelFunc
	)
	if isStream {
		// 流式请求：ctx 生命周期由 chat.stop / 连接断开管理（注册进 s.active），
		// 不能随本函数返回而取消（streamChat 在后台 goroutine 继续推流）。
		ctx, cancel = context.WithCancel(context.Background())
		// cancel 随 ctx 传给 dispatch（chat.send 启动 streamChat 时注册用）
		ctx = context.WithValue(ctx, ctxKeyStreamCancel, cancel)
		// 注：流式 cancel 的 active 注册/注销由 streamChat 内部完成（见
		// streamChat 开头注册、defer 注销）——历史实现在这里注册并在本函数
		// 返回时删除，导致流还在后台跑时 chat.stop/切换会话取消全部失效。
	} else {
		// 非流式：随请求返回取消
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()
	}

	result, err := s.dispatch(ctx, env.Method, env.Params, conn)
	if err != nil {
		s.log("err", "方法 %s 失败: %v", env.Method, err)
		_ = s.send(conn, Envelope{ID: env.ID, Type: "resp", OK: false, Error: &ErrObj{Code: "E_METHOD", Msg: err.Error()}})
		return
	}
	s.log("ok", "方法 %s 成功", env.Method)
	_ = s.send(conn, Envelope{ID: env.ID, Type: "resp", OK: true, Result: result})
}

// dispatch 方法路由（设计文档 5.3 核心子集）。
// 返回 (result, error)；流式方法（chat.send）返回 {accepted:true} 并
// 通过 conn 推送 agent.stream / agent.done 事件。
func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage, conn *websocket.Conn) (any, error) {
	switch method {
	case "ping":
		return map[string]any{"pong": time.Now().UnixMilli(), "version": version.Version}, nil
	case "chat.listSessions":
		sessions := s.rt.ListSessions()
		out := make([]map[string]any, 0, len(sessions))
		for _, sess := range sessions {
			out = append(out, map[string]any{
				"uuid":      sess.UUID,
				"title":     sess.Title,
				"updatedAt": sess.UpdatedAt.Format(time.RFC3339),
				"preview":   sess.Preview,
			})
		}
		return out, nil
	case "chat.loadSession":
		var p struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.UUID == "" {
			return nil, fmt.Errorf("缺少 uuid")
		}
		_, sess, err := s.rt.LoadSession(p.UUID)
		if err != nil {
			return nil, err
		}
		// 切换会话【不中断】其他会话正在进行的流（多对话并行）：
		//   1. 后台流继续生成，agent.stream/done 事件携带发起流时的 sessionId，
		//      前端按当前会话过滤/缓冲，互不串台；
		//   2. 回复经 AddToHistoryForSession(sid) 写回发起流时的会话事件日志，
		//      不会污染当前会话；切回时 LoadSession 从事件日志完整恢复。
		// 流的生命周期只由三件事控制：该会话自身的 chat.stop(sid)、该会话新消息
		// 覆盖（streamChat 注册时 cancel 同 streamID 旧流）、连接断开。
		return map[string]any{
			"uuid":     sess.UUID,
			"title":    sess.Title,
			"mode":     s.rt.GetMode(),
			"messages": s.messagesWithPending(p.UUID),
		}, nil
	case "chat.newSession":
		var p struct {
			Title string `json:"title"`
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p)
		}
		sess := s.rt.NewSession(p.Title)
		// 新建会话【不中断】任何在途流（多对话并行）：旧会话的流继续在后台生成，
		// 其回复经 AddToHistoryForSession(sid) 写回原会话事件日志；新会话的上下文
		// 为空（NewSession 清空内存历史），两者互不影响。
		return map[string]any{"uuid": sess.UUID, "title": sess.Title}, nil
	case "chat.currentSession":
		// 获取当前会话：uuid 为空表示无当前会话；LoadSession 出错时
		// 返回空 uuid（会话可能已被外部删除，不报错）。
		uuid := s.rt.GetSessionID()
		if uuid == "" {
			return map[string]any{"uuid": ""}, nil
		}
		_, sess, err := s.rt.LoadSession(uuid)
		if err != nil {
			return map[string]any{"uuid": ""}, nil
		}
		return map[string]any{
			"uuid":     sess.UUID,
			"title":    sess.Title,
			"mode":     s.rt.GetMode(),
			"messages": s.messagesWithPending(uuid),
		}, nil
	case "chat.history":
		var p struct {
			UUID string `json:"uuid"`
		}
		_ = json.Unmarshal(params, &p)
		_, _, err := s.rt.LoadSession(p.UUID)
		if err != nil {
			return nil, err
		}
		target := p.UUID
		if target == "" {
			target = s.rt.GetSessionID()
		}
		return map[string]any{"messages": s.messagesWithPending(target)}, nil
	case "chat.send":
		var p struct {
			Input       string `json:"input"`
			SessionID   string `json:"sessionId"`
			Attachments []struct {
				Name    string `json:"name"`
				Content string `json:"content"`
			} `json:"attachments"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数错误: %v", err)
		}
		if p.Input == "" && len(p.Attachments) == 0 {
			return nil, fmt.Errorf("input 不能为空")
		}
		// 会话绑定（多轮连续性基础）：
		//   无当前会话（GUI 首次发送前未显式 newSession）→ 自动创建；
		//   指定了会话且与当前活跃会话不同 → 切换（恢复该会话历史）。
		// 注：GetSessionID 已修复为返回 currentSessionID（此前恒为空串，
		//     导致 agent.done 的 sessionId 永远为空、前端无法跟踪会话）。
		if s.rt.GetSessionID() == "" {
			s.rt.NewSession("")
		} else if p.SessionID != "" && s.rt.GetSessionID() != p.SessionID {
			if _, _, err := s.rt.LoadSession(p.SessionID); err != nil {
				return nil, fmt.Errorf("会话不存在: %v", err)
			}
		}
		// 附件合并：非空时每个附件以 "[附件: 文件名]\n内容\n" 前缀拼入 input。
		// 模型据此获得附件内容（与 TUI 侧直接粘贴文件内容语义一致）。
		// 图片附件（扩展名命中 vision.ImageMIMETypes）特殊处理：dataURL 解码后
		// 结构化暂存（StageImages）→ ProcessInput 消费为 OpenAI 标准 image_url
		// 多模态片段（不拼路径文本、不写临时文件）；input 仅保留用户可见的
		// "[图片×N]" 计数标记（由 runtime 侧 Agent 展示层正确渲染为图片）。
		input := p.Input
		stagedImgs := 0
		if len(p.Attachments) > 0 {
			var b strings.Builder
			projectRoot := s.rt.ProjectRoot()
			var staged []vision.Image
			for _, a := range p.Attachments {
				if a.Name == "" {
					continue
				}
				// 图片附件：主流识图接口——结构化 dataURL 直传（OpenAI
				// image_url 片段），不再拼 "[图片: 路径]" 文本标记绕行。
				if _, isImage := vision.MIMEForPath(a.Name); isImage {
					if img, ok := decodeImageAttachment(a.Content, a.Name, projectRoot); ok {
						staged = append(staged, img)
						stagedImgs++
						continue
					}
					// dataURL 缺失/解码失败 → 回退项目根内定位同名文件
					// （旧客户端或大图只记名场景），仍以 StageImages 暂存。
					// 与 decodeImageAttachment 同语义：大图先 CompressImage
					// 压缩再编码——保证该分支与主分支（dataURL 直传）都是
					// 压缩后图片，绝不裸传原图（否则同图双发：原图+压缩图）。
					if abs := locateAttachment(projectRoot, a.Name); abs != "" {
						mime, _ := vision.MIMEForPath(a.Name)
						if data, err := os.ReadFile(abs); err == nil {
							payload, outMIME, _ := vision.CompressImage(data, mime)
							staged = append(staged, vision.Image{
								Path: abs, MIME: outMIME, Size: int64(len(payload)),
								DataURI: "data:" + outMIME + ";base64," + base64.StdEncoding.EncodeToString(payload),
							})
							stagedImgs++
							continue
						}
					}
					b.WriteString("[附件: ")
					b.WriteString(a.Name)
					b.WriteString("]\n")
					b.WriteString("（图片附件，内容读取失败，请在项目中搜索该文件名后提供完整路径）\n\n")
					continue
				}
				b.WriteString("[附件: ")
				b.WriteString(a.Name)
				b.WriteString("]\n")
				if a.Content != "" {
					b.WriteString(a.Content)
				} else {
					// 大文件（前端 >200KB 只记文件名不读内容）：在项目根内
					// 定位同名文件并给出绝对路径，AI 直接 read_file 读取，
					// 不必全盘搜索（用户痛点：大附件只传文件名，AI 花大量
					// 时间全盘 find）。定位失败时如实提示，避免 AI 瞎猜。
					if abs := locateAttachment(projectRoot, a.Name); abs != "" {
						b.WriteString(fmt.Sprintf("（文件较大未读取内容，绝对路径：%s，可调用 read_file 按需读取）", abs))
					} else {
						b.WriteString(fmt.Sprintf("（文件较大未读取内容，项目根内未找到 %s，请在项目中搜索该文件名）", a.Name))
					}
				}
				b.WriteString("\n\n")
			}
			// 图片结构化直传：全部暂存后统一消费（本轮 ProcessInput）。
			if len(staged) > 0 {
				s.rt.StageImages(staged)
				b.WriteString(fmt.Sprintf("[图片×%d]\n\n", stagedImgs))
			}
			if b.Len() > 0 {
				input = b.String() + input
			}
		}
		// 用户消息写入对话历史（多轮连续性核心修复）：
		// GUI 路径此前从不调用 AddToHistory（TUI 在外部写、GUI 没有），
		// conversationHistory 恒为空 → 第二次对话 AI 看不到用户消息。
		// 现在与 TUI 行为对齐：发送前写 user（含附件合并后的完整输入），
		// 流结束后写 assistant（见 streamChat）。
		// 绑定发起流时的会话 ID：streamChat 结束写历史必须落到【本】会话，
		// 而不是流结束时 currentSessionID 指向的会话（跨会话流式修复）。
		sid := s.rt.GetSessionID()
		// 首次对话自动更新会话标题（与 TUI 行为对齐：标题取第一个问题前 50 字，
		// 受 memory.auto_title 开关控制；判断用发送前的内存历史——为空即首次）。
		if sid != "" && len(s.rt.GetConversationHistory()) == 0 {
			s.rt.UpdateSessionTitle(sid, input)
		}
		s.rt.AddToHistory("user", input)
		streamCancel, _ := ctx.Value(ctxKeyStreamCancel).(context.CancelFunc)
		if streamCancel == nil {
			streamCancel = func() {}
		}
		go s.streamChat(ctx, input, sid, p.SessionID, streamCancel)
		return map[string]any{"accepted": true}, nil
	case "chat.stop":
		var p struct {
			SessionID string `json:"sessionId"`
			StreamID  uint64 `json:"streamId"`
		}
		_ = json.Unmarshal(params, &p)
		s.mu.Lock()
		// stopOne 标记流已停止并取消：stopped=true 后该流不再下发 delta
		// （残留事件抑制），但 streamStillCurrent 仍为 true → 会发出
		// interrupted done 解锁前端。同会话新流注册时会把注册替换掉，
		// 旧流的停止标记随之自然消失（不误伤新流）。
		stopOne := func(a *activeStream) {
			a.stopped = true
			a.cancel()
		}
		if p.StreamID != 0 {
			if a, ok := s.active[uidKey(p.StreamID)]; ok {
				stopOne(a)
			}
		} else if p.SessionID != "" {
			if a, ok := s.active[p.SessionID]; ok {
				stopOne(a)
			}
		} else {
			for _, a := range s.active {
				stopOne(a)
			}
		}
		s.mu.Unlock()
		return map[string]any{"stopped": true}, nil
	case "mode.set":
		var p struct {
			Mode string `json:"mode"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		switch p.Mode {
		case "chat", "task", "goal":
			// 无会话时先自动建会话：模式按会话隔离（SetMode 有 sid 写
			// modeBySID，无 sid 会覆盖全局默认）。否则 GUI 启动后（尚未
			// 发送消息）点模式按钮 → 全局默认被改成 task/goal → 之后所有
			// 新会话都继承该模式，"工具默认模式"被无意识污染（用户实锤：
			// settings.json 全局 mode 残留 task，新会话默认变 task）。
			if s.rt.GetSessionID() == "" {
				s.rt.NewSession("")
			}
			s.rt.SetMode(p.Mode)
			return map[string]any{"mode": s.rt.GetMode()}, nil
		default:
			return nil, fmt.Errorf("无效模式: %s（支持 chat/task/goal）", p.Mode)
		}
	case "config.get":
		// 返回 {summary(文本摘要,向后兼容), values(脱敏), schema(字段清单), path}——
		// 前端设置面板据此动态渲染表单；旧 /config 命令消费 summary。
		snap := s.rt.ConfigSnapshot()
		snap["summary"] = s.rt.ConfigSummary()
		return snap, nil
	case "status.bar":
		used, max := s.rt.ContextUsageInfo()
		// model 字段：从 ModelInfo 取 backend/effort，随 2s 轮询下发给状态栏
		// #modelInfo——保证"设置面板保存 config.set 后状态栏模型信息同步更新"
		// （refreshStatus 轮询自带模型信息，任何路径改设置都会自动同步）。
		mi := s.rt.ModelInfo()
		return map[string]any{
			"mode":      s.rt.GetMode(),
			"sessionId": s.rt.GetSessionID(),
			"ctx":       formatCtxUsage(used, max),
			"ctxRaw":    map[string]any{"used": used, "max": max},
			"goal":      s.rt.GoalStatus(),
			"model": map[string]any{
				"backend": mi["backend"],
				"effort":  mi["effort"],
			},
		}, nil
	case "paths":
		return s.rt.PersistencePaths(), nil

	// ─── v2 扩展方法（契约 CONTRACT.md 第一节）───
	case "chat.deleteSession":
		var p struct {
			UUID string `json:"uuid"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.UUID == "" {
			return nil, fmt.Errorf("缺少 uuid")
		}
		if err := s.rt.DeleteSession(p.UUID); err != nil {
			return nil, err
		}
		return map[string]any{"deleted": true}, nil
	case "chat.renameSession":
		var p struct {
			UUID  string `json:"uuid"`
			Title string `json:"title"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.UUID == "" {
			return nil, fmt.Errorf("缺少 uuid")
		}
		if err := s.rt.RenameSession(p.UUID, p.Title); err != nil {
			return nil, err
		}
		return map[string]any{"title": p.Title}, nil
	case "fs.list":
		var p struct {
			Path string `json:"path"`
			Root string `json:"root"` // 可选：绝对路径基座（v3 合并面板跨项目浏览）；空=当前项目根
		}
		_ = json.Unmarshal(params, &p)
		abs, err := s.safePath(p.Path, p.Root)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(abs)
		if err != nil {
			return nil, fmt.Errorf("读取目录失败: %v", err)
		}
		out := make([]map[string]any, 0, len(entries))
		for _, e := range entries {
			name := e.Name()
			if isIgnoredDir(name) {
				continue
			}
			info, ierr := e.Info()
			size := int64(0)
			var mtime string
			if ierr == nil {
				size = info.Size()
				mtime = info.ModTime().Format(time.RFC3339)
			}
			out = append(out, map[string]any{
				"name":  name,
				"path":  filepath.ToSlash(filepath.Join(p.Path, name)), // 前端按 "/" 分隔处理，Windows 统一转义
				"isDir": e.IsDir(),
				"size":  size,
				"mtime": mtime,
			})
		}
		return map[string]any{"path": p.Path, "root": p.Root, "entries": out}, nil
	case "fs.read":
		var p struct {
			Path string `json:"path"`
			Root string `json:"root"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
			return nil, fmt.Errorf("缺少 path")
		}
		abs, err := s.safePath(p.Path, p.Root)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, fmt.Errorf("文件不存在: %v", err)
		}
		if info.IsDir() {
			return nil, fmt.Errorf("是目录不是文件: %s", p.Path)
		}
		if info.Size() > 5<<20 {
			return nil, fmt.Errorf("文件过大（>5MB），请用 TUI 打开: %s", p.Path)
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, fmt.Errorf("读取失败: %v", err)
		}
		return map[string]any{
			"path":    p.Path,
			"content": string(data),
			"size":    info.Size(),
			"mtime":   info.ModTime().Format(time.RFC3339),
		}, nil
	case "fs.write":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Root    string `json:"root"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
			return nil, fmt.Errorf("缺少 path")
		}
		abs, err := s.safePath(p.Path, p.Root)
		if err != nil {
			return nil, err
		}
		if err := s.checkWritableRoot(p.Root); err != nil {
			return nil, err
		}
		if err := os.WriteFile(abs, []byte(p.Content), 0o644); err != nil {
			return nil, fmt.Errorf("写入失败: %v", err)
		}
		info, _ := os.Stat(abs)
		size := int64(len(p.Content))
		if info != nil {
			size = info.Size()
		}
		return map[string]any{"path": p.Path, "size": size}, nil
	case "fs.mkdir":
		var p struct {
			Path string `json:"path"`
			Root string `json:"root"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
			return nil, fmt.Errorf("缺少 path")
		}
		abs, err := s.safePath(p.Path, p.Root)
		if err != nil {
			return nil, err
		}
		if err := s.checkWritableRoot(p.Root); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(abs, 0o755); err != nil {
			return nil, fmt.Errorf("创建目录失败: %v", err)
		}
		return map[string]any{"ok": true}, nil
	case "fs.rename":
		var p struct {
			OldPath string `json:"oldPath"`
			NewPath string `json:"newPath"`
			Root    string `json:"root"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.OldPath == "" || p.NewPath == "" {
			return nil, fmt.Errorf("缺少 oldPath/newPath")
		}
		absOld, err := s.safePath(p.OldPath, p.Root)
		if err != nil {
			return nil, err
		}
		absNew, err := s.safePath(p.NewPath, p.Root)
		if err != nil {
			return nil, err
		}
		if err := s.checkWritableRoot(p.Root); err != nil {
			return nil, err
		}
		if err := os.Rename(absOld, absNew); err != nil {
			return nil, fmt.Errorf("重命名失败: %v", err)
		}
		return map[string]any{"ok": true}, nil
	case "fs.delete":
		var p struct {
			Path string `json:"path"`
			Root string `json:"root"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Path == "" {
			return nil, fmt.Errorf("缺少 path")
		}
		abs, err := s.safePath(p.Path, p.Root)
		if err != nil {
			return nil, err
		}
		if err := s.checkWritableRoot(p.Root); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(abs); err != nil {
			return nil, fmt.Errorf("删除失败: %v", err)
		}
		return map[string]any{"ok": true}, nil
	case "goal.status":
		return s.rt.GoalStatusDetail(), nil
	case "skill.list":
		skills := s.rt.ListSkills()
		active := make(map[string]bool)
		for _, n := range s.rt.GetActiveSkills() {
			active[n] = true
		}
		out := make([]map[string]any, 0, len(skills))
		for _, sk := range skills {
			out = append(out, map[string]any{
				"name":   sk.Name,
				"desc":   sk.Description,
				"active": active[sk.Name],
			})
		}
		return map[string]any{"skills": out}, nil
	case "skill.toggle":
		var p struct {
			Name   string `json:"name"`
			Active bool   `json:"active"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		cur := s.rt.GetActiveSkills()
		found := false
		for _, n := range cur {
			if n == p.Name {
				found = true
				break
			}
		}
		if p.Active && !found {
			s.rt.SetActiveSkills(append(cur, p.Name))
		} else if !p.Active && found {
			// 必须新建切片：cur 是 rt.activeSkills 的内部引用（GetActiveSkills
			// 直接返回），cur[:0] 复用底层数组会破坏运行时状态（并发/持久化竞态）
			next := make([]string, 0, len(cur)-1)
			for _, n := range cur {
				if n != p.Name {
					next = append(next, n)
				}
			}
			s.rt.SetActiveSkills(next)
		}
		return map[string]any{"ok": true}, nil
	case "plugin.list":
		return map[string]any{"plugins": s.rt.PluginsList()}, nil
	case "model.info":
		return s.rt.ModelInfo(), nil
	case "model.list":
		// 多模型预设列表（GUI 设置面板下拉）：返回全部预设名 + 当前激活
		presets := make([]map[string]any, 0)
		for _, n := range s.rt.ModelNames() {
			presets = append(presets, map[string]any{"name": n})
		}
		return map[string]any{"presets": presets, "current": s.rt.CurrentModel()}, nil
	case "model.switch":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		if err := s.rt.SwitchModel(p.Name); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "name": p.Name}, nil
	case "model.delete":
		// GUI 设置面板删除模型配置（含删除当前激活项时自动切换）
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		if err := s.rt.RemoveModelPreset(p.Name); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "name": p.Name, "current": s.rt.CurrentModel()}, nil
	case "model.create":
		// GUI 傻瓜式新建模型预设：只收核心字段，其余用默认（新建后可在表单微调）。
		var p struct {
			Name       string `json:"name"`
			Backend    string `json:"backend"`
			Endpoint   string `json:"endpoint"`
			APIKey     string `json:"api_key"`
			MaxContext int    `json:"max_context"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数错误: %v", err)
		}
		if err := s.rt.AddModelPreset(config.ModelPreset{
			Name:       p.Name,
			Backend:    p.Backend,
			Endpoint:   p.Endpoint,
			APIKey:     p.APIKey,
			MaxContext: p.MaxContext,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "name": p.Name}, nil
	case "model.set":
		var p struct {
			Effort string `json:"effort"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Effort == "" {
			return nil, fmt.Errorf("缺少 effort")
		}
		switch p.Effort {
		case "low", "medium", "high", "xhigh", "max":
			s.rt.SetEffort(p.Effort)
			return map[string]any{"ok": true}, nil
		default:
			return nil, fmt.Errorf("无效 effort: %s", p.Effort)
		}
	case "context.stats":
		return map[string]any{
			"usage": s.rt.ContextUsage(),
			"stats": s.rt.ContextStats(),
		}, nil
	case "context.compress":
		// GUI 手动强制深度压缩（等价 TUI /compress）。
		// 之前该能力未接线：前端 /compact 只能提示"请用 TUI"。
		// 现直接调 Runtime.CompressContext()（渐进压缩+摘要+索引衰减），
		// 返回结果文案 + 压缩后 usage，前端展示并刷新状态栏。
		// 关键：压缩只改内存 conversationHistory，而 GUI 消息区显示的是
		// 事件日志完整重放（GetDisplayHistory）——若不推送，用户点压缩后
		// 界面"毫无变化"（"我的问题从来都是显示问题"根因）。
		// 因此压缩成功后主动推送 ctx.compressed 事件（带压缩态消息列表），
		// 前端收到后重绘消息区为 [历史摘要] + 保留消息，压缩立刻可见。
		msg := s.rt.CompressContext()
		sid := s.rt.GetSessionID()
		// 无条件推送（sid 可能为空=尚未建会话）：前端按 sessionId 匹配
		// 当前会话才重绘，其余场景仅刷新状态栏占比（resp 同步返回 usage）。
		compacted := s.rt.GetConversationHistory()
		// ⚠️ 事件帧防爆：messagesToJSON 会把保留消息的每张图片 dataURI
		// （单张可达 MB 级）全量塞进 WS 帧 + 前端 renderMessages 全量建 <img>
		// → 主线程卡死，状态栏轮询/点击全部无响应（"压缩后无反应+状态栏不刷"
		// 真实根因之一）。压缩态重绘不需要真实图片——历史图片预览在
		// 会话重载（chat.loadSession）时由 messagesToJSON 完整输出，压缩事件
		// 只负责"让用户看到压缩后的消息结构"，因此这里用轻量行（图片降级为
		// 计数文本，不携带 dataURI），帧体从 MB 级降到 KB 级。
		s.broadcast(Envelope{Type: "event", Event: "ctx.compressed", Data: map[string]any{
			"sessionId": sid,
			"message":   msg,
			"messages":  s.messagesToJSONLight(compacted),
			"usage":     s.rt.ContextUsage(),
		}})
		return map[string]any{
			"ok":      true,
			"message": msg,
			"usage":   s.rt.ContextUsage(),
		}, nil
	case "log.tail":
		return map[string]any{"lines": s.logLines()}, nil
	case "log.clear":
		s.logMu.Lock()
		s.logBuf = s.logBuf[:0]
		s.logMu.Unlock()
		return map[string]any{"ok": true}, nil
	case "config.set":
		// 扁平 key 补丁（如 {"model.max_context": 262144}），仅提交变更字段。
		// 保存链路：ApplyPatch → 阈值单调修正 → Validate（api_key 例外）→ Save 写盘（自动备份 .bak）
		// → applyLiveConfig 热同步运行中组件（上下文预算/模型后端/检索参数）；
		// 返回 needsRestart：本次补丁中无法热更新、需重启进程生效的字段清单。
		var patch map[string]any
		if err := json.Unmarshal(params, &patch); err != nil {
			return nil, fmt.Errorf("无效补丁: %v", err)
		}
		if len(patch) == 0 {
			return nil, fmt.Errorf("补丁为空")
		}
		needsRestart, err := s.rt.ApplyConfigPatch(patch)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok":           true,
			"values":       s.rt.ConfigSnapshot()["values"],
			"needsRestart": needsRestart,
		}, nil

	// ─── §4.7 GUI 全配置化：员工/场景/工具链/门禁对象 CRUD（workspace.go）───
	case "employee.list":
		return s.employeeList()
	case "employee.get":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		return s.employeeGet(p.Name)
	case "employee.save":
		var p struct {
			Name  string `json:"name"`
			YAML  string `json:"yaml"`
			Force bool   `json:"force"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数错误: %v", err)
		}
		return s.employeeSave(p.Name, p.YAML, p.Force)
	case "employee.delete":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		return s.employeeDelete(p.Name)
	case "scene.list":
		return s.sceneList()
	case "scene.get":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		return s.sceneGet(p.Name)
	case "scene.save":
		var p struct {
			Name  string `json:"name"`
			YAML  string `json:"yaml"`
			Force bool   `json:"force"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数错误: %v", err)
		}
		return s.sceneSave(p.Name, p.YAML, p.Force)
	case "scene.delete":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		return s.sceneDelete(p.Name)
	case "toolchain.list":
		return s.toolchainList()
	case "toolchain.get":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		return s.toolchainGet(p.Name)
	case "toolchain.save":
		var p struct {
			Name  string `json:"name"`
			YAML  string `json:"yaml"`
			Force bool   `json:"force"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数错误: %v", err)
		}
		return s.toolchainSave(p.Name, p.YAML, p.Force)
	case "toolchain.delete":
		var p struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Name == "" {
			return nil, fmt.Errorf("缺少 name")
		}
		return s.toolchainDelete(p.Name)
	case "gate.list":
		return s.gateList()
	case "gate.save":
		var p struct {
			Level     string `json:"level"`
			PassScore int    `json:"pass_score"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数错误: %v", err)
		}
		return s.gateSave(p.Level, p.PassScore)
	case "term.spawn":
		var p struct {
			CWD  string `json:"cwd"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if len(params) > 0 {
			_ = json.Unmarshal(params, &p)
		}
		return s.terms.spawn(p.CWD, p.Cols, p.Rows)
	case "term.resize":
		var p struct {
			ID   string `json:"id"`
			Cols uint16 `json:"cols"`
			Rows uint16 `json:"rows"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if err := s.terms.resize(p.ID, p.Cols, p.Rows); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "term.input":
		var p struct {
			ID   string `json:"id"`
			Data string `json:"data"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if err := s.terms.input(p.ID, p.Data); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "term.kill":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		if err := s.terms.kill(p.ID); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true}, nil
	case "term.list":
		return s.terms.list(), nil
	case "term.buffer":
		var p struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, fmt.Errorf("参数解析失败: %w", err)
		}
		return s.terms.buffer(p.ID)
	default:
		return nil, fmt.Errorf("未知方法: %s", method)
	}
}

// safePath 校验并解析路径，防止目录穿越。
// root == "" → 基座 = 当前项目根（rt.ProjectRoot()），行为与现状一致（向后兼容）；
// root != "" → 基座 = 该绝对路径（必须 filepath.IsAbs，否则报错），越界检查基于该基座。
// 用于 v3 合并面板跨项目目录浏览（读方法）；写方法另有 checkWritableRoot 保护。
func (s *Server) safePath(p, root string) (string, error) {
	base := root
	if base == "" {
		base = s.rt.ProjectRoot()
		if base == "" {
			base = "."
		}
	} else if !filepath.IsAbs(base) {
		return "", fmt.Errorf("root 必须是绝对路径: %s", root)
	}
	absRoot, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	if p == "" {
		return absRoot, nil
	}
	clean := filepath.Clean(p)
	abs := clean
	if !filepath.IsAbs(clean) {
		abs = filepath.Join(absRoot, clean)
	}
	abs, err = filepath.Abs(abs)
	if err != nil {
		return "", err
	}
	// 越界检查：Windows 路径大小写不敏感（EqualFold），并统一前缀比较
	if !strings.EqualFold(abs, absRoot) &&
		!strings.HasPrefix(strings.ToLower(abs), strings.ToLower(absRoot)+string(filepath.Separator)) {
		return "", fmt.Errorf("路径越界（仅允许项目根内）: %s", p)
	}
	return abs, nil
}

// checkWritableRoot 写保护：root 非空且绝对路径规范化后 ≠ 当前项目根时拒绝写操作。
// 理由：会话/记忆/设置等数据均绑定当前项目 .contextloom，写非当前项目文件
// 无法纳入当前会话的作用域管理——合并面板中非当前项目树必须只读。
func (s *Server) checkWritableRoot(root string) error {
	if root == "" {
		return nil
	}
	cur := s.rt.ProjectRoot()
	if cur == "" {
		cur = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absCur, err := filepath.Abs(cur)
	if err != nil {
		return err
	}
	if !strings.EqualFold(absRoot, absCur) {
		return fmt.Errorf("只读项目：请先切换到该项目再修改")
	}
	return nil
}

// isIgnoredDir 判断目录是否应过滤（.git/.contextloom/node_modules/.DS_Store 等）。
func isIgnoredDir(name string) bool {
	switch name {
	case ".git", ".contextloom", "node_modules", ".DS_Store", ".idea", ".vscode":
		return true
	}
	return false
}

// streamChat 流式发送：把 delta 推为 agent.stream 事件，结束推 agent.done。
// 子 Agent 活动信号（§ 前缀，来自 spawn_task 工具 EmitProgress）从主文本流中
// 剥离，单独转发为 orchestration.progress 事件（GUI Agent 活动面板数据源），
// 与 TUI 的 handleSubTaskSignal 路由逻辑同构。
func (s *Server) streamChat(ctx context.Context, input, sid, streamID string, cancel context.CancelFunc) {
	// 注册流式 cancel（覆盖同会话旧流；key 同时登记前端传的 streamID 与
	// 真实 sid——chat.stop 按 sid 匹配、loadSession 按 a.sid 过滤）。
	// uid 是本次流的全局唯一标识：随所有 agent.stream/done 事件下发，
	// 前端据此精确判定事件归属（旧流的延迟事件不再误伤新流）。
	s.mu.Lock()
	if old, ok := s.active[streamID]; ok {
		old.cancel()
	}
	s.activeGen++
	uid := s.activeGen
	a := &activeStream{cancel: cancel, gen: uid, uid: uid, sid: sid}
	s.active[streamID] = a
	if sid != "" && sid != streamID {
		s.active[sid] = a
	}
	// 独立 uid 键：仅用于本流存活判定（streamStillCurrent），避免
	// streamID 为空串时多会话互相覆盖导致误判。
	s.active[uidKey(uid)] = a
	s.mu.Unlock()
	// 流结束：注销自己的注册（gen 匹配防误删新流；两个 key 都清理）；
	// 同时清理在途流记录（历史已写入，partial 不再需要——注意各出口
	// AddToHistoryForSession 均在函数体内先于 defer 执行，顺序安全）。
	defer func() {
		s.mu.Lock()
		if cur, ok := s.active[streamID]; ok && cur.gen == a.gen {
			delete(s.active, streamID)
		}
		if sid != "" && sid != streamID {
			if cur, ok := s.active[sid]; ok && cur.gen == a.gen {
				delete(s.active, sid)
			}
		}
		if cur, ok := s.active[uidKey(uid)]; ok && cur.gen == a.gen {
			delete(s.active, uidKey(uid))
		}
		s.mu.Unlock()
		s.clearPending(sid)
	}()

	// goal 模式：持久目标循环（v2 自进化：反思→缺陷返工→收敛→打磨全链路）。
	// 每轮结束监工评估 + 状态机推进；未完成自动发起下一轮（continuation 驱动），
	// 与 TUI 行为一致。事件流：agent.stream/agent.done 每轮一组 + goal.round 轮结果。
	//
	// ⚠️ 跨会话串台防护（用户实测"停下的对话因别的对话仍激活而诡异再次触发"）：
	// goal 循环的监工/目标状态机是【Runtime 级单例】（GoalSession/GoalOnRoundEnd
	// 取 currentSession），若本流所属会话已不是当前活跃会话（用户切到别的会话），
	// 继续循环会对【别的会话】的目标做推进——既让停下的对话不断"再次触发"，
	// 又污染其他会话目标状态。因此每轮续跑前校验：
	//   ① 本流仍是注册中的活跃流（未被同会话新流取代/被 chat.stop 停止）；
	//   ② 当前活跃会话仍是本流绑定的 sid（未被切走）。
	// 不满足则结束循环（本轮已生成内容已写回本会话历史，切回可继续）。
	full, stop := s.runAgentRound(ctx, input, sid, streamID, uid, false, cancel)
	if stop {
		return
	}
	for s.rt.GetMode() == "goal" && s.streamCanEmit(streamID, uid) && s.rt.GetSessionID() == sid {
		gs := s.rt.GoalSession()
		if gs == nil || gs.Done() {
			return
		}
		passed, score, report := true, types.Score(100), types.OverseerReport{}
		if s.rt.HasOverseer() && full != "" {
			passed, score, report = s.rt.OverseerEvaluate(full)
		}
		cont, reason := s.rt.GoalOnRoundEnd(full, passed, score, report)
		s.broadcast(Envelope{Type: "event", Event: "goal.round", Data: map[string]any{"continue": cont, "reason": reason, "score": score, "round": gs.Round, "sessionId": sid, "streamId": uid}})
		if !cont {
			return
		}
		full, stop = s.runAgentRound(ctx, "", sid, streamID, uid, true, cancel)
		if stop {
			return
		}
	}
}

// runAgentRound 执行一轮 agent 生成并推送 agent.stream/agent.done 事件。
// isContinuation=true 时走 ProcessGoalRound（注入 continuation，不经过用户输入）。
// 返回 (完整回复文本, 是否应停止整个流)。完整回复在 agent.done 前已写入历史。
// streamCancel 供流空闲看门狗使用：上游挂起（TCP 黑洞/无 delta 无 error）时
// 强制取消 → 走既有 interrupted 收尾推送 agent.done，界面必解锁。
func (s *Server) runAgentRound(ctx context.Context, input, sid, streamID string, uid uint64, isContinuation bool, streamCancel context.CancelFunc) (string, bool) {
	var ch <-chan string
	var errCh <-chan error
	var err error
	if isContinuation {
		ch, errCh, err = s.rt.ProcessGoalRound(ctx)
	} else {
		ch, errCh, err = s.rt.ProcessInput(ctx, input)
	}
	if err != nil {
		s.broadcast(Envelope{Type: "event", Event: "agent.done", Data: map[string]any{"error": err.Error(), "sessionId": sid, "streamId": uid}})
		return "", true
	}
	// 流开始：立即推送一次 ctx.usage 起点值（user 消息已在 chat.send 写入历史，
	// used 已增长）——状态栏"发送即更新"，不等 2s 轮询（轮询主用于无流稳态）。
	s.pushCtxUsage(0)
	var full strings.Builder
	// 流式实时 ctx 推送节流：字符阈值 1KB 或时间间隔 1.5s，先到先推。
	// 旧实现纯字符阈值 4096——普通回复（几百~几千 token）永远达不到，
	// 整轮生成过程不推一次 ctx.usage，只有结束才校准（"生成中 ctx 不动、
	// 结束后才跳变"的观感根因）。双阈值保证短回复也至少推 1~2 次中间值。
	lastCtxPush := time.Now()
	lastCtxChars := 0
	// 流空闲看门狗：最后收到输出（delta/err）的时间戳。上游挂起（TCP 黑洞：
	// 连接活着但永不返回数据、也不报错）时 ch/errCh 永无事件、ctx 不取消——
	// select 永久阻塞 → 无 agent.done → 前端永远 streaming → "卡死不响应
	// 也不中断"。挂起超过 idleTimeout 强制取消，走正常 interrupted 收尾。
	// 12 分钟：与 spawn_task 子任务无输出终止宽限（10 分钟）同数量级；
	// 思考模型（R 系列）长思考期可超 4 分钟无 delta，过短会误杀正常长思考。
	const idleTimeout = 12 * time.Minute
	lastActivity := time.Now()
	watchdogFired := false
	// emitStream/emitDone：本流的 agent.stream/agent.done 出口统一带上
	// streamId（=uid），并在下发前确认本流仍是该会话的当前活跃流——
	// 被同会话新流取代的旧流，其延迟 delta/done 一律不再下发（前端即便
	// 收到也可按 streamId 丢弃；双保险）。被 chat.stop 取消的流注册仍在，
	// 故仍会发出 interrupted done（前端据此解锁该会话）。
	emitStream := func(delta string) {
		if !s.streamCanEmit(streamID, uid) {
			return
		}
		s.broadcast(Envelope{Type: "event", Event: "agent.stream", Data: map[string]any{"delta": delta, "sessionId": sid, "streamId": uid}})
	}
	emitDone := func(extra map[string]any) {
		if !s.streamStillCurrent(streamID, uid) {
			return
		}
		data := map[string]any{"sessionId": sid, "streamId": uid}
		for k, v := range extra {
			data[k] = v
		}
		s.broadcast(Envelope{Type: "event", Event: "agent.done", Data: data})
	}
loop:
	for {
		// 每轮 select 前计算剩余空闲额度；耗尽则触发看门狗（只触发一次，
		// 活动恢复后重新武装——长流多次挂起各自有 12 分钟宽限）
		remaining := idleTimeout - time.Since(lastActivity)
		if remaining <= 0 && !watchdogFired {
			watchdogFired = true
			emitStream("\n\n[流空闲看门狗] 上游 " + idleTimeout.String() + " 无输出，已强制中断\n")
			s.rt.AddToHistoryForSession(sid, "system", "[流空闲看门狗] 上游 "+idleTimeout.String()+" 无输出，已强制中断")
			if streamCancel != nil {
				streamCancel()
			}
		}
		if remaining <= 0 {
			remaining = time.Second // 已触发：给取消传播留时间，防忙等
		}
		select {
		case <-ctx.Done():
			// 中断：已产生的回复片段也写入历史（与 TUI Ctrl+C 保存行为一致），
			// 避免"有问无答"断档；先写历史再发 done，保证前端收到 done 后可
			// 立即发起下一轮时历史已完整（无并发竞态）。
			if full.Len() > 0 {
				s.rt.AddToHistoryForSession(sid, "assistant", full.String())
			}
			s.pushCtxUsage(0) // 历史已含全部回复，校准状态栏
			emitDone(map[string]any{"interrupted": true})
			return "", true
		case <-time.After(remaining):
			// 空闲额度耗尽：回循环顶部触发看门狗（cancel 后 ctx.Done 必然命中收尾）
			continue
		case delta, ok := <-ch:
			if !ok {
				break loop
			}
			lastActivity = time.Now()
			// § 信号（子 Agent spawn/done/fail/进度）：转 orchestration 事件，不进主文本流
			if strings.HasPrefix(delta, "§") {
				s.broadcast(Envelope{Type: "event", Event: "orchestration.progress", Data: map[string]any{"msg": delta}})
				continue
			}
			full.WriteString(delta)
			// 在途内容实时记录：生成中 loadSession 可合并返回（partial assistant），
			// 前端重绘不再丢失"正在生成的半截回复"（历史缺陷根因之一）
			s.recordPending(sid, delta)
			emitStream(delta)
			// 状态栏实时同步：字符累计 ≥1KB 或距上次推送 ≥1.5s 时推一次 ctx.usage
			// 事件（前端 applyCtxUsage 即时更新进度条——对话中状态栏不再纹丝不动，
			// 短回复也因时间阈值触发中间推送，而非等结束才跳变）。
			if full.Len()-lastCtxChars >= 1024 || time.Since(lastCtxPush) >= 1500*time.Millisecond {
				lastCtxPush = time.Now()
				lastCtxChars = full.Len()
				s.pushCtxUsage(full.Len())
			}
		case e, ok := <-errCh:
			lastActivity = time.Now() // err 事件也算活动（重置看门狗）
			if ok && e != nil {
				if full.Len() > 0 {
					s.rt.AddToHistoryForSession(sid, "assistant", full.String())
				}
				s.pushCtxUsage(0)
				emitDone(map[string]any{"error": e.Error()})
				return "", true
			}
		}
	}
	// 等待 errCh 关闭后收尾（下方 select 带 default 非阻塞，无挂起风险）
	select {
	case <-ctx.Done():
		// 中断：必须发 agent.done（历史 bug：主循环因 ch 关闭 break 后，
		// 取消只写历史不发 done → 前端永远等不到结束事件，streaming 卡死，
		// 表现为"无法停止对话/无法追加发送"）
		if full.Len() > 0 {
			s.rt.AddToHistoryForSession(sid, "assistant", full.String())
		}
		s.pushCtxUsage(0)
		emitDone(map[string]any{"interrupted": true})
		return "", true
	case e, ok := <-errCh:
		if ok && e != nil {
			if full.Len() > 0 {
				s.rt.AddToHistoryForSession(sid, "assistant", full.String())
			}
			s.pushCtxUsage(0)
			emitDone(map[string]any{"error": e.Error()})
			return "", true
		}
	default:
	}
	// 完整回复写入历史（StripThinking 在 AddToHistory 内部剥离思考内容）——
	// 必须在发送 agent.done 之前完成，否则前端收到 done 后立刻发起下一轮
	// chat.send 时 user 消息会先于本 assistant 消息写入，历史顺序错乱。
	if full.Len() > 0 {
		finalText := promoteIfThinkOnly(full.String())
		s.rt.AddToHistoryForSession(sid, "assistant", finalText)
		s.pushCtxUsage(0) // 历史已含完整回复，校准最终值（streaming=0）
		emitDone(map[string]any{"text": finalText})
		return finalText, false
	}
	// 空流（无任何输出）：不发 text，仅正常结束
	s.pushCtxUsage(0)
	emitDone(nil)
	return "", false
}

// streamStillCurrent 判定 (streamID, uid) 是否仍是该会话注册的当前活跃流。
// 被同会话新流取代的旧流（注册已被覆盖）返回 false——其延迟 delta/done
// 不再下发，避免旧流事件干扰新流（"停下的对话被别的对话诡异重触发"根因）。
// 被 chat.stop 取消的流注册仍在（需要发 interrupted done 解锁前端）→ true。
func (s *Server) streamStillCurrent(streamID string, uid uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.active[uidKey(uid)]
	if !ok || cur.uid != uid {
		return false
	}
	// 若该流曾以 streamID 注册（非空），则 streamID 键必须仍指向本流——
	// 被同会话新流取代时该键已被覆盖，旧流不得再发 done（否则前端会因
	// 旧流的 done 提前结束新流/把停下的对话再次触发）。
	if streamID != "" {
		if byID, ok := s.active[streamID]; ok && byID.uid != uid {
			return false
		}
	}
	return true
}

// streamCanEmit 判定本流是否仍可下发 agent.stream 增量：注册仍在且未被
// 用户显式停止（chat.stop 置 stopped=true）。被同会话新流取代的旧流、
// 被用户停止的流，其延迟 delta 一律抑制（残留事件抑制双保险）。
func (s *Server) streamCanEmit(streamID string, uid uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.active[uidKey(uid)]
	return ok && cur.uid == uid && !cur.stopped
}

// uidKey 流 uid 在 active 表中的独立键（与 session/streamID 键空间隔离，
// 避免 streamID 为空串时多会话互相覆盖）。
func uidKey(uid uint64) string { return "uid:" + strconv.FormatUint(uid, 10) }

// promoteIfThinkOnly 整轮回复只有思维链没有正文（R 系列模型把结论全部写进
// reasoning_content 的上游行为）时，保留原始思维链（折叠可回看），尾部追加
// PromoteThinkBody 提升的可见正文。有正文或无思考时原样返回。
// 入史时 AddToHistoryForSession 内部 SplitReasoning 会把标签段归入
// reasoning_content、提升段归入 Content，与普通回复的持久化语义一致。
func promoteIfThinkOnly(raw string) string {
	body, reasoning := devruntime.SplitReasoning(raw)
	if strings.TrimSpace(body) != "" || strings.TrimSpace(reasoning) == "" {
		return raw
	}
	promoted, ok := devruntime.PromoteThinkBody([]string{reasoning})
	if !ok {
		return raw
	}
	return raw + "\n\n" + promoted
}

// formatCtxUsage 与 runtime.ContextUsage 同一格式（"ctx 0.6% (800/131k)"），
// 供 status.bar 从单次 ContextUsageInfo 快照构建 ctx 字符串与 ctxRaw——
// 避免两次独立读取在并发写历史（goal 自动续跑等）下出现微秒级不一致。
func formatCtxUsage(used, max int) string {
	if max <= 0 {
		return ""
	}
	pct := float64(used) / float64(max) * 100
	if pct > 99 {
		pct = 99
	}
	return fmt.Sprintf("ctx %.2f%% (%s/%s)", pct, fmtCtxTokens(used), fmtCtxTokens(max))
}

// fmtCtxTokens 与 runtime.fmtCtxTokens 同语义（<1000 真实数值 / <10000
// 1 位小数 k / 否则整数 k 截断）。
func fmtCtxTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 10000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%dk", n/1000)
	}
}

// pushCtxUsage 推送实时上下文占用事件（GUI 状态栏"同步改变"的核心机制）：
// used = 历史已用 token + 流式在途内容估算（字符/3 粗估，CJK 约 1.5 字符/token）。
// 流式期间每累计 ~4KB 推送一次（进度条持续增长），流结束/中断时以 streaming=0
// 推送最终校准值（历史已含完整回复，数值与 status.bar 轮询一致，无跳变）。
func (s *Server) pushCtxUsage(extraChars int) {
	used, max := s.rt.ContextUsageInfo()
	if max <= 0 {
		return
	}
	streaming := extraChars / 3
	if streaming < 0 {
		streaming = 0
	}
	s.broadcast(Envelope{Type: "event", Event: "ctx.usage", Data: map[string]any{
		"used":      used + streaming,
		"max":       max,
		"streaming": streaming,
		"action":    "",
	}})
}

func (s *Server) messagesToJSON(msgs []types.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		// 多模态消息渲染为 "[图片×N] + 文本"（修复旧版 %v 输出 Go 结构体
		// 转储的隐患：Content 为 []ContentPart 时前端会看到不可读的
		// [{image_url 0xc000...} ...] 样式垃圾文本）。
		content := m.TextContent()
		if n := m.ImagePartCount(); n > 0 {
			if content == "" {
				content = fmt.Sprintf("[图片×%d]", n)
			} else {
				content = fmt.Sprintf("[图片×%d] %s", n, content)
			}
		}
		entry := map[string]any{"role": m.Role, "content": content}
		// 对话记录图片预览：图片 data URI 随消息输出（前端渲染 <img> 预览）。
		// 历史缺陷：只给 "[图片×N]" 计数文本、丢弃 data URI → 前端无图可渲染。
		if urls := m.ImageURLs(); len(urls) > 0 {
			entry["images"] = urls
		}
		out = append(out, entry)
	}
	return out
}

// messagesToJSONLight 是 messagesToJSON 的轻量变体：图片 data URI 一律
// 降级为 "[图片×N]" 计数文本、不输出 images 字段。
// 用途：ctx.compressed 压缩态事件帧——压缩重绘只需展示消息结构，携带
// MB 级 dataURI 会让 WS 帧膨胀 + 前端全量建 <img> 卡死主线程（真实用户
// 反馈"压缩后点击无反应/状态栏不刷"根因之一）。完整图片预览由
// chat.loadSession（messagesToJSON 完整版）在会话重载时输出。
func (s *Server) messagesToJSONLight(msgs []types.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		content := m.TextContent()
		if n := m.ImagePartCount(); n > 0 {
			if content == "" {
				content = fmt.Sprintf("[图片×%d]", n)
			} else {
				content = fmt.Sprintf("[图片×%d] %s", n, content)
			}
		}
		out = append(out, map[string]any{"role": m.Role, "content": content})
	}
	return out
}

func (s *Server) send(conn *websocket.Conn, env Envelope) error {
	s.mu.Lock()
	if !s.conns[conn] {
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	// 写超时兜底：WS 底层 TCP 拥塞/对端不读时 WriteJSON 会无限阻塞，
	// 长持 writeMu 又拖垮心跳/其他事件（历史缺陷：对话中途"卡死"根因之一）。
	// 超时返回后由调用方继续推流，连接本身留给心跳死循环判定关闭。
	_ = conn.SetWriteDeadline(time.Now().Add(15 * time.Second))
	err := conn.WriteJSON(env)
	_ = conn.SetWriteDeadline(time.Time{})
	return err
}

// decodeImageAttachment 把前端上传的图片 base64 data URL 解码为 vision.Image
// （结构化多模态片段，走主流识图接口——OpenAI 标准 image_url data URI）。
// 失败（非 data URL/解码失败/空内容）返回 ok=false。
//
// 图片解码后落盘到项目 .contextloom/attachments/（持久目录）：该路径写入
// 会话事件日志 [图片: 路径] 标记，多轮追问/重启后按路径重读恢复图片——
// 系统临时目录方案在清理/重启后标记失效（文件没了，历史图恢复不了）。
// 语义变化：旧实现写临时文件再以 "[图片: 路径]" 文本标记拼入 input（路径
// 垃圾进上下文 + 依赖文本正则提取）；新实现图片经 StageImages 结构化暂存、
// ProcessInput 消费为 image_url 片段。临时文件仅作历史事件日志恢复锚点
// （EventText 的 [图片: 路径] 标记按路径重读，多轮追问图片仍在上下文中）。
func decodeImageAttachment(dataURL, name, projectRoot string) (vision.Image, bool) {
	if dataURL == "" || !strings.HasPrefix(dataURL, "data:") {
		return vision.Image{}, false
	}
	comma := strings.IndexByte(dataURL, ',')
	if comma < 0 {
		return vision.Image{}, false
	}
	raw := dataURL[comma+1:]
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return vision.Image{}, false
	}
	mime, ok := vision.MIMEForPath(name)
	if !ok {
		mime = "image/png" // 无扩展名图片默认 PNG（与旧实现兜底一致）
	}
	// 图片落盘目录：优先项目 .contextloom/attachments/（持久目录，事件日志恢复
	// 锚点长期有效：重启/清理系统临时目录后历史图片仍可恢复）；
	// 目录不可用（无项目根/只读）时回退系统临时目录（仅当前进程有效）。
	// 旧实现 os.CreateTemp("") 写系统临时目录 → contextloom 清理临时文件/重启后
	// 历史恢复的 [图片: 路径] 标记指向已删除文件 → 多轮追问看不到图。
	dir := ""
	if projectRoot != "" && projectRoot != "." {
		dir = filepath.Join(projectRoot, ".contextloom", "attachments")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		dir = "" // 落盘失败 → 回退系统临时目录
	}
	// 大图先按 DeepSeek 官方缩放标准本地压缩（≤200KB 原样）：
	// 请求体从 MB 级降为 KB 级，落盘文件同步变小（.contextloom/attachments/
	// 持久目录不膨胀）；webp/bmp 转 JPEG 时扩展名与 MIME 一起更新。
	payload, outMIME, _ := vision.CompressImage(decoded, mime)
	outExt := filepath.Ext(name)
	if outMIME != mime && outExt != "" {
		switch outMIME {
		case "image/jpeg":
			outExt = ".jpg"
		case "image/png":
			outExt = ".png"
		}
	}
	f, err := os.CreateTemp(dir, "contextloom-gui-img-*"+outExt)
	if err != nil {
		return vision.Image{}, false
	}
	defer f.Close()
	if _, err := f.Write(payload); err != nil {
		os.Remove(f.Name())
		return vision.Image{}, false
	}
	return vision.Image{
		Path:    f.Name(), // 事件日志恢复锚点（不拼入 input 文本）
		MIME:    outMIME,
		Size:    int64(len(payload)),
		DataURI: "data:" + outMIME + ";base64," + base64.StdEncoding.EncodeToString(payload),
	}, true
}

// locateAttachment 在项目根内递归查找指定文件名（不区分大小写，最多 2000 个
// 目录防失控），返回首个匹配的绝对路径；未找到返回空串。
// 用途：GUI 大附件只传文件名时，给 AI 提供可直接 read_file 的绝对路径，
// 避免全盘搜索（用户痛点：大文件附件 AI 花大量时间找文件）。
// 只匹配文件名相等（含扩展名），不按内容模糊匹配——宁可找不到也不给错路径。
func locateAttachment(root, name string) string {
	if root == "" || name == "" {
		return ""
	}
	clean := filepath.Clean(root)
	st, err := os.Stat(clean)
	if err != nil || !st.IsDir() {
		return ""
	}
	want := strings.ToLower(name)
	found := ""
	visited := 0
	var walk func(dir string)
	walk = func(dir string) {
		if found != "" || visited > 2000 {
			return
		}
		visited++
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if found != "" {
				return
			}
			// 跳过隐藏目录与 .git/node_modules 等大目录，减少扫描量
			if e.IsDir() {
				if strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" || e.Name() == "vendor" {
					continue
				}
				walk(filepath.Join(dir, e.Name()))
				continue
			}
			if strings.EqualFold(e.Name(), name) || strings.ToLower(e.Name()) == want {
				found = filepath.Join(dir, e.Name())
				return
			}
		}
	}
	walk(clean)
	return found
}

func (s *Server) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terms != nil {
		s.terms.closeAll()
	}
	// 服务进程关闭：此刻才取消在途流（正常退出前的收尾）。连接断开不再
	// 取消流（见 handleWS defer 注释），避免"GUI 刷新/切项目即掐断对话"。
	for k, a := range s.active {
		a.cancel()
		delete(s.active, k)
	}
	for c := range s.conns {
		_ = c.Close()
	}
	s.conns = map[*websocket.Conn]bool{}
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func indexFallbackHTML(port int, token string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>ContextLoom GUI</title>
<style>body{font-family:system-ui;background:#1e1e2e;color:#cdd6f4;display:flex;align-items:center;justify-content:center;height:100vh;margin:0}
.card{background:#313244;padding:32px 48px;border-radius:12px;text-align:center}
code{background:#45475a;padding:2px 8px;border-radius:6px}</style></head>
<body><div class="card"><h1>ContextLoom GUI v%s</h1><p>服务已就绪（端口 %d）</p>
<p>前端资源尚未内嵌（M1 占位）。<br/>请使用 <code>contextloom-gui</code> 可执行文件启动完整界面。</p></div></body></html>`, version.Version, port)
}

// OpenBrowser 在系统默认浏览器打开 URL（GUI 启动器回退路径）。
func OpenBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}
