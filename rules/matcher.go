package rules

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Matcher handles domain matching logic similar to dnsmasq
type Matcher struct {
	domainLists map[string][]string // filename -> list of domains
}

// NewMatcher creates a new domain matcher
func NewMatcher() *Matcher {
	return &Matcher{
		domainLists: make(map[string][]string),
	}
}

// LoadDomainList loads a domain list from file
func (m *Matcher) LoadDomainList(filename string) error {
	return m.LoadDomainListAs(filename, filename)
}

// LoadDomainListAs loads a file and registers its patterns under a configured name.
func (m *Matcher) LoadDomainListAs(name, filename string) error {
	absPath := filename
	if !strings.HasPrefix(filename, "/") {
		absPath = filepath.Join("/etc/updns/domainlists", filename)
	}

	file, err := os.Open(absPath)
	if err != nil {
		return fmt.Errorf("cannot open domain list file %s: %w", absPath, err)
	}
	defer file.Close()

	domains := []string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		domains = append(domains, line)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading domain list %s: %w", absPath, err)
	}

	m.domainLists[name] = domains
	return nil
}

// ClearDomainList removes a domain list from memory
func (m *Matcher) ClearDomainList(filename string) {
	delete(m.domainLists, filename)
}

// ClearAllDomainLists removes all domain lists from memory
func (m *Matcher) ClearAllDomainLists() {
	m.domainLists = make(map[string][]string)
}

// Match checks if a domain matches any pattern in a domain list
// Supports exact match and wildcard match (dnsmasq-style)
// Examples:
// - "example.com" matches "example.com" exactly
// - "*.example.com" matches "sub.example.com", "deep.sub.example.com", etc.
// - "example.com" as pattern also matches "www.example.com" (suffix match)
func (m *Matcher) Match(domain, listName string) bool {
	patterns, exists := m.domainLists[listName]
	if !exists {
		return false
	}

	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for _, pattern := range patterns {
		if m.matchPattern(domain, pattern) {
			return true
		}
	}
	return false
}

// matchPattern matches a domain against a pattern (dnsmasq-style)
func (m *Matcher) matchPattern(domain, pattern string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))

	// Exact match
	if domain == pattern {
		return true
	}

	// Wildcard match: *.example.com
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[2:] // Remove "*."
		if strings.HasSuffix(domain, "."+suffix) {
			return true
		}
		// Also match the suffix itself
		if domain == suffix {
			return true
		}
	}

	// Suffix match: example.com also matches www.example.com (for patterns without *)
	if !strings.Contains(pattern, "*") {
		if strings.HasSuffix(domain, "."+pattern) {
			return true
		}
	}

	return false
}

// MatchMultipleListsOR checks if domain matches any of the given lists (OR logic)
func (m *Matcher) MatchMultipleListsOR(domain string, listNames []string) bool {
	for _, listName := range listNames {
		if m.Match(domain, listName) {
			return true
		}
	}
	return false
}

// MatchMultipleListsAND checks if domain matches all of the given lists (AND logic)
func (m *Matcher) MatchMultipleListsAND(domain string, listNames []string) bool {
	if len(listNames) == 0 {
		return true
	}
	for _, listName := range listNames {
		if !m.Match(domain, listName) {
			return false
		}
	}
	return true
}
