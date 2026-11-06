package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/garinasset/updns/config"
	"github.com/garinasset/updns/server"
	"github.com/miekg/dns"
)

func TestQueryPathReportsMixedAddressFamilyResults(t *testing.T) {
	results := []*server.QueryResult{
		{QueryType: "A", CacheHit: true},
		{QueryType: "AAAA", Action: "ipv4+ipv6"},
	}

	if got, want := queryPath(results), "A=Cache Hit / AAAA=Upstream Query"; got != want {
		t.Fatalf("queryPath() = %q, want %q", got, want)
	}
}

func TestQueryPathReportsPrefetchWait(t *testing.T) {
	results := []*server.QueryResult{
		{QueryType: "A", PrefetchWait: true},
		{QueryType: "AAAA", CacheHit: true},
	}

	if got := queryPath(results); got != "A=Prefetch Hit / AAAA=Cache Hit" {
		t.Fatalf("incorrect wait path: %s", got)
	}
}

func TestQueryPathReportsLocalAnswer(t *testing.T) {
	results := []*server.QueryResult{{QueryType: "A", LocalAnswer: true}, {QueryType: "AAAA", LocalAnswer: true}}
	if got := queryPath(results); got != "Local Answer" {
		t.Fatalf("local query path = %q", got)
	}
}

func TestWriteQueryOutputFormatsAlignedFields(t *testing.T) {
	results := []*server.QueryResult{
		{
			Domain:       "example.com.",
			QueryType:    "A",
			Answers:      []string{"192.0.2.1"},
			MatchedRule:  "example",
			Action:       "ipv4+ipv6",
			UsedUpstream: "Cloudflare-DoH",
			IPv4NFTSets:  []string{"inet/fw4/example4"},
			IPv4InSets:   map[string]bool{"inet/fw4/example4": true},
			Rcode:        dns.RcodeSuccess,
		},
		{
			Domain:      "example.com.",
			QueryType:   "AAAA",
			CacheHit:    true,
			Action:      "ipv4+ipv6",
			IPv6NFTSets: []string{"inet/fw4/example6"},
			Rcode:       dns.RcodeSuccess,
		},
	}
	var output bytes.Buffer
	writeQueryOutput(&output, results)
	text := output.String()

	for _, want := range []string{
		"│  DOMAIN",
		"│  Domain       : example.com.",
		"│  Query Type   : A / AAAA",
		"│  Query Path   : A=Upstream Query / AAAA=Cache Hit",
		"│  Matched Rule : example",
		"│  Action       : IPv4 + IPv6",
		"│  Response     : NOERROR",
		"│  Upstream     : Cloudflare-DoH",
		"│  IPv4         : inet/fw4/example4",
		"│                 ✓ Contains resolved IPv4",
		"│  IPv6         : inet/fw4/example6",
		"│                 — No IPv6 answer",
		"│  • 192.0.2.1",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("output does not contain %q:\n%s", want, text)
		}
	}

	colonPositions := make(map[int]bool)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "│  ") && strings.Contains(line, " : ") {
			colonPositions[strings.Index(line, ":")] = true
		}
	}
	if len(colonPositions) != 1 {
		t.Errorf("field colons are not aligned: positions %v\n%s", colonPositions, text)
	}
	lines := strings.Split(text, "\n")
	if w := len([]rune(lines[1])); w != 48 || len([]rune(lines[len(lines)-3])) != 48 {
		t.Errorf("frame width = %d, want 48\n%s", w, text)
	}
	if !strings.HasSuffix(text, "\n\n") {
		t.Errorf("output must end with a blank line, got suffix %q", text[len(text)-min(len(text), 10):])
	}
}

func TestWriteNFTFieldDistinguishesMissingAddressFromMissingSetMember(t *testing.T) {
	tests := []struct {
		name        string
		hasAnswers  *bool
		inSet       bool
		wantMessage string
	}{
		{name: "no address answer", hasAnswers: boolPointer(false), wantMessage: "— No IPv6 answer"},
		{name: "address absent from set", hasAnswers: boolPointer(true), wantMessage: "✗ Does not contain resolved IPv6"},
		{name: "query unavailable", hasAnswers: nil, wantMessage: "— resolved IPv6 query unavailable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			writeNFTField(&output, "IPv6", []string{"inet/fw4/example6"},
				map[string]bool{"inet/fw4/example6": test.inSet}, test.hasAnswers, "resolved IPv6")
			if !strings.Contains(output.String(), test.wantMessage) {
				t.Fatalf("output %q does not contain %q", output.String(), test.wantMessage)
			}
		})
	}
}

func boolPointer(value bool) *bool {
	return &value
}

func TestRegisterUpstreamsKeepsEachUpstreamDistinct(t *testing.T) {
	cfg := &config.Config{UpstreamDNS: []config.UpstreamDNS{
		{Name: "local-resolv-1", Address: "10.0.0.1", Protocol: "udp"},
		{Name: "local-resolv-2", Address: "10.0.0.2", Protocol: "udp"},
		{Name: "doh", Address: "https://dns.example/dns-query", Protocol: "doh"},
	}}

	registered := make(map[string]*config.UpstreamDNS)
	registerUpstreams(cfg, func(upstream *config.UpstreamDNS) error {
		registered[upstream.Name] = upstream
		return nil
	}, func(*config.UpstreamDNS, error) {
		t.Fatal("unexpected registration error")
	})

	for _, want := range cfg.UpstreamDNS {
		got := registered[want.Name]
		if got == nil || got.Address != want.Address || got.Protocol != want.Protocol {
			t.Fatalf("upstream %s registered as %+v, want address %s protocol %s", want.Name, got, want.Address, want.Protocol)
		}
	}
}

func TestWriteQueryOutputShowsFailureReason(t *testing.T) {
	results := []*server.QueryResult{
		{Domain: "baidu.jp.", QueryType: "A", Action: "ipv4+ipv6", Rcode: dns.RcodeServerFailure, Error: "Upstream timeout"},
		{Domain: "baidu.jp.", QueryType: "AAAA", Action: "ipv4+ipv6", Rcode: dns.RcodeServerFailure, Error: "Upstream timeout"},
	}
	var output bytes.Buffer
	writeQueryOutput(&output, results)
	text := output.String()
	for _, want := range []string{"│  Response     : ERROR", "│  ERRORS", "│  A            : Upstream timeout", "│  AAAA         : Upstream timeout", "│  (none)"} {
		if !strings.Contains(text, want) {
			t.Errorf("output does not contain %q:\n%s", want, text)
		}
	}
}
