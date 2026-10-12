package model

import (
	"strings"
)

// NewBackend 按后端名创建模型后端（对标前沿"多 provider + 模型无关"的零依赖落地）。
//
// 支持的 backend 值（大小写不敏感）：
//   - "openai" / "openai-compatible" / "llama" / "ollama" / "local" / "mock"（默认）：
//     OpenAI 兼容通道（/v1/chat/completions）——不发送 reasoning_effort、
//     api_key 可为空（本地模型）、空时回退环境变量 OPENAI_API_KEY。
//     已验证兼容 llama.cpp / 各类本地推理服务；"mock" 用于测试/本地 stub。
//   - "deepseek"：DeepSeek 官方通道——发送 reasoning_effort（sendEffort 控制，
//     默认 true）、api_key 空时回退环境变量 DEEPSEEK_API_KEY。
func NewBackend(backendName, endpoint, apiKey, modelID string, maxContext int, sendEffort bool) (Backend, error) {
	name := strings.ToLower(strings.TrimSpace(backendName))
	if name == "" {
		name = "openai-compatible"
	}
	switch name {
	case "deepseek":
		return NewDeepSeekBackendWithEffort(endpoint, apiKey, modelID, maxContext, sendEffort), nil
	case "openai", "openai-compatible", "llama", "ollama", "local", "mock":
		// "mock" 用于测试/本地 stub（走 OpenAI 兼容通道，无 effort、api_key 可空）
		return NewOpenAICompatible(endpoint, apiKey, modelID, maxContext), nil
	default:
		// 向后兼容：未识别的 backend 名视为 DeepSeek 模型 ID（旧配置
		// backend: "legacy-model-id-0731" 直接可用，模型 ID 由 API 校验）
		return NewDeepSeekBackendWithEffort(endpoint, apiKey, backendName, maxContext, sendEffort), nil
	}
}

// LocalBackendNames 本地/OpenAI 兼容后端名集合（api_key 可为空）。
func LocalBackendNames() map[string]bool {
	return map[string]bool{
		"openai":            true,
		"openai-compatible": true,
		"llama":             true,
		"ollama":            true,
		"local":             true,
		"mock":              true,
	}
}
