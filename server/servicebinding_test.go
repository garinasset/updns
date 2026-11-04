package server

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func startScriptedUpstream(t *testing.T, answer func(dns.Question) []dns.RR) string {
	t.Helper()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{
		PacketConn: packetConn,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
			response := new(dns.Msg)
			response.SetReply(request)
			response.Answer = answer(request.Question[0])
			_ = w.WriteMsg(response)
		}),
	}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return packetConn.LocalAddr().String()
}

func httpsRecord(name, target string, ttl uint32, hints ...net.IP) *dns.HTTPS {
	record := &dns.HTTPS{SVCB: dns.SVCB{
		Hdr:      dns.RR_Header{Name: name, Rrtype: dns.TypeHTTPS, Class: dns.ClassINET, Ttl: ttl},
		Priority: 1, Target: target,
		Value: []dns.SVCBKeyValue{&dns.SVCBAlpn{Alpn: []string{"h3"}}},
	}}
	var v4, v6 []net.IP
	for _, hint := range hints {
		if hint.To4() != nil {
			v4 = append(v4, hint)
		} else {
			v6 = append(v6, hint)
		}
	}
	if len(v4) > 0 {
		record.Value = append(record.Value, &dns.SVCBIPv4Hint{Hint: v4})
	}
	if len(v6) > 0 {
		record.Value = append(record.Value, &dns.SVCBIPv6Hint{Hint: v6})
	}
	return record
}

// addressUpstream answers A queries with ip and HTTPS queries from https.
func addressUpstream(t *testing.T, ip string, https map[string]dns.RR) string {
	return startScriptedUpstream(t, func(q dns.Question) []dns.RR {
		switch q.Qtype {
		case dns.TypeA:
			return []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 300}, A: net.ParseIP(ip)}}
		case dns.TypeHTTPS:
			if rr := https[q.Name]; rr != nil {
				return []dns.RR{dns.Copy(rr)}
			}
		}
		return nil
	})
}

type bindingFixture struct {
	srv *Server
	nft *recordingNFTSetManager
}

// newBindingFixture builds rules Y (y.test), Z (z.test), X (x.test),
// BLOCK (block.test, nxdomain) and a fallback rule F, each with its own
// upstream answering A queries with a distinct address.
func newBindingFixture(t *testing.T, bindingLimit int, https map[string]dns.RR) *bindingFixture {
	t.Helper()
	dir := t.TempDir()
	lists := map[string]string{"y": "y.test\n", "z": "z.test\n", "x": "x.test\n", "block": "block.test\n"}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.CacheEnabled = true
	cfg.CacheSize = 64
	for name, content := range lists {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg.DomainLists = append(cfg.DomainLists, config.DomainList{Name: name, File: path})
	}
	cfg.Rules = []config.Rule{
		{ID: "Y", DomainLists: []string{"y"}, Upstreams: []string{"uy"}, Action: "ipv4+ipv6", IPv4NFTSets: []string{"y4"}},
		{ID: "Z", DomainLists: []string{"z"}, Upstreams: []string{"uz"}, Action: "ipv4+ipv6", IPv4NFTSets: []string{"z4"}},
		{ID: "X", DomainLists: []string{"x"}, Upstreams: []string{"ux"}, Action: "ipv4+ipv6", IPv4NFTSets: []string{"x4"}},
		{ID: "BLOCK", DomainLists: []string{"block"}, Action: "nxdomain"},
		{ID: "F", IgnoreDomainMatch: true, Upstreams: []string{"uf"}, Action: "ipv4+ipv6"},
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Stop() })
	if bindingLimit > 0 {
		srv.bindings = newServiceBindings(bindingLimit)
	}
	nft := &recordingNFTSetManager{added: map[string][]string{}, contents: map[string][]string{}}
	srv.nftMgr = nft
	for name, ip := range map[string]string{"uy": "198.51.100.1", "uz": "198.51.100.2", "ux": "198.51.100.3", "uf": "203.0.113.1"} {
		if err := srv.RegisterUpstream(&config.UpstreamDNS{Name: name, Address: addressUpstream(t, ip, https), Protocol: "udp", Timeout: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, list := range cfg.DomainLists {
		if err := srv.LoadDomainList(list.Name, list.File); err != nil {
			t.Fatal(err)
		}
	}
	return &bindingFixture{srv: srv, nft: nft}
}

func (f *bindingFixture) query(t *testing.T, name string, qtype uint16) *QueryResult {
	t.Helper()
	result, err := f.srv.queryFrom(name, qtype, "")
	if err != nil || result == nil {
		t.Fatalf("query %s %s failed: %v", name, dns.TypeToString[qtype], err)
	}
	return result
}

func addressOf(t *testing.T, result *QueryResult) string {
	t.Helper()
	if len(result.Answers) != 1 {
		t.Fatalf("expected one address for %s, got %v", result.Domain, result.Answers)
	}
	return result.Answers[0]
}

func TestServiceHintsFilteredAndWrittenToSets(t *testing.T) {
	f := newBindingFixture(t, 0, map[string]dns.RR{
		"www.y.test.": httpsRecord("www.y.test.", ".", 300, net.ParseIP("192.0.2.7"), net.ParseIP("2001:db8::7")),
	})
	f.srv.cfg.Rules[0].Action = "ipv4only"
	result := f.query(t, "www.y.test.", dns.TypeHTTPS)
	if len(result.Records) != 1 {
		t.Fatalf("HTTPS record not returned: %v", result.Records)
	}
	_, hint4, hint6 := filterServiceHints(result.Records[0], true, true)
	if !slices.Equal(hint4, []string{"192.0.2.7"}) || len(hint6) != 0 {
		t.Fatalf("hints = %v %v, want only the IPv4 hint", hint4, hint6)
	}
	if !f.nft.has("y4", "192.0.2.7") {
		t.Fatalf("IPv4 hint not written to y4: %v", f.nft.added)
	}
	if record := result.Records[0].(*dns.HTTPS); len(record.Value) != 2 {
		t.Fatalf("non-address parameters were not preserved: %v", record)
	}
}

func TestServiceTargetFollowsOriginRule(t *testing.T) {
	f := newBindingFixture(t, 0, map[string]dns.RR{
		"www.y.test.": httpsRecord("www.y.test.", "cdn.other.test.", 300),
	})
	// Cached under the fallback rule before any binding exists.
	if got := addressOf(t, f.query(t, "cdn.other.test.", dns.TypeA)); got != "203.0.113.1" {
		t.Fatalf("unbound target resolved to %s", got)
	}
	https := f.query(t, "www.y.test.", dns.TypeHTTPS)
	if len(https.Records) != 1 {
		t.Fatalf("HTTPS record with a bindable target was dropped: %v", https.Records)
	}
	target := f.query(t, "cdn.other.test.", dns.TypeA)
	if got := addressOf(t, target); got != "198.51.100.1" || target.MatchedRule != "Y" || target.CacheHit {
		t.Fatalf("bound target = %s via %s (cache hit %v), want Y's upstream", got, target.MatchedRule, target.CacheHit)
	}
	if !f.nft.has("y4", "198.51.100.1") {
		t.Fatalf("target address not written to y4: %v", f.nft.added)
	}
}

func TestServiceTargetBoundByMultipleRulesUsesUnionOfSets(t *testing.T) {
	f := newBindingFixture(t, 0, map[string]dns.RR{
		"www.y.test.": httpsRecord("www.y.test.", "cdn.x.test.", 300),
		"www.z.test.": httpsRecord("www.z.test.", "cdn.x.test.", 300),
	})
	f.query(t, "www.y.test.", dns.TypeHTTPS)
	f.query(t, "www.z.test.", dns.TypeHTTPS)
	target := f.query(t, "cdn.x.test.", dns.TypeA)
	if got := addressOf(t, target); got != "198.51.100.1" || target.MatchedRule != "Y" {
		t.Fatalf("target = %s via %s, want first-bound rule Y", got, target.MatchedRule)
	}
	for _, set := range []string{"y4", "z4", "x4"} {
		if !f.nft.has(set, "198.51.100.1") {
			t.Fatalf("target address missing from %s: %v", set, f.nft.added)
		}
	}
}

func TestServiceTargetRecordsDroppedWhenUnbindable(t *testing.T) {
	t.Run("blocked target", func(t *testing.T) {
		f := newBindingFixture(t, 0, map[string]dns.RR{"www.y.test.": httpsRecord("www.y.test.", "ads.block.test.", 300)})
		if result := f.query(t, "www.y.test.", dns.TypeHTTPS); len(result.Records) != 0 {
			t.Fatalf("record targeting a blocked name was returned: %v", result.Records)
		}
	})
	t.Run("table full", func(t *testing.T) {
		f := newBindingFixture(t, 1, map[string]dns.RR{
			"a.y.test.": httpsRecord("a.y.test.", "one.other.test.", 300),
			"b.y.test.": httpsRecord("b.y.test.", "two.other.test.", 300),
		})
		if result := f.query(t, "a.y.test.", dns.TypeHTTPS); len(result.Records) != 1 {
			t.Fatalf("first binding failed: %v", result.Records)
		}
		if result := f.query(t, "b.y.test.", dns.TypeHTTPS); len(result.Records) != 0 {
			t.Fatalf("record was returned although the binding table is full: %v", result.Records)
		}
		if got := addressOf(t, f.query(t, "one.other.test.", dns.TypeA)); got != "198.51.100.1" {
			t.Fatalf("existing binding was evicted: %s", got)
		}
	})
	t.Run("hop limit", func(t *testing.T) {
		f := newBindingFixture(t, 0, map[string]dns.RR{"deep.other.test.": httpsRecord("deep.other.test.", "next.other.test.", 300)})
		f.srv.bindings.bind("deep.other.test.", "Y", time.Minute, maxServiceHops, time.Now())
		if result := f.query(t, "deep.other.test.", dns.TypeHTTPS); len(result.Records) != 0 {
			t.Fatalf("record beyond the hop limit was returned: %v", result.Records)
		}
	})
}

func TestServiceTargetSameRuleKeepsRoutingAndPrecedence(t *testing.T) {
	f := newBindingFixture(t, 0, map[string]dns.RR{
		"www.y.test.": httpsRecord("www.y.test.", "cdn.y.test.", 300),
		"www.z.test.": httpsRecord("www.z.test.", "cdn.y.test.", 300),
	})
	f.query(t, "www.y.test.", dns.TypeHTTPS)
	if !f.srv.bindings.bound("cdn.y.test.", time.Now()) {
		t.Fatal("same-rule target binding was not recorded")
	}
	if got := f.srv.routeFor("cdn.y.test.").signature; got != "r:Y" {
		t.Fatalf("same-rule binding changed routing signature to %q", got)
	}
	f.query(t, "www.z.test.", dns.TypeHTTPS)
	target := f.query(t, "cdn.y.test.", dns.TypeA)
	if got := addressOf(t, target); got != "198.51.100.1" || target.MatchedRule != "Y" {
		t.Fatalf("target = %s via %s, want Y to keep precedence", got, target.MatchedRule)
	}
	for _, set := range []string{"y4", "z4"} {
		if !f.nft.has(set, "198.51.100.1") {
			t.Fatalf("target address missing from %s: %v", set, f.nft.added)
		}
	}
}

func TestServiceTargetChainCarriesAllRules(t *testing.T) {
	alias := httpsRecord("b.other.test.", "c.other.test.", 300)
	alias.Priority = 0
	alias.Value = nil
	f := newBindingFixture(t, 0, map[string]dns.RR{
		"www.y.test.":   httpsRecord("www.y.test.", "b.other.test.", 300),
		"www.z.test.":   httpsRecord("www.z.test.", "b.other.test.", 300),
		"b.other.test.": alias,
	})
	f.query(t, "www.y.test.", dns.TypeHTTPS)
	f.query(t, "www.z.test.", dns.TypeHTTPS)
	if result := f.query(t, "b.other.test.", dns.TypeHTTPS); len(result.Records) != 1 {
		t.Fatalf("AliasMode record was dropped: %v", result.Records)
	}
	ids, hop, ok := f.srv.bindings.lookup("c.other.test.", time.Now())
	if !ok || !slices.Equal(ids, []string{"Y", "Z", "F"}) || hop != 2 {
		t.Fatalf("chained binding = %v hop %d, want [Y Z F] hop 2", ids, hop)
	}
	target := f.query(t, "c.other.test.", dns.TypeA)
	if got := addressOf(t, target); got != "198.51.100.1" {
		t.Fatalf("chained target resolved to %s", got)
	}
	for _, set := range []string{"y4", "z4"} {
		if !f.nft.has(set, "198.51.100.1") {
			t.Fatalf("chained target address missing from %s: %v", set, f.nft.added)
		}
	}
}

func TestServiceBindingsLifecycle(t *testing.T) {
	now := time.Now()
	b := newServiceBindings(2)
	if added, ok := b.bind("t.", "Y", time.Minute, 3, now); !added || !ok {
		t.Fatal("first bind failed")
	}
	if added, ok := b.bind("t.", "Z", time.Second, 1, now); !added || !ok {
		t.Fatal("second rule not added")
	}
	if added, _ := b.bind("t.", "Y", time.Hour, 5, now); added {
		t.Fatal("refresh reported a new rule")
	}
	ids, hop, ok := b.lookup("t.", now)
	if !ok || !slices.Equal(ids, []string{"Y", "Z"}) || hop != 1 {
		t.Fatalf("lookup = %v hop %d", ids, hop)
	}
	// The short TTL is raised to the minimum binding lifetime.
	if ids, _, _ := b.lookup("t.", now.Add(minServiceBindingTTL-time.Second)); len(ids) != 2 {
		t.Fatalf("binding expired before the minimum lifetime: %v", ids)
	}
	if ids, _, _ := b.lookup("t.", now.Add(2*time.Minute)); !slices.Equal(ids, []string{"Y"}) {
		t.Fatalf("expired rule not pruned: %v", ids)
	}
	b.bind("u.", "Y", time.Minute, 1, now)
	if _, ok := b.bind("v.", "Y", time.Minute, 1, now); ok {
		t.Fatal("bind succeeded on a full table")
	}
	if _, ok := b.bind("v.", "Y", time.Minute, 1, now.Add(2*time.Hour)); !ok {
		t.Fatal("expired bindings were not reclaimed")
	}
}
