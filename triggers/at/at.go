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
// set(trigger.<name>) wakes the goroutine early so it re-evaluates the time
// expression immediately. This allows an external trigger (e.g. trigger
// "interval") to poke the at trigger whenever conditions change, such as
// a vehicle's position shifting significantly.
type AtTrigger struct {
	name           string
	config         *cfg.Config
	timeExpr       hcl.Expression
	actionExpr     hcl.Expression
	tracerProvider trace.TracerProvider

	mu            sync.RWMutex
	scheduledTime time.Time // zero = not yet evaluated
	runCount      int64
	lastResult    cty.Value // cty.NilVal until first fire

	stopCh chan struct{}
	doneCh chan struct{}
	setCh  chan struct{} // buffered 1; signals goroutine to re-evaluate time
}

// Count returns the number of times the trigger has fired. Implements
// types.Countable so count(trigger.<name>) is callable from any expression.
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

// Set wakes the goroutine so it re-evaluates the time expression immediately,
// resetting the scheduled fire time. Implements Settable.
func (t *AtTrigger) Set(_ context.Context, args []cty.Value) (cty.Value, error) {
	select {
	case t.setCh <- struct{}{}:
	default:
	}
	if len(args) > 0 {
		return args[0], nil
	}
	return cty.NullVal(cty.DynamicPseudoType), nil
}

// Start initialises the set and stop channels. Implements Startable.
func (t *AtTrigger) Start() error {
	t.setCh = make(chan struct{}, 1)
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

func (t *AtTrigger) buildEvalContext(ctx context.Context, runCount int64, lastResult cty.Value) (*hcl.EvalContext, error) {
	if lastResult == cty.NilVal {
		lastResult = cty.NullVal(cty.DynamicPseudoType)
	}
	return hclutil.NewEvalContext(ctx).
		WithStringAttribute("trigger", "at").
		WithStringAttribute("name", t.name).
		WithInt64Attribute("run_count", runCount).
		WithAttribute("last_result", lastResult).
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

	for {
		// Read current state for eval context.
		t.mu.RLock()
		runCount := t.runCount
		lastResult := t.lastResult
		t.mu.RUnlock()

		// Evaluate the time expression (no span here — the span lives in fire()).
		evalCtx, err := t.buildEvalContext(context.Background(), runCount, lastResult)
		if err != nil {
			t.config.Logger.Error("at trigger: error building eval context",
				zap.String("name", t.name), zap.Error(err))
			if !t.waitOrStop(errorRetry, nil) {
				return
			}
			continue
		}

		timeVal, diags := t.timeExpr.Value(evalCtx)
		if diags.HasErrors() {
			t.config.Logger.Error("at trigger: time expression error",
				zap.String("name", t.name), zap.Error(diags))
			if !t.waitOrStop(errorRetry, nil) {
				return
			}
			continue
		}

		targetTime, err := timecty.GetTime(timeVal)
		if err != nil {
			t.config.Logger.Error("at trigger: time expression did not produce a time value",
				zap.String("name", t.name), zap.Error(err))
			if !t.waitOrStop(errorRetry, nil) {
				return
			}
			continue
		}

		// Store the scheduled time so Get() can return it.
		t.mu.Lock()
		t.scheduledTime = targetTime
		t.mu.Unlock()

		delay := time.Until(targetTime)
		if delay < 0 {
			t.config.Logger.Warn("at trigger: computed time is in the past, firing immediately",
				zap.String("name", t.name), zap.Time("scheduled_time", targetTime))
			delay = 0
		} else {
			t.config.Logger.Debug("at trigger: scheduled",
				zap.String("name", t.name), zap.Time("fire_at", targetTime), zap.Duration("delay", delay))
		}

		// Set or reset the timer.
		if timer == nil {
			timer = time.NewTimer(delay)
		} else {
			safeTimerReset(timer, delay)
		}

		select {
		case <-timer.C:
			// Time to fire.
			t.fire(evalCtx)
			// Loop back to re-evaluate time for next occurrence.

		case <-t.setCh:
			// External poke: re-evaluate time without firing.
			t.config.Logger.Debug("at trigger: re-evaluating time due to set()",
				zap.String("name", t.name))
			safeTimerReset(timer, 0) // drain/reset so next iteration creates cleanly
			// Drain the timer channel in case it fired concurrently.
			select {
			case <-timer.C:
			default:
			}

		case <-t.stopCh:
			return
		}
	}
}

// fire evaluates the action expression and updates runCount and lastResult.
func (t *AtTrigger) fire(_ *hcl.EvalContext) {
	t.config.Logger.Debug("at trigger: firing", zap.String("name", t.name))

	t.mu.RLock()
	runCount := t.runCount
	lastResult := t.lastResult
	t.mu.RUnlock()

	spanCtx, stopSpan := hclutil.StartTriggerSpan(context.Background(), t.tracerProvider, "at", t.name)

	evalCtx, err := t.buildEvalContext(spanCtx, runCount, lastResult)
	if err != nil {
		t.config.Logger.Error("at trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		stopSpan(err)
		return
	}

	val, diags := t.actionExpr.Value(evalCtx)
	t.mu.Lock()
	t.runCount++
	if diags.HasErrors() {
		t.config.Logger.Error("at trigger: action error",
			zap.String("name", t.name), zap.Error(diags))
		stopSpan(diags)
	} else {
		t.lastResult = val
		t.config.Logger.Debug("at trigger: action completed",
			zap.String("name", t.name), zap.Any("result", val))
		stopSpan(nil)
	}
	t.mu.Unlock()
}

// waitOrStop waits for the given duration (or a set() signal), then returns
// true if execution should continue, or false if the trigger should stop.
// A nil timer means "create a fresh one".
func (t *AtTrigger) waitOrStop(d time.Duration, timer *time.Timer) bool {
	if timer == nil {
		timer = time.NewTimer(d)
		defer timer.Stop()
	} else {
		safeTimerReset(timer, d)
	}
	select {
	case <-timer.C:
		return true
	case <-t.setCh:
		return true
	case <-t.stopCh:
		return false
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
	Time   hcl.Expression `hcl:"time"`
	Action hcl.Expression `hcl:"action"`
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

	name := block.Labels[1]
	t := &AtTrigger{
		name:           name,
		config:         config,
		timeExpr:       body.Time,
		actionExpr:     body.Action,
		tracerProvider: triggerDef.TracerProvider,
	}

	config.CtyTriggerMap[name] = NewAtTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.PostStartables = append(config.PostStartables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
