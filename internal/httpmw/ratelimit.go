package httpmw

import (
	"sync"
	"time"
)

// RateLimiter enforces a token-bucket rate limit keyed on an arbitrary string
// (a client IP for per-IP limiting, or a user_id for per-account quotas). One
// bucket per key, refilled continuously at ratePerSec and capped at burst.
// Shared by the coordinator (per-IP + per-user) and the directory (per-IP).
type RateLimiter struct {
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

// NewRateLimiter starts a limiter allowing ratePerSec sustained requests per
// key with bursts up to burst. ratePerSec <= 0 disables limiting entirely —
// Allow always returns true (used for tests and explicit --rate-limit-rps=0).
// A background goroutine evicts buckets idle for more than 10 minutes so
// memory doesn't grow unbounded under a distributed/spoofed-source flood.
func NewRateLimiter(ratePerSec, burst float64) *RateLimiter {
	l := &RateLimiter{
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
func (l *RateLimiter) Stop() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
}

func (l *RateLimiter) runEviction() {
	t := time.NewTicker(2 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-t.C:
			l.mu.Lock()
			for key, b := range l.buckets {
				if time.Since(b.lastSeen) > 10*time.Minute {
					delete(l.buckets, key)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Allow reports whether a request under key should proceed, consuming one token
// if so. Always true when the limiter was constructed with ratePerSec <= 0.
func (l *RateLimiter) Allow(key string) bool {
	if l.ratePerSec <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastSeen: now}
		l.buckets[key] = b
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
