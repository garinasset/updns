package server

import (
	"context"
	"errors"
	"maps"
	"time"

	"github.com/garinasset/updns/cache"
	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

type queryFlight struct {
	done    chan struct{}
	cancel  context.CancelFunc
	preload bool
	route   string
	result  *QueryResult
	err     error
}

// queryMu protects registration and commit; network I/O never holds it.
func (s *Server) startQueryLocked(key, domain string, queryType uint16, client string, own *config.Rule, route string, previous *cache.CacheEntry, epoch uint64, preload bool) *queryFlight {
	ctx, cancel := context.WithCancel(s.ctx)
	flight := &queryFlight{done: make(chan struct{}), cancel: cancel, preload: preload, route: route}
	if s.flights == nil {
		s.flights = make(map[string]*queryFlight)
	}
	s.flights[key] = flight
	if preload {
		s.preloading++
	}
	s.queryWG.Add(1)
	go func() {
		defer s.queryWG.Done()
		defer cancel()
		preferred := ""
		if previous != nil && previous.Route == route {
			preferred = previous.Upstream
		}
		current := s.routeForRule(domain, own)
		if current.signature != route {
			preferred = ""
		}
		result, err := s.resolveQueryForRoute(ctx, domain, queryType, client, preferred, current)
		s.queryMu.Lock()
		defer s.queryMu.Unlock()
		if ctx.Err() != nil {
			result, err = nil, ctx.Err()
		}
		if result == nil && err == nil {
			err = errors.New("DNS resolution returned no result")
		}
		if s.flights[key] == flight {
			if s.cache != nil {
				var replacement *cache.CacheEntry
				if err == nil && result != nil && result.TTL > 0 && !result.LocalAnswer && !isLocalAnswerAction(result.Action) {
					now := time.Now()
					replacement = &cache.CacheEntry{
						Query: domain, QueryType: queryType, Answers: result.Records, Authority: result.Authority,
						Upstream: result.UsedUpstream, Rcode: result.Rcode, TTL: result.TTL, Route: result.route,
						CreatedAt: now, ExpireTime: now.Add(time.Duration(result.TTL) * time.Second),
					}
				}
				committed := s.cache.Commit(key, previous, epoch, replacement)
				if committed && s.started && (replacement != nil || previous != nil) {
					s.publishCacheSize()
				}
			}
			delete(s.flights, key)
			if preload {
				s.preloading--
			}
		}
		flight.result, flight.err = result, err
		close(flight.done)
	}()
	return flight
}

func cloneQueryResult(original *QueryResult) *QueryResult {
	result := *original
	result.Answers = append([]string(nil), original.Answers...)
	result.Records = copyRecords(original.Records)
	result.Authority = copyRecords(original.Authority)
	result.IPv4InSets, result.IPv6InSets = maps.Clone(original.IPv4InSets), maps.Clone(original.IPv6InSets)
	result.IPv4NFTSets = append([]string(nil), original.IPv4NFTSets...)
	result.IPv6NFTSets = append([]string(nil), original.IPv6NFTSets...)
	return &result
}

func copyRecords(records []dns.RR) []dns.RR {
	result := make([]dns.RR, len(records))
	for i, rr := range records {
		result[i] = dns.Copy(rr)
	}
	return result
}
