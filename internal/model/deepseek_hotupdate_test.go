package model

import "testing"

// ── GUI 保存设置"立即生效"热更新 setter 单测 ──
// 这些 setter 由 Runtime.applyLiveConfig 在保存 config 后调用，
// 是"保存即生效"链路的核心路径；与请求构造共用 modelMu 互斥。

func TestSetEndpoint_HotUpdate(t *testing.T) {
	b := NewDeepSeekBackend("https://old.example.com", "sk-1", "deepseek-chat", 8192)
	b.SetEndpoint("https://new.example.com")
	if b.endpoint != "https://new.example.com" {
		t.Errorf("SetEndpoint 后 endpoint = %q, want %q", b.endpoint, "https://new.example.com")
	}
}

func TestSetAPIKey_HotUpdate(t *testing.T) {
	b := NewDeepSeekBackend("https://api.deepseek.com", "sk-old", "deepseek-chat", 8192)
	// 更新 key
	b.SetAPIKey("sk-new")
	if b.apiKey != "sk-new" {
		t.Errorf("SetAPIKey 后 apiKey = %q, want %q", b.apiKey, "sk-new")
	}
	// 边界：空串不覆盖（GUI 密码框留空 = 保持原 key，防误清空）
	b.SetAPIKey("")
	if b.apiKey != "sk-new" {
		t.Errorf("SetAPIKey(\"\") 应保持现状, got %q, want %q", b.apiKey, "sk-new")
	}
}

func TestSetMaxContext_HotUpdate(t *testing.T) {
	b := NewDeepSeekBackend("https://api.deepseek.com", "sk-1", "deepseek-chat", 8192)
	b.SetMaxContext(131072)
	if b.MaxContext() != 131072 {
		t.Errorf("SetMaxContext 后 MaxContext() = %d, want 131072", b.MaxContext())
	}
}

func TestSetTopP_HotUpdate(t *testing.T) {
	b := NewDeepSeekBackend("https://api.deepseek.com", "sk-1", "deepseek-chat", 8192)
	// 初始 nil → TopP() 返回 0
	if got := b.TopP(); got != 0 {
		t.Fatalf("初始 TopP() = %v, want 0（未设置）", got)
	}
	b.SetTopP(0.8)
	if got := b.TopP(); got != 0.8 {
		t.Errorf("SetTopP(0.8) 后 TopP() = %v, want 0.8", got)
	}
	// 覆盖更新
	b.SetTopP(0.5)
	if got := b.TopP(); got != 0.5 {
		t.Errorf("SetTopP(0.5) 后 TopP() = %v, want 0.5", got)
	}
}

func TestSetTemperature_HotUpdate(t *testing.T) {
	b := NewDeepSeekBackend("https://api.deepseek.com", "sk-1", "deepseek-chat", 8192)
	if got := b.Temperature(); got != 0 {
		t.Fatalf("初始 Temperature() = %v, want 0（未设置）", got)
	}
	b.SetTemperature(1.2)
	if got := b.Temperature(); got != 1.2 {
		t.Errorf("SetTemperature(1.2) 后 Temperature() = %v, want 1.2", got)
	}
}
