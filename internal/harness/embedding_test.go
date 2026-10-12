package harness

import (
	"math"
	"testing"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// ── 基础性质测试 ──

func TestNewEmbedder_DefaultDim(t *testing.T) {
	if d := NewEmbedder(0).Dim(); d != 256 {
		t.Errorf("默认维度应为 256, got %d", d)
	}
	if d := NewEmbedder(-5).Dim(); d != 256 {
		t.Errorf("负维度应回退默认 256, got %d", d)
	}
}

func TestEmbedder_Dims(t *testing.T) {
	for _, dim := range []int{128, 256, 512} {
		e := NewEmbedder(dim)
		v := e.Embed("登录超时 login timeout")
		if len(v) != dim {
			t.Errorf("维度 %d: 向量长度 %d 不匹配", dim, len(v))
		}
	}
}

func TestEmbed_EmptyText_ZeroVector(t *testing.T) {
	e := NewEmbedder(64)
	v := e.Embed("")
	for i, x := range v {
		if x != 0 {
			t.Fatalf("空文本应产生零向量, idx %d = %v", i, x)
		}
	}
}

func TestEmbed_Deterministic(t *testing.T) {
	e := NewEmbedder(256)
	a := e.Embed("修复登录超时 handle login timeout")
	b := e.Embed("修复登录超时 handle login timeout")
	if len(a) != len(b) {
		t.Fatal("两次嵌入长度不一致")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("相同输入应逐元素相等, idx %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestEmbed_L2Normalized(t *testing.T) {
	e := NewEmbedder(256)
	for _, text := range []string{"登录超时", "func handleLoginTimeout() error", "修复登录超时问题"} {
		v := e.Embed(text)
		norm := 0.0
		for _, x := range v {
			norm += x * x
		}
		norm = math.Sqrt(norm)
		if math.Abs(norm-1) > 1e-9 {
			t.Errorf("非空文本应 L2 归一化(范数≈1), got %v (text=%q)", norm, text)
		}
	}
}

// ── 余弦相似度测试 ──

func TestCosine_SameText(t *testing.T) {
	e := NewEmbedder(256)
	v := e.Embed("登录功能实现")
	if c := Cosine(v, v); c < 0.99 {
		t.Errorf("相同文本余弦应 >0.99, got %.4f", c)
	}
}

func TestCosine_Unrelated(t *testing.T) {
	e := NewEmbedder(256)
	a := e.Embed("登录超时问题排查")
	b := e.Embed("苹果香蕉橙子水果")
	if c := Cosine(a, b); c >= 0.5 {
		t.Errorf("无关文本余弦应 <0.5, got %.4f", c)
	}
}

func TestCosine_DimMismatchOrZero(t *testing.T) {
	if c := Cosine([]float64{1, 0}, []float64{1, 0, 0}); c != 0 {
		t.Errorf("维度不一致应返回 0, got %v", c)
	}
	if c := Cosine([]float64{0, 0}, []float64{1, 1}); c != 0 {
		t.Errorf("零向量应返回 0, got %v", c)
	}
	if c := Cosine(nil, nil); c != 0 {
		t.Errorf("空切片应返回 0, got %v", c)
	}
}

// ── 跨语言语义命中（核心验收项）──

func TestCosine_CrossLanguage(t *testing.T) {
	e := NewEmbedder(256)
	// "登录超时"（中文）与 "login timeout"（英文）应比与无关中文更高相似
	zh := e.Embed("登录超时")
	en := e.Embed("login timeout")
	unrelated := e.Embed("苹果香蕉")
	if c1, c2 := Cosine(zh, en), Cosine(zh, unrelated); c1 <= c2 {
		t.Errorf("跨语言命中应高于无关文本: zh↔en=%.4f, zh↔无关=%.4f", c1, c2)
	}
}

func TestCosine_CaseInsensitive(t *testing.T) {
	e := NewEmbedder(256)
	// 大小写不敏感：相同分词的纯大小写差异应产生相同特征
	a := e.Embed("LOGIN TIMEOUT")
	b := e.Embed("login timeout")
	if c := Cosine(a, b); c < 0.99 {
		t.Errorf("纯大小写差异应产生≈1 相似, got %.4f", c)
	}
	// 驼峰函数名与空格分词（同义）特征一致（代码检索核心场景）
	c := e.Embed("handleLoginTimeout")
	d := e.Embed("login timeout")
	if cos := Cosine(c, d); cos < 0.5 {
		t.Errorf("驼峰函数名与空格分词应高相似, got %.4f", cos)
	}
}

// ── 特征提取单元测试 ──

func TestEmbedFeatures_LatinBigram(t *testing.T) {
	feats := embedFeatures("login timeout")
	set := map[string]bool{}
	for _, f := range feats {
		set[f] = true
	}
	for _, want := range []string{"login", "timeout", "login_timeout"} {
		if !set[want] {
			t.Errorf("拉丁特征应含 %q: %v", want, feats)
		}
	}
}

func TestEmbedFeatures_CJKUnigramBigram(t *testing.T) {
	feats := embedFeatures("登录超时")
	set := map[string]bool{}
	for _, f := range feats {
		set[f] = true
	}
	for _, want := range []string{"登", "录", "超", "时", "登录", "录超", "超时"} {
		if !set[want] {
			t.Errorf("CJK 特征应含 %q: %v", want, feats)
		}
	}
}

func TestEmbedFeatures_NumAndSym(t *testing.T) {
	feats := embedFeatures("v2.0 超时")
	set := map[string]bool{}
	for _, f := range feats {
		set[f] = true
	}
	for _, want := range []string{"v2", "0", ".", "超时"} {
		if !set[want] {
			t.Errorf("数字/符号应保留为特征 %q: %v", want, feats)
		}
	}
}

func TestEmbedFeatures_DeterministicOrder(t *testing.T) {
	a := embedFeatures("修复登录超时 handle login timeout")
	b := embedFeatures("修复登录超时 handle login timeout")
	if len(a) != len(b) {
		t.Fatal("特征数量应一致")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("特征顺序应确定, idx %d: %q vs %q", i, a[i], b[i])
		}
	}
}

// ── 缓存失效测试 ──

func TestEmbedCache_Invalidate(t *testing.T) {
	id := "embed-cache-test"
	blockVector(id, "登录 timeout 处理")
	if _, ok := embedCache.Load(id); !ok {
		t.Fatal("向量缓存应存在")
	}
	invalidateEmbedding(id)
	if _, ok := embedCache.Load(id); ok {
		t.Fatal("失效后向量缓存应删除")
	}
}

func TestEmbedCache_IndexUpdateRecomputes(t *testing.T) {
	// 索引更新路径：Add 相同 ID 不同内容 → invalidateEmbedding 被调用
	// → blockVector 应返回新内容对应的向量（旧向量已被失效）。
	idx := NewHybridIndex()
	id := "idx-embed-update"
	idx.Add(&types.ContextBlock{ID: id, Content: "登录超时"})
	v1 := blockVector(id, "登录超时")

	// 更新块内容（走 Add 覆盖路径 → 缓存失效）
	idx.Add(&types.ContextBlock{ID: id, Content: "苹果香蕉"})
	if _, ok := embedCache.Load(id); ok {
		t.Fatal("索引更新后向量缓存应被失效")
	}
	v2 := blockVector(id, "苹果香蕉")
	// 新向量应接近"苹果香蕉"自身、远离"登录超时"旧内容
	e := NewEmbedder(256)
	if c := Cosine(v2, e.Embed("苹果香蕉")); c < 0.99 {
		t.Errorf("更新后向量应匹配新内容: %.4f", c)
	}
	if c := Cosine(v2, v1); c > 0.5 {
		t.Errorf("更新后向量应远离旧内容: %.4f", c)
	}
}
