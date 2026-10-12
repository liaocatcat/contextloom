package model

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 流末尾部丢失回归：回复尾部含 ` / < / [ 时 safeTail 扣留 win 等待拼合，
// finish_reason=stop 原先只 flush pendingText、丢弃 win 残留 →
//   ① 最后几个字/一两行被截断；
//   ② 全文密集含标记字符时整条回复滞留 win → 正文空。
// 修复：finish_reason 收尾统一 win→pending→flush（与 eof 兜底同规则）。

// SSE mock：content 分片逐条下发，最后发 finish_reason
func sseServer(t *testing.T, chunks []string, finish string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fl, _ := w.(http.Flusher)
		for _, c := range chunks {
			line := `data: {"choices":[{"delta":{"content":` + jsonStr(c) + `}}]}`
			w.Write([]byte(line + "\n"))
			fl.Flush()
		}
		w.Write([]byte(`data: {"choices":[{"delta":{},"finish_reason":"` + finish + `"}]}` + "\n"))
		w.Write([]byte("data: [DONE]\n"))
		fl.Flush()
	}))
}

func collectTailStream(t *testing.T, ch <-chan StreamChunk) (content string, finish string) {
	t.Helper()
	for sc := range ch {
		content += sc.Content
		if sc.FinishReason != "" {
			finish = sc.FinishReason
		}
	}
	return
}

// ① 结尾是代码围栏：修复前尾部 "```"（含其前几字符）被 win 吞掉
func TestStreamEx_TailCodeFenceNotLost(t *testing.T) {
	full := "如下：\n\n```go\nfmt.Println(\"hi\")\n```\n\n以上就是全部内容。"
	ts := sseServer(t, []string{
		"如下：\n\n```go\nfmt.Println(\"hi\")\n",
		"```\n\n以上就是全部内容。",
	}, "stop")
	defer ts.Close()
	ds := NewDeepSeekBackend(ts.URL, "sk-test", "m", 131072)
	ch, errCh := ds.StreamEx(context.Background(), nil)
	got, _ := collectTailStream(t, ch)
	if e := <-errCh; e != nil {
		t.Fatalf("errCh: %v", e)
	}
	if got != full {
		t.Fatalf("尾部内容丢失\nwant: %q\ngot:  %q", full, got)
	}
}

// ② 全文密集含 backtick/尖括号：整条回复不得被 win 吞掉
func TestStreamEx_AllContentHeldInWindowNotLost(t *testing.T) {
	full := "<a> 与 <b> 比较，`code` 与 [引用1]，<c> 与 `d` 交替出现且每片尾部都有标记字符 <"
	parts := []string{}
	// 逐 12 字符切片，保证每个分片尾部都落在标记字符附近（safeTail 扣留路径）
	rs := []rune(full)
	for i := 0; i < len(rs); i += 12 {
		end := i + 12
		if end > len(rs) {
			end = len(rs)
		}
		parts = append(parts, string(rs[i:end]))
	}
	ts := sseServer(t, parts, "stop")
	defer ts.Close()
	ds := NewDeepSeekBackend(ts.URL, "sk-test", "m", 131072)
	ch, errCh := ds.StreamEx(context.Background(), nil)
	got, _ := collectTailStream(t, ch)
	if e := <-errCh; e != nil {
		t.Fatalf("errCh: %v", e)
	}
	if got != full {
		t.Fatalf("整条回复被 win 吞掉\nwant: %q\ngot:  %q", full, got)
	}
}

// ③ tool_calls 收尾：win 残留正文也不丢（回归壳尾+正文混合场景）
func TestStreamEx_ToolCallsFinishKeepsTailText(t *testing.T) {
	ts := sseServer(t, []string{
		"查询结果如下：",
		"<tool_calls>\n<invoke name=\"read_file\">\n<parameter name=\"path\">a.go</parameter>\n</invoke>",
		"</tool_calls>",
	}, "tool_calls")
	defer ts.Close()
	ds := NewDeepSeekBackend(ts.URL, "sk-test", "m", 131072)
	ch, errCh := ds.StreamEx(context.Background(), nil)
	got, _ := collectTailStream(t, ch)
	if e := <-errCh; e != nil {
		t.Fatalf("errCh: %v", e)
	}
	if !strings.HasPrefix(got, "查询结果如下：") {
		t.Fatalf("调用块前缀正文丢失: %q", got)
	}
}

// jsonStr JSON 字符串转义（测试辅助）
func jsonStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString("\\\"")
		case '\\':
			b.WriteString("\\\\")
		case '\n':
			b.WriteString("\\n")
		case '\t':
			b.WriteString("\\t")
		case '\r':
			b.WriteString("\\r")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
