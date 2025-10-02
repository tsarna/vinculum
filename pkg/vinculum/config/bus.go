package config

import (
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/tsarna/vinculum-bus"
	"github.com/zclconf/go-cty/cty"
)

type BusDefinition struct {
	Name      string   `hcl:",label"`
	Type      *string  `hcl:"type,optional"`
	QueueSize *int     `hcl:"queue_size,optional"`
	Options   hcl.Body `hcl:",remain"`
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
	busBuilder := bus.NewEventBus().WithLogger(config.Logger).WithName(busDef.Name)
	if busDef.QueueSize != nil {
		busBuilder = busBuilder.WithBufferSize(*busDef.QueueSize)
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

	config.Buses[busDef.Name] = eventBus
	config.CtyBusMap[busDef.Name] = NewEventBusCapsule(eventBus)

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
