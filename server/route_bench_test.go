package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func BenchmarkQueryRouting(b *testing.B) {
	for _, ruleCount := range []int{1, 32} {
		for _, path := range []string{"cache", "nxdomain", "zeroip", "local", "upstream", "bound-cache", "bound-upstream"} {
			b.Run(fmt.Sprintf("%d-rules/%s", ruleCount, path), func(b *testing.B) {
				cfg := config.DefaultConfig()
				cfg.UseLocalResolv = false
				for i := 1; i < ruleCount; i++ {
					cfg.Rules = append(cfg.Rules, config.Rule{ID: fmt.Sprint(i), DomainLists: []string{"miss"}})
				}
				action := "ipv4+ipv6"
				if path == "nxdomain" || path == "zeroip" {
					action = path
				}
				cfg.Rules = append(cfg.Rules, config.Rule{ID: "match", DomainLists: []string{"match"}, Action: action, Upstreams: []string{"test"}})
				srv, err := NewServer(cfg)
				if err != nil {
					b.Fatal(err)
				}
				defer srv.Stop()
				dir := b.TempDir()
				for name, content := range map[string]string{"match": "matched.test\n", "miss": "absent.test\n"} {
					path := filepath.Join(dir, name)
					if err := os.WriteFile(path, []byte(content), 0600); err != nil {
						b.Fatal(err)
					}
					if err := srv.LoadDomainList(name, path); err != nil {
						b.Fatal(err)
					}
				}
				if path == "local" {
					hosts := filepath.Join(dir, "hosts")
					if err := os.WriteFile(hosts, []byte("192.0.2.1 matched.test\n"), 0600); err != nil {
						b.Fatal(err)
					}
					srv.local = newLocalDNS(filepath.Join(dir, "missing-config"))
					srv.local.configured = true
					srv.local.settings = config.LocalDNSConfig{HostsFiles: []string{hosts}}
				} else {
					srv.local = nil
				}
				if path == "bound-cache" || path == "bound-upstream" {
					srv.cfg.Rules = append(srv.cfg.Rules, config.Rule{ID: "bound", DomainLists: []string{"miss"}, Action: "ipv4+ipv6", Upstreams: []string{"test"}})
					srv.bindings.bind("matched.test.", "bound", time.Hour, 1, time.Now())
				}
				if path == "cache" || path == "upstream" || path == "bound-cache" || path == "bound-upstream" {
					pc, err := net.ListenPacket("udp", "127.0.0.1:0")
					if err != nil {
						b.Fatal(err)
					}
					ready := make(chan struct{})
					upstream := &dns.Server{
						PacketConn: pc, NotifyStartedFunc: func() { close(ready) },
						Handler: dns.HandlerFunc(func(w dns.ResponseWriter, query *dns.Msg) {
							response := new(dns.Msg)
							response.SetReply(query)
							response.Answer = []dns.RR{&dns.A{
								Hdr: dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 3600},
								A:   net.IPv4(192, 0, 2, 1),
							}}
							if err := w.WriteMsg(response); err != nil {
								b.Errorf("upstream: %v", err)
							}
						}),
					}
					go func() {
						if err := upstream.ActivateAndServe(); err != nil {
							b.Errorf("upstream: %v", err)
						}
					}()
					<-ready
					defer upstream.Shutdown()
					if err := srv.RegisterUpstream(&config.UpstreamDNS{Name: "test", Protocol: "udp", Address: pc.LocalAddr().String(), Timeout: 1}); err != nil {
						b.Fatal(err)
					}
					if path == "upstream" || path == "bound-upstream" {
						srv.cache = nil
					}
				}
				first, err := srv.Query("matched.test.", dns.TypeA)
				if err != nil || first == nil {
					b.Fatalf("warm query: %v", err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					result, err := srv.Query("matched.test.", dns.TypeA)
					if err != nil || result == nil {
						b.Fatalf("query: %v", err)
					}
				}
				b.StopTimer()
			})
		}
	}
}
