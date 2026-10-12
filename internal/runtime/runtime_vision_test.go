package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/contextloom/contextloom-agent/internal/modelgateway"
	"github.com/contextloom/contextloom-agent/internal/vision"
	"github.com/contextloom/contextloom-agent/pkg/types"
)

// captureBackend 捕获 Stream 收到的消息列表（验证图片片段是否进入请求）。
type captureBackend struct {
	messages []types.Message
}

func (m *captureBackend) Chat(ctx context.Context, messages []types.Message, tools []types.ToolDefinition) (string, []types.ToolCall, error) {
	return "", nil, nil
}
func (m *captureBackend) Stream(ctx context.Context, messages []types.Message) (<-chan string, <-chan error) {
	m.messages = messages
	ch := make(chan string)
	errCh := make(chan error)
	close(ch)
	close(errCh)
	return ch, errCh
}
func (m *captureBackend) ModelID() string                 { return "capture" }
func (m *captureBackend) MaxContext() int                 { return 4096 }
func (m *captureBackend) SupportsVision() bool            { return false }
func (m *captureBackend) SetReasoningEffort(level string) {}
func (m *captureBackend) ReasoningEffort() string         { return "" }
func (m *captureBackend) SetTopP(p float64)               {}
func (m *captureBackend) TopP() float64                   { return 0 }
func (m *captureBackend) SetTemperature(t float64)        {}
func (m *captureBackend) Temperature() float64            { return 0 }

// writePNG 在 dir 下写一个伪 PNG 文件并返回绝对路径。
func writePNG(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("\x89PNG\r\n\x1a\nfake-image-bytes"), 0o644); err != nil {
		t.Fatalf("写测试图片失败: %v", err)
	}
	return p
}

func newVisionRuntime(t *testing.T) (*Runtime, string) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "contextloom-vision-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(tmpDir) })
	cfg := testConfig(tmpDir)
	// 识图测试前置条件：显式声明端点支持多模态（DeepSeek 官方默认纯文本，
	// supports_vision=false 时历史/任务按路径文本降级，见降级测试）。
	cfg.Models[0].SupportsVision = true
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	return rt, tmpDir
}

// TestAddToHistory_MultimodalUserMessage 含图片路径的 user 消息入历史时为
// 多模态（ContentParts），多轮追问图片仍在上下文中。
func TestAddToHistory_MultimodalUserMessage(t *testing.T) {
	rt, dir := newVisionRuntime(t)
	p := writePNG(t, dir, "shot.png")

	rt.AddToHistory("user", "看下 "+p+" 里有什么")
	hist := rt.GetConversationHistory()
	if len(hist) != 1 {
		t.Fatalf("历史长度 = %d，期望 1", len(hist))
	}
	msg := hist[0]
	if msg.ImagePartCount() != 1 {
		t.Fatalf("历史消息应含 1 张图片，实际 %d: %#v", msg.ImagePartCount(), msg.Content)
	}
	if !strings.Contains(msg.TextContent(), "里有什么") {
		t.Errorf("文本应保留: %q", msg.TextContent())
	}
}

// TestAddToHistory_NoImagePlainText 无图片输入保持纯文本（旧行为不变）。
func TestAddToHistory_NoImagePlainText(t *testing.T) {
	rt, _ := newVisionRuntime(t)
	rt.AddToHistory("user", "普通消息")
	hist := rt.GetConversationHistory()
	if len(hist) != 1 {
		t.Fatalf("历史长度 = %d，期望 1", len(hist))
	}
	if s, ok := hist[0].Content.(string); !ok || s != "普通消息" {
		t.Errorf("应保持纯文本，实际 %#v", hist[0].Content)
	}
}

// TestEventToMessage_RestoresImages 事件日志纯文本（含 [图片: 路径] 标记）
// 恢复为多模态消息；文件已丢失时保留失效提示文本。
func TestEventToMessage_RestoresImages(t *testing.T) {
	rt, dir := newVisionRuntime(t)
	p := writePNG(t, dir, "pic.png")

	// 文件存在 → 重建 image_url
	evt := &types.SessionEvent{Type: "user_msg", Data: vision.EventText("看图", []vision.Image{{Path: p}})}
	msg := eventToMessage(evt)
	if msg == nil || msg.Role != "user" {
		t.Fatalf("恢复失败: %+v", msg)
	}
	if msg.ImagePartCount() != 1 {
		t.Fatalf("恢复后应含 1 张图: %#v", msg.Content)
	}

	// 文件丢失 → 退化纯文本 + 失效提示
	evt2 := &types.SessionEvent{Type: "user_msg", Data: "看图\n[图片: /不存在/gone.png]"}
	msg2 := eventToMessage(evt2)
	s, ok := msg2.Content.(string)
	if !ok {
		t.Fatalf("丢失文件应退化纯文本: %#v", msg2.Content)
	}
	if !strings.Contains(s, "[图片已失效: /不存在/gone.png]") {
		t.Errorf("缺少失效提示: %q", s)
	}

	// 无标记文本原样恢复
	evt3 := &types.SessionEvent{Type: "user_msg", Data: "普通消息"}
	msg3 := eventToMessage(evt3)
	if s, ok := msg3.Content.(string); !ok || s != "普通消息" {
		t.Errorf("普通消息应原样恢复: %#v", msg3.Content)
	}
	_ = rt
}

// TestDetectTaskImages_RelativeToProjectRoot 相对图片路径基于项目根解析
// （与 config.project.root 一致，不受 CWD 影响）。
func TestDetectTaskImages_RelativeToProjectRoot(t *testing.T) {
	rt, dir := newVisionRuntime(t)
	writePNG(t, dir, "rel.png")

	parts, err := rt.detectTaskImages("看 rel.png 和不存在.png")
	if err != nil {
		t.Fatalf("detectTaskImages 返回错误: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("应识别 1 张图（相对项目根），实际 %d", len(parts))
	}
	if parts[0].Type != "image_url" || parts[0].ImageURL == nil || !strings.HasPrefix(parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("图片片段结构错误: %+v", parts[0])
	}

	// 无图片输入 → nil
	parts, err = rt.detectTaskImages("普通文本")
	if err != nil || parts != nil {
		t.Errorf("无图片输入应返回 (nil,nil)，实际 (%v,%v)", parts, err)
	}
}

// TestProcessInput_ChatAttachesImages 不经 AddToHistory 的入口（模拟
// webhook/ACP 渠道）也会把图片附到本轮请求消息。
func TestProcessInput_ChatAttachesImages(t *testing.T) {
	rt, dir := newVisionRuntime(t)
	p := writePNG(t, dir, "webhook.png")
	capB := &captureBackend{}
	rt.Backend = capB

	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "看 "+p)
	if err != nil {
		t.Fatalf("ProcessInput 返回错误: %v", err)
	}
	for range ch {
	}
	if e := <-errCh; e != nil {
		t.Fatalf("流错误: %v", e)
	}
	found := false
	for _, m := range capB.messages {
		if m.Role == "user" && m.ImagePartCount() == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("请求消息中应含图片片段: %+v", capB.messages)
	}
}

// TestProcessInput_NoImageBackwardCompat 无图片输入：请求消息不含图片片段，
// 行为与旧版一致。
func TestProcessInput_NoImageBackwardCompat(t *testing.T) {
	rt, _ := newVisionRuntime(t)
	capB := &captureBackend{}
	rt.Backend = capB

	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "普通问题")
	if err != nil {
		t.Fatalf("ProcessInput 返回错误: %v", err)
	}
	for range ch {
	}
	if e := <-errCh; e != nil {
		t.Fatalf("流错误: %v", e)
	}
	for _, m := range capB.messages {
		if m.ImagePartCount() != 0 {
			t.Fatalf("无图片输入不应含图片片段: %+v", m.Content)
		}
	}
}

// TestVisionE2E_FullChain 全链路冒烟：配置 vision_model → AddToHistory 写多模态
// 历史 → ProcessInput 携带图片 → 请求体 model 切换为视觉模型且 messages 含
// image_url（base64 data URI），流结束后主模型恢复。
func TestVisionE2E_FullChain(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-vision-e2e-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)
	p := writePNG(t, tmpDir, "e2e.png")

	var mu sync.Mutex
	var gotModel string
	var gotImageURLs int
	var reqCount int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					ImageURL *struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		reqCount++
		if reqCount > 1 {
			// 仅首个请求统计图片（agent 工具循环后续请求是历史重发，
			// 图片已随历史透传——跨请求累计会让断言失真）
			mu.Unlock()
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"图里有一只猫\"},\"finish_reason\":\"stop\"}]}\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		gotModel = body.Model
		for _, m := range body.Messages {
			for _, c := range m.Content {
				if c.Type == "image_url" && c.ImageURL != nil {
					gotImageURLs++
				}
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		// 标准 DeepSeek 流式格式：finish_reason 在最后一个 chunk 的
		// choices[0] 顶层（旧 mock 把它放 choices[1]——解析只取 choices[0]，
		// 结束信号永远收不到 → agent 误判截断循环 3 轮重发请求）。
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"图里有一只猫\"},\"finish_reason\":\"stop\"}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	cfg := testConfig(tmpDir)
	cfg.Models[0].Backend = "deepseek"
	cfg.Models[0].Endpoint = srv.URL
	cfg.Models[0].APIKey = "sk-e2e"
	cfg.Models[0].VisionModel = "vision-model-x"
	cfg.Models[0].SupportsVision = true // 视觉端点前置条件
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}

	// 模拟 TUI 发送链路：先写历史，再 ProcessInput
	rt.AddToHistory("user", "看看 "+p+" 里是什么")
	ctx := context.Background()
	ch, errCh, err := rt.ProcessInput(ctx, "看看 "+p+" 里是什么")
	if err != nil {
		t.Fatalf("ProcessInput 返回错误: %v", err)
	}
	var out strings.Builder
	for tok := range ch {
		out.WriteString(tok)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("流错误: %v", e)
	}
	if !strings.Contains(out.String(), "猫") {
		t.Errorf("响应应包含模型输出: %q", out.String())
	}

	mu.Lock()
	model := gotModel
	imgs := gotImageURLs
	mu.Unlock()
	if model != "vision-model-x" {
		t.Errorf("请求 model = %q，期望 vision-model-x（图片轮自动切换）", model)
	}
	if imgs != 1 {
		t.Errorf("请求应含 1 个 image_url，实际 %d（历史多模态透传）", imgs)
	}
	if rt.Backend.ModelID() != "deepseek" {
		t.Errorf("流结束后应恢复主模型，实际 %q", rt.Backend.ModelID())
	}
}

// TestAddToHistory_NoVisionSupport_PlainText 目标模型不支持多模态
// （DeepSeek 官方默认，supports_vision=false）：含图片路径的 user 消息
// 历史保持纯文本（路径文本原样保留），绝不产生 image_url（防 API 400）。
func TestAddToHistory_NoVisionSupport_PlainText(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig(tmpDir)
	cfg.Models[0].SupportsVision = false // DeepSeek 官方默认
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	p := writePNG(t, tmpDir, "shot.png")

	rt.AddToHistory("user", "看下 "+p+" 里有什么")
	hist := rt.GetConversationHistory()
	if len(hist) != 1 {
		t.Fatalf("历史长度 = %d，期望 1", len(hist))
	}
	msg := hist[0]
	if msg.ImagePartCount() != 0 {
		t.Fatalf("不支持多模态时历史不得含图片片段: %#v", msg.Content)
	}
	s, ok := msg.Content.(string)
	if !ok || !strings.Contains(s, "shot.png") {
		t.Errorf("历史应为含路径的纯文本，实际 %#v", msg.Content)
	}
}

// TestBuildHistoryMessage_SupportsVisionGate 能力门：不支持→纯文本；
// 支持→多模态。二者共享同一输入与图片文件，验证唯一变量是配置开关。
func TestBuildHistoryMessage_SupportsVisionGate(t *testing.T) {
	tmpDir := t.TempDir()
	p := writePNG(t, tmpDir, "shot.png")
	input := "分析 " + p

	// 不支持多模态 → 纯文本
	cfgOff := testConfig(tmpDir)
	rtOff, err := New(cfgOff)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	msgOff, evOff := rtOff.buildHistoryMessage("user", input)
	if msgOff.ImagePartCount() != 0 {
		t.Fatalf("不支持时历史消息不得含图片: %#v", msgOff.Content)
	}
	if evOff != input {
		t.Errorf("不支持时事件日志应为原文，实际 %q", evOff)
	}

	// 支持多模态 → 图片片段 + [图片: 路径] 事件标记
	cfgOn := testConfig(tmpDir)
	cfgOn.Models[0].SupportsVision = true
	rtOn, err := New(cfgOn)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	msgOn, evOn := rtOn.buildHistoryMessage("user", input)
	if msgOn.ImagePartCount() != 1 {
		t.Fatalf("支持时历史消息应含 1 张图片: %#v", msgOn.Content)
	}
	if !strings.Contains(evOn, "[图片: "+p+"]") {
		t.Errorf("支持时事件日志应含图片标记，实际 %q", evOn)
	}
}

// TestStageImages_StructuredVisionChain GUI 图片上传结构化直传全链路：
// StageImages 暂存（dataURL 解码 vision.Image）→ AddToHistory 历史写入多模态
// （多轮追问图片仍在上下文）→ ProcessInput 消费暂存图片 → 请求体含 OpenAI
// 标准 image_url（data URI）且不带任何 "[图片: 路径]" 文本垃圾。
func TestStageImages_StructuredVisionChain(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "contextloom-stageimg-*")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	var mu sync.Mutex
	var gotModel string
	var gotTexts []string
	var gotImageURLs int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type     string `json:"type"`
					Text     string `json:"text"`
					ImageURL *struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		gotModel = body.Model
		for _, m := range body.Messages {
			for _, c := range m.Content {
				if c.Type == "image_url" && c.ImageURL != nil {
					gotImageURLs++
				}
				if c.Type == "text" {
					gotTexts = append(gotTexts, c.Text)
				}
			}
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"图中有一只猫\"},\"finish_reason\":\"stop\"}]}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	cfg := testConfig(tmpDir)
	cfg.Models[0].Backend = "deepseek"
	cfg.Models[0].Endpoint = srv.URL
	cfg.Models[0].APIKey = "sk-e2e"
	cfg.Models[0].VisionModel = "vision-model-x"
	cfg.Models[0].SupportsVision = true
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}

	// 模拟 GUI chat.send：dataURL 解码（decodeImageAttachment 同语义）→ StageImages
	rawPNG := []byte("\x89PNG\r\n\x1a\nfake-image-body")
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawPNG)
	imgFile := filepath.Join(tmpDir, "shot.png")
	if err := os.WriteFile(imgFile, rawPNG, 0o644); err != nil {
		t.Fatal(err)
	}
	rt.StageImages([]vision.Image{{
		Path:    imgFile,
		MIME:    "image/png",
		Size:    int64(len(rawPNG)),
		DataURI: dataURL,
	}})

	// GUI 先 AddToHistory 写用户消息（input 仅含计数标记，无路径文本）
	rt.AddToHistory("user", "[图片×1]\n\n这张图里有什么")
	hist := rt.GetConversationHistory()
	if len(hist) != 1 || hist[0].ImagePartCount() != 1 {
		t.Fatalf("历史应含 1 张结构化图片: %#v", hist)
	}

	// 再 ProcessInput（本轮消费暂存图片 → 识图模型切换）
	ch, errCh, err := rt.ProcessInput(context.Background(), "[图片×1]\n\n这张图里有什么")
	if err != nil {
		t.Fatalf("ProcessInput 返回错误: %v", err)
	}
	var out strings.Builder
	for tok := range ch {
		out.WriteString(tok)
	}
	if e := <-errCh; e != nil {
		t.Fatalf("流错误: %v", e)
	}
	if !strings.Contains(out.String(), "猫") {
		t.Errorf("响应应包含模型输出: %q", out.String())
	}

	mu.Lock()
	model := gotModel
	imgs := gotImageURLs
	texts := gotTexts
	mu.Unlock()
	if model != "vision-model-x" {
		t.Errorf("请求 model = %q，期望 vision-model-x（暂存图片轮自动切换）", model)
	}
	if imgs < 1 {
		t.Errorf("请求应至少含 1 个 image_url，实际 %d", imgs)
	}
	for _, tx := range texts {
		if strings.Contains(tx, "[图片: ") {
			t.Errorf("请求文本不得含 [图片: 路径] 垃圾标记: %q", tx)
		}
	}
	if rt.Backend.ModelID() != "deepseek" {
		t.Errorf("流结束后应恢复主模型，实际 %q", rt.Backend.ModelID())
	}
}

// TestStageImages_ConsumeOnce 暂存图片消费语义：ProcessInput 消费后清空，
// 不跨轮错位（未消费的暂存图片随下一次输入自然废弃）。
func TestStageImages_ConsumeOnce(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig(tmpDir)
	cfg.Models[0].SupportsVision = true
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	img := vision.Image{Path: filepath.Join(tmpDir, "a.png"), MIME: "image/png", DataURI: "data:image/png;base64,QUJD"}
	rt.StageImages([]vision.Image{img})
	if got := rt.ConsumeStagedImages(); len(got) != 1 {
		t.Fatalf("应消费到 1 张，实际 %d", len(got))
	}
	if got := rt.ConsumeStagedImages(); len(got) != 0 {
		t.Fatalf("消费后应清空，实际 %d", len(got))
	}

	// Peek 不消费：副本可取，本体仍保留给下一次 Consume
	rt.StageImages([]vision.Image{img})
	if got := rt.PeekStagedImages(); len(got) != 1 {
		t.Fatalf("Peek 应返回 1 张，实际 %d", len(got))
	}
	if got := rt.ConsumeStagedImages(); len(got) != 1 {
		t.Fatalf("Peek 后 Consume 仍应消费到 1 张，实际 %d", len(got))
	}
}

// TestDetectImages_StagedVSPathDedup 同图双通道去重：同一张图片既经暂存
// （GUI 上传压缩版）又被输入文本按路径扫描命中（历史/多轮场景）时，
// detectTaskImages 只保留一份 image_url——staged 优先（压缩版）。
// 回归用户实锤："原图和压缩后的图片竟然一起发给了 AI 接口，识别出两个图片"。
func TestDetectImages_StagedVSPathDedup(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig(tmpDir)
	cfg.Models[0].SupportsVision = true
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	p := writePNG(t, tmpDir, "shot.png")

	// 模拟 GUI 上传：StageImages 暂存压缩版（DataURI 与文件内容不同——
	// 反正是压缩后字节；Path 指向同一文件）
	stagedURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("compressed-bytes"))
	rt.StageImages([]vision.Image{{
		Path:    p,
		MIME:    "image/png",
		Size:    20,
		DataURI: stagedURI,
	}})

	// 用户文本同时提到该路径（历史恢复重读同一文件 → 文本扫描也会读到）
	parts, err := rt.detectTaskImages("看看 " + p + " 里有什么")
	if err != nil {
		t.Fatalf("detectTaskImages 错误: %v", err)
	}
	if len(parts) != 1 {
		t.Fatalf("同图双通道应合并为 1 个 image_url，实际 %d 个", len(parts))
	}
	if parts[0].ImageURL == nil || parts[0].ImageURL.URL != stagedURI {
		t.Errorf("应保留 staged 压缩版 URI，实际 %+v", parts[0].ImageURL)
	}
	// 暂存已被消费（本轮），下一轮无残留
	if got := rt.ConsumeStagedImages(); len(got) != 0 {
		t.Errorf("暂存应已被消费清空，实际 %d", len(got))
	}
}

// TestDedupeImages_SamePathOnce 去重单元：同路径/同 URI 只保留一份。
func TestDedupeImages_SamePathOnce(t *testing.T) {
	base := filepath.Join(t.TempDir(), "a.png")
	imgs := []vision.Image{
		{Path: base + "/x.png", DataURI: "u1"},
		{Path: base + "/x.png", DataURI: "u2"}, // 同路径 → 去重
		{Path: "", DataURI: "u3"},
		{Path: "", DataURI: "u3"}, // 同 URI → 去重
		{Path: "", DataURI: "u4"},
	}
	out := dedupeImages(imgs)
	if len(out) != 3 {
		t.Fatalf("应剩 3 张（去重后），实际 %d: %+v", len(out), out)
	}
	if out[0].DataURI != "u1" {
		t.Errorf("应保留首次出现者 u1，实际 %s", out[0].DataURI)
	}
}

// TestGatewayPath_VisionCapabilityFromPreset 端到端回归：配置了 gateway: 节
// （真实 config.yaml 的形态）且预设 supports_vision=true 时，网关路由路径
// 选出的后端必须报告 SupportsVision()==true。
//
// 用户报障：使用 deepseek（配置内 supports_vision: true）识图仍提示"不支持"。
// 根因：网关池建实例时漏注入能力标志 → handle.SupportsVision() 恒 false →
// prepareVision 误判为纯文本模型而降级。此测试锁定"配置优先"。
func TestGatewayPath_VisionCapabilityFromPreset(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := testConfig(tmpDir)
	cfg.Models[0].SupportsVision = true // 配置内显式开启（唯一事实来源）
	// 配置 gateway 节：default 池同名预设 "mock" → 桥接连接与能力参数
	cfg.Gateway = modelgateway.GatewayConfig{
		Default: "default",
		Pools: map[string]modelgateway.PoolConfig{
			"mock": {MaxConcurrent: 2},
		},
	}
	rt, err := New(cfg)
	if err != nil {
		t.Fatalf("创建 runtime 失败: %v", err)
	}
	if rt.Gateway == nil {
		t.Fatal("配置了 gateway 节，网关应启用")
	}
	// 网关路径选后端（识图任务 → Task=vision）
	b := rt.selectBackend(context.Background(), modelgateway.Req{Task: "vision"})
	if b == nil {
		t.Fatal("selectBackend 不应返回 nil")
	}
	if !b.SupportsVision() {
		t.Fatalf("网关路径后端应报告 SupportsVision()=true（配置 supports_vision=true 优先），"+
			"实际 false——识图会被误降级为'不支持视觉输入'。类型 %T", b)
	}
}
