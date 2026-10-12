package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 复现 16:26 真实场景（多工具调用 + XML 文本格式）：
// 模型第一轮流式输出 2 个 XML 文本调用（无 API 级 id），工具执行后第二轮
// 携带 tool_call_id 回传。mock 服务器模拟真实 DeepSeek 的严格校验：
//   - assistant.tool_calls[].id 必须非空、非 "text-*" 标记、批次内唯一
//   - tool 消息的 tool_call_id 必须匹配 assistant tool_calls 的 id
//
// 非法则返回 400（与真实 API 行为一致）。
//
// 修复前：XML 解析 ID 为 "text-xml"（重复）→ 第二轮 400 → 对话中止无最终回答。
// 修复后：ID 规范化为 call_<name>_<n> → 第二轮正常 → 模型输出最终回答。
func TestAgentRunStream_Real1626_MultiXMLCallIDValid(t *testing.T) {
	var reqCount atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := reqCount.Add(1)

		// 解析请求体，校验 tool_call_id（真实 DeepSeek 的 400 触发条件）
		var body struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
				ToolCalls  []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)

		var assistantIDs []string
		for _, m := range body.Messages {
			for _, tc := range m.ToolCalls {
				assistantIDs = append(assistantIDs, tc.ID)
			}
		}
		// 严格校验（模拟 OpenAI/DeepSeek 兼容 API）：
		// 非法（text-* 标记/空）或重复 → 400
		seen := map[string]bool{}
		valid := true
		for _, id := range assistantIDs {
			if id == "" || strings.HasPrefix(id, "text-") || seen[id] {
				valid = false
				break
			}
			seen[id] = true
		}
		for _, m := range body.Messages {
			if m.Role == "tool" {
				if m.ToolCallID == "" || strings.HasPrefix(m.ToolCallID, "text-") || !seen[m.ToolCallID] {
					valid = false
					break
				}
			}
		}
		if !valid {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			w.Write([]byte(`{"error":{"message":"tool_calls[0].id must be a valid unique id"}}`))
			return
		}

		// 合法：返回 SSE 流
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		var payload string
		if n == 1 {
			// 第一轮：正文 + 2 个 XML 文本调用（真实 16:26 形态，无 id）
			payload = `data: {"choices":[{"delta":{"content":"我来查找项目中存储历史对话记录的位置。先查看项目结构和内存目录。"}}]}
data: {"choices":[{"delta":{"content":"<tool_calls>\n<invoke name=\"list_files\"><parameter name=\"path\">/tmp/mem</parameter></invoke>\n<invoke name=\"exec_command\"><parameter name=\"command\">find /tmp -name \"*chat*\"</parameter></invoke>\n</tool_calls>"}}]}
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
		} else {
			// 第二轮：最终回答（工具结果已回传）
			payload = `data: {"choices":[{"delta":{"content":"已查看。历史对话记录位于 .contextloom/memory 目录，共 3 条。"}}]}
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
`
		}
		w.Write([]byte(payload))
		fl.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "e2e-1626",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "列出历史对话", Mode: "task"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: ds,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	t.Logf("agent 输出: %q", got)

	// 两个工具都必须执行（16:26 场景：list_files + exec_command）
	if !strings.Contains(got, "执行工具: list_files") {
		t.Errorf("应执行 list_files，实际输出: %q", got)
	}
	if !strings.Contains(got, "✔ exec_command") {
		t.Errorf("应执行 exec_command，实际输出: %q", got)
	}
	// 第二轮必须输出最终回答（修复前：400 → 中止，无最终回答）
	if !strings.Contains(got, "历史对话记录位于") {
		t.Errorf("工具执行后应输出最终回答（第二轮未被 400 中止），实际输出: %q", got)
	}
	// 至少两轮请求（第一轮工具调用 + 第二轮最终回答）
	if reqCount.Load() < 2 {
		t.Errorf("应有 ≥2 轮 API 请求（工具回传后继续），实际 %d 轮", reqCount.Load())
	}
}

// normalizeToolCallIDs 单测：text-* 标记 ID 全部重生成，唯一且合法
func TestNormalizeToolCallIDs(t *testing.T) {
	calls := []types.ToolCall{
		{ID: "text-xml", Type: "function", Function: types.FunctionCall{Name: "list_files", Arguments: `{"path":"/a"}`}},
		{ID: "text-xml", Type: "function", Function: types.FunctionCall{Name: "exec_command", Arguments: `{"command":"ls"}`}},
		{ID: "text-line", Type: "function", Function: types.FunctionCall{Name: "list_files", Arguments: `{"path":"/b"}`}},
		{ID: "call_ok_1", Type: "function", Function: types.FunctionCall{Name: "read_file", Arguments: `{"path":"/c"}`}},
		{ID: "", Type: "function", Function: types.FunctionCall{Name: "read_file", Arguments: `{"path":"/d"}`}},
	}
	got := normalizeToolCallIDs(calls)
	seen := map[string]bool{}
	for i, c := range got {
		if c.ID == "" || strings.HasPrefix(c.ID, "text-") {
			t.Errorf("第 %d 个调用 ID 非法: %q", i, c.ID)
		}
		if seen[c.ID] {
			t.Errorf("第 %d 个调用 ID 重复: %q", i, c.ID)
		}
		seen[c.ID] = true
	}
	// 合法 ID 原样保留
	if got[3].ID != "call_ok_1" {
		t.Errorf("合法 ID 应保留，got %q", got[3].ID)
	}
	// 空 ID 已生成
	if got[4].ID == "" {
		t.Errorf("空 ID 应被生成")
	}
}
