package config

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.uber.org/zap"
)

// The health state metrics, shaped by OpenTelemetry's convention for status
// metrics: a `<context>.status` instrument carrying a `<context>.state`
// attribute, one timeseries per possible state, valued 1 for the state the
// subject is currently in and 0 for the others.
//
// That shape is why these are UpDownCounters rather than gauges. With a 0/1
// value per state, a plain sum aggregates into "how many things are in this
// state" — which a gauge's last-value aggregation cannot do.
const (
	healthStatusMetric          = "vinculum.health.status"
	healthComponentStatusMetric = "vinculum.health.component.status"

	healthStateAttr         = "vinculum.health.state"
	healthProbeAttr         = "vinculum.health.probe"
	healthComponentAttr     = "vinculum.health.component"
	healthComponentTypeAttr = "vinculum.health.component.type"

	healthStatePassing = "passing"
	healthStateFailing = "failing"
)

// healthProbeNames maps a probe to the value its attribute carries. The
// attribute names the question being asked, so it reads as the noun —
// "readiness", not "ready" — leaving the state attribute to carry the answer.
var healthProbeNames = map[string]string{
	ProbeReady: "readiness",
	ProbeLive:  "liveness",
}

// registerHealthMetrics wires the health state metrics into every metrics
// backend the configuration declares, and is a no-op when it declares none.
//
// Every backend, not the default one — which is how the Go runtime metrics are
// already wired: each `server "metrics"` and each `client "otlp"` starts them
// on its own provider, without consulting default resolution. Process-level
// telemetry belongs in every pipeline the process has, because an "is it up"
// signal that exists in only one of them is a blind spot in the other.
//
// Default resolution would be actively wrong here. Its rule for several
// backends with none marked default is "explicit wiring required" — which
// assumes a block to put `metrics = server.<name>` on. Health has none, so the
// rule cannot be satisfied and the metrics would simply vanish, silently.
//
// The instruments are **observable**: their callback runs when a collector
// scrapes, so the exported value is true as of that moment. A synchronous
// instrument would report whatever the last prober happened to see, at an age
// nothing bounds — which for readiness computed only on demand could be
// arbitrarily stale. Making a scrape another asker also means a deployment that
// scrapes but never probes still gets accurate readiness, for free.
func (c *Config) registerHealthMetrics() hcl.Diagnostics {
	// Deduplicated by provider identity: a metrics server that pushes through
	// an otlp client shares its provider, and registering the same instruments
	// against it twice would double-report.
	seen := make(map[metric.MeterProvider]bool)

	backends := make([]InstrumentMetrics, 0, len(c.MetricsServers)+len(c.OtlpClients))
	for _, name := range sortedKeys(c.MetricsServers) {
		backends = append(backends, c.MetricsServers[name])
	}
	for _, name := range sortedKeys(c.OtlpClients) {
		backends = append(backends, c.OtlpClients[name])
	}

	for _, im := range backends {
		mp := im.GetMeterProvider()
		if mp == nil || seen[mp] {
			continue
		}
		seen[mp] = true

		if err := registerHealthInstruments(c, mp.Meter("github.com/tsarna/vinculum/health")); err != nil {
			return healthMetricDiag(err)
		}
	}
	return nil
}

// registerHealthInstruments creates the two instruments on meter and installs
// the callback that reports them. Separate from backend resolution so a test
// can point it at a manual reader.
func registerHealthInstruments(c *Config, meter metric.Meter) error {
	status, err := meter.Int64ObservableUpDownCounter(
		healthStatusMetric,
		metric.WithUnit("1"),
		metric.WithDescription("Whether the process is passing each health probe: 1 for the state it is in, 0 for the other."),
	)
	if err != nil {
		return err
	}

	componentStatus, err := meter.Int64ObservableUpDownCounter(
		healthComponentStatusMetric,
		metric.WithUnit("1"),
		metric.WithDescription("Whether each health contributor is passing: 1 for the state it is in, 0 for the other."),
	)
	if err != nil {
		return err
	}

	_, err = meter.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			c.observeHealth(ctx, o, status, componentStatus)
			return nil
		},
		status, componentStatus,
	)
	return err
}

// observeHealth reports both metrics for both probes.
//
// One refresh covers everything: Status shares the cached report, so the two
// probes and every component here describe a single evaluation rather than
// several taken moments apart.
func (c *Config) observeHealth(ctx context.Context, o metric.Observer, status, componentStatus metric.Int64ObservableUpDownCounter) {
	for _, probe := range []string{ProbeReady, ProbeLive} {
		probeAttr := attribute.String(healthProbeAttr, healthProbeNames[probe])
		statuses := c.Health.Status(ctx, probe, false)

		passing := true
		for _, s := range statuses {
			if !s.Ready {
				passing = false
			}
			attrs := []attribute.KeyValue{
				probeAttr,
				attribute.String(healthComponentAttr, s.Component),
			}
			// Omitted rather than sent empty, as error.type is elsewhere: a
			// `check` and the built-in `process` have no type label, and an
			// empty one would be a value that reads as if it meant something.
			if s.Type != "" {
				attrs = append(attrs, attribute.String(healthComponentTypeAttr, s.Type))
			}
			observeState(o, componentStatus, s.Ready, attrs...)
		}

		observeState(o, status, passing, probeAttr)
	}
}

// observeState emits the pair of timeseries one subject's state produces: 1 for
// the state it is in, 0 for the other. Reporting both is what lets a sum count
// subjects in a state, and what stops a series going stale rather than to zero
// when a subject leaves it.
func observeState(o metric.Observer, inst metric.Int64ObservableUpDownCounter, passing bool, attrs ...attribute.KeyValue) {
	passingVal, failingVal := int64(0), int64(1)
	if passing {
		passingVal, failingVal = 1, 0
	}
	o.ObserveInt64(inst, passingVal,
		metric.WithAttributes(append(attrs, attribute.String(healthStateAttr, healthStatePassing))...))
	o.ObserveInt64(inst, failingVal,
		metric.WithAttributes(append(attrs, attribute.String(healthStateAttr, healthStateFailing))...))
}

func healthMetricDiag(err error) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Failed to register health metrics",
		Detail:   err.Error(),
	}}
}

// logHealthTransition reports a change in a probe's verdict.
//
// On transition only, never per probe: a probe every ten seconds across three
// endpoints would otherwise fill the log with lines saying nothing changed. The
// failing entries are named, because "not ready" without them sends the reader
// to an endpoint they may not have.
//
// UserLogger, not Logger: the cause is nearly always the configuration or the
// environment it runs in — a broker that is down, a check the author wrote —
// rather than a bug in Vinculum, so a Go caller and stacktrace would be noise.
func (h *Health) logHealthTransition(probe string, passing bool, failing []ComponentStatus) {
	if h == nil || h.logger == nil {
		return
	}

	// The probe constants are the adjectives — "ready", "live" — so they read
	// straight into the sentence.
	if passing {
		h.logger.Info("Process is now " + probe)
		return
	}

	reasons := make([]string, 0, len(failing))
	for _, f := range failing {
		reasons = append(reasons, f.Component+": "+f.Reason)
	}
	h.logger.Warn("Process is no longer "+probe,
		zap.Strings("failing", reasons),
	)
}
