package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestFirstValidResponseWinsWhateverRcode(t *testing.T) {
	kinds := []string{"NODATA without SOA", "NXDOMAIN without SOA", "FORMERR", "SERVFAIL", "REFUSED", "NOTIMP"}
	for _, kind := range kinds {
		for _, concurrent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/concurrent=%t", kind, concurrent), func(t *testing.T) {
				firstReturned := make(chan struct{})
				var laterCalls atomic.Int32
				first := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					response := new(dns.Msg)
					response.SetReply(query)
					switch kind {
					case "NXDOMAIN without SOA":
						response.Rcode = dns.RcodeNameError
					case "FORMERR":
						response.Rcode = dns.RcodeFormatError
					case "SERVFAIL":
						response.Rcode = dns.RcodeServerFailure
					case "REFUSED":
						response.Rcode = dns.RcodeRefused
					case "NOTIMP":
						response.Rcode = dns.RcodeNotImplemented
					}
					if err := w.WriteMsg(response); err != nil {
						t.Errorf("write first response: %v", err)
					}
					close(firstReturned)
				})
				later := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					laterCalls.Add(1)
					<-firstReturned
					time.Sleep(20 * time.Millisecond)
					_ = w.WriteMsg(testAnswer(query))
				})
				r := NewResolver(0)
				for _, upstream := range []*config.UpstreamDNS{
					{Name: "first", Protocol: "udp", Address: first, Timeout: 1},
					{Name: "later", Protocol: "udp", Address: later, Timeout: 1},
				} {
					if err := r.RegisterUpstream(upstream); err != nil {
						t.Fatal(err)
					}
				}
				resolve := r.ResolveMultiple
				if concurrent {
					resolve = r.ResolveConcurrent
				}
				response, winner, err := resolve(context.Background(), "www.example.", dns.TypeA, []string{"first", "later"})
				if err != nil || winner != "first" || response == nil {
					t.Fatalf("first valid response did not win: winner=%s response=%v err=%v", winner, response, err)
				}
				if GetMinTTL(response) != 0 {
					t.Fatalf("uncacheable winner reported cache TTL %d", GetMinTTL(response))
				}
				if !concurrent && laterCalls.Load() != 0 {
					t.Fatal("sequential query continued after a valid response")
				}
			})
		}
	}
}

func TestConcurrentWinnerIsFirstValidResponse(t *testing.T) {
	slowStarted := make(chan struct{})
	slow := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		close(slowStarted)
		time.Sleep(300 * time.Millisecond)
		_ = w.WriteMsg(testAnswer(query))
	})
	fast := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		<-slowStarted
		response := new(dns.Msg)
		response.SetReply(query)
		response.Rcode = dns.RcodeNameError
		_ = w.WriteMsg(response)
	})
	r := NewResolver(0)
	for _, upstream := range []*config.UpstreamDNS{
		{Name: "slow", Protocol: "udp", Address: slow, Timeout: 2},
		{Name: "fast", Protocol: "udp", Address: fast, Timeout: 2},
	} {
		if err := r.RegisterUpstream(upstream); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	response, winner, err := r.ResolveConcurrent(context.Background(), "www.example.", dns.TypeA, []string{"slow", "fast"})
	if err != nil || winner != "fast" || response.Rcode != dns.RcodeNameError {
		t.Fatalf("winner=%s response=%v err=%v", winner, response, err)
	}
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Fatalf("SOA-less NXDOMAIN waited for other upstreams: %s", elapsed)
	}
}

func TestInvalidResponsesFallThroughToFailure(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%t", concurrent), func(t *testing.T) {
			refused := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				response := new(dns.Msg)
				response.SetReply(query)
				response.Rcode = dns.RcodeRefused
				response.Question = nil
				_ = w.WriteMsg(response)
			})
			mismatch := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				response := testAnswer(query)
				response.Question[0].Qtype = dns.TypeAAAA
				_ = w.WriteMsg(response)
			})
			r := NewResolver(0)
			for _, upstream := range []*config.UpstreamDNS{
				{Name: "refused", Protocol: "udp", Address: refused, Timeout: 1},
				{Name: "mismatch", Protocol: "udp", Address: mismatch, Timeout: 1},
			} {
				if err := r.RegisterUpstream(upstream); err != nil {
					t.Fatal(err)
				}
			}
			resolve := r.ResolveMultiple
			if concurrent {
				resolve = r.ResolveConcurrent
			}
			response, _, err := resolve(context.Background(), "www.example.", dns.TypeA, []string{"refused", "mismatch"})
			if err == nil || response != nil {
				t.Fatalf("invalid responses won: response=%v err=%v", response, err)
			}
			for _, want := range []string{"all upstreams failed", "REFUSED", "question count: 0", "mismatch"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("failure %q lacks %q", err, want)
				}
			}
		})
	}
}

func TestCancellationIsNotAResponse(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent=%t", concurrent), func(t *testing.T) {
			started := make(chan struct{})
			silent := startDNSUpstream(t, "udp", "127.0.0.1:0", func(dns.ResponseWriter, *dns.Msg) { close(started) })
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "silent", Protocol: "udp", Address: silent, Timeout: 5}); err != nil {
				t.Fatal(err)
			}
			resolve := r.ResolveMultiple
			if concurrent {
				resolve = r.ResolveConcurrent
			}
			ctx, cancel := context.WithCancel(context.Background())
			go func() { <-started; cancel() }()
			response, _, err := resolve(ctx, "www.example.", dns.TypeA, []string{"silent"})
			if !errors.Is(err, context.Canceled) || response != nil {
				t.Fatalf("cancellation: response=%v err=%v", response, err)
			}
		})
	}
}

func TestNXDOMAINCNAMEIsFinalWithoutTerminalQuery(t *testing.T) {
	var calls atomic.Int32
	address := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
		calls.Add(1)
		response := new(dns.Msg)
		response.SetReply(query)
		response.Rcode = dns.RcodeNameError
		response.Answer = []dns.RR{&dns.CNAME{
			Hdr:    dns.RR_Header{Name: query.Question[0].Name, Rrtype: dns.TypeCNAME, Class: dns.ClassINET, Ttl: 300},
			Target: "terminal.example.",
		}}
		_ = w.WriteMsg(response)
	})
	r := NewResolver(0)
	if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "test", Protocol: "udp", Address: address, Timeout: 1}); err != nil {
		t.Fatal(err)
	}
	response, err := r.Resolve(context.Background(), "www.example.", dns.TypeA, "test")
	if err != nil || response.Rcode != dns.RcodeNameError || len(response.Answer) != 1 || calls.Load() != 1 {
		t.Fatalf("response=%v err=%v calls=%d", response, err, calls.Load())
	}
	if GetMinTTL(response) != 0 {
		t.Fatal("SOA-less NXDOMAIN is cacheable")
	}
}

func TestGetMinTTLCacheEligibility(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("www.example.", dns.TypeA)
	tests := []struct {
		name   string
		change func(*dns.Msg)
		want   uint32
	}{
		{"positive", func(*dns.Msg) {}, 60},
		{"NODATA with SOA", func(m *dns.Msg) { m.Answer = nil; m.Ns = []dns.RR{testSOA()} }, 20},
		{"NXDOMAIN with SOA", func(m *dns.Msg) { m.Rcode = dns.RcodeNameError; m.Answer = nil; m.Ns = []dns.RR{testSOA()} }, 20},
		{"NODATA without SOA", func(m *dns.Msg) { m.Answer = nil }, 0},
		{"NXDOMAIN without SOA", func(m *dns.Msg) { m.Rcode = dns.RcodeNameError; m.Answer = nil }, 0},
		{"unrelated SOA", func(m *dns.Msg) {
			m.Answer = nil
			soa := testSOA()
			soa.Header().Name = "other."
			m.Ns = []dns.RR{soa}
		}, 0},
		{"SERVFAIL", func(m *dns.Msg) { m.Rcode = dns.RcodeServerFailure }, 0},
		{"REFUSED with SOA", func(m *dns.Msg) { m.Rcode = dns.RcodeRefused; m.Answer = nil; m.Ns = []dns.RR{testSOA()} }, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := testAnswer(query)
			tt.change(response)
			if got := GetMinTTL(response); got != tt.want {
				t.Fatalf("GetMinTTL() = %d, want %d", got, tt.want)
			}
		})
	}
}
