package harness

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// indexSnapshot 是 HybridIndex 的磁盘序列化格式（版本化，便于将来迁移）。
type indexSnapshot struct {
	Version int                  `json:"version"`
	SavedAt time.Time            `json:"saved_at"`
	Blocks  []types.ContextBlock `json:"blocks"`
}

const indexFormatVersion = 1

type HybridIndex struct {
	mu       sync.RWMutex
	inverted map[string]map[string]struct{}
	blocks   map[string]*types.ContextBlock
	// blockTokens 反向映射 blockID → 该块 token 列表（写入时记录），
	// Remove/覆盖写时 unindexTokens 从 O(全部词表) 降为 O(该块 token 数)。
	blockTokens map[string][]string
	// df token → 含该 token 的块数（与 inverted 镜像维护，供 IDF 加权）。
	df map[string]int
}

func NewHybridIndex() *HybridIndex {
	return &HybridIndex{
		inverted:    make(map[string]map[string]struct{}),
		blocks:      make(map[string]*types.ContextBlock),
		blockTokens: make(map[string][]string),
		df:          make(map[string]int),
	}
}

func (idx *HybridIndex) Add(block *types.ContextBlock) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	// If overwriting an existing block, clean up old tokens first
	if _, exists := idx.blocks[block.ID]; exists {
		idx.unindexTokens(block.ID)
	}
	idx.blocks[block.ID] = block
	idx.indexTokens(block.ID, block.Content)
	// 语义特征缓存 + embedding 向量缓存同步失效（内容已更新）
	invalidateSemantic(block.ID)
	invalidateEmbedding(block.ID)
}

func (idx *HybridIndex) indexTokens(id, content string) {
	tokens := tokenize(content)
	idx.blockTokens[id] = tokens
	for _, token := range tokens {
		if _, ok := idx.inverted[token][id]; !ok {
			// DF 计数仅在"新块-新 token"组合时 +1（同块重复 token 不重复计）
			idx.df[token]++
			if idx.inverted[token] == nil {
				idx.inverted[token] = make(map[string]struct{})
			}
			idx.inverted[token][id] = struct{}{}
		}
	}
}

// unindexTokens 从倒排表中移除指定块的全部 token。
// 优化：原实现遍历全部词表（O(词表总大小)），现按 blockTokens 反向映射
// 只处理该块自己的 token（O(该块 token 数)）——超大索引删除单块不再全扫。
func (idx *HybridIndex) unindexTokens(id string) {
	tokens, ok := idx.blockTokens[id]
	if !ok {
		return
	}
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if _, dup := seen[token]; dup {
			continue // 重复 token 只减一次（与 indexTokens 的 DF 去重对称）
		}
		seen[token] = struct{}{}
		if ids, ok := idx.inverted[token]; ok {
			delete(ids, id)
			if len(ids) == 0 {
				delete(idx.inverted, token)
			}
		}
		if n := idx.df[token]; n <= 1 {
			delete(idx.df, token)
		} else {
			idx.df[token] = n - 1
		}
	}
	delete(idx.blockTokens, id)
}

func (idx *HybridIndex) Query(keyword string, metadata map[string]string) []*types.ContextBlock {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	queryTokens := tokenize(keyword)
	if len(queryTokens) == 0 {
		results := make([]*types.ContextBlock, 0, len(idx.blocks))
		for _, block := range idx.blocks {
			if matchesMetadata(block, metadata) {
				results = append(results, block)
			}
		}
		return results
	}

	var candidateIDs map[string]int
	for _, token := range queryTokens {
		blockIDs, ok := idx.inverted[token]
		if !ok {
			continue
		}
		if candidateIDs == nil {
			candidateIDs = make(map[string]int, len(blockIDs))
			for id := range blockIDs {
				candidateIDs[id] = 1
			}
			continue
		}
		for id := range candidateIDs {
			if _, exists := blockIDs[id]; exists {
				candidateIDs[id]++
			}
		}
	}

	if candidateIDs == nil {
		// AND 交集为空 → 回退 OR 并集：任一 token 命中的块都进候选。
		// 原实现直接返回 nil 导致多词英文查询零召回（部分命中也被杀）；
		// 召回层放宽到 OR，精确度由上层 scorer 的 overlap 打分收敛。
		for _, token := range queryTokens {
			for id := range idx.inverted[token] {
				if candidateIDs == nil {
					candidateIDs = make(map[string]int, 8)
				}
				candidateIDs[id] = 1
			}
		}
		if candidateIDs == nil {
			return nil
		}
	}

	// IDF 加权（BM25 思想的轻量近似）：稀有 token 区分度高、常见 token
	// （the/err 等停用级词）区分度低。idf = ln(1 + N/df)，N=总块数。
	// 返回按 (命中数×IDF 权重和) 降序的候选，供上层评分参考。
	totalBlocks := len(idx.blocks)
	type scored struct {
		id     string
		weight float64
	}
	scoredIDs := make([]scored, 0, len(candidateIDs))
	for id := range candidateIDs {
		w := 0.0
		for _, token := range tokenize(keyword) {
			if _, hit := idx.inverted[token]; !hit {
				continue
			}
			if _, inCand := candidateIDs[id]; !inCand {
				continue
			}
			// 该 token 是否命中此块
			if _, hit := idx.inverted[token][id]; hit {
				dfv := idx.df[token]
				if dfv <= 0 {
					dfv = 1
				}
				w += math.Log(1 + float64(totalBlocks)/float64(dfv))
			}
		}
		scoredIDs = append(scoredIDs, scored{id: id, weight: w})
	}
	sort.Slice(scoredIDs, func(i, j int) bool { return scoredIDs[i].weight > scoredIDs[j].weight })

	results := make([]*types.ContextBlock, 0, len(scoredIDs))
	for _, sc := range scoredIDs {
		if block, ok := idx.blocks[sc.id]; ok && matchesMetadata(block, metadata) {
			results = append(results, block)
		}
	}
	return results
}

func (idx *HybridIndex) Remove(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.unindexTokens(id)
	delete(idx.blocks, id)
	invalidateSemantic(id)
	invalidateEmbedding(id)
}

func (idx *HybridIndex) MarkDirty(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	if block, ok := idx.blocks[id]; ok {
		block.Dirty = true
	}
}

func (idx *HybridIndex) Get(id string) (*types.ContextBlock, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	block, ok := idx.blocks[id]
	return block, ok
}

func (idx *HybridIndex) ApplyDecay(factor float64) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	for _, block := range idx.blocks {
		block.Score = types.Score(float64(block.Score) * factor)
	}
}

func matchesMetadata(block *types.ContextBlock, metadata map[string]string) bool {
	if len(metadata) == 0 {
		return true
	}
	for k, v := range metadata {
		switch k {
		case "id":
			if block.ID != v {
				return false
			}
		case "content_contains":
			if !strings.Contains(block.Content, v) {
				return false
			}
		case "session_id":
			// 会话隔离：项目级共享块（SessionID=""）对所有会话可见；
			// 会话私有块仅对所属会话可见（空过滤值=不限制，兼容旧调用方）。
			if v != "" && block.SessionID != "" && block.SessionID != v {
				return false
			}
		}
	}
	return true
}

// Save 将索引原子持久化到 path（临时文件 + rename，崩溃不损坏旧索引）。
// blocks 按 ID 排序输出保证确定性（便于 diff/去重）。保存成功后调用方
// （Engine.SaveIndex）同步 savedSeq，使 NeedsSave 归位。
func (idx *HybridIndex) Save(path string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	blocks := make([]types.ContextBlock, 0, len(idx.blocks))
	for _, b := range idx.blocks {
		blocks = append(blocks, *b)
	}
	sort.Slice(blocks, func(i, j int) bool { return blocks[i].ID < blocks[j].ID })
	snap := indexSnapshot{
		Version: indexFormatVersion,
		SavedAt: time.Now(),
		Blocks:  blocks,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化索引: %w", err)
	}
	if err := atomicWriteFile(path, data); err != nil {
		return fmt.Errorf("写入索引文件 %s: %w", path, err)
	}
	return nil
}

// Load 从 path 加载索引并重建倒排表（inverted）。
// 文件不存在时返回 nil（首次运行视为空索引）；文件损坏返回错误（由调用方决定降级策略）。
// 语义/embedding 缓存是包级内存缓存，按 ID 失效即可（内容可能已被新数据覆盖）。
func (idx *HybridIndex) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var snap indexSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return fmt.Errorf("解析索引文件 %s: %w", path, err)
	}
	if snap.Version != indexFormatVersion {
		return fmt.Errorf("索引文件 %s 版本不兼容: 文件 v%d, 期望 v%d", path, snap.Version, indexFormatVersion)
	}

	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.blocks = make(map[string]*types.ContextBlock, len(snap.Blocks))
	idx.inverted = make(map[string]map[string]struct{})
	idx.blockTokens = make(map[string][]string, len(snap.Blocks))
	idx.df = make(map[string]int)
	for i := range snap.Blocks {
		b := snap.Blocks[i]
		idx.blocks[b.ID] = &b
		idx.indexTokens(b.ID, b.Content)
		// 缓存一致性：同名 ID 的旧语义/embedding 缓存全部失效
		invalidateSemantic(b.ID)
		invalidateEmbedding(b.ID)
	}
	return nil
}

// atomicWriteFile 原子写：先写同目录临时文件并 fsync，再 rename 覆盖目标。
// 进程崩溃/断电时旧索引保持完整，杜绝"写一半损坏"。
func atomicWriteFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".index-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // 失败路径清理；成功后 rename 已移走，Remove 静默失败
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
