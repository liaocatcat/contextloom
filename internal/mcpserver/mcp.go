// Package mcpserver 轻量 MCP (Model Context Protocol) Server。
//
// 设计动机（竞品分析结论）：
//   - ContextLoom 的核心资产（Harness 索引、记忆、技能）目前只有 TUI 一个消费端；
//   - 通过 MCP Server 以只读方式暴露这些能力，可让 Claude Code / Cursor / 其他 AI 工具
//     直接调用 ContextLoom 的上下文服务 —— "Server 优先，Client 后置"。
//
// 实现约束：
//   - 零外部依赖：手写 JSON-RPC 2.0 over stdio，不引入 MCP SDK（保持"6 个直接依赖"哲学）；
//   - 只读安全：所有工具均为只读（检索/列表/加载），不做任何写操作，无需确认弹窗；
//   - 协议子集：initialize / tools/list / tools/call，满足主流 MCP 客户端接入。
package mcpserver

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/contextloom/contextloom-agent/pkg/version"
)

// Runtime 是 MCP Server 需要的最小能力集（由 runtime.Runtime 实现）。
type Runtime interface {
	HarnessSearch(ctx context.Context, query string, limit int) []map[string]any
	MemoryList() []map[string]any
	MemoryLoad(uuid string) (map[string]any, error)
	SkillsList() []map[string]any
}

// Tool 描述一个 MCP 工具。
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

// Server 实现 stdio JSON-RPC 2.0 MCP Server。
type Server struct {
	rt    Runtime
	in    io.Reader
	out   io.Writer
	tools []Tool
}

// NewServer 创建 MCP Server，默认使用 stdin/stdout。
func NewServer(rt Runtime) *Server {
	s := &Server{rt: rt, in: os.Stdin, out: os.Stdout}
	s.tools = []Tool{
		{
			Name:        "harness_search",
			Description: "在 ContextLoom Harness 索引中检索项目上下文（代码块、文档片段），返回按相关性排序的结果",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"检索查询"},"limit":{"type":"integer","description":"返回条数上限，默认5"}},"required":["query"]}`),
		},
		{
			Name:        "memory_list",
			Description: "列出 ContextLoom 所有历史会话记忆（标题/时间/摘要）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
		{
			Name:        "memory_load",
			Description: "加载指定会话记忆的完整内容（历史消息）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"uuid":{"type":"string","description":"会话 UUID"}},"required":["uuid"]}`),
		},
		{
			Name:        "skill_list",
			Description: "列出 ContextLoom 所有可用技能（SKILL.md 定义）",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		},
	}
	return s
}

// Run 阻塞运行 MCP Server，逐行读取 stdin 上的 JSON-RPC 请求并响应。
func (s *Server) Run(ctx context.Context) error {
	sc := bufio.NewScanner(s.in)
	sc.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		resp := s.handle(line)
		if resp != nil {
			b, _ := json.Marshal(resp)
			fmt.Fprintln(s.out, string(b))
		}
	}
	return sc.Err()
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) handle(line string) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal([]byte(line), &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: nil, Error: &rpcError{Code: -32700, Message: "Parse error"}}
	}
	ctx := context.Background()

	switch req.Method {
	case "initialize":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "contextloom", "version": version.Version},
		}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": s.tools}}
	case "tools/call":
		return s.callTool(ctx, req)
	case "notifications/initialized":
		return nil // 通知无响应
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Method not found: " + req.Method}}
	}
}

func (s *Server) callTool(ctx context.Context, req rpcRequest) *rpcResponse {
	var params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "Invalid params"}}
	}

	var result any
	var err error
	switch params.Name {
	case "harness_search":
		query, _ := params.Arguments["query"].(string)
		limit, _ := params.Arguments["limit"].(float64)
		result, err = s.safeHarnessSearch(ctx, query, int(limit))
	case "memory_list":
		result = s.rt.MemoryList()
	case "memory_load":
		uuid, _ := params.Arguments["uuid"].(string)
		result, err = s.rt.MemoryLoad(uuid)
	case "skill_list":
		result = s.rt.SkillsList()
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "Unknown tool: " + params.Name}}
	}

	if err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: err.Error()}}
	}
	return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"content": []map[string]any{{"type": "text", "text": mustJSON(result)}}}}
}

// safeHarnessSearch 捕获 panic，防止异常输入导致 MCP 崩溃。
func (s *Server) safeHarnessSearch(ctx context.Context, query string, limit int) (result any, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("harness search panic: %v", r)
		}
	}()
	return s.rt.HarnessSearch(ctx, query, limit), nil
}

func mustJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return string(b)
}
