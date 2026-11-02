package server

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/cache"
	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

type recordingNFTSetManager struct {
	added    map[string][]string
	contents map[string][]string
}

func (m *recordingNFTSetManager) AddIPsToSet(ips []string, setName string) error {
	m.added[setName] = append(m.added[setName], ips...)
	m.contents[setName] = append(m.contents[setName], ips...)
	return nil
}

func (m *recordingNFTSetManager) FlushSet(string) error {
	return nil
}

func (m *recordingNFTSetManager) GetSetContents(setName string) ([]string, error) {
	return m.contents[setName], nil
}

func TestResolveQueryPreservesAddressFamilyAndTTL(t *testing.T) {
	upstreamAddress := startTestUpstream(t)
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.CacheEnabled = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{
		Name:     "test",
		Address:  upstreamAddress,
		Protocol: "udp",
		Timeout:  1,
	}}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.RegisterUpstream(&cfg.UpstreamDNS[0]); err != nil {
		t.Fatal(err)
	}

	a := srv.resolveQuery("example.com.", dns.TypeA)
	if a == nil || len(a.Records) != 1 {
		t.Fatalf("A query returned unexpected result: %#v", a)
	}
	if a.Records[0].Header().Rrtype != dns.TypeA || a.Records[0].Header().Ttl != 77 {
		t.Fatalf("A response did not preserve type/TTL: %v", a.Records[0])
	}

	aaaa := srv.resolveQuery("example.com.", dns.TypeAAAA)
	if aaaa == nil || len(aaaa.Records) != 1 {
		t.Fatalf("AAAA query returned unexpected result: %#v", aaaa)
	}
	if aaaa.Records[0].Header().Rrtype != dns.TypeAAAA || aaaa.Records[0].Header().Ttl != 42 {
		t.Fatalf("AAAA response did not preserve type/TTL: %v", aaaa.Records[0])
	}
}

func TestResolveQueryAppliesRuleAddressFilter(t *testing.T) {
	upstreamAddress := startTestUpstream(t)
	listPath := filepath.Join(t.TempDir(), "ipv4.txt")
	if err := os.WriteFile(listPath, []byte("example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.CacheEnabled = false
	cfg.DomainLists = []config.DomainList{{Name: "ipv4", File: listPath}}
	cfg.Rules = []config.Rule{{
		ID:          "v4-only",
		DomainLists: []string{"ipv4"},
		Upstreams:   []string{"test"},
		Action:      "ipv4only",
	}}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.RegisterUpstream(&config.UpstreamDNS{
		Name: "test", Address: upstreamAddress, Protocol: "udp", Timeout: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := srv.LoadDomainList("ipv4", listPath); err != nil {
		t.Fatal(err)
	}

	result := srv.resolveQuery("example.com.", dns.TypeAAAA)
	if result == nil || result.MatchedRule != "v4-only" {
		t.Fatalf("expected v4-only rule to match, got %#v", result)
	}
	if len(result.Answers) != 0 || len(result.Records) != 0 {
		t.Fatalf("IPv4-only rule returned an AAAA answer: %#v", result.Records)
	}
}

func TestFindMatchingRuleUsesConfiguredOrder(t *testing.T) {
	listPath := filepath.Join(t.TempDir(), "domains.txt")
	if err := os.WriteFile(listPath, []byte("example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.DomainLists = []config.DomainList{{Name: "example", File: listPath}}
	cfg.Rules = []config.Rule{
		{ID: "first", DomainLists: []string{"example"}},
		{ID: "second", DomainLists: []string{"example"}},
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.LoadDomainList("example", listPath); err != nil {
		t.Fatal(err)
	}

	if rule := srv.findMatchingRule("example.com."); rule == nil || rule.ID != "first" {
		t.Fatalf("findMatchingRule() = %#v, want first configured rule", rule)
	}
}

func TestFindMatchingRuleUsesIgnoreDomainMatchAsLastFallback(t *testing.T) {
	listPath := filepath.Join(t.TempDir(), "domains.txt")
	if err := os.WriteFile(listPath, []byte("example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.DomainLists = []config.DomainList{{Name: "example-list", File: listPath}}
	cfg.Rules = []config.Rule{
		{ID: "fallback", IgnoreDomainMatch: true},
		{ID: "matching", DomainLists: []string{"example-list"}},
	}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.LoadDomainList("example-list", listPath); err != nil {
		t.Fatal(err)
	}

	if rule := srv.findMatchingRule("example.com."); rule == nil || rule.ID != "matching" {
		t.Fatalf("matching list rule should precede fallback regardless of row order, got %#v", rule)
	}
	if rule := srv.findMatchingRule("other.example."); rule == nil || rule.ID != "fallback" {
		t.Fatalf("unmatched query should use ignore-domain-match rule, got %#v", rule)
	}
}

func TestFindMatchingRuleWithoutRulesKeepsDefaultBehavior(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	if rule := srv.findMatchingRule("example.com."); rule != nil {
		t.Fatalf("expected no rule so default upstream behavior is used, got %#v", rule)
	}
}

func TestUpstreamEndpointHostSupportsDNSProtocols(t *testing.T) {
	tests := []struct {
		name     string
		upstream config.UpstreamDNS
		want     string
	}{
		{name: "IPv4 with port", upstream: config.UpstreamDNS{Address: "192.0.2.53:5353", Protocol: "udp"}, want: "192.0.2.53"},
		{name: "IPv6 literal", upstream: config.UpstreamDNS{Address: "[2001:db8::53]:853", Protocol: "dot"}, want: "2001:db8::53"},
		{name: "hostname with port", upstream: config.UpstreamDNS{Address: "resolver.example:5353", Protocol: "tcp"}, want: "resolver.example"},
		{name: "DoH URL", upstream: config.UpstreamDNS{Address: "https://resolver.example:8443/dns-query", Protocol: "doh"}, want: "resolver.example"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := upstreamEndpointHost(test.upstream)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("upstreamEndpointHost() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRefreshUpstreamNFTSetsAddsBothAddressFamilies(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.Rules = []config.Rule{{
		ID:             "route-upstream",
		AddUpstreamIPs: true,
		Upstreams:      []string{"doh", "literal"},
		IPv4NFTSets:    []string{"inet/fw4/ipv4"},
		IPv6NFTSets:    []string{"inet/fw4/ipv6"},
	}}
	cfg.UpstreamDNS = []config.UpstreamDNS{
		{Name: "doh", Address: "https://resolver.example/dns-query", Protocol: "doh"},
		{Name: "literal", Address: "[2001:db8::10]:853", Protocol: "dot"},
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	manager := &recordingNFTSetManager{
		added:    make(map[string][]string),
		contents: make(map[string][]string),
	}
	srv.nftMgr = manager
	srv.lookupIPs = func(_ context.Context, host string) ([]net.IP, error) {
		if host != "resolver.example" {
			t.Fatalf("lookup host = %q, want resolver.example", host)
		}
		return []net.IP{net.ParseIP("192.0.2.7"), net.ParseIP("2001:db8::7")}, nil
	}

	srv.refreshUpstreamNFTSets()
	srv.refreshUpstreamNFTSets()

	assertContains := func(setName, ip string) {
		t.Helper()
		for _, got := range manager.added[setName] {
			if got == ip {
				return
			}
		}
		t.Errorf("nftset %s did not receive %s: %#v", setName, ip, manager.added[setName])
	}
	assertContains("inet/fw4/ipv4", "192.0.2.7")
	assertContains("inet/fw4/ipv6", "2001:db8::7")
	assertContains("inet/fw4/ipv6", "2001:db8::10")
	if got := manager.added["inet/fw4/ipv4"]; len(got) != 1 {
		t.Errorf("unexpected IPv4 nftset elements: %#v", got)
	}
}

func TestQueryReturnsAnswersAndReportsCacheHit(t *testing.T) {
	upstreamAddress := startTestUpstream(t)
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{
		Name:     "test",
		Address:  upstreamAddress,
		Protocol: "udp",
		Timeout:  1,
	}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.RegisterUpstream(&cfg.UpstreamDNS[0]); err != nil {
		t.Fatal(err)
	}

	first, err := srv.Query("example.com", dns.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || len(first.Answers) != 1 || first.Answers[0] != "192.0.2.1" || first.UsedUpstream != "test" {
		t.Fatalf("unexpected first query result: %#v", first)
	}
	second, err := srv.Query("example.com", dns.TypeA)
	if err != nil {
		t.Fatal(err)
	}
	if !second.CacheHit || len(second.Answers) != 1 || second.Answers[0] != first.Answers[0] || second.UsedUpstream != "test" {
		t.Fatalf("unexpected cached query result: %#v", second)
	}
}

func TestControlSocketQueryUsesRunningServiceCache(t *testing.T) {
	upstreamAddress := startTestUpstream(t)
	cfg := config.DefaultConfig()
	cfg.Port = 0
	cfg.UseLocalResolv = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{
		Name:     "test",
		Address:  upstreamAddress,
		Protocol: "udp",
		Timeout:  1,
	}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "updns-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("remove temporary socket directory: %v", err)
		}
	})
	srv.controlSocketPath = filepath.Join(socketDir, "updns.sock")
	defer srv.Stop()
	if err := srv.RegisterUpstream(&cfg.UpstreamDNS[0]); err != nil {
		t.Fatal(err)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err := listenControlSocket(srv.controlSocketPath); err == nil {
		t.Fatal("opening an active control socket succeeded")
	}

	first, err := queryControlSocket(srv.controlSocketPath, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].CacheHit || first[1].CacheHit {
		t.Fatalf("first control query did not miss both cache entries: %#v", first)
	}

	second, err := queryControlSocket(srv.controlSocketPath, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 2 || !second[0].CacheHit || !second[1].CacheHit {
		t.Fatalf("second control query did not use both service cache entries: %#v", second)
	}

	if _, err := srv.Query("mixed.example", dns.TypeA); err != nil {
		t.Fatal(err)
	}
	mixed, err := queryControlSocket(srv.controlSocketPath, "mixed.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) != 2 || !mixed[0].CacheHit || mixed[1].CacheHit {
		t.Fatalf("control query did not preserve mixed A/AAAA cache paths: %#v", mixed)
	}
}

func TestStopRemovesCacheStatusFile(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Port = 0
	cfg.UseLocalResolv = false
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	socketDir, err := os.MkdirTemp("/tmp", "updns-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(socketDir); err != nil {
			t.Errorf("remove temporary socket directory: %v", err)
		}
	})
	srv.controlSocketPath = filepath.Join(socketDir, "updns.sock")
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(cacheStatusPath); err != nil {
		t.Fatalf("cache status file was not created: %v", err)
	}
	if err := srv.Stop(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cacheStatusPath); !os.IsNotExist(err) {
		t.Fatalf("cache status file still exists after stopping service: %v", err)
	}
}

func TestFormatQueryLogIncludesCacheHit(t *testing.T) {
	tests := []struct {
		name     string
		cacheHit bool
		action   string
		want     string
	}{
		{name: "upstream query", want: "Query: example.com. A (Upstream Query)"},
		{name: "cache hit", cacheHit: true, want: "Query: example.com. A (Cache Hit)"},
		{name: "NXDOMAIN rule", action: "nxdomain", want: "Query: example.com. A (NXDOMAIN RULE)"},
		{name: "Zero-IP rule", action: "zeroip", want: "Query: example.com. A (ZERO-IP RULE)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatQueryLog("example.com.", dns.TypeA, tt.cacheHit, tt.action); got != tt.want {
				t.Fatalf("formatQueryLog() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDNSQueryLogIncludesClientAndECS(t *testing.T) {
	got := formatDNSQueryLog("example.com.", dns.TypeA, false, "", "192.168.1.1", "192.168.1.0/24")
	want := "Query: example.com. A (Upstream Query) client=192.168.1.1 ecs=192.168.1.0/24"
	if got != want {
		t.Fatalf("formatDNSQueryLog() = %q, want %q", got, want)
	}

	got = formatDNSQueryLog("example.com.", dns.TypeA, true, "", "192.168.1.1", "")
	want = "Query: example.com. A (Cache Hit) client=192.168.1.1"
	if got != want {
		t.Fatalf("formatDNSQueryLog() without ECS = %q, want %q", got, want)
	}
}

func TestClientSubnet(t *testing.T) {
	tests := []struct {
		name    string
		family  uint16
		mask    uint8
		address net.IP
		want    string
		wire    bool
	}{
		{name: "IPv4 subnet", family: 1, mask: 24, address: net.ParseIP("192.168.1.23"), want: "192.168.1.0/24", wire: true},
		{name: "IPv6 subnet", family: 2, mask: 96, address: net.ParseIP("2001:db8:1234:5678::1"), want: "2001:db8:1234:5678::/96", wire: true},
		{name: "IPv4 zero prefix", family: 1, mask: 0, address: net.IPv4zero, want: "0.0.0.0/0", wire: true},
		{name: "invalid prefix", family: 1, mask: 33, address: net.ParseIP("192.168.1.23")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			message := new(dns.Msg)
			message.SetEdns0(1232, false)
			message.IsEdns0().Option = append(message.IsEdns0().Option, &dns.EDNS0_SUBNET{
				Code:          dns.EDNS0SUBNET,
				Family:        tt.family,
				SourceNetmask: tt.mask,
				Address:       tt.address,
			})
			if tt.wire {
				packed, err := message.Pack()
				if err != nil {
					t.Fatal(err)
				}
				message = new(dns.Msg)
				if err := message.Unpack(packed); err != nil {
					t.Fatal(err)
				}
			}
			if got := clientSubnet(message); got != tt.want {
				t.Fatalf("clientSubnet() = %q, want %q", got, tt.want)
			}
		})
	}

	if got := clientSubnet(new(dns.Msg)); got != "" {
		t.Fatalf("clientSubnet() without EDNS = %q, want empty", got)
	}
}

func TestPreloadRefreshesNearExpiryCacheEntry(t *testing.T) {
	upstreamAddress := startTestUpstream(t)
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{
		Name:     "test",
		Address:  upstreamAddress,
		Protocol: "udp",
		Timeout:  1,
	}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.RegisterUpstream(&cfg.UpstreamDNS[0]); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	srv.cache.Set("example.com.:1", &cache.CacheEntry{
		Query:      "example.com.",
		QueryType:  dns.TypeA,
		Answers:    []dns.RR{&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 8}, A: net.IPv4(198, 51, 100, 1)}},
		TTL:        8,
		CreatedAt:  now.Add(-6 * time.Second),
		ExpireTime: now.Add(time.Second),
	})

	srv.preloadCacheEntries()

	entry, found := srv.cache.Get("example.com.:1")
	if !found {
		t.Fatal("expected refreshed cache entry")
	}
	answer, ok := entry.Answers[0].(*dns.A)
	if !ok || !answer.A.Equal(net.IPv4(192, 0, 2, 1)) {
		t.Fatalf("expected refreshed A record, got %#v", entry.Answers)
	}
	if !entry.CreatedAt.After(now) {
		t.Fatalf("expected refresh timestamp after %v, got %v", now, entry.CreatedAt)
	}
}

func TestStartReturnsListenErrorsAndReleasesPartialBinds(t *testing.T) {
	tcpListener, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcpListener.Close()

	cfg := config.DefaultConfig()
	cfg.Port = tcpListener.Addr().(*net.TCPAddr).Port
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	if err := srv.Start(); err == nil {
		t.Fatal("expected Start to report the occupied TCP port")
	}
	udpConn, err := net.ListenPacket("udp", fmt.Sprintf(":%d", cfg.Port))
	if err != nil {
		t.Fatalf("Start did not release its UDP socket after TCP bind failed: %v", err)
	}
	udpConn.Close()
}

func TestResolveQueryNXDOMAINRuleDoesNotQueryUpstream(t *testing.T) {
	listPath := filepath.Join(t.TempDir(), "blocked.txt")
	if err := os.WriteFile(listPath, []byte("blocked.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.CacheEnabled = false
	cfg.DomainLists = []config.DomainList{{Name: "blocked", File: listPath}}
	cfg.Rules = []config.Rule{{
		ID:          "blocked",
		DomainLists: []string{"blocked"},
		Action:      "nxdomain",
	}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.LoadDomainList("blocked", listPath); err != nil {
		t.Fatal(err)
	}

	result := srv.resolveQuery("blocked.example.", dns.TypeA)
	if result == nil || result.Rcode != dns.RcodeNameError || result.MatchedRule != "blocked" {
		t.Fatalf("unexpected NXDOMAIN result: %#v", result)
	}
}

func TestQueryZeroIPRuleCachesWithoutUpstream(t *testing.T) {
	listPath := filepath.Join(t.TempDir(), "blocked.txt")
	if err := os.WriteFile(listPath, []byte("blocked.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.DomainLists = []config.DomainList{{Name: "blocked", File: listPath}}
	cfg.Rules = []config.Rule{{ID: "blocked", DomainLists: []string{"blocked"}, Action: "zeroip"}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.LoadDomainList("blocked", listPath); err != nil {
		t.Fatal(err)
	}

	for i, qtype := range []uint16{dns.TypeA, dns.TypeAAAA} {
		want := []string{"0.0.0.0", "::"}[i]
		for pass := 0; pass < 2; pass++ {
			result, err := srv.Query("blocked.example.", qtype)
			if err != nil {
				t.Fatal(err)
			}
			if result.CacheHit != (pass == 1) || result.Rcode != dns.RcodeSuccess ||
				len(result.Answers) != 1 || result.Answers[0] != want {
				t.Fatalf("type %d pass %d: unexpected result %#v", qtype, pass, result)
			}
		}
	}
}

func TestQueryNXDOMAINRuleIsCached(t *testing.T) {
	listPath := filepath.Join(t.TempDir(), "blocked.txt")
	if err := os.WriteFile(listPath, []byte("blocked.example\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.DomainLists = []config.DomainList{{Name: "blocked", File: listPath}}
	cfg.Rules = []config.Rule{{ID: "blocked", DomainLists: []string{"blocked"}, Action: "nxdomain"}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	if err := srv.LoadDomainList("blocked", listPath); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		result, err := srv.Query("blocked.example.", dns.TypeA)
		if err != nil {
			t.Fatal(err)
		}
		if result.CacheHit != (pass == 1) || result.Rcode != dns.RcodeNameError {
			t.Fatalf("pass %d: unexpected result %#v", pass, result)
		}
	}
}

func startTestUpstream(t *testing.T) string {
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
			switch request.Question[0].Qtype {
			case dns.TypeA:
				response.Answer = append(response.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 77},
					A:   net.IPv4(192, 0, 2, 1),
				})
			case dns.TypeAAAA:
				response.Answer = append(response.Answer, &dns.AAAA{
					Hdr:  dns.RR_Header{Name: request.Question[0].Name, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 42},
					AAAA: net.ParseIP("2001:db8::1"),
				})
			}
			_ = w.WriteMsg(response)
		}),
	}
	go func() {
		_ = server.ActivateAndServe()
	}()
	t.Cleanup(func() {
		_ = server.Shutdown()
	})
	return packetConn.LocalAddr().String()
}

func TestRefreshLocalResolversPicksUpFileCreatedAfterStart(t *testing.T) {
	upstreamAddress := startTestUpstream(t)
	resolvFile := filepath.Join(t.TempDir(), "resolv.conf.auto")

	cfg := config.DefaultConfig()
	cfg.CacheEnabled = false
	cfg.UseLocalResolv = true
	cfg.LocalResolvFile = resolvFile
	cfg.Rules = []config.Rule{{ID: "fallback", IgnoreDomainMatch: true, Upstreams: []string{"local-resolv-1"}}}

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	if result := srv.resolveQuery("example.com.", dns.TypeA); result != nil {
		t.Fatalf("query succeeded before the resolver file existed: %#v", result)
	}

	if err := os.WriteFile(resolvFile, []byte("nameserver "+upstreamAddress+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.refreshLocalResolvers()

	result := srv.resolveQuery("example.com.", dns.TypeA)
	if result == nil || len(result.Records) != 1 || result.UsedUpstream != "local-resolv-1" {
		t.Fatalf("query after resolver file appeared returned unexpected result: %#v", result)
	}

	if err := os.WriteFile(resolvFile, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	srv.refreshLocalResolvers()
	for _, upstream := range srv.upstreamsSnapshot() {
		if strings.HasPrefix(upstream.Name, config.LocalResolverPrefix) {
			t.Fatalf("stale local upstream was kept: %#v", upstream)
		}
	}
}

func TestLocalResolverChangeTriggersUpstreamNFTRefresh(t *testing.T) {
	resolvFile := filepath.Join(t.TempDir(), "resolv.conf.auto")
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = true
	cfg.LocalResolvFile = resolvFile
	cfg.Rules = []config.Rule{{
		ID:             "direct",
		AddUpstreamIPs: true,
		Upstreams:      []string{"local-resolv-1", "local-resolv-2"},
		IPv4NFTSets:    []string{"inet/fw4/ipv4"},
		IPv6NFTSets:    []string{"inet/fw4/ipv6"},
	}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	manager := &recordingNFTSetManager{added: make(map[string][]string), contents: make(map[string][]string)}
	srv.nftMgr = manager

	if complete := srv.refreshUpstreamNFTSets(); !complete {
		t.Fatal("missing upstreams must not force retries; the local resolver trigger covers them")
	}
	if len(manager.added) != 0 {
		t.Fatalf("nothing should be added before resolvers exist: %#v", manager.added)
	}

	if err := os.WriteFile(resolvFile, []byte("nameserver 192.0.2.53\nnameserver 2001:db8::53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.refreshLocalResolvers()

	select {
	case <-srv.nftRefreshNow:
	default:
		t.Fatal("local resolver update did not request an upstream nftset refresh")
	}

	srv.refreshUpstreamNFTSets()
	if got := manager.added["inet/fw4/ipv4"]; len(got) != 1 || got[0] != "192.0.2.53" {
		t.Errorf("IPv4 set = %#v, want 192.0.2.53", got)
	}
	if got := manager.added["inet/fw4/ipv6"]; len(got) != 1 || got[0] != "2001:db8::53" {
		t.Errorf("IPv6 set = %#v, want 2001:db8::53", got)
	}
}

func TestRefreshUpstreamNFTSetsReportsIncompleteOnLookupFailure(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{Name: "doh", Address: "https://resolver.example/dns-query", Protocol: "doh"}}
	cfg.Rules = []config.Rule{{ID: "r", AddUpstreamIPs: true, Upstreams: []string{"doh"}, IPv4NFTSets: []string{"inet/fw4/ipv4"}}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	srv.nftMgr = &recordingNFTSetManager{added: make(map[string][]string), contents: make(map[string][]string)}
	srv.lookupIPs = func(context.Context, string) ([]net.IP, error) { return nil, fmt.Errorf("dns not ready") }

	if srv.refreshUpstreamNFTSets() {
		t.Fatal("a failed endpoint lookup must be reported so the refresh is retried soon")
	}
}

func TestSequentialUpstreamsUseTheirOwnTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for two upstream timeouts")
	}
	silent := func() string {
		conn, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		return conn.LocalAddr().String()
	}
	good := startTestUpstream(t)

	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.CacheEnabled = false
	cfg.UpstreamDNS = []config.UpstreamDNS{
		{Name: "silent-1", Address: silent(), Protocol: "udp", Timeout: 3},
		{Name: "silent-2", Address: silent(), Protocol: "udp", Timeout: 3},
		{Name: "good", Address: good, Protocol: "udp", Timeout: 3},
	}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	for i := range cfg.UpstreamDNS {
		if err := srv.RegisterUpstream(&cfg.UpstreamDNS[i]); err != nil {
			t.Fatal(err)
		}
	}

	result := srv.resolveQuery("example.com.", dns.TypeA)
	if result == nil || result.UsedUpstream != "good" {
		t.Fatalf("later upstream was not tried after earlier ones timed out: %#v", result)
	}
}

func TestSVCBFailureCachedAsNoData(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.CacheEnabled = false
	cfg.Rules = []config.Rule{{ID: "fallback", IgnoreDomainMatch: true, Upstreams: []string{"missing"}}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	if result := srv.resolveQuery("example.com.", dns.TypeA); result != nil {
		t.Fatalf("A failure must stay an error: %#v", result)
	}
	if srv.svcbFailureActive("example.com.:" + "1") {
		t.Fatal("A failure must not enter the SVCB failure cache")
	}

	result := srv.resolveQuery("example.com.", dns.TypeHTTPS)
	if result == nil || result.Rcode != dns.RcodeSuccess || len(result.Records) != 0 || result.TTL != 0 {
		t.Fatalf("HTTPS failure should become empty NODATA: %#v", result)
	}
	key := fmt.Sprintf("example.com.:%d", dns.TypeHTTPS)
	if !srv.svcbFailureActive(key) {
		t.Fatal("HTTPS failure was not cached")
	}
	if result := srv.resolveQuery("example.com.", dns.TypeHTTPS); result == nil {
		t.Fatal("cached HTTPS failure should return NODATA")
	}

	srv.ClearCache()
	if srv.svcbFailureActive(key) {
		t.Fatal("ClearCache should clear SVCB failures")
	}
}

func startServFailUpstream(t *testing.T, queries *atomic.Int32) string {
	t.Helper()
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{
		PacketConn: packetConn,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, request *dns.Msg) {
			queries.Add(1)
			response := new(dns.Msg)
			response.SetRcode(request, dns.RcodeServerFailure)
			_ = w.WriteMsg(response)
		}),
	}
	go func() {
		_ = server.ActivateAndServe()
	}()
	t.Cleanup(func() {
		_ = server.Shutdown()
	})
	return packetConn.LocalAddr().String()
}

func TestPTRFailureCachedBriefly(t *testing.T) {
	var queries atomic.Int32
	cfg := config.DefaultConfig()
	cfg.UseLocalResolv = false
	cfg.CacheEnabled = false
	cfg.UpstreamDNS = []config.UpstreamDNS{{Name: "bad", Address: startServFailUpstream(t, &queries), Protocol: "udp", Timeout: 3}}
	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	for i := range cfg.UpstreamDNS {
		if err := srv.RegisterUpstream(&cfg.UpstreamDNS[i]); err != nil {
			t.Fatal(err)
		}
	}

	const ptrName = "4.0.0.0.0.0.0.0.0.0.0.0.8.3.0.0.1.0.2.0.0.0.b.1.4.4.c.8.9.0.4.2.ip6.arpa."
	for i := 0; i < 3; i++ {
		if result := srv.resolveQueryFrom(ptrName, dns.TypePTR, "192.0.2.9"); result != nil {
			t.Fatalf("PTR SERVFAIL must stay an error: %#v", result)
		}
	}
	if got := queries.Load(); got != 1 {
		t.Fatalf("upstream queried %d times, want 1 (failure cached)", got)
	}

	if result := srv.resolveQuery("example.com.", dns.TypeA); result != nil {
		t.Fatalf("A SERVFAIL must stay an error: %#v", result)
	}
	if result := srv.resolveQuery("example.com.", dns.TypeA); result != nil {
		t.Fatalf("A SERVFAIL must stay an error: %#v", result)
	}
	if got := queries.Load(); got != 3 {
		t.Fatalf("non-PTR failures must not be cached: upstream queried %d times, want 3", got)
	}

	srv.ClearCache()
	srv.resolveQuery(ptrName, dns.TypePTR)
	if got := queries.Load(); got != 4 {
		t.Fatalf("ClearCache should clear PTR failures: upstream queried %d times, want 4", got)
	}
}
