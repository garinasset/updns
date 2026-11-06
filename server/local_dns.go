package server

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/garinasset/updns/logging"
	"github.com/miekg/dns"
)

var errLocalDNSData = errors.New("local DNS data unavailable")

type localSource struct {
	path  string
	lease bool
	info  fs.FileInfo
}

type localRecord struct {
	address netip.Addr
	name    string
	lease   bool
	expires time.Time
}

type localDNS struct {
	mu         sync.Mutex
	configPath string
	configInfo fs.FileInfo
	configured bool
	settings   config.LocalDNSConfig
	sources    []localSource
	loaded     bool
	forward    map[string][]localRecord
	reverse    map[string][]localRecord
	now        func() time.Time
}

func newLocalDNS(configPath string) *localDNS {
	return &localDNS{configPath: configPath, now: time.Now}
}

func sameLocalFile(a, b fs.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func optionalLocalStat(path string) (fs.FileInfo, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return info, err
}

func (l *localDNS) reload() error {
	info, err := optionalLocalStat(l.configPath)
	if err != nil {
		return fmt.Errorf("stat DHCP configuration %s: %w", l.configPath, err)
	}
	configChanged := !l.configured || !sameLocalFile(info, l.configInfo)
	if configChanged {
		l.loaded = false
		settings, err := config.LoadLocalDNSConfig(l.configPath)
		if errors.Is(err, os.ErrNotExist) {
			logging.Debugf("local DHCP configuration %s absent; using default local DNS paths", l.configPath)
		} else if err != nil {
			return err
		}
		l.settings, l.configInfo, l.configured = settings, info, true
	}
	var sources []localSource
	seen := make(map[string]bool)
	add := func(path string, lease bool) error {
		if path == "" || seen[path] {
			return nil
		}
		seen[path] = true
		info, err := optionalLocalStat(path)
		if err != nil {
			return fmt.Errorf("stat local DNS source %s: %w", path, err)
		}
		sources = append(sources, localSource{path, lease, info})
		return nil
	}
	for _, path := range l.settings.HostsFiles {
		if err := add(path, false); err != nil {
			return err
		}
	}
	for _, dir := range l.settings.HostsDirs {
		files, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read local hosts directory %s: %w", dir, err)
		}
		for _, file := range files {
			if !file.IsDir() {
				if err := add(filepath.Join(dir, file.Name()), false); err != nil {
					return err
				}
			}
		}
	}
	if err := add(l.settings.LeaseFile, true); err != nil {
		return err
	}
	changed := configChanged || !l.loaded || len(sources) != len(l.sources)
	if !changed {
		for i, source := range sources {
			old := l.sources[i]
			if source.path != old.path || source.lease != old.lease || !sameLocalFile(source.info, old.info) {
				changed = true
				break
			}
		}
	}
	if !changed {
		return nil
	}
	forward, reverse := make(map[string][]localRecord), make(map[string][]localRecord)
	for i, source := range sources {
		if source.info == nil {
			logging.Debugf("local DNS source %s absent", source.path)
			continue
		}
		if err := readLocalSource(source, l.settings, forward, reverse); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				logging.Debugf("local DNS source %s disappeared during reload", source.path)
				sources[i].info = nil
				continue
			}
			return err
		}
	}
	l.sources, l.forward, l.reverse, l.loaded = sources, forward, reverse, true
	return nil
}

func localName(name string) (string, bool) {
	name = strings.ToLower(dns.Fqdn(name))
	_, ok := dns.IsDomainName(name)
	return name, ok && name != "." && name != "*."
}

func readLocalSource(source localSource, settings config.LocalDNSConfig, forward, reverse map[string][]localRecord) error {
	file, err := os.Open(source.path)
	if err != nil {
		return fmt.Errorf("open local DNS source %s: %w", source.path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Fields(strings.SplitN(scanner.Text(), "#", 2)[0])
		if len(fields) == 0 || fields[0] == "duid" {
			continue
		}
		ipIndex, nameIndex := 0, 1
		var expires time.Time
		if source.lease {
			ipIndex, nameIndex = 2, 3
			if len(fields) < 4 {
				logging.Warnf("invalid DHCP lease at %s:%d", source.path, line)
				continue
			}
			expiry, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil || expiry < 0 {
				logging.Warnf("invalid DHCP lease expiry at %s:%d", source.path, line)
				continue
			}
			if expiry != 0 {
				expires = time.Unix(expiry, 0)
			}
			if fields[nameIndex] == "*" {
				continue
			}
		}
		if len(fields) <= nameIndex {
			logging.Warnf("invalid hosts entry at %s:%d", source.path, line)
			continue
		}
		address, err := netip.ParseAddr(fields[ipIndex])
		if err != nil || address.Zone() != "" {
			logging.Warnf("invalid local DNS address at %s:%d", source.path, line)
			continue
		}
		address = address.Unmap()
		names := fields[nameIndex:]
		if source.lease {
			names = names[:1]
		}
		canonical := ""
		for _, raw := range names {
			name, valid := localName(raw)
			if !valid {
				logging.Warnf("invalid local DNS name at %s:%d", source.path, line)
				continue
			}
			qualified := name
			if !strings.Contains(strings.TrimSuffix(name, "."), ".") && settings.Domain != "" && (source.lease || settings.ExpandHosts) {
				var ok bool
				qualified, ok = localName(strings.TrimSuffix(name, ".") + "." + settings.Domain)
				if !ok {
					return fmt.Errorf("invalid local DNS domain %q", settings.Domain)
				}
			}
			record := localRecord{address: address, name: qualified, lease: source.lease, expires: expires}
			forward[name] = append(forward[name], record)
			if qualified != name {
				forward[qualified] = append(forward[qualified], record)
			}
			if canonical == "" {
				canonical = qualified
				reversed, err := dns.ReverseAddr(address.String())
				if err != nil {
					return fmt.Errorf("reverse local address %s: %w", address, err)
				}
				reverse[reversed] = append(reverse[reversed], record)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read local DNS source %s: %w", source.path, err)
	}
	return nil
}

func (l *localDNS) lookup(domain string, queryType uint16) (*dns.Msg, string, error) {
	if queryType != dns.TypeA && queryType != dns.TypeAAAA && queryType != dns.TypePTR && queryType != dns.TypeANY {
		return nil, "", nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.reload(); err != nil {
		return nil, "", fmt.Errorf("%w: %v", errLocalDNSData, err)
	}
	domain = strings.ToLower(dns.Fqdn(domain))
	records := l.forward[domain]
	if queryType == dns.TypePTR {
		records = l.reverse[domain]
	}
	now := l.now()
	var active []localRecord
	haveHosts := false
	for _, record := range records {
		if record.expires.IsZero() || now.Before(record.expires) {
			active = append(active, record)
			haveHosts = haveHosts || !record.lease
		}
	}
	if len(active) == 0 {
		return nil, "", nil
	}
	response := new(dns.Msg)
	response.SetQuestion(domain, queryType)
	response.Response = true
	source := "local-dhcp"
	seen := make(map[string]bool)
	for _, record := range active {
		if haveHosts && record.lease {
			continue
		}
		if haveHosts {
			source = "local-hosts"
		}
		var rr dns.RR
		header := dns.RR_Header{Name: domain, Class: dns.ClassINET}
		switch {
		case queryType == dns.TypePTR:
			header.Rrtype = dns.TypePTR
			rr = &dns.PTR{Hdr: header, Ptr: record.name}
		case record.address.Is4() && (queryType == dns.TypeA || queryType == dns.TypeANY):
			header.Rrtype = dns.TypeA
			rr = &dns.A{Hdr: header, A: record.address.AsSlice()}
		case record.address.Is6() && (queryType == dns.TypeAAAA || queryType == dns.TypeANY):
			header.Rrtype = dns.TypeAAAA
			rr = &dns.AAAA{Hdr: header, AAAA: record.address.AsSlice()}
		}
		if rr != nil && !seen[rr.String()] {
			seen[rr.String()] = true
			response.Answer = append(response.Answer, rr)
		}
	}
	return response, source, nil
}

// queryLocal answers from hosts/DHCP data; unreadable local data is logged
// and treated as a miss so cache and upstream resolution keep working.
func (s *Server) queryLocal(domain string, queryType uint16) (*QueryResult, bool) {
	route := s.routeFor(domain)
	if s.local == nil || (route.rule != nil && isLocalAnswerAction(route.rule.Action)) {
		return nil, false
	}
	response, source, err := s.local.lookup(domain, queryType)
	if err != nil {
		if ok, summary := s.warnLimit.allow("local-dns-data", ""); ok {
			logging.Warnf("%v; falling back to cache and upstream DNS (%s)", err, formatSuppressed(summary, s.warnLimit.interval))
		}
		return nil, false
	}
	if response == nil {
		return nil, false
	}
	result := s.metadataFor(domain, queryType, route)
	result.LocalAnswer, result.UsedUpstream = true, source
	s.processResponse(result, queryType, response)
	return result, true
}

func (s *Server) discardLocalCache(domain string, queryType uint16) {
	s.queryMu.Lock()
	defer s.queryMu.Unlock()
	if s.cache != nil {
		key := fmt.Sprintf("%s:%d", domain, queryType)
		if previous, _ := s.cache.Snapshot(key); previous != nil {
			s.cache.Remove(key)
			if s.started {
				s.publishCacheSize()
			}
		}
	}
}
