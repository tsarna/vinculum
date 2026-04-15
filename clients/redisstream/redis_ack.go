package redisstream

import (
	"fmt"

	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum-redis/stream"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// redisAckFunc is the HCL function `redis_ack(ctx, consumer, message_id)`.
// It XACKs the given entry on the consumer's stream and group. Registered
// globally so any action expression can reach it without a per-consumer
// eval-context shim.
var redisAckFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "ctx", Type: cty.DynamicPseudoType},
		{Name: "consumer", Type: stream.ConsumerCapsuleType},
		{Name: "message_id", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		goCtx, err := richcty.GetContextFromValue(args[0])
		if err != nil {
			return cty.False, fmt.Errorf("redis_ack: invalid ctx: %w", err)
		}
		c, err := stream.GetConsumerFromCapsule(args[1])
		if err != nil {
			return cty.False, fmt.Errorf("redis_ack: %w", err)
		}
		if args[2].IsNull() {
			return cty.False, fmt.Errorf("redis_ack: message_id must not be null")
		}
		if err := c.Ack(goCtx, args[2].AsString()); err != nil {
			return cty.False, err
		}
		return cty.True, nil
	},
})

func init() {
	cfg.RegisterFunctionPlugin("redis_ack", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"redis_ack": redisAckFunc,
		}
	})
}
