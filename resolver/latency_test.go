package resolver

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestSelectionDelay(t *testing.T) {
	for _, test := range []struct {
		latency, timeout, want time.Duration
	}{
		{20 * time.Millisecond, time.Second, 40 * time.Millisecond},
		{600 * time.Millisecond, time.Second, time.Second},
		{time.Second / 2, time.Second, time.Second},
		{time.Duration(1<<63 - 1), time.Second, time.Second},
	} {
		if got := selectionDelay(test.latency, test.timeout); got != test.want {
			t.Fatalf("delay(%s, %s)=%s, want %s", test.latency, test.timeout, got, test.want)
		}
	}
}

func TestOptimalHedgeMeasuresWinnerFromOwnStart(t *testing.T) {
	var originalCalls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	original := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		originalCalls.Add(1)
		<-release
		_ = w.WriteMsg(testAnswer(query))
	})
	start := time.Now()
	addedAt := make(chan time.Duration, 1)
	added := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		addedAt <- time.Since(start)
		time.Sleep(15 * time.Millisecond)
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "original", Protocol: "udp", Address: original, Timeout: 1})
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "added", Protocol: "udp", Address: added, Timeout: 1})
	names := []string{"original", "added"}
	state := &selection{preferred: "original", latency: 80 * time.Millisecond}
	r.selections[selectionKey(names)] = state
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, winner, err := r.ResolveStrategy(ctx, "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
	if err != nil || winner != "added" {
		t.Fatalf("hedge winner %s: %v", winner, err)
	}
	launchDelay := <-addedAt
	if launchDelay < 160*time.Millisecond || launchDelay > 700*time.Millisecond {
		t.Fatalf("hedge not launched at 2t before configured timeout: %s", launchDelay)
	}
	if originalCalls.Load() != 1 {
		t.Fatalf("original was restarted: calls=%d", originalCalls.Load())
	}
	if state.latency < 15*time.Millisecond || state.latency >= launchDelay {
		t.Fatalf("winner latency includes original waiting time: %s, launch delay %s", state.latency, launchDelay)
	}
	if state.preferred != "added" {
		t.Fatal("hedge winner not remembered")
	}
}

func TestOptimalOriginalCanWinAfterHedge(t *testing.T) {
	addedStarted := make(chan struct{})
	original := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		<-addedStarted
		_ = w.WriteMsg(testAnswer(query))
	})
	added := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		close(addedStarted)
		time.Sleep(50 * time.Millisecond)
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "original", Protocol: "udp", Address: original, Timeout: 1})
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "added", Protocol: "udp", Address: added, Timeout: 1})
	names := []string{"original", "added"}
	state := &selection{preferred: "original", latency: 20 * time.Millisecond}
	r.selections[selectionKey(names)] = state
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, winner, err := r.ResolveStrategy(ctx, "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
	if err != nil || winner != "original" || state.latency < 40*time.Millisecond {
		t.Fatalf("original did not win/update baseline after hedge: %s %s %v", winner, state.latency, err)
	}
}

func TestOptimalHedgeCancellation(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	upstream := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		<-release
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	for _, name := range []string{"a", "b"} {
		_ = r.RegisterUpstream(&config.UpstreamDNS{Name: name, Protocol: "udp", Address: upstream, Timeout: 1})
	}

	names := []string{"a", "b"}
	r.selections[selectionKey(names)] = &selection{preferred: "a", latency: 10 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, _, err := r.ResolveStrategy(ctx, "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestOptimalFailedRaceClearsSelection(t *testing.T) {
	invalid := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		response := testAnswer(query)
		response.Question = nil
		_ = w.WriteMsg(response)
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "invalid", Protocol: "udp", Address: invalid, Timeout: 1})
	names := []string{"invalid", "missing"}
	state := &selection{preferred: "invalid", latency: 400 * time.Millisecond}
	r.selections[selectionKey(names)] = state
	start := time.Now()
	_, _, err := r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
	if err == nil || state.preferred != "" {
		t.Fatalf("failed race retained preference: %q %v", state.preferred, err)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("immediate invalid response waited for latency threshold")
	}
}
