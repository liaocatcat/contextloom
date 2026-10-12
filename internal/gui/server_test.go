package gui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/internal/config"
	"github.com/contextloom/contextloom-agent/internal/vision"
	"github.com/contextloom/contextloom-agent/pkg/types"
	"github.com/gorilla/websocket"
)

// fakeRuntime 实现 Runtime 接口的最小桩（并发安全：GUI 服务将方法调用
// 分散在多个 goroutine——handleReq/streamChat/status 轮询，race 测试要求桩线程安全）。
type fakeRuntime struct {
	mu           sync.Mutex
	mode         string
	sessionID    string
	sessions     []*types.Session
	msgs         []types.Message
	lastInput    string
	activeSkills []string
	projectRoot  string
	cfgPath      string
	lastPatch    map[string]any
	patchErr     error
	offSess      []offMsg       // 非当前会话的历史写入记录（模拟事件日志）
	staged       []vision.Image // StageImages 收到的图片（测试断言用）
	streamDelay  time.Duration  // 流式输出间隔（测试可控；0=默认 5ms）
	// v4 目标循环桩
	goalSession       *types.GoalSession
	goalContinueTimes int // >0：轮末评估返回继续（每次递减）；0：返回完成
	goalRoundEnds     int
	goalRounds        int
	goalRoundOutput   []string // ProcessGoalRound 每轮输出（无则立即结束）
	// v5 多模型预设桩
	switchedModel string
	removedModel  string
}

// offMsg 记录 AddToHistoryForSession 写入非当前会话的历史（测试断言用）。
type offMsg struct {
	SID     string
	Role    string
	Content string
}

func (f *fakeRuntime) ListSessions() []*types.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sessions == nil {
		return []*types.Session{{UUID: "u1", Title: "会话一"}}
	}
	return f.sessions
}
func (f *fakeRuntime) LoadSession(uuid string) (*types.Session, *types.Session, error) {
	s := &types.Session{UUID: uuid, Title: "会话_" + uuid}
	f.mu.Lock()
	f.sessionID = uuid
	f.mu.Unlock()
	return s, s, nil
}
func (f *fakeRuntime) NewSession(input string) *types.Session {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessionID = "new-" + input
	s := &types.Session{UUID: f.sessionID, Title: "新会话"}
	f.sessions = append(f.sessions, s)
	return s
}
func (f *fakeRuntime) GetSessionID() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sessionID
}
func (f *fakeRuntime) AddToHistory(role, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, types.Message{Role: role, Content: content})
}
func (f *fakeRuntime) AddToHistoryForSession(sid, role, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 桩语义与 runtime 一致：sid 为空或等于当前会话 → 写内存历史；
	// 否则仅记录（模拟事件日志写入，不污染当前会话内存历史）
	if sid == "" || sid == f.sessionID {
		f.msgs = append(f.msgs, types.Message{Role: role, Content: content})
		return
	}
	f.offSess = append(f.offSess, offMsg{SID: sid, Role: role, Content: content})
}
func (f *fakeRuntime) ProcessInput(ctx context.Context, input string) (<-chan string, <-chan error, error) {
	f.mu.Lock()
	f.lastInput = input
	f.mu.Unlock()
	ch := make(chan string, 3)
	errCh := make(chan error, 1)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, part := range []string{"你", "好", "世界"} {
			select {
			case <-ctx.Done():
				return
			case ch <- part:
			}
			d := f.streamDelay
			if d <= 0 {
				d = 5 * time.Millisecond
			}
			time.Sleep(d)
		}
	}()
	return ch, errCh, nil
}
func (f *fakeRuntime) ConfigSummary() string { return "配置摘要" }
func (f *fakeRuntime) StageImages(images []vision.Image) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.staged = append(f.staged, images...)
}
func (f *fakeRuntime) GetMode() string                         { return f.mode }
func (f *fakeRuntime) SetMode(m string)                        { f.mode = m }
func (f *fakeRuntime) ContextUsage() string                    { return "ctx 10% (1k/100k)" }
func (f *fakeRuntime) ContextUsageInfo() (int, int)            { return 1000, 100000 }
func (f *fakeRuntime) GoalStatus() string                      { return "无活动目标" }
func (f *fakeRuntime) GetConversationHistory() []types.Message { return f.historySnapshot() }
func (f *fakeRuntime) GetDisplayHistory(sid string) []types.Message {
	// 测试桩：与 GetConversationHistory 一致（fake 不模拟压缩快照差异，
	// 压缩/展示差异由 runtime 层真实实现 + runtime_test 覆盖）。
	return f.historySnapshot()
}

// historySnapshot 返回历史消息副本（测试断言用，线程安全）。
func (f *fakeRuntime) historySnapshot() []types.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]types.Message(nil), f.msgs...)
}

// lastInputValue 返回最近一次 ProcessInput 的输入（测试断言用，线程安全）。
func (f *fakeRuntime) lastInputValue() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastInput
}

func (f *fakeRuntime) PersistencePaths() map[string]string {
	return map[string]string{"memory": "/tmp/.contextloom/memory"}
}

// ── v2 扩展方法（接口补齐）──
func (f *fakeRuntime) DeleteSession(uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.sessions {
		if s.UUID == uuid {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			// 与 runtime 行为对齐：允许删除当前活跃会话，删除后回到无会话状态
			if f.sessionID == uuid {
				f.sessionID = ""
				f.msgs = nil
			}
			return nil
		}
	}
	return fmt.Errorf("会话不存在")
}
func (f *fakeRuntime) UpdateSessionTitle(uuid, userInput string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.UUID == uuid {
			if len([]rune(userInput)) > 50 {
				s.Title = string([]rune(userInput)[:50])
			} else {
				s.Title = userInput
			}
			return
		}
	}
}
func (f *fakeRuntime) RenameSession(uuid, title string) error {
	for _, s := range f.sessions {
		if s.UUID == uuid {
			s.Title = title
			return nil
		}
	}
	return fmt.Errorf("会话不存在")
}
func (f *fakeRuntime) ProjectRoot() string {
	if f.projectRoot != "" {
		return f.projectRoot
	}
	return "/tmp/contextloom-gui-test"
}
func (f *fakeRuntime) ModelInfo() map[string]any {
	return map[string]any{"backend": "deepseek", "model": "default-model", "effort": "max", "maxContext": 131072}
}
func (f *fakeRuntime) GoalStatusDetail() map[string]any {
	return map[string]any{"active": false, "phase": "", "round": 0, "totalSteps": 0, "doneSteps": 0, "pendingSteps": []string{}, "score": 0.0, "passed": false}
}
func (f *fakeRuntime) ListSkills() []*types.Skill     { return nil }
func (f *fakeRuntime) GetActiveSkills() []string      { return f.activeSkills }
func (f *fakeRuntime) SetActiveSkills(names []string) { f.activeSkills = names }
func (f *fakeRuntime) PluginsList() string            { return "无插件" }
func (f *fakeRuntime) SetEffort(effort string)        {}
func (f *fakeRuntime) ContextStats() string           { return "统计" }
func (f *fakeRuntime) CompressContext() string {
	return "上下文已压缩：截断 2 条 / 丢弃 3 条，释放 5000 tokens（60% → 12%）"
}
func (f *fakeRuntime) ConfigSnapshot() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	vals := map[string]any{"model.backend": "default-model", "model.api_key": "sk-****abcd"}
	if f.cfgPath != "" {
		vals["_path"] = f.cfgPath
	}
	return map[string]any{"values": vals, "schema": []map[string]any{{"key": "model.backend", "type": "text"}}, "path": f.cfgPath}
}
func (f *fakeRuntime) ApplyConfigPatch(patch map[string]any) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastPatch = patch
	return []string{"acp.port"}, f.patchErr
}

// ── v4 目标循环桩（goal 自动续跑测试用）──

func (f *fakeRuntime) GoalSession() *types.GoalSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.goalSession
}

func (f *fakeRuntime) HasOverseer() bool { return false }

func (f *fakeRuntime) OverseerEvaluate(response string) (bool, types.Score, types.OverseerReport) {
	return true, types.Score(100), types.OverseerReport{}
}

func (f *fakeRuntime) GoalOnRoundEnd(response string, passed bool, score types.Score, report types.OverseerReport) (bool, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goalRoundEnds++
	if f.goalContinueTimes > 0 {
		f.goalContinueTimes--
		return true, "测试桩继续下一轮"
	}
	return false, "测试桩目标完成"
}

func (f *fakeRuntime) ProcessGoalRound(ctx context.Context) (<-chan string, <-chan error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.goalRounds++
	ch := make(chan string)
	errCh := make(chan error)
	go func() {
		defer close(ch)
		defer close(errCh)
		for _, s := range f.goalRoundOutput {
			ch <- s
		}
	}()
	return ch, errCh, nil
}

// v5 多模型预设桩
func (f *fakeRuntime) ModelNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []string{"default-model", "gpt-4o"}
}

func (f *fakeRuntime) CurrentModel() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "default-model"
}

func (f *fakeRuntime) SwitchModel(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchedModel = name
	return nil
}

func (f *fakeRuntime) AddModelPreset(p config.ModelPreset) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.switchedModel = p.Name
	return nil
}

func (f *fakeRuntime) RemoveModelPreset(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		return fmt.Errorf("模型名称不能为空")
	}
	f.removedModel = name
	return nil
}

// startTestServer 启动真实 HTTP server（httptest），返回 ws 地址与 server。
func startTestServer(t *testing.T, rt Runtime) (*httptest.Server, *Server) {
	t.Helper()
	srv := NewServer(rt, 0, "test-token")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/ws") {
			srv.handleWS(w, r)
			return
		}
		srv.handleIndex(w, r)
	}))
	t.Cleanup(ts.Close)
	return ts, srv
}

// dialWS 建立带 token 的 WS 连接。
func dialWS(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?token=test-token"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("WS 连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// sendReq 发送请求并等待对应 id 的响应。
func sendReq(t *testing.T, conn *websocket.Conn, id, method string, params any) Envelope {
	t.Helper()
	env := Envelope{ID: id, Type: "req", Method: method}
	if params != nil {
		b, _ := json.Marshal(params)
		env.Params = b
	}
	if err := conn.WriteJSON(env); err != nil {
		t.Fatalf("发送请求失败: %v", err)
	}
	for {
		var resp Envelope
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("读取响应失败: %v", err)
		}
		if resp.ID == id {
			return resp
		}
	}
}

func TestWS_Ping(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "ping", nil)
	if !resp.OK {
		t.Fatalf("ping 失败: %+v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("ping 结果类型错误: %T", resp.Result)
	}
	if m["version"] == "" {
		t.Fatal("ping 缺少 version")
	}
}

func TestWS_Unauthorized(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws?token=wrong"
	_, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err == nil {
		t.Fatal("错误 token 应拒绝连接")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("应返回 403，实际: %v", resp)
	}
}

func TestWS_ListSessions(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "2", "chat.listSessions", nil)
	if !resp.OK {
		t.Fatalf("listSessions 失败: %+v", resp.Error)
	}
	list, ok := resp.Result.([]any)
	if !ok || len(list) == 0 {
		t.Fatalf("会话列表为空或类型错误: %T", resp.Result)
	}
	first := list[0].(map[string]any)
	if first["uuid"] != "u1" || first["title"] != "会话一" {
		t.Fatalf("会话字段错误: %+v", first)
	}
}

func TestWS_LoadSession(t *testing.T) {
	rt := &fakeRuntime{msgs: []types.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！"},
	}}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "3", "chat.loadSession", map[string]any{"uuid": "abc"})
	if !resp.OK {
		t.Fatalf("loadSession 失败: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("消息数错误: %+v", m["messages"])
	}
}

func TestWS_NewSession(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "4", "chat.newSession", map[string]any{"title": "测试"})
	if !resp.OK {
		t.Fatalf("newSession 失败: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["uuid"] != "new-测试" {
		t.Fatalf("新建会话 uuid 错误: %+v", m)
	}
}

// 无当前会话（GetSessionID 返回空串）时，chat.currentSession 返回 uuid 为空。
func TestWS_CurrentSession_NoSession(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "5", "chat.currentSession", nil)
	if !resp.OK {
		t.Fatalf("currentSession 失败: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if uuid, _ := m["uuid"].(string); uuid != "" {
		t.Fatalf("无会话时应返回空 uuid: %+v", m)
	}
}

// 有当前会话时，chat.currentSession 返回 uuid/title/mode 正确，
// 且 messages 与 GetConversationHistory 一致。
func TestWS_CurrentSession_WithSession(t *testing.T) {
	rt := &fakeRuntime{msgs: []types.Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！"},
	}}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	// 先加载会话使 fakeRuntime.sessionID 非空
	resp := sendReq(t, conn, "5", "chat.loadSession", map[string]any{"uuid": "abc"})
	if !resp.OK {
		t.Fatalf("loadSession 失败: %+v", resp.Error)
	}
	resp = sendReq(t, conn, "6", "chat.currentSession", nil)
	if !resp.OK {
		t.Fatalf("currentSession 失败: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["uuid"] != "abc" {
		t.Fatalf("uuid 错误: %+v", m)
	}
	if m["title"] != "会话_abc" {
		t.Fatalf("title 错误: %+v", m)
	}
	if m["mode"] != rt.GetMode() {
		t.Fatalf("mode 错误: got %v, want %v", m["mode"], rt.GetMode())
	}
	msgs, ok := m["messages"].([]any)
	if !ok || len(msgs) != 2 {
		t.Fatalf("messages 错误: %+v", m["messages"])
	}
	// messages 与 GetConversationHistory 一致
	hist := rt.historySnapshot()
	if len(hist) != len(msgs) {
		t.Fatalf("messages 与历史不一致: got %d, want %d", len(msgs), len(hist))
	}
	for i, h := range hist {
		jm := msgs[i].(map[string]any)
		if jm["role"] != h.Role || jm["content"] != h.Content {
			t.Fatalf("messages[%d] 与历史不一致: got %+v, want %+v", i, jm, h)
		}
	}
}

// readUntilDone 读取 WS 事件直到 agent.done（跳过 accepted 响应）。
func readUntilDone(t *testing.T, conn *websocket.Conn, id string) {
	t.Helper()
	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if env.Type == "event" && env.Event == "agent.done" {
			return
		}
	}
}

// 回归测试：GUI 多轮对话上下文。
// 根因（历史缺陷）：chat.send 路径从不写 conversationHistory（TUI 在外部写、
// GUI 没有），且 GetSessionID 恒返回空串 → 第二次对话 AI 看不到用户消息。
// 修复后：send 前写 user、流结束后写 assistant、无会话自动创建。
func TestWS_ChatSend_MultiTurnContext(t *testing.T) {
	rt := &fakeRuntime{}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// 第一轮（无会话：应自动创建）
	resp := sendReq(t, conn, "10", "chat.send", map[string]any{"input": "第一问", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("第一轮 chat.send 失败: %+v", resp.Error)
	}
	if rt.GetSessionID() == "" {
		t.Fatal("无会话时 chat.send 应自动创建会话")
	}
	if len(rt.historySnapshot()) != 1 || rt.historySnapshot()[0].Role != "user" || rt.historySnapshot()[0].Content != "第一问" {
		t.Fatalf("第一轮用户消息未写入历史: %+v", rt.historySnapshot())
	}
	readUntilDone(t, conn, "10")
	if len(rt.historySnapshot()) != 2 || rt.historySnapshot()[1].Role != "assistant" || rt.historySnapshot()[1].Content != "你好世界" {
		t.Fatalf("第一轮 assistant 回复未写入历史: %+v", rt.historySnapshot())
	}

	// 第二轮（携带第一轮的 sessionId，模拟真实前端行为）
	sid := rt.GetSessionID()
	resp = sendReq(t, conn, "11", "chat.send", map[string]any{"input": "第二问", "sessionId": sid})
	if !resp.OK {
		t.Fatalf("第二轮 chat.send 失败: %+v", resp.Error)
	}
	// 第二轮发送时历史必须已含第一轮 user+assistant（多轮上下文完整）
	if len(rt.historySnapshot()) != 3 || rt.historySnapshot()[2].Role != "user" || rt.historySnapshot()[2].Content != "第二问" {
		t.Fatalf("第二轮用户消息写入时历史不完整: %+v", rt.historySnapshot())
	}
	readUntilDone(t, conn, "11")
	if len(rt.historySnapshot()) != 4 || rt.historySnapshot()[3].Role != "assistant" {
		t.Fatalf("第二轮 assistant 回复未写入历史: %+v", rt.historySnapshot())
	}
	// 顺序校验：user/assistant/user/assistant
	expect := []struct{ role, content string }{
		{"user", "第一问"}, {"assistant", "你好世界"},
		{"user", "第二问"}, {"assistant", "你好世界"},
	}
	for i, e := range expect {
		if rt.historySnapshot()[i].Role != e.role || rt.historySnapshot()[i].Content != e.content {
			t.Fatalf("历史顺序错乱 [%d]: got %+v, want %+v", i, rt.historySnapshot()[i], e)
		}
	}
}

// 回归测试：agent.done 必须携带有效 sessionId（此前 GetSessionID 恒空串导致
// 前端永远无法跟踪会话、第二次发送找不到上下文）。
func TestWS_ChatSend_DoneCarriesSessionID(t *testing.T) {
	rt := &fakeRuntime{}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "12", "chat.send", map[string]any{"input": "你好", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if env.Type != "event" || env.Event != "agent.done" {
			continue
		}
		data, ok := env.Data.(map[string]any)
		if !ok {
			t.Fatalf("agent.done 数据格式错误: %T", env.Data)
		}
		sid, _ := data["sessionId"].(string)
		if sid == "" {
			t.Fatalf("agent.done 缺少有效 sessionId: %+v", data)
		}
		if sid != rt.GetSessionID() {
			t.Fatalf("agent.done sessionId 与当前会话不一致: got %q want %q", sid, rt.GetSessionID())
		}
		return
	}
}

func TestWS_ModeSet(t *testing.T) {
	rt := &fakeRuntime{mode: "chat"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	for _, mode := range []string{"task", "goal", "chat"} {
		resp := sendReq(t, conn, "m"+mode, "mode.set", map[string]any{"mode": mode})
		if !resp.OK {
			t.Fatalf("mode.set %s 失败: %+v", mode, resp.Error)
		}
		if rt.GetMode() != mode {
			t.Fatalf("模式未切换: want %s got %s", mode, rt.GetMode())
		}
	}
	// 非法模式
	resp := sendReq(t, conn, "bad", "mode.set", map[string]any{"mode": "xxx"})
	if resp.OK {
		t.Fatal("非法模式应报错")
	}
}

func TestWS_UnknownMethod(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "5", "no.such", nil)
	if resp.OK {
		t.Fatal("未知方法应报错")
	}
	if resp.Error == nil || !strings.Contains(resp.Error.Msg, "未知方法") {
		t.Fatalf("错误信息不符: %+v", resp.Error)
	}
}

func TestWS_ChatSendStreamsEvents(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	// 发送 chat.send，异步等待事件
	if err := conn.WriteJSON(Envelope{ID: "s1", Type: "req", Method: "chat.send",
		Params: json.RawMessage(`{"input":"你好"}`)}); err != nil {
		t.Fatal(err)
	}
	var gotDelta strings.Builder
	var done bool
	deadline := time.After(5 * time.Second)
	for !done {
		select {
		case <-deadline:
			t.Fatal("等待 agent.done 超时")
		default:
		}
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		switch env.Event {
		case "agent.stream":
			if d, ok := env.Data.(map[string]any); ok {
				gotDelta.WriteString(fmt.Sprintf("%v", d["delta"]))
			}
		case "agent.done":
			done = true
		case "":
			// 可能是 resp（accepted）
		}
	}
	if gotDelta.String() != "你好世界" {
		t.Fatalf("流式 delta 不完整: %q", gotDelta.String())
	}
}

func TestWS_StatusBar(t *testing.T) {
	rt := &fakeRuntime{mode: "goal"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "6", "status.bar", nil)
	if !resp.OK {
		t.Fatalf("status.bar 失败: %+v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["mode"] != "goal" {
		t.Fatalf("mode 错误: %+v", m)
	}
	if m["ctx"] == "" || m["goal"] == "" {
		t.Fatalf("status 字段缺失: %+v", m)
	}
	// ctxRaw：前端精确渲染进度条/流式估算基准的原始数值（状态栏同步的
	// 数据基础——只有字符串时前端无法精确累计流式增量）
	raw, ok := m["ctxRaw"].(map[string]any)
	if !ok {
		t.Fatalf("ctxRaw 缺失: %+v", m)
	}
	if raw["used"] != float64(1000) || raw["max"] != float64(100000) {
		t.Fatalf("ctxRaw 数值错误: %+v", raw)
	}
	// model：设置面板保存 config.set 后状态栏模型信息随轮询同步的关键字段
	// （#modelInfo = backend · effort，任何路径改设置都会自动同步）。
	mdl, ok := m["model"].(map[string]any)
	if !ok {
		t.Fatalf("model 缺失: %+v", m)
	}
	if mdl["backend"] != "deepseek" || mdl["effort"] != "max" {
		t.Fatalf("model 字段错误: %+v", mdl)
	}
}

// TestWS_ChatSend_PushesCtxUsage 验证流式期间/结束推送 ctx.usage 事件
// （GUI 状态栏"同步改变"的核心：流式对话中进度条实时增长，而非等 2s 轮询）。
func TestWS_ChatSend_PushesCtxUsage(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	if err := conn.WriteJSON(Envelope{ID: "s1", Type: "req", Method: "chat.send",
		Params: json.RawMessage(`{"input":"你好"}`)}); err != nil {
		t.Fatal(err)
	}
	var gotDelta strings.Builder
	var gotUsage bool
	var done bool
	deadline := time.After(5 * time.Second)
	for !done {
		select {
		case <-deadline:
			t.Fatal("等待 agent.done 超时")
		default:
		}
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		switch env.Event {
		case "agent.stream":
			if d, ok := env.Data.(map[string]any); ok {
				gotDelta.WriteString(fmt.Sprintf("%v", d["delta"]))
			}
		case "ctx.usage":
			d, ok := env.Data.(map[string]any)
			if !ok {
				t.Fatalf("ctx.usage 数据格式错误: %+v", env.Data)
			}
			gotUsage = true
			if d["max"] == nil || d["used"] == nil {
				t.Fatalf("ctx.usage 缺 used/max: %+v", d)
			}
			if d["streaming"] == nil {
				t.Fatalf("ctx.usage 缺 streaming: %+v", d)
			}
		case "agent.done":
			done = true
		case "":
			// 可能是 resp（accepted）
		}
	}
	if gotDelta.String() != "你好世界" {
		t.Fatalf("流式 delta 不完整: %q", gotDelta.String())
	}
	if !gotUsage {
		t.Fatal("流结束前必须推送 ctx.usage 事件（状态栏同步机制缺失）")
	}
}

func TestIndex_RejectsBadToken(t *testing.T) {
	srv := NewServer(&fakeRuntime{}, 0, "secret")
	req := httptest.NewRequest("GET", "/?token=bad", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("错误 token 应 403，实际 %d", w.Code)
	}
}

func TestIndex_GoodTokenServesPage(t *testing.T) {
	srv := NewServer(&fakeRuntime{}, 0, "secret")
	req := httptest.NewRequest("GET", "/?token=secret", nil)
	w := httptest.NewRecorder()
	srv.handleIndex(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("正确 token 应 200，实际 %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "ContextLoom GUI") {
		t.Fatalf("页面内容缺失: %s", w.Body.String()[:100])
	}
}

func TestRandomToken_Unique(t *testing.T) {
	a, b := randomToken(), randomToken()
	if a == "" || a == b {
		t.Fatalf("token 应唯一非空: %q %q", a, b)
	}
	if len(a) != 64 {
		t.Fatalf("token 长度应为 64（32 字节 hex），实际 %d", len(a))
	}
}

// ── v2 扩展方法测试（契约 CONTRACT.md 第一节）──

// fsRuntime 项目根指向临时目录的 fakeRuntime。
type fsRuntime struct {
	fakeRuntime
	root string
}

func (f *fsRuntime) ProjectRoot() string { return f.root }

func TestWS_FS_ListReadWriteMkdirRenameDelete(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ts, _ := startTestServer(t, &fsRuntime{fakeRuntime: fakeRuntime{}, root: root})
	conn := dialWS(t, ts)

	// fs.list
	resp := sendReq(t, conn, "1", "fs.list", map[string]any{"path": ""})
	if !resp.OK {
		t.Fatalf("fs.list 失败: %v", resp.Error)
	}
	entries := resp.Result.(map[string]any)["entries"].([]any)
	if len(entries) == 0 {
		t.Fatal("fs.list 应有文件")
	}
	// fs.read
	resp = sendReq(t, conn, "2", "fs.read", map[string]any{"path": "a.txt"})
	if !resp.OK {
		t.Fatalf("fs.read 失败: %v", resp.Error)
	}
	if got := resp.Result.(map[string]any)["content"].(string); got != "hello" {
		t.Fatalf("fs.read content=%q 期望 hello", got)
	}
	// fs.write
	resp = sendReq(t, conn, "3", "fs.write", map[string]any{"path": "b.txt", "content": "world"})
	if !resp.OK {
		t.Fatalf("fs.write 失败: %v", resp.Error)
	}
	data, err := os.ReadFile(filepath.Join(root, "b.txt"))
	if err != nil || string(data) != "world" {
		t.Fatalf("fs.write 未落盘: %v %q", err, data)
	}
	// fs.mkdir
	resp = sendReq(t, conn, "4", "fs.mkdir", map[string]any{"path": "sub/dir"})
	if !resp.OK {
		t.Fatalf("fs.mkdir 失败: %v", resp.Error)
	}
	if fi, err := os.Stat(filepath.Join(root, "sub", "dir")); err != nil || !fi.IsDir() {
		t.Fatalf("fs.mkdir 未创建目录: %v", err)
	}
	// fs.rename
	resp = sendReq(t, conn, "5", "fs.rename", map[string]any{"oldPath": "b.txt", "newPath": "c.txt"})
	if !resp.OK {
		t.Fatalf("fs.rename 失败: %v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); err != nil {
		t.Fatalf("fs.rename 失败: %v", err)
	}
	// fs.delete
	resp = sendReq(t, conn, "6", "fs.delete", map[string]any{"path": "sub"})
	if !resp.OK {
		t.Fatalf("fs.delete 失败: %v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(root, "sub")); !os.IsNotExist(err) {
		t.Fatalf("fs.delete 未删除: %v", err)
	}
}

func TestWS_FS_PathTraversalRejected(t *testing.T) {
	root := t.TempDir()
	ts, _ := startTestServer(t, &fsRuntime{fakeRuntime: fakeRuntime{}, root: root})
	conn := dialWS(t, ts)
	// 路径穿越尝试
	for _, p := range []string{"../../etc/passwd", "/etc/passwd", ".."} {
		resp := sendReq(t, conn, "x", "fs.read", map[string]any{"path": p})
		if resp.OK {
			t.Fatalf("路径穿越应被拒绝: %s", p)
		}
	}
	// 合法路径仍可用
	resp := sendReq(t, conn, "y", "fs.list", map[string]any{"path": "."})
	if !resp.OK {
		t.Fatalf("合法 fs.list 被误拒: %v", resp.Error)
	}
}

func TestWS_ChatDeleteAndRenameSession(t *testing.T) {
	rt := &fakeRuntime{
		sessions:  []*types.Session{{UUID: "u1", Title: "会话一"}, {UUID: "u2", Title: "会话二"}},
		sessionID: "u1",
	}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// 删除非活跃会话
	resp := sendReq(t, conn, "1", "chat.deleteSession", map[string]any{"uuid": "u2"})
	if !resp.OK {
		t.Fatalf("chat.deleteSession 失败: %v", resp.Error)
	}
	if len(rt.sessions) != 1 || rt.sessions[0].UUID != "u1" {
		t.Fatalf("删除后会话数量不对: %v", rt.sessions)
	}
	// 重命名剩余会话
	resp = sendReq(t, conn, "2", "chat.renameSession", map[string]any{"uuid": "u1", "title": "新标题"})
	if !resp.OK {
		t.Fatalf("chat.renameSession 失败: %v", resp.Error)
	}
	if rt.sessions[0].Title != "新标题" {
		t.Fatalf("重命名未生效: %s", rt.sessions[0].Title)
	}
	// 删除当前活跃会话（最后一个）应成功，且回到无会话状态（用户要求：
	// 删除最后一个会话后不自动新建，前端进入空状态）
	resp = sendReq(t, conn, "3", "chat.deleteSession", map[string]any{"uuid": "u1"})
	if !resp.OK {
		t.Fatalf("删除当前会话失败: %v", resp.Error)
	}
	if len(rt.sessions) != 0 {
		t.Fatalf("删除最后一个会话后列表应为空: %v", rt.sessions)
	}
	if rt.sessionID != "" {
		t.Fatalf("删除当前会话后 sessionID 应清空: %q", rt.sessionID)
	}
}

func TestWS_ModelInfoAndSetEffort(t *testing.T) {
	rt := &fakeRuntime{mode: "chat"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "model.info", nil)
	if !resp.OK {
		t.Fatalf("model.info 失败: %v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["backend"] != "deepseek" || m["effort"] != "max" {
		t.Fatalf("model.info 内容异常: %v", m)
	}
	resp = sendReq(t, conn, "2", "model.set", map[string]any{"effort": "high"})
	if !resp.OK {
		t.Fatalf("model.set 失败: %v", resp.Error)
	}
	resp = sendReq(t, conn, "3", "model.set", map[string]any{"effort": "bogus"})
	if resp.OK {
		t.Fatal("非法 effort 应失败")
	}
}

// TestWS_ModelListAndSwitch：多模型预设列表 + 一键切换（GUI 下拉）
func TestWS_ModelListAndSwitch(t *testing.T) {
	rt := &fakeRuntime{mode: "chat"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "model.list", nil)
	if !resp.OK {
		t.Fatalf("model.list 失败: %v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	presets, ok := m["presets"].([]any)
	if !ok || len(presets) != 2 {
		t.Fatalf("presets 应为 2 项, got %#v", m["presets"])
	}
	if m["current"] != "default-model" {
		t.Fatalf("current 应为 default-model, got %v", m["current"])
	}

	resp = sendReq(t, conn, "2", "model.switch", map[string]any{"name": "gpt-4o"})
	if !resp.OK {
		t.Fatalf("model.switch 失败: %v", resp.Error)
	}
	if rt.switchedModel != "gpt-4o" {
		t.Fatalf("fakeRuntime 应收到 gpt-4o, got %q", rt.switchedModel)
	}

	// 缺 name 报错
	resp = sendReq(t, conn, "3", "model.switch", map[string]any{})
	if resp.OK {
		t.Fatal("缺少 name 应失败")
	}

	// 傻瓜式新建预设
	resp = sendReq(t, conn, "4", "model.create", map[string]any{
		"name": "brand-new", "backend": "openai", "endpoint": "https://x/v1",
	})
	if !resp.OK {
		t.Fatalf("model.create 失败: %v", resp.Error)
	}
	if rt.switchedModel != "brand-new" {
		t.Fatalf("fakeRuntime 应收到新建名 brand-new, got %q", rt.switchedModel)
	}

	// 删除模型配置
	resp = sendReq(t, conn, "5", "model.delete", map[string]any{"name": "gpt-4o"})
	if !resp.OK {
		t.Fatalf("model.delete 失败: %v", resp.Error)
	}
	if rt.removedModel != "gpt-4o" {
		t.Fatalf("fakeRuntime 应收到删除名 gpt-4o, got %q", rt.removedModel)
	}

	// 缺 name 报错
	resp = sendReq(t, conn, "6", "model.delete", map[string]any{})
	if resp.OK {
		t.Fatal("model.delete 缺少 name 应失败")
	}
}

func TestWS_GoalStatusDetail(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "goal.status", nil)
	if !resp.OK {
		t.Fatalf("goal.status 失败: %v", resp.Error)
	}
	g := resp.Result.(map[string]any)
	if g["active"] != false {
		t.Fatalf("goal.status active 应为 false: %v", g)
	}
}

func TestWS_SkillRestorePluginList(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	for _, m := range []string{"skill.list", "plugin.list", "context.stats"} {
		resp := sendReq(t, conn, "x", m, nil)
		if !resp.OK {
			t.Fatalf("%s 失败: %v", m, resp.Error)
		}
	}
}

// TestWS_ContextCompress 验证 GUI 手动深度压缩已接线：
// context.compress 返回 {ok, message, usage}，message 含压缩结果
// （修复前该能力缺失：/compact 只能提示去 TUI，无后端 API）。
func TestWS_ContextCompress(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "context.compress", nil)
	if !resp.OK {
		t.Fatalf("context.compress 失败: %v", resp.Error)
	}
	r := resp.Result.(map[string]any)
	msg, _ := r["message"].(string)
	if msg == "" || !strings.Contains(msg, "上下文已压缩") {
		t.Fatalf("message 应包含压缩结果文案: %q", msg)
	}
	if r["usage"] == "" {
		t.Fatalf("usage 不应为空: %v", r["usage"])
	}
}

// TestWS_ContextCompress_PushesDisplayEvent 验证压缩成功后后端推送
// ctx.compressed 事件（带压缩态消息列表）——GUI 消息区据此重绘为
// [历史摘要]+保留消息，"点压缩按钮界面毫无变化"的显示问题得以解决。
func TestWS_ContextCompress_PushesDisplayEvent(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{
		msgs: []types.Message{
			{Role: "user", Content: "[历史摘要] 前面的对话摘要"},
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "你好，有什么可以帮你？"},
		},
	})
	conn := dialWS(t, ts)
	// 先建立会话（fakeRuntime.LoadSession 会设置 sessionID，事件才会带 sid）
	if resp := sendReq(t, conn, "8", "chat.loadSession", map[string]any{"uuid": "u1"}); !resp.OK {
		t.Fatalf("loadSession 失败: %v", resp.Error)
	}
	// 发送压缩请求（id=9）
	if err := conn.WriteJSON(Envelope{ID: "9", Type: "req", Method: "context.compress"}); err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	// 事件先于 resp 到达（dispatch 内先 send 事件，handleReq 再发 resp）：
	// 应收到 ctx.compressed 事件
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
			// resp 先到（理论上不会）：事件仍应随后到达，继续读
		default:
			// 其他事件（如 ctx.usage）跳过
		}
	}
	if gotEvt == nil || gotEvt.Event != "ctx.compressed" {
		t.Fatalf("应收到 ctx.compressed 事件，got %+v", gotEvt)
	}
	d := gotEvt.Data.(map[string]any)
	if d["sessionId"] == "" {
		t.Fatalf("事件应带 sessionId: %v", d)
	}
	if d["message"] == "" {
		t.Fatalf("事件应带压缩结果 message: %v", d)
	}
	msgs, _ := d["messages"].([]any)
	if len(msgs) == 0 {
		t.Fatalf("事件应带压缩态消息列表: %v", d)
	}
	first := msgs[0].(map[string]any)
	if first["content"] == "" {
		t.Fatalf("消息内容不应为空: %v", first)
	}
	// 呼吸确认：最后还应正常收到 id=9 的 resp
	var resp Envelope
	for {
		if err := conn.ReadJSON(&resp); err != nil {
			t.Fatalf("读取 resp 失败: %v", err)
		}
		if resp.ID == "9" {
			break
		}
	}
	if !resp.OK {
		t.Fatalf("resp 应 OK: %v", resp.Error)
	}
}

func TestWS_LogTailAndClear(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	// 先发几个请求产生日志
	sendReq(t, conn, "1", "ping", nil)
	sendReq(t, conn, "2", "ping", nil)
	sendReq(t, conn, "3", "bogus", nil)
	resp := sendReq(t, conn, "4", "log.tail", nil)
	if !resp.OK {
		t.Fatalf("log.tail 失败: %v", resp.Error)
	}
	lines := resp.Result.(map[string]any)["lines"].([]any)
	if len(lines) < 2 {
		t.Fatalf("log.tail 应有日志: %d", len(lines))
	}
	// 最新在前
	first := lines[0].(map[string]any)
	if first["msg"] == nil {
		t.Fatal("日志行缺 msg")
	}
	resp = sendReq(t, conn, "5", "log.clear", nil)
	if !resp.OK {
		t.Fatalf("log.clear 失败: %v", resp.Error)
	}
	resp = sendReq(t, conn, "6", "log.tail", nil)
	// clear 请求本身会记录一条 "方法 log.clear 成功"，故允许 ≤1 条
	if got := len(resp.Result.(map[string]any)["lines"].([]any)); got > 1 {
		t.Fatalf("log.clear 后应 ≤1 条（clear 自身日志）: %d", got)
	}
}

// TestWS_ConfigGet 验证 config.get：返回脱敏 values + schema + path。
func TestWS_ConfigGet(t *testing.T) {
	rt := &fakeRuntime{cfgPath: "/tmp/cfg/config.yaml"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "config.get", nil)
	if !resp.OK {
		t.Fatalf("config.get 失败: %v", resp.Error)
	}
	m := resp.Result.(map[string]any)
	if m["path"] != "/tmp/cfg/config.yaml" {
		t.Fatalf("path 不符: %v", m["path"])
	}
	schema, ok := m["schema"].([]any)
	if !ok || len(schema) == 0 {
		t.Fatalf("schema 应为非空数组: %T", m["schema"])
	}
	values, ok := m["values"].(map[string]any)
	if !ok {
		t.Fatalf("values 类型错误: %T", m["values"])
	}
	// 脱敏验证：api_key 必须打码，绝不返回明文
	if k, _ := values["model.api_key"].(string); !strings.Contains(k, "****") {
		t.Fatalf("api_key 未脱敏: %q", k)
	}
}

// TestWS_ConfigSet 验证 config.set：补丁透传 + 返回更新后 values。
func TestWS_ConfigSet(t *testing.T) {
	rt := &fakeRuntime{}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	patch := map[string]any{"model.max_context": 262144, "context.keep_recent": 16}
	resp := sendReq(t, conn, "1", "config.set", patch)
	if !resp.OK {
		t.Fatalf("config.set 失败: %v", resp.Error)
	}
	rt.mu.Lock()
	got := rt.lastPatch
	rt.mu.Unlock()
	if got["model.max_context"] != float64(262144) || got["context.keep_recent"] != float64(16) {
		t.Fatalf("补丁透传不符: %v", got)
	}
	// 返回更新后的 values + 需重启字段清单（热更新提示）
	m := resp.Result.(map[string]any)
	if m["values"] == nil {
		t.Fatal("config.set 应返回 values")
	}
	nr, ok := m["needsRestart"].([]any)
	if !ok || len(nr) == 0 || nr[0] != "acp.port" {
		t.Fatalf("config.set 应返回 needsRestart 清单: %v", m["needsRestart"])
	}
}

// TestWS_ConfigSet_Errors 验证 config.set 错误路径：空补丁/未知 key/后端错误。
func TestWS_ConfigSet_Errors(t *testing.T) {
	ts, _ := startTestServer(t, &fakeRuntime{})
	conn := dialWS(t, ts)
	// 空补丁
	resp := sendReq(t, conn, "1", "config.set", map[string]any{})
	if resp.OK {
		t.Fatal("空补丁应失败")
	}
	// 非法 JSON（字符串参数）
	resp = sendReq(t, conn, "2", "config.set", "not-a-map")
	if resp.OK {
		t.Fatal("非法参数应失败")
	}
	// 后端错误透传
	ts2, _ := startTestServer(t, &fakeRuntime{patchErr: fmt.Errorf("未知配置项: model.bogus")})
	conn2 := dialWS(t, ts2)
	resp = sendReq(t, conn2, "3", "config.set", map[string]any{"model.bogus": 1})
	if resp.OK {
		t.Fatal("未知配置项应失败")
	}
}

// TestWS_ChatSend_AttachmentsMerged 验证 chat.send 附件合并：
// attachments 非空时以 "[附件: 名]\n内容\n\n" 前缀拼入 input。
func TestWS_ChatSend_AttachmentsMerged(t *testing.T) {
	rt := &fakeRuntime{sessionID: "s1"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "请分析这个文件",
		"sessionId": "s1",
		"attachments": []map[string]any{
			{"name": "a.txt", "content": "hello world"},
			{"name": "b.go", "content": "package main"},
		},
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	// 等待流结束（agent.done）
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	if !strings.HasPrefix(rt.lastInputValue(), "[附件: a.txt]\nhello world\n\n[附件: b.go]\npackage main\n\n") {
		t.Fatalf("附件未合并到 input，got: %q", rt.lastInputValue())
	}
	if !strings.HasSuffix(rt.lastInputValue(), "请分析这个文件") {
		t.Fatalf("原始 input 应保留在附件之后，got: %q", rt.lastInputValue())
	}
}

// TestWS_ChatSend_NoAttachmentsUnchanged 验证无附件时 input 原样传递。
// TestWS_ChatSend_LargeAttachmentLocatesPath 验证大附件（content 为空/只记名）
// 时后端在项目根内定位同名文件并注入绝对路径——AI 直接 read_file 读取，
// 不必全盘搜索（用户痛点：大文件附件只传文件名，AI 花大量时间找文件）。
func TestWS_ChatSend_LargeAttachmentLocatesPath(t *testing.T) {
	root := t.TempDir()
	// 构造项目根：深层子目录放目标文件 + 一个同名的干扰文件（应返回先扫到的）
	if err := os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "src", "deep", "bigfile.go")
	if err := os.WriteFile(target, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &fsRuntime{fakeRuntime: fakeRuntime{sessionID: "s1"}, root: root}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "请分析这个文件",
		"sessionId": "s1",
		"attachments": []map[string]any{
			// 模拟前端 >200KB 大文件：content 为空，仅文件名
			{"name": "bigfile.go", "content": ""},
		},
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	in := rt.lastInputValue()
	if !strings.Contains(in, "绝对路径") || !strings.Contains(in, target) {
		t.Fatalf("大附件应注入绝对路径: %q (want contains %q)", in, target)
	}
	if !strings.Contains(in, "read_file") {
		t.Fatalf("应提示可 read_file 按需读取: %q", in)
	}
}

// TestWS_ChatSend_LargeAttachmentNotFound 验证项目根内找不到同名文件时
// 如实提示"未找到"，避免 AI 全盘搜索或瞎猜路径。
func TestWS_ChatSend_LargeAttachmentNotFound(t *testing.T) {
	root := t.TempDir()
	rt := &fsRuntime{fakeRuntime: fakeRuntime{sessionID: "s1"}, root: root}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "分析它",
		"sessionId": "s1",
		"attachments": []map[string]any{
			{"name": "ghost-absolutely-not-exist.txt", "content": ""},
		},
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	in := rt.lastInputValue()
	if !strings.Contains(in, "未找到") {
		t.Fatalf("未找到时应如实提示: %q", in)
	}
}

// TestWS_ChatSend_ImageAttachmentRoutesToVision 验证图片附件走主流识图接口：
// 前端传 base64 data URL → 后端解码为 vision.Image 结构化暂存（StageImages）
// → input 仅含 [图片×N] 计数标记（不再拼 "[图片: 路径]" 文本标记、不把
// 路径垃圾注入模型上下文）——ProcessInput 消费暂存图片为 OpenAI 标准
// image_url 多模态片段并切换识图模型。
func TestWS_ChatSend_ImageAttachmentRoutesToVision(t *testing.T) {
	root := t.TempDir()
	rt := &fsRuntime{fakeRuntime: fakeRuntime{sessionID: "s1"}, root: root}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	// 真实图片字节（最小 PNG 头），前端 readAsDataURL 的产物
	rawPNG := []byte("\x89PNG\r\n\x1a\nfake-image-body")
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(rawPNG)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "这张图里有什么",
		"sessionId": "s1",
		"attachments": []map[string]any{
			{"name": "shot.png", "content": dataURL},
		},
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	in := rt.lastInputValue()
	if strings.Contains(in, "[图片: ") {
		t.Fatalf("图片附件不应再拼 [图片: 路径] 文本标记: %q", in)
	}
	if !strings.Contains(in, "[图片×1]") {
		t.Fatalf("input 应含 [图片×1] 计数标记: %q", in)
	}
	if strings.Contains(in, "[附件:") {
		t.Fatalf("图片附件不应走 [附件:] 文本合并: %q", in)
	}
	if !strings.HasSuffix(in, "这张图里有什么") {
		t.Fatalf("原始 input 应保留在计数标记之后，got: %q", in)
	}
	// 结构化暂存：StageImages 收到 1 张、MIME 正确、dataURI 为原图字节
	if len(rt.staged) != 1 {
		t.Fatalf("StageImages 应收到 1 张图，实际 %d", len(rt.staged))
	}
	img := rt.staged[0]
	if img.MIME != "image/png" {
		t.Errorf("MIME = %q，期望 image/png", img.MIME)
	}
	if !strings.HasPrefix(img.DataURI, "data:image/png;base64,") {
		t.Errorf("DataURI 应为标准 data URI: %q", img.DataURI)
	}
	// 落盘临时文件内容与上传字节一致（历史事件日志恢复锚点）
	data, err := os.ReadFile(img.Path)
	if err != nil {
		t.Fatalf("临时图片文件不可读: %v (%q)", err, img.Path)
	}
	if string(data) != string(rawPNG) {
		t.Fatalf("临时图片内容与上传字节不一致: got %q want %q", data, rawPNG)
	}
	// 落盘目录必须为项目 .contextloom/attachments/（持久目录）——旧实现写系统
	// 临时目录，事件日志恢复锚点在清理/重启后失效（历史图片恢复不了）。
	wantDir := filepath.Join(root, ".contextloom", "attachments")
	if got := filepath.Dir(img.Path); got != wantDir {
		t.Fatalf("图片落盘目录 = %q，期望持久目录 %q", got, wantDir)
	}
}

// TestWS_ChatSend_ImageAttachmentLocatesInProject 图片附件无 dataURL（旧客户端
// 或大图只记名）时，回退在项目根内定位同名文件 → 结构化暂存。
func TestWS_ChatSend_ImageAttachmentLocatesInProject(t *testing.T) {
	root := t.TempDir()
	img := filepath.Join(root, "shot.png")
	if err := os.WriteFile(img, []byte("\x89PNG\r\n\x1a\nfake"), 0o644); err != nil {
		t.Fatal(err)
	}
	rt := &fsRuntime{fakeRuntime: fakeRuntime{sessionID: "s1"}, root: root}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "这张图里有什么",
		"sessionId": "s1",
		"attachments": []map[string]any{
			{"name": "shot.png", "content": ""}, // 无 dataURL
		},
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	in := rt.lastInputValue()
	if !strings.Contains(in, "[图片×1]") {
		t.Fatalf("input 应含 [图片×1] 计数标记: %q", in)
	}
	if strings.Contains(in, "[图片: ") {
		t.Fatalf("不应再注入 [图片: 路径] 文本标记: %q", in)
	}
	if len(rt.staged) != 1 {
		t.Fatalf("StageImages 应收到 1 张图，实际 %d", len(rt.staged))
	}
	if rt.staged[0].Path != img {
		t.Errorf("暂存图片 Path = %q，期望项目内路径 %q", rt.staged[0].Path, img)
	}
}

// TestWS_ChatSend_ImageAttachmentNotFound 图片附件在项目根内找不到同名文件时：
// 回退 [附件:] 提示用户搜索，不让模型瞎猜路径。
func TestWS_ChatSend_ImageAttachmentNotFound(t *testing.T) {
	root := t.TempDir()
	rt := &fsRuntime{fakeRuntime: fakeRuntime{sessionID: "s1"}, root: root}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "看这个图",
		"sessionId": "s1",
		"attachments": []map[string]any{
			{"name": "ghost-shot.png", "content": ""},
		},
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	in := rt.lastInputValue()
	if !strings.Contains(in, "读取失败") && !strings.Contains(in, "未找到") {
		t.Fatalf("图片读取失败时应如实提示: %q", in)
	}
	if strings.Contains(in, "[图片: ") {
		t.Fatalf("不应再注入 [图片: 路径] 文本标记: %q", in)
	}
}

func TestWS_ChatSend_NoAttachmentsUnchanged(t *testing.T) {
	rt := &fakeRuntime{sessionID: "s1"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"input":     "普通消息",
		"sessionId": "s1",
	})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %v", resp.Error)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("读消息失败: %v", err)
		}
		var env Envelope
		if err := json.Unmarshal(msg, &env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}
	if rt.lastInputValue() != "普通消息" {
		t.Fatalf("无附件时应原样传递，got: %q", rt.lastInputValue())
	}
}

// TestWS_ChatSend_OnlyAttachments 验证只有附件、无 input 时也可发送。
func TestWS_ChatSend_OnlyAttachments(t *testing.T) {
	rt := &fakeRuntime{sessionID: "s1"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "chat.send", map[string]any{
		"sessionId": "s1",
		"attachments": []map[string]any{
			{"name": "only.txt", "content": "只有附件"},
		},
	})
	if !resp.OK {
		t.Fatalf("只有附件也应接受: %v", resp.Error)
	}
	if !strings.Contains(rt.lastInputValue(), "[附件: only.txt]\n只有附件") {
		t.Fatalf("附件未合并，got: %q", rt.lastInputValue())
	}
}

// TestSkillToggle_DoesNotCorruptSharedSlice 回归：skill.toggle 停用技能时
// 必须新建切片（旧实现 cur[:0] 复用 GetActiveSkills 返回的内部切片底层数组，
// 会破坏运行时状态）。
func TestSkillToggle_DoesNotCorruptSharedSlice(t *testing.T) {
	rt := &fakeRuntime{activeSkills: []string{"a", "b", "c"}}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "skill.toggle", map[string]any{"name": "b", "active": false})
	if !resp.OK {
		t.Fatalf("skill.toggle 失败: %v", resp.Error)
	}
	if got := rt.activeSkills; len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("activeSkills 应为 [a c]，实际 %v", got)
	}
	// 再次 toggle 验证连续操作稳定
	resp = sendReq(t, conn, "2", "skill.toggle", map[string]any{"name": "a", "active": false})
	if !resp.OK {
		t.Fatalf("第二次 toggle 失败: %v", resp.Error)
	}
	if got := rt.activeSkills; len(got) != 1 || got[0] != "c" {
		t.Fatalf("activeSkills 应为 [c]，实际 %v", got)
	}
	// 启用回 b
	resp = sendReq(t, conn, "3", "skill.toggle", map[string]any{"name": "b", "active": true})
	if !resp.OK {
		t.Fatalf("第三次 toggle 失败: %v", resp.Error)
	}
	if got := rt.activeSkills; len(got) != 2 {
		t.Fatalf("activeSkills 应为 [c b]，实际 %v", got)
	}
}

// TestFS_ListUsesForwardSlash 回归：fs.list 返回的 path 必须统一 "/" 分隔
// （Windows 上 filepath.Join 产生 "\"，前端按 "/" 处理会失效）。
func TestFS_ListUsesForwardSlash(t *testing.T) {
	rt := &fakeRuntime{projectRoot: t.TempDir()}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "1", "fs.list", map[string]any{"path": ""})
	if !resp.OK {
		t.Fatalf("fs.list 失败: %v", resp.Error)
	}
	entries := resp.Result.(map[string]any)["entries"].([]any)
	for _, e := range entries {
		m := e.(map[string]any)
		p, _ := m["path"].(string)
		if strings.Contains(p, "\\") {
			t.Fatalf("fs.list path 含反斜杠（应统一 / 分隔）: %q", p)
		}
	}
}

// TestSafePath_CaseInsensitivePrefix 回归：Windows 路径大小写不敏感，
// 越界检查必须 EqualFold（否则 C:\Proj 与 c:\proj 误判越界）。
func TestSafePath_CaseInsensitivePrefix(t *testing.T) {
	root := t.TempDir()
	srv := &Server{rt: &fakeRuntime{projectRoot: root}}
	abs, err := srv.safePath("sub/dir", "")
	if err != nil {
		t.Fatalf("safePath 子目录失败: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(abs), strings.ToLower(root)+string(filepath.Separator)) {
		t.Fatalf("safePath 未解析到项目根内: %q", abs)
	}
	// 越界：绝对路径在项目根外
	if _, err := srv.safePath(filepath.Join(filepath.Dir(root), "outside.txt"), ""); err == nil {
		t.Fatal("项目根外绝对路径应拒绝")
	}
	// 大小写变体（模拟 Windows 盘符大小写差异）
	upper := strings.ToUpper(root[:1]) + root[1:]
	if filepath.IsAbs(upper) {
		ok, err := srv.safePath(filepath.Join(upper, "sub"), "")
		if err == nil {
			if !strings.EqualFold(ok, filepath.Join(root, "sub")) {
				t.Fatalf("大小写变体路径应视为项目根内: %q vs %q", ok, root)
			}
		}
	}
}

// 回归测试（用户报告：GUI 切换会话再返回，丢失聊天记录和状态）：
// 流式生成期间切换会话 → 正在生成的 assistant 回复必须写回【发起流时】的
// 会话（事件日志），而不是写进切换后的新会话；agent.stream/done 事件必须
// 携带发起流时的 sessionId，供前端按会话过滤。
func TestWS_ChatSend_SwitchSession_MidStream_ReplyStaysInOrigSession(t *testing.T) {
	rt := &fakeRuntime{}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// 1. 会话 A：发送并开始流式（fake 输出 3 个 part，每个间隔 5ms）
	resp := sendReq(t, conn, "sw1", "chat.send", map[string]any{"input": "A 的提问", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	sidA := rt.GetSessionID()
	if sidA == "" {
		t.Fatal("chat.send 未自动创建会话")
	}

	// 2. 读到至少一个 agent.stream 事件（确认流已开始），且事件携带 sidA
	var gotStream bool
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type != "event" || env.Event != "agent.stream" {
			continue
		}
		gotStream = true
		if data, ok := env.Data.(map[string]any); ok {
			if sid, _ := data["sessionId"].(string); sid != sidA {
				t.Fatalf("agent.stream sessionId = %q, want %q", sid, sidA)
			}
		}
		break
	}
	if !gotStream {
		t.Fatal("未收到 agent.stream 事件（流未开始）")
	}

	// 3. 流未结束前切换会话 B（前端 switchSession → chat.loadSession）
	resp = sendReq(t, conn, "sw2", "chat.loadSession", map[string]any{"uuid": "sess-B"})
	if !resp.OK {
		t.Fatalf("chat.loadSession 失败: %+v", resp.Error)
	}
	if rt.GetSessionID() != "sess-B" {
		t.Fatalf("切换后当前会话 = %q, want sess-B", rt.GetSessionID())
	}

	// 4. 收到 agent.done：sessionId 必须是发起流时的 A，且 A 的回复写回 A 的事件日志
	var doneSid string
	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取 agent.done 失败: %v", err)
		}
		if env.Type != "event" || env.Event != "agent.done" {
			continue
		}
		data, ok := env.Data.(map[string]any)
		if !ok {
			t.Fatalf("agent.done 数据格式错误: %T", env.Data)
		}
		doneSid, _ = data["sessionId"].(string)
		break
	}
	if doneSid != sidA {
		t.Fatalf("agent.done sessionId = %q, want %q（必须是发起流时的会话）", doneSid, sidA)
	}

	// 5. A 的回复必须写回 A（事件日志），不得写进当前会话 B 的内存历史
	rt.mu.Lock()
	off := append([]offMsg(nil), rt.offSess...)
	msgs := append([]types.Message(nil), rt.msgs...)
	rt.mu.Unlock()
	found := false
	for _, m := range off {
		// 中断流只保存已产出片段（首个 part "你" 或更多），非空即视为已写回
		if m.SID == sidA && m.Role == "assistant" && m.Content != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("A 的 assistant 回复未写回 A 会话（offSess=%+v）——切走会话后回复丢失", off)
	}
	// 当前会话 B 的内存历史只应有 B 自己的内容（不得混入 A 的回复）
	for _, m := range msgs {
		if m.Role == "assistant" {
			t.Fatalf("A 的回复污染了当前会话 B 的内存历史: %+v", msgs)
		}
	}

	// 6. 切回 A：LoadSession(A) 应从事件日志恢复完整历史（此处验证接口调用成功）
	resp = sendReq(t, conn, "sw3", "chat.loadSession", map[string]any{"uuid": sidA})
	if !resp.OK {
		t.Fatalf("切回会话 A 失败: %+v", resp.Error)
	}
	if rt.GetSessionID() != sidA {
		t.Fatalf("切回后当前会话 = %q, want %q", rt.GetSessionID(), sidA)
	}
}

// 回归测试：切换会话（loadSession）【不中断】其他会话正在进行的流（多对话并行）：
// 后台流继续生成直至正常完成（agent.done 不带 interrupted=true），回复经
// AddToHistoryForSession 写回发起流时的会话事件日志，不污染当前会话。
func TestWS_LoadSession_KeepsOtherSessionStream(t *testing.T) {
	rt := &fakeRuntime{streamDelay: 10 * time.Millisecond}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "c1", "chat.send", map[string]any{"input": "提问", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	sidA := rt.GetSessionID()

	// 确保流已启动（读到一个 agent.stream）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&env); err == nil && env.Type == "event" && env.Event == "agent.stream" {
			break
		}
	}

	// 流未结束时切换会话 C
	resp = sendReq(t, conn, "c2", "chat.loadSession", map[string]any{"uuid": "sess-C"})
	if !resp.OK {
		t.Fatalf("chat.loadSession 失败: %+v", resp.Error)
	}
	if rt.GetSessionID() != "sess-C" {
		t.Fatalf("切换后当前会话 = %q, want sess-C", rt.GetSessionID())
	}

	// 切换会话【不中断】A 的流：agent.done 正常完成（interrupted 不为 true）
	// 且 sessionId 仍为 A；完整回复写回 A 的事件日志（offSess）
	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取 agent.done 失败: %v", err)
		}
		if env.Type != "event" || env.Event != "agent.done" {
			continue
		}
		data, _ := env.Data.(map[string]any)
		if inter, _ := data["interrupted"].(bool); inter {
			t.Fatalf("切换会话后旧流不应被中断（多对话并行）: %+v", data)
		}
		if s, _ := data["sessionId"].(string); s != sidA {
			t.Fatalf("done 的 sessionId = %q, want %q", s, sidA)
		}
		break
	}
	rt.mu.Lock()
	off := append([]offMsg(nil), rt.offSess...)
	msgs := append([]types.Message(nil), rt.msgs...)
	rt.mu.Unlock()
	found := false
	for _, m := range off {
		if m.SID == sidA && m.Role == "assistant" && m.Content != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("A 的完整回复未写回 A 会话事件日志（offSess=%+v）", off)
	}
	for _, m := range msgs {
		if m.Role == "assistant" {
			t.Fatalf("A 的回复污染了当前会话 C 的内存历史: %+v", msgs)
		}
	}
}

// 回归测试：多会话并行——A 流进行中切换到 B 并向 B 发送，两条流同时运行
// 均正常完成，回复各写回各的会话，互不干扰（用户需求：切换会话不中断对话）。
func TestWS_TwoSessions_ParallelStreams(t *testing.T) {
	rt := &fakeRuntime{streamDelay: 30 * time.Millisecond}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// A 发送并启动流
	resp := sendReq(t, conn, "p1", "chat.send", map[string]any{"input": "A问", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("A chat.send 失败: %+v", resp.Error)
	}
	sidA := rt.GetSessionID()
	if sidA == "" {
		t.Fatal("A chat.send 未自动创建会话")
	}
	// 确保 A 流已启动
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&env); err == nil && env.Type == "event" && env.Event == "agent.stream" {
			break
		}
	}

	// 切到 B 并向 B 发送（不中断 A 的流）
	resp = sendReq(t, conn, "p2", "chat.loadSession", map[string]any{"uuid": "sess-B"})
	if !resp.OK {
		t.Fatalf("loadSession B 失败: %+v", resp.Error)
	}
	resp = sendReq(t, conn, "p3", "chat.send", map[string]any{"input": "B问", "sessionId": "sess-B"})
	if !resp.OK {
		t.Fatalf("B chat.send 失败: %+v", resp.Error)
	}

	// 收集两条流的 done：A 与 B 各一条，均正常完成（interrupted 不为 true）
	dones := map[string]bool{}
	for len(dones) < 2 {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if env.Type != "event" || env.Event != "agent.done" {
			continue
		}
		data, _ := env.Data.(map[string]any)
		if inter, _ := data["interrupted"].(bool); inter {
			t.Fatalf("并行流不应被中断: %+v", data)
		}
		s, _ := data["sessionId"].(string)
		if s != sidA && s != "sess-B" {
			t.Fatalf("未知会话的 done: %q", s)
		}
		dones[s] = true
	}
	if !dones[sidA] || !dones["sess-B"] {
		t.Fatalf("两条流未都正常完成: %v", dones)
	}
	// 两条回复各写回各会话：A 的回复（当前会话是 B）→ offSess；B 的回复（当前会话 B）→ msgs
	rt.mu.Lock()
	off := append([]offMsg(nil), rt.offSess...)
	msgs := append([]types.Message(nil), rt.msgs...)
	rt.mu.Unlock()
	foundA := false
	for _, m := range off {
		if m.SID == sidA && m.Role == "assistant" && m.Content != "" {
			foundA = true
		}
	}
	foundB := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content != "" {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("并行流回复未各写回各会话（A=%v B=%v off=%+v）", foundA, foundB, off)
	}
}

// TestWS_LoadSession_IncludesPartialInFlight 治本回归（"点击本会话标题后清空
// 重置、只能看到新生成的"根因修复）：
//   - 生成中 chat.loadSession 必须返回在途流已生成内容（partial assistant），
//     前端重绘不再把"正在生成的半截回复"从界面清掉；
//   - 流结束后 partial 消失，历史包含完整 assistant（不重复）。
func TestWS_LoadSession_IncludesPartialInFlight(t *testing.T) {
	rt := &fakeRuntime{streamDelay: 40 * time.Millisecond}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// 1. 发送并启动流（fake 输出 你/好/世界 3 个 part）
	resp := sendReq(t, conn, "pf1", "chat.send", map[string]any{"input": "生成中提问", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	sid := rt.GetSessionID()
	if sid == "" {
		t.Fatal("chat.send 未自动创建会话")
	}

	// 2. 读到至少一个 agent.stream（确保 pending 已有内容）
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&env); err == nil && env.Type == "event" && env.Event == "agent.stream" {
			break
		}
	}

	// 3. 生成中 loadSession：必须包含在途 partial assistant（不丢半截回复）
	resp = sendReq(t, conn, "pf2", "chat.loadSession", map[string]any{"uuid": sid})
	if !resp.OK {
		t.Fatalf("生成中 loadSession 失败: %+v", resp.Error)
	}
	msgs, _ := resp.Result.(map[string]any)["messages"].([]any)
	if len(msgs) < 2 {
		t.Fatalf("生成中 loadSession 消息数 = %d, want >= 2（user + partial assistant）: %+v", len(msgs), resp.Result)
	}
	last := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" {
		t.Fatalf("生成中最后一条角色 = %v, want assistant（在途内容必须合并）: %+v", last["role"], msgs)
	}
	if partial, _ := last["partial"].(bool); !partial {
		t.Fatalf("生成中最后一条无 partial 标记: %+v", last)
	}
	content, _ := last["content"].(string)
	if content == "" || !strings.HasPrefix(content, "你") {
		t.Fatalf("生成中 partial 内容 = %q, want 以'你'开头（已生成的在途片段）", content)
	}
	// 历史中的 user 消息仍在（"前面的记录"不丢）
	first := msgs[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "生成中提问" {
		t.Fatalf("生成中历史第一条 = %+v, want user 生成中提问", first)
	}

	// 4. 等流结束（agent.done）
	for {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取 agent.done 失败: %v", err)
		}
		if env.Type == "event" && env.Event == "agent.done" {
			break
		}
	}

	// 5. 流结束后 loadSession：无 partial（pending 已清理），历史含完整 assistant
	resp = sendReq(t, conn, "pf3", "chat.loadSession", map[string]any{"uuid": sid})
	if !resp.OK {
		t.Fatalf("流结束后 loadSession 失败: %+v", resp.Error)
	}
	msgs2, _ := resp.Result.(map[string]any)["messages"].([]any)
	last2 := msgs2[len(msgs2)-1].(map[string]any)
	if last2["role"] != "assistant" {
		t.Fatalf("流结束后最后一条 = %+v, want assistant", last2)
	}
	if partial, _ := last2["partial"].(bool); partial {
		t.Fatalf("流结束后仍残留 partial（pending 未清理）: %+v", msgs2)
	}
	content2, _ := last2["content"].(string)
	if content2 != "你好世界" {
		t.Fatalf("流结束后完整回复 = %q, want 你好世界", content2)
	}
	if len(msgs2) != 2 {
		t.Fatalf("流结束后消息数 = %d, want 2（user + assistant，无重复 partial）: %+v", len(msgs2), msgs2)
	}
}

// 回归测试：首次对话自动更新会话标题（与 TUI 行为对齐，取第一个问题）。
// GUI 此前从不更新标题，新建会话永远是"新会话"。
func TestWS_ChatSend_AutoTitle(t *testing.T) {
	rt := &fakeRuntime{}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	// 首次对话：无会话 → chat.send 自动创建并更新标题为第一个问题
	first := "这是第一个问题，用于验证自动标题更新功能"
	resp := sendReq(t, conn, "10", "chat.send", map[string]any{"input": first, "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	readUntilDone(t, conn, "10")
	sid := rt.GetSessionID()
	if sid == "" {
		t.Fatal("无会话时 chat.send 应自动创建会话")
	}
	rt.mu.Lock()
	var title string
	for _, s := range rt.sessions {
		if s.UUID == sid {
			title = s.Title
		}
	}
	rt.mu.Unlock()
	if title != first {
		t.Fatalf("首次对话标题应更新为第一个问题: got %q want %q", title, first)
	}

	// 第二次对话：标题不再变化（非首次）
	resp = sendReq(t, conn, "11", "chat.send", map[string]any{"input": "第二个问题", "sessionId": sid})
	if !resp.OK {
		t.Fatalf("第二轮 chat.send 失败: %+v", resp.Error)
	}
	readUntilDone(t, conn, "11")
	rt.mu.Lock()
	title2 := ""
	for _, s := range rt.sessions {
		if s.UUID == sid {
			title2 = s.Title
		}
	}
	rt.mu.Unlock()
	if title2 != first {
		t.Fatalf("非首次对话标题不应变化: %q", title2)
	}
}

// 回归测试：GUI goal 模式自动续跑（v2 自进化闭环接线）。
// chat.send → 第一轮 → agent.done → 轮末评估（GoalOnRoundEnd）→ 继续 →
// ProcessGoalRound 自动发起第二轮 → agent.done → 评估完成 → goal.round 结束。
func TestWS_ChatSend_GoalAutoContinue(t *testing.T) {
	rt := &fakeRuntime{
		mode:              "goal",
		goalSession:       &types.GoalSession{ID: "g1", Phase: types.GoalPhaseExecute},
		goalContinueTimes: 1, // 第一轮后继续，第二轮后完成
		goalRoundOutput:   []string{"第二轮输出"},
	}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "20", "chat.send", map[string]any{"input": "目标", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	// 事件序列：round1 stream×3 + done → goal.round(continue) → round2 stream + done → goal.round(done)
	rounds := 0
	goalRoundEvents := 0
	for goalRoundEvents < 2 {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if env.Type != "event" {
			continue
		}
		switch env.Event {
		case "agent.done":
			rounds++
		case "goal.round":
			goalRoundEvents++
			data, _ := env.Data.(map[string]any)
			if goalRoundEvents == 1 && data["continue"] != true {
				t.Fatalf("第一轮后应继续，goal.round=%v", env.Data)
			}
			if goalRoundEvents == 2 && data["continue"] != false {
				t.Fatalf("第二轮后应完成，goal.round=%v", env.Data)
			}
			if data["reason"] == "" {
				t.Fatalf("goal.round 应携带 reason，got %v", env.Data)
			}
		}
	}
	if rounds != 2 {
		t.Fatalf("应执行 2 轮，got %d", rounds)
	}
	if goalRoundEvents != 2 {
		t.Fatalf("应收到 2 个 goal.round 事件，got %d", goalRoundEvents)
	}
	rt.mu.Lock()
	roundsGot, endsGot := rt.goalRounds, rt.goalRoundEnds
	rt.mu.Unlock()
	if roundsGot != 1 {
		t.Fatalf("ProcessGoalRound 应被调用 1 次（自动续跑），got %d", roundsGot)
	}
	if endsGot != 2 {
		t.Fatalf("GoalOnRoundEnd 应调用 2 次，got %d", endsGot)
	}
	// 历史完整性：user + 两轮 assistant 都写入历史（自动续跑不丢轮；
	// 真实 runtime 的 ProcessGoalRound 还会注入续跑指令 user 消息）
	hist := rt.historySnapshot()
	if len(hist) != 3 {
		t.Fatalf("历史应有 3 条（user+2×assistant），got %d: %+v", len(hist), hist)
	}
}

// 回归测试：非 goal 模式（chat）不触发自动续跑（goal.round 事件为零）。
func TestWS_ChatSend_NoGoalAutoContinueInChatMode(t *testing.T) {
	rt := &fakeRuntime{mode: "chat"}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "21", "chat.send", map[string]any{"input": "普通问题", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	rounds := 0
	goalEvents := 0
	for rounds < 1 {
		var env Envelope
		if err := conn.ReadJSON(&env); err != nil {
			t.Fatalf("读取事件失败: %v", err)
		}
		if env.Type != "event" {
			continue
		}
		if env.Event == "agent.done" {
			rounds++
		}
		if env.Event == "goal.round" {
			goalEvents++
		}
	}
	if goalEvents != 0 {
		t.Fatalf("chat 模式不应有 goal.round 事件，got %d", goalEvents)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.goalRounds != 0 {
		t.Fatalf("chat 模式不应触发 ProcessGoalRound，got %d", rt.goalRounds)
	}
}

// 对话记录图片预览：多模态消息经 messagesToJSON 输出时必须携带 images
// （data URI 列表），前端才能渲染 <img> 预览——历史缺陷：只输出
// "[图片×N]" 计数文本、丢弃 data URI → GUI 对话记录看不到图片。
func TestMessagesToJSON_CarriesImageURLs(t *testing.T) {
	s := &Server{}
	img := vision.Image{
		Path:    "/tmp/x.png",
		MIME:    "image/png",
		DataURI: "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==",
	}
	msgs := []types.Message{
		{Role: "user", Content: "看图"},
		vision.BuildUserMessage("看图", []vision.Image{img}),
	}
	out := s.messagesToJSON(msgs)

	// 纯文本消息：无 images 字段
	if _, ok := out[0]["images"]; ok {
		t.Fatalf("纯文本消息不应有 images 字段: %+v", out[0])
	}
	if got := out[0]["content"]; got != "看图" {
		t.Fatalf("纯文本消息 content 应原样: %+v", got)
	}

	// 多模态消息：content 为计数标记 + images 携带 data URI
	m1 := out[1]
	if got := m1["content"]; got != "[图片×1] 看图" {
		t.Fatalf("多模态 content 应为计数标记，got %+v", got)
	}
	imgs, ok := m1["images"].([]string)
	if !ok || len(imgs) != 1 {
		t.Fatalf("images 应为 1 个 data URI 列表: %+v", m1["images"])
	}
	if !strings.HasPrefix(imgs[0], "data:image/png;base64,") {
		t.Fatalf("images[0] 应为 data URI: %q", imgs[0])
	}
}

// 多图：多模态消息携带多张图片 data URI。
func TestMessagesToJSON_MultipleImages(t *testing.T) {
	s := &Server{}
	imgs := []vision.Image{
		{Path: "/tmp/a.png", MIME: "image/png", DataURI: "data:image/png;base64,AAAA"},
		{Path: "/tmp/b.jpg", MIME: "image/jpeg", DataURI: "data:image/jpeg;base64,BBBB"},
	}
	out := s.messagesToJSON([]types.Message{
		vision.BuildUserMessage("", imgs),
	})
	m0 := out[0]
	if got := m0["content"]; got != "[图片×2]" {
		t.Fatalf("content 应为 [图片×2]，got %+v", got)
	}
	urls := m0["images"].([]string)
	if len(urls) != 2 || urls[0] != "data:image/png;base64,AAAA" || urls[1] != "data:image/jpeg;base64,BBBB" {
		t.Fatalf("images 列表错误: %+v", urls)
	}
}

// TestWS_ModeSet_NoSessionAutoCreates 无会话时 mode.set 应自动建会话并按
// 会话隔离模式——绝不写全局默认。回归用户实锤："工具的默认模式应该是 chat，
// 结果 settings.json 全局 mode 残留 task，新会话默认变 task"（旧逻辑 SetMode
// 无 sid → 覆盖 CLIDefaults.Mode 全局默认）。
func TestWS_ModeSet_NoSessionAutoCreates(t *testing.T) {
	rt := &fakeRuntime{mode: "chat"} // 无 sessionID
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "m", "mode.set", map[string]any{"mode": "task"})
	if !resp.OK {
		t.Fatalf("mode.set 失败: %+v", resp.Error)
	}
	// 自动建会话后 SetMode 走 modeBySID（会话级），不再触碰全局默认
	if rt.GetSessionID() == "" {
		t.Fatal("mode.set 应自动创建会话")
	}
	if rt.GetMode() != "task" {
		t.Fatalf("模式未切换: want task got %s", rt.GetMode())
	}
	// 新会话应仍继承全局默认 chat（未被 mode.set 污染）
	rt.sessionID = "" // 模拟切换到一个全新的未设置会话
	// fakeRuntime.SetMode 是简单赋值（无会话隔离），断言其不写全局的语义
	// 由 runtime.SetMode 测试覆盖；此处仅验证 GUI 层自动建会话行为。
}

// 回归测试（用户实测："切换项目时正在进行的对话被中止，切回来也没用了"）：
// WS 连接断开（GUI 切换项目会 location.reload，旧连接必然断开）【不得】取消
// 在途流——旧实现 handleWS 的 defer 无条件 cancel 全部 s.active，后端进程虽
// 常驻，对话仍被这一步掐死。
// 本测试：连接 1 发起流 → 中途关闭连接 1 → 连接 2 重连 → 必须仍能收到后续
// agent.stream 与 agent.done（interrupted 不为 true），且完整回复写回历史。
func TestWS_ConnClose_DoesNotCancelStream(t *testing.T) {
	rt := &fakeRuntime{streamDelay: 80 * time.Millisecond}
	ts, _ := startTestServer(t, rt)

	conn1 := dialWS(t, ts)
	if err := conn1.WriteJSON(Envelope{ID: "k1", Type: "req", Method: "chat.send",
		Params: json.RawMessage(`{"input":"长流提问","sessionId":""}`)}); err != nil {
		t.Fatalf("发送 chat.send 失败: %v", err)
	}
	// 等待会话创建（chat.send 异步处理：立即读可能还是空）
	sid := ""
	for i := 0; i < 100 && sid == ""; i++ {
		time.Sleep(10 * time.Millisecond)
		sid = rt.GetSessionID()
	}
	if sid == "" {
		t.Fatal("chat.send 未创建会话")
	}

	// 确认流已开始（收到首个 delta）
	gotStream := false
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !gotStream {
		var env Envelope
		_ = conn1.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn1.ReadJSON(&env); err == nil && env.Type == "event" && env.Event == "agent.stream" {
			gotStream = true
		}
	}
	if !gotStream {
		t.Fatal("未收到 agent.stream（流未开始）")
	}

	// 模拟 GUI 切换项目/刷新：关闭连接 1（旧实现在此 cancel 全部流）
	_ = conn1.Close()
	time.Sleep(50 * time.Millisecond)

	// 重连（新连接）：流应仍在生成，后续 delta/done 必须能收到（broadcast）
	conn2 := dialWS(t, ts)
	var deltas strings.Builder
	done := false
	var interrupted bool
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !done {
		var env Envelope
		_ = conn2.SetReadDeadline(time.Now().Add(1 * time.Second))
		if err := conn2.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type != "event" {
			continue
		}
		data, _ := env.Data.(map[string]any)
		switch env.Event {
		case "agent.stream":
			if d, ok := data["delta"].(string); ok {
				deltas.WriteString(d)
			}
		case "agent.done":
			done = true
			interrupted, _ = data["interrupted"].(bool)
			if got, _ := data["sessionId"].(string); got != sid {
				t.Fatalf("agent.done sessionId = %q, want %q", got, sid)
			}
		}
	}
	if !done {
		t.Fatal("断线重连后未收到 agent.done——在途流被连接断开取消了（本修复禁止）")
	}
	if interrupted {
		t.Fatal("断线重连后流被标记 interrupted——连接断开不得取消在途流")
	}
	if deltas.Len() == 0 {
		t.Fatal("断线重连后未收到任何后续 delta（流未广播给新连接）")
	}
	// 完整回复必须写回历史（重连后 loadSession 可见）
	rt.mu.Lock()
	msgs := append([]types.Message(nil), rt.msgs...)
	off := append([]offMsg(nil), rt.offSess...)
	rt.mu.Unlock()
	found := false
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content != "" {
			found = true
		}
	}
	for _, m := range off {
		if m.Role == "assistant" && m.Content != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("断线后回复未写回会话历史（msgs=%+v offSess=%+v）", msgs, off)
	}
}
