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
        on_entry = log_info("Door is now closed")
    }
    state "open" {
        on_entry = log_info("Door is now open")
    }
    state "locked" {
        on_entry = log_info("Door is now locked")
    }

    event "open"   { transition "closed" "open"   {} }
    event "close"  { transition "open"   "closed" {} }
    event "lock"   { transition "closed" "locked" {} }
    event "unlock" { transition "locked" "closed" {} }

    on_change = log_info("Door: ${ctx.old_state} -> ${ctx.new_state}")
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
    action = log_info("Door changed: ${ctx.old_value} -> ${ctx.new_value}")
}
```

---

## States

A named set of states. One state is designated as the `initial` state.

```hcl
state "name" {
    on_init  = <action-expr>    # optional: called once at startup, initial state only
    on_entry = <action-expr>    # optional: called on each transition into this state
    on_exit  = <action-expr>    # optional: called on each transition out of this state
    on_event = <action-expr>    # optional: called when an event arrives but no transition matches
}
```

- **`on_init`** — Evaluated exactly once, at startup, only on the initial
  state. The context has `ctx.fsm` but no event or transition fields. Ignored
  on non-initial states (produces a config warning).
- **`on_entry`** — Evaluated when the state is entered via a transition (not
  at startup for the initial state).
- **`on_exit`** — Evaluated when the state is about to be exited, before the
  transition action runs.
- **`on_event`** — Evaluated when an event is received but no matching
  transition exists for the current state. No transition semantics — no
  exit/entry, no `on_change`, no watch notification.

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
    topic = "mqtt/pattern/+name"   # optional: MQTT-style topic pattern
    when  = <bool-expr>            # optional: reactive trigger (edge-triggered)

    transition "from" "to" {
        guard  = <bool-expr>       # optional: transition only if true
        action = <action-expr>     # optional: evaluated during the transition
    }
}
```

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
        action = log_warn("Emergency stop from ${ctx.old_state}!")
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

```hcl
transition "idle" "active" {
    guard = get(var.enabled) == true
}
```

Guards are boolean expressions evaluated at event-processing time. If false,
the transition is skipped and the next candidate is tried. Guards have access
to the full runtime context (storage, variables, state).

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
    input    = var.temperature
    on_above = 100
    off_below = 80
    debounce = "30s"
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
        action = log_info("Alert from sensor: ${ctx.topic_params.sensor}")
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

All hook expressions (`on_init`, `on_entry`, `on_exit`, `on_event`,
transition `action`, `on_change`, `on_error`) receive a `ctx` object with
the following attributes:

| Attribute | Type | Description |
|-----------|------|-------------|
| `ctx.event` | string | The event name |
| `ctx.event_value` | dynamic | The message from `send()` or `OnEvent` |
| `ctx.event_fields` | object | Optional string metadata from `send()`/`OnEvent` |
| `ctx.old_state` | string | State before the transition |
| `ctx.new_state` | string | State after the transition |
| `ctx.topic` | string | Raw topic string from `OnEvent` |
| `ctx.topic_params` | object | Named captures from MQTT pattern matching |
| `ctx.fsm` | capsule | The FSM instance |
| `ctx.error` | string | Error message (on_error only) |
| `ctx.hook` | string | Hook name that failed (on_error only) |

For `on_init`, only `ctx.fsm` is available. For reactive events,
`ctx.event_value` and `ctx.event_fields` are null.

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

### `on_change`

```hcl
fsm "door" {
    on_change = log("${ctx.old_state} -> ${ctx.new_state}")
}
```

Evaluated after every state transition (including self-transitions), after
`on_entry` of the new state.

### `on_error`

```hcl
fsm "door" {
    on_error = log("FSM error in ${ctx.hook}: ${ctx.error}")
}
```

Evaluated when a hook expression produces an error. If not provided, errors
are logged.

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
- **Queue sizing**: The buffer size is configurable via `queue_size`
  (default 64).

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

```hcl
fsm "name" {
    initial        = "state_name"        # required
    queue_size     = 64                  # optional (default 64)
    shutdown_event = "event_name"        # optional
    tracing        = <server-expr>       # optional (auto-wired)
    disabled       = false               # optional
    on_change      = <action-expr>       # optional
    on_error       = <action-expr>       # optional

    storage {
        key = value
    }

    state "name" {
        on_init  = <action-expr>
        on_entry = <action-expr>
        on_exit  = <action-expr>
        on_event = <action-expr>
    }

    event "name" {
        topic = "mqtt/pattern"
        when  = <bool-expr>

        transition "from" "to" {
            guard  = <bool-expr>
            action = <action-expr>
        }
    }
}
```

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
