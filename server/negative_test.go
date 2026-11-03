package server

import (
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestNegativeSOATTLOnWireAndCache(t *testing.T) {
	for _, code := range []int{dns.RcodeSuccess, dns.RcodeNameError} {
		t.Run(dns.RcodeToString[code], func(t *testing.T) {
			var calls atomic.Int32
			srv := newNegativeTestServer(t, &calls, func(response *dns.Msg) {
				response.Rcode = code
			})
			pc, err := net.ListenPacket("udp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ready := make(chan struct{})
			listener := &dns.Server{
				PacketConn: pc, Handler: dns.HandlerFunc(srv.handleQuery),
				NotifyStartedFunc: func() { close(ready) },
			}
			go func() {
				if err := listener.ActivateAndServe(); err != nil {
					t.Errorf("serve downstream DNS: %v", err)
				}
			}()
			<-ready
			defer listener.Shutdown()
			query := new(dns.Msg)
			query.SetQuestion("negative.example.", dns.TypeAAAA)
			exchange := func() *dns.Msg {
				t.Helper()
				client := &dns.Client{Timeout: time.Second}
				response, _, err := client.Exchange(query, pc.LocalAddr().String())
				if err != nil {
					t.Fatal(err)
				}
				if response.Rcode != code || len(response.Ns) != 1 {
					t.Fatalf("unexpected negative response: %v", response)
				}
				return response
			}
			first := exchange()
			soa := first.Ns[0].(*dns.SOA)
			if soa.Hdr.Ttl != 60 || soa.Minttl != 60 {
				t.Fatalf("first wire response not normalized: %v", soa)
			}
			entries := srv.cache.GetAllEntries()
			if len(entries) != 1 || entries[0].Authority[0].Header().Ttl != 60 {
				t.Fatalf("cached SOA not normalized: %#v", entries)
			}
			entry := entries[0]
			entry.CreatedAt = time.Now().Add(-20 * time.Second)
			entry.ExpireTime = entry.CreatedAt.Add(60 * time.Second)
			srv.cache.Set("negative.example.:28", entry)
			for i := 0; i < 2; i++ {
				cached := exchange().Ns[0].(*dns.SOA)
				if cached.Hdr.Ttl > 40 || cached.Hdr.Ttl < 39 || cached.Minttl != 60 {
					t.Fatalf("cached wire TTL reset or not aged: %v", cached)
				}
			}
			stored := srv.cache.GetAllEntries()[0]
			if calls.Load() != 1 || stored.Authority[0].Header().Ttl != 60 ||
				!stored.CreatedAt.Equal(entry.CreatedAt) || !stored.ExpireTime.Equal(entry.ExpireTime) {
				t.Fatal("cache read re-queried, mutated the stored SOA or reset expiration")
			}
		})
	}
}

func TestUncacheableNegativePreloadRetainsLiveNXDOMAIN(t *testing.T) {
	var calls atomic.Int32
	var fail atomic.Bool
	srv := newNegativeTestServer(t, &calls, func(response *dns.Msg) {
		response.Rcode = dns.RcodeNameError
		if fail.Load() {
			response.Ns = nil
		}
	})
	if _, err := srv.Query("negative.example.", dns.TypeAAAA); err != nil {
		t.Fatal(err)
	}
	entry := srv.cache.GetAllEntries()[0]
	entry.CreatedAt = time.Now().Add(-50 * time.Second)
	entry.ExpireTime = entry.CreatedAt.Add(60 * time.Second)
	srv.cache.Set("negative.example.:28", entry)
	fail.Store(true)
	srv.preloadCacheEntries()
	result, err := srv.Query("negative.example.", dns.TypeAAAA)
	if err != nil || result == nil || !result.CacheHit || result.Rcode != dns.RcodeNameError {
		t.Fatalf("failed refresh invalidated cached NXDOMAIN: result=%#v error=%v", result, err)
	}
	stored := srv.cache.GetAllEntries()[0]
	if calls.Load() != 2 || !stored.CreatedAt.Equal(entry.CreatedAt) ||
		!stored.ExpireTime.Equal(entry.ExpireTime) || result.Authority[0].Header().Ttl > 10 {
		t.Fatal("failed refresh extended or replaced cached NXDOMAIN")
	}
}

func TestNegativeFallbackOnWireAndNoCache(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		for _, code := range []int{dns.RcodeSuccess, dns.RcodeNameError} {
			for _, chain := range []string{"direct", "completed", "inline"} {
				if code == dns.RcodeSuccess && chain == "inline" {
					continue
				}
				t.Run(fmt.Sprintf("concurrent=%t/%s/%s", concurrent, dns.RcodeToString[code], chain), func(t *testing.T) {
					var calls atomic.Int32
					srv := newNegativeTestServer(t, &calls, func(response *dns.Msg) {
						response.Ns = nil
						response.Rcode = code
						if chain != "direct" && response.Question[0].Name == "negative.example." {
							response.Answer = []dns.RR{&dns.CNAME{
								Hdr:    dns.RR_Header{Name: "negative.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300},
								Target: "terminal.example.",
							}}
							if chain == "completed" {
								response.Rcode = dns.RcodeSuccess
							}
						}
					})
					srv.cfg.ConcurrentQuery = concurrent
					pc, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						t.Fatal(err)
					}
					ready := make(chan struct{})
					listener := &dns.Server{
						PacketConn: pc, Handler: dns.HandlerFunc(srv.handleQuery),
						NotifyStartedFunc: func() { close(ready) },
					}
					go func() {
						if err := listener.ActivateAndServe(); err != nil {
							t.Errorf("serve downstream DNS: %v", err)
						}
					}()
					<-ready
					defer listener.Shutdown()
					client := &dns.Client{Timeout: time.Second}
					for _, queryType := range []uint16{dns.TypeA, dns.TypeAAAA} {
						query := new(dns.Msg)
						query.SetQuestion("negative.example.", queryType)
						for pass := 0; pass < 2; pass++ {
							response, _, err := client.Exchange(query, pc.LocalAddr().String())
							if err != nil {
								t.Fatal(err)
							}
							wantAnswers := 0
							if chain != "direct" {
								wantAnswers = 1
							}
							if response.Rcode != code || len(response.Ns) != 0 || len(response.Answer) != wantAnswers {
								t.Fatalf("wire fallback lost RCODE/CNAME or synthesized authority: %v", response)
							}
						}
					}
					wantCalls := int32(4)
					if chain == "completed" {
						wantCalls *= 2
					}
					if calls.Load() != wantCalls || srv.cache.Size() != 0 {
						t.Fatalf("fallback was cached: calls=%d want=%d cache=%d", calls.Load(), wantCalls, srv.cache.Size())
					}
				})
			}
		}
	}
}

func newNegativeTestServer(t *testing.T, calls *atomic.Int32, change func(*dns.Msg)) *Server {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	upstream := &dns.Server{
		PacketConn:        pc,
		NotifyStartedFunc: func() { close(ready) },
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
			calls.Add(1)
			response := new(dns.Msg)
			response.SetReply(query)
			response.Ns = []dns.RR{&dns.SOA{
				Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 600},
				Ns:  "ns.example.", Mbox: "hostmaster.example.", Minttl: 60,
			}}
			change(response)
			if err := w.WriteMsg(response); err != nil {
				t.Errorf("write negative upstream response: %v", err)
			}
		}),
	}
	go func() {
		if err := upstream.ActivateAndServe(); err != nil {
			t.Errorf("serve negative upstream: %v", err)
		}
	}()
	<-ready
	t.Cleanup(func() { _ = upstream.Shutdown() })
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.ConcurrentQuery = true
	cfg.UpstreamDNS = []config.UpstreamDNS{{Name: "test", Protocol: "udp", Address: pc.LocalAddr().String(), Timeout: 1}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	if err := srv.RegisterUpstream(&cfg.UpstreamDNS[0]); err != nil {
		t.Fatal(err)
	}
	return srv
}
