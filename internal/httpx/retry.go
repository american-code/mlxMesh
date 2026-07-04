// Package httpx holds small, dependency-free HTTP client resilience helpers:
// retry-with-backoff for transient failures (task #22) and an outbound token-
// bucket rate limiter for external API calls (task #24).
package httpx

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"
)

// RetryConfig controls Do. The zero value is not useful; use DefaultRetry.
type RetryConfig struct {
	MaxAttempts int           // total tries, including the first (>=1)
	BaseDelay   time.Duration // backoff before the 2nd attempt
	MaxDelay    time.Duration // upper bound on any single backoff
}

// DefaultRetry is a sensible policy for idempotent coordinator calls
// (register / refresh / reputation): 4 tries over a few seconds.
func DefaultRetry() RetryConfig {
	return RetryConfig{MaxAttempts: 4, BaseDelay: 200 * time.Millisecond, MaxDelay: 5 * time.Second}
}

// transient marks an error returned by a Do callback as worth retrying. Errors
// that are NOT transient (e.g. a 4xx response, a marshal failure) short-circuit
// immediately so we never hammer the server on a permanent fault.
type transient struct{ err error }

func (t transient) Error() string { return t.err.Error() }
func (t transient) Unwrap() error { return t.err }

// Transient wraps err so Do will retry it. Callers classify their own failures:
// wrap network errors and 5xx/429 responses; leave 4xx and programming errors
// bare.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return transient{err}
}

// jitter guards math/rand for full-jitter backoff. Package-level so every caller
// shares one source; seeded once at init.
var (
	jmu  sync.Mutex
	jrng = rand.New(rand.NewSource(time.Now().UnixNano()))
)

// backoffFor returns the (jittered) delay before the given attempt number
// (attempt 1 is the first retry). Exponential, capped at MaxDelay, with full
// jitter to avoid thundering-herd reconnects.
func backoffFor(cfg RetryConfig, attempt int) time.Duration {
	d := cfg.BaseDelay
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= cfg.MaxDelay {
			d = cfg.MaxDelay
			break
		}
	}
	if d <= 0 {
		return 0
	}
	jmu.Lock()
	j := time.Duration(jrng.Int63n(int64(d)))
	jmu.Unlock()
	return j
}

// Do runs fn up to cfg.MaxAttempts times. It retries only when fn returns an
// error wrapped with Transient, sleeping with exponential backoff + jitter
// between attempts, and returns the last error (unwrapped) on exhaustion. It
// stops early — returning ctx.Err() — if the context is canceled while waiting.
func Do(ctx context.Context, cfg RetryConfig, fn func() error) error {
	if cfg.MaxAttempts < 1 {
		cfg.MaxAttempts = 1
	}
	var err error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		err = fn()
		if err == nil {
			return nil
		}
		var t transient
		if !errors.As(err, &t) {
			return err // permanent — do not retry
		}
		if attempt == cfg.MaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoffFor(cfg, attempt)):
		}
	}
	// Unwrap the transient marker so callers see their original error.
	var t transient
	if errors.As(err, &t) {
		return t.err
	}
	return err
}
