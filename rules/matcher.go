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
	domainLists map[string]*domainIndex
}

// NewMatcher creates a new domain matcher
func NewMatcher() *Matcher {
	return &Matcher{
		domainLists: make(map[string]*domainIndex),
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

	index := new(domainIndex)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		index.add(line)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading domain list %s: %w", absPath, err)
	}

	m.domainLists[name] = index
	return nil
}

// ClearDomainList removes a domain list from memory
func (m *Matcher) ClearDomainList(filename string) {
	delete(m.domainLists, filename)
}

// ClearAllDomainLists removes all domain lists from memory
func (m *Matcher) ClearAllDomainLists() {
	m.domainLists = make(map[string]*domainIndex)
}

// Match checks if a domain matches any pattern in a domain list.
// Examples:
//   - "example.com" matches "example.com" and its subdomains such as "www.example.com",
//     but not "superexample.com"
//   - "*example.com" matches any domain ending in "example.com", including
//     "example.com", "www.example.com" and "superexample.com"
func (m *Matcher) Match(domain, listName string) bool {
	index, exists := m.domainLists[listName]
	if !exists {
		return false
	}

	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	return index.match(strings.TrimSuffix(domain, "."))
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
