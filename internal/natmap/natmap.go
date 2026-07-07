// Package natmap attempts automatic router port mapping (UPnP IGD or
// NAT-PMP, whichever the local gateway supports — github.com/fd/go-nat tries
// both) so a contributor behind a home router's NAT becomes reachable by a
// remote coordinator with zero manual port-forwarding or public-IP lookup.
//
// Always best-effort and silent on failure: callers must have a working
// fallback (see internal/agent.resolveReachabilityEndpoint), since a real
// fraction of networks can't be solved this way at all — carrier-grade NAT
// (the ISP itself does the NAT, not the user's own router, so there's no
// local gateway that can help), UPnP/NAT-PMP disabled, or corporate/cloud
// networks with no consumer router in the path.
package natmap

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	nat "github.com/fd/go-nat"
)

const (
	// DiscoverTimeout bounds gateway discovery. The underlying library call
	// has no context/deadline support of its own, so this package enforces
	// one — node startup must never hang indefinitely on a network with no
	// UPnP/NAT-PMP-capable router at all (most cloud/VPC/corporate networks).
	DiscoverTimeout = 5 * time.Second

	// MappingLifetime is how long the router promises to keep the mapping
	// before it needs renewing.
	MappingLifetime = 1 * time.Hour

	// RenewInterval is roughly 1/3 of MappingLifetime — renews well before
	// expiry without re-requesting on every fast (~30s) heartbeat tick.
	RenewInterval = 20 * time.Minute
)

// gateway is the subset of nat.NAT this package actually uses, so tests can
// inject a fake without any real network/router access. A nat.NAT value
// satisfies this automatically (its method set is a superset).
type gateway interface {
	AddPortMapping(protocol string, internalPort int, description string, timeout time.Duration) (int, error)
	GetExternalAddress() (net.IP, error)
	DeletePortMapping(protocol string, internalPort int) error
}

// discoverGateway is swapped out in tests.
var discoverGateway = func() (gateway, error) {
	return nat.DiscoverGateway()
}

// Result is a successfully-established port mapping.
type Result struct {
	// Endpoint is the externally-reachable http(s)://host:port to advertise
	// to the coordinator in place of the auto-derived localhost fallback.
	Endpoint string
	// Close removes the port mapping from the router. Safe to call more
	// than once (idempotent) — always call it on graceful node shutdown so
	// the mapping doesn't linger pointing at a now-stopped node.
	Close func()
}

// TryMap attempts to map internalPort on the local gateway to an external
// port and returns the resulting external endpoint. Returns ok=false — NEVER
// an error the caller must handle — on any failure at any step: no gateway
// found, discovery timed out, mapping rejected, external IP unavailable.
// This is always a best-effort enhancement layered over an existing,
// already-working fallback, so failure here must never be fatal to node
// startup.
func TryMap(ctx context.Context, internalPort int, tlsEnabled bool) (Result, bool) {
	gw, ok := discoverWithTimeout(ctx, DiscoverTimeout)
	if !ok {
		return Result{}, false
	}

	externalPort, err := gw.AddPortMapping("tcp", internalPort, "oim-node", MappingLifetime)
	if err != nil {
		return Result{}, false
	}
	externalIP, err := gw.GetExternalAddress()
	if err != nil {
		_ = gw.DeletePortMapping("tcp", internalPort) // best-effort cleanup of the mapping we just added
		return Result{}, false
	}

	scheme := "http"
	if tlsEnabled {
		scheme = "https"
	}
	endpoint := fmt.Sprintf("%s://%s:%d", scheme, externalIP.String(), externalPort)

	var once sync.Once
	closeFn := func() {
		once.Do(func() { _ = gw.DeletePortMapping("tcp", internalPort) })
	}
	return Result{Endpoint: endpoint, Close: closeFn}, true
}

// Renew re-requests the same port mapping to extend its lease — routers
// generally treat a repeat AddPortMapping call for an already-mapped
// internal port as a renewal, not a duplicate/error. Returns false if
// renewal fails; the caller should log this as a warning; the node keeps
// running either way — it may just become unreachable again until the next
// successful attempt, exactly like it was before automatic mapping existed.
func Renew(internalPort int) bool {
	gw, ok := discoverWithTimeout(context.Background(), DiscoverTimeout)
	if !ok {
		return false
	}
	_, err := gw.AddPortMapping("tcp", internalPort, "oim-node", MappingLifetime)
	return err == nil
}

// discoverWithTimeout runs the blocking DiscoverGateway call in a goroutine
// and abandons it (returning ok=false) if it doesn't complete within timeout
// or ctx is canceled first. The abandoned goroutine still completes in the
// background and its result is simply discarded — DiscoverGateway does
// real network I/O with no cancellation hook, so there's no way to
// interrupt it early, only to stop waiting on it.
func discoverWithTimeout(ctx context.Context, timeout time.Duration) (gateway, bool) {
	type result struct {
		gw  gateway
		err error
	}
	done := make(chan result, 1)
	go func() {
		gw, err := discoverGateway()
		done <- result{gw, err}
	}()

	select {
	case r := <-done:
		return r.gw, r.err == nil
	case <-time.After(timeout):
		return nil, false
	case <-ctx.Done():
		return nil, false
	}
}
