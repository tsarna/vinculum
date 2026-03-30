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

// Gettable is implemented by types whose current value can be read via get().
// args contains all arguments after the "thing": typically args[0] is the
// default value, but implementors may interpret additional args freely.
type Gettable interface {
	Get(args []cty.Value) (cty.Value, error)
}

// Settable is implemented by types whose value can be updated via set().
// args contains all arguments after the "thing": args[0] is the value to set;
// implementors may interpret additional args (e.g. labels) freely.
type Settable interface {
	Set(args []cty.Value) (cty.Value, error)
}

// Incrementable is implemented by types that support numeric increment via increment().
// args contains all arguments after the "thing": args[0] is the delta;
// implementors may interpret additional args freely.
type Incrementable interface {
	Increment(args []cty.Value) (cty.Value, error)
}

// Observable is implemented by types that support observe() (histograms).
// args contains all arguments after the "thing": args[0] is the observed value;
// implementors may interpret additional args (e.g. labels) freely.
type Observable interface {
	Observe(args []cty.Value) (cty.Value, error)
}

// --- Variable ---

// Variable is a mutable, goroutine-safe value container.
type Variable struct {
	mu       sync.RWMutex
	value    cty.Value
	typeName string // empty means untyped; if set, enforced on Set()
	nullable bool   // if false, null values are rejected by Set()
}

func NewVariable(initial cty.Value) *Variable {
	return &Variable{value: initial, nullable: true}
}

// NewTypedVariable creates a Variable that enforces values match the given type
// friendly name (e.g. "number", "string", "bool"). An empty typeName means untyped.
func NewTypedVariable(initial cty.Value, typeName string) *Variable {
	return &Variable{value: initial, typeName: typeName, nullable: true}
}

// Get returns the current value, or the default (args[0]) if null. Implements Gettable.
func (v *Variable) Get(args []cty.Value) (cty.Value, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.value.IsNull() {
		if len(args) > 0 {
			return args[0], nil
		}
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return v.value, nil
}

// Set updates the value (args[0]) and returns it. If called with no arguments,
// sets the value to null. Implements Settable. If the variable has a type
// constraint, non-null values must match it.
func (v *Variable) Set(args []cty.Value) (cty.Value, error) {
	value := cty.NullVal(cty.DynamicPseudoType)
	if len(args) > 0 {
		value = args[0]
	}
	if value.IsNull() {
		if !v.nullable {
			return cty.NilVal, fmt.Errorf("variable is not nullable")
		}
	} else if v.typeName != "" {
		if value.Type().FriendlyName() != v.typeName {
			return cty.NilVal, fmt.Errorf("type mismatch: variable expects %s, got %s", v.typeName, value.Type().FriendlyName())
		}
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.value = value
	return value, nil
}

// Increment adds args[0] (delta) to the current numeric value and returns the new value.
// Both the current value and delta must be cty.Number. Implements Incrementable.
func (v *Variable) Increment(args []cty.Value) (cty.Value, error) {
	delta := args[0]
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

	// Parse optional attributes
	attrs, diags := block.Body.JustAttributes()
	if diags.HasErrors() {
		return diags
	}

	// Handle optional nullable constraint (default true)
	v.nullable = true
	if nullableAttr, hasNullable := attrs["nullable"]; hasNullable {
		nullableVal, nullableDiags := nullableAttr.Expr.Value(config.evalCtx)
		diags = diags.Extend(nullableDiags)
		if diags.HasErrors() {
			return diags
		}
		if nullableVal.Type() != cty.Bool {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid nullable value",
				Detail:   `The "nullable" attribute must be a bool`,
				Subject:  nullableAttr.Expr.StartRange().Ptr(),
			})
		}
		v.nullable = nullableVal.True()
	}

	// Handle optional type constraint
	if typeAttr, hasType := attrs["type"]; hasType {
		typeVal, typeDiags := typeAttr.Expr.Value(config.evalCtx)
		diags = diags.Extend(typeDiags)
		if diags.HasErrors() {
			return diags
		}
		if typeVal.Type() != cty.String {
			return diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid type constraint",
				Detail:   `The "type" attribute must be a string`,
				Subject:  typeAttr.Expr.StartRange().Ptr(),
			})
		}
		v.typeName = typeVal.AsString()
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

	_, err := v.Set([]cty.Value{val})
	if err != nil {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to set variable initial value",
			Detail:   err.Error(),
			Subject:  valueAttr.Expr.StartRange().Ptr(),
		})
	}

	return diags
}

func init() {
	RegisterFunctionPlugin("variable", func(_ *Config) map[string]function.Function {
		return GetVariableFunctions()
	})
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
			Name: "args",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			g, err := extractGettable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return g.Get(args[1:])
		},
	})
}

func makeSetFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			s, err := extractSettable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return s.Set(args[1:])
		},
	})
}

func makeIncrementFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
			{Name: "delta", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			i, err := extractIncrementable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return i.Increment(args[1:])
		},
	})
}

func makeObserveFunction() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "thing", Type: cty.DynamicPseudoType},
			{Name: "value", Type: cty.DynamicPseudoType},
		},
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			o, err := extractObservable(args[0])
			if err != nil {
				return cty.NilVal, err
			}
			return o.Observe(args[1:])
		},
	})
}

func extractGettable(val cty.Value) (Gettable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("get: argument is not gettable (got %s)", val.Type().FriendlyName())
	}
	g, ok := val.EncapsulatedValue().(Gettable)
	if !ok {
		return nil, fmt.Errorf("get: %s does not support get()", val.Type().FriendlyName())
	}
	return g, nil
}

func extractSettable(val cty.Value) (Settable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("set: argument is not settable (got %s)", val.Type().FriendlyName())
	}
	s, ok := val.EncapsulatedValue().(Settable)
	if !ok {
		return nil, fmt.Errorf("set: %s does not support set()", val.Type().FriendlyName())
	}
	return s, nil
}

func extractIncrementable(val cty.Value) (Incrementable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("increment: argument is not incrementable (got %s)", val.Type().FriendlyName())
	}
	i, ok := val.EncapsulatedValue().(Incrementable)
	if !ok {
		return nil, fmt.Errorf("increment: %s does not support increment()", val.Type().FriendlyName())
	}
	return i, nil
}

func extractObservable(val cty.Value) (Observable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("observe: argument is not observable (got %s)", val.Type().FriendlyName())
	}
	o, ok := val.EncapsulatedValue().(Observable)
	if !ok {
		return nil, fmt.Errorf("observe: %s does not support observe()", val.Type().FriendlyName())
	}
	return o, nil
}
