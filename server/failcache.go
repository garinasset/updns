package server

import (
	"sync"
	"time"
)

// ptrFailureTTL is how long an all-upstreams-failed PTR query is remembered.
// Reverse zones of arbitrary Internet hosts are often broken and clients tend
// to ask again immediately, so repeating the upstream round trip is wasteful.
const ptrFailureTTL = 30 * time.Second

// failureCacheMax bounds the memory used when many distinct names fail.
const failureCacheMax = 4096

// failureCache remembers failed queries until their TTL expires.
type failureCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]time.Time
}

func newFailureCache(ttl time.Duration) *failureCache {
	return &failureCache{ttl: ttl, now: time.Now, entries: make(map[string]time.Time)}
}

// active reports whether a non-expired failure is remembered for key.
func (c *failureCache) active(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	expire, ok := c.entries[key]
	if !ok {
		return false
	}
	if !c.now().Before(expire) {
		delete(c.entries, key)
		return false
	}
	return true
}

// record remembers a failure for key. Expired entries are swept when the cache
// is full; if it is still full the failure is simply not remembered.
func (c *failureCache) record(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= failureCacheMax {
		for k, expire := range c.entries {
			if !now.Before(expire) {
				delete(c.entries, k)
			}
		}
		if len(c.entries) >= failureCacheMax {
			return
		}
	}
	c.entries[key] = now.Add(c.ttl)
}

func (c *failureCache) clear() {
	c.mu.Lock()
	c.entries = make(map[string]time.Time)
	c.mu.Unlock()
}
