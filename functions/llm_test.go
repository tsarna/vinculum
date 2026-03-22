package functions

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestLLMWrap(t *testing.T) {
	result, err := LLMWrapFunc.Call([]cty.Value{cty.StringVal("hello world")})
	require.NoError(t, err)
	assert.Equal(t, "<user_input>\nhello world\n</user_input>", result.AsString())
}

func TestLLMWrapEmpty(t *testing.T) {
	result, err := LLMWrapFunc.Call([]cty.Value{cty.StringVal("")})
	require.NoError(t, err)
	assert.Equal(t, "<user_input>\n\n</user_input>", result.AsString())
}
