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

// TypeConstraint enforces a type on a variable's value. It is satisfied
// structurally by functy's TypeConstraint (functy.TypeConstraint), so the config
// layer can hand a resolved functy type constraint to a Variable without this
// package importing functy. Coerce converts/validates a value (returning the
// value to store, or an error); Cty exposes the underlying type; String names it.
type TypeConstraint interface {
	Coerce(cty.Value) (cty.Value, error)
	Cty() cty.Type
	String() string
}

// Variable is a mutable, goroutine-safe value container.
type Variable struct {
	mu sync.RWMutex
	richcty.WatchableMixin
	value      cty.Value
	constraint TypeConstraint // nil means untyped; if set, enforced on Set()
	nullable   bool           // if false, null values are rejected by Set()
}

func NewVariable(initial cty.Value) *Variable {
	return &Variable{value: initial, nullable: true}
}

// NewTypedVariable creates a Variable that enforces values match the given type
// friendly name (e.g. "number", "string", "bool") by exact-name equality. An
// empty typeName means untyped. Prefer SetConstraint with a resolved
// TypeConstraint for the full type grammar; this constructor preserves the
// legacy string-name behavior.
func NewTypedVariable(initial cty.Value, typeName string) *Variable {
	v := &Variable{value: initial, nullable: true}
	if typeName != "" {
		v.constraint = friendlyNameConstraint{typeName}
	}
	return v
}

func (v *Variable) SetNullable(b bool)             { v.nullable = b }
func (v *Variable) SetConstraint(c TypeConstraint) { v.constraint = c }

// friendlyNameConstraint enforces a value's type by exact friendly-name equality,
// preserving the legacy behavior of NewTypedVariable / the old string typeName.
type friendlyNameConstraint struct{ name string }

func (c friendlyNameConstraint) Coerce(v cty.Value) (cty.Value, error) {
	if v.Type().FriendlyName() != c.name {
		return cty.NilVal, fmt.Errorf("type mismatch: variable expects %s, got %s", c.name, v.Type().FriendlyName())
	}
	return v, nil
}
func (c friendlyNameConstraint) Cty() cty.Type  { return cty.DynamicPseudoType }
func (c friendlyNameConstraint) String() string { return c.name }

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
	} else if v.constraint != nil {
		coerced, err := v.constraint.Coerce(value)
		if err != nil {
			return cty.NilVal, err
		}
		value = coerced
	}
	v.mu.Lock()
	old := v.value
	v.value = value
	v.mu.Unlock()
	v.NotifyAll(ctx, v, old, value)
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
	v.NotifyAll(ctx, v, old, newVal)
	return newVal, nil
}

// Toggle flips a boolean value and returns the new value. Implements
// richcty.Toggleable. The current value must be a non-null bool; extra args
// are ignored.
func (v *Variable) Toggle(ctx context.Context, _ []cty.Value) (cty.Value, error) {
	v.mu.Lock()
	if v.value.IsNull() || v.value.Type() != cty.Bool {
		v.mu.Unlock()
		return cty.NilVal, fmt.Errorf("toggle: current value is not a bool, got %s", v.value.Type().FriendlyName())
	}
	newVal := cty.BoolVal(!v.value.True())
	old := v.value
	v.value = newVal
	v.mu.Unlock()
	v.NotifyAll(ctx, v, old, newVal)
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
