package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/hcl/v2"
)

// Acknowledgement modes. Every receiver that settles with a broker spells its
// policy the same way, in one attribute named `ack`.
//
// Before this there were four spellings for one concept, and they disagreed
// worse than cosmetically: `redis_stream.auto_ack` defaulted true and meant
// "vinculum acks after delivery", `rabbitmq.auto_ack` defaulted false and meant
// the *broker*-side no-ack mode, `sqs_receiver.auto_delete` meant the Redis
// thing under a third name, and `kafka.commit_mode` was a three-way enum none
// of whose values was manual in the sense the others meant it. A config author
// also had to know which protocol delivered a message in order to pick the
// right settle function, which defeats routing through a bus.
//
// The defaults collapse without changing a single configuration's meaning:
// every one of the four already behaved as AckAuto by default, RabbitMQ's
// `auto_ack = false` included — which is the argument for one uniform default
// rather than a per-protocol table to memorise.
const (
	// AckAuto settles when delivery returns without error. The default
	// everywhere, and what all four receivers did before `ack` existed.
	AckAuto = "auto"

	// AckManual settles nothing until the configuration calls inbound::ack or
	// inbound::nack, bounded by settle_timeout.
	AckManual = "manual"

	// AckPeriodic commits on a timer regardless of outcome. Kafka only, where
	// it was commit_mode = "periodic".
	AckPeriodic = "periodic"

	// AckNone is AMQP no-ack mode, where the broker treats a message as
	// delivered the moment it is sent and vinculum never acknowledges at all.
	// RabbitMQ only, where it was auto_ack = true.
	//
	// It is genuinely absent elsewhere rather than merely unimplemented: "never
	// settle" on SQS or Redis means redelivery loops and an unbounded pending
	// list, not fire-and-forget.
	AckNone = "none"
)

// AckAttr documents `ack`. Every host overrides Enum with exactly what it
// accepts — the generated page for a type must not name an option that type
// would reject — so the values here are only the two every receiver has.
var AckAttr = AttrMeta{
	Summary: "When a received message is settled with the broker.",
	Doc: "`auto` settles the message for you, on the outcome of the work: the delivery " +
		"travels on `ctx`, so the acknowledgement follows it through a `queue_size` " +
		"queue, a bus, an `fsm`, and any number of hops, and arrives when the work " +
		"finishes rather than when delivery returns. A handler that fails nacks instead, " +
		"so the broker redelivers or dead-letters it. `manual` settles nothing until the " +
		"configuration calls `inbound::ack()` or `inbound::nack()`, and requires " +
		"`settle_timeout` to bound how long a message may go unsettled.",
	Enum:    []string{AckAuto, AckManual},
	Default: AckAuto,
}

// SettleTimeoutAttr documents `settle_timeout`.
//
// It has no default, deliberately. Too short nacks slow work; too long and a
// RabbitMQ receiver stalls quietly on a held prefetch slot; and there is no one
// value that is right for both a 50ms enrichment and a five-minute batch. So a
// configuration that takes manual settle states the bound it can live with,
// rather than inheriting one nobody chose.
//
// Optional under `auto` for a different reason: the framework settles at a
// known point, so a configuration that asks for no bound is no worse off than
// it was. Whether `auto` should derive one from the broker's own lease is left
// to specs/SETTLE-ON-SHUTDOWN.md, which needs a bound of its own and would
// otherwise have to agree with one chosen here.
var SettleTimeoutAttr = AttrMeta{
	Summary: "How long a message may go unsettled before it is nacked automatically.",
	Doc: "Required with `ack = \"manual\"`, where nothing settles the message until the " +
		"configuration does. Optional with `ack = \"auto\"`, to bound a chain the " +
		"configuration does not fully trust — the acknowledgement follows the work, so a " +
		"handler that never finishes leaves the message outstanding. An unsettled message " +
		"costs something for as long as it is outstanding: an SQS visibility window, a " +
		"RabbitMQ prefetch slot, a Kafka partition's committable offset. On expiry the " +
		"message is nacked and the failure is logged against the receiver.",
	Hint: HintDuration,
}

// AckPolicy is one receiver's settled acknowledgement policy.
type AckPolicy struct {
	// Mode is one of the Ack* constants, defaulted to AckAuto.
	Mode string

	// SettleTimeout is the bound on an unsettled message, and is non-zero
	// exactly when Mode is AckManual.
	SettleTimeout time.Duration
}

// Manual reports whether the configuration settles messages itself.
func (p AckPolicy) Manual() bool { return p.Mode == AckManual }

// AckRequest is one receiver's `ack` and `settle_timeout` as written, plus what
// that receiver is able to honour.
type AckRequest struct {
	// Receiver names the block in diagnostics, already phrased for a summary
	// line: `kafka receiver "in"`.
	Receiver string

	// Value and ValueRange are the `ack` attribute as written. An empty Value
	// means it was omitted.
	Value      string
	ValueRange hcl.Range

	// SettleTimeout is the `settle_timeout` attribute as written.
	SettleTimeout hcl.Expression

	// QueueSize and QueueSizeRange are the `queue_size` attribute as written.
	QueueSize      *int
	QueueSizeRange hcl.Range

	// Extra names the mode this receiver accepts beyond auto and manual —
	// AckPeriodic on kafka, AckNone on rabbitmq — or is empty.
	Extra string

	// NoSettler, when non-empty, says why this receiver cannot give a delivery
	// a settler of its own. It becomes the detail of the diagnostics below, so
	// it should say what is missing and what to use instead.
	//
	// It is one field rather than two because it is one fact with two
	// consequences, and a receiver that gained the ability to settle one
	// delivery would gain both at once. Without a per-delivery settler:
	//
	//   - `manual` has nothing for inbound::ack() to settle, and
	//   - `auto` can only settle when delivery returns, so it is back to
	//     acknowledging at the enqueue whenever a queue is in front of it.
	//
	// Only `kafka` sets it: committing an offset is not acknowledging a record,
	// and completing record 7 while 5 is outstanding cannot commit anything
	// without a low-water-mark tracker it does not have.
	NoSettler string

	// DefRange is the block's own range, for the diagnostic when `ack` itself
	// was not written.
	DefRange hcl.Range
}

// ResolveAck validates a receiver's acknowledgement policy.
func (c *Config) ResolveAck(req AckRequest) (AckPolicy, hcl.Diagnostics) {
	accepted := []string{AckAuto, AckManual}
	if req.Extra != "" {
		accepted = append(accepted, req.Extra)
	}

	policy := AckPolicy{Mode: req.Value}
	if policy.Mode == "" {
		policy.Mode = AckAuto
	}

	switch {
	case policy.Mode == AckAuto:
	case policy.Mode == req.Extra:
	case policy.Mode == AckManual:
		if req.NoSettler != "" {
			return AckPolicy{}, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("%s: ack \"manual\" is not implemented yet", req.Receiver),
				Detail:   req.NoSettler,
				Subject:  ackSubject(req),
			}}
		}
	default:
		return AckPolicy{}, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("%s: invalid ack", req.Receiver),
			Detail:   fmt.Sprintf("%q is not valid; use %s.", policy.Mode, orList(accepted)),
			Subject:  ackSubject(req),
		}}
	}

	// A queue makes delivery return at the moment the message is queued. Where a
	// receiver can hand the delivery its own settler that is harmless — the
	// settler travels with the message and whatever finishes the work settles
	// it, however many hops away. Where it cannot, auto has nothing to settle on
	// but delivery's return, so the message would be acknowledged before
	// anything handled it: an error could no longer redeliver or dead-letter,
	// and a full queue would drop a message the broker was already told was
	// handled. At-least-once quietly becomes at-most-once.
	//
	// So the refusal is not about auto, it is about the receiver — which is why
	// this reads NoSettler and the diagnostic has to say why one receiver
	// refuses what another accepts.
	//
	// AckNone and AckPeriodic are unaffected either way: nothing is ever
	// acknowledged under the first, and the offset advances on a timer
	// regardless of outcome under the second, so neither has anything left to
	// give up to a queue.
	if policy.Mode == AckAuto && req.QueueSize != nil && req.NoSettler != "" {
		return AckPolicy{}, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("%s: queue_size cannot be combined with ack = %q", req.Receiver, AckAuto),
			Detail: "Delivery returns as soon as the message is queued, so a receiver that cannot " +
				"give the delivery a settler of its own has nothing left to settle on but that " +
				"return — the message would be acknowledged before anything handled it, and a " +
				"handler error could no longer redeliver or dead-letter it. Other receivers " +
				"accept this combination because their deliveries carry a settler that follows " +
				"the message to wherever the work finishes. " + req.NoSettler +
				" Or remove queue_size.",
			Subject: &req.QueueSizeRange,
		}}
	}

	hasTimeout := IsExpressionProvided(req.SettleTimeout)

	switch {
	case policy.Manual():
		if !hasTimeout {
			return AckPolicy{}, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("%s: ack = \"manual\" requires settle_timeout", req.Receiver),
				Detail: "Nothing settles a message until the configuration calls inbound::ack() or " +
					"inbound::nack(), so a message that reaches an expression that forgets to " +
					"would go unsettled indefinitely — holding a visibility window, a prefetch " +
					"slot, or a partition's committable offset. There is no default worth " +
					"choosing here, because no one value suits both a 50ms enrichment and a " +
					"five-minute batch: state the bound this receiver can live with.",
				Subject: ackSubject(req),
			}}
		}

	case !hasTimeout:
		return policy, nil

	// Permitted under auto, where the settle now happens wherever the work
	// finishes rather than when delivery returns — so there is a genuinely
	// unsettled message for a bound to apply to. Not required: the framework
	// settles at a known point, and a configuration that does not ask for a
	// bound is no worse off than it was.
	case policy.Mode == AckAuto && req.NoSettler == "":

	case policy.Mode == AckAuto:
		return AckPolicy{}, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("%s: settle_timeout is not supported by this receiver", req.Receiver),
			Detail: "settle_timeout bounds how long one delivery may go unsettled, and this " +
				"receiver cannot track a single delivery. " + req.NoSettler,
			Subject: req.SettleTimeout.Range().Ptr(),
		}}

	default:
		return AckPolicy{}, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary: fmt.Sprintf("%s: settle_timeout does not apply to ack = %q",
				req.Receiver, policy.Mode),
			Detail: fmt.Sprintf("With ack = %q there is no per-message acknowledgement for this "+
				"to bound. Use ack = \"auto\" to settle on the outcome of the work, or "+
				"ack = \"manual\" to settle it yourself, or remove settle_timeout.",
				policy.Mode),
			Subject: req.SettleTimeout.Range().Ptr(),
		}}
	}

	d, diags := c.ParseDuration(req.SettleTimeout)
	if diags.HasErrors() {
		return AckPolicy{}, diags
	}
	if d <= 0 {
		return AckPolicy{}, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("%s: settle_timeout must be positive", req.Receiver),
			Detail:   "A bound of zero or less would nack every message before it could be handled.",
			Subject:  req.SettleTimeout.Range().Ptr(),
		}}
	}
	policy.SettleTimeout = d
	return policy, nil
}

// ackSubject points a diagnostic at the `ack` attribute, or at the block when
// the problem is that `ack` was not written.
func ackSubject(req AckRequest) *hcl.Range {
	if req.ValueRange.Empty() {
		return &req.DefRange
	}
	return &req.ValueRange
}

// orList renders accepted values the way the other enum diagnostics do.
func orList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	switch len(quoted) {
	case 1:
		return quoted[0]
	case 2:
		return quoted[0] + " or " + quoted[1]
	default:
		return strings.Join(quoted[:len(quoted)-1], ", ") + ", or " + quoted[len(quoted)-1]
	}
}
