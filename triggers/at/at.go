package at

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	timecty "github.com/tsarna/time-cty-funcs"
	cfg "github.com/tsarna/vinculum/config"
	"github.com/tsarna/vinculum/hclutil"
	"github.com/zclconf/go-cty/cty"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// AtTrigger evaluates its action at a dynamically computed absolute time, then
// repeats by re-evaluating the time expression to find the next target. This
// is the natural fit for non-uniform recurring schedules such as sunrise/sunset,
// where the interval between firings varies.
//
// get(trigger.<name>) returns null until the first time expression has been
// evaluated, then the currently scheduled fire time as a time capsule.
//
// set(trigger.<name>, time) overrides the next fire with an explicit absolute
// time; the override is consumed on fire and subsequent iterations fall back
// to the configured time expression. Revives a dormant trigger.
//
// set(trigger.<name>) with no argument re-evaluates the time expression
// immediately without firing the action. Returns an error if no time
// expression was configured.
//
// reset(trigger.<name>) cancels any pending timer and puts the trigger into
// a dormant state, waiting for the next set() call.
//
// If time is omitted from the configuration, the trigger starts dormant and
// waits for the first set() call before firing.
type AtTrigger struct {
	name           string
	config         *cfg.Config
	timeExpr       hcl.Expression
	actionExpr     hcl.Expression
	stopWhenExpr   hcl.Expression // optional; trigger stops when this evaluates true
	repeat         bool           // if true (default), loop; if false, fire once then go dormant
	tracerProvider trace.TracerProvider

	mu            sync.RWMutex
	scheduledTime time.Time  // zero = not yet evaluated
	runCount      int64
	lastResult    cty.Value  // cty.NilVal until first fire
	lastError     error
	timeOverride  *time.Time // non-nil when set() provided an explicit time

	setCh   chan *time.Time // buffered 1; signals goroutine to re-evaluate / use override
	resetCh chan struct{}   // buffered 1; signals goroutine to go dormant
	stopCh  chan struct{}
	doneCh  chan struct{}
}

// Count returns the number of times the trigger has fired. Implements
// richcty.Countable so count(trigger.<name>) is callable from any expression.
func (t *AtTrigger) Count(_ context.Context) (int64, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.runCount, nil
}

// Get returns the currently scheduled fire time as a time capsule, or null if
// the time expression has not been evaluated yet. Implements Gettable.
func (t *AtTrigger) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.scheduledTime.IsZero() {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return timecty.NewTimeCapsule(t.scheduledTime), nil
}

// Set reschedules the trigger. If called with a time argument, that time
// overrides the next fire (the override is consumed on fire). If called with
// no arguments, wakes the goroutine so it re-evaluates the time expression
// immediately (returns an error if no time expression was configured). In
// either case resets runCount and clears lastError, and revives the trigger
// if it was dormant. Implements Settable.
func (t *AtTrigger) Set(_ context.Context, args []cty.Value) (cty.Value, error) {
	var override *time.Time
	if len(args) > 0 {
		tm, err := timecty.GetTime(args[0])
		if err != nil {
			return cty.NilVal, fmt.Errorf("at trigger %q: invalid time for set(): %w", t.name, err)
		}
		override = &tm
	} else if !cfg.IsExpressionProvided(t.timeExpr) {
		return cty.NilVal, fmt.Errorf("at trigger %q: set() with no time requires a configured time expression", t.name)
	}

	t.mu.Lock()
	t.timeOverride = override
	t.runCount = 0
	t.lastError = nil
	t.mu.Unlock()

	select {
	case t.setCh <- override:
	default:
	}

	if override != nil {
		return timecty.NewTimeCapsule(*override), nil
	}
	return cty.NullVal(cty.DynamicPseudoType), nil
}

// Reset cancels any pending timer and puts the trigger into a dormant state,
// waiting for the next set() call. Clears the override, scheduled time, run
// count, and last result/error. Implements Resettable.
func (t *AtTrigger) Reset(_ context.Context) error {
	t.mu.Lock()
	t.timeOverride = nil
	t.scheduledTime = time.Time{}
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
func (t *AtTrigger) Start() error {
	t.setCh = make(chan *time.Time, 1)
	t.resetCh = make(chan struct{}, 1)
	t.stopCh = make(chan struct{})
	return nil
}

// PostStart launches the background goroutine. Called after all Startable
// components have completed, so buses and clients are ready when the action fires.
// Implements PostStartable.
func (t *AtTrigger) PostStart() error {
	t.doneCh = make(chan struct{})
	go t.run()
	return nil
}

// Stop signals the goroutine to exit and waits for it to finish.
// Implements Stoppable.
func (t *AtTrigger) Stop() error {
	if t.stopCh == nil {
		return nil
	}
	close(t.stopCh)
	if t.doneCh != nil {
		<-t.doneCh
	}
	return nil
}

func (t *AtTrigger) buildEvalContext(ctx context.Context, runCount int64, lastResult cty.Value, lastErr error) (*hcl.EvalContext, error) {
	if lastResult == cty.NilVal {
		lastResult = cty.NullVal(cty.DynamicPseudoType)
	}
	lastErrStr := cty.NullVal(cty.String)
	if lastErr != nil {
		lastErrStr = cty.StringVal(lastErr.Error())
	}
	return hclutil.NewEvalContext(ctx).
		WithStringAttribute("trigger", "at").
		WithStringAttribute("name", t.name).
		WithInt64Attribute("run_count", runCount).
		WithAttribute("last_result", lastResult).
		WithAttribute("last_error", lastErrStr).
		BuildEvalContext(t.config.EvalCtx())
}

func (t *AtTrigger) run() {
	defer close(t.doneCh)

	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	const errorRetry = time.Minute

	// If no time expression and no override, start dormant.
	if !cfg.IsExpressionProvided(t.timeExpr) {
		t.mu.RLock()
		hasOverride := t.timeOverride != nil
		t.mu.RUnlock()
		if !hasOverride {
			if t.waitForRevival() {
				return
			}
		}
	}

	for {
		// Snapshot state for this iteration.
		t.mu.RLock()
		override := t.timeOverride
		runCount := t.runCount
		lastResult := t.lastResult
		lastErr := t.lastError
		t.mu.RUnlock()

		var targetTime time.Time
		if override != nil {
			targetTime = *override
		} else {
			// Evaluate the time expression.
			evalCtx, err := t.buildEvalContext(context.Background(), runCount, lastResult, lastErr)
			if err != nil {
				t.config.UserLogger.Error("at trigger: error building eval context",
					zap.String("name", t.name), zap.Error(err))
				switch t.waitForRetry(errorRetry) {
				case runStop:
					return
				case runDormant:
					if t.waitForRevival() {
						return
					}
				}
				continue
			}

			timeVal, diags := t.timeExpr.Value(evalCtx)
			if diags.HasErrors() {
				t.config.UserLogger.Error("at trigger: time expression error",
					zap.String("name", t.name), zap.Error(diags))
				switch t.waitForRetry(errorRetry) {
				case runStop:
					return
				case runDormant:
					if t.waitForRevival() {
						return
					}
				}
				continue
			}

			tm, err := timecty.GetTime(timeVal)
			if err != nil {
				t.config.UserLogger.Error("at trigger: time expression did not produce a time value",
					zap.String("name", t.name), zap.Error(err))
				switch t.waitForRetry(errorRetry) {
				case runStop:
					return
				case runDormant:
					if t.waitForRevival() {
						return
					}
				}
				continue
			}
			targetTime = tm
		}

		// Publish the scheduled time so Get() can return it.
		t.mu.Lock()
		t.scheduledTime = targetTime
		t.mu.Unlock()

		delay := time.Until(targetTime)
		if delay < 0 {
			t.config.UserLogger.Warn("at trigger: computed time is in the past, firing immediately",
				zap.String("name", t.name), zap.Time("scheduled_time", targetTime))
			delay = 0
		} else {
			t.config.Logger.Debug("at trigger: scheduled",
				zap.String("name", t.name), zap.Time("fire_at", targetTime), zap.Duration("delay", delay))
		}

		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			safeTimerReset(timer, delay)
		}

		select {
		case <-timer.C:
			// Consume the override (if any) now that it has fired. Only clear
			// if it's still the same pointer — a concurrent Set() may have
			// replaced it, and we must preserve the new one for the next loop.
			t.mu.Lock()
			if t.timeOverride == override {
				t.timeOverride = nil
			}
			t.mu.Unlock()

			t.fire()

			if t.shouldStop() {
				t.config.Logger.Debug("at trigger: stop condition met, going dormant",
					zap.String("name", t.name))
				if t.waitForRevival() {
					return
				}
			}

		case <-t.setCh:
			// External poke: re-read state on next iteration (override may have changed).
			t.config.Logger.Debug("at trigger: re-evaluating due to set()",
				zap.String("name", t.name))
			safeTimerReset(timer, 0)
			select {
			case <-timer.C:
			default:
			}

		case <-t.resetCh:
			safeTimerReset(timer, 0)
			select {
			case <-timer.C:
			default:
			}
			if t.waitForRevival() {
				return
			}

		case <-t.stopCh:
			return
		}
	}
}

// fire evaluates the action expression and updates runCount, lastResult, and
// lastError.
func (t *AtTrigger) fire() {
	t.config.Logger.Debug("at trigger: firing", zap.String("name", t.name))

	t.mu.RLock()
	runCount := t.runCount
	lastResult := t.lastResult
	lastErr := t.lastError
	t.mu.RUnlock()

	spanCtx, stopSpan := hclutil.StartTriggerSpan(context.Background(), t.tracerProvider, "at", t.name)

	evalCtx, err := t.buildEvalContext(spanCtx, runCount, lastResult, lastErr)
	if err != nil {
		t.config.UserLogger.Error("at trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		stopSpan(err)
		return
	}

	val, diags := t.actionExpr.Value(evalCtx)
	var actionErr error
	if diags.HasErrors() {
		actionErr = diags
		val = cty.NilVal
		t.config.UserLogger.Error("at trigger: action error",
			zap.String("name", t.name), zap.Error(actionErr))
	} else {
		t.config.Logger.Debug("at trigger: action completed",
			zap.String("name", t.name), zap.Any("result", val))
	}
	stopSpan(actionErr)

	t.mu.Lock()
	t.runCount++
	t.lastResult = val
	t.lastError = actionErr
	t.mu.Unlock()
}

// shouldStop reports whether the trigger should go dormant after a fire:
// either repeat is false, or stop_when evaluates true.
func (t *AtTrigger) shouldStop() bool {
	if !t.repeat {
		return true
	}
	if !cfg.IsExpressionProvided(t.stopWhenExpr) {
		return false
	}

	t.mu.RLock()
	runCount := t.runCount
	lastResult := t.lastResult
	lastErr := t.lastError
	t.mu.RUnlock()

	evalCtx, err := t.buildEvalContext(context.Background(), runCount, lastResult, lastErr)
	if err != nil {
		t.config.UserLogger.Error("at trigger: error building stop_when context",
			zap.String("name", t.name), zap.Error(err))
		return false
	}
	stopVal, stopDiags := t.stopWhenExpr.Value(evalCtx)
	if stopDiags.HasErrors() {
		t.config.UserLogger.Error("at trigger: error evaluating stop_when",
			zap.String("name", t.name), zap.Error(stopDiags))
		return false
	}
	return stopVal.IsKnown() && !stopVal.IsNull() && stopVal.Type() == cty.Bool && stopVal.True()
}

// shouldRemainStopped reports whether the trigger should stay dormant after a
// set() call. repeat=false is always revivable (set() resets runCount); with
// stop_when, we re-evaluate against the post-set() state.
func (t *AtTrigger) shouldRemainStopped() bool {
	if !cfg.IsExpressionProvided(t.stopWhenExpr) {
		return false
	}

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
	return stopVal.IsKnown() && !stopVal.IsNull() && stopVal.Type() == cty.Bool && stopVal.True()
}

// waitForRevival blocks until a set() call revives the trigger or Stop() is
// called. Returns true if the goroutine should exit (shutdown), false if
// revived.
func (t *AtTrigger) waitForRevival() bool {
	t.config.Logger.Debug("at trigger: dormant, waiting for set()",
		zap.String("name", t.name))
	for {
		select {
		case <-t.setCh:
			// Only revive if there is something to fire: an override, or a
			// configured time expression. Set() errors otherwise, so this
			// shouldn't normally fail — guard anyway.
			t.mu.RLock()
			hasOverride := t.timeOverride != nil
			t.mu.RUnlock()
			if !hasOverride && !cfg.IsExpressionProvided(t.timeExpr) {
				continue
			}
			if !t.shouldRemainStopped() {
				t.config.Logger.Debug("at trigger: revived by set()",
					zap.String("name", t.name))
				return false
			}
		case <-t.resetCh:
			// Already dormant; drain.
		case <-t.stopCh:
			return true
		}
	}
}

// runAction reports how the main loop should proceed after an error-retry wait.
type runAction int

const (
	runResume runAction = iota
	runDormant
	runStop
)

// waitForRetry waits up to d for a retry opportunity, responding to external
// signals. Returns runResume to retry, runDormant to go dormant, or runStop
// to exit.
func (t *AtTrigger) waitForRetry(d time.Duration) runAction {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return runResume
	case <-t.setCh:
		return runResume
	case <-t.resetCh:
		return runDormant
	case <-t.stopCh:
		return runStop
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

// --- Capsule type ---

var AtCapsuleType = cty.CapsuleWithOps("at_trigger", reflect.TypeOf((*AtTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("at_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "AtTrigger"
	},
})

func NewAtTriggerCapsule(t *AtTrigger) cty.Value {
	return cty.CapsuleVal(AtCapsuleType, t)
}

func GetAtTriggerFromCapsule(val cty.Value) (*AtTrigger, error) {
	if val.Type() != AtCapsuleType {
		return nil, fmt.Errorf("expected at_trigger capsule, got %s", val.Type().FriendlyName())
	}
	t, ok := val.EncapsulatedValue().(*AtTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not an AtTrigger, got %T", val.EncapsulatedValue())
	}
	return t, nil
}

// --- Block processing ---

type triggerAtBody struct {
	Time     hcl.Expression `hcl:"time,optional"`
	Repeat   *bool          `hcl:"repeat,optional"`
	StopWhen hcl.Expression `hcl:"stop_when,optional"`
	Action   hcl.Expression `hcl:"action"`
}

func init() {
	cfg.RegisterTriggerType("at", cfg.TriggerRegistration{Process: processAtTrigger, HasDependencyId: true})
}

func processAtTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerAtBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	repeat := true
	if body.Repeat != nil {
		repeat = *body.Repeat
	}

	name := block.Labels[1]
	t := &AtTrigger{
		name:           name,
		config:         config,
		timeExpr:       body.Time,
		actionExpr:     body.Action,
		stopWhenExpr:   body.StopWhen,
		repeat:         repeat,
		tracerProvider: triggerDef.TracerProvider,
	}

	config.CtyTriggerMap[name] = NewAtTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.PostStartables = append(config.PostStartables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
