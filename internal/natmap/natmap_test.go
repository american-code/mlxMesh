package natmap

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGateway is a controllable stand-in for a real UPnP/NAT-PMP router —
// there's no way to unit test against a real one, so every TryMap/Renew
// path is exercised through this instead.
type fakeGateway struct {
	addPortMappingErr    error
	addPortMappingResult int
	externalAddr         net.IP
	getExternalAddrErr   error

	addPortMappingCalls    int32
	deletePortMappingCalls int32
}

func (f *fakeGateway) AddPortMapping(protocol string, internalPort int, description string, timeout time.Duration) (int, error) {
	atomic.AddInt32(&f.addPortMappingCalls, 1)
	if f.addPortMappingErr != nil {
		return 0, f.addPortMappingErr
	}
	return f.addPortMappingResult, nil
}

func (f *fakeGateway) GetExternalAddress() (net.IP, error) {
	if f.getExternalAddrErr != nil {
		return nil, f.getExternalAddrErr
	}
	return f.externalAddr, nil
}

func (f *fakeGateway) DeletePortMapping(protocol string, internalPort int) error {
	atomic.AddInt32(&f.deletePortMappingCalls, 1)
	return nil
}

func withFakeDiscover(t *testing.T, gw gateway, err error) {
	t.Helper()
	original := discoverGateway
	discoverGateway = func() (gateway, error) { return gw, err }
	t.Cleanup(func() { discoverGateway = original })
}

func TestTryMap_Success(t *testing.T) {
	fg := &fakeGateway{addPortMappingResult: 51820, externalAddr: net.ParseIP("203.0.113.5")}
	withFakeDiscover(t, fg, nil)

	result, ok := TryMap(context.Background(), 8765, false)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if want := "http://203.0.113.5:51820"; result.Endpoint != want {
		t.Errorf("endpoint = %q, want %q", result.Endpoint, want)
	}
}

func TestTryMap_SuccessWithTLS(t *testing.T) {
	fg := &fakeGateway{addPortMappingResult: 8765, externalAddr: net.ParseIP("203.0.113.5")}
	withFakeDiscover(t, fg, nil)

	result, ok := TryMap(context.Background(), 8765, true)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if want := "https://203.0.113.5:8765"; result.Endpoint != want {
		t.Errorf("endpoint = %q, want %q (tlsEnabled must select https)", result.Endpoint, want)
	}
}

func TestTryMap_DiscoveryFails(t *testing.T) {
	withFakeDiscover(t, nil, errors.New("no NAT found"))

	_, ok := TryMap(context.Background(), 8765, false)
	if ok {
		t.Fatal("expected ok=false when gateway discovery fails")
	}
}

func TestTryMap_AddPortMappingFails(t *testing.T) {
	fg := &fakeGateway{addPortMappingErr: errors.New("mapping rejected")}
	withFakeDiscover(t, fg, nil)

	_, ok := TryMap(context.Background(), 8765, false)
	if ok {
		t.Fatal("expected ok=false when AddPortMapping fails")
	}
	if atomic.LoadInt32(&fg.deletePortMappingCalls) != 0 {
		t.Error("nothing was mapped — DeletePortMapping should not have been called")
	}
}

func TestTryMap_GetExternalAddressFailsCleansUpMapping(t *testing.T) {
	fg := &fakeGateway{addPortMappingResult: 8765, getExternalAddrErr: errors.New("no external address")}
	withFakeDiscover(t, fg, nil)

	_, ok := TryMap(context.Background(), 8765, false)
	if ok {
		t.Fatal("expected ok=false when GetExternalAddress fails")
	}
	if atomic.LoadInt32(&fg.deletePortMappingCalls) != 1 {
		t.Errorf("expected the just-added mapping to be cleaned up, got %d DeletePortMapping calls",
			atomic.LoadInt32(&fg.deletePortMappingCalls))
	}
}

func TestTryMap_CloseIsIdempotent(t *testing.T) {
	fg := &fakeGateway{addPortMappingResult: 8765, externalAddr: net.ParseIP("203.0.113.5")}
	withFakeDiscover(t, fg, nil)

	result, ok := TryMap(context.Background(), 8765, false)
	if !ok {
		t.Fatal("expected ok=true")
	}
	result.Close()
	result.Close()
	result.Close()
	if got := atomic.LoadInt32(&fg.deletePortMappingCalls); got != 1 {
		t.Errorf("expected exactly 1 DeletePortMapping call across 3 Close() calls, got %d", got)
	}
}

func TestDiscoverWithTimeout_NeverHangsPastTheDeadline(t *testing.T) {
	// Simulates a router/network that never answers at all — discoverGateway
	// blocks forever. This must never make node startup hang: the timeout,
	// not the fake, is what has to end the test.
	blockForever := make(chan struct{})
	original := discoverGateway
	discoverGateway = func() (gateway, error) {
		<-blockForever
		return nil, nil // unreachable in this test
	}
	defer func() { discoverGateway = original; close(blockForever) }()

	start := time.Now()
	_, ok := discoverWithTimeout(context.Background(), 50*time.Millisecond)
	elapsed := time.Since(start)

	if ok {
		t.Fatal("expected ok=false for a discovery call that never returns")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("discoverWithTimeout took %s, expected to bail out at ~50ms", elapsed)
	}
}

func TestDiscoverWithTimeout_RespectsContextCancellation(t *testing.T) {
	blockForever := make(chan struct{})
	original := discoverGateway
	discoverGateway = func() (gateway, error) {
		<-blockForever
		return nil, nil
	}
	defer func() { discoverGateway = original; close(blockForever) }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled

	_, ok := discoverWithTimeout(ctx, DiscoverTimeout)
	if ok {
		t.Fatal("expected ok=false for an already-canceled context")
	}
}

func TestRenew_Success(t *testing.T) {
	fg := &fakeGateway{addPortMappingResult: 8765}
	withFakeDiscover(t, fg, nil)

	if !Renew(8765) {
		t.Fatal("expected Renew to succeed")
	}
	if atomic.LoadInt32(&fg.addPortMappingCalls) != 1 {
		t.Error("expected Renew to re-request the mapping via AddPortMapping")
	}
}

func TestRenew_FailsWithoutPanicking(t *testing.T) {
	fg := &fakeGateway{addPortMappingErr: errors.New("lease rejected")}
	withFakeDiscover(t, fg, nil)

	if Renew(8765) {
		t.Fatal("expected Renew to report failure")
	}
}
