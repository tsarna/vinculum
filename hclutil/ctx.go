package hclutil

import (
	"context"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/vinculum/ctyutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"go.opentelemetry.io/otel/trace"
)

// EvalContextBuilder builds an HCL eval context with a "ctx" variable containing
// a cty object that wraps a Go context and any additional attributes or functions.
type EvalContextBuilder struct {
	b         *ctyutil.ContextObjectBuilder
	Functions map[string]function.Function
}

// NewEvalContext creates a new EvalContextBuilder wrapping the given context.
func NewEvalContext(ctx context.Context) *EvalContextBuilder {
	return &EvalContextBuilder{b: ctyutil.NewContextObject(ctx)}
}

func (e *EvalContextBuilder) WithAttribute(name string, value cty.Value) *EvalContextBuilder {
	e.b.WithAttribute(name, value)
	return e
}

func (e *EvalContextBuilder) WithInt64Attribute(name string, value int64) *EvalContextBuilder {
	e.b.WithInt64Attribute(name, value)
	return e
}

func (e *EvalContextBuilder) WithUInt64Attribute(name string, value uint64) *EvalContextBuilder {
	e.b.WithUInt64Attribute(name, value)
	return e
}

func (e *EvalContextBuilder) WithStringAttribute(name string, value string) *EvalContextBuilder {
	e.b.WithStringAttribute(name, value)
	return e
}

func (e *EvalContextBuilder) WithFunction(name string, fn function.Function) *EvalContextBuilder {
	if e.Functions == nil {
		e.Functions = make(map[string]function.Function)
	}
	e.Functions[name] = fn

	return e
}

func (e *EvalContextBuilder) WithFunctions(functions map[string]function.Function) *EvalContextBuilder {
	if e.Functions == nil {
		e.Functions = functions
	} else {
		for name, fn := range functions {
			e.Functions[name] = fn
		}
	}
	return e
}

// BuildEvalContext creates a child HCL eval context with the "ctx" variable set.
// It automatically includes ctx.auth from the Go context (set by auth middleware),
// and ctx.trace_id / ctx.span_id from any active OTel span.
func (e *EvalContextBuilder) BuildEvalContext(parent *hcl.EvalContext) (*hcl.EvalContext, error) {
	// Auto-include ctx.auth from Go context (populated by auth middleware, or null).
	e.b.WithAttribute("auth", AuthValueFromContext(e.b.Ctx))

	// Auto-include ctx.trace_id / ctx.span_id from the active OTel span.
	// Prefer a locally-started span; fall back to the remote span context
	// extracted from incoming headers (e.g. traceparent) so that trace_id is
	// populated even when no client "otlp" is configured and the tracer is NOOP.
	sc := trace.SpanFromContext(e.b.Ctx).SpanContext()
	if !sc.IsValid() {
		sc = trace.SpanContextFromContext(e.b.Ctx)
	}
	if sc.IsValid() {
		e.b.WithAttribute("trace_id", cty.StringVal(sc.TraceID().String()))
		e.b.WithAttribute("span_id", cty.StringVal(sc.SpanID().String()))
	} else {
		e.b.WithAttribute("trace_id", cty.StringVal(""))
		e.b.WithAttribute("span_id", cty.StringVal(""))
	}

	ctxObj, err := e.b.Build()
	if err != nil {
		return nil, err
	}
	evalCtx := parent.NewChild()
	evalCtx.Variables = make(map[string]cty.Value)
	evalCtx.Variables["ctx"] = ctxObj
	evalCtx.Functions = e.Functions
	return evalCtx, nil
}
