package functions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFunctyStdlibNewFunctions verifies the functions gained by adopting functy's
// stdlib (typekind, assert, can) are available in the VCL eval context.
func TestFunctyStdlibNewFunctions(t *testing.T) {
	c, err := buildVCL(t, []byte(`const {
		k  = typekind(["a", "b"])
		ok = assert(true)
		c1 = can(upper("x"))
		c2 = can(jsondecode("{not valid json"))
	}`))
	require.NoError(t, err)
	assert.Equal(t, "tuple", c.Constants["k"].AsString())
	assert.True(t, c.Constants["ok"].True())
	assert.True(t, c.Constants["c1"].True())
	assert.False(t, c.Constants["c2"].True(), "can() reports a failing expression as false")
}

// TestFunctyAssertFailsBuild verifies assert(false, msg) surfaces as an error with
// the message.
func TestFunctyAssertFailsBuild(t *testing.T) {
	_, err := buildVCL(t, []byte(`const { x = assert(false, "boom") }`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}
