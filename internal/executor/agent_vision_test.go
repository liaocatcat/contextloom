package executor

import (
	"context"
	"encoding/json"
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

// imgParts 构造 N 张图片片段（测试用固定 data URI）。
func imgParts(n int) []types.ContentPart {
	parts := make([]types.ContentPart, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, types.ContentPart{
			Type:     "image_url",
			ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"},
		})
	}
	return parts
}

// newChatAgent 构造 chat 模式测试 Agent（含可用后端与工具注册表）。
func newChatAgent() *Agent {
	ds := model.NewDeepSeekBackend("http://127.0.0.1:1", "sk-test", "main-model", 131072)
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	return &Agent{
		ID:      "test-agent",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "测试", Mode: "chat"},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: ds,
	}
}

// ── buildMessages 识图组装 ──

// TestBuildMessages_ChatHistoryCarriesImages chat 模式图片随对话历史透传：
// 历史中的多模态 user 消息原样保留（多轮追问可见图），payload 消息不再重复附图。
func TestBuildMessages_ChatHistoryCarriesImages(t *testing.T) {
	a := newChatAgent()
	a.conversationHistory = []types.Message{
		{Role: "user", Content: []types.ContentPart{
			{Type: "text", Text: "看图"},
			{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,QUJD"}},
		}},
	}
	task := types.TaskCreate{
		Mode:    "chat",
		Payload: map[string]string{"directive": "CODE_AGENT"},
		Images:  imgParts(1),
	}
	msgs := a.buildMessages(task, nil)

	// 历史消息中的图片保留
	foundHistoryImage := false
	for _, m := range msgs {
		if m.Role == "user" && m.ImagePartCount() == 1 {
			foundHistoryImage = true
		}
	}
	if !foundHistoryImage {
		t.Fatalf("历史多模态消息应保留图片: %+v", msgs)
	}
	// 所有消息图片总数 = 1（不能重复附图导致 token 翻倍）
	total := 0
	for _, m := range msgs {
		total += m.ImagePartCount()
	}
	if total != 1 {
		t.Errorf("图片总数 = %d，期望 1（历史已带图，payload 不得重复）", total)
	}
	// payload 消息仍是纯文本 JSON
	last := msgs[len(msgs)-1]
	if _, ok := last.Content.(string); !ok {
		t.Errorf("payload 消息应为纯文本，实际 %#v", last.Content)
	}
}

// TestBuildMessages_ChatAttachWhenHistoryLacksImages 无历史图片（webhook/ACP
// 渠道不经 AddToHistory）时，图片必须附到本轮 user 消息，否则视觉模型看不到图。
func TestBuildMessages_ChatAttachWhenHistoryLacksImages(t *testing.T) {
	a := newChatAgent()
	a.conversationHistory = []types.Message{{Role: "user", Content: "纯文本历史"}}
	task := types.TaskCreate{
		Mode:    "chat",
		Payload: map[string]string{"directive": "CODE_AGENT"},
		Images:  imgParts(2),
	}
	msgs := a.buildMessages(task, nil)
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.ImagePartCount() != 2 {
		t.Fatalf("最后一轮 user 消息应附 2 张图: role=%s images=%d", last.Role, last.ImagePartCount())
	}
	if !strings.Contains(last.TextContent(), "directive") {
		t.Errorf("payload JSON 文本应保留: %q", last.TextContent())
	}
}

// TestBuildMessages_TaskImagesAttached task 模式历史被展平为纯文本，
// 本轮图片必须显式附到最终 user 消息（OpenAI 兼容 image_url）。
func TestBuildMessages_TaskImagesAttached(t *testing.T) {
	a := newChatAgent()
	task := types.TaskCreate{
		Mode:    "task",
		Task:    "看这张图",
		Payload: map[string]string{"task": "看这张图"},
		Images:  imgParts(1),
	}
	msgs := a.buildMessages(task, nil)
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.ImagePartCount() != 1 {
		t.Fatalf("task 模式最终 user 消息应附 1 张图: role=%s images=%d", last.Role, last.ImagePartCount())
	}
	parts, ok := last.Content.([]types.ContentPart)
	if !ok || len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Fatalf("多模态结构错误: %#v", last.Content)
	}
}

// TestBuildMessages_GoalImagesAttached goal 模式：图片以独立 user 消息附带
// （goal 文本在 system 提示词中，无需重复）。
func TestBuildMessages_GoalImagesAttached(t *testing.T) {
	a := newChatAgent()
	task := types.TaskCreate{
		Mode:    "goal",
		Task:    "目标：看图",
		Payload: &types.GoalSession{Goal: "目标：看图", Phase: types.GoalPhaseExecute},
		Images:  imgParts(1),
	}
	msgs := a.buildMessages(task, nil)
	last := msgs[len(msgs)-1]
	if last.Role != "user" || last.ImagePartCount() != 1 {
		t.Fatalf("goal 模式应附图片消息: role=%s images=%d", last.Role, last.ImagePartCount())
	}
}

// TestBuildMessages_NoImagesUnchanged 无图片时行为与旧版完全一致（纯文本消息）。
func TestBuildMessages_NoImagesUnchanged(t *testing.T) {
	a := newChatAgent()
	task := types.TaskCreate{
		Mode:    "chat",
		Payload: map[string]string{"directive": "CODE_AGENT"},
	}
	msgs := a.buildMessages(task, nil)
	last := msgs[len(msgs)-1]
	if _, ok := last.Content.(string); !ok {
		t.Errorf("无图片时最终消息应为纯文本，实际 %#v", last.Content)
	}
}

// ── 模型切换 / 降级（prepareVision）──

// TestPrepareVision 决策矩阵：
// 无图片 → (nil,false)；后端不支持多模态 → 降级清空 Images；
// 后端支持 + 未配 visionModel → (nil,false) 主模型收图；
// 后端支持 + 同模型 → (nil,false)；不同模型 → 切换并返回恢复函数。
func TestPrepareVision(t *testing.T) {
	ds := model.NewDeepSeekBackend("http://127.0.0.1:1", "sk", "main-model", 131072)

	// 无图片：无操作
	a := &Agent{backend: ds, visionModel: "", Task: types.TaskCreate{}}
	if f, d := a.prepareVision(); f != nil || d {
		t.Errorf("无图片应 (nil,false)，实际 (f=%v, degraded=%v)", f != nil, d)
	}

	// 默认（DeepSeek 纯文本，supportsVision=false）+ 有图 → 降级
	a = &Agent{backend: ds, visionModel: "", Task: types.TaskCreate{Images: imgParts(2)}}
	f, degraded := a.prepareVision()
	if f != nil || !degraded {
		t.Fatalf("不支持多模态 + 有图应降级 (nil,true)，实际 (f=%v, degraded=%v)", f != nil, degraded)
	}
	if len(a.Task.Images) != 0 {
		t.Errorf("降级后 Task.Images 应清空，实际 %d 张", len(a.Task.Images))
	}

	// 后端支持多模态 + 未配 visionModel → 不切换不降级（主模型直接收图）
	ds.SetSupportsVision(true)
	a = &Agent{backend: ds, visionModel: "", Task: types.TaskCreate{Images: imgParts(1)}}
	if f, d := a.prepareVision(); f != nil || d {
		t.Errorf("支持多模态 + 未配 visionModel 应 (nil,false)，实际 (f=%v, degraded=%v)", f != nil, d)
	}
	if len(a.Task.Images) != 1 {
		t.Errorf("支持多模态时图片应保留，实际 %d 张", len(a.Task.Images))
	}

	// 支持 + 同模型 → 不切换
	a.visionModel = "main-model"
	if f, d := a.prepareVision(); f != nil || d {
		t.Errorf("同模型应 (nil,false)，实际 (f=%v, degraded=%v)", f != nil, d)
	}

	// 支持 + 不同模型 → 切换并返回恢复函数
	a.visionModel = "vision-model"
	restore, d := a.prepareVision()
	if restore == nil || d {
		t.Fatalf("不同模型应切换 (restore!=nil, degraded=false)，实际 (restore=%v, degraded=%v)", restore != nil, d)
	}
	if ds.ModelID() != "vision-model" {
		t.Errorf("切换后 ModelID = %q，期望 vision-model", ds.ModelID())
	}
	restore()
	if ds.ModelID() != "main-model" {
		t.Errorf("恢复后 ModelID = %q，期望 main-model", ds.ModelID())
	}

	// 非 vision-capable 后端（mock）→ 按不支持降级
	a.backend = &mockBackend{}
	a.Task.Images = imgParts(1)
	if f, d := a.prepareVision(); f != nil || !d {
		t.Errorf("mock 后端应按不支持降级，实际 (f=%v, degraded=%v)", f != nil, d)
	}
	if len(a.Task.Images) != 0 {
		t.Errorf("mock 后端降级后 Images 应清空")
	}
}

// TestVisionDegradedNotice 降级提示包含原因（400）与解决路径（配置项）。
func TestVisionDegradedNotice(t *testing.T) {
	msg := visionDegradedNotice(2)
	for _, want := range []string{"2 张图片", "400", "supports_vision", "vision_model"} {
		if !strings.Contains(msg, want) {
			t.Errorf("降级提示缺少关键信息 %q：%s", want, msg)
		}
	}
}

// TestRunStream_VisionModelSwitchAndRestore 端到端：带图片运行 → 请求体 model
// 为视觉模型；流结束后主模型恢复。
func TestRunStream_VisionModelSwitchAndRestore(t *testing.T) {
	var mu sync.Mutex
	var seenModels []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		seenModels = append(seenModels, body.Model)
		mu.Unlock()
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("not flusher")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"这是图里的内容\"}},{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		flusher.Flush()
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	ds.SetSupportsVision(true) // 端到端切换前置条件：显式声明端点支持多模态
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "vision-agent",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "看图", Mode: "task", Images: imgParts(1)},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: ds,
	}
	agent.SetVisionModel("vision-model")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	_ = collectAgentStream(t, ch, errCh)

	mu.Lock()
	models := append([]string(nil), seenModels...)
	mu.Unlock()
	foundVision := false
	for _, m := range models {
		if m == "vision-model" {
			foundVision = true
		}
	}
	if !foundVision {
		t.Errorf("请求 model 应为 vision-model，实际 %v", models)
	}
	if ds.ModelID() != "main-model" {
		t.Errorf("流结束后应恢复主模型，实际 %q", ds.ModelID())
	}
}

// TestRunStream_VisionNotice 带图片运行：随流输出识图提示（TUI/GUI 统一可见）。
func TestRunStream_VisionNotice(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}},{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer ts.Close()

	ds := model.NewDeepSeekBackend(ts.URL, "sk-test", "main-model", 131072)
	ds.SetSupportsVision(true) // 识图提示的前置条件：端点支持多模态
	eng := harness.New(harness.Config{ScoreThreshold: 50, DecayFactor: 0.9, AnchorRatio: 0.2})
	agent := &Agent{
		ID:      "vision-agent",
		Task:    types.TaskCreate{Directive: "CODE_AGENT", Task: "看图", Mode: "task", Images: imgParts(1)},
		harness: eng,
		tools:   tools.NewRegistry(),
		backend: ds,
	}
	agent.SetVisionModel("vision-model")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, errCh, err := agent.RunStream(ctx)
	if err != nil {
		t.Fatalf("RunStream 启动失败: %v", err)
	}
	got := collectAgentStream(t, ch, errCh)
	if !strings.Contains(got, "🖼 识图模式") || !strings.Contains(got, "vision-model") {
		t.Errorf("应输出识图提示（含模型名），实际 %q", got)
	}
}
