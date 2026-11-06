package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Config holds the main configuration
// LocalResolverPrefix is the name prefix of upstreams read from the local resolver file.
const LocalResolverPrefix = "local-resolv-"

type QueryStrategy string

const (
	StrategyOptimal    QueryStrategy = "0"
	StrategyStrict     QueryStrategy = "1"
	StrategyConcurrent QueryStrategy = "2"
)

func (s QueryStrategy) Valid() bool {
	return s == StrategyOptimal || s == StrategyStrict || s == StrategyConcurrent
}

type Config struct {
	Enabled         bool
	Port            int
	UseLocalResolv  bool
	LocalResolvFile string
	QueryStrategy   QueryStrategy
	CacheEnabled    bool
	CacheSize       int
	ActivePreload   bool
	LogQuery        bool
	LogLevel        string
	UpstreamDNS     []UpstreamDNS
	DomainLists     []DomainList
	Rules           []Rule
}

// UpstreamDNS represents a DNS upstream server
type UpstreamDNS struct {
	Name     string // Identifier
	Address  string // IP or URL
	Protocol string // udp, tcp, dot, doh
	Timeout  int    // seconds
}

// DomainList identifies a domain-list file by its unique configured name.
type DomainList struct {
	Name string
	File string
}

// Rule represents a domain matching rule
type Rule struct {
	ID                string // Rule identifier
	IgnoreDomainMatch bool
	DomainLists       []string // Domain list names to match against
	Upstreams         []string // References to UpstreamDNS.Name
	Action            string   // ipv4+ipv6, ipv4only, ipv6only, nxdomain, zeroip
	IPv4NFTSets       []string // nftset names for IPv4 results
	IPv6NFTSets       []string // nftset names for IPv6 results
	AddUpstreamIPs    bool     // Add selected upstream endpoint IPs to the rule's nftsets
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		Enabled:         false,
		Port:            153,
		UseLocalResolv:  true,
		LocalResolvFile: "/tmp/resolv.conf.d/resolv.conf.auto",
		QueryStrategy:   StrategyOptimal,
		CacheEnabled:    true,
		CacheSize:       4096,
		ActivePreload:   true,
		LogQuery:        false,
		LogLevel:        "info",
		DomainLists:     []DomainList{},
		Rules:           []Rule{},
	}
}

type uciSection struct {
	kind    string
	name    string
	options map[string]string
	lists   map[string][]string
}

// LoadFromUCI parses the standard OpenWrt UCI config format.
func LoadFromUCI(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("read UCI config %s: %w", configPath, err)
	}

	sections, err := parseUCI(data)
	if err != nil {
		return nil, fmt.Errorf("parse UCI config %s: %w", configPath, err)
	}

	var upstreams []UpstreamDNS
	for _, section := range sections {
		switch section.kind {
		case "updns":
			if section.name != "general" {
				continue
			}
			cfg.Enabled, err = parseBoolOption(section, "enabled", cfg.Enabled)
			if err == nil {
				cfg.Port, err = parseIntOption(section, "port", cfg.Port)
			}
			if err == nil {
				cfg.UseLocalResolv, err = parseBoolOption(section, "use_local_resolv", cfg.UseLocalResolv)
			}
			if err == nil {
				cfg.LocalResolvFile = optionOr(section, "local_resolv_file", cfg.LocalResolvFile)
				cfg.QueryStrategy = QueryStrategy(optionOr(section, "query_strategy", string(cfg.QueryStrategy)))
				if !cfg.QueryStrategy.Valid() {
					err = fmt.Errorf("query_strategy must be 0 (smart selection), 1 (strict order) or 2 (concurrent query)")
				}
			}
			if err == nil {
				cfg.CacheEnabled, err = parseBoolOption(section, "cache_enabled", cfg.CacheEnabled)
			}
			if err == nil {
				cfg.CacheSize, err = parseIntOption(section, "cache_size", cfg.CacheSize)
			}
			if err == nil {
				cfg.ActivePreload, err = parseBoolOption(section, "active_preload", cfg.ActivePreload)
			}
			if err == nil {
				cfg.LogQuery, err = parseBoolOption(section, "log_query", cfg.LogQuery)
			}
			cfg.LogLevel = optionOr(section, "log_level", cfg.LogLevel)
			if err != nil {
				return nil, fmt.Errorf("invalid updns.general option: %w", err)
			}
			if cfg.Port < 1 || cfg.Port > 65535 {
				return nil, fmt.Errorf("updns.general.port must be between 1 and 65535")
			}
			if cfg.CacheSize < 1 {
				return nil, fmt.Errorf("updns.general.cache_size must be greater than zero")
			}
		case "upstream":
			timeout, parseErr := parseIntOption(section, "timeout", 5)
			if parseErr != nil || timeout < 1 {
				return nil, fmt.Errorf("invalid timeout for upstream %q", section.name)
			}
			address := optionOr(section, "address", "")
			protocol := strings.ToLower(optionOr(section, "protocol", "udp"))
			if address == "" || (protocol != "udp" && protocol != "tcp" && protocol != "dot" && protocol != "doh") {
				return nil, fmt.Errorf("upstream %q needs a valid address and protocol", section.name)
			}
			upstreams = append(upstreams, UpstreamDNS{
				Name:     optionOr(section, "name", section.name),
				Address:  address,
				Protocol: protocol,
				Timeout:  timeout,
			})
		case "domainlist":
			name := strings.TrimSpace(optionOr(section, "name", ""))
			file := optionOr(section, "file", optionOr(section, "path", ""))
			if name == "" || file == "" {
				return nil, fmt.Errorf("domain list section %q needs a unique name and file", section.name)
			}
			for _, existing := range cfg.DomainLists {
				if strings.EqualFold(existing.Name, name) {
					return nil, fmt.Errorf("duplicate domain list name %q", name)
				}
			}
			cfg.DomainLists = append(cfg.DomainLists, DomainList{Name: name, File: file})
		case "rule":
			domainMatch, parseErr := parseBoolOption(section, "domain_match", true)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid domain_match for rule %q: %w", section.name, parseErr)
			}
			addUpstreamIPs, parseErr := parseBoolOption(section, "add_upstream_ips", false)
			if parseErr != nil {
				return nil, fmt.Errorf("invalid add_upstream_ips for rule %q: %w", section.name, parseErr)
			}
			if !domainMatch {
				for _, existing := range cfg.Rules {
					if existing.IgnoreDomainMatch {
						return nil, fmt.Errorf("only one fallback rule is allowed")
					}
				}
			}
			cfg.Rules = append(cfg.Rules, Rule{
				ID:                optionOr(section, "name", optionOr(section, "id", section.name)),
				IgnoreDomainMatch: !domainMatch,
				DomainLists:       listOption(section, "domain_lists", "domain_list", "lists"),
				Upstreams:         listOption(section, "upstreams", "upstream"),
				Action:            optionOr(section, "action", "ipv4+ipv6"),
				IPv4NFTSets:       listOption(section, "ipv4_nftsets", "ipv4_nftset"),
				IPv6NFTSets:       listOption(section, "ipv6_nftsets", "ipv6_nftset"),
				AddUpstreamIPs:    addUpstreamIPs,
			})
		}
	}

	cfg.UpstreamDNS = nil
	for _, upstream := range upstreams {
		cfg.UpstreamDNS = upsertUpstream(cfg.UpstreamDNS, upstream)
	}
	if cfg.UseLocalResolv {
		cfg, err = loadLocalResolvers(cfg)
		if err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// SaveToUCI serializes configuration using OpenWrt's UCI syntax.
func SaveToUCI(cfg *Config, configPath string) error {
	var content strings.Builder

	content.WriteString("config updns 'general'\n")
	writeOption(&content, "enabled", boolString(cfg.Enabled))
	writeOption(&content, "port", strconv.Itoa(cfg.Port))
	writeOption(&content, "use_local_resolv", boolString(cfg.UseLocalResolv))
	writeOption(&content, "local_resolv_file", cfg.LocalResolvFile)
	writeOption(&content, "query_strategy", string(cfg.QueryStrategy))
	writeOption(&content, "cache_enabled", boolString(cfg.CacheEnabled))
	writeOption(&content, "cache_size", strconv.Itoa(cfg.CacheSize))
	writeOption(&content, "active_preload", boolString(cfg.ActivePreload))
	writeOption(&content, "log_query", boolString(cfg.LogQuery))
	writeOption(&content, "log_level", cfg.LogLevel)

	for _, upstream := range cfg.UpstreamDNS {
		if strings.HasPrefix(upstream.Name, LocalResolverPrefix) {
			continue
		}
		content.WriteString(fmt.Sprintf("\nconfig upstream %s\n", strconv.Quote(upstream.Name)))
		writeOption(&content, "name", upstream.Name)
		writeOption(&content, "address", upstream.Address)
		writeOption(&content, "protocol", strings.ToLower(upstream.Protocol))
		writeOption(&content, "timeout", strconv.Itoa(upstream.Timeout))
	}
	for _, list := range cfg.DomainLists {
		content.WriteString(fmt.Sprintf("\nconfig domainlist %s\n", strconv.Quote(list.Name)))
		writeOption(&content, "name", list.Name)
		writeOption(&content, "file", list.File)
	}
	for _, rule := range cfg.Rules {
		content.WriteString(fmt.Sprintf("\nconfig rule %s\n", strconv.Quote(rule.ID)))
		writeOption(&content, "name", rule.ID)
		writeOption(&content, "domain_match", boolString(!rule.IgnoreDomainMatch))
		writeList(&content, "domain_lists", rule.DomainLists)
		writeList(&content, "upstreams", rule.Upstreams)
		writeList(&content, "ipv4_nftsets", rule.IPv4NFTSets)
		writeList(&content, "ipv6_nftsets", rule.IPv6NFTSets)
		writeOption(&content, "add_upstream_ips", boolString(rule.AddUpstreamIPs))
		writeOption(&content, "action", rule.Action)
	}
	return os.WriteFile(configPath, []byte(content.String()), 0644)
}

func parseUCI(data []byte) ([]uciSection, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	sections := make([]uciSection, 0)
	var current *uciSection
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		fields, err := splitUCIWords(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "config":
			if len(fields) < 2 || len(fields) > 3 {
				return nil, fmt.Errorf("line %d: malformed config declaration", lineNumber)
			}
			name := ""
			if len(fields) == 3 {
				name = fields[2]
			}
			current = &uciSection{
				kind:    fields[1],
				name:    name,
				options: make(map[string]string),
				lists:   make(map[string][]string),
			}
			sections = append(sections, *current)
			current = &sections[len(sections)-1]
		case "option", "list":
			if current == nil || len(fields) != 3 {
				return nil, fmt.Errorf("line %d: malformed %s declaration", lineNumber, fields[0])
			}
			if fields[0] == "option" {
				current.options[fields[1]] = fields[2]
			} else {
				current.lists[fields[1]] = append(current.lists[fields[1]], fields[2])
			}
		default:
			return nil, fmt.Errorf("line %d: unknown directive %q", lineNumber, fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return sections, nil
}

func splitUCIWords(line string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	inWord := false
	for _, r := range line {
		if escaped {
			word.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			inWord = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == '#' && !inWord:
			return words, nil
		case r == ' ' || r == '\t' || r == '\r':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated escape or quote")
	}
	if inWord {
		words = append(words, word.String())
	}
	return words, nil
}

func parseBool(value string) (bool, error) {
	switch strings.ToLower(value) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean %q", value)
	}
}

func parseBoolOption(section uciSection, key string, fallback bool) (bool, error) {
	value, ok := section.options[key]
	if !ok {
		return fallback, nil
	}
	return parseBool(value)
}

func parseIntOption(section uciSection, key string, fallback int) (int, error) {
	value, ok := section.options[key]
	if !ok {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", key, err)
	}
	return n, nil
}

func optionOr(section uciSection, key, fallback string) string {
	if value, ok := section.options[key]; ok {
		return value
	}
	return fallback
}

func listOption(section uciSection, keys ...string) []string {
	var values []string
	for _, key := range keys {
		values = append(values, section.lists[key]...)
		if value, ok := section.options[key]; ok {
			for _, item := range strings.Split(value, ",") {
				item = strings.TrimSpace(item)
				if item != "" {
					values = append(values, item)
				}
			}
		}
	}
	return values
}

func loadLocalResolvers(cfg *Config) (*Config, error) {
	localResolvers, err := ParseLocalResolvers(cfg.LocalResolvFile, cfg.UpstreamDNS)
	if err != nil {
		return nil, err
	}
	cfg.UpstreamDNS = append(localResolvers, cfg.UpstreamDNS...)
	return cfg, nil
}

// ParseLocalResolvers reads nameservers from a resolv.conf style file and
// returns them as UDP upstreams named local-resolv-N. Addresses already used by
// the given upstreams are skipped. A missing file yields no upstreams.
func ParseLocalResolvers(path string, existing []UpstreamDNS) ([]UpstreamDNS, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open local resolver file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	var localResolvers []UpstreamDNS
	seen := make(map[string]bool)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 || fields[0] != "nameserver" {
			continue
		}
		if seen[fields[1]] || containsUpstream(existing, fields[1]) {
			continue
		}
		seen[fields[1]] = true
		localResolvers = append(localResolvers, UpstreamDNS{
			Name:     fmt.Sprintf("%s%d", LocalResolverPrefix, len(localResolvers)+1),
			Address:  fields[1],
			Protocol: "udp",
			Timeout:  5,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read local resolver file: %w", err)
	}
	return localResolvers, nil
}

func upsertUpstream(upstreams []UpstreamDNS, replacement UpstreamDNS) []UpstreamDNS {
	for i := range upstreams {
		if upstreams[i].Name == replacement.Name {
			upstreams[i] = replacement
			return upstreams
		}
	}
	return append(upstreams, replacement)
}

func containsUpstream(upstreams []UpstreamDNS, address string) bool {
	for _, upstream := range upstreams {
		if upstream.Address == address {
			return true
		}
	}
	return false
}

func writeOption(content *strings.Builder, key, value string) {
	fmt.Fprintf(content, "\toption %s %s\n", key, strconv.Quote(value))
}

func writeList(content *strings.Builder, key string, values []string) {
	for _, value := range values {
		fmt.Fprintf(content, "\tlist %s %s\n", key, strconv.Quote(value))
	}
}

func boolString(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
