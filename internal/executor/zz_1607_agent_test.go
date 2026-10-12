package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/harness"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/tools"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 用户 16:07 真实截断内容：两个 list_files，第二个缺闭合标签
const content1607Agent = `<tool_calls>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom</parameter>
</invoke>
<invoke name="list_files">
<parameter name="path" string="true">/path/to/contextloom/.contextloom/memory`

// agentSSEServer 两轮对话 SSE 服务器：轮1 输出截断 XML，轮2 输出正常回答
func agentSSEServer1607(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	round := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter 不支持 Flush")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		mu.Lock()
		round++
		cur := round
		mu.Unlock()

		var events []map[string]any
		if cur == 1 {
			// 轮1：逐 token 输出截断 XML（每 chunk 3 个 rune）
			runes := []rune(content1607Agent)
			for i := 0; i < len(runes); i += 3 {
				end := i + 3
				if end > len(runes) {
					end = len(runes)
				}
				events = append(events, map[string]any{
					"choices": []any{map[string]any{"delta": map[string]any{"content": string(runes[i:end])}}},
				})
			}
			events = append(events, map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
			})
		} else {
			// 轮2：正常回答
			events = append(events, map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": "以下是项目历史对话记录：\n1. 14:07 目录列表\n2. 15:35 流式问题分析"}}},
			})
			events = append(events, map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "stop"}},
			})
		}
		for _, evt := range events {
			b, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}))
}

// 端到端：16:07 截断 XML 流 → Agent.RunStream 应执行工具并继续对话到正常回答
func TestAgentRunStream_Real1607_TruncatedXML(t *testing.T) {
	ts := agentSSEServer1607(t)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "flash-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	toolReg := tools.NewRegistry()

	agent := &Agent{
		ID: "test-1607",
		Task: types.TaskCreate{
			Directive: "CODE_AGENT",
			Task:      "列出项目所有历史对话记录",
		},
		harness: eng,
		tools:   toolReg,
		backend: ds,
	}

	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}

	var sb strings.Builder
	for token := range ch {
		sb.WriteString(token)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("RunStream 错误: %v", e)
	}

	out := sb.String()
	t.Logf("完整输出:\n%s", out)

	// 1) 工具必须被执行（不再是"直接工作完成"）
	if !strings.Contains(out, "⚡ 执行工具") {
		t.Errorf("期望工具被执行（含 ⚡ 执行工具），实际输出:\n%s", out)
	}
	// 2) XML 调用块不应泄漏到正文
	if strings.Contains(out, "<tool_calls") || strings.Contains(out, "<invoke") {
		t.Errorf("XML 调用块泄漏到输出:\n%s", out)
	}
	// 3) 对话必须继续到下一轮并给出最终回答（不再中止）
	if !strings.Contains(out, "历史对话记录") {
		t.Errorf("期望第二轮正常回答出现（对话未中止），实际输出:\n%s", out)
	}
}
