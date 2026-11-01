package resolver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/garinasset/updns/config"
	"github.com/miekg/dns"
)

func unreachableErr() error {
	return fmt.Errorf("UDP query failed: %w", &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ENETUNREACH)})
}

func TestUnreachableUpstreamIsPausedThenProbed(t *testing.T) {
	r := NewResolver(0)
	now := time.Unix(1000, 0)
	r.now = func() time.Time { return now }

	if !r.allow("up") {
		t.Fatal("healthy upstream must be allowed")
	}
	r.record(context.Background(), "up", unreachableErr())
	if r.allow("up") {
		t.Fatal("upstream must be paused right after an unreachable error")
	}

	now = now.Add(unreachablePause + time.Millisecond)
	if !r.allow("up") {
		t.Fatal("one probe must be allowed after the pause")
	}
	if r.allow("up") {
		t.Fatal("only one probe may run per pause window")
	}

	r.record(context.Background(), "up", unreachableErr())
	if r.allow("up") {
		t.Fatal("failed probe must keep the upstream paused")
	}

	now = now.Add(unreachablePause + time.Millisecond)
	if !r.allow("up") {
		t.Fatal("probe expected")
	}
	r.record(context.Background(), "up", nil)
	if !r.allow("up") || !r.allow("up") {
		t.Fatal("successful probe must clear the pause")
	}
}

func TestNonNetworkErrorsDoNotPause(t *testing.T) {
	r := NewResolver(0)
	r.record(context.Background(), "up", errors.New("i/o timeout"))
	if !r.allow("up") {
		t.Fatal("timeouts must not pause an upstream")
	}
}

func TestPausedUpstreamSkipsQueryAndReportsNotReady(t *testing.T) {
	r := NewResolver(0)
	r.RegisterUpstream(&config.UpstreamDNS{Name: "up", Address: "192.0.2.1", Protocol: "udp"})
	r.record(context.Background(), "up", unreachableErr())

	_, _, err := r.ResolveMultiple(context.Background(), "example.com", dns.TypeA, []string{"up", "local-resolv-1"})
	if !errors.Is(err, ErrNetworkNotReady) {
		t.Fatalf("error = %v, want ErrNetworkNotReady", err)
	}
	_, _, err = r.ResolveConcurrent(context.Background(), "example.com", dns.TypeA, []string{"up", "local-resolv-1"})
	if !errors.Is(err, ErrNetworkNotReady) {
		t.Fatalf("concurrent error = %v, want ErrNetworkNotReady", err)
	}
}

func TestUnknownNonLocalUpstreamIsNotNetworkNotReady(t *testing.T) {
	r := NewResolver(0)
	_, _, err := r.ResolveMultiple(context.Background(), "example.com", dns.TypeA, []string{"typo"})
	if err == nil || errors.Is(err, ErrNetworkNotReady) {
		t.Fatalf("error = %v, want a real failure", err)
	}
}

func TestReplacingLocalUpstreamsResetsPause(t *testing.T) {
	r := NewResolver(0)
	r.record(context.Background(), "local-resolv-1", unreachableErr())
	r.ReplaceUpstreamsWithPrefix("local-resolv-", nil)
	if !r.allow("local-resolv-1") {
		t.Fatal("replacing local upstreams must reset their pause state")
	}
}
