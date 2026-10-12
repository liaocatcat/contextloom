package model

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func imgMsg(text string) types.Message {
	return types.Message{Role: "user", Content: []types.ContentPart{
		{Type: "text", Text: text},
		{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
	}}
}

// TestStripImagesForTextModel：剥离全部 image_url、纯文本不变、无变化返回原切片。
func TestStripImagesForTextModel(t *testing.T) {
	msgs := []types.Message{imgMsg("看图"), {Role: "assistant", Content: "收到"}}
	out := stripImagesForTextModel(msgs)
	if got := out[0].ImagePartCount(); got != 0 {
		t.Errorf("剥离后仍有 %d 张图片", got)
	}
	if !strings.Contains(out[0].TextContent(), "看图") ||
		!strings.Contains(out[0].TextContent(), types.ImageStrippedPlaceholder) {
		t.Errorf("剥离后文本 = %q（应保留原文与占位）", out[0].TextContent())
	}
	if out[1].Content != "收到" {
		t.Errorf("纯文本消息不应变化")
	}

	// 无图片 → 返回原切片（零拷贝快路径）
	plain := []types.Message{{Role: "user", Content: "纯文本"}}
	if got := stripImagesForTextModel(plain); &got[0] != &plain[0] {
		t.Errorf("无图片应返回原切片")
	}
}

// TestChat_StripsImagesWhenUnsupported：supportsVision=false（DeepSeek 官方默认）
// 时请求体不得包含 image_url——这是 400（unknown variant 'image_url'）的根治点。
func TestChat_StripsImagesWhenUnsupported(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok","role":"assistant"}}]}`))
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "sk", "main-model", 131072)
	d.SetSupportsVision(false)
	if _, _, err := d.Chat(context.Background(), []types.Message{imgMsg("看图")}, nil); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	bodyJSON, _ := json.Marshal(gotBody)
	if strings.Contains(string(bodyJSON), "image_url") {
		t.Fatalf("不支持多模态时请求体不得含 image_url：%s", bodyJSON)
	}
	if !strings.Contains(string(bodyJSON), types.ImageStrippedPlaceholder) {
		t.Fatalf("请求体应含剥离占位文本：%s", bodyJSON)
	}
}

// TestChat_KeepsImagesWhenSupported：supportsVision=true 时图片正常透传。
func TestChat_KeepsImagesWhenSupported(t *testing.T) {
	var gotBody map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok","role":"assistant"}}]}`))
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "sk", "main-model", 131072)
	d.SetSupportsVision(true)
	if _, _, err := d.Chat(context.Background(), []types.Message{imgMsg("看图")}, nil); err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	bodyJSON, _ := json.Marshal(gotBody)
	if !strings.Contains(string(bodyJSON), "image_url") {
		t.Fatalf("支持多模态时请求体应保留 image_url：%s", bodyJSON)
	}
}

// TestChat_400Retry_StripsImages：历史遗留多模态消息（JSONL 恢复/检查点）在
// 纯文本模型上触发 400 后，自修复重试自动剥离 image_url 并成功。
func TestChat_400Retry_StripsImages(t *testing.T) {
	var attempts int
	var lastBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		buf := make([]byte, 8192)
		n, _ := r.Body.Read(buf)
		lastBody = string(buf[:n])
		if attempts == 1 {
			// 模拟 DeepSeek 真实错误：unknown variant 'image_url', expected 'text'
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"message":"Failed to deserialize the JSON body into the target type: messages[0]: unknown variant ` + "`image_url`" + `, expected ` + "`text`" + `","type":"invalid_request_error"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok","role":"assistant"}}]}`))
	}))
	defer ts.Close()

	d := NewDeepSeekBackend(ts.URL, "sk", "main-model", 131072)
	d.SetSupportsVision(false) // 模拟 supports_vision 配置有误或历史遗留多模态
	if _, _, err := d.Chat(context.Background(), []types.Message{imgMsg("看图")}, nil); err != nil {
		t.Fatalf("400 自修复后应成功，实际 %v", err)
	}
	if attempts != 2 {
		t.Fatalf("应请求 2 次（400+重试），实际 %d", attempts)
	}
	if strings.Contains(lastBody, "image_url") {
		t.Fatalf("重试请求体不得含 image_url：%s", lastBody)
	}
	if !strings.Contains(lastBody, types.ImageStrippedPlaceholder) {
		t.Fatalf("重试请求体应含占位文本：%s", lastBody)
	}
}
