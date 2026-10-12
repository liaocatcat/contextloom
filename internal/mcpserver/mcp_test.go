package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeRuntime 实现 Runtime 接口的最小桩。
type fakeRuntime struct{}

func (f *fakeRuntime) HarnessSearch(ctx context.Context, query string, limit int) []map[string]any {
	return []map[string]any{{"id": "block1", "score": 88.5, "content": "搜索结果: " + query}}
}
func (f *fakeRuntime) MemoryList() []map[string]any {
	return []map[string]any{{"uuid": "u1", "title": "会话一", "updated": "2026-08-01 10:00"}}
}
func (f *fakeRuntime) MemoryLoad(uuid string) (map[string]any, error) {
	return map[string]any{"uuid": uuid, "title": "会话一", "messages": []map[string]any{{"role": "user", "content": "你好"}}}, nil
}
func (f *fakeRuntime) SkillsList() []map[string]any {
	return []map[string]any{{"name": "code-review", "description": "代码审查"}}
}

func newTestServer() *Server {
	return &Server{rt: &fakeRuntime{}, tools: []Tool{}}
}

func mustResp(t *testing.T, s *Server, line string) *rpcResponse {
	t.Helper()
	resp := s.handle(line)
	if resp == nil {
		t.Fatalf("handle(%q) 返回 nil", line)
	}
	return resp
}

func TestHandle_Initialize(t *testing.T) {
	s := newTestServer()
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if resp.Error != nil {
		t.Fatalf("initialize 报错: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok || m["serverInfo"] == nil {
		t.Fatalf("initialize 结果缺少 serverInfo: %v", resp.Result)
	}
}

func TestHandle_ToolsList(t *testing.T) {
	s := NewServer(&fakeRuntime{})
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	if resp.Error != nil {
		t.Fatalf("tools/list 报错: %v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	tools := m["tools"].([]Tool)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.Name] = true
	}
	for _, want := range []string{"harness_search", "memory_list", "memory_load", "skill_list"} {
		if !names[want] {
			t.Errorf("缺少工具 %s，实际: %v", want, names)
		}
	}
}

func TestHandle_ToolsCall_HarnessSearch(t *testing.T) {
	s := newTestServer()
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"harness_search","arguments":{"query":"缓存","limit":5}}}`)
	if resp.Error != nil {
		t.Fatalf("tools/call 报错: %v", resp.Error)
	}
	text := resp.Result.(map[string]any)["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(text, "block1") || !strings.Contains(text, "缓存") {
		t.Errorf("harness_search 结果异常: %s", text)
	}
}

func TestHandle_ToolsCall_MemoryList(t *testing.T) {
	s := newTestServer()
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"memory_list","arguments":{}}}`)
	if resp.Error != nil {
		t.Fatalf("memory_list 报错: %v", resp.Error)
	}
	if !strings.Contains(mustJSON(resp.Result), "会话一") {
		t.Errorf("memory_list 结果缺少会话: %v", resp.Result)
	}
}

func TestHandle_ToolsCall_UnknownTool(t *testing.T) {
	s := newTestServer()
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"rm_rf","arguments":{}}}`)
	if resp.Error == nil {
		t.Fatalf("未知工具应报错，实际无错误: %v", resp)
	}
}

func TestHandle_UnknownMethod(t *testing.T) {
	s := newTestServer()
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":6,"method":"evil/method","params":{}}`)
	if resp.Error == nil || resp.Error.Code != -32601 {
		t.Fatalf("未知方法应返回 -32601，实际: %v", resp.Error)
	}
}

func TestHandle_ParseError(t *testing.T) {
	s := newTestServer()
	resp := s.handle(`{invalid json`)
	if resp == nil || resp.Error == nil || resp.Error.Code != -32700 {
		t.Fatalf("非法 JSON 应返回 -32700，实际: %v", resp)
	}
}

// 验证响应是合法 JSON-RPC 2.0（可序列化）。
func TestResponse_Marshalable(t *testing.T) {
	s := newTestServer()
	resp := mustResp(t, s, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"memory_load","arguments":{"uuid":"u1"}}}`)
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("响应无法序列化: %v", err)
	}
	if !strings.Contains(string(b), `"jsonrpc":"2.0"`) {
		t.Errorf("响应缺少 jsonrpc 字段: %s", b)
	}
}
