package agent

import "testing"

func TestResolveReachabilityEndpoint_PlainHTTP(t *testing.T) {
	got, err := resolveReachabilityEndpoint(":8765", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "http://localhost:8765"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveReachabilityEndpoint_TLS(t *testing.T) {
	got, err := resolveReachabilityEndpoint(":8765", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "https://localhost:8765"; got != want {
		t.Errorf("got %q, want %q (tlsEnabled must select https)", got, want)
	}
}

func TestResolveReachabilityEndpoint_ExplicitHost(t *testing.T) {
	got, err := resolveReachabilityEndpoint("192.168.1.10:8765", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "http://192.168.1.10:8765"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveReachabilityEndpoint_InvalidListenAddr(t *testing.T) {
	if _, err := resolveReachabilityEndpoint("not-a-valid-address", false); err == nil {
		t.Error("expected an error for an unparseable listen address")
	}
}

func TestReachabilityPort(t *testing.T) {
	port, err := reachabilityPort(":8765")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 8765 {
		t.Errorf("port = %d, want 8765", port)
	}
}

func TestReachabilityPort_WithHost(t *testing.T) {
	port, err := reachabilityPort("192.168.1.10:9000")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if port != 9000 {
		t.Errorf("port = %d, want 9000", port)
	}
}

func TestReachabilityPort_Invalid(t *testing.T) {
	if _, err := reachabilityPort("garbage"); err == nil {
		t.Error("expected an error for an unparseable listen address")
	}
}
