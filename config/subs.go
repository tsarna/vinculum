package config

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"go.uber.org/zap"
)

type SubscriptionDefinition struct {
	Name       string         `hcl:",label"`
	BusExpr    hcl.Expression `hcl:"bus,optional"`
	Topics     []string       `hcl:"topics"`
	QueueSize  *int           `hcl:"queue_size,optional"`
	Transforms hcl.Expression `hcl:"transforms,optional"`
	Subscriber hcl.Expression `hcl:"subscriber,optional"`
	ActionExpr hcl.Expression `hcl:"action,optional"`
	Disabled   bool           `hcl:"disabled,optional"`
}

type SubscriptionBlockHandler struct {
	BlockHandlerBase
}

func NewSubscriptionBlockHandler() *SubscriptionBlockHandler {
	return &SubscriptionBlockHandler{}
}

func (h *SubscriptionBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "subscription." + block.Labels[0], nil
}

func (h *SubscriptionBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	subscriptionDef := SubscriptionDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &subscriptionDef)
	if diags.HasErrors() {
		return diags
	}

	if subscriptionDef.Disabled {
		return nil
	}

	// Manually set the name from the block label since DecodeBody doesn't handle labels
	if len(block.Labels) > 0 {
		subscriptionDef.Name = block.Labels[0]
	}

	eventBus, diags := GetEventBusFromExpression(config, subscriptionDef.BusExpr)
	if diags.HasErrors() {
		return diags
	}

	// Check if expressions are actually present (not just empty HCL expressions)
	hasSubscriber := IsExpressionProvided(subscriptionDef.Subscriber)
	hasAction := IsExpressionProvided(subscriptionDef.ActionExpr)

	if hasSubscriber && hasAction || !hasSubscriber && !hasAction {
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Exactly one of subscriber or action must be specified",
				Subject:  &block.DefRange,
			},
		}
	}

	var subscriber bus.Subscriber

	if hasSubscriber {
		subscriber, diags = GetSubscriberFromExpression(config, subscriptionDef.Subscriber)
		if diags.HasErrors() {
			return diags
		}
	} else {
		subscriber, diags = CreateActionSubscriber(config, &subscriptionDef)
		if diags.HasErrors() {
			return diags
		}
	}

	if IsExpressionProvided(subscriptionDef.Transforms) {
		transforms, diags := config.GetMessageTransforms(subscriptionDef.Transforms)
		if diags.HasErrors() {
			return diags
		}

		subscriber = subutils.NewTransformingSubscriber(subscriber, transforms...)
	}

	if subscriptionDef.QueueSize != nil {
		subscriber = subutils.NewAsyncQueueingSubscriber(subscriber, *subscriptionDef.QueueSize)
	}

	for _, topic := range subscriptionDef.Topics {
		err := eventBus.Subscribe(context.Background(), topic, subscriber) // TODO: context for otel
		if err != nil {
			diags = diags.Append(
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to subscribe to bus",
					Detail:   err.Error(),
					Subject:  &block.DefRange,
				})
		}
	}

	return diags
}

func CreateActionSubscriber(config *Config, subscriptionDef *SubscriptionDefinition) (bus.Subscriber, hcl.Diagnostics) {
	config.Logger.Info("CreateSubscriber called", zap.String("subscription", subscriptionDef.Name))
	return &ActionSubscriber{
		Config:     config,
		ActionExpr: subscriptionDef.ActionExpr,
	}, nil
}

type ActionSubscriber struct {
	bus.BaseSubscriber
	Config     *Config
	ActionExpr hcl.Expression
}

func (a *ActionSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	ctyMessage, err := go2cty2go.AnyToCty(message)
	if err != nil {
		return err
	}

	evalCtxBuilder := NewContext(ctx).
		WithStringAttribute("topic", topic).
		WithAttribute("msg", ctyMessage)

	if len(fields) > 0 {
		ctyFields := make(map[string]cty.Value)
		for key, value := range fields {
			ctyFields[key] = cty.StringVal(value)
		}
		evalCtxBuilder = evalCtxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))
	}

	evalCtx, diags := evalCtxBuilder.BuildEvalContext(a.Config.evalCtx)
	if diags.HasErrors() {
		return diags
	}

	a.Config.Logger.Info("OnEvent called", zap.String("topic", topic), zap.Any("message", message), zap.Any("fields", fields))

	_, diags = a.ActionExpr.Value(evalCtx)
	if diags.HasErrors() {
		return diags
	}

	return nil
}

// Subscriber is a cty capsule type for wrapping Susbcriber instances
var SubscriberCapsuleType = cty.CapsuleWithOps("subscriber", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("subscriber(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "subscriber"
	},
})

// NewSubscriberCapsule creates a new cty capsule value wrapping an Subscriber
func NewSubscriberCapsule(subscriber bus.Subscriber) cty.Value {
	return cty.CapsuleVal(SubscriberCapsuleType, subscriber)
}

// GetSubscriberFromCapsule extracts an Subscriber from a cty capsule value
func GetSubscriberFromCapsule(val cty.Value) (bus.Subscriber, error) {
	// A bus may be used as a subscriber
	if val.Type() == EventBusCapsuleType {
		return GetEventBusFromCapsule(val)
	} else if val.Type() != SubscriberCapsuleType {
		return nil, fmt.Errorf("expected Subscriber capsule, got %s", val.Type().FriendlyName())
	}

	encapsulated := val.EncapsulatedValue()
	subscriber, ok := encapsulated.(bus.Subscriber)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an Subscriber, got %T", encapsulated)
	}
	return subscriber, nil
}

func GetSubscriberFromExpression(config *Config, subscriberExpr hcl.Expression) (bus.Subscriber, hcl.Diagnostics) {
	subscriberCapsule, diags := subscriberExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	if subscriberCapsule.IsNull() {
		if subscriber, ok := config.Buses["main"]; ok {
			return subscriber, nil
		} else {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Main bus not found",
				},
			}
		}
	}

	subscriber, err := GetSubscriberFromCapsule(subscriberCapsule)
	if err != nil {
		exprRange := subscriberExpr.Range()

		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to get subscriber from expression",
				Detail:   err.Error(),
				Subject:  &exprRange,
			},
		}
	}

	return subscriber, nil
}

// MessageConverter defines how to convert a cty.Value message before sending
type MessageConverter func(cty.Value) (any, error)

// createSendFunction is a shared helper that creates send functions with different message converters
func createSendFunction(config *Config, converter MessageConverter) function.Function {
	return function.New(&function.Spec{
		Params: []function.Parameter{
			{
				Name: "ctx",
				Type: cty.DynamicPseudoType,
			},
			{
				Name: "subscriber",
				Type: cty.DynamicPseudoType,
			},
			{
				Name: "topic",
				Type: cty.String,
			},
			{
				Name: "message",
				Type: cty.DynamicPseudoType,
			},
		},
		VarParam: &function.Parameter{
			Name: "fields",
			Type: cty.DynamicPseudoType,
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, diags := GetContextFromObject(args[0])
			if diags.HasErrors() {
				return cty.False, fmt.Errorf("context error: %s", diags.Error())
			}
			subscriber, err := GetSubscriberFromCapsule(args[1])
			if err != nil {
				return cty.False, err
			}
			topic := args[2].AsString()
			message := args[3]

			// Convert the message using the provided converter
			convertedMessage, err := converter(message)
			if err != nil {
				return cty.False, fmt.Errorf("failed to convert message: %w", err)
			}

			// Convert fields if provided, otherwise use nil
			var fields map[string]string
			if len(args) > 4 && !args[4].IsNull() {
				var err error
				fields, err = convertToStringMap(args[4])
				if err != nil {
					return cty.False, fmt.Errorf("failed to convert fields to map[string]string: %w", err)
				}
			}

			err = subscriber.OnEvent(ctx, topic, convertedMessage, fields)
			if err != nil {
				return cty.False, fmt.Errorf("failed to send event: %w", err)
			}

			return cty.True, nil
		},
	})
}

// convertToStringMap converts a cty.Value to map[string]string for use as fields
func convertToStringMap(value cty.Value) (map[string]string, error) {
	if value.IsNull() {
		return nil, nil
	}

	// Handle object types
	if value.Type().IsObjectType() {
		result := make(map[string]string)
		for name := range value.Type().AttributeTypes() {
			attrVal := value.GetAttr(name)
			if attrVal.IsNull() {
				result[name] = ""
			} else if attrVal.Type() == cty.String {
				result[name] = attrVal.AsString()
			} else {
				// Convert non-string values to string representation
				result[name] = ctyValueToString(attrVal)
			}
		}
		return result, nil
	}

	// Handle map types
	if value.Type().IsMapType() {
		result := make(map[string]string)
		it := value.ElementIterator()
		for it.Next() {
			keyVal, elemVal := it.Element()
			key := keyVal.AsString()
			if elemVal.IsNull() {
				result[key] = ""
			} else if elemVal.Type() == cty.String {
				result[key] = elemVal.AsString()
			} else {
				// Convert non-string values to string representation
				result[key] = ctyValueToString(elemVal)
			}
		}
		return result, nil
	}

	return nil, fmt.Errorf("fields must be an object or map, got %s", value.Type().FriendlyName())
}

// ctyValueToString converts a cty.Value to its string representation
func ctyValueToString(val cty.Value) string {
	if val.IsNull() {
		return ""
	}

	switch val.Type() {
	case cty.String:
		return val.AsString()
	case cty.Bool:
		if val.True() {
			return "true"
		}
		return "false"
	case cty.Number:
		// Try to convert to int first, then float
		if bigFloat := val.AsBigFloat(); bigFloat.IsInt() {
			if intVal, accuracy := bigFloat.Int64(); accuracy == 0 {
				return fmt.Sprintf("%d", intVal)
			}
		}
		if floatVal, _ := val.AsBigFloat().Float64(); true {
			return fmt.Sprintf("%g", floatVal)
		}
		return val.AsBigFloat().String()
	default:
		// For complex types, use a simple string representation
		return fmt.Sprintf("%#v", val)
	}
}

// defaultMessageConverter passes the cty.Value through as-is (original behavior)
func defaultMessageConverter(message cty.Value) (any, error) {
	return message, nil
}

// jsonMessageConverter converts the cty.Value to JSON bytes
func jsonMessageConverter(message cty.Value) (any, error) {
	jsonBytes, err := ctyjson.Marshal(message, message.Type())
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cty value to JSON: %w", err)
	}

	return jsonBytes, nil
}

// SendFunction returns a cty function for sending a message to a bus subscriber (original behavior)
func SendFunction(config *Config) function.Function {
	return createSendFunction(config, defaultMessageConverter)
}

// SendJSONFunction returns a cty function for sending a JSON string message to a bus subscriber
func SendJSONFunction(config *Config) function.Function {
	return createSendFunction(config, jsonMessageConverter)
}

// SendGoFunction returns a cty function for sending a Go native type message to a bus subscriber
func SendGoFunction(config *Config) function.Function {
	return createSendFunction(config, func(message cty.Value) (any, error) {
		return go2cty2go.CtyToAny(message)
	})
}
