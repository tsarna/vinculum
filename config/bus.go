package config

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
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

	mainBusDefined bool
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

func GetEventBusFromExpression(config *Config, busExpr hcl.Expression) (bus.EventBus, hcl.Diagnostics) {
	busCapsule, diags := busExpr.Value(config.evalCtx)
	if diags.HasErrors() {
		return nil, diags
	}

	if busCapsule.IsNull() {
		if bus, ok := config.Buses["main"]; ok {
			return bus, nil
		} else {
			return nil, hcl.Diagnostics{
				&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Main bus not found",
				},
			}
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

` + "`bus.main`" + ` always exists implicitly and does not need to be declared.`,
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

// TODO: use preprocess hook to see if the main bus is defined, and automatically define it if not

func (h *BusBlockHandler) GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics) {
	return "bus." + block.Labels[0], nil
}

func (h *BusBlockHandler) Preprocess(block *hcl.Block) hcl.Diagnostics {
	if block.Labels[0] == "main" {
		h.mainBusDefined = true
	}

	return nil
}

func (h *BusBlockHandler) FinishPreprocessing(config *Config) hcl.Diagnostics {
	config.BusCapsuleType = EventBusCapsuleType

	if !h.mainBusDefined {
		busDef := BusDefinition{
			Name: "main",
		}

		return h.BuildEventBus(config, &busDef, &hcl.Range{})
	}

	return nil
}

func (h *BusBlockHandler) Process(config *Config, block *hcl.Block) hcl.Diagnostics {
	busDef := BusDefinition{}
	diags := gohcl.DecodeBody(block.Body, config.evalCtx, &busDef)
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
