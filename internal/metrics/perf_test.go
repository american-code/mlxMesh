package metrics

import "testing"

// Performance regression guard (task #28). Counter/gauge mutation is on the hot
// path — every coordinator request touches it — so it must stay allocation-free.
// A regression that starts allocating here (e.g. re-looking-up the counter by
// name on every hit) would show up as a CI failure, not a silent slowdown.
func TestPerf_CounterIncIsAllocationFree(t *testing.T) {
	r := New()
	c := r.Counter("hot_path_total")
	if allocs := testing.AllocsPerRun(1000, func() { c.Inc() }); allocs != 0 {
		t.Errorf("Counter.Inc allocates %.1f/op, want 0", allocs)
	}
	g := r.Gauge("in_flight")
	if allocs := testing.AllocsPerRun(1000, func() { g.Inc(); g.Dec() }); allocs != 0 {
		t.Errorf("Gauge inc/dec allocates %.1f/op, want 0", allocs)
	}
}

func BenchmarkCounterInc(b *testing.B) {
	c := New().Counter("bench_total")
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Inc()
	}
}

// Named-lookup path (registry.Counter) is used when a label value is computed
// per call, e.g. rejections_total{reason=...}. Bench it so the map-lookup cost
// stays visible.
func BenchmarkCounterByName(b *testing.B) {
	r := New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Counter("bench_total").Inc()
	}
}
