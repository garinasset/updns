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

type selection struct {
	preferred string
	latency   time.Duration
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
	preferred := state.preferred
	latency := state.latency
	r.selectionMu.Unlock()

	var delay time.Duration
	if preferred != "" {
		r.mu.RLock()
		upstream := r.upstreams[preferred]
		if upstream != nil {
			delay = time.Duration(upstream.Timeout) * time.Second
		}
		r.mu.RUnlock()
		delay = selectionDelay(latency, delay)
	}
	response, winner, elapsed, competed, err := r.resolveTimed(ctx, domain, queryType, names, preferred, delay)
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	r.selectionMu.Lock()
	if epoch == r.selectionEpoch && r.selections[key] == state {
		if err == nil {
			if competed {
				state.preferred = winner
				state.latency = elapsed
			}
			state.revision++
		} else if err != nil && state.revision == revision {
			state.preferred = ""
		}
	}
	r.selectionMu.Unlock()
	return response, winner, err
}

func selectionDelay(latency, timeout time.Duration) time.Duration {
	// Compare before doubling to avoid duration overflow.
	if latency > 0 && latency < timeout/2 {
		return 2 * latency
	}
	return timeout
}

// resolveTimed retains the original request when the delay triggers hedging.
// Each response carries latency measured from its own upstream request start.
func (r *Resolver) resolveTimed(ctx context.Context, domain string, queryType uint16, names []string, preferred string, delay time.Duration) (*dns.Msg, string, time.Duration, bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type answer struct {
		response *dns.Msg
		name     string
		elapsed  time.Duration
		err      error
	}
	answers := make(chan answer, len(names))
	pending := 0
	launch := func(group []string) {
		for _, name := range group {
			pending++
			go func(name string) {
				start := time.Now()
				response, err := r.Resolve(ctx, domain, queryType, name)
				answers <- answer{response, name, time.Since(start), err}
			}(name)
		}
	}
	competed := preferred == ""
	var timer *time.Timer
	var deadline <-chan time.Time
	if competed {
		launch(names)
	} else {
		launch([]string{preferred})
		timer = time.NewTimer(delay)
		defer timer.Stop()
		deadline = timer.C
	}
	expand := func() {
		competed = true
		deadline = nil
		launch(otherUpstreams(names, preferred))
	}
	var failures []string
	allNotReady := true
	for pending > 0 {
		select {
		case result := <-answers:
			pending--
			if err := ctx.Err(); err != nil {
				return nil, "", 0, competed, err
			}
			if result.err == nil {
				return result.response, result.name, result.elapsed, competed, nil
			}
			failures = append(failures, upstreamFailure(result.name, result.response, result.err))
			allNotReady = allNotReady && notReady(result.name, result.err)
			if !competed {
				expand()
			}
		case <-deadline:
			// Prefer an already completed result over launching extra work.
			select {
			case result := <-answers:
				pending--
				if err := ctx.Err(); err != nil {
					return nil, "", 0, competed, err
				}
				if result.err == nil {
					return result.response, result.name, result.elapsed, false, nil
				}
				failures = append(failures, upstreamFailure(result.name, result.response, result.err))
				allNotReady = allNotReady && notReady(result.name, result.err)
			default:
			}
			expand()
		case <-ctx.Done():
			return nil, "", 0, competed, ctx.Err()
		}
	}
	return nil, "", 0, competed, allUpstreamsFailed(domain, failures, allNotReady)
}
