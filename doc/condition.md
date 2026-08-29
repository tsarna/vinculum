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

There are four subtypes:

<!-- vinculum:begin block-index condition level=3 -->

- [`condition "counter"`](condition.md#condition-counter-name) — Counts events and activates when the count reaches a preset.
- [`condition "flipflop"`](condition.md#condition-flipflop-name) — Edge-driven bistable: T, SR, JK, D, D-latch, and gated variants.
- [`condition "threshold"`](condition.md#condition-threshold-name) — Derives a boolean from a numeric input, with hysteresis.
- [`condition "timer"`](condition.md#condition-timer-name) — Applies temporal rules to a boolean signal.

<!-- vinculum:end block-index condition -->

They differ in what drives them. A `timer` takes a boolean — imperatively via
`set()`, or from a declared `input` expression. A `threshold` reads a numeric
`input`. A `counter` is driven only by `increment()` and `decrement()` calls.
A `flipflop` is driven by boolean *wire* expressions, on their edges.

All four share a common four-state model and a common set of behavioral
attributes described below. Each subtype's own section carries the full
attribute reference for it, including which of the common attributes it
accepts.

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

## Behavior Common to Every Subtype

Every subtype draws on the same vocabulary of behavioral attributes —
`activate_after`, `deactivate_after`, `timeout`, `latch`, `start_active`,
`invert`, `cooldown`, `inhibit`, and, where they make sense, `debounce` and
`retentive`. All are optional: a condition that declares none tracks its input
one-to-one. Each subtype's reference table below lists exactly the ones it
accepts, with their types and defaults.

### Delaying, filtering, and rate-limiting

Three of them are easy to confuse, because each one stops a condition from
reacting immediately. They act at different points:

| Attribute | Acts on | Answers |
|---|---|---|
| `debounce` | the input, before any transition begins | Is this change real? |
| `activate_after` | the transition into `active` | Has it been true long enough to matter? |
| `cooldown` | the *next* activation, after deactivating | Not more often than this |

`debounce` restarts its timer whenever the input flips, so noise never gets
through. `activate_after` does not — it is a deliberate delay, and a signal
that flickers during the window still activates at the end of it. Combining
them runs debounce first.

`deactivate_after` completes the family and is the only one that *extends*
rather than defers: it holds an already-active output up past the point it
would otherwise drop.

### Fail-safe start

`start_active = true` with `latch = true` is the standard
power-loss-is-a-fault pattern. The system comes up with the condition already
latched and cannot proceed until an operator clears it:

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

Without `latch` this is instead the "assume the worst until proven otherwise"
variant: the condition starts active, and the first thing that would normally
deactivate it does — the next input edge for a `timer`, the first numeric
sample for a `threshold`, the `Start()` preset reconcile for a `counter`.

Clearing a latch does not silence a cause that is still present. A declared
`input` is re-sampled on `clear()`, so a condition whose input is still
asserted re-activates and re-latches immediately. That is deliberate: clearing
tells you whether the fault really went away instead of masking it.

### Suppressing a condition while something else is true

`inhibit` is a reactive boolean gate. While it is true the condition cannot
enter `pending_activation`, and a pending activation already underway is
cancelled. It does not deactivate a condition that is already active — it
prevents activations, it does not force deactivations.

Any `Watchable` the expression references — conditions, variables, metrics —
is subscribed to, and the expression is re-evaluated whenever any of them
changes.

```hcl
# Suppress the tank alarm during a scheduled maintenance window.
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

<!-- vinculum:begin context condition-hook level=3 -->

Evaluated on a condition's lifecycle transition.

Hooks fire inline on the goroutine that caused the transition, so the
`set()` / `clear()` / `reset()` call blocks until the hook returns.

Fields readable as `ctx.<name>` (shape `condition-hook`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"condition"`. |
| `ctx.name` | string | Name of the condition. |
| `ctx.new_value` | bool | The output after the transition. |
| `ctx.old_value` | bool | The output before the transition. *(not always present)* |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.new_value`**

For `on_init`, the condition's current output at startup, whatever it is.

**`ctx.old_value`**

Absent in `on_init`, which reports a starting state rather than a transition.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

#### Evaluated by

- `condition "counter"` › `on_init`
- `condition "counter"` › `on_activate`
- `condition "counter"` › `on_deactivate`
- `condition "flipflop"` › `on_init`
- `condition "flipflop"` › `on_activate`
- `condition "flipflop"` › `on_deactivate`
- `condition "threshold"` › `on_init`
- `condition "threshold"` › `on_activate`
- `condition "threshold"` › `on_deactivate`
- `condition "timer"` › `on_init`
- `condition "timer"` › `on_activate`
- `condition "timer"` › `on_deactivate`

<!-- vinculum:end context condition-hook -->

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

A timer can be driven either declaratively or imperatively. Declare an `input`
and it evaluates that expression reactively, whenever any Watchable the
expression reads changes:

```hcl
input = get(condition.high_temp) || get(condition.low_voltage)
```

Leave `input` out and drive it imperatively instead:

```hcl
subscription "door_sensor" {
    bus    = bus.main
    topics = ["sensor/door"]
    action = set(condition.door_open, ctx.payload.open)
}
```

It is one or the other: calling `set()` on a condition that declares an
`input` is a runtime error.

### Timer attributes

<!-- vinculum:begin block-attrs condition timer level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `activate_after` | expression (duration) |  | Wait this long after the input asserts before activating. |
| `cooldown` | expression (duration) |  | Minimum quiet period between activations. |
| `deactivate_after` | expression (duration) |  | Hold the output active this long after it would otherwise deactivate. |
| `debounce` | expression (duration) |  | The input must be stable this long before any transition begins. |
| `disabled` | bool |  | Skip this block entirely. |
| `inhibit` | expression (reactive-expression) |  | While true, block new activations. |
| `input` | expression (reactive-expression) |  | Boolean expression driving the condition. |
| `invert` | bool |  | Invert the output after every other rule applies. |
| `latch` | bool |  | Once active, stay active regardless of input. |
| `on_activate` | expression (action-expression) |  | Evaluated on each transition to active. |
| `on_deactivate` | expression (action-expression) |  | Evaluated on each transition to inactive. |
| `on_init` | expression (action-expression) |  | Evaluated once at startup, after every startable component is ready. |
| `retentive` | bool |  | Accumulate time toward `activate_after` across separate asserted intervals. |
| `start_active` | bool |  | Begin in the active state at startup. |
| `timeout` | expression (duration) |  | Auto-deactivate after this long active. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`activate_after`**

An intentional delay, not a noise filter: the timer does not restart if the underlying signal flickers during the window. Use `debounce` to filter noise.

**`cooldown`**

After deactivating, the condition cannot re-activate until this has elapsed, even if the input immediately re-asserts. Distinct from `debounce` (which filters input noise before the first activation) and `deactivate_after` (which extends an active period).

**`deactivate_after`**

Prevents flapping and enforces a minimum active time.

**`debounce`**

The timer restarts whenever the input flips during the window, filtering transient noise: it answers "is this change real?". Combined with `activate_after`, debounce runs first — the input settles, then the activation delay begins.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`inhibit`**

A pending activation is cancelled and the condition returns to inactive; a retentive timer discards its accumulated time. An already-active condition is unaffected — inhibit prevents activation, it does not force deactivation. When it clears with the input still asserting, activation resumes from scratch, including any `activate_after` delay.

**`input`**

Re-evaluated whenever any watchable it references changes. Omit it to drive the condition imperatively with `set(condition.<name>, bool)` instead — calling `set()` on a condition that declares `input` is a runtime error.

**`invert`**

`get()` returns true where the underlying state would be false, and watchers see the inverted values.

**`latch`**

`deactivate_after` and `timeout` are ignored while latched. Release with `clear(condition.<name>)` — or `reset()` on a counter, which also resets the count. Clearing does not silence an input that is still asserting: a declared `input` is re-sampled and may re-activate and re-latch immediately, so clearing tells you whether the cause really went away rather than masking it. That re-activation edge skips `debounce`, since the signal has already proven stable, but `activate_after`, `cooldown`, and `inhibit` apply to it as usual.

**`on_activate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_deactivate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_init`**

Fires whatever the boot state, with `ctx.new_value` set to the condition's current output and no `ctx.old_value`. `on_activate` does not fire at boot, so this is how a dashboard learns the initial state.

Evaluated against the `condition-hook` context.

**`retentive`**

Rather than requiring continuous assertion. Accumulated time persists until the condition activates or is cleared. Corresponds to IEC 61131-3's TONR.

**`start_active`**

No transition event is emitted, so `on_activate` and `trigger "watch"` fire only on the first transition *out of* the boot state. `activate_after`, `cooldown`, and `inhibit` do not apply to it — they govern input-driven activations, and the boot state is a configured starting point rather than an activation. `invert` does still apply, so `start_active` and `invert` together boot to a `get()` of false. With `latch = true` this is the standard fail-safe pattern: the system comes up latched and an operator must clear it before work resumes. Without a latch the condition merely starts active and behaves normally from the next input onward. `clear()` and `reset()` return to inactive; they never restore this state, or a boot-latched fault could never be cleared.

**`timeout`**

The clock starts on activation and restarts whenever the input re-asserts while already active. A condition that boots active through `start_active` starts its clock at boot. Ignored when `latch = true`.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted. A condition is traced through its lifecycle hooks: each `on_init` / `on_activate` / `on_deactivate` evaluation runs in a `condition.<hook>` span. A condition with no hooks has nothing to trace.

<!-- vinculum:end block-attrs condition timer -->

### Timer examples

Cumulative run time — alarm after the motor has run hot for a total of ten
minutes, however the time is spread across intervals:

```hcl
condition "timer" "motor_overtemp" {
    input          = get(condition.high_temp)
    activate_after = "10m"
    retentive      = true
    latch          = true
}
```

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
    input     = get(metric.temperature)
    on_above  = 80.0   # activate when the value crosses above
    off_below = 70.0   # deactivate when it crosses back below
}
```

Low-threshold form (activate when the value falls below a level):

```hcl
condition "threshold" "low_battery" {
    input     = get(metric.battery_pct)
    on_below  = 20.0   # activate when the value crosses below
    off_above = 25.0   # deactivate when it crosses back above
}
```

The two forms are mutually exclusive; mixing attributes from both pairs is a
configuration error. For high form `on_above > off_below` is required; for
low form `off_above > on_below` is required. There is no imperative `set()`
for a threshold condition — the `input` expression is the only way to drive it.

### Initial state

If the input value starts within the hysteresis deadband at startup, the
initial output is `inactive`. The condition activates only when an
unambiguous threshold crossing is observed. Use `start_active = true` to
override this default.

### Threshold attributes

<!-- vinculum:begin block-attrs condition threshold level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `input` | expression (reactive-expression) | yes | Numeric expression to compare against the thresholds. |
| `activate_after` | expression (duration) |  | Wait this long after the input asserts before activating. |
| `cooldown` | expression (duration) |  | Minimum quiet period between activations. |
| `deactivate_after` | expression (duration) |  | Hold the output active this long after it would otherwise deactivate. |
| `debounce` | expression (duration) |  | The input must be stable this long before any transition begins. |
| `disabled` | bool |  | Skip this block entirely. |
| `inhibit` | expression (reactive-expression) |  | While true, block new activations. |
| `invert` | bool |  | Invert the output after every other rule applies. |
| `latch` | bool |  | Once active, stay active regardless of input. |
| `off_above` | number |  | Deactivate when the value crosses above this. |
| `off_below` | number |  | Deactivate when the value crosses below this. |
| `on_above` | number |  | Activate when the value crosses above this. |
| `on_activate` | expression (action-expression) |  | Evaluated on each transition to active. |
| `on_below` | number |  | Activate when the value crosses below this. |
| `on_deactivate` | expression (action-expression) |  | Evaluated on each transition to inactive. |
| `on_init` | expression (action-expression) |  | Evaluated once at startup, after every startable component is ready. |
| `retentive` | bool |  | Accumulate time toward `activate_after` across separate asserted intervals. |
| `start_active` | bool |  | Begin in the active state at startup. |
| `timeout` | expression (duration) |  | Auto-deactivate after this long active. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

- on_above and off_below must be specified together.
- on_below and off_above must be specified together.
- the high form (on_above/off_below) and the low form (on_below/off_above) cannot be mixed
- the high form (on_above/off_below) and the low form (on_below/off_above) cannot be mixed
- a threshold condition needs one complete pair: on_above/off_below or on_below/off_above

**`input`**

Re-evaluated whenever any watchable it references changes. There is no imperative `set()` for a threshold condition.

**`activate_after`**

An intentional delay, not a noise filter: the timer does not restart if the underlying signal flickers during the window. Use `debounce` to filter noise.

**`cooldown`**

After deactivating, the condition cannot re-activate until this has elapsed, even if the input immediately re-asserts. Distinct from `debounce` (which filters input noise before the first activation) and `deactivate_after` (which extends an active period).

**`deactivate_after`**

Prevents flapping and enforces a minimum active time.

**`debounce`**

Applies to the derived boolean — has the threshold been crossed? — not to the raw numeric value.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`inhibit`**

A pending activation is cancelled and the condition returns to inactive; a retentive timer discards its accumulated time. An already-active condition is unaffected — inhibit prevents activation, it does not force deactivation. When it clears with the input still asserting, activation resumes from scratch, including any `activate_after` delay.

**`invert`**

`get()` returns true where the underlying state would be false, and watchers see the inverted values.

**`latch`**

`deactivate_after` and `timeout` are ignored while latched. Release with `clear(condition.<name>)` — or `reset()` on a counter, which also resets the count. Clearing does not silence an input that is still asserting: a declared `input` is re-sampled and may re-activate and re-latch immediately, so clearing tells you whether the cause really went away rather than masking it. That re-activation edge skips `debounce`, since the signal has already proven stable, but `activate_after`, `cooldown`, and `inhibit` apply to it as usual.

**`off_above`**

Pairs with `on_below`.

**`off_below`**

Pairs with `on_above`.

**`on_above`**

Pairs with `off_below`.

**`on_activate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_below`**

Pairs with `off_above`.

**`on_deactivate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_init`**

Fires whatever the boot state, with `ctx.new_value` set to the condition's current output and no `ctx.old_value`. `on_activate` does not fire at boot, so this is how a dashboard learns the initial state.

Evaluated against the `condition-hook` context.

**`retentive`**

Time spent above (or below) the threshold accumulates across separate crossings; time in the deadband or on the inactive side does not.

**`start_active`**

No transition event is emitted, so `on_activate` and `trigger "watch"` fire only on the first transition *out of* the boot state. `activate_after`, `cooldown`, and `inhibit` do not apply to it — they govern input-driven activations, and the boot state is a configured starting point rather than an activation. `invert` does still apply, so `start_active` and `invert` together boot to a `get()` of false. With `latch = true` this is the standard fail-safe pattern: the system comes up latched and an operator must clear it before work resumes. Without a latch the condition merely starts active and behaves normally from the next input onward. `clear()` and `reset()` return to inactive; they never restore this state, or a boot-latched fault could never be cleared.

**`timeout`**

The clock starts on activation and restarts whenever the input re-asserts while already active. A condition that boots active through `start_active` starts its clock at boot. Ignored when `latch = true`.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted. A condition is traced through its lifecycle hooks: each `on_init` / `on_activate` / `on_deactivate` evaluation runs in a `condition.<hook>` span. A condition with no hooks has nothing to trace.

<!-- vinculum:end block-attrs condition threshold -->

---

## `condition "counter" "name"`

Tracks a running integer count via `increment()` / `decrement()` calls and
produces a boolean output when the count reaches a configured preset.
Corresponds to IEC 61131-3 CTU, CTD, and CTUD function blocks.

```hcl
condition "counter" "fault_count" {
    preset = 5
}
```

`decrement()` always clamps the count at `0`, whatever else is configured,
which is what makes the bidirectional CTUD pattern work.

### Sliding-window mode

Setting `window` turns the counter into the classic "N events in the last T"
rate primitive: the count becomes the number of `increment()` calls inside the
window, and entries age out on their own.

```hcl
# Trip if 5 errors arrive within any 1-minute span. Latched so the alarm
# survives the burst's tail aging out and requires an explicit clear.
condition "counter" "error_rate" {
    preset = 5
    window = "1m"
    latch  = true
}
```

One internal timer is armed for the next event to expire, so the cost is
`O(1)` timers regardless of event rate; memory is one timestamp per live
event. `decrement(condition.x [, n])` pops the `n` oldest entries — useful to
retract an in-flight count — and decrementing past empty is a no-op.

### Counter attributes

<!-- vinculum:begin block-attrs condition counter level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `preset` | number | yes |  | The count at which the output activates. |
| `activate_after` | expression (duration) |  |  | Wait this long after the input asserts before activating. |
| `cooldown` | expression (duration) |  |  | Minimum quiet period between activations. |
| `count_down` | bool |  | `false` | Activate when the count falls to `preset` rather than rising to it. |
| `deactivate_after` | expression (duration) |  |  | Hold the output active this long after it would otherwise deactivate. |
| `debounce` | expression |  |  | Not supported on a counter. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `inhibit` | expression (reactive-expression) |  |  | While true, block new activations. |
| `initial` | number |  | `0` | The count assigned at startup and after `reset()`. |
| `input` | expression |  |  | Not supported on a counter. |
| `invert` | bool |  |  | Invert the output after every other rule applies. |
| `latch` | bool |  |  | Once active, stay active regardless of input. |
| `on_activate` | expression (action-expression) |  |  | Evaluated on each transition to active. |
| `on_deactivate` | expression (action-expression) |  |  | Evaluated on each transition to inactive. |
| `on_init` | expression (action-expression) |  |  | Evaluated once at startup, after every startable component is ready. |
| `retentive` | bool |  |  | Not supported on a counter. |
| `rollover` | bool |  | `false` | Reset the count to `initial` on reaching `preset`. |
| `start_active` | bool |  |  | Begin in the active state at startup. |
| `timeout` | expression (duration) |  |  | Auto-deactivate after this long active. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `window` | expression (duration) |  |  | Count only events from the last this-long. |

**`preset`**

After any `activate_after` delay.

**`activate_after`**

An intentional delay, not a noise filter: the timer does not restart if the underlying signal flickers during the window. Use `debounce` to filter noise.

**`cooldown`**

After deactivating, the condition cannot re-activate until this has elapsed, even if the input immediately re-asserts. Distinct from `debounce` (which filters input noise before the first activation) and `deactivate_after` (which extends an active period).

**`count_down`**

Only the comparison direction flips; everything else behaves the same. Typically paired with `initial = N` and `preset = 0` for the classic load-N-and-count-to-zero pattern.

**`deactivate_after`**

Prevents flapping and enforces a minimum active time.

**`debounce`**

The count is a discrete integer, not a noisy continuous signal.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`inhibit`**

A pending activation is cancelled and the condition returns to inactive; a retentive timer discards its accumulated time. An already-active condition is unaffected — inhibit prevents activation, it does not force deactivation. When it clears with the input still asserting, activation resumes from scratch, including any `activate_after` delay.

**`initial`**

Starting non-zero supports the CTUD pattern of counting down from a preset toward zero.

**`input`**

The count is driven by `increment()` and `decrement()` calls.

**`invert`**

`get()` returns true where the underlying state would be false, and watchers see the inverted values.

**`latch`**

`deactivate_after` and `timeout` are ignored while latched. Release with `clear(condition.<name>)` — or `reset()` on a counter, which also resets the count. Clearing does not silence an input that is still asserting: a declared `input` is re-sampled and may re-activate and re-latch immediately, so clearing tells you whether the cause really went away rather than masking it. That re-activation edge skips `debounce`, since the signal has already proven stable, but `activate_after`, `cooldown`, and `inhibit` apply to it as usual.

**`on_activate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_deactivate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_init`**

Fires whatever the boot state, with `ctx.new_value` set to the condition's current output and no `ctx.old_value`. `on_activate` does not fire at boot, so this is how a dashboard learns the initial state.

Evaluated against the `condition-hook` context.

**`retentive`**

The counter is itself the accumulator; `retentive` is a timer concept.

**`rollover`**

When false the count saturates: it stops at `preset` going up and at 0 going down, and the output stays active until `reset()`. When true, reaching `preset` fires and snaps the count back — a one-shot pulse. `decrement()` clamps at 0 either way. If `latch` is also set the latch wins: the count snaps back but the output stays continuously active, with no spurious deactivate/reactivate edge.

**`start_active`**

No transition event is emitted, so `on_activate` and `trigger "watch"` fire only on the first transition *out of* the boot state. `activate_after`, `cooldown`, and `inhibit` do not apply to it — they govern input-driven activations, and the boot state is a configured starting point rather than an activation. `invert` does still apply, so `start_active` and `invert` together boot to a `get()` of false. With `latch = true` this is the standard fail-safe pattern: the system comes up latched and an operator must clear it before work resumes. Without a latch the condition merely starts active and behaves normally from the next input onward. `clear()` and `reset()` return to inactive; they never restore this state, or a boot-latched fault could never be cleared.

**`timeout`**

The clock starts on activation and restarts whenever the input re-asserts while already active. A condition that boots active through `start_active` starts its clock at boot. Ignored when `latch = true`.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted. A condition is traced through its lifecycle hooks: each `on_init` / `on_activate` / `on_deactivate` evaluation runs in a `condition.<hook>` span. A condition with no hooks has nothing to trace.

**`window`**

Switches the counter to sliding-window mode, the classic "N events in T" rate primitive: each increment is timestamped and entries age out automatically. `decrement()` pops the oldest entries. Cannot be combined with `rollover = true`, `count_down = true`, or a non-zero `initial`, all of which need a current count independent of event timestamps.

<!-- vinculum:end block-attrs condition counter -->

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

## `condition "flipflop" "name"`

A flipflop exposes the standard digital-logic bistables — T, SR, gated SR, D,
D-latch, JK — through a uniform set of **wire** attributes. Each wire is a
boolean expression watched reactively; the *combination* of wires declared
names the variant. Use a flipflop when you need edge-driven state
(toggle-on-press), multi-input set/reset, or sample-and-hold of one signal on
the edge of another. (For purely temporal "active for N seconds" behavior, use
`condition "timer"` instead — a flipflop responds to its inputs immediately.)

### Wires

Each wire must evaluate to a boolean; null / non-boolean values are logged to
the user log and ignored. At least one of `set_on`, `reset_on`, `toggle_on`,
or `set_from` must be declared.

| Wire | Edge attribute | Effect |
|---|---|---|
| `set_on` | `set_edge` | On its edge, drive the output **true** |
| `reset_on` | `reset_edge` | On its edge, drive the output **false** |
| `toggle_on` | `toggle_edge` | On its edge, **flip** the output |
| `set_from` + `gate_on` | `gate_edge` | Sample `set_from`'s level when the gate permits |

**Event wires** (`set_on` / `reset_on` / `toggle_on`) fire on an *edge* of their
expression, and their edge attribute says which:

| Edge | Fires when |
|---|---|
| `"rising"` | previous evaluation was `false`, current is `true` |
| `"falling"` | previous was `true`, current is `false` |
| `"both"` | value changed in either direction |

An edge attribute only means something alongside its wire, so declaring one
without the other — `set_edge` with no `set_on` — is a configuration error
rather than a silently ignored line.

The **first evaluation** of every wire (at startup) only establishes the
baseline and fires **no** edge — a source that happens to be asserting at boot
will not spuriously drive the flipflop. Use `start_active` to boot the output
true.

**`set_from` + `gate_on`** implement the D variants. `set_from` is a *level*
that is never edge-detected on its own; it is *sampled* when `gate_on` permits.
The gate's `gate_edge` additionally accepts the level-sensitive modes `"high"`
and `"low"`:

- `"rising"` / `"falling"` / `"both"` — sample `set_from` on that edge of the
  gate (edge-triggered **D flip-flop**).
- `"high"` — while `gate_on` is true, the output tracks `set_from` reactively;
  when the gate goes false the last sampled value is held (**D latch**,
  active-high). `"low"` is the symmetric active-low latch.

`gate_on` may also be declared **without** `set_from`, where it acts as an
enable that gates the effective window of `set_on` / `reset_on` / `toggle_on`
(the **gated SR / gated T** pattern — edges outside the window are ignored).
`set_from` **without** `gate_on` is a configuration error.

### Wire combinations and resulting variants

| Wires declared | Variant |
|---|---|
| `toggle_on` only | T flip-flop |
| `set_on` + `reset_on` | SR flip-flop |
| `set_on` + `reset_on` + `toggle_on` | JK flip-flop |
| `set_on` + `reset_on` + `gate_on` (level) | Gated SR |
| `set_from` + `gate_on` (edge) | D flip-flop |
| `set_from` + `gate_on` (level) | D latch |
| `toggle_on` + `gate_on` (level) | Gated T |

### Conflict resolution

When one notification causes more than one wire to fire in the same evaluation
(common when wires share an upstream source), the flipflop resolves to a single
output value per this priority:

1. **Gate first.** If `gate_on` is configured and its edge / level criterion is
   not satisfied this cycle, the other wires are suppressed.
2. **Set/Reset dominance.** If both `set_on` and `reset_on` fire, `dominant`
   picks the winner.
3. **Set/Reset over toggle.** A `set_on` / `reset_on` fire wins over `toggle_on`.
4. **D-sample over toggle.** A `set_from` sample is applied before `toggle_on`.

Conflict resolution is atomic only *within* a single notification — i.e. across
wires sharing the source that fired. Two *different* sources changing
"simultaneously" arrive as sequential notifications; `dominant` still resolves
the eventual state, but a downstream watcher may briefly observe the
intermediate value. Funnel inputs through a single derived source upstream if
you need strict cross-input atomicity.

### Flipflop attributes

A flipflop responds to its inputs immediately, so it accepts none of the
temporal attributes — no `activate_after`, `deactivate_after`, `timeout`,
`retentive`, or `debounce`, and no `input`. Debounce belongs on the
signal-producing source upstream; for self-deactivating or continuous-level
behavior use `condition "timer"`.

<!-- vinculum:begin block-attrs condition flipflop level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `cooldown` | expression (duration) |  |  | Minimum quiet period between activations. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `dominant` | string |  | `"reset"` | Which wire wins when `set_on` and `reset_on` fire together. |
| `gate_edge` | string |  | `"rising"` | How `gate_on` gates. |
| `gate_on` | expression (reactive-expression) |  |  | Gate controlling when the other wires take effect. |
| `inhibit` | expression (reactive-expression) |  |  | While true, block new activations. |
| `invert` | bool |  |  | Invert the output after every other rule applies. |
| `latch` | bool |  |  | Once active, stay active regardless of input. |
| `on_activate` | expression (action-expression) |  |  | Evaluated on each transition to active. |
| `on_deactivate` | expression (action-expression) |  |  | Evaluated on each transition to inactive. |
| `on_init` | expression (action-expression) |  |  | Evaluated once at startup, after every startable component is ready. |
| `reset_edge` | string |  | `"rising"` | Which edge of `reset_on` fires. |
| `reset_on` | expression (reactive-expression) |  |  | On this wire's edge, drive the output false. |
| `set_edge` | string |  | `"rising"` | Which edge of `set_on` fires. |
| `set_from` | expression (reactive-expression) |  |  | Level sampled into the output when the gate permits. |
| `set_on` | expression (reactive-expression) |  |  | On this wire's edge, drive the output true. |
| `start_active` | bool |  |  | Begin in the active state at startup. |
| `toggle_edge` | string |  | `"rising"` | Which edge of `toggle_on` fires. |
| `toggle_on` | expression (reactive-expression) |  |  | On this wire's edge, flip the output. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |

- a flipflop needs at least one of set_on, reset_on, toggle_on, or set_from
- set_from requires gate_on.
- set_edge requires set_on.
- reset_edge requires reset_on.
- toggle_edge requires toggle_on.
- gate_edge requires gate_on.

**`cooldown`**

After deactivating, the condition cannot re-activate until this has elapsed, even if the input immediately re-asserts. Distinct from `debounce` (which filters input noise before the first activation) and `deactivate_after` (which extends an active period).

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`dominant`**

One of: `reset`, `set`.

**`gate_edge`**

`"rising"`, `"falling"`, and `"both"` sample on that edge, giving an edge-triggered D flip-flop. `"high"` makes the output track `set_from` reactively while the gate is true and hold the last sample when it goes false — an active-high D latch; `"low"` is the active-low counterpart.

One of: `rising`, `falling`, `both`, `high`, `low`.

**`gate_on`**

With `set_from`, it clocks the sample. Without `set_from`, it is an enable that gates the effective window of `set_on` / `reset_on` / `toggle_on` — edges outside the window are ignored.

**`inhibit`**

A pending activation is cancelled and the condition returns to inactive; a retentive timer discards its accumulated time. An already-active condition is unaffected — inhibit prevents activation, it does not force deactivation. When it clears with the input still asserting, activation resumes from scratch, including any `activate_after` delay.

**`invert`**

`get()` returns true where the underlying state would be false, and watchers see the inverted values.

**`latch`**

A latched flipflop ignores `reset_on`, gate drop-out, and deactivating `toggle_on` flips until released with `clear(condition.<name>)`.

**`on_activate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_deactivate`**

Fires on the user-visible edge, after `invert` applies, inline on the goroutine that caused the transition.

Evaluated against the `condition-hook` context.

**`on_init`**

Fires whatever the boot state, with `ctx.new_value` set to the condition's current output and no `ctx.old_value`. `on_activate` does not fire at boot, so this is how a dashboard learns the initial state.

Evaluated against the `condition-hook` context.

**`reset_edge`**

One of: `rising`, `falling`, `both`.

**`set_edge`**

One of: `rising`, `falling`, `both`.

**`set_from`**

Never edge-detected on its own — this is the D input. Requires `gate_on`.

**`start_active`**

No transition event is emitted, so `on_activate` and `trigger "watch"` fire only on the first transition *out of* the boot state. `activate_after`, `cooldown`, and `inhibit` do not apply to it — they govern input-driven activations, and the boot state is a configured starting point rather than an activation. `invert` does still apply, so `start_active` and `invert` together boot to a `get()` of false. With `latch = true` this is the standard fail-safe pattern: the system comes up latched and an operator must clear it before work resumes. Without a latch the condition merely starts active and behaves normally from the next input onward. `clear()` and `reset()` return to inactive; they never restore this state, or a boot-latched fault could never be cleared.

**`toggle_edge`**

One of: `rising`, `falling`, `both`.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted. A condition is traced through its lifecycle hooks: each `on_init` / `on_activate` / `on_deactivate` evaluation runs in a `condition.<hook>` span. A condition with no hooks has nothing to trace.

<!-- vinculum:end block-attrs condition flipflop -->

### Flipflop examples

T flip-flop — toggle a lamp on each button press:

```hcl
condition "flipflop" "lamp" {
    toggle_on = get(condition.button)   # default toggle_edge = "rising"
}
```

SR flip-flop — latching fault with operator reset (reset wins by default):

```hcl
condition "flipflop" "fault" {
    set_on   = get(condition.high_temp) || get(condition.low_voltage)
    reset_on = get(condition.operator_ack)
}
```

D flip-flop — capture one signal on the rising edge of another:

```hcl
condition "flipflop" "captured_state" {
    set_from  = get(condition.measured_high)
    gate_on   = get(condition.clock_pulse)
    gate_edge = "rising"
}
```

D latch — output tracks the input while enable is high:

```hcl
condition "flipflop" "tracking" {
    set_from  = get(metric.live_value_gt_threshold)
    gate_on   = get(condition.enable)
    gate_edge = "high"
}
```

JK flip-flop — set, reset, and toggle in one:

```hcl
condition "flipflop" "mode" {
    set_on    = get(condition.go_button)
    reset_on  = get(condition.stop_button)
    toggle_on = get(condition.flip_button)
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
| `set(condition.name, value)` | timer (no declared `input`), flipflop | Force the boolean output (honors latch / inhibit / cooldown) |
| `toggle(condition.name)` → bool | timer (no declared `input`), flipflop | Flip the output; equivalent to `set(condition.name, !current)`. Returns the new value |
| `clear(condition.name)` | all | Reset to inactive, release latch, discard retentive accumulation. On a counter it is identical to `reset()` — there is no `input` to re-sample |
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

### Composing a condition into a health check

A condition computes a boolean and says nothing about what it is *for*. A
[`check`](health.md#the-check-block) is the opposite: it attaches meaning to one
— this boolean, when false, means do not send this process traffic. So a health
check that needs flapping suppression does not reimplement any of the above; it
names a condition:

```hcl
condition "timer" "broker_backlog_ok" {
    input            = get(var.backlog) < 1000
    deactivate_after = "30s"          # a momentary spike is not an outage
}

check "backlog" {
    input  = get(condition.broker_backlog_ok)
    reason = "message backlog above threshold for 30s"
}
```

The condition remains a plain boolean anything else may read; the check is the
single place that says what it means for serving traffic. See
[health.md](health.md).

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
