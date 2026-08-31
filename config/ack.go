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
	Doc: "`auto` settles as soon as delivery returns without error, which is fast but " +
		"loses a message whose handling fails after that point — including whenever " +
		"`queue_size` is set, since delivery then returns at the moment the message is " +
		"queued. `manual` settles nothing until the configuration calls " +
		"`inbound::ack()` or `inbound::nack()`, and requires `settle_timeout` to bound " +
		"how long a message may go unsettled.",
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
var SettleTimeoutAttr = AttrMeta{
	Summary: "How long a message may go unsettled before it is nacked automatically.",
	Doc: "Required with `ack = \"manual\"`, and rejected without it. An unsettled message " +
		"costs something for as long as it is outstanding — an SQS visibility window, a " +
		"RabbitMQ prefetch slot, a Kafka partition's committable offset — and forgetting " +
		"to call `inbound::ack()` should be diagnosable rather than a slow stall. On " +
		"expiry the message is nacked and the failure is logged against the receiver.",
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

	// Extra names the mode this receiver accepts beyond auto and manual —
	// AckPeriodic on kafka, AckNone on rabbitmq — or is empty.
	Extra string

	// ManualPending, when non-empty, says why this receiver cannot honour
	// manual settle yet. It becomes the detail of the diagnostic, so it should
	// say what is missing and what to use instead.
	ManualPending string

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
		if req.ManualPending != "" {
			return AckPolicy{}, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("%s: ack \"manual\" is not implemented yet", req.Receiver),
				Detail:   req.ManualPending,
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

	hasTimeout := IsExpressionProvided(req.SettleTimeout)
	if !policy.Manual() {
		if hasTimeout {
			// Not merely redundant: it states a bound on something that is
			// already settled by the time the bound could apply, so honouring
			// it would be impossible and ignoring it would be a lie.
			return AckPolicy{}, hcl.Diagnostics{{
				Severity: hcl.DiagError,
				Summary:  fmt.Sprintf("%s: settle_timeout applies only to ack = \"manual\"", req.Receiver),
				Detail: fmt.Sprintf("With ack = %q the message is settled for you when delivery "+
					"returns, so there is no unsettled message for this to bound. Either set "+
					"ack = \"manual\" and settle the message yourself, or remove settle_timeout.",
					policy.Mode),
				Subject: req.SettleTimeout.Range().Ptr(),
			}}
		}
		return policy, nil
	}

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
