package harness

import (
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

func TestTokenizeFeatures_CJK(t *testing.T) {
	feats := tokenizeFeatures("登录超时")
	// 登录/录超/超时 三个 bigram + 同义词扩展（login/signin/timeout）
	expect := map[string]bool{"登录": true, "录超": true, "超时": true,
		"w:login": true, "w:timeout": true}
	for _, f := range feats {
		if expect[f] {
			delete(expect, f)
		}
	}
	if len(expect) > 0 {
		t.Errorf("缺少特征 %v; got %v", expect, feats)
	}
}

func TestTokenizeFeatures_ASCII(t *testing.T) {
	feats := tokenizeFeatures("timeout")
	// w:timeout + 3-gram: tim/ime/meo/eou/out
	var hasWord, hasGram bool
	for _, f := range feats {
		if f == "w:timeout" {
			hasWord = true
		}
		if f == "g:tim" {
			hasGram = true
		}
	}
	if !hasWord || !hasGram {
		t.Errorf("ASCII 特征应含整词与 3-gram: %v", feats)
	}
}

func TestTokenizeFeatures_Mixed(t *testing.T) {
	feats := tokenizeFeatures("修登录timeout问题")
	all := map[string]bool{}
	for _, f := range feats {
		all[f] = true
	}
	for _, want := range []string{"修登", "登录", "w:timeout", "g:tim", "问题"} {
		if !all[want] {
			t.Errorf("混合文本应含特征 %q: %v", want, feats)
		}
	}
	// 中英同义词扩展：timeout → 中文"超时"
	if !all["超时"] {
		t.Errorf("英文词应扩展中文同义词: %v", feats)
	}
}

func TestSemanticScore_SameText(t *testing.T) {
	s := SemanticScore("登录功能实现", "登录功能实现")
	if s < 0.99 {
		t.Errorf("相同文本相似度应≈1, got %.4f", s)
	}
}

func TestSemanticScore_Unrelated(t *testing.T) {
	s := SemanticScore("登录超时问题", "天气很好今天出去散步")
	if s > 0.1 {
		t.Errorf("无关文本相似度应接近 0, got %.4f", s)
	}
}

func TestSemanticScore_FuzzyChinese(t *testing.T) {
	// 模糊查询"修登录超时" vs 代码注释"登录接口 timeout 处理"
	query := "修登录超时"
	direct := SemanticScore(query, "登录接口的 timeout 处理逻辑位于此函数")
	unrelated := SemanticScore(query, "文件上传的进度条样式调整")
	if direct <= unrelated {
		t.Errorf("语义命中应显著高于无关文本: direct=%.4f unrelated=%.4f", direct, unrelated)
	}
	if direct < 0.15 {
		t.Errorf("语义命中分过低: %.4f", direct)
	}
}

func TestSemanticScore_CrossLanguage(t *testing.T) {
	// 跨语言：中文查询 vs 英文实现（同义词表扩展后应命中）
	query := "登录超时"
	enText := "handle login timeout and session expiry"
	cnText := "处理购物车结算流程"
	if SemanticScore(query, enText) <= SemanticScore(query, cnText) {
		t.Error("跨语言语义命中应高于无关中文")
	}
}

func TestSemanticRerank_BoostAndNoDilute(t *testing.T) {
	// 关键词强命中（高分）不被稀释；低分但语义命中的候选获得提升
	blocks := []types.ContextBlock{
		{ID: "strong", Content: "登录 timeout 处理函数", Score: 90},
		{ID: "fuzzy", Content: "认证会话过期处理 timeout 相关代码", Score: 40},
		{ID: "unrel", Content: "样式调整按钮颜色", Score: 60},
	}
	before := blocks[0].Score
	semanticRerank("登录超时", blocks, 30)
	if blocks[0].Score < before {
		t.Errorf("关键词强命中不应被稀释: before=%v after=%v", before, blocks[0].Score)
	}
	if blocks[1].Score <= 40 {
		t.Errorf("语义命中候选应被提升: %v", blocks[1].Score)
	}
	if blocks[2].Score != 60 {
		t.Errorf("无关候选分不应变化: %v", blocks[2].Score)
	}
}

func TestSemanticRerank_Limit(t *testing.T) {
	blocks := make([]types.ContextBlock, 50)
	for i := range blocks {
		blocks[i] = types.ContextBlock{ID: string(rune('a'+i%26)) + "x", Content: "登录 timeout 处理", Score: 50}
	}
	// 只处理前 30：不 panic 即可，且不超 100
	semanticRerank("登录超时", blocks, 30)
	for _, b := range blocks[:30] {
		if b.Score > 100 {
			t.Errorf("分数超上限: %v", b.Score)
		}
	}
}

func TestSemanticCache_Invalidate(t *testing.T) {
	id := "cache-test"
	blockFeatures(id, "登录 timeout 处理")
	if _, ok := semanticCache.Load(id); !ok {
		t.Fatal("缓存应存在")
	}
	invalidateSemantic(id)
	if _, ok := semanticCache.Load(id); ok {
		t.Fatal("失效后缓存应删除")
	}
}

func TestSemanticScore_Empty(t *testing.T) {
	if s := SemanticScore("", "内容"); s != 0 {
		t.Errorf("空查询应 0, got %v", s)
	}
	if s := SemanticScore("查询", ""); s != 0 {
		t.Errorf("空文本应 0, got %v", s)
	}
}

func TestQueryCoverage_Empty(t *testing.T) {
	if c := queryCoverage(nil, nil); c != 0 {
		t.Errorf("空向量覆盖率应 0, got %v", c)
	}
}

// ── SemanticScore 集成（embedding 余弦融合层）──

func TestSemanticScore_CrossLanguageCode(t *testing.T) {
	// 核心验收：中文查询 → 英文代码块（语义相关）应高于无关英文
	query := "修复登录超时"
	code := "func handleLoginTimeout() { retry with backoff }"
	unrelated := "apple banana cherry"
	if SemanticScore(query, code) <= SemanticScore(query, unrelated) {
		t.Errorf("英文代码块语义命中应高于无关文本: code=%.4f unrelated=%.4f",
			SemanticScore(query, code), SemanticScore(query, unrelated))
	}
}

func TestSemanticCosine_CrossLanguage(t *testing.T) {
	// 稠密向量余弦：中文查询 vs 英文实现 > 中文查询 vs 无关中文
	q, en, cn := "登录超时", "login timeout", "苹果香蕉"
	if c1, c2 := SemanticCosine(q, en), SemanticCosine(q, cn); c1 <= c2 {
		t.Errorf("跨语言余弦命中应高于无关文本: zh↔en=%.4f, zh↔无关=%.4f", c1, c2)
	}
	if c := SemanticCosine(q, q); c < 0.99 {
		t.Errorf("相同文本余弦应 >0.99, got %.4f", c)
	}
}

func TestSemanticCosine_Empty(t *testing.T) {
	if c := SemanticCosine("", "内容"); c != 0 {
		t.Errorf("空查询应 0, got %v", c)
	}
	if c := SemanticCosine("查询", ""); c != 0 {
		t.Errorf("空文本应 0, got %v", c)
	}
}

func TestSemanticRerank_EnglishCodeBoostByChineseQuery(t *testing.T) {
	// 余弦融合验收：中文查询下，语义相关的英文代码块排名提升，
	// 且关键词强命中不被稀释。
	blocks := []types.ContextBlock{
		{ID: "en-code", Content: "func handleLoginTimeout() error { return retry(err, 3) }", Score: 40},
		{ID: "kw-hit", Content: "登录 timeout 处理函数", Score: 85},
		{ID: "unrel", Content: "apple banana cherry orange", Score: 35},
	}
	before := blocks[1].Score
	semanticRerank("修复登录超时", blocks, 30)

	if blocks[0].Score <= 40 {
		t.Errorf("英文代码块应因余弦命中被提升: %v", blocks[0].Score)
	}
	if blocks[1].Score < before {
		t.Errorf("关键词强命中不应被稀释: before=%v after=%v", before, blocks[1].Score)
	}
	if blocks[2].Score != 35 {
		t.Errorf("无关候选分不应变化: %v", blocks[2].Score)
	}
	// 语义命中的英文代码块应排名高于无关文本（同量级初始分下）
	if blocks[0].Score <= blocks[2].Score {
		t.Errorf("语义相关英文代码应高于无关文本: en=%v unrel=%v", blocks[0].Score, blocks[2].Score)
	}
}
