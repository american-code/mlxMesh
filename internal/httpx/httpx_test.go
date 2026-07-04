package httpx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fastRetry() RetryConfig {
	return RetryConfig{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}
}

func TestDo_SucceedsFirstTry(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastRetry(), func() error { calls++; return nil })
	if err != nil || calls != 1 {
		t.Fatalf("want 1 call no error, got calls=%d err=%v", calls, err)
	}
}

func TestDo_RetriesTransientThenSucceeds(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fastRetry(), func() error {
		calls++
		if calls < 3 {
			return Transient(errors.New("boom"))
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("want success on 3rd try, got calls=%d err=%v", calls, err)
	}
}

func TestDo_PermanentErrorNotRetried(t *testing.T) {
	calls := 0
	sentinel := errors.New("400 bad request")
	err := Do(context.Background(), fastRetry(), func() error { calls++; return sentinel })
	if calls != 1 {
		t.Fatalf("permanent error must not retry, got calls=%d", calls)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestDo_ExhaustsAndUnwraps(t *testing.T) {
	inner := errors.New("still down")
	calls := 0
	err := Do(context.Background(), fastRetry(), func() error { calls++; return Transient(inner) })
	if calls != 4 {
		t.Fatalf("want MaxAttempts=4 calls, got %d", calls)
	}
	if !errors.Is(err, inner) {
		t.Fatalf("exhausted error should unwrap to inner, got %v", err)
	}
}

func TestDo_ContextCancelStopsRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	err := Do(ctx, RetryConfig{MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Second},
		func() error { calls++; return Transient(errors.New("x")) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("cancel should stop before a 2nd attempt, got calls=%d", calls)
	}
}

func TestLimiter_BurstThenBlocks(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	l := NewLimiter(10, 3)
	l.now = func() time.Time { return base }
	l.lastFill = base // align the refill clock with the injected now

	// Burst of 3 tokens available immediately.
	for i := 0; i < 3; i++ {
		if !l.Allow() {
			t.Fatalf("token %d should be allowed within burst", i)
		}
	}
	// 4th is denied at the same instant.
	if l.Allow() {
		t.Fatal("4th call should be rate-limited")
	}
	// After 0.2s at 10/s, ~2 tokens refill.
	l.now = func() time.Time { return base.Add(200 * time.Millisecond) }
	firstRefilled, secondRefilled := l.Allow(), l.Allow()
	if !firstRefilled || !secondRefilled {
		t.Fatal("tokens should refill after elapsed time")
	}
	if l.Allow() {
		t.Fatal("only ~2 tokens should have refilled")
	}
}

func TestLimiter_DisabledAlwaysAllows(t *testing.T) {
	l := NewLimiter(0, 1)
	for i := 0; i < 100; i++ {
		if !l.Allow() {
			t.Fatal("perSec<=0 disables limiting")
		}
	}
	if err := l.Wait(context.Background()); err != nil {
		t.Fatalf("disabled Wait should return nil, got %v", err)
	}
}
