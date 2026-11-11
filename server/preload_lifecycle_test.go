package server

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/cache"
	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func newFlightTestServer(t *testing.T, handler dns.HandlerFunc) *Server {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	upstream := &dns.Server{PacketConn: pc, Handler: handler, NotifyStartedFunc: func() { close(ready) }}
	go func() {
		if err := upstream.ActivateAndServe(); err != nil {
			t.Errorf("upstream: %v", err)
		}
	}()
	<-ready
	t.Cleanup(func() { _ = upstream.Shutdown() })
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{Name: "test", Protocol: "udp", Address: pc.LocalAddr().String(), Timeout: 2}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.RegisterUpstream(&cfg.UpstreamDNS[0]); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	return srv
}

func seedPreload(srv *Server, name string, remaining time.Duration) {
	now := time.Now()
	srv.cache.Set(name+":1", &cache.CacheEntry{
		Query: name, QueryType: dns.TypeA, TTL: 100, CreatedAt: now.Add(remaining - 100*time.Second),
		ExpireTime: now.Add(remaining), HitCount: 7,
	})
}

func writeFlightAnswer(t *testing.T, w dns.ResponseWriter, query *dns.Msg) {
	t.Helper()
	response := new(dns.Msg)
	response.SetReply(query)
	response.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 100},
		A:   net.IPv4(192, 0, 2, 1),
	}}
	if err := w.WriteMsg(response); err != nil {
		t.Errorf("write upstream: %v", err)
	}
}

func TestExpiredClientsSharePreloadAndReportWait(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var calls atomic.Int32
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		writeFlightAnswer(t, w, query)
	})
	defer unblock()
	seedPreload(srv, "wait.example.", -time.Second)
	srv.preloadCacheEntries()
	<-started
	const clients = 12
	results := make(chan *QueryResult, clients)
	for i := 0; i < clients; i++ {
		go func() {
			result, err := srv.Query("WAIT.example.", dns.TypeA)
			if err != nil {
				t.Errorf("wait: %v", err)
			}
			results <- result
		}()
	}
	select {
	case <-results:
		t.Fatal("expired answer served before refresh")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	for i := 0; i < clients; i++ {
		select {
		case result := <-results:
			if result == nil || result.CacheHit || !result.PrefetchWait || len(result.Records) != 1 {
				t.Fatalf("incorrect waiting path: %#v", result)
			}
		case <-time.After(time.Second):
			t.Fatal("waiters not notified")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate upstream queries")
	}
	entry, _ := srv.cache.Snapshot("wait.example.:1")
	if entry.HitCount != 7 {
		t.Fatal("wait counted as cache hit or history lost")
	}
	if result, _ := srv.Query("wait.example.", dns.TypeA); !result.CacheHit || result.PrefetchWait {
		t.Fatal("refreshed entry not usable")
	}
}

func TestPreloadFailureRetainsLiveEntryAndRetriesAtExpiry(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		if fail.Load() {
			response := new(dns.Msg)
			response.SetRcode(query, dns.RcodeServerFailure)
			if err := w.WriteMsg(response); err != nil {
				t.Errorf("write failure: %v", err)
			}
			return
		}
		writeFlightAnswer(t, w, query)
	})
	seedPreload(srv, "failed.example.", 10*time.Second)
	srv.preloadCacheEntries()
	srv.queryWG.Wait()
	if srv.cache.Size() != 1 {
		t.Fatal("failed prefetch deleted live entry")
	}
	srv.preloadCacheEntries()
	srv.queryMu.Lock()
	pending := srv.preloading
	srv.queryMu.Unlock()
	if pending != 0 {
		t.Fatal("failed live prefetch was immediately retried")
	}
	result, err := srv.Query("failed.example.", dns.TypeA)
	if err != nil || !result.CacheHit {
		t.Fatalf("live entry unavailable: %#v %v", result, err)
	}
	seedPreload(srv, "failed.example.", -time.Second)
	fail.Store(false)
	result, err = srv.Query("failed.example.", dns.TypeA)
	if err != nil || result.CacheHit {
		t.Fatalf("new query: %#v %v", result, err)
	}
	entry, _ := srv.cache.Snapshot("failed.example.:1")
	if entry.HitCount != 8 {
		t.Fatal("refresh lost hits on retained entry")
	}
}

func TestForegroundOwnsExpiredEntryAndPreservesHits(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		writeFlightAnswer(t, w, query)
	})
	defer unblock()
	seedPreload(srv, "foreground.example.", -time.Second)
	done := make(chan *QueryResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			result, err := srv.Query("foreground.example.", dns.TypeA)
			if err != nil {
				t.Errorf("foreground query: %v", err)
			}
			done <- result
		}()
	}
	<-started
	srv.preloadCacheEntries()
	select {
	case <-done:
		t.Fatal("expired entry served")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	first, second := <-done, <-done
	if first == nil || second == nil || first.CacheHit || second.CacheHit ||
		first.PrefetchWait || second.PrefetchWait || calls.Load() != 1 {
		t.Fatal("foreground queries not merged or mislabeled")
	}
	first.Records[0].Header().Ttl = 0
	if second.Records[0].Header().Ttl != 100 {
		t.Fatal("waiters share mutable DNS records")
	}
	entry, _ := srv.cache.Snapshot("foreground.example.:1")
	if entry.HitCount != 7 {
		t.Fatal("expired foreground refresh lost count")
	}
}

func TestFailedPreloadWaitPreservesFailurePath(t *testing.T) {
	// An invalid reply (no question) fails; an upstream SERVFAIL is a valid winner.
	for _, invalid := range []bool{true, false} {
		t.Run(fmt.Sprintf("invalid=%t", invalid), func(t *testing.T) {
			started := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
				close(started)
				<-release
				response := new(dns.Msg)
				response.SetRcode(query, dns.RcodeServerFailure)
				if invalid {
					response.Question = nil
				}
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write failure: %v", err)
				}
			})
			defer unblock()
			seedPreload(srv, "error.example.", -time.Second)
			srv.preloadCacheEntries()
			<-started
			done := make(chan *QueryResult, 1)
			go func() {
				result, err := srv.Query("error.example.", dns.TypeA)
				if (err != nil) != invalid {
					t.Errorf("error=%v, want failure=%t", err, invalid)
				}
				done <- result
			}()
			select {
			case <-done:
				t.Fatal("wait returned early")
			case <-time.After(50 * time.Millisecond):
			}
			unblock()
			result := <-done
			if result == nil || !result.PrefetchWait || (result.Error != "") != invalid || result.Rcode != dns.RcodeServerFailure {
				t.Fatalf("failed wait lost metadata: %#v", result)
			}
			if srv.cache.Size() != 0 {
				t.Fatal("failed wait left stale entry")
			}
		})
	}
}

func TestScanDoesNotWaitForSlowBatch(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 2*preloadConcurrency(config.DefaultConfig().CacheSize))
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		writeFlightAnswer(t, w, query)
	})
	defer unblock()
	limit := preloadConcurrency(srv.cfg.CacheSize)
	for i := 0; i < 2*limit; i++ {
		seedPreload(srv, fmt.Sprintf("scan%d.example.", i), -time.Duration(i+1)*time.Second)
	}
	srv.preloadCacheEntries()
	for i := 0; i < limit; i++ {
		<-started
	}
	for i := 0; i < 10; i++ {
		srv.preloadCacheEntries()
	}
	if calls.Load() != int32(limit) {
		t.Fatal("scan exceeded global limit or duplicated requests")
	}
	unblock()
	srv.queryWG.Wait()
	srv.preloadCacheEntries()
	srv.queryWG.Wait()
	if calls.Load() != int32(2*limit) {
		t.Fatal("remaining pending tasks not scheduled")
	}
}

func TestClearCancelsWaitersAndPreventsResurrection(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		close(started)
		<-release
		writeFlightAnswer(t, w, query)
	})
	defer unblock()
	seedPreload(srv, "clear.example.", -time.Second)
	srv.preloadCacheEntries()
	<-started
	srv.ClearCache()
	srv.queryWG.Wait()
	if srv.cache.Size() != 0 {
		t.Fatal("clear resurrected cache")
	}
	srv.queryMu.Lock()
	if len(srv.flights) != 0 || srv.preloading != 0 {
		t.Error("clear retained task pool")
	}
	srv.queryMu.Unlock()
}

func TestPrefetchWaitLog(t *testing.T) {
	result := &QueryResult{Domain: "wait.example.", QueryType: "A", PrefetchWait: true}
	if got := FormatQueryLog("SRV", result, "", ""); !strings.Contains(got, "→ Prefetch Hit →") {
		t.Fatalf("missing waiting path: %s", got)
	}
}
