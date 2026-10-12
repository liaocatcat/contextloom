package gui

import (
	"testing"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// 回归：goal 模式自动续跑不得跨会话串台。
// 背景（用户实测）：GUI 同时开多个对话，某对话明明已停下，却因别的对话仍在
// 激活而"诡异再次触发、难以停下"。goal 循环的监工/目标状态机是 Runtime 级
// 单例（GoalSession/GoalOnRoundEnd 取 currentSession）——本流所属会话一旦被
// 用户切走，继续续跑会对【别的会话】的目标做推进，既让停下的对话不断重触发，
// 又污染其他会话目标状态。
// 修复：每轮续跑前校验"当前活跃会话仍是本流绑定的 sid"。
func TestWS_GoalAutoContinue_StopsOnSessionSwitch(t *testing.T) {
	rt := &fakeRuntime{
		mode:              "goal",
		goalSession:       &types.GoalSession{ID: "g1", Phase: types.GoalPhaseExecute},
		goalContinueTimes: 5, // 若不设防会持续续跑多轮
		goalRoundOutput:   []string{"续跑轮"},
		streamDelay:       20 * time.Millisecond,
	}
	ts, _ := startTestServer(t, rt)
	conn := dialWS(t, ts)

	resp := sendReq(t, conn, "g1", "chat.send", map[string]any{"input": "目标", "sessionId": ""})
	if !resp.OK {
		t.Fatalf("chat.send 失败: %+v", resp.Error)
	}
	sidA := rt.GetSessionID()
	if sidA == "" {
		t.Fatal("chat.send 未自动创建会话")
	}
	// 等第一轮流开始
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&env); err == nil && env.Type == "event" && env.Event == "agent.stream" {
			break
		}
	}
	// 第一轮进行中：用户切到别的会话 B
	resp = sendReq(t, conn, "g2", "chat.loadSession", map[string]any{"uuid": "sess-B"})
	if !resp.OK {
		t.Fatalf("loadSession 失败: %+v", resp.Error)
	}
	if rt.GetSessionID() != "sess-B" {
		t.Fatalf("切换后当前会话 = %q, want sess-B", rt.GetSessionID())
	}
	// 收集事件：第一轮 done 之后【不得】再出现 goal.round / 第二轮 agent.stream
	// （跨会话串台防护：会话已切走 → 不续跑）
	sawFirstDone := false
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var env Envelope
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if err := conn.ReadJSON(&env); err != nil {
			if sawFirstDone {
				break // 第一轮 done 后静默：符合预期
			}
			continue
		}
		if env.Type != "event" {
			continue
		}
		data, _ := env.Data.(map[string]any)
		switch env.Event {
		case "goal.round":
			t.Fatalf("会话已切走，goal 循环不得继续（收到 goal.round: %+v）", data)
		case "agent.stream":
			if sawFirstDone {
				t.Fatalf("会话已切走，不得再发起新一轮流（收到 agent.stream: %+v）", data)
			}
		case "agent.done":
			if s, _ := data["sessionId"].(string); s != sidA {
				t.Fatalf("第一轮 done 的 sessionId = %q, want %q", s, sidA)
			}
			sawFirstDone = true
		}
	}
	if !sawFirstDone {
		t.Fatal("未收到第一轮 agent.done")
	}
	rt.mu.Lock()
	rounds, ends := rt.goalRounds, rt.goalRoundEnds
	rt.mu.Unlock()
	if rounds != 0 {
		t.Fatalf("会话切走后不得续跑（ProcessGoalRound 调用 %d 次）", rounds)
	}
	if ends != 0 {
		t.Fatalf("会话切走后不得做轮末评估（GoalOnRoundEnd 调用 %d 次）", ends)
	}
}
