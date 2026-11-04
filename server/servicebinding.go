package server

import (
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

// HTTPS/SVCB TargetName handling. Clients resolve a service target name
// themselves, so the target is bound to the rule that produced the record:
// the client's follow-up queries for the target then use that rule's
// upstreams and its addresses land in that rule's nftsets.

const (
	maxServiceHops         = 16
	minServiceBindingTTL   = 30 * time.Second
	defaultServiceBindings = 4096
	serviceTargetPrefetch  = 8
)

type boundRule struct {
	id     string
	expire time.Time
}

type serviceBinding struct {
	rules []boundRule // in bind order; the first one supplies upstreams and action
	hop   int
}

type serviceBindings struct {
	mu      sync.Mutex
	limit   int
	entries map[string]*serviceBinding
	size    atomic.Int64 // lets lookups skip the lock while nothing is bound
}

func newServiceBindings(limit int) *serviceBindings {
	if limit <= 0 {
		limit = defaultServiceBindings
	}
	return &serviceBindings{limit: limit, entries: make(map[string]*serviceBinding)}
}

// pruneLocked drops expired rules of one entry and reports whether it is still bound.
func (b *serviceBindings) pruneLocked(name string, entry *serviceBinding, now time.Time) bool {
	kept := entry.rules[:0]
	for _, rule := range entry.rules {
		if now.Before(rule.expire) {
			kept = append(kept, rule)
		}
	}
	entry.rules = kept
	if len(kept) == 0 {
		delete(b.entries, name)
		b.size.Store(int64(len(b.entries)))
		return false
	}
	return true
}

func (b *serviceBindings) lookup(name string, now time.Time) ([]string, int, bool) {
	if b.size.Load() == 0 {
		return nil, 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[name]
	if entry == nil || !b.pruneLocked(name, entry, now) {
		return nil, 0, false
	}
	ids := make([]string, len(entry.rules))
	for i, rule := range entry.rules {
		ids[i] = rule.id
	}
	return ids, entry.hop, true
}

func (b *serviceBindings) bound(name string, now time.Time) bool {
	_, _, ok := b.lookup(name, now)
	return ok
}

// bind adds or refreshes ruleID for name. added reports a new rule for the
// name; ok is false when the table is full and name is not yet bound.
func (b *serviceBindings) bind(name, ruleID string, ttl time.Duration, hop int, now time.Time) (added, ok bool) {
	if ttl < minServiceBindingTTL {
		ttl = minServiceBindingTTL
	}
	expire := now.Add(ttl)
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entries[name]
	if entry != nil && !b.pruneLocked(name, entry, now) {
		entry = nil
	}
	if entry == nil {
		if len(b.entries) >= b.limit {
			for key, candidate := range b.entries {
				b.pruneLocked(key, candidate, now)
			}
			if len(b.entries) >= b.limit {
				return false, false
			}
		}
		b.entries[name] = &serviceBinding{rules: []boundRule{{id: ruleID, expire: expire}}, hop: hop}
		b.size.Store(int64(len(b.entries)))
		return true, true
	}
	if hop < entry.hop {
		entry.hop = hop
	}
	for i := range entry.rules {
		if entry.rules[i].id == ruleID {
			if expire.After(entry.rules[i].expire) {
				entry.rules[i].expire = expire
			}
			return false, true
		}
	}
	entry.rules = append(entry.rules, boundRule{id: ruleID, expire: expire})
	return true, true
}

// queryRoute is the rule context a query is resolved under.
type queryRoute struct {
	rule      *config.Rule
	ruleIDs   []string // every rule the name is routed for, first one in effect
	ipv4Sets  []string
	ipv6Sets  []string
	hop       int
	signature string // identifies the routing a cache entry was resolved under
}

func (s *Server) ruleByID(id string) *config.Rule {
	for i := range s.cfg.Rules {
		if s.cfg.Rules[i].ID == id {
			rule := s.cfg.Rules[i]
			return &rule
		}
	}
	return nil
}

func (s *Server) routeFor(domain string) queryRoute {
	own := s.findMatchingRule(domain)
	route := queryRoute{rule: own}
	if own != nil {
		route.ruleIDs = []string{own.ID}
		route.ipv4Sets, route.ipv6Sets = own.IPv4NFTSets, own.IPv6NFTSets
		route.signature = "r:" + own.ID
	}
	if own != nil && isLocalAnswerAction(own.Action) {
		return route
	}
	ids, hop, ok := s.bindings.lookup(domain, time.Now())
	if !ok {
		return route
	}
	if own != nil {
		ids = append(ids, own.ID)
	}
	var related []*config.Rule
	var relatedIDs []string
	for _, id := range ids {
		if slices.Contains(relatedIDs, id) {
			continue
		}
		if rule := s.ruleByID(id); rule != nil && !isLocalAnswerAction(rule.Action) {
			related = append(related, rule)
			relatedIDs = append(relatedIDs, id)
		}
	}
	if len(related) == 0 {
		return route
	}
	route.hop = hop
	if len(related) == 1 && own != nil && related[0].ID == own.ID {
		// Bound only to its own rule: routing is unchanged, keep cache entries valid.
		return route
	}
	route.rule, route.ruleIDs = related[0], relatedIDs
	route.ipv4Sets, route.ipv6Sets = nil, nil
	for _, rule := range related {
		route.ipv4Sets = appendUnique(route.ipv4Sets, rule.IPv4NFTSets...)
		route.ipv6Sets = appendUnique(route.ipv6Sets, rule.IPv6NFTSets...)
	}
	route.signature = "b:" + route.rule.ID + "|" + strings.Join(route.ipv4Sets, ",") + "|" + strings.Join(route.ipv6Sets, ",")
	return route
}

func appendUnique(list []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range list {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			list = append(list, value)
		}
	}
	return list
}

// filterServiceHints copies an HTTPS/SVCB record, removing address hints the
// rule action does not allow, and returns the kept hint addresses.
func filterServiceHints(rr dns.RR, allowIPv4, allowIPv6 bool) (dns.RR, []string, []string) {
	copied := dns.Copy(rr)
	var record *dns.SVCB
	switch typed := copied.(type) {
	case *dns.HTTPS:
		record = &typed.SVCB
	case *dns.SVCB:
		record = typed
	default:
		return copied, nil, nil
	}
	var ipv4s, ipv6s []string
	values := record.Value[:0:0]
	for _, value := range record.Value {
		switch hint := value.(type) {
		case *dns.SVCBIPv4Hint:
			if !allowIPv4 {
				continue
			}
			ipv4s = appendIPs(ipv4s, hint.Hint)
		case *dns.SVCBIPv6Hint:
			if !allowIPv6 {
				continue
			}
			ipv6s = appendIPs(ipv6s, hint.Hint)
		}
		values = append(values, value)
	}
	record.Value = values
	return copied, ipv4s, ipv6s
}

func appendIPs(list []string, ips []net.IP) []string {
	for _, ip := range ips {
		list = append(list, ip.String())
	}
	return list
}

// serviceTarget returns the name a client will resolve for an HTTPS/SVCB
// record, or "" when the record names no further host.
func serviceTarget(rr dns.RR) (string, bool) {
	var record *dns.SVCB
	switch typed := rr.(type) {
	case *dns.HTTPS:
		record = &typed.SVCB
	case *dns.SVCB:
		record = typed
	default:
		return "", false
	}
	if record.Target == "." || record.Target == "" {
		if record.Priority == 0 {
			return "", true // AliasMode "." means the service is unavailable
		}
		return strings.ToLower(record.Hdr.Name), true
	}
	return strings.ToLower(dns.Fqdn(record.Target)), true
}

// applyServiceBindings binds the target names of returned HTTPS/SVCB records
// to the query's rule, dropping records whose target cannot be bound.
func (s *Server) applyServiceBindings(result *QueryResult) {
	if result == nil || result.Rcode != dns.RcodeSuccess || !hasServiceRecords(result.Records) {
		return
	}
	route := s.routeFor(result.Domain)
	if route.rule == nil {
		return
	}
	qname := strings.ToLower(dns.Fqdn(result.Domain))
	hop := route.hop + 1
	for _, rr := range result.Records {
		if _, ok := rr.(*dns.CNAME); ok {
			hop++
		}
	}
	kept := make([]dns.RR, 0, len(result.Records))
	dropped := false
	for _, rr := range result.Records {
		target, ok := serviceTarget(rr)
		if !ok || target == "" || target == qname {
			kept = append(kept, rr)
			continue
		}
		ttl := time.Duration(rr.Header().Ttl) * time.Second
		if hop > maxServiceHops || !s.bindServiceTarget(target, route.ruleIDs, ttl, hop) {
			dropped = true
			continue
		}
		kept = append(kept, rr)
	}
	if dropped {
		result.Records = kept
	}
}

func hasServiceRecords(records []dns.RR) bool {
	for _, rr := range records {
		switch rr.(type) {
		case *dns.HTTPS, *dns.SVCB:
			return true
		}
	}
	return false
}

// bindServiceTarget binds target to every rule of the originating route, so
// chained targets keep the first rule's precedence and all rules' sets.
func (s *Server) bindServiceTarget(target string, ruleIDs []string, ttl time.Duration, hop int) bool {
	now := time.Now()
	own := s.findMatchingRule(target)
	if own != nil && isLocalAnswerAction(own.Action) {
		return false
	}
	before := s.routeFor(target).signature
	for _, id := range ruleIDs {
		if _, ok := s.bindings.bind(target, id, ttl, hop, now); !ok {
			return false
		}
	}
	if s.routeFor(target).signature != before {
		s.prefetchServiceTarget(target)
	}
	return true
}

// prefetchServiceTarget warms the cache for a newly bound target without
// delaying the HTTPS/SVCB answer; it is skipped when enough are in flight.
func (s *Server) prefetchServiceTarget(target string) {
	route := s.routeFor(target)
	action := "ipv4+ipv6"
	if route.rule != nil {
		action = normalizeAction(route.rule.Action)
	}
	var types []uint16
	if action == "ipv4+ipv6" || action == "ipv4only" {
		types = append(types, dns.TypeA)
	}
	if action == "ipv4+ipv6" || action == "ipv6only" {
		types = append(types, dns.TypeAAAA)
	}
	for _, queryType := range types {
		select {
		case s.targetPrefetch <- struct{}{}:
		default:
			return
		}
		go func(queryType uint16) {
			defer func() { <-s.targetPrefetch }()
			_, _ = s.queryFrom(target, queryType, "")
		}(queryType)
	}
}
