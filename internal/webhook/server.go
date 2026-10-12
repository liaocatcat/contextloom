// Package webhook 提供轻量消息渠道：HTTP 接收外部消息触发 goal/task 执行，
// 对标业界同类 Agent 的消息渠道，零依赖纯 Go 落地。
//
// 设计原则：
//   - 随机端口 + 随机 token 鉴权（用户明确不做完整鉴权体系，token 为轻量防线）；
//   - 触发历史环形记录（最近 50 条），供查询与审计；
//   - 全部纯 Go，可交叉编译。
package webhook

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Message 渠道收到的外部消息。
type Message struct {
	Text    string `json:"message"`           // 指令内容（发给 goal/task）
	Mode    string `json:"mode"`              // goal / task / chat
	Session string `json:"session,omitempty"` // 指定会话（空=新建）
	Meta    string `json:"meta,omitempty"`    // 来源标记（如 "cron" / "webhook"）
}

// Handler 处理消息并返回结果文本。
type Handler func(ctx context.Context, msg Message) (string, error)

// HistoryEntry 触发历史条目。
type HistoryEntry struct {
	TS     time.Time `json:"ts"`
	Mode   string    `json:"mode"`
	Text   string    `json:"text"`
	Result string    `json:"result,omitempty"`
	Err    string    `json:"err,omitempty"`
	Ms     int64     `json:"ms"`
}

// Server 轻量 webhook 渠道服务。
type Server struct {
	mu      sync.RWMutex
	token   string
	handler Handler
	history []HistoryEntry
	maxHist int
	srv     *http.Server
	addr    string
}

// New 创建渠道服务。token 为空时自动生成随机 token。
// handler 为 nil 时服务可启动但所有请求返回 503。
func New(token string, handler Handler) *Server {
	if token == "" {
		token = randomToken()
	}
	return &Server{token: token, handler: handler, maxHist: 50}
}

// Token 返回当前 token（供启动后打印）。
func (s *Server) Token() string { return s.token }

// Addr 返回监听地址（Start 后可用）。
func (s *Server) Addr() string { return s.addr }

// SetHandler 设置消息处理函数（可在 Start 前调用）。
func (s *Server) SetHandler(h Handler) { s.handler = h }

// Start 启动 HTTP 服务，监听 127.0.0.1 随机端口。
// 返回实际监听地址（如 http://127.0.0.1:54321）。
func (s *Server) Start() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("webhook 监听失败: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/hook/", s.handleHook)
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/history", s.handleHistory)
	s.srv = &http.Server{Handler: mux}
	s.addr = "http://" + ln.Addr().String()
	go s.srv.Serve(ln) //nolint:errcheck // 退出由 Shutdown 管理
	return s.addr, nil
}

// Stop 优雅关闭服务。
func (s *Server) Stop(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	return s.srv.Shutdown(ctx)
}

// History 返回触发历史副本（最新在前）。
func (s *Server) History() []HistoryEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]HistoryEntry, len(s.history))
	copy(out, s.history)
	return out
}

func (s *Server) handleHook(w http.ResponseWriter, r *http.Request) {
	// 路径形如 /hook/<token>
	tok := r.URL.Path[len("/hook/"):]
	if tok == "" || tok != s.token {
		http.Error(w, `{"error":"forbidden"}`, http.StatusForbidden)
		return
	}
	var msg Message
	if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
		http.Error(w, `{"error":"bad request: `+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	if msg.Text == "" {
		http.Error(w, `{"error":"message 不能为空"}`, http.StatusBadRequest)
		return
	}
	if msg.Mode == "" {
		msg.Mode = "goal"
	}
	s.mu.RLock()
	h := s.handler
	s.mu.RUnlock()
	if h == nil {
		http.Error(w, `{"error":"handler 未配置"}`, http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Minute)
	defer cancel()
	start := time.Now()
	result, err := h(ctx, msg)
	ms := time.Since(start).Milliseconds()

	entry := HistoryEntry{TS: start, Mode: msg.Mode, Text: msg.Text, Ms: ms}
	if err != nil {
		entry.Err = err.Error()
		s.record(entry)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	entry.Result = result
	s.record(entry)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().Format(time.RFC3339)})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"history": s.History()})
}

func (s *Server) record(e HistoryEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.history = append([]HistoryEntry{e}, s.history...)
	if len(s.history) > s.maxHist {
		s.history = s.history[:s.maxHist]
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// randomToken 生成 32 位十六进制随机 token（crypto/rand）。
func randomToken() string {
	const hex = "0123456789abcdef"
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// 极低概率回退：纳秒种子伪随机（仍唯一）
		var seed uint64 = uint64(time.Now().UnixNano())
		for i := range b {
			seed = seed*6364136223846793005 + 1442695040888963407
			b[i] = byte(seed >> 32)
		}
	}
	for i := range b {
		b[i] = hex[b[i]&0xf]
	}
	return string(b)
}
