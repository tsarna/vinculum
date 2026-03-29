package hclutil

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// IsExpressionProvided checks if an HCL expression was actually provided in the configuration.
// HCL creates empty expression objects for optional fields that aren't specified,
// but empty expressions have Start.Byte == End.Byte (zero-length range).
// Real expressions have End.Byte > Start.Byte (non-zero length range).
func IsExpressionProvided(expr hcl.Expression) bool {
	return expr != nil && expr.Range().End.Byte > expr.Range().Start.Byte
}

// IsConstantExpression checks if an expression is a constant (evaluatable with nil context).
// Returns the value and true if constant, or cty.NilVal and false otherwise.
func IsConstantExpression(expr hcl.Expression) (cty.Value, bool) {
	val, diags := expr.Value(nil)
	if diags.HasErrors() {
		return cty.NilVal, false
	}
	return val, true
}
