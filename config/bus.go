package config

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/hcl/v2"
	richcty "github.com/tsarna/rich-cty-types"
	bus "github.com/tsarna/vinculum-bus"
	"github.com/zclconf/go-cty/cty"
)

type BusDefinition struct {
	Name          string         `hcl:"name,label"`
	Type          *string        `hcl:"type,optional"`
	QueueSize     *int           `hcl:"queue_size,optional"`
	Undeliverable bool           `hcl:"undeliverable,optional"`
	Metrics       hcl.Expression `hcl:"metrics,optional"`
	Tracing       hcl.Expression `hcl:"tracing,optional"`
}

type BusBlockHandler struct {
	BlockHandlerBase
	BackendDeps
}

// EventBusCapsuleType is a cty capsule type for wrapping EventBus instances
var EventBusCapsuleType = cty.CapsuleWithOps("eventbus", reflect.TypeOf((*any)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("eventbus(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "eventbus"
	},
})

// NewEventBusCapsule creates a new cty capsule value wrapping an EventBus
func NewEventBusCapsule(eventBus bus.EventBus) cty.Value {
	return cty.CapsuleVal(EventBusCapsuleType, eventBus)
}

// BusHandle is the value behind `bus.<name>`: the event bus itself, plus the
// `get()` members that read what it is doing.
//
// It embeds the bus rather than wrapping it by hand, so a handle *is* an
// EventBus and a Subscriber and everything that already accepted one still
// does. Exactly one handle exists per bus, and it is what is stored in both
// Config.Buses and the capsule, because a subscriber's identity is its map key
// inside the bus.
type BusHandle struct {
	bus.EventBus

	name string
}

// Name returns the bus's block name.
func (h *BusHandle) Name() string { return h.name }

// Unwrap returns the bus this handle stands for, so a settle point asking what
// a delivery's return will mean gets the bus's answer — deferred — rather than
// this handle's silence.
//
// The embed above is why this is needed and why it is easy to miss. A handle
// gets every method in the bus.EventBus *interface* for free, and
// DeliveryDisposition is not one of them: it belongs to the concrete bus, so it
// is not promoted through an embedded interface. Nothing fails to compile, and
// the handle simply reports that its return means the work is done — so a queue
// in front of a bus acknowledged every message at the moment it was enqueued,
// which is the one defect this whole mechanism exists to prevent.
func (h *BusHandle) Unwrap() bus.Subscriber { return h.EventBus }

// busMembers is the set `get(bus.<name>, …)` answers, in the order a diagnostic
// should list them.
var busMembers = []string{"queue_depth", "queue_capacity", "queue_ratio", "dropped", "undelivered"}

// Get implements richcty.Gettable: the bus's own backpressure numbers.
//
// These are polled, deliberately, and the bus is not watchable on them: depth
// changes on the hottest path in the system, and a health feature has no
// business adding work there. A condition that wants to react to saturation
// samples into a `var` on an interval — see doc/health.md.
func (h *BusHandle) Get(_ context.Context, args []cty.Value) (cty.Value, error) {
	if len(args) == 0 {
		return cty.NilVal, fmt.Errorf("bus get: which member? one of %s",
			strings.Join(busMembers, ", "))
	}
	if args[0].Type() != cty.String {
		return cty.NilVal, fmt.Errorf("bus get: member argument must be a string")
	}

	switch member := args[0].AsString(); member {
	case "queue_depth":
		return cty.NumberIntVal(int64(h.QueueDepth())), nil

	case "queue_capacity":
		return cty.NumberIntVal(int64(h.QueueCapacity())), nil

	case "queue_ratio":
		// Derived, and included because it is what a threshold is written
		// against: unlike a depth, it is comparable across buses of different
		// sizes. Both are offered because the absolute numbers are what an
		// operator wants in a log line, and a ratio cannot give them back.
		capacity := h.QueueCapacity()
		if capacity <= 0 {
			return cty.NumberFloatVal(0), nil
		}
		return cty.NumberFloatVal(float64(h.QueueDepth()) / float64(capacity)), nil

	case "dropped":
		return cty.NumberUIntVal(h.DroppedTotal()), nil

	case "undelivered":
		return cty.NumberUIntVal(h.UndeliveredTotal()), nil

	default:
		return cty.NilVal, fmt.Errorf("bus get: no member %q; expected one of %s",
			member, strings.Join(busMembers, ", "))
	}
}

var _ richcty.Gettable = (*BusHandle)(nil)

// GetEventBusFromCapsule extracts an EventBus from a cty capsule value
func GetEventBusFromCapsule(val cty.Value) (bus.EventBus, error) {
	if val.Type() != EventBusCapsuleType {
		return nil, fmt.Errorf("expected EventBus capsule, got %s", val.Type().FriendlyName())
	}

	encapsulated := val.EncapsulatedValue()
	eventBus, ok := encapsulated.(bus.EventBus)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an EventBus, got %T", encapsulated)
	}
	return eventBus, nil
}

// UndeclaredBusDiags reports a `bus.<name>` that names no declared bus, in the
// wording the deferred-reference checker uses for the same mistake.
//
// The checker in exprcheck.go covers only attributes evaluated at event time. A
// bus reference is the opposite: `target` on a subscription and `bus` on a vws
// or websocket server resolve as the block is processed, so without this they
// get HCL's generic "This object does not have an attribute named "main"",
// which names neither what `bus` is nor what to do about it.
//
// It returns nil when the expression is not a plain bus reference or when every
// bus it names exists, so the caller falls back to the diagnostics HCL gave it.
func UndeclaredBusDiags(config *Config, expr hcl.Expression) hcl.Diagnostics {
	var diags hcl.Diagnostics

	for _, traversal := range expr.Variables() {
		if traversal.RootName() != "bus" {
			continue
		}
		step, ok := traversal[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		if _, found := config.Buses[step.Name]; found {
			continue
		}

		detail := fmt.Sprintf("Declared bus names are: %s.", joinNames(sortedKeys(config.Buses)))
		if len(config.Buses) == 0 {
			detail = "No bus is declared by this configuration. Declare one with `bus \"" +
				step.Name + "\" {}`."
		}

		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  fmt.Sprintf("No bus named %q", step.Name),
			Detail:   detail,
			Subject:  traversal.SourceRange().Ptr(),
		})
	}

	return diags
}

func GetEventBusFromExpression(config *Config, busExpr hcl.Expression) (bus.EventBus, hcl.Diagnostics) {
	busCapsule, diags := busExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		if better := UndeclaredBusDiags(config, busExpr); better.HasErrors() {
			return nil, better
		}
		return nil, diags
	}

	// An omitted attribute never reaches here — every caller's is required, and
	// DecodeBody reports it missing. What does reach here is an expression that
	// evaluated to null, which is to say `bus = null` written out, or a
	// reference to something that is.
	if busCapsule.IsNull() {
		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "No bus given",
				Detail:   "This attribute names the bus to use, and evaluated to null. Name a declared bus, such as `bus.main`.",
				Subject:  busExpr.Range().Ptr(),
			},
		}
	}

	bus, err := GetEventBusFromCapsule(busCapsule)
	if err != nil {
		exprRange := busExpr.Range()

		return nil, hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to get bus from expression",
				Detail:   err.Error(),
				Subject:  &exprRange,
			},
		}
	}

	return bus, nil
}

func NewBusBlockHandler() *BusBlockHandler {
	return &BusBlockHandler{}
}

// Schema describes the bus block for `vinculum schema`.
func (h *BusBlockHandler) Schema() TypeSchema { return busSchema }

var busSchema = TypeSchema{
	Sample:  &BusDefinition{},
	Summary: "An in-process publish/subscribe event bus.",
	Doc: `Declares an event bus, available in expressions as ` + "`bus.<name>`" + `.

Every bus is declared, ` + "`bus.main`" + ` included: the name carries no special
meaning and nothing creates one on your behalf. A configuration that declares no
bus has none, and starts no delivery goroutine.`,
	Attrs: map[string]AttrMeta{
		"type": {
			Summary: "Bus implementation to use.",
			Doc:     "Reserved for alternative bus implementations; omit for the default in-process bus.",
		},
		"queue_size": {
			Summary: "Maximum messages queued before messages are dropped.",
			Doc: "A message published when the queue is full is discarded, and counted in " +
				"`get(bus.<name>, \"dropped\")`.",
			Default: "1000",
		},
		"undeliverable": {
			Summary: "Republish messages no subscriber matched under `$undeliverable`.",
			Doc: `A message that matches no subscriber is normally discarded in silence,
which is the right default: publishing to a topic nobody wants is ordinary
pub/sub. Set this on a bus where an unmatched message means something is wrong —
a ` + "`topics`" + ` typo, a ` + "`vinculum_topic`" + ` expression that came out wrong, a
subscription left ` + "`disabled`" + ` after a deploy.

The message is republished under the reserved topic ` + "`$undeliverable`" + ` carrying its
original payload and context, with the topic that failed to route available to the
handler as ` + "`ctx.undeliverable_topic`" + `. A ` + "`$`" + `-prefixed topic is never itself
republished, so an unhandled ` + "`$undeliverable`" + ` cannot loop.

It is not free: every unmatched publish becomes a second publish on the same
single delivery goroutine. The ` + "`undelivered`" + ` counter — always on, readable as
` + "`get(bus.<name>, \"undelivered\")`" + ` — is the diagnostic; this is the remedy.`,
			Default: "false",
		},
		"metrics": MetricsAttr,
		"tracing": {
			Summary: "Where to report bus traces.",
			Doc:     "A `client \"otlp\"` block. When set, each publish and delivery is wrapped in an OTel span. Auto-wires to the default when omitted.",
			Hint:    HintTracingRef,
		},
	},
}

func (h *BusBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "bus." + block.Labels[0], nil
}

// GetBlockDependencies adds the implicit backend dependency for a bus that does
// not name both of its backends. A bus is the most visible case of the bug this
// prevents: `bus "main" {}` written above the `server "metrics"` block reported
// nothing, and the same two blocks in the other order reported everything.
func (h *BusBlockHandler) GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics) {
	return h.AddBackendDeps(ExtractBlockDependencies(block), block, "metrics", "tracing"), nil
}

// FinishPreprocessing establishes the `bus` root before any block is processed.
//
// It is set unconditionally, so a configuration with no bus block has an empty
// `bus` object rather than no `bus` at all. That is the difference between
// `bus.main` reporting "No bus is declared by this configuration" and reporting
// that `bus` is not a name the language has.
func (h *BusBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	config.BusCapsuleType = EventBusCapsuleType
	config.Constants["bus"] = cty.ObjectVal(config.CtyBusMap)

	return nil
}

func (h *BusBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	busDef := BusDefinition{}
	diags := DecodeBody(block.Body, config.evalCtx, &busDef)
	if diags.HasErrors() {
		return diags
	}

	// Manually set the name from the block label since DecodeBody doesn't handle labels
	if len(block.Labels) > 0 {
		busDef.Name = block.Labels[0]
	}

	addDiags := h.BuildEventBus(config, &busDef, &block.DefRange)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	return nil
}

func (h *BusBlockHandler) BuildEventBus(config *Config, busDef *BusDefinition, defRange *hcl.Range) hcl.Diagnostics {
	busBuilder := bus.NewEventBus().
		WithLogger(config.Logger).
		WithName(busDef.Name).
		WithUndeliverable(busDef.Undeliverable)
	if busDef.QueueSize != nil {
		busBuilder = busBuilder.WithBufferSize(*busDef.QueueSize)
	}

	mp, diags := ResolveMeterProvider(config, busDef.Metrics)
	if diags.HasErrors() {
		return diags
	}
	if mp != nil {
		busBuilder = busBuilder.WithMeterProvider(mp)
	}

	tp, tracingDiags := config.ResolveTracerProvider(busDef.Tracing)
	if tracingDiags.HasErrors() {
		return tracingDiags
	}
	if tp != nil {
		busBuilder = busBuilder.WithTracerProvider(tp)
	}

	eventBus, err := busBuilder.Build()
	if err != nil {
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to build event bus",
				Detail:   err.Error(),
				Subject:  defRange,
			},
		}
	}

	// One handle, stored everywhere the bus is reachable: a subscriber's
	// identity inside the bus is the interface value itself, so two wrappers
	// around one bus would be two different subscribers.
	handle := &BusHandle{EventBus: eventBus, name: busDef.Name}

	config.Buses[busDef.Name] = handle
	config.CtyBusMap[busDef.Name] = NewEventBusCapsule(handle)

	// A published message sits in the bus's channel until the dispatch loop
	// reaches it, so shutdown has to wait for that channel rather than exit
	// past it. No Close: Stop abandons what is queued instead of dispatching
	// it, so the only way to empty a bus is to let it run.
	config.InFlight = append(config.InFlight, InFlightHolder{
		Name:       "bus." + busDef.Name,
		QueueDepth: eventBus.QueueDepth,
	})

	// Attributes can't be added on the fly, do we have to redefine the object to add each new bus
	config.Constants["bus"] = cty.ObjectVal(config.CtyBusMap)

	err = eventBus.Start()
	if err != nil {
		return hcl.Diagnostics{
			&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Failed to start event bus",
				Detail:   err.Error(),
			},
		}
	}

	return nil
}
