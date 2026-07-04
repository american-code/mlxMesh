package httpmw

import (
	"net/http"
)

// DefaultMaxBodyBytes caps request bodies to bound memory per request. Inference
// prompts are text; 8 MiB is generous for that while stopping a single client
// from forcing the coordinator to buffer arbitrarily large payloads.
const DefaultMaxBodyBytes int64 = 8 << 20 // 8 MiB

// MaxBodyBytes wraps every request body in http.MaxBytesReader so oversized
// uploads are rejected as they stream in, instead of being fully buffered first.
// A body over the limit surfaces as a decode error in the handler (400), not an
// OOM. GET/HEAD (no body) pass through untouched.
func MaxBodyBytes(limit int64, next http.Handler) http.Handler {
	if limit <= 0 {
		limit = DefaultMaxBodyBytes
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, limit)
		}
		next.ServeHTTP(w, r)
	})
}

// LimitConcurrency caps the number of requests in flight across the whole server
// with a buffered-channel semaphore. Beyond per-IP rate limiting (which a
// distributed flood defeats), this bounds total resource use: excess requests
// get 503 + Retry-After immediately rather than piling up and exhausting memory
// or file descriptors. max <= 0 disables the limit.
func LimitConcurrency(max int, next http.Handler) http.Handler {
	if max <= 0 {
		return next
	}
	sem := make(chan struct{}, max)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "server at capacity, retry shortly", http.StatusServiceUnavailable)
		}
	})
}
