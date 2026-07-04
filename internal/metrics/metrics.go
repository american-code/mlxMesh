// Package metrics is a tiny, dependency-free metrics registry with a Prometheus
// text-exposition endpoint. Just enough to make a running coordinator
// observable — request/dispatch/credit counters and live gauges — without
// pulling in the full Prometheus client library.
package metrics

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
)

// Registry holds named counters and gauges. The zero value is not usable; call
// New. Safe for concurrent use.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*Counter
	gauges   map[string]*Gauge
}

func New() *Registry {
	return &Registry{
		counters: make(map[string]*Counter),
		gauges:   make(map[string]*Gauge),
	}
}

// Counter is a monotonically increasing value (resets only on restart).
type Counter struct{ v atomic.Int64 }

func (c *Counter) Inc()         { c.v.Add(1) }
func (c *Counter) Add(n int64)  { c.v.Add(n) }
func (c *Counter) Value() int64 { return c.v.Load() }

// Gauge is a value that can go up or down (queue depth, in-flight, nodes).
type Gauge struct{ v atomic.Int64 }

func (g *Gauge) Set(n int64)  { g.v.Store(n) }
func (g *Gauge) Inc()         { g.v.Add(1) }
func (g *Gauge) Dec()         { g.v.Add(-1) }
func (g *Gauge) Value() int64 { return g.v.Load() }

// Counter returns (creating if needed) the counter with this name. name should
// be a valid Prometheus metric name, optionally with inline labels already
// baked in, e.g. `rejections_total{reason="ssrf"}`.
func (r *Registry) Counter(name string) *Counter {
	r.mu.RLock()
	c := r.counters[name]
	r.mu.RUnlock()
	if c != nil {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c = r.counters[name]; c == nil {
		c = &Counter{}
		r.counters[name] = c
	}
	return c
}

func (r *Registry) Gauge(name string) *Gauge {
	r.mu.RLock()
	g := r.gauges[name]
	r.mu.RUnlock()
	if g != nil {
		return g
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if g = r.gauges[name]; g == nil {
		g = &Gauge{}
		r.gauges[name] = g
	}
	return g
}

// IncCounter and AddCounter are convenience wrappers for one-off use.
func (r *Registry) IncCounter(name string)          { r.Counter(name).Inc() }
func (r *Registry) AddCounter(name string, n int64) { r.Counter(name).Add(n) }
func (r *Registry) SetGauge(name string, n int64)   { r.Gauge(name).Set(n) }

// Expose renders the registry in Prometheus text-exposition format. Deterministic
// (names sorted) so scrapes and tests are stable.
func (r *Registry) Expose() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.counters)+len(r.gauges))
	vals := make(map[string]int64)
	for n, c := range r.counters {
		names = append(names, n)
		vals[n] = c.Value()
	}
	for n, g := range r.gauges {
		names = append(names, n)
		vals[n] = g.Value()
	}
	sort.Strings(names)

	var b []byte
	for _, n := range names {
		b = append(b, fmt.Sprintf("%s %d\n", n, vals[n])...)
	}
	return string(b)
}
