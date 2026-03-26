package config

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/zclconf/go-cty/cty"
)

// OnceTrigger evaluates its action expression at most once, on the first call
// to Get(), and caches the result (or error) for all subsequent calls.
type OnceTrigger struct {
	once   sync.Once
	value  cty.Value
	err    error
	expr   hcl.Expression
	config *Config
	name   string
}

// Get evaluates the action expression on the first call and returns the cached
// result on all subsequent calls. Implements Gettable.
func (o *OnceTrigger) Get(_ []cty.Value) (cty.Value, error) {
	o.once.Do(func() {
		evalCtx, diags := NewContext(context.Background()).
			WithStringAttribute("trigger", "once").
			WithStringAttribute("name", o.name).
			BuildEvalContext(o.config.evalCtx)
		if diags.HasErrors() {
			o.err = diags
			return
		}

		val, diags := o.expr.Value(evalCtx)
		if diags.HasErrors() {
			o.err = diags
			return
		}
		o.value = val
	})
	if o.err != nil {
		return cty.NilVal, o.err
	}
	return o.value, nil
}

// --- Capsule type ---

var OnceCapsuleType = cty.CapsuleWithOps("once_trigger", reflect.TypeOf((*OnceTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("once_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "OnceTrigger"
	},
})

func NewOnceTriggerCapsule(o *OnceTrigger) cty.Value {
	return cty.CapsuleVal(OnceCapsuleType, o)
}

func GetOnceTriggerFromCapsule(val cty.Value) (*OnceTrigger, error) {
	if val.Type() != OnceCapsuleType {
		return nil, fmt.Errorf("expected once_trigger capsule, got %s", val.Type().FriendlyName())
	}
	o, ok := val.EncapsulatedValue().(*OnceTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an OnceTrigger, got %T", val.EncapsulatedValue())
	}
	return o, nil
}

// --- Block processing ---

type triggerOnceBody struct {
	Action hcl.Expression `hcl:"action"`
}

func processOnceTrigger(config *Config, block *hcl.Block, triggerDef *TriggerDefinition) hcl.Diagnostics {
	body := triggerOnceBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.evalCtx, &body)
	if diags.HasErrors() {
		return diags
	}

	name := block.Labels[1]
	o := &OnceTrigger{
		expr:   body.Action,
		config: config,
		name:   name,
	}

	config.CtyTriggerMap[name] = NewOnceTriggerCapsule(o)
	config.evalCtx.Variables["trigger"] = ctyObjectOrEmpty(config.CtyTriggerMap)

	return diags
}
