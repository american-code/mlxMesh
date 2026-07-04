package httptls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCertWithExpiry writes a self-signed cert (only, no key needed by
// WarnIfExpiringSoon) with a controllable NotAfter, for testing expiry logic
// that httptest's fixed built-in cert can't exercise.
func writeCertWithExpiry(t *testing.T, notAfter time.Time) string {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "expiry.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// captureStderr redirects os.Stderr for the duration of fn and returns what
// was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = orig
	buf := make([]byte, 4096)
	n, _ := r.Read(buf)
	return string(buf[:n])
}

func TestWarnIfExpiringSoon_WarnsWhenExpiringSoon(t *testing.T) {
	certPath := writeCertWithExpiry(t, time.Now().Add(10*24*time.Hour))
	out := captureStderr(t, func() { WarnIfExpiringSoon(certPath, 30*24*time.Hour, "test") })
	if out == "" {
		t.Error("expected a warning for a cert expiring within the window")
	}
}

func TestWarnIfExpiringSoon_WarnsWhenAlreadyExpired(t *testing.T) {
	certPath := writeCertWithExpiry(t, time.Now().Add(-24*time.Hour))
	out := captureStderr(t, func() { WarnIfExpiringSoon(certPath, 30*24*time.Hour, "test") })
	if out == "" {
		t.Error("expected a warning for an already-expired cert")
	}
}

func TestWarnIfExpiringSoon_SilentWhenHealthy(t *testing.T) {
	certPath := writeCertWithExpiry(t, time.Now().Add(365*24*time.Hour))
	out := captureStderr(t, func() { WarnIfExpiringSoon(certPath, 30*24*time.Hour, "test") })
	if out != "" {
		t.Errorf("expected no warning for a healthy cert, got: %q", out)
	}
}

func TestWarnIfExpiringSoon_BadFileDoesNotPanic(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "missing.crt")
	captureStderr(t, func() { WarnIfExpiringSoon(bad, 30*24*time.Hour, "test") }) // must not panic
}

// writeCACert extracts the server's own cert (httptest signs with an ad-hoc CA)
// into a PEM file usable as a trust anchor.
func writeCACert(t *testing.T, srv *httptest.Server) string {
	t.Helper()
	cert := srv.Certificate()
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
	path := filepath.Join(t.TempDir(), "ca.crt")
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigureClientTrustsProvidedCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	caPath := writeCACert(t, srv)

	// Default client must NOT trust the ad-hoc cert.
	def := &http.Client{}
	if resp, err := def.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Fatal("expected untrusted-cert error from default client")
	}

	// Configured with the CA, the same call must succeed.
	c := &http.Client{}
	if err := ConfigureClient(c, caPath, false); err != nil {
		t.Fatalf("ConfigureClient: %v", err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("CA-trusted request failed: %v", err)
	}
	resp.Body.Close()
}

func TestConfigureClientSkipVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	c := &http.Client{}
	if err := ConfigureClient(c, "", true); err != nil {
		t.Fatalf("ConfigureClient skip-verify: %v", err)
	}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("skip-verify request failed: %v", err)
	}
	resp.Body.Close()
}

func TestConfigureClientNoopWhenUnset(t *testing.T) {
	c := &http.Client{Transport: nil}
	if err := ConfigureClient(c, "", false); err != nil {
		t.Fatalf("no-op ConfigureClient errored: %v", err)
	}
	if c.Transport != nil {
		t.Error("ConfigureClient must not install a transport when neither CA nor skip-verify is set")
	}
}

func TestClientTLSConfigRejectsBadCA(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	os.WriteFile(bad, []byte("not a certificate"), 0o600)
	if _, err := ClientTLSConfig(bad, false); err == nil {
		t.Error("expected error parsing a non-PEM CA file")
	}
}

func TestClientTLSConfigMinVersion(t *testing.T) {
	cfg, err := ClientTLSConfig("", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("expected TLS 1.2 floor, got %x", cfg.MinVersion)
	}
}

// ensure a real cert pool round-trips (guards AppendCertsFromPEM usage).
func TestClientTLSConfigLoadsPool(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	caPath := writeCACert(t, srv)
	cfg, err := ClientTLSConfig(caPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil {
		t.Fatal("RootCAs not set")
	}
	if _, err := x509.SystemCertPool(); err != nil {
		t.Skip("no system pool on this platform")
	}
}

func fingerprintOf(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

// A pinned client trusts a self-signed node cert when the fingerprint
// matches — no shared CA involved, the whole point of TOFU pinning.
func TestPinnedClient_AcceptsMatchingFingerprint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := PinnedClient(fingerprintOf(srv.Certificate()), 5*time.Second)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("expected pinned client to trust the matching cert, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// A pinned client rejects a cert that doesn't match the pinned fingerprint —
// the MITM-resistance property this whole mechanism exists for.
func TestPinnedClient_RejectsMismatchedFingerprint(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wrongFingerprint := "0000000000000000000000000000000000000000000000000000000000000000"
	client := PinnedClient(wrongFingerprint, 5*time.Second)
	if resp, err := client.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Error("expected pinned client to reject a mismatched fingerprint")
	}
}

// An empty pin (no fingerprint recorded for this node) must always reject —
// a node that never advertised a cert shouldn't be dispatched to over "TLS"
// that verifies nothing.
func TestPinnedClient_EmptyPinAlwaysRejects(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := PinnedClient("", 5*time.Second)
	if resp, err := client.Get(srv.URL); err == nil {
		resp.Body.Close()
		t.Error("expected an empty pinned fingerprint to always reject")
	}
}

func TestCertFingerprint(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	certPath := writeCACert(t, srv)

	got, err := CertFingerprint(certPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := fingerprintOf(srv.Certificate()); got != want {
		t.Errorf("CertFingerprint = %q, want %q", got, want)
	}
}

func TestCertFingerprint_BadFileRejected(t *testing.T) {
	bad := filepath.Join(t.TempDir(), "bad.pem")
	os.WriteFile(bad, []byte("not a certificate"), 0o600)
	if _, err := CertFingerprint(bad); err == nil {
		t.Error("expected error parsing a non-PEM cert file")
	}
}
