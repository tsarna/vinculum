package config

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	vbxform "github.com/tsarna/vinculum-bus/transform"
	"github.com/tsarna/vinculum/pkg/vinculum/transform"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// MessageTransformWrapper wraps a MessageTransformFunc for use in cty capsules
type MessageTransformWrapper struct {
	Func vbxform.MessageTransformFunc
}

// MessageTransformCapsuleType is a cty capsule type for wrapping MessageTransformFunc instances
var MessageTransformCapsuleType = cty.CapsuleWithOps("transform", reflect.TypeOf((*MessageTransformWrapper)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("transform(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "MessageTransform"
	},
})

// NewMessageTransformCapsule creates a new cty capsule value wrapping a MessageTransformFunc
func NewMessageTransformCapsule(transformFunc vbxform.MessageTransformFunc) cty.Value {
	wrapper := &MessageTransformWrapper{Func: transformFunc}
	return cty.CapsuleVal(MessageTransformCapsuleType, wrapper)
}

// GetMessageTransformFromCapsule extracts a MessageTransformFunc from a cty capsule value
func GetMessageTransformFromCapsule(val cty.Value) (vbxform.MessageTransformFunc, error) {
	if val.Type() != MessageTransformCapsuleType {
		return nil, fmt.Errorf("expected Message Transform capsule, got %s", val.Type().FriendlyName())
	}

	encapsulated := val.EncapsulatedValue()
	wrapper, ok := encapsulated.(*MessageTransformWrapper)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a MessageTransformWrapper, got %T", encapsulated)
	}
	return wrapper.Func, nil
}

func (config *Config) GetMessageTransforms(expr hcl.Expression) (transforms []vbxform.MessageTransformFunc, diags hcl.Diagnostics) {
	evalCtx := config.getTransformExprEvalCtx()
	vals, diags := expr.Value(evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	return config.ctyToTransforms(expr, vals)
}

func (config *Config) ctyToTransforms(expr hcl.Expression, vals cty.Value) (transforms []vbxform.MessageTransformFunc, diags hcl.Diagnostics) {
	// Accept both a single transform or a list/tuple of transforms
	if vals.Type() == MessageTransformCapsuleType {
		// Single transformFunc, wrap in slice
		transformFunc, err := GetMessageTransformFromCapsule(vals)
		if err != nil {
			exprRange := expr.Range()
			diags = diags.Extend(hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to get message transformation from capsule",
					Detail:   err.Error(),
					Subject:  &exprRange,
				},
			})
			return nil, diags
		}
		return []vbxform.MessageTransformFunc{transformFunc}, diags
	} else if vals.Type().IsTupleType() || vals.Type().IsListType() {
		// handled below
	} else {
		exprRange := expr.Range()
		diags = diags.Extend(hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid type for message transforms",
				Detail:   fmt.Sprintf("Expected a transform or list/tuple of transforms, got %s", vals.Type().FriendlyName()),
				Subject:  &exprRange,
			},
		})
		return nil, diags
	}

	transforms = make([]vbxform.MessageTransformFunc, 0, vals.LengthInt())

	for _, val := range vals.AsValueSlice() {
		transformFunc, err := GetMessageTransformFromCapsule(val)
		if err != nil {
			exprRange := expr.Range()

			diags = diags.Extend(hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to get message transformation from capsule",
					Detail:   err.Error(),
					Subject:  &exprRange,
				},
			})
			continue
		}
		transforms = append(transforms, transformFunc)
	}

	return transforms, diags
}

func (config *Config) getTransformExprEvalCtx() *hcl.EvalContext {
	ctx := config.evalCtx.NewChild()
	ctx.Functions = map[string]function.Function{
		"add_topic_prefix":      AddTopicPrefixTransform,
		"chain":                 ChainTransformsTransform,
		"cty2go":                Cty2GoTransformFunc,
		"diff":                  DiffTransform,
		"drop_topic_pattern":    DropTopicPatternTransform,
		"drop_topic_prefix":     DropTopicPrefixTransform,
		"if_pattern":            IfPatternTransform,
		"if_topic_prefix":       IfPrefixTransform,
		"if_else_topic_pattern": IfElsePatternTransform,
		"if_else_topic_prefix":  IfElsePrefixTransform,
		"stop":                  StopTransforms,
		"jq":                    config.makeJqTransform(),
	}

	return ctx
}

var DropTopicPatternTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "pattern", Type: cty.String},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		capsule := NewMessageTransformCapsule(vbxform.DropTopicPattern(args[0].AsString()))
		return capsule, nil
	},
})

var DropTopicPrefixTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		capsule := NewMessageTransformCapsule(vbxform.DropTopicPrefix(args[0].AsString()))
		return capsule, nil
	},
})

var AddTopicPrefixTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		capsule := NewMessageTransformCapsule(vbxform.AddTopicPrefix(args[0].AsString()))
		return capsule, nil
	},
})

var ChainTransformsTransform = function.New(&function.Spec{
	Params: []function.Parameter{},
	VarParam: &function.Parameter{
		Name: "transforms",
		Type: cty.List(MessageTransformCapsuleType),
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		var transforms []vbxform.MessageTransformFunc
		for _, arg := range args {
			transform, err := GetMessageTransformFromCapsule(arg)
			if err != nil {
				return cty.NullVal(retType), err
			}
			transforms = append(transforms, transform)
		}
		capsule := NewMessageTransformCapsule(vbxform.ChainTransforms(transforms...))
		return capsule, nil
	},
})

var IfPatternTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "pattern", Type: cty.String},
		{Name: "transform", Type: MessageTransformCapsuleType},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		transformFunc, err := GetMessageTransformFromCapsule(args[1])
		if err != nil {
			return cty.NullVal(retType), err
		}
		capsule := NewMessageTransformCapsule(vbxform.IfPattern(args[0].AsString(), transformFunc))
		return capsule, nil
	},
})

var IfPrefixTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
		{Name: "transform", Type: MessageTransformCapsuleType},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		transformFunc, err := GetMessageTransformFromCapsule(args[1])
		if err != nil {
			return cty.NullVal(retType), err
		}
		capsule := NewMessageTransformCapsule(vbxform.IfPrefix(args[0].AsString(), transformFunc))
		return capsule, nil
	},
})

var IfElsePatternTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "pattern", Type: cty.String},
		{Name: "ifTransform", Type: MessageTransformCapsuleType},
		{Name: "elseTransform", Type: MessageTransformCapsuleType},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		ifTransform, err := GetMessageTransformFromCapsule(args[1])
		if err != nil {
			return cty.NullVal(retType), err
		}
		elseTransform, err := GetMessageTransformFromCapsule(args[2])
		if err != nil {
			return cty.NullVal(retType), err
		}
		capsule := NewMessageTransformCapsule(vbxform.IfElsePattern(args[0].AsString(), ifTransform, elseTransform))
		return capsule, nil
	},
})

var IfElsePrefixTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "prefix", Type: cty.String},
		{Name: "ifTransform", Type: MessageTransformCapsuleType},
		{Name: "elseTransform", Type: MessageTransformCapsuleType},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		ifTransform, err := GetMessageTransformFromCapsule(args[1])
		if err != nil {
			return cty.NullVal(retType), err
		}
		elseTransform, err := GetMessageTransformFromCapsule(args[2])
		if err != nil {
			return cty.NullVal(retType), err
		}
		capsule := NewMessageTransformCapsule(vbxform.IfElsePrefix(args[0].AsString(), ifTransform, elseTransform))
		return capsule, nil
	},
})

var DiffTransform = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "oldKey", Type: cty.String},
		{Name: "newKey", Type: cty.String},
	},
	Type: function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		capsule := NewMessageTransformCapsule(vbxform.ModifyPayload(vbxform.DiffTransform(args[0].AsString(), args[1].AsString())))
		return capsule, nil
	},
})

var StopTransforms = function.New(&function.Spec{
	Params: []function.Parameter{},
	Type:   function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		capsule := NewMessageTransformCapsule(vbxform.StopTransforms())
		return capsule, nil
	},
})

func (config *Config) makeJqTransform() function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{Name: "jqQuery", Type: cty.String},
		},
		Type: function.StaticReturnType(MessageTransformCapsuleType),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			transformFunc, err := transform.JqTransform(args[0].AsString(), config.Logger)
			if err != nil {
				return cty.NilVal, err
			}
			return NewMessageTransformCapsule(transformFunc), nil
		},
	})
}

func cty2goSimpleTransform(ctx context.Context, payload any, fields map[string]string) any {
	var err error

	if payload != nil {
		if val, ok := payload.(cty.Value); ok {
			payload, err = go2cty2go.CtyToAny(val)
			if err != nil {
				return nil
			}
		}
	}

	return payload
}

var cty2goTransform = vbxform.ModifyPayload(cty2goSimpleTransform)

var Cty2GoTransformFunc = function.New(&function.Spec{
	Params: []function.Parameter{},
	Type:   function.StaticReturnType(MessageTransformCapsuleType),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return NewMessageTransformCapsule(cty2goTransform), nil
	},
})
