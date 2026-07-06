package httpmw

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientIP_NoTrustedProxies_UsesPeer(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5555"
	r.Header.Set("X-Forwarded-For", "10.0.0.1") // must be ignored — no trusted proxies
	if got := ClientIP(r, nil); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want the direct peer 203.0.113.9 (XFF ignored)", got)
	}
}

func TestClientIP_UntrustedPeer_IgnoresXFF(t *testing.T) {
	nets, err := ParseTrustedProxies([]string{"192.168.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.9:5555" // NOT a trusted proxy
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	// A spoofed XFF from an untrusted peer must not be honored.
	if got := ClientIP(r, nets); got != "203.0.113.9" {
		t.Errorf("ClientIP = %q, want 203.0.113.9 (untrusted peer's XFF must be ignored)", got)
	}
}

func TestClientIP_TrustedProxy_UsesXFFClient(t *testing.T) {
	nets, err := ParseTrustedProxies([]string{"192.168.1.10"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.10:443" // the trusted nginx
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := ClientIP(r, nets); got != "198.51.100.7" {
		t.Errorf("ClientIP = %q, want the real client 198.51.100.7 from XFF", got)
	}
}

func TestClientIP_TrustedChain_SkipsProxyHops(t *testing.T) {
	// Two chained trusted proxies: XFF = "<client>, <proxy2>"; the direct peer
	// is proxy1. Walking right-to-left must skip trusted hops and land on the
	// real client.
	nets, err := ParseTrustedProxies([]string{"192.168.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "192.168.1.10:443"
	r.Header.Set("X-Forwarded-For", "198.51.100.7, 192.168.1.20")
	if got := ClientIP(r, nets); got != "198.51.100.7" {
		t.Errorf("ClientIP = %q, want 198.51.100.7 (skip trusted proxy hop in XFF)", got)
	}
}

func TestParseTrustedProxies_RejectsGarbage(t *testing.T) {
	if _, err := ParseTrustedProxies([]string{"not-an-ip"}); err == nil {
		t.Error("expected an error for an invalid trusted-proxy spec")
	}
}
