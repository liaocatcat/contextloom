// Package mcpclient 提供 MCP（Model Context Protocol）Client 实现
// （对标前沿"agent 既是 MCP server 也是 MCP client"的协议位势补全）。
//
// 零依赖：手写 JSON-RPC 2.0 over stdio（与 mcpserver 同风格）。
// 通过配置 mcp.servers 启动外部 MCP server 子进程，将其工具动态注册为
// "mcp.<server>.<tool>" 供模型调用——ContextLoom 得以消费外部 MCP 生态
// （2000+ 服务器），不再是单向协议位势。
package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
	"github.com/contextloom/contextloom-agent/pkg/version"
)

// ProtocolVersion MCP 协议版本（2025-03-26 稳定版）。
const ProtocolVersion = "2025-03-26"

// DefaultCallTimeout 工具调用默认超时。
const DefaultCallTimeout = 60 * time.Second

// ServerConfig MCP server 配置（config.yaml mcp.servers.<name>）。
type ServerConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
	Env     []string `yaml:"env,omitempty"` // 追加子进程环境变量（如 KEY=VALUE）
}

// Client 单个 MCP server 的 stdio 客户端。
type Client struct {
	mu         sync.Mutex
	serverName string
	command    string
	args       []string
	env        []string // 追加到子进程环境（测试注入用）
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	scanner    *bufio.Scanner
	readCh     chan readMsg
	pending    map[int64]chan json.RawMessage
	nextID     int64
	closed     bool
}

type readMsg struct {
	raw json.RawMessage
	err error
}

// New 创建客户端（未连接）。
func New(serverName string, cfg ServerConfig) *Client {
	return &Client{
		serverName: serverName,
		command:    cfg.Command,
		args:       cfg.Args,
		env:        cfg.Env,
		readCh:     make(chan readMsg, 16),
		pending:    make(map[int64]chan json.RawMessage),
	}
}

// SetEnv 追加子进程环境变量（主要用于测试注入 helper 标记）。
func (c *Client) SetEnv(env []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.env = append(c.env, env...)
}

// Connect 启动 server 子进程并完成 initialize 握手。
func (c *Client) Connect(ctx context.Context) error {
	cmd := exec.Command(c.cfgCommand(), c.cfgArgs()...)
	prepareProcess(cmd)
	if len(c.env) > 0 {
		cmd.Env = append(os.Environ(), c.env...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp[%s] 创建 stdin 失败: %w", c.serverName, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp[%s] 创建 stdout 失败: %w", c.serverName, err)
	}
	// stderr 直通，便于排查 server 自身报错
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("mcp[%s] 启动 %s 失败: %w", c.serverName, c.cfgCommand(), err)
	}

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.mu.Unlock()

	go c.readPump()

	// initialize 握手
	resp, err := c.request(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "contextloom",
			"version": version.Version,
		},
	})
	if err != nil {
		c.Close()
		return fmt.Errorf("mcp[%s] initialize 失败: %w", c.serverName, err)
	}
	var initRes struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	_ = json.Unmarshal(resp, &initRes)
	// 通知 server 初始化完成（无 id 的 JSON-RPC 通知）
	if err := c.notify("notifications/initialized", nil); err != nil {
		c.Close()
		return fmt.Errorf("mcp[%s] 发送 initialized 通知失败: %w", c.serverName, err)
	}
	return nil
}

func (c *Client) cfgCommand() string { return c.command }
func (c *Client) cfgArgs() []string  { return c.args }

// readPump 逐行读取 stdout，把带 id 的响应投递给 pending。
func (c *Client) readPump() {
	for {
		if !c.scanner.Scan() {
			c.readCh <- readMsg{err: c.scanner.Err()}
			if c.scanner.Err() == nil {
				c.readCh <- readMsg{err: io.EOF}
			}
			return
		}
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)
		var head struct {
			ID *int64 `json:"id"`
		}
		if err := json.Unmarshal(raw, &head); err != nil || head.ID == nil {
			continue // 通知或无法解析：忽略
		}
		c.mu.Lock()
		ch, ok := c.pending[*head.ID]
		if ok {
			delete(c.pending, *head.ID)
		}
		c.mu.Unlock()
		if ok {
			ch <- raw
		}
	}
}

// request 发送 JSON-RPC 请求并等待响应（ctx 超时控制）。
func (c *Client) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp[%s] 客户端已关闭", c.serverName)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan json.RawMessage, 1)
	c.pending[id] = ch
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
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	c.stdin.Write(append(data, '\n'))
	c.mu.Unlock()

	select {
	case raw := <-ch:
		return c.parseResponse(raw)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("mcp[%s] %s 超时: %w", c.serverName, method, ctx.Err())
	}
}

// notify 发送 JSON-RPC 通知（无 id，不等待响应）。
func (c *Client) notify(method string, params any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return fmt.Errorf("mcp[%s] 客户端已关闭", c.serverName)
	}
	req := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		req["params"] = params
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

// parseResponse 解析 JSON-RPC 响应（错误或 result）。
func (c *Client) parseResponse(raw json.RawMessage) (json.RawMessage, error) {
	var r struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Error != nil {
		return nil, fmt.Errorf("mcp[%s] 错误 %d: %s", c.serverName, r.Error.Code, r.Error.Message)
	}
	return r.Result, nil
}

// ToolInfo MCP tools/list 返回的工具结构。
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListTools 调用 tools/list，返回带 "mcp.<server>.<tool>" 前缀的工具定义。
func (c *Client) ListTools(ctx context.Context) ([]types.ToolDefinition, error) {
	resp, err := c.request(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var list struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := json.Unmarshal(resp, &list); err != nil {
		return nil, fmt.Errorf("mcp[%s] tools/list 响应解析失败: %w", c.serverName, err)
	}
	defs := make([]types.ToolDefinition, 0, len(list.Tools))
	for _, t := range list.Tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		defs = append(defs, types.ToolDefinition{
			Type: "function",
			Function: types.FunctionDefinition{
				Name:        "mcp." + c.serverName + "." + t.Name,
				Description: "MCP[" + c.serverName + "] " + t.Description,
				Parameters:  schema,
			},
		})
	}
	return defs, nil
}

// CallTool 调用 tools/call，返回拼接后的文本结果。
func (c *Client) CallTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultCallTimeout)
	defer cancel()
	resp, err := c.request(ctx, "tools/call", map[string]any{
		"name":      toolName,
		"arguments": args,
	})
	if err != nil {
		return "", err
	}
	var callRes struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(resp, &callRes); err != nil {
		return "", fmt.Errorf("mcp[%s] tools/call 响应解析失败: %w", c.serverName, err)
	}
	var sb strings.Builder
	for _, ct := range callRes.Content {
		if ct.Type == "text" || ct.Text != "" {
			sb.WriteString(ct.Text)
		}
	}
	if callRes.IsError {
		return sb.String(), fmt.Errorf("mcp[%s] 工具 %s 执行失败: %s", c.serverName, toolName, sb.String())
	}
	return sb.String(), nil
}

// Close 终止 server 子进程（杀整棵进程树，平台隔离）。
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	cmd := c.cmd
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		killProcessTree(cmd)
		done := make(chan struct{})
		go func() {
			cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}
