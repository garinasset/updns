package resolver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/garinasset/updns/logging"
	"github.com/miekg/dns"
)

// unreachablePause is how long an upstream is skipped after its network was
// found unreachable, before a single probe query is let through.
const unreachablePause = 2 * time.Second
const maxCNAMEHops = 16

var (
	// ErrUpstreamNotFound means a rule referenced an upstream that is not registered.
	ErrUpstreamNotFound = errors.New("upstream not found")
	// ErrUpstreamDown means the upstream was skipped because its network is unreachable.
	ErrUpstreamDown = errors.New("upstream paused: network unreachable")
	// ErrNetworkNotReady means every upstream failed only because the network
	// is unreachable or a local resolver is not available yet.
	ErrNetworkNotReady = errors.New("network not ready")
)

type notFoundError struct{ name string }

func (e *notFoundError) Error() string        { return fmt.Sprintf("upstream %s not found", e.name) }
func (e *notFoundError) Is(target error) bool { return target == ErrUpstreamNotFound }

type notReadyError struct{ msg string }

func (e *notReadyError) Error() string        { return e.msg }
func (e *notReadyError) Is(target error) bool { return target == ErrNetworkNotReady }

type upstreamHealth struct {
	until time.Time
}

// Resolver handles DNS resolution with support for multiple upstream protocols
type Resolver struct {
	mu         sync.RWMutex
	upstreams  map[string]*config.UpstreamDNS
	timeout    time.Duration
	httpClient *http.Client

	healthMu sync.Mutex
	down     map[string]*upstreamHealth
	now      func() time.Time
	pause    time.Duration
}

// NewResolver creates a new resolver
func NewResolver(timeout time.Duration) *Resolver {
	return &Resolver{
		upstreams:  make(map[string]*config.UpstreamDNS),
		timeout:    timeout,
		httpClient: http.DefaultClient,
		down:       make(map[string]*upstreamHealth),
		now:        time.Now,
		pause:      unreachablePause,
	}
}

// RegisterUpstream registers an upstream DNS server
func (r *Resolver) RegisterUpstream(upstream *config.UpstreamDNS) error {
	if upstream.Timeout == 0 {
		upstream.Timeout = 5
	}
	r.mu.Lock()
	r.upstreams[upstream.Name] = upstream
	r.mu.Unlock()
	return nil
}

// ReplaceUpstreamsWithPrefix atomically swaps all upstreams whose name starts
// with prefix for the given ones.
func (r *Resolver) ReplaceUpstreamsWithPrefix(prefix string, upstreams []*config.UpstreamDNS) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.healthMu.Lock()
	for name := range r.upstreams {
		if strings.HasPrefix(name, prefix) {
			delete(r.upstreams, name)
		}
	}
	for name := range r.down {
		if strings.HasPrefix(name, prefix) {
			delete(r.down, name)
		}
	}
	r.healthMu.Unlock()
	for _, upstream := range upstreams {
		if upstream.Timeout == 0 {
			upstream.Timeout = 5
		}
		r.upstreams[upstream.Name] = upstream
	}
}

// Resolve performs DNS resolution against a specific upstream
func (r *Resolver) Resolve(ctx context.Context, domain string, queryType uint16, upstreamName string) (*dns.Msg, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	upstream, exists := r.upstreams[upstreamName]
	r.mu.RUnlock()
	if !exists {
		return nil, &notFoundError{name: upstreamName}
	}
	if !r.allow(upstreamName) {
		return nil, ErrUpstreamDown
	}

	resp, err := r.query(ctx, domain, queryType, upstream)
	r.record(ctx, upstreamName, err)
	return resp, err
}

// allow reports whether a query may be sent. While an upstream is paused only
// one probe per pause window is let through.
func (r *Resolver) allow(name string) bool {
	r.healthMu.Lock()
	defer r.healthMu.Unlock()
	h := r.down[name]
	if h == nil {
		return true
	}
	now := r.now()
	if now.Before(h.until) {
		return false
	}
	h.until = now.Add(r.pause)
	return true
}

// record tracks reachability: an unreachable network pauses the upstream, any
// other outcome that proves packets can be sent clears the pause.
func (r *Resolver) record(ctx context.Context, name string, err error) {
	if isUnreachable(err) {
		r.healthMu.Lock()
		_, wasDown := r.down[name]
		r.down[name] = &upstreamHealth{until: r.now().Add(r.pause)}
		r.healthMu.Unlock()
		if wasDown {
			logging.Debugf("upstream %s still unreachable: %v", name, err)
		} else {
			logging.Warnf("upstream %s unreachable (%v), pausing queries for %s", name, err, r.pause)
		}
		return
	}
	if err != nil && ctx.Err() != nil {
		return
	}
	r.healthMu.Lock()
	_, wasDown := r.down[name]
	delete(r.down, name)
	r.healthMu.Unlock()
	if wasDown {
		logging.Infof("upstream %s recovered", name)
	}
}

func isUnreachable(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH)
}

// notReady reports a failure that is expected while the network or the local
// resolver file is not up yet. Unknown non-local upstreams are real errors.
func notReady(name string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ErrUpstreamDown), isUnreachable(err):
		return true
	case errors.Is(err, ErrUpstreamNotFound):
		return strings.HasPrefix(name, config.LocalResolverPrefix)
	}
	return false
}

func allUpstreamsFailed(domain string, failures []string, allNotReady bool) error {
	msg := fmt.Sprintf("all upstreams failed for %s: %s", domain, strings.Join(failures, "; "))
	if allNotReady {
		return &notReadyError{msg: msg}
	}
	return errors.New(msg)
}

// ResolveMultiple queries upstreams in order and returns the first valid
// final response, whatever its RCODE.
func (r *Resolver) ResolveMultiple(ctx context.Context, domain string, queryType uint16, upstreamNames []string) (*dns.Msg, string, error) {
	if len(upstreamNames) == 0 {
		return nil, "", fmt.Errorf("no upstreams specified")
	}

	var failures []string
	allNotReady := true
	for _, name := range upstreamNames {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		resp, err := r.Resolve(ctx, domain, queryType, name)
		if err == nil {
			return resp, name, nil
		}
		if !notReady(name, err) {
			allNotReady = false
		}
		failures = append(failures, upstreamFailure(name, resp, err))
	}

	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", allUpstreamsFailed(domain, failures, allNotReady)
}

// ResolveConcurrent queries all upstreams at once and returns the first valid
// final response, whatever its RCODE; the remaining queries are canceled.
func (r *Resolver) ResolveConcurrent(ctx context.Context, domain string, queryType uint16, upstreamNames []string) (*dns.Msg, string, error) {
	if len(upstreamNames) == 0 {
		return nil, "", fmt.Errorf("no upstreams specified")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	type answer struct {
		msg  *dns.Msg
		name string
		err  error
	}
	respChan := make(chan answer, len(upstreamNames))
	for _, name := range upstreamNames {
		go func(upstreamName string) {
			resp, err := r.Resolve(ctx, domain, queryType, upstreamName)
			respChan <- answer{resp, upstreamName, err}
		}(name)
	}

	var failures []string
	allNotReady := true
	for i := 0; i < len(upstreamNames); i++ {
		select {
		case result := <-respChan:
			if err := ctx.Err(); err != nil {
				return nil, "", err
			}
			if result.err == nil {
				cancel()
				return result.msg, result.name, nil
			}
			if !notReady(result.name, result.err) {
				allNotReady = false
			}
			failures = append(failures, upstreamFailure(result.name, result.msg, result.err))
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}

	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return nil, "", allUpstreamsFailed(domain, failures, allNotReady)
}

func upstreamFailure(name string, response *dns.Msg, err error) string {
	if err != nil {
		return fmt.Sprintf("%s: %v", name, err)
	}
	if response == nil {
		return fmt.Sprintf("%s: no response", name)
	}
	if rcode, ok := dns.RcodeToString[response.Rcode]; ok {
		return fmt.Sprintf("%s: DNS response %s", name, rcode)
	}
	return fmt.Sprintf("%s: DNS response code %d", name, response.Rcode)
}

// query performs the actual DNS query based on protocol
func (r *Resolver) query(ctx context.Context, domain string, queryType uint16, upstream *config.UpstreamDNS) (*dns.Msg, error) {
	msg := new(dns.Msg)
	msg.SetQuestion(dns.Fqdn(domain), queryType)
	msg.RecursionDesired = true

	timeout := time.Duration(upstream.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	question := msg.Question[0]
	var combined *dns.Msg
	for {
		resp, err := r.exchange(ctx, msg, upstream, timeout)
		if err != nil {
			return nil, err
		}
		if err := validateReply(msg, resp); err != nil {
			return nil, err
		}
		if resp.Truncated {
			return nil, errors.New("DNS response is still truncated")
		}
		if combined == nil {
			combined = resp
		} else {
			combined.Answer = append(combined.Answer, resp.Answer...)
			combined.Ns = resp.Ns
			combined.Extra = resp.Extra
			combined.Rcode = resp.Rcode
			// A locally assembled response is not a single authenticated reply.
			combined.AuthenticatedData = false
			combined.Authoritative = false
		}
		next, err := pendingTerminal(question, msg.Question[0].Name, combined)
		if err != nil {
			return nil, err
		}
		if next == "" {
			normalizeNegativeSOA(question, combined)
			return combined, nil
		}
		msg = new(dns.Msg)
		msg.SetQuestion(next, queryType)
		msg.RecursionDesired = true
	}
}

func (r *Resolver) exchange(ctx context.Context, msg *dns.Msg, upstream *config.UpstreamDNS, timeout time.Duration) (*dns.Msg, error) {
	var resp *dns.Msg
	var err error
	switch upstream.Protocol {
	case "tcp", "TCP":
		resp, err = exchangeDNS(ctx, msg, withDefaultPort(upstream.Address, "53"), "tcp", timeout)
	case "dot", "DOT":
		resp, err = exchangeDNS(ctx, msg, withDefaultPort(upstream.Address, "853"), "tcp-tls", timeout)
	case "doh", "DOH":
		resp, err = r.queryDoH(ctx, msg, upstream.Address)
	default:
		address := withDefaultPort(upstream.Address, "53")
		resp, err = exchangeDNS(ctx, msg, address, "udp", timeout)
		if err == nil && resp.Truncated {
			if err = validateReply(msg, resp); err == nil {
				resp, err = exchangeDNS(ctx, msg, address, "tcp", timeout)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func exchangeDNS(ctx context.Context, msg *dns.Msg, address, network string, timeout time.Duration) (*dns.Msg, error) {
	client := &dns.Client{Net: network, Timeout: timeout}
	conn, err := client.DialContext(ctx, address)
	if err != nil {
		return nil, fmt.Errorf("%s dial failed for %s: %w", network, address, err)
	}
	defer conn.Close()
	// This DNS library applies context deadlines but does not interrupt an
	// established connection on cancellation.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	resp, _, err := client.ExchangeWithConnContext(ctx, msg, conn)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, fmt.Errorf("%s query failed for %s: %w", network, address, err)
	}

	return resp, nil
}

// queryDoH performs DNS resolution over HTTPS (DoH)
func (r *Resolver) queryDoH(ctx context.Context, msg *dns.Msg, address string) (*dns.Msg, error) {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid DoH URL %q", address)
	}

	wire, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack DoH query for %s: %w", address, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(wire))
	if err != nil {
		return nil, fmt.Errorf("create DoH request for %s: %w", address, err)
	}
	request.Header.Set("Content-Type", "application/dns-message")
	request.Header.Set("Accept", "application/dns-message")

	response, err := r.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("DoH query failed for %s: %w", address, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("DoH query failed for %s: HTTP %s", address, response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 65536))
	if err != nil {
		return nil, fmt.Errorf("read DoH response from %s: %w", address, err)
	}
	if len(body) > 65535 {
		return nil, fmt.Errorf("DoH response from %s exceeds the DNS message size limit", address)
	}
	result := new(dns.Msg)
	if err := result.Unpack(body); err != nil {
		return nil, fmt.Errorf("unpack DoH response from %s: %w", address, err)
	}
	return result, nil
}

func withDefaultPort(address, port string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	host := strings.Trim(address, "[]")
	if ip := net.ParseIP(host); ip != nil {
		return net.JoinHostPort(host, port)
	}
	return net.JoinHostPort(address, port)
}

// validateReply checks that a reply answers the query, whatever its RCODE.
func validateReply(query, response *dns.Msg) error {
	if response == nil {
		return errors.New("no DNS response")
	}
	if !response.Response || response.Id != query.Id || response.Opcode != query.Opcode {
		return errors.New("DNS response header does not match query")
	}
	if len(response.Question) != 1 {
		return fmt.Errorf("DNS response %s must contain the original question (question count: %d)",
			rcodeText(response.Rcode), len(response.Question))
	}
	want, got := query.Question[0], response.Question[0]
	if !strings.EqualFold(want.Name, got.Name) || want.Qtype != got.Qtype || want.Qclass != got.Qclass {
		return fmt.Errorf("DNS response %s question does not match query", rcodeText(response.Rcode))
	}
	return nil
}

func rcodeText(rcode int) string {
	if text, ok := dns.RcodeToString[rcode]; ok {
		return text
	}
	return fmt.Sprintf("RCODE %d", rcode)
}

// validateResponse reports whether a single reply is a final response.
func validateResponse(query, response *dns.Msg) error {
	if err := validateReply(query, response); err != nil {
		return err
	}
	next, err := pendingTerminal(query.Question[0], query.Question[0].Name, response)
	if err != nil {
		return err
	}
	if next != "" {
		return fmt.Errorf("DNS response ends with an unresolved CNAME chain at %s", next)
	}
	return nil
}

// pendingTerminal returns the CNAME chain terminal still to be queried, or ""
// once the response is final. asked is the name of the latest query sent.
func pendingTerminal(question dns.Question, asked string, response *dns.Msg) (string, error) {
	if response.Truncated {
		return "", errors.New("DNS response is still truncated")
	}
	terminal, positive, err := responseTerminal(question, response)
	if err != nil {
		return "", err
	}
	switch response.Rcode {
	case dns.RcodeSuccess:
		if positive || coveringSOA(question, terminal, response) {
			return "", nil
		}
		if !strings.EqualFold(terminal, asked) {
			return terminal, nil
		}
		if isReferral(response) {
			return "", errors.New("DNS response is a referral, not a final answer")
		}
	case dns.RcodeNameError:
		if positive {
			return "", errors.New("NXDOMAIN response contains a positive answer")
		}
	}
	return "", nil
}

func coveringSOA(question dns.Question, terminal string, response *dns.Msg) bool {
	for _, rr := range response.Ns {
		if soa, ok := rr.(*dns.SOA); ok && soa.Hdr.Class == question.Qclass &&
			dns.IsSubDomain(strings.ToLower(soa.Hdr.Name), strings.ToLower(terminal)) {
			return true
		}
	}
	return false
}

// isReferral reports delegation NS records without an SOA (RFC 2308 type 4).
func isReferral(response *dns.Msg) bool {
	referral := false
	for _, rr := range response.Ns {
		switch rr.(type) {
		case *dns.SOA:
			return false
		case *dns.NS:
			referral = true
		}
	}
	return referral
}

// negativeTerminal reports a NODATA/NXDOMAIN response and its chain terminal.
func negativeTerminal(question dns.Question, response *dns.Msg) (string, bool) {
	if response.Rcode != dns.RcodeSuccess && response.Rcode != dns.RcodeNameError {
		return "", false
	}
	terminal, positive, err := responseTerminal(question, response)
	return terminal, err == nil && !positive
}

// normalizeNegativeSOA caps the SOA TTL of a negative response at SOA.MINIMUM.
func normalizeNegativeSOA(question dns.Question, response *dns.Msg) {
	terminal, negative := negativeTerminal(question, response)
	if !negative || !coveringSOA(question, terminal, response) {
		return
	}
	for _, rr := range response.Ns {
		if soa, ok := rr.(*dns.SOA); ok && soa.Minttl < soa.Hdr.Ttl {
			soa.Hdr.Ttl = soa.Minttl
		}
	}
}

func responseTerminal(question dns.Question, response *dns.Msg) (string, bool, error) {
	name := strings.ToLower(question.Name)
	seen := make(map[string]bool)
	for {
		if seen[name] {
			return "", false, errors.New("DNS response contains a CNAME loop")
		}
		if len(seen) > maxCNAMEHops {
			return "", false, fmt.Errorf("DNS response exceeds %d CNAME hops", maxCNAMEHops)
		}
		seen[name] = true
		target := ""
		for _, rr := range response.Answer {
			h := rr.Header()
			if !strings.EqualFold(h.Name, name) || h.Class != question.Qclass {
				continue
			}
			if h.Rrtype == question.Qtype || question.Qtype == dns.TypeANY {
				return name, true, nil
			}
			if cname, ok := rr.(*dns.CNAME); ok {
				target = strings.ToLower(cname.Target)
			}
		}
		if target == "" {
			return name, false, nil
		}
		name = target
	}
}

// ExtractIPs extracts IPv4 and IPv6 addresses from DNS response
func ExtractIPs(msg *dns.Msg) ([]string, []string) {
	var ipv4s, ipv6s []string

	for _, answer := range msg.Answer {
		switch rr := answer.(type) {
		case *dns.A:
			ipv4s = append(ipv4s, rr.A.String())
		case *dns.AAAA:
			ipv6s = append(ipv6s, rr.AAAA.String())
		case *dns.CNAME:
			// Could recursively resolve CNAME but for now just store it
		}
	}

	return ipv4s, ipv6s
}

// GetMinTTL returns the cache lifetime of a response; 0 means uncacheable.
// Error RCODEs are never cached, and a negative answer is cacheable only with
// an SOA covering the chain terminal (RFC 2308).
func GetMinTTL(msg *dns.Msg) uint32 {
	if msg.Rcode != dns.RcodeSuccess && msg.Rcode != dns.RcodeNameError {
		return 0
	}
	var minTTL uint32
	found := false
	for _, answer := range msg.Answer {
		ttl := answer.Header().Ttl
		if !found || ttl < minTTL {
			minTTL = ttl
			found = true
		}
	}
	if len(msg.Question) != 1 {
		if found {
			return minTTL
		}
		return NegativeTTL(msg)
	}
	question := msg.Question[0]
	terminal, positive, err := responseTerminal(question, msg)
	if err != nil || positive && msg.Rcode != dns.RcodeSuccess {
		return 0
	}
	if positive {
		return minTTL
	}
	if !coveringSOA(question, terminal, msg) {
		return 0
	}
	if negativeTTL := NegativeTTL(msg); !found || negativeTTL < minTTL {
		return negativeTTL
	}
	return minTTL
}

// NegativeTTL returns the RFC 2308 negative-cache lifetime from the SOA.
func NegativeTTL(msg *dns.Msg) uint32 {
	var ttl uint32
	found := false
	for _, rr := range msg.Ns {
		soa, ok := rr.(*dns.SOA)
		if !ok {
			continue
		}
		candidate := soa.Hdr.Ttl
		if soa.Minttl < candidate {
			candidate = soa.Minttl
		}
		if !found || candidate < ttl {
			ttl = candidate
			found = true
		}
	}
	if !found {
		return 0
	}
	return ttl
}
