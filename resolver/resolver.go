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

// ResolveMultiple resolves against multiple upstreams
// Returns the first successful response
func (r *Resolver) ResolveMultiple(ctx context.Context, domain string, queryType uint16, upstreamNames []string) (*dns.Msg, string, error) {
	if len(upstreamNames) == 0 {
		return nil, "", fmt.Errorf("no upstreams specified")
	}

	var failures []string
	allNotReady := true
	for _, name := range upstreamNames {
		resp, err := r.Resolve(ctx, domain, queryType, name)
		if err == nil && usableResponse(resp) {
			return resp, name, nil
		}
		if !notReady(name, err) {
			allNotReady = false
		}
		failures = append(failures, upstreamFailure(name, resp, err))
	}

	return nil, "", allUpstreamsFailed(domain, failures, allNotReady)
}

// ResolveConcurrent resolves against multiple upstreams concurrently
// Returns the first successful response
func (r *Resolver) ResolveConcurrent(ctx context.Context, domain string, queryType uint16, upstreamNames []string) (*dns.Msg, string, error) {
	if len(upstreamNames) == 0 {
		return nil, "", fmt.Errorf("no upstreams specified")
	}

	respChan := make(chan struct {
		msg  *dns.Msg
		name string
		err  error
	}, len(upstreamNames))

	// Launch concurrent queries
	for _, name := range upstreamNames {
		go func(upstreamName string) {
			resp, err := r.Resolve(ctx, domain, queryType, upstreamName)
			respChan <- struct {
				msg  *dns.Msg
				name string
				err  error
			}{resp, upstreamName, err}
		}(name)
	}

	// Wait for first successful response
	var failures []string
	allNotReady := true
	for i := 0; i < len(upstreamNames); i++ {
		select {
		case result := <-respChan:
			if result.err == nil && usableResponse(result.msg) {
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

	switch upstream.Protocol {
	case "udp", "UDP":
		return r.queryUDP(ctx, msg, upstream.Address, timeout)
	case "tcp", "TCP":
		return r.queryTCP(ctx, msg, upstream.Address, timeout)
	case "dot", "DOT":
		return r.queryDoT(ctx, msg, upstream.Address, timeout)
	case "doh", "DOH":
		return r.queryDoH(ctx, msg, upstream.Address, timeout)
	default:
		return r.queryUDP(ctx, msg, upstream.Address, timeout)
	}
}

// queryUDP performs DNS resolution over UDP
func (r *Resolver) queryUDP(ctx context.Context, msg *dns.Msg, address string, timeout time.Duration) (*dns.Msg, error) {
	address = withDefaultPort(address, "53")

	client := &dns.Client{
		Net:     "udp",
		Timeout: timeout,
	}

	resp, _, err := client.ExchangeContext(ctx, msg, address)
	if err != nil {
		return nil, fmt.Errorf("UDP query failed for %s: %w", address, err)
	}

	return resp, nil
}

// queryTCP performs DNS resolution over TCP
func (r *Resolver) queryTCP(ctx context.Context, msg *dns.Msg, address string, timeout time.Duration) (*dns.Msg, error) {
	address = withDefaultPort(address, "53")

	client := &dns.Client{
		Net:     "tcp",
		Timeout: timeout,
	}

	resp, _, err := client.ExchangeContext(ctx, msg, address)
	if err != nil {
		return nil, fmt.Errorf("TCP query failed for %s: %w", address, err)
	}

	return resp, nil
}

// queryDoT performs DNS resolution over TLS (DoT)
func (r *Resolver) queryDoT(ctx context.Context, msg *dns.Msg, address string, timeout time.Duration) (*dns.Msg, error) {
	address = withDefaultPort(address, "853")

	client := &dns.Client{
		Net:     "tcp-tls",
		Timeout: timeout,
	}

	resp, _, err := client.ExchangeContext(ctx, msg, address)
	if err != nil {
		return nil, fmt.Errorf("DoT query failed for %s: %w", address, err)
	}

	return resp, nil
}

// queryDoH performs DNS resolution over HTTPS (DoH)
func (r *Resolver) queryDoH(ctx context.Context, msg *dns.Msg, address string, timeout time.Duration) (*dns.Msg, error) {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return nil, fmt.Errorf("invalid DoH URL %q", address)
	}

	wire, err := msg.Pack()
	if err != nil {
		return nil, fmt.Errorf("pack DoH query for %s: %w", address, err)
	}

	requestContext := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		requestContext, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint.String(), bytes.NewReader(wire))
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

func usableResponse(response *dns.Msg) bool {
	return response != nil && response.Rcode != dns.RcodeServerFailure && response.Rcode != dns.RcodeRefused
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

// GetMinTTL returns the minimum TTL from the response
func GetMinTTL(msg *dns.Msg) uint32 {
	var minTTL uint32
	found := false
	for _, answer := range msg.Answer {
		ttl := answer.Header().Ttl
		if !found || ttl < minTTL {
			minTTL = ttl
			found = true
		}
	}
	if found {
		return minTTL
	}
	return NegativeTTL(msg)
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
