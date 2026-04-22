package config

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/metric"
)

// --- InstrumentMetrics interface ---

// InstrumentMetrics is satisfied by both server "metrics" and client "otlp".
// Used to resolve the metrics MeterProvider for metric blocks and server
// auto-instrumentation.
type InstrumentMetrics interface {
	GetMeterProvider() metric.MeterProvider
	IsDefaultMetricsBackend() bool
}

// --- MetricsRegistrar interface ---

// MetricsRegistrar is implemented by server types that provide a Prometheus
// registry for metric blocks to register against.
type MetricsRegistrar interface {
	Listener
	InstrumentMetrics
	GetRegistry() *prometheus.Registry
	IsDefaultServer() bool
}

// GetMetricsRegistrarFromExpression evaluates an HCL expression expecting a
// server capsule and returns it as a MetricsRegistrar. Returns an error if the
// server does not implement MetricsRegistrar.
func GetMetricsRegistrarFromExpression(config *Config, expr hcl.Expression) (MetricsRegistrar, hcl.Diagnostics) {
	server, diags := GetServerFromExpression(config, expr)
	if diags.HasErrors() {
		return nil, diags
	}

	ms, ok := server.(MetricsRegistrar)
	if !ok {
		exprRange := expr.Range()
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Server is not a metrics server",
				Detail:   fmt.Sprintf("Expected a server \"metrics\" block, got server %q of a different type", server.GetName()),
				Subject:  &exprRange,
			},
		}
	}

	return ms, nil
}

// GetDefaultMetricsRegistrar returns the default MetricsRegistrar per these rules:
//  1. Exactly one metrics server → use it
//  2. Multiple, exactly one with IsDefaultServer()=true → use it
//  3. Multiple with IsDefaultServer()=true → config error
//  4. Multiple, none default → return nil (explicit wiring required)
//  5. Zero → return nil
func (c *Config) GetDefaultMetricsRegistrar() (MetricsRegistrar, hcl.Diagnostics) {
	if len(c.MetricsServers) == 0 {
		return nil, nil
	}

	if len(c.MetricsServers) == 1 {
		for _, ms := range c.MetricsServers {
			return ms, nil
		}
	}

	var defaults []MetricsRegistrar
	for _, ms := range c.MetricsServers {
		if ms.IsDefaultServer() {
			defaults = append(defaults, ms)
		}
	}

	if len(defaults) == 1 {
		return defaults[0], nil
	}

	if len(defaults) > 1 {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Multiple default metrics servers",
				Detail:   "More than one server \"metrics\" block has default = true. At most one can be the default.",
			},
		}
	}

	// Multiple servers, none marked default
	return nil, nil
}

// --- InstrumentMetrics resolution ---

// GetDefaultInstrumentMetrics returns the default InstrumentMetrics backend
// by searching both MetricsServers and OtlpClients. Selection rules:
//  1. Exactly one candidate total → use it
//  2. Multiple, exactly one with IsDefaultMetricsBackend()=true → use it
//  3. Multiple with IsDefaultMetricsBackend()=true → config error
//  4. Multiple, none default → return nil (explicit wiring required)
//  5. Zero → return nil
func (c *Config) GetDefaultInstrumentMetrics() (InstrumentMetrics, hcl.Diagnostics) {
	var all []InstrumentMetrics

	for _, ms := range c.MetricsServers {
		all = append(all, ms)
	}
	for _, oc := range c.OtlpClients {
		all = append(all, oc)
	}

	if len(all) == 0 {
		return nil, nil
	}
	if len(all) == 1 {
		return all[0], nil
	}

	var defaults []InstrumentMetrics
	for _, im := range all {
		if im.IsDefaultMetricsBackend() {
			defaults = append(defaults, im)
		}
	}

	if len(defaults) == 1 {
		return defaults[0], nil
	}
	if len(defaults) > 1 {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Multiple default metrics backends",
				Detail:   "More than one server \"metrics\" or client \"otlp\" block has default_metrics = true. At most one can be the default.",
			},
		}
	}

	// Multiple backends, none marked default
	return nil, nil
}

// GetInstrumentMetricsFromExpression evaluates an HCL expression and returns
// it as an InstrumentMetrics. The expression may reference either a
// server "metrics" block or a client "otlp" block.
func GetInstrumentMetricsFromExpression(config *Config, expr hcl.Expression) (InstrumentMetrics, hcl.Diagnostics) {
	val, diags := expr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	exprRange := expr.Range()

	switch val.Type() {
	case ServerCapsuleType:
		server, err := GetServerFromCapsule(val)
		if err != nil {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Invalid server reference", Detail: err.Error(), Subject: &exprRange}}
		}
		im, ok := server.(InstrumentMetrics)
		if !ok {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Server does not support metrics", Detail: fmt.Sprintf("Server %q does not implement InstrumentMetrics", server.GetName()), Subject: &exprRange}}
		}
		return im, nil

	case ClientCapsuleType:
		client, err := GetClientFromCapsule(val)
		if err != nil {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Invalid client reference", Detail: err.Error(), Subject: &exprRange}}
		}
		im, ok := client.(InstrumentMetrics)
		if !ok {
			return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Client does not support metrics", Detail: fmt.Sprintf("Client %q does not implement InstrumentMetrics", client.GetName()), Subject: &exprRange}}
		}
		return im, nil

	default:
		return nil, hcl.Diagnostics{{Severity: hcl.DiagError, Summary: "Invalid metrics reference", Detail: "Expected a server \"metrics\" or client \"otlp\" reference", Subject: &exprRange}}
	}
}

// ResolveMeterProvider resolves a metric.MeterProvider from an optional HCL
// expression. If the expression is provided it must reference a server "metrics"
// or client "otlp" block. If omitted, the default metrics backend is used.
// Returns nil, nil when no metrics backend is configured (metrics disabled).
func ResolveMeterProvider(config *Config, expr hcl.Expression) (metric.MeterProvider, hcl.Diagnostics) {
	if IsExpressionProvided(expr) {
		im, diags := GetInstrumentMetricsFromExpression(config, expr)
		if diags.HasErrors() {
			return nil, diags
		}
		return im.GetMeterProvider(), nil
	}
	im, diags := config.GetDefaultInstrumentMetrics()
	if diags.HasErrors() || im == nil {
		return nil, diags
	}
	return im.GetMeterProvider(), nil
}

// --- MetricBlockHandler ---

type MetricBlockHandler struct {
	BlockHandlerBase
	names               []string             // declaration order for duplicate check
	metrics             map[string]cty.Value // name → capsule, populated during Process
	implicitBackendDeps []string             // server/client block IDs for implicit deps
}

func NewMetricBlockHandler() *MetricBlockHandler {
	return &MetricBlockHandler{
		metrics: make(map[string]cty.Value),
	}
}

// SetImplicitBackendDeps records the dependency IDs of server "metrics" and
// client "otlp" blocks so that metric blocks without an explicit server
// attribute are ordered after them.
func (h *MetricBlockHandler) SetImplicitBackendDeps(ids []string) {
	h.implicitBackendDeps = ids
}

func (h *MetricBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "metric." + block.Labels[1], nil
}

func (h *MetricBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	deps := ExtractBlockDependencies(block)

	// If the metric block has no explicit "server" attribute, it will use the
	// default metrics backend. Add implicit dependencies on all server "metrics"
	// and client "otlp" blocks so they are processed first.
	if len(h.implicitBackendDeps) > 0 {
		hasServerAttr := false
		if syntaxBody, ok := block.Body.(*hclsyntax.Body); ok {
			_, hasServerAttr = syntaxBody.Attributes["server"]
		}
		if !hasServerAttr {
			deps = append(deps, h.implicitBackendDeps...)
		}
	}

	return deps, nil
}

func (h *MetricBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	kind := block.Labels[0]
	switch kind {
	case "gauge", "counter", "histogram":
		// valid
	default:
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid metric type",
			Detail:   fmt.Sprintf("Metric type must be \"gauge\", \"counter\", or \"histogram\", got %q", kind),
			Subject:  block.DefRange.Ptr(),
		}}
	}

	name := block.Labels[1]
	if _, exists := h.metrics[name]; exists {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate metric",
			Detail:   fmt.Sprintf("Metric %q is already defined", name),
			Subject:  block.DefRange.Ptr(),
		}}
	}
	// Reserve the name so duplicate check works across preprocess calls
	h.metrics[name] = cty.NilVal
	h.names = append(h.names, name)
	return nil
}

func (h *MetricBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	if len(h.names) == 0 {
		return nil
	}
	// Pre-populate an empty metric namespace so that subscription blocks that
	// reference metric.* don't get "variable not defined" errors during
	// dependency extraction. The actual capsule values are filled in Process.
	// Use VCL keys (dots → underscores) for attribute names.
	placeholder := make(map[string]cty.Value, len(h.names))
	for _, name := range h.names {
		key := strings.ReplaceAll(name, ".", "_")
		placeholder[key] = cty.NullVal(cty.DynamicPseudoType)
	}
	config.Constants["metric"] = cty.ObjectVal(placeholder)
	return nil
}

// MetricDefinition holds the decoded HCL body of a metric block.
type MetricDefinition struct {
	Help             string         `hcl:"help"`
	LabelNames       []string       `hcl:"label_names,optional"`
	Namespace        string         `hcl:"namespace,optional"`
	Buckets          []float64      `hcl:"buckets,optional"`
	Server           hcl.Expression `hcl:"server,optional"`
	Value            hcl.Expression `hcl:"value,optional"`
	ComputedInterval *string        `hcl:"computed_interval,optional"`
	DefRange         hcl.Range      `hcl:",def_range"`
}

// computedMetricStarter wraps a computed metric's Start(ctx) method as a
// config.Startable + config.Stoppable so it can be registered in the lifecycle.
type computedMetricStarter struct {
	startFn func(ctx context.Context)
	cancel  context.CancelFunc
}

func (s *computedMetricStarter) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.startFn(ctx)
	return nil
}

func (s *computedMetricStarter) Stop() error {
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

func (h *MetricBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	kind := block.Labels[0]
	name := block.Labels[1]

	def := MetricDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &def)
	if diags.HasErrors() {
		return diags
	}
	def.DefRange = block.DefRange

	// Resolve the metrics backend (MeterProvider)
	var mp metric.MeterProvider
	if def.Server != nil && IsExpressionProvided(def.Server) {
		im, imDiags := GetInstrumentMetricsFromExpression(config, def.Server)
		if imDiags.HasErrors() {
			return imDiags
		}
		mp = im.GetMeterProvider()
	} else {
		im, imDiags := config.GetDefaultInstrumentMetrics()
		if imDiags.HasErrors() {
			return imDiags
		}
		if im == nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "No metrics backend",
				Detail:   "No metrics backend is available. Either declare a server \"metrics\" or client \"otlp\" block, or set server = server.<name> on the metric block.",
				Subject:  def.DefRange.Ptr(),
			}}
		}
		mp = im.GetMeterProvider()
	}

	meter := mp.Meter("github.com/tsarna/vinculum/metric")

	// Build the OTel instrument name: namespace.name (dots preserved for OTel)
	fullName := name
	if def.Namespace != "" {
		fullName = def.Namespace + "." + name
	}

	// VCL key: dots → underscores
	vclKey := strings.ReplaceAll(name, ".", "_")
	if def.Namespace != "" {
		vclKey = strings.ReplaceAll(def.Namespace, ".", "_") + "_" + strings.ReplaceAll(name, ".", "_")
	}

	// Check for VCL key collisions
	for _, existingName := range h.names {
		if existingName == name {
			continue
		}
		existingVCLKey := strings.ReplaceAll(existingName, ".", "_")
		if existingVCLKey == vclKey {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Metric VCL name collision",
				Detail:   fmt.Sprintf("Metrics %q and %q both map to VCL key %q", existingName, name, vclKey),
				Subject:  def.DefRange.Ptr(),
			}}
		}
	}

	attrKeys := types.LabelNamesToAttrKeys(def.LabelNames)

	isComputed := def.Value != nil && IsExpressionProvided(def.Value)

	if isComputed && len(attrKeys) > 0 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid computed metric",
			Detail:   "Computed metrics (with value = ...) cannot have label_names. Labeled computed metrics are not yet supported.",
			Subject:  def.DefRange.Ptr(),
		}}
	}

	computedInterval := 15 * time.Second
	if def.ComputedInterval != nil {
		d, err := time.ParseDuration(*def.ComputedInterval)
		if err != nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Invalid computed_interval",
				Detail:   fmt.Sprintf("computed_interval: %s", err),
				Subject:  def.DefRange.Ptr(),
			}}
		}
		computedInterval = d
	}

	var capsule cty.Value

	switch kind {
	case "gauge":
		inst, err := meter.Float64UpDownCounter(fullName, metric.WithDescription(def.Help))
		if err != nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Failed to create gauge metric",
				Detail:   err.Error(),
				Subject:  def.DefRange.Ptr(),
			}}
		}
		if isComputed {
			m := types.NewComputedGaugeMetric(inst, def.Value, config.evalCtx, config.Logger, computedInterval)
			starter := &computedMetricStarter{startFn: m.Start}
			config.Startables = append(config.Startables, starter)
			config.Stoppables = append(config.Stoppables, starter)
			capsule = types.NewMetricCapsule(m)
		} else {
			capsule = types.NewMetricCapsule(types.NewGaugeMetric(inst, attrKeys))
		}

	case "counter":
		inst, err := meter.Float64Counter(fullName, metric.WithDescription(def.Help))
		if err != nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Failed to create counter metric",
				Detail:   err.Error(),
				Subject:  def.DefRange.Ptr(),
			}}
		}
		if isComputed {
			m := types.NewComputedCounterMetric(inst, def.Value, config.evalCtx, config.Logger, computedInterval)
			starter := &computedMetricStarter{startFn: m.Start}
			config.Startables = append(config.Startables, starter)
			config.Stoppables = append(config.Stoppables, starter)
			capsule = types.NewMetricCapsule(m)
		} else {
			capsule = types.NewMetricCapsule(types.NewCounterMetric(inst, attrKeys))
		}

	case "histogram":
		opts := []metric.Float64HistogramOption{metric.WithDescription(def.Help)}
		if len(def.Buckets) > 0 {
			opts = append(opts, metric.WithExplicitBucketBoundaries(def.Buckets...))
		}
		inst, err := meter.Float64Histogram(fullName, opts...)
		if err != nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Failed to create histogram metric",
				Detail:   err.Error(),
				Subject:  def.DefRange.Ptr(),
			}}
		}
		if isComputed {
			m := types.NewComputedHistogramMetric(inst, def.Value, config.evalCtx, config.Logger, computedInterval)
			starter := &computedMetricStarter{startFn: m.Start}
			config.Startables = append(config.Startables, starter)
			config.Stoppables = append(config.Stoppables, starter)
			capsule = types.NewMetricCapsule(m)
		} else {
			capsule = types.NewMetricCapsule(types.NewHistogramMetric(inst, attrKeys))
		}
	}

	h.metrics[name] = capsule

	// Rebuild the metric namespace object with real capsule values.
	// Only include metrics that have been fully processed (non-nil capsule).
	// Use VCL keys (dots → underscores) for the object attribute names.
	metricMap := make(map[string]cty.Value)
	for _, n := range h.names {
		if v := h.metrics[n]; v != cty.NilVal {
			key := strings.ReplaceAll(n, ".", "_")
			metricMap[key] = v
		}
	}
	if len(metricMap) > 0 {
		config.Constants["metric"] = cty.ObjectVal(metricMap)
	}

	return nil
}
