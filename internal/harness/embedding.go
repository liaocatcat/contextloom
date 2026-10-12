package harness

// ─────────────────────────────────────────────────────────────
// 稠密向量 embedding 层（对标前沿语义索引的零依赖实现）
//
// 原理：特征哈希（feature hashing / hashing trick）——
//  1. 从文本提取确定性特征：
//      - 拉丁词：整词（小写）+ 相邻词 bigram（"login timeout" → login, timeout, login_timeout）；
//      - CJK：连续 CJK 字符 unigram + bigram（"登录超时" → 登,录,超,时,登录,录超,超时）；
//      - 数字/符号：原样保留作为特征；
//      - 中英同义词双向扩展（复用 semantic.go 的 zhEnSynonyms/enZhSynonyms），
//        使中文查询与英文代码块在稠密空间产生重叠（跨语言语义命中）。
//  2. 每个特征经 FNV-1a 哈希映射到 dim 维向量中的一个 bin，
//     符号位 (h>>63)&1 决定 ±1 累加（hashing trick）；
//  3. L2 归一化（零向量保持零）。
//
// 特性：零依赖（纯 stdlib）、确定性（相同输入恒同输出）、
// 全平台（无 CGO/无模型文件）、O(特征数) 计算。
//
// 与 semantic.go 的覆盖率度量互补：
//   - queryCoverage：精确特征交集比例（稀疏、可解释）；
//   - Cosine：稠密向量相似度（对特征分布/长度更鲁棒）。
// 二者在 semanticRerank 中以 0.5/0.5 权重融合参与最终评分。
//
// 向量缓存：embedCache（blockID → []float64），块内容更新时由
// invalidateEmbedding 失效（与 invalidateSemantic 在 Index 写路径同步调用）。
// ─────────────────────────────────────────────────────────────

import (
	"hash/fnv"
	"math"
	"strings"
	"sync"
	"unicode"
)

// Embedder 稠密向量嵌入器（特征哈希，零依赖）。
type Embedder struct {
	dim int
}

// NewEmbedder 创建嵌入器；dim <= 0 时默认 256。
func NewEmbedder(dim int) *Embedder {
	if dim <= 0 {
		dim = 256
	}
	return &Embedder{dim: dim}
}

// Dim 返回向量维度。
func (e *Embedder) Dim() int {
	return e.dim
}

// embedFeatures 提取 embedding 特征（确定性、大小写不敏感）：
//   - 拉丁词（字母数字下划线连续序列）：小写整词 + 相邻词 bigram；
//   - CJK（中日韩）：unigram + bigram；
//   - 数字序列：作为整词特征（与字母混合则并入词）；
//   - 符号（非空白非字母数字非 CJK）：原样保留；
//   - 同义词扩展：中文 bigram ↔ 英文词 双向附加（跨语言命中）。
func embedFeatures(text string) []string {
	var feats []string
	var word []rune // 拉丁词缓冲（字母/数字/下划线）
	var cjk []rune  // CJK 缓冲
	var prevWord string
	seen := make(map[string]bool)

	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			feats = append(feats, f)
		}
	}

	// flushWord 输出当前拉丁词 + 与上一词的 bigram（若相邻）。
	// 驼峰/下划线边界拆分（"handleLoginTimeout" → handle, login, timeout；
	// "login_timeout" → login, timeout），使代码函数名与空格分词产生
	// 一致特征；空白不打断词对（"login timeout" → login_timeout）。
	// CJK 段/符号显式清 prevWord 打断词对。
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		// 先按边界拆分（保留原始大小写以便识别驼峰边界）
		var subs []string
		var cur []rune
		flushSub := func() {
			if len(cur) == 0 {
				return
			}
			subs = append(subs, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
		for i, r := range word {
			if r == '_' {
				flushSub()
				continue
			}
			if i > 0 && unicode.IsUpper(r) && !unicode.IsUpper(word[i-1]) {
				flushSub() // 大写字母前拆分（驼峰边界）
			}
			cur = append(cur, r)
		}
		flushSub()
		for _, w := range subs {
			add(w)
			if prevWord != "" {
				add(prevWord + "_" + w)
			}
			prevWord = w
			// 英文词 → 中文 bigram 同义词扩展
			if zhs, ok := enZhSynonyms[w]; ok {
				for _, zh := range zhs {
					add(zh)
				}
			}
		}
		word = word[:0]
	}

	flushCJK := func() {
		if len(cjk) == 0 {
			return
		}
		for i := 0; i < len(cjk); i++ {
			add(string(cjk[i])) // unigram
			if i+1 < len(cjk) {
				bg := string(cjk[i : i+2]) // bigram
				add(bg)
				// 中文 bigram → 英文同义词扩展
				if ens, ok := zhEnSynonyms[bg]; ok {
					for _, en := range ens {
						add(en)
					}
				}
			}
		}
		cjk = cjk[:0]
		prevWord = "" // CJK 段打断英文词对
	}

	for _, r := range text {
		switch {
		case isCJK(r):
			flushWord()
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_':
			flushCJK()
			word = append(word, r) // 保留原始大小写（驼峰拆分需要）
		case unicode.IsSpace(r):
			// 空白：flush 缓冲区，保留英文词对
			flushWord()
			flushCJK()
		default:
			// 符号：flush 缓冲区 + 打断词对 + 原样保留
			flushWord()
			flushCJK()
			prevWord = ""
			add(string(r))
		}
	}
	flushWord()
	flushCJK()
	return feats
}

// Embed 将文本嵌入为 dim 维 L2 归一化向量（确定性；空文本/空特征返回零向量）。
func (e *Embedder) Embed(text string) []float64 {
	vec := make([]float64, e.dim)
	if text == "" {
		return vec
	}
	for _, f := range embedFeatures(text) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(f))
		hv := h.Sum64()
		idx := int(hv % uint64(e.dim))
		if hv&(1<<63) != 0 { // 符号位决定 ±1
			vec[idx]--
		} else {
			vec[idx]++
		}
	}
	// L2 归一化（零向量保持零）
	norm := 0.0
	for _, v := range vec {
		norm += v * v
	}
	if norm > 0 {
		inv := 1.0 / math.Sqrt(norm)
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec
}

// Cosine 计算两个向量的余弦相似度（-1~1）。
// 维度不一致或任一为零向量时返回 0。
func Cosine(a, b []float64) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	dot, na, nb := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		na += a[i] * a[i]
		nb += b[i] * b[i]
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// ── 共享 embedder 与块向量缓存 ──

// sharedEmbedder 包级共享嵌入器（惰性初始化，默认 256 维）。
var (
	embedderOnce   sync.Once
	sharedEmbedder *Embedder
)

func getEmbedder() *Embedder {
	embedderOnce.Do(func() {
		sharedEmbedder = NewEmbedder(0)
	})
	return sharedEmbedder
}

// embedCache 块向量缓存（blockID → []float64）。
var embedCache sync.Map // map[string][]float64

// invalidateEmbedding 块内容变更时失效向量缓存（由 Index 写路径调用，
// 与 invalidateSemantic 同步失效，保证两套缓存一致）。
func invalidateEmbedding(blockID string) {
	embedCache.Delete(blockID)
}

// blockVector 取块向量（惰性缓存，并发安全）。
func blockVector(id, content string) []float64 {
	if v, ok := embedCache.Load(id); ok {
		return v.([]float64)
	}
	v := getEmbedder().Embed(content)
	embedCache.Store(id, v)
	return v
}
