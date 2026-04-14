package types

import (
	"context"
	"fmt"
	"math/big"
	"reflect"
	"sync"

	richcty "github.com/tsarna/rich-cty-types"

	"github.com/zclconf/go-cty/cty"
)

// Variable is a mutable, goroutine-safe value container.
type Variable struct {
	mu sync.RWMutex
	richcty.WatchableMixin
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

func (v *Variable) SetNullable(b bool)   { v.nullable = b }
func (v *Variable) SetTypeName(s string) { v.typeName = s }

// Get returns the current value, or the default (args[0]) if null. Implements richcty.Gettable.
func (v *Variable) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
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
// sets the value to null. Implements richcty.Settable. If the variable has a type
// constraint, non-null values must match it.
func (v *Variable) Set(ctx context.Context, args []cty.Value) (cty.Value, error) {
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
	old := v.value
	v.value = value
	v.mu.Unlock()
	v.NotifyAll(ctx, old, value)
	return value, nil
}

// Increment adds args[0] (delta) to the current numeric value and returns the new value.
// Both the current value and delta must be cty.Number. Implements richcty.Incrementable.
func (v *Variable) Increment(ctx context.Context, args []cty.Value) (cty.Value, error) {
	delta := args[0]
	v.mu.Lock()
	if v.value.IsNull() || v.value.Type() != cty.Number {
		v.mu.Unlock()
		return cty.NilVal, fmt.Errorf("increment: current value is not a number, got %s", v.value.Type().FriendlyName())
	}
	if delta.Type() != cty.Number {
		v.mu.Unlock()
		return cty.NilVal, fmt.Errorf("increment: delta is not a number, got %s", delta.Type().FriendlyName())
	}
	sum := new(big.Float).Add(v.value.AsBigFloat(), delta.AsBigFloat())
	newVal := cty.NumberVal(sum)
	old := v.value
	v.value = newVal
	v.mu.Unlock()
	v.NotifyAll(ctx, old, newVal)
	return newVal, nil
}

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
