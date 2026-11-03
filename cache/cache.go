package cache

import (
	"sync"
	"time"

	"github.com/miekg/dns"
)

// CacheEntry represents a cached DNS response
type CacheEntry struct {
	Query      string   // Domain name
	QueryType  uint16   // DNS query type (A, AAAA, etc.)
	Answers    []dns.RR // dns.RR answers
	Authority  []dns.RR
	Upstream   string
	Rcode      int       // Response code
	TTL        uint32    // Time to live in seconds
	ExpireTime time.Time // When the entry expires
	CreatedAt  time.Time // When the entry was created
	HitCount   int       // Number of times this entry was used
}

// Cache implements a simple DNS response cache
type Cache struct {
	mu       sync.RWMutex
	entries  map[string]*CacheEntry
	maxSize  int
	ticker   *time.Ticker
	stopChan chan struct{}
}

// NewCache creates a new cache with specified max size
func NewCache(maxSize int) *Cache {
	c := &Cache{
		entries:  make(map[string]*CacheEntry),
		maxSize:  maxSize,
		stopChan: make(chan struct{}),
	}

	// Start cleanup goroutine
	c.ticker = time.NewTicker(30 * time.Second)
	go c.cleanupLoop()

	return c
}

// Set adds or updates a cache entry
func (c *Cache) Set(key string, entry *CacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setLocked(key, entry)
}

func (c *Cache) setLocked(key string, entry *CacheEntry) {
	if entry == nil || entry.TTL == 0 || !entry.ExpireTime.After(time.Now()) {
		delete(c.entries, key)
		return
	}

	// Check if cache is full
	if len(c.entries) >= c.maxSize && c.entries[key] == nil {
		c.evictOldest()
	}

	copyEntry := *entry
	copyEntry.Answers = cloneRecords(entry.Answers)
	copyEntry.Authority = cloneRecords(entry.Authority)
	if copyEntry.CreatedAt.IsZero() {
		copyEntry.CreatedAt = time.Now()
	}
	c.entries[key] = &copyEntry
}

// Refresh replaces the scanned entry without losing hits received during I/O.
// Entries removed or replaced since the scan are left untouched.
func (c *Cache) Refresh(key string, previous, replacement *CacheEntry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	current := c.entries[key]
	if previous == nil || replacement == nil || replacement.TTL == 0 ||
		!replacement.ExpireTime.After(time.Now()) || current == nil ||
		!current.CreatedAt.Equal(previous.CreatedAt) || !current.ExpireTime.Equal(previous.ExpireTime) {
		return false
	}
	updated := *replacement
	updated.HitCount = current.HitCount
	c.setLocked(key, &updated)
	return true
}

// Get retrieves a cache entry if not expired
func (c *Cache) Get(key string) (*CacheEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[key]
	if !exists {
		return nil, false
	}

	now := time.Now()
	if !now.Before(entry.ExpireTime) {
		delete(c.entries, key)
		return nil, false
	}

	elapsed := uint32(now.Sub(entry.CreatedAt) / time.Second)
	result := *entry
	result.Answers = cloneRecords(entry.Answers)
	result.Authority = cloneRecords(entry.Authority)
	for _, answer := range result.Answers {
		if answer.Header().Ttl > elapsed {
			answer.Header().Ttl -= elapsed
		} else {
			answer.Header().Ttl = 0
		}
	}
	for _, rr := range result.Authority {
		if rr.Header().Ttl > elapsed {
			rr.Header().Ttl -= elapsed
		} else {
			rr.Header().Ttl = 0
		}
	}
	result.HitCount++
	entry.HitCount++
	return &result, true
}

// Remove deletes a cache entry
func (c *Cache) Remove(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, key)
}

// Clear removes all entries from cache
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}

// Size returns the current number of entries
func (c *Cache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.entries)
}

// evictOldest removes the entry with lowest hit count and oldest creation time
func (c *Cache) evictOldest() {
	var oldestKey string
	var oldestEntry *CacheEntry

	for key, entry := range c.entries {
		if oldestEntry == nil || entry.HitCount < oldestEntry.HitCount ||
			(entry.HitCount == oldestEntry.HitCount && entry.CreatedAt.Before(oldestEntry.CreatedAt)) {
			oldestKey = key
			oldestEntry = entry
		}
	}

	if oldestKey != "" {
		delete(c.entries, oldestKey)
	}
}

// cleanupLoop periodically removes expired entries
func (c *Cache) cleanupLoop() {
	for {
		select {
		case <-c.ticker.C:
			c.cleanup()
		case <-c.stopChan:
			c.ticker.Stop()
			return
		}
	}
}

// cleanup removes all expired entries
func (c *Cache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.ExpireTime) {
			delete(c.entries, key)
		}
	}
}

// Stop gracefully stops the cache cleanup goroutine
func (c *Cache) Stop() {
	close(c.stopChan)
}

// GetAllEntries returns all cache entries (for debugging/status)
func (c *Cache) GetAllEntries() []*CacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entries := make([]*CacheEntry, 0, len(c.entries))
	for _, entry := range c.entries {
		copyEntry := *entry
		copyEntry.Answers = cloneRecords(entry.Answers)
		copyEntry.Authority = cloneRecords(entry.Authority)
		entries = append(entries, &copyEntry)
	}
	return entries
}

// GetEntriesNeedingPreload returns non-expired entries close to expiration.
func (c *Cache) GetEntriesNeedingPreload() []*CacheEntry {
	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	entries := make([]*CacheEntry, 0)
	for _, entry := range c.entries {
		if !entry.needPreloadAt(now) {
			continue
		}
		copyEntry := *entry
		copyEntry.Answers = cloneRecords(entry.Answers)
		copyEntry.Authority = cloneRecords(entry.Authority)
		entries = append(entries, &copyEntry)
	}
	return entries
}

func cloneRecords(records []dns.RR) []dns.RR {
	cloned := make([]dns.RR, len(records))
	for i, record := range records {
		cloned[i] = dns.Copy(record)
	}
	return cloned
}

// NeedPreload checks if an entry needs active preload based on TTL
func (e *CacheEntry) NeedPreload() bool {
	return e.needPreloadAt(time.Now())
}

func (e *CacheEntry) needPreloadAt(now time.Time) bool {
	// Preload if 75% of TTL has passed
	timeLeft := e.ExpireTime.Sub(now)
	originalTTL := time.Duration(e.TTL) * time.Second
	return timeLeft > 0 && timeLeft < (originalTTL/4)
}
