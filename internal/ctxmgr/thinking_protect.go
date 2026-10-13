// 设计文档：docs/contextloom改造详细设计-多轮思考触发.md §3.6（M10 思考保护）。
//
// 背景：thinking 模型的 ReasoningContent 是上下文占用大头，压缩器会把
// 它当普通大字段削掉（step3 二次截断 / step4 折半兜底）——但验证轮的
// "上一轮思考"是复核的关键证据，且结论通常在思考末尾，前缀截断恰好丢
// 结论。本文件提供两个原语：
//
//  1. MarkProtected：按策略给消息打"思考保护"标记（零值=恒等返回，零回归）；
//     压缩时带标记消息的 ReasoningContent 不参与截断目标竞争（正文仍可削）。
//  2. ThinkingSummary：提取最近一段思考的尾段（≤maxRunes runes）作为
//     "[历史思考摘要]（已压缩）" 注入系统侧，兜底"思考被压缩后结论丢失"。
//
// 尾段提取思路复用 internal/runtime/helpers.go PromoteThinkBody（只读参考，
// 不改 runtime 包）。
package ctxmgr

import (
	"strings"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ThinkingRetention 压缩时的思考保护策略（零值 = 现行为，零回归）。
type ThinkingRetention struct {
	// ProtectVerifyRound 验证轮保护：对上一轮 assistant 消息的思考
	// （ReasoningContent）打保护标记，压缩选择截断目标时跳过（正文仍可削）。
	ProtectVerifyRound bool
	// ProtectRounds 保护最近 N 轮思考不被折半（默认 0 = 现行为）。
	// 与 ProtectVerifyRound 叠加时取较大覆盖范围（验证轮保护等价于 N=1）。
	ProtectRounds int
}

// ThinkingSummaryMarker 思考摘要来源标记（设计文档 §3.6：以
// "[历史思考摘要]（已压缩）" 注入系统侧，标注来源与"已压缩"状态）。
const ThinkingSummaryMarker = "[历史思考摘要]（已压缩）"

// DefaultThinkingSummaryRunes 摘要默认上限（设计文档 §3.6/§6.1 #11：≤400 runes）。
const DefaultThinkingSummaryRunes = 400

// MarkProtected 按策略给需要保护的思考打标记，返回打标后的消息切片。
//
// 零值策略（ProtectVerifyRound=false 且 ProtectRounds<=0）恒等返回入参切片
// 本身：压缩行为与现状逐字一致（零回归）。
//
// 保护语义：
//   - 只标记含思考（ReasoningContent != ""）的 assistant 消息——思考是验证轮
//     复核的关键证据；user/tool 消息没有思考，system 恒受既有保护无需标记；
//   - 从最新往前数，标记最近 n 条含思考的 assistant 消息（n=ProtectRounds；
//     ProtectVerifyRound=true 时至少 n=1，即"上一轮"）；
//   - 打标不修改入参（包内约定"不修改原切片"）：非零策略返回浅拷贝后打标；
//     消息为值类型，压缩各步重建切片时标记自然携带、不丢。
func MarkProtected(msgs []types.Message, r ThinkingRetention) []types.Message {
	if !r.ProtectVerifyRound && r.ProtectRounds <= 0 {
		return msgs // 零值：恒等返回
	}
	n := r.ProtectRounds
	if n < 1 {
		n = 1 // ProtectVerifyRound=true 且未显式给轮数：保护最近 1 轮（上一轮）
	}
	idxs := make([]int, 0, n)
	for i := len(msgs) - 1; i >= 0 && len(idxs) < n; i-- {
		if msgs[i].Role == "assistant" && msgs[i].ReasoningContent != "" {
			idxs = append(idxs, i)
		}
	}
	if len(idxs) == 0 {
		return msgs // 无思考可保护：恒等返回（不产生无谓拷贝）
	}
	out := make([]types.Message, len(msgs))
	copy(out, msgs)
	for _, i := range idxs {
		out[i].ReasoningProtected = true
	}
	return out
}

// ThinkingSummary 提取最近一段思考的尾段摘要（≤maxRunes runes，含来源标记），
// 无思考内容返回 ""。maxRunes<=0 时用 DefaultThinkingSummaryRunes(400)。
//
// 取尾段而非前缀：结论通常写在思考末尾（复用 PromoteThinkBody 的尾段提取
// 思路）；被裁掉前段时加"思考前段略"提示。整串（来源标记+尾段）总长硬约束
// ≤maxRunes，保证注入系统侧不挤占预算（设计文档 §6.1 #11）。
func ThinkingSummary(msgs []types.Message, maxRunes int) string {
	if maxRunes <= 0 {
		maxRunes = DefaultThinkingSummaryRunes
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		t := strings.TrimSpace(msgs[i].ReasoningContent)
		if t == "" {
			continue
		}
		head := ThinkingSummaryMarker + "\n"
		tailBudget := maxRunes - len([]rune(head))
		if tailBudget < 1 {
			tailBudget = 1
		}
		r := []rune(t)
		tail := t
		if len(r) > tailBudget {
			const hint = "…（思考前段略，仅取结论尾段）"
			if hb := len([]rune(hint)); hb < tailBudget {
				tail = hint + string(r[len(r)-(tailBudget-hb):])
			} else {
				tail = string(r[len(r)-tailBudget:])
			}
		}
		out := head + tail
		if rr := []rune(out); len(rr) > maxRunes { // 硬上限兜底（极端 maxRunes 配置）
			out = string(rr[:maxRunes])
		}
		return out
	}
	return ""
}
