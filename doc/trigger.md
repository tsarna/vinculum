# Trigger Reference

`trigger` blocks define lifecycle actions that fire at specific points: on a
schedule, at startup, during shutdown, in response to OS signals, or when
filesystem events occur.

```hcl
trigger "type" "name" {
    disabled = false  # optional — if true, block is skipped entirely
    ...
}
```

All trigger types share a single name namespace — you cannot have two non-disabled
triggers with the same name regardless of type. `trigger "after"`, `trigger "at"`,
`trigger "file"`, `trigger "interval"`, `trigger "once"`, `trigger "start"`,
`trigger "watch"`, and `trigger "watchdog"` blocks expose their result as
`trigger.<name>` in the global evaluation context.

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

<!-- vinculum:begin block-attrs trigger after level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) | yes | Evaluated once when the delay elapses. |
| `delay` | expression (duration) | yes | How long to wait after startup before firing. |
| `disabled` | bool |  | Skip this block entirely. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`action`**

Evaluated against the `trigger-after` context.

**`delay`**

Parsed at config load time. Accepts a number of seconds, a Go duration string (`"500ms"`, `"2m30s"`), or an ISO 8601 duration (`"PT5M"`).

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger after -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger after action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-after`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"after"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger after action -->

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

## `trigger "at"`

```hcl
trigger "at" "name" {
    time      = expression  # optional — must evaluate to a time capsule; omit for set()-driven triggers
    repeat    = true        # optional — if false, fire once then go dormant until set()
    stop_when = expression  # optional — boolean; trigger stops itself when this evaluates true
    action    = expression  # required — evaluated each time the trigger fires
    disabled  = false       # optional
}
```

Fires its `action` at a dynamically computed absolute time, then immediately
re-evaluates `time` to schedule the next firing. This is the natural fit for
non-uniform recurring schedules — such as sunrise or sunset — where the
interval between firings varies and depends on runtime state.

Unlike `trigger "cron"` (which uses fixed schedule strings) and
`trigger "interval"` (which uses a computed delay relative to the previous
firing), `trigger "at"` works with absolute wall-clock times. The `time`
expression is re-evaluated after each firing, so it can return a different
time on every cycle.

`get(trigger.<name>)` returns `null` until the first evaluation of `time`,
then the currently scheduled fire time as a time capsule. This lets other
expressions compute how far away the next firing is, e.g.
`time::until(get(trigger.<name>))`.

If `time` evaluates to a time in the past, the action fires immediately and a
warning is logged. If `time` evaluation fails (wrong type, expression error),
the trigger logs the error and retries after one minute.

### Imperative control with `set()` and `reset()`

`set(trigger.<name>, time)` overrides the next fire with an explicit absolute
time, resets the run count, and revives the trigger if it was dormant (via
`stop_when`, `repeat = false`, or a dormant start). The override is consumed
on fire — subsequent iterations fall back to the configured `time`
expression. If the override is already in the past, the trigger fires
immediately and logs a warning (same as the `time` expression case).

`set(trigger.<name>)` with no argument re-evaluates the `time` expression
immediately without firing the action. Useful when conditions change — for
example, when a vehicle's position shifts and the computed target time
needs updating. Returns an error if no `time` expression was configured.

`reset(trigger.<name>)` cancels any pending timer and puts the trigger into a
dormant state, waiting for the next `set()` call. Clears the override,
scheduled time, run count, and last result/error.

### Dormant start (no `time`)

When `time` is omitted, the trigger starts dormant and waits for the first
`set(trigger.<name>, time_value)` call before firing. This is the idiomatic
way to create a fully `set()`-driven trigger, e.g. an alarm that an FSM state
arms with an explicit wall-clock time. A no-argument `set()` cannot revive a
dormant trigger without a `time` expression — it requires an override.

### `repeat = false`

When `repeat` is `false`, the trigger fires once and then goes dormant,
waiting for `set()` to revive it. Combined with dormant start (no `time`),
this yields a classic one-shot alarm.

<!-- vinculum:begin block-attrs trigger at level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) | yes |  | Evaluated each time the trigger fires. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `repeat` | bool |  | `true` | Keep rescheduling after each firing. |
| `stop_when` | expression (predicate-expression) |  |  | Stop the trigger when this evaluates true. |
| `time` | expression (action-expression) |  |  | When to fire next. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |

**`action`**

Evaluated against the `trigger-at` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`repeat`**

When false, the trigger fires once and goes dormant until `set()` revives it. Combined with no `time`, that is a classic one-shot alarm.

**`stop_when`**

Evaluated after each fire, with `ctx.run_count` already incremented.

Evaluated against the `trigger-at` context.

**`time`**

Must evaluate to a time capsule. Re-evaluated after each firing, so it can return a different time every cycle. A time in the past fires immediately and logs a warning; an evaluation error is logged and retried after a minute. Omit it to start dormant, waiting for the first `set(trigger.<name>, time)`.

Evaluated against the `trigger-at` context.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger at -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger at action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-at`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"at"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.run_count` | number | How many times the action has fired. |
| `ctx.last_result` | dynamic | Result of the previous action, or null on the first fire. |
| `ctx.last_error` | string | Error from the previous evaluation, or null if it succeeded. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger at action -->

The same context is available when evaluating the `time` expression, so the
schedule can adapt based on how many times the trigger has already fired.
`stop_when` is evaluated after each fire with the same `ctx`, with
`ctx.run_count` already incremented.

**Creates** `trigger.<name>` as a capsule; read the next scheduled time with
`get(trigger.<name>)`, override the next fire with
`set(trigger.<name>, time)`, re-evaluate the configured `time` expression
with `set(trigger.<name>)`, or cancel with `reset(trigger.<name>)`.

Example — fire at a dynamically computed time each day, for example using Vinculum's sunrise/sunset functions:

```hcl
trigger "at" "sunrise" {
    time   = sky::sunrise({lat = get(var.latitude), lon = get(var.longitude)})
    action = set(var.scene, "morning")
}
```

Example — pair with `trigger "interval"` to recompute the fire time more
frequently as a moving vehicle approaches its destination, or
recompute sunrise/sunset as its position changes:

```hcl
trigger "at" "arrival_alert" {
    time   = eta(var.lat, var.lon, var.destination)
    action = send(ctx, bus.main, "nav/arrived", null)
}

trigger "interval" "recompute_eta" {
    delay  = recheck_interval(var.speed, time::until(get(trigger.arrival_alert)))
    action = set(trigger.arrival_alert)
}
```

Example — overnight scene change that can be forced earlier by an external
event. The `time` expression recomputes sunset each day; a weather alert can
override the next fire with the storm's expected arrival time:

```hcl
trigger "at" "draw_curtains" {
    time   = sky::sunset({lat = get(var.lat), lon = get(var.lon)})
    action = send(ctx, bus.main, "home/curtains/close", {})
}

subscription "weather_override" {
    target = bus.main
    topics = ["weather/storm_alert"]
    action = set(trigger.draw_curtains, ctx.msg.expected_at)
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

<!-- vinculum:begin block-attrs trigger cron level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `disabled` | bool |  |  | Skip this block entirely. |
| `timezone` | string |  | `Local` | IANA time zone the schedules are interpreted in. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`timezone`**

For example `"UTC"` or `"America/New_York"`. `Local` is Go's name for the host's own zone, which is what an omitted `timezone` selects.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

### Blocks

- `at "<schedule>" "<name>"` (0..n) — One scheduled rule.

<!-- vinculum:end block-attrs trigger cron -->

Each `at` rule takes:

<!-- vinculum:begin block-attrs trigger cron at level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) | yes | Evaluated each time this rule's schedule fires. |

**`action`**

Evaluated against the `trigger-cron` context.

<!-- vinculum:end block-attrs trigger cron at -->

When a rule fires, `ctx` provides:

<!-- vinculum:begin block-ctx trigger cron at action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-cron`):

| Field | Type | Description |
|---|---|:---|
| `ctx.cron_name` | string | Name of the enclosing `trigger "cron"` block. |
| `ctx.at_name` | string | Name of the `at` rule that fired. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger cron at action -->

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

## `trigger "file"`

```hcl
trigger "file" "name" {
    path              = expression  # required — file or directory to watch
    action            = expression  # required — evaluated on each matching event
    events            = ["create", "write", "delete", "rename", "chmod"]  # optional
    recursive         = false       # optional — watch subdirectories too
    filter            = expression  # optional — glob pattern applied to event_path
    debounce          = expression  # optional — coalesce rapid events on same path
    on_start_existing = false       # optional — synthetic create for existing files at startup
    skip_when         = expression  # optional — skip this firing if true
    disabled          = false       # optional
}
```

Fires `action` in response to filesystem events — file or directory creates,
writes, deletes, renames, and permission changes. Backed by OS-native
notification mechanisms (`inotify` on Linux, `kqueue`/`FSEvents` on macOS,
`ReadDirectoryChangesW` on Windows) via the
[`fsnotify`](https://github.com/fsnotify/fsnotify) library.

**Available only with `--file-path`.** Filesystem access is opt-in, so this
trigger type is registered only in a process started with that flag; declaring
one without it is an error rather than a watcher that never fires.

`path` must exist when vinculum starts; a missing path is a runtime startup
error. To watch multiple paths, declare multiple `trigger "file"` blocks.

`get(trigger.<name>)` returns the result of the most recently completed action
invocation, or `null` before any action has run.

> **Network filesystems:** `fsnotify` relies on kernel-level notifications and
> does not observe remote writes on NFS, SMB/CIFS, SSHFS, or FUSE mounts. Use
> `trigger "interval"` with a directory-listing function to poll network paths
> instead.

### `events`

Controls which filesystem event types trigger the action. The default is all
five types.

| Value | When it fires |
|---|---|
| `"create"` | A file or directory is created |
| `"write"` | A file's contents are modified |
| `"delete"` | A file or directory is removed |
| `"rename"` | A file or directory is renamed or moved |
| `"chmod"` | A file's permissions or attributes change |

> On some platforms `inotify` does not distinguish `write` from `chmod`; both
> may arrive as `write`. Rely on `"chmod"` only when portability is not required.

### `recursive`

When `true`, subdirectories are also watched and `ctx.event_path` carries the
full path of the specific file that changed. New subdirectories created after
startup are added automatically.

> On Linux each subdirectory consumes one inotify watch descriptor. The system
> default (`fs.inotify.max_user_watches`) may need to be increased for very
> large trees.

### `filter`

A glob pattern matched against `ctx.event_path` using
[`path.Match`](https://pkg.go.dev/path#Match) semantics. Events whose path does
not match are discarded without invoking `action`. `**` is not supported; use
`*` to match within a single directory component.

### `debounce`

Coalesces rapid successive events on the **same path** into a single invocation.
The timer resets on each new event; the action fires once the path has been
quiet for the full duration. Particularly useful when watching files written by
editors that produce several events per save.

Debounce is per-path: simultaneous changes on two different files each
independently dispatch after their own quiet window.

Durations follow the same formats as other duration attributes: numbers
(seconds), Go strings (`"200ms"`, `"1s"`), and ISO 8601 strings (`"PT0.2S"`).

### `on_start_existing`

When `true`, a synthetic `"create"` event is fired for every file already
present under `path` at startup. This lets a spool-directory handler process
files that arrived while vinculum was not running. Synthetic events respect
`filter` and `debounce`. They are dispatched from `PostStart()`, after all
`Startable` components have completed, so buses and clients are fully ready
when the action runs.

<!-- vinculum:begin block-attrs trigger file level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) | yes |  | Evaluated on each matching event. |
| `path` | expression | yes |  | File or directory to watch. |
| `debounce` | expression (duration) |  | `0` | Quiet period that coalesces rapid events on the same path. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `events` | expression |  | `["create", "write", "delete", "rename", "chmod"]` | Which event types fire the action. |
| `filter` | expression |  |  | Glob pattern the event path must match. |
| `on_start_existing` | bool |  | `false` | Emit a synthetic create for files already present at startup. |
| `recursive` | bool |  | `false` | Watch subdirectories too. |
| `skip_when` | expression (predicate-expression) |  |  | Skip this firing when true. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |

**`action`**

A failure is logged and surfaced as `ctx.last_error` on the next invocation; watching continues.

Evaluated against the `trigger-file` context.

**`debounce`**

The timer restarts on each new event; the action fires once the path has been quiet for the full duration. Per-path, so two files each dispatch after their own window. Useful for editors that emit several events per save. Zero dispatches every event as it arrives.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`events`**

On some platforms inotify reports a permission change as `"write"`, so rely on `"chmod"` only where portability does not matter.

One of: `create`, `write`, `delete`, `rename`, `chmod`.

**`filter`**

Uses Go `path.Match` semantics against `ctx.event_path`; non-matching events are discarded without evaluating the action. `**` is not supported — `*` matches within one directory component.

**`on_start_existing`**

Lets a spool-directory handler pick up files that arrived while vinculum was not running. Synthetic events respect `filter` and `debounce`, and are dispatched after every startable component is ready.

**`recursive`**

Subdirectories created after startup are picked up automatically. On Linux each one consumes an inotify watch descriptor, so a very large tree may need `fs.inotify.max_user_watches` raised.

**`skip_when`**

Evaluated before the action, against the same `ctx`.

Evaluated against the `trigger-file` context.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger file -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger file action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-file`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"file"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.path` | string | The configured `path` — the watched root. |
| `ctx.event_path` | string | Full path of the file or directory that produced the event. |
| `ctx.event` | string | Event type: `"create"`, `"write"`, `"delete"`, `"rename"`, or `"chmod"`. |
| `ctx.run_count` | number | Action invocations since startup. |
| `ctx.last_result` | dynamic | Result of the most recently completed action, or null. |
| `ctx.last_error` | string | Error from the most recent action, or null if none. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.event_path`**

For a `"rename"` event this is the **old** path. Not every OS backend reports the destination, so pair rename with the subsequent `"create"` when you need it.

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger file action -->

For `"rename"` events, `ctx.event_path` is the **old** (source) path. The new
destination path is not available from all OS backends; pair rename with a
subsequent `"create"` event when the destination is needed.

If an action fails, the error is logged and `ctx.last_error` is set for the
next invocation. The trigger continues watching; subsequent events are not
suppressed.

**Creates** `trigger.<name>` as a capsule; read the most recent action result
with `get(trigger.<name>)`.

Example — process a spool directory, including files that arrived while stopped:

```hcl
trigger "file" "spool" {
    path              = "/var/spool/myapp/inbound"
    events            = ["create"]
    filter            = "*.json"
    on_start_existing = true
    action = [
        log::info("processing spool file", {path = ctx.event_path}),
        send(ctx, bus.main, "spool/inbound", fileread(ctx.event_path)),
    ]
}
```

Example — recursive YAML config directory, skipping deletes:

```hcl
trigger "file" "conf_d" {
    path      = "/etc/myapp/conf.d"
    recursive = true
    filter    = "*.yaml"
    events    = ["create", "write", "rename"]
    debounce  = "200ms"
    action    = reload_fragment(ctx.event_path)
}
```

Example — re-read TLS certificates when renewed:

```hcl
trigger "file" "tls_cert_rotate" {
    path     = "/etc/ssl/myapp"
    events   = ["create", "write"]
    filter   = "*.pem"
    debounce = "500ms"
    action   = log::info("TLS cert changed, reload required", {file = ctx.event_path})
}
```

Example — guard against false creates with `skip_when`:

```hcl
trigger "file" "spool_arrive" {
    path      = "/var/spool/drop"
    events    = ["create"]
    skip_when = !fileexists(ctx.event_path)
    action    = send(ctx, bus.main, "spool/new", ctx.event_path)
}
```

---

## `trigger "interval"`

```hcl
trigger "interval" "name" {
    delay         = expression  # optional — duration between runs; omit for set()-driven triggers
    initial_delay = expression  # optional — duration before the very first run (default: delay)
    error_delay   = expression  # optional — duration after a failed run (default: delay)
    jitter        = 0.0         # optional — fraction in [0, 1]; actual delay uniform in [delay*(1-jitter/2), delay*(1+jitter/2)]
    repeat        = true        # optional — if false, fire once then go dormant until set()
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

### Imperative control with `set()` and `reset()`

`set(trigger.<name>, duration)` restarts the trigger with the given delay,
resetting the run count. If the trigger has stopped (via `stop_when` or
`repeat = false`), `set()` revives it. The duration override persists until
cleared by `reset()` or replaced by another `set()`. **Note that if jitter is
configured, it will be applied to the passed-in duration**.

`set(trigger.<name>)` with no duration argument restarts using the configured
`delay` expression. Returns an error if no `delay` was configured.

`reset(trigger.<name>)` cancels any pending timer and puts the trigger into a
dormant state, waiting for the next `set()` call. Clears the delay override,
run count, and last result/error.

### Dormant start (no `delay`)

When `delay` is omitted, the trigger starts dormant and waits for the first
`set()` call before firing. This is the idiomatic way to create a fully
`set()`-driven trigger. `initial_delay` and `error_delay` cannot be used
without `delay`.

### `repeat = false`

When `repeat` is `false`, the trigger fires once and then goes dormant,
waiting for `set()` to revive it. This is useful for one-shot timers driven
by external state — for example, an FSM whose `on_entry` hooks set different
delays for each state.

<!-- vinculum:begin block-attrs trigger interval level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) | yes |  | Evaluated each time the delay elapses. |
| `delay` | expression (action-expression) |  |  | How long to wait between runs. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `error_delay` | expression (action-expression) |  |  | How long to wait after a run that failed. |
| `initial_delay` | expression (duration) |  |  | How long to wait before the very first run. |
| `jitter` | number |  | `0` | Fraction of the delay to randomize by, in [0, 1]. |
| `repeat` | bool |  | `true` | Keep rescheduling after each run. |
| `stop_when` | expression (predicate-expression) |  |  | Stop the trigger when this evaluates true. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |

**`action`**

Sees the state from *before* this iteration: `ctx.run_count` is 0 on the first run.

Evaluated against the `trigger-interval` context.

**`delay`**

Re-evaluated each iteration. Accepts a number of seconds, a Go duration string, an ISO 8601 duration, or a duration capsule. Omit it to start dormant, waiting for the first `set()`; `initial_delay` and `error_delay` cannot be used without it.

Evaluated against the `trigger-interval` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`error_delay`**

Defaults to `delay`. Re-evaluated each iteration, like `delay`.

Evaluated against the `trigger-interval` context.

**`initial_delay`**

Defaults to `delay`. Set `"0s"` to run immediately at startup.

**`jitter`**

The actual wait is drawn uniformly from `[delay*(1-jitter/2), delay*(1+jitter/2)]`, so the average is unchanged. Use it to desynchronize instances running the same schedule. Applies to a `set()` override as well.

**`repeat`**

When false, the trigger fires once and goes dormant until `set()` revives it — a one-shot timer driven by external state, such as an FSM whose `on_entry` hooks set a different delay per state.

**`stop_when`**

Evaluated after the action completes, with `ctx.run_count` already incremented.

Evaluated against the `trigger-interval` context.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger interval -->

When each iteration runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger interval action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-interval`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"interval"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.run_count` | number | Completed action evaluations; 0 on the first run. |
| `ctx.last_result` | dynamic | Result of the previous evaluation, or null on the first run. |
| `ctx.last_error` | string | Error from the previous evaluation, or null if it succeeded. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger interval action -->

`ctx` is available in `delay`, `error_delay`, and `action` (evaluated with the
state *before* this iteration). `stop_when` is evaluated *after* the action
completes, with `ctx.run_count` already incremented.

**Creates** `trigger.<name>` as a capsule; read the most recent result with
`get(trigger.<name>)`, restart with `set(trigger.<name>, duration)`, or cancel
with `reset(trigger.<name>)`.

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

Example — FSM-driven one-shot timer with variable delays per state:

```hcl
trigger "interval" "phase_timer" {
    repeat = false
    action = send(ctx, fsm.intersection, "next_phase", {})
}

fsm "intersection" {
    initial = "ns_green"

    state "ns_green" {
        on_entry = set(trigger.phase_timer, "30s")
    }
    state "ns_yellow" {
        on_entry = set(trigger.phase_timer, "5s")
    }
    state "clearance" {
        on_entry = set(trigger.phase_timer, "2s")
    }
    state "fault" {
        on_entry = reset(trigger.phase_timer)  # cancel pending timer
    }

    event "next_phase" {
        transition "ns_green" "ns_yellow" {}
        transition "ns_yellow" "clearance" {}
        transition "clearance" "ns_green" {}
    }
    event "fault" {
        transition "*" "fault" {}
    }
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

<!-- vinculum:begin block-attrs trigger once level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) | yes | Evaluated on the first `get(trigger.<name>)`. |
| `disabled` | bool |  | Skip this block entirely. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`action`**

Evaluated against the `trigger-once` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger once -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger once action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-once`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"once"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger once action -->

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

The action runs with the front door already closed and the runtime behind it
still up: servers have stopped accepting and drained their in-flight work
(see each server's `shutdown_timeout`), while buses, clients, and subscriptions
are stopped only after every shutdown action has returned. So an action may
still `send()` to a bus or call out through a client, but it will not see new
inbound requests arriving alongside it.

<!-- vinculum:begin block-attrs trigger shutdown level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) | yes | Evaluated once during shutdown. |
| `disabled` | bool |  | Skip this block entirely. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`action`**

Evaluated against the `trigger-shutdown` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger shutdown -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger shutdown action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-shutdown`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"shutdown"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger shutdown action -->

Does **not** create a `trigger.<name>` value.

Example — log a goodbye message on shutdown:

```hcl
trigger "shutdown" "bye" {
    action = log::info("shutting down", {name = ctx.name})
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

<!-- vinculum:begin block-attrs trigger signals level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `SIGHUP` | expression (action-expression) |  | Evaluated on SIGHUP. |
| `SIGINFO` | expression (action-expression) |  | Evaluated on SIGINFO. |
| `SIGUSR1` | expression (action-expression) |  | Evaluated on SIGUSR1. |
| `SIGUSR2` | expression (action-expression) |  | Evaluated on SIGUSR2. |
| `disabled` | bool |  | Skip this block entirely. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`SIGHUP`**

Conventionally a request to reload configuration or reopen log files.

Evaluated against the `trigger-signals` context.

**`SIGINFO`**

A BSD/macOS status-request signal, typically sent with Ctrl-T. Not available on Linux.

Evaluated against the `trigger-signals` context.

**`SIGUSR1`**

Reserved for the application; give it whatever meaning suits.

Evaluated against the `trigger-signals` context.

**`SIGUSR2`**

Reserved for the application; give it whatever meaning suits.

Evaluated against the `trigger-signals` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger signals -->

When a signal fires, `ctx` provides:

<!-- vinculum:begin block-ctx trigger signals SIGHUP level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-signals`):

| Field | Type | Description |
|---|---|:---|
| `ctx.signal` | string | Signal name, e.g. `"SIGHUP"`. |
| `ctx.signal_num` | number | OS-level signal number. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.signal_num`**

Numbers vary by platform; `sys.signals.SIGHUP` is the portable way to name one.

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger signals SIGHUP -->

Unlike the other trigger types there is no `ctx.trigger` or `ctx.name`: a
signal handler is identified by the signal, not by the block it was declared
in.

Does **not** create a `trigger.<name>` value.

Example — log the signal name on SIGUSR1:

```hcl
trigger "signals" "main" {
    SIGUSR1 = log::info("received signal", {signal = ctx.signal})
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

Evaluates `action` once at startup, after every startable component — buses,
servers, clients — is ready, so the action can send messages and reach
external services. An error is logged to the user log and does not abort
startup.

The result of `action` is stored as `trigger.<name>` in the global evaluation
context. Because it is produced after startup rather than during the
configuration build, a block that reads `trigger.<name>` at config load time
sees `null`; read it at runtime with `get(trigger.<name>)` instead.

<!-- vinculum:begin block-attrs trigger start level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) | yes | Evaluated once at startup; its result becomes `trigger.<name>`. |
| `disabled` | bool |  | Skip this block entirely. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`action`**

Evaluated against the `trigger-start` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger start -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger start action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-start`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"start"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger start action -->

**Creates** `trigger.<name>` with the value returned by `action`.

Example — log a startup message:

```hcl
trigger "start" "hello" {
    action = log::info("vinculum started", {host = sys.hostname})
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

> For condition-local reactions, consider `on_activate` / `on_deactivate` /
> `on_init` declared inline on the condition block instead — see
> [Lifecycle Hooks in condition.md](condition.md#lifecycle-hooks). Hooks run
> synchronously on the caller's goroutine and guarantee post-bootstrap
> ordering via `on_init`; `trigger "watch"` remains the right choice for
> async dispatch and cross-cutting observers.

When a change is observed, `action` is dispatched to a new goroutine so the caller of
`set()` / `increment()` is not blocked. If `skip_when` is provided, it is evaluated first
(in the same goroutine); if it returns `true`, the action is skipped. Each firing evaluates
`skip_when` independently.

The dispatched action runs with the caller's context **values** (trace spans, auth, etc.)
preserved, but the caller's cancellation is severed — so an upstream ctx cancel (e.g. an
HTTP request that drove the change completing) cannot interrupt the in-flight action
mid-operation. The action's span is a new root linked to the caller's span, matching OTel
async-messaging conventions.

`get(trigger.<name>)` returns the most recently observed `newValue`, or `null` before any
change has been observed since startup.

On `Stop()`, the trigger unregisters itself from the Watchable and waits for all in-flight
action goroutines to complete before returning.

<!-- vinculum:begin block-attrs trigger watch level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) | yes | Evaluated on each observed change. |
| `watch` | expression | yes | The value to watch. |
| `disabled` | bool |  | Skip this block entirely. |
| `skip_when` | expression (predicate-expression) |  | Skip this firing when true. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`action`**

Evaluated against the `trigger-watch` context.

**`watch`**

Evaluated once at config build time and must produce a watchable capsule; anything else is a config error.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`skip_when`**

Evaluated first, in the same goroutine, against the same `ctx`. Each firing evaluates it independently.

Evaluated against the `trigger-watch` context.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

<!-- vinculum:end block-attrs trigger watch -->

When `action` (and `skip_when`) are evaluated, `ctx` provides:

<!-- vinculum:begin block-ctx trigger watch action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-watch`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"watch"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.old_value` | dynamic | The value before the change. |
| `ctx.new_value` | dynamic | The value after the change. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger watch action -->

**Creates** `trigger.<name>` as a capsule; read the last observed value with
`get(trigger.<name>)`.

Example — log every update on a variable (fires on every `set()`, including repeated equal values):

```hcl
var "temperature" {}

trigger "watch" "on_temp_update" {
    watch  = var.temperature
    action = log::info("temperature updated", {
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
    action    = log::info("temperature changed", {
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
    action    = log::warn("queue depth exceeded threshold", {depth = ctx.new_value})
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

<!-- vinculum:begin block-attrs trigger watchdog level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) | yes |  | Evaluated each time the watchdog fires. |
| `window` | expression (duration) | yes |  | Fire if not fed within this duration. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `initial_grace` | expression (duration) |  |  | Grace period before the first countdown. |
| `max_misses` | number |  |  | Auto-stop after this many consecutive fires. |
| `repeat` | bool |  | `false` | Keep firing every window until fed. |
| `stop_when` | expression (predicate-expression) |  |  | Auto-stop when this evaluates true. |
| `tracing` | expression (tracing-ref) |  |  | Where to report traces. |
| `watch` | expression |  |  | A watchable value that feeds the watchdog automatically. |

**`action`**

Evaluated against the `trigger-watchdog` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`initial_grace`**

Defaults to `window`. Gives components time to start up and feed the watchdog once before it can fire.

**`max_misses`**

At least 1, and unlimited when omitted. Feeding the watchdog resets the miss count to 0 and re-arms it immediately, so this caps how many times an alert repeats before an explicit acknowledgement.

**`repeat`**

When false the watchdog goes dormant after firing and waits to be fed again, so a known-broken condition does not flood alerts. Set true for paging systems where ongoing alerting is wanted.

**`stop_when`**

Evaluated after each fire with the same `ctx` as the action, including the updated `ctx.miss_count`. Feeding the watchdog re-evaluates it against the post-`set()` state, re-arming if it is now false. `stop_when = ctx.miss_count >= N` is equivalent to `max_misses = N`.

Evaluated against the `trigger-watchdog` context.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

**`watch`**

Each change to it counts as `set(trigger.<name>, newValue)`, which decouples the producer from the watchdog: the producer only updates the variable and need not know a watchdog exists. Manual `set()` calls still work alongside it.

<!-- vinculum:end block-attrs trigger watchdog -->

When the action runs, `ctx` provides:

<!-- vinculum:begin block-ctx trigger watchdog action level=3 -->

Fields readable as `ctx.<name>` (shape `trigger-watchdog`):

| Field | Type | Description |
|---|---|:---|
| `ctx.trigger` | string | Always `"watchdog"`. |
| `ctx.name` | string | Name of this trigger block. |
| `ctx.miss_count` | number | Consecutive fires since the last feed. |
| `ctx.last_set` | capsule | Time of the last feed, or null if never fed. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.miss_count`**

Reset to 0 by `set(trigger.<name>)`.

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx trigger watchdog action -->

**Creates** `trigger.<name>` as a capsule; use `set()` to feed it and `get()` to
read the last stored value.

Example — heartbeat monitoring (alert if worker goes silent for 90 seconds):

```hcl
trigger "watchdog" "worker_alive" {
    window = "90s"
    action = log::warn("worker missed heartbeat", {missed = ctx.miss_count})
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
    action        = log::warn("upstream health check stopped", {name = ctx.name})
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
    action = log::warn("sensor went silent", {last = ctx.last_set})
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

`reset(trigger.<name>)` returns the watchdog to its post-startup state:
the stored value (`get()`) is cleared back to `null`, `miss_count` is
zeroed, and the countdown is re-armed (reviving the trigger if it had
auto-stopped via `max_misses` or `stop_when`). Use `reset()` when an
operator wants to discard the existing heartbeat history rather than
acknowledge it (which `set()` does); for the common ack-and-rearm flow
`set()` is usually the right call.

---

## Watchables

`var`, gauge `metric`, and counter `metric` values implement the `Watchable` interface, as do `condition` and `fsm` types.
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

---

## Distributed Tracing

When a `client "otlp"` block is configured, every trigger firing automatically
creates an OTel span. No `tracing =` attribute is needed — trigger spans use the
global `TracerProvider` set by `client "otlp"`.

**Span naming:**

| Trigger type | Span name |
|---|---|
| `trigger "after"` | `trigger.after <name>` |
| `trigger "at"` | `trigger.at <name>` |
| `trigger "cron"` | `trigger.cron <cron-name>/<at-name>` |
| `trigger "file"` | `trigger.file <name>` |
| `trigger "interval"` | `trigger.interval <name>` |
| `trigger "once"` | `trigger.once <name>` |
| `trigger "shutdown"` | `trigger.shutdown <name>` |
| `trigger "signals"` | `trigger.signal <signal-name>` |
| `trigger "start"` | `trigger.start <name>` |
| `trigger "watch"` | `trigger.watch <name>` |
| `trigger "watchdog"` | `trigger.watchdog <name>` |

**Span scope:** The span covers the action expression evaluation only — idle
wait time (delays, sleep periods, timer countdowns) is excluded. This ensures
span duration reflects actual work, not scheduling overhead.

**Span parenting:**

- Most trigger types start a **new root span** on each firing.
- `trigger "watch"` uses the **incoming context** as its parent, so the span
  becomes a child of whatever called `set()` or `increment()` on the watched
  value. This chains watch-triggered actions into the same trace as the
  upstream event (e.g. an HTTP request that triggered a `var` update).

**Error recording:** If the action expression returns an HCL diagnostic error,
the error is recorded on the span (`span.RecordError`) and the span status is
set to `Error`.

**`ctx.trace_id` / `ctx.span_id`:** Available in all trigger `action`
expressions when a span is active. Both are `""` when no OTLP client is
configured.

Example — include the trace ID in a downstream HTTP request:

```hcl
trigger "interval" "poll" {
    delay  = "30s"
    action = send(ctx, bus.main, "poll/result", {
        trace = ctx.trace_id
        data  = fetch_data()
    })
}
```

See [client "otlp"](client-otlp.md) for full tracing configuration.
