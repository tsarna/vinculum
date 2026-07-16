package redisstream

import (
	"fmt"

	richcty "github.com/tsarna/rich-cty-types"
	"github.com/tsarna/vinculum-redis/stream"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// redisAckFunc is the HCL function `redis::ack(ctx, consumer, message_id)`.
// It XACKs the given entry on the consumer's stream and group. Registered
// globally so any action expression can reach it without a per-consumer
// eval-context shim.
var redisAckFunc = function.New(&function.Spec{
	Description: "XACKs a Redis Streams entry on a consumer's stream and group; returns true on success. Used with a redis_stream consumer configured with auto_ack = false.",
	Params: []function.Parameter{
		// AllowDynamicType keeps the static bool return visible in reflected metadata.
		{Name: "ctx", Type: cty.DynamicPseudoType, AllowDynamicType: true, Description: "The handler context"},
		{Name: "consumer", Type: stream.ConsumerCapsuleType, Description: "The consumer to ack on (client.<name>.consumer.<c>)"},
		{Name: "message_id", Type: cty.String, Description: "The entry ID to acknowledge (exposed to the action as ctx.message_id)"},
	},
	Type: function.StaticReturnType(cty.Bool),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		goCtx, err := richcty.GetContextFromValue(args[0])
		if err != nil {
			return cty.False, fmt.Errorf("redis::ack: invalid ctx: %w", err)
		}
		c, err := stream.GetConsumerFromCapsule(args[1])
		if err != nil {
			return cty.False, fmt.Errorf("redis::ack: %w", err)
		}
		if args[2].IsNull() {
			return cty.False, fmt.Errorf("redis::ack: message_id must not be null")
		}
		if err := c.Ack(goCtx, args[2].AsString()); err != nil {
			return cty.False, err
		}
		return cty.True, nil
	},
})

func init() {
	cfg.RegisterFunctionPlugin("redis::ack", func(_ *cfg.Config) map[string]function.Function {
		return map[string]function.Function{
			"redis::ack": redisAckFunc,
		}
	})
}
