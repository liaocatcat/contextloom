package harness

import (
	"context"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

type Engine struct {
	mu     sync.RWMutex
	index  *HybridIndex
	scorer *RelevanceScorer
	window *SlidingWindow
	syms   *SymbolIndex // 符号索引（对标前沿 repo map 的零依赖轻量实现）
	cfg    Config

	// 持久化记账：changeSeq 在 Index/Invalidate/Rerank 时递增，
	// SaveIndex 成功后 savedSeq 对齐——NeedsSave 据此判断是否有未落盘变更。
	changeSeq uint64
	savedSeq  uint64

	// cache 检索结果 LRU 缓存（Engine 私有；原包级全局变量迁移至此，
	// 多引擎实例不再共享、有容量上限，杜绝长期运行内存增长）。
	cache *searchCacheLRU
}

// symbolScoreBoost 符号命中权重加成（加在原始相关度得分之上）。
const symbolScoreBoost = 30

type Config struct {
	ScoreThreshold float64
	DecayFactor    float64
	AnchorRatio    float64

	// SearchCacheCap 检索缓存容量上限（LRU 淘汰），<=0 用缺省 256。
	SearchCacheCap int
	// SearchCacheTTL 缓存存活时长，<=0 用缺省 2s。
	SearchCacheTTL time.Duration
	// SemanticRerankTopN 语义重排候选宽度（原硬编码 30），<=0 用缺省。
	SemanticRerankTopN int
	// OverflowCompressBlocks 触发窗口压缩的块数阈值（原硬编码 50）。
	OverflowCompressBlocks int
	// OverflowCompressScore 压缩判定的得分阈值（原硬编码 60）。
	OverflowCompressScore float64
	// OverflowCompressKeep 压缩保留前缀长度（原硬编码 100）。
	OverflowCompressKeep int
}

// 缺省检索行为常量（原散落 Search 内的魔法数收编；Config 同名字段可覆盖）。
const (
	defaultSemanticRerankTopN     = 30  // 语义重排候选宽度
	defaultOverflowCompressBlocks = 50  // 触发窗口压缩的块数阈值
	defaultOverflowCompressScore  = 60  // 压缩判定的得分阈值
	defaultOverflowCompressKeep   = 100 // 压缩保留前缀长度
)

func New(cfg Config) *Engine {
	scorer := NewRelevanceScorer()
	scorer.SetThreshold(types.Score(cfg.ScoreThreshold))
	return &Engine{
		index:  NewHybridIndex(),
		scorer: scorer,
		window: NewSlidingWindow(cfg.AnchorRatio),
		syms:   NewSymbolIndex(),
		cfg:    cfg,
		cache:  newSearchCache(cfg.SearchCacheCap, cfg.SearchCacheTTL),
	}
}

func (e *Engine) ApplyDecay() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.index.ApplyDecay(e.cfg.DecayFactor)
}

func (e *Engine) Index(ctx context.Context, blocks []types.ContextBlock) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range blocks {
		e.index.Add(&blocks[i])
		e.syms.Add(blocks[i].Content, blocks[i].ID)
	}
	e.changeSeq++
	// 索引内容变更，清空检索缓存（防止旧结果在 TTL 内被复用）
	e.cache = newSearchCache(e.cfg.SearchCacheCap, e.cfg.SearchCacheTTL)
}

func (e *Engine) Search(ctx context.Context, query string, metadata map[string]string) []types.ContextBlock {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 缓存检查（TTL 过期则丢弃，LRU 超容量淘汰队尾）。缓存键 = query + 会话过滤，
	// 防止同 query 不同会话命中他会话的缓存结果（会话串扰）。
	cacheKey := query
	if sid, ok := metadata["session_id"]; ok && sid != "" {
		cacheKey = "sid:" + sid + "|" + query
	}
	if cached, ok := e.cache.get(cacheKey); ok {
		// 返回深拷贝，避免调用方修改污染缓存
		cp := make([]types.ContextBlock, len(cached))
		copy(cp, cached)
		return cp
	}

	candidates := e.index.Query(query, metadata)

	// 符号命中加权：查询命中函数/类型/常量名时，对应 block 加分（对标 repo map 的符号感知）
	symbolHits := e.syms.Lookup(query)

	// Copy candidates into value blocks to avoid mutating index data
	blocks := make([]types.ContextBlock, len(candidates))
	for i, c := range candidates {
		blocks[i] = *c
		blocks[i].Score = e.scorer.Score(query, &blocks[i])
		if symbolHits != nil {
			if _, ok := symbolHits[blocks[i].ID]; ok {
				blocks[i].Score += symbolScoreBoost
			}
		}
	}

	// 语义重排：对 top N 候选计算 n-gram 语义相似度并混合加分
	// （弥补关键词倒排对模糊/跨语言查询的硬上限；关键词强命中不被稀释）
	topN := e.cfg.SemanticRerankTopN
	if topN <= 0 {
		topN = defaultSemanticRerankTopN
	}
	semanticRerank(query, blocks, topN)

	// 窗口溢出压缩（需求 §13.2）：超过阈值块数时压缩低分块（阈值可配）
	compressAt := e.cfg.OverflowCompressBlocks
	if compressAt <= 0 {
		compressAt = defaultOverflowCompressBlocks
	}
	if len(blocks) > compressAt {
		scoreFloor := e.cfg.OverflowCompressScore
		if scoreFloor <= 0 {
			scoreFloor = defaultOverflowCompressScore
		}
		keep := e.cfg.OverflowCompressKeep
		if keep <= 0 {
			keep = defaultOverflowCompressKeep
		}
		for i := range blocks {
			if float64(blocks[i].Score) < scoreFloor {
				r := []rune(blocks[i].Content)
				if len(r) > keep {
					blocks[i].Content = string(r[:keep]) + "...(压缩)"
				}
			}
		}
	}

	// Use a local window to avoid races on the shared window
	win := NewSlidingWindow(e.cfg.AnchorRatio)
	win.arrangeBlocks(blocks)
	slots := win.Slots()
	// 写缓存（LRU + TTL，容量可配）——保存窗口最终形态
	e.cache.set(cacheKey, slots)
	return slots
}

func (e *Engine) Rerank(ctx context.Context, blockID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.index.MarkDirty(blockID)
	e.changeSeq++
}

func (e *Engine) Invalidate(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.index.Remove(id)
	e.changeSeq++
	e.cache = newSearchCache(e.cfg.SearchCacheCap, e.cfg.SearchCacheTTL)
}

func (e *Engine) SetThreshold(t types.Score) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg.ScoreThreshold = float64(t)
	e.scorer.SetThreshold(t)
}

// ApplyConfig 热更新检索参数（GUI 保存 harness.* 后立即生效，供
// Runtime.applyLiveConfig 调用）。与 Search/BackgroundCalibrate 的
// e.cfg 读取互斥（mu 保护）；Search 内窗口为每次查询本地新建，
// AnchorRatio 更新无需重建共享状态。
func (e *Engine) ApplyConfig(cfg Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cfg = cfg
	e.scorer.SetThreshold(types.Score(cfg.ScoreThreshold))
	// 缓存容量/TTL 变更时重建（简单起见不清旧数据语义：直接换新实例）
	if cfg.SearchCacheCap > 0 || cfg.SearchCacheTTL > 0 {
		e.cache = newSearchCache(cfg.SearchCacheCap, cfg.SearchCacheTTL)
	}
}

// Threshold 返回当前检索评分阈值（供配置热更新测试断言）。
func (e *Engine) Threshold() types.Score {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.scorer.Threshold()
}

// BackgroundCalibrate 后台全量校准（§4.3）
// 系统空闲时对全量上下文进行重评分，保证长对话中的分数一致性
func (e *Engine) BackgroundCalibrate() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.index.ApplyDecay(e.cfg.DecayFactor)
}

// SaveIndex 将引擎索引持久化到 path（原子写）。保存成功后 savedSeq 对齐，
// NeedsSave 归位。符号/倒排索引可从 blocks 重建，无需单独序列化。
func (e *Engine) SaveIndex(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.index.Save(path); err != nil {
		return err
	}
	e.savedSeq = e.changeSeq
	return nil
}

// LoadIndex 从 path 加载索引并重建倒排表与符号索引。
// 文件不存在时静默返回 nil（首次运行）；损坏/版本不兼容返回错误，
// 调用方可降级为空索引继续运行（检索能力缺失但主流程不受影响）。
func (e *Engine) LoadIndex(path string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.index.Load(path); err != nil {
		return err
	}
	e.syms = NewSymbolIndex()
	for _, b := range e.index.blocks {
		e.syms.Add(b.Content, b.ID)
	}
	e.changeSeq = 0
	e.savedSeq = 0
	return nil
}

// NeedsSave 报告索引是否有未落盘的变更（自动保存轮询用，避免每次 Index 都写盘）。
func (e *Engine) NeedsSave() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.changeSeq != e.savedSeq
}
