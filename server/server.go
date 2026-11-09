package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/garinasset/updns/cache"
	"github.com/garinasset/updns/config"
	"github.com/garinasset/updns/logging"
	"github.com/garinasset/updns/nftset"
	"github.com/garinasset/updns/resolver"
	"github.com/garinasset/updns/rules"
	"github.com/miekg/dns"
)

// Server represents the DNS server
type Server struct {
	cfg               *config.Config
	upstreamMu        sync.RWMutex
	resolver          *resolver.Resolver
	cache             *cache.Cache
	matcher           *rules.Matcher
	nftMgr            nftSetManager
	lookupIPs         func(context.Context, string) ([]net.IP, error)
	udpServer         *dns.Server
	tcpServer         *dns.Server
	controlListener   net.Listener
	controlSocketPath string
	stopChan          chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	preloadWG         sync.WaitGroup
	queryMu           sync.Mutex
	queryWG           sync.WaitGroup
	flights           map[string]*queryFlight
	preloading        int
	nftRefreshNow     chan struct{}
	logQuery          bool
	started           bool
	svcbFailMu        sync.Mutex
	svcbFailures      map[string]time.Time
	ptrFailures       *failureCache
	warnLimit         *warnLimiter
	bindings          *serviceBindings
	targetPrefetch    chan struct{}
	local             *localDNS
}

// HTTPS/SVCB queries are optional hints that some upstreams silently drop.
// They get a short overall deadline, and an all-upstreams-failed result is
// remembered (as NODATA) for svcbFailureTTL. See MEMO.md.
const svcbQueryTimeout = 5 * time.Second
const svcbFailureTTL = 5 * time.Minute

func isSVCBType(queryType uint16) bool {
	return queryType == dns.TypeHTTPS || queryType == dns.TypeSVCB
}

// svcbFailureActive reports whether a non-expired failure is cached, purging expired entries.
func (s *Server) svcbFailureActive(key string) bool {
	s.svcbFailMu.Lock()
	defer s.svcbFailMu.Unlock()
	now := time.Now()
	for k, expire := range s.svcbFailures {
		if !now.Before(expire) {
			delete(s.svcbFailures, k)
		}
	}
	_, ok := s.svcbFailures[key]
	return ok
}

func (s *Server) recordSVCBFailure(key string) {
	s.svcbFailMu.Lock()
	defer s.svcbFailMu.Unlock()
	if s.svcbFailures == nil {
		s.svcbFailures = make(map[string]time.Time)
	}
	s.svcbFailures[key] = time.Now().Add(svcbFailureTTL)
}

func (s *Server) clearSVCBFailures() {
	s.svcbFailMu.Lock()
	s.svcbFailures = nil
	s.svcbFailMu.Unlock()
	s.ptrFailures.clear()
}

const cacheStatusPath = "/tmp/updns_cache_size"
const upstreamNFTRefreshInterval = 5 * time.Minute
const upstreamNFTRetryInterval = 30 * time.Second
const localResolverWatchInterval = 5 * time.Second

func preloadConcurrency(cacheSize int) int {
	concurrency := cacheSize / 128
	if cacheSize%128 != 0 {
		concurrency++
	}
	if concurrency < 16 {
		return 16
	}
	if concurrency > 64 {
		return 64
	}
	return concurrency
}

type nftSetManager interface {
	AddIPsToSet([]string, string) error
	FlushSet(string) error
	GetSetContents(string) ([]string, error)
}

// QueryResult contains information about a DNS query result
type QueryResult struct {
	Domain       string          `json:"domain"`
	QueryType    string          `json:"query_type"`
	Answers      []string        `json:"answers"`
	CacheHit     bool            `json:"cache_hit"`
	PrefetchWait bool            `json:"prefetch_wait,omitempty"`
	LocalAnswer  bool            `json:"local_answer,omitempty"`
	MatchedRule  string          `json:"matched_rule"`
	Action       string          `json:"action"`
	IPv4NFTSets  []string        `json:"ipv4_nft_sets"`
	IPv6NFTSets  []string        `json:"ipv6_nft_sets"`
	IPv4InSets   map[string]bool `json:"ipv4_in_sets"`
	IPv6InSets   map[string]bool `json:"ipv6_in_sets"`
	UsedUpstream string          `json:"used_upstream"`
	Records      []dns.RR        `json:"-"`
	Authority    []dns.RR        `json:"-"`
	Rcode        int             `json:"rcode"`
	TTL          uint32          `json:"ttl"`
	// Error is a short, normalized failure reason; set only when the query failed.
	Error string `json:"error,omitempty"`
	route string
}

// NewServer creates a new DNS server
func NewServer(cfg *config.Config) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{
		cfg:           cfg,
		stopChan:      make(chan struct{}),
		ctx:           ctx,
		cancel:        cancel,
		nftRefreshNow: make(chan struct{}, 1),
		logQuery:      cfg.LogQuery,
		ptrFailures:   newFailureCache(ptrFailureTTL),
		warnLimit:     newWarnLimiter(warnInterval),
		bindings:      newServiceBindings(cfg.CacheSize),
		// Bounds concurrent background lookups of newly bound service targets.
		targetPrefetch: make(chan struct{}, serviceTargetPrefetch),
		local:          newLocalDNS("/etc/config/dhcp"),
	}

	// Initialize components
	s.controlSocketPath = controlSocketPath
	s.resolver = resolver.NewResolver(5 * time.Second)
	s.matcher = rules.NewMatcher()
	s.nftMgr = nftset.NewNFTSetManager()
	s.lookupIPs = func(ctx context.Context, host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip", host)
	}

	// Initialize cache if enabled
	if cfg.CacheEnabled {
		s.cache = cache.NewCache(cfg.CacheSize)
	}

	return s, nil
}

// RegisterUpstream registers an upstream DNS server
func (s *Server) RegisterUpstream(upstream *config.UpstreamDNS) error {
	return s.resolver.RegisterUpstream(upstream)
}

// LoadDomainList loads a domain list
func (s *Server) LoadDomainList(name, filename string) error {
	return s.matcher.LoadDomainListAs(name, filename)
}

// Start starts the DNS server
func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.cfg.Port)
	udpConn, err := net.ListenPacket("udp", addr)
	if err != nil {
		return fmt.Errorf("listen on UDP %s: %w", addr, err)
	}
	tcpListener, err := net.Listen("tcp", addr)
	if err != nil {
		udpConn.Close()
		return fmt.Errorf("listen on TCP %s: %w", addr, err)
	}
	controlListener, err := listenControlSocket(s.controlSocketPath)
	if err != nil {
		tcpListener.Close()
		udpConn.Close()
		return fmt.Errorf("listen on control socket %s: %w", s.controlSocketPath, err)
	}
	s.controlListener = controlListener

	mux := dns.NewServeMux()
	mux.HandleFunc(".", s.handleQuery)

	s.udpServer = &dns.Server{
		PacketConn: udpConn,
		Net:        "udp",
		Handler:    mux,
	}

	s.tcpServer = &dns.Server{
		Listener: tcpListener,
		Net:      "tcp",
		Handler:  mux,
	}

	s.started = true
	s.publishCacheSize()

	// Start UDP and TCP servers in goroutines
	go func() {
		if err := s.udpServer.ActivateAndServe(); err != nil {
			logging.Errorf("UDP server error: %v", err)
		}
	}()

	go func() {
		if err := s.tcpServer.ActivateAndServe(); err != nil {
			logging.Errorf("TCP server error: %v", err)
		}
	}()
	go s.serveControl()

	if s.cfg.ActivePreload && s.cache != nil {
		s.preloadWG.Add(1)
		go func() {
			defer s.preloadWG.Done()
			s.preloadLoop()
		}()
	}
	if s.cfg.UseLocalResolv {
		go s.watchLocalResolversLoop(localResolverWatchInterval)
	}
	if hasUpstreamNFTSetRules(s.cfg.Rules) {
		go s.refreshUpstreamNFTSetsLoop()
	}

	logging.Infof("DNS server started on %s (UDP/TCP)", addr)
	return nil
}

func hasUpstreamNFTSetRules(rules []config.Rule) bool {
	for _, rule := range rules {
		if rule.AddUpstreamIPs && !isLocalAnswerAction(rule.Action) &&
			(len(rule.IPv4NFTSets) > 0 || len(rule.IPv6NFTSets) > 0) {
			return true
		}
	}
	return false
}

// refreshUpstreamNFTSetsLoop refreshes upstream endpoint IPs periodically, on
// demand (for example when local resolvers change) and soon again after a
// failed attempt, such as when nftables sets or DNS are not ready yet.
func (s *Server) refreshUpstreamNFTSetsLoop() {
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
		case <-s.nftRefreshNow:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		case <-s.stopChan:
			return
		}

		next := upstreamNFTRefreshInterval
		if !s.refreshUpstreamNFTSets() {
			next = upstreamNFTRetryInterval
		}
		timer.Reset(next)
	}
}

// requestUpstreamNFTRefresh asks the refresh loop to run as soon as possible.
func (s *Server) requestUpstreamNFTRefresh() {
	select {
	case s.nftRefreshNow <- struct{}{}:
	default:
	}
}

// refreshUpstreamNFTSets reports whether every lookup and nftables update succeeded.
func (s *Server) refreshUpstreamNFTSets() bool {
	complete := true
	configured := s.upstreamsSnapshot()
	upstreams := make(map[string]config.UpstreamDNS, len(configured))
	for _, upstream := range configured {
		upstreams[upstream.Name] = upstream
	}

	resolved := make(map[string][]net.IP)
	failed := make(map[string]bool)
	ipv4BySet := make(map[string]map[string]struct{})
	ipv6BySet := make(map[string]map[string]struct{})

	for _, rule := range s.cfg.Rules {
		if !rule.AddUpstreamIPs || isLocalAnswerAction(rule.Action) {
			continue
		}
		for _, name := range rule.Upstreams {
			upstream, found := upstreams[name]
			if !found {
				logging.Warnf("cannot add upstream IPs for rule %s: upstream %s is not configured", rule.ID, name)
				continue
			}
			ips, lookedUp := resolved[name]
			if !lookedUp && !failed[name] {
				var err error
				ips, err = s.resolveUpstreamEndpointIPs(upstream)
				if err != nil {
					logging.Warnf("failed to resolve upstream %s endpoint for nftsets: %v", name, err)
					complete = false
					failed[name] = true
					continue
				}
				resolved[name] = ips
			}
			for _, ip := range ips {
				if ipv4 := ip.To4(); ipv4 != nil {
					for _, setName := range rule.IPv4NFTSets {
						if ipv4BySet[setName] == nil {
							ipv4BySet[setName] = make(map[string]struct{})
						}
						ipv4BySet[setName][ipv4.String()] = struct{}{}
					}
				} else if ipv6 := ip.To16(); ipv6 != nil {
					for _, setName := range rule.IPv6NFTSets {
						if ipv6BySet[setName] == nil {
							ipv6BySet[setName] = make(map[string]struct{})
						}
						ipv6BySet[setName][ipv6.String()] = struct{}{}
					}
				}
			}
		}
	}

	addResolvedIPs := func(setName string, addresses map[string]struct{}) {
		if len(addresses) == 0 {
			return
		}
		existing, err := s.nftMgr.GetSetContents(setName)
		if err != nil {
			logging.Warnf("failed to inspect nftset %s before adding upstream IPs: %v", setName, err)
			complete = false
		} else {
			for _, ip := range existing {
				delete(addresses, ip)
			}
		}
		if len(addresses) == 0 {
			return
		}
		ips := make([]string, 0, len(addresses))
		for ip := range addresses {
			ips = append(ips, ip)
		}
		if err := s.nftMgr.AddIPsToSet(ips, setName); err != nil {
			logging.Warnf("failed to add upstream IPs to nftset %s: %v", setName, err)
			complete = false
		}
	}
	for setName, addresses := range ipv4BySet {
		addResolvedIPs(setName, addresses)
	}
	for setName, addresses := range ipv6BySet {
		addResolvedIPs(setName, addresses)
	}
	return complete
}

func (s *Server) resolveUpstreamEndpointIPs(upstream config.UpstreamDNS) ([]net.IP, error) {
	host, err := upstreamEndpointHost(upstream)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.lookupIPs(ctx, host)
}

func upstreamEndpointHost(upstream config.UpstreamDNS) (string, error) {
	if strings.EqualFold(upstream.Protocol, "doh") {
		endpoint, err := url.Parse(upstream.Address)
		if err != nil {
			return "", fmt.Errorf("invalid DoH URL %q: %w", upstream.Address, err)
		}
		if endpoint.Hostname() == "" {
			return "", fmt.Errorf("DoH URL %q has no host", upstream.Address)
		}
		return endpoint.Hostname(), nil
	}

	if host, _, err := net.SplitHostPort(upstream.Address); err == nil {
		return host, nil
	}
	host := strings.Trim(upstream.Address, "[]")
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if strings.Contains(host, ":") {
		return "", fmt.Errorf("invalid upstream address %q", upstream.Address)
	}
	if host == "" {
		return "", fmt.Errorf("upstream address is empty")
	}
	return host, nil
}

// Stop stops the DNS server
func (s *Server) Stop() error {
	close(s.stopChan)
	s.cancel()

	if s.controlListener != nil {
		s.controlListener.Close()
	}
	if s.udpServer != nil {
		s.udpServer.Shutdown()
	}
	if s.tcpServer != nil {
		s.tcpServer.Shutdown()
	}
	s.preloadWG.Wait()
	s.queryMu.Lock()
	for _, flight := range s.flights {
		flight.cancel()
	}
	s.queryMu.Unlock()
	s.queryWG.Wait()
	if s.cache != nil {
		s.cache.Stop()
	}
	if s.started {
		if err := os.Remove(cacheStatusPath); err != nil && !os.IsNotExist(err) {
			logging.Warnf("failed to remove cache status file: %v", err)
		}
	}

	return nil
}

// Query resolves a domain and reports the cache and rule metadata.
func (s *Server) Query(domain string, queryType uint16) (*QueryResult, error) {
	return s.queryFrom(domain, queryType, "")
}

// queryFrom is Query with the requesting client's address, used for logging.
func (s *Server) queryFrom(domain string, queryType uint16, client string) (*QueryResult, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	domain = strings.ToLower(dns.Fqdn(domain))
	if result, found := s.queryLocal(domain, queryType); found {
		s.discardLocalCache(domain, queryType)
		s.applyServiceBindings(result)
		return result, nil
	}
	result, err := s.queryRouted(domain, queryType, client)
	// A binding created while this query was resolving changes its routing;
	// resolve again so the returned addresses reach the current nftsets.
	if err == nil && result != nil && s.bindings.size.Load() > 0 && result.route != s.routeFor(domain).signature {
		result, err = s.resolveQueryDetailContext(s.ctx, domain, queryType, client)
	}
	if err == nil {
		s.applyServiceBindings(result)
	}
	return result, err
}

func (s *Server) queryRouted(domain string, queryType uint16, client string) (*QueryResult, error) {
	cacheKey := fmt.Sprintf("%s:%d", domain, queryType)
	route := s.routeFor(domain)
	if route.rule != nil && isLocalAnswerAction(route.rule.Action) {
		s.discardLocalCache(domain, queryType)
		return s.resolveQueryDetailContext(s.ctx, domain, queryType, client)
	}
	s.queryMu.Lock()
	if err := s.ctx.Err(); err != nil {
		s.queryMu.Unlock()
		return nil, err
	}
	if s.cache != nil {
		// An entry resolved under different routing (a service binding was
		// added or expired) is a miss, so its addresses reach the current sets.
		if entry, found := s.cache.GetForRoute(cacheKey, route.signature); found {
			s.queryMu.Unlock()
			result := s.metadataFor(domain, queryType, route)
			result.CacheHit = true
			result.Records = entry.Answers
			result.Authority = entry.Authority
			result.UsedUpstream = entry.Upstream
			result.Rcode = entry.Rcode
			result.TTL = entry.TTL
			for _, rr := range result.Records {
				switch record := rr.(type) {
				case *dns.A:
					result.Answers = append(result.Answers, record.A.String())
				case *dns.AAAA:
					result.Answers = append(result.Answers, record.AAAA.String())
				}
			}
			s.populateNFTMembership(result)
			return result, nil
		}
	}
	flight := s.flights[cacheKey]
	if flight != nil && flight.route != route.signature {
		s.queryMu.Unlock()
		return s.resolveQueryDetailContext(s.ctx, domain, queryType, client)
	}
	waited := flight != nil && flight.preload
	if flight == nil {
		var previous *cache.CacheEntry
		var epoch uint64
		if s.cache != nil {
			previous, epoch = s.cache.Snapshot(cacheKey)
			s.cache.Suspend(cacheKey)
		}
		flight = s.startQueryLocked(cacheKey, domain, queryType, client, route.signature, previous, epoch, false)
	}
	s.queryMu.Unlock()
	select {
	case <-flight.done:
		if flight.result == nil {
			if waited {
				result := s.queryMetadata(domain, queryType)
				result.PrefetchWait = true
				result.Rcode = dns.RcodeServerFailure
				result.Error = failureReason(flight.err)
				return result, flight.err
			}
			return nil, flight.err
		}
		result := cloneQueryResult(flight.result)
		result.PrefetchWait = waited
		return result, flight.err
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	}
}

func (s *Server) preloadLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			s.preloadCacheEntries()
		case <-s.stopChan:
			return
		}
	}
}

func (s *Server) preloadCacheEntries() {
	s.queryMu.Lock()
	defer s.queryMu.Unlock()
	limit := preloadConcurrency(s.cfg.CacheSize)
	if s.cache == nil || s.ctx.Err() != nil || s.preloading >= limit {
		return
	}
	entries, epoch := s.cache.TakePreloads(limit - s.preloading)
	for _, entry := range entries {
		key := fmt.Sprintf("%s:%d", entry.Query, entry.QueryType)
		route := s.routeFor(entry.Query)
		if route.rule != nil && isLocalAnswerAction(route.rule.Action) {
			s.cache.Remove(key)
			if s.started {
				s.publishCacheSize()
			}
			continue
		}
		if s.flights[key] == nil {
			s.startQueryLocked(key, entry.Query, entry.QueryType, "", route.signature, entry, epoch, true)
		}
	}
}

func (s *Server) queryMetadata(domain string, queryType uint16) *QueryResult {
	return s.metadataFor(domain, queryType, s.routeFor(domain))
}

func (s *Server) metadataFor(domain string, queryType uint16, route queryRoute) *QueryResult {
	result := &QueryResult{
		Domain:     domain,
		QueryType:  dns.TypeToString[queryType],
		Action:     "ipv4+ipv6",
		IPv4InSets: make(map[string]bool),
		IPv6InSets: make(map[string]bool),
		route:      route.signature,
	}
	if rule := route.rule; rule != nil {
		result.MatchedRule = rule.ID
		result.Action = rule.Action
		result.IPv4NFTSets = route.ipv4Sets
		result.IPv6NFTSets = route.ipv6Sets
	}
	return result
}

// handleQuery handles incoming DNS queries
func (s *Server) handleQuery(w dns.ResponseWriter, r *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(r)
	m.Compress = false

	client := clientAddr(w.RemoteAddr())
	ecs := clientSubnet(r)
	for _, q := range r.Question {
		domain := q.Name
		queryType := q.Qtype

		result, err := s.queryFrom(domain, queryType, client)
		if s.logQuery {
			logResult := result
			if err != nil && logResult == nil {
				logResult = s.queryMetadata(domain, queryType)
				logResult.Rcode = dns.RcodeServerFailure
			}
			logging.Infof("%s", FormatQueryLog("SRV", logResult, client, ecs))
		}
		if err != nil {
			m.SetRcode(r, dns.RcodeServerFailure)
			continue
		}
		m.Answer = append(m.Answer, result.Records...)
		m.Ns = append(m.Ns, result.Authority...)
		m.Rcode = result.Rcode
	}

	w.WriteMsg(m)
}

// FormatQueryLog renders one query log line with optional client and ECS fields.
func FormatQueryLog(kind string, result *QueryResult, client, ecs string) string {
	path := result.Path()
	rcode := result.Rcode
	if result.Error != "" {
		rcode = dns.RcodeServerFailure
	}
	rcodeText, ok := dns.RcodeToString[rcode]
	if !ok {
		rcodeText = fmt.Sprintf("RCODE%d", rcode)
	}
	line := fmt.Sprintf("QUERY [%s] %s %-4s → %s → %s → %s",
		kind,
		result.Domain,
		result.QueryType,
		logValue(result.MatchedRule),
		path,
		rcodeText,
	)
	if client != "" {
		line += " @" + client
	}
	if ecs != "" {
		line += " #" + ecs
	}
	return line
}

func logValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func clientSubnet(message *dns.Msg) string {
	edns := message.IsEdns0()
	if edns == nil {
		return ""
	}

	for _, option := range edns.Option {
		subnet, ok := option.(*dns.EDNS0_SUBNET)
		if !ok {
			continue
		}

		var address netip.Addr
		switch subnet.Family {
		case 1:
			if subnet.SourceNetmask > 32 || subnet.Address.To4() == nil {
				continue
			}
			address, _ = netip.AddrFromSlice(subnet.Address.To4())
		case 2:
			if subnet.SourceNetmask > 128 || subnet.Address.To4() != nil {
				continue
			}
			address, _ = netip.AddrFromSlice(subnet.Address.To16())
		default:
			continue
		}
		if !address.IsValid() {
			continue
		}

		return netip.PrefixFrom(address, int(subnet.SourceNetmask)).Masked().String()
	}
	return ""
}

// failureRule names the rule a failed query was handled by.
func failureRule(result *QueryResult) string {
	if result.MatchedRule == "" {
		return "default upstreams"
	}
	return result.MatchedRule
}

// clientAddr returns the host part of a client's network address.
func clientAddr(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}

// ruleContext renders the "(rule: ..., client: ...)" detail of failure logs.
func ruleContext(rule, client string) string {
	if client == "" {
		return "rule: " + rule
	}
	return "rule: " + rule + ", client: " + client
}

// formatSuppressed renders the suppressed-warning tail, listing the clients of
// the dropped warnings when known.
func formatSuppressed(summary warnSummary, interval time.Duration) string {
	text := fmt.Sprintf("suppressed %d similar in last %s", summary.Suppressed, interval)
	if len(summary.Clients) == 0 {
		return text
	}
	count := fmt.Sprintf("%d", len(summary.Clients))
	if summary.MoreClients {
		count += "+"
	}
	return fmt.Sprintf("%s from %s clients: %s", text, count, strings.Join(summary.Clients, ", "))
}

// logResolveFailure keeps "network not ready" failures at debug level (the
// resolver already warns once per upstream state change), keeps PTR failures at
// debug level (broken reverse zones of remote hosts are not actionable), and
// rate-limits the remaining warnings per rule.
func (s *Server) logResolveFailure(domain, queryType, rule, client string, err error) {
	detail := ruleContext(rule, client)
	if errors.Is(err, resolver.ErrNetworkNotReady) {
		logging.Debugf("DNS resolution failed for %s %s (%s), network not ready: %v", domain, queryType, detail, err)
		return
	}
	if queryType == dns.TypeToString[dns.TypePTR] {
		logging.Debugf("DNS resolution failed for %s %s (%s): %v", domain, queryType, detail, err)
		return
	}
	ok, summary := s.warnLimit.allow(rule, client)
	if !ok {
		logging.Debugf("DNS resolution failed for %s %s (%s): %v", domain, queryType, detail, err)
		return
	}
	if summary.Suppressed > 0 {
		logging.Warnf("DNS resolution failed for %s %s (%s): %v (%s)", domain, queryType, detail, err, formatSuppressed(summary, s.warnLimit.interval))
		return
	}
	logging.Warnf("DNS resolution failed for %s %s (%s): %v", domain, queryType, detail, err)
}

// resolveQuery resolves a single query and returns the result
func (s *Server) resolveQuery(domain string, queryType uint16) *QueryResult {
	return s.resolveQueryFrom(domain, queryType, "")
}

// resolveQueryFrom is resolveQuery with the requesting client's address, used
// for failure logs.
func (s *Server) resolveQueryFrom(domain string, queryType uint16, client string) *QueryResult {
	result, _ := s.resolveQueryDetail(domain, queryType, client)
	return result
}

// resolveQueryDetail also returns the failure cause when the result is nil.
func (s *Server) resolveQueryDetail(domain string, queryType uint16, client string) (*QueryResult, error) {
	return s.resolveQueryDetailContext(s.ctx, domain, queryType, client)
}

func (s *Server) resolveQueryDetailContext(ctx context.Context, domain string, queryType uint16, client string) (*QueryResult, error) {
	return s.resolveQueryWithUpstreamContext(ctx, domain, queryType, client, "")
}

func (s *Server) resolveQueryWithUpstreamContext(ctx context.Context, domain string, queryType uint16, client, preferred string) (*QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	route := s.routeFor(domain)
	result := s.metadataFor(domain, queryType, route)
	result.Rcode = dns.RcodeSuccess

	matchedRule := route.rule
	if matchedRule != nil {
		switch normalizeAction(result.Action) {
		case "nxdomain":
			result.Rcode = dns.RcodeNameError
			result.TTL = localAnswerTTL
			return result, nil
		case "zeroip":
			result.TTL = localAnswerTTL
			switch queryType {
			case dns.TypeA, dns.TypeANY:
				rr := &dns.A{Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: localAnswerTTL}, A: net.IPv4zero.To4()}
				result.Records = append(result.Records, rr)
				result.Answers = append(result.Answers, "0.0.0.0")
			}
			switch queryType {
			case dns.TypeAAAA, dns.TypeANY:
				rr := &dns.AAAA{Hdr: dns.RR_Header{Name: domain, Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: localAnswerTTL}, AAAA: net.IPv6zero}
				result.Records = append(result.Records, rr)
				result.Answers = append(result.Answers, "::")
			}
			return result, nil
		}
	}
	if local, found := s.queryLocal(domain, queryType); found {
		return local, nil
	}

	failKey := fmt.Sprintf("%s:%d", domain, queryType)
	svcb := isSVCBType(queryType)
	if svcb && s.svcbFailureActive(failKey) {
		return result, nil
	}
	ptr := queryType == dns.TypePTR
	if ptr && s.ptrFailures.active(failKey) {
		logging.Debugf("DNS resolution failed for %s %s (%s), recent failure cached for %s", domain, result.QueryType, ruleContext(failureRule(result), client), ptrFailureTTL)
		return nil, errCachedFailure
	}

	// Other types have no overall deadline: each upstream uses its own timeout.
	if svcb {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, svcbQueryTimeout)
		defer cancel()
	}

	var resp *dns.Msg
	var err error

	var upstreamNames []string
	if matchedRule == nil {
		// Use default upstreams
		defaults := s.upstreamsSnapshot()
		if len(defaults) == 0 {
			return nil, errNoUpstream
		}

		upstreamNames = make([]string, len(defaults))
		for i, upstream := range defaults {
			upstreamNames[i] = upstream.Name
		}

	} else {
		upstreamNames = matchedRule.Upstreams
	}
	resp, result.UsedUpstream, err = s.resolver.ResolveStrategy(ctx, domain, queryType, upstreamNames, s.cfg.QueryStrategy, preferred)

	if err != nil || resp == nil {
		rule := failureRule(result)
		if svcb && err != nil {
			s.recordSVCBFailure(failKey)
			logging.Debugf("DNS resolution failed for %s %s (rule: %s), cached as empty answer for %s: %v", domain, result.QueryType, rule, svcbFailureTTL, err)
			result.UsedUpstream = ""
			return result, nil
		}
		if ptr && !errors.Is(err, resolver.ErrNetworkNotReady) {
			s.ptrFailures.record(failKey)
		}
		s.logResolveFailure(domain, result.QueryType, rule, client, err)
		if err == nil {
			err = errors.New("no response")
		}
		return nil, err
	}

	s.processResponse(result, queryType, resp)
	return result, nil
}

func (s *Server) processResponse(result *QueryResult, queryType uint16, resp *dns.Msg) {
	result.Rcode = resp.Rcode
	action := normalizeAction(result.Action)
	allowIPv4 := action == "ipv4+ipv6" || action == "ipv4only"
	allowIPv6 := action == "ipv4+ipv6" || action == "ipv6only"
	var ipv4s, ipv6s []string
	for _, rr := range resp.Answer {
		switch record := rr.(type) {
		case *dns.A:
			if allowIPv4 && (queryType == dns.TypeA || queryType == dns.TypeANY) {
				result.Records = append(result.Records, dns.Copy(rr))
				ipv4s = append(ipv4s, record.A.String())
				result.Answers = append(result.Answers, record.A.String())
			}
		case *dns.AAAA:
			if allowIPv6 && (queryType == dns.TypeAAAA || queryType == dns.TypeANY) {
				result.Records = append(result.Records, dns.Copy(rr))
				ipv6s = append(ipv6s, record.AAAA.String())
				result.Answers = append(result.Answers, record.AAAA.String())
			}
		case *dns.HTTPS, *dns.SVCB:
			filtered, hint4, hint6 := filterServiceHints(rr, allowIPv4, allowIPv6)
			result.Records = append(result.Records, filtered)
			ipv4s = append(ipv4s, hint4...)
			ipv6s = append(ipv6s, hint6...)
		default:
			result.Records = append(result.Records, dns.Copy(rr))
		}
	}
	result.Authority = append(result.Authority, resp.Ns...)
	result.TTL = resolver.GetMinTTL(resp)

	// Error RCODE responses are returned verbatim but their addresses are not trusted.
	if resp.Rcode != dns.RcodeSuccess {
		ipv4s, ipv6s = nil, nil
	}
	if len(result.IPv4NFTSets) > 0 && len(ipv4s) > 0 {
		for _, setName := range result.IPv4NFTSets {
			if err := s.nftMgr.AddIPsToSet(ipv4s, setName); err != nil {
				logging.Warnf("failed to add resolved IPv4 addresses to nftset %s: %v", setName, err)
				continue
			}
			result.IPv4InSets[setName] = true
		}
	}

	if len(result.IPv6NFTSets) > 0 && len(ipv6s) > 0 {
		for _, setName := range result.IPv6NFTSets {
			if err := s.nftMgr.AddIPsToSet(ipv6s, setName); err != nil {
				logging.Warnf("failed to add resolved IPv6 addresses to nftset %s: %v", setName, err)
				continue
			}
			result.IPv6InSets[setName] = true
		}
	}

}

var (
	errCachedFailure = errors.New("recent failure cached")
	errNoUpstream    = errors.New("no upstream configured")
)

// failureReason maps a resolution error to a short, normalized reason.
func failureReason(err error) string {
	if err == nil {
		return "Unknown error"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, errNoUpstream):
		return "No upstream configured"
	case errors.Is(err, resolver.ErrNetworkNotReady):
		return "Network not ready"
	case errors.Is(err, resolver.ErrUpstreamNotFound), strings.Contains(msg, "not found"):
		return "Upstream not found"
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "server misbehaving"):
		return "Upstream host unresolvable"
	case strings.Contains(msg, "connection refused"):
		return "Upstream connection refused"
	case strings.Contains(msg, "tls:"), strings.Contains(msg, "x509"):
		return "Upstream TLS error"
	case strings.Contains(msg, "http "):
		return "Upstream HTTP error"
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "deadline exceeded"):
		return "Upstream timeout"
	case strings.Contains(msg, "unreachable"):
		return "Network unreachable"
	case strings.Contains(msg, "dns response"):
		return "Upstream returned an invalid response"
	}
	return "All upstreams failed"
}

func (s *Server) populateNFTMembership(result *QueryResult) {
	var ipv4s, ipv6s []string
	for _, rr := range result.Records {
		switch record := rr.(type) {
		case *dns.A:
			ipv4s = append(ipv4s, record.A.String())
		case *dns.AAAA:
			ipv6s = append(ipv6s, record.AAAA.String())
		case *dns.HTTPS, *dns.SVCB:
			_, hint4, hint6 := filterServiceHints(rr, true, true)
			ipv4s = append(ipv4s, hint4...)
			ipv6s = append(ipv6s, hint6...)
		}
	}
	for _, setName := range result.IPv4NFTSets {
		result.IPv4InSets[setName] = s.allIPsInSet(setName, ipv4s)
	}
	for _, setName := range result.IPv6NFTSets {
		result.IPv6InSets[setName] = s.allIPsInSet(setName, ipv6s)
	}
}

func (s *Server) allIPsInSet(setName string, addresses []string) bool {
	if len(addresses) == 0 {
		return false
	}
	contents, err := s.nftMgr.GetSetContents(setName)
	if err != nil {
		logging.Warnf("failed to read nftset %s: %v", setName, err)
		return false
	}
	set := make(map[string]struct{}, len(contents))
	for _, address := range contents {
		set[address] = struct{}{}
	}
	for _, address := range addresses {
		if _, found := set[address]; !found {
			return false
		}
	}
	return true
}

// findMatchingRule finds the first matching rule for a domain
func (s *Server) findMatchingRule(domain string) *config.Rule {
	var fallback *config.Rule
	for _, rule := range s.cfg.Rules {
		if rule.IgnoreDomainMatch {
			matched := rule
			fallback = &matched
			continue
		}
		if s.matcher.MatchMultipleListsOR(domain, rule.DomainLists) {
			matched := rule
			return &matched
		}
	}

	return fallback
}

// localAnswerTTL is the TTL of answers a rule produces without querying upstreams.
const localAnswerTTL = 60

// isLocalAnswerAction reports whether a rule answers without upstreams or nftsets.
func isLocalAnswerAction(action string) bool {
	switch normalizeAction(action) {
	case "nxdomain", "zeroip":
		return true
	}
	return false
}

func normalizeAction(action string) string {
	switch action {
	case "ipv4-only", "ipv4_only", "ipv4only":
		return "ipv4only"
	case "ipv6-only", "ipv6_only", "ipv6only":
		return "ipv6only"
	case "nxdomain", "zeroip":
		return action
	case "", "ipv4+ipv6":
		return "ipv4+ipv6"
	default:
		return action
	}
}

// ClearCache clears the DNS cache
func (s *Server) ClearCache() {
	s.queryMu.Lock()
	defer s.queryMu.Unlock()
	for _, flight := range s.flights {
		flight.cancel()
	}
	s.flights = nil
	s.preloading = 0
	if s.cache != nil {
		s.cache.Clear()
		if s.started {
			s.publishCacheSize()
		}
	}
	s.clearSVCBFailures()
	s.resolver.ClearSelection()
}

// ClearNFTSets clears all nftsets referenced by rules
func (s *Server) ClearNFTSets() error {
	nftsetMap := make(map[string]bool)

	// Collect all nftsets from rules
	for _, rule := range s.cfg.Rules {
		for _, setName := range rule.IPv4NFTSets {
			nftsetMap[setName] = true
		}
		for _, setName := range rule.IPv6NFTSets {
			nftsetMap[setName] = true
		}
	}

	// Flush each nftset
	for setName := range nftsetMap {
		if err := s.nftMgr.FlushSet(setName); err != nil {
			return fmt.Errorf("failed to flush nftset %s: %w", setName, err)
		}
	}

	return nil
}

// GetCacheSize returns the current cache size
func (s *Server) GetCacheSize() int {
	if s.cache != nil {
		return s.cache.Size()
	}
	return 0
}

func (s *Server) publishCacheSize() {
	if err := os.WriteFile(cacheStatusPath, fmt.Appendf(nil, "%d\n", s.GetCacheSize()), 0644); err != nil {
		logging.Warnf("failed to publish cache size: %v", err)
	}
}

func (s *Server) upstreamsSnapshot() []config.UpstreamDNS {
	s.upstreamMu.RLock()
	defer s.upstreamMu.RUnlock()
	return append([]config.UpstreamDNS(nil), s.cfg.UpstreamDNS...)
}

func (s *Server) watchLocalResolversLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			s.refreshLocalResolvers()
		case <-s.stopChan:
			return
		}
	}
}

// refreshLocalResolvers re-reads the local resolver file so upstreams that
// appear later (for example after WAN comes up) or change are picked up
// without a service restart.
func (s *Server) refreshLocalResolvers() {
	current := s.upstreamsSnapshot()
	var configured, oldLocal []config.UpstreamDNS
	for _, upstream := range current {
		if strings.HasPrefix(upstream.Name, config.LocalResolverPrefix) {
			oldLocal = append(oldLocal, upstream)
		} else {
			configured = append(configured, upstream)
		}
	}

	newLocal, err := config.ParseLocalResolvers(s.cfg.LocalResolvFile, configured)
	if err != nil {
		logging.Warnf("failed to refresh local resolvers: %v", err)
		return
	}
	if sameUpstreams(oldLocal, newLocal) {
		return
	}

	pointers := make([]*config.UpstreamDNS, len(newLocal))
	for i := range newLocal {
		pointers[i] = &newLocal[i]
	}
	s.upstreamMu.Lock()
	s.cfg.UpstreamDNS = append(append([]config.UpstreamDNS(nil), newLocal...), configured...)
	s.upstreamMu.Unlock()
	s.resolver.ReplaceUpstreamsWithPrefix(config.LocalResolverPrefix, pointers)
	s.clearSVCBFailures()

	names := make([]string, len(newLocal))
	for i, upstream := range newLocal {
		names[i] = fmt.Sprintf("%s=%s", upstream.Name, upstream.Address)
	}
	logging.Infof("Local resolver upstreams updated (%d): %s", len(newLocal), strings.Join(names, ", "))
	s.requestUpstreamNFTRefresh()
}

func sameUpstreams(a, b []config.UpstreamDNS) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Address != b[i].Address {
			return false
		}
	}
	return true
}
