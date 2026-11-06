package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/garinasset/updns/server"
	"github.com/rivo/uniseg"
)

func TestLayoutTerminalWidths(t *testing.T) {
	for _, width := range []int{1, 2, 3, 8, 16, 24, 32, 48, 80, 120} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			t.Setenv("COLUMNS", fmt.Sprint(width))
			var out bytes.Buffer
			writeQueryOutput(&out, []*server.QueryResult{{
				Domain:       "very-long-domain.production.example.com.",
				QueryType:    "A",
				MatchedRule:  "国内访问规则",
				UsedUpstream: "东京-e\u0301-👩‍💻-DoH-Primary",
				Action:       "ipv4+ipv6",
				Answers:      []string{"2001:db8:1234:5678:abcd:9876:1234:5678"},
				IPv4NFTSets:  []string{"inet/fw4/国内地址集合"},
				IPv4InSets:   map[string]bool{"inet/fw4/国内地址集合": true},
				Error:        "A long upstream error with 中文内容",
			}})
			for _, line := range strings.Split(out.String(), "\n") {
				if got := uniseg.StringWidth(line); got > width {
					t.Fatalf("width %d exceeds %d: %q", got, width, line)
				}
			}
			lines := strings.Split(out.String(), "\n")
			if got := uniseg.StringWidth(lines[1]); got != width {
				t.Fatalf("frame width %d, want %d", got, width)
			}
			if width >= 24 {
				if !strings.Contains(out.String(), "👩‍💻") || !strings.Contains(out.String(), "e\u0301") {
					t.Fatal("grapheme cluster was split")
				}
			}
		})
	}
}

func TestLayoutDisplayWidthAndSanitization(t *testing.T) {
	var out bytes.Buffer
	writer := &layoutWriter{Writer: &out, width: 80}
	writeField(writer, "中文", "\x1b[31m东京\x1b[0m\tDNS\nvalue")
	writeField(writer, "ASCII", "value")
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	for _, line := range lines {
		before, _, ok := strings.Cut(line, " : ")
		if !ok || uniseg.StringWidth(before) != 15 {
			t.Fatalf("label is not display-column aligned: %q", line)
		}
	}
	if strings.ContainsAny(out.String(), "\x1b\t") || !strings.Contains(out.String(), "东京 DNS value") {
		t.Fatalf("control characters were not normalized: %q", out.String())
	}
}

func TestTerminalWidthFallback(t *testing.T) {
	for _, value := range []string{"", "invalid", "0", "-1"} {
		t.Setenv("COLUMNS", value)
		if width := terminalWidth(&bytes.Buffer{}); width != boxMinWidth {
			t.Fatalf("fallback width %d", width)
		}
	}
}
