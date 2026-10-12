package gui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestMessagesToJSON_Multimodal 多模态消息渲染为 "[图片×N] + 文本"，
// 不再输出 Go 结构体转储（%v 隐患）；纯文本/空内容行为不变。
func TestMessagesToJSON_Multimodal(t *testing.T) {
	s := &Server{}
	msgs := []types.Message{
		{Role: "user", Content: []types.ContentPart{
			{Type: "text", Text: "看图"},
			{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
		}},
		{Role: "user", Content: []types.ContentPart{
			{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
			{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
		}},
		{Role: "assistant", Content: "回复文本"},
	}
	out := s.messagesToJSON(msgs)
	if len(out) != 3 {
		t.Fatalf("输出长度 = %d，期望 3", len(out))
	}
	if c := out[0]["content"].(string); c != "[图片×1] 看图" {
		t.Errorf("文本+图渲染 = %q，期望 [图片×1] 看图", c)
	}
	if c := out[1]["content"].(string); c != "[图片×2]" {
		t.Errorf("纯图渲染 = %q，期望 [图片×2]", c)
	}
	if c := out[2]["content"].(string); c != "回复文本" {
		t.Errorf("纯文本渲染 = %q，期望 回复文本", c)
	}
	for i, m := range out {
		if !strings.Contains(m["role"].(string), "user") && i < 2 {
			t.Errorf("第 %d 条 role 错误: %v", i, m["role"])
		}
	}
	// 渲染结果不得包含 Go 结构体转储特征
	for _, m := range out {
		if strings.Contains(m["content"].(string), "ImageURL") || strings.Contains(m["content"].(string), "0xc") {
			t.Errorf("渲染结果泄漏结构体转储: %q", m["content"])
		}
	}
}

// TestMessagesToJSON_JSONRoundTripContent 检查点恢复后的 []any 形态也能渲染。
func TestMessagesToJSON_JSONRoundTripContent(t *testing.T) {
	s := &Server{}
	msg := types.Message{Role: "user", Content: []types.ContentPart{
		{Type: "text", Text: "看图"},
		{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
	}}
	// 模拟 JSON 序列化再反序列化（Content 变 []any）
	var raw map[string]any
	b, err := jsonMarshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if err := jsonUnmarshal(b, &raw); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	rt := types.Message{Role: "user", Content: raw["content"]}
	out := s.messagesToJSON([]types.Message{rt})
	if len(out) != 1 {
		t.Fatalf("输出长度 = %d，期望 1", len(out))
	}
	if c := out[0]["content"].(string); c != "[图片×1] 看图" {
		t.Errorf("[]any 渲染 = %q，期望 [图片×1] 看图", c)
	}
}

// jsonMarshal/jsonUnmarshal 封装标准库调用（测试内避免额外 import 噪声）。
func jsonMarshal(v any) ([]byte, error)   { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
