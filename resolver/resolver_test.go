package resolver

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
