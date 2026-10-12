package gui

import (
	"testing"
	"time"
)

// 回归：流身份（streamId=uid）与停止抑制的核心判定。
// 背景（用户实测）：GUI 同时开多个对话窗口时状态错乱——某对话明明已停下，
// 却因别的对话仍在激活而"诡异再次触发、难以停下"。根因是前端仅凭
// sessionId 判定事件归属，同会话的新旧流/跨会话的延迟事件无法区分。
// 后端为每条流分配全局唯一 uid，并新增两条判定：
//   - streamCanEmit：被同会话新流取代、或被 chat.stop 停止的旧流，delta 不再下发；
//   - streamStillCurrent：仍注册的流（含已停止）允许下发 interrupted done 解锁前端。
func TestStreamStopSuppression(t *testing.T) {
	s := NewServer(&fakeRuntime{}, 0, "tok")
	var canceled int
	a := &activeStream{cancel: func() { canceled++ }, gen: 1, uid: 1, sid: "S"}
	s.mu.Lock()
	s.active["S"] = a
	s.active[uidKey(1)] = a
	s.mu.Unlock()

	// 初始：可发 delta、可发 done
	if !s.streamCanEmit("S", 1) {
		t.Fatal("新注册流应可下发 delta")
	}
	if !s.streamStillCurrent("S", 1) {
		t.Fatal("新注册流应为活跃流")
	}

	// chat.stop：置 stopped 并取消 → delta 抑制，但 done 仍允许（interrupted 解锁前端）
	s.mu.Lock()
	a.stopped = true
	a.cancel()
	s.mu.Unlock()
	if s.streamCanEmit("S", 1) {
		t.Fatal("已停止的流不得再下发 delta（残留事件抑制）")
	}
	if !s.streamStillCurrent("S", 1) {
		t.Fatal("已停止的流仍应下发 interrupted done（否则前端无法解锁）")
	}
	if canceled != 1 {
		t.Fatalf("chat.stop 应取消流一次，got %d", canceled)
	}

	// 同会话新流取代旧流：旧流的延迟 delta/done 一律抑制，新流正常
	b := &activeStream{cancel: func() {}, gen: 2, uid: 2, sid: "S"}
	s.mu.Lock()
	s.active["S"] = b
	s.active[uidKey(2)] = b
	s.mu.Unlock()
	if s.streamCanEmit("S", 1) {
		t.Fatal("被新流取代的旧流不得再下发 delta")
	}
	if s.streamStillCurrent("S", 1) {
		t.Fatal("被新流取代的旧流不得再下发 done（否则会打断新流）")
	}
	if !s.streamCanEmit("S", 2) || !s.streamStillCurrent("S", 2) {
		t.Fatal("同会话新流应正常可发")
	}
}

// 回归：uid 键与 streamID/session 键空间隔离——streamID 为空串时，
// 不同会话的流不会因共用 "" 键而互相覆盖（存活判定必须按 uid 精确命中）。
func TestUIDKeyIsolation(t *testing.T) {
	s := NewServer(&fakeRuntime{}, 0, "tok")
	a := &activeStream{uid: 1, sid: "A"}
	b := &activeStream{uid: 2, sid: "B"}
	s.mu.Lock()
	// 模拟 streamID 为空串：两条流都用 "" 作为 streamID 键（后者覆盖前者），
	// 但 uid 键彼此独立。
	s.active[""] = a
	s.active[uidKey(1)] = a
	s.active[""] = b
	s.active[uidKey(2)] = b
	s.mu.Unlock()
	if !s.streamStillCurrent("", 1) {
		t.Fatal("uid=1 应仍存活（不因 streamID 空串覆盖而误判）")
	}
	if !s.streamStillCurrent("", 2) {
		t.Fatal("uid=2 应仍存活")
	}
	if s.streamStillCurrent("", 3) {
		t.Fatal("未注册的 uid 不得判定为存活")
	}
}

// 回归：agent.stream/agent.done 必须携带非零 streamId，且同一条流的所有
// 事件 streamId 一致（前端据此过滤旧流残留事件）。
func TestWS_StreamEvents_CarryStreamID(t *testing.T) {
	rt := &fakeRuntime{}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "sid1", "chat.send", map[string]any{"input": "你好"})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}

	var sid string
	var uid float64
	seenStream := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type != "event" {
			continue
		}
		data, _ := env.Data.(map[string]any)
		switch env.Event {
		case "agent.stream":
			got, _ := data["streamId"].(float64)
			if got == 0 {
				t.Fatalf("agent.stream 缺少非零 streamId: %+v", data)
			}
			if seenStream && got != uid {
				t.Fatalf("同一条流的 streamId 应一致: got %v want %v", got, uid)
			}
			uid, sid, seenStream = got, data["sessionId"].(string), true
		case "agent.done":
			got, _ := data["streamId"].(float64)
			if got == 0 {
				t.Fatalf("agent.done 缺少非零 streamId: %+v", data)
			}
			if seenStream && got != uid {
				t.Fatalf("done 的 streamId 与 stream 不一致: got %v want %v", got, uid)
			}
			if s, _ := data["sessionId"].(string); s != sid {
				t.Fatalf("done 的 sessionId 与 stream 不一致: got %q want %q", s, sid)
			}
			return
		}
	}
	t.Fatal("未收到 agent.done")
}

// 回归：chat.stop 支持按 streamId 精确停止——只停止指定流，不影响其他流。
func TestWS_ChatStop_ByStreamID(t *testing.T) {
	rt := &fakeRuntime{streamDelay: 300 * time.Millisecond}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "st1", "chat.send", map[string]any{"input": "长回答"})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	// 读到一个 agent.stream，捕获 streamId
	var uid float64
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && uid == 0 {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type == "event" && env.Event == "agent.stream" {
			uid, _ = env.Data.(map[string]any)["streamId"].(float64)
		}
	}
	if uid == 0 {
		t.Fatal("未捕获到 agent.stream 的 streamId")
	}
	// 按 streamId 停止
	resp = sendReq(t, conn, "st2", "chat.stop", map[string]any{"streamId": uid})
	if !resp.OK {
		t.Fatalf("chat.stop 失败: %+v", resp.Error)
	}
	// 期望：该流收到 interrupted done（streamId 一致）
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		if err := conn.ReadJSON(&env); err != nil {
			continue
		}
		if env.Type != "event" || env.Event != "agent.done" {
			continue
		}
		data, _ := env.Data.(map[string]any)
		if inter, _ := data["interrupted"].(bool); !inter {
			t.Fatalf("按 streamId 停止后应收到 interrupted done: %+v", data)
		}
		if got, _ := data["streamId"].(float64); got != uid {
			t.Fatalf("interrupted done 的 streamId = %v, want %v", got, uid)
		}
		return
	}
	t.Fatal("按 streamId 停止后未收到 interrupted done")
}
