package cache

import (
	"testing"
	"time"
)

func TestFailedRefreshPreservesLiveVersionAndWaitsForExpiry(t *testing.T) {
	c := NewCache(2)
	c.Set("live", testEntry(100, 10*time.Second))
	old, epoch := c.Snapshot("live")
	c.Suspend("live")
	c.Get("live")
	if !c.Commit("live", old, epoch, nil) {
		t.Fatal("failed refresh was not committed")
	}
	entry, _ := c.Snapshot("live")
	if entry == nil || entry.HitCount != 1 || entry.version != old.version || !entry.ExpireTime.Equal(old.ExpireTime) {
		t.Fatal("failed refresh changed the live version, count or TTL")
	}
	if pending, _ := c.TakePreloads(2); len(pending) != 0 {
		t.Fatal("failed refresh was retried before expiry")
	}
	if !c.entries["live"].refresh.Equal(old.ExpireTime) {
		t.Fatal("failed refresh was not scheduled for expiry")
	}
}

func TestFailedExpiredRefreshDeletesAndForgetsCount(t *testing.T) {
	c := NewCache(2)
	entry := testEntry(100, -time.Second)
	entry.HitCount = 9
	c.Set("expired", entry)
	old, epoch := c.Snapshot("expired")
	if !c.Commit("expired", old, epoch, &CacheEntry{TTL: 0}) || c.Size() != 0 {
		t.Fatal("expired failure was retained")
	}
	c.Set("expired", testEntry(100, time.Minute))
	newEntry, _ := c.Snapshot("expired")
	if newEntry.HitCount != 0 {
		t.Fatal("deleted entry resurrected historical hits")
	}
}

func TestRouteMismatchDoesNotCountAsHit(t *testing.T) {
	c := NewCache(1)
	entry := testEntry(100, time.Minute)
	entry.Route = "old"
	c.Set("name", entry)
	if _, found := c.GetForRoute("name", "new"); found {
		t.Fatal("cache served an obsolete route")
	}
	remaining, _ := c.Snapshot("name")
	if remaining.HitCount != 0 {
		t.Fatal("route miss incremented HitCount")
	}
}
