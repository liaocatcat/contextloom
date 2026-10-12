package mcpclient

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestHelperProcess 是 fake MCP server 的测试 helper 进程：
// 通过 GO_WANT_MCP_HELPER=1 环境变量启动，从 stdin 读取 JSON-RPC 请求并响应。
// 支持：initialize / tools/list / tools/call（返回固定文本或 isError）。
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue // 通知
		}
		var resp map[string]any
		switch req.Method {
		case "initialize":
			resp = map[string]any{"id": *req.ID, "result": map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
			}}
		case "tools/list":
			resp = map[string]any{"id": *req.ID, "result": map[string]any{
				"tools": []map[string]any{
					{"name": "read_file", "description": "读取文件", "inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
						"required": []string{"path"},
					}},
					{"name": "echo", "description": "回显", "inputSchema": map[string]any{
						"type": "object", "properties": map[string]any{"msg": map[string]any{"type": "string"}},
					}},
				},
			}}
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			json.Unmarshal(req.Params, &params)
			if params.Name == "fail_tool" {
				resp = map[string]any{"id": *req.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "boom"}},
					"isError": true,
				}}
			} else if params.Name == "nosuch" {
				resp = map[string]any{"id": *req.ID, "error": map[string]any{"code": -32602, "message": "method not found"}}
			} else {
				resp = map[string]any{"id": *req.ID, "result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": "echo:" + fmt.Sprint(params.Arguments["msg"])}},
				}}
			}
		default:
			resp = map[string]any{"id": *req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}}
		}
		enc.Encode(resp)
	}
	os.Exit(0)
}

// newTestClient 启动 fake MCP server 并完成握手。
func newTestClient(t *testing.T) *Client {
	t.Helper()
	c := New("fake", ServerConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestHelperProcess"},
	})
	c.SetEnv([]string{"GO_WANT_MCP_HELPER=1"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestConnect_Initialize(t *testing.T) {
	c := newTestClient(t)
	if c.cmd == nil || c.cmd.Process == nil {
		t.Fatal("连接后子进程应存活")
	}
}

func TestListTools_PrefixAndSchema(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defs, err := c.ListTools(ctx)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(defs) != 2 {
		t.Fatalf("应返回 2 个工具，got %d", len(defs))
	}
	if defs[0].Function.Name != "mcp.fake.read_file" {
		t.Fatalf("工具名应带前缀 mcp.fake.，got %q", defs[0].Function.Name)
	}
	// schema 转换：properties 保留、required 保留
	props, ok := defs[0].Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema 应含 properties: %v", defs[0].Function.Parameters)
	}
	if _, ok := props["path"]; !ok {
		t.Fatalf("properties 应含 path: %v", props)
	}
	req, ok := defs[0].Function.Parameters["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "path" {
		t.Fatalf("required 应含 path: %v", defs[0].Function.Parameters["required"])
	}
	if !strings.Contains(defs[0].Function.Description, "MCP[fake]") {
		t.Fatalf("描述应带 server 名: %q", defs[0].Function.Description)
	}
}

func TestCallTool_Success(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := c.CallTool(ctx, "echo", map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("输出应含 hello，got %q", out)
	}
}

func TestCallTool_IsError(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.CallTool(ctx, "fail_tool", nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("isError 工具应返回错误，got %v", err)
	}
}

func TestCallTool_UnknownMethod(t *testing.T) {
	c := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := c.CallTool(ctx, "nosuch", nil)
	if err == nil || !strings.Contains(err.Error(), "method not found") {
		t.Fatalf("未知方法应报错，got %v", err)
	}
}

func TestCallTool_Timeout(t *testing.T) {
	c := newTestClient(t)
	// 极短超时（通过 DefaultCallTimeout 无法注入，用已取消的 ctx 模拟超时路径）
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.CallTool(ctx, "echo", nil)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("取消 ctx 应报超时，got %v", err)
	}
}

func TestClose_KillsProcess(t *testing.T) {
	c := newTestClient(t)
	pid := c.cmd.Process.Pid
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// 进程应已退出（kill 后 2s 内回收；signal: killed 时 Exited() 为 false，故以 ProcessState 非 nil 为准）
	done := make(chan struct{})
	go func() {
		for {
			if c.cmd.ProcessState != nil {
				close(done)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("Close 后进程 %d 未退出", pid)
	}
	// 幂等
	if err := c.Close(); err != nil {
		t.Fatalf("二次 Close 应无错，got %v", err)
	}
}
