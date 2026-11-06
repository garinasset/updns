package main

import (
	"testing"

	"github.com/garinasset/updns/server"
)

func TestUpstreamByType(t *testing.T) {
	for _, test := range []struct {
		a, aaaa, want string
	}{
		{"same", "same", "A=same / AAAA=same"},
		{"cached", "current", "A=cached / AAAA=current"},
		{"local-dhcp", "local-hosts", "A=local-dhcp / AAAA=local-hosts"},
		{"", "current", "A=(none) / AAAA=current"},
		{"cached", "", "A=cached / AAAA=(none)"},
	} {
		results := []*server.QueryResult{
			{QueryType: "A", UsedUpstream: test.a, CacheHit: true},
			{QueryType: "AAAA", UsedUpstream: test.aaaa},
		}
		if got := upstreamByType(results); got != test.want {
			t.Fatalf("got %q want %q", got, test.want)
		}
	}
}
