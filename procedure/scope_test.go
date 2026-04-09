package procedure

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zclconf/go-cty/cty"
)

func TestScope_GetSet(t *testing.T) {
	s := NewScope(nil)
	s.Set("x", cty.NumberIntVal(42))

	val, ok := s.Get("x")
	assert.True(t, ok)
	assert.True(t, val.RawEquals(cty.NumberIntVal(42)))

	_, ok = s.Get("missing")
	assert.False(t, ok)
}

func TestScope_ParentLookup(t *testing.T) {
	parent := NewScope(nil)
	parent.Set("x", cty.NumberIntVal(1))

	child := NewScope(parent)
	child.Set("y", cty.NumberIntVal(2))

	// Child can see parent's variable
	val, ok := child.Get("x")
	assert.True(t, ok)
	assert.True(t, val.RawEquals(cty.NumberIntVal(1)))

	// Parent cannot see child's variable
	_, ok = parent.Get("y")
	assert.False(t, ok)
}

func TestScope_UpdateOuter(t *testing.T) {
	parent := NewScope(nil)
	parent.Set("x", cty.NumberIntVal(1))

	child := NewScope(parent)
	child.Set("x", cty.NumberIntVal(99))

	// Parent should be updated
	val, ok := parent.Get("x")
	assert.True(t, ok)
	assert.True(t, val.RawEquals(cty.NumberIntVal(99)))

	// Child sees updated value too
	val, ok = child.Get("x")
	assert.True(t, ok)
	assert.True(t, val.RawEquals(cty.NumberIntVal(99)))
}

func TestScope_Discard(t *testing.T) {
	s := NewScope(nil)
	s.Set("_", cty.StringVal("ignored"))
	s.Set("_foo", cty.StringVal("also ignored"))

	_, ok := s.Get("_")
	assert.False(t, ok, "discard name _ should never be stored")
	_, ok = s.Get("_foo")
	assert.False(t, ok, "discard name _foo should never be stored")
}

func TestScope_ToMap(t *testing.T) {
	parent := NewScope(nil)
	parent.Set("a", cty.NumberIntVal(1))
	parent.Set("b", cty.NumberIntVal(2))

	child := NewScope(parent)
	child.Set("b", cty.NumberIntVal(20)) // updates parent
	child.Set("c", cty.NumberIntVal(3))

	m := child.ToMap()
	assert.True(t, m["a"].RawEquals(cty.NumberIntVal(1)))
	assert.True(t, m["b"].RawEquals(cty.NumberIntVal(20)))
	assert.True(t, m["c"].RawEquals(cty.NumberIntVal(3)))
	assert.Len(t, m, 3)
}
