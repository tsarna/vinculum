package config

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tsarna/vinculum-bus/o11y"
	"github.com/tsarna/vinculum/types"
	"github.com/zclconf/go-cty/cty"
)

// --- MetricsRegistrar interface ---

// MetricsRegistrar is implemented by server types that provide a Prometheus
// registry for metric blocks to register against.
type MetricsRegistrar interface {
	Listener
	GetRegistry() *prometheus.Registry
	GetMetricsProvider() o11y.MetricsProvider
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

// GetDefaultMetricsProvider returns the o11y.MetricsProvider from the default
// MetricsRegistrar. See GetDefaultMetricsRegistrar for selection rules.
func (c *Config) GetDefaultMetricsProvider() (o11y.MetricsProvider, hcl.Diagnostics) {
	ms, diags := c.GetDefaultMetricsRegistrar()
	if diags.HasErrors() || ms == nil {
		return nil, diags
	}
	return ms.GetMetricsProvider(), nil
}

// ResolveMetricsProvider resolves an o11y.MetricsProvider from an optional HCL
// expression. If the expression is provided it must reference a server "metrics"
// block. If omitted, the default metrics provider is used. Returns nil, nil when
// no metrics server is configured at all (metrics are simply disabled).
func ResolveMetricsProvider(config *Config, expr hcl.Expression) (o11y.MetricsProvider, hcl.Diagnostics) {
	if IsExpressionProvided(expr) {
		ms, diags := GetMetricsRegistrarFromExpression(config, expr)
		if diags.HasErrors() {
			return nil, diags
		}
		return ms.GetMetricsProvider(), nil
	}
	return config.GetDefaultMetricsProvider()
}

// --- MetricBlockHandler ---

type MetricBlockHandler struct {
	BlockHandlerBase
	names   []string             // declaration order for duplicate check
	metrics map[string]cty.Value // name → capsule, populated during Process
}

func NewMetricBlockHandler() *MetricBlockHandler {
	return &MetricBlockHandler{
		metrics: make(map[string]cty.Value),
	}
}

func (h *MetricBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "metric." + block.Labels[1], nil
}

func (h *MetricBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	return ExtractBlockDependencies(block), nil
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
	placeholder := make(map[string]cty.Value, len(h.names))
	for _, name := range h.names {
		placeholder[name] = cty.NullVal(cty.DynamicPseudoType)
	}
	config.Constants["metric"] = cty.ObjectVal(placeholder)
	return nil
}

// MetricDefinition holds the decoded HCL body of a metric block.
type MetricDefinition struct {
	Help       string         `hcl:"help"`
	LabelNames []string       `hcl:"label_names,optional"`
	Namespace  string         `hcl:"namespace,optional"`
	Buckets    []float64      `hcl:"buckets,optional"`
	Server     hcl.Expression `hcl:"server,optional"`
	Value      hcl.Expression `hcl:"value,optional"`
	DefRange   hcl.Range      `hcl:",def_range"`
}

func (h *MetricBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	kind := block.Labels[0]
	name := block.Labels[1]

	def := MetricDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &def)
	if diags.HasErrors() {
		return diags
	}

	// Resolve the registry
	var ms MetricsRegistrar
	if def.Server != nil && IsExpressionProvided(def.Server) {
		var msDiags hcl.Diagnostics
		ms, msDiags = GetMetricsRegistrarFromExpression(config, def.Server)
		if msDiags.HasErrors() {
			return msDiags
		}
	} else {
		var defDiags hcl.Diagnostics
		ms, defDiags = config.GetDefaultMetricsRegistrar()
		if defDiags.HasErrors() {
			return defDiags
		}
		if ms == nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "No metrics server",
				Detail:   "No metrics server is available. Either declare a server \"metrics\" block or set server = server.<name> on the metric block.",
				Subject:  def.DefRange.Ptr(),
			}}
		}
	}

	reg := ms.GetRegistry()

	fullName := name
	if def.Namespace != "" {
		fullName = def.Namespace + "_" + name
	}

	labelNames := def.LabelNames
	if labelNames == nil {
		labelNames = []string{}
	}

	isComputed := def.Value != nil && IsExpressionProvided(def.Value)

	if isComputed && len(labelNames) > 0 {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Invalid computed metric",
			Detail:   "Computed metrics (with value = ...) cannot have label_names. Labeled computed metrics are not yet supported.",
			Subject:  def.DefRange.Ptr(),
		}}
	}

	var capsule cty.Value

	switch kind {
	case "gauge":
		if isComputed {
			desc := prometheus.NewDesc(fullName, def.Help, nil, nil)
			m := types.NewComputedGaugeMetric(desc, def.Value, config.evalCtx, config.Logger)
			if err := reg.Register(m); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register computed gauge metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = types.NewMetricCapsule(m)
		} else {
			vec := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: fullName, Help: def.Help}, labelNames)
			if err := reg.Register(vec); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register gauge metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = types.NewMetricCapsule(types.NewGaugeMetric(vec, labelNames))
		}

	case "counter":
		if isComputed {
			desc := prometheus.NewDesc(fullName, def.Help, nil, nil)
			m := types.NewComputedCounterMetric(desc, def.Value, config.evalCtx, config.Logger)
			if err := reg.Register(m); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register computed counter metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = types.NewMetricCapsule(m)
		} else {
			vec := prometheus.NewCounterVec(prometheus.CounterOpts{Name: fullName, Help: def.Help}, labelNames)
			if err := reg.Register(vec); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register counter metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = types.NewMetricCapsule(types.NewCounterMetric(vec, labelNames))
		}

	case "histogram":
		buckets := prometheus.DefBuckets
		if len(def.Buckets) > 0 {
			buckets = def.Buckets
		}
		if isComputed {
			vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: fullName, Help: def.Help, Buckets: buckets}, nil)
			m := types.NewComputedHistogramMetric(vec, def.Value, config.evalCtx, config.Logger)
			if err := reg.Register(m); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register computed histogram metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = types.NewMetricCapsule(m)
		} else {
			vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: fullName, Help: def.Help, Buckets: buckets}, labelNames)
			if err := reg.Register(vec); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register histogram metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = types.NewMetricCapsule(types.NewHistogramMetric(vec, labelNames))
		}
	}

	h.metrics[name] = capsule

	// Rebuild the metric namespace object with real capsule values.
	// Only include metrics that have been fully processed (non-nil capsule).
	metricMap := make(map[string]cty.Value)
	for _, n := range h.names {
		if v := h.metrics[n]; v != cty.NilVal {
			metricMap[n] = v
		}
	}
	if len(metricMap) > 0 {
		config.Constants["metric"] = cty.ObjectVal(metricMap)
	}

	return nil
}
