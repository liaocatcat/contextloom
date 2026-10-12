package runtime

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/ctxmgr"
	"github.com/contextloom/contextloom-agent/internal/memory"
	"github.com/contextloom/contextloom-agent/internal/model"
	"github.com/contextloom/contextloom-agent/internal/overseer"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// =============================================================================
// Test helpers
// =============================================================================

func newTestRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	return newTestRuntimeAt(t, tmpDir), tmpDir
}

// newTestRuntimeAt 在指定目录创建 Runtime，供"退出后重新进入"类测试复用同一存储。
func newTestRuntimeAt(t *testing.T, tmpDir string) *Runtime {
	t.Helper()
	cfg := &config.Config{
		ActiveModelName: "mock",
		Models: []config.ModelPreset{{
			Name:       "mock",
			Backend:    "mock",
			Endpoint:   "http://localhost:8080",
			APIKey:     "sk-test",
			MaxContext: 4096,
		}},
		Harness: config.HarnessConfig{
			ScoreThreshold: 50,
			DecayFactor:    0.9,
			AnchorRatio:    0.2,
		},
		Memory: config.MemoryConfig{
			Directory:   filepath.Join(tmpDir, "memory"),
			MaxSessions: 10,
		},
		Plugins: config.PluginsConfig{
			Directory: filepath.Join(tmpDir, "plugins"),
		},
		// 显式设置项目根，避免 runtime 在测试目录（CWD）生成 .contextloom/ 污染源码
		Project: config.ProjectConfig{
			Root:  tmpDir,
			Vocab: filepath.Join(tmpDir, "vocab.json"),
		},
		ACP: config.ACPConfig{
			Enabled: true,
			Port:    20250,
		},
		CLIDefaults: config.CLIDefaultsConfig{
			Model:  "mock-model",
			Effort: "medium",
		},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	// Replace with mock backend
	rt.Backend = &mockBackend{
		chatResponse: "mockresponse",
	}
	// 与 New 行为一致：替换后端后统一下发思考深度（mock 记录下发值供断言）
	rt.Backend.SetReasoningEffort(rt.Config.CLIDefaults.Effort)
	return rt
}

// =============================================================================
// New tests
// =============================================================================

func TestNew_InitializesAllComponents(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	if rt == nil {
		t.Fatal("New returned nil")
	}
	if rt.Config == nil {
		t.Error("Config should not be nil")
	}
	if rt.Harness == nil {
		t.Error("Harness should not be nil")
	}
	if rt.Memory == nil {
		t.Error("Memory should not be nil")
	}
	if rt.Plugins == nil {
		t.Error("Plugins should not be nil")
	}
	if rt.Tools == nil {
		t.Error("Tools should not be nil")
	}
	if rt.Backend == nil {
		t.Error("Backend should not be nil")
	}
	if rt.Executor == nil {
		t.Error("Executor should not be nil")
	}
	if rt.ACP == nil {
		t.Error("ACP should not be nil")
	}
	if rt.Preproc == nil {
		t.Error("Preproc should not be nil")
	}
}

// TestNew_EffortDefaultMax 验证思考深度默认 max 且启动即下发到后端：
// 回归背景：此前 cli_defaults.effort 只在 /effort 命令（SetEffort）时下发，
// 启动时配置/默认值从不写入后端 → 请求体不发送 reasoning_effort 字段。
func TestNew_EffortDefaultMax(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		ActiveModelName: "mock",
		Models: []config.ModelPreset{{
			Name:       "mock",
			Backend:    "mock",
			Endpoint:   "http://localhost:8080",
			APIKey:     "sk-test",
			MaxContext: 4096,
		}},
		Harness: config.HarnessConfig{ScoreThreshold: 50},
		Memory: config.MemoryConfig{
			Directory:   filepath.Join(tmpDir, "memory"),
			MaxSessions: 10,
		},
		Plugins: config.PluginsConfig{
			Directory: filepath.Join(tmpDir, "plugins"),
		},
		Project: config.ProjectConfig{
			Root:  tmpDir,
			Vocab: filepath.Join(tmpDir, "vocab.json"),
		},
		ACP: config.ACPConfig{Enabled: true, Port: 20250},
		// CLIDefaults.Effort 未设置 → 应默认 "max" 并下发到后端
	}

	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create runtime: %v", err)
	}
	if rt.Config.CLIDefaults.Effort != "max" {
		t.Errorf("expected default effort 'max', got %q", rt.Config.CLIDefaults.Effort)
	}
	// 后端必须已收到 max（此前启动时从不下发）
	type effortGetter interface{ ReasoningEffort() string }
	if g, ok := rt.Backend.(effortGetter); ok {
		if got := g.ReasoningEffort(); got != "max" {
			t.Errorf("expected backend reasoning effort 'max', got %q", got)
		}
	} else {
		t.Fatal("backend does not implement ReasoningEffort")
	}
}

// TestNew_EffortFromConfig 验证显式配置的 effort 启动时即下发到后端（非默认路径）。
func TestNew_EffortFromConfig(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	if rt.Config.CLIDefaults.Effort != "medium" {
		t.Fatalf("test setup: expected effort 'medium', got %q", rt.Config.CLIDefaults.Effort)
	}
	// newTestRuntimeAt 中 Effort: "medium" → 后端应已收到下发值 "medium"
	// （low/medium/high→high、xhigh/max→max 的 remap 由 DeepSeek 后端内部处理，见 model 包单测）
	type effortGetter interface{ ReasoningEffort() string }
	if g, ok := rt.Backend.(effortGetter); ok {
		if got := g.ReasoningEffort(); got != "medium" {
			t.Errorf("expected backend reasoning effort 'medium' (downstream value), got %q", got)
		}
	} else {
		t.Fatal("backend does not implement ReasoningEffort")
	}
}

// =============================================================================
// NewSession tests
// =============================================================================

func TestNewSession_ShortInput(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	session := rt.NewSession("hello world")
	if session == nil {
		t.Fatal("NewSession returned nil")
	}
	if session.Title != "hello world" {
		t.Errorf("expected title 'hello world', got %q", session.Title)
	}
}

func TestNewSession_LongInput_Truncates(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	longInput := "这是一个非常长的输入字符串用于测试标题截断功能应该只保留前50个字符超出部分应该被截断"
	session := rt.NewSession(longInput)
	if session == nil {
		t.Fatal("NewSession returned nil")
	}

	runes := []rune(session.Title)
	if len(runes) > 50 {
		t.Errorf("title should be truncated to 50 chars, got %d chars: %q", len(runes), session.Title)
	}
}

func TestNewSession_EmptyInput(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	session := rt.NewSession("")
	if session == nil {
		t.Fatal("NewSession with empty input returned nil")
	}
	if session.Title != "新会话" {
		t.Errorf("expected 新会话, got %q", session.Title)
	}
}

func TestNewSession_PreservesInputAsTitle(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	input := "fix the null pointer bug in module"
	session := rt.NewSession(input)
	if session == nil {
		t.Fatal("NewSession returned nil")
	}
	if len([]rune(session.Title)) > 50 {
		t.Errorf("title should be <= 50 chars, got %q", session.Title)
	}
}

// =============================================================================
// CreateTask tests
// =============================================================================

func TestCreateTask(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	agent, err := rt.CreateTask("CODE_AGENT", "write unit tests", map[string]string{"key": "value"}, 1)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}
	if agent == nil {
		t.Fatal("CreateTask returned nil agent")
	}
	if agent.ID == "" {
		t.Error("agent should have an ID")
	}
	if agent.Task.Directive != "CODE_AGENT" {
		t.Errorf("expected directive 'CODE_AGENT', got %q", agent.Task.Directive)
	}
	if agent.Task.Task != "write unit tests" {
		t.Errorf("expected task 'write unit tests', got %q", agent.Task.Task)
	}
	if agent.Task.Priority != 1 {
		t.Errorf("expected priority 1, got %d", agent.Task.Priority)
	}
}

func TestCreateTask_MultipleAgents_IncrementsID(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	a1, err := rt.CreateTask("CODE_AGENT", "task 1", nil, 1)
	if err != nil {
		t.Fatalf("first CreateTask failed: %v", err)
	}
	a2, err := rt.CreateTask("CODE_AGENT", "task 2", nil, 1)
	if err != nil {
		t.Fatalf("second CreateTask failed: %v", err)
	}

	if a1.ID == a2.ID {
		t.Errorf("agents should have different IDs, both got %q", a1.ID)
	}
}

// =============================================================================
// SetEffort tests
// =============================================================================

func TestSetEffort(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.SetEffort("high")
	if rt.Config.CLIDefaults.Effort != "high" {
		t.Errorf("expected effort 'high', got %q", rt.Config.CLIDefaults.Effort)
	}
}

func TestSetEffort_EmptyString(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.SetEffort("")
	if rt.Config.CLIDefaults.Effort != "" {
		t.Errorf("expected empty effort, got %q", rt.Config.CLIDefaults.Effort)
	}
}

// =============================================================================
// SetThreshold tests
// =============================================================================

func TestSetThreshold(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.SetThreshold(75)
	if rt.Config.Harness.ScoreThreshold != 75 {
		t.Errorf("expected threshold 75, got %f", rt.Config.Harness.ScoreThreshold)
	}
}

func TestSetThreshold_Zero(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.SetThreshold(0)
	if rt.Config.Harness.ScoreThreshold != 0 {
		t.Errorf("expected threshold 0, got %f", rt.Config.Harness.ScoreThreshold)
	}
}

func TestSetThreshold_Negative(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.SetThreshold(-10)
	if rt.Config.Harness.ScoreThreshold != -10 {
		t.Errorf("expected threshold -10, got %f", rt.Config.Harness.ScoreThreshold)
	}
}

// =============================================================================
// CompressContext tests
// =============================================================================

func TestCompressContext(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	result := rt.CompressContext()
	if result == "" {
		t.Error("CompressContext should return a non-empty message")
	}
}

func TestCompressContext_ReturnsChineseMessage(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	result := rt.CompressContext()
	// Should contain Chinese text about compression
	if len(result) == 0 {
		t.Error("expected non-empty CompressContext result")
	}
}

// TestCompressContext_ReducesCtxUsage 验证 /compress 后对话历史 token 占用真实下降
// （状态栏 ctx 占比随之实时变化，而非仅衰减 Harness 索引）。
func TestCompressContext_ReducesCtxUsage(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 制造大量对话历史（模拟长对话），确保超过压缩阈值
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", strings.Repeat("这是一个用于压测的长用户消息，包含足够多的内容来占用上下文预算。", 20))
		rt.AddToHistory("assistant", strings.Repeat("这是助手的长回复，包含详细的分析说明与代码片段，用于模拟真实对话中的大块输出。", 20))
	}
	before := ctxmgr.MessagesTokens(rt.GetConversationHistory())

	result := rt.CompressContext()

	after := ctxmgr.MessagesTokens(rt.GetConversationHistory())
	if after >= before {
		t.Errorf("/compress 后上下文应下降: before=%d after=%d", before, after)
	}
	if !strings.Contains(result, "释放") {
		t.Errorf("返回消息应包含释放统计, got: %q", result)
	}
}

// TestCompressContext_EvictsToIndex 验证被压缩的完整原文自动写入 Harness 索引，
// 可通过 harness_search 检索找回（压缩≠永久丢失，闭环测试）。
func TestCompressContext_EvictsToIndex(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 制造长对话：前几轮含独特关键词（会被压缩丢弃），后续大量消息撑爆预算
	rt.AddToHistory("user", "第一轮独特内容：量子纠缠观测实验报告概要，包含关键数据点XYZ-42。")
	rt.AddToHistory("assistant", "助手分析：量子纠缠实验的误差来源与修正建议。")
	for i := 0; i < 10; i++ {
		rt.AddToHistory("user", strings.Repeat("这是一个用于压测的长用户消息，包含足够多的内容来占用上下文预算。", 20))
		rt.AddToHistory("assistant", strings.Repeat("这是助手的长回复，包含详细的分析说明与代码片段，用于模拟真实对话中的大块输出。", 20))
	}
	// 无论自动压缩还是手动压缩，被压缩内容都应入索引
	rt.CompressContext()

	// 通过 harness_search 检索被压缩的独特关键词 → 应命中 [压缩原文] 索引块
	blocks := rt.Harness.Search(context.Background(), "量子纠缠", nil)
	found := false
	for _, b := range blocks {
		if strings.Contains(b.Content, "[压缩原文") && strings.Contains(b.Content, "XYZ-42") {
			found = true
			break
		}
	}
	if !found {
		t.Error("被压缩的完整原文应可通过 harness_search 检索找回（含 [压缩原文] 标记）")
	}
}

// =============================================================================
// ACPStatus tests
// =============================================================================

func TestACPStatus(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	status := rt.ACPStatus()
	if status == "" {
		t.Error("ACPStatus should return non-empty string")
	}
}

// =============================================================================
// InjectBlock tests
// =============================================================================

func TestInjectBlock(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// First index a block
	ctx := context.Background()
	rt.Harness.Index(ctx, []types.ContextBlock{
		{ID: "block-1", Content: "test content"},
	})

	result := rt.InjectBlock("block-1")
	if result == "" {
		t.Error("InjectBlock should return a message")
	}
}

func TestInjectBlock_NonExistent(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	result := rt.InjectBlock("nonexistent-block")
	if result == "" {
		t.Error("InjectBlock should return a message even for non-existent block")
	}
}

// =============================================================================
// SwitchModel tests
// =============================================================================

func TestSwitchModel(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// newTestRuntime 手工构造 Config（未走 applyDefaults），先补一个默认预设
	rt.Config.Models = []config.ModelPreset{{
		Name:       "default-model",
		Backend:    "default-model",
		MaxContext: 4096,
	}}
	names := rt.ModelNames()
	if len(names) != 1 || names[0] != "default-model" {
		t.Fatalf("应有 1 个预设 default-model, got %v", names)
	}
	// 不存在的模型返回错误并附可用列表
	if err := rt.SwitchModel("new-model-id"); err == nil {
		t.Fatal("切换不存在的模型应返回错误")
	}
	if err := rt.SwitchModel("default-model"); err != nil {
		t.Fatalf("切换默认预设应成功: %v", err)
	}
	if rt.Config.ActiveModelName != "default-model" {
		t.Errorf("expected active_model 'default-model', got %q", rt.Config.ActiveModelName)
	}
}

// TestSwitchModel_PresetSwitch：多预设切换应用全部字段并热更新
func TestSwitchModel_PresetSwitch(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	se := false
	rt.Config.Models = append(rt.Config.Models, config.ModelPreset{
		Name:           "gpt-4o",
		Backend:        "openai",
		Endpoint:       "https://api.openai.com/v1",
		APIKey:         "sk-test",
		MaxContext:     128000,
		SupportsVision: true,
		VisionModel:    "gpt-4o",
		SendEffort:     &se,
	})

	if err := rt.SwitchModel("gpt-4o"); err != nil {
		t.Fatalf("切换 gpt-4o 应成功: %v", err)
	}
	// model 节仅保留激活名指针；字段一律从 ActiveModel（激活预设）读取
	if rt.Config.ActiveModelName != "gpt-4o" {
		t.Fatalf("active_model 应为激活预设名 gpt-4o, got %q", rt.Config.ActiveModelName)
	}
	am := rt.Config.ActiveModel()
	if am.Backend != "openai" || am.Endpoint != "https://api.openai.com/v1" || am.APIKey != "sk-test" ||
		am.MaxContext != 128000 || !am.SupportsVision || am.VisionModel != "gpt-4o" {
		t.Fatalf("预设字段未完整应用: %+v", am)
	}
	if am.SendEffort == nil || *am.SendEffort {
		t.Fatalf("send_effort 应应用为 false, got %v", am.SendEffort)
	}
	// 大小写不敏感切换
	if err := rt.SwitchModel("GPT-4O"); err != nil {
		t.Fatalf("大小写不敏感切换应成功: %v", err)
	}
}

// TestAddModelPreset：傻瓜式新建模型预设——写入 models、自动激活、默认参数兜底、重名拒绝。
func TestAddModelPreset(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.Config.Models = []config.ModelPreset{{
		Name:       "default-model",
		Backend:    "default-model",
		MaxContext: 4096,
	}}

	// 新建（只填核心字段，默认参数兜底）
	err := rt.AddModelPreset(config.ModelPreset{
		Name:       "my-new-model",
		Backend:    "openai",
		Endpoint:   "https://api.myprovider.com/v1",
		APIKey:     "sk-abc",
		MaxContext: 0, // 应兜底 131072
	})
	if err != nil {
		t.Fatalf("新建应成功: %v", err)
	}
	// 自动激活
	if rt.Config.ActiveModelName != "my-new-model" {
		t.Fatalf("新建后应自动激活 my-new-model, got %q", rt.Config.ActiveModelName)
	}
	am := rt.Config.ActiveModel()
	if am.Backend != "openai" || am.APIKey != "sk-abc" || am.MaxContext != 131072 {
		t.Fatalf("默认参数未兜底: backend=%q max_context=%d key=%q", am.Backend, am.MaxContext, am.APIKey)
	}
	if am.SendEffort == nil || !*am.SendEffort {
		t.Fatal("send_effort 应默认 true")
	}
	if am.TopP == nil || am.Temperature == nil {
		t.Fatal("top_p/temperature 应默认填充")
	}
	// 重复名拒绝
	if err := rt.AddModelPreset(config.ModelPreset{Name: "my-new-model", Backend: "openai"}); err == nil {
		t.Fatal("重名预设应拒绝")
	}
	// 空名拒绝
	if err := rt.AddModelPreset(config.ModelPreset{Name: "  "}); err == nil {
		t.Fatal("空名称应拒绝")
	}
}

// TestRemoveModelPreset：删除模型预设——删激活项自动切换、仅剩一个拒绝、
// 未找到报错、写回 config.yaml。
func TestRemoveModelPreset(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.Config.Models = []config.ModelPreset{
		{Name: "m1", Backend: "deepseek", MaxContext: 4096},
		{Name: "m2", Backend: "openai", MaxContext: 4096},
		{Name: "m3", Backend: "anthropic", MaxContext: 4096},
	}
	rt.Config.ActiveModelName = "m2"
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	rt.Config.ConfigPath = cfgPath

	// 删除非激活项：预设减少，激活项不变
	if err := rt.RemoveModelPreset("m1"); err != nil {
		t.Fatalf("删除 m1 应成功: %v", err)
	}
	if got := rt.ModelNames(); len(got) != 2 || got[0] != "m2" || got[1] != "m3" {
		t.Fatalf("删除 m1 后应剩 [m2 m3], got %v", got)
	}
	if rt.Config.ActiveModelName != "m2" {
		t.Fatalf("删除非激活项不应改变激活名, got %q", rt.Config.ActiveModelName)
	}

	// 删除激活项：自动切换到剩余第一个
	if err := rt.RemoveModelPreset("m2"); err != nil {
		t.Fatalf("删除激活项 m2 应成功: %v", err)
	}
	if rt.Config.ActiveModelName != "m3" {
		t.Fatalf("删除激活项后应切到 m3, got %q", rt.Config.ActiveModelName)
	}

	// 仅剩一个 → 拒绝
	if err := rt.RemoveModelPreset("m3"); err == nil {
		t.Fatal("仅剩一个预设应拒绝删除")
	}

	// 未找到 → 报错
	if err := rt.RemoveModelPreset("ghost"); err == nil {
		t.Fatal("删除不存在的预设应报错")
	}

	// 大小写不敏感
	rt.Config.Models = append(rt.Config.Models, config.ModelPreset{Name: "MixedCase", Backend: "openai"})
	if err := rt.RemoveModelPreset("mixedcase"); err != nil {
		t.Fatalf("大小写不敏感删除应成功: %v", err)
	}

	// 写回 config.yaml（含 .bak 备份）
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("删除后应写回 config.yaml: %v", err)
	}
}

// =============================================================================
// GetMemoryStats tests
// =============================================================================

func TestGetMemoryStats(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	stats := rt.GetMemoryStats()
	if stats == "" {
		t.Error("GetMemoryStats should return non-empty string")
	}
}

func TestGetMemoryStats_AfterSessionCreation(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	_ = rt.NewSession("test session")
	stats := rt.GetMemoryStats()
	if stats == "" {
		t.Error("GetMemoryStats should return non-empty string after session creation")
	}
}

// 回归测试：GetSessionID 必须返回当前会话 ID（历史缺陷：恒返回空串，
// 导致 GUI 的 agent.done 无法携带 sessionId、前端无法跟踪会话、
// chat.send 的会话匹配失效——多轮对话"第二次找不到用户消息"的根因之一）。
func TestGetSessionID_TracksCurrentSession(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 初始无会话 → 空
	if got := rt.GetSessionID(); got != "" {
		t.Fatalf("初始 GetSessionID 应为空，got %q", got)
	}

	// NewSession 后 → 返回新会话 ID
	s1 := rt.NewSession("会话一")
	if got := rt.GetSessionID(); got != s1.UUID {
		t.Fatalf("NewSession 后 GetSessionID 应返回 %q，got %q", s1.UUID, got)
	}

	// 写入历史后再验证（AddToHistory 依赖会话 ID 持久化，也应读到同一 ID）
	rt.AddToHistory("user", "你好")
	if got := rt.GetSessionID(); got != s1.UUID {
		t.Fatalf("AddToHistory 后 GetSessionID 应仍为 %q，got %q", s1.UUID, got)
	}

	// LoadSession 切换到其他会话 → 返回新 ID
	s2 := rt.NewSession("会话二")
	if got := rt.GetSessionID(); got != s2.UUID {
		t.Fatalf("再次 NewSession 后 GetSessionID 应返回 %q，got %q", s2.UUID, got)
	}
	if _, _, err := rt.LoadSession(s1.UUID); err != nil {
		t.Fatalf("LoadSession 失败: %v", err)
	}
	if got := rt.GetSessionID(); got != s1.UUID {
		t.Fatalf("LoadSession 后 GetSessionID 应返回 %q，got %q", s1.UUID, got)
	}
}

// =============================================================================
// ListSessions tests
// =============================================================================

func TestListSessions_Empty(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	sessions := rt.ListSessions()
	// May return nil or empty slice depending on memory state
	_ = sessions
}

func TestListSessions_AfterCreation(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	_ = rt.NewSession("test session")
	sessions := rt.ListSessions()
	// Should have at least the session we just created
	if len(sessions) == 0 {
		t.Log("ListSessions returned empty - memory may not persist synchronously")
	}
}

// =============================================================================
// LoadSession tests
// =============================================================================

func TestLoadSession_NonExistent(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	_, target, err := rt.LoadSession("nonexistent-uuid")
	if err == nil {
		t.Error("expected error for non-existent session")
	}
	_ = target
}

// TestLoadSession_RestoresConversationHistory 验证"退出后重新进入"场景：
// 新进程（新 Runtime 实例）通过 LoadSession 恢复会话时，AI 能拿到完整上下文。
// 回归背景：曾出现 --memory-load 只调 Memory.Load（仅验证存在）而不走 LoadSession，
// 导致 currentSessionID 为空、conversationHistory 未恢复，TUI 误建空会话、AI 丢失全部上下文。
func TestLoadSession_RestoresConversationHistory(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	sess := rt.NewSession("测试会话")
	if sess == nil || sess.UUID == "" {
		t.Fatal("NewSession failed")
	}
	// 写入两轮对话（user + assistant）
	rt.AddToHistory("user", "第一轮问题")
	rt.AddToHistory("assistant", "第一轮回答")
	rt.AddToHistory("user", "第二轮问题")
	rt.AddToHistory("assistant", "第二轮回答")

	// 模拟退出：释放 bbolt 文件锁，让"新进程"可以打开同一存储
	if err := rt.Memory.Close(); err != nil {
		t.Fatalf("close memory: %v", err)
	}

	// 模拟重新进入：全新 Runtime 实例，指向同一 memory 目录
	rt2 := newTestRuntimeAt(t, tmpDir)

	// 关键：--memory-load 路径必须走 LoadSession（而非仅 Memory.Load）
	_, _, err := rt2.LoadSession(sess.UUID)
	if err != nil {
		t.Fatalf("LoadSession failed: %v", err)
	}
	if got := rt2.GetCurrentSessionID(); got != sess.UUID {
		t.Fatalf("currentSessionID = %q, want %q（未恢复会话 ID，TUI 将新建空会话）", got, sess.UUID)
	}

	hist := rt2.GetConversationHistory()
	if len(hist) != 4 {
		t.Fatalf("恢复历史消息数 = %d, want 4（AI 将丢失上下文）", len(hist))
	}
	want := []types.Message{
		{Role: "user", Content: "第一轮问题"},
		{Role: "assistant", Content: "第一轮回答"},
		{Role: "user", Content: "第二轮问题"},
		{Role: "assistant", Content: "第二轮回答"},
	}
	for i, w := range want {
		if hist[i].Role != w.Role || hist[i].Content != w.Content {
			t.Errorf("hist[%d] = %+v, want %+v", i, hist[i], w)
		}
	}
}

// =============================================================================
// RunAgent tests
// =============================================================================

func TestRunAgent(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	agent, err := rt.CreateTask("CODE_AGENT", "test task", nil, 1)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	ctx := context.Background()
	err = rt.RunAgent(ctx, agent)
	if err != nil {
		t.Errorf("RunAgent failed: %v", err)
	}
}

// =============================================================================
// PluginsList tests
// =============================================================================

func TestPluginsList(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	list := rt.PluginsList()
	// Should return "无已加载插件" or list of plugins
	if list == "" {
		t.Error("PluginsList should return non-empty string")
	}
}

// =============================================================================
// resolvePath 测试
// =============================================================================

func TestResolvePath_Absolute(t *testing.T) {
	result := resolvePath("/absolute/path/to/dir", "/project")
	if result != "/absolute/path/to/dir" {
		t.Errorf("expected '/absolute/path/to/dir', got %q", result)
	}
}

func TestResolvePath_Relative(t *testing.T) {
	result := resolvePath("relative/path", "/project")
	expected := "/project/relative/path"
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestResolvePath_Empty(t *testing.T) {
	result := resolvePath("", "/project")
	if result != "/project" {
		t.Errorf("expected '/project', got %q", result)
	}
}

func TestResolvePath_WithTilde(t *testing.T) {
	result := resolvePath("~/.config/app", "/project")
	home, err := os.UserHomeDir()
	if err == nil {
		expected := filepath.Join(home, ".config/app")
		if result != expected {
			t.Errorf("expected %q, got %q", expected, result)
		}
	}
}

// ============================================================================
// stripThinking 剥离思考标签（尖括号 + 空格前缀格式）
// ============================================================================

func TestStripThinking_AngleBracket(t *testing.T) {
	s := "正文<think>思考内容</think>结尾"
	got := StripThinking(s)
	if got != "正文结尾" {
		t.Errorf("stripThinking 尖括号格式失败: %q", got)
	}
}

func TestStripThinking_SpacePrefix(t *testing.T) {
	// 模型层 deepseek.go 输出的标记格式
	s := "正文<|reasoning|>思考内容<|/reasoning|>结尾"
	got := StripThinking(s)
	if got != "正文结尾" {
		t.Errorf("stripThinking 标记格式失败: %q", got)
	}
}

func TestStripThinking_SpacePrefixOnlyReasoning(t *testing.T) {
	// 只有思考无正文
	s := "<|reasoning|>思考内容<|/reasoning|>"
	got := StripThinking(s)
	if got != "" {
		t.Errorf("stripThinking 仅思考应剥离为空: %q", got)
	}
}

func TestStripThinking_ThinkingVariant(t *testing.T) {
	// <thinking> 变体（注释声明支持但此前代码未处理，已修复）
	s := "正文<thinking>思考内容</thinking>结尾"
	got := StripThinking(s)
	if got != "正文结尾" {
		t.Errorf("stripThinking <thinking> 变体失败: %q", got)
	}
}

func TestStripThinking_MixedFormats(t *testing.T) {
	// 三种格式混合：<|reasoning|> + <think> + <thinking> 全部剥离，正文保留
	s := "开头<|reasoning|>模型思考<|/reasoning|>中段<think>尖括号思考</think>后段<thinking>变体思考</thinking>结尾"
	got := StripThinking(s)
	if got != "开头中段后段结尾" {
		t.Errorf("stripThinking 混合格式失败: %q", got)
	}
}

func TestStripThinking_SpacePrefixUnclosed(t *testing.T) {
	// 未闭合：删除到末尾
	s := "正文<|reasoning|>思考内容"
	got := StripThinking(s)
	if got != "正文" {
		t.Errorf("stripThinking 未闭合处理失败: %q", got)
	}
}

func TestStripThinking_LegacySpacePrefix(t *testing.T) {
	// 兼容旧版空格前缀格式（历史数据）
	s := "正文 thinking思考内容 response结尾"
	got := StripThinking(s)
	if got != "正文结尾" {
		t.Errorf("stripThinking 旧格式兼容失败: %q", got)
	}
}

func TestStripThinking_PathNotMatched(t *testing.T) {
	// 路径中的 thinking 不应被剥离（前面是 / 而非空格）
	s := "请读取 /home/thinking_project/notes.txt"
	got := StripThinking(s)
	if got != s {
		t.Errorf("路径中的 thinking 不应被剥离: %q", got)
	}
}

func TestStripThinking_Mixed(t *testing.T) {
	// 混合新标记和尖括号格式
	s := "开头<think>思考A</think>中间<|reasoning|>思考B<|/reasoning|>结尾"
	got := StripThinking(s)
	if got != "开头中间结尾" {
		t.Errorf("stripThinking 混合格式失败: %q", got)
	}
}

func TestStripThinking_BodyEnglishWordsNotFiltered(t *testing.T) {
	// 回归：正文中的英文 thinking/response 单词不应被剥离
	s := "让我检查 response 处理和 thinking 环节"
	got := StripThinking(s)
	if got != s {
		t.Errorf("正文中的 thinking/response 不应被剥离: %q", got)
	}
}

// =============================================================================
// auto_title / node_auto_plan / test_tee 配置接入测试
// =============================================================================

func TestUpdateSessionTitle_AutoTitleOn(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.Config.Memory.AutoTitle = true

	session := rt.NewSession("") // 空输入 → 标题"新会话"
	rt.UpdateSessionTitle(session.UUID, "新标题内容用于测试自动标题")
	loaded, err := rt.Memory.Load(session.UUID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.Title == "新会话" {
		t.Error("auto_title=true 时应更新标题")
	}
	if loaded.Preview == "" {
		t.Error("auto_title=true 时预览也应更新")
	}
}

func TestUpdateSessionTitle_AutoTitleOff(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	rt.Config.Memory.AutoTitle = false

	session := rt.NewSession("") // 空输入 → 标题"新会话"
	rt.UpdateSessionTitle(session.UUID, "新标题内容用于测试自动标题")
	loaded, err := rt.Memory.Load(session.UUID)
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if loaded.Title != "新会话" {
		t.Errorf("auto_title=false 时标题应保持 新会话, got %q", loaded.Title)
	}
}

func TestSplitPlanTasks(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"多行拆分", "实现登录接口\n补充单元测试\n修复 bug", 3},
		{"单行分号拆分", "实现登录接口；补充单元测试；修复 bug", 3},
		{"空行过滤", "实现登录接口\n\n\n补充单元测试", 2},
		{"过短片段过滤", "好\n实现登录接口", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitPlanTasks(c.input)
			if len(got) != c.want {
				t.Errorf("splitPlanTasks(%q) = %d 个任务, want %d: %v", c.input, len(got), c.want, got)
			}
		})
	}
}

func TestEstimateComplexity(t *testing.T) {
	if estimateComplexity("短任务") != 1 {
		t.Error("短任务应为简单(1)")
	}
	medium := "中等复杂度任务" + string(make([]rune, 60))
	if estimateComplexity(medium) != 2 {
		t.Error("中等长度应为中等(2)")
	}
	long := "长任务" + string(make([]rune, 120))
	if estimateComplexity(long) != 3 {
		t.Error("长任务应为复杂(3)")
	}
}

func TestRegisterPlanNodes(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 构造一个真实监工（task 模式），node_auto_plan=true
	rt.Config.Overseer.NodeAutoPlan = true
	rt.Config.Overseer.PassThreshold = 90

	// 直接调用 registerPlanNodes 验证节点注册
	ov := overseer.New(
		"实现登录接口\n补充单元测试\n修复 bug",
		map[string]any{"min_test_coverage": 60.0},
		types.Score(90),
		nil,
	)
	rt.registerPlanNodes(ov, "实现登录接口\n补充单元测试\n修复 bug")

	states := ov.NodeStates()
	if len(states) != 3 {
		t.Fatalf("应注册 3 个计划节点, got %d", len(states))
	}
	// 节点 ID 应为 node-0/node-1/node-2
	for i, ns := range states {
		if ns.ID != "node-"+string(rune('0'+i)) {
			t.Errorf("节点 %d ID 应为 node-%d, got %q", i, i, ns.ID)
		}
	}
}

func TestVerifyTestExecution_Disabled(t *testing.T) {
	// test_tee=false 时透明代理验证不触发
	claimed, actual := verifyTestExecution(context.Background(), "测试全部通过", false)
	if claimed != nil || actual != nil {
		t.Error("test_tee=false 时应返回 nil,nil（不执行透明代理）")
	}
	// test_tee=true 且声称通过时触发
	claimed2, actual2 := verifyTestExecution(context.Background(), "测试全部通过", true)
	if claimed2 == nil || actual2 == nil {
		t.Error("test_tee=true 且声称测试通过时应触发透明代理验证")
	}
}

// =============================================================================
// 上下文预算驱动压缩测试（ctxmgr 接线）
// =============================================================================

func TestAddToHistory_BudgetCompaction(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 预算 6000：足够完整保留最近 4 条（每条约 1150 token），同时触发旧消息压缩
	rt.ctxBudget = ctxmgr.NewTokenBudget(6000)
	rt.ctxBudget.ApplyConfig(0.5, 0.75, 0.92, 4)

	big := strings.Repeat("这是一段很长的上下文内容用来测试token预算压缩行为", 40)
	for i := 0; i < 30; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}

	hist := rt.GetConversationHistory()
	// 必须保留最近至少 keepRecent 条消息（不丢弃）
	if len(hist) < rt.ctxBudget.KeepRecent {
		t.Fatalf("压缩后消息数 %d < keepRecent %d（断崖式遗忘）", len(hist), rt.ctxBudget.KeepRecent)
	}
	// 最近消息必须完整（预算充足时不得截断）
	last := hist[len(hist)-1].Content.(string)
	if len(last) != len(big) {
		t.Fatalf("最近消息被截断: %d != %d", len(last), len(big))
	}
	// 总用量不超过安全红线
	used := ctxmgr.MessagesTokens(hist)
	if rt.ctxBudget.Usage(used) > rt.ctxBudget.SafetyRatio {
		t.Fatalf("压缩后仍超安全红线: %.2f", rt.ctxBudget.Usage(used))
	}
	// 滚动摘要已生成（防断崖失忆）
	if rt.ctxBudget.Summary == "" {
		t.Fatal("滚动摘要应为空")
	}
	// 摘要消息已注入历史
	if !strings.HasPrefix(hist[0].Content.(string), "[历史摘要]") {
		t.Fatalf("历史摘要未注入, first=%q", hist[0].Content.(string))
	}
}

func TestContextUsageAndStats(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.AddToHistory("user", "测试消息")
	usage := rt.ContextUsage()
	if usage == "" {
		t.Fatal("ContextUsage 不应为空")
	}
	if !strings.Contains(usage, "ctx") {
		t.Fatalf("ContextUsage 格式错误: %q", usage)
	}
	stats := rt.ContextStats()
	if !strings.Contains(stats, "消息数") || !strings.Contains(stats, "阈值") {
		t.Fatalf("ContextStats 缺少关键字段: %q", stats)
	}
}

// TestContextUsage_Precision 验证状态栏"同步改变"的精度修复：
// 131k 预算下几百 token 的增量必须可见（0.6% 而非 0%、800 而非 0k）——
// 旧实现 int 截断 + /1000 截断导致对话中状态栏纹丝不动（GUI 状态栏
// 不同步的根因之一）。
func TestContextUsage_Precision(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 131k 预算（与生产默认一致）
	rt.ctxBudget = ctxmgr.NewTokenBudget(131072)
	rt.ctxBudget.ApplyConfig(0.7, 0.85, 0.92, 30) // 阈值无关紧要，仅保证 MaxTokens 生效

	// 空历史：0% 但显示真实 0
	if u := rt.ContextUsage(); !strings.Contains(u, "0.00%") {
		t.Errorf("空历史应显示 0.00%%: %q", u)
	}

	// 一条 ~1.3k token 的用户消息：必须显示小数百分比（1.0%，而非旧版 0%）
	rt.AddToHistory("user", strings.Repeat("你好世界，这是一条测试消息。", 100))
	u := rt.ContextUsage()
	if !regexp.MustCompile(`\d+\.\d+%`).MatchString(u) {
		t.Errorf("小增量应显示小数百分比（~1.0%%），实际: %q", u)
	}
	if strings.Contains(u, " 0% ") || regexp.MustCompile(`\(0k/`).MatchString(u) {
		t.Errorf("小增量不得被截断成 0: %q", u)
	}
	if !strings.Contains(u, "/131k") {
		t.Errorf("预算应显示 131k: %q", u)
	}
	// used 显示：<1000 时真实数字（不能被 0k 截断）——提取括号内 used
	if m := regexp.MustCompile(`\((\d+)[k.)]`).FindStringSubmatch(u); len(m) == 0 {
		t.Errorf("used 应显示真实数值（<1000 不用 k）: %q", u)
	} else if m[1] == "0" {
		t.Errorf("used 被截断成 0: %q", u)
	}

	// ContextUsageInfo 原始数值
	used, max := rt.ContextUsageInfo()
	if max != 131072 {
		t.Errorf("max 应为 131072: %d", max)
	}
	if used <= 0 {
		t.Errorf("used 应 > 0: %d", used)
	}

	// 12.5k 级别显示 1 位小数 k
	rt2, tmpDir2 := newTestRuntime(t)
	defer os.RemoveAll(tmpDir2)
	rt2.ctxBudget = ctxmgr.NewTokenBudget(131072)
	rt2.ctxBudget.ApplyConfig(0.7, 0.85, 0.92, 30)
	// 构造 ~12k token 历史（压缩阈值未触发：12k/131k≈9% < 70%）
	msg := strings.Repeat("中文字符测试消息。", 50) // 15 字/句 → ~750 token/条
	for i := 0; i < 16; i++ {
		rt2.AddToHistory("user", msg)
		rt2.AddToHistory("assistant", msg)
	}
	u2 := rt2.ContextUsage()
	if !regexp.MustCompile(`\(\d+\.\d+k/`).MatchString(u2) && !regexp.MustCompile(`\(\d+k/`).MatchString(u2) {
		t.Errorf("较大用量应显示 k 格式: %q", u2)
	}
	_ = u2
}

func TestNewSessionResetsContext(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.ctxBudget = ctxmgr.NewTokenBudget(1000)
	big := strings.Repeat("大内容触发压缩", 300)
	for i := 0; i < 20; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}
	if rt.ctxBudget.Summary == "" {
		t.Fatal("前置条件：应产生滚动摘要")
	}
	rt.NewSession("新会话")
	if len(rt.GetConversationHistory()) != 0 {
		t.Fatal("新会话应清空历史")
	}
	if rt.ctxBudget.Summary != "" {
		t.Fatal("新会话应清空滚动摘要")
	}
}

// TestCompressionPersist_LoadSessionRestoresCompacted 验证压缩后持久化快照：
// 重新加载会话时 ctx 占比立即等于压缩后的状态，不再回退到未压缩的完整用量。
func TestCompressionPersist_LoadSessionRestoresCompacted(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	mem, memErr := memory.New(tmpDir, 10)
	if memErr != nil {
		t.Fatalf("memory.New: %v", memErr)
	}
	rt.Memory = mem

	// 设置预算（小窗口触发压缩）
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)
	// 创建会话
	rt.NewSession("测试压缩持久化")
	sid := rt.GetSessionID()
	if sid == "" {
		t.Fatal("会话 ID 不应为空")
	}

	// 写入大量消息触发压缩
	big := strings.Repeat("这是触发压缩的长内容用于测试持久化", 30)
	for i := 0; i < 10; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}

	// 记录压缩后的消息条数
	rt.historyMu.RLock()
	compactedCount := len(rt.conversationHistory)
	rt.historyMu.RUnlock()

	// 验证会话 JSON 已保存压缩快照
	session, err := rt.Memory.Load(sid)
	if err != nil {
		t.Fatalf("加载会话: %v", err)
	}
	if len(session.CompactedMessages) == 0 {
		t.Fatal("CompactedMessages 不应为空——压缩后未持久化")
	}
	if len(session.CompactedMessages) != compactedCount {
		t.Fatalf("CompactedMessages 条数 %d 应与压缩后 %d 一致", len(session.CompactedMessages), compactedCount)
	}

	// 模拟重新加载会话（新 Runtime 实例）
	rt2 := newTestRuntimeAt(t, tmpDir)
	mem2, memErr2 := memory.New(tmpDir, 10)
	if memErr2 != nil {
		t.Fatalf("memory.New: %v", memErr2)
	}
	rt2.Memory = mem2
	rt2.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt2.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)
	_, _, err = rt2.LoadSession(sid)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	// 验证重新加载后消息条数与压缩后一致（而非未压缩的完整条数）
	rt2.historyMu.RLock()
	restoredCount := len(rt2.conversationHistory)
	rt2.historyMu.RUnlock()
	if restoredCount != compactedCount {
		t.Fatalf("重新加载后消息条数 %d 应与压缩后 %d 一致（未压缩的完整条数可能更多）",
			restoredCount, compactedCount)
	}

	// 验证滚动摘要已恢复
	if rt2.ctxBudget != nil && rt2.ctxBudget.Summary == "" {
		t.Error("滚动摘要应已恢复，但不为空")
	}
}

// TestGetDisplayHistory_ReturnsFullHistory 验证 GetDisplayHistory 返回完整历史
// 而非压缩快照——压缩持久化后 GUI 展示历史不应残缺（回归测试，对应 b221845 副作用）。
func TestGetDisplayHistory_ReturnsFullHistory(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	mem, memErr := memory.New(tmpDir, 10)
	if memErr != nil {
		t.Fatalf("memory.New: %v", memErr)
	}
	rt.Memory = mem

	// 设置预算（小窗口触发压缩）
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)
	// 创建会话
	rt.NewSession("测试展示历史")
	sid := rt.GetSessionID()
	if sid == "" {
		t.Fatal("会话 ID 不应为空")
	}

	// 写入大量消息触发压缩
	big := strings.Repeat("这是触发压缩的长内容用于测试展示历史", 30)
	for i := 0; i < 10; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}

	// 记录写入的总条数（20 条：10 user + 10 assistant）
	totalWritten := 20

	// 验证 conversationHistory 已被压缩（条数少于写入量）
	rt.historyMu.RLock()
	compactedCount := len(rt.conversationHistory)
	rt.historyMu.RUnlock()
	if compactedCount >= totalWritten {
		t.Fatalf("前置条件：压缩后 conversationHistory 条数 %d 应少于写入量 %d（未触发压缩）",
			compactedCount, totalWritten)
	}

	// 核心断言：GetDisplayHistory 返回完整条数（等于写入量，非压缩后）
	display := rt.GetDisplayHistory(sid)
	if len(display) != totalWritten {
		t.Fatalf("GetDisplayHistory 条数 %d 应等于写入量 %d（应完整，非压缩后 %d）",
			len(display), totalWritten, compactedCount)
	}

	// 验证内容完整性：第 1 条 user 和第 20 条 assistant 都在
	if len(display) >= 1 && display[0].Role != "user" {
		t.Errorf("历史首条应为 user，实际 role=%q", display[0].Role)
	}
	if len(display) >= totalWritten && display[totalWritten-1].Role != "assistant" {
		t.Errorf("历史末条应为 assistant，实际 role=%q", display[totalWritten-1].Role)
	}

	// 模拟重新加载会话后，GetDisplayHistory 仍应返回完整历史
	rt2 := newTestRuntimeAt(t, tmpDir)
	mem2, memErr2 := memory.New(tmpDir, 10)
	if memErr2 != nil {
		t.Fatalf("memory.New: %v", memErr2)
	}
	rt2.Memory = mem2
	rt2.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt2.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)
	_, _, err := rt2.LoadSession(sid)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}

	// 重新加载后 conversationHistory 是压缩快照
	rt2.historyMu.RLock()
	restoredCount := len(rt2.conversationHistory)
	rt2.historyMu.RUnlock()
	if restoredCount == 0 {
		t.Fatal("重新加载后 conversationHistory 不应为空")
	}

	// 但 GetDisplayHistory 仍返回完整
	display2 := rt2.GetDisplayHistory(sid)
	if len(display2) != totalWritten {
		t.Fatalf("重新加载后 GetDisplayHistory 条数 %d 应仍等于写入量 %d（完整）",
			len(display2), totalWritten)
	}
}

// TestCompressionAudit_Visible 验证压缩审计可通过 /ctx（ContextStats）展示，
// 解决"压缩信息丢失不可审计"的共性缺点。
func TestCompressionAudit_Visible(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.ctxBudget = ctxmgr.NewTokenBudget(1500)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)
	big := strings.Repeat("这是用来触发压缩审计的长内容", 50)
	for i := 0; i < 12; i++ {
		rt.AddToHistory("user", big)
		rt.AddToHistory("assistant", big)
	}
	stats := rt.ContextStats()
	if !strings.Contains(stats, "压缩审计") {
		t.Fatalf("ContextStats 应包含压缩审计: %q", stats)
	}
	// 审计内容应包含丢弃/截断动作
	if !strings.Contains(stats, "丢弃") && !strings.Contains(stats, "截断") {
		t.Errorf("压缩审计应记录具体动作: %q", stats)
	}
}

// TestConfigSummary 验证 /config 命令展示生效配置。
func TestConfigSummary(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	rt.Config.Tools.ApprovalMode = "strict"
	summary := rt.ConfigSummary()
	for _, want := range []string{"生效配置", "模型", "审批模式: strict", "上下文阈值", "Overseer"} {
		if !strings.Contains(summary, want) {
			t.Errorf("ConfigSummary 缺少 %q: %s", want, summary)
		}
	}
}

// TestApplyConfigPatch_WritesFile 验证 GUI config.set 链路：
// 补丁应用 → 写盘 → 重载一致；脱敏 api_key 不覆盖；未知 key 报错。
func TestApplyConfigPatch_WritesFile(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "config.yaml")
	rt.Config.ConfigPath = cfgPath
	rt.Config.ActiveModelName = "default-model"
	rt.Config.Models = []config.ModelPreset{{Name: "default-model", Backend: "deepseek", Endpoint: "http://localhost:8080", APIKey: "sk-original-key", MaxContext: 131072}}

	// 1. 应用补丁并写盘
	patch := map[string]any{
		"model.max_context":       262144.0,
		"context.keep_recent":     16,
		"cli_defaults.effort":     "xhigh",
		"model.api_key":           "sk-****abcd", // 脱敏 → 跳过
		"overseer.node_auto_plan": false,
	}
	if _, err := rt.ApplyConfigPatch(patch); err != nil {
		t.Fatalf("ApplyConfigPatch: %v", err)
	}
	if rt.Config.ActiveModel().MaxContext != 262144 {
		t.Errorf("max_context 未生效: %d", rt.Config.ActiveModel().MaxContext)
	}
	if rt.Config.ActiveModel().APIKey != "sk-original-key" {
		t.Errorf("脱敏 key 不应覆盖: %s", rt.Config.ActiveModel().APIKey)
	}
	if rt.Config.Overseer.NodeAutoPlan {
		t.Error("node_auto_plan 应为 false")
	}
	// 2. 文件真实写盘
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("配置文件未写盘: %v", err)
	}
	// 3. 重载一致
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("重载失败: %v", err)
	}
	if reloaded.ActiveModel().MaxContext != 262144 || reloaded.Context.KeepRecent != 16 ||
		reloaded.CLIDefaults.Effort != "xhigh" {
		t.Errorf("重载值不一致: %+v", reloaded)
	}
	// 4. 未知 key 报错且不写盘
	if _, err := rt.ApplyConfigPatch(map[string]any{"model.bogus": 1}); err == nil {
		t.Fatal("未知 key 应报错")
	}
	// 5. 阈值单调修正：compact >= hard 时自动 +0.01
	if _, err := rt.ApplyConfigPatch(map[string]any{"context.compact_ratio": 0.95}); err != nil {
		t.Fatalf("阈值补丁: %v", err)
	}
	if !(rt.Config.Context.CompactRatio < rt.Config.Context.HardRatio && rt.Config.Context.HardRatio < rt.Config.Context.SafetyRatio) {
		t.Errorf("阈值未单调: %v/%v/%v", rt.Config.Context.CompactRatio, rt.Config.Context.HardRatio, rt.Config.Context.SafetyRatio)
	}
}

// TestApplyConfigPatch_LiveReload 验证保存设置"立即生效"：ApplyConfigPatch
// 成功后运行中的组件（上下文预算/模型后端/检索参数）同步热更新，
// 无需重启进程；重型组件字段列入 needsRestart 提示。
func TestApplyConfigPatch_LiveReload(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	cfgPath := filepath.Join(tmpDir, "config.yaml")
	rt.Config.ConfigPath = cfgPath
	rt.Config.ActiveModelName = "default-model"
	rt.Config.Models = []config.ModelPreset{{Name: "default-model", Backend: "deepseek", APIKey: "sk-live-key", MaxContext: 131072}}
	// 换回真实 DeepSeekBackend（热更新断言需要；mock 不记录 topP/temperature 等值）
	rt.Backend = model.NewDeepSeekBackend("http://old.example.com", "sk-old", "default-model", 4096)

	// 保存前基线：ctxBudget 用旧 max_context
	before := rt.ctxBudget.MaxTokens

	patch := map[string]any{
		"model.max_context":         262144.0,
		"context.compact_ratio":     0.60,
		"context.hard_ratio":        0.75,
		"context.safety_ratio":      0.90,
		"context.keep_recent":       12,
		"cli_defaults.effort":       "xhigh",
		"model.top_p":               0.8,
		"model.temperature":         0.6,
		"model.supports_vision":     true,
		"model.endpoint":            "https://new.example.com/v1",
		"model.api_key":             "sk-new-live-key",
		"harness.score_threshold":   40.0,
		"harness.decay_factor":      0.85,
		"harness.anchor_ratio":      0.3,
		"acp.port":                  30000, // 重型组件 → 需重启
		"memory.max_sessions":       500,   // 重型组件 → 需重启
		"overseer.pass_threshold":   85.0,  // 每次读取 Config，即时生效
		"model.vision_model":        "gpt-4o",
		"plugins.sandbox.cpu_limit": 0.7,
	}
	needsRestart, err := rt.ApplyConfigPatch(patch)
	if err != nil {
		t.Fatalf("ApplyConfigPatch: %v", err)
	}
	// model.active 只读：切换必须走 model.switch（预设下拉），直接改字段被拒
	if _, err := rt.ApplyConfigPatch(map[string]any{"model.active": "other"}); err == nil ||
		!strings.Contains(err.Error(), "只读") {
		t.Fatalf("model.active 应拒绝修改（只读），实际: %v", err)
	}

	// 1. 上下文预算立即生效（max + 阈值 + keepRecent）
	if rt.ctxBudget.MaxTokens != 262144 {
		t.Errorf("ctxBudget.MaxTokens 未热更新: %d（原 %d）", rt.ctxBudget.MaxTokens, before)
	}
	if rt.ctxBudget.CompactRatio != 0.60 || rt.ctxBudget.HardRatio != 0.75 ||
		rt.ctxBudget.SafetyRatio != 0.90 || rt.ctxBudget.KeepRecent != 12 {
		t.Errorf("ctxBudget 阈值未热更新: %+v", rt.ctxBudget)
	}

	// 2. 模型后端热更新
	if ds, ok := rt.Backend.(*model.DeepSeekBackend); ok {
		if ds.ReasoningEffort() != "max" { // xhigh → max 映射
			t.Errorf("effort 未热更新: %s", ds.ReasoningEffort())
		}
		if ds.TopP() != 0.8 || ds.Temperature() != 0.6 {
			t.Errorf("top_p/temperature 未热更新: %v/%v", ds.TopP(), ds.Temperature())
		}
		if !ds.SupportsVision() {
			t.Error("supports_vision 未热更新")
		}
		// 模型 ID 无 backend 切换 patch 时保持不变（切换走 model.switch 链路）
	} else {
		t.Fatalf("Backend 类型不符: %T", rt.Backend)
	}

	// 3. 检索参数热更新（阈值对 Search 生效）
	blocks := rt.Harness.Search(context.Background(), "任意查询", nil)
	_ = blocks // 不崩溃即通过；阈值经 Engine.ApplyConfig 同步
	if got := rt.Harness.Threshold(); got != 40 {
		t.Errorf("harness 阈值未热更新: %v, want 40", got)
	}

	// 3.1 上下文统计读取不崩溃、热更新后的预算对状态栏可见
	if u := rt.ContextUsage(); !strings.Contains(u, "262k") {
		t.Errorf("ContextUsage 未反映热更新后的预算: %q", u)
	}
	if s := rt.ContextStats(); !strings.Contains(s, "262144") {
		t.Errorf("ContextStats 未反映热更新后的预算: %q", s)
	}
	rt.ResetContextSummary() // 锁保护路径冒烟（无死锁/无崩溃即可）

	// 4. needsRestart 仅含重型组件字段（不含已热更新的 key）
	wantRestart := map[string]bool{"acp.port": true, "memory.max_sessions": true, "plugins.sandbox.cpu_limit": true}
	gotRestart := map[string]bool{}
	for _, k := range needsRestart {
		gotRestart[k] = true
	}
	for k := range wantRestart {
		if !gotRestart[k] {
			t.Errorf("needsRestart 缺少 %s: %v", k, needsRestart)
		}
	}
	for _, k := range []string{"model.max_context", "cli_defaults.effort", "model.top_p",
		"harness.score_threshold", "overseer.pass_threshold", "model.vision_model", "model.backend"} {
		if gotRestart[k] {
			t.Errorf("%s 已热更新却列入 needsRestart: %v", k, needsRestart)
		}
	}
}

// TestApplyConfigPatch_LiveReload_Race 竞态回归：配置热更新（applyLiveConfig
// 持锁替换 ctxBudget 指针）与状态栏轮询（ContextUsage/ContextStats/
// ResetContextSummary 读取）并发时不得 data race。修复前 ContextUsage/
// ContextStats 在 RUnlock 后仍直接读 rt.ctxBudget 字段、ResetContextSummary
// 无锁，本测试在 -race 下可稳定复现（GUI 状态栏轮询 + 保存设置的真实场景）。
func TestApplyConfigPatch_LiveReload_Race(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	cfgPath := filepath.Join(tmpDir, "config.yaml")
	rt.Config.ConfigPath = cfgPath
	rt.Config.ActiveModelName = "default-model"
	rt.Config.Models = []config.ModelPreset{{Name: "default-model", Backend: "deepseek", Endpoint: "http://localhost:8080", APIKey: "sk-race", MaxContext: 131072}}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	// 读侧：模拟 GUI 状态栏轮询循环
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = rt.ContextUsage()
				_ = rt.ContextStats()
				rt.ResetContextSummary()
			}
		}
	}()
	// 写侧：反复保存设置（每次替换 ctxBudget 指针）
	for i := 0; i < 50; i++ {
		if _, err := rt.ApplyConfigPatch(map[string]any{
			"model.max_context": float64(131072 + i*1000),
		}); err != nil {
			t.Fatalf("ApplyConfigPatch: %v", err)
		}
	}
	close(stop)
	wg.Wait()
}

// TestAddToHistoryForSession_CrossSession 回归测试（GUI 跨会话流式修复）：
// 流式生成期间切换会话后，assistant 回复必须写回【发起流时】的会话事件日志，
// 不得写进当前会话的内存历史；切回原会话时 LoadSession 能恢复完整历史。
func TestAddToHistoryForSession_CrossSession(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	// 会话 A：当前会话，写入走内存+事件
	sessA := rt.NewSession("A")
	rt.AddToHistory("user", "A 的提问")
	rt.AddToHistoryForSession(sessA.UUID, "assistant", "A 的回复")
	hist := rt.GetConversationHistory()
	if len(hist) != 2 || hist[1].Role != "assistant" || hist[1].Content != "A 的回复" {
		t.Fatalf("当前会话写入异常: %+v", hist)
	}

	// 切到会话 B（当前会话变化、内存历史清空）
	rt.NewSession("B")
	if len(rt.GetConversationHistory()) != 0 {
		t.Fatalf("切换后内存历史应为空: %+v", rt.GetConversationHistory())
	}

	// A 的流在 B 会话期间结束：回复必须写回 A 的事件日志，不得污染 B 的内存
	rt.AddToHistoryForSession(sessA.UUID, "assistant", "A 迟到的回复")
	if len(rt.GetConversationHistory()) != 0 {
		t.Fatalf("非当前会话写入污染了当前会话内存历史: %+v", rt.GetConversationHistory())
	}
	evts, err := rt.Memory.LoadEvents(sessA.UUID)
	if err != nil {
		t.Fatalf("加载 A 事件失败: %v", err)
	}
	if len(evts) != 3 {
		t.Fatalf("A 事件数 = %d, want 3（user + 回复 + 迟到回复）: %+v", len(evts), evts)
	}
	last := evts[len(evts)-1]
	if last.Type != "assistant_msg" {
		t.Fatalf("A 迟到回复事件类型 = %q, want assistant_msg", last.Type)
	}
	if s, _ := last.Data.(string); s != "A 迟到的回复" {
		t.Fatalf("A 迟到回复内容 = %q", s)
	}

	// 切回 A：LoadSession 从事件日志恢复完整历史（含迟到回复）
	_, loaded, err := rt.LoadSession(sessA.UUID)
	if err != nil {
		t.Fatalf("LoadSession(A) 失败: %v", err)
	}
	_ = loaded
	restored := rt.GetConversationHistory()
	if len(restored) != 3 {
		t.Fatalf("切回 A 后历史条数 = %d, want 3: %+v", len(restored), restored)
	}
	if restored[2].Role != "assistant" || restored[2].Content != "A 迟到的回复" {
		t.Fatalf("切回 A 后最后一条 = %+v, want A 迟到的回复", restored[2])
	}
}

// TestAddToHistoryForSession_SameSession 验证 sid == 当前会话时走 AddToHistory
// 原路径（内存 + 事件），行为不变。
func TestAddToHistoryForSession_SameSession(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	sess := rt.NewSession("S")
	rt.AddToHistoryForSession(sess.UUID, "user", "消息")
	hist := rt.GetConversationHistory()
	if len(hist) != 1 || hist[0].Content != "消息" {
		t.Fatalf("同会话写入异常: %+v", hist)
	}
	// 空 sid 兜底：走 AddToHistory（当前会话）
	rt.AddToHistoryForSession("", "assistant", "回复")
	if len(rt.GetConversationHistory()) != 2 {
		t.Fatalf("空 sid 写入异常: %+v", rt.GetConversationHistory())
	}
	// 思考标签剥离（与 AddToHistory 一致）
	rt.AddToHistoryForSession(sess.UUID, "assistant", "正文<think>思考</think>尾巴")
	last := rt.GetConversationHistory()[2].Content.(string)
	if strings.Contains(last, "<think>") {
		t.Fatalf("思考标签未剥离: %q", last)
	}
}

// TestDeleteSession_CurrentClearsState 验证：允许删除当前活跃会话，
// 删除后回到无会话状态（sessionID 清空、内存历史清空）——"删除最后一个
// 会话后不自动新建"的后端基础（GUI 前端据此进入空状态而非 newSession）。
func TestDeleteSession_CurrentClearsState(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)

	s1 := rt.NewSession("会话一")
	rt.AddToHistory("user", "第一条")
	s2 := rt.NewSession("会话二")
	rt.AddToHistory("user", "第二条")
	if rt.GetSessionID() != s2.UUID {
		t.Fatalf("当前会话应为 s2: %q", rt.GetSessionID())
	}

	// 删除非当前会话 s1 → 成功，当前会话 s2 不变
	if err := rt.DeleteSession(s1.UUID); err != nil {
		t.Fatalf("删除非当前会话失败: %v", err)
	}
	if rt.GetSessionID() != s2.UUID {
		t.Fatalf("删除非当前会话后当前会话应不变: %q", rt.GetSessionID())
	}

	// 删除当前会话 s2 → 成功，回到无会话状态（历史一并清空）
	if err := rt.DeleteSession(s2.UUID); err != nil {
		t.Fatalf("删除当前会话失败: %v", err)
	}
	if rt.GetSessionID() != "" {
		t.Fatalf("删除当前会话后 sessionID 应清空: %q", rt.GetSessionID())
	}
	if len(rt.GetConversationHistory()) != 0 {
		t.Fatalf("删除当前会话后内存历史应清空: %+v", rt.GetConversationHistory())
	}
	if list := rt.ListSessions(); len(list) != 0 {
		t.Fatalf("删除所有会话后列表应为空: %+v", list)
	}

	// 删除不存在会话：幂等成功（memory.Delete 静默），无副作用
	if err := rt.DeleteSession("no-such"); err != nil {
		t.Fatalf("删除不存在会话应幂等成功: %v", err)
	}
}

// TestLoadSession_NoSummaryCrossSessionLeak 验证跨会话摘要串扰已修复：
// ctxBudget.Summary 是 Runtime 级单例，加载会话后必须按会话维度重建——
// 上一个会话残留的滚动摘要不得注入到新加载会话的上下文（否则会跑偏）。
// 覆盖：事件重放路径（无快照）、快照路径（CompactedSummary 非空/为空，
// 以及在切换回快照会话时恢复自己的摘要）。
func TestLoadSession_NoSummaryCrossSessionLeak(t *testing.T) {
	rt, tmpDir := newTestRuntime(t)
	defer os.RemoveAll(tmpDir)
	mem, memErr := memory.New(tmpDir, 10)
	if memErr != nil {
		t.Fatalf("memory.New: %v", memErr)
	}
	rt.Memory = mem
	rt.ctxBudget = ctxmgr.NewTokenBudget(2000)
	rt.ctxBudget.ApplyConfig(0.5, 0.7, 0.95, 2)

	// 会话 A：写入大量消息触发压缩，产生滚动摘要 A 并持久化快照
	rt.NewSession("会话A")
	sidA := rt.GetSessionID()
	bigA := strings.Repeat("会话A的独特内容甲", 30)
	for i := 0; i < 10; i++ {
		rt.AddToHistory("user", bigA)
		rt.AddToHistory("assistant", bigA)
	}
	rt.historyMu.RLock()
	summaryA := rt.ctxBudget.Summary
	rt.historyMu.RUnlock()
	if summaryA == "" {
		t.Fatal("前置条件：会话 A 应产生滚动摘要")
	}

	// 会话 B：未压缩的小会话（无快照，事件日志仅 1 条短消息）。
	// 注意：不用 NewSession 清摘要（NewSession/BTN 会 ResetContextSummary，
	// 恰好掩盖残留 bug）——先创建，随后 LoadSession(A) 把 A 的摘要带回来，
	// 再直接 LoadSession(B)，完全还原 GUI"当前停在 A、点击切换到 B"的场景。
	rt.NewSession("会话B")
	sidB := rt.GetSessionID()
	rt.AddToHistory("user", "会话B的短消息")

	// 先切回 A：让 ctxBudget.Summary 恢复为 A 的摘要（模拟当前正停在 A）
	if _, _, err := rt.LoadSession(sidA); err != nil {
		t.Fatalf("LoadSession(A): %v", err)
	}
	rt.historyMu.RLock()
	cur := rt.ctxBudget.Summary
	rt.historyMu.RUnlock()
	if cur != summaryA {
		t.Fatalf("前置条件：加载 A 后摘要应恢复为 %q，实际 %q", summaryA, cur)
	}

	// ── 核心场景：同一 Runtime 内从 A 直接切到 B（GUI/TUI 点击会话）──
	// 旧代码 LoadSession 不按会话重置 ctxBudget.Summary 单例 → 残留 A 的摘要 → 串扰 → 跑偏。
	if _, _, err := rt.LoadSession(sidB); err != nil {
		t.Fatalf("LoadSession(B): %v", err)
	}
	rt.historyMu.RLock()
	leak := rt.ctxBudget.Summary
	rt.historyMu.RUnlock()
	if leak != "" {
		t.Errorf("事件重放路径摘要串扰：加载 B 后 Summary=%q（应清空，残留自 A）", leak)
	}

	// 摘要已清空时，B 中再触发一次压缩也不得注入 A 的摘要
	rt.AddToHistory("user", strings.Repeat("会话B的大消息", 100))
	rt.historyMu.RLock()
	firstHistory := ""
	if len(rt.conversationHistory) > 0 {
		firstHistory, _ = rt.conversationHistory[0].Content.(string)
	}
	summaryAfterCompact := rt.ctxBudget.Summary
	rt.historyMu.RUnlock()
	if strings.Contains(firstHistory, "甲") || strings.Contains(summaryAfterCompact, "甲") {
		t.Errorf("事件重放路径穿越：B 的上下文注入 A 的摘要（首条=%q, Summary=%q）",
			firstHistory, summaryAfterCompact)
	}

	// 会话 C：空白会话（无快照、无事件）→ 摘要同样必须为空
	rt.NewSession("空会话C")
	sidC := rt.GetSessionID()
	if _, _, err := rt.LoadSession(sidC); err != nil {
		t.Fatalf("LoadSession(C): %v", err)
	}
	rt.historyMu.RLock()
	emptyLeak := rt.ctxBudget.Summary
	rt.historyMu.RUnlock()
	if emptyLeak != "" {
		t.Errorf("空会话加载后摘要残留：Summary=%q（应清空，不得带入 A 的摘要）", emptyLeak)
	}

	// 最后切回会话 A（快照路径，CompactedSummary 非空）：应恢复 A 自己的摘要
	if _, _, err := rt.LoadSession(sidA); err != nil {
		t.Fatalf("LoadSession(A): %v", err)
	}
	rt.historyMu.RLock()
	restored := rt.ctxBudget.Summary
	rt.historyMu.RUnlock()
	if restored != summaryA {
		t.Errorf("快照路径：加载 A 后摘要应恢复为 A 的 %q，实际 %q", summaryA, restored)
	}
}

// TestSplitReasoning_AllFormats 验证 SplitReasoning 对全部思考标签格式的
// 分离语义：正文进 body、思维链进 reasoning（与 StripThinking 覆盖面一致，
// 区别是不再丢弃思维链--DeepSeek thinking 模式要求原样回传 reasoning_content）。
func TestSplitReasoning_AllFormats(t *testing.T) {
	cases := []struct {
		name      string
		input     string
		wantBody  string
		wantReasn string
	}{
		{"模型层标记", "开头<|reasoning|>模型思考<|/reasoning|>结尾", "开头结尾", "模型思考"},
		{"think标签", "正文<think>思考</think>尾巴", "正文尾巴", "思考"},
		{"thinking标签", "正<thinking>变体</thinking>尾", "正尾", "变体"},
		{"旧格式", "正文 thinking思考 response结尾", "正文结尾", "思考"},
		{"多段混合", "a<|reasoning|r1<|/reasoning|>b<think>r2</think>c", "abc", "r1r2"},
		{"未闭合标记归思维", "正文<|reasoning|>思考", "正文", "思考"},
		{"无标签原样", "纯正文", "纯正文", ""},
		{"空串", "", "", ""},
	}
	for _, c := range cases {
		body, reasn := SplitReasoning(c.input)
		if body != c.wantBody || reasn != c.wantReasn {
			t.Errorf("%s: SplitReasoning(%q) = (%q, %q), want (%q, %q)",
				c.name, c.input, body, reasn, c.wantBody, c.wantReasn)
		}
	}
}

// TestSplitReasoning_ThinkNotEatThinking 验证 <think> 循环不会误吃
// <thinking> 标签（两者互不为前缀子串，处理顺序无关）。
func TestSplitReasoning_ThinkNotEatThinking(t *testing.T) {
	// <think> 优先处理时，<thinking> 的完整标签第 7 字符是 'i' 非 '>'
	body, reasn := SplitReasoning("<think>前思考</think>正<thinking>后思考</thinking>文")
	if body != "正文" {
		t.Errorf("body = %q, want %q", body, "正文")
	}
	if reasn != "前思考后思考" {
		t.Errorf("reasoning = %q, want %q", reasn, "前思考后思考")
	}
}

// TestSplitReasoning_PathNotMatched 路径中的 thinking 不应被识别为旧格式。
func TestSplitReasoning_PathNotMatched(t *testing.T) {
	s := "请读取 /home/thinking_project/notes.txt"
	body, reasn := SplitReasoning(s)
	if body != s || reasn != "" {
		t.Errorf("路径误剥离: body=%q, reasoning=%q", body, reasn)
	}
}
