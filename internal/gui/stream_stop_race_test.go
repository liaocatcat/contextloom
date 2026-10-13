package gui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// gatedRuntime 在 fakeRuntime 基础上给 ProcessInput 加"启动闸门"：
// 闸门未关闭时 ProcessInput 阻塞等待（ctx 取消则返回空流，绝不放行已取消
// 的流去启动模型）。用途：确定性复现"chat.stop / 同会话新输入先于模型启动
// 到达"的竞态——闸门冻结模型启动，断言不依赖 goroutine 调度时序。
type gatedRuntime struct {
	fakeRuntime
	gate chan struct{}
}

func (g *gatedRuntime) ProcessInput(ctx context.Context, input string) (<-chan string, <-chan error, error) {
	emptyStream := func() (<-chan string, <-chan error, error) {
		ch := make(chan string)
		errCh := make(chan error)
		close(ch)
		close(errCh)
		return ch, errCh, nil
	}
	if ctx.Err() != nil {
		return emptyStream()
	}
	select {
	case <-ctx.Done():
		return emptyStream()
	case <-g.gate:
		if ctx.Err() != nil { // 闸门打开前已被取消：不放行（确定性收尾）
			return emptyStream()
		}
		return g.fakeRuntime.ProcessInput(ctx, input)
	}
}

// startEventReader 启动后台 goroutine 读取 WS 消息（单一读取者、无读截止：
// 读截止会在帧解析中途打断连接状态，复用连接触发 gorilla repeated-read
// panic）。出错/连接关闭时关闭 channel，读取随之终止。
func startEventReader(conn *websocket.Conn) <-chan Envelope {
	out := make(chan Envelope, 128)
	go func() {
		defer close(out)
		for {
			var env Envelope
			if err := conn.ReadJSON(&env); err != nil {
				return
			}
			out <- env
		}
	}()
	return out
}

// sendMethod 仅发送请求（读取一律走事件通道，绝不与读取 goroutine 竞争）。
func sendMethod(t *testing.T, conn *websocket.Conn, id, method string, params map[string]any) {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := conn.WriteJSON(Envelope{ID: id, Type: "req", Method: method, Params: b}); err != nil {
		t.Fatalf("发送 %s 失败: %v", method, err)
	}
}

// awaitResp 从事件通道等待指定 id 的 resp；途中事件经 onEvent 处理（不丢弃）。
func awaitResp(t *testing.T, ch <-chan Envelope, id string, onEvent func(Envelope), timeout time.Duration) Envelope {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatalf("连接在等待 %s resp 前关闭", id)
			}
			if env.ID == id && env.Type == "resp" {
				return env
			}
			if onEvent != nil {
				onEvent(env)
			}
		case <-deadline:
			t.Fatalf("等待 %s resp 超时", id)
		}
	}
}

// 回归：chat.stop 与流注册的竞态窗口——"已停止的对话被重新激活空跑"根因。
// 修复前：流注册发生在 streamChat goroutine 内部，chat.send 的 accepted 响应
// 发出后、注册完成前到达的 chat.stop 查 s.active 落空被静默丢弃 → 流随后
// 注册并跑完全程（空跑复活）。修复后：注册前移到 dispatch（accepted 响应前），
// 窗口内的 stop 直接命中条目，streamChat 启动时检测到已停止即 interrupted
// 收尾，绝不启动模型。
func TestWS_ChatStop_BeforeStreamStart(t *testing.T) {
	gate := make(chan struct{})
	rt := &gatedRuntime{fakeRuntime: fakeRuntime{streamDelay: 30 * time.Millisecond}, gate: gate}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	ch := startEventReader(conn)

	var doneData map[string]any
	streams, dones := 0, 0
	drain := func(env Envelope) {
		if env.Type != "event" {
			return
		}
		switch env.Event {
		case "agent.stream":
			streams++
		case "agent.done":
			dones++
			if doneData == nil {
				if d, ok := env.Data.(map[string]any); ok {
					doneData = d
				}
			}
		}
	}

	// 发送并等 accepted（流已预注册；模型被闸门冻结未启动）
	sendMethod(t, conn, "r1", "chat.send", map[string]any{"input": "被停的问题", "sessionId": ""})
	if resp := awaitResp(t, ch, "r1", drain, 3*time.Second); !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	sid := rt.GetSessionID()
	if sid == "" {
		t.Fatal("chat.send 未自动创建会话")
	}

	// 立即停止（不等任何 agent.stream）：必须命中预注册条目
	sendMethod(t, conn, "r2", "chat.stop", map[string]any{"sessionId": sid})
	if resp := awaitResp(t, ch, "r2", drain, 3*time.Second); !resp.OK {
		t.Fatalf("chat.stop 失败: %+v", resp.Error)
	}

	// 等待 interrupted done 到达（可能先于 stop 的 resp，已在 drain 中缓冲）
	deadline := time.After(3 * time.Second)
	for doneData == nil {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("连接关闭且未收到停止收尾 done")
			}
			drain(env)
		case <-deadline:
			t.Fatal("停止后未收到 agent.done（前端将永远 streaming）")
		}
	}
	if inter, _ := doneData["interrupted"].(bool); !inter {
		t.Fatalf("停止收尾必须 interrupted=true: %+v", doneData)
	}
	if s, _ := doneData["sessionId"].(string); s != sid {
		t.Fatalf("done sessionId = %q, want %q", s, sid)
	}

	// 静默期：不得再有流事件/第二个 done（空跑复活 = 此处出现 stream/多 done）
	quiet := time.After(200 * time.Millisecond)
quietLoop:
	for {
		select {
		case env, ok := <-ch:
			if !ok {
				break quietLoop // 连接关闭：静默期内无新事件
			}
			drain(env)
		case <-quiet:
			break quietLoop
		}
	}
	if streams != 0 {
		t.Fatalf("已停止的流不得下发任何 agent.stream（空跑复活），got %d", streams)
	}
	if dones != 1 {
		t.Fatalf("应恰好 1 个 agent.done（interrupted 收尾），got %d", dones)
	}
	// 模型从未启动：闸门冻结下真实 ProcessInput 未被调用（输入未被消费）
	if in := rt.lastInputValue(); in != "" {
		t.Fatalf("已停止的流不得启动模型（ProcessInput 输入 = %q）", in)
	}

	// 停止不影响后续对话：打开闸门再发一条 → 正常流 + 非 interrupted done
	close(gate)
	uid2 := 0.0
	deltas := 0
	normalDone := false
	process := func(env Envelope) {
		if env.Type != "event" {
			return
		}
		switch env.Event {
		case "agent.stream":
			deltas++
			if d, ok := env.Data.(map[string]any); ok {
				if id, _ := d["streamId"].(float64); id != 0 && uid2 == 0 {
					uid2 = id
				}
			}
		case "agent.done":
			data, _ := env.Data.(map[string]any)
			id, _ := data["streamId"].(float64)
			if inter, _ := data["interrupted"].(bool); inter {
				return // 旧流的收尾事件按 streamId 归属忽略，与后续流无关
			}
			if uid2 != 0 && id != uid2 {
				return
			}
			normalDone = true
		}
	}
	sendMethod(t, conn, "r3", "chat.send", map[string]any{"input": "后续问题", "sessionId": sid})
	deadline = time.After(3 * time.Second)
	for !normalDone {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatal("连接关闭且未收到后续流 done")
			}
			process(env)
		case <-deadline:
			t.Fatal("后续对话未收到正常（非 interrupted）agent.done")
		}
	}
	if deltas == 0 {
		t.Fatal("后续对话未收到 agent.stream")
	}
}

// 回归：同会话连续两次 chat.send（不先 stop）——第一条被取代的流必须以
// interrupted done 收尾（streamId=第一条 uid），第二条正常完成；两条 done
// 都到达（中断收尾必须送达，否则前端该会话永远 streaming）。
func TestWS_SameSession_NewInputCancelsPendingOld(t *testing.T) {
	gate := make(chan struct{})
	rt := &gatedRuntime{fakeRuntime: fakeRuntime{streamDelay: 30 * time.Millisecond}, gate: gate}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)
	ch := startEventReader(conn)

	var doneOld, doneNew map[string]any
	classify := func(env Envelope) {
		if env.Type != "event" || env.Event != "agent.done" {
			return
		}
		data, _ := env.Data.(map[string]any)
		if inter, _ := data["interrupted"].(bool); inter {
			if doneOld == nil {
				doneOld = data
			}
			return
		}
		if doneNew == nil {
			doneNew = data
		}
	}

	// 第一条发送（闸门冻结模型启动：流已预注册）
	sendMethod(t, conn, "q1", "chat.send", map[string]any{"input": "第一条", "sessionId": ""})
	if resp := awaitResp(t, ch, "q1", classify, 3*time.Second); !resp.OK {
		t.Fatalf("第一条 chat.send 失败: %+v", resp.Error)
	}
	sid := rt.GetSessionID()
	if sid == "" {
		t.Fatal("chat.send 未自动创建会话")
	}

	// 第二条同会话发送（不先 stop）：取代第一条
	sendMethod(t, conn, "q2", "chat.send", map[string]any{"input": "第二条", "sessionId": sid})
	if resp := awaitResp(t, ch, "q2", classify, 3*time.Second); !resp.OK {
		t.Fatalf("第二条 chat.send 失败: %+v", resp.Error)
	}

	// 打开闸门放行第二条（第一条已被取消，闸门不再放行其模型启动）
	close(gate)

	deadline := time.After(5 * time.Second)
	for doneOld == nil || doneNew == nil {
		select {
		case env, ok := <-ch:
			if !ok {
				t.Fatalf("连接关闭：doneOld=%v doneNew=%v", doneOld != nil, doneNew != nil)
			}
			classify(env)
		case <-deadline:
			if doneOld == nil {
				t.Fatal("被取代的第一条流未收到 interrupted done（前端永远 streaming）")
			}
			t.Fatal("第二条流未收到正常 done")
		}
	}
	if s, _ := doneOld["sessionId"].(string); s != sid {
		t.Fatalf("第一条 done sessionId = %q, want %q", s, sid)
	}
	if s, _ := doneNew["sessionId"].(string); s != sid {
		t.Fatalf("第二条 done sessionId = %q, want %q", s, sid)
	}
	// streamId 归属：两条 done 的 streamId 必须不同（旧流事件不得冒充新流）
	idOld, _ := doneOld["streamId"].(float64)
	idNew, _ := doneNew["streamId"].(float64)
	if idOld == 0 || idNew == 0 {
		t.Fatalf("done 缺少 streamId: old=%+v new=%+v", doneOld, doneNew)
	}
	if idOld == idNew {
		t.Fatalf("新旧流 streamId 不应相同: %v", idOld)
	}
}
