package rules

import (
	"sort"
	"strings"
)

// Edges contain reversed bytes; terminals distinguish DNS-label and raw suffixes.
type domainIndex struct {
	edge     string
	domain   bool
	wildcard bool
	children []domainIndex
}

func (n *domainIndex) add(pattern string) {
	pattern = strings.ToLower(strings.TrimSuffix(pattern, "."))
	wildcard := strings.HasPrefix(pattern, "*")
	if wildcard {
		pattern = pattern[1:]
	}
	reversed := []byte(pattern)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	n.insert(string(reversed), wildcard)
}

func (n *domainIndex) insert(key string, wildcard bool) {
	if key == "" {
		if wildcard {
			n.wildcard = true
		} else {
			n.domain = true
		}
		return
	}
	i := sort.Search(len(n.children), func(i int) bool { return n.children[i].edge[0] >= key[0] })
	if i == len(n.children) || n.children[i].edge[0] != key[0] {
		n.children = append(n.children, domainIndex{})
		copy(n.children[i+1:], n.children[i:])
		n.children[i] = domainIndex{edge: key, domain: !wildcard, wildcard: wildcard}
		return
	}
	child := &n.children[i]
	common := 0
	for common < len(key) && common < len(child.edge) && key[common] == child.edge[common] {
		common++
	}
	if common < len(child.edge) {
		tail := *child
		tail.edge = tail.edge[common:]
		*child = domainIndex{edge: child.edge[:common], children: []domainIndex{tail}}
	}
	child.insert(key[common:], wildcard)
}

func (n *domainIndex) match(domain string) bool {
	remaining := len(domain)
	for {
		if n.wildcard || n.domain && (remaining == 0 || domain[remaining-1] == '.') {
			return true
		}
		if remaining == 0 {
			return false
		}
		next := domain[remaining-1]
		i := sort.Search(len(n.children), func(i int) bool { return n.children[i].edge[0] >= next })
		if i == len(n.children) || n.children[i].edge[0] != next {
			return false
		}
		n = &n.children[i]
		if len(n.edge) > remaining {
			return false
		}
		for j := 0; j < len(n.edge); j++ {
			if n.edge[j] != domain[remaining-1-j] {
				return false
			}
		}
		remaining -= len(n.edge)
	}
}
