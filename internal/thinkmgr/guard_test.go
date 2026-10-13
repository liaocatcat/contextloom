package thinkmgr

import "testing"

// TestShouldStopNudging 连续 stallRounds 轮无新证据 → 停止催轮；有变化 → 计数清零（M12）。
func TestShouldStopNudging(t *testing.T) {
	prev := ThinkSignals{ToolCalls: 0, ResultDigest: "digest-a", BodyRunes: 100}

	// 连续两次相同（stallRounds=2）→ 第 2 次达到停止线
	stop, count := ShouldStopNudging(prev, prev, 0, 2)
	if stop || count != 1 {
		t.Fatalf("第 1 次停滞 = (%v, %d), want (false, 1)", stop, count)
	}
	stop, count = ShouldStopNudging(prev, prev, count, 2)
	if !stop || count != 2 {
		t.Fatalf("第 2 次停滞 = (%v, %d), want (true, 2)", stop, count)
	}

	// 有工具调用 → 清零
	if stop, count := ShouldStopNudging(prev,
		ThinkSignals{ToolCalls: 1, ResultDigest: "digest-a", BodyRunes: 100}, 2, 2); stop || count != 0 {
		t.Errorf("工具调用后 = (%v, %d), want (false, 0)", stop, count)
	}
	// 结果摘要变化 → 清零
	if stop, count := ShouldStopNudging(prev,
		ThinkSignals{ResultDigest: "digest-b", BodyRunes: 100}, 1, 2); stop || count != 0 {
		t.Errorf("摘要变化后 = (%v, %d), want (false, 0)", stop, count)
	}
	// 正文变化 → 清零
	if stop, count := ShouldStopNudging(prev,
		ThinkSignals{ResultDigest: "digest-a", BodyRunes: 101}, 1, 2); stop || count != 0 {
		t.Errorf("正文变化后 = (%v, %d), want (false, 0)", stop, count)
	}
	// 有变化且计数已为 0 → 不停止
	if stop, count := ShouldStopNudging(ThinkSignals{}, ThinkSignals{BodyRunes: 1}, 0, 2); stop || count != 0 {
		t.Errorf("有变化 = (%v, %d), want (false, 0)", stop, count)
	}
}
