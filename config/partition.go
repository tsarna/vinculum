package config

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/go2cty2go"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/tsarna/vinculum-bus/subutils"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

func init() {
	// Registered here, beside the builder above, because this is the shape that
	// function constructs: topic and fields always, and the payload only when
	// the expression was seen to ask for it.
	RegisterContextSchema("partition-key", ContextSchema{
		Summary: "Evaluated once per message, before it is queued, to decide which messages must stay in order.",
		Doc: "Evaluated on the goroutine handing the message over rather than on the one that " +
			"will process it, so an expensive expression here is charged to the source the " +
			"queue was decoupling.",
		Fields: []ContextField{
			{
				Name: "topic", Type: attrTypeString,
				Summary: "Topic the message arrived on.",
				Doc: "As it arrived: a `transforms` pipeline that rewrites the topic runs after the " +
					"partition has been chosen.",
			},
			{
				Name: "fields", Type: CtxTypeObject,
				Summary: "String metadata attached to the message.",
				Doc: "Always present; an empty object when the message carries no metadata. This is " +
					"the cheap place to find a key — a routing key, a partition key, a device id.",
			},
			{
				Name: "msg", Type: CtxTypeDynamic, Optional: true,
				Summary: "The message payload.",
				Doc: "Present only when the expression refers to it, because building it converts the " +
					"payload — the expensive part of computing a key, and paid again when the work " +
					"itself runs. Prefer a key drawn from `topic` or `fields` where the data allows.",
			},
		},
	})
}

// partitionTopicWarning reports a subscription that has asked for more
// partitions than it can possibly use.
//
// Partitioning by the default key puts every message on a topic into one
// partition, so the number of distinct topics is a ceiling on how many
// partitions ever run. Where the subscription's patterns are all literal, that
// ceiling is known at load — and a configuration reading `partitions = 8` while
// three goroutines can ever be busy has been told something that is not true.
//
// Only a `subscription` can be checked this way. A client receiver's topic can
// come from a `vinculum_topic` expression evaluated per message, so there is
// nothing to count; that asymmetry is honest rather than a gap, and the trap is
// documented on the attribute for everyone.
func partitionTopicWarning(def SubscriptionDefinition) hcl.Diagnostics {
	if def.Partitions == nil || *def.Partitions < 2 || IsExpressionProvided(def.PartitionKey) {
		return nil
	}

	for _, topic := range def.Topics {
		// A pattern matches topics this cannot count, so nothing is knowable.
		if strings.ContainsAny(topic, "+#") {
			return nil
		}
	}

	if len(def.Topics) >= *def.Partitions {
		return nil
	}

	return hcl.Diagnostics{{
		Severity: hcl.DiagWarning,
		Summary:  "partitions exceeds the number of topics that can fill them",
		Detail: fmt.Sprintf(
			"This subscription names %d literal %s and asks for %d partitions. Messages are "+
				"assigned by hashing the partition key, which defaults to the topic, so at most "+
				"%d partitions can ever be busy and the rest hold nothing. Set partition_key to "+
				"something that varies per message — or partition_key = null if no ordering is "+
				"required — or lower partitions.",
			len(def.Topics), pluralTopics(len(def.Topics)), *def.Partitions, len(def.Topics)),
		Subject: def.PartitionsRange.Ptr(),
	}}
}

func pluralTopics(n int) string {
	if n == 1 {
		return "topic"
	}

	return "topics"
}

// partitionsSubject points a diagnostic at the `partitions` attribute where the
// block captured its range, and at the block itself where it did not.
func (s SubscriberSource) partitionsSubject(defRange hcl.Range) *hcl.Range {
	if s.PartitionsRange.End.Byte > s.PartitionsRange.Start.Byte {
		return &s.PartitionsRange
	}

	return &defRange
}

// buildPartitionKeyFunc turns a `partition_key` expression into the function
// the queue calls to decide which messages must stay in order relative to each
// other.
//
// A nil function with no diagnostics means the expression asked for no ordering
// at all — see partitionKeyIsUnordered — and the caller should select
// round-robin instead.
func buildPartitionKeyFunc(config *Config, expr hcl.Expression, name string) (subutils.PartitionKeyFunc, hcl.Diagnostics) {
	if partitionKeyIsUnordered(expr) {
		return nil, nil
	}

	// Whether the payload has to be converted at all is decided once, here,
	// rather than per message. See partitionKeyNeedsPayload for why that
	// matters more than it looks.
	needsPayload := partitionKeyNeedsPayload(expr)

	// A key expression that fails does so for every message, at message rate,
	// on the goroutine the queue exists to keep moving. One report is the
	// whole of the information; the rest is an outage of its own.
	var reported atomic.Bool

	// The keys people actually write are a field or the topic, and both are
	// already Go strings on the message. Recognizing them here — once, at
	// construction, with the name curried in — answers them with a map lookup
	// instead of building a cty context per message to read a string back out
	// of it. `partition_key = ctx.fields.key` on a kafka receiver is the record
	// key the consumer already had in hand.
	if field, ok := partitionKeyField(expr); ok {
		return func(msg bus.EventBusMessage) string {
			// Comma-ok because the general path below *errors* on a field the
			// message does not carry: `ctx.fields` is a cty object built from
			// what is there, and reading an absent attribute is a failure
			// rather than a null. The fast path has to agree with it, or how a
			// key is spelled would decide what a missing field means.
			key, present := msg.Fields[field]
			if !present {
				return partitionKeyFallback(config, name, msg, &reported,
					fmt.Errorf("this message has no %q field", field))
			}

			return key
		}, nil
	}

	if partitionKeyIsTopic(expr) {
		return func(msg bus.EventBusMessage) string { return msg.Topic }, nil
	}

	return func(msg bus.EventBusMessage) string {
		builder := hclutil.NewEvalContext(msg.Ctx).
			WithStringAttribute("topic", msg.Topic).
			WithStringMapAttribute("fields", msg.Fields)

		if needsPayload {
			payload, err := go2cty2go.AnyToCty(msg.Payload)
			if err != nil {
				return partitionKeyFallback(config, name, msg, &reported,
					fmt.Errorf("converting the message payload: %w", err))
			}
			builder = builder.WithAttribute("msg", payload)
		}

		evalCtx, err := builder.BuildEvalContext(config.evalCtx)
		if err != nil {
			return partitionKeyFallback(config, name, msg, &reported, err)
		}

		val, diags := expr.Value(evalCtx)
		if diags.HasErrors() {
			return partitionKeyFallback(config, name, msg, &reported, diags)
		}

		key, err := partitionKeyString(val)
		if err != nil {
			return partitionKeyFallback(config, name, msg, &reported, err)
		}

		return key
	}, nil
}

// partitionKeyIsUnordered reports whether the expression is a literal null,
// which is how a configuration says it has no ordering requirement at all.
//
// A literal null and an expression that merely *evaluates* to null for one
// message are different requests and both are legal: this one asks for
// round-robin across every partition, while the second is a message with no key,
// which shares a partition with every other keyless message so that they stay
// ordered among themselves. Telling them apart is possible because a constant
// expression can be evaluated with no context at all.
func partitionKeyIsUnordered(expr hcl.Expression) bool {
	val, ok := hclutil.IsConstantExpression(expr)
	return ok && val.IsNull()
}

// partitionKeyField reports the field name when the expression is exactly
// `ctx.fields.<name>`, which is the key most configurations write.
//
// Exactly, and nothing near it. `hcl.AbsTraversalForExpr` succeeds only for a
// bare traversal, so `lower(ctx.fields.key)` and `"${ctx.fields.key}"` — which
// mention the same field and are not the same expression — take the general
// path, as they must.
func partitionKeyField(expr hcl.Expression) (string, bool) {
	traversal, diags := hcl.AbsTraversalForExpr(expr)
	if diags.HasErrors() || len(traversal) != 3 || traversal.RootName() != "ctx" {
		return "", false
	}

	if fields, ok := traversal[1].(hcl.TraverseAttr); !ok || fields.Name != "fields" {
		return "", false
	}

	name, ok := traversal[2].(hcl.TraverseAttr)
	if !ok {
		return "", false
	}

	return name.Name, true
}

// partitionKeyIsTopic reports whether the expression is exactly `ctx.topic`,
// which is the default key written out longhand — and worth recognising for
// the same reason, since it costs a context to answer with a string the
// message is already carrying.
func partitionKeyIsTopic(expr hcl.Expression) bool {
	traversal, diags := hcl.AbsTraversalForExpr(expr)
	if diags.HasErrors() || len(traversal) != 2 || traversal.RootName() != "ctx" {
		return false
	}

	topic, ok := traversal[1].(hcl.TraverseAttr)

	return ok && topic.Name == "topic"
}

// partitionKeyNeedsPayload reports whether the expression reads the message
// payload, and so whether building its context has to convert one.
//
// The conversion is the expensive part of evaluating a key — for a large
// payload it dominates, and it would be paid twice, once here and again when
// the action builds its own context on the drain goroutine. Most keys do not
// need it: a routing key from `ctx.fields`, a topic segment, a constant.
//
// The answer is conservative in the one direction that matters. A traversal
// rooted at bare `ctx` — a functy call handed the whole context — could reach
// anything, and a functy body is not visible to any traversal walk, so it
// counts as a read.
func partitionKeyNeedsPayload(expr hcl.Expression) bool {
	for _, traversal := range expr.Variables() {
		if traversal.RootName() != "ctx" {
			continue
		}

		if len(traversal) < 2 {
			return true
		}

		attr, ok := traversal[1].(hcl.TraverseAttr)
		if !ok || attr.Name == "msg" {
			return true
		}
	}

	return false
}

// partitionKeyString renders a key value.
//
// Strings, numbers and booleans are the types a key is: something with a small,
// stable printed form that means the same thing every time. Anything else is
// refused rather than formatted, because a key nobody can read in a log line is
// a key nobody can debug — and the refusal happens here, per message, only
// because a type is not knowable at load.
//
// A null is not a failure. It is a message with no key, and the empty string is
// a partition like any other, so keyless messages stay ordered among themselves.
func partitionKeyString(val cty.Value) (string, error) {
	if val.IsNull() {
		return "", nil
	}

	switch val.Type() {
	case cty.String:
		return val.AsString(), nil
	case cty.Number, cty.Bool:
		return ctyValueToString(val), nil
	default:
		return "", fmt.Errorf(
			"a partition key must be a string, number, or boolean, but this is %s",
			val.Type().FriendlyName())
	}
}

// partitionKeyFallback handles a key that could not be computed: the message
// goes to its topic's partition and the failure is reported once.
//
// Falling back rather than refusing the message, because the alternative on an
// acknowledged path is a redelivery loop — nack, redeliver, fail the same way,
// nack — over a configuration error that redelivery cannot fix. What is lost is
// the ordering the key asked for, which was already not being provided.
func partitionKeyFallback(config *Config, name string, msg bus.EventBusMessage, reported *atomic.Bool, err error) string {
	if reported.CompareAndSwap(false, true) {
		fields := []zap.Field{zap.String("subscriber", name), zap.String("topic", msg.Topic)}
		if diags, ok := err.(hcl.Diagnostics); ok && config.ActionErrorShowsSource(diags) {
			fields = append(fields, config.ActionError(diags))
		} else {
			fields = append(fields, zap.Error(err))
		}

		config.UserLogger.Error(
			"partition_key could not be evaluated; ordering by topic instead. "+
				"This is reported once per subscriber.", fields...)
	}

	return msg.Topic
}
