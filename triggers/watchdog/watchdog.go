package watchdog

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
	"go.uber.org/zap"
)

// WatchdogTrigger fires its action when a time window elapses without
// set(trigger.<name>) being called. It is the inverse of trigger "interval":
// rather than doing work on a schedule, it detects when expected work stops.
//
// set(trigger.<name>, value) resets the countdown and stores the value.
// set(trigger.<name>) with no value resets the countdown and stores null.
// get(trigger.<name>) returns the last value passed to set(), or null if never set.
//
// If shutdown occurs while the watchdog is waiting, it exits without firing.
type WatchdogTrigger struct {
	name         string
	config       *cfg.Config
	window       time.Duration
	initialGrace time.Duration // 0 means "use window"
	actionExpr   hcl.Expression
	repeat       bool // if true, re-arms immediately after firing instead of going dormant

	mu        sync.RWMutex
	lastValue cty.Value // cty.NilVal until first set()
	lastSet   time.Time // zero until first set()
	missCount int64     // consecutive fires since last set(); resets to 0 on set()

	setCh  chan struct{} // signals the goroutine to reset the timer (buffered 1)
	stopCh chan struct{}
	doneCh chan struct{}
}

// Set resets the watchdog countdown and stores the value. If called with no
// arguments, stores null. Returns the stored value. Implements Settable.
func (t *WatchdogTrigger) Set(_ context.Context, args []cty.Value) (cty.Value, error) {
	val := cty.NullVal(cty.DynamicPseudoType)
	if len(args) > 0 {
		val = args[0]
	}
	t.mu.Lock()
	t.lastValue = val
	t.lastSet = time.Now()
	t.missCount = 0
	t.mu.Unlock()

	// Signal the goroutine non-blocking: if the buffer is full, the goroutine
	// is already about to receive a signal and will reset the timer anyway.
	select {
	case t.setCh <- struct{}{}:
	default:
	}
	return val, nil
}

// Get returns the last value passed to set(), or null if set() has never been
// called. Implements Gettable.
func (t *WatchdogTrigger) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastValue == cty.NilVal {
		return cty.NullVal(cty.DynamicPseudoType), nil
	}
	return t.lastValue, nil
}

// Start launches the background watchdog goroutine. Implements Startable.
func (t *WatchdogTrigger) Start() error {
	t.setCh = make(chan struct{}, 1)
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	go t.run()
	return nil
}

// Stop signals the watchdog to exit and waits for it to finish.
// Implements Stoppable.
func (t *WatchdogTrigger) Stop() error {
	if t.stopCh == nil {
		return nil
	}
	close(t.stopCh)
	<-t.doneCh
	return nil
}

func (t *WatchdogTrigger) buildEvalContext(missCount int64, lastSet time.Time) (*hcl.EvalContext, error) {
	lastSetVal := cty.NullVal(cty.DynamicPseudoType)
	if !lastSet.IsZero() {
		lastSetVal = timecty.NewTimeCapsule(lastSet)
	}
	return hclutil.NewEvalContext(context.Background()).
		WithStringAttribute("trigger", "watchdog").
		WithStringAttribute("name", t.name).
		WithInt64Attribute("miss_count", missCount).
		WithAttribute("last_set", lastSetVal).
		BuildEvalContext(t.config.EvalCtx())
}

func (t *WatchdogTrigger) fire() {
	t.mu.Lock()
	t.missCount++
	missCount := t.missCount
	lastSet := t.lastSet
	t.mu.Unlock()

	t.config.Logger.Debug("watchdog trigger: window expired",
		zap.String("name", t.name), zap.Int64("miss_count", missCount))

	evalCtx, err := t.buildEvalContext(missCount, lastSet)
	if err != nil {
		t.config.Logger.Error("watchdog trigger: error building eval context",
			zap.String("name", t.name), zap.Error(err))
		return
	}
	val, diags := t.actionExpr.Value(evalCtx)
	if diags.HasErrors() {
		t.config.Logger.Error("watchdog trigger: action error",
			zap.String("name", t.name), zap.Error(diags))
		return
	}
	t.config.Logger.Debug("watchdog trigger: action completed",
		zap.String("name", t.name), zap.Any("result", val))
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

func (t *WatchdogTrigger) run() {
	defer close(t.doneCh)

	grace := t.initialGrace
	if grace == 0 {
		grace = t.window
	}

	timer := time.NewTimer(grace)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			t.fire()

			if !t.repeat {
				// DORMANT: wait for a set() signal before re-arming.
				select {
				case <-t.setCh:
				case <-t.stopCh:
					return
				}
			}
			timer.Reset(t.window)

		case <-t.setCh:
			// Fed while armed — reset the countdown.
			safeTimerReset(timer, t.window)

		case <-t.stopCh:
			return
		}
	}
}

// --- Capsule type ---

var WatchdogCapsuleType = cty.CapsuleWithOps("watchdog_trigger", reflect.TypeOf((*WatchdogTrigger)(nil)).Elem(), &cty.CapsuleOps{
	GoString: func(val interface{}) string {
		return fmt.Sprintf("watchdog_trigger(%p)", val)
	},
	TypeGoString: func(_ reflect.Type) string {
		return "WatchdogTrigger"
	},
})

func NewWatchdogTriggerCapsule(t *WatchdogTrigger) cty.Value {
	return cty.CapsuleVal(WatchdogCapsuleType, t)
}

func GetWatchdogTriggerFromCapsule(val cty.Value) (*WatchdogTrigger, error) {
	if val.Type() != WatchdogCapsuleType {
		return nil, fmt.Errorf("expected watchdog_trigger capsule, got %s", val.Type().FriendlyName())
	}
	t, ok := val.EncapsulatedValue().(*WatchdogTrigger)
	if !ok {
		return nil, fmt.Errorf("encapsulated value is not a WatchdogTrigger, got %T", val.EncapsulatedValue())
	}
	return t, nil
}

// --- Block processing ---

type triggerWatchdogBody struct {
	Window       hcl.Expression `hcl:"window"`
	Action       hcl.Expression `hcl:"action"`
	InitialGrace hcl.Expression `hcl:"initial_grace,optional"`
	Repeat       *bool          `hcl:"repeat,optional"`
}

func init() {
	cfg.RegisterTriggerType("watchdog", cfg.TriggerRegistration{Process: processWatchdogTrigger, HasDependencyId: true})
}

func processWatchdogTrigger(config *cfg.Config, block *hcl.Block, triggerDef *cfg.TriggerDefinition) hcl.Diagnostics {
	body := triggerWatchdogBody{}
	diags := gohcl.DecodeBody(triggerDef.RemainingBody, config.EvalCtx(), &body)
	if diags.HasErrors() {
		return diags
	}

	window, addDiags := config.ParseDuration(body.Window)
	diags = diags.Extend(addDiags)
	if diags.HasErrors() {
		return diags
	}

	var initialGrace time.Duration
	if cfg.IsExpressionProvided(body.InitialGrace) {
		initialGrace, addDiags = config.ParseDuration(body.InitialGrace)
		diags = diags.Extend(addDiags)
		if diags.HasErrors() {
			return diags
		}
	}

	repeat := false
	if body.Repeat != nil {
		repeat = *body.Repeat
	}

	name := block.Labels[1]
	t := &WatchdogTrigger{
		name:         name,
		config:       config,
		window:       window,
		initialGrace: initialGrace,
		actionExpr:   body.Action,
		repeat:       repeat,
	}

	config.CtyTriggerMap[name] = NewWatchdogTriggerCapsule(t)
	config.EvalCtx().Variables["trigger"] = cfg.CtyObjectOrEmpty(config.CtyTriggerMap)
	config.Startables = append(config.Startables, t)
	config.Stoppables = append(config.Stoppables, t)

	return diags
}
