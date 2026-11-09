package server

import (
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestQueryRechecksBindingsDuringResolution(t *testing.T) {
	for _, change := range []string{"new-https-binding", "expired-binding", "last-binding-pruned"} {
		t.Run(change, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			wait := func() {
				close(started)
				<-release
			}
			srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
				if change == "new-https-binding" {
					wait()
				}
				writeFlightAnswer(t, w, query)
			})
			// Exercise the real HTTPS binding path without launching target prefetch.
			srv.targetPrefetch = make(chan struct{})
			srv.local = nil
			origin := startScriptedUpstream(t, func(q dns.Question) []dns.RR {
				if q.Qtype == dns.TypeHTTPS {
					return []dns.RR{httpsRecord(q.Name, "target.test.", 300)}
				}
				if change != "new-https-binding" {
					wait()
				}
				return []dns.RR{&dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300},
					A:   net.ParseIP("198.51.100.10"),
				}}
			})
			if err := srv.RegisterUpstream(&config.UpstreamDNS{Name: "bound", Protocol: "udp", Address: origin, Timeout: 2}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "origin")
			if err := os.WriteFile(path, []byte("origin.test\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := srv.LoadDomainList("origin", path); err != nil {
				t.Fatal(err)
			}
			srv.cfg.Rules = []config.Rule{
				{ID: "video", DomainLists: []string{"origin"}, Action: "ipv4only", Upstreams: []string{"bound"}, IPv4NFTSets: []string{"video4"}},
				{ID: "default", IgnoreDomainMatch: true, Action: "ipv4+ipv6", Upstreams: []string{"test"}},
			}
			manager := &recordingNFTSetManager{added: make(map[string][]string), contents: make(map[string][]string)}
			srv.nftMgr = manager
			if change != "new-https-binding" {
				srv.bindings.bind("target.test.", "video", time.Minute, 1, time.Now())
			}
			type outcome struct {
				result *QueryResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := srv.Query("target.test.", dns.TypeA)
				done <- outcome{result, err}
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("initial upstream query did not start")
			}
			if change == "new-https-binding" {
				result, err := srv.Query("origin.test.", dns.TypeHTTPS)
				if err != nil || len(result.Records) != 1 || !srv.bindings.bound("target.test.", time.Now()) {
					t.Fatalf("HTTPS binding failed: %#v %v", result, err)
				}
			} else {
				srv.bindings.mu.Lock()
				srv.bindings.entries["target.test."].rules[0].expire = time.Now().Add(-time.Second)
				srv.bindings.mu.Unlock()
				if change == "last-binding-pruned" {
					srv.bindings.lookup("target.test.", time.Now())
					if srv.bindings.size.Load() != 0 {
						t.Fatal("last binding was not pruned")
					}
				}
			}
			unblock()
			select {
			case out := <-done:
				if out.err != nil || out.result == nil {
					t.Fatalf("query failed: %v", out.err)
				}
				wantRule, wantIP, wantAction := "default", "192.0.2.1", "ipv4+ipv6"
				if change == "new-https-binding" {
					wantRule, wantIP, wantAction = "video", "198.51.100.10", "ipv4only"
					if !manager.has("video4", wantIP) {
						t.Fatal("new binding did not populate video nftset")
					}
				}
				if out.result.MatchedRule != wantRule || addressOf(t, out.result) != wantIP || out.result.Action != wantAction {
					t.Fatalf("returned stale routing: %#v", out.result)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("query did not finish")
			}
		})
	}
}

func TestAsyncQueryRefreshesBindingsBeforeResolution(t *testing.T) {
	for _, preload := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreground", true: "preload"}[preload], func(t *testing.T) {
			f := newBindingFixture(t, 0, nil)
			f.srv.local = nil
			name := "cdn.other.test."
			own := f.srv.findMatchingRule(name)
			initial := f.srv.routeForRule(name, own)
			// A binding appears after scheduling chose a route but before resolution.
			f.srv.bindings.bind(name, "Y", time.Minute, 1, time.Now())
			f.srv.queryMu.Lock()
			flight := f.srv.startQueryLocked(name+":1", name, dns.TypeA, "", own, initial.signature, nil, 0, preload)
			f.srv.queryMu.Unlock()
			select {
			case <-flight.done:
			case <-time.After(3 * time.Second):
				t.Fatal("query did not finish")
			}
			if flight.err != nil || flight.result == nil || flight.result.MatchedRule != "Y" ||
				addressOf(t, flight.result) != "198.51.100.1" || !f.nft.has("y4", "198.51.100.1") {
				t.Fatalf("asynchronous resolution used stale binding: %#v %v", flight.result, flight.err)
			}
		})
	}
}
