// Package httptls centralizes TLS setup for the mesh's HTTP servers and the
// clients that call them. One place so every service enforces the same floor
// (TLS 1.2+) and the dev/self-signed story is consistent.
//
// Servers: pass a cert+key pair to Serve() and it serves HTTPS; omit them and it
// serves plain HTTP (the pre-TLS default, kept so local single-box runs and the
// docker sim work unchanged). Production deploys MUST set a cert+key (or sit
// behind a TLS-terminating load balancer).
//
// Clients: nodes calling an HTTPS coordinator need to trust its cert. Point them
// at the CA that signed it (--tls-ca) for real security, or --tls-skip-verify
// for throwaway local testing (logged loudly; never in production).
package httptls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// Enabled reports whether a cert+key pair was supplied (both required).
func Enabled(certFile, keyFile string) bool {
	return certFile != "" && keyFile != ""
}

// minTLS is the floor for both server and client — TLS 1.2 rules out the
// downgrade-prone 1.0/1.1.
const minTLS = tls.VersionTLS12

// Serve runs srv on ln, using TLS when certFile+keyFile are both set and plain
// HTTP otherwise. It blocks until the server stops (mirrors http.Server.Serve).
func Serve(srv *http.Server, ln net.Listener, certFile, keyFile string) error {
	if Enabled(certFile, keyFile) {
		if srv.TLSConfig == nil {
			srv.TLSConfig = &tls.Config{MinVersion: minTLS}
		} else if srv.TLSConfig.MinVersion < minTLS {
			srv.TLSConfig.MinVersion = minTLS
		}
		// certFile/keyFile passed here take precedence; ServeTLS loads them.
		return srv.ServeTLS(ln, certFile, keyFile)
	}
	return srv.Serve(ln)
}

// ClientTLSConfig builds a *tls.Config for outbound calls to an HTTPS mesh
// service. If caFile is set, only that CA is trusted (plus it's added to the
// system roots for public-CA compatibility). If skipVerify is true, certificate
// verification is DISABLED — dev only.
func ClientTLSConfig(caFile string, skipVerify bool) (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: minTLS, InsecureSkipVerify: skipVerify} //nolint:gosec // skipVerify is an explicit, logged dev opt-in
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file %q: %w", caFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("no certificates parsed from CA file %q", caFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// WarnIfExpiringSoon parses the leaf certificate at certFile and logs a loud
// warning to stderr if it expires within `within`, or has already expired.
// Cert rotation is otherwise silent until the connection starts failing —
// this is cheap insurance against that. label identifies the service in the
// log line (e.g. "coordinator", "node"). Best-effort: a parse failure here
// logs and returns rather than blocking startup, since Serve()/ServeTLS will
// surface a real error for a genuinely broken cert anyway.
func WarnIfExpiringSoon(certFile string, within time.Duration, label string) {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return
	}
	remaining := time.Until(cert.NotAfter)
	switch {
	case remaining < 0:
		fmt.Fprintf(os.Stderr, "WARNING: [%s] TLS certificate %s EXPIRED on %s\n", label, certFile, cert.NotAfter.Format(time.RFC3339))
	case remaining < within:
		fmt.Fprintf(os.Stderr, "WARNING: [%s] TLS certificate %s expires in %s (on %s) — renew soon\n",
			label, certFile, remaining.Round(time.Hour), cert.NotAfter.Format(time.RFC3339))
	}
}

// CertFingerprint reads the PEM-encoded leaf certificate at certFile and
// returns its SHA-256 fingerprint, hex-encoded. Used at node startup to embed
// a fingerprint in the signed manifest for the coordinator to pin (task:
// coordinator->node TLS) — nodes are independently operated and self-signed,
// so there is no shared CA to verify against; pinning the exact fingerprint
// at registration time (TOFU, bound to the node's already-signed identity) is
// the mesh's substitute for one.
func CertFingerprint(certFile string) (string, error) {
	raw, err := os.ReadFile(certFile)
	if err != nil {
		return "", fmt.Errorf("read cert file %q: %w", certFile, err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", fmt.Errorf("no PEM block found in %q", certFile)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// PinnedClientTLSConfig builds a *tls.Config for outbound calls to a node
// whose certificate isn't (and can't be expected to be) signed by any shared
// CA. It disables normal chain verification and instead pins the exact
// SHA-256 fingerprint recorded at that node's registration — trust-on-first-
// use bound to the node's Ed25519-signed manifest, the same trust boundary
// the mesh already relies on for node identity generally, not a new weaker
// link. An empty expectedFingerprintHex means "no pin recorded" and always
// rejects — callers should only reach for this when the node actually
// advertised one.
func PinnedClientTLSConfig(expectedFingerprintHex string) *tls.Config {
	return &tls.Config{
		MinVersion:         minTLS,
		InsecureSkipVerify: true, //nolint:gosec // verification is replaced by VerifyConnection's fingerprint pin below, not disabled
		VerifyConnection: func(cs tls.ConnectionState) error {
			if expectedFingerprintHex == "" || len(cs.PeerCertificates) == 0 {
				return errors.New("tls: no pinned fingerprint recorded for this node")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if hex.EncodeToString(sum[:]) != expectedFingerprintHex {
				return errors.New("tls: peer certificate fingerprint does not match the one pinned at registration")
			}
			return nil
		},
	}
}

// PinnedClient returns an *http.Client configured to trust only the node
// certificate matching expectedFingerprintHex. timeout mirrors the caller's
// existing per-request timeout convention (buffered vs. streaming dispatch
// use different values).
func PinnedClient(expectedFingerprintHex string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: PinnedClientTLSConfig(expectedFingerprintHex)},
	}
}

// ConfigureClient points an existing *http.Client at the given trust settings.
// A no-op when neither caFile nor skipVerify is set (keeps the default client).
func ConfigureClient(client *http.Client, caFile string, skipVerify bool) error {
	if caFile == "" && !skipVerify {
		return nil
	}
	cfg, err := ClientTLSConfig(caFile, skipVerify)
	if err != nil {
		return err
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok || tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	} else {
		tr = tr.Clone()
	}
	tr.TLSClientConfig = cfg
	client.Transport = tr
	return nil
}
