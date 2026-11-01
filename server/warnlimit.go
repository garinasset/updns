package server

import (
	"sync"
	"time"
)

// warnInterval is the minimum gap between two WARN lines of the same kind.
const warnInterval = 30 * time.Second

// maxTrackedClients bounds how many distinct suppressed clients are listed.
const maxTrackedClients = 10

// warnLimiter throttles repeated warnings per key and counts what it dropped.
type warnLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	now      func() time.Time
	entries  map[string]*warnEntry
}

type warnEntry struct {
	last       time.Time
	suppressed int
	clients    []string
	moreClient bool
}

// warnSummary describes the warnings dropped since the previous logged one.
type warnSummary struct {
	Suppressed int
	// Clients lists the distinct sources of the suppressed warnings, capped at
	// maxTrackedClients; MoreClients is set when the list was truncated.
	Clients     []string
	MoreClients bool
}

func newWarnLimiter(interval time.Duration) *warnLimiter {
	return &warnLimiter{interval: interval, now: time.Now, entries: make(map[string]*warnEntry)}
}

// allow reports whether a warning for key may be logged now, along with a
// summary of what was suppressed since the previous one. client identifies the
// source of this occurrence and may be empty.
func (l *warnLimiter) allow(key, client string) (bool, warnSummary) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e := l.entries[key]
	if e == nil {
		l.entries[key] = &warnEntry{last: now}
		return true, warnSummary{}
	}
	if now.Sub(e.last) < l.interval {
		e.suppressed++
		e.addClient(client)
		return false, warnSummary{}
	}
	summary := warnSummary{Suppressed: e.suppressed, Clients: e.clients, MoreClients: e.moreClient}
	e.last = now
	e.suppressed = 0
	e.clients = nil
	e.moreClient = false
	return true, summary
}

func (e *warnEntry) addClient(client string) {
	if client == "" {
		return
	}
	for _, c := range e.clients {
		if c == client {
			return
		}
	}
	if len(e.clients) >= maxTrackedClients {
		e.moreClient = true
		return
	}
	e.clients = append(e.clients, client)
}
