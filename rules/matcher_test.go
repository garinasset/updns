package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatchExact(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		domain   string
		pattern  string
		expected bool
	}{
		{"example.com", "example.com", true},
		{"www.example.com", "example.com", true}, // suffix match
		{"example.com", "www.example.com", false},
		{"notexample.com", "example.com", false},
	}

	for _, tt := range tests {
		result := matchIndexed(m, tt.domain, tt.pattern)
		if result != tt.expected {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.domain, tt.pattern, result, tt.expected)
		}
	}
}

func TestMatchWildcard(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		domain   string
		pattern  string
		expected bool
	}{
		{"example.com", "*example.com", true},
		{"www.example.com", "*example.com", true},
		{"superexample.com", "*example.com", true},
		{"example.org", "*example.com", false},
		{"superexample.com", "example.com", false},
		{"www.example.com", "example.com", true},
	}

	for _, tt := range tests {
		result := matchIndexed(m, tt.domain, tt.pattern)
		if result != tt.expected {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.domain, tt.pattern, result, tt.expected)
		}
	}
}

func TestMatchCaseInsensitive(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		domain   string
		pattern  string
		expected bool
	}{
		{"EXAMPLE.COM", "example.com", true},
		{"Example.Com", "EXAMPLE.COM", true},
		{"WwW.EXAMPLE.COM", "example.com", true},
	}

	for _, tt := range tests {
		result := matchIndexed(m, tt.domain, tt.pattern)
		if result != tt.expected {
			t.Errorf("matchPattern(%q, %q) = %v, want %v", tt.domain, tt.pattern, result, tt.expected)
		}
	}
}

func TestMatchMultipleListsOR(t *testing.T) {
	m := NewMatcher()

	// Create test patterns directly
	m.domainLists["list1"] = testIndex("example.com")
	m.domainLists["list2"] = testIndex("test.com")

	if !m.MatchMultipleListsOR("example.com", []string{"list1", "list2"}) {
		t.Error("Expected to match in list1")
	}

	if !m.MatchMultipleListsOR("test.com", []string{"list1", "list2"}) {
		t.Error("Expected to match in list2")
	}

	if m.MatchMultipleListsOR("notfound.com", []string{"list1", "list2"}) {
		t.Error("Expected not to match")
	}
}

func TestMatchMultipleListsAND(t *testing.T) {
	m := NewMatcher()

	m.domainLists["list1"] = testIndex("example.com")
	m.domainLists["list2"] = testIndex("example.com")

	if !m.MatchMultipleListsAND("example.com", []string{"list1", "list2"}) {
		t.Error("Expected to match in both lists")
	}

	m.domainLists["list2"] = testIndex("test.com")
	if m.MatchMultipleListsAND("example.com", []string{"list1", "list2"}) {
		t.Error("Expected not to match in both lists")
	}
}

func testIndex(patterns ...string) *domainIndex {
	index := new(domainIndex)
	for _, pattern := range patterns {
		index.add(pattern)
	}
	return index
}

func matchIndexed(m *Matcher, domain, pattern string) bool {
	m.domainLists["test"] = testIndex(pattern)
	return m.Match(domain, "test")
}

func linearMatch(domain, pattern string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(domain, pattern[1:])
	}
	return domain == pattern || strings.HasSuffix(domain, "."+pattern)
}

func TestLoadDomainListAsUsesConfiguredName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "domains.txt")
	if err := os.WriteFile(path, []byte("example.com\n"), 0600); err != nil {
		t.Fatal(err)
	}

	matcher := NewMatcher()
	if err := matcher.LoadDomainListAs("custom-name", path); err != nil {
		t.Fatal(err)
	}
	if !matcher.Match("www.example.com", "custom-name") {
		t.Fatal("configured name did not resolve to the loaded domain-list file")
	}
	if matcher.Match("www.example.com", path) {
		t.Fatal("domain list should be registered by its custom name, not file path")
	}
}
