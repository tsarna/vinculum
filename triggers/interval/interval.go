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
	"go.opentelemetry.io/otel/trace"
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
//
// set(trigger.<name>, duration) restarts the trigger with the given delay,
// resetting the run count. If the trigger has stopped (via stop_when or
// repeat=false), set() revives it.
//
// set(trigger.<name>) with no duration restarts using the configured delay
// expression; returns an error if no delay was configured.
//
// reset(trigger.<name>) cancels any pending timer and puts the trigger into
// a dormant state, waiting for the next set() call.
//
// If delay is omitted from the configuration, the trigger starts dormant and
// waits for the first set() call before firing.
type IntervalTrigger struct {
	name             string
	config           *cfg.Config
	delayExpr        hcl.Expression
	initialDelayExpr hcl.Expression // optional; used only on first iteration
	errorDelayExpr   hcl.Expression // optional; used when last run errored
	actionExpr       hcl.Expression
	stopWhenExpr     hcl.Expression // optional; trigger stops when this evaluates true
	jitter           float64        // fraction in [0,1]: actual delay uniform in [delay*(1-jitter/2), delay*(1+jitter/2)]
	repeat           bool           // if true (default), loop; if false, fire once then go dormant
	tracerProvider   trace.TracerProvider

	mu            sync.RWMutex
	runCount      int64          // number of completed action evaluations
	lastResult    cty.Value      // cty.NilVal until first run
	lastError     error
	delayOverride *time.Duration // non-nil when set() provided an explicit duration

	setCh   chan *time.Duration // signals goroutine to restart; payload is override or nil
	resetCh chan struct{}       // signals goroutine to go dormant
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// Count returns the number of completed action evaluations. Implements
// richcty.Countable so count(trigger.<name>) is callable from any expression.
func (t *IntervalTrigger) Count(_ context.Context) (int64, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.runCount, nil
}

// Get returns the most recent action result, null if the action has not yet
// run, or an error if the most recent evaluation failed. Implements Gettable.
func (t *IntervalTrigger) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
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

// Set restarts the interval trigger, optionally with a new delay duration.
// If called with a duration argument, that duration overrides the configured
// delay expression until cleared by Reset(). If called with no arguments,
// the configured delay expression is used (returns an error if no delay was
// configured). Resets the run count so stop_when / repeat=false cycles work
// again. Implements Settable.
func (t *IntervalTrigger) Set(_ context.Context, args []cty.Value) (cty.Value, error) {
	var override *time.Duration
	if len(args) > 0 {
		dur, err := cfg.ParseDurationFromValue(args[0])
		if err != nil {
			return cty.NilVal, fmt.Errorf("interval trigger %q: invalid duration for set(): %w", t.name, err)
		}
		override = &dur
	} else if !cfg.IsExpressionProvided(t.delayExpr) {
		return cty.NilVal, fmt.Errorf("interval trigger %q: set() with no duration requires a configured delay", t.name)
	}

	t.mu.Lock()
	t.delayOverride = override
	t.runCount = 0
	t.lastError = nil
	t.mu.Unlock()

	// Signal the goroutine non-blocking.
	select {
	case t.setCh <- override:
	default:
	}

	if override != nil {
		return cty.StringVal(override.String()), nil
	}
	return cty.NullVal(cty.DynamicPseudoType), nil
}

// Reset cancels any pending timer and puts the trigger into a dormant state,
// waiting for the next set() call. Clears the delay override, run count, and
// last result/error. Implements Resettable.
func (t *IntervalTrigger) Reset(_ context.Context) error {
	t.mu.Lock()
	t.delayOverride = nil
	t.runCount = 0
	t.lastResult = cty.NilVal
	t.lastError = nil
	t.mu.Unlock()

	if t.resetCh != nil {
		select {
		case t.resetCh <- struct{}{}:
		default:
		}
	}
	return nil
}

// Start initialises the control channels. Implements Startable.
func (t *IntervalTrigger) Start() error {
	t.setCh = make(chan *time.Duration, 1)
	t.resetCh = make(chan struct{}, 1)
	t.stopCh = make(chan struct{})
	return nil
}

// PostStart launches the background interval loop. Called after all Startable
// components have completed, so buses and clients are ready on first invocation.
// Implements PostStartable.
func (t *IntervalTrigger) PostStart() error {
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
	if t.doneCh != nil {
		<-t.doneCh
	}
	return nil
}

// buildEvalContext constructs the per-iteration HCL eval context, exposing
// ctx.trigger, ctx.name, ctx.run_count, ctx.last_result, and ctx.last_error.
func (t *IntervalTrigger) buildEvalContext(ctx context.Context, runCount int64, lastResult cty.Value, lastErr error) (*hcl.EvalContext, error) {
	lastResultVal := lastResult
	if lastResultVal == cty.NilVal {
		lastResultVal = cty.NullVal(cty.DynamicPseudoType)
	}
	lastErrStr := cty.NullVal(cty.String)
	if lastErr != nil {
		lastErrStr = cty.StringVal(lastErr.Error())
	}
	return hclutil.NewEvalContext(ctx).
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

// computeDelay determines the delay for the current iteration. It checks the
// delay override (set via Set()) first, then falls back to the configured delay
// expression. Returns a negative duration and error if no delay is available.
func (t *IntervalTrigger) computeDelay(evalCtx *hcl.EvalContext) (time.Duration, error) {
	t.mu.RLock()
	override := t.delayOverride
	runCount := t.runCount
	lastErr := t.lastError
	t.mu.RUnlock()

	if override != nil {
		return t.applyJitter(*override), nil
	}

	// Pick the delay expression for this iteration.
	delayExpr := t.delayExpr
	if runCount == 0 && cfg.IsExpressionProvided(t.initialDelayExpr) {
		delayExpr = t.initialDelayExpr
	} else if lastErr != nil && cfg.IsExpressionProvided(t.errorDelayExpr) {
		delayExpr = t.errorDelayExpr
	}

	if !cfg.IsExpressionProvided(delayExpr) {
		return -1, fmt.Errorf("no delay configured and no override set")
	}

	delayVal, diags := delayExpr.Value(evalCtx)
	if diags.HasErrors() {
		return -1, fmt.Errorf("error evaluating delay: %w", diags)
	}
	dur, err := cfg.ParseDurationFromValue(delayVal)
	if err != nil {
		return -1, fmt.Errorf("invalid delay value: %w", err)
	}
	return t.applyJitter(dur), nil
}

// shouldStop checks whether the trigger should stop after this iteration:
// either repeat is false, or stop_when evaluates to true.
func (t *IntervalTrigger) shouldStop(runCount int64, lastResult cty.Value, lastErr error) bool {
	if !t.repeat {
		return true
	}

	if cfg.IsExpressionProvided(t.stopWhenExpr) {
		evalCtx, err := t.buildEvalContext(context.Background(), runCount, lastResult, lastErr)
		if err != nil {
			t.config.UserLogger.Error("interval trigger: error building stop_when context",
				zap.String("name", t.name), zap.Error(err))
			return false
		}
		stopVal, stopDiags := t.stopWhenExpr.Value(evalCtx)
		if stopDiags.HasErrors() {
			t.config.UserLogger.Error("interval trigger: error evaluating stop_when",
				zap.String("name", t.name), zap.Error(stopDiags))
			return false
		}
		if stopVal.IsKnown() && !stopVal.IsNull() && stopVal.Type() == cty.Bool && stopVal.True() {
			return true
		}
	}

	return false
}

// shouldRemainStopped checks whether the trigger should stay dormant after a
// set() call. Returns true if stop conditions are still met (e.g. stop_when
// is still true and run count was not reset by set()).
func (t *IntervalTrigger) shouldRemainStopped() bool {
	if !t.repeat {
		// repeat=false stops after each fire, but set() resets runCount to 0
		// so it's always revivable.
		return false
	}

	if cfg.IsExpressionProvided(t.stopWhenExpr) {
		t.mu.RLock()
		runCount := t.runCount
		lastResult := t.lastResult
		lastErr := t.lastError
		t.mu.RUnlock()

		evalCtx, err := t.buildEvalContext(context.Background(), runCount, lastResult, lastErr)
		if err != nil {
			return false
		}
		stopVal, stopDiags := t.stopWhenExpr.Value(evalCtx)
		if stopDiags.HasErrors() {
			return false
		}
		if stopVal.IsKnown() && !stopVal.IsNull() && stopVal.Type() == cty.Bool && stopVal.True() {
			return true
		}
	}

	return false
}

// waitForRevival blocks until a set() call revives the trigger or Stop() is
// called. Returns true if the goroutine should exit (shutdown), false if
// revived.
func (t *IntervalTrigger) waitForRevival() bool {
	t.config.Logger.Debug("interval trigger: dormant, waiting for set()",
		zap.String("name", t.name))
	for {
		select {
		case <-t.setCh:
			if !t.shouldRemainStopped() {
				t.config.Logger.Debug("interval trigger: revived by set()",
					zap.String("name", t.name))
				return false
			}
		case <-t.resetCh:
			// Already dormant; just drain the signal.
		case <-t.stopCh:
			return true
		}
	}
}

// safeTimerReset stops a timer and resets it to d, correctly draining
// the channel if the timer had already expired.
func safeTimerReset(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

func (t *IntervalTrigger) run() {
	defer close(t.doneCh)

	// If no delay is configured, start dormant.
	if !cfg.IsExpressionProvided(t.delayExpr) {
		if t.waitForRevival() {
			return
		}
	}

	for {
		// Snapshot current state for this iteration's eval context.
		t.mu.RLock()
		runCount := t.runCount
		lastResult := t.lastResult
		lastErr := t.lastError
		t.mu.RUnlock()

		// Build eval context for delay computation.
		evalCtx, err := t.buildEvalContext(context.Background(), runCount, lastResult, lastErr)
		if err != nil {
			t.config.UserLogger.Error("interval trigger: error building eval context",
				zap.String("name", t.name), zap.Error(err))
			return
		}

		dur, err := t.computeDelay(evalCtx)
		if err != nil {
			t.config.UserLogger.Error("interval trigger: error computing delay",
				zap.String("name", t.name), zap.Error(err))
			return
		}

		// Wait for the delay, a set/reset signal, or shutdown.
		timer := time.NewTimer(dur)
		select {
		case <-timer.C:
			// Proceed to execute the action.
		case <-t.setCh:
			timer.Stop()
			continue // restart loop with new delay
		case <-t.resetCh:
			timer.Stop()
			if t.waitForRevival() {
				return
			}
			continue
		case <-t.stopCh:
			timer.Stop()
			return
		}

		// Each action execution gets its own root span.
		spanCtx, stopSpan := hclutil.StartTriggerSpan(context.Background(), t.tracerProvider, "interval", t.name)
		actionEvalCtx, err := t.buildEvalContext(spanCtx, runCount, lastResult, lastErr)
		if err != nil {
			t.config.UserLogger.Error("interval trigger: error building action eval context",
				zap.String("name", t.name), zap.Error(err))
			stopSpan(err)
			return
		}

		// Evaluate the action.
		t.config.Logger.Debug("interval trigger: executing action",
			zap.String("name", t.name), zap.Int64("run_count", runCount))
		actionVal, actionDiags := t.actionExpr.Value(actionEvalCtx)
		var actionErr error
		if actionDiags.HasErrors() {
			actionErr = actionDiags
			actionVal = cty.NilVal
			t.config.UserLogger.Error("interval trigger: action error",
				zap.String("name", t.name), zap.Error(actionErr))
		} else {
			t.config.Logger.Debug("interval trigger: action completed",
				zap.String("name", t.name), zap.Int64("run_count", runCount), zap.Any("result", actionVal))
		}

		stopSpan(actionErr)

		t.mu.Lock()
		t.runCount++
		newRunCount := t.runCount
		t.lastResult = actionVal
		t.lastError = actionErr
		t.mu.Unlock()

		// Check whether to stop after this iteration.
		if t.shouldStop(newRunCount, actionVal, actionErr) {
			t.config.Logger.Debug("interval trigger: stop condition met, going dormant",
				zap.String("name", t.name))
			if t.waitForRevival() {
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
	Delay        hcl.Expression `hcl:"delay,optional"`
	InitialDelay hcl.Expression `hcl:"initial_delay,optional"`
	ErrorDelay   hcl.Expression `hcl:"error_delay,optional"`
	Jitter       *float64       `hcl:"jitter,optional"`
	Repeat       *bool          `hcl:"repeat,optional"`
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

	hasDelay := cfg.IsExpressionProvided(body.Delay)

	// initial_delay and error_delay require delay to be set.
	if !hasDelay {
		if cfg.IsExpressionProvided(body.InitialDelay) {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "initial_delay requires delay",
				Detail:   "initial_delay cannot be used without delay; the trigger starts dormant when delay is omitted",
				Subject:  block.DefRange.Ptr(),
			})
		}
		if cfg.IsExpressionProvided(body.ErrorDelay) {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "error_delay requires delay",
				Detail:   "error_delay cannot be used without delay; the trigger starts dormant when delay is omitted",
				Subject:  block.DefRange.Ptr(),
			})
		}
		if diags.HasErrors() {
			return diags
		}
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

	repeat := true
	if body.Repeat != nil {
		repeat = *body.Repeat
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
		repeat:           repeat,
		tracerProvider:   triggerDef.TracerProvider,
	}

	config.CtyTriggerMap[name] = NewIntervalTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.PostStartables = append(config.PostStartables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
