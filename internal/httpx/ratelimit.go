package httpx

import (
	"context"
	"sync"
	"time"
)

// Limiter is a simple token-bucket rate limiter for OUTBOUND external calls
// (task #24) — e.g. a node chasing encrypted-pointer fetch URLs. It bounds how
// fast we hit an external endpoint so a burst of jobs (or a hostile pointer set)
// can't turn this process into an amplifier. Safe for concurrent use.
type Limiter struct {
	mu       sync.Mutex
	tokens   float64
	max      float64
	perSec   float64
	lastFill time.Time
	now      func() time.Time // injectable for tests
}

// NewLimiter builds a bucket of `burst` tokens that refills at `perSec` tokens
// per second. A non-positive perSec disables limiting (Allow always true).
func NewLimiter(perSec float64, burst int) *Limiter {
	b := float64(burst)
	if b < 1 {
		b = 1
	}
	return &Limiter{
		tokens:   b,
		max:      b,
		perSec:   perSec,
		lastFill: time.Now(),
		now:      time.Now,
	}
}

// refill adds tokens for elapsed time. Caller holds mu.
func (l *Limiter) refill() {
	if l.perSec <= 0 {
		return
	}
	t := l.now()
	elapsed := t.Sub(l.lastFill).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tokens += elapsed * l.perSec
	if l.tokens > l.max {
		l.tokens = l.max
	}
	l.lastFill = t
}

// Allow consumes one token if available, returning true. Non-blocking.
func (l *Limiter) Allow() bool {
	if l.perSec <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.refill()
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}

// Wait blocks until a token is available or ctx is done. Returns ctx.Err() on
// cancellation. With limiting disabled it returns immediately.
func (l *Limiter) Wait(ctx context.Context) error {
	if l.perSec <= 0 {
		return nil
	}
	for {
		if l.Allow() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
