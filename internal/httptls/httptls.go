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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
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
