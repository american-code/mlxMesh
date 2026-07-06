package httpmw

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestValidateFetchURL(t *testing.T) {
	blocked := []string{
		"http://169.254.169.254/latest/meta-data/", // cloud metadata (SSRF classic)
		"http://127.0.0.1:9000/admin",              // loopback → coordinator/node internals
		"http://localhost/secret",                  // loopback by name
		"https://[::1]/",                           // IPv6 loopback
		"http://0.0.0.0/",                          // unspecified
		"ftp://example.com/x",                      // non-http scheme
		"file:///etc/passwd",                       // file scheme
		"http://169.254.10.5/",                     // link-local
	}
	for _, u := range blocked {
		if err := ValidateFetchURL(u); err == nil {
			t.Errorf("expected %q to be blocked, but it was allowed", u)
		}
	}

	// Allowed: public hosts and general private LAN (legit iOS pointer hosts).
	// Literal IPs so the test needs no DNS.
	allowed := []string{
		"https://1.1.1.1/payload/abc",   // public
		"http://192.168.1.135:8080/p/1", // LAN pointer host
		"http://10.0.0.9/p/2",           // LAN pointer host
		"http://172.16.4.4/p/3",         // LAN pointer host
	}
	for _, u := range allowed {
		if err := ValidateFetchURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got: %v", u, err)
		}
	}
}

func TestSafeFetchClient_RevalidatesRedirect(t *testing.T) {
	// A pointer host that passes the initial check (loopback httptest server is
	// reached directly, bypassing ValidateFetchURL which only guards the URL a
	// client hands us) but then redirects to the cloud-metadata endpoint. The
	// CheckRedirect hook must block the follow.
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/iam/", http.StatusFound)
	}))
	defer redirector.Close()

	// Permit the loopback httptest listener at dial time (the real checkIP
	// blocks loopback — see TestSafeFetchClient_BlocksDialToBlockedIP) so the
	// test can reach the redirector and exercise the CheckRedirect path.
	allowAll := func(net.IP) error { return nil }
	resp, err := safeFetchClient(5*time.Second, allowAll).Get(redirector.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected redirect to cloud-metadata to be blocked, but the fetch succeeded")
	}
	if !strings.Contains(err.Error(), "redirect blocked") {
		t.Errorf("expected a 'redirect blocked' error, got: %v", err)
	}
}

func TestSafeFetchClient_AllowsNormalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("payload-bytes"))
	}))
	defer srv.Close()

	allowAll := func(net.IP) error { return nil }
	resp, err := safeFetchClient(5*time.Second, allowAll).Get(srv.URL)
	if err != nil {
		t.Fatalf("expected a normal (non-redirecting) fetch to succeed, got: %v", err)
	}
	resp.Body.Close()
}

func TestSafeFetchClient_BlocksDialToBlockedIP(t *testing.T) {
	// The dialer Control hook must reject a connection whose resolved IP is a
	// blocked target even when the URL itself was never passed through
	// ValidateFetchURL — this is the DNS-rebinding backstop (validate resolves
	// once, the transport re-resolves at dial time). A literal loopback URL
	// exercises the same connect-time path a rebinding host would reach.
	resp, err := SafeFetchClient(2 * time.Second).Get("http://127.0.0.1:9/blocked")
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected dial to a loopback IP to be blocked at connect time")
	}
	if !strings.Contains(err.Error(), "loopback") {
		t.Errorf("expected a loopback-blocked dial error, got: %v", err)
	}
}
