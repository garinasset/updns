package cache

import (
	"container/heap"
	"container/list"
	"math"
	"sync"
	"time"

	"github.com/miekg/dns"
)

type CacheEntry struct {
	Query      string
	QueryType  uint16
	Answers    []dns.RR
	Authority  []dns.RR
	Upstream   string
	Rcode      int
	TTL        uint32
	ExpireTime time.Time
	CreatedAt  time.Time
	HitCount   int
	version    uint64
}

type node struct {
	key     string
	entry   CacheEntry
	index   [4]int
	touched uint64
}

// Independent indices track expiration, eviction, refresh due, and ready tasks.
type entryHeap struct {
	nodes []*node
	kind  int
}

func (h entryHeap) Len() int { return len(h.nodes) }
func (h entryHeap) Less(i, j int) bool {
	a, b := h.nodes[i], h.nodes[j]
	switch h.kind {
	case 0, 3:
		if !a.entry.ExpireTime.Equal(b.entry.ExpireTime) {
			return a.entry.ExpireTime.Before(b.entry.ExpireTime)
		}
	case 1:
		if a.entry.HitCount != b.entry.HitCount {
			return a.entry.HitCount < b.entry.HitCount
		}
		if a.touched != b.touched {
			return a.touched < b.touched
		}
	case 2:
		aDue, bDue := a.entry.preloadAt(), b.entry.preloadAt()
		if !aDue.Equal(bDue) {
			return aDue.Before(bDue)
		}
		if !a.entry.ExpireTime.Equal(b.entry.ExpireTime) {
			return a.entry.ExpireTime.Before(b.entry.ExpireTime)
		}
	}
	return a.key < b.key
}
func (h entryHeap) Swap(i, j int) {
	h.nodes[i], h.nodes[j] = h.nodes[j], h.nodes[i]
	h.nodes[i].index[h.kind], h.nodes[j].index[h.kind] = i, j
}
func (h *entryHeap) Push(value interface{}) {
	n := value.(*node)
	n.index[h.kind] = len(h.nodes)
	h.nodes = append(h.nodes, n)
}
func (h *entryHeap) Pop() interface{} {
	i := len(h.nodes) - 1
	n := h.nodes[i]
	h.nodes[i] = nil
	h.nodes = h.nodes[:i]
	n.index[h.kind] = -1
	return n
}

type historyEntry struct {
	key  string
	hits int
}

// Cache retains expired entries for refresh, never for serving stale DNS data.
type Cache struct {
	mu       sync.Mutex
	entries  map[string]*node
	maxSize  int
	expiry   entryHeap
	eviction entryHeap
	due      entryHeap
	ready    entryHeap
	history  map[string]*list.Element
	order    list.List
	sequence uint64
	epoch    uint64
}

func NewCache(maxSize int) *Cache {
	if maxSize < 0 {
		maxSize = 0
	}
	return &Cache{
		entries: make(map[string]*node), maxSize: maxSize,
		expiry: entryHeap{kind: 0}, eviction: entryHeap{kind: 1},
		due: entryHeap{kind: 2}, ready: entryHeap{kind: 3},
		history: make(map[string]*list.Element),
	}
}

func (c *Cache) Set(key string, entry *CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, entry)
}

func (c *Cache) setLocked(key string, entry *CacheEntry) {
	if entry == nil || entry.TTL == 0 {
		c.removeLocked(key)
		return
	}
	if c.maxSize == 0 {
		return
	}
	copyEntry := *entry
	copyEntry.Answers, copyEntry.Authority = cloneRecords(entry.Answers), cloneRecords(entry.Authority)
	if copyEntry.CreatedAt.IsZero() {
		copyEntry.CreatedAt = time.Now()
	}
	touched := uint64(0)
	if previous := c.entries[key]; previous != nil {
		copyEntry.HitCount = previous.entry.HitCount
		touched = previous.touched
		c.detachLocked(previous)
	} else {
		if old := c.history[key]; old != nil {
			copyEntry.HitCount = old.Value.(historyEntry).hits
			c.order.Remove(old)
			delete(c.history, key)
		}
		if len(c.entries) >= c.maxSize {
			now := time.Now()
			for c.expiry.Len() > 0 && !c.expiry.nodes[0].entry.ExpireTime.After(now) {
				c.removeLocked(c.expiry.nodes[0].key)
			}
			if len(c.entries) >= c.maxSize {
				c.removeLocked(c.eviction.nodes[0].key)
			}
		}
	}
	c.sequence++
	copyEntry.version = c.sequence
	if touched == 0 {
		touched = c.sequence
	}
	n := &node{key: key, entry: copyEntry, index: [4]int{-1, -1, -1, -1}, touched: touched}
	c.entries[key] = n
	heap.Push(&c.expiry, n)
	heap.Push(&c.eviction, n)
	heap.Push(&c.due, n)
}

func (c *Cache) detachLocked(n *node) {
	heap.Remove(&c.expiry, n.index[0])
	heap.Remove(&c.eviction, n.index[1])
	// A scheduled entry is absent from both due queues.
	if n.index[2] >= 0 {
		heap.Remove(&c.due, n.index[2])
	}
	if n.index[3] >= 0 {
		heap.Remove(&c.ready, n.index[3])
	}
	delete(c.entries, n.key)
}

func (c *Cache) removeLocked(key string) {
	n := c.entries[key]
	if n == nil {
		return
	}
	c.detachLocked(n)
	if old := c.history[key]; old != nil {
		c.order.Remove(old)
	}
	c.history[key] = c.order.PushBack(historyEntry{key, n.entry.HitCount})
	for c.order.Len() > c.maxSize {
		first := c.order.Front()
		delete(c.history, first.Value.(historyEntry).key)
		c.order.Remove(first)
	}
}

// Snapshot supplies a version token without counting a cache hit.
func (c *Cache) Snapshot(key string) (*CacheEntry, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.entries[key]; n != nil {
		entry := n.entry
		entry.Answers, entry.Authority = nil, nil
		return &entry, c.epoch
	}
	return nil, c.epoch
}

// Suspend removes an entry from scheduling while a shared query owns it.
func (c *Cache) Suspend(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if n := c.entries[key]; n != nil {
		if n.index[2] >= 0 {
			heap.Remove(&c.due, n.index[2])
		}
		if n.index[3] >= 0 {
			heap.Remove(&c.ready, n.index[3])
		}
	}
}

// TakePreloads promotes newly due entries, then selects the earliest expiry.
// Selected entries stay out of scheduling until refreshed or deleted.
func (c *Cache) TakePreloads(limit int) ([]*CacheEntry, uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for c.due.Len() > 0 && !now.Before(c.due.nodes[0].entry.preloadAt()) {
		heap.Push(&c.ready, heap.Pop(&c.due))
	}
	var selected []*CacheEntry
	for len(selected) < limit && c.ready.Len() > 0 {
		n := heap.Pop(&c.ready).(*node)
		entry := n.entry
		entry.Answers, entry.Authority = nil, nil
		selected = append(selected, &entry)
	}
	return selected, c.epoch
}

// Commit prevents old work from overwriting or resurrecting deleted entries.
func (c *Cache) Commit(key string, previous *CacheEntry, epoch uint64, replacement *CacheEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch != c.epoch {
		return false
	}
	current := c.entries[key]
	if previous == nil {
		if current != nil {
			return false
		}
	} else if current == nil || current.entry.version != previous.version {
		return false
	}
	c.setLocked(key, replacement)
	return true
}

func (c *Cache) Get(key string) (*CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.entries[key]
	now := time.Now()
	if n == nil || !now.Before(n.entry.ExpireTime) {
		return nil, false
	}
	if n.entry.HitCount < math.MaxInt {
		n.entry.HitCount++
	}
	c.sequence++
	n.touched = c.sequence
	heap.Fix(&c.eviction, n.index[1])
	result := n.entry
	result.Answers, result.Authority = cloneRecords(n.entry.Answers), cloneRecords(n.entry.Authority)
	elapsed := uint32(0)
	if now.After(result.CreatedAt) {
		elapsed = uint32(now.Sub(result.CreatedAt) / time.Second)
	}
	for _, records := range [][]dns.RR{result.Answers, result.Authority} {
		for _, rr := range records {
			if rr.Header().Ttl > elapsed {
				rr.Header().Ttl -= elapsed
			} else {
				rr.Header().Ttl = 0
			}
		}
	}
	return &result, true
}

func (c *Cache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeLocked(key)
}

func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*node)
	c.history = make(map[string]*list.Element)
	c.order.Init()
	c.expiry.nodes, c.eviction.nodes, c.due.nodes, c.ready.nodes = nil, nil, nil, nil
	c.epoch++
}
func (c *Cache) Stop() { c.Clear() }
func (c *Cache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
func (c *Cache) GetAllEntries() []*CacheEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entries := make([]*CacheEntry, 0, len(c.entries))
	for _, n := range c.entries {
		copyEntry := n.entry
		copyEntry.Answers, copyEntry.Authority = cloneRecords(n.entry.Answers), cloneRecords(n.entry.Authority)
		entries = append(entries, &copyEntry)
	}
	return entries
}
func cloneRecords(records []dns.RR) []dns.RR {
	cloned := make([]dns.RR, len(records))
	for i, rr := range records {
		cloned[i] = dns.Copy(rr)
	}
	return cloned
}
func (e *CacheEntry) preloadAt() time.Time {
	return e.ExpireTime.Add(-time.Duration(e.TTL) * time.Second / 5)
}
func (e *CacheEntry) NeedPreload() bool { return e.needPreloadAt(time.Now()) }

func (e *CacheEntry) needPreloadAt(now time.Time) bool {
	return !now.Before(e.preloadAt())
}
