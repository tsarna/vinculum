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
- [Backpressure as a readiness signal](#backpressure-as-a-readiness-signal)
- [HTTP endpoints](#http-endpoints)
- [Metrics and logging](#metrics-and-logging)
- [Liveness](#liveness)
- [When readiness is computed](#when-readiness-is-computed)
- [Boot and shutdown](#boot-and-shutdown)
- [Starting when a dependency is down](#starting-when-a-dependency-is-down)

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
    since     = <time>            # when it last changed to this state
}
```

`since` is a `time`, the same type `sys.starttime` carries, so the `time::`
functions work on it directly — which is how you ask the question that actually
matters, "has this been broken long enough to care?":

```hcl
trigger "interval" "page_on_a_sustained_outage" {
    delay  = "1m"
    action = send(ctx, bus.alerts, "outage", [
        for f in health::failing(ctx) : f.component
        if duration::gt(time::since(f.since), duration("5m"))
    ])
}
```

A component that has never changed carries the moment it was **first observed**
rather than the moment the process started. Those differ: a check is not
evaluated until something probes, so in a configuration nothing polls, the
first probe is when every age begins. `process` is the exception and the useful
one — it flips when boot completes, so its `since` is how long the process has
been serving.

A client that reports its own disconnect (see
[What is prompt, and what is sampled](#what-is-prompt-and-what-is-sampled))
dates the drop to the moment it happened rather than to the probe that later
noticed it — on a ten-second probe period, that difference is most of the
number.

A list rather than a map keyed by name: it preserves boot order, so
infrastructure reads before the things that depend on it, and it serializes to
JSON the way an operator expects.

`health::failing` is the one to log. A healthy process has nothing to say, so an
empty result is the good case and a transition line stays short:

```hcl
log::warn("not ready", {problems = health::failing(ctx)})
```

At an interactive prompt, [`:ready`](repl.md#is-it-serving-ready) prints the
same report without your having to write a call — and without the process
needing a health endpoint configured at all.

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

**Use `cond()` to choose between those shapes, not `?:`.** HCL's ternary
requires both branches to have the same type, and the two most useful results —
an object carrying a reason, and a bare `true` — do not unify:

```hcl
# Wrong. Passes `vinculum check`, then fails on every probe with
# "Inconsistent conditional result types: the 'true' value is object,
#  but the 'false' value is bool."
input = get(condition.degraded) ? {ready = false, reason = "degraded"} : true

# Right.
input = cond(get(condition.degraded), {ready = false, reason = "degraded"}, true)
```

`input` is evaluated at probe time, so a mistake here is not caught at load —
the check simply reports the evaluation error as its reason on every probe.

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

## Backpressure as a readiness signal

Every readiness signal above is about a **dependency**: a broker connection, a
listener, a check you wrote. None is about the process being *overwhelmed*.

That is the gap this fills. A Vinculum instance whose bus queue is full is still
connected to everything, still passing every check, still answering `/readyz`
with 200 — and dropping messages on the floor. From outside, a process silently
shedding load is indistinguishable from a healthy one, which is the worst shape
a failure can have.

The bus offers the numbers ([what the bus counts](config.md#what-the-bus-counts));
this section is the composition that turns one into unreadiness, and the reasons
it is shaped the way it is.

### The composition

```hcl
var "bus_load" { value = 0 }

trigger "interval" "sample_bus" {
    delay  = "1s"
    action = set(ctx, var.bus_load, get(bus.main, "queue_ratio"))
}

condition "threshold" "bus_saturated" {
    input = get(var.bus_load)

    on_above  = 0.9      # trip when the queue is 90% full …
    off_below = 0.5      # … and recover only when it is back under half

    activate_after   = "15s"    # sustained, not a spike
    deactivate_after = "120s"   # much slower to come back than to leave
}

check "bus_has_headroom" {
    input = cond(get(condition.bus_saturated),
                 { ready = false, reason = "bus queue saturated" },
                 true)
}
```

**Every number in that example is a policy decision.** They are not defaults
that suit anyone: 90% of a 4096-slot queue on a bus that normally idles at 3 is
an emergency, and on a bus that normally runs at 3500 it is Tuesday. Vinculum
deliberately does not pick them for you — a wrong default here takes a service
down.

### Why the sampler

A `condition`'s `input` is a reactive expression: it re-evaluates when a
watchable it names changes. **Bus depth is deliberately not watchable.** Depth
changes on the hottest path in the system, and a health feature has no business
adding work there — so a condition naming `get(bus.main, "queue_ratio")`
directly would evaluate once and never again.

Hence the `var` and the `trigger "interval"`: `var` *is* watchable, so the
condition re-evaluates on each sample. One second is a reasonable cadence; the
accessors are `len`/`cap` on a channel and cost nothing.

### Why the timers are asymmetric

`deactivate_after` is an order of magnitude longer than `activate_after`, on
purpose.

Backpressure is unlike every other readiness signal in one specific way: **it is
load-dependent, and load is redistributed by the act of reporting it.** When a
broker connection drops, every replica usually reports it together and nothing
moves. When one replica reports a saturated queue, its traffic moves to the
others — pushing them closer to the same threshold. If they cross it, they leave
too, concentrating the load further. The end state is every replica out of
rotation and no traffic served, from a condition where every replica was still
serving most of its traffic.

That is a positive feedback loop reachable from a *correct* implementation.
Nothing about it requires a bug. A fleet in which replicas leave quickly and
return slowly settles; one in which they return as fast as they left
oscillates.

The deadband (`0.9` / `0.5`) does the same job on the value axis that the timers
do on the time axis. Both are wanted; neither substitutes for the other.

### What Vinculum cannot do for you

The third thing that bounds the loop above is **a floor on available replicas**:
a Kubernetes PodDisruptionBudget, `maxUnavailable`, or a load balancer's
outlier-detection floor, so that "everyone is saturated" cannot become "nobody
serves traffic".

**A single process cannot see the fleet**, so Vinculum cannot provide this and
does not pretend to. Set the floor in your orchestrator. Reporting honestly and
letting something else decide what to do about it is the entire job of a probe.

### Readiness only. Never liveness.

`probe = "live"` on a saturation check turns a load spike into a fleet-wide
restart loop — and worse, a restarting process sheds its queue, which looks like
recovery, which leaves it primed to restart again under the next spike.

This is the same mistake as putting a dependency in a liveness check
([Liveness](#liveness)), in a more tempting costume.

### Use `cond()`, not `?:`

HCL's ternary requires both branches to unify, and a `check` input's two useful
shapes — an object carrying a reason, and a bare `true` — do not:

```hcl
# Wrong. Passes `vinculum check`, then fails on every probe with
# "Inconsistent conditional result types: the 'true' value is object,
#  but the 'false' value is bool."
input = get(condition.bus_saturated) ? { ready = false, reason = "…" } : true

# Right.
input = cond(get(condition.bus_saturated), { ready = false, reason = "…" }, true)
```

### Drops as a signal

Dropping is the unambiguous harm, so "unready when `dropped` is climbing" is
tempting and simpler than a depth threshold. It is the wrong *primary* signal:
by the time a message has been dropped the damage is done, and readiness exists
to shed load before that. Depth is the leading indicator; drops are the
confirmation.

They are still worth watching — as a metric, for alerting, or as a check you
write deliberately. Sample the difference between two readings, not the
cumulative total.

### Per-subscription queues

A `subscription` with `queue_size` set is a second, independent backpressure
point, and the one that catches a single slow handler rather than an overloaded
process. Those queues count their drops, but the count is not yet readable from
a configuration — there is no `subscription.<name>` to `get()` from. For now the
bus-level numbers above are what a VCL composition can see.

---

## HTTP endpoints

Three paths, wherever they are served:

| Path | Probe |
|---|---|
| `/readyz` | readiness |
| `/livez` | liveness |
| `/healthz` | liveness — the legacy Kubernetes name for the same question, and an alias for `/livez`. Giving it a third meaning would be a trap. |

| State | Code |
|---|---|
| passing | `200 OK` |
| failing | `503 Service Unavailable` |

`503`, not a 4xx: nothing is wrong with the *request*; the server is temporarily
unable to handle it, which is exactly what `503` means and what every probe tool
and load balancer expects. `Cache-Control: no-store` is set on every response,
and `HEAD` returns the status with no body.

### Bodies

Terse, the default:

```
ok
```
```
not ready
```

Verbose (`?verbose`), mirroring `kube-apiserver`'s `/readyz?verbose` so operator
habits transfer:

```
[+]process ok (for 3h12m8s)
[+]server.api ok (for 3h12m8s)
[-]client.broker failed: not connected: dial tcp 10.0.0.5:1883: connection refused (for 47s)
[+]check.database ok (for 3h12m8s)
readyz check failed
```

The trailing age is how long each component has held its verdict, so a genuine
outage reads differently from a client that is flapping: one shows a failure
minutes old, the other shows everything reset to a few seconds.

JSON (`Accept: application/json`, or `?format=json`):

```json
{"ready": false,
 "checks": [
   {"component": "process", "type": "", "ready": true, "reason": "",
    "since": "2026-03-04T08:51:22.117382Z"},
   {"component": "client.broker", "type": "mqtt", "ready": false,
    "reason": "not connected: dial tcp 10.0.0.5:1883: connection refused",
    "since": "2026-03-04T12:03:23.402881Z"}
 ]}
```

`since` is RFC 3339 here and a `time` in expressions, which is the same value:
`jsonencode(health::status(ctx))` produces exactly this.

The `checks` array is the serialization of `health::status`, so the two views
cannot drift. On an endpoint that does not allow detail the array is omitted
entirely and only the verdict is returned — negotiating your way past the
setting would defeat it.

### The standalone listener

```sh
vinculum serve --health-listen :8081 config.vcl
VINCULUM_HEALTH_LISTEN=:8081 vinculum serve config.vcl
```

Serves all three from an internal listener that needs no VCL at all and no
`server "http"` block. This is what lets a configuration consisting of nothing
but an MQTT bridge have working probes, and it is the primary answer for
deployments.

It binds **before** anything starts, so a probe arriving during boot gets an
honest `503 starting` rather than a refused connection — the difference between
a `startupProbe` that works and one that reports the pod unreachable — and it is
closed **after** everything stops, so draining answers `503` while in-flight work
finishes. A port it cannot bind is a startup error.

`--health-verbose` (`VINCULUM_HEALTH_VERBOSE`) lets it honor `?verbose`. It is
off by default: a listener that comes up whether or not you asked for it must
not also volunteer the names of every component and the text of every connection
error.

The listener is not a `server "http"` and publishes no `server.<name>`. It has no
TLS, no auth, no request logging, and no tracing; for any of those, declare a
server and use `health_endpoints` below.

### Endpoints on your own `server "http"`

```hcl
server "http" "internal" {
    listen           = ":8080"
    health_endpoints = "on"       # "off" (default) | "on" | "verbose"
}
```

`on` registers all three on that server, serving the terse body and **ignoring
`?verbose`**; `verbose` honors it. The verbose body names your components and
quotes their connection errors, which is fine on an internal listener and an
information leak on a public one, so the safe reading is the one you get without
thinking about it.

They are **off by default**, for two reasons. Turning them on silently claims
three paths: a configuration serving `handle "/"` as a reverse proxy to an
application with its own `/healthz` would find exactly those intercepted on
upgrade, with nothing changed in the config and nothing warned. And a probe
endpoint has to bypass `auth` — a kubelet cannot authenticate — so defaulting it
on would add unauthenticated surface to a server you may have deliberately
locked down.

Three rules apply wherever they are registered:

1. **An explicit route wins.** If the block declares a `handle` for one of those
   paths, nothing is registered over it. Comparison is by path alone, so
   `handle "GET /readyz"` takes the whole path rather than leaving other methods
   to Vinculum.
2. **They are not wrapped in the server's `auth`.** This is why the default body
   reveals nothing. For authenticated health, leave `health_endpoints` off and
   write a `handle` with an `auth` block.
3. **They are not logged or traced.** A probe every ten seconds across three
   endpoints would dominate both request logs and trace volume. A `handle` you
   wrote for the same path is logged and traced normally.

### Building a response yourself

```hcl
handle "GET /readyz" { action = http::readyz(ctx, ctx.request) }
handle "GET /livez"  { action = http::livez(ctx, ctx.request) }
```

`ctx` is the handler context, required, and used for the probe alone — trace
parent, deadline, baggage. **It is not where the request comes from.** The
optional second argument is explicit, and accepts either form:

| Second argument | Behavior |
|---|---|
| omitted | Terse body, no negotiation. |
| `ctx.request` | Honors `?verbose`, `?format=json`, and `Accept`, and takes the method so a `HEAD` gets the status with no body. |
| an options object | As stated: `{verbose = true}`, `{format = "json"}`. |

```hcl
# the common form — the dependency is visible at the call site
action = http::readyz(ctx, ctx.request)

# always verbose, whatever the caller asked for
action = http::readyz(ctx, {verbose = true})

# terse, always
action = http::readyz(ctx)
```

A function that reached into its `ctx` for the request would be the only one in
the language that digs a request out of a context — `http::basic_auth`,
`http::set_cookie`, and `http::response` all take explicit values — and it would
silently degrade wherever that `ctx` did not come from an HTTP handler. A
hand-written `handle` always honors `?verbose`: that is your choice to make,
unlike the built-in endpoints, where terse is a property of an unauthenticated
endpoint nobody wrote by hand.

### Kubernetes

With the published image, which sets `VINCULUM_HEALTH_LISTEN=:8081`, no health
configuration is needed at all:

```yaml
startupProbe:
  httpGet: { path: /readyz, port: 8081 }
  failureThreshold: 30
  periodSeconds: 2
readinessProbe:
  httpGet: { path: /readyz, port: 8081 }
  periodSeconds: 10
livenessProbe:
  httpGet: { path: /livez, port: 8081 }
  periodSeconds: 10
```

`startupProbe` and `readinessProbe` share a path because readiness already
reports `starting` until boot completes; there is nothing a separate startup
endpoint would add.

**Do not point `livenessProbe` at `/readyz`.** It is the single most common way
to turn a dependency outage into a fleet-wide restart loop: every replica loses
the broker, every replica fails its liveness probe, every replica restarts, and
none of them comes back any faster.

---

## Metrics and logging

### Metrics

With a metrics backend configured — a [`server "metrics"`](server-metrics.md) or
a [`client "otlp"`](client-otlp.md) — health is exported as two instruments:

| Metric | Reports |
|---|---|
| `vinculum.health.status` | Whether the process passes each probe. |
| `vinculum.health.component.status` | Whether each contributor passes. |

Both follow OpenTelemetry's convention for
[status metrics](https://opentelemetry.io/docs/specs/semconv/how-to-write-conventions/status-metrics/):
an UpDownCounter with unit `1`, carrying a `vinculum.health.state` attribute of
`passing` or `failing`, valued **1 for the state the subject is in and 0 for the
other**. That shape means a plain sum counts subjects in a state, which a
last-value gauge cannot do.

| Attribute | On | Values |
|---|---|---|
| `vinculum.health.state` | both | `passing`, `failing` |
| `vinculum.health.probe` | both | `readiness`, `liveness` |
| `vinculum.health.component` | component | `client.broker`, `check.database`, `process`, … |
| `vinculum.health.component.type` | component | the block's type label; omitted where there is none |

Scraped from Prometheus, that reads:

```
vinculum_health_status{vinculum_health_probe="readiness",vinculum_health_state="failing"} 1
vinculum_health_component_status{vinculum_health_component="client.broker",vinculum_health_component_type="mqtt",vinculum_health_probe="readiness",vinculum_health_state="failing"} 1
```

so alerting on `vinculum_health_status{vinculum_health_state="failing"} == 1`
catches the process, and the component metric says which dependency to look at.

The instruments are **observable**: their callback runs when a collector
scrapes, so the exported value is true as of that moment rather than being
whatever the last prober happened to see. A deployment that scrapes but never
probes therefore still gets accurate readiness. Cardinality is bounded by the
configuration's block count, which is static.

They are registered on **every** metrics backend, not a resolved default — the
same way [Go runtime metrics](server-metrics.md#the-default-metrics-backend)
are. Health is a property of the process rather than a metric you declared, so
it belongs in every pipeline the process has; an "is it up" series that exists
in one pipeline and not the other is a blind spot in the second.

### Logging

A probe's verdict is logged when it **changes**, never per probe — three
endpoints polled every ten seconds would otherwise bury the log in lines saying
nothing happened:

```
WARN  Process is no longer ready  {"failing": ["client.broker: dial tcp 10.0.0.5:1883: connection refused"]}
INFO  Process is now ready
```

The failing components are named, because "not ready" on its own sends the
reader to an endpoint they may not have. Readiness and liveness are logged
independently.

The first verdict read establishes a baseline rather than announcing an edge, so
a process that starts healthy and stays healthy logs nothing. Where something
probes during boot — a Kubernetes `startupProbe`, for instance — the baseline is
the `starting` state, so completing boot logs `Process is now ready` and marks
the end of startup.

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

### What is prompt, and what is sampled

A connected client **knows** the moment it loses its connection, and says so.
`client "mqtt"` and `client "rabbitmq"` report the drop from the same callback
that fires `on_disconnect`, so:

```hcl
# Fires within milliseconds of the broker going away, with no probe
# configured and nothing polling.
trigger "watch" "readiness_changed" {
    watch  = sys.ready
    action = log::warn("readiness changed", {ready = ctx.new_value})
}
```

`/readyz` stops claiming the process is ready at the same moment, rather than
serving the cached answer until it expires.

Two things remain **sampled**, and are observed when something next asks:

- **Recovery.** One component reconnecting does not make the process ready —
  others may still be down, and finding out costs a full evaluation. A recovery
  instead drops the cache, so the next reader gets the truth rather than a stale
  failure. In Kubernetes that is the next probe, typically within 10s.
- **`check` blocks.** A check is an expression, and nothing evaluates it until a
  prober arrives. A check over a `condition` that flips is invisible until then.
- **Clients that cannot push.** `redis`, `kafka`, and `sql` have no usable
  connection-state callback in their drivers, so they are answered by polling
  their `Ready` — which is what a probe does anyway.

Where prompt recovery matters, poll at a cadence you control with the
`trigger "interval"` above — an interval you can name, disable, trace, and
change, none of which a hidden ticker would have offered.

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

### What a graceful shutdown does, in order

1. **Readiness goes false.** Before anything is torn down, so a probe arriving
   during the shutdown gets an honest `503` rather than a refused connection.
2. **Listeners stop accepting** and finish the requests already in flight —
   every `server` block, and any client that holds connections. Each applies its
   own `shutdown_timeout` — see [`server "http"`](server-http.md).
3. **`trigger "shutdown"` actions run**, with the whole runtime still available
   behind the closed front door: they can publish, send, and call a client.
4. **The message pipeline empties.** Every bus dispatches what is on its
   channel, and every `queue_size` queue runs the backlog it is holding and
   finishes the action it is running.
5. **Everything stops.** Connections close, clients disconnect, timers stop.

Step 4 is what keeps a queue from being a hole in the guarantee. A
`subscription` with `queue_size = 500` can be holding five hundred messages
when the signal arrives, and they are work that was accepted; on a path where
nothing acknowledged them, there is no broker to redeliver them either. It runs
*after* step 3 so that what a shutdown trigger publishes is delivered rather
than left on a channel, and *before* step 5 because the acknowledgement for a
message the pipeline is still carrying travels over a connection that step 5
closes.

The wait is bounded by ten seconds for the phase as a whole. What is still held
when that expires is named in the log:

```text
warn  Shutting down with messages still in flight  {"messages": 412,
      "holders": ["subscription/enrich=412"]}
```

Nothing new enters the pipeline during step 4 through a *listener*, since those
closed in step 2. A client **receiver** consuming from a broker is still
consuming, though, and a message it delivers during the wait is handled
normally. One that arrives after the queues have closed is refused, which on an
acknowledging transport means the broker redelivers it on the next boot.

That is also the one shape in which the wait runs long: a pipeline saturated
enough that it never momentarily empties will use the whole ten seconds before
giving up. A pipeline that keeps up reaches zero within a sampling interval or
two, so an ordinary shutdown is not measurably slower than it was.

---

## Starting when a dependency is down

Readiness makes a degraded process *visible*. This section is about what makes
one *recover* — the other half, and the one that decides whether an operator has
to do anything.

### A component never waits for a dependency

Components are started one after another, in dependency order. A component
starts whatever connection machinery it owns and returns straight away; it does
not wait for a broker or a database to answer, and it does not fail because one
did not.

That is a contract rather than an implementation detail, and two things follow
from it:

- **Boot is bounded.** No dependency can stall the sequence, so an unreachable
  broker cannot stop an HTTP listener declared after it from binding. The
  listener comes up and serves; `/readyz` reports the broker.
- **A late dependency needs no restart.** A database that comes up thirty
  seconds after the process does is connected to on the next retry. The pod
  reports `503` for thirty seconds and then serves.

The visible cost is that **a message sent before the first connection fails**
rather than waiting for one. `trigger "start"` runs during boot, so an action
that publishes from one can run before the connection exists:

```hcl
# Wrong: runs during boot, so it races the first connection.
trigger "start" "announce" {
    action = send(ctx, client.broker.sender.out, "up", "hello")
}
```

```text
error  start trigger: action error  name=announce_wrong
       Call to function "send" failed: failed to send event:
       rabbitmq sender: not yet connected.
```

Use the client's own
[`on_connect`](client-rabbitmq.md#on_connect--on_disconnect) instead — every
client with a connection lifecycle has one. It is evaluated once the
connection is established and ready, and again after every reconnection, so the
announcement survives a broker restart as well as a slow start:

```hcl
client "rabbitmq" "broker" {
    brokers    = ["amqp://broker.example.com/"]
    on_connect = send(ctx, client.broker.sender.out, "up", "hello")

    sender "out" { exchange = "events" }
}
```

**Prefer `on_connect` to watching `sys.ready` for this.** `trigger "watch"` on
`sys.ready` looks equivalent and is not, because readiness is computed only when
something asks for it. If nothing probes `/readyz` while the process is still
connecting, the first value ever computed is `true`, that becomes the baseline,
and no transition is ever observed — so the trigger never fires. Under Kubernetes
a readinessProbe polls and it works; run the same configuration with no probe
and it silently does nothing. `on_connect` has no such dependency: the client
calls it. See [What is prompt, and what is sampled](#what-is-prompt-and-what-is-sampled).

### What stops the process instead

A failure that retrying cannot fix does not leave the process running and
permanently unready — that is a crash-loop with worse diagnostics. It is logged
naming the component, whatever already started is torn down, and the exit code
is non-zero.

Deliberately, this is a short list:

| Failure | Why it is fatal |
|---|---|
| a port already in use | Local, and no amount of waiting frees it |
| a SQL driver that is not registered | A build or plugin problem, not a connectivity one |

Everything else is treated as recoverable, **including things that look
permanent**: a refused connection, a rejected password, a queue the user may not
declare. Each of those is more often a broker mid-restart, a credential
mid-rotation, or a provisioning step that has not run yet than a permanent fact.

The asymmetry is on purpose. Guessing "terminal" wrongly turns a recoverable
outage into a crash-loop across every replica at once. Guessing "recoverable"
wrongly costs an unready pod that says exactly why, on `/readyz`, for as long as
it lasts. The second mistake is much cheaper, so the default leans that way and
only a component that is *certain* declares otherwise.

### Per-client behaviour

Every client below reports through `/readyz` while it is connecting, and
recovers without a restart.

| Client | On an unreachable dependency at startup |
|---|---|
| `mqtt`, `rabbitmq`, `vws` | Retries on the `reconnect` schedule. `max_retries` bounds recovery of a *lost* connection, never the initial one |
| `sql` | The pool is kept and `database/sql` reconnects on its own; the first ping is a diagnostic, not a gate |
| `redis` | Reconnects lazily on the next command |
| `kafka` | Retries in the background |
| `otlp` | Export failures are handled by the OTel SDK |
| `http`, `llm`, `sns`, `sqs` | Nothing to do — connections are per-request |

A dependency that should not gate traffic at all is a different question, and
has its own answer: [`readiness = false`](#opting-a-component-out).
