package gui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestCompressEventFrame_NoImageDataURI 回归：ctx.compressed 压缩态事件帧
// 不得携带图片 dataURI（图片可达 MB 级，事件帧膨胀 + 前端全量建 <img>
// 会卡死主线程 → "压缩后点击无反应/状态栏不刷"根因之一）。
// 历史缺陷：事件帧用 messagesToJSON（带 images: [dataURI,...]），压缩后
// 界面重绘直接卡死；修复后必须走 messagesToJSONLight（图片降级为计数文本）。
func TestCompressEventFrame_NoImageDataURI(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{
		msgs: []types.Message{
			{Role: "user", Content: "[历史摘要] 前面的对话摘要"},
			{Role: "user", Content: []types.ContentPart{
				{Type: "text", Text: "看图"},
				{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}},
			}},
		},
	})
	conn := dialWS(t, ts)
	if resp := sendReq(t, conn, "8", "chat.loadSession", map[string]any{"uuid": "u1"}); !resp.OK {
		t.Fatalf("loadSession 失败: %v", resp.Error)
	}
	if err := conn.WriteJSON(Envelope{ID: "9", Type: "req", Method: "context.compress"}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	var gotEvt *Envelope
	for reading := true; reading && gotEvt == nil; {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取失败: %v", err)
		}
		switch {
		case env.Type == "event" && env.Event == "ctx.compressed":
			gotEvt = &env
		case env.ID == "9" && env.Type == "resp":
		default:
		}
	}
	raw, _ := json.Marshal(gotEvt.Data)
	if strings.Contains(string(raw), "data:image/") {
		t.Fatalf("ctx.compressed 事件帧不得携带图片 dataURI（帧体膨胀根因）:\n%s", truncateStr(string(raw), 500))
	}
	if !strings.Contains(string(raw), "[图片×1]") {
		t.Fatalf("图片消息应降级为计数文本 [图片×1]: %s", truncateStr(string(raw), 500))
	}
}

func TestStripImageDataURI_RemovesDataURIKeepsText(t *testing.T) {
	m := types.Message{Role: "user", Content: []types.ContentPart{
		{Type: "text", Text: "看图说话"},
		{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="}},
	}}
	out, ok := ctxmgr.StripImageDataURI(m)
	if !ok {
		t.Fatal("含图消息应被降级")
	}
	content := out.TextContent()
	if !strings.Contains(content, "看图说话") {
		t.Errorf("降级后应保留文本: %q", content)
	}
	if !strings.Contains(content, types.ImageOmittedPlaceholder) {
		t.Errorf("降级后应含图片占位: %q", content)
	}
	if out.ImagePartCount() != 0 {
		t.Errorf("降级后不应再有图片片段: %d", out.ImagePartCount())
	}
}

func TestStripImageDataURI_PlainTextUnchanged(t *testing.T) {
	m := types.Message{Role: "user", Content: "普通文本"}
	out, ok := ctxmgr.StripImageDataURI(m)
	if ok {
		t.Fatal("纯文本消息不应被降级")
	}
	if out.Content != "普通文本" {
		t.Fatalf("内容不应变化: %v", out.Content)
	}
}

// truncateStr 测试辅助：截断长字符串用于断言输出。
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
