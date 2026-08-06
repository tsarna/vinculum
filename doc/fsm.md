# FSM Reference

`fsm` blocks define finite state machines — named sets of states with
event-driven transitions, guards, hooks, and key-value storage. They are
the automation primitive for encoding *how* something should behave —
answering questions like "what should happen when the door receives an
unlock command while it's in the locked state?" or "should we start
cooling when the temperature has been above 100 for 30 seconds?"

```hcl
fsm "name" {
    initial = "state_name"
    # ... states, events, transitions, hooks
}
```

Every FSM instance implements the [Watchable](trigger.md#watchables)
interface — watchers are notified on every state transition with old and
new state names as `cty.StringVal`. It also implements
[Subscriber](overview.md#subscriptions), so it can be wired to a bus or
driven by `send()`.

---

## Quick Example

```hcl
fsm "door" {
    initial = "closed"

    state "closed" {
        on_entry = log::info("Door is now closed")
    }
    state "open" {
        on_entry = log::info("Door is now open")
    }
    state "locked" {
        on_entry = log::info("Door is now locked")
    }

    event "open"   { transition "closed" "open"   {} }
    event "close"  { transition "open"   "closed" {} }
    event "lock"   { transition "closed" "locked" {} }
    event "unlock" { transition "locked" "closed" {} }

    on_change = log::info("Door: ${ctx.old_state} -> ${ctx.new_state}")
}

# Drive via subscription
subscription "door_commands" {
    target     = bus.main
    topics     = ["door/command/#"]
    subscriber = fsm.door
}

# Or drive imperatively
trigger "cron" "auto_lock" {
    at "0 22 * * *" "nightly" {
        action = send(ctx, fsm.door, "lock", {})
    }
}

# React to state changes
trigger "watch" "door_state" {
    watch  = fsm.door
    action = log::info("Door changed: ${ctx.old_value} -> ${ctx.new_value}")
}
```

---

## States

A named set of states. One state is designated as the `initial` state.

```hcl
state "name" {
    on_entry = log::info("entered")
}
```

<!-- vinculum:begin block-attrs fsm state level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `on_entry` | expression (action-expression) |  | Evaluated on entering this state. |
| `on_event` | expression (action-expression) |  | Evaluated for an event received in this state. |
| `on_exit` | expression (action-expression) |  | Evaluated on leaving this state. |
| `on_init` | expression (action-expression) |  | Evaluated once at startup, for the initial state only. |

**`on_entry`**

Evaluated against the `fsm-hook` context.

**`on_event`**

Runs whether or not the event causes a transition.

Evaluated against the `fsm-hook` context.

**`on_exit`**

Evaluated against the `fsm-hook` context.

**`on_init`**

Evaluated against the `fsm-hook` context.

<!-- vinculum:end block-attrs fsm state -->

`on_init` is ignored on a non-initial state, which produces a config warning.
`on_exit` runs before the transition's own action. `on_event` carries no
transition semantics at all — no exit or entry, no `on_change`, no watch
notification.

**Context propagation.** FSM events are processed asynchronously on the
FSM's own event-loop goroutine, so hooks run after the caller that
enqueued the event has already returned. Hook `ctx` carries the caller's
context **values** (trace spans, auth, etc.) across the queue boundary,
but the caller's cancellation is severed — so an upstream ctx cancel
(e.g. an HTTP request that triggered the event completing) cannot
interrupt a hook mid-execution. Each FSM transition opens a new root
span linked to the caller's span, matching OTel async-messaging
conventions.

States may be declared with an empty body if they have no associated behavior: `state "idle" {}`

---

## Events

Events trigger transitions. An event has a name and one or more transitions
that define which state changes it can cause.

```hcl
event "name" {
    topic = "mqtt/pattern/+name"

    transition "from" "to" {
        guard  = get(var.enabled)
        action = log::info("moving")
    }
}
```

<!-- vinculum:begin block-attrs fsm event level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `topic` | string (topic-pattern) |  | Topic pattern that maps a received message to this event. |
| `when` | expression (reactive-expression) |  | Reactive condition that fires this event. |

**`topic`**

MQTT-style; named captures are available to hooks as `ctx.topic_params`.

**`when`**

Re-evaluated whenever any watchable it references changes; the event fires on a false-to-true edge.

### Blocks

- `transition "<from>" "<to>"` (0..n) — A transition from one state to another.

<!-- vinculum:end block-attrs fsm event -->

### `transition` blocks

<!-- vinculum:begin block-attrs fsm event transition level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) |  | Evaluated when the transition is taken. |
| `guard` | expression (predicate-expression) |  | Condition that must hold for the transition to be taken. |

**`action`**

Evaluated against the `fsm-hook` context.

**`guard`**

Must evaluate to a bool. A false guard leaves the machine in its current state.

Evaluated against the `fsm-hook` context.

<!-- vinculum:end block-attrs fsm event transition -->

When an event is received, transitions are evaluated in declaration order.
The first transition whose `from-state` matches the current state and whose
guard (if any) evaluates to true is executed. If no transition matches, the
event is silently ignored (or `on_event` fires, if declared on the current
state).

### Wildcard Transitions

A transition with `"*"` as the from-state matches any current state. Wildcard
transitions are checked after all explicit from-state matches for the same
event:

```hcl
event "emergency_stop" {
    transition "*" "emergency" {
        action = log::warn("Emergency stop from ${ctx.old_state}!")
    }
}
```

`"*"` is not valid as a to-state — the destination must always be explicit.

### Self-Transitions

A transition where from-state and to-state are the same state is a
self-transition. The full hook sequence executes (`on_exit`, action,
`on_entry`, `on_change`, watch notification), which is useful for
heartbeat or refresh patterns.

### Guards

A guard is evaluated at event-processing time, so it sees the machine's live
storage and any variable or condition it references. A false guard does not
stop the event: the next candidate transition is tried.

```hcl
transition "idle" "active" {
    guard = get(var.enabled) == true
}
```

---

## Reactive Events

Events can be triggered reactively by watching expressions:

```hcl
event "overheat" {
    when = get(var.temperature) > 100

    transition "normal" "overheated" {
        action = log("Temperature is ${get(var.temperature)}")
    }
}
```

The `when` expression is edge-triggered: the event fires only on the
false-to-true transition, not continuously while the expression remains true.

Reactive events work naturally with [conditions](condition.md), which handle
debouncing, hysteresis, and timing — the FSM handles the state logic:

```hcl
condition "threshold" "high_temp" {
    input     = get(var.temperature)
    on_above  = 100
    off_below = 80
    debounce  = "30s"
}

fsm "hvac" {
    initial = "idle"
    state "idle" {}
    state "cooling" {}

    event "overheat" {
        when = get(condition.high_temp)
        transition "idle" "cooling" {}
    }
    event "normal" {
        when = !get(condition.high_temp)
        transition "cooling" "idle" {}
    }
}
```

Events with `when` but no `topic` are reactive-only — they do not participate
in topic matching. To make an event triggerable by both a reactive expression
and incoming topics, declare both `when` and `topic`.

---

## Topic-to-Event Mapping

When events arrive via `OnEvent` (from a bus subscription or `send()`), each
event definition's `topic` pattern is checked using MQTT-style matching. Named
wildcards (`+name`, `#name`) capture segments from the topic and expose them
as `ctx.topic_params`:

```hcl
event "alert" {
    topic = "sensors/+sensor/alert"

    transition "idle" "alerting" {
        action = log::info("Alert from sensor: ${ctx.topic_params.sensor}")
    }
}
```

Events without a `topic` attribute (and no `when`) match when the topic string equals
the event name literally — the simple case for `send(ctx, fsm.door, "open", ...)`.

---

## Storage

The FSM instance acts as a key-value store, accessible via the standard
generic functions:

```hcl
set(fsm.door, "last_user", "alice")
get(fsm.door, "last_user", "unknown")    # "alice"
increment(fsm.door, "open_count")        # delta defaults to 1
state(fsm.door)                          # current state name
count(fsm.door)                          # total transitions since startup
length(fsm.door)                         # number of queued events
```

### Initial Storage

Storage can be pre-populated via a `storage` block:

```hcl
fsm "door" {
    initial = "closed"

    storage {
        open_count = 0
        last_user  = "unknown"
    }

    # ...
}
```

<!-- vinculum:begin block-attrs fsm storage level=4 -->

*Attribute names here are chosen by you rather than fixed by the parser.*

<!-- vinculum:end block-attrs fsm storage -->

Values are evaluated at config time and set before `on_init` runs.

### Snapshot and Restore

`get(fsm.x)` without a key returns a complete snapshot of the instance's
runtime state:

```hcl
snapshot = get(fsm.door)
# {
#   _type            = "fsm"
#   state            = "locked"
#   transition_count = 42
#   storage          = { open_count = 17, last_user = "alice" }
# }
```

`set(fsm.x, snapshot)` restores a previously captured snapshot, replacing
the current state and storage entirely. Validation is synchronous (bad
snapshots fail immediately); the actual state swap is async, processed by
the event goroutine like any other event. No hooks fire during restore,
but watchers are notified.

This enables saving and restoring FSM state for crash recovery or
migration. Use `tojson()`/`fromjson()` for persistent storage:

```hcl
# Save on shutdown
trigger "shutdown" "save_door" {
    action = set(ctx, client.rediskv, "fsm:door:state", tojson(get(fsm.door)))
}

# Restore on startup
fsm "door" {
    initial = "closed"
    state "closed" {
        on_init = set(fsm.door, fromjson(get(client.rediskv, "fsm:door:state")))
    }
    # ...
}
```

---

## Hook Context

Every hook expression — `on_init`, `on_entry`, `on_exit`, `on_event`, a
transition's `guard` and `action`, and `on_change` — sees the same shape:

<!-- vinculum:begin context fsm-hook level=3 -->

Evaluated on a state-machine hook, guard, or transition action.

Which fields are present depends on what drove the transition. `on_init` sees only `ctx.fsm`.

Fields readable as `ctx.<name>` (shape `fsm-hook`):

| Field | Type | Description |
|---|---|:---|
| `ctx.event` | string | Name of the event being processed. *(not always present)* |
| `ctx.event_value` | dynamic | Payload the event carried. *(not always present)* |
| `ctx.event_fields` | object | String metadata the event carried. *(not always present)* |
| `ctx.old_state` | string | State before the transition. *(not always present)* |
| `ctx.new_state` | string | State after the transition. *(not always present)* |
| `ctx.topic` | string | Topic the driving message arrived on. *(not always present)* |
| `ctx.topic_params` | object | Named captures from matching the event's topic pattern. *(not always present)* |
| `ctx.fsm` | capsule | The machine itself, for `get()` and `set()` on its storage. *(not always present)* |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.event_value`**

Null for a reactive event, which has no message behind it.

**`ctx.event_fields`**

Null when the event carried none.

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

#### Evaluated by

- `fsm` › `on_change`
- `fsm "event"` › `transition` › `guard`
- `fsm "event"` › `transition` › `action`
- `fsm "state"` › `on_init`
- `fsm "state"` › `on_entry`
- `fsm "state"` › `on_exit`
- `fsm "state"` › `on_event`

<!-- vinculum:end context fsm-hook -->

`on_error` sees that shape plus the two fields describing the failure:

<!-- vinculum:begin context fsm-error level=3 -->

Evaluated when a hook, guard, or action fails.

The hook shape, plus which hook failed and why.

Fields readable as `ctx.<name>` (shape `fsm-error`):

| Field | Type | Description |
|---|---|:---|
| `ctx.event` | string | Name of the event being processed. *(not always present)* |
| `ctx.event_value` | dynamic | Payload the event carried. *(not always present)* |
| `ctx.event_fields` | object | String metadata the event carried. *(not always present)* |
| `ctx.old_state` | string | State before the transition. *(not always present)* |
| `ctx.new_state` | string | State after the transition. *(not always present)* |
| `ctx.topic` | string | Topic the driving message arrived on. *(not always present)* |
| `ctx.topic_params` | object | Named captures from matching the event's topic pattern. *(not always present)* |
| `ctx.fsm` | capsule | The machine itself, for `get()` and `set()` on its storage. *(not always present)* |
| `ctx.error` | string | The error message. |
| `ctx.hook` | string | Name of the hook that failed. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.event_value`**

Null for a reactive event, which has no message behind it.

**`ctx.event_fields`**

Null when the event carried none.

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

#### Evaluated by

- `fsm` › `on_error`

<!-- vinculum:end context fsm-error -->

---

## Hook Execution Order

### At Startup

```
1. Instance created in initial state
2. Initial state: on_init
```

### On Event (Transition)

When event triggers a transition from state A to state B:

```
1. Guard expressions evaluated (first match wins)
2. State A: on_exit
3. Transition: action
4. Current state updated: A -> B
5. State B: on_entry
6. Machine: on_change
7. Watchable: NotifyAll(old=A, new=B)
```

### On Event (No Transition)

If no transition matches but the current state has `on_event`, only
`on_event` executes — no other hooks fire.

### Hook Errors

Hook return values are ignored (hooks are fire-and-forget). If a hook
produces a diagnostic error, the error is routed to `on_error` if
configured, or logged. Errors do not prevent the transition from
completing.

---

## Machine-Level Hooks

Two hooks sit on the machine rather than on a state. `on_change` fires after
the new state's `on_entry`, so it is the last thing to run in a transition;
`on_error` catches a failure from any hook, guard, or action, and errors are
logged when it is absent.

```hcl
fsm "door" {
    on_change = log("${ctx.old_state} -> ${ctx.new_state}")
    on_error  = log("FSM error in ${ctx.hook}: ${ctx.error}")
}
```

---

## Concurrency

Each instance uses an event queue (buffered channel) processed by a single
goroutine. This guarantees serialization of transitions without risk of
deadlock or hook interleaving.

- **Re-entrancy**: If a hook calls `send(ctx, fsm.x, ...)` back to the same
  instance, the event is enqueued and processed after the current transition
  completes.
- **Concurrent reads**: `state()`, `get()`, and `count()` use a separate
  `RWMutex` and do not block event processing.
- **Queue sizing**: `queue_size` sets the buffer depth — how far the machine
  may fall behind before a `send()` blocks.

---

## Lifecycle and Shutdown

The FSM implements `Startable` and `Stoppable`. On stop, if
`shutdown_event` is configured, it is injected via a priority channel and
processed before remaining queued events:

```hcl
fsm "process" {
    initial        = "running"
    shutdown_event = "shutdown"

    state "running" {}
    state "stopped" {
        on_entry = log("Shutting down cleanly")
    }

    event "shutdown" {
        transition "*" "stopped" {}
    }
}
```

---

## Tracing

If an OTLP client is configured, each transition creates a span named
`fsm.<name>/<event>` with attributes `fsm.name`, `fsm.event`,
`fsm.old_state`, `fsm.new_state`. Hook errors are recorded on the span.
An explicit `tracing` attribute can override the auto-wired provider:

```hcl
fsm "door" {
    tracing = client.otel
}
```

---

## Full Attribute Reference

<!-- vinculum:begin block-attrs fsm level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `initial` | string | yes |  | Name of the state the machine starts in. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `on_change` | expression (action-expression) |  |  | Evaluated after every state change. |
| `on_error` | expression (action-expression) |  |  | Evaluated when a hook or guard fails. |
| `queue_size` | number |  | `64` | Depth of the inbound event queue. |
| `shutdown_event` | string |  |  | Event delivered to the machine at shutdown. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |

**`initial`**

Must match one of the `state` blocks.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`on_change`**

Includes self-transitions.

Evaluated against the `fsm-hook` context.

**`on_error`**

`ctx.error` is the message and `ctx.hook` names the hook that failed.

Evaluated against the `fsm-error` context.

**`queue_size`**

Events are processed one at a time by a single goroutine, so this is how far the machine can fall behind before a `send()` blocks.

**`shutdown_event`**

Lets the machine run its exit hooks and reach a terminal state before the process stops.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

### Blocks

- `event "<name>"` (0..n) — One event the machine accepts, and the transitions it causes.
- `state "<name>"` (0..n) — One state of the machine, with its lifecycle hooks.
- `storage` (optional) — Initial values of the machine's storage keys.

<!-- vinculum:end block-attrs fsm -->

### Interfaces

| Interface | Behavior |
|-----------|----------|
| Gettable | `get(fsm.x, "key")` / `get(fsm.x, "key", default)` |
| Settable | `set(fsm.x, "key", value)` |
| Incrementable | `increment(fsm.x, "key")` / `increment(fsm.x, "key", delta)` |
| Stateful | `state(fsm.x)` — current state name |
| Countable | `count(fsm.x)` — total transitions since startup |
| Lengthable | `length(fsm.x)` — events currently queued |
| Watchable | Fires on every state transition (including self-transitions) |
| Subscriber | Receives events via `OnEvent` / `send()` / bus subscription |
