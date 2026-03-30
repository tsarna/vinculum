package after

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.uber.org/zap"
)

// AfterTrigger evaluates its action expression once, after a fixed delay from
// startup. It is the time-deferred analogue of trigger "once": get(trigger.<name>)
// returns null until the delay elapses, then the action result (or error) forever
// after.
//
// If shutdown occurs before the delay elapses, the action is skipped entirely —
// the trigger never fires and get(trigger.<name>) continues to return null.
type AfterTrigger struct {
	name   string
	config *cfg.Config
	delay  time.Duration
	expr   hcl.Expression

	mu     sync.RWMutex
	result cty.Value // cty.NilVal until fired
	err    error

	stopCh chan struct{}
	doneCh chan struct{}
}

// Get returns null until the action has fired, then the cached result or error.
// Implements Gettable.
func (t *AfterTrigger) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.err != nil {
		return cty.NilVal, t.err
	}
	if t.result == cty.NilVal {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return t.result, nil
}

// Start launches the background wait-then-fire goroutine. Implements Startable.
func (t *AfterTrigger) Start() error {
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	go t.run()
	return nil
}

// Stop interrupts the pending wait; if the delay has not yet elapsed the action
// is abandoned and will never fire. Implements Stoppable.
func (t *AfterTrigger) Stop() error {
	if t.stopCh == nil {
		return nil
	}
	close(t.stopCh)
	<-t.doneCh
	return nil
}

func (t *AfterTrigger) run() {
	defer close(t.doneCh)

	// Wait for the delay. If shutdown arrives first, abandon without firing.
	select {
	case <-time.After(t.delay):
	case <-t.stopCh:
		return
	}

	t.config.Logger.Debug("after trigger: firing", zap.String("name", t.name))

	evalCtx, err := hclutil.NewEvalContext(context.Background()).
		WithStringAttribute("trigger", "after").
		WithStringAttribute("name", t.name).
		BuildEvalContext(t.config.EvalCtx())
	if err != nil {
		t.config.Logger.Error("after trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		t.mu.Lock()
		t.err = err
		t.mu.Unlock()
		return
	}

	val, diags := t.expr.Value(evalCtx)
	t.mu.Lock()
	if diags.HasErrors() {
		t.err = diags
		t.config.Logger.Error("after trigger: action error",
			zap.String("name", t.name), zap.Error(diags))
	} else {
		t.result = val
		t.config.Logger.Debug("after trigger: action completed",
			zap.String("name", t.name), zap.Any("result", val))
	}
	t.mu.Unlock()
}

// --- Capsule type ---

var AfterCapsuleType = cty.CapsuleWithOps("after_trigger", reflect.TypeOf((*AfterTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("after_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "AfterTrigger"
	},
})

func NewAfterTriggerCapsule(t *AfterTrigger) cty.Value {
	return cty.CapsuleVal(AfterCapsuleType, t)
}

func GetAfterTriggerFromCapsule(val cty.Value) (*AfterTrigger, error) {
	if val.Type() != AfterCapsuleType {
		return nil, fmt.Errorf("expected after_trigger capsule, got %s", val.Type().FriendlyName())
	}
	t, ok := val.EncapsulatedValue().(*AfterTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an AfterTrigger, got %T", val.EncapsulatedValue())
	}
	return t, nil
}

// --- Block processing ---

type triggerAfterBody struct {
	Delay  hcl.Expression `hcl:"delay"`
	Action hcl.Expression `hcl:"action"`
}

func init() {
	cfg.RegisterTriggerType("after", cfg.TriggerRegistration{Process: processAfterTrigger, HasDependencyId: true})
}

func processAfterTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerAfterBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	delay, addDiags := config.ParseDuration(body.Delay)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	name := block.Labels[1]
	t := &AfterTrigger{
		name:   name,
		config: config,
		delay:  delay,
		expr:   body.Action,
	}

	config.CtyTriggerMap[name] = NewAfterTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
