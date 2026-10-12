package harness

import (
	"strings"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// TestTokenize_ChineseBigram 验证中文分词产生 bigram（单字+双字组合），
// 而非之前仅单字，导致匹配发散。
func TestTokenize_ChineseBigram(t *testing.T) {
	tokens := tokenize("总结项目特征")
	joined := strings.Join(tokens, "|")

	// 必须含 bigram：bigram 命中比单字匹配更可靠
	mustContain := []string{
		"总结", // bigram
		"项目", // bigram
		"特征", // bigram
		"总",  // 单字兜底
		"特",  // 单字兜底
	}
	for _, want := range mustContain {
		if !strings.Contains(joined, want) {
			t.Errorf("分词应含 %q，got: %s", want, joined)
		}
	}

	// 不应有跨标点 bigram
	crossPunct := tokenize("总结。项目")
	cpJoined := strings.Join(crossPunct, "|")
	if strings.Contains(cpJoined, "结项") {
		t.Errorf("跨标点不应产生 bigram：'总结。项目' → %s", cpJoined)
	}
	t.Logf("'总结项目特征' → %s", joined)
}

// TestScorer_ChineseRelevance 验证中文查询：相关块得分 > 无关块
func TestScorer_ChineseRelevance(t *testing.T) {
	s := NewRelevanceScorer()

	query := "总结项目的技术特征"
	related := &types.ContextBlock{
		ID:      "ctx-1",
		Content: "本项目技术特征包括：Go 语言、Bubble Tea TUI、BoltDB 持久化、JSON 极简提示词",
	}
	unrelated := &types.ContextBlock{
		ID:      "ctx-2",
		Content: "hello world this is a test file for english content only",
	}

	relScore := s.Score(query, related)
	unrScore := s.Score(query, unrelated)
	t.Logf("相关块得分: %.2f, 无关块得分: %.2f", relScore, unrScore)

	if relScore <= unrScore {
		t.Errorf("相关块得分(%.2f) 应高于无关块(%.2f)", relScore, unrScore)
	}
	if relScore <= 0 {
		t.Error("相关块应有正分数")
	}
}

// TestTokenize_EnglishUnchanged 确认英文/代码分词不受 bigram 改进影响
func TestTokenize_EnglishUnchanged(t *testing.T) {
	tokens := tokenize("hello_world ReadFile")
	joined := strings.Join(tokens, "|")
	t.Logf("'hello_world ReadFile' → %s", joined)

	if !strings.Contains(joined, "hello_world") {
		t.Error("英文单词应保留")
	}
	if !strings.Contains(joined, "readfile") {
		t.Error("英文单词应小写")
	}
}

// TestTokenize_MixedChineseEnglish 混排文本：中文 bigram + 英文单词
func TestTokenize_MixedChineseEnglish(t *testing.T) {
	tokens := tokenize("实现 users.go 的登录功能")
	joined := strings.Join(tokens, "|")
	t.Logf("'实现 users.go 的登录功能' → %s", joined)

	// 英文单词
	if !strings.Contains(joined, "users") || !strings.Contains(joined, "go") {
		t.Errorf("英文 token 应保留: %s", joined)
	}
	// 中文 bigram
	if !strings.Contains(joined, "实现") || !strings.Contains(joined, "登录") || !strings.Contains(joined, "功能") {
		t.Errorf("中文 bigram 应存在: %s", joined)
	}
}
