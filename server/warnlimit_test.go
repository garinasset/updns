package server

import (
	"fmt"
	"net"
	"testing"
	"time"
)

func TestWarnLimiterSuppressesAndCounts(t *testing.T) {
	now := time.Unix(0, 0)
	l := newWarnLimiter(30 * time.Second)
	l.now = func() time.Time { return now }

	if ok, s := l.allow("r", "c0"); !ok || s.Suppressed != 0 {
		t.Fatalf("first = %v,%+v", ok, s)
	}
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("r", ""); ok {
			t.Fatal("must be suppressed inside the interval")
		}
	}
	if ok, _ := l.allow("other", ""); !ok {
		t.Fatal("keys are independent")
	}
	now = now.Add(31 * time.Second)
	if ok, s := l.allow("r", ""); !ok || s.Suppressed != 3 {
		t.Fatalf("after interval = %v,%+v, want true,3", ok, s)
	}
}

func TestWarnLimiterCollectsSuppressedClients(t *testing.T) {
	now := time.Unix(0, 0)
	l := newWarnLimiter(30 * time.Second)
	l.now = func() time.Time { return now }

	l.allow("r", "first")
	for _, c := range []string{"a", "b", "a", "", "b"} {
		l.allow("r", c)
	}
	now = now.Add(31 * time.Second)
	ok, s := l.allow("r", "later")
	if !ok || s.Suppressed != 5 || fmt.Sprint(s.Clients) != "[a b]" || s.MoreClients {
		t.Fatalf("summary = %v,%+v", ok, s)
	}

	now = now.Add(31 * time.Second)
	if ok, s := l.allow("r", ""); !ok || s.Suppressed != 0 || len(s.Clients) != 0 {
		t.Fatalf("clients must reset after a summary: %v,%+v", ok, s)
	}
}

func TestWarnLimiterCapsTrackedClients(t *testing.T) {
	now := time.Unix(0, 0)
	l := newWarnLimiter(30 * time.Second)
	l.now = func() time.Time { return now }

	l.allow("r", "first")
	for i := 0; i < maxTrackedClients+3; i++ {
		l.allow("r", fmt.Sprintf("c%d", i))
	}
	now = now.Add(31 * time.Second)
	_, s := l.allow("r", "")
	if len(s.Clients) != maxTrackedClients || !s.MoreClients || s.Suppressed != maxTrackedClients+3 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFormatSuppressed(t *testing.T) {
	interval := 30 * time.Second
	if got := formatSuppressed(warnSummary{Suppressed: 12}, interval); got != "suppressed 12 similar in last 30s" {
		t.Errorf("no clients: %q", got)
	}
	got := formatSuppressed(warnSummary{Suppressed: 12, Clients: []string{"a", "b"}}, interval)
	if want := "suppressed 12 similar in last 30s from 2 clients: a, b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	got = formatSuppressed(warnSummary{Suppressed: 99, Clients: []string{"a"}, MoreClients: true}, interval)
	if want := "suppressed 99 similar in last 30s from 1+ clients: a"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestClientAddr(t *testing.T) {
	if got := clientAddr(&net.UDPAddr{IP: net.ParseIP("2409:8a00:24a0:5610::4"), Port: 5353}); got != "2409:8a00:24a0:5610::4" {
		t.Errorf("udp v6 = %q", got)
	}
	if got := clientAddr(&net.TCPAddr{IP: net.ParseIP("192.168.1.5"), Port: 40000}); got != "192.168.1.5" {
		t.Errorf("tcp v4 = %q", got)
	}
	if got := clientAddr(nil); got != "" {
		t.Errorf("nil = %q", got)
	}
}

func TestFailureCacheExpiresAndClears(t *testing.T) {
	now := time.Unix(0, 0)
	c := newFailureCache(30 * time.Second)
	c.now = func() time.Time { return now }

	if c.active("k") {
		t.Fatal("unknown key must not be active")
	}
	c.record("k")
	if !c.active("k") {
		t.Fatal("recorded key must be active")
	}
	now = now.Add(31 * time.Second)
	if c.active("k") {
		t.Fatal("expired key must not be active")
	}
	c.record("k")
	c.clear()
	if c.active("k") {
		t.Fatal("clear must drop entries")
	}
}

func TestFailureCacheIsBounded(t *testing.T) {
	now := time.Unix(0, 0)
	c := newFailureCache(30 * time.Second)
	c.now = func() time.Time { return now }
	for i := 0; i < failureCacheMax+10; i++ {
		c.record(fmt.Sprintf("k%d", i))
	}
	if len(c.entries) != failureCacheMax {
		t.Fatalf("entries = %d, want %d", len(c.entries), failureCacheMax)
	}
	now = now.Add(31 * time.Second)
	c.record("fresh")
	if !c.active("fresh") || len(c.entries) != 1 {
		t.Fatalf("expired entries must be swept when full: %d", len(c.entries))
	}
}
