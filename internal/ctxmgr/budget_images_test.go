package ctxmgr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// imgMsg 构造带图片片段的多模态消息（image_url + text）。
func imgMsg(role, text, url string) types.Message {
	parts := []types.ContentPart{{Type: "text", Text: text}}
	if url != "" {
		parts = append(parts, types.ContentPart{Type: "image_url", ImageURL: &types.ImageURLPart{URL: url}})
	}
	return types.Message{Role: role, Content: parts}
}

// TestMessageTokens_ImageParts 图片片段计入 token 估算：每张固定上限 384 token
// （DeepSeek 官方缩放规则：~800×800 总像素，与 base64 长度无关）+ 85 结构开销；
// 纯图消息不再被"空消息"短路判 0。
func TestMessageTokens_ImageParts(t *testing.T) {
	url := "data:image/png;base64," + strings.Repeat("A", 400) // 前缀 22 + 400 = 422
	msg := imgMsg("user", "", url)
	got := MessageTokens(msg)
	want := 8 + 85 + 384 // 消息级开销 8 + 图片固定 384（官方上限）
	if got != want {
		t.Errorf("纯图消息 MessageTokens = %d，期望 %d", got, want)
	}

	// 文本 + 图片都计入
	msg2 := imgMsg("user", "看图", url)
	got2 := MessageTokens(msg2)
	if got2 <= want {
		t.Errorf("文本+图片 MessageTokens = %d，应大于纯图 %d", got2, want)
	}
}

// TestMessageTokens_ImagePartTokenCap 大图与参考小图估算相同（官方规则：
// 图片自动缩放至 ~800×800，token 上限 384，与文件大小/base64 长度无关）。
// 旧实现按 len(url)/4 估算，单张 5MB 图会算成 ~167 万 token 打爆预算。
func TestMessageTokens_ImagePartTokenCap(t *testing.T) {
	small := "data:image/png;base64," + strings.Repeat("A", 100)
	big := "data:image/png;base64," + strings.Repeat("B", 5<<20) // 5MB 级 base64
	msgSmall := MessageTokens(imgMsg("user", "", small))
	msgBig := MessageTokens(imgMsg("user", "", big))
	if msgSmall != msgBig {
		t.Errorf("大图 %d 与小图 %d 估算应相同（官方 384/张上限）", msgBig, msgSmall)
	}
	if msgBig > 8+85+384 {
		t.Errorf("大图估算 %d 不应超过 8+85+384=%d", msgBig, 8+85+384)
	}
}

// TestMessageTokens_ImagePartsJSONRoundTrip 检查点恢复后 Content 变为 []any，
// 图片 token 仍应被计入（否则恢复的多模态历史被低估 → 压缩不触发 → API 400）。
func TestMessageTokens_ImagePartsJSONRoundTrip(t *testing.T) {
	url := "data:image/png;base64," + strings.Repeat("A", 400)
	msg := imgMsg("user", "看图", url)
	b, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("反序列化失败: %v", err)
	}
	rt := types.Message{Role: "user", Content: raw.(map[string]any)["content"]}
	if n := rt.ImagePartCount(); n != 1 {
		t.Errorf("[]any 形态 ImagePartCount = %d，期望 1", n)
	}
	if got := MessageTokens(rt); got < 8+85+384 {
		t.Errorf("[]any 形态 MessageTokens = %d，应计入图片 token", got)
	}
}

// TestCompactToBudget_DegradesOldImages 预算压缩时旧消息图片降级为占位文本、
// 新消息图片完整保留（多轮识图历史防爆上下文，最新一轮图不丢）。
func TestCompactToBudget_DegradesOldImages(t *testing.T) {
	url := "data:image/png;base64," + strings.Repeat("B", 80000)
	msgs := []types.Message{
		{Role: "system", Content: "system 提示"},
		imgMsg("user", "第一张图", url),
		{Role: "assistant", Content: "回复一"},
		imgMsg("user", "第二张图", url),
		{Role: "assistant", Content: "回复二"},
		imgMsg("user", "第三张图（最新）", url),
		{Role: "assistant", Content: "回复三"},
	}
	// 图片每张 384 token，8 条消息远小于预算 → 不触发压缩（图片不因大小虚高）。
	b := NewTokenBudget(10000)
	b.KeepRecent = 4
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if changed {
		t.Fatalf("图片按官方 384/张计费后不应触发压缩: %d tokens", MessagesTokens(out))
	}
	if len(out) != len(msgs) {
		t.Fatalf("不应有消息被压缩: %d → %d", len(msgs), len(out))
	}
	// 文本超限场景（图片作为最后手段降级）：预算小到只剩文本大头时，
	// 旧消息图片降级、最新一条 user 图片保留。
	bigText := strings.Repeat("X", 30000)
	msgs2 := []types.Message{
		{Role: "system", Content: "system 提示"},
		imgMsg("user", "旧图"+bigText, url),
		{Role: "assistant", Content: "回复一"},
		imgMsg("user", "新图（最新）", url),
	}
	b2 := NewTokenBudget(2000)
	b2.KeepRecent = 4
	out2, changed2 := CompactToBudgetWithEvict(msgs2, b2, nil)
	if !changed2 {
		t.Fatalf("文本超限应触发压缩")
	}
	// 最新一条 user 消息的图片必须完整保留
	last := out2[len(out2)-1]
	if last.Role != "user" || last.ImagePartCount() != 1 {
		t.Fatalf("最新 user 消息图片应保留: role=%s images=%d", last.Role, last.ImagePartCount())
	}
	// 旧 user 图消息的图片应降级为占位
	for _, m := range out2 {
		if m.Role == "user" && strings.Contains(m.TextContent(), "旧图") {
			if m.ImagePartCount() != 0 {
				t.Errorf("旧消息图片应降级: %s", m.TextContent())
			}
			if !strings.Contains(m.TextContent(), types.ImageOmittedPlaceholder) {
				t.Errorf("旧消息应含占位: %q", m.TextContent())
			}
		}
	}
}

// TestCompactToBudget_SingleGiantImageMessage 单条超大图片消息在预算内时
// 不再触发压缩/降级（旧实现按 base64 长度估算 → 一张 5MB 图就爆预算 → 图被降级）。
func TestCompactToBudget_SingleGiantImageMessage(t *testing.T) {
	bigURL := "data:image/png;base64," + strings.Repeat("C", 400000)
	msgs := []types.Message{
		{Role: "system", Content: "system"},
		imgMsg("user", "巨图", bigURL),
	}
	b := NewTokenBudget(4000) // 图片官方计费 384，预算内
	b.KeepRecent = 8
	out, changed := CompactToBudgetWithEvict(msgs, b, nil)
	if changed {
		t.Fatalf("图片按 384/张计费后不应触发压缩（旧实现 10 万 token 会误触发）")
	}
	if len(out) != 2 {
		t.Fatalf("不应有消息被压缩: %d", len(out))
	}
	for _, m := range out {
		if m.ImagePartCount() == 0 && strings.Contains(m.TextContent(), "巨图") {
			t.Fatalf("图片不应被降级: %q", m.TextContent())
		}
	}
}

// TestDegradeImageParts_TextPreserved 降级保留全部文本片段并追加占位。
func TestDegradeImageParts_TextPreserved(t *testing.T) {
	parts := []types.ContentPart{
		{Type: "text", Text: "第一段"},
		{Type: "image_url", ImageURL: &types.ImageURLPart{URL: "data:image/png;base64,AA"}},
		{Type: "text", Text: "第二段"},
	}
	m := types.Message{Role: "user", Content: parts}
	got, ok := degradeImageParts(m)
	if !ok {
		t.Fatalf("应发生降级")
	}
	if got.TextContent() != "第一段第二段"+types.ImageOmittedPlaceholder {
		t.Errorf("降级文本 = %q，期望文本保留 + 占位", got.TextContent())
	}
	if got.ImagePartCount() != 0 {
		t.Errorf("降级后不应再有图片片段")
	}
	// 无图消息：不变化
	plain := types.Message{Role: "user", Content: "纯文本"}
	if _, ok := degradeImageParts(plain); ok {
		t.Errorf("纯文本消息不应被降级")
	}
}
