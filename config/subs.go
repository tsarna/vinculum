package config

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	richcty "github.com/tsarna/rich-cty-types"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	ctyjson "github.com/zclconf/go-cty/cty/json"
	"go.opentelemetry.io/otel/trace"
)

type SubscriptionDefinition struct {
	Name            string         `hcl:"name,label"`
	TargetExpr      hcl.Expression `hcl:"target"`
	Topics          []string       `hcl:"topics"`
	QueueSize       *int           `hcl:"queue_size,optional"`
	Partitions      *int           `hcl:"partitions,optional"`
	PartitionsRange hcl.Range      `hcl:"partitions,attr_range"`
	PartitionKey    hcl.Expression `hcl:"partition_key,optional"`
	Transforms      hcl.Expression `hcl:"transforms,optional"`
	Subscriber      hcl.Expression `hcl:"subscriber,optional"`
	ActionExpr      hcl.Expression `hcl:"action,optional"`
	Tracing         hcl.Expression `hcl:"tracing,optional"`
	Disabled        bool           `hcl:"disabled,optional"`
}

// SubscriberSource groups the HCL attributes that together specify where a
// block delivers events: a destination (a named subscriber or an inline
// action), plus an optional transform pipeline, an optional async queue, and
// optionally how many messages that queue may work on at once.
//
// Every block that accepts this pattern should declare the attributes on its
// own definition struct with these exact HCL names:
//
//	Subscriber   hcl.Expression `hcl:"subscriber,optional"`
//	Action       hcl.Expression `hcl:"action,optional"`
//	Transforms   hcl.Expression `hcl:"transforms,optional"`
//	QueueSize    *int           `hcl:"queue_size,optional"`
//	Partitions   *int           `hcl:"partitions,optional"`
//	PartitionKey hcl.Expression `hcl:"partition_key,optional"`
//
// After decoding, populate a SubscriberSource from those fields and call
// Resolve to obtain the final bus.Subscriber.
//
// Example VCL:
//
//	subscriber    = bus.main                 // XOR
//	action        = log_info(topic, msg)
//	transforms    = [ jq(".payload") ]       // optional
//	queue_size    = 100                      // optional — enables async queue
//	partitions    = 8                        // optional — needs queue_size
//	partition_key = ctx.fields.device_id     // optional — needs partitions
type SubscriberSource struct {
	Subscriber   hcl.Expression
	Action       hcl.Expression
	Transforms   hcl.Expression
	QueueSize    *int
	Partitions   *int
	PartitionKey hcl.Expression

	// PartitionsRange is the `partitions` attribute's own source range, so a
	// value the queue cannot use is reported against the number rather than
	// against the block. Optional: a zero range falls back to defRange.
	PartitionsRange hcl.Range
}

// SubscriberSourceAttrs documents the attributes of a SubscriberSource,
// for every block that accepts the pattern — the subscription block and every
// client receiver. Fold it into a block's own attributes with MergeAttrs, and
// pair it with SubscriberSourceConstraints.
//
// The `action` entry names no ctx shape, because that genuinely differs per
// block: a receiver's action sees the fields its protocol extracted. Override
// that one entry where the shape is known.
var SubscriberSourceAttrs = map[string]AttrMeta{
	"subscriber": {
		Summary: "Subscriber to forward messages to, instead of evaluating an action.",
		Doc:     "Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.",
		Hint:    HintSubscriberRef,
	},
	"action": {
		Summary: "Expression evaluated once per message.",
		Doc:     "`ctx.topic` is the message topic and `ctx.msg` the payload; a protocol that extracts metadata also provides `ctx.fields`.",
		Hint:    HintActionExpression,
		Context: "message",
	},
	"transforms": {
		Summary: "Transform pipeline applied before the action or subscriber.",
		Doc:     "A list of transform functions applied in order to each message. Only transform functions are in scope here.",
		Hint:    HintTransformPipeline,
	},
	"queue_size": {
		Summary: "Depth of an async queue wrapping the subscriber.",
		Doc: "When set, delivery is handed to a background goroutine so slow work does not " +
			"block the source. The queue is bounded, and what happens to a message that " +
			"arrives when it is full depends on where it came from: one that arrived over a " +
			"transport that acknowledges is nacked, so the broker redelivers it, and any " +
			"other is dropped and counted. On a receiver this composes with `ack` rather " +
			"than conflicting with it — the acknowledgement follows the message through the " +
			"queue and arrives when the work finishes.\n\n" +
			"A graceful shutdown runs the queue out rather than exiting past it: see " +
			"[Boot and shutdown](health.md#boot-and-shutdown).",
	},
	"partitions": {
		Summary: "Number of messages that may be processed at once.",
		Doc: "Runs this many queues, each drained by its own goroutine, so that many messages " +
			"are handled in parallel. Order is preserved within a partition and not across " +
			"them, and `partition_key` decides which messages share one — so the key is where " +
			"ordering is configured and this is only how much parallelism the rest may use.\n\n" +
			"**A message picks its partition by hashing its key, so partitions do nothing " +
			"until the key varies.** The default key is the topic: on a receiver where every " +
			"message arrives on the same topic, every message hashes to the same partition and " +
			"nothing runs in parallel. Set `partition_key`, or `partition_key = null` if no " +
			"ordering is required at all.\n\n" +
			"`queue_size` is per partition, so `queue_size = 500` with `partitions = 8` is up " +
			"to 4000 messages buffered — reconcile that with whatever bounds in-flight " +
			"messages on the source, such as a RabbitMQ `prefetch` or an SQS visibility " +
			"timeout.\n\n" +
			"Work that runs in parallel must tolerate running in parallel. Two partitions " +
			"evaluating `set(ctx, var.n, get(var.n) + 1)` lose updates, whatever the key.",
		Default: "1",
	},
	"partition_key": {
		Summary: "Expression deciding which messages must stay in order.",
		Doc: "Messages whose key is equal are processed in the order they arrived, by one " +
			"goroutine; messages whose keys differ may be processed at once. Choose the key " +
			"that names the thing order matters for — a device, an account, a conversation.\n\n" +
			"Defaults to the topic. `null` asks for no ordering at all, dealing messages " +
			"round-robin across every partition, which is both faster and more evenly spread " +
			"than a key contrived to vary.\n\n" +
			"It is evaluated on the goroutine that hands the message over — the receiver's " +
			"poll loop, or the bus's dispatch — so its cost falls on the thing `queue_size` " +
			"was protecting. A plain `ctx.fields.<name>` or `ctx.topic` costs nothing: it is " +
			"read straight off the message, with no expression evaluated at all. Anything " +
			"else is evaluated per message, and reading `ctx.msg` is the expensive case, " +
			"since the payload is converted for this expression as well as for the work.\n\n" +
			"The key sees the message as it arrived, not as `transforms` will deliver it: a " +
			"pipeline that rewrites the topic does so after the partition has been chosen.",
		Hint:    HintExpression,
		Context: "partition-key",
	},
}

// SubscriberSourceConstraints states the rules Resolve enforces: exactly one of
// subscriber or action, and each half of partitioning needs the half beneath it.
var SubscriberSourceConstraints = []Constraint{
	MutuallyExclusive("action", "subscriber"),
	AtLeastOneOf("action", "subscriber").
		WithMessage("Specify either an action to evaluate or a subscriber to forward to."),
	Requires("partitions", "queue_size").
		WithMessage("partitions runs one queue per partition, so it needs queue_size to say how deep each is."),
	Requires("partition_key", "partitions").
		WithMessage("partition_key decides which partition a message goes to, which means nothing without partitions."),
}

// Resolve produces the bus.Subscriber specified by the source. Wrappers are
// applied in order: action|subscriber → transforms → async queue. Exactly one
// of Subscriber or Action must be provided.
//
// `name` is used as the AsyncQueueingSubscriber instrumentation name (tracer
// scope + span attribute). `tp` (if non-nil) is forwarded to the async queue
// so background processing emits new-root SpanKindConsumer spans linked to
// the caller's span (vinculum-bus v0.13.0+).
func (s SubscriberSource) Resolve(
	config *Config,
	defRange hcl.Range,
	name string,
	tp trace.TracerProvider,
) (bus.Subscriber, hcl.Diagnostics) {
	hasSub := IsExpressionProvided(s.Subscriber)
	hasAct := IsExpressionProvided(s.Action)
	if hasSub == hasAct {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Exactly one of subscriber or action must be specified",
			Subject:  &defRange,
		}}
	}

	// Partitioning is two attributes over the queue, and each is inert without
	// what is beneath it. Refused rather than ignored: a configuration that
	// names partitions and silently gets one is worse off than one that is told
	// what is missing, and the schema's constraints only describe this rule.
	if s.Partitions != nil && s.QueueSize == nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "partitions requires queue_size",
			Detail: "Each partition is a queue drained by its own goroutine, so there is nothing to " +
				"partition without one. Set queue_size to the depth each partition should have.",
			Subject: s.partitionsSubject(defRange),
		}}
	}
	if IsExpressionProvided(s.PartitionKey) && s.Partitions == nil {
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "partition_key requires partitions",
			Detail: "The key decides which partition a message goes to, and with one partition every " +
				"message is already processed in order by a single goroutine. Set partitions to " +
				"how many messages may be handled at once.",
			Subject: s.PartitionKey.Range().Ptr(),
		}}
	}

	var subscriber bus.Subscriber
	var diags hcl.Diagnostics
	if hasSub {
		subscriber, diags = GetSubscriberFromExpression(config, s.Subscriber)
		if diags.HasErrors() {
			return nil, diags
		}
	} else {
		subscriber = NewActionSubscriber(config, s.Action)
	}

	if IsExpressionProvided(s.Transforms) {
		transforms, tDiags := config.GetMessageTransforms(s.Transforms)
		if tDiags.HasErrors() {
			return nil, tDiags
		}
		subscriber = subutils.NewTransformingSubscriber(subscriber, transforms...)
	}

	if s.QueueSize != nil {
		async := subutils.NewAsyncQueueingSubscriber(subscriber, *s.QueueSize)
		if name != "" {
			async = async.WithName(name)
		}
		if tp != nil {
			async = async.WithTracerProvider(tp)
		}

		if s.Partitions != nil {
			if *s.Partitions < 1 {
				return nil, hcl.Diagnostics{{
					Severity: hcl.DiagError,
					Summary:  "partitions must be at least 1",
					Detail: "Each partition is a queue and a goroutine that drains it, so fewer than one " +
						"leaves nothing to process the messages. Omit the attribute for the default of 1.",
					Subject: s.partitionsSubject(defRange),
				}}
			}

			async = async.WithPartitions(*s.Partitions)

			if IsExpressionProvided(s.PartitionKey) {
				keyFn, keyDiags := buildPartitionKeyFunc(config, s.PartitionKey, name)
				if keyDiags.HasErrors() {
					return nil, keyDiags
				}

				// A nil function is the literal `partition_key = null`: no
				// ordering wanted, so deal messages round-robin rather than
				// hashing a key that says nothing.
				if keyFn == nil {
					async = async.WithoutOrdering()
				} else {
					async = async.WithPartitionKey(keyFn)
				}
			}
		}

		subscriber = async.Start()

		// The queue is where a message waits longest and where shutdown used to
		// lose it: the goroutine started above dies with the process, taking
		// whatever it had not run yet. Registering here covers every block that
		// accepts the pattern, since this is the only place one is built.
		config.InFlight = append(config.InFlight, InFlightHolder{
			Name:       name,
			QueueDepth: async.QueueDepth,
			Close:      async.Close,
		})
	}

	return subscriber, nil
}

type SubscriptionBlockHandler struct {
	BlockHandlerBase
	BackendDeps
}

func NewSubscriptionBlockHandler() *SubscriptionBlockHandler {
	return &SubscriptionBlockHandler{}
}

// Schema describes the subscription block for `vinculum schema`.
func (h *SubscriptionBlockHandler) Schema() TypeSchema { return subscriptionSchema }

var subscriptionSchema = TypeSchema{
	Sample:  &SubscriptionDefinition{},
	Summary: "Subscribes to messages from a bus or client.",
	Doc: `Subscribes to a bus (or client) and either evaluates an ` + "`action`" + ` expression
for each message or forwards messages to another subscriber.

The ` + "`subscriber`/`action`/`transforms`/`queue_size`" + ` set is a shared delivery-target
pattern: the same four attributes, with identical semantics, are accepted by every
client *receiver* block.`,
	Attrs: MergeAttrs(SubscriberSourceAttrs, map[string]AttrMeta{
		"target": {
			Summary: "Bus to subscribe to.",
			Doc:     "A bus — `bus.main`, `bus.events`. Unlike `subscriber`, this slot resolves an event bus and nothing else.",
			Hint:    HintBusRef,
		},
		"topics": {
			Summary: "Topic patterns to subscribe to.",
			Doc: "MQTT-style patterns: `+` matches one segment, `#` matches any number of trailing segments. " +
				"Naming a wildcard captures the segments it matched — `data/changed/+collection/+id` — " +
				"and the delivery target reads them as `ctx.fields.collection` and `ctx.fields.id`.",
			Hint: HintTopicPattern,
		},
		"action": AttrMeta{
			Summary: "Expression evaluated once per message.",
			Doc: "`ctx.topic` is the message topic, `ctx.msg` the payload, and `ctx.fields` any string metadata " +
				"attached to it — including whatever the matching pattern in `topics` captured.",
			Hint:    HintActionExpression,
			Context: "message",
		}.WithContextFields(ContextField{
			Name: "undeliverable_topic", Type: attrTypeString, Optional: true,
			Summary: "The topic that matched no subscriber.",
			Doc: "Present only on a delivery of `$undeliverable`, from a `bus` with " +
				"`undeliverable = true`. `topic` is `$undeliverable` on such a message — " +
				"it has to be, or this subscription's own matcher could not have selected " +
				"it — so the topic that failed to route is named after what it is.",
		}),
		"tracing": TracingAttr.WithDoc(
			"A `client \"otlp\"` block. Auto-wires to the default tracing backend when omitted.\n\n" +
				"Applies to the hop `queue_size` introduces, and so has no effect without it. " +
				"Delivery to a subscription is otherwise traced by the bus, under the span of " +
				"whatever published the message; a queue hands the work to a background " +
				"goroutine, which needs a span of its own linked back to that one."),
		"disabled": DisabledAttr,
	}),
	Constraints: SubscriberSourceConstraints,
}

func (h *SubscriptionBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "subscription." + block.Labels[0], nil
}

func (h *SubscriptionBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	// Exclude "action": it is runtime-evaluated (in OnEvent) and must not create config-time deps.
	// A subscription that does not name its tracing backend auto-wires one, so
	// it waits for every block that could be one.
	return h.AddBackendDeps(ExtractBlockDependencies(block, "action"), block, "tracing"), nil
}

func (h *SubscriptionBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	subscriptionDef := SubscriptionDefinition{}
	diags := DecodeBody(block.Body, config.evalCtx, &subscriptionDef)
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

	// Only reached when queue_size is set, where it is what keeps the trace
	// alive across the hop to the background goroutine. ResolveTracerProvider
	// answers (nil, nil) when no OTLP client is configured at all, which is the
	// common case and has to stay quiet — so only real diagnostics return here.
	tp, tracingDiags := config.ResolveTracerProvider(subscriptionDef.Tracing)
	if tracingDiags.HasErrors() {
		return tracingDiags
	}

	// The provider has one job here, so saying so is the whole of the check.
	// Without a queue there is no hop to trace: delivery happens inside the
	// bus's own span, which this cannot redirect.
	//
	// Held separately rather than appended to diags, because diags is reassigned
	// by the next `:=` and one of the two success paths below returns nil.
	var warnings hcl.Diagnostics
	if IsExpressionProvided(subscriptionDef.Tracing) && subscriptionDef.QueueSize == nil {
		warnings = warnings.Append(&hcl.Diagnostic{
			Severity: hcl.DiagWarning,
			Summary:  "tracing without queue_size has no effect",
			Detail: "A subscription's tracing applies to the background dispatch queue_size introduces. " +
				"Without it, delivery runs inside the span of whatever published the message and is " +
				"already traced, wherever that publisher reports to.",
			Subject: subscriptionDef.Tracing.Range().Ptr(),
		})
	}

	warnings = warnings.Extend(partitionTopicWarning(subscriptionDef))

	subscriber, diags := SubscriberSource{
		Subscriber:      subscriptionDef.Subscriber,
		Action:          subscriptionDef.ActionExpr,
		Transforms:      subscriptionDef.Transforms,
		QueueSize:       subscriptionDef.QueueSize,
		Partitions:      subscriptionDef.Partitions,
		PartitionsRange: subscriptionDef.PartitionsRange,
		PartitionKey:    subscriptionDef.PartitionKey,
	}.Resolve(config, block.DefRange, "subscription/"+subscriptionDef.Name, tp)
	if diags.HasErrors() {
		return diags
	}

	target, diags := GetTargetFromExpression(config, subscriptionDef.TargetExpr)
	if diags.HasErrors() {
		return diags
	}

	switch target := target.(type) {
	case bus.EventBus:
		for _, topic := range subscriptionDef.Topics {
			err := target.Subscribe(context.Background(), topic, subscriber) // TODO: context for otel
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

	case BusClient:
		target.SetSubscriber(subscriber)
		_, err := target.Build()
		if err != nil {
			return hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to build client",
					Detail:   err.Error(),
					Subject:  &block.DefRange,
				},
			}
		}

		/* @@@ TODO
		for _, topic := range subscriptionDef.Topics {
			// OnSubscribe the connection monitor to pre-register the subscriptions
		}
		*/

		return warnings
	}

	return diags.Extend(warnings)
}

func GetTargetFromExpression(config *Config, targetExpr hcl.Expression) (any, hcl.Diagnostics) {
	targetCapsule, diags := targetExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		if better := UndeclaredBusDiags(config, targetExpr); better.HasErrors() {
			return nil, better
		}
		return nil, diags
	}
	if targetCapsule.Type() == EventBusCapsuleType {
		eventBus, err := GetEventBusFromCapsule(targetCapsule)
		if err != nil {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Failed to get event bus from expression",
					Detail:   err.Error(),
					Subject:  targetExpr.Range().Ptr(),
				},
			}
		}
		return eventBus, nil
		/*	} else if targetCapsule.Type() == ClientCapsuleType {
			client, err := GetClientFromCapsule(targetCapsule)
			if err != nil {
				return nil, hcl.Diagnostics{
					&hcl.Diagnostic{
						Severity: hcl.DiagError,
						Summary:  "Failed to get client from expression",
						Detail:   err.Error(),
						Subject:  targetExpr.Range().Ptr(),
					},
				}
			}
			return client, nil*/
	}

	return nil, hcl.Diagnostics{
		&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Failed to get target from expression",
			Detail:   fmt.Sprintf("expected EventBus or Client capsule, got %s", targetCapsule.Type().FriendlyName()),
			Subject:  targetExpr.Range().Ptr(),
		},
	}
}

type ActionSubscriber struct {
	bus.BaseSubscriber
	Config     *Config
	ActionExpr hcl.Expression
}

// NewActionSubscriber creates an ActionSubscriber for use by plugin sub-packages.
func NewActionSubscriber(config *Config, expr hcl.Expression) bus.Subscriber {
	return &ActionSubscriber{Config: config, ActionExpr: expr}
}

func (a *ActionSubscriber) OnEvent(ctx context.Context, topic string, message any, fields map[string]string) error {
	ctyMessage, err := go2cty2go.AnyToCty(message)
	if err != nil {
		return err
	}

	evalCtxBuilder := hclutil.NewEvalContext(ctx).
		WithStringAttribute("topic", topic).
		WithAttribute("msg", ctyMessage)

	ctyFields := make(map[string]cty.Value)
	for key, value := range fields {
		ctyFields[key] = cty.StringVal(value)
	}
	evalCtxBuilder = evalCtxBuilder.WithAttribute("fields", cty.ObjectVal(ctyFields))

	// A message the bus is handing back because nothing matched it. `topic` is
	// `$undeliverable` — it has to be, or this subscription's own matcher could
	// not have selected it — so the topic that failed to route is named after
	// what it is rather than shadowing `topic`.
	if undeliverableTopic, ok := bus.UndeliverableTopicFromContext(ctx); ok {
		evalCtxBuilder = evalCtxBuilder.WithStringAttribute("undeliverable_topic", undeliverableTopic)
	}

	evalCtx, err := evalCtxBuilder.BuildEvalContext(a.Config.evalCtx)
	if err != nil {
		return err
	}

	_, diags := a.ActionExpr.Value(evalCtx)
	if diags.HasErrors() {
		// A failure that can be shown against its source — the failing line of
		// the action, or the `assert` a functy throw came from — is reported
		// here, because the bus renders only the text. Saying so on the way out
		// is what keeps the bus from repeating it, less well; the error itself
		// is unchanged, so anything reading the outcome is unaffected.
		if a.Config.ActionErrorShowsSource(diags) {
			a.Config.UserLogger.Error("subscription action error", a.Config.ActionError(diags))
			return reportedError{diags}
		}
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

// GetSubscriberFromCapsule extracts a Subscriber from a cty capsule value.
// Any capsule whose encapsulated value implements bus.Subscriber can be used
// (buses, clients, FSM instances, subscriber capsules, etc.).
func GetSubscriberFromCapsule(val cty.Value) (bus.Subscriber, error) {
	if val.Type().IsCapsuleType() {
		if sub, ok := val.EncapsulatedValue().(bus.Subscriber); ok {
			return sub, nil
		}
	}
	return nil, fmt.Errorf("expected Subscriber capsule, got %s", val.Type().FriendlyName())
}

// IsSubscriber reports whether val is a capsule whose encapsulated value
// implements bus.Subscriber (a bus, client, FSM, subscriber capsule, etc.). It
// returns nil when so, else the error from GetSubscriberFromCapsule. It backs the
// functy "subscriber" open type (RegisterOpenType), naming any such value in a
// .cty annotation while passing it through untouched. Null is handled by the
// constraint before the predicate runs.
func IsSubscriber(val cty.Value) error {
	_, err := GetSubscriberFromCapsule(val)
	return err
}

func GetSubscriberFromExpression(config *Config, subscriberExpr hcl.Expression) (bus.Subscriber, hcl.Diagnostics) {
	subscriberCapsule, diags := subscriberExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
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
func createSendFunction(config *Config, description string, converter MessageConverter) function.Function {
	return function.New(&function.Spec{
		Description: description,
		Params: []function.Parameter{
			{
				// AllowDynamicType keeps the static bool return visible in reflected
				// metadata — a dynamic argument without it poisons the return to dynamic.
				Name:             "ctx",
				Type:             cty.DynamicPseudoType,
				AllowDynamicType: true,
				Description:      "The handler context (carries tracing, deadlines, and request scope)",
			},
			{
				Name:             "subscriber",
				Type:             cty.DynamicPseudoType,
				AllowDynamicType: true,
				Description:      "Where to send: a bus, or a server/client that is a subscriber",
			},
			{
				Name:        "topic",
				Type:        cty.String,
				Description: "The topic to publish under",
			},
			{
				Name:             "message",
				Type:             cty.DynamicPseudoType,
				AllowDynamicType: true,
				Description:      "The message payload",
			},
		},
		VarParam: &function.Parameter{
			Name:        "fields",
			Type:        cty.DynamicPseudoType,
			Description: "Optional structured fields (a single map/object, or positional values) merged into the message envelope",
		},
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
			ctx, err := richcty.GetContextFromValue(args[0])
			if err != nil {
				return cty.False, fmt.Errorf("context error: %w", err)
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

			// send() derives a *new* message; it does not hand the current one
			// on. So the inbound delivery's settler stays with the original,
			// which settles on this action's own outcome, and does not travel
			// with what the action produced.
			//
			// Carrying it would make an author's fan-out a race: three messages
			// derived from one delivery would be three things able to settle
			// it, and settle-once would pick an arbitrary winner. It would also
			// give two spellings of one topology different guarantees, since a
			// send() inside an action is an asynchronous hand-off that nothing
			// downstream can see. Not carrying it makes the distinction real
			// and teachable: `subscriber = X` hands the delivery on and the
			// acknowledgement waits for X, while send() starts something new.
			//
			// This is deliberately the opposite of what origin does through the
			// same call — origin must survive send(), or a derived message
			// could not reply. Two rules, one line, and each silently undoes
			// the other if written without reading both.
			err = subscriber.OnEvent(bus.WithoutSettler(ctx), topic, convertedMessage, fields)
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

func init() {
	RegisterFunctionPlugin("send", func(_ *Config) map[string]function.Function {
		return map[string]function.Function{
			"send":       SendFunction(nil),
			"send::json": SendJSONFunction(nil),
			"send::go":   SendGoFunction(nil),
		}
	})

	// ActionSubscriber.OnEvent above builds this, and every client receiver
	// builds the same shape — it is the delivery-target pattern's context, so
	// it is described once here alongside SubscriberSourceAttrs.
	RegisterContextSchema("message", ContextSchema{
		Summary: "Evaluated once per message delivered.",
		Fields: []ContextField{
			{Name: "topic", Type: attrTypeString, Summary: "Topic the message was delivered on."},
			{
				Name: "msg", Type: CtxTypeDynamic,
				Summary: "The message payload.",
				Doc:     "Already decoded by the client's `wire_format`, so its type follows the data rather than the transport.",
			},
			{
				Name: "fields", Type: CtxTypeObject,
				Summary: "String metadata attached to the message.",
				Doc: "Always present; an empty object when the message carries no metadata. " +
					"On a bus delivery it holds what the subscribed topic pattern captured: " +
					"naming a wildcard — `data/changed/+collection/+id` — puts " +
					"`ctx.fields.collection` and `ctx.fields.id` in scope for every matching " +
					"message, so an action need not slice `ctx.topic` apart itself. On a client " +
					"receiver it holds the metadata the transport attached instead. Either way " +
					"these are per-delivery and do not propagate: what a publisher attaches is " +
					"not what the next subscriber reads here.",
			},
		},
		// Open, because a bus delivery can carry one field a client receiver's
		// never can: `undeliverable_topic`, which only the bus knows how to set.
		// The subscription block names it; the receivers correctly do not.
		OpenFields: true,
	})
}

// SendFunction returns a cty function for sending a message to a bus subscriber (original behavior)
func SendFunction(config *Config) function.Function {
	return createSendFunction(config, "Sends a message to a bus subscriber under a topic; returns true on success", defaultMessageConverter)
}

// SendJSONFunction returns a cty function for sending a JSON string message to a bus subscriber
func SendJSONFunction(config *Config) function.Function {
	return createSendFunction(config, "Sends a message to a bus subscriber under a topic, encoded as a JSON string; returns true on success", jsonMessageConverter)
}

// SendGoFunction returns a cty function for sending a Go native type message to a bus subscriber
func SendGoFunction(config *Config) function.Function {
	return createSendFunction(config, "Sends a message to a bus subscriber under a topic, converted to native Go types; returns true on success", func(message cty.Value) (any, error) {
		return go2cty2go.CtyToAny(message)
	})
}
