package httpmw

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// ValidateFetchURL guards the coordinator against SSRF via the client-supplied
// encrypted-payload fetch URL. The coordinator itself never fetches it, but it
// hands the URL to a node, which does — so an unvalidated URL lets an attacker
// make nodes hit internal-only targets (cloud metadata, loopback admin ports).
//
// Policy: allow only http/https to a routable host, and reject the classic SSRF
// targets — loopback, link-local (incl. the 169.254.169.254 cloud metadata
// endpoint), and the unspecified address. General private LAN ranges
// (10/8, 192.168/16, 172.16/12) are ALLOWED, because a legitimate iOS pointer
// host on the same LAN advertises one of those; blocking them would break the
// normal on-LAN pointer path. A literal-IP host is checked directly; a hostname
// is resolved and every returned IP must pass (defends against DNS rebinding to
// a blocked range at fetch time — best effort, since the node re-resolves).
func ValidateFetchURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("payload fetch URL: parse: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("payload fetch URL: scheme %q not allowed (http/https only)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("payload fetch URL: missing host")
	}

	// Literal IP: check directly. Hostname: resolve and check every IP.
	if ip := net.ParseIP(host); ip != nil {
		return checkIP(ip)
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("payload fetch URL: resolve %q: %w", host, err)
	}
	if len(ips) == 0 {
		return fmt.Errorf("payload fetch URL: %q resolved to no addresses", host)
	}
	for _, ip := range ips {
		if err := checkIP(ip); err != nil {
			return err
		}
	}
	return nil
}

// SafeFetchClient returns an HTTP client for fetching a client-supplied payload
// URL that keeps the SSRF guard attached through two bypass vectors the
// front-door ValidateFetchURL check can't cover on its own:
//
//   - Redirects: CheckRedirect re-validates every hop, so a host that passes
//     the initial check with a public IP can't 302 the follower to a
//     loopback/link-local target (cloud metadata, an internal admin port).
//   - DNS rebinding: the dialer's Control hook validates the ACTUAL IP being
//     connected to, after resolution, right before connect. ValidateFetchURL
//     resolves the host once, but the transport re-resolves at dial time — an
//     attacker controlling DNS could return a public IP for the check and a
//     blocked IP for the dial. Validating at connect time closes that gap.
//
// Redirect count is bounded by net/http's default (10 hops).
func SafeFetchClient(timeout time.Duration) *http.Client {
	return safeFetchClient(timeout, checkIP)
}

// safeFetchClient is SafeFetchClient with an injectable connect-time IP check,
// so tests can permit loopback httptest servers (which the real checkIP blocks)
// while still exercising the redirect-revalidation path.
func safeFetchClient(timeout time.Duration, ipCheck func(net.IP) error) *http.Client {
	dialer := &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return fmt.Errorf("dial address parse: %w", err)
			}
			ip := net.ParseIP(host)
			if ip == nil {
				return fmt.Errorf("dial address %q is not a literal IP", host)
			}
			return ipCheck(ip)
		},
	}
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          10,
			IdleConnTimeout:       30 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: time.Second,
		},
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			if err := ValidateFetchURL(req.URL.String()); err != nil {
				return fmt.Errorf("redirect blocked: %w", err)
			}
			return nil
		},
	}
}

// checkIP rejects loopback, link-local (v4 169.254/16 incl. metadata, v6
// fe80::/10), and unspecified addresses. Everything else (public + private LAN)
// is allowed.
func checkIP(ip net.IP) error {
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("payload fetch URL: loopback address %s not allowed", ip)
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return fmt.Errorf("payload fetch URL: link-local address %s not allowed (blocks cloud metadata / SSRF)", ip)
	case ip.IsUnspecified():
		return fmt.Errorf("payload fetch URL: unspecified address not allowed")
	}
	return nil
}
