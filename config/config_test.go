package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Enabled {
		t.Error("Expected Enabled to be false by default")
	}

	if cfg.Port != 153 {
		t.Errorf("Expected Port 153, got %d", cfg.Port)
	}

	if !cfg.UseLocalResolv {
		t.Error("Expected UseLocalResolv to be true by default")
	}

	if !cfg.CacheEnabled {
		t.Error("Expected CacheEnabled to be true by default")
	}

	if cfg.CacheSize != 4096 {
		t.Errorf("Expected CacheSize 4096, got %d", cfg.CacheSize)
	}

	if !cfg.ActivePreload {
		t.Error("Expected ActivePreload to be true by default")
	}

	if cfg.LogQuery {
		t.Error("Expected LogQuery to be false by default")
	}
	if len(cfg.UpstreamDNS) != 0 {
		t.Fatalf("configuration defaults must not include upstream DNS servers: %#v", cfg.UpstreamDNS)
	}
}

func TestLoadMissingUCIReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-updns")
	cfg, err := LoadFromUCI(path)
	if !errors.Is(err, os.ErrNotExist) || cfg != nil {
		t.Fatalf("expected missing configuration error without fallback, got config=%#v error=%v", cfg, err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("configuration error must identify the missing path: %v", err)
	}
}

func TestSaveAndLoadUCI(t *testing.T) {
	cfg := &Config{
		Enabled:         true,
		Port:            1153,
		UseLocalResolv:  false,
		LocalResolvFile: "/custom/resolv.conf",
		ConcurrentQuery: true,
		CacheEnabled:    false,
		CacheSize:       4096,
		ActivePreload:   false,
		LogQuery:        true,
		LogLevel:        "debug",
	}

	// Test SaveToUCI
	tmpFile := filepath.Join(t.TempDir(), "updns")
	err := SaveToUCI(cfg, tmpFile)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}
	savedConfig, err := os.ReadFile(tmpFile)
	if err != nil {
		t.Fatalf("Failed to read saved config: %v", err)
	}
	savedText := string(savedConfig)
	if strings.Contains(savedText, "option enable ") {
		t.Fatal("saved UCI config must not contain the obsolete service lifecycle option")
	}
	if !strings.Contains(savedText, `option enabled "1"`) {
		t.Fatal("saved UCI config must contain the service lifecycle option")
	}
	if strings.Index(savedText, `option enabled "1"`) > strings.Index(savedText, `option port "1153"`) {
		t.Fatal("enabled must be serialized before port")
	}

	// Test LoadFromUCI
	loadedCfg, err := LoadFromUCI(tmpFile)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if loadedCfg.Port != cfg.Port {
		t.Errorf("Port mismatch: expected %d, got %d", cfg.Port, loadedCfg.Port)
	}
	if loadedCfg.Enabled != cfg.Enabled {
		t.Errorf("Enabled mismatch: expected %v, got %v", cfg.Enabled, loadedCfg.Enabled)
	}

	if loadedCfg.UseLocalResolv != cfg.UseLocalResolv {
		t.Errorf("UseLocalResolv mismatch: expected %v, got %v", cfg.UseLocalResolv, loadedCfg.UseLocalResolv)
	}

	if loadedCfg.CacheEnabled != cfg.CacheEnabled {
		t.Errorf("CacheEnabled mismatch: expected %v, got %v", cfg.CacheEnabled, loadedCfg.CacheEnabled)
	}

	if loadedCfg.CacheSize != cfg.CacheSize {
		t.Errorf("CacheSize mismatch: expected %d, got %d", cfg.CacheSize, loadedCfg.CacheSize)
	}
}

func TestLoadOpenWrtUCISections(t *testing.T) {
	dir := t.TempDir()
	resolvFile := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(resolvFile, []byte("nameserver 192.0.2.53\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "updns")
	content := `config updns 'general'
	option enabled '1'
	option port '153'
	option use_local_resolv '1'
	option local_resolv_file '` + resolvFile + `'
	option cache_size '1024'

config upstream 'test_doh'
	option name 'test_doh'
	option address 'https://dns.example/dns-query'
	option protocol 'doh'
	option timeout '3'

config domainlist 'blocked'
	option name 'blocked'
	option file 'blocked.txt'

config rule 'blocked_rule'
	list domain_lists 'blocked'
	list upstreams 'test_doh'
	option action 'ipv4only'
	list ipv4_nftsets 'blocked_v4'
`
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatalf("LoadFromUCI failed: %v", err)
	}
	if !cfg.Enabled || cfg.Port != 153 || cfg.CacheSize != 1024 {
		t.Fatalf("unexpected general settings: enabled=%v port=%d cache_size=%d", cfg.Enabled, cfg.Port, cfg.CacheSize)
	}
	if !containsUpstream(cfg.UpstreamDNS, "192.0.2.53") {
		t.Fatal("local resolver was not imported")
	}
	if cfg.UpstreamDNS[0].Name != "local-resolv-1" {
		t.Fatalf("local resolver should precede configured defaults, got first upstream %q", cfg.UpstreamDNS[0].Name)
	}
	if !containsUpstream(cfg.UpstreamDNS, "https://dns.example/dns-query") {
		t.Fatal("configured upstream was not loaded")
	}
	wantUpstreams := []UpstreamDNS{
		{Name: "local-resolv-1", Address: "192.0.2.53", Protocol: "udp", Timeout: 5},
		{Name: "test_doh", Address: "https://dns.example/dns-query", Protocol: "doh", Timeout: 3},
	}
	if !reflect.DeepEqual(cfg.UpstreamDNS, wantUpstreams) {
		t.Fatalf("unexpected upstreams: got %#v, want %#v", cfg.UpstreamDNS, wantUpstreams)
	}
	if len(cfg.DomainLists) != 1 || cfg.DomainLists[0] != (DomainList{Name: "blocked", File: "blocked.txt"}) {
		t.Fatalf("unexpected domain lists: %#v", cfg.DomainLists)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Action != "ipv4only" || cfg.Rules[0].Upstreams[0] != "test_doh" || cfg.Rules[0].DomainLists[0] != "blocked" {
		t.Fatalf("unexpected rules: %#v", cfg.Rules)
	}
}

func TestLoadUCIUsesOnlyConfiguredUpstreams(t *testing.T) {
	tests := []struct {
		name      string
		upstreams []UpstreamDNS
	}{
		{name: "all upstreams deleted"},
		{
			name: "default endpoint renamed",
			upstreams: []UpstreamDNS{
				{Name: "My-Google", Address: "https://dns.google/dns-query", Protocol: "doh", Timeout: 3},
			},
		},
		{
			name: "default endpoint modified and order preserved",
			upstreams: []UpstreamDNS{
				{Name: "custom", Address: "192.0.2.53", Protocol: "udp", Timeout: 2},
				{Name: "google_doh", Address: "https://dns.example/dns-query", Protocol: "doh", Timeout: 7},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.UseLocalResolv = false
			cfg.UpstreamDNS = tt.upstreams
			path := filepath.Join(t.TempDir(), "updns")
			if err := SaveToUCI(cfg, path); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadFromUCI(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(loaded.UpstreamDNS, tt.upstreams) {
				t.Fatalf("got upstreams %#v, want %#v", loaded.UpstreamDNS, tt.upstreams)
			}
		})
	}
}

func TestLoadPackagedUCIHasNoImplicitUpstreams(t *testing.T) {
	for _, name := range []string{"updns", "updns-E"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "openwrt", "updns", "root", "etc", "config", name))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "updns")
			data = []byte(strings.Replace(string(data), "option use_local_resolv '1'", "option use_local_resolv '0'", 1))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFromUCI(path)
			if err != nil {
				t.Fatal(err)
			}
			want := []UpstreamDNS{
				{Name: "Tencent-UDP", Address: "119.29.29.29", Protocol: "udp", Timeout: 5},
				{Name: "Google-DoH", Address: "https://dns.google/dns-query", Protocol: "doh", Timeout: 5},
				{Name: "Cloudflare-DoH", Address: "https://one.one.one.one/dns-query", Protocol: "doh", Timeout: 5},
				{Name: "Tencent-DoH", Address: "https://doh.pub/dns-query", Protocol: "doh", Timeout: 5},
				{Name: "AdGuard-DoH", Address: "https://unfiltered.adguard-dns.com/dns-query", Protocol: "doh", Timeout: 5},
			}
			if !reflect.DeepEqual(cfg.UpstreamDNS, want) {
				t.Fatalf("got upstreams %#v, want %#v", cfg.UpstreamDNS, want)
			}
		})
	}
}

func TestLoadRulesPreservesUCIOrder(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	content := `config updns 'general'
	option use_local_resolv '0'

config rule 'first'
	option name 'first'
	list domain_lists 'first.txt'

config rule 'second'
	option name 'second'
	list domain_lists 'second.txt'
`
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 2 || cfg.Rules[0].ID != "first" || cfg.Rules[1].ID != "second" {
		t.Fatalf("UCI rule order was not preserved: %#v", cfg.Rules)
	}
}

func TestLoadLocalResolversAddsEveryNameserver(t *testing.T) {
	dir := t.TempDir()
	resolvFile := filepath.Join(dir, "resolv.conf")
	if err := os.WriteFile(resolvFile, []byte("# generated\nnameserver 192.0.2.53\nsearch example.test\nnameserver 2001:db8::53\n"), 0600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "updns")
	content := "config updns 'general'\noption use_local_resolv '1'\noption local_resolv_file '" + resolvFile + "'\n"
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.UpstreamDNS) != 2 {
		t.Fatalf("expected only the two local resolvers, got %#v", cfg.UpstreamDNS)
	}
	for _, expected := range []struct {
		name    string
		address string
	}{
		{name: "local-resolv-1", address: "192.0.2.53"},
		{name: "local-resolv-2", address: "2001:db8::53"},
	} {
		found := false
		for _, upstream := range cfg.UpstreamDNS {
			if upstream.Name == expected.name && upstream.Address == expected.address && upstream.Protocol == "udp" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("local resolver %q (%s) was not added: %#v", expected.name, expected.address, cfg.UpstreamDNS)
		}
	}
}

func TestLoadRulesParsesDomainMatchFallback(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	content := `config updns 'general'
option use_local_resolv '0'

config rule 'fallback'
	option name 'fallback'
	option domain_match '0'
	list upstreams 'google_doh'
`
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 1 || !cfg.Rules[0].IgnoreDomainMatch {
		t.Fatalf("ignore-domain-match rule was not loaded: %#v", cfg.Rules)
	}
}

func TestLoadRulesIgnoresLegacyIgnoreDomainMatch(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	content := `config updns 'general'
option use_local_resolv '0'

config rule 'legacy'
	option ignore_domain_match '1'
`
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].IgnoreDomainMatch {
		t.Fatalf("legacy ignore_domain_match must not affect the rule: %#v", cfg.Rules)
	}
}

func TestLoadUCIRejectsMultipleIgnoreDomainMatchFallbacks(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	content := `config updns 'general'
option use_local_resolv '0'

config rule 'fallback1'
option domain_match '0'

config rule 'fallback2'
option domain_match '0'
`
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromUCI(configFile); err == nil {
		t.Fatal("expected multiple fallback rules to be rejected")
	}
}

func TestLoadUCIRejectsDuplicateDomainListNames(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	content := `config updns 'general'
option use_local_resolv '0'

config domainlist 'first'
option name 'same'
option file 'first.txt'

config domainlist 'second'
option name 'same'
option file 'second.txt'
`
	if err := os.WriteFile(configFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromUCI(configFile); err == nil {
		t.Fatal("expected duplicate domain-list names to be rejected")
	}
}

func TestSaveToUCIIncludesUpstreamName(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	cfg := DefaultConfig()
	cfg.UpstreamDNS = []UpstreamDNS{
		{Name: "google_doh", Address: "https://dns.google/dns-query", Protocol: "doh", Timeout: 5},
	}
	if err := SaveToUCI(cfg, configFile); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `option name "google_doh"`) {
		t.Fatalf("saved UCI config is missing the upstream name: %s", data)
	}
	if strings.Contains(string(data), "priority") {
		t.Fatalf("saved UCI config must not serialize rule priorities: %s", data)
	}
}

func TestSaveToUCISerializesDomainListNamesAndFallback(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	cfg := DefaultConfig()
	cfg.DomainLists = []DomainList{{Name: "custom-name", File: "/etc/updns/domainlists/list.txt"}}
	cfg.Rules = []Rule{{ID: "fallback", IgnoreDomainMatch: true, Upstreams: []string{"google_doh"}}}
	if err := SaveToUCI(cfg, configFile); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.DomainLists) != 1 || loaded.DomainLists[0] != cfg.DomainLists[0] {
		t.Fatalf("domain-list identity was not serialized: %#v", loaded.DomainLists)
	}
	if len(loaded.Rules) != 1 || !loaded.Rules[0].IgnoreDomainMatch {
		t.Fatalf("fallback rule was not serialized: %#v", loaded.Rules)
	}
}

func TestSaveToUCISerializesUpstreamIPOptionAndPositiveFallback(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	cfg := DefaultConfig()
	cfg.Rules = []Rule{{
		ID:                "fallback",
		IgnoreDomainMatch: true,
		AddUpstreamIPs:    true,
		IPv4NFTSets:       []string{"inet/fw4/example"},
		Upstreams:         []string{"google_doh"},
	}}
	if err := SaveToUCI(cfg, configFile); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "ignore_domain_match") ||
		!strings.Contains(string(data), `option domain_match "0"`) ||
		!strings.Contains(string(data), `option add_upstream_ips "1"`) {
		t.Fatalf("fallback or upstream-IP options were not serialized with positive names: %s", data)
	}
	loaded, err := LoadFromUCI(configFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Rules) != 1 || !loaded.Rules[0].IgnoreDomainMatch || !loaded.Rules[0].AddUpstreamIPs {
		t.Fatalf("new options did not round-trip: %#v", loaded.Rules)
	}
}

func TestLoadUCIRejectsMalformedBoolean(t *testing.T) {
	configFile := filepath.Join(t.TempDir(), "updns")
	if err := os.WriteFile(configFile, []byte("config updns 'general'\noption use_local_resolv 'sometimes'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFromUCI(configFile); err == nil {
		t.Fatal("expected invalid boolean to fail")
	}
}
