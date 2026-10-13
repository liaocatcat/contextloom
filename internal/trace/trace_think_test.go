package trace

import (
	"encoding/json"
	"testing"
)

// thinkRoundDetail 与 thinkmgr.ThinkRound 的 JSON 形状一致（trace 包不依赖
// thinkmgr，测试用等价结构验证 detail 往返可解析）。
type thinkRoundDetail struct {
	Round    int    `json:"round"`
	Kind     string `json:"kind"`
	HasTools bool   `json:"has_tools"`
	ToolN    int    `json:"tool_n"`
	BodyLen  int    `json:"body_len"`
	Reason   string `json:"reason"`
}

// TestTraceThinkRound_EventAndDetail M11：EventThinkRound 事件写入与 JSON detail 可解析。
func TestTraceThinkRound_EventAndDetail(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, true)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer r.Close()

	want := thinkRoundDetail{Round: 2, Kind: "verify", HasTools: true, ToolN: 3, BodyLen: 128, Reason: "验证轮"}
	detail, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}

	r.TraceThinkRound("task", 2, string(detail))

	recent := r.Recent(10)
	if len(recent) != 1 {
		t.Fatalf("应写入 1 条，got %d", len(recent))
	}
	e := recent[0]
	if e.Event != EventThinkRound {
		t.Fatalf("Event 应为 %q，got %q", EventThinkRound, e.Event)
	}
	if EventThinkRound != "think_round" {
		t.Fatalf("EventThinkRound 常量值应为 think_round，got %q", EventThinkRound)
	}
	if e.Mode != "task" || e.Round != 2 {
		t.Fatalf("Mode/Round 异常: %+v", e)
	}
	var got thinkRoundDetail
	if err := json.Unmarshal([]byte(e.Detail), &got); err != nil {
		t.Fatalf("Detail 应可解析为 JSON: %v (detail=%q)", err, e.Detail)
	}
	if got != want {
		t.Fatalf("detail 往返不一致: got %+v want %+v", got, want)
	}
}

// TestTraceThinkRound_Disabled 零回归：Recorder 关闭时零事件零落盘。
func TestTraceThinkRound_Disabled(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, false)
	if err != nil {
		t.Fatalf("New disabled: %v", err)
	}
	r.TraceThinkRound("task", 1, `{"round":1}`)
	r.Close()
	if got := r.Recent(5); got != nil {
		t.Fatalf("disabled 时 Recent 应为 nil，got %+v", got)
	}
}
