package hclutil

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zclconf/go-cty/cty"
)

func TestEnvObjectFromKeepsRealNames(t *testing.T) {
	env := EnvObjectFrom([]string{
		"A_B=real",
		"A.B=impostor",
		"_=/usr/bin/env",
		"\xff=invalid-bytes",
		"café=crème",
		"EMPTY=",
		"WITH_EQUALS=a=b",
		"=C:=C:\\dir",
		"=empty-name",
		"NOSEP",
		"",
	})

	require.True(t, env.Type().IsMapType())
	assert.Equal(t, map[string]cty.Value{
		"A_B":         cty.StringVal("real"),
		"A.B":         cty.StringVal("impostor"),
		"_":           cty.StringVal("/usr/bin/env"),
		"\xff":        cty.StringVal("invalid-bytes"),
		"café":        cty.StringVal("crème"),
		"EMPTY":       cty.StringVal(""),
		"WITH_EQUALS": cty.StringVal("a=b"),
		"=C:":         cty.StringVal("C:\\dir"),
		"":            cty.StringVal("empty-name"),
	}, env.AsValueMap())
}

func TestEnvObjectFromFirstDuplicateWins(t *testing.T) {
	env := EnvObjectFrom([]string{"X=first", "X=second"})
	assert.Equal(t, cty.StringVal("first"), env.Index(cty.StringVal("X")))
}

func TestEnvObjectFromFirstOfEquivalentUnicodeNamesWins(t *testing.T) {
	for range 20 {
		env := EnvObjectFrom([]string{"caf\u00e9=composed", "cafe\u0301=decomposed"})
		assert.Equal(t, map[string]cty.Value{"caf\u00e9": cty.StringVal("composed")}, env.AsValueMap())
	}
}

func TestEnvObjectFromEmpty(t *testing.T) {
	assert.True(t, EnvObjectFrom(nil).RawEquals(cty.MapValEmpty(cty.String)))
	assert.True(t, EnvObjectFrom([]string{"NOSEP"}).RawEquals(cty.MapValEmpty(cty.String)))
}

func TestEnvObjectInExpressions(t *testing.T) {
	ctx := &hcl.EvalContext{Variables: map[string]cty.Value{
		"env": EnvObjectFrom([]string{"HOME=/home/me", "A.B=dotted", "café=crème"}),
	}}
	eval := func(src string) (cty.Value, hcl.Diagnostics) {
		expr, diags := hclsyntax.ParseExpression([]byte(src), "test", hcl.InitialPos)
		require.False(t, diags.HasErrors(), diags.Error())
		return expr.Value(ctx)
	}

	for src, want := range map[string]string{
		`env.HOME`:    "/home/me",
		`env["A.B"]`:  "dotted",
		`env.café`:    "crème",
		`env["HOME"]`: "/home/me",
	} {
		v, diags := eval(src)
		require.False(t, diags.HasErrors(), "%s: %s", src, diags.Error())
		assert.Equal(t, want, v.AsString(), src)
	}

	_, diags := eval(`env.UNSET`)
	assert.True(t, diags.HasErrors())
}
