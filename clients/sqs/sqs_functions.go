package sqs

import (
	"fmt"

	richcty "github.com/tsarna/rich-cty-types"
	sqsreceiver "github.com/tsarna/vinculum-sqs/receiver"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func init() {
	cfg.RegisterFunctionPlugin("sqs", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"sqs_delete":            sqsDeleteFunc,
			"sqs_extend_visibility": sqsExtendVisibilityFunc,
		}
	})
}

// sqs_delete(ctx, receiver, receipt_handle) — deletes an SQS message.
var sqsDeleteFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "ctx", Type: cty.DynamicPseudoType},
		{Name: "receiver", Type: sqsreceiver.ReceiverCapsuleType},
		{Name: "receipt_handle", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		goCtx, err := richcty.GetContextFromValue(args[0])
		if err != nil {
			return cty.False, fmt.Errorf("sqs_delete: invalid ctx: %w", err)
		}

		r, err := sqsreceiver.GetReceiverFromCapsule(args[1])
		if err != nil {
			return cty.False, fmt.Errorf("sqs_delete: %w", err)
		}

		if args[2].IsNull() {
			return cty.False, fmt.Errorf("sqs_delete: receipt_handle must not be null")
		}

		if err := r.DeleteMsg(goCtx, args[2].AsString()); err != nil {
			return cty.False, err
		}
		return cty.True, nil
	},
})

// sqs_extend_visibility(ctx, receiver, receipt_handle, timeout) — extends
// the visibility timeout for an SQS message. timeout is a duration string
// or number of seconds.
var sqsExtendVisibilityFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "ctx", Type: cty.DynamicPseudoType},
		{Name: "receiver", Type: sqsreceiver.ReceiverCapsuleType},
		{Name: "receipt_handle", Type: cty.String},
		{Name: "timeout_seconds", Type: cty.Number},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		goCtx, err := richcty.GetContextFromValue(args[0])
		if err != nil {
			return cty.False, fmt.Errorf("sqs_extend_visibility: invalid ctx: %w", err)
		}

		r, err := sqsreceiver.GetReceiverFromCapsule(args[1])
		if err != nil {
			return cty.False, fmt.Errorf("sqs_extend_visibility: %w", err)
		}

		if args[2].IsNull() {
			return cty.False, fmt.Errorf("sqs_extend_visibility: receipt_handle must not be null")
		}

		seconds, _ := args[3].AsBigFloat().Int64()
		if seconds <= 0 {
			// Also handle float durations
			f, _ := args[3].AsBigFloat().Float64()
			seconds = int64(f)
		}
		if seconds <= 0 {
			return cty.False, fmt.Errorf("sqs_extend_visibility: timeout_seconds must be positive")
		}

		if err := r.ExtendVisibility(goCtx, args[2].AsString(), int32(seconds)); err != nil {
			return cty.False, err
		}
		return cty.True, nil
	},
})
