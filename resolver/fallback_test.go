package resolver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestFallbackSelection(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		for _, other := range []string{"positive", "NODATA", "NXDOMAIN", "REFUSED", "empty", "referral"} {
			t.Run(fmt.Sprintf("concurrent=%t/other=%s", concurrent, other), func(t *testing.T) {
				emptyReturned := make(chan struct{})
				empty := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					response := new(dns.Msg)
					response.SetReply(query)
					if err := w.WriteMsg(response); err != nil {
						t.Errorf("write fallback: %v", err)
					}
					close(emptyReturned)
				})
				second := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					<-emptyReturned
					time.Sleep(20 * time.Millisecond)
					response := testAnswer(query)
					switch other {
					case "NODATA", "NXDOMAIN":
						response.Answer = nil
						response.Ns = []dns.RR{testSOA()}
						if other == "NXDOMAIN" {
							response.Rcode = dns.RcodeNameError
						}
					case "REFUSED":
						response.Rcode = dns.RcodeRefused
						response.Question = nil
						response.Answer = nil
					case "empty":
						response.Answer = nil
					case "referral":
						response.Answer = nil
						response.Ns = []dns.RR{&dns.NS{
							Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: dns.ClassINET},
							Ns:  "ns.example.",
						}}
					}
					if err := w.WriteMsg(response); err != nil {
						t.Errorf("write second upstream: %v", err)
					}
				})
				r := NewResolver(0)
				for _, upstream := range []*config.UpstreamDNS{
					{Name: "empty", Protocol: "udp", Address: empty, Timeout: 1},
					{Name: "other", Protocol: "udp", Address: second, Timeout: 1},
				} {
					if err := r.RegisterUpstream(upstream); err != nil {
						t.Fatal(err)
					}
				}
				resolve := r.ResolveMultiple
				if concurrent {
					resolve = r.ResolveConcurrent
				}
				response, winner, err := resolve(context.Background(), "www.example.", dns.TypeA, []string{"empty", "other"})
				if err != nil {
					t.Fatal(err)
				}
				want := "other"
				if other == "REFUSED" || other == "empty" || other == "referral" {
					want = "empty"
				}
				if winner != want {
					t.Fatalf("winner=%s, want %s", winner, want)
				}
				if winner == "empty" && (response.Rcode != dns.RcodeSuccess || GetMinTTL(response) != 0) {
					t.Fatalf("fallback not NOERROR/uncacheable: %v", response)
				}
			})
		}
	}
}

func TestFallbackDeadlineAndCancellation(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		for _, canceled := range []bool{false, true} {
			t.Run(fmt.Sprintf("concurrent=%t/canceled=%t", concurrent, canceled), func(t *testing.T) {
				silentStarted := make(chan struct{})
				empty := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
					response := new(dns.Msg)
					response.SetReply(query)
					if err := w.WriteMsg(response); err != nil {
						t.Errorf("write fallback: %v", err)
					}
				})
				silent := startDNSUpstream(t, "udp", "127.0.0.1:0", func(dns.ResponseWriter, *dns.Msg) {
					close(silentStarted)
				})
				r := NewResolver(0)
				for _, upstream := range []*config.UpstreamDNS{
					{Name: "empty", Protocol: "udp", Address: empty, Timeout: 1},
					{Name: "silent", Protocol: "udp", Address: silent, Timeout: 5},
				} {
					if err := r.RegisterUpstream(upstream); err != nil {
						t.Fatal(err)
					}
				}
				resolve := r.ResolveMultiple
				if concurrent {
					resolve = r.ResolveConcurrent
				}
				ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
				defer cancel()
				if canceled {
					go func() { <-silentStarted; time.Sleep(30 * time.Millisecond); cancel() }()
				}
				response, winner, err := resolve(ctx, "www.example.", dns.TypeA, []string{"empty", "silent"})
				if canceled {
					if !errors.Is(err, context.Canceled) || response != nil {
						t.Fatalf("cancellation became success: response=%v err=%v", response, err)
					}
				} else if err != nil || winner != "empty" || response == nil {
					t.Fatalf("deadline fallback: winner=%s err=%v", winner, err)
				}
			})
		}
	}
}

func TestRefusedWithoutQuestionReportsRcode(t *testing.T) {
	query := new(dns.Msg)
	query.SetQuestion("qq.com.", dns.TypeAAAA)
	response := new(dns.Msg)
	response.SetReply(query)
	response.Rcode = dns.RcodeRefused
	response.Question = nil
	err := validateReply(query, response)
	if err == nil || !strings.Contains(err.Error(), "REFUSED") || !strings.Contains(err.Error(), "question count: 0") {
		t.Fatalf("missing actual refusal diagnosis: %v", err)
	}
}

func TestNoFallbackForMalformedOrReferralResponse(t *testing.T) {
	for _, kind := range []string{"referral", "wrong question", "wrong SOA", "NXDOMAIN", "REFUSED"} {
		t.Run(kind, func(t *testing.T) {
			address := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				response := new(dns.Msg)
				response.SetReply(query)
				response.Question = append([]dns.Question(nil), query.Question...)
				switch kind {
				case "referral":
					response.Ns = []dns.RR{&dns.NS{
						Hdr: dns.RR_Header{Name: "example.", Rrtype: dns.TypeNS, Class: dns.ClassINET}, Ns: "ns.example.",
					}}
				case "wrong question":
					response.Question[0].Name = "wrong.example."
				case "wrong SOA":
					soa := testSOA()
					soa.Header().Name = "unrelated."
					response.Ns = []dns.RR{soa}
				case "NXDOMAIN":
					response.Rcode = dns.RcodeNameError
				case "REFUSED":
					response.Rcode = dns.RcodeRefused
					response.Question = nil
				}
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write invalid response: %v", err)
				}
			})
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "bad", Protocol: "udp", Address: address, Timeout: 1}); err != nil {
				t.Fatal(err)
			}
			for _, resolve := range []func(context.Context, string, uint16, []string) (*dns.Msg, string, error){r.ResolveMultiple, r.ResolveConcurrent} {
				response, _, err := resolve(context.Background(), "www.example.", dns.TypeAAAA, []string{"bad"})
				if err == nil || response != nil {
					t.Fatalf("invalid response returned as fallback: response=%v err=%v", response, err)
				}
			}
		})
	}
}
