// Package httpmw holds small, dependency-free HTTP middleware shared across the
// coordinator, directory, and node-agent servers.
package httpmw

import "net/http"

// SecurityHeaders sets a conservative set of response headers appropriate for
// JSON APIs. It deliberately does NOT set Strict-Transport-Security: these
// services run over plain http on a LAN / in local Docker, where HSTS is either
// ignored or actively harmful (it would pin a hostname to https it can't serve).
//
// The headers set:
//   - X-Content-Type-Options: nosniff — never let a browser MIME-sniff a JSON
//     body into something executable.
//   - X-Frame-Options: DENY + frame-ancestors 'none' — these are APIs; nothing
//     should ever embed them in a frame (clickjacking surface = zero).
//   - Referrer-Policy: no-referrer — API URLs can carry ids; don't leak them.
//   - Content-Security-Policy: default-src 'none' — an API returns data, not a
//     document, so it needs no resources of any kind; the strictest policy that
//     still lets JSON through.
//
// Applied at the outermost layer so it covers every response, including errors
// and CORS preflights.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
