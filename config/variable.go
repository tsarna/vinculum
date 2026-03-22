package config

import (
	"fmt"
	"math/big"
	"reflect"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// --- Interfaces for set/get/increment extensibility ---

// Settable is implemented by types whose value can be updated via set().
type Settable interface {
	Set(value cty.Value) (cty.Value, error)
}

// Gettable is implemented by types whose current value can be read via get().
type Gettable interface {
	Get(defaultValue cty.Value) cty.Value
}

// Incrementable is implemented by types that support numeric increment via increment().
type Incrementable interface {
	Increment(delta cty.Value) (cty.Value, error)
}

// LabeledGettable is implemented by types that support labeled get().
type LabeledGettable interface {
	GetWithLabels(labels cty.Value) cty.Value
}

// LabeledSettable is implemented by types that support labeled set().
type LabeledSettable interface {
	SetWithLabels(value cty.Value, labels cty.Value) (cty.Value, error)
}

// LabeledIncrementable is implemented by types that support labeled increment().
type LabeledIncrementable interface {
	IncrementWithLabels(delta cty.Value, labels cty.Value) (cty.Value, error)
}

// Observable is implemented by types that support observe() (histograms).
type Observable interface {
	Observe(value cty.Value) (cty.Value, error)
}

// LabeledObservable is implemented by types that support labeled observe().
type LabeledObservable interface {
	ObserveWithLabels(value cty.Value, labels cty.Value) (cty.Value, error)
}

// --- Variable ---

// Variable is a mutable, goroutine-safe value container.
type Variable struct {
	mu    sync.RWMutex
	value cty.Value
}

func NewVariable(initial cty.Value) *Variable {
	return &Variable{value: initial}
}

// Get returns the current value, or defaultValue if the variable is null.
func (v *Variable) Get(defaultValue cty.Value) cty.Value {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.value.IsNull() {
		return defaultValue
	}
	return v.value
}

// Set updates the value and returns it.
func (v *Variable) Set(value cty.Value) (cty.Value, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.value = value
	return value, nil
}

// Increment adds delta to the current numeric value and returns the new value.
// Both the current value and delta must be cty.Number.
func (v *Variable) Increment(delta cty.Value) (cty.Value, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.value.IsNull() || v.value.Type() != cty.Number {
		return cty.NilVal, fmt.Errorf("increment: current value is not a number, got %s", v.value.Type().FriendlyName())
	}
	if delta.Type() != cty.Number {
		return cty.NilVal, fmt.Errorf("increment: delta is not a number, got %s", delta.Type().FriendlyName())
	}
	sum := new(big.Float).Add(v.value.AsBigFloat(), delta.AsBigFloat())
	newVal := cty.NumberVal(sum)
	v.value = newVal
	return newVal, nil
}

// --- Capsule type ---

var VariableCapsuleType = cty.CapsuleWithOps("variable", reflect.TypeOf((*Variable)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("variable(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "Variable"
	},
})

func NewVariableCapsule(v *Variable) cty.Value {
	return cty.CapsuleVal(VariableCapsuleType, v)
}

func GetVariableFromCapsule(val cty.Value) (*Variable, error) {
	if val.Type() != VariableCapsuleType {
		return nil, fmt.Errorf("expected Variable capsule, got %s", val.Type().FriendlyName())
	}
	encapsulated := val.EncapsulatedValue()
	variable, ok := encapsulated.(*Variable)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a Variable, got %T", encapsulated)
	}
	return variable, nil
}

// --- Block handler ---

type VariableBlockHandler struct {
	BlockHandlerBase
	variables map[string]*Variable
}

func NewVariableBlockHandler() *VariableBlockHandler {
	return &VariableBlockHandler{
		variables: make(map[string]*Variable),
	}
}

func (h *VariableBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "var." + block.Labels[0], nil
}

func (h *VariableBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	name := block.Labels[0]
	if _, exists := h.variables[name]; exists {
		return hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Duplicate variable",
			Detail:   fmt.Sprintf("Variable %q is already defined", name),
			Subject:  block.DefRange.Ptr(),
		}}
	}
	h.variables[name] = NewVariable(cty.NullVal(cty.DynamicPseudoType))
	return nil
}

func (h *VariableBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	for name, v := range h.variables {
		config.CtyVarMap[name] = NewVariableCapsule(v)
	}
	if len(config.CtyVarMap) > 0 {
		config.Constants["var"] = cty.ObjectVal(config.CtyVarMap)
	}
	return nil
}

func (h *VariableBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	name := block.Labels[0]
	v := h.variables[name]

	// Parse optional value attribute
	attrs, diags := block.Body.JustAttributes()
	if diags.HasErrors() {
		return diags
	}

	valueAttr, hasValue := attrs["value"]
	if !hasValue {
		return nil
	}

	val, valDiags := valueAttr.Expr.Value(config.evalCtx)
	diags = diags.Extend(valDiags)
	if diags.HasErrors() {
		return diags
	}

	_, err := v.Set(val)
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to set variable initial value",
			Detail:   err.Error(),
			Subject:  block.DefRange.Ptr(),
		})
	}

	return diags
}

// --- Functions ---

func GetVariableFunctions() map[string]function.Function {
	return map[string]function.Function{
		"get":       makeGetFunction(),
		"set":       makeSetFunction(),
		"increment": makeIncrementFunction(),
		"observe":   makeObserveFunction(),
	}
}

func makeGetFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{
			Name: "default_value",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			g, err := extractGettable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			defaultVal := cty.NullVal(cty.DynamicPseudoType)
			if len(args) > 1 {
				defaultVal = args[1]
			}
			return g.Get(defaultVal), nil
		},
	})
}

func makeSetFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
			{Name: "value", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "labels", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			if len(args) > 2 {
				m, err := extractMetric(args[0])
				if err != nil {
					return cty.NilVal, err
				}
				ls, ok := m.(LabeledSettable)
				if !ok {
					return cty.NilVal, fmt.Errorf("set: metric does not support labeled set()")
				}
				return ls.SetWithLabels(args[1], args[2])
			}
			s, err := extractSettable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return s.Set(args[1])
		},
	})
}

func makeIncrementFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
			{Name: "delta", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "labels", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			if len(args) > 2 {
				m, err := extractMetric(args[0])
				if err != nil {
					return cty.NilVal, err
				}
				li, ok := m.(LabeledIncrementable)
				if !ok {
					return cty.NilVal, fmt.Errorf("increment: metric does not support labeled increment()")
				}
				return li.IncrementWithLabels(args[1], args[2])
			}
			i, err := extractIncrementable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return i.Increment(args[1])
		},
	})
}

func makeObserveFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
			{Name: "value", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "labels", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			m, err := extractMetric(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			if len(args) > 2 {
				lo, ok := m.(LabeledObservable)
				if !ok {
					return cty.NilVal, fmt.Errorf("observe: metric does not support labeled observe()")
				}
				return lo.ObserveWithLabels(args[1], args[2])
			}
			o, ok := m.(Observable)
			if !ok {
				return cty.NilVal, fmt.Errorf("observe: metric does not support observe()")
			}
			return o.Observe(args[1])
		},
	})
}

func extractMetric(val cty.Value) (MetricValue, error) {
	if val.Type() != MetricCapsuleType {
		return nil, fmt.Errorf("expected a metric, got %s", val.Type().FriendlyName())
	}
	return GetMetricFromCapsule(val)
}

func extractGettable(val cty.Value) (Gettable, error) {
	if val.Type() == VariableCapsuleType {
		return GetVariableFromCapsule(val)
	}
	if val.Type() == MetricCapsuleType {
		m, err := GetMetricFromCapsule(val)
		if err != nil {
			return nil, err
		}
		if g, ok := m.(Gettable); ok {
			return g, nil
		}
		return nil, fmt.Errorf("get: metric does not support get() (histograms are not gettable)")
	}
	return nil, fmt.Errorf("get: argument does not support get() (got %s)", val.Type().FriendlyName())
}

func extractSettable(val cty.Value) (Settable, error) {
	if val.Type() == VariableCapsuleType {
		return GetVariableFromCapsule(val)
	}
	if val.Type() == MetricCapsuleType {
		m, err := GetMetricFromCapsule(val)
		if err != nil {
			return nil, err
		}
		if s, ok := m.(Settable); ok {
			return s, nil
		}
		return nil, fmt.Errorf("set: metric does not support set() (counters and histograms are not settable)")
	}
	return nil, fmt.Errorf("set: argument does not support set() (got %s)", val.Type().FriendlyName())
}

func extractIncrementable(val cty.Value) (Incrementable, error) {
	if val.Type() == VariableCapsuleType {
		return GetVariableFromCapsule(val)
	}
	if val.Type() == MetricCapsuleType {
		m, err := GetMetricFromCapsule(val)
		if err != nil {
			return nil, err
		}
		if i, ok := m.(Incrementable); ok {
			return i, nil
		}
		return nil, fmt.Errorf("increment: metric does not support increment() (histograms are not incrementable)")
	}
	return nil, fmt.Errorf("increment: argument does not support increment() (got %s)", val.Type().FriendlyName())
}
