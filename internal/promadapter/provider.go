// Package promadapter implements the vinculum-bus o11y.MetricsProvider interface
// backed by a private prometheus.Registry.
package promadapter

import (
	"context"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tsarna/vinculum-bus/o11y"
)

// Provider implements o11y.MetricsProvider using a prometheus.Registry.
type Provider struct {
	reg *prometheus.Registry
}

// New creates a new Provider backed by the given registry.
func New(reg *prometheus.Registry) *Provider {
	return &Provider{reg: reg}
}

// Counter returns an o11y.Counter backed by a prometheus CounterVec.
// Label names are captured lazily from the first Add call.
func (p *Provider) Counter(name string) o11y.Counter {
	return &promCounter{reg: p.reg, name: name}
}

// Gauge returns an o11y.Gauge backed by a prometheus GaugeVec.
func (p *Provider) Gauge(name string) o11y.Gauge {
	return &promGauge{reg: p.reg, name: name}
}

// Histogram returns an o11y.Histogram backed by a prometheus HistogramVec.
func (p *Provider) Histogram(name string) o11y.Histogram {
	return &promHistogram{reg: p.reg, name: name}
}

// labelsToKeys extracts the label key names from a slice of o11y.Label.
func labelsToKeys(labels []o11y.Label) []string {
	keys := make([]string, len(labels))
	for i, l := range labels {
		keys[i] = l.Key
	}
	return keys
}

// labelsToValues extracts the label values in the same order as labelsToKeys.
func labelsToValues(labels []o11y.Label) []string {
	vals := make([]string, len(labels))
	for i, l := range labels {
		vals[i] = l.Value
	}
	return vals
}

// promCounter wraps a lazily-registered prometheus.CounterVec.
type promCounter struct {
	reg  *prometheus.Registry
	name string

	mu  sync.Mutex
	vec *prometheus.CounterVec
}

func (c *promCounter) getVec(labels []o11y.Label) *prometheus.CounterVec {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.vec == nil {
		c.vec = prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: c.name},
			labelsToKeys(labels),
		)
		// Ignore registration errors — the metric may already exist in the
		// registry if the bus is recreated (e.g. in tests). Use MustRegister
		// equivalent with recovery.
		_ = c.reg.Register(c.vec)
	}
	return c.vec
}

func (c *promCounter) Add(_ context.Context, value int64, labels ...o11y.Label) {
	c.getVec(labels).WithLabelValues(labelsToValues(labels)...).Add(float64(value))
}

// promGauge wraps a lazily-registered prometheus.GaugeVec.
type promGauge struct {
	reg  *prometheus.Registry
	name string

	mu  sync.Mutex
	vec *prometheus.GaugeVec
}

func (g *promGauge) getVec(labels []o11y.Label) *prometheus.GaugeVec {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.vec == nil {
		g.vec = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: g.name},
			labelsToKeys(labels),
		)
		_ = g.reg.Register(g.vec)
	}
	return g.vec
}

func (g *promGauge) Set(_ context.Context, value float64, labels ...o11y.Label) {
	g.getVec(labels).WithLabelValues(labelsToValues(labels)...).Set(value)
}

// promHistogram wraps a lazily-registered prometheus.HistogramVec.
type promHistogram struct {
	reg  *prometheus.Registry
	name string

	mu  sync.Mutex
	vec *prometheus.HistogramVec
}

func (h *promHistogram) getVec(labels []o11y.Label) *prometheus.HistogramVec {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.vec == nil {
		h.vec = prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    h.name,
				Buckets: prometheus.DefBuckets,
			},
			labelsToKeys(labels),
		)
		_ = h.reg.Register(h.vec)
	}
	return h.vec
}

func (h *promHistogram) Record(_ context.Context, value float64, labels ...o11y.Label) {
	h.getVec(labels).WithLabelValues(labelsToValues(labels)...).Observe(value)
}
