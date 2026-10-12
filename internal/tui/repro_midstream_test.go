package tui

import (
	"testing"
)

// 回归：对话进行中插入新对话（生成中按 Enter 发送）——旧内容必须收尾保留，
// 不被静默丢弃（"所有内容重新开始"根因修复）。
func TestMidStreamSend_FinalizesOldStream(t *testing.T) {
	m := initialModel(nil)
	m.streaming = true
	m.streamBuf = "第一段已完成的内容"
	m.chatInput = "插入的新问题"
	m.mode = modeChat

	// 旧流代际（send 前）
	oldGen := m.streamGen

	m2, _ := m.send()
	mm := m2.(*model)

	// ① 旧内容已落为消息（不丢失）
	found := false
	for _, msg := range mm.messages {
		if msg.Content == "第一段已完成的内容" {
			found = true
		}
	}
	if !found {
		t.Errorf("生成中插入对话：旧回复内容丢失（应落为消息保留）")
	}
	// ② 旧流缓冲已清空（新流从干净状态开始）
	if mm.streamBuf != "" {
		t.Errorf("新流应从干净状态开始，streamBuf=%q", mm.streamBuf)
	}
	// ③ 代际已推进（旧流残留消息将被丢弃）
	if mm.streamGen == oldGen {
		t.Errorf("插入新对话应推进 streamGen（旧流消息作废），仍为 %d", mm.streamGen)
	}
	// ④ 旧流已取消（cancelFn 已清）
	if mm.cancelFn != nil {
		t.Errorf("插入对话后旧流的 cancelFn 应已清空")
	}
}

// 回归：旧流残留 token 到达时按代际丢弃，不污染新对话。
func TestStaleStreamTokenDiscarded(t *testing.T) {
	m := initialModel(nil)
	m.streaming = true
	m.streamBuf = "旧流内容"
	m.chatInput = "新问题"
	m.mode = modeChat
	m2, _ := m.send() // 推进代际并启动新流
	mm := m2.(*model)

	// 模拟新流已就绪（nil runtime 下 send 不会真正进入 streaming）
	mm.streaming = true
	mm.streamBuf = ""
	newGen := mm.streamGen
	stale := newGen - 1

	// 旧代际的 token 到达 → 必须被丢弃
	res, _ := mm.Update(streamMsg{token: "旧流残留", gen: stale})
	mm = res.(*model)
	if mm.streamBuf != "" {
		t.Errorf("旧流残留 token 污染了新对话，streamBuf=%q", mm.streamBuf)
	}
	// 旧代际的 done 到达 → 不得结束新流
	res, _ = mm.Update(streamDoneMsg{gen: stale})
	mm = res.(*model)
	if !mm.streaming {
		t.Errorf("旧流的 done 不得结束新对话的流")
	}
	// 当前代际的 token 正常累积（对照组）
	res, _ = mm.Update(streamMsg{token: "新流内容", gen: newGen})
	mm = res.(*model)
	if mm.streamBuf != "新流内容" {
		t.Errorf("当前代际 token 应正常累积，streamBuf=%q", mm.streamBuf)
	}
}
