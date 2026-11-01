package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/garinasset/updns/config"
	"github.com/garinasset/updns/logging"
	"github.com/garinasset/updns/server"
	"github.com/miekg/dns"
)

var (
	configFile = flag.String("c", "/etc/config/updns", "Config file path")
	action     = flag.String("action", "", "Action: start, stop, restart, reload, query")
	domain     = flag.String("domain", "", "Domain to query (for query action)")
	verbose    = flag.Bool("v", false, "Verbose output")
)

func main() {
	flag.Parse()

	switch *action {
	case "start":
		startService()
	case "stop":
		stopService()
	case "restart":
		stopService()
		startService()
	case "reload":
		reloadService()
	case "clear":
		clearState()
	case "query":
		if *domain == "" {
			fatalf("domain is required for query action")
		}
		queryDomain(*domain)
	case "status":
		statusService()
	default:
		if flag.NArg() > 0 {
			// If first argument looks like a domain, treat as query
			queryDomain(flag.Arg(0))
		} else {
			flag.PrintDefaults()
		}
	}
}

func startService() {
	cfg, err := loadConfig()
	if err != nil {
		fatalf("Failed to load config: %v", err)
	}

	srv, err := server.NewServer(cfg)
	if err != nil {
		fatalf("Failed to create server: %v", err)
	}
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	// Register upstreams
	registerUpstreams(cfg, srv.RegisterUpstream, func(upstream *config.UpstreamDNS, err error) {
		logging.Warnf("failed to register upstream %s: %v", upstream.Name, err)
	})

	logStartupUpstreams(cfg)

	// Load domain lists
	for _, list := range cfg.DomainLists {
		if err := srv.LoadDomainList(list.Name, list.File); err != nil {
			logging.Warnf("failed to load domain list %s (%s): %v", list.Name, list.File, err)
		}
	}

	// Start server
	if err := srv.Start(); err != nil {
		fatalf("Failed to start server: %v", err)
	}
	defer func() {
		if err := srv.Stop(); err != nil {
			logging.Errorf("Failed to stop UpDNS service cleanly: %v", err)
		}
	}()

	logging.Infof("updns service started")
	<-signalCtx.Done()
	logging.Infof("stopping updns service")
}

// registerUpstreams passes each upstream by its own address; the register
// callback keeps the pointer, so it must not alias a shared loop variable.
func registerUpstreams(cfg *config.Config, register func(*config.UpstreamDNS) error, onError func(*config.UpstreamDNS, error)) {
	for i := range cfg.UpstreamDNS {
		upstream := &cfg.UpstreamDNS[i]
		if err := register(upstream); err != nil {
			onError(upstream, err)
		}
	}
}

func logStartupUpstreams(cfg *config.Config) {
	registered := make(map[string]bool, len(cfg.UpstreamDNS))
	names := make([]string, 0, len(cfg.UpstreamDNS))
	localCount := 0
	for _, upstream := range cfg.UpstreamDNS {
		registered[upstream.Name] = true
		names = append(names, fmt.Sprintf("%s=%s(%s)", upstream.Name, upstream.Address, upstream.Protocol))
		if strings.HasPrefix(upstream.Name, "local-resolv-") {
			localCount++
		}
	}
	logging.Infof("Registered %d upstreams: %s", len(names), strings.Join(names, ", "))

	if cfg.UseLocalResolv && localCount == 0 {
		logging.Warnf("local resolver file %s is enabled but missing or has no nameserver; rules referencing local-resolv-N fail until it appears (it is re-read every few seconds)", cfg.LocalResolvFile)
	}
	for _, rule := range cfg.Rules {
		for _, name := range rule.Upstreams {
			if !registered[name] {
				logging.Warnf("rule %q references unknown upstream %q", rule.ID, name)
			}
		}
	}
}

func stopService() {
	// In a real implementation, this would kill the running process
	fmt.Println("Stop service")
}

func reloadService() {
	// In a real implementation, this would reload config and restart
	fmt.Println("Reload service and clear cache")
}

func clearState() {
	cfg, err := loadConfig()
	if err != nil {
		fatalf("Failed to load config: %v", err)
	}
	srv, err := server.NewServer(cfg)
	if err != nil {
		fatalf("Failed to create server: %v", err)
	}
	defer srv.Stop()
	if err := srv.ClearNFTSets(); err != nil {
		fatalf("Failed to clear nftsets: %v", err)
	}
}

func statusService() {
	fmt.Println("Get service status")
}

func queryDomain(domain string) {
	results, err := server.QueryRunningService(domain)
	if err != nil {
		if !server.ControlSocketUnavailable(err) {
			fatalf("Failed to query UpDNS service: %v", err)
		}

		cfg, configErr := loadConfig()
		if configErr != nil {
			fatalf("Failed to load config: %v", configErr)
		}
		cfg.CacheEnabled = false

		srv, serverErr := server.NewServer(cfg)
		if serverErr != nil {
			fatalf("Failed to create standalone resolver: %v", serverErr)
		}
		defer srv.Stop()

		registerUpstreams(cfg, srv.RegisterUpstream, func(upstream *config.UpstreamDNS, err error) {
			fatalf("Failed to register upstream %s: %v", upstream.Name, err)
		})
		for _, list := range cfg.DomainLists {
			if err := srv.LoadDomainList(list.Name, list.File); err != nil {
				fatalf("Failed to load domain list %s (%s): %v", list.Name, list.File, err)
			}
		}

		results, err = srv.QueryDomain(domain)
		logStandaloneQueries(cfg.LogQuery && logLevelIncludesInfo(cfg.LogLevel), domain, results, err)
		if err != nil {
			fatalf("Failed to resolve %s: %v", domain, err)
		}
	}

	if len(results) == 0 {
		fatalf("No DNS results returned for %s", domain)
	}

	writeQueryOutput(os.Stdout, results)
}

func logStandaloneQueries(enabled bool, domain string, results []*server.QueryResult, queryErr error) {
	if !enabled {
		return
	}

	writer, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, "updns-cli")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cannot write standalone query to system log: %v\n", err)
		return
	}
	defer writer.Close()

	if len(results) == 0 && queryErr != nil {
		if err := writer.Info(fmt.Sprintf("CLI standalone query %s A/AAAA failed: %v", domain, queryErr)); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot write standalone query to system log: %v\n", err)
		}
		return
	}

	for _, result := range results {
		if err := writer.Info(fmt.Sprintf("CLI standalone %s", queryLogLine(result))); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot write standalone query to system log: %v\n", err)
			return
		}
	}
}

func logLevelIncludesInfo(level string) bool {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug", "info":
		return true
	default:
		return false
	}
}

func queryLogLine(result *server.QueryResult) string {
	path := "Upstream Query"
	if result.CacheHit {
		path = "Cache Hit"
	} else if strings.EqualFold(result.Action, "nxdomain") {
		path = "NXDOMAIN RULE"
	}
	return fmt.Sprintf("Query: %s %s (%s)", result.Domain, result.QueryType, path)
}

func loadConfig() (*config.Config, error) {
	cfg, err := config.LoadFromUCI(*configFile)
	if err != nil {
		return nil, err
	}
	if err := logging.SetLevel(cfg.LogLevel); err != nil {
		return nil, err
	}
	return cfg, nil
}

func fatalf(format string, args ...any) {
	logging.Errorf(format, args...)
	os.Exit(1)
}

const queryFieldWidth = 12

func writeQueryOutput(out io.Writer, results []*server.QueryResult) {
	domain := results[0].Domain

	writeSection(out, "DOMAIN")
	writeField(out, "Domain", domain)
	writeField(out, "Query Type", "A / AAAA")
	writeField(out, "Query Path", queryPath(results))

	writeSection(out, "DNS RESULT")
	writeField(out, "Matched Rule", matchedRule(results))
	writeField(out, "Action", displayAction(results[0].Action))
	writeField(out, "Response", responseCode(results))
	upstream := joinedUnique(results, func(result *server.QueryResult) string {
		return result.UsedUpstream
	})
	if upstream == "" {
		upstream = "(none)"
	}
	writeField(out, "Upstream", upstream)

	ipv4Sets, ipv4Membership := nftMembership(results, true)
	ipv6Sets, ipv6Membership := nftMembership(results, false)
	if len(ipv4Sets) > 0 || len(ipv6Sets) > 0 {
		writeSection(out, "NFT SETS")
		writeNFTField(out, "IPv4", ipv4Sets, ipv4Membership, addressFamilyState(results, "A"), "resolved IPv4")
		writeNFTField(out, "IPv6", ipv6Sets, ipv6Membership, addressFamilyState(results, "AAAA"), "resolved IPv6")
	}

	writeSection(out, "ANSWERS")
	answers := make([]string, 0)
	seenAnswers := make(map[string]bool)
	for _, result := range results {
		for _, answer := range result.Answers {
			if answer == "" || seenAnswers[answer] {
				continue
			}
			seenAnswers[answer] = true
			answers = append(answers, answer)
		}
	}
	if len(answers) == 0 {
		fmt.Fprintln(out, "  (none)")
	} else {
		for _, answer := range answers {
			fmt.Fprintf(out, "  • %s\n", answer)
		}
	}
	fmt.Fprintln(out)
}

func writeSection(out io.Writer, title string) {
	fmt.Fprintf(out, "\n  %s\n  ──────────────────────────────────────────\n", title)
}

func writeField(out io.Writer, label, value string) {
	fmt.Fprintf(out, "  %-*s : %s\n", queryFieldWidth, label, value)
}

func writeNFTField(out io.Writer, label string, sets []string, membership map[string]bool, hasAnswers *bool, addressFamily string) {
	value := "(none)"
	if len(sets) > 0 {
		value = strings.Join(sets, ", ")
	}
	writeField(out, label, value)
	if hasAnswers == nil {
		fmt.Fprintf(out, "                 — %s query unavailable\n", addressFamily)
		return
	}
	if !*hasAnswers {
		family := strings.TrimPrefix(addressFamily, "resolved ")
		fmt.Fprintf(out, "                 — No %s answer\n", family)
		return
	}
	for _, setName := range sets {
		detail := ""
		contains, doesNotContain := "Contains", "Does not contain"
		if len(sets) > 1 {
			detail = setName + " "
			contains, doesNotContain = "contains", "does not contain"
		}
		if membership[setName] {
			fmt.Fprintf(out, "                 ✓ %s%s %s\n", detail, contains, addressFamily)
		} else {
			fmt.Fprintf(out, "                 ✗ %s%s %s\n", detail, doesNotContain, addressFamily)
		}
	}
}

func addressFamilyState(results []*server.QueryResult, queryType string) *bool {
	for _, result := range results {
		if result.QueryType != queryType {
			continue
		}
		hasAnswers := len(result.Answers) > 0
		return &hasAnswers
	}
	return nil
}

func queryPath(results []*server.QueryResult) string {
	paths := make([]string, 0, len(results))
	for _, result := range results {
		path := "Upstream Query"
		if result.CacheHit {
			path = "Cache Hit"
		} else if strings.EqualFold(result.Action, "nxdomain") {
			path = "NXDOMAIN RULE"
		}
		paths = append(paths, result.QueryType+"="+path)
	}
	if len(paths) == 0 {
		return "Unavailable"
	}
	first := strings.SplitN(paths[0], "=", 2)[1]
	for _, path := range paths[1:] {
		if strings.SplitN(path, "=", 2)[1] != first {
			return strings.Join(paths, " / ")
		}
	}
	return first
}

func matchedRule(results []*server.QueryResult) string {
	value := joinedUnique(results, func(result *server.QueryResult) string { return result.MatchedRule })
	if value == "" {
		return "(default)"
	}
	return value
}

func displayAction(action string) string {
	switch strings.ToLower(action) {
	case "ipv4+ipv6":
		return "IPv4 + IPv6"
	case "ipv4only":
		return "IPv4 only"
	case "ipv6only":
		return "IPv6 only"
	case "nxdomain":
		return "NXDOMAIN"
	default:
		if action == "" {
			return "(default)"
		}
		return action
	}
}

func responseCode(results []*server.QueryResult) string {
	codes := make([]string, 0, len(results))
	for _, result := range results {
		name := dns.RcodeToString[result.Rcode]
		if name == "" {
			name = fmt.Sprintf("RCODE %d", result.Rcode)
		}
		codes = append(codes, result.QueryType+"="+name)
	}
	if len(codes) == 0 {
		return "Unavailable"
	}
	first := strings.SplitN(codes[0], "=", 2)[1]
	for _, code := range codes[1:] {
		if strings.SplitN(code, "=", 2)[1] != first {
			return strings.Join(codes, " / ")
		}
	}
	return first
}

func nftMembership(results []*server.QueryResult, ipv4 bool) ([]string, map[string]bool) {
	sets := make([]string, 0)
	membership := make(map[string]bool)
	for _, result := range results {
		resultSets, resultMembership := result.IPv6NFTSets, result.IPv6InSets
		if ipv4 {
			resultSets, resultMembership = result.IPv4NFTSets, result.IPv4InSets
		}
		for _, setName := range resultSets {
			if _, exists := membership[setName]; !exists {
				sets = append(sets, setName)
			}
			membership[setName] = membership[setName] || resultMembership[setName]
		}
	}
	return sets, membership
}

func joinedUnique(results []*server.QueryResult, value func(*server.QueryResult) string) string {
	seen := make(map[string]bool)
	values := make([]string, 0)
	for _, result := range results {
		for _, item := range strings.Split(value(result), ", ") {
			item = strings.TrimSpace(item)
			if item == "" || seen[item] {
				continue
			}
			seen[item] = true
			values = append(values, item)
		}
	}
	return strings.Join(values, ", ")
}
