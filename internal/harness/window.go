package harness

import (
	"sort"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

const (
	RegionAnchor = "anchor"
	RegionCore   = "core"
	RegionRef    = "reference"
)

type SlidingWindow struct {
	anchorRatio float64
	slots       []types.WindowSlot
}

func NewSlidingWindow(anchorRatio float64) *SlidingWindow {
	return &SlidingWindow{anchorRatio: anchorRatio}
}

func (sw *SlidingWindow) Arrange(blocks []*types.ContextBlock) {
	if len(blocks) == 0 {
		sw.slots = nil
		return
	}

	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Score > blocks[j].Score
	})

	total := len(blocks)
	anchorCount := max(1, int(float64(total)*sw.anchorRatio))
	refCount := max(1, int(float64(total)*(1-sw.anchorRatio)*0.2))
	coreCount := total - anchorCount - refCount
	if coreCount < 0 {
		coreCount = total - anchorCount
		if coreCount < 0 {
			coreCount = 0
		}
	}

	anchorEnd := anchorCount
	coreEnd := anchorCount + coreCount
	if coreEnd > total {
		coreEnd = total
	}

	sw.slots = []types.WindowSlot{
		{Region: RegionAnchor, Blocks: derefBlocks(blocks[:anchorEnd])},
		{Region: RegionCore, Blocks: derefBlocks(blocks[anchorEnd:coreEnd])},
		{Region: RegionRef, Blocks: derefBlocks(blocks[coreEnd:])},
	}
}

// arrangeBlocks arranges value-type blocks, used by Engine.Search for thread safety.
func (sw *SlidingWindow) arrangeBlocks(blocks []types.ContextBlock) {
	if len(blocks) == 0 {
		sw.slots = nil
		return
	}

	sort.Slice(blocks, func(i, j int) bool {
		return blocks[i].Score > blocks[j].Score
	})

	total := len(blocks)
	anchorCount := max(1, int(float64(total)*sw.anchorRatio))
	refCount := max(1, int(float64(total)*(1-sw.anchorRatio)*0.2))
	coreCount := total - anchorCount - refCount
	if coreCount < 0 {
		coreCount = total - anchorCount
		if coreCount < 0 {
			coreCount = 0
		}
	}

	anchorEnd := anchorCount
	coreEnd := anchorCount + coreCount
	if coreEnd > total {
		coreEnd = total
	}

	sw.slots = []types.WindowSlot{
		{Region: RegionAnchor, Blocks: blocks[:anchorEnd]},
		{Region: RegionCore, Blocks: blocks[anchorEnd:coreEnd]},
		{Region: RegionRef, Blocks: blocks[coreEnd:]},
	}

	// 锚定区首尾重复（§4.4）：Score≥95 的块在参考区开头插入 KEY_REMIND
	var reminds []types.ContextBlock
	for _, b := range blocks[:anchorEnd] {
		if b.Score >= 95 {
			reminds = append(reminds, types.ContextBlock{
				ID:      "KEY_REMIND_" + b.ID,
				Content: "KEY_REMIND: " + truncateStr(b.Content, 100),
				Score:   b.Score,
			})
		}
	}
	// 只在参考区有内容时才插入提醒
	if len(reminds) > 0 && len(sw.slots) >= 3 && len(sw.slots[2].Blocks) > 0 {
		sw.slots[2].Blocks = append(reminds, sw.slots[2].Blocks...)
	}
}

func (sw *SlidingWindow) Slots() []types.ContextBlock {
	var all []types.ContextBlock
	for _, slot := range sw.slots {
		all = append(all, slot.Blocks...)
	}
	// 锚定区首尾重复（需求 §4.4）：仅在上下文块较多时触发，避免小数据集副作用
	if len(all) > 10 && len(sw.slots) > 0 && len(sw.slots[0].Blocks) > 0 {
		top := sw.slots[0].Blocks[0]
		if int(top.Score) >= 85 {
			remind := top
			remind.ID = "KEY_REMIND:" + top.ID
			remind.Content = "KEY_REMIND: " + top.ID
			all = append(all, remind)
		}
	}
	return all
}

func derefBlocks(blocks []*types.ContextBlock) []types.ContextBlock {
	result := make([]types.ContextBlock, len(blocks))
	for i, b := range blocks {
		result[i] = *b
	}
	return result
}

func truncateStr(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-3]) + "..."
}
