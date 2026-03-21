package config

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/internal/hclutil"
	"github.com/zclconf/go-cty/cty"
)

// ContextCapsuleType is a cty capsule type for wrapping Context instances
var ContextCapsuleType = hclutil.ContextCapsuleType

// NewContextCapsule creates a new cty capsule value wrapping a Context
func NewContextCapsule(ctx context.Context) cty.Value {
	return hclutil.NewContextCapsule(ctx)
}

// GetContextFromCapsule extracts a Context from a cty capsule value
func GetContextFromCapsule(val cty.Value) (context.Context, hcl.Diagnostics) {
	return hclutil.GetContextFromCapsule(val)
}

// ContextObjectBuilder builds a cty object for use as the "ctx" variable in HCL eval contexts.
type ContextObjectBuilder = hclutil.ContextObjectBuilder

// NewContext creates a new ContextObjectBuilder wrapping the given context.
func NewContext(ctx context.Context) *ContextObjectBuilder {
	return hclutil.NewContext(ctx)
}

// GetContextFromObject extracts a Go context from the "_ctx" attribute of a cty object.
func GetContextFromObject(obj cty.Value) (context.Context, hcl.Diagnostics) {
	return hclutil.GetContextFromObject(obj)
}
