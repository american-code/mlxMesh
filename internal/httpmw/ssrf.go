package httpmw

import (
	"fmt"
	"net"
	"net/url"
	"strings"
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
