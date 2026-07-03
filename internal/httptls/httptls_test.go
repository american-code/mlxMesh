package httptls

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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
	if _, err := def.Get(srv.URL); err == nil {
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
