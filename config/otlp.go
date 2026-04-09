package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// OtlpClient is implemented by client "otlp" blocks.
// It provides both tracing and push metrics via the o11y abstractions,
// so it can satisfy metrics = and tracing = attributes on servers.
type OtlpClient interface {
	Client
	// GetTracerProvider returns the OTel SDK TracerProvider for span extraction
	// (e.g. to call tracer.Start() or trace.SpanFromContext()).
	GetTracerProvider() trace.TracerProvider
	// IsDefaultClient returns true if this client should be used when no
	// explicit tracing = attribute is specified.
	IsDefaultClient() bool
	// GetMeterProvider returns the OTel SDK MeterProvider for creating
	// metric instruments (counters, histograms, gauges).
	GetMeterProvider() metric.MeterProvider
	// IsDefaultMetricsBackend returns true if this client should be used
	// as the default metrics backend when no explicit metrics = is specified.
	IsDefaultMetricsBackend() bool
}

// GetOtlpClientFromExpression evaluates an HCL expression expecting a client
// capsule and returns it as an OtlpClient. Returns an error if the client does
// not implement OtlpClient.
func GetOtlpClientFromExpression(config *Config, expr hcl.Expression) (OtlpClient, hcl.Diagnostics) {
	client, diags := GetClientFromExpression(config, expr)
	if diags.HasErrors() {
		return nil, diags
	}

	oc, ok := client.(OtlpClient)
	if !ok {
		exprRange := expr.Range()
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Client is not an OTLP client",
				Detail:   fmt.Sprintf("Expected a client \"otlp\" block, got client %q of a different type", client.GetName()),
				Subject:  &exprRange,
			},
		}
	}

	return oc, nil
}

// GetDefaultOtlpClient returns the default OtlpClient using these rules:
//  1. Exactly one OTLP client → use it
//  2. Multiple, exactly one with IsDefaultClient()=true → use it
//  3. Multiple with IsDefaultClient()=true → config error
//  4. Multiple, none default → return nil (explicit wiring required)
//  5. Zero → return nil
func (c *Config) GetDefaultOtlpClient() (OtlpClient, hcl.Diagnostics) {
	if len(c.OtlpClients) == 0 {
		return nil, nil
	}

	if len(c.OtlpClients) == 1 {
		for _, oc := range c.OtlpClients {
			return oc, nil
		}
	}

	var defaults []OtlpClient
	for _, oc := range c.OtlpClients {
		if oc.IsDefaultClient() {
			defaults = append(defaults, oc)
		}
	}

	if len(defaults) == 1 {
		return defaults[0], nil
	}

	if len(defaults) > 1 {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Multiple default OTLP clients",
				Detail:   "More than one client \"otlp\" block has default = true. At most one can be the default.",
			},
		}
	}

	// Multiple clients, none marked default
	return nil, nil
}

// ResolveOtlpClient returns the OtlpClient selected by expr (explicit wiring)
// or the default OTLP client (auto-wire). Returns nil with no error when no
// OTLP client is configured and no explicit tracing = is set.
func (c *Config) ResolveOtlpClient(expr hcl.Expression) (OtlpClient, hcl.Diagnostics) {
	if IsExpressionProvided(expr) {
		return GetOtlpClientFromExpression(c, expr)
	}
	return c.GetDefaultOtlpClient()
}

// ResolveTracerProvider returns the TracerProvider selected by expr (explicit
// wiring) or the default OTLP client (auto-wire). Returns nil with no error
// when no OTLP client is configured and no explicit tracing = is set.
func (c *Config) ResolveTracerProvider(expr hcl.Expression) (trace.TracerProvider, hcl.Diagnostics) {
	oc, diags := c.ResolveOtlpClient(expr)
	if diags.HasErrors() || oc == nil {
		return nil, diags
	}
	return oc.GetTracerProvider(), nil
}
