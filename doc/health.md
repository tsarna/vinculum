# Health, Readiness, and Liveness

Vinculum knows whether it is able to do its job, and will tell you. A process
whose broker connection is down is running but not serving, and the difference
matters to anything routing traffic at it.

Three separate questions, deliberately not the same one:

| Question | Who asks | Failing means |
|---|---|---|
| **Readiness** — should traffic be routed here right now? | a load balancer, a Kubernetes `readinessProbe` | stop sending requests here, for now. Reversible. |
| **Liveness** — is this process wedged beyond recovery? | a Kubernetes `livenessProbe` | kill and restart the process. |
| **Startup** — has boot finished? | a Kubernetes `startupProbe` | not up yet; hold off on the other probes. |

Conflating readiness and liveness is the classic production incident: a broker
outage that makes every replica *not ready* is a degraded service, but one that
makes every replica *not live* is a fleet-wide restart loop. So:

- Anything that can lose its connection contributes to **readiness**.
- **Nothing** contributes to liveness except the process itself and checks you
  deliberately wrote for it.
- Startup is not a separate answer. Readiness reports `starting` until boot
  completes, so a `startupProbe` aimed at readiness behaves correctly.

---

## Contents

- [What contributes to readiness](#what-contributes-to-readiness)
- [Reading readiness from a configuration](#reading-readiness-from-a-configuration)
- [`sys.ready`](#sysready)
- [Reacting to transitions](#reacting-to-transitions)
- [The `check` block](#the-check-block)
- [Liveness](#liveness)
- [When readiness is computed](#when-readiness-is-computed)

---

## What contributes to readiness

| Contributor | Ready when |
|---|---|
| `process` | Boot has completed and shutdown has not begun. Always present. |
| a `server` | Its listener is bound and serving. |
| a `client` | Its connection is established. |
| a [`check`](#the-check-block) | Its `input` says so. |

The process is ready when every contributor is. A component with nothing to
report — a stateless client, a bus, a trigger — contributes nothing and is
absent from the report entirely: unknown is treated as ready, so adding a
component never makes a working configuration permanently unready.

### Opting a component out

A client or server that reports its own state gates readiness by default. For an
integration the service can do without, say so:

```hcl
client "mqtt" "optional_feed" {
    broker    = "tcp://feed.example.com:1883"
    readiness = false     # losing this does not take us out of rotation
}
```

Without it, a configuration with one optional integration would have to choose
between a permanently unready process and deleting the integration.

`readiness` exists only on the types that have readiness to report. On any other
it is rejected rather than silently ignored, so the attribute's presence in a
block's reference page means it does something there.

---

## Reading readiness from a configuration

Two booleans and two detail views, each taking the handler context as its first
argument:

| Function | Returns |
|---|---|
| `health::ready(ctx)` | `true` when the process is ready to serve traffic. |
| `health::live(ctx)` | `true` when the process is live. |
| `health::status(ctx [, probe])` | Every contributor to that probe, passing ones included. |
| `health::failing(ctx [, probe])` | Only the contributors that are failing, with the reason for each. |
| `health::refresh(ctx)` | Re-evaluates everything now, ignoring the cache, and returns readiness as a boolean. |

The optional `probe` selects `"ready"` (the default) or `"live"`.

```hcl
action = health::ready(ctx) ? "ok" : http::error(503, "not ready")
```

Each entry of a detail view is an object:

```hcl
{
    component = "client.broker"   # or "check.database", or "process"
    type      = "mqtt"            # the block's type label, "" where there is none
    ready     = false
    reason    = "not connected: dial tcp 10.0.0.5:1883: connection refused"
}
```

A list rather than a map keyed by name: it preserves boot order, so
infrastructure reads before the things that depend on it, and it serializes to
JSON the way an operator expects.

`health::failing` is the one to log. A healthy process has nothing to say, so an
empty result is the good case and a transition line stays short:

```hcl
log::warn("not ready", {problems = health::failing(ctx)})
```

`ctx` is required, and leading, as it is for `send()` and `http::get()`. A
refresh evaluates `check` expressions, which run arbitrary I/O, so it needs the
caller's trace parent and deadline. It also means `health::ready()` in a `const`
or other load-time expression fails to resolve, rather than quietly reporting
that everything is fine from a registry nothing has filled in yet.

---

## `sys.ready`

The aggregate as a plain boolean, reachable anywhere:

```hcl
get(sys.ready)          # true / false
get(ctx, sys.ready)     # the same, but traced and deadline-bounded
```

Prefer the second form wherever a `ctx` is in scope. `sys.ready` is the
primitive and the `health::` functions are the detail views over the same
report.

It is also **watchable**, so a reactive expression naming it is re-evaluated
whenever readiness flips:

```hcl
condition "timer" "degraded" {
    input          = !get(sys.ready)
    activate_after = "1m"           # ... and has stayed down a while
}
```

This split falls out of the required `ctx` rather than being imposed: the
boolean is reachable everywhere through the accessor, and the detail — which
costs I/O to produce — only where there is a context to charge it to. A
[`condition`](condition.md)'s `input` has no `ctx` in scope, so it uses the
accessor, which is the better form there anyway: a reactive expression must name
a watchable to re-evaluate at all.

---

## Reacting to transitions

There is no `on_ready` hook and no `trigger "ready"`. `sys.ready` is watchable,
so [`trigger "watch"`](trigger.md#trigger-watch) already covers it, with
`skip_when`, tracing, and `ctx.old_value` / `ctx.new_value` included:

```hcl
trigger "watch" "announce_ready" {
    watch     = sys.ready
    skip_when = !ctx.new_value
    action    = send(ctx, bus.main, "system/ready", health::status(ctx))
}
```

An individual check is watchable too, so you can react to one without involving
the aggregate:

```hcl
trigger "watch" "database_flapped" {
    watch  = check.database
    action = log::warn("database check changed", {passing = ctx.new_value})
}
```

---

## The `check` block

A check adds a condition of your own to a probe.

```hcl
check "database" {
    input  = get(ctx, client.db)          # required
    reason = "database is unreachable"
}
```

<!-- vinculum:begin block-attrs check level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `input` | expression | yes |  | What this check tests. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `probe` | string |  | `ready` | Which probe this check belongs to. |
| `reason` | string |  | `check failed` | Why a failing check matters, in words. |
| `timeout` | expression (duration) |  | `2s` | How long to allow `input` before giving up on it. |

**`input`**

Evaluated on each probe rather than on an event, so it is the one place a
configuration gets to compute something at the moment a prober asks.

How the result is read:

| Result | Meaning |
|---|---|
| `true` | passing |
| `false` | failing, with `reason` as the reason |
| a string | failing, with that string as the reason |
| `null` | failing, reason `check returned null` |
| `{ready = bool, reason = string}` | as stated |
| an evaluation error | failing, with the error as the reason |

A returned string is always a complaint: a healthy check has nothing to say, the
same principle that makes an empty `health::failing()` mean healthy.

Evaluated against the `check` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`probe`**

`ready` means a failure should stop traffic being routed here — the pod leaves the load balancer and comes back when the check passes again. `live` means a failure should **restart the process**, so use it only for a wedge nothing else can clear, never for a dependency being down. A check participates in exactly one probe; a signal that belongs to both is two checks over one condition, which is rare and should be conspicuous.

One of: `ready`, `live`.

**`reason`**

Reported as the reason whenever `input` says the check fails without supplying one of its own. It reads as a fragment completing "`check.<name>` is not ready: …".

**`timeout`**

A check that exceeds this is reported as failing with `timed out after …`, so one slow dependency cannot hold a probe open past the deadline its caller set.

<!-- vinculum:end block-attrs check -->

### What `input` may return

`input` is evaluated **on each probe**, not on an event — a check is the one
construct in the language that runs when a prober asks rather than when
something happens.

| Result | Meaning |
|---|---|
| `true` | passing |
| `false` | failing, with `reason` as the reason |
| a string | failing, with that string as the reason |
| `null` | failing, reason `check returned null` |
| `{ready = bool, reason = string}` | as stated |
| an evaluation error | failing, with the error as the reason |

A returned string is always a complaint. A healthy check has nothing to say —
the same principle that makes an empty `health::failing()` mean healthy.

### Composing with conditions

Flapping suppression, delay, and latching are not reimplemented here.
[`condition`](condition.md) already does all of that, and the two layers stay
separate on purpose:

- **`condition`** is *behavior* over a boolean: debounce it, delay it, latch
  it. It has no idea what the boolean is for.
- **`check`** is *meaning* over a boolean: this one, when false, means do not
  send this process traffic.

So a check that needs hysteresis names a condition:

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

The condition remains a plain boolean anything may read; the check is the single
place that says what it means for serving traffic.

### `check.<name>`

Each check publishes `check.<name>`, holding its last result. It is readable
with `get()` and watchable. A check nothing has probed yet reads as `true`.

<!-- vinculum:begin context check level=3 -->

Evaluated on each health probe that consults this check.

Derived from the context of whatever asked for the probe — an HTTP request to `/readyz`, a `health::` call, a metrics scrape — so a slow probe is diagnosable as "the database check took 1.8s of it" rather than as an unexplained pause. There is no message in flight, so there are no message fields.

Fields readable as `ctx.<name>` (shape `check`):

| Field | Type | Description |
|---|---|:---|
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

#### Evaluated by

- `check` › `input`

<!-- vinculum:end context check -->

---

## Liveness

Liveness is deliberately **not** symmetric with readiness.

No client and no server contributes to it. A dependency outage restarting every
replica is a well-known cascading failure, and the mechanism that would permit
it is simply not built. Liveness fails only when the process itself cannot
answer, or when a check you wrote says so.

A liveness check takes a deliberate, visible declaration — because this is the
setting that restarts your pods:

```hcl
trigger "watchdog" "pipeline" {
    window = "10m"
    action = set(var.pipeline_stalled, true)
}

check "pipeline_progress" {
    probe  = "live"
    input  = !get(var.pipeline_stalled, false)
    reason = "no pipeline output for 10 minutes"
}
```

That is the canonical shape: a watchdog detecting that work has stopped
entirely, which no amount of waiting will fix. **Never** put a dependency in a
liveness check. "The database is down" is a readiness failure; restarting the
process does not bring the database back, and doing it across every replica at
once turns an outage into a worse one.

A check belongs to exactly one probe. A signal that genuinely belongs to both is
two checks over one condition — rare, and conspicuous when written out.

---

## When readiness is computed

**Readiness is computed only when something asks for it. Vinculum runs no health
goroutine, ever.** A bridge on an edge box with no probes never pings its
database on a timer nobody asked for.

What asks, in practice:

| Asker | How often |
|---|---|
| any `health::` function in an action | whenever that action runs |
| `get(sys.ready)` in a reactive expression | whenever it is re-evaluated |
| `trigger "interval"` calling `health::refresh(ctx)` | whatever cadence you chose |
| `vinculum test`'s readiness barrier | during its startup poll |

A computed report is reused for **5 seconds**. That is not a rate limit on a
ticker — there is no ticker — but a bound on how much work a caller asking
repeatedly can make the process do. Concurrent askers share one evaluation
rather than each starting their own.

`health::refresh(ctx)` ignores that cache. It exists because scheduling is
already a block type, so polling is something you write rather than something
Vinculum hides:

```hcl
trigger "interval" "health_poll" {
    delay  = "10s"
    action = cond(health::refresh(ctx), "ok",
                  log::warn("not ready", {problems = health::failing(ctx)}))
}
```

and a rate limit that silently overrode your stated `delay = "1s"` would be
indefensible. Note `cond()` rather than `?:`: HCL's ternary evaluates both
branches in no guaranteed order, so it can read the cache before the refresh
replaces it. The cache governs callers the configuration did not choose; the
configuration's own instructions are not second-guessed.

### `sys.ready` is a *sampled* watchable

This is the one real cost of having no poller, and it is part of the contract
rather than an implementation detail. Every other watchable in Vinculum — `var`,
`condition`, `metric` — fires because something actively changed it. `sys.ready`
changes during a refresh, and refreshes happen only when something asks. So:

```hcl
# In a configuration with no probe and no interval trigger, this never fires.
# It is not broken; nothing ever looked.
trigger "watch" "readiness_changed" {
    watch  = sys.ready
    action = log::warn("readiness changed", {ready = ctx.new_value})
}
```

A transition is therefore observed up to one asking-interval late. Where that
matters, add the `trigger "interval"` above and you have an interval you can
name, disable, trace, and change — none of which a hidden ticker would have
offered.

---

## Boot and shutdown

Two states carry more weight than any component's:

**Boot.** Readiness is false from process start until every component has
started, with reason `starting`. A `startupProbe` therefore works without a
separate endpoint.

**Shutdown.** On `SIGTERM`, readiness goes false *before* anything is torn down,
with reason `shutting down`, so a load balancer stops sending new work while
requests already in flight finish. This is what distinguishes a graceful rollout
from a lossy one.

Both are one-way. Vinculum has no configuration reload, so the set of
contributors is fixed for the life of the process and the sequence is always
`starting → serving → shutting down`.
