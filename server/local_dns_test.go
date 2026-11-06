package server

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func testLocalDNS(t *testing.T, hosts, leases string) (*localDNS, string, string) {
	t.Helper()
	dir := t.TempDir()
	hostsPath, leasesPath := filepath.Join(dir, "hosts"), filepath.Join(dir, "leases")
	for path, data := range map[string]string{hostsPath: hosts, leasesPath: leases} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	local := newLocalDNS(filepath.Join(dir, "missing-config"))
	local.configured = true
	local.settings = config.LocalDNSConfig{
		HostsFiles: []string{hostsPath}, LeaseFile: leasesPath, Domain: "lan", ExpandHosts: true,
	}
	return local, hostsPath, leasesPath
}

func TestLocalHostsAliasesFamiliesAndReverse(t *testing.T) {
	local, _, _ := testLocalDNS(t, "192.0.2.10 desktop alias\n2001:db8::10 desktop\n", "")
	for _, sample := range []struct {
		name string
		kind uint16
		want string
	}{
		{"DESKTOP.", dns.TypeA, "192.0.2.10"},
		{"alias.lan.", dns.TypeA, "192.0.2.10"},
		{"desktop.lan.", dns.TypeAAAA, "2001:db8::10"},
		{"10.2.0.192.in-addr.arpa.", dns.TypePTR, "desktop.lan."},
	} {
		response, source, err := local.lookup(sample.name, sample.kind)
		if err != nil || response == nil || source != "local-hosts" || len(response.Answer) != 1 ||
			!strings.Contains(response.Answer[0].String(), sample.want) || response.Answer[0].Header().Ttl != 0 {
			t.Fatalf("%s %s: response=%v source=%s error=%v", sample.name, dns.TypeToString[sample.kind], response, source, err)
		}
	}
	response, _, err := local.lookup("alias.lan.", dns.TypeAAAA)
	if err != nil || response == nil || len(response.Answer) != 0 {
		t.Fatal("known local name with absent family should return local NODATA")
	}
	if response, _, err := local.lookup("desktop.lan.", dns.TypeTXT); response != nil || err != nil {
		t.Fatal("unsupported local record type should follow normal resolution")
	}
}

func TestLocalDHCPExpiryAndHostsPrecedence(t *testing.T) {
	now := time.Unix(1000, 0)
	local, _, _ := testLocalDNS(t, "192.0.2.99 fixed\n", "1005 mac 192.0.2.20 leased client\n0 mac 192.0.2.30 fixed client\n0 mac 2001:db8::20 v6 client\n")
	local.now = func() time.Time { return now }
	response, source, err := local.lookup("leased.lan.", dns.TypeA)
	if err != nil || source != "local-dhcp" || len(response.Answer) != 1 {
		t.Fatalf("DHCP record missing: %v %s %v", response, source, err)
	}
	response, source, err = local.lookup("fixed.lan.", dns.TypeA)
	if err != nil || source != "local-hosts" || len(response.Answer) != 1 || !strings.Contains(response.Answer[0].String(), "192.0.2.99") {
		t.Fatal("DHCP lease overrode hosts")
	}
	response, _, err = local.lookup("v6.lan.", dns.TypeAAAA)
	if err != nil || response == nil || len(response.Answer) != 1 {
		t.Fatal("IPv6 DHCP record missing")
	}
	now = now.Add(5 * time.Second)
	if response, _, err := local.lookup("leased.lan.", dns.TypeA); response != nil || err != nil {
		t.Fatal("expired DHCP lease was served")
	}
}

func TestLocalSourceAtomicReplacementAndDeletion(t *testing.T) {
	local, hosts, _ := testLocalDNS(t, "192.0.2.10 desktop\n", "")
	if _, _, err := local.lookup("desktop.lan.", dns.TypeA); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(hosts)
	if err != nil {
		t.Fatal(err)
	}
	replacement := hosts + ".replacement"
	if err := os.WriteFile(replacement, []byte("192.0.2.11 desktop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, hosts); err != nil {
		t.Fatal(err)
	}
	response, _, err := local.lookup("desktop.lan.", dns.TypeA)
	if err != nil || response == nil || !strings.Contains(response.Answer[0].String(), "192.0.2.11") {
		t.Fatal("same-size and same-mtime replacement was not reloaded")
	}
	if err := os.Remove(hosts); err != nil {
		t.Fatal(err)
	}
	if response, _, err := local.lookup("desktop.lan.", dns.TypeA); response != nil || err != nil {
		t.Fatal("deleted hosts record was retained")
	}
}

func TestLocalDataOverridesUpstreamCacheAndRespectsRules(t *testing.T) {
	var calls atomic.Int32
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		calls.Add(1)
		writeFlightAnswer(t, w, query)
	})
	local, hosts, _ := testLocalDNS(t, "", "")
	srv.local = local
	name := "desktop.lan."
	if result, err := srv.Query(name, dns.TypeA); err != nil || result.LocalAnswer {
		t.Fatalf("initial upstream response: %#v %v", result, err)
	}
	if err := os.WriteFile(hosts, []byte("192.0.2.10 desktop\n2001:db8::10 desktop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, strategy := range []config.QueryStrategy{config.StrategyOptimal, config.StrategyStrict, config.StrategyConcurrent} {
		srv.cfg.QueryStrategy = strategy
		result, err := srv.Query(name, dns.TypeA)
		if err != nil || !result.LocalAnswer || result.CacheHit || result.Path() != "Local Answer" ||
			result.TTL != 0 || len(result.Answers) != 1 || result.Answers[0] != "192.0.2.10" {
			t.Fatalf("%s: local response %#v %v", strategy, result, err)
		}
	}
	if calls.Load() != 1 || srv.cache.Size() != 0 {
		t.Fatal("local data queried upstream or retained obsolete upstream cache")
	}
	srv.cfg.Rules = []config.Rule{{
		ID: "local", IgnoreDomainMatch: true, Action: "ipv4only",
		Upstreams: []string{"test"}, IPv4NFTSets: []string{"inet/fw4/local4"},
	}}
	manager := &recordingNFTSetManager{added: make(map[string][]string), contents: make(map[string][]string)}
	srv.nftMgr = manager
	result, err := srv.Query(name, dns.TypeAAAA)
	if err != nil || !result.LocalAnswer || len(result.Records) != 0 {
		t.Fatal("local data bypassed IPv4-only rule")
	}
	result, err = srv.Query(name, dns.TypeA)
	if err != nil || result.MatchedRule != "local" || !manager.has("inet/fw4/local4", "192.0.2.10") {
		t.Fatal("local data did not use rule nftsets")
	}
	for _, action := range []string{"nxdomain", "zeroip"} {
		srv.cfg.Rules[0].Action = action
		srv.ClearCache()
		result, err = srv.Query(name, dns.TypeA)
		if err != nil || result.LocalAnswer || result.Action != action {
			t.Fatalf("local data bypassed %s: %#v %v", action, result, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("local or blocking rule queried upstream")
	}
	srv.cfg.Rules = nil
	srv.ClearCache()
	if err := os.WriteFile(hosts, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Query(name, dns.TypeA); err != nil || calls.Load() != 2 {
		t.Fatal("removing local record did not restore fresh upstream resolution")
	}
}

func TestLocalSourceErrorsAreReported(t *testing.T) {
	local, hosts, _ := testLocalDNS(t, "", "")
	if err := os.Remove(hosts); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(hosts, 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := local.lookup("desktop.lan.", dns.TypeA); err == nil || !strings.Contains(err.Error(), hosts) {
		t.Fatal("local source read error was not reported")
	}
}

func TestLocalDNSConfigurationReloadsDomain(t *testing.T) {
	local, hosts, leases := testLocalDNS(t, "192.0.2.10 desktop\n", "")
	path := filepath.Join(t.TempDir(), "dhcp")
	local = newLocalDNS(path)
	writeConfig := func(domain string) {
		t.Helper()
		content := fmt.Sprintf("config dnsmasq\n option domain '%s'\n option expandhosts '1'\n list addnhosts '%s'\n option leasefile '%s'\n", domain, hosts, leases)
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig("lan")
	if response, _, err := local.lookup("desktop.lan.", dns.TypeA); response == nil || err != nil {
		t.Fatal("configured local domain missing")
	}
	writeConfig("home")
	if response, _, err := local.lookup("desktop.home.", dns.TypeA); response == nil || err != nil {
		t.Fatal("local domain update was not loaded")
	}
	if response, _, err := local.lookup("desktop.lan.", dns.TypeA); response != nil || err != nil {
		t.Fatal("old local domain survived configuration update")
	}
}

func TestUnreadableLocalDataFallsBackToUpstream(t *testing.T) {
	var calls atomic.Int32
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		calls.Add(1)
		writeFlightAnswer(t, w, query)
	})
	local, hosts, _ := testLocalDNS(t, "", "")
	srv.local = local
	if err := os.Remove(hosts); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(hosts, 0700); err != nil {
		t.Fatal(err)
	}
	result, err := srv.Query("desktop.lan.", dns.TypeA)
	if err != nil || result == nil || result.LocalAnswer || calls.Load() != 1 {
		t.Fatalf("unreadable local data blocked upstream resolution: %#v %v", result, err)
	}
}
