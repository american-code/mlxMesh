package coordinator

import (
	"sync"
	"time"
)

// IPRateLimiter enforces a per-client-IP token-bucket rate limit. One bucket
// per IP, refilled continuously at ratePerSec and capped at burst. No
// endpoint on this coordinator had any rate limiting before this — a single
// client could hammer /v1/chat/completions, /users/{id}/startup-grant, or any
// other endpoint as fast as the network allowed.
type IPRateLimiter struct {
	mu         sync.Mutex
	buckets    map[string]*bucket
	ratePerSec float64
	burst      float64
	stop       chan struct{}
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

// NewIPRateLimiter starts a limiter allowing ratePerSec sustained requests per
// IP with bursts up to burst. ratePerSec <= 0 disables limiting entirely —
// Allow always returns true (used for tests and explicit --rate-limit-rps=0).
// A background goroutine evicts buckets idle for more than 10 minutes so
// memory doesn't grow unbounded under a distributed/spoofed-source flood.
func NewIPRateLimiter(ratePerSec, burst float64) *IPRateLimiter {
	l := &IPRateLimiter{
		buckets:    make(map[string]*bucket),
		ratePerSec: ratePerSec,
		burst:      burst,
		stop:       make(chan struct{}),
	}
	if ratePerSec > 0 {
		go l.runEviction()
	}
	return l
}

// Stop halts the background eviction goroutine. Safe to call multiple times.
func (l *IPRateLimiter) Stop() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
}

func (l *IPRateLimiter) runEviction() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.mu.Lock()
			for ip, b := range l.buckets {
				if time.Since(b.lastSeen) > 10*time.Minute {
					delete(l.buckets, ip)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Allow reports whether a request from ip should proceed, consuming one token
// if so. Always true when the limiter was constructed with ratePerSec <= 0.
func (l *IPRateLimiter) Allow(ip string) bool {
	if l.ratePerSec <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[ip] = b
	}
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens = min(l.burst, b.tokens+elapsed*l.ratePerSec)
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
