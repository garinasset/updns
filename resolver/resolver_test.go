package resolver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestGetMinTTLUsesAllAnswerRecords(t *testing.T) {
	response := new(dns.Msg)
	response.Answer = []dns.RR{
		&dns.A{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 120}},
		&dns.AAAA{Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeAAAA, Class: dns.ClassINET, Ttl: 45}},
	}
	if got := GetMinTTL(response); got != 45 {
		t.Fatalf("GetMinTTL() = %d, want 45", got)
	}
}

func TestResolveMultipleReportsUpstreamFailures(t *testing.T) {
	r := NewResolver(0)
	_, _, err := r.ResolveMultiple(context.Background(), "example.com", dns.TypeA, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ResolveMultiple() error = %v, want upstream-specific failure details", err)
	}
}

func TestResolveConcurrentReportsUpstreamFailures(t *testing.T) {
	r := NewResolver(0)
	_, _, err := r.ResolveConcurrent(context.Background(), "example.com", dns.TypeA, []string{"missing"})
	if err == nil || !strings.Contains(err.Error(), "missing") || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("ResolveConcurrent() error = %v, want upstream-specific failure details", err)
	}
}

func TestResolveDoHUsesRFC8484POST(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("DoH method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/dns-message" {
			t.Errorf("Content-Type = %q, want application/dns-message", got)
		}
		if got := r.Header.Get("Accept"); got != "application/dns-message" {
			t.Errorf("Accept = %q, want application/dns-message", got)
		}

		request := new(dns.Msg)
		if err := request.Unpack(readRequestBody(t, r)); err != nil {
			t.Errorf("unpack DoH request: %v", err)
			http.Error(w, "bad DNS message", http.StatusBadRequest)
			return
		}
		response := new(dns.Msg)
		response.SetReply(request)
		response.Answer = []dns.RR{
			&dns.A{
				Hdr: dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
				A:   net.ParseIP("192.0.2.9").To4(),
			},
		}
		wire, err := response.Pack()
		if err != nil {
			t.Errorf("pack DoH response: %v", err)
			http.Error(w, "could not encode DNS message", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/dns-message")
		_, _ = w.Write(wire)
	}))
	defer server.Close()

	r := NewResolver(0)
	r.httpClient = server.Client()
	upstream := &config.UpstreamDNS{
		Name:     "test-doh",
		Address:  server.URL,
		Protocol: "doh",
		Timeout:  1,
	}
	if err := r.RegisterUpstream(upstream); err != nil {
		t.Fatal(err)
	}

	response, err := r.Resolve(context.Background(), "example.com", dns.TypeA, upstream.Name)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Answer) != 1 || response.Answer[0].String() != "example.com.\t60\tIN\tA\t192.0.2.9" {
		t.Fatalf("unexpected DoH response: %#v", response.Answer)
	}
}

func readRequestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	defer request.Body.Close()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read DoH request body: %v", err)
	}
	return body
}

func TestGetMinTTLUsesSOAForNegativeResponse(t *testing.T) {
	response := new(dns.Msg)
	response.Ns = []dns.RR{
		&dns.SOA{
			Hdr:     dns.RR_Header{Name: "example.com.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 180},
			Ns:      "ns.example.com.",
			Mbox:    "hostmaster.example.com.",
			Serial:  1,
			Refresh: 3600,
			Retry:   600,
			Expire:  86400,
			Minttl:  60,
		},
	}
	if got := GetMinTTL(response); got != 60 {
		t.Fatalf("GetMinTTL() = %d, want 60", got)
	}
}

func TestNegativeTTLWithoutSOAIsNotCacheable(t *testing.T) {
	if got := NegativeTTL(new(dns.Msg)); got != 0 {
		t.Fatalf("NegativeTTL() = %d, want 0", got)
	}
}

func TestWithDefaultPort(t *testing.T) {
	tests := []struct {
		address string
		port    string
		want    string
	}{
		{address: "192.0.2.53", port: "53", want: "192.0.2.53:53"},
		{address: "dns.example", port: "853", want: "dns.example:853"},
		{address: "2001:db8::53", port: "53", want: "[2001:db8::53]:53"},
		{address: "[2001:db8::53]:5353", port: "53", want: "[2001:db8::53]:5353"},
	}
	for _, test := range tests {
		if got := withDefaultPort(test.address, test.port); got != test.want {
			t.Errorf("withDefaultPort(%q, %q) = %q, want %q", test.address, test.port, got, test.want)
		}
	}
}

func testAnswer(query *dns.Msg) *dns.Msg {
	response := new(dns.Msg)
	response.SetReply(query)
	response.Question = append([]dns.Question(nil), query.Question...)
	response.Answer = []dns.RR{&dns.A{
		Hdr: dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
		A:   net.IPv4(192, 0, 2, 1),
	}}
	return response
}

func testSOA() dns.RR {
	return &dns.SOA{
		Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
		Ns:  "ns.example.", Mbox: "hostmaster.example.", Minttl: 20,
	}
}

func TestValidateResponse(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("www.example.", dns.TypeA)
	tests := []struct {
		name   string
		change func(*dns.Msg)
		valid  bool
	}{
		{"positive", func(*dns.Msg) {}, true},
		{"zero TTL positive", func(m *dns.Msg) { m.Answer[0].Header().Ttl = 0 }, true},
		{"case insensitive question", func(m *dns.Msg) { m.Question[0].Name = "WWW.EXAMPLE." }, true},
		{"NODATA", func(m *dns.Msg) { m.Answer = nil; m.Ns = []dns.RR{testSOA()} }, true},
		{"NXDOMAIN", func(m *dns.Msg) {
			m.Rcode = dns.RcodeNameError
			m.Answer = nil
			m.Ns = []dns.RR{testSOA()}
		}, true},
		{"zero TTL negative", func(m *dns.Msg) {
			m.Answer = nil
			soa := testSOA().(*dns.SOA)
			soa.Hdr.Ttl, soa.Minttl = 0, 0
			m.Ns = []dns.RR{soa}
		}, true},
		{"FORMERR", func(m *dns.Msg) { m.Rcode = dns.RcodeFormatError }, true},
		{"SERVFAIL", func(m *dns.Msg) { m.Rcode = dns.RcodeServerFailure }, true},
		{"REFUSED", func(m *dns.Msg) { m.Rcode = dns.RcodeRefused }, true},
		{"REFUSED without question", func(m *dns.Msg) { m.Rcode = dns.RcodeRefused; m.Question = nil }, false},
		{"NOTIMP", func(m *dns.Msg) { m.Rcode = dns.RcodeNotImplemented }, true},
		{"truncated", func(m *dns.Msg) { m.Truncated = true }, false},
		{"query instead of reply", func(m *dns.Msg) { m.Response = false }, false},
		{"wrong ID", func(m *dns.Msg) { m.Id++ }, false},
		{"wrong opcode", func(m *dns.Msg) { m.Opcode = dns.OpcodeUpdate }, false},
		{"wrong name", func(m *dns.Msg) { m.Question[0].Name = "other.example." }, false},
		{"wrong type", func(m *dns.Msg) { m.Question[0].Qtype = dns.TypeAAAA }, false},
		{"wrong class", func(m *dns.Msg) { m.Question[0].Qclass = dns.ClassCHAOS }, false},
		{"missing question", func(m *dns.Msg) { m.Question = nil }, false},
		{"empty without SOA", func(m *dns.Msg) { m.Answer = nil }, true},
		{"NXDOMAIN without SOA", func(m *dns.Msg) { m.Rcode = dns.RcodeNameError; m.Answer = nil }, true},
		{"contradictory NXDOMAIN", func(m *dns.Msg) { m.Rcode = dns.RcodeNameError; m.Ns = []dns.RR{testSOA()} }, false},
		{"unrelated answer", func(m *dns.Msg) { m.Answer[0].Header().Name = "other.example." }, true},
		{"referral", func(m *dns.Msg) {
			m.Answer = nil
			m.Ns = []dns.RR{&dns.NS{Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: dns.ClassINET}, Ns: "ns.example."}}
		}, false},
		{"unrelated SOA", func(m *dns.Msg) {
			m.Answer = nil
			soa := testSOA()
			soa.Header().Name = "other."
			m.Ns = []dns.RR{soa}
		}, true},
		{"CNAME only", func(m *dns.Msg) {
			m.Answer = []dns.RR{&dns.CNAME{
				Hdr:    dns.RR_Header{Name: "www.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300},
				Target: "target.example.",
			}}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := testAnswer(query)
			tt.change(response)
			err := validateResponse(query, response)
			if (err == nil) != tt.valid {
				t.Fatalf("validateResponse() = %v, want valid=%t", err, tt.valid)
			}
		})
	}
}

func TestCNAMEFinalAnswersAndNegativeTTL(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("www.example.", dns.TypeA)
	response := testAnswer(query)
	response.Answer[0].Header().Name = "target.example."
	cname := &dns.CNAME{
		Hdr:    dns.RR_Header{Name: "www.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300},
		Target: "target.example.",
	}
	response.Answer = append(response.Answer, cname)
	if err := validateResponse(query, response); err != nil {
		t.Fatalf("complete CNAME chain rejected: %v", err)
	}
	response.Answer = []dns.RR{cname}
	response.Ns = []dns.RR{testSOA()}
	for _, code := range []int{dns.RcodeSuccess, dns.RcodeNameError} {
		response.Rcode = code
		if err := validateResponse(query, response); err != nil {
			t.Fatalf("negative CNAME result rejected: %v", err)
		}
		if got := GetMinTTL(response); got != 20 {
			t.Fatalf("negative CNAME TTL = %d, want 20", got)
		}
	}
	response.Rcode = dns.RcodeSuccess
	response.Answer = append(response.Answer, &dns.CNAME{
		Hdr:    dns.RR_Header{Name: "target.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET},
		Target: "www.example.",
	})
	if err := validateResponse(query, response); err == nil {
		t.Fatal("CNAME loop accepted")
	}
}

func startDNSUpstream(t *testing.T, network, address string, handler dns.HandlerFunc) string {
	t.Helper()
	s := &dns.Server{Net: network, Handler: handler}
	var err error
	if network == "udp" {
		s.PacketConn, err = net.ListenPacket(network, address)
		if err == nil {
			address = s.PacketConn.LocalAddr().String()
		}
	} else {
		s.Listener, err = net.Listen(network, address)
		if err == nil {
			address = s.Listener.Addr().String()
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	s.NotifyStartedFunc = func() { close(ready) }
	go func() {
		if err := s.ActivateAndServe(); err != nil {
			t.Errorf("serve test DNS upstream: %v", err)
		}
	}()
	<-ready
	t.Cleanup(func() {
		if err := s.Shutdown(); err != nil {
			t.Errorf("shutdown test DNS upstream: %v", err)
		}
	})
	return address
}

func TestUpstreamSelectionSkipsNonFinalResponses(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		for _, kind := range []string{"REFUSED without question", "wrong question", "referral", "CNAME only", "truncated TCP"} {
			t.Run(fmt.Sprintf("concurrent=%t/%s", concurrent, kind), func(t *testing.T) {
				badReturned := make(chan struct{})
				var returned sync.Once
				badNetwork := "udp"
				if kind == "truncated TCP" {
					badNetwork = "tcp"
				}
				bad := startDNSUpstream(t, badNetwork, "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					response := testAnswer(query)
					switch kind {
					case "REFUSED without question":
						response.Rcode = dns.RcodeRefused
						response.Question = nil
						response.Answer = nil
					case "wrong question":
						response.Question[0].Name = "other.example."
					case "referral":
						response.Answer = nil
						response.Ns = []dns.RR{&dns.NS{
							Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: dns.ClassINET}, Ns: "ns.example.",
						}}
					case "CNAME only":
						response.Answer = []dns.RR{&dns.CNAME{
							Hdr:    dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET},
							Target: "target.example.",
						}}
					case "truncated TCP":
						response.Truncated = true
					}
					if err := w.WriteMsg(response); err != nil {
						t.Errorf("write bad response: %v", err)
					}
					returned.Do(func() { close(badReturned) })
				})
				good := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					<-badReturned
					if err := w.WriteMsg(testAnswer(query)); err != nil {
						t.Errorf("write good response: %v", err)
					}
				})
				r := NewResolver(0)
				for _, upstream := range []*config.UpstreamDNS{
					{Name: "bad", Protocol: badNetwork, Address: bad, Timeout: 1},
					{Name: "good", Protocol: "udp", Address: good, Timeout: 1},
				} {
					if err := r.RegisterUpstream(upstream); err != nil {
						t.Fatal(err)
					}
				}
				resolve := r.ResolveMultiple
				if concurrent {
					resolve = r.ResolveConcurrent
				}
				resp, name, err := resolve(context.Background(), "www.example.", dns.TypeA, []string{"bad", "good"})
				if err != nil || name != "good" || resp == nil || len(resp.Answer) != 1 {
					t.Fatalf("selected %q, response=%v, error=%v", name, resp, err)
				}
			})
		}
	}
}

func TestUDPTruncationRetriesTCP(t *testing.T) {
	for _, truncatedTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("truncatedTCP=%t", truncatedTCP), func(t *testing.T) {
			tcpCalled := make(chan struct{}, 1)
			address := startDNSUpstream(t, "tcp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				tcpCalled <- struct{}{}
				response := testAnswer(query)
				response.Truncated = truncatedTCP
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write TCP response: %v", err)
				}
			})
			startDNSUpstream(t, "udp", address, func(w dns.ResponseWriter, query *dns.Msg) {
				response := new(dns.Msg)
				response.SetReply(query)
				response.Truncated = true
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write truncated response: %v", err)
				}
			})
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "test", Protocol: "udp", Address: address, Timeout: 1}); err != nil {
				t.Fatal(err)
			}
			resp, err := r.Resolve(context.Background(), "www.example.", dns.TypeA, "test")
			if truncatedTCP {
				if err == nil {
					t.Fatal("accepted truncated TCP reply")
				}
			} else if err != nil || resp == nil || resp.Truncated || len(resp.Answer) != 1 {
				t.Fatalf("TCP retry failed: response=%v, error=%v", resp, err)
			}
			select {
			case <-tcpCalled:
			default:
				t.Fatal("TCP retry was not made")
			}
		})
	}
}

type delayedDeadlineContext struct {
	context.Context
	deadline time.Time
}

func (c delayedDeadlineContext) Deadline() (time.Time, bool) { return c.deadline, true }

func TestDNSDeadlineWaitsForContext(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			started := make(chan struct{})
			address := startDNSUpstream(t, network, "127.0.0.1:0", func(dns.ResponseWriter, *dns.Msg) { close(started) })
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := delayedDeadlineContext{Context: parent, deadline: time.Now().Add(100 * time.Millisecond)}
			msg := new(dns.Msg)
			msg.SetQuestion("www.example.", dns.TypeA)
			done := make(chan error, 1)
			go func() { _, err := exchangeDNS(ctx, msg, address, network, time.Second); done <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("upstream request not started")
			}
			select {
			case err := <-done:
				t.Fatalf("socket timeout returned before context cancellation: %v", err)
			case <-time.After(time.Until(ctx.deadline) + 100*time.Millisecond):
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("exchange error = %v, want cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("exchange did not return after cancellation")
			}
		})
	}
}

func TestResolveCancellationInterruptsDNSRead(t *testing.T) {
	for _, network := range []string{"udp", "tcp"} {
		t.Run(network, func(t *testing.T) {
			started := make(chan struct{})
			address := startDNSUpstream(t, network, "127.0.0.1:0", func(dns.ResponseWriter, *dns.Msg) { close(started) })
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "silent", Protocol: network, Address: address, Timeout: 10}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { _, err := r.Resolve(ctx, "www.example.", dns.TypeA, "silent"); done <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("upstream request not started")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("Resolve error = %v, want cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt DNS read")
			}
		})
	}
}

func TestConcurrentWinnerCancelsLosingDoHRequest(t *testing.T) {
	started, canceled := make(chan struct{}), make(chan struct{})
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		readRequestBody(t, request)
		close(started)
		select {
		case <-request.Context().Done():
			close(canceled)
		case <-time.After(3 * time.Second):
			t.Error("losing DoH handler did not observe cancellation")
		}
	}))
	defer slow.Close()
	fast := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Error("losing request did not start")
		}
		if err := w.WriteMsg(testAnswer(query)); err != nil {
			t.Errorf("write winning response: %v", err)
		}
	})
	r := NewResolver(0)
	r.httpClient = slow.Client()
	for _, upstream := range []*config.UpstreamDNS{
		{Name: "slow", Protocol: "doh", Address: slow.URL, Timeout: 10},
		{Name: "fast", Protocol: "udp", Address: fast, Timeout: 10},
	} {
		if err := r.RegisterUpstream(upstream); err != nil {
			t.Fatal(err)
		}
	}
	resp, name, err := r.ResolveConcurrent(context.Background(), "www.example.", dns.TypeA, []string{"slow", "fast"})
	if err != nil || name != "fast" || resp == nil {
		t.Fatalf("concurrent resolution failed: name=%s, err=%v", name, err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("losing DoH request was not canceled")
	}
}

func TestConcurrentWinnerClosesLosingTCPConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			closed <- err
			return
		}
		dc := &dns.Conn{Conn: conn}
		if _, err := dc.ReadMsg(); err != nil {
			closed <- err
			return
		}
		close(started)
		_, err = dc.ReadMsg()
		closed <- err
	}()
	address := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Error("losing TCP query did not start")
		}
		if err := w.WriteMsg(testAnswer(query)); err != nil {
			t.Errorf("write winning response: %v", err)
		}
	})
	r := NewResolver(0)
	for _, upstream := range []*config.UpstreamDNS{
		{Name: "slow", Protocol: "tcp", Address: listener.Addr().String(), Timeout: 10},
		{Name: "fast", Protocol: "udp", Address: address, Timeout: 10},
	} {
		if err := r.RegisterUpstream(upstream); err != nil {
			t.Fatal(err)
		}
	}
	_, name, err := r.ResolveConcurrent(context.Background(), "www.example.", dns.TypeA, []string{"slow", "fast"})
	if err != nil || name != "fast" {
		t.Fatalf("concurrent query: winner=%s error=%v", name, err)
	}
	select {
	case err := <-closed:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("losing connection did not close cleanly: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("losing TCP connection was not closed promptly")
	}
}

func TestResolveCancellationInterruptsTLSHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started := make(chan struct{})
	closed := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			closed <- err
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
			closed <- err
			return
		}
		buf := make([]byte, 4096)
		if _, err := conn.Read(buf); err != nil {
			closed <- err
			return
		}
		close(started)
		_, err = io.Copy(io.Discard, conn)
		closed <- err
	}()
	r := NewResolver(0)
	if err := r.RegisterUpstream(&config.UpstreamDNS{
		Name: "tls", Protocol: "dot", Address: listener.Addr().String(), Timeout: 10,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Resolve(ctx, "www.example.", dns.TypeA, "tls"); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("TLS handshake did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("TLS cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS handshake was not canceled")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("TLS connection did not close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("TLS connection remained open")
	}
}

func TestUDPAndTCPRetryShareTimeoutBudget(t *testing.T) {
	tcpStarted := make(chan struct{}, 1)
	address := startDNSUpstream(t, "tcp", "127.0.0.1:0", func(dns.ResponseWriter, *dns.Msg) {
		tcpStarted <- struct{}{}
	})
	startDNSUpstream(t, "udp", address, func(w dns.ResponseWriter, query *dns.Msg) {
		time.Sleep(600 * time.Millisecond)
		response := new(dns.Msg)
		response.SetReply(query)
		response.Truncated = true
		if err := w.WriteMsg(response); err != nil {
			t.Errorf("write UDP response: %v", err)
		}
	})
	r := NewResolver(0)
	if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "test", Protocol: "udp", Address: address, Timeout: 1}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := r.Resolve(context.Background(), "www.example.", dns.TypeA, "test")
	elapsed := time.Since(start)
	if err == nil || elapsed > 1500*time.Millisecond {
		t.Fatalf("retry exceeded shared timeout: elapsed=%s, error=%v", elapsed, err)
	}
	select {
	case <-tcpStarted:
	default:
		t.Fatal("TCP retry never started")
	}
}

func TestCNAMECompletion(t *testing.T) {
	for _, terminal := range []string{"positive", "NODATA", "NXDOMAIN", "empty", "FORMERR", "loop", "limit"} {
		t.Run(terminal, func(t *testing.T) {
			var mu sync.Mutex
			var names []string
			address := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				name := query.Question[0].Name
				mu.Lock()
				names = append(names, name)
				count := len(names)
				mu.Unlock()
				response := new(dns.Msg)
				response.SetReply(query)
				cname := func(from, to string, ttl uint32) dns.RR {
					return &dns.CNAME{
						Hdr:    dns.RR_Header{Name: from, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: ttl},
						Target: to,
					}
				}
				if name == "www.example." {
					response.Answer = []dns.RR{cname(name, "middle.example.", 60), cname("middle.example.", "target.example.", 10)}
				} else {
					switch terminal {
					case "positive":
						response = testAnswer(query)
					case "NODATA", "NXDOMAIN":
						response.Ns = []dns.RR{testSOA()}
						if terminal == "NXDOMAIN" {
							response.Rcode = dns.RcodeNameError
						}
					case "FORMERR":
						response.Rcode = dns.RcodeFormatError
					case "loop":
						response.Answer = []dns.RR{cname(name, "www.example.", 60)}
					case "limit":
						response.Answer = []dns.RR{cname(name, fmt.Sprintf("hop%d.example.", count), 60)}
					}
				}
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write CNAME response: %v", err)
				}
			})
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "same-upstream", Protocol: "udp", Address: address, Timeout: 2}); err != nil {
				t.Fatal(err)
			}
			response, err := r.Resolve(context.Background(), "www.example.", dns.TypeA, "same-upstream")
			valid := terminal != "loop" && terminal != "limit"
			if (err == nil) != valid {
				t.Fatalf("completion error=%v, want valid=%t", err, valid)
			}
			mu.Lock()
			gotNames := append([]string(nil), names...)
			mu.Unlock()
			if len(gotNames) < 2 || gotNames[0] != "www.example." || gotNames[1] != "target.example." {
				t.Fatalf("did not query chain terminal on same upstream: %v", gotNames)
			}
			if terminal == "limit" && (len(gotNames) > maxCNAMEHops || !strings.Contains(err.Error(), "CNAME hops")) {
				t.Fatalf("hop limit failed: requests=%d error=%v", len(gotNames), err)
			}
			if !valid {
				return
			}
			if response.Question[0].Name != "www.example." || len(response.Answer) < 2 {
				t.Fatalf("original question or CNAME chain lost: %v", response)
			}
			wantTTL := uint32(10)
			if terminal == "empty" || terminal == "FORMERR" {
				wantTTL = 0
			}
			if got := GetMinTTL(response); got != wantTTL {
				t.Fatalf("combined TTL=%d, want %d", got, wantTTL)
			}
			if terminal == "positive" && len(response.Answer) != 3 {
				t.Fatalf("terminal address missing: %v", response)
			}
			if terminal == "NXDOMAIN" && response.Rcode != dns.RcodeNameError {
				t.Fatal("terminal NXDOMAIN not propagated")
			}
			if terminal == "FORMERR" && response.Rcode != dns.RcodeFormatError {
				t.Fatal("terminal FORMERR not propagated")
			}
			if (terminal == "NODATA" || terminal == "NXDOMAIN") && len(response.Ns) != 1 {
				t.Fatal("negative SOA missing")
			}
		})
	}
}

func TestCNAMECompletionSharesDeadlineAndCancellation(t *testing.T) {
	for _, cancelDuringFollowup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel=%t", cancelDuringFollowup), func(t *testing.T) {
			started := make(chan struct{})
			address := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				if query.Question[0].Name != "www.example." {
					close(started)
					return
				}
				time.Sleep(400 * time.Millisecond)
				response := new(dns.Msg)
				response.SetReply(query)
				response.Answer = []dns.RR{&dns.CNAME{
					Hdr:    dns.RR_Header{Name: "www.example.", Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 60},
					Target: "target.example.",
				}}
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write CNAME response: %v", err)
				}
			})
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "test", Protocol: "udp", Address: address, Timeout: 1}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			start := time.Now()
			go func() { _, err := r.Resolve(ctx, "www.example.", dns.TypeA, "test"); done <- err }()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("CNAME follow-up did not start")
			}
			if cancelDuringFollowup {
				cancel()
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("silent terminal unexpectedly succeeded")
				}
				if cancelDuringFollowup && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation error=%v", err)
				}
				if !cancelDuringFollowup && time.Since(start) > 1250*time.Millisecond {
					t.Fatal("CNAME follow-up received a fresh timeout budget")
				}
			case <-time.After(time.Second):
				t.Fatal("CNAME follow-up did not obey shared timeout/cancellation")
			}
		})
	}
}
