package rules

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIndexMatchesLinear(t *testing.T) {
	patterns := []string{"example.com", "*.example.org", "*ample.net", "**x", "例子.测试", "EXAMPLE.COM.", "example.com"}
	domains := []string{"", ".", "example.com", "WWW.EXAMPLE.COM.", "superexample.com", "example.org", "x.example.org", "sample.net", "x..", "例子.测试", "子.例子.测试", "*x", "other.invalid"}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 500; i++ {
		domain := fmt.Sprintf("host%d.zone%d.test", rng.Intn(100), rng.Intn(30))
		patterns = append(patterns, domain, "*"+domain[4:])
		domains = append(domains, domain, "www."+domain, "super"+domain, domain+".", domain+".invalid")
	}
	for order := 0; order < 3; order++ {
		rng.Shuffle(len(patterns), func(i, j int) { patterns[i], patterns[j] = patterns[j], patterns[i] })
		index := new(domainIndex)
		// Check incremental insertion too: a broad pattern must not mask broken splits.
		for i, pattern := range patterns {
			index.add(pattern)
			for _, domain := range []string{pattern, "www." + pattern, "absent.zone99.test"} {
				checkIndex(t, index, patterns[:i+1], domain)
			}
		}
		for _, domain := range domains {
			checkIndex(t, index, patterns, domain)
		}
	}
}

func checkIndex(t testing.TB, index *domainIndex, patterns []string, domain string) {
	t.Helper()
	want := false
	for _, pattern := range patterns {
		want = want || linearMatch(domain, pattern)
	}
	if got := index.match(strings.ToLower(strings.TrimSuffix(domain, "."))); got != want {
		t.Fatalf("domain %q: indexed=%v linear=%v patterns=%q", domain, got, want, patterns)
	}
}

func FuzzIndexMatchesLinear(f *testing.F) {
	f.Add("example.com\n*example.net\nwww.example.com", "superexample.net.")
	f.Add("*\n.", "")
	f.Add("abc\nabcdef\nabd\nab", "x.abd")
	f.Fuzz(func(t *testing.T, data, domain string) {
		patterns := strings.Split(data, "\n")
		checkIndex(t, testIndex(patterns...), patterns, domain)
		m := NewMatcher()
		m.domainLists["list"] = testIndex(patterns...)
		normalized := strings.ToLower(strings.TrimSuffix(domain, "."))
		want := false
		for _, pattern := range patterns {
			want = want || linearMatch(normalized, pattern)
		}
		if got := m.Match(domain, "list"); got != want {
			t.Fatalf("matcher %q: indexed=%v linear=%v", domain, got, want)
		}
	})
}

func TestIndexedListLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "list")
	m := NewMatcher()
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if err := m.LoadDomainListAs("list", path); err != nil {
			t.Fatal(err)
		}
	}
	write("# comment\n\n Example.COM. \n*example.net\n")
	if !m.Match("www.example.com.", "list") || !m.Match("superexample.net", "list") {
		t.Fatal("loaded index lost matching semantics")
	}
	if err := m.LoadDomainListAs("list", path+".missing"); err == nil || !m.Match("example.com", "list") {
		t.Fatal("failed reload replaced the active index")
	}
	write("other.test\n")
	if m.Match("example.com", "list") || !m.Match("other.test", "list") {
		t.Fatal("reload retained old patterns")
	}
	m.ClearDomainList("list")
	if m.Match("other.test", "list") {
		t.Fatal("cleared list still matches")
	}
	write("*\n")
	m.ClearAllDomainLists()
	if m.Match("anything", "list") {
		t.Fatal("clear-all retained index")
	}
}

func benchmarkPatterns(size int) []string {
	patterns := make([]string, size)
	for i := range patterns {
		patterns[i] = fmt.Sprintf("host%d.zone%d.example", i, i%100)
		if i%10 == 0 {
			patterns[i] = "*" + patterns[i]
		}
	}
	return patterns
}

func BenchmarkDomainMatch(b *testing.B) {
	for _, size := range []int{1000, 100000, 300000} {
		patterns := benchmarkPatterns(size)
		m := NewMatcher()
		m.domainLists["list"] = testIndex(patterns...)
		for _, query := range []struct{ name, domain string }{
			{"first", "host0.zone0.example"},
			{"last", fmt.Sprintf("www.host%d.zone%d.example", size-1, (size-1)%100)},
			{"miss", "absent.zone99.example"},
			{"wildcard", "superhost0.zone0.example"},
		} {
			b.Run(fmt.Sprintf("%d/%s/indexed", size, query.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					m.Match(query.domain, "list")
				}
			})
			b.Run(fmt.Sprintf("%d/%s/linear", size, query.name), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					for _, pattern := range patterns {
						if linearMatch(query.domain, pattern) {
							break
						}
					}
				}
			})
		}
	}
}

func BenchmarkDomainIndexBuild(b *testing.B) {
	for _, size := range []int{1000, 100000, 300000} {
		patterns := benchmarkPatterns(size)
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				testIndex(patterns...)
			}
		})
	}
}
