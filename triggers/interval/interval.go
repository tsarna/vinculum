package interval

import (
	"context"
	"fmt"
	"math/rand"
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

// IntervalTrigger repeatedly evaluates its action expression, sleeping for a
// dynamically computed delay between each run. Both the delay and the action
// are re-evaluated each iteration against a context that exposes ctx.run_count,
// ctx.last_result, and ctx.last_error, enabling adaptive schedules (e.g. "poll
// more often when the object is moving fast").
//
// get(trigger.<name>) returns the most recent action result, null before the
// first run, or an error if the most recent run failed.
type IntervalTrigger struct {
	name             string
	config           *cfg.Config
	delayExpr        hcl.Expression
	initialDelayExpr hcl.Expression // optional; used only on first iteration
	errorDelayExpr   hcl.Expression // optional; used when last run errored
	actionExpr       hcl.Expression
	stopWhenExpr     hcl.Expression // optional; trigger stops when this evaluates true
	jitter           float64        // fraction in [0,1]: actual delay uniform in [delay*(1-jitter/2), delay*(1+jitter/2)]

	mu         sync.RWMutex
	runCount   int64     // number of completed action evaluations
	lastResult cty.Value // cty.NilVal until first run
	lastError  error

	stopCh chan struct{}
	doneCh chan struct{}
}

// Get returns the most recent action result, null if the action has not yet
// run, or an error if the most recent evaluation failed. Implements Gettable.
func (t *IntervalTrigger) Get(_ []cty.Value) (cty.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastError != nil {
		return cty.NilVal, t.lastError
	}
	if t.lastResult == cty.NilVal {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return t.lastResult, nil
}

// Start launches the background interval loop. Implements Startable.
func (t *IntervalTrigger) Start() error {
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	go t.run()
	return nil
}

// Stop signals the interval loop to exit and waits for it to finish.
// Implements Stoppable.
func (t *IntervalTrigger) Stop() error {
	if t.stopCh == nil {
		return nil
	}
	close(t.stopCh)
	<-t.doneCh
	return nil
}

// buildEvalContext constructs the per-iteration HCL eval context, exposing
// ctx.trigger, ctx.name, ctx.run_count, ctx.last_result, and ctx.last_error.
func (t *IntervalTrigger) buildEvalContext(runCount int64, lastResult cty.Value, lastErr error) (*hcl.EvalContext, error) {
	lastResultVal := lastResult
	if lastResultVal == cty.NilVal {
		lastResultVal = cty.NullVal(cty.DynamicPseudoType)
	}
	lastErrStr := cty.NullVal(cty.String)
	if lastErr != nil {
		lastErrStr = cty.StringVal(lastErr.Error())
	}
	return hclutil.NewEvalContext(context.Background()).
		WithStringAttribute("trigger", "interval").
		WithStringAttribute("name", t.name).
		WithInt64Attribute("run_count", runCount).
		WithAttribute("last_result", lastResultVal).
		WithAttribute("last_error", lastErrStr).
		BuildEvalContext(t.config.EvalCtx())
}

// applyJitter returns a duration drawn uniformly from
// [dur*(1-jitter/2), dur*(1+jitter/2)], preserving the average delay.
func (t *IntervalTrigger) applyJitter(dur time.Duration) time.Duration {
	if t.jitter == 0 || dur == 0 {
		return dur
	}
	minDur := time.Duration(float64(dur) * (1 - t.jitter/2))
	maxDur := time.Duration(float64(dur) * (1 + t.jitter/2))
	if spread := int64(maxDur - minDur); spread > 0 {
		return minDur + time.Duration(rand.Int63n(spread)) //nolint:gosec // non-crypto jitter
	}
	return dur
}

func (t *IntervalTrigger) run() {
	defer close(t.doneCh)

	for {
		// Snapshot current state for this iteration's eval context.
		t.mu.RLock()
		runCount := t.runCount
		lastResult := t.lastResult
		lastErr := t.lastError
		t.mu.RUnlock()

		evalCtx, err := t.buildEvalContext(runCount, lastResult, lastErr)
		if err != nil {
			t.config.Logger.Error("interval trigger: error building eval context",
				zap.String("name", t.name), zap.Error(err))
			return
		}

		// Pick the delay expression for this iteration.
		delayExpr := t.delayExpr
		if runCount == 0 && cfg.IsExpressionProvided(t.initialDelayExpr) {
			delayExpr = t.initialDelayExpr
		} else if lastErr != nil && cfg.IsExpressionProvided(t.errorDelayExpr) {
			delayExpr = t.errorDelayExpr
		}

		// Evaluate delay in this iteration's context (may depend on run_count etc).
		delayVal, addDiags := delayExpr.Value(evalCtx)
		if addDiags.HasErrors() {
			t.config.Logger.Error("interval trigger: error evaluating delay",
				zap.String("name", t.name), zap.Error(addDiags))
			return
		}
		dur, err := cfg.ParseDurationFromValue(delayVal)
		if err != nil {
			t.config.Logger.Error("interval trigger: invalid delay value",
				zap.String("name", t.name), zap.Error(err))
			return
		}

		dur = t.applyJitter(dur)

		// Wait for the delay or a shutdown signal.
		select {
		case <-time.After(dur):
		case <-t.stopCh:
			return
		}

		// Evaluate the action.
		t.config.Logger.Debug("interval trigger: executing action",
			zap.String("name", t.name), zap.Int64("run_count", runCount))
		actionVal, actionDiags := t.actionExpr.Value(evalCtx)
		var actionErr error
		if actionDiags.HasErrors() {
			actionErr = actionDiags
			actionVal = cty.NilVal
			t.config.Logger.Error("interval trigger: action error",
				zap.String("name", t.name), zap.Error(actionErr))
		} else {
			t.config.Logger.Debug("interval trigger: action completed",
				zap.String("name", t.name), zap.Int64("run_count", runCount), zap.Any("result", actionVal))
		}

		t.mu.Lock()
		t.runCount++
		newRunCount := t.runCount
		t.lastResult = actionVal
		t.lastError = actionErr
		t.mu.Unlock()

		// Evaluate stop_when (if provided) against the post-action state.
		if cfg.IsExpressionProvided(t.stopWhenExpr) {
			stopCtx, stopErr := t.buildEvalContext(newRunCount, actionVal, actionErr)
			if stopErr != nil {
				t.config.Logger.Error("interval trigger: error building stop_when context",
					zap.String("name", t.name), zap.Error(stopErr))
				continue
			}
			stopVal, stopDiags := t.stopWhenExpr.Value(stopCtx)
			if stopDiags.HasErrors() {
				t.config.Logger.Error("interval trigger: error evaluating stop_when",
					zap.String("name", t.name), zap.Error(stopDiags))
				continue
			}
			if stopVal.IsKnown() && !stopVal.IsNull() && stopVal.Type() == cty.Bool && stopVal.True() {
				t.config.Logger.Debug("interval trigger: stop_when satisfied, stopping",
					zap.String("name", t.name))
				return
			}
		}
	}
}

// --- Capsule type ---

var IntervalCapsuleType = cty.CapsuleWithOps("interval_trigger", reflect.TypeOf((*IntervalTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("interval_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "IntervalTrigger"
	},
})

func NewIntervalTriggerCapsule(t *IntervalTrigger) cty.Value {
	return cty.CapsuleVal(IntervalCapsuleType, t)
}

func GetIntervalTriggerFromCapsule(val cty.Value) (*IntervalTrigger, error) {
	if val.Type() != IntervalCapsuleType {
		return nil, fmt.Errorf("expected interval_trigger capsule, got %s", val.Type().FriendlyName())
	}
	t, ok := val.EncapsulatedValue().(*IntervalTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an IntervalTrigger, got %T", val.EncapsulatedValue())
	}
	return t, nil
}

// --- Block processing ---

type triggerIntervalBody struct {
	Delay        hcl.Expression `hcl:"delay"`
	InitialDelay hcl.Expression `hcl:"initial_delay,optional"`
	ErrorDelay   hcl.Expression `hcl:"error_delay,optional"`
	Jitter       *float64       `hcl:"jitter,optional"`
	StopWhen     hcl.Expression `hcl:"stop_when,optional"`
	Action       hcl.Expression `hcl:"action"`
}

func init() {
	cfg.RegisterTriggerType("interval", cfg.TriggerRegistration{Process: processIntervalTrigger, HasDependencyId: true})
}

func processIntervalTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerIntervalBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	jitter := 0.0
	if body.Jitter != nil {
		if *body.Jitter < 0 || *body.Jitter > 1 {
			return hcl.Diagnostics{&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Invalid jitter value",
				Detail:   fmt.Sprintf("jitter must be between 0.0 and 1.0, got %f", *body.Jitter),
				Subject:  block.DefRange.Ptr(),
			}}
		}
		jitter = *body.Jitter
	}

	name := block.Labels[1]
	t := &IntervalTrigger{
		name:             name,
		config:           config,
		delayExpr:        body.Delay,
		initialDelayExpr: body.InitialDelay,
		errorDelayExpr:   body.ErrorDelay,
		actionExpr:       body.Action,
		stopWhenExpr:     body.StopWhen,
		jitter:           jitter,
	}

	config.CtyTriggerMap[name] = NewIntervalTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
