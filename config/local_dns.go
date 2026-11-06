package config

import (
	"fmt"
	"os"
)

type LocalDNSConfig struct {
	HostsFiles  []string
	HostsDirs   []string
	LeaseFile   string
	Domain      string
	ExpandHosts bool
}

func DefaultLocalDNSConfig() LocalDNSConfig {
	return LocalDNSConfig{
		HostsFiles: []string{"/etc/hosts"},
		HostsDirs:  []string{"/tmp/hosts"},
		LeaseFile:  "/tmp/dhcp.leases",
		Domain:     "lan",
	}
}

// LoadLocalDNSConfig uses the first dnsmasq instance's local naming settings.
func LoadLocalDNSConfig(path string) (LocalDNSConfig, error) {
	cfg := DefaultLocalDNSConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read local DHCP configuration %s: %w", path, err)
	}
	sections, err := parseUCI(data)
	if err != nil {
		return cfg, fmt.Errorf("parse local DHCP configuration %s: %w", path, err)
	}
	found := false
	for _, section := range sections {
		if section.kind == "dnsmasq" && !found {
			found = true
			cfg.Domain = optionOr(section, "domain", cfg.Domain)
			cfg.LeaseFile = optionOr(section, "leasefile", cfg.LeaseFile)
			cfg.ExpandHosts, err = parseBoolOption(section, "expandhosts", cfg.ExpandHosts)
			if err != nil {
				return cfg, fmt.Errorf("invalid dnsmasq.expandhosts: %w", err)
			}
			cfg.HostsFiles = append(cfg.HostsFiles, listOption(section, "addnhosts")...)
			cfg.HostsDirs = append(cfg.HostsDirs, listOption(section, "hostsdir")...)
		}
		if section.kind == "odhcpd" {
			if dir := optionOr(section, "hostsdir", ""); dir != "" {
				cfg.HostsDirs = append(cfg.HostsDirs, dir)
			}
		}
	}
	return cfg, nil
}
