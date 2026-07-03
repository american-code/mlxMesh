package tests

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/open-inference-mesh/oim/internal/httpmw"
)

// TestSecurityHeadersSetOnEveryResponse confirms the middleware stamps the
// hardening headers regardless of what the wrapped handler does, and that it
// does NOT set HSTS (these services run over plain http on a LAN).
func TestSecurityHeadersSetOnEveryResponse(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := httptest.NewServer(httpmw.SecurityHeaders(inner))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	want := map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	}
	for header, expected := range want {
		if got := resp.Header.Get(header); got != expected {
			t.Errorf("%s = %q, want %q", header, got, expected)
		}
	}
	if hsts := resp.Header.Get("Strict-Transport-Security"); hsts != "" {
		t.Errorf("HSTS should not be set on a plain-http LAN service, got %q", hsts)
	}
	// Middleware must not swallow the wrapped handler's status.
	if resp.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d (wrapped handler's status preserved)", resp.StatusCode, http.StatusTeapot)
	}
}
