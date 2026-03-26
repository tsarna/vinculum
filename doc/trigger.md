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
triggers with the same name regardless of type. `trigger "start"` blocks additionally
expose their result value as `trigger.<name>` in the global evaluation context.

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
