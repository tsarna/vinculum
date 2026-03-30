package ctyutil

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

// testThing is a simple Go type used to make a test capsule.
type testThing struct{ Val string }

var testCapsuleType = cty.CapsuleWithOps("testThing", reflect.TypeOf(testThing{}), &cty.CapsuleOps{})

func newTestCapsule(val string) cty.Value {
	return cty.CapsuleVal(testCapsuleType, &testThing{Val: val})
}

func TestGetCapsuleFromValue_DirectCapsule(t *testing.T) {
	cap := newTestCapsule("hello")
	enc, err := GetCapsuleFromValue(cap)
	require.NoError(t, err)
	thing, ok := enc.(*testThing)
	require.True(t, ok)
	assert.Equal(t, "hello", thing.Val)
}

func TestGetCapsuleFromValue_ObjectWithCapsule(t *testing.T) {
	cap := newTestCapsule("world")
	obj := cty.ObjectVal(map[string]cty.Value{
		"name":     cty.StringVal("test"),
		"_capsule": cap,
	})
	enc, err := GetCapsuleFromValue(obj)
	require.NoError(t, err)
	thing, ok := enc.(*testThing)
	require.True(t, ok)
	assert.Equal(t, "world", thing.Val)
}

func TestGetCapsuleFromValue_ObjectWithoutCapsule_Error(t *testing.T) {
	obj := cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("test"),
	})
	_, err := GetCapsuleFromValue(obj)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "_capsule")
}

func TestGetCapsuleFromValue_String_Error(t *testing.T) {
	_, err := GetCapsuleFromValue(cty.StringVal("not a capsule"))
	assert.Error(t, err)
}

func TestGetCapsuleFromValue_Number_Error(t *testing.T) {
	_, err := GetCapsuleFromValue(cty.NumberIntVal(42))
	assert.Error(t, err)
}
