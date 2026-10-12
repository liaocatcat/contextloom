package harness

import (
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func makeBlocks(scores ...float64) []*types.ContextBlock {
	blocks := make([]*types.ContextBlock, len(scores))
	for i, s := range scores {
		blocks[i] = &types.ContextBlock{
			ID:      string(rune('a' + i)),
			Content: "content",
			Score:   types.Score(s),
		}
	}
	return blocks
}

func TestSlidingWindow_Arrange_Empty(t *testing.T) {
	sw := NewSlidingWindow(0.2)
	sw.Arrange(nil)
	if len(sw.slots) != 0 {
		t.Errorf("expected 0 slots for nil input, got %d", len(sw.slots))
	}

	sw.Arrange([]*types.ContextBlock{})
	if len(sw.slots) != 0 {
		t.Errorf("expected 0 slots for empty input, got %d", len(sw.slots))
	}
}

func TestSlidingWindow_Arrange_RegionsExist(t *testing.T) {
	sw := NewSlidingWindow(0.3)
	blocks := makeBlocks(90, 80, 70, 60, 50, 40, 30, 20, 10, 5)
	sw.Arrange(blocks)

	if len(sw.slots) != 3 {
		t.Fatalf("expected 3 slots, got %d", len(sw.slots))
	}

	if sw.slots[0].Region != RegionAnchor {
		t.Errorf("first slot should be %s, got %s", RegionAnchor, sw.slots[0].Region)
	}
	if sw.slots[1].Region != RegionCore {
		t.Errorf("second slot should be %s, got %s", RegionCore, sw.slots[1].Region)
	}
	if sw.slots[2].Region != RegionRef {
		t.Errorf("third slot should be %s, got %s", RegionRef, sw.slots[2].Region)
	}
}

func TestSlidingWindow_Arrange_SortsByScoreDesc(t *testing.T) {
	sw := NewSlidingWindow(0.3)
	// Use 10 blocks so anchorCount = max(1, int(10*0.3)) = 3
	blocks := makeBlocks(30, 90, 10, 80, 50, 70, 20, 60, 40, 100)
	sw.Arrange(blocks)

	// Verify descending order in anchor region
	anchorBlocks := sw.slots[0].Blocks
	if len(anchorBlocks) < 2 {
		t.Fatalf("expected at least 2 anchor blocks, got %d", len(anchorBlocks))
	}
	if anchorBlocks[0].Score < anchorBlocks[1].Score {
		t.Errorf("blocks not sorted descending: %v then %v", anchorBlocks[0].Score, anchorBlocks[1].Score)
	}
	// Top score should be 100
	if anchorBlocks[0].Score != 100 {
		t.Errorf("expected top score 100, got %v", anchorBlocks[0].Score)
	}
}

func TestSlidingWindow_Arrange_NegativeCoreCount(t *testing.T) {
	// With very high anchor ratio (0.9), core count may become negative
	// and should be clamped to 0
	sw := NewSlidingWindow(0.99)
	blocks := makeBlocks(90, 80, 70)

	// Should not panic
	sw.Arrange(blocks)

	totalBlocks := 0
	for _, slot := range sw.slots {
		totalBlocks += len(slot.Blocks)
	}
	if totalBlocks != len(blocks) {
		t.Errorf("expected %d total blocks, got %d", len(blocks), totalBlocks)
	}
}

func TestSlidingWindow_Slots_ReturnsAllBlocks(t *testing.T) {
	sw := NewSlidingWindow(0.2)
	blocks := makeBlocks(90, 80, 70, 60, 50)
	sw.Arrange(blocks)

	result := sw.Slots()
	if len(result) != len(blocks) {
		t.Errorf("expected %d blocks in result, got %d", len(blocks), len(result))
	}
}

func TestSlidingWindow_Slots_ReturnsCopy(t *testing.T) {
	sw := NewSlidingWindow(0.2)
	blocks := makeBlocks(90, 80, 70)
	sw.Arrange(blocks)

	result := sw.Slots()
	// Modify result - should not affect internal slots
	if len(result) > 0 {
		result[0].Content = "modified"
	}

	slotsAgain := sw.Slots()
	if len(slotsAgain) > 0 && slotsAgain[0].Content == "modified" {
		t.Error("Slots should return a copy, not reference internal data")
	}
}

func TestNewSlidingWindow_DefaultRatio(t *testing.T) {
	sw := NewSlidingWindow(0.0)
	if sw.anchorRatio != 0.0 {
		t.Errorf("expected anchorRatio 0.0, got %f", sw.anchorRatio)
	}

	sw2 := NewSlidingWindow(0.5)
	if sw2.anchorRatio != 0.5 {
		t.Errorf("expected anchorRatio 0.5, got %f", sw2.anchorRatio)
	}
}

func TestSlidingWindow_Arrange_SingleBlock(t *testing.T) {
	sw := NewSlidingWindow(0.3)
	blocks := makeBlocks(50)
	sw.Arrange(blocks)

	result := sw.Slots()
	if len(result) != 1 {
		t.Errorf("expected 1 block, got %d", len(result))
	}
	// With 1 block and 0.3 ratio: anchorCount = max(1, 0) = 1, refCount = max(1, 0) = 1, coreCount = 1 - 1 - 1 = -1 -> 0
	// So anchor has the only block, core empty, ref empty
	if len(sw.slots[0].Blocks) != 1 {
		t.Errorf("anchor should have 1 block, got %d", len(sw.slots[0].Blocks))
	}
}

func TestSlidingWindow_Arrange_TwoBlocks(t *testing.T) {
	sw := NewSlidingWindow(0.5)
	blocks := makeBlocks(90, 50)
	sw.Arrange(blocks)

	result := sw.Slots()
	if len(result) != 2 {
		t.Errorf("expected 2 blocks, got %d", len(result))
	}
	// 2 blocks, 0.5 ratio: anchorCount = max(1, 1) = 1, refCount = max(1, 0) = 1, coreCount = 2-1-1 = 0
	if sw.slots[0].Blocks[0].Score != 90 {
		t.Errorf("anchor should have highest scored block, got %v", sw.slots[0].Blocks[0].Score)
	}
}
