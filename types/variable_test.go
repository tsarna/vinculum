package types

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

var bg = context.Background()

func TestVariableGetSetIncrement(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Get on null returns default
	result, err := v.Get(bg, []cty.Value{cty.NumberIntVal(42)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(42)))

	// Get on null without default returns null
	result, err = v.Get(bg, []cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())

	// Set
	newVal, err := v.Set(bg, []cty.Value{cty.NumberIntVal(10)})
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(10)))

	// Get after Set
	result, err = v.Get(bg, []cty.Value{cty.NumberIntVal(99)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(10)))

	// Increment
	newVal, err = v.Increment(bg, []cty.Value{cty.NumberIntVal(5)})
	assert.NoError(t, err)
	assert.True(t, newVal.RawEquals(cty.NumberIntVal(15)))

	// Get after Increment
	result, err = v.Get(bg, []cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(15)))
}

func TestVariableTypedSet_Valid(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")

	result, err := v.Set(bg, []cty.Value{cty.NumberIntVal(7)})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.NumberIntVal(7)))
}

func TestVariableTypedSet_Invalid(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")

	_, err := v.Set(bg, []cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "number")
	assert.Contains(t, err.Error(), "string")
}

func TestVariableNonNullable_SetNull(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(5))
	v.nullable = false

	_, err := v.Set(bg, []cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
}

func TestVariableNonNullable_SetNoArgs(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(5))
	v.nullable = false

	_, err := v.Set(bg, []cty.Value{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "nullable")
}

func TestVariableNonNullable_SetValue(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))
	v.nullable = false

	result, err := v.Set(bg, []cty.Value{cty.StringVal("ok")})
	assert.NoError(t, err)
	assert.True(t, result.RawEquals(cty.StringVal("ok")))
}

func TestVariableTypedSet_Null(t *testing.T) {
	v := NewTypedVariable(cty.NumberIntVal(5), "number")

	// Setting null is always allowed even on typed variables
	result, err := v.Set(bg, []cty.Value{cty.NullVal(cty.DynamicPseudoType)})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())
}

func TestVariableTypedSet_NoArgs(t *testing.T) {
	v := NewTypedVariable(cty.NumberIntVal(5), "number")

	// Set with no args sets to null (allowed)
	result, err := v.Set(bg, []cty.Value{})
	assert.NoError(t, err)
	assert.True(t, result.IsNull())
}

func TestVariableIncrementErrors(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))

	// Increment on null fails
	_, err := v.Increment(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)

	// Increment with non-number delta fails
	_, _ = v.Set(bg, []cty.Value{cty.NumberIntVal(10)})
	_, err = v.Increment(bg, []cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)

	// Set to string then increment fails
	_, _ = v.Set(bg, []cty.Value{cty.StringVal("hello")})
	_, err = v.Increment(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)
}

// --- richcty.Watchable tests ---

func TestVariableWatch_SetNotifiesOldAndNew(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(1))
	w := &testWatcher{}
	v.Watch(w)

	_, err := v.Set(bg, []cty.Value{cty.NumberIntVal(2)})
	assert.NoError(t, err)

	assert.Equal(t, 1, w.count())
	c := w.get(0)
	assert.True(t, c.old.RawEquals(cty.NumberIntVal(1)))
	assert.True(t, c.new.RawEquals(cty.NumberIntVal(2)))
}

func TestVariableWatch_SetEqualValueStillNotifies(t *testing.T) {
	// Equal-value sets must still fire (watchdog heartbeat semantics).
	v := NewVariable(cty.NumberIntVal(42))
	w := &testWatcher{}
	v.Watch(w)

	_, _ = v.Set(bg, []cty.Value{cty.NumberIntVal(42)})
	_, _ = v.Set(bg, []cty.Value{cty.NumberIntVal(42)})
	assert.Equal(t, 2, w.count())
}

func TestVariableWatch_SetErrorNoNotify(t *testing.T) {
	v := NewTypedVariable(cty.NullVal(cty.DynamicPseudoType), "number")
	w := &testWatcher{}
	v.Watch(w)

	_, err := v.Set(bg, []cty.Value{cty.StringVal("bad")})
	assert.Error(t, err)
	assert.Equal(t, 0, w.count(), "failed Set must not notify watchers")
}

func TestVariableWatch_IncrementNotifiesOldAndNew(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(10))
	w := &testWatcher{}
	v.Watch(w)

	_, err := v.Increment(bg, []cty.Value{cty.NumberIntVal(5)})
	assert.NoError(t, err)

	assert.Equal(t, 1, w.count())
	c := w.get(0)
	assert.True(t, c.old.RawEquals(cty.NumberIntVal(10)))
	assert.True(t, c.new.RawEquals(cty.NumberIntVal(15)))
}

func TestVariableWatch_IncrementErrorNoNotify(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType)) // null, not a number
	w := &testWatcher{}
	v.Watch(w)

	_, err := v.Increment(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Error(t, err)
	assert.Equal(t, 0, w.count(), "failed Increment must not notify watchers")
}

func TestVariableWatch_UnwatchStopsNotifications(t *testing.T) {
	v := NewVariable(cty.NumberIntVal(0))
	w := &testWatcher{}
	v.Watch(w)

	_, _ = v.Set(bg, []cty.Value{cty.NumberIntVal(1)})
	assert.Equal(t, 1, w.count())

	v.Unwatch(w)
	_, _ = v.Set(bg, []cty.Value{cty.NumberIntVal(2)})
	assert.Equal(t, 1, w.count(), "no notification after Unwatch")
}

func TestVariableWatch_ContextPropagated(t *testing.T) {
	v := NewVariable(cty.NullVal(cty.DynamicPseudoType))
	w := &testWatcher{}
	v.Watch(w)

	type ctxKey struct{}
	ctx := context.WithValue(bg, ctxKey{}, "sentinel")
	_, _ = v.Set(ctx, []cty.Value{cty.True})

	assert.Equal(t, "sentinel", w.get(0).ctx.Value(ctxKey{}))
}
