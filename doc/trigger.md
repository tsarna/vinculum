# Trigger Reference

`trigger` blocks define lifecycle actions that fire at specific points: on a
schedule, at startup, during shutdown, or in response to OS signals.

```hcl
trigger "type" "name" {
    disabled = false  # optional — if true, block is skipped entirely
    ...
}
```

All trigger types share a single name namespace — you cannot have two non-disabled
triggers with the same name regardless of type. `trigger "after"`, `trigger "interval"`,
`trigger "once"`, `trigger "start"`, `trigger "watch"`, and `trigger "watchdog"` blocks
expose their result as `trigger.<name>` in the global evaluation context.

---

## `trigger "after"`

```hcl
trigger "after" "name" {
    delay    = expression  # required — how long to wait after startup
    action   = expression  # required — evaluated once when the delay elapses
    disabled = false       # optional
}
```

Waits a fixed duration after startup, then evaluates `action` exactly once. It
is the time-deferred analogue of `trigger "once"`: rather than firing on demand,
it fires automatically after the specified delay.

`get(trigger.<name>)` returns `null` until the action fires, then the cached
result (or error) on every subsequent call. If shutdown occurs before the delay
elapses, the action is abandoned and `get(trigger.<name>)` continues to return
`null`.

The delay is parsed at configuration load time and supports the same formats as
other duration attributes: numbers (seconds), Go duration strings (`"500ms"`,
`"2m30s"`), and ISO 8601 strings (`"PT5M"`).

When the action runs, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"after"` |
| `ctx.name` | Name of this trigger block |

**Creates** `trigger.<name>` as a capsule; read the result with
`get(trigger.<name>)`.

Example — allow dependent services 30 seconds to come online before connecting:

```hcl
trigger "after" "connect" {
    delay  = "30s"
    action = connect(ctx, client.upstream)
}
```

Example — emit a startup-complete event after a brief grace period:

```hcl
trigger "after" "announce" {
    delay  = "5s"
    action = send(ctx, bus.main, "system/ready", {host = sys.hostname})
}
```

---

## `trigger "cron"`

```hcl
trigger "cron" "name" {
    timezone = "UTC"  # optional, default: local time zone
    disabled = false  # optional

    at "schedule" "rule_name" {  # one or more
        action = expression
    }
}
```

Defines a cron-style scheduler. Multiple `trigger "cron"` blocks may exist, each
with a different name. One use for multiple blocks is running schedules in different
time zones.

Each `at` block specifies a schedule in standard cron format (five fields:
minute, hour, day-of-month, month, day-of-week). A six-field format is also
supported where the first field represents seconds. The
[`@every`](https://pkg.go.dev/github.com/robfig/cron/v3#hdr-Predefined_schedules)
descriptor and other standard descriptors are also accepted.

When a rule fires, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.cron_name` | Name of the enclosing `trigger "cron"` block |
| `ctx.at_name` | Name of the `at` rule that fired |

Does **not** create a `trigger.<name>` value.

Example — send a heartbeat every 30 seconds:

```hcl
trigger "cron" "heartbeat" {
    at "@every 30s" "ping" {
        action = send(ctx, bus.main, "system/heartbeat", "ping")
    }
}
```

---

## `trigger "interval"`

```hcl
trigger "interval" "name" {
    delay         = expression  # required — duration between runs
    initial_delay = expression  # optional — duration before the very first run (default: delay)
    error_delay   = expression  # optional — duration after a failed run (default: delay)
    jitter        = 0.0         # optional — fraction in [0, 1]; actual delay uniform in [delay*(1-jitter/2), delay*(1+jitter/2)]
    stop_when     = expression  # optional — boolean; trigger stops itself when this evaluates true
    action        = expression  # required — evaluated each time the delay elapses
    disabled      = false       # optional
}
```

Repeatedly evaluates `action` on a dynamic schedule: wait the computed delay,
evaluate the action, repeat. Both `delay` and `action` are re-evaluated each
iteration against a context that includes the run count and the previous result,
so the schedule can adapt at runtime — for example, polling more frequently when
an object is moving fast, or backing off when errors occur.

Durations may be expressed as numbers (seconds), Go duration strings (`"500ms"`,
`"2m30s"`), ISO 8601 duration strings (`"PT5M"`), or duration capsule values.

`get(trigger.<name>)` returns the most recent result of `action`, `null` before
the first run, or an error if the most recent run failed.

When each iteration runs, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"interval"` |
| `ctx.name` | Name of this trigger block |
| `ctx.run_count` | Number of completed action evaluations (0 on the first run) |
| `ctx.last_result` | Result of the previous action evaluation, or `null` on the first run |
| `ctx.last_error` | Error string from the previous evaluation, or `null` if it succeeded |

`ctx` is available in `delay`, `error_delay`, and `action` (evaluated with the
state *before* this iteration). `stop_when` is evaluated *after* the action
completes, with `ctx.run_count` already incremented.

**Creates** `trigger.<name>` as a capsule; read the most recent result with
`get(trigger.<name>)`.

Example — poll every 10 seconds, starting immediately:

```hcl
trigger "interval" "poller" {
    initial_delay = "0s"
    delay         = "10s"
    action        = fetch(ctx, "https://example.com/status")
}
```

Example — adaptive interval based on object speed (poll faster when moving faster):

```hcl
trigger "interval" "tracker" {
    delay  = clamp(div(1.0, get(var.speed)), "100ms", "30s")
    action = compute_position(ctx, get(var.speed))
}
```

Example — retry quickly on errors, use normal cadence otherwise; stop after 100 runs:

```hcl
trigger "interval" "worker" {
    delay       = "1m"
    error_delay = "5s"
    stop_when   = ctx.run_count >= 100
    action      = do_work(ctx)
}
```

Example — add jitter to desynchronize multiple instances running the same schedule:

```hcl
trigger "interval" "reporter" {
    delay  = "30s"
    jitter = 0.2  # actual delay uniform in [27s, 33s], average stays 30s
    action = send_metrics(ctx)
}
```

---

## `trigger "once"`

```hcl
trigger "once" "name" {
    action   = expression
    disabled = false  # optional
}
```

Defers evaluation of `action` until the first time `get(trigger.<name>)` is
called. The result is then cached — every subsequent call to
`get(trigger.<name>)` returns the same value without re-evaluating the
expression. This is useful for lazy initialization: an expensive or
side-effecting operation that should run at most once, on demand.

If the expression produces an error on the first call, that error is also
cached and returned on every subsequent call.

When the action runs, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"once"` |
| `ctx.name` | Name of this trigger block |

**Creates** `trigger.<name>` as a lazy capsule; read it with `get(trigger.<name>)`.

Example — compute an expensive value once and reuse it everywhere:

```hcl
trigger "once" "value" {
    action = some_expensive_computation(...)
}

# Any block can read the cached result on demand:
# get(trigger.value)
```

Example — lazy initialization with a side effect that must only run once:

```hcl
var "counter" {}

trigger "once" "init" {
    action = set(var.counter, 0)
}

# First get() sets counter to 0 and returns 0.
# Later calls return 0 from the cache without calling set() again.
```

---

## `trigger "shutdown"`

```hcl
trigger "shutdown" "name" {
    action   = expression
    disabled = false  # optional
}
```

Evaluates `action` once during graceful shutdown (after SIGINT or SIGTERM is
received), in the reverse order that stoppable components were registered. Errors
are logged but do not abort the shutdown sequence.

When the action runs, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"shutdown"` |
| `ctx.name` | Name of this trigger block |

Does **not** create a `trigger.<name>` value.

Example — log a goodbye message on shutdown:

```hcl
trigger "shutdown" "bye" {
    action = loginfo("shutting down", {name = ctx.name})
}
```

---

## `trigger "signals"`

```hcl
trigger "signals" "name" {
    SIGHUP  = expression  # optional
    SIGINFO = expression  # optional
    SIGUSR1 = expression  # optional
    SIGUSR2 = expression  # optional

    disabled = false  # optional
}
```

Maps OS signals to action expressions. The available signals are `SIGHUP`,
`SIGINFO`, `SIGUSR1`, and `SIGUSR2` (availability varies by OS).

Multiple `trigger "signals"` blocks may exist, but a given signal may only be
defined in one non-disabled block.

When a signal fires, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"signals"` |
| `ctx.signal` | Signal name as a string (e.g. `"SIGHUP"`) |
| `ctx.signal_num` | OS-level signal number |

Does **not** create a `trigger.<name>` value.

Example — log the signal name on SIGUSR1:

```hcl
trigger "signals" "main" {
    SIGUSR1 = loginfo("received signal", {signal = ctx.signal})
}
```

---

## `trigger "start"`

```hcl
trigger "start" "name" {
    action   = expression
    disabled = false  # optional
}
```

Evaluates `action` once at startup, during the configuration build phase, before
any server or client component starts. If the action expression returns an error
the configuration build is aborted.

The result of `action` is stored as `trigger.<name>` in the global evaluation
context, making it available to any block declared after this trigger (dependency
ordering is handled automatically).

When the action runs, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"start"` |
| `ctx.name` | Name of this trigger block |

**Creates** `trigger.<name>` with the value returned by `action`.

Example — log a startup message:

```hcl
trigger "start" "hello" {
    action = loginfo("vinculum started", {host = sys.hostname})
}
```

---

## `trigger "watch"`

```hcl
trigger "watch" "name" {
    watch     = expression  # required — must evaluate to a watchable var or metric capsule
    action    = expression  # required — evaluated on each change
    skip_when = expression  # optional — skip this firing if true
    disabled  = false       # optional
}
```

Fires `action` each time a [Watchable](#watchables) value changes. The `watch` expression
is evaluated once at config build time and must produce a `var` or non-computed `metric`
capsule (gauge or counter). A config build error is raised if the expression does not resolve
to a watchable type.

When a change is observed, `action` is dispatched to a new goroutine so the caller of
`set()` / `increment()` is not blocked. If `skip_when` is provided, it is evaluated first
(in the same goroutine); if it returns `true`, the action is skipped. Each firing evaluates
`skip_when` independently.

`get(trigger.<name>)` returns the most recently observed `newValue`, or `null` before any
change has been observed since startup.

On `Stop()`, the trigger unregisters itself from the Watchable and waits for all in-flight
action goroutines to complete before returning.

When `action` (and `skip_when`) are evaluated, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"watch"` |
| `ctx.name` | Name of this trigger block |
| `ctx.old_value` | The value before the change |
| `ctx.new_value` | The value after the change |

**Creates** `trigger.<name>` as a capsule; read the last observed value with
`get(trigger.<name>)`.

Example — log every update on a variable (fires on every `set()`, including repeated equal values):

```hcl
var "temperature" {}

trigger "watch" "on_temp_update" {
    watch  = var.temperature
    action = loginfo("temperature updated", {
        was = ctx.old_value,
        now = ctx.new_value,
    })
}
```

Example — fire only when the value actually changes (opt-in via `skip_when`):

```hcl
trigger "watch" "on_temp_change" {
    watch     = var.temperature
    skip_when = ctx.old_value == ctx.new_value
    action    = loginfo("temperature changed", {
        was = ctx.old_value,
        now = ctx.new_value,
    })
}
```

Example — alert on rising edge only (skip deactivation events):

```hcl
trigger "watch" "alarm_on" {
    watch     = condition.high_temp
    skip_when = !ctx.new_value
    action    = send(ctx, bus.alerts, "alarm/high_temp", "activated")
}
```

Example — react to a metric crossing a threshold:

```hcl
metric "gauge" "queue_depth" {}

trigger "watch" "queue_alert" {
    watch     = metric.queue_depth
    skip_when = ctx.new_value < 1000
    action    = logwarn("queue depth exceeded threshold", {depth = ctx.new_value})
}
```

---

## `trigger "watchdog"`

```hcl
trigger "watchdog" "name" {
    window        = expression  # required — fires if not set() within this duration
    action        = expression  # required — evaluated each time the watchdog fires
    watch         = expression  # optional — Watchable capsule to auto-feed the watchdog
    initial_grace = expression  # optional — grace period before first check; default: window
    repeat        = false       # optional — if true, re-fires every window until set() again
    max_misses    = number      # optional — auto-stop after this many consecutive fires
    stop_when     = expression  # optional — stop when this boolean expression is true
    disabled      = false       # optional
}
```

Fires an action when a time window elapses without `set(trigger.<name>)` being
called. It is the inverse of `trigger "interval"`: rather than doing work on a
schedule, it detects when expected work *stops* happening.

`set(trigger.<name>, value)` resets the countdown and stores the value.
`set(trigger.<name>)` with no value resets the countdown and stores `null`.
`get(trigger.<name>)` returns the last value passed to `set()`, or `null` if
`set()` has never been called.

**`watch`** — optional; when set to a [Watchable](#watchables) `var` or `metric` capsule,
the watchdog auto-feeds itself each time that value changes — exactly as if the producer
had called `set(trigger.<name>, newValue)`. This decouples the producer from the watchdog:
the producer only needs to update the variable, not know about any watchdog. Manual
`set(trigger.<name>, ...)` calls remain valid alongside `watch`.

**`repeat = false` (default)** — After firing, the watchdog goes dormant and
waits for the next `set()` before re-arming. This avoids flooding repeated
alerts for a condition that is already known to be broken.

**`repeat = true`** — After firing, immediately re-arms with a fresh `window`
countdown. Keeps firing on every window until `set()` is called. Useful for
paging systems where ongoing alerting is desired.

The `initial_grace` defaults to `window`, giving components time to start up
and call `set()` for the first time before the watchdog can fire.

**`max_misses`** — optional integer (≥ 1); after the watchdog fires this many
consecutive times without a `set()` in between, it auto-stops and waits. Calling
`set()` resets `miss_count` to 0, which clears the condition, and the watchdog
re-arms immediately. Use this to cap how many times an alert fires before
requiring explicit acknowledgement via `set()`.

**`stop_when`** — optional boolean expression evaluated after each fire using
the same `ctx` as the action (including the updated `ctx.miss_count`). When it
evaluates `true`, the watchdog auto-stops and waits. Calling `set()` re-evaluates
the expression against the post-`set()` state (where `ctx.miss_count` is 0); if
it is now `false`, the watchdog re-arms. For a simple count-based stop,
`stop_when = ctx.miss_count >= N` is equivalent to `max_misses = N`.

When the action runs, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.trigger` | `"watchdog"` |
| `ctx.name` | Name of this trigger block |
| `ctx.last_set` | Time of the last `set()` call (time capsule), or `null` if never set |
| `ctx.miss_count` | Consecutive fires since the last `set()`; resets to 0 on `set()` |

**Creates** `trigger.<name>` as a capsule; use `set()` to feed it and `get()` to
read the last stored value.

Example — heartbeat monitoring (alert if worker goes silent for 90 seconds):

```hcl
trigger "watchdog" "worker_alive" {
    window = "90s"
    action = logwarn("worker missed heartbeat", {missed = ctx.miss_count})
}

trigger "interval" "worker" {
    delay  = "30s"
    action = set(trigger.worker_alive, do_work(ctx))
}
```

Example — repeated alerting until acknowledged:

```hcl
trigger "watchdog" "pipeline" {
    window  = "5m"
    repeat  = true
    action  = send(ctx, bus.alerts, "pipeline/stalled", {
        missed = ctx.miss_count,
        since  = ctx.last_set,
    })
}
```

Example — allow 2 minutes for a dependency to start before monitoring begins:

```hcl
trigger "watchdog" "upstream" {
    initial_grace = "2m"
    window        = "30s"
    action        = logwarn("upstream health check stopped", {name = ctx.name})
}
```

Example — use `watch =` to decouple the producer from the watchdog (producer only knows
about the variable; the watchdog feeds itself automatically):

```hcl
var "sensor_reading" {}

# Producer only sets the variable — no knowledge of any watchdog required.
subscription "sensor" {
    bus    = bus.main
    topics = ["sensor/+"]
    action = set(var.sensor_reading, ctx.payload.value)
}

trigger "watchdog" "sensor_alive" {
    window = "60s"
    watch  = var.sensor_reading  # auto-feeds on every set(var.sensor_reading, ...)
    action = logwarn("sensor went silent", {last = ctx.last_set})
}
```

Example — alert at most 3 times, then wait for acknowledgement via `set()`:

```hcl
trigger "watchdog" "pipeline" {
    window     = "5m"
    repeat     = true
    max_misses = 3
    action     = send(ctx, bus.alerts, "pipeline/stalled", {missed = ctx.miss_count})
}

# Operator acknowledges the alert by calling set(trigger.pipeline) — this
# resets miss_count to 0 and re-arms the watchdog.
```

---

## Watchables

`var`, gauge `metric`, and counter `metric` values implement the `Watchable` interface.
Any code that calls `set()` or `increment()` on one of these values will synchronously
notify all registered watchers after the value is committed and the internal lock is
released. The `context.Context` passed to `set()`/`increment()` is forwarded verbatim
to each watcher's `OnChange` callback.

Notifications fire on **every** `set()`/`increment()` call, even when the new value
equals the old value. This is intentional: a producer that repeatedly writes the same
value is still alive, and watchdog heartbeat patterns require a notification on every
write regardless of value change. Consumers that want changes-only semantics should use
`skip_when = ctx.old_value == ctx.new_value` on a `trigger "watch"` block.

**Watchable types:**

- `var` — fires on `set()` and `increment()`
- `metric "gauge"` (non-computed) — fires on `set()` and `increment()`; old/new values are `cty.Number`
- `metric "counter"` (non-computed) — fires on `set()` and `increment()`; old/new values are the
  cached monotonically increasing total (not the raw argument); a `set()` call that produces no
  delta (new value ≤ current total) still fires with `old == new`

**Not Watchable:**

- `metric "histogram"` — no meaningful "old value" per observation
- Computed metric variants (`value = expression`) — value is derived at scrape time, not via `set()`
