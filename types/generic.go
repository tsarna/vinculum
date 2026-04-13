package types

import (
	"context"
	"fmt"

	"github.com/zclconf/go-cty/cty"
)

// Stringable is implemented by types that have a natural string representation,
// allowing them to be used with tostring().
type Stringable interface {
	ToString(ctx context.Context) (string, error)
}

// Lengthable is implemented by types that have a meaningful length,
// allowing them to be used with length().
type Lengthable interface {
	Length(ctx context.Context) (int64, error)
}

// Callable is a pure request/response capability. It is not specific to LLM
// clients and is designed to cover future types (HTTP, JSON-RPC, MCP, etc.).
// args contains all arguments after the "thing": the implementor fully controls
// what they mean.
type Callable interface {
	Call(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Gettable is implemented by types whose current value can be read via get().
// args contains all arguments after the "thing": typically args[0] is the
// default value, but implementors may interpret additional args freely.
type Gettable interface {
	Get(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Settable is implemented by types whose value can be updated via set().
// args contains all arguments after the "thing": args[0] is the value to set;
// implementors may interpret additional args (e.g. labels) freely.
type Settable interface {
	Set(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Incrementable is implemented by types that support numeric increment via increment().
// args contains all arguments after the "thing": args[0] is the delta;
// implementors may interpret additional args freely.
type Incrementable interface {
	Increment(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Observable is implemented by types that support observe() (histograms).
// args contains all arguments after the "thing": args[0] is the observed value;
// implementors may interpret additional args (e.g. labels) freely.
type Observable interface {
	Observe(ctx context.Context, args []cty.Value) (cty.Value, error)
}

// Countable is implemented by types that maintain a running count accessible
// via count(). Distinct from Lengthable: Lengthable answers "how many elements
// does this collection have?"; Countable answers "how many times has this
// happened?" — it backs counters and run-count accumulators.
type Countable interface {
	Count(ctx context.Context) (int64, error)
}

// Resettable is implemented by types that can be reset to an initial state
// via reset(). Used by counter conditions and watchdog triggers. Distinct from
// the condition-specific clear() semantics on timer/threshold conditions.
type Resettable interface {
	Reset(ctx context.Context) error
}

// Stateful is implemented by types that expose a named internal state via
// state(). Condition blocks implement this with the four-state vocabulary
// ("inactive", "pending_activation", "active", "pending_deactivation").
type Stateful interface {
	State(ctx context.Context) (string, error)
}

// Clearable is implemented by types that support the clear() function. On
// timer and threshold conditions this cancels any pending state, releases any
// latch, and (for retentive timers) discards accumulated time. Semantically
// distinct from Resettable.
type Clearable interface {
	Clear(ctx context.Context) error
}

// Watchable is implemented by types whose value can be observed for changes.
// Implementations must be safe to call from multiple goroutines concurrently.
type Watchable interface {
	// Watch registers w to receive OnChange notifications when the value changes.
	// Registering the same Watcher twice is a no-op.
	Watch(w Watcher)

	// Unwatch removes a previously registered Watcher.
	// Removing a Watcher that is not registered is a no-op.
	Unwatch(w Watcher)
}

// Watcher receives notifications from a Watchable when its value changes.
// OnChange is called synchronously, outside the Watchable's internal mutex.
type Watcher interface {
	// OnChange is called after the Watchable's value has been updated.
	// oldValue is the value immediately before the change.
	// newValue is the value immediately after the change.
	// ctx is the context.Context that was passed to the Set (or Increment) call
	// that triggered the change.
	OnChange(ctx context.Context, oldValue, newValue cty.Value)
}

// WatchableFromCtyValue extracts a Watchable from a cty capsule value,
// returning an error if the value is not a capsule or its encapsulated type
// does not implement Watchable.
func WatchableFromCtyValue(val cty.Value) (Watchable, error) {
	if !val.Type().IsCapsuleType() {
		return nil, fmt.Errorf("expected a watchable capsule (var, metric), got %s", val.Type().FriendlyName())
	}
	w, ok := val.EncapsulatedValue().(Watchable)
	if !ok {
		return nil, fmt.Errorf("value of type %s is not watchable", val.Type().FriendlyName())
	}
	return w, nil
}
