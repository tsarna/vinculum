package config

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// ContextCapsuleType is a cty capsule type for wrapping Context instances
var ContextCapsuleType = cty.CapsuleWithOps("_context", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("_ctx(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "_ctx"
	},
})

// NewEventBusCapsule creates a new cty capsule value wrapping an EventBus
func NewContextCapsule(ctx context.Context) cty.Value {
	return cty.CapsuleVal(ContextCapsuleType, &ctx)
}

// GetContextFromCapsule extracts an Context from a cty capsule value
func GetContextFromCapsule(val cty.Value) (context.Context, hcl.Diagnostics) {
	if val.Type() != ContextCapsuleType {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Expected Context capsule",
				Detail:   fmt.Sprintf("expected Context capsule, got %s", val.Type().FriendlyName()),
			},
		}
	}

	encapsulated := val.EncapsulatedValue()
	ctx, ok := encapsulated.(*context.Context)
	if !ok {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Encapsulated value is not a Context",
				Detail:   fmt.Sprintf("encapsulated value is not a Context, got %T", encapsulated),
			},
		}
	}
	return *ctx, nil
}

type ContextObjectBuilder struct {
	ctx        context.Context
	attributes map[string]cty.Value
	functions  map[string]function.Function
}

func NewContext(ctx context.Context) *ContextObjectBuilder {
	return &ContextObjectBuilder{
		ctx:        ctx,
		attributes: make(map[string]cty.Value),
	}
}

func (b *ContextObjectBuilder) WithAttribute(name string, value cty.Value) *ContextObjectBuilder {
	b.attributes[name] = value

	return b
}

func (b *ContextObjectBuilder) WithInt64Attribute(name string, value int64) *ContextObjectBuilder {
	b.attributes[name] = cty.NumberIntVal(value)

	return b
}

func (b *ContextObjectBuilder) WithUInt64Attribute(name string, value uint64) *ContextObjectBuilder {
	b.attributes[name] = cty.NumberUIntVal(value)

	return b
}

func (b *ContextObjectBuilder) WithStringAttribute(name string, value string) *ContextObjectBuilder {
	b.attributes[name] = cty.StringVal(value)

	return b
}

func (b *ContextObjectBuilder) WithFunction(name string, function function.Function) *ContextObjectBuilder {
	b.functions[name] = function

	return b
}

func (b *ContextObjectBuilder) WithFunctions(functions map[string]function.Function) *ContextObjectBuilder {
	if b.functions == nil {
		b.functions = functions
	} else {
		for name, function := range functions {
			b.functions[name] = function
		}
	}

	return b
}

func (b *ContextObjectBuilder) Build() (cty.Value, hcl.Diagnostics) {
	b.attributes["_ctx"] = NewContextCapsule(b.ctx)

	return cty.ObjectVal(b.attributes), nil
}

func (b *ContextObjectBuilder) BuildEvalContext(parent *hcl.EvalContext) (*hcl.EvalContext, hcl.Diagnostics) {
	evalCtx := parent.NewChild()
	ctxObj, diags := b.Build()
	if diags.HasErrors() {
		return nil, diags
	}

	evalCtx.Variables = make(map[string]cty.Value)
	evalCtx.Variables["ctx"] = ctxObj
	evalCtx.Functions = b.functions

	return evalCtx, diags
}

func GetContextFromObject(obj cty.Value) (context.Context, hcl.Diagnostics) {
	if !obj.Type().IsObjectType() {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Expected object",
				Detail:   fmt.Sprintf("expected object, got %s", obj.Type().FriendlyName()),
			},
		}
	}

	return GetContextFromCapsule(obj.GetAttr("_ctx"))
}
