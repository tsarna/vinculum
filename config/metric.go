package config

import (
	"fmt"
	"math/big"
	"reflect"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// --- MetricValue marker interface ---

// MetricValue is implemented by all metric types (gauge, counter, histogram).
type MetricValue interface {
	metricValue()
}

// --- Capsule type ---

var MetricCapsuleType = cty.CapsuleWithOps("metric", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("metric(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "metric"
	},
})

func NewMetricCapsule(m MetricValue) cty.Value {
	return cty.CapsuleVal(MetricCapsuleType, m)
}

func GetMetricFromCapsule(val cty.Value) (MetricValue, error) {
	if val.Type() != MetricCapsuleType {
		return nil, fmt.Errorf("expected metric capsule, got %s", val.Type().FriendlyName())
	}
	encapsulated := val.EncapsulatedValue()
	m, ok := encapsulated.(MetricValue)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a MetricValue, got %T", encapsulated)
	}
	return m, nil
}

// --- labelsFromCtyObject ---

// labelsFromCtyObject converts an HCL object value to a prometheus.Labels map.
// It validates that the keys exactly match labelNames.
func labelsFromCtyObject(val cty.Value, labelNames []string) (prometheus.Labels, error) {
	if !val.Type().IsObjectType() {
		return nil, fmt.Errorf("labels must be an object, got %s", val.Type().FriendlyName())
	}

	attrs := val.Type().AttributeTypes()
	if len(attrs) != len(labelNames) {
		return nil, fmt.Errorf("expected %d label(s) %v, got %d", len(labelNames), labelNames, len(attrs))
	}

	labels := make(prometheus.Labels, len(labelNames))
	for _, name := range labelNames {
		attrVal, ok := val.GetAttr(name), true
		_ = ok
		if !val.Type().HasAttribute(name) {
			return nil, fmt.Errorf("missing label %q", name)
		}
		attrVal = val.GetAttr(name)
		if attrVal.Type() != cty.String {
			return nil, fmt.Errorf("label %q must be a string, got %s", name, attrVal.Type().FriendlyName())
		}
		labels[name] = attrVal.AsString()
	}

	// Check for extra keys
	for attrName := range attrs {
		found := false
		for _, name := range labelNames {
			if attrName == name {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unexpected label %q (declared label_names: %v)", attrName, labelNames)
		}
	}

	return labels, nil
}

// --- GaugeMetric ---

// GaugeMetric implements Gettable, Settable, Incrementable.
// When an optional labels object is passed as an extra arg, it operates on
// the labeled time series; otherwise it operates on the unlabeled series.
type GaugeMetric struct {
	vec        *prometheus.GaugeVec
	labelNames []string
	mu         sync.RWMutex
	noLabelVal float64            // cached value for unlabeled get
	labelVals  map[string]float64 // key = labelSetKey(labels), cached for labeled get
}

func (m *GaugeMetric) metricValue() {}

// --- Gettable ---
func (m *GaugeMetric) Get(args []cty.Value) (cty.Value, error) {
	if len(args) > 0 {
		pl, err := labelsFromCtyObject(args[0], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("get: %w", err)
		}
		key := labelSetKey(pl, m.labelNames)
		m.mu.RLock()
		defer m.mu.RUnlock()
		return cty.NumberFloatVal(m.labelVals[key]), nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cty.NumberFloatVal(m.noLabelVal), nil
}

// --- Settable ---
func (m *GaugeMetric) Set(args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("set: gauge metric requires a numeric value")
	}
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("set: %w", err)
	}
	if len(args) > 1 {
		pl, err := labelsFromCtyObject(args[1], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("set: %w", err)
		}
		key := labelSetKey(pl, m.labelNames)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		m.labelVals[key] = f
		m.mu.Unlock()
		m.vec.With(pl).Set(f)
		return value, nil
	}
	m.mu.Lock()
	m.noLabelVal = f
	m.mu.Unlock()
	m.vec.WithLabelValues().Set(f)
	return value, nil
}

// --- Incrementable ---
func (m *GaugeMetric) Increment(args []cty.Value) (cty.Value, error) {
	delta := args[0]
	f, err := valueToFloat64(delta)
	if err != nil {
		return cty.NilVal, fmt.Errorf("increment: %w", err)
	}
	if len(args) > 1 {
		pl, err := labelsFromCtyObject(args[1], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("increment: %w", err)
		}
		key := labelSetKey(pl, m.labelNames)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		m.labelVals[key] += f
		cur := m.labelVals[key]
		m.mu.Unlock()
		m.vec.With(pl).Add(f)
		return cty.NumberFloatVal(cur), nil
	}
	m.mu.Lock()
	m.noLabelVal += f
	cur := m.noLabelVal
	m.mu.Unlock()
	m.vec.WithLabelValues().Add(f)
	return cty.NumberFloatVal(cur), nil
}

// --- CounterMetric ---

// CounterMetric implements Gettable, Settable, Incrementable.
// set() uses delta semantics: only positive differences are applied to the
// underlying counter, so the Prometheus counter never decreases. If the supplied
// value is less than the last set value (e.g. an external reset), the call is a
// no-op and the counter holds its current value.
// When an optional labels object is passed as an extra arg, it operates on
// the labeled time series; otherwise it operates on the unlabeled series.
type CounterMetric struct {
	vec        *prometheus.CounterVec
	labelNames []string
	mu         sync.Mutex
	noLabelVal float64            // cached for unlabeled get/set
	labelVals  map[string]float64 // key = labelSetKey(labels), cached for labeled set
}

func (m *CounterMetric) metricValue() {}

// --- Gettable ---
func (m *CounterMetric) Get(args []cty.Value) (cty.Value, error) {
	if len(args) > 0 {
		_, err := labelsFromCtyObject(args[0], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("get: %w", err)
		}
		// prometheus CounterVec doesn't expose a Get; return 0
		return cty.NumberIntVal(0), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.noLabelVal), nil
}

// --- Settable ---
// Set updates the counter to the supplied value using delta semantics. Only
// positive differences are forwarded to the Prometheus counter; a value lower
// than the last seen value is silently ignored (the counter holds its current
// value). This allows syncing a counter from an external source that may reset.
func (m *CounterMetric) Set(args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("set: counter metric requires a numeric value")
	}
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("set: %w", err)
	}
	if len(args) > 1 {
		pl, err := labelsFromCtyObject(args[1], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("set: %w", err)
		}
		key := labelSetKey(pl, m.labelNames)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		delta := f - m.labelVals[key]
		if delta > 0 {
			m.labelVals[key] = f
			m.mu.Unlock()
			m.vec.With(pl).Add(delta)
		} else {
			m.mu.Unlock()
		}
		return value, nil
	}
	m.mu.Lock()
	delta := f - m.noLabelVal
	if delta > 0 {
		m.noLabelVal = f
		m.mu.Unlock()
		m.vec.WithLabelValues().Add(delta)
	} else {
		m.mu.Unlock()
	}
	return value, nil
}

// --- Incrementable ---
func (m *CounterMetric) Increment(args []cty.Value) (cty.Value, error) {
	delta := args[0]
	f, err := valueToFloat64(delta)
	if err != nil {
		return cty.NilVal, fmt.Errorf("increment: %w", err)
	}
	if f < 0 {
		return cty.NilVal, fmt.Errorf("increment: counter delta must be >= 0, got %v", f)
	}
	if len(args) > 1 {
		pl, err := labelsFromCtyObject(args[1], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("increment: %w", err)
		}
		m.vec.With(pl).Add(f)
		return delta, nil
	}
	m.mu.Lock()
	m.noLabelVal += f
	cur := m.noLabelVal
	m.mu.Unlock()
	m.vec.WithLabelValues().Add(f)
	return cty.NumberFloatVal(cur), nil
}

// --- HistogramMetric ---

// HistogramMetric implements Observable.
// When an optional labels object is passed as an extra arg, it operates on
// the labeled time series; otherwise it operates on the unlabeled series.
type HistogramMetric struct {
	vec        *prometheus.HistogramVec
	labelNames []string
}

func (m *HistogramMetric) metricValue() {}

// --- Observable ---
func (m *HistogramMetric) Observe(args []cty.Value) (cty.Value, error) {
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("observe: %w", err)
	}
	if len(args) > 1 {
		pl, err := labelsFromCtyObject(args[1], m.labelNames)
		if err != nil {
			return cty.NilVal, fmt.Errorf("observe: %w", err)
		}
		m.vec.With(pl).Observe(f)
		return value, nil
	}
	m.vec.WithLabelValues().Observe(f)
	return value, nil
}

// --- computedMetric marker ---

// computedMetric is implemented by all computed metric types. It is used by
// the VCL dispatch layer to produce a better error message when set() or
// increment() is called on a metric whose value is derived from an expression.
type computedMetric interface {
	isComputed()
}

// --- ComputedGaugeMetric ---

// ComputedGaugeMetric implements Gettable and prometheus.Collector.
// Its value is derived by evaluating a stored hcl.Expression at each Prometheus
// scrape. set() and increment() are not supported.
type ComputedGaugeMetric struct {
	desc    *prometheus.Desc
	expr    hcl.Expression
	evalCtx *hcl.EvalContext
	logger  *zap.Logger
	mu      sync.Mutex
	cached  float64
}

func (m *ComputedGaugeMetric) metricValue() {}
func (m *ComputedGaugeMetric) isComputed()  {}

func (m *ComputedGaugeMetric) Describe(ch chan<- *prometheus.Desc) { ch <- m.desc }
func (m *ComputedGaugeMetric) Collect(ch chan<- prometheus.Metric) {
	val, diags := m.expr.Value(m.evalCtx)
	m.mu.Lock()
	if !diags.HasErrors() {
		if f, err := valueToFloat64(val); err == nil {
			m.cached = f
		} else {
			m.logger.Error("computed gauge: expression did not return a number", zap.Error(err))
		}
	} else {
		m.logger.Error("computed gauge: expression evaluation failed", zap.String("err", diags.Error()))
	}
	f := m.cached
	m.mu.Unlock()
	ch <- prometheus.MustNewConstMetric(m.desc, prometheus.GaugeValue, f)
}

func (m *ComputedGaugeMetric) Get(args []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.cached), nil
}

// --- ComputedCounterMetric ---

// ComputedCounterMetric implements Gettable and prometheus.Collector.
// Its value is derived by evaluating a stored hcl.Expression at each Prometheus
// scrape. The raw expression result is reported as a CounterValue; Prometheus
// detects any resets (value drops) automatically.
type ComputedCounterMetric struct {
	desc    *prometheus.Desc
	expr    hcl.Expression
	evalCtx *hcl.EvalContext
	logger  *zap.Logger
	mu      sync.Mutex
	cached  float64
}

func (m *ComputedCounterMetric) metricValue() {}
func (m *ComputedCounterMetric) isComputed()  {}

func (m *ComputedCounterMetric) Describe(ch chan<- *prometheus.Desc) { ch <- m.desc }
func (m *ComputedCounterMetric) Collect(ch chan<- prometheus.Metric) {
	val, diags := m.expr.Value(m.evalCtx)
	m.mu.Lock()
	if !diags.HasErrors() {
		if f, err := valueToFloat64(val); err == nil {
			m.cached = f
		} else {
			m.logger.Error("computed counter: expression did not return a number", zap.Error(err))
		}
	} else {
		m.logger.Error("computed counter: expression evaluation failed", zap.String("err", diags.Error()))
	}
	f := m.cached
	m.mu.Unlock()
	ch <- prometheus.MustNewConstMetric(m.desc, prometheus.CounterValue, f)
}

func (m *ComputedCounterMetric) Get(args []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.cached), nil
}

// --- ComputedHistogramMetric ---

// ComputedHistogramMetric implements Observable and prometheus.Collector.
// At each Prometheus scrape it evaluates the stored expression and records one
// observation. Manual observe() calls are also supported.
type ComputedHistogramMetric struct {
	vec     *prometheus.HistogramVec // not registered directly; collected by this type
	expr    hcl.Expression
	evalCtx *hcl.EvalContext
	logger  *zap.Logger
}

func (m *ComputedHistogramMetric) metricValue() {}
func (m *ComputedHistogramMetric) isComputed()  {}

func (m *ComputedHistogramMetric) Describe(ch chan<- *prometheus.Desc) { m.vec.Describe(ch) }
func (m *ComputedHistogramMetric) Collect(ch chan<- prometheus.Metric) {
	val, diags := m.expr.Value(m.evalCtx)
	if !diags.HasErrors() {
		if f, err := valueToFloat64(val); err == nil {
			m.vec.WithLabelValues().Observe(f)
		} else {
			m.logger.Error("computed histogram: expression did not return a number", zap.Error(err))
		}
	} else {
		m.logger.Error("computed histogram: expression evaluation failed", zap.String("err", diags.Error()))
	}
	m.vec.Collect(ch)
}

// Observe allows manual observations in addition to the automatic scrape-time one.
func (m *ComputedHistogramMetric) Observe(args []cty.Value) (cty.Value, error) {
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("observe: %w", err)
	}
	m.vec.WithLabelValues().Observe(f)
	return value, nil
}

// --- helpers ---

// labelSetKey produces a stable string key for a prometheus.Labels map,
// ordered by labelNames, suitable for use as a map key.
func labelSetKey(pl prometheus.Labels, labelNames []string) string {
	key := ""
	for _, name := range labelNames {
		key += name + "=" + pl[name] + "\x00"
	}
	return key
}

func valueToFloat64(v cty.Value) (float64, error) {
	if v.Type() != cty.Number {
		return 0, fmt.Errorf("expected number, got %s", v.Type().FriendlyName())
	}
	f, _ := new(big.Float).SetPrec(64).Set(v.AsBigFloat()).Float64()
	return f, nil
}

// --- MetricBlockHandler ---

type MetricBlockHandler struct {
	BlockHandlerBase
	names   []string            // declaration order for duplicate check
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
	var ms *MetricsServer
	if def.Server != nil && IsExpressionProvided(def.Server) {
		var msDiags hcl.Diagnostics
		ms, msDiags = GetMetricsServerFromExpression(config, def.Server)
		if msDiags.HasErrors() {
			return msDiags
		}
	} else {
		provider, defDiags := config.GetDefaultMetricsProvider()
		if defDiags.HasErrors() {
			return defDiags
		}
		if provider == nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "No metrics server",
				Detail:   "No metrics server is available. Either declare a server \"metrics\" block or set server = server.<name> on the metric block.",
				Subject:  def.DefRange.Ptr(),
			}}
		}
		// Find the MetricsServer that owns this provider
		for _, candidate := range config.MetricsServers {
			if candidate.GetMetricsProvider() == provider {
				ms = candidate
				break
			}
		}
		if ms == nil {
			return hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  "Internal error",
				Detail:   "Could not locate MetricsServer for the default metrics provider",
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
			m := &ComputedGaugeMetric{desc: desc, expr: def.Value, evalCtx: config.evalCtx, logger: config.Logger}
			if err := reg.Register(m); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register computed gauge metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = NewMetricCapsule(m)
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
			capsule = NewMetricCapsule(&GaugeMetric{vec: vec, labelNames: labelNames})
		}

	case "counter":
		if isComputed {
			desc := prometheus.NewDesc(fullName, def.Help, nil, nil)
			m := &ComputedCounterMetric{desc: desc, expr: def.Value, evalCtx: config.evalCtx, logger: config.Logger}
			if err := reg.Register(m); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register computed counter metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = NewMetricCapsule(m)
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
			capsule = NewMetricCapsule(&CounterMetric{vec: vec, labelNames: labelNames})
		}

	case "histogram":
		buckets := prometheus.DefBuckets
		if len(def.Buckets) > 0 {
			buckets = def.Buckets
		}
		if isComputed {
			vec := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: fullName, Help: def.Help, Buckets: buckets}, nil)
			m := &ComputedHistogramMetric{vec: vec, expr: def.Value, evalCtx: config.evalCtx, logger: config.Logger}
			if err := reg.Register(m); err != nil {
				return hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "Failed to register computed histogram metric",
					Detail:   err.Error(),
					Subject:  def.DefRange.Ptr(),
				}}
			}
			capsule = NewMetricCapsule(m)
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
			capsule = NewMetricCapsule(&HistogramMetric{vec: vec, labelNames: labelNames})
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
