package acp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Server agent-client-protocol 1.0 WebSocket 服务器。
//
// 会话模型：一个连接可承载多个会话（session_id 隔离）；每个会话有独立
// 推送通道（保序）；连接断开时会话保留在注册表（可被其他连接关闭），
// 但其推送通道随连接关闭而失效。
type Server struct {
	port     int
	upgrader websocket.Upgrader
	mu       sync.Mutex
	sessions map[string]*Session
	conns    map[*conn]bool
	handler  SessionHandler
}

// Session 单个 ACP 会话。
type Session struct {
	ID   string
	send chan []byte
	done chan struct{}
	conn *conn // 绑定连接（会话消息推送到此连接）
}

// conn 客户端连接。
type conn struct {
	ws     *websocket.Conn
	send   chan []byte
	closed chan struct{}
	once   sync.Once
	srv    *Server
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.closed)
		_ = c.ws.Close()
	})
}

// SessionHandler ACP 会话处理器（由上层实现并注入）。
type SessionHandler interface {
	// GetCapabilities 返回能力声明（server/hello 与 session/init 携带）。
	GetCapabilities() Capabilities
	// OnNewSession 会话创建回调。
	OnNewSession(sessionID string, tools []ToolDefinition, model string) error
	// OnPrompt 收到提示（异步执行；通过 Server.Push 流式推送 agent_message，
	// 执行完成后推送 session/prompt_response——由 handler 负责时序，
	// 保证 prompt_response 在流结束后到达客户端）。
	OnPrompt(sessionID, prompt string, tools []ToolDefinition, model string) error
	// OnComplete 会话完成（返回摘要）。
	OnComplete(sessionID string) (summary string, err error)
	// OnCancel 取消当前执行。
	OnCancel(sessionID string) error
	// OnClose 会话关闭。
	OnClose(sessionID string) error
	// OnUpdate 更新会话工具列表。
	OnUpdate(sessionID string, tools []ToolDefinition) error
	// OnPermissionResponse 权限审批结果（allow=true 表示允许）。
	OnPermissionResponse(sessionID, reqID string, allow bool) error
	// OnToolCallResponse 客户端工具执行结果。
	OnToolCallResponse(sessionID, toolCallID string, content []ContentBlock) error
	// OnAgentMessageResponse 客户端对 agent_message 的确认。
	OnAgentMessageResponse(sessionID string, success bool) error
}

// NewServer 创建 ACP 服务器（保留签名，兼容 runtime/main 既有调用）。
func NewServer(port int) *Server {
	return &Server{
		port: port,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
		sessions: make(map[string]*Session),
		conns:    make(map[*conn]bool),
	}
}

// Port 返回监听端口。
func (s *Server) Port() int { return s.port }

// SetHandler 注入会话处理器（须在 Start 前调用）。
func (s *Server) SetHandler(h SessionHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handler = h
}

// Start 启动 HTTP/WebSocket 服务（阻塞）。
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleConnection)
	return http.ListenAndServe(fmt.Sprintf(":%d", s.port), mux)
}

// StartAddr 启动到指定地址（测试用）。
func (s *Server) StartAddr(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleConnection)
	return http.ListenAndServe(addr, mux)
}

// Shutdown 关闭全部连接与会话。
func (s *Server) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		c.close()
	}
	for _, sess := range s.sessions {
		select {
		case <-sess.done:
		default:
			close(sess.send)
		}
	}
}

// handleConnection 升级 WebSocket 并发送 server/hello。
func (s *Server) handleConnection(w http.ResponseWriter, r *http.Request) {
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &conn{ws: ws, send: make(chan []byte, 256), closed: make(chan struct{}), srv: s}
	s.mu.Lock()
	s.conns[c] = true
	handler := s.handler
	s.mu.Unlock()

	go c.writePump()
	// 连接建立即发送 server/hello（版本 + 能力），格式为 JSON-RPC 请求通知
	// {id:null, method:"server/hello", params:{...}}（agent-client-protocol 1.0 推送规范）
	capabilities := Capabilities{}
	if handler != nil {
		capabilities = handler.GetCapabilities()
	}
	if data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"method":  MethodServerHello,
		"params":  HelloParams{Version: Version, Capabilities: capabilities},
	}); err == nil {
		select {
		case c.send <- data:
		case <-c.closed:
		}
	}
	s.readPump(c, handler)
}

func (c *conn) writePump() {
	for data := range c.send {
		select {
		case <-c.closed:
			return
		default:
		}
		if err := c.ws.WriteMessage(websocket.TextMessage, data); err != nil {
			c.close()
			return
		}
	}
}

// readPump 读取客户端消息并分发（JSON-RPC 2.0）。
func (s *Server) readPump(c *conn, handler SessionHandler) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		s.mu.Unlock()
		c.close()
	}()

	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		var req RPCRequest
		if err := json.Unmarshal(data, &req); err != nil {
			s.replyError(c, json.RawMessage("null"), ErrInvalidRequest, "请求必须是 JSON-RPC 2.0 对象")
			continue
		}
		s.dispatch(c, handler, &req)
	}
}

// dispatch 分发单个方法请求。
func (s *Server) dispatch(c *conn, handler SessionHandler, req *RPCRequest) {
	if handler == nil {
		s.replyError(c, req.RawID, ErrInternal, "ACP handler 未设置")
		return
	}
	switch req.Method {
	case MethodSessionNew:
		var p SessionNewParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/new 参数无效")
			return
		}
		sessionID := p.SessionID
		if sessionID == "" {
			sessionID = uuid.NewString()
		}
		if err := handler.OnNewSession(sessionID, p.Tools, p.Model); err != nil {
			s.replyError(c, req.RawID, ErrInternal, "创建会话失败: "+err.Error())
			return
		}
		sess := &Session{ID: sessionID, send: make(chan []byte, 256), done: make(chan struct{}), conn: c}
		s.mu.Lock()
		s.sessions[sessionID] = sess
		s.mu.Unlock()
		go s.sessionPump(sess)
		// 推送 session/init（含能力）→ 推送 session/init_response
		capabilities := handler.GetCapabilities()
		_ = s.Push(sessionID, MethodSessionInit, SessionInitParams{SessionID: sessionID, Capabilities: capabilities})
		_ = s.Push(sessionID, MethodInitResponse, ResponseParams{Success: true, SessionID: sessionID})
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: sessionID}))

	case MethodSessionPrompt:
		var p SessionPromptParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/prompt 参数无效")
			return
		}
		if p.SessionID == "" {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/prompt 参数无效")
			return
		}
		if !s.hasSession(p.SessionID) {
			s.replyError(c, req.RawID, ErrInvalidParams, "会话不存在: "+p.SessionID)
			return
		}
		// 异步执行：OnPrompt 内通过 Push 流式推送 agent_message；
		// session/prompt_response 由 handler 在执行完成后推送（语义：处理完成）。
		go func() {
			if err := handler.OnPrompt(p.SessionID, p.Prompt, p.Tools, p.Model); err != nil {
				_ = s.Push(p.SessionID, MethodPromptResponse, ResponseParams{Success: false, SessionID: p.SessionID, Error: err.Error()})
			}
		}()
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodSessionComplete:
		var p SessionCompleteParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/complete 参数无效")
			return
		}
		summary, err := handler.OnComplete(p.SessionID)
		if err != nil {
			_ = s.Push(p.SessionID, MethodCompleteResponse, ResponseParams{Success: false, SessionID: p.SessionID, Error: err.Error()})
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		_ = s.Push(p.SessionID, MethodCompleteResponse, ResponseParams{Success: true, SessionID: p.SessionID})
		_ = s.Push(p.SessionID, MethodSummary, SummaryParams{SessionID: p.SessionID, Summary: summary, Action: "keep"})
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodSessionCancel:
		var p SessionCancelParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/cancel 参数无效")
			return
		}
		if err := handler.OnCancel(p.SessionID); err != nil {
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		_ = s.Push(p.SessionID, MethodCancelResponse, ResponseParams{Success: true, SessionID: p.SessionID})
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodSessionClose:
		var p SessionCloseParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/close 参数无效")
			return
		}
		if err := handler.OnClose(p.SessionID); err != nil {
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		_ = s.Push(p.SessionID, MethodCloseResponse, ResponseParams{Success: true, SessionID: p.SessionID})
		s.removeSession(p.SessionID)
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodSessionUpdate:
		var p SessionUpdateParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/update 参数无效")
			return
		}
		if err := handler.OnUpdate(p.SessionID, p.Tools); err != nil {
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		_ = s.Push(p.SessionID, MethodUpdateResponse, ResponseParams{Success: true, SessionID: p.SessionID})
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodPermissionResponse:
		var p PermissionResponseParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/request_permission_response 参数无效")
			return
		}
		if err := handler.OnPermissionResponse(p.SessionID, p.PermissionRequestID, p.Response == "allow"); err != nil {
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodToolCallResponse:
		var p ToolCallResponseParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/tool_call_response 参数无效")
			return
		}
		if err := handler.OnToolCallResponse(p.SessionID, p.ToolCallID, p.Content); err != nil {
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	case MethodAgentMessageResponse:
		var p AgentMessageResponseParams
		if err := Params(req.Params, &p); err != nil {
			s.replyError(c, req.RawID, ErrInvalidParams, "session/agent_message_response 参数无效")
			return
		}
		if err := handler.OnAgentMessageResponse(p.SessionID, p.Success); err != nil {
			s.replyError(c, req.RawID, ErrInternal, err.Error())
			return
		}
		s.reply(c, req.RawID, mustJSON(ResponseParams{Success: true, SessionID: p.SessionID}))

	default:
		s.replyError(c, req.RawID, ErrMethodNotFound, "未知方法: "+req.Method)
	}
}

// sessionPump 会话推送通道 → 绑定连接（保序）。
func (s *Server) sessionPump(sess *Session) {
	defer close(sess.done)
	for {
		select {
		case data, ok := <-sess.send:
			if !ok {
				return
			}
			select {
			case sess.conn.send <- data:
			case <-sess.conn.closed:
				return
			}
		case <-sess.conn.closed:
			return
		}
	}
}

// Push 向指定会话推送消息（按会话串行保序）。会话不存在或连接已断返回错误。
func (s *Server) Push(sessionID, method string, params any) error {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("acp: 会话 %q 不存在", sessionID)
	}
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return err
	}
	select {
	case sess.send <- data:
		return nil
	case <-sess.done:
		return fmt.Errorf("acp: 会话 %q 已关闭", sessionID)
	case <-sess.conn.closed:
		return fmt.Errorf("acp: 会话 %q 连接已断开", sessionID)
	}
}

// Broadcast 向所有连接的客户端推送（无会话归属的广播）。
func (s *Server) Broadcast(method string, params any) {
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		select {
		case c.send <- data:
		default:
		}
	}
}

func (s *Server) hasSession(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[sessionID]
	return ok
}

func (s *Server) removeSession(sessionID string) {
	s.mu.Lock()
	sess, ok := s.sessions[sessionID]
	if ok {
		delete(s.sessions, sessionID)
		select {
		case <-sess.done:
		default:
			close(sess.send)
		}
	}
	s.mu.Unlock()
}

func (s *Server) reply(c *conn, id json.RawMessage, result json.RawMessage) {
	data, _ := json.Marshal(RPCResponse{RawID: id, Result: result})
	select {
	case c.send <- data:
	case <-c.closed:
	}
}

func (s *Server) replyError(c *conn, id json.RawMessage, code int, msg string) {
	data, _ := json.Marshal(RPCResponse{RawID: id, Error: &RPCError{Code: code, Message: msg}})
	select {
	case c.send <- data:
	case <-c.closed:
	}
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
