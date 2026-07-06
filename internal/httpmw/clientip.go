package httpmw

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// ParseTrustedProxies turns a list of IP or CIDR strings into networks for
// ClientIP. A bare IP becomes a /32 (or /128). Invalid entries are an error so
// a typo in an operator flag fails loudly instead of silently trusting nothing.
func ParseTrustedProxies(specs []string) ([]*net.IPNet, error) {
	var out []*net.IPNet
	for _, s := range specs {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if !strings.Contains(s, "/") {
			if ip := net.ParseIP(s); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				s = fmt.Sprintf("%s/%d", s, bits)
			}
		}
		_, ipnet, err := net.ParseCIDR(s)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", s, err)
		}
		out = append(out, ipnet)
	}
	return out, nil
}

func ipInAny(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the connecting client's IP for rate-limit keying. When the
// direct peer (r.RemoteAddr) is within one of trustedProxies, the rightmost
// X-Forwarded-For entry that is NOT itself a trusted proxy is used — so a
// reverse proxy (e.g. nginx) in front of the server doesn't collapse every
// external client into a single bucket. When the peer is NOT trusted, XFF is
// ignored entirely: it's client-set and spoofable, so honoring it from an
// untrusted source would let anyone forge a fresh source IP per request and
// defeat the limiter. With no trusted proxies configured, this is always just
// the direct peer address.
func ClientIP(r *http.Request, trustedProxies []*net.IPNet) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if len(trustedProxies) == 0 {
		return host
	}
	peer := net.ParseIP(host)
	if peer == nil || !ipInAny(peer, trustedProxies) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ipStr := strings.TrimSpace(parts[i])
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}
		if !ipInAny(ip, trustedProxies) {
			return ipStr
		}
	}
	return host
}

// RateLimitByIP enforces a per-client-IP token-bucket limit, skipping CORS
// preflight. Returns 429 + Retry-After when exceeded. Client IP is resolved via
// ClientIP (honoring trustedProxies). Used by the directory; the coordinator
// has its own variant that additionally exempts its SSE stream.
func RateLimitByIP(limiter *RateLimiter, trustedProxies []*net.IPNet, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		if !limiter.Allow(ClientIP(r, trustedProxies)) {
			w.Header().Set("Retry-After", "1")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			json.NewEncoder(w).Encode(map[string]string{"error": "rate limit exceeded, retry shortly"})
			return
		}
		next.ServeHTTP(w, r)
	})
}
