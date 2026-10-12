package harness

import (
	"container/list"
	"sync"
	"time"

	"github.com/contextloom/contextloom-agent/pkg/types"
)

// searchCache：原包级全局变量（多引擎实例共享 + 无容量上限、只做过期删除没有 LRU）
// 迁移为 Engine 私有成员，加 LRU 容量上限：命中/插入移队头，超容量淘汰队尾最久未用；
// TTL 过期惰性淘汰（保留原"过期删除"语义）；容量/TTL 可由 Config 覆盖。

// searchCacheEntry 缓存条目（key 随条目携带，淘汰时免 map 反查）。
type searchCacheEntry struct {
	key     string
	blocks  []types.ContextBlock
	created time.Time
}

const (
	defaultSearchCacheCap = 256
	defaultSearchCacheTTL = 2 * time.Second
)

// searchCacheLRU Engine 私有 LRU 缓存（container/list + map 索引）。
type searchCacheLRU struct {
	mu      sync.Mutex
	entries map[string]*list.Element // key -> 链表节点
	lru     *list.List               // 队头 = 最近使用
	cap     int
	ttl     time.Duration
}

func newSearchCache(capacity int, ttl time.Duration) *searchCacheLRU {
	if capacity <= 0 {
		capacity = defaultSearchCacheCap
	}
	if ttl <= 0 {
		ttl = defaultSearchCacheTTL
	}
	return &searchCacheLRU{
		entries: make(map[string]*list.Element),
		lru:     list.New(),
		cap:     capacity,
		ttl:     ttl,
	}
}

// get 命中返回缓存切片（调用方 Search 负责拷贝后返回），未命中/过期返回 false。
// 命中即移队头（LRU 语义）；过期条目惰性删除（原"过期删除"语义保留）。
func (c *searchCacheLRU) get(key string) ([]types.ContextBlock, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	entry := el.Value.(*searchCacheEntry)
	if time.Since(entry.created) >= c.ttl {
		c.lru.Remove(el)
		delete(c.entries, key)
		return nil, false
	}
	c.lru.MoveToFront(el)
	return entry.blocks, true
}

// set 插入/更新条目并移队头；超容量 LRU 淘汰队尾。
func (c *searchCacheLRU) set(key string, blocks []types.ContextBlock) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.entries[key]; ok {
		entry := el.Value.(*searchCacheEntry)
		entry.blocks = blocks
		entry.created = time.Now()
		c.lru.MoveToFront(el)
		return
	}
	c.entries[key] = c.lru.PushFront(&searchCacheEntry{key: key, blocks: blocks, created: time.Now()})
	for len(c.entries) > c.cap {
		back := c.lru.Back()
		if back == nil {
			break
		}
		delete(c.entries, back.Value.(*searchCacheEntry).key)
		c.lru.Remove(back)
	}
}

// len 当前条目数（测试用）。
func (c *searchCacheLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
