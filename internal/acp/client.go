package acp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// notifMsg 缓冲的推送消息。
type notifMsg struct {
	method string
	params json.RawMessage
}

// Client agent-client-protocol 1.0 客户端（测试与外部接入用）。
// 并发安全：所有连接读写由 mu 串行化；Request 读到的不匹配推送进入 pending 队列，
// 由 Recv 消费——保证"推送先于响应到达"时消息不丢失。
type Client struct {
	conn    *websocket.Conn
	mu      sync.Mutex
	nextID  int
	pending []notifMsg
}

// Dial 连接 ACP 服务器（ws://host:port）。
func Dial(addr string) (*Client, error) {
	u := url.URL{Scheme: "ws", Host: addr, Path: "/"}
	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// DialURL 连接完整 ws URL。
func DialURL(raw string) (*Client, error) {
	conn, _, err := websocket.DefaultDialer.Dial(raw, nil)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

// Request 发送 JSON-RPC 请求并等待匹配 id 的响应（最多 10s）。
// 期间到达的推送消息（id=null）缓冲到 pending 供 Recv 消费。
func (c *Client) Request(method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID

	req := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
	}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := c.conn.WriteMessage(websocket.TextMessage, data); err != nil {
		return nil, err
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		_ = c.conn.SetReadDeadline(deadline)
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("acp %s: 等待响应失败: %w", method, err)
		}
		var resp RPCResponse
		if err := json.Unmarshal(msg, &resp); err != nil {
			continue // 无法解析的消息忽略
		}
		// 匹配请求 id（数字）
		var rid int
		if err := json.Unmarshal(resp.RawID, &rid); err != nil || rid != id {
			// 非本请求响应：若是推送（id=null + method）则入队供 Recv
			var notif struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &notif) == nil && notif.Method != "" {
				c.pending = append(c.pending, notifMsg{method: notif.Method, params: notif.Params})
			}
			continue
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("acp %s: %s (code %d)", method, resp.Error.Message, resp.Error.Code)
		}
		return resp.Result, nil
	}
}

// Recv 读取下一条 Agent→Client 推送消息（server/hello、session/init、
// session/agent_message 等）。优先消费 Request 缓冲的 pending 队列；
// 队列空时阻塞读连接（5s 读超时）。返回 method 与 params 原始 JSON。
func (c *Client) Recv() (method string, params json.RawMessage, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) > 0 {
		m := c.pending[0]
		c.pending = c.pending[1:]
		return m.method, m.params, nil
	}
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	_, msg, err := c.conn.ReadMessage()
	if err != nil {
		return "", nil, err
	}
	var notif struct {
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(msg, &notif); err != nil || notif.Method == "" {
		return "", msg, fmt.Errorf("非 ACP 推送消息: %s", msg)
	}
	return notif.Method, notif.Params, nil
}

// Close 关闭连接。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.Close()
}
