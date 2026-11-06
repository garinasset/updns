package resolver

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

const selectionQueries = 50
const selectionInterval = 20 * time.Second

type selection struct {
	preferred string
	queries   int
	tested    time.Time
	revision  uint64
}

func selectionKey(names []string) string {
	var key strings.Builder
	for _, name := range names {
		fmt.Fprintf(&key, "%d:%s", len(name), name)
	}
	return key.String()
}

func otherUpstreams(names []string, excluded string) []string {
	remaining := make([]string, 0, len(names))
	for _, name := range names {
		if name != excluded {
			remaining = append(remaining, name)
		}
	}
	return remaining
}

func (r *Resolver) ClearSelection() {
	r.selectionMu.Lock()
	defer r.selectionMu.Unlock()
	r.selections = make(map[string]*selection)
	r.selectionEpoch++
}

// ResolveStrategy bypasses group re-evaluation when refreshing a known path.
func (r *Resolver) ResolveStrategy(ctx context.Context, domain string, queryType uint16, names []string, strategy config.QueryStrategy, preferred string) (*dns.Msg, string, error) {
	if !strategy.Valid() {
		return nil, "", fmt.Errorf("invalid query strategy %q", strategy)
	}
	if len(names) == 0 {
		return nil, "", fmt.Errorf("no upstreams specified")
	}
	if slices.Contains(names, preferred) {
		response, err := r.Resolve(ctx, domain, queryType, preferred)
		if err == nil {
			return response, preferred, nil
		}
		if ctx.Err() != nil {
			return nil, "", ctx.Err()
		}
		remaining := otherUpstreams(names, preferred)
		if len(remaining) == 0 {
			return nil, "", allUpstreamsFailed(domain, []string{upstreamFailure(preferred, response, err)}, notReady(preferred, err))
		}
		fallback, winner, fallbackErr := r.resolveStrategy(ctx, domain, queryType, remaining, strategy)
		if fallbackErr != nil {
			if ctx.Err() != nil {
				return nil, "", ctx.Err()
			}
			return nil, "", allUpstreamsFailed(domain, []string{upstreamFailure(preferred, response, err), fallbackErr.Error()},
				notReady(preferred, err) && errors.Is(fallbackErr, ErrNetworkNotReady))
		}
		return fallback, winner, nil
	}
	return r.resolveStrategy(ctx, domain, queryType, names, strategy)
}

func (r *Resolver) resolveStrategy(ctx context.Context, domain string, queryType uint16, names []string, strategy config.QueryStrategy) (*dns.Msg, string, error) {
	switch strategy {
	case config.StrategyStrict:
		return r.ResolveMultiple(ctx, domain, queryType, names)
	case config.StrategyConcurrent:
		return r.ResolveConcurrent(ctx, domain, queryType, names)
	default:
		return r.resolveOptimal(ctx, domain, queryType, names)
	}
}

func (r *Resolver) resolveOptimal(ctx context.Context, domain string, queryType uint16, names []string) (*dns.Msg, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if len(names) == 1 {
		return r.ResolveMultiple(ctx, domain, queryType, names)
	}
	key := selectionKey(names)
	r.selectionMu.Lock()
	state := r.selections[key]
	if state == nil {
		state = &selection{}
		r.selections[key] = state
	}
	epoch := r.selectionEpoch
	revision := state.revision
	now := r.now()
	retest := state.preferred == "" || state.queries >= selectionQueries || now.Sub(state.tested) >= selectionInterval
	preferred := state.preferred
	if retest {
		state.queries = 0
		state.tested = now
	} else {
		state.queries++
	}
	r.selectionMu.Unlock()

	var response *dns.Msg
	var winner string
	var err error
	if retest {
		response, winner, err = r.ResolveConcurrent(ctx, domain, queryType, names)
	} else {
		response, err = r.Resolve(ctx, domain, queryType, preferred)
		winner = preferred
		if err != nil && ctx.Err() == nil {
			firstResponse, firstErr := response, err
			response, winner, err = r.ResolveConcurrent(ctx, domain, queryType, otherUpstreams(names, preferred))
			if err != nil && ctx.Err() == nil {
				err = allUpstreamsFailed(domain, []string{upstreamFailure(preferred, firstResponse, firstErr), err.Error()},
					notReady(preferred, firstErr) && errors.Is(err, ErrNetworkNotReady))
			}
		}
	}
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	r.selectionMu.Lock()
	if epoch == r.selectionEpoch && r.selections[key] == state {
		if err == nil {
			state.preferred = winner
			state.revision++
		} else if state.revision == revision {
			state.preferred = ""
		}
	}
	r.selectionMu.Unlock()
	return response, winner, err
}
