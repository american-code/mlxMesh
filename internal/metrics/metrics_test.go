package metrics

import (
	"strings"
	"sync"
	"testing"
)

func TestCounterAndGauge(t *testing.T) {
	r := New()
	r.Counter("requests_total").Inc()
	r.Counter("requests_total").Inc()
	r.AddCounter("tokens_total", 50)
	g := r.Gauge("in_flight")
	g.Inc()
	g.Inc()
	g.Dec()

	if got := r.Counter("requests_total").Value(); got != 2 {
		t.Errorf("requests_total = %d, want 2", got)
	}
	if got := r.Gauge("in_flight").Value(); got != 1 {
		t.Errorf("in_flight = %d, want 1", got)
	}

	out := r.Expose()
	for _, want := range []string{"requests_total 2", "tokens_total 50", "in_flight 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expose missing %q in:\n%s", want, out)
		}
	}
	// Deterministic ordering (sorted).
	if strings.Index(out, "in_flight") > strings.Index(out, "requests_total") {
		t.Error("expose output not sorted")
	}
}

func TestConcurrentInc(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r.Counter("c").Inc() }()
	}
	wg.Wait()
	if got := r.Counter("c").Value(); got != 100 {
		t.Errorf("concurrent inc = %d, want 100", got)
	}
}
