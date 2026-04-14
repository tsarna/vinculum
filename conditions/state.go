package conditions

import (
	"context"
	"sync"
	"time"

	richcty "github.com/tsarna/rich-cty-types"

	"github.com/zclconf/go-cty/cty"
)

// State is the four-state vocabulary shared by every condition subtype.
type State int

const (
	StateInactive State = iota
	StatePendingActivation
	StateActive
	StatePendingDeactivation
)

func (s State) String() string {
	switch s {
	case StateInactive:
		return "inactive"
	case StatePendingActivation:
		return "pending_activation"
	case StateActive:
		return "active"
	case StatePendingDeactivation:
		return "pending_deactivation"
	default:
		return "unknown"
	}
}

// Clock abstracts time so the state machine is testable. Production code uses
// RealClock; tests use a fake implementation.
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) ClockTimer
}

// ClockTimer is the subset of time.Timer the state machine uses.
type ClockTimer interface {
	Stop() bool
}

// RealClock is a Clock backed by the stdlib time package.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) AfterFunc(d time.Duration, f func()) ClockTimer {
	return time.AfterFunc(d, f)
}

// StateMachine implements the four-state output logic shared by every
// condition subtype. It consumes a single boolean "raw" input signal (already
// filtered by the subtype — e.g. through debounce for timer, hysteresis for
// threshold, preset comparison for counter) and exposes the conditioned
// output with temporal semantics layered on top.
//
// StateMachine is Watchable (fires on output transitions only — never on
// pending-state transitions), Gettable (returns the output bool, inverted if
// configured), Stateful (returns the internal four-state name), and
// Clearable (cancels any pending state / releases any latch).
//
// Zero-valued Behavior fields disable their respective behaviors; the
// StateMachine with a fully-zeroed Behavior simply tracks the raw input
// one-to-one.
type StateMachine struct {
	richcty.WatchableMixin
	behavior Behavior
	clock    Clock

	mu            sync.Mutex
	state         State
	rawInput      bool
	latched       bool
	inhibited     bool
	cooldownUntil time.Time
	pendingTimer  ClockTimer // activate_after / deactivate_after
	timeoutTimer  ClockTimer // active-state timeout
	cooldownTimer ClockTimer

	// Retentive accumulator. retAccumulated holds the cumulative time the
	// pending-activation timer has been running; retStartedAt is the
	// wall-clock instant the current tick-in-progress began, or zero when
	// paused.
	retAccumulated time.Duration
	retStartedAt   time.Time
}

// NewStateMachine creates a StateMachine with the given behavior. If clock is
// nil, RealClock is used.
func NewStateMachine(behavior Behavior, clock Clock) *StateMachine {
	if clock == nil {
		clock = RealClock{}
	}
	return &StateMachine{behavior: behavior, clock: clock, state: StateInactive}
}

// SetRawInput drives the state machine from the subtype's pre-filtered boolean
// input signal. ctx is forwarded to watcher notifications.
func (sm *StateMachine) SetRawInput(ctx context.Context, value bool) {
	sm.mu.Lock()
	if sm.rawInput == value {
		sm.mu.Unlock()
		return
	}
	sm.rawInput = value
	before := sm.outputLocked()
	sm.onInputChangeLocked(value)
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(ctx, cty.BoolVal(before), cty.BoolVal(after))
	}
}

// onInputChangeLocked evaluates the input edge against the current state.
// Called with sm.mu held.
func (sm *StateMachine) onInputChangeLocked(value bool) {
	switch sm.state {
	case StateInactive:
		if value {
			sm.tryActivateLocked()
		}
	case StatePendingActivation:
		if !value {
			if sm.behavior.Retentive {
				// Pause accumulator; stay in pending_activation waiting
				// for the input to re-assert.
				sm.pauseRetentiveLocked()
			} else {
				sm.cancelPendingLocked()
				sm.state = StateInactive
			}
		} else {
			// Possible under retentive when a previously-paused accumulator
			// sees its input re-assert. Resume ticking from where we left off.
			if sm.behavior.Retentive && sm.pendingTimer == nil {
				sm.resumeRetentiveLocked()
			}
		}
	case StateActive:
		if value {
			// Re-assertion: restart timeout clock if one is running.
			sm.restartTimeoutLocked()
		} else {
			if sm.latched {
				return
			}
			if sm.behavior.DeactivateAfter > 0 {
				sm.state = StatePendingDeactivation
				sm.pendingTimer = sm.clock.AfterFunc(sm.behavior.DeactivateAfter, sm.onDeactivateTimer)
			} else {
				sm.becomeInactiveLocked()
			}
		}
	case StatePendingDeactivation:
		if value {
			sm.cancelPendingLocked()
			sm.state = StateActive
			sm.restartTimeoutLocked()
		}
	}
}

// tryActivateLocked attempts to move from Inactive toward Active. Gated by
// cooldown and inhibit: while cooldown is in its quiet period or inhibit is
// asserted, the transition is silently suppressed (re-evaluated when the
// gate clears).
func (sm *StateMachine) tryActivateLocked() {
	if sm.cooldownActiveLocked() || sm.inhibited {
		return
	}
	if sm.behavior.ActivateAfter <= 0 {
		sm.becomeActiveLocked()
		return
	}
	remaining := sm.behavior.ActivateAfter - sm.retAccumulated
	if remaining <= 0 {
		// Retentive accumulator is already full.
		sm.becomeActiveLocked()
		return
	}
	sm.state = StatePendingActivation
	sm.retStartedAt = sm.clock.Now()
	sm.pendingTimer = sm.clock.AfterFunc(remaining, sm.onActivateTimer)
}

// pauseRetentiveLocked stops the pending-activation timer and adds the
// elapsed tick to the retentive accumulator. State stays in
// StatePendingActivation; only the timer is paused.
func (sm *StateMachine) pauseRetentiveLocked() {
	if sm.pendingTimer != nil {
		sm.pendingTimer.Stop()
		sm.pendingTimer = nil
	}
	if !sm.retStartedAt.IsZero() {
		sm.retAccumulated += sm.clock.Now().Sub(sm.retStartedAt)
		sm.retStartedAt = time.Time{}
	}
}

// resumeRetentiveLocked restarts the pending-activation timer for the
// remaining slice of activate_after. If the accumulator has already filled,
// we activate immediately.
func (sm *StateMachine) resumeRetentiveLocked() {
	remaining := sm.behavior.ActivateAfter - sm.retAccumulated
	if remaining <= 0 {
		sm.becomeActiveLocked()
		return
	}
	sm.retStartedAt = sm.clock.Now()
	sm.pendingTimer = sm.clock.AfterFunc(remaining, sm.onActivateTimer)
}

// becomeActiveLocked transitions to Active and arms timeout/latch as
// configured. Retentive accumulator is discarded on activation.
func (sm *StateMachine) becomeActiveLocked() {
	sm.state = StateActive
	sm.retAccumulated = 0
	sm.retStartedAt = time.Time{}
	if sm.behavior.Latch {
		sm.latched = true
	}
	sm.restartTimeoutLocked()
}

// becomeInactiveLocked transitions to Inactive, cancels any in-flight timers,
// and starts the cooldown window if configured.
func (sm *StateMachine) becomeInactiveLocked() {
	sm.state = StateInactive
	sm.cancelTimeoutLocked()
	if sm.behavior.Cooldown > 0 {
		sm.cooldownUntil = sm.clock.Now().Add(sm.behavior.Cooldown)
		// Schedule a re-evaluation when cooldown expires so a still-asserted
		// input can resume activation.
		if sm.cooldownTimer != nil {
			sm.cooldownTimer.Stop()
		}
		sm.cooldownTimer = sm.clock.AfterFunc(sm.behavior.Cooldown, sm.onCooldownTimer)
	}
}

func (sm *StateMachine) restartTimeoutLocked() {
	sm.cancelTimeoutLocked()
	if sm.latched || sm.behavior.Timeout <= 0 {
		return
	}
	sm.timeoutTimer = sm.clock.AfterFunc(sm.behavior.Timeout, sm.onTimeoutTimer)
}

func (sm *StateMachine) cancelPendingLocked() {
	if sm.pendingTimer != nil {
		sm.pendingTimer.Stop()
		sm.pendingTimer = nil
	}
}

func (sm *StateMachine) cancelTimeoutLocked() {
	if sm.timeoutTimer != nil {
		sm.timeoutTimer.Stop()
		sm.timeoutTimer = nil
	}
}

func (sm *StateMachine) cooldownActiveLocked() bool {
	return !sm.cooldownUntil.IsZero() && sm.clock.Now().Before(sm.cooldownUntil)
}

// --- timer callbacks ---

func (sm *StateMachine) onActivateTimer() {
	sm.mu.Lock()
	if sm.state != StatePendingActivation {
		sm.mu.Unlock()
		return
	}
	sm.pendingTimer = nil
	before := sm.outputLocked()
	sm.becomeActiveLocked()
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(context.Background(), cty.BoolVal(before), cty.BoolVal(after))
	}
}

func (sm *StateMachine) onDeactivateTimer() {
	sm.mu.Lock()
	if sm.state != StatePendingDeactivation {
		sm.mu.Unlock()
		return
	}
	sm.pendingTimer = nil
	before := sm.outputLocked()
	sm.becomeInactiveLocked()
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(context.Background(), cty.BoolVal(before), cty.BoolVal(after))
	}
}

func (sm *StateMachine) onTimeoutTimer() {
	sm.mu.Lock()
	if sm.state != StateActive {
		sm.mu.Unlock()
		return
	}
	sm.timeoutTimer = nil
	before := sm.outputLocked()
	sm.becomeInactiveLocked()
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(context.Background(), cty.BoolVal(before), cty.BoolVal(after))
	}
}

func (sm *StateMachine) onCooldownTimer() {
	sm.mu.Lock()
	sm.cooldownTimer = nil
	sm.cooldownUntil = time.Time{}
	if sm.state != StateInactive || !sm.rawInput {
		sm.mu.Unlock()
		return
	}
	before := sm.outputLocked()
	sm.tryActivateLocked()
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(context.Background(), cty.BoolVal(before), cty.BoolVal(after))
	}
}

// --- output + interface methods ---

// outputLocked returns the boolean output before inversion. Per spec:
// pending_activation → false, pending_deactivation → true.
func (sm *StateMachine) outputLocked() bool {
	switch sm.state {
	case StateActive, StatePendingDeactivation:
		return true
	default:
		return false
	}
}

func (sm *StateMachine) notifyAll(ctx context.Context, before, after cty.Value) {
	if sm.behavior.Invert {
		before = cty.BoolVal(!before.True())
		after = cty.BoolVal(!after.True())
	}
	sm.NotifyAll(ctx, before, after)
}

// Output returns the current boolean output (post-inversion). Safe for
// concurrent use.
func (sm *StateMachine) Output() bool {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	out := sm.outputLocked()
	if sm.behavior.Invert {
		out = !out
	}
	return out
}

// Get implements richcty.Gettable. The optional default argument is ignored:
// a condition always has a defined output.
func (sm *StateMachine) Get(_ context.Context, _ []cty.Value) (cty.Value, error) {
	return cty.BoolVal(sm.Output()), nil
}

// State implements richcty.Stateful, returning the internal four-state name.
func (sm *StateMachine) State(_ context.Context) (string, error) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	return sm.state.String(), nil
}

// SetInhibited updates the inhibit gate. When inhibit is asserted and the
// machine is currently in pending_activation, the pending transition is
// cancelled and (for retentive timers) the accumulator is discarded. Active
// and pending_deactivation states are unaffected per spec: inhibit prevents
// new activations, it does not force deactivation. When inhibit clears while
// the underlying input is still asserted, activation resumes from scratch
// (activate_after restarts; retentive accumulator is already zeroed).
func (sm *StateMachine) SetInhibited(ctx context.Context, inhibited bool) {
	sm.mu.Lock()
	if sm.inhibited == inhibited {
		sm.mu.Unlock()
		return
	}
	sm.inhibited = inhibited
	before := sm.outputLocked()
	if inhibited {
		if sm.state == StatePendingActivation {
			sm.cancelPendingLocked()
			sm.retAccumulated = 0
			sm.retStartedAt = time.Time{}
			sm.state = StateInactive
		}
	} else {
		if sm.state == StateInactive && sm.rawInput {
			sm.tryActivateLocked()
		}
	}
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(ctx, cty.BoolVal(before), cty.BoolVal(after))
	}
}

// Clear implements richcty.Clearable. Resets the state machine to Inactive,
// cancels any pending state, releases any latch, and (for subtypes backing
// them onto this machine) stops timers. Safe to call in any state.
func (sm *StateMachine) Clear(ctx context.Context) error {
	sm.mu.Lock()
	before := sm.outputLocked()
	sm.cancelPendingLocked()
	sm.cancelTimeoutLocked()
	sm.latched = false
	sm.retAccumulated = 0
	sm.retStartedAt = time.Time{}
	// Clear() is a complete reset: pending state cancelled, latch released,
	// accumulator discarded, and the tracked raw input reset to false so a
	// subsequent rising edge re-arms the state machine. It does NOT start a
	// cooldown window (cooldown is a post-deactivate quiet period for the
	// natural state flow, not an externally-forced reset).
	sm.rawInput = false
	sm.state = StateInactive
	after := sm.outputLocked()
	sm.mu.Unlock()
	if before != after {
		sm.notifyAll(ctx, cty.BoolVal(before), cty.BoolVal(after))
	}
	return nil
}
