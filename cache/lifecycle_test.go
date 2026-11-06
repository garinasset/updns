package cache

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func testEntry(ttl uint32, remaining time.Duration) *CacheEntry {
	now := time.Now()
	return &CacheEntry{TTL: ttl, ExpireTime: now.Add(remaining), CreatedAt: now.Add(remaining - time.Duration(ttl)*time.Second)}
}

func TestUpdatesInheritButDeletionForgetsHits(t *testing.T) {
	c := NewCache(2)
	defer c.Stop()
	c.Set("a", testEntry(100, time.Minute))
	for i := 0; i < 5; i++ {
		c.Get("a")
	}
	c.Set("a", testEntry(200, time.Minute))
	if entry, _ := c.Snapshot("a"); entry.HitCount != 5 {
		t.Fatal("update lost hits")
	}
	c.Remove("a")
	c.Set("a", testEntry(100, time.Minute))
	if entry, _ := c.Snapshot("a"); entry.HitCount != 0 {
		t.Fatal("deleted entry retained historical hits")
	}
	c.Clear()
	if c.Size() != 0 {
		t.Fatal("clear retained entries")
	}
}

func TestCapacityEvictsLFUWithoutReclaimingHotExpiredEntry(t *testing.T) {
	c := NewCache(2)
	defer c.Stop()
	c.Set("hot-expired", testEntry(100, time.Minute))
	for i := 0; i < 20; i++ {
		c.Get("hot-expired")
	}
	c.Set("hot-expired", testEntry(100, -time.Second))
	c.Set("live", testEntry(100, time.Minute))
	c.Set("new", testEntry(100, time.Minute))
	if entry, _ := c.Snapshot("live"); entry != nil {
		t.Fatal("cold live entry survived LFU eviction")
	}
	if entry, _ := c.Snapshot("hot-expired"); entry.HitCount != 20 {
		t.Fatal("hot expired entry was reclaimed")
	}
	if c.Size() != 2 {
		t.Fatal("capacity insertion reclaimed more than one entry")
	}
}

func TestEvictionTieUsesClientRecencyThenInsertionOrder(t *testing.T) {
	t.Run("hit entries", func(t *testing.T) {
		c := NewCache(2)
		defer c.Stop()
		c.Set("old-hit", testEntry(100, time.Minute))
		c.Set("recent-hit", testEntry(100, time.Minute))
		c.Get("old-hit")
		c.Get("recent-hit")
		c.Get("old-hit")
		c.Set("new", testEntry(100, time.Minute))
		if entry, _ := c.Snapshot("old-hit"); entry == nil {
			t.Fatal("most recently hit entry was evicted")
		}
		if entry, _ := c.Snapshot("recent-hit"); entry != nil {
			t.Fatal("least recently hit entry survived")
		}
	})
	t.Run("never hit entries", func(t *testing.T) {
		c := NewCache(2)
		defer c.Stop()
		c.Set("first", testEntry(100, time.Minute))
		c.Set("second", testEntry(100, time.Minute))
		c.Set("new", testEntry(100, time.Minute))
		if entry, _ := c.Snapshot("first"); entry != nil {
			t.Fatal("first-inserted entry should lose an untouched tie")
		}
	})
}

func TestExpiredIsRetainedButNeverServed(t *testing.T) {
	c := NewCache(2)
	defer c.Stop()
	c.Set("expired", testEntry(100, -time.Second))
	if _, ok := c.Get("expired"); ok || c.Size() != 1 {
		t.Fatal("expired data served or prematurely removed")
	}
	entries, _ := c.TakePreloads(1)
	if len(entries) != 1 {
		t.Fatal("expired entry excluded from prefetch")
	}
}

func TestPreloadThresholdPriorityAndReservation(t *testing.T) {
	c := NewCache(8)
	defer c.Stop()
	for _, item := range []struct {
		key       string
		ttl       uint32
		remaining time.Duration
	}{
		{"not-due", 100, 21 * time.Second},
		{"due-long", 100, 19 * time.Second},
		{"oldest", 100, -20 * time.Second},
		{"expired", 100, -time.Second},
		{"due-short", 10, time.Second},
	} {
		c.Set(item.key, testEntry(item.ttl, item.remaining))
	}
	// Keys are opaque to Cache; use version tokens to identify selected nodes.
	versions := make(map[uint64]string)
	for key, n := range c.entries {
		versions[n.entry.version] = key
	}
	entries, epoch := c.TakePreloads(2)
	if len(entries) != 2 || versions[entries[0].version] != "oldest" || versions[entries[1].version] != "expired" {
		t.Fatal("not scheduled by earliest expiration")
	}
	next, _ := c.TakePreloads(8)
	if len(next) != 2 || versions[next[0].version] != "due-short" || versions[next[1].version] != "due-long" {
		t.Fatal("reservation or due threshold incorrect")
	}
	if !c.Commit("oldest", entries[0], epoch, testEntry(100, time.Minute)) {
		t.Fatal("expired refresh failed")
	}
	if more, _ := c.TakePreloads(8); len(more) != 0 {
		t.Fatal("refreshed or not-yet-due entry scheduled")
	}
}

func TestExactEightyPercentThreshold(t *testing.T) {
	now := time.Now()
	entry := &CacheEntry{TTL: 100, CreatedAt: now, ExpireTime: now.Add(100 * time.Second)}
	for _, sample := range []struct {
		elapsed time.Duration
		want    bool
	}{
		{80*time.Second - time.Nanosecond, false},
		{80 * time.Second, true},
		{100 * time.Second, true},
		{120 * time.Second, true},
	} {
		if got := entry.needPreloadAt(now.Add(sample.elapsed)); got != sample.want {
			t.Fatalf("elapsed=%s got=%t want=%t", sample.elapsed, got, sample.want)
		}
	}
}

func TestVersionProtectsFailureDeletionAndClear(t *testing.T) {
	c := NewCache(2)
	defer c.Stop()
	c.Set("a", testEntry(100, time.Second))
	old, epoch := c.Snapshot("a")
	c.Set("a", testEntry(100, time.Minute))
	if c.Commit("a", old, epoch, nil) || c.Size() != 1 {
		t.Fatal("old failure deleted new version")
	}
	old, epoch = c.Snapshot("a")
	c.Remove("a")
	c.Set("a", testEntry(100, time.Minute))
	if c.Commit("a", old, epoch, testEntry(100, time.Minute)) {
		t.Fatal("old refresh replaced re-added entry")
	}
	missing, epoch := c.Snapshot("missing")
	c.Clear()
	if c.Commit("missing", missing, epoch, testEntry(100, time.Minute)) {
		t.Fatal("clear resurrected entry")
	}
}

func TestHitCountSaturates(t *testing.T) {
	c := NewCache(1)
	defer c.Stop()
	entry := testEntry(100, time.Minute)
	entry.HitCount = math.MaxInt
	c.Set("a", entry)
	got, _ := c.Get("a")
	if got.HitCount != math.MaxInt {
		t.Fatal("hit count overflowed")
	}
}

func TestIndexedHeapInvariants(t *testing.T) {
	c := NewCache(32)
	defer c.Stop()
	random := rand.New(rand.NewSource(42))
	for i := 0; i < 2000; i++ {
		key := fmt.Sprint(random.Intn(64))
		switch random.Intn(6) {
		case 0, 1:
			c.Set(key, testEntry(100, time.Duration(random.Intn(100)-20)*time.Second))
		case 2:
			c.Get(key)
		case 3:
			c.Remove(key)
		case 4:
			entries, epoch := c.TakePreloads(4)
			for _, entry := range entries {
				for key, n := range c.entries {
					if n.entry.version == entry.version {
						c.Commit(key, entry, epoch, testEntry(100, time.Minute))
						break
					}
				}
			}
		case 5:
			if random.Intn(50) == 0 {
				c.Clear()
			}
		}
		for _, h := range []*entryHeap{&c.eviction, &c.due, &c.ready} {
			for index, n := range h.nodes {
				if n.index[h.kind] != index || c.entries[n.key] != n {
					t.Fatal("stale heap index")
				}
				if index > 0 && h.Less(index, (index-1)/2) {
					t.Fatal("heap ordering broken")
				}
			}
		}
		if c.Size() > 32 || c.eviction.Len() != c.Size() {
			t.Fatal("capacity or index leak")
		}
	}
}

func BenchmarkCacheHit8192(b *testing.B) {
	c := NewCache(8192)
	for i := 0; i < 8192; i++ {
		entry := testEntry(100, time.Minute)
		entry.Answers = []dns.RR{&dns.A{
			Hdr: dns.RR_Header{Name: fmt.Sprintf("%d.example.", i), Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 100},
			A:   []byte{192, 0, 2, byte(i)},
		}}
		c.Set(fmt.Sprint(i), entry)
	}
	keys := make([]string, 8192)
	for i := range keys {
		keys[i] = fmt.Sprint(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Get(keys[i%len(keys)])
	}
}

func BenchmarkIdlePreload8192(b *testing.B) {
	c := NewCache(8192)
	for i := 0; i < 8192; i++ {
		c.Set(fmt.Sprint(i), testEntry(100, time.Minute))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.TakePreloads(8)
	}
}
