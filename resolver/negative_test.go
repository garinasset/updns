package resolver

import (
	"context"
	"testing"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func TestNegativeSOATTLNormalization(t *testing.T) {
	tests := []struct {
		name     string
		rcode    int
		qtype    uint16
		positive bool
		ttl      uint32
		minimum  uint32
		want     uint32
	}{
		{"NXDOMAIN", dns.RcodeNameError, dns.TypeAAAA, false, 600, 60, 60},
		{"NODATA", dns.RcodeSuccess, dns.TypeAAAA, false, 600, 60, 60},
		{"shorter upstream TTL", dns.RcodeNameError, dns.TypeA, false, 30, 60, 30},
		{"zero minimum", dns.RcodeSuccess, dns.TypeAAAA, false, 600, 0, 0},
		{"zero TTL", dns.RcodeNameError, dns.TypeA, false, 0, 60, 0},
		{"positive answer", dns.RcodeSuccess, dns.TypeA, true, 600, 60, 600},
		{"SOA query", dns.RcodeSuccess, dns.TypeSOA, true, 600, 60, 600},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			address := startDNSUpstream(t, "udp", "127.0.0.1:0", func(w dns.ResponseWriter, query *dns.Msg) {
				response := new(dns.Msg)
				response.SetReply(query)
				response.Rcode = tt.rcode
				soa := testSOA().(*dns.SOA)
				soa.Hdr.Ttl, soa.Minttl = tt.ttl, tt.minimum
				response.Ns = []dns.RR{soa}
				if tt.positive {
					if tt.qtype == dns.TypeSOA {
						soa.Hdr.Name = query.Question[0].Name
						response.Answer = []dns.RR{dns.Copy(soa)}
					} else {
						response.Answer = testAnswer(query).Answer
					}
				}
				if err := w.WriteMsg(response); err != nil {
					t.Errorf("write upstream response: %v", err)
				}
			})
			r := NewResolver(0)
			if err := r.RegisterUpstream(&config.UpstreamDNS{Name: "test", Protocol: "udp", Address: address, Timeout: 1}); err != nil {
				t.Fatal(err)
			}
			response, err := r.Resolve(context.Background(), "www.example.", tt.qtype, "test")
			if err != nil {
				t.Fatal(err)
			}
			soa := response.Ns[0].(*dns.SOA)
			if soa.Hdr.Ttl != tt.want || soa.Minttl != tt.minimum {
				t.Fatalf("SOA TTL=%d MINIMUM=%d, want TTL=%d MINIMUM=%d", soa.Hdr.Ttl, soa.Minttl, tt.want, tt.minimum)
			}
			if tt.qtype == dns.TypeSOA && response.Answer[0].Header().Ttl != tt.ttl {
				t.Fatal("positive SOA answer was changed")
			}
		})
	}
}
