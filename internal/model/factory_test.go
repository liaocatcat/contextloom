package model

import (
	"os"
	"testing"
)

func TestNewBackend_DeepSeek(t *testing.T) {
	b, err := NewBackend("deepseek", "https://x/v1", "sk-test", "default-model", 1000, true)
	if err != nil {
		t.Fatalf("NewBackend deepseek: %v", err)
	}
	db, ok := b.(*DeepSeekBackend)
	if !ok {
		t.Fatalf("deepseek 应为 *DeepSeekBackend，got %T", b)
	}
	if !db.sendEffort {
		t.Fatal("deepseek 通道应发送 effort")
	}
}

func TestNewBackend_OpenAICompatible(t *testing.T) {
	for _, name := range []string{"openai", "openai-compatible", "llama", "ollama", "local", "OPENAI"} {
		b, err := NewBackend(name, "http://127.0.0.1:8080/v1", "", "local-model", 1000, true)
		if err != nil {
			t.Fatalf("NewBackend %s: %v", name, err)
		}
		db, ok := b.(*DeepSeekBackend)
		if !ok {
			t.Fatalf("%s 应为 *DeepSeekBackend，got %T", name, b)
		}
		if db.sendEffort {
			t.Fatalf("%s 通道不应发送 effort", name)
		}
		if db.apiKey != "" {
			t.Fatalf("%s 本地模型 api_key 应可为空", name)
		}
	}
}

func TestNewBackend_EnvFallback(t *testing.T) {
	os.Setenv("OPENAI_API_KEY", "sk-env-openai")
	defer os.Unsetenv("OPENAI_API_KEY")

	b, err := NewBackend("openai", "http://127.0.0.1:8080/v1", "", "m", 1000, false)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	if b.(*DeepSeekBackend).apiKey != "sk-env-openai" {
		t.Fatalf("openai 通道应回退 OPENAI_API_KEY，got %q", b.(*DeepSeekBackend).apiKey)
	}
}

func TestNewBackend_Unknown(t *testing.T) {
	// 向后兼容：未识别的 backend 名视为 DeepSeek 模型 ID（旧配置 backend: "legacy-model-id-0731" 直接可用）
	b, err := NewBackend("anthropic", "https://x/v1", "", "anthropic-model", 1000, true)
	if err != nil {
		t.Fatalf("未知名应回退 DeepSeek 通道而非报错: %v", err)
	}
	if !b.(*DeepSeekBackend).sendEffort {
		t.Fatal("回退 DeepSeek 通道应发送 effort")
	}
}

func TestNewBackend_EmptyDefaultsOpenAICompat(t *testing.T) {
	b, err := NewBackend("", "http://127.0.0.1:8080/v1", "", "m", 1000, true)
	if err != nil {
		t.Fatalf("空 backend 应默认 openai-compatible: %v", err)
	}
	// NewOpenAICompatible 返回 *DeepSeekBackend（复用实现），但 sendEffort 应为 false
	ds := b.(*DeepSeekBackend)
	if ds.sendEffort {
		t.Fatal("空 backend 默认应为 openai-compatible 通道（不发送 effort），而非 deepseek 通道")
	}
}

func TestLocalBackendNames(t *testing.T) {
	names := LocalBackendNames()
	for _, n := range []string{"openai", "llama", "ollama", "local"} {
		if !names[n] {
			t.Fatalf("LocalBackendNames 应含 %s", n)
		}
	}
	if names["deepseek"] {
		t.Fatal("LocalBackendNames 不应含 deepseek")
	}
}
