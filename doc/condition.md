# Condition Reference

`condition` blocks produce a named, Watchable boolean output from various
input types and behavioral rules. They are the automation primitive for
encoding *when* something should be considered true — answering questions
like "has the temperature been above 80° for at least 30 seconds?" or
"have we seen three faults in a row without an acknowledgement?"

```hcl
condition "type" "name" {
    # ... behavioral attributes
}
```

Every condition implements the [Watchable](trigger.md#watchables) interface,
so it can be referenced from `trigger "watch"`, composed into other
conditions via `input = get(condition.other)`, or read imperatively from any
expression with `get(condition.name)`.

There are three subtypes, each answering a different question:

| Subtype | Input | Question |
|---|---|---|
| `condition "timer"` | Boolean (imperative or declared) | Apply temporal semantics to a boolean signal |
| `condition "threshold"` | Numeric (declared expression) | Derive a boolean from a numeric value with hysteresis |
| `condition "counter"` | Events (`increment()` / `decrement()` calls) | Produce a boolean when an event count reaches a preset |

All three share a common four-state model and a common set of behavioral
attributes described below.

---

## State Model

Every condition transitions through four internal states:

| State | Meaning | `get()` returns |
|---|---|---|
| `inactive` | Output false; input false (or timed out / cleared) | `false` |
| `pending_activation` | Input asserted; waiting for activation delay / debounce / retentive accumulation | `false` |
| `active` | Output true | `true` |
| `pending_deactivation` | Input de-asserted; waiting for deactivation delay / debounce | `true` |

`get(condition.name)` returns the stable output; pending states keep reporting
the output that was in effect before the pending transition began.
`state(condition.name)` returns the full internal state string, useful for
dashboards and diagnostics.

`trigger "watch"` fires **only on output transitions** (`inactive` → `active`
and `active` → `inactive`). Transitions into or out of the `pending_*` states
are internal and never fire the trigger.

---

## Common Behavioral Attributes

These attributes apply to every subtype unless the per-subtype section below
notes otherwise. All are optional; a condition with no attributes tracks its
input signal one-to-one.

### `activate_after`

```hcl
activate_after = "30s"
```

After the input asserts (or the counter reaches its preset), wait this
additional duration before the output activates. The timer does not reset if
the underlying signal flickers during the window — it is an intentional delay,
not a noise filter. Use `debounce` for noise filtering.

### `deactivate_after`

```hcl
deactivate_after = "5m"
```

After the output would otherwise deactivate, hold it active for this
additional duration. Prevents flapping and enforces a minimum active time.

### `timeout`

```hcl
timeout = "10m"
```

If the condition remains active for this duration, auto-deactivate. The
timeout clock starts on activation and restarts whenever the input is
re-asserted while the condition is already active. Ignored when
`latch = true`. When the condition boots active via `start_active`, the
timeout clock starts at boot (unless latched, in which case timeout is
ignored as usual).

### `latch`

```hcl
latch = true
```

Once active, stay active regardless of input. `deactivate_after` and
`timeout` are ignored while latched.

- Release a timer or threshold latch with `clear(condition.name)`.
- Release a counter latch with `reset(condition.name)` (which also resets the count).

`clear()` releases the latch but does **not** silence an ongoing-true input.
After resetting, a declared `input =` expression is re-sampled: if it is still
asserted (for `threshold`, with hysteresis applied from the freshly-reset
baseline), the condition re-activates and — when `latch = true` — re-engages
the latch. Debounce is bypassed on the re-activation edge since the signal
has already proven stable; `activate_after`, `cooldown`, and `inhibit` still
apply normally. This avoids the "latched fault you can clear while the cause
persists" footgun: clearing tells you whether the fault really went away,
rather than masking it.

### `start_active`

```hcl
start_active = true
```

Force the condition to begin in the `active` state at startup, rather than the
default `inactive`. No `inactive → active` transition event is emitted —
`trigger "watch"` will only fire on the first transition *out of* the boot-
active state. `activate_after` and `cooldown` do not apply to the boot state;
they govern input-driven activations.

Combined with `latch = true`, this implements the standard **fail-safe** /
**power-loss-is-a-fault** pattern: the system comes up with the condition
latched and cannot proceed until an operator clears it.

```hcl
# Power loss is a fault. Fault must be cleared before operation resumes.
condition "timer" "safety_fault" {
    latch        = true
    start_active = true
}

subscription "clear_fault" {
    bus    = bus.main
    topics = ["operator/reset"]
    action = clear(condition.safety_fault)
}
```

Without `latch`, the condition starts active but behaves normally from then on:
the next input edge (for `timer`), the first numeric sample crossing a
threshold (for `threshold`), or the `Start()` preset reconcile (for `counter`)
may deactivate it. This is the "assume worst until proven otherwise" variant.

**Interactions:**

- `start_active` sets the internal state. `invert` still applies as a final
  transform on the output — `start_active = true` combined with `invert = true`
  yields `get() == false` at boot.
- `inhibit` does **not** suppress `start_active`. A condition with
  `start_active = true` starts active even if `inhibit` evaluates `true` at
  boot, because the boot state is a configured initial condition, not a new
  activation. Inhibit takes effect from the first subsequent input event.
- `clear()` / `reset()` always return the condition to `inactive`. They do
  **not** restore the configured `start_active` state — otherwise a
  boot-latched fault could never be cleared.

### `invert`

```hcl
invert = true
```

Invert the logical output after all other rules apply. `get()` returns
`true` when the underlying state would otherwise be `false`, and vice versa.
Watcher notifications see the inverted values.

### `cooldown`

```hcl
cooldown = "5m"
```

Minimum quiet period between activations. After the condition fully
deactivates, it cannot re-activate until this duration has elapsed, even if
the input immediately re-asserts.

Distinct from `debounce` (which filters input noise *before* the first
activation) and from `deactivate_after` (which extends an existing active
period). Example: prevent a notification from firing more than once every
five minutes.

### `inhibit`

```hcl
inhibit = expression
```

A reactive boolean gate. While `inhibit` evaluates to `true`, the condition
cannot enter `pending_activation`. If it is already in `pending_activation`
when `inhibit` becomes true, the pending activation is cancelled and the
condition returns to `inactive`.

An already-active condition is **not** affected by `inhibit` becoming true —
inhibit prevents new activations, it does not force deactivation.

When `inhibit` clears while the input is still asserting, activation resumes
from scratch (including any `activate_after` delay).

**Interaction with retentive timers:** if a retentive timer is in
`pending_activation` (accumulating time) when `inhibit` becomes true, the
pending activation is cancelled and the accumulated time is discarded.

The `inhibit` expression is reactive: any `Watchable` it references
(conditions, variables, metrics) is subscribed to, and the expression is
re-evaluated whenever any source changes.

Example — suppress a temperature alarm during a scheduled maintenance window:

```hcl
condition "timer" "tank_alarm" {
    input   = get(condition.high_pressure)
    latch   = true
    inhibit = get(condition.maintenance_mode)
}
```

---

## Lifecycle Hooks

Three optional action-expression attributes — `on_init`, `on_activate`, and
`on_deactivate` — declare inline reactions to a condition's lifecycle events.
They are the locality-friendly alternative to a separate
`trigger "watch"` block for condition-local side effects.

| Hook | Fires | `ctx.new_value` | `ctx.old_value` |
|---|---|---|---|
| `on_init` | Once at startup, after every Startable has bootstrapped (runs in the `PostStart` phase). | condition's current output | — (absent) |
| `on_activate` | Each `inactive → active` output transition, on the user-visible edge (after `invert` applies). | `true` | `false` |
| `on_deactivate` | Each `active → inactive` output transition. | `false` | `true` |

All three expressions see the same `ctx`:

| Key | Value |
|---|---|
| `ctx.trigger` | `"condition"` |
| `ctx.name` | Name of this condition |
| `ctx.new_value` | See table above |
| `ctx.old_value` | See table above (not set for `on_init`) |

**Synchronous dispatch.** `on_activate` / `on_deactivate` fire inline on the
caller's goroutine at the moment of the transition — this means a `set()` /
`clear()` / `reset()` call blocks until the hook's action expression has
evaluated (and any side effects it issues have been enqueued). This is
different from `trigger "watch"`, which dispatches the action to a
goroutine. For high-throughput inputs where blocking is unacceptable, use
`trigger "watch"` instead. For ordering guarantees where the hook's side
effects must be visible before the caller returns, hooks are the right tool.

**Boot semantics.** `on_init` fires regardless of the boot state, once, at
`PostStart`. If `start_active = true`, `on_init` sees `new_value = true`.
`on_activate` does **not** fire at boot — consistent with the
"no synthetic transition event" rule documented under `start_active`.
Ordinary transitions *during* the Startables phase (e.g. an unlatched
`start_active` counter reconciling against its count) fire their
transition hook normally, which can produce an `on_deactivate` *before* the
later `on_init` fires.

**Context propagation.** The hook's `ctx` carries the caller's context when
the transition is input-driven (e.g. from a subscription action), including
any trace span. For autonomous transitions (timer callbacks like
`activate_after`, `deactivate_after`, `timeout`, `cooldown`) the hook runs
under a fresh root trace span. Every hook invocation opens a
`trigger.condition.<hook>` span.

**Errors** in hook expressions are logged to the user log
(condition name + hook name) and are non-fatal. A broken `on_activate`
does not prevent `on_deactivate` from firing on the next transition, nor
block other watchers.

**Re-entrancy.** Hooks fire outside the condition's state-machine lock, so a
hook calling `set()` / `clear()` / `reset()` on its own condition is safe.
Flicker-loops (`on_activate = clear(condition.self)`) are possible — the
same footgun that exists with `trigger "watch"` today. Don't do that.

### Hooks vs `trigger "watch"`

Both mechanisms observe the same output transitions. Use hooks when the
reaction is local to the condition and you want inline declaration,
synchronous dispatch, or guaranteed post-bootstrap ordering. Use
`trigger "watch"` when you want async dispatch, cross-cutting observers, or
decoupled reactions that live independently from the condition block.

### Example — fail-safe fault with status broadcasts

```hcl
condition "timer" "safety_fault" {
    latch         = true
    start_active  = true

    on_init       = send(ctx, bus.status, "fault/safety", {
                        active = ctx.new_value,
                        source = "boot",
                    })
    on_activate   = send(ctx, bus.status, "fault/safety", {
                        active = true,
                        source = "runtime",
                    })
    on_deactivate = send(ctx, bus.status, "fault/safety", {
                        active = false,
                        source = "cleared",
                    })
}

subscription "clear_fault" {
    bus    = bus.main
    topics = ["operator/reset"]
    action = clear(condition.safety_fault)
}
```

At boot the fault starts latched; `on_init` publishes an `active = true`
status message so any dashboard subscriber knows immediately. When the
operator clears the fault, `on_deactivate` publishes the transition.

---

## `condition "timer" "name"`

Applies temporal conditioning rules to a boolean signal. Equivalent in
capability to IEC 61131-3 timer function blocks (TON, TOF, TP, TONR) plus the
SR bistable.

```hcl
condition "timer" "name" {
    # behavioral attributes (all optional)
}
```

### Timer-specific attributes

#### `debounce`

```hcl
debounce = "50ms"
```

The input must be stable for this duration before any transition begins. The
timer **resets** whenever the input flips during the window, filtering
transient noise. Answers "is this change real?" When combined with
`activate_after`, debounce runs first: the input must settle, then the
activation delay begins.

#### `retentive`

```hcl
retentive = true
```

Accumulate elapsed time toward `activate_after` **across** multiple
asserted intervals instead of requiring continuous assertion. Accumulated
time persists until the condition activates or is explicitly cleared via
`clear()`. Corresponds to IEC 61131-3's TONR function block.

Example — alarm after the motor has run hot for a cumulative 10 minutes,
regardless of how the time is spread across intervals:

```hcl
condition "timer" "motor_overtemp" {
    input          = get(condition.high_temp)
    activate_after = "10m"
    retentive      = true
    latch          = true
}
```

#### `input` (declared expression)

```hcl
input = get(condition.high_temp) || get(condition.low_voltage)
```

When `input` is declared, the condition evaluates the expression reactively
— whenever any referenced Watchable changes — rather than relying on
imperative `set()` calls. Calling `set(condition.name, …)` on a condition
with a declared `input` is a runtime error.

When `input` is **not** declared, drive the condition imperatively:

```hcl
subscription "door_sensor" {
    bus    = bus.main
    topics = ["sensor/door"]
    action = set(condition.door_open, ctx.payload.open)
}
```

### Timer examples

Debounced door sensor:

```hcl
condition "timer" "door_open" {
    debounce = "50ms"
}
```

Temperature alarm with asymmetric delays and an absolute timeout:

```hcl
condition "timer" "high_temp_alarm" {
    input            = get(condition.high_temp)
    activate_after   = "30s"   # must be hot for 30s before alarming
    deactivate_after = "5m"    # alarm holds for 5m after cooling
    timeout          = "1h"    # auto-clear after 1h regardless
}
```

Latching fault with acknowledgement:

```hcl
condition "timer" "fault" {
    activate_after = "100ms"
    latch          = true
}

subscription "fault_events" {
    bus    = bus.main
    topics = ["system/fault"]
    action = set(condition.fault, true)
}

subscription "ack_events" {
    bus    = bus.main
    topics = ["operator/ack"]
    action = clear(condition.fault)
}
```

Rate-limited motion notification (at most one activation per 5 minutes):

```hcl
condition "timer" "motion_detected" {
    debounce = "100ms"
    cooldown = "5m"
}
```

---

## `condition "threshold" "name"`

Derives a boolean from a numeric input using separate activation and
deactivation thresholds (hysteresis). The gap between the two thresholds is
the **deadband** — a region where the output does not change — which prevents
rapid toggling when a value hovers near a single threshold point.

### Declaration

High-threshold form (activate when the value rises above a level):

```hcl
condition "threshold" "high_temp" {
    input     = get(metric.temperature)   # required: numeric expression
    on_above  = 80.0                      # activate when value crosses above
    off_below = 70.0                      # deactivate when value crosses below
}
```

Low-threshold form (activate when the value falls below a level):

```hcl
condition "threshold" "low_battery" {
    input     = get(metric.battery_pct)   # required: numeric expression
    on_below  = 20.0                      # activate when value crosses below
    off_above = 25.0                      # deactivate when value crosses above
}
```

The two forms are mutually exclusive; mixing attributes from both pairs is a
configuration error. For high form `on_above > off_below` is required; for
low form `off_above > on_below` is required.

### Required attributes

- `input` — a numeric expression, evaluated reactively whenever referenced
  Watchables change. There is no imperative `set()` for threshold conditions.
- One complete pair: either (`on_above`, `off_below`) or (`on_below`,
  `off_above`).

### Threshold-specific attribute notes

- **`debounce`** applies to the derived boolean (has the threshold been
  crossed?), not to the raw numeric value.
- **`retentive`** accumulates time spent above (or below) the threshold
  across multiple crossings; time in the deadband or on the inactive side
  does not accumulate.

### Initial state

If the input value starts within the hysteresis deadband at startup, the
initial output is `inactive`. The condition activates only when an
unambiguous threshold crossing is observed. Use `start_active = true` to
override this default — see the common attribute section above.

---

## `condition "counter" "name"`

Tracks a running integer count via `increment()` / `decrement()` calls and
produces a boolean output when the count reaches a configured preset.
Corresponds to IEC 61131-3 CTU, CTD, and CTUD function blocks.

```hcl
condition "counter" "fault_count" {
    preset = 5           # required
    initial = 0          # optional (default 0)
    rollover = false     # optional (default false)
    count_down = false   # optional (default false)
}
```

### Required attributes

- `preset` — the count value at which the output becomes true (after any
  `activate_after` delay).

### Optional counter-specific attributes

#### `initial`

```hcl
initial = 0   # default
```

The count value assigned at startup and after `reset()`. Allows starting at
a non-zero value — for example, a CTUD pattern that counts down from a
preset toward zero.

#### `rollover`

```hcl
rollover = false   # default
```

When `false`: the count saturates — it stops incrementing once it reaches
`preset` and stops decrementing below `0`. The condition remains active
until `reset()` is called.

When `true`: when the count reaches `preset`, the output fires and the
count automatically resets to `initial`. This implements a one-shot /
auto-reset pulse pattern.

The lower bound is always clamped at `0` by `decrement()` regardless of the
`rollover` setting (bidirectional CTUD use case).

**Interaction with `latch`:** when both `rollover = true` and `latch = true`
are set, the latch wins. Reaching `preset` auto-resets the count to
`initial` but the output remains continuously active — no deactivate/
reactivate edge is emitted, and `trigger "watch"` does not fire a spurious
transition.

#### `count_down`

```hcl
count_down = false   # default
```

When `false`: the output activates when `count >= preset` (count-up
semantics, CTU).

When `true`: the output activates when `count <= preset` (count-down
semantics, CTD). Typically paired with `initial = N` and `preset = 0` to
implement the classic "load N, count down to zero" pattern. All other
behavior (latch, activate_after, rollover, etc.) is unchanged — only the
comparison direction flips.

#### `window`

```hcl
window = "1m"   # optional, default unset
```

When set, the counter runs in **sliding-window** mode: the count reflects
the number of `increment()` calls in the last `window` duration. Each
increment timestamps the events into a FIFO; entries that age out (i.e.
`event_time + window <= now`) are dropped automatically. This implements
the classic "N events in the last T" rate primitive.

```hcl
# Trip if 5 errors arrive within any 1-minute span. Latched so the alarm
# survives the burst's tail aging out and requires explicit clear.
condition "counter" "error_rate" {
    preset = 5
    window = "1m"
    latch  = true
}
```

A single internal timer is armed for the next-to-expire event, so the
implementation cost is `O(1)` timers regardless of event rate; memory is
`O(N-in-window)` (one timestamp per live event).

`decrement(condition.x [, n])` pops the `n` oldest entries from the FIFO
(useful for "retract an in-flight count"); decrementing past empty is a
no-op.

`reset(condition.x)` and `clear(condition.x)` both empty the FIFO and
release any latch — counters have no `input =` to re-sample, so the two
functions are equivalent on a counter.

`window` is incompatible with `rollover`, `count_down`, and a non-zero
`initial`: rollover snap-back, count-down semantics, and synthetic
baseline events all require a notion of "current count" independent of
event timestamps. These combinations are rejected at parse time.

### Attribute applicability

Counter conditions support the common attributes `activate_after`,
`deactivate_after`, `timeout`, `latch`, `invert`, `cooldown`, `inhibit`.

Counter conditions do **not** support:

- `debounce` — the count is a discrete integer, not a noisy continuous signal.
- `retentive` — the counter itself is the accumulator; `retentive` is a timer concept.
- `input =` — input is driven exclusively by `increment()` and `decrement()` calls.

### Counter examples

Latching fault counter with acknowledgement:

```hcl
condition "counter" "fault_count" {
    preset = 5
    latch  = true
}

subscription "fault_events" {
    bus    = bus.main
    topics = ["system/fault"]
    action = increment(condition.fault_count)
}

subscription "ack_events" {
    bus    = bus.main
    topics = ["operator/ack"]
    action = reset(condition.fault_count)
}
```

CTUD bidirectional occupancy counter:

```hcl
condition "counter" "room_occupied" {
    preset  = 1
    initial = 0
}

subscription "entry_events" {
    bus    = bus.main
    topics = ["sensor/entry"]
    action = increment(condition.room_occupied)
}

subscription "exit_events" {
    bus    = bus.main
    topics = ["sensor/exit"]
    action = decrement(condition.room_occupied)
}

# get(condition.room_occupied) → true when anyone is present
# count(condition.room_occupied) → number of people currently in the room
```

Count-down batch:

```hcl
condition "counter" "batch_remaining" {
    initial    = 10
    preset     = 0
    count_down = true
}
```

---

## Functions

See [functions.md](functions.md#conditions) for the full reference. A short
summary:

| Function | Applies to | Description |
|---|---|---|
| `get(condition.name)` → bool | all | Current boolean output |
| `state(condition.name)` → string | all | Current internal state name |
| `set(condition.name, value)` | timer (no declared `input`) | Provide the boolean input |
| `clear(condition.name)` | timer, threshold | Reset to inactive, release latch, discard retentive accumulation |
| `increment(condition.name[, n])` | counter | Add `n` (default 1) to the count |
| `decrement(condition.name[, n])` | counter | Subtract `n` (default 1) from the count |
| `reset(condition.name)` | counter | Reset count to `initial`, release latch, return to inactive |
| `count(condition.name)` → number | counter | Current numeric count value |

`count()` also works on trigger types that track a run count
(`trigger "at"`, `trigger "interval"`, `trigger "file"`), returning the
lifetime number of times the trigger has fired — equivalent to
`ctx.run_count` inside that trigger's own action, but accessible from any
other expression.

---

## Composition

Conditions compose cleanly. `get(condition.name)` may appear in any boolean
expression and in the `input =` attribute of another condition, enabling
declarative pipelines:

```hcl
# Threshold detects the raw signal
condition "threshold" "high_temp" {
    input     = get(metric.temperature)
    on_above  = 80.0
    off_below = 70.0
    activate_after = "30s"
}

# Counter accumulates events; latches after 3 exceedances
condition "counter" "high_temp_events" {
    preset = 3
    latch  = true
}

trigger "watch" "count_exceedances" {
    watch     = condition.high_temp
    skip_when = !ctx.new_value   # count only rising edges
    action    = increment(condition.high_temp_events)
}

# Master fault: either sustained heat or repeated exceedances
condition "timer" "system_fault" {
    input = get(condition.high_temp) || get(condition.high_temp_events)
    latch = true
}
```

Circular dependencies between conditions (via `input =` or `inhibit =`) are
detected at configuration load time and rejected.

---

## Integration with `trigger "watch"`

All condition types are Watchable and integrate with `trigger "watch"`
identically. The trigger fires on output state transitions
(`inactive` → `active` and back). Pending-state transitions do not fire it.

```hcl
trigger "watch" "alarm_on" {
    watch     = condition.high_temp
    skip_when = !ctx.new_value     # skip deactivation events
    action    = send(ctx, bus.main, "alarm/high_temp", "activated")
}
```

To observe pending state, poll `state(condition.name)` from a cron or
interval trigger.
