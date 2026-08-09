package types

import (
	"context"
	"fmt"
	"math/big"
	"reflect"
	"sync"
	"time"

	richcty "github.com/tsarna/rich-cty-types"

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

// GaugeMetric implements richcty.Gettable, richcty.Settable, richcty.Incrementable, richcty.Watchable.
// Uses OTel Float64UpDownCounter with delta tracking to support absolute Set().
type GaugeMetric struct {
	inst     metric.Float64UpDownCounter
	attrKeys []attribute.Key
	mu       sync.RWMutex
	richcty.WatchableMixin
	noLabelVal float64            // cached value for unlabeled get
	labelVals  map[string]float64 // key = attrSetKey, cached for labeled get
}

func NewGaugeMetric(inst metric.Float64UpDownCounter, attrKeys []attribute.Key) *GaugeMetric {
	return &GaugeMetric{inst: inst, attrKeys: attrKeys}
}

func (m *GaugeMetric) metricValue() {}

// --- richcty.Gettable ---
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

// --- richcty.Settable ---
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
		m.NotifyAll(ctx, m, cty.NumberFloatVal(old), value)
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
	m.NotifyAll(ctx, m, cty.NumberFloatVal(old), value)
	return value, nil
}

// --- richcty.Incrementable ---
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
		m.NotifyAll(ctx, m, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
		return cty.NumberFloatVal(cur), nil
	}
	m.mu.Lock()
	old := m.noLabelVal
	m.noLabelVal += f
	cur := m.noLabelVal
	m.mu.Unlock()
	m.inst.Add(ctx, f)
	m.NotifyAll(ctx, m, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
	return cty.NumberFloatVal(cur), nil
}

// --- CounterMetric ---

// CounterMetric implements richcty.Gettable, richcty.Settable, richcty.Incrementable, richcty.Watchable.
// set() uses delta semantics: only positive differences are applied to the
// underlying counter. If the supplied value is less than the last set value
// (e.g. an external reset), the call is a no-op and the counter holds its
// current value.
type CounterMetric struct {
	inst     metric.Float64Counter
	attrKeys []attribute.Key
	mu       sync.Mutex
	richcty.WatchableMixin
	noLabelVal float64            // cached for unlabeled get/set
	labelVals  map[string]float64 // key = attrSetKey, cached for labeled set
}

func NewCounterMetric(inst metric.Float64Counter, attrKeys []attribute.Key) *CounterMetric {
	return &CounterMetric{inst: inst, attrKeys: attrKeys}
}

func (m *CounterMetric) metricValue() {}

// --- richcty.Gettable ---
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

// --- richcty.Settable ---
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
		m.NotifyAll(ctx, m, cty.NumberFloatVal(old), cty.NumberFloatVal(newVal))
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
	m.NotifyAll(ctx, m, cty.NumberFloatVal(old), cty.NumberFloatVal(newVal))
	return value, nil
}

// --- richcty.Incrementable ---
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
		m.NotifyAll(ctx, m, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
		return cty.NumberFloatVal(cur), nil
	}
	m.mu.Lock()
	old := m.noLabelVal
	m.noLabelVal += f
	cur := m.noLabelVal
	m.mu.Unlock()
	m.inst.Add(ctx, f)
	m.NotifyAll(ctx, m, cty.NumberFloatVal(old), cty.NumberFloatVal(cur))
	return cty.NumberFloatVal(cur), nil
}

// --- HistogramMetric ---

// HistogramMetric implements richcty.Observable.
type HistogramMetric struct {
	inst     metric.Float64Histogram
	attrKeys []attribute.Key
}

func NewHistogramMetric(inst metric.Float64Histogram, attrKeys []attribute.Key) *HistogramMetric {
	return &HistogramMetric{inst: inst, attrKeys: attrKeys}
}

func (m *HistogramMetric) metricValue() {}

// --- richcty.Observable ---
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

// --- computed metrics ---

// PollScope is the environment for one poll of a computed metric: a context to
// evaluate against, the HCL eval context built from it, and a function to end
// the poll with whatever went wrong.
type PollScope struct {
	Ctx     context.Context
	EvalCtx *hcl.EvalContext
	Done    func(error)
}

// PollScopeFunc opens the scope for one poll. It is supplied by the config
// layer rather than built here: assembling a `ctx` goes through hclutil, which
// depends on this package, so this package cannot depend on it.
type PollScopeFunc func(context.Context) (PollScope, error)

// computedEval is the part every computed metric shares: evaluating its
// expression once per interval and reporting what went wrong.
type computedEval struct {
	kind     string // "gauge", "counter", "histogram" — for messages
	expr     hcl.Expression
	scopeFn  PollScopeFunc
	logger   *zap.Logger // UserLogger: these errors are all caused by the VCL
	interval time.Duration

	// The failure the last poll reported, so an unchanging one can be said
	// once rather than every interval. Only ever touched from the goroutine
	// startPolling launches, which is the sole caller of evaluate.
	lastWhat, lastDetail string
	repeats              int
}

// failed reports one failed poll: loudly the first time, and quietly for as
// long as it keeps saying the same thing.
//
// A `value` that cannot be evaluated cannot be evaluated at every interval
// forever, so at Error level one broken expression will fill a log with one
// fact restated a few times a minute — and bury the failure that is new. The
// repeat count rides along on the quiet lines so it is still visible that it is
// still happening, and a failure that changes is loud again.
//
// Only the log line is dampened. The poll's span is still marked and the
// duration histogram still records it, so nothing that is being watched by a
// monitor goes quiet.
func (c *computedEval) failed(what, detail string) {
	msg := "computed " + c.kind + ": " + what
	if what == c.lastWhat && detail == c.lastDetail {
		c.repeats++
		c.logger.Debug(msg, zap.String("error", detail), zap.Int("repeats", c.repeats))
		return
	}
	c.lastWhat, c.lastDetail, c.repeats = what, detail, 0
	c.logger.Error(msg, zap.String("error", detail))
}

// succeeded closes out a run of failures, so a recovery is as visible as the
// break was and the next failure starts loud again.
func (c *computedEval) succeeded() {
	if c.lastWhat == "" {
		return
	}
	c.logger.Info("computed "+c.kind+": recovered",
		zap.Int("failed_polls", c.repeats+1))
	c.lastWhat, c.lastDetail, c.repeats = "", "", 0
}

// evaluate runs one poll, returning false when no value could be produced.
// The returned context is the poll's own — recording the value against it
// keeps the metric write inside the poll's span.
func (c *computedEval) evaluate(ctx context.Context) (context.Context, float64, bool) {
	scope, err := c.scopeFn(ctx)
	if err != nil {
		c.failed("building poll context", err.Error())
		return ctx, 0, false
	}

	val, diags := c.expr.Value(scope.EvalCtx)
	if diags.HasErrors() {
		c.failed("expression evaluation failed", diags.Error())
		scope.Done(diags)
		return scope.Ctx, 0, false
	}

	f, err := valueToFloat64(val)
	if err != nil {
		c.failed("expression did not return a number", err.Error())
		scope.Done(err)
		return scope.Ctx, 0, false
	}

	c.succeeded()
	scope.Done(nil)
	return scope.Ctx, f, true
}

// startPolling runs poll immediately and then every interval until ctx is done.
func startPolling(ctx context.Context, interval time.Duration, poll func(context.Context)) {
	go func() {
		poll(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				poll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
}

// --- ComputedGaugeMetric ---

// ComputedGaugeMetric implements richcty.Gettable and Startable.
// Its value is derived by evaluating a stored hcl.Expression on a polling
// interval. set() and increment() are not supported.
type ComputedGaugeMetric struct {
	computedEval
	inst   metric.Float64UpDownCounter
	mu     sync.Mutex
	cached float64
}

func NewComputedGaugeMetric(inst metric.Float64UpDownCounter, expr hcl.Expression, scopeFn PollScopeFunc, logger *zap.Logger, interval time.Duration) *ComputedGaugeMetric {
	return &ComputedGaugeMetric{
		computedEval: computedEval{kind: "gauge", expr: expr, scopeFn: scopeFn, logger: logger, interval: interval},
		inst:         inst,
	}
}

func (m *ComputedGaugeMetric) metricValue() {}
func (m *ComputedGaugeMetric) isComputed()  {}

func (m *ComputedGaugeMetric) poll(ctx context.Context) {
	pollCtx, f, ok := m.evaluate(ctx)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delta := f - m.cached
	m.cached = f
	if delta != 0 {
		m.inst.Add(pollCtx, delta)
	}
}

// Start launches the polling goroutine. Implements config.Startable.
func (m *ComputedGaugeMetric) Start(ctx context.Context) {
	startPolling(ctx, m.interval, m.poll)
}

func (m *ComputedGaugeMetric) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.cached), nil
}

// --- ComputedCounterMetric ---

// ComputedCounterMetric implements richcty.Gettable and Startable.
// Its value is derived by evaluating a stored hcl.Expression on a polling
// interval. Only positive deltas are forwarded to the OTel counter.
type ComputedCounterMetric struct {
	computedEval
	inst   metric.Float64Counter
	mu     sync.Mutex
	cached float64
}

func NewComputedCounterMetric(inst metric.Float64Counter, expr hcl.Expression, scopeFn PollScopeFunc, logger *zap.Logger, interval time.Duration) *ComputedCounterMetric {
	return &ComputedCounterMetric{
		computedEval: computedEval{kind: "counter", expr: expr, scopeFn: scopeFn, logger: logger, interval: interval},
		inst:         inst,
	}
}

func (m *ComputedCounterMetric) metricValue() {}
func (m *ComputedCounterMetric) isComputed()  {}

func (m *ComputedCounterMetric) poll(ctx context.Context) {
	pollCtx, f, ok := m.evaluate(ctx)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delta := f - m.cached
	if delta > 0 {
		m.cached = f
		m.inst.Add(pollCtx, delta)
	}
}

// Start launches the polling goroutine. Implements config.Startable.
func (m *ComputedCounterMetric) Start(ctx context.Context) {
	startPolling(ctx, m.interval, m.poll)
}

func (m *ComputedCounterMetric) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cty.NumberFloatVal(m.cached), nil
}

// --- ComputedHistogramMetric ---

// ComputedHistogramMetric implements richcty.Observable and Startable.
// At each polling interval it evaluates the stored expression and records one
// observation. Manual Observe() calls are also supported.
type ComputedHistogramMetric struct {
	computedEval
	inst metric.Float64Histogram
}

func NewComputedHistogramMetric(inst metric.Float64Histogram, expr hcl.Expression, scopeFn PollScopeFunc, logger *zap.Logger, interval time.Duration) *ComputedHistogramMetric {
	return &ComputedHistogramMetric{
		computedEval: computedEval{kind: "histogram", expr: expr, scopeFn: scopeFn, logger: logger, interval: interval},
		inst:         inst,
	}
}

func (m *ComputedHistogramMetric) metricValue() {}
func (m *ComputedHistogramMetric) isComputed()  {}

func (m *ComputedHistogramMetric) poll(ctx context.Context) {
	pollCtx, f, ok := m.evaluate(ctx)
	if !ok {
		return
	}
	m.inst.Record(pollCtx, f)
}

// Start launches the polling goroutine. Implements config.Startable.
func (m *ComputedHistogramMetric) Start(ctx context.Context) {
	startPolling(ctx, m.interval, m.poll)
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
