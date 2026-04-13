package types

import (
	"context"
	"fmt"
	"math/big"
	"reflect"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
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

// --- attributesFromCtyObject ---

// attributesFromCtyObject converts an HCL object value to OTel attribute key-values.
// It validates that the keys exactly match attrKeys.
func attributesFromCtyObject(val cty.Value, attrKeys []attribute.Key) ([]attribute.KeyValue, error) {
	if !val.Type().IsObjectType() {
		return nil, fmt.Errorf("labels must be an object, got %s", val.Type().FriendlyName())
	}

	attrs := val.Type().AttributeTypes()
	if len(attrs) != len(attrKeys) {
		names := make([]string, len(attrKeys))
		for i, k := range attrKeys {
			names[i] = string(k)
		}
		return nil, fmt.Errorf("expected %d label(s) %v, got %d", len(attrKeys), names, len(attrs))
	}

	kvs := make([]attribute.KeyValue, len(attrKeys))
	for i, key := range attrKeys {
		name := string(key)
		if !val.Type().HasAttribute(name) {
			return nil, fmt.Errorf("missing label %q", name)
		}
		attrVal := val.GetAttr(name)
		if attrVal.Type() != cty.String {
			return nil, fmt.Errorf("label %q must be a string, got %s", name, attrVal.Type().FriendlyName())
		}
		kvs[i] = key.String(attrVal.AsString())
	}

	// Check for extra keys
	for attrName := range attrs {
		found := false
		for _, key := range attrKeys {
			if attrName == string(key) {
				found = true
				break
			}
		}
		if !found {
			names := make([]string, len(attrKeys))
			for i, k := range attrKeys {
				names[i] = string(k)
			}
			return nil, fmt.Errorf("unexpected label %q (declared label_names: %v)", attrName, names)
		}
	}

	return kvs, nil
}

// attrSetKey produces a stable string key for an OTel attribute set,
// ordered by attrKeys, suitable for use as a map key.
func attrSetKey(kvs []attribute.KeyValue, attrKeys []attribute.Key) string {
	key := ""
	for _, ak := range attrKeys {
		for _, kv := range kvs {
			if kv.Key == ak {
				key += string(ak) + "=" + kv.Value.AsString() + "\x00"
				break
			}
		}
	}
	return key
}

// labelNamesToAttrKeys converts string label names to OTel attribute keys.
func LabelNamesToAttrKeys(names []string) []attribute.Key {
	keys := make([]attribute.Key, len(names))
	for i, n := range names {
		keys[i] = attribute.Key(n)
	}
	return keys
}

// --- GaugeMetric ---

// GaugeMetric implements Gettable, Settable, Incrementable, Watchable.
// Uses OTel Float64UpDownCounter with delta tracking to support absolute Set().
type GaugeMetric struct {
	inst     metric.Float64UpDownCounter
	attrKeys []attribute.Key
	mu       sync.RWMutex
	WatchableMixin
	noLabelVal float64            // cached value for unlabeled get
	labelVals  map[string]float64 // key = attrSetKey, cached for labeled get
}

func NewGaugeMetric(inst metric.Float64UpDownCounter, attrKeys []attribute.Key) *GaugeMetric {
	return &GaugeMetric{inst: inst, attrKeys: attrKeys}
}

func (m *GaugeMetric) metricValue() {}

// --- Gettable ---
func (m *GaugeMetric) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) > 0 {
		kvs, err := attributesFromCtyObject(args[0], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("get: %w", err)
		}
		key := attrSetKey(kvs, m.attrKeys)
		m.mu.RLock()
		defer m.mu.RUnlock()
		return cty.NumberFloatVal(m.labelVals[key]), nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cty.NumberFloatVal(m.noLabelVal), nil
}

// --- Settable ---
func (m *GaugeMetric) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("set: gauge metric requires a numeric value")
	}
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("set: %w", err)
	}
	if len(args) > 1 {
		kvs, err := attributesFromCtyObject(args[1], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("set: %w", err)
		}
		key := attrSetKey(kvs, m.attrKeys)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		old := m.labelVals[key]
		m.labelVals[key] = f
		m.mu.Unlock()
		delta := f - old
		if delta != 0 {
			m.inst.Add(ctx, delta, metric.WithAttributes(kvs...))
		}
		m.NotifyAll(ctx, cty.NumberFloatVal(old), value)
		return value, nil
	}
	m.mu.Lock()
	old := m.noLabelVal
	m.noLabelVal = f
	m.mu.Unlock()
	delta := f - old
	if delta != 0 {
		m.inst.Add(ctx, delta)
	}
	m.NotifyAll(ctx, cty.NumberFloatVal(old), value)
	return value, nil
}

// --- Incrementable ---
func (m *GaugeMetric) Increment(ctx context.Context, args []cty.Value) (cty.Value, error) {
	delta := args[0]
	f, err := valueToFloat64(delta)
	if err != nil {
		return cty.NilVal, fmt.Errorf("increment: %w", err)
	}
	if len(args) > 1 {
		kvs, err := attributesFromCtyObject(args[1], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("increment: %w", err)
		}
		key := attrSetKey(kvs, m.attrKeys)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		old := m.labelVals[key]
		m.labelVals[key] += f
		cur := m.labelVals[key]
		m.mu.Unlock()
		m.inst.Add(ctx, f, metric.WithAttributes(kvs...))
		m.NotifyAll(ctx, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
		return cty.NumberFloatVal(cur), nil
	}
	m.mu.Lock()
	old := m.noLabelVal
	m.noLabelVal += f
	cur := m.noLabelVal
	m.mu.Unlock()
	m.inst.Add(ctx, f)
	m.NotifyAll(ctx, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
	return cty.NumberFloatVal(cur), nil
}

// --- CounterMetric ---

// CounterMetric implements Gettable, Settable, Incrementable, Watchable.
// set() uses delta semantics: only positive differences are applied to the
// underlying counter. If the supplied value is less than the last set value
// (e.g. an external reset), the call is a no-op and the counter holds its
// current value.
type CounterMetric struct {
	inst     metric.Float64Counter
	attrKeys []attribute.Key
	mu       sync.Mutex
	WatchableMixin
	noLabelVal float64            // cached for unlabeled get/set
	labelVals  map[string]float64 // key = attrSetKey, cached for labeled set
}

func NewCounterMetric(inst metric.Float64Counter, attrKeys []attribute.Key) *CounterMetric {
	return &CounterMetric{inst: inst, attrKeys: attrKeys}
}

func (m *CounterMetric) metricValue() {}

// --- Gettable ---
func (m *CounterMetric) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) > 0 {
		_, err := attributesFromCtyObject(args[0], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("get: %w", err)
		}
		return cty.NumberIntVal(0), nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.noLabelVal), nil
}

// --- Settable ---
func (m *CounterMetric) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("set: counter metric requires a numeric value")
	}
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("set: %w", err)
	}
	if len(args) > 1 {
		kvs, err := attributesFromCtyObject(args[1], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("set: %w", err)
		}
		key := attrSetKey(kvs, m.attrKeys)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		old := m.labelVals[key]
		delta := f - old
		newVal := old
		if delta > 0 {
			m.labelVals[key] = f
			newVal = f
		}
		m.mu.Unlock()
		if delta > 0 {
			m.inst.Add(ctx, delta, metric.WithAttributes(kvs...))
		}
		m.NotifyAll(ctx, cty.NumberFloatVal(old), cty.NumberFloatVal(newVal))
		return value, nil
	}
	m.mu.Lock()
	old := m.noLabelVal
	delta := f - old
	newVal := old
	if delta > 0 {
		m.noLabelVal = f
		newVal = f
	}
	m.mu.Unlock()
	if delta > 0 {
		m.inst.Add(ctx, delta)
	}
	m.NotifyAll(ctx, cty.NumberFloatVal(old), cty.NumberFloatVal(newVal))
	return value, nil
}

// --- Incrementable ---
func (m *CounterMetric) Increment(ctx context.Context, args []cty.Value) (cty.Value, error) {
	delta := args[0]
	f, err := valueToFloat64(delta)
	if err != nil {
		return cty.NilVal, fmt.Errorf("increment: %w", err)
	}
	if f < 0 {
		return cty.NilVal, fmt.Errorf("increment: counter delta must be >= 0, got %v", f)
	}
	if len(args) > 1 {
		kvs, err := attributesFromCtyObject(args[1], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("increment: %w", err)
		}
		key := attrSetKey(kvs, m.attrKeys)
		m.mu.Lock()
		if m.labelVals == nil {
			m.labelVals = make(map[string]float64)
		}
		old := m.labelVals[key]
		m.labelVals[key] += f
		cur := m.labelVals[key]
		m.mu.Unlock()
		m.inst.Add(ctx, f, metric.WithAttributes(kvs...))
		m.NotifyAll(ctx, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
		return cty.NumberFloatVal(cur), nil
	}
	m.mu.Lock()
	old := m.noLabelVal
	m.noLabelVal += f
	cur := m.noLabelVal
	m.mu.Unlock()
	m.inst.Add(ctx, f)
	m.NotifyAll(ctx, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
	return cty.NumberFloatVal(cur), nil
}

// --- HistogramMetric ---

// HistogramMetric implements Observable.
type HistogramMetric struct {
	inst     metric.Float64Histogram
	attrKeys []attribute.Key
}

func NewHistogramMetric(inst metric.Float64Histogram, attrKeys []attribute.Key) *HistogramMetric {
	return &HistogramMetric{inst: inst, attrKeys: attrKeys}
}

func (m *HistogramMetric) metricValue() {}

// --- Observable ---
func (m *HistogramMetric) Observe(ctx context.Context, args []cty.Value) (cty.Value, error) {
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("observe: %w", err)
	}
	if len(args) > 1 {
		kvs, err := attributesFromCtyObject(args[1], m.attrKeys)
		if err != nil {
			return cty.NilVal, fmt.Errorf("observe: %w", err)
		}
		m.inst.Record(ctx, f, metric.WithAttributes(kvs...))
		return value, nil
	}
	m.inst.Record(ctx, f)
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

// ComputedGaugeMetric implements Gettable and Startable.
// Its value is derived by evaluating a stored hcl.Expression on a polling
// interval. set() and increment() are not supported.
type ComputedGaugeMetric struct {
	inst     metric.Float64UpDownCounter
	expr     hcl.Expression
	evalCtx  *hcl.EvalContext
	logger   *zap.Logger
	interval time.Duration
	mu       sync.Mutex
	cached   float64
}

func NewComputedGaugeMetric(inst metric.Float64UpDownCounter, expr hcl.Expression, evalCtx *hcl.EvalContext, logger *zap.Logger, interval time.Duration) *ComputedGaugeMetric {
	return &ComputedGaugeMetric{inst: inst, expr: expr, evalCtx: evalCtx, logger: logger, interval: interval}
}

func (m *ComputedGaugeMetric) metricValue() {}
func (m *ComputedGaugeMetric) isComputed()  {}

func (m *ComputedGaugeMetric) poll(ctx context.Context) {
	val, diags := m.expr.Value(m.evalCtx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !diags.HasErrors() {
		if f, err := valueToFloat64(val); err == nil {
			delta := f - m.cached
			m.cached = f
			if delta != 0 {
				m.inst.Add(ctx, delta)
			}
		} else {
			m.logger.Error("computed gauge: expression did not return a number", zap.Error(err))
		}
	} else {
		m.logger.Error("computed gauge: expression evaluation failed", zap.String("err", diags.Error()))
	}
}

// Start launches the polling goroutine. Implements config.Startable.
func (m *ComputedGaugeMetric) Start(ctx context.Context) {
	go func() {
		// Poll once immediately
		m.poll(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.poll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *ComputedGaugeMetric) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.cached), nil
}

// --- ComputedCounterMetric ---

// ComputedCounterMetric implements Gettable and Startable.
// Its value is derived by evaluating a stored hcl.Expression on a polling
// interval. Only positive deltas are forwarded to the OTel counter.
type ComputedCounterMetric struct {
	inst     metric.Float64Counter
	expr     hcl.Expression
	evalCtx  *hcl.EvalContext
	logger   *zap.Logger
	interval time.Duration
	mu       sync.Mutex
	cached   float64
}

func NewComputedCounterMetric(inst metric.Float64Counter, expr hcl.Expression, evalCtx *hcl.EvalContext, logger *zap.Logger, interval time.Duration) *ComputedCounterMetric {
	return &ComputedCounterMetric{inst: inst, expr: expr, evalCtx: evalCtx, logger: logger, interval: interval}
}

func (m *ComputedCounterMetric) metricValue() {}
func (m *ComputedCounterMetric) isComputed()  {}

func (m *ComputedCounterMetric) poll(ctx context.Context) {
	val, diags := m.expr.Value(m.evalCtx)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !diags.HasErrors() {
		if f, err := valueToFloat64(val); err == nil {
			delta := f - m.cached
			if delta > 0 {
				m.cached = f
				m.inst.Add(ctx, delta)
			}
		} else {
			m.logger.Error("computed counter: expression did not return a number", zap.Error(err))
		}
	} else {
		m.logger.Error("computed counter: expression evaluation failed", zap.String("err", diags.Error()))
	}
}

// Start launches the polling goroutine. Implements config.Startable.
func (m *ComputedCounterMetric) Start(ctx context.Context) {
	go func() {
		m.poll(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.poll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (m *ComputedCounterMetric) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.cached), nil
}

// --- ComputedHistogramMetric ---

// ComputedHistogramMetric implements Observable and Startable.
// At each polling interval it evaluates the stored expression and records one
// observation. Manual Observe() calls are also supported.
type ComputedHistogramMetric struct {
	inst     metric.Float64Histogram
	expr     hcl.Expression
	evalCtx  *hcl.EvalContext
	logger   *zap.Logger
	interval time.Duration
}

func NewComputedHistogramMetric(inst metric.Float64Histogram, expr hcl.Expression, evalCtx *hcl.EvalContext, logger *zap.Logger, interval time.Duration) *ComputedHistogramMetric {
	return &ComputedHistogramMetric{inst: inst, expr: expr, evalCtx: evalCtx, logger: logger, interval: interval}
}

func (m *ComputedHistogramMetric) metricValue() {}
func (m *ComputedHistogramMetric) isComputed()  {}

func (m *ComputedHistogramMetric) poll(ctx context.Context) {
	val, diags := m.expr.Value(m.evalCtx)
	if !diags.HasErrors() {
		if f, err := valueToFloat64(val); err == nil {
			m.inst.Record(ctx, f)
		} else {
			m.logger.Error("computed histogram: expression did not return a number", zap.Error(err))
		}
	} else {
		m.logger.Error("computed histogram: expression evaluation failed", zap.String("err", diags.Error()))
	}
}

// Start launches the polling goroutine. Implements config.Startable.
func (m *ComputedHistogramMetric) Start(ctx context.Context) {
	go func() {
		m.poll(ctx)
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				m.poll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// Observe allows manual observations in addition to the automatic polling one.
func (m *ComputedHistogramMetric) Observe(ctx context.Context, args []cty.Value) (cty.Value, error) {
	value := args[0]
	f, err := valueToFloat64(value)
	if err != nil {
		return cty.NilVal, fmt.Errorf("observe: %w", err)
	}
	m.inst.Record(ctx, f)
	return value, nil
}

// --- helpers ---

func valueToFloat64(v cty.Value) (float64, error) {
	if v.Type() != cty.Number {
		return 0, fmt.Errorf("expected number, got %s", v.Type().FriendlyName())
	}
	f, _ := new(big.Float).SetPrec(64).Set(v.AsBigFloat()).Float64()
	return f, nil
}
