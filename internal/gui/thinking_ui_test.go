package gui

// 本次改造 GUI 层测试：status.bar 对多轮思考状态（thinking 可选字段）的
// 透传与缺失兼容。
//
// 零回归断言：旧 runtime 的 ModelInfo 不含 thinking 时，status.bar.model
// 不得带该键（前端字段缺失不渲染）；runtime 侧扩展 ModelInfo 后原样透传。
// 说明：gui 包既有 fakeRuntime 的 ModelInfo 不含 thinking 字段，
// 本文件用嵌入覆盖（thinkSpyRuntime）补齐，不修改既有测试文件。

import (
	"testing"
)

// thinkSpyRuntime 在 fakeRuntime 之上返回可选 thinking 字段的 ModelInfo。
type thinkSpyRuntime struct {
	*fakeRuntime
	thinking map[string]any
}

func (t *thinkSpyRuntime) ModelInfo() map[string]any {
	mi := map[string]any{"backend": "deepseek", "model": "deepseek-v4", "effort": "max", "maxContext": 131072}
	if t.thinking != nil {
		mi["thinking"] = t.thinking
	}
	return mi
}

// TestWS_StatusBar_ThinkingPassthrough：
//  1. 无 thinking（旧 runtime）→ status.bar.model 无该键（零回归）；
//  2. 有 thinking（runtime 落盘后）→ 原样透传各字段。
func TestWS_StatusBar_ThinkingPassthrough(t *testing.T) {
	// 1) 旧 runtime：无 thinking 字段
	rtOld := &thinkSpyRuntime{fakeRuntime: &fakeRuntime{mode: "chat"}}
	ts, _ := startTestServer(t, rtOld)
	conn := dialWS(t, ts)
	resp := sendReq(t, conn, "1", "status.bar", nil)
	if !resp.OK {
		t.Fatalf("status.bar 失败: %v", resp.Error)
	}
	result, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("status.bar 结果类型异常: %T", resp.Result)
	}
	modelField, ok := result["model"].(map[string]any)
	if !ok {
		t.Fatalf("status.bar 缺 model 字段: %#v", result)
	}
	if _, exists := modelField["thinking"]; exists {
		t.Fatalf("旧 runtime 无 thinking 数据时不应带该键（零回归），got: %#v", modelField["thinking"])
	}

	// 2) 新 runtime：thinking 原样透传
	rtNew := &thinkSpyRuntime{
		fakeRuntime: &fakeRuntime{mode: "chat"},
		thinking: map[string]any{
			"policy": "strict", "round": 2, "maxRound": 3,
			"verify": "auto", "verifying": true, "source": "override",
		},
	}
	ts2, _ := startTestServer(t, rtNew)
	conn2 := dialWS(t, ts2)
	resp2 := sendReq(t, conn2, "2", "status.bar", nil)
	if !resp2.OK {
		t.Fatalf("status.bar 失败: %v", resp2.Error)
	}
	result2, ok := resp2.Result.(map[string]any)
	if !ok {
		t.Fatalf("status.bar 结果类型异常: %T", resp2.Result)
	}
	modelField2, ok := result2["model"].(map[string]any)
	if !ok {
		t.Fatalf("status.bar 缺 model 字段: %#v", result2)
	}
	th, ok := modelField2["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking 应透传为对象, got: %#v", modelField2["thinking"])
	}
	// JSON number 经 WS 解码为 float64
	if th["policy"] != "strict" || th["round"] != float64(2) || th["maxRound"] != float64(3) ||
		th["verify"] != "auto" || th["verifying"] != true || th["source"] != "override" {
		t.Fatalf("thinking 字段透传异常: %#v", th)
	}
}
