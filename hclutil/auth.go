package hclutil

import (
	"context"

	"github.com/zclconf/go-cty/cty"
)

type authContextKey struct{}

// WithAuthValue stores a ctx.auth cty value in the Go context. Called by auth
// middleware after successful authentication.
func WithAuthValue(ctx context.Context, val cty.Value) context.Context {
	return context.WithValue(ctx, authContextKey{}, val)
}

// AuthValueFromContext retrieves the ctx.auth value stored by WithAuthValue.
// Returns cty.NullVal(cty.DynamicPseudoType) if no auth value is present.
func AuthValueFromContext(ctx context.Context) cty.Value {
	if v, ok := ctx.Value(authContextKey{}).(cty.Value); ok {
		return v
	}
	return cty.NullVal(cty.DynamicPseudoType)
}
