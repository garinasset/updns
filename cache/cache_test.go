package cache

import (
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestCacheSetGet(t *testing.T) {
	c := NewCache(10)
	defer c.Stop()

	entry := &CacheEntry{
		Query:      "example.com",
		QueryType:  1,
		TTL:        300,
		ExpireTime: time.Now().Add(5 * time.Minute),
		CreatedAt:  time.Now(),
	}

	c.Set("example.com:1", entry)

	retrieved, found := c.Get("example.com:1")
	if !found {
		t.Fatal("Entry not found in cache")
	}

	if retrieved.Query != "example.com" {
		t.Errorf("Expected query 'example.com', got %q", retrieved.Query)
	}
}

func TestCacheExpiry(t *testing.T) {
	c := NewCache(10)
	defer c.Stop()

	entry := &CacheEntry{
		Query:      "example.com",
		QueryType:  1,
		TTL:        1,
		ExpireTime: time.Now().Add(-1 * time.Second), // Already expired
		CreatedAt:  time.Now().Add(-10 * time.Second),
	}

	c.Set("example.com:1", entry)

	_, found := c.Get("example.com:1")
	if found {
		t.Error("Expected expired entry to not be found")
	}
}

func TestCacheSize(t *testing.T) {
	c := NewCache(3)
	defer c.Stop()

	for i := 0; i < 3; i++ {
		entry := &CacheEntry{
			Query:      "example" + string(rune(i)) + ".com",
			QueryType:  1,
			TTL:        300,
			ExpireTime: time.Now().Add(5 * time.Minute),
			CreatedAt:  time.Now(),
		}
		c.Set("key"+string(rune(i)), entry)
	}

	if c.Size() != 3 {
		t.Errorf("Expected cache size 3, got %d", c.Size())
	}

	// Add one more entry, should evict oldest
	entry := &CacheEntry{
		Query:      "new.com",
		QueryType:  1,
		TTL:        300,
		ExpireTime: time.Now().Add(5 * time.Minute),
		CreatedAt:  time.Now(),
	}
	c.Set("key3", entry)

	if c.Size() != 3 {
		t.Errorf("Expected cache size to stay 3 after eviction, got %d", c.Size())
	}
}

func TestCacheEviction(t *testing.T) {
	c := NewCache(2)
	defer c.Stop()

	// Add first entry with low hit count
	entry1 := &CacheEntry{
		Query:      "old.com",
		QueryType:  1,
		TTL:        300,
		ExpireTime: time.Now().Add(5 * time.Minute),
		CreatedAt:  time.Now().Add(-10 * time.Second),
		HitCount:   1,
	}
	c.Set("key1", entry1)

	// Add second entry
	entry2 := &CacheEntry{
		Query:      "medium.com",
		QueryType:  1,
		TTL:        300,
		ExpireTime: time.Now().Add(5 * time.Minute),
		CreatedAt:  time.Now(),
		HitCount:   5,
	}
	c.Set("key2", entry2)

	// Add third entry (should evict entry1 as it has lower hit count and is older)
	entry3 := &CacheEntry{
		Query:      "new.com",
		QueryType:  1,
		TTL:        300,
		ExpireTime: time.Now().Add(5 * time.Minute),
		CreatedAt:  time.Now(),
		HitCount:   3,
	}
	c.Set("key3", entry3)

	_, found := c.Get("key1")
	if found {
		t.Error("Expected oldest entry to be evicted")
	}

	_, found = c.Get("key2")
	if !found {
		t.Error("Expected medium entry to still exist")
	}

	_, found = c.Get("key3")
	if !found {
		t.Error("Expected new entry to exist")
	}
}

func TestCacheClear(t *testing.T) {
	c := NewCache(10)
	defer c.Stop()

	entry := &CacheEntry{
		Query:      "example.com",
		QueryType:  1,
		TTL:        300,
		ExpireTime: time.Now().Add(5 * time.Minute),
		CreatedAt:  time.Now(),
	}

	c.Set("example.com:1", entry)
	if c.Size() != 1 {
		t.Errorf("Expected size 1, got %d", c.Size())
	}

	c.Clear()
	if c.Size() != 0 {
		t.Error("Expected cache to be empty after clear")
	}
}

func TestCacheNeedPreload(t *testing.T) {
	entry := &CacheEntry{
		Query:      "example.com",
		QueryType:  1,
		TTL:        100,
		ExpireTime: time.Now().Add(20 * time.Second), // 20% TTL left
		CreatedAt:  time.Now().Add(-80 * time.Second),
	}

	// Should need preload (80% TTL elapsed).
	if !entry.NeedPreload() {
		t.Error("Expected entry to need preload")
	}

	entry.ExpireTime = time.Now().Add(80 * time.Second) // 80% TTL left
	if entry.NeedPreload() {
		t.Error("Expected entry not to need preload")
	}
}

func TestCacheReturnsDecrementedTTLWithoutMutatingStoredRecord(t *testing.T) {
	c := NewCache(4)
	defer c.Stop()

	created := time.Now().Add(-20 * time.Second)
	record := &dns.A{
		Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   []byte{192, 0, 2, 1},
	}
	c.Set("example.com.:1", &CacheEntry{
		Query:      "example.com.",
		QueryType:  dns.TypeA,
		Answers:    []dns.RR{record},
		TTL:        60,
		ExpireTime: created.Add(60 * time.Second),
		CreatedAt:  created,
	})

	entry, ok := c.Get("example.com.:1")
	if !ok {
		t.Fatal("expected cache entry")
	}
	if got := entry.Answers[0].Header().Ttl; got >= 60 || got < 39 {
		t.Fatalf("cached response TTL should reflect elapsed time, got %d", got)
	}
	if got := record.Header().Ttl; got != 60 {
		t.Fatalf("Get mutated the stored record: TTL = %d", got)
	}
}

func TestRefreshPreservesHitsReceivedAfterScan(t *testing.T) {
	c := NewCache(4)
	defer c.Stop()
	now := time.Now()
	c.Set("key", &CacheEntry{TTL: 100, CreatedAt: now.Add(-80 * time.Second), ExpireTime: now.Add(20 * time.Second), HitCount: 7})
	scanned, epoch := c.Snapshot("key")
	for i := 0; i < 3; i++ {
		if _, ok := c.Get("key"); !ok {
			t.Fatal("entry unexpectedly expired")
		}
	}
	replacement := &CacheEntry{TTL: 200, CreatedAt: now, ExpireTime: now.Add(200 * time.Second)}
	if !c.Commit("key", scanned, epoch, replacement) {
		t.Fatal("refresh should succeed")
	}
	got := c.GetAllEntries()[0]
	if got.HitCount != 10 || got.TTL != 200 || !got.CreatedAt.Equal(now) {
		t.Fatalf("refresh lost hits or timestamps: %#v", got)
	}
	if replacement.HitCount != 0 {
		t.Fatal("refresh mutated caller's entry")
	}
}

func TestRefreshDoesNotOverwriteReplacedOrRemovedEntry(t *testing.T) {
	for _, action := range []string{"replace", "remove", "clear"} {
		t.Run(action, func(t *testing.T) {
			c := NewCache(4)
			defer c.Stop()
			now := time.Now()
			c.Set("key", &CacheEntry{TTL: 100, CreatedAt: now.Add(-80 * time.Second), ExpireTime: now.Add(20 * time.Second)})
			scanned, epoch := c.Snapshot("key")
			next := &CacheEntry{TTL: 200, CreatedAt: now, ExpireTime: now.Add(200 * time.Second), HitCount: 5}
			switch action {
			case "replace":
				c.Set("key", next)
			case "remove":
				c.Remove("key")
			case "clear":
				c.Clear()
			}
			if c.Commit("key", scanned, epoch, next) {
				t.Fatal("stale refresh should not be committed")
			}
			if action == "replace" {
				if got := c.GetAllEntries()[0]; got.HitCount != 0 {
					t.Fatalf("new entry was changed: %#v", got)
				}
			} else if c.Size() != 0 {
				t.Fatal("refresh resurrected a removed entry")
			}
		})
	}
}
