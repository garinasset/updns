package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/cache"
	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestUnmatchedQueryUsesWholeOrderedUpstreamList(t *testing.T) {
	for _, withRule := range []bool{false, true} {
		t.Run(fmt.Sprintf("with-unmatched-rule=%t", withRule), func(t *testing.T) {
			var localCalls, configuredCalls atomic.Int32
			local := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
				localCalls.Add(1)
				response := new(dns.Msg)
				response.SetReply(query)
				response.Question = nil
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write invalid local response: %v", err)
				}
			})
			srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
				configuredCalls.Add(1)
				if localCalls.Load() != 1 {
					t.Error("strict order queried the configured upstream before the local resolver")
				}
				writeFlightAnswer(t, w, query)
			})
			srv.cfg.QueryStrategy = config.StrategyStrict
			upstream := local.cfg.UpstreamDNS[0]
			upstream.Name = config.LocalResolverPrefix + "1"
			srv.cfg.UpstreamDNS = append([]config.UpstreamDNS{upstream}, srv.cfg.UpstreamDNS...)
			if err := srv.RegisterUpstream(&upstream); err != nil {
				t.Fatal(err)
			}
			if withRule {
				path := filepath.Join(t.TempDir(), "domains")
				if err := os.WriteFile(path, []byte("other.example\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := srv.LoadDomainList("other", path); err != nil {
					t.Fatal(err)
				}
				srv.cfg.Rules = []config.Rule{{ID: "other", DomainLists: []string{"other"}, Upstreams: []string{"missing"}}}
			}
			result, err := srv.Query("unmatched.example.", dns.TypeA)
			if err != nil {
				t.Fatal(err)
			}
			if result.MatchedRule != "" || result.UsedUpstream != "test" || localCalls.Load() != 1 || configuredCalls.Load() != 1 {
				t.Fatalf("unmatched query did not use the full ordered group: %#v, local=%d configured=%d", result, localCalls.Load(), configuredCalls.Load())
			}
		})
	}
}

func TestRefreshReusesOriginalUpstreamAndSwitchesOnlyOnFailure(t *testing.T) {
	for _, strategy := range []config.QueryStrategy{config.StrategyOptimal, config.StrategyStrict, config.StrategyConcurrent} {
		t.Run(string(strategy), func(t *testing.T) {
			var originalCalls, otherCalls atomic.Int32
			var invalid atomic.Bool
			srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
				originalCalls.Add(1)
				if invalid.Load() {
					response := new(dns.Msg)
					response.SetReply(query)
					response.Question = nil
					_ = w.WriteMsg(response)
				} else {
					writeFlightAnswer(t, w, query)
				}
			})
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan struct{})
			upstream := &dns.Server{PacketConn: pc, NotifyStartedFunc: func() { close(ready) }, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
				otherCalls.Add(1)
				writeFlightAnswer(t, w, query)
			})}
			go func() { _ = upstream.ActivateAndServe() }()
			<-ready
			t.Cleanup(func() { _ = upstream.Shutdown() })
			other := config.UpstreamDNS{Name: "other", Protocol: "udp", Address: pc.LocalAddr().String(), Timeout: 1}
			srv.cfg.UpstreamDNS = append([]config.UpstreamDNS{other}, srv.cfg.UpstreamDNS...)
			if err := srv.RegisterUpstream(&other); err != nil {
				t.Fatal(err)
			}
			srv.cfg.QueryStrategy = strategy
			name, key := "refresh.example.", "refresh.example.:1"
			seed := func(remaining time.Duration) {
				now := time.Now()
				srv.cache.Set(key, &cache.CacheEntry{
					Query: name, QueryType: dns.TypeA, TTL: 100, HitCount: 7,
					Upstream: "test", Route: srv.routeFor(name).signature,
					CreatedAt: now.Add(remaining - 100*time.Second), ExpireTime: now.Add(remaining),
				})
			}
			seed(10 * time.Second)
			srv.preloadCacheEntries()
			srv.queryWG.Wait()
			if originalCalls.Load() != 1 || otherCalls.Load() != 0 {
				t.Fatal("refresh queried other upstreams despite a working original path")
			}
			result, err := srv.Query(name, dns.TypeA)
			if err != nil || !result.CacheHit || result.UsedUpstream != "test" || originalCalls.Load() != 1 {
				t.Fatal("cache hit did not retain the original path")
			}
			seed(-time.Second)
			invalid.Store(true)
			result, err = srv.Query(name, dns.TypeA)
			if err != nil || result.CacheHit || result.UsedUpstream != "other" ||
				originalCalls.Load() != 2 || otherCalls.Load() != 1 {
				t.Fatalf("failed original path did not switch: %#v %v", result, err)
			}
			entry, _ := srv.cache.Snapshot(key)
			if entry.Upstream != "other" || entry.HitCount != 8 {
				t.Fatal("fallback did not update the path or retain current hits")
			}
		})
	}
}
