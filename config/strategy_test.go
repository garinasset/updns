package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryStrategyConfiguration(t *testing.T) {
	strategies := map[string]QueryStrategy{
		"":  StrategyOptimal,
		"0": StrategyOptimal,
		"1": StrategyStrict,
		"2": StrategyConcurrent,
	}
	for _, value := range []string{"", "0", "1", "2", "optimal", "strict", "concurrent", "invalid", "-1", "3", "01", "1.0"} {
		t.Run(value, func(t *testing.T) {
			content := "config updns 'general'\n option use_local_resolv '0'\n"
			if value != "" {
				content += " option query_strategy '" + value + "'\n"
			}
			path := filepath.Join(t.TempDir(), "updns")
			if err := os.WriteFile(path, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFromUCI(path)
			want, valid := strategies[value]
			if !valid {
				if err == nil || cfg != nil || !strings.Contains(err.Error(), "query_strategy must be 0") {
					t.Fatal("invalid strategy was not reported")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.QueryStrategy != want {
				t.Fatalf("got %s want %s", cfg.QueryStrategy, want)
			}
			if err := SaveToUCI(cfg, path); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(saved), `option query_strategy "`+string(want)+`"`) {
				t.Fatalf("numeric strategy was not serialized: %s", saved)
			}
			reloaded, err := LoadFromUCI(path)
			if err != nil || reloaded.QueryStrategy != want {
				t.Fatalf("numeric strategy did not round trip: %#v %v", reloaded, err)
			}
		})
	}
}

func TestLocalDNSConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dhcp")
	content := `config dnsmasq
 option domain 'home'
 option expandhosts '1'
 option leasefile '/custom/leases'
 list addnhosts '/custom/hosts'
config dnsmasq
 option domain 'ignored'
config odhcpd
 option hostsdir '/custom/ipv6'
`
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadLocalDNSConfig(path)
	if err != nil || cfg.Domain != "home" || !cfg.ExpandHosts || cfg.LeaseFile != "/custom/leases" ||
		len(cfg.HostsFiles) != 2 || cfg.HostsFiles[1] != "/custom/hosts" || cfg.HostsDirs[len(cfg.HostsDirs)-1] != "/custom/ipv6" {
		t.Fatalf("local DNS configuration: %#v %v", cfg, err)
	}
}

func TestLegacyConcurrentFieldHasNoCompatibilityMapping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updns")
	if err := os.WriteFile(path, []byte("config updns 'general'\n option use_local_resolv '0'\n option concurrent_query '1'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFromUCI(path)
	if err != nil || cfg.QueryStrategy != StrategyOptimal {
		t.Fatalf("legacy field changed the new default: %#v %v", cfg, err)
	}
}
