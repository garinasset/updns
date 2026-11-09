package server

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/cache"
	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestBlockingRulesBypassExistingCacheAndPreload(t *testing.T) {
	for _, action := range []string{"nxdomain", "zeroip"} {
		t.Run(action, func(t *testing.T) {
			var calls atomic.Int32
			srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
				calls.Add(1)
				writeFlightAnswer(t, w, query)
			})
			srv.cfg.Rules = []config.Rule{{ID: "blocked", IgnoreDomainMatch: true, Action: action}}
			for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeANY, dns.TypeTXT, dns.TypeHTTPS, dns.TypePTR} {
				name := "blocked.example."
				key := fmt.Sprintf("%s:%d", name, qtype)
				seed := func() {
					srv.cache.Set(key, &cache.CacheEntry{
						Query: name, QueryType: qtype, Route: "r:blocked", TTL: 60,
						ExpireTime: time.Now().Add(time.Second), Rcode: dns.RcodeServerFailure,
					})
				}
				seed()
				for pass := 0; pass < 2; pass++ {
					result, err := srv.Query(name, qtype)
					if err != nil || result.CacheHit || result.PrefetchWait || result.TTL != localAnswerTTL || result.Action != action {
						t.Fatalf("type %d: result=%#v error=%v", qtype, result, err)
					}
					if srv.GetCacheSize() != 0 || len(srv.flights) != 0 {
						t.Fatal("blocking query used cache or shared flights")
					}
				}
				seed()
				srv.preloadCacheEntries()
				if srv.GetCacheSize() != 0 || srv.preloading != 0 || len(srv.flights) != 0 {
					t.Fatal("blocking rule entered preload")
				}
			}
			if calls.Load() != 0 {
				t.Fatal("blocking rule contacted upstream")
			}
		})
	}
}

func TestBlockingRuleWireResponses(t *testing.T) {
	for _, action := range []string{"nxdomain", "zeroip"} {
		for _, network := range []string{"udp", "tcp"} {
			t.Run(action+"/"+network, func(t *testing.T) {
				cfg := config.DefaultConfig()
				cfg.Rules = []config.Rule{{ID: "blocked", IgnoreDomainMatch: true, Action: action}}
				srv, err := NewServer(cfg)
				if err != nil {
					t.Fatal(err)
				}
				defer srv.Stop()
				listener := &dns.Server{Handler: dns.HandlerFunc(srv.handleQuery)}
				var addr string
				if network == "udp" {
					listener.PacketConn, err = net.ListenPacket("udp", "127.0.0.1:0")
					if err == nil {
						addr = listener.PacketConn.LocalAddr().String()
					}
				} else {
					listener.Listener, err = net.Listen("tcp", "127.0.0.1:0")
					if err == nil {
						addr = listener.Listener.Addr().String()
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				ready := make(chan struct{})
				listener.NotifyStartedFunc = func() { close(ready) }
				go func() {
					if err := listener.ActivateAndServe(); err != nil {
						t.Errorf("serve downstream: %v", err)
					}
				}()
				<-ready
				defer listener.Shutdown()
				for _, qtype := range []uint16{dns.TypeA, dns.TypeAAAA, dns.TypeANY, dns.TypeTXT} {
					for pass := 0; pass < 2; pass++ {
						query := new(dns.Msg)
						query.SetQuestion("blocked.example.", qtype)
						response, _, err := (&dns.Client{Net: network, Timeout: time.Second}).Exchange(query, addr)
						if err != nil {
							t.Fatal(err)
						}
						wantCode, wantCount := dns.RcodeSuccess, 0
						if action == "nxdomain" {
							wantCode = dns.RcodeNameError
						} else {
							switch qtype {
							case dns.TypeA, dns.TypeAAAA:
								wantCount = 1
							case dns.TypeANY:
								wantCount = 2
							}
						}
						if response.Rcode != wantCode || len(response.Answer) != wantCount || len(response.Ns) != 0 {
							t.Fatalf("wire response changed: %v", response)
						}
						for _, rr := range response.Answer {
							if rr.Header().Ttl != 60 {
								t.Fatalf("TTL changed: %v", rr)
							}
						}
						if srv.GetCacheSize() != 0 {
							t.Fatal("wire query populated cache")
						}
					}
				}
			})
		}
	}
}
