package resolver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestOptimalReusesWinnerAndReevaluatesExactThresholds(t *testing.T) {
	var slowCalls, fastCalls atomic.Int32
	slow := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		slowCalls.Add(1)
		time.Sleep(20 * time.Millisecond)
		_ = w.WriteMsg(testAnswer(query))
	})
	fast := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		fastCalls.Add(1)
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }
	for _, upstream := range []*config.UpstreamDNS{
		{Name: "slow", Protocol: "udp", Address: slow, Timeout: 1},
		{Name: "fast", Protocol: "udp", Address: fast, Timeout: 1},
	} {
		_ = r.RegisterUpstream(upstream)
	}
	names := []string{"slow", "fast"}
	query := func() {
		t.Helper()
		_, winner, err := r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
		if err != nil || winner != "fast" {
			t.Fatalf("winner=%s error=%v", winner, err)
		}
	}
	query()
	for i := 0; i < selectionQueries; i++ {
		query()
	}
	r.selectionMu.Lock()
	state := r.selections[selectionKey(names)]
	if state.queries != 50 {
		t.Fatalf("expected exactly 50 single-upstream selections, got %d", state.queries)
	}
	r.selectionMu.Unlock()
	query()
	r.selectionMu.Lock()
	count := state.queries
	r.selectionMu.Unlock()
	if count != 0 {
		t.Fatal("query threshold did not trigger reevaluation")
	}
	now = now.Add(selectionInterval - time.Nanosecond)
	query()
	r.selectionMu.Lock()
	count = state.queries
	r.selectionMu.Unlock()
	if count != 1 {
		t.Fatal("reevaluated before 20 seconds elapsed")
	}
	now = now.Add(time.Nanosecond)
	query()
	r.selectionMu.Lock()
	count = state.queries
	r.selectionMu.Unlock()
	if count != 0 {
		t.Fatal("20-second boundary did not trigger reevaluation")
	}
	if fastCalls.Load() != 54 {
		t.Fatalf("unexpected upstream request count: %d", fastCalls.Load())
	}
	// Canceled UDP requests may not reach a remote server, so test dispatch via state above.
	if slowCalls.Load() > 3 {
		t.Fatalf("preferred requests were broadcast: %d slow queries", slowCalls.Load())
	}
}

func TestOptimalAcceptsErrorWinnerAndPinnedPathBypassesReevaluation(t *testing.T) {
	var otherCalls atomic.Int32
	other := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		otherCalls.Add(1)
		_ = w.WriteMsg(testAnswer(query))
	})
	preferred := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		response := new(dns.Msg)
		response.SetRcode(query, dns.RcodeServerFailure)
		_ = w.WriteMsg(response)
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "other", Protocol: "udp", Address: other, Timeout: 1})
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "preferred", Protocol: "udp", Address: preferred, Timeout: 1})
	for _, strategy := range []config.QueryStrategy{config.StrategyOptimal, config.StrategyStrict, config.StrategyConcurrent} {
		response, winner, err := r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, []string{"other", "preferred"}, strategy, "preferred")
		if err != nil || winner != "preferred" || response.Rcode != dns.RcodeServerFailure || otherCalls.Load() != 0 {
			t.Fatalf("%s ignored cached path or errored RCODE: winner=%s response=%v error=%v", strategy, winner, response, err)
		}
	}
	if len(r.selections) != 0 {
		t.Fatal("pinned refresh entered group reevaluation")
	}
}

func TestOptimalFallbackAndSelectionReset(t *testing.T) {
	var invalid atomic.Bool
	first := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		response := testAnswer(query)
		if invalid.Load() {
			response.Question = nil
		}

		_ = w.WriteMsg(response)
	})
	second := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		time.Sleep(20 * time.Millisecond)
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "first", Protocol: "udp", Address: first, Timeout: 1})
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "second", Protocol: "udp", Address: second, Timeout: 1})
	names := []string{"first", "second"}
	_, winner, err := r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
	if err != nil || winner != "first" {
		t.Fatalf("initial selection %s: %v", winner, err)
	}
	invalid.Store(true)
	_, winner, err = r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, names, config.StrategyOptimal, "")
	if err != nil || winner != "second" {
		t.Fatalf("preferred failure did not switch: %s %v", winner, err)
	}
	r.ReplaceUpstreamsWithPrefix("first", nil)
	if len(r.selections) != 0 {
		t.Fatal("upstream replacement retained selection state")
	}
}

func TestOptimalColdErrorResponseWins(t *testing.T) {
	fast := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		response := new(dns.Msg)
		response.SetRcode(query, dns.RcodeRefused)
		_ = w.WriteMsg(response)
	})
	slow := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		time.Sleep(30 * time.Millisecond)
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "fast", Protocol: "udp", Address: fast, Timeout: 1})
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "slow", Protocol: "udp", Address: slow, Timeout: 1})
	response, winner, err := r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, []string{"fast", "slow"}, config.StrategyOptimal, "")
	if err != nil || winner != "fast" || response.Rcode != dns.RcodeRefused {
		t.Fatalf("valid DNS error did not win optimal selection: %s %v %v", winner, response, err)
	}
}

func TestSelectionClearCannotBeUndoneByOldQuery(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	first := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		close(started)
		<-release
		_ = w.WriteMsg(testAnswer(query))
	})
	r := NewResolver(0)
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "first", Protocol: "udp", Address: first, Timeout: 1})
	_ = r.RegisterUpstream(&config.UpstreamDNS{Name: "missing", Protocol: "udp", Address: "127.0.0.1:1", Timeout: 1})
	done := make(chan error, 1)
	go func() {
		_, _, err := r.ResolveStrategy(context.Background(), "example.org.", dns.TypeA, []string{"first", "missing"}, config.StrategyOptimal, "")
		done <- err
	}()
	<-started
	r.ClearSelection()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(r.selections) != 0 {
		t.Fatal("completed old query resurrected cleared preference")
	}
}
