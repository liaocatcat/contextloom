package thinkmgr

// ── 护栏（M12）──

// ThinkSignals 单轮证据信号（主循环确定性采集）。
type ThinkSignals struct {
	ToolCalls    int    // 本轮工具调用数
	ResultDigest string // 工具结果摘要（无工具=空）
	BodyRunes    int    // 正文长度（runes）
}

// ShouldStopNudging 边际收益检测：连续 stallRounds 轮无新证据（无工具调用、结果摘要与正文均无变化）
// → 停止催轮。返回（是否停止, 更新后的连续停滞计数）；有变化则计数清零。
func ShouldStopNudging(prev, cur ThinkSignals, stallCount, stallRounds int) (bool, int) {
	same := cur.ToolCalls == 0 && cur.ResultDigest == prev.ResultDigest && cur.BodyRunes == prev.BodyRunes
	if same {
		stallCount++
	} else {
		stallCount = 0
	}
	return stallCount >= stallRounds, stallCount
}
