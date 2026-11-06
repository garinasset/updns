package server

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

func TestLocalHostsPrecedencePerAddressFamily(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, test := range []struct {
		name, hosts, leases, ipv4, ipv6 string
		aSource, aaaaSource, anySource  string
	}{
		{"hosts-v6-dhcp-v4", "2001:db8::4 dev\nfd00::4 dev\n", "1005 mac 192.0.2.4 dev client\n",
			"192.0.2.4", "2001:db8::4", "local-dhcp", "local-hosts", "local-hosts+local-dhcp"},
		{"hosts-v4-dhcp-v6", "192.0.2.4 dev\n", "1005 mac 2001:db8::4 dev client\n",
			"192.0.2.4", "2001:db8::4", "local-hosts", "local-dhcp", "local-hosts+local-dhcp"},
		{"hosts-both-override-leases", "192.0.2.4 dev\n2001:db8::4 dev\n",
			"1005 mac 192.0.2.99 dev client\n1005 mac 2001:db8::99 dev client\n",
			"192.0.2.4", "2001:db8::4", "local-hosts", "local-hosts", "local-hosts"},
	} {
		t.Run(test.name, func(t *testing.T) {
			local, _, _ := testLocalDNS(t, test.hosts, test.leases)
			local.now = func() time.Time { return now }
			for _, name := range []string{"dev.", "dev.lan."} {
				for _, query := range []struct {
					kind   uint16
					source string
					want   []string
				}{
					{dns.TypeA, test.aSource, []string{test.ipv4}},
					{dns.TypeAAAA, test.aaaaSource, []string{test.ipv6}},
					{dns.TypeANY, test.anySource, []string{test.ipv4, test.ipv6}},
				} {
					response, source, err := local.lookup(name, query.kind)
					if err != nil || response == nil || source != query.source {
						t.Fatalf("%s %s: %v %s %v", name, dns.TypeToString[query.kind], response, source, err)
					}
					for _, address := range query.want {
						if !strings.Contains(response.String(), address) {
							t.Fatalf("missing %s: %v", address, response)
						}
					}
					if strings.Contains(response.String(), "::99") || strings.Contains(response.String(), "192.0.2.99") {
						t.Fatal("lease overrode same-family hosts record")
					}
				}
			}
			local.now = func() time.Time { return now.Add(5 * time.Second) }
			response, _, err := local.lookup("dev.", dns.TypeANY)
			if err != nil || response == nil {
				t.Fatalf("hosts disappeared at lease expiry: %v %v", response, err)
			}
			if test.aSource == "local-dhcp" && strings.Contains(response.String(), test.ipv4) ||
				test.aaaaSource == "local-dhcp" && strings.Contains(response.String(), test.ipv6) {
				t.Fatal("expired lease served")
			}
		})
	}
}

func TestLocalMixedFamiliesDoNotQueryUpstream(t *testing.T) {
	var calls atomic.Int32
	srv := newFlightTestServer(t, func(w dns.ResponseWriter, query *dns.Msg) {
		calls.Add(1)
		writeFlightAnswer(t, w, query)
	})
	leases := fmt.Sprintf("%d mac 192.0.2.4 dev client\n", time.Now().Add(time.Hour).Unix())
	local, _, _ := testLocalDNS(t, "2001:db8::4 dev\n", leases)
	srv.local = local
	for _, test := range []struct {
		kind            uint16
		address, source string
	}{
		{dns.TypeA, "192.0.2.4", "local-dhcp"},
		{dns.TypeAAAA, "2001:db8::4", "local-hosts"},
	} {
		result, err := srv.Query("dev.", test.kind)
		if err != nil || result == nil || !result.LocalAnswer || result.UsedUpstream != test.source ||
			len(result.Answers) != 1 || result.Answers[0] != test.address {
			t.Fatalf("local family not served: %#v %v", result, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("local mixed-family records requested upstream")
	}
}
