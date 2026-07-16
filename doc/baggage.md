# Baggage (`ctx.baggage`)

[OpenTelemetry Baggage](https://opentelemetry.io/docs/specs/otel/baggage/) is a
[W3C](https://www.w3.org/TR/baggage/) mechanism for propagating arbitrary
key/value pairs alongside a trace through a distributed system. Entries ride the
`baggage` HTTP header (and its equivalents on other transports), flow through
intermediate services, and are available to your config as `ctx.baggage`.

Vinculum extracts baggage from inbound requests and injects it into outbound
ones automatically — `ctx.baggage` lets your config **read** what arrived and
**add, change, or remove** entries so that downstream `send()`, `http_*()`, and
publish calls carry them.

`ctx.baggage` is available in every context that exposes `ctx`: HTTP and MCP
handler actions, subscription actions, trigger actions, client receive handlers,
`on_connect` / `on_disconnect`, and so on.

---

## Reading

Reading uses the generic [`get()`](functions.md) function:

```hcl
get(ctx.baggage)               # map(string) of all entries ({} if none)
get(ctx.baggage, key)          # string, or null if the key is absent
get(ctx.baggage, key, default) # string, or default if the key is absent
```

Keys are plain strings, so keys containing `-`, `.`, etc. need no special
syntax:

```hcl
handle "/api/data" {
    action = log::info("received request", {
        tenant  = get(ctx.baggage, "tenant_id"),
        user    = get(ctx.baggage, "user_id", "anonymous"),
        feature = get(ctx.baggage, "feature.flag.new-ui"),
    })
}
```

`length(ctx.baggage)` returns the entry count without building a map, and
`tostring(ctx.baggage)` returns the W3C header encoding
(`tenant_id=acme,user_id=42`) — handy for log lines.

> **Note.** Dot/index access (`ctx.baggage.tenant_id`, `ctx.baggage["k"]`) is
> **not** available — use `get(ctx.baggage, "tenant_id")`. This keeps reads lazy
> (the full map is materialized only by the one-argument `get`) and sidesteps
> the fact that most baggage keys are not valid identifiers.

---

## Writing

Writing uses the generic [`set()`](functions.md), [`delete()`](functions.md),
and [`clear()`](functions.md) functions:

```hcl
set(ctx.baggage, key, value)   # add or overwrite one entry; returns value
set(ctx.baggage, key, null)    # remove one entry; returns null
set(ctx.baggage, {k = v, ...}) # merge a map of entries; returns the merged map
delete(ctx.baggage, key)       # remove one entry
delete(ctx.baggage)            # remove all entries
clear(ctx.baggage)             # remove all entries
```

Map-merge semantics match the rest of VCL: keys present in the argument are
added or overwritten, keys absent are left alone, and a `null` value removes a
key.

Baggage values are always strings; passing a non-string, non-null value is an
evaluation error (matching the rest of VCL's strict typing). Keys and values are
validated, so an entry the [W3C spec](https://www.w3.org/TR/baggage/) rejects
surfaces as an evaluation error.

### Propagation

A write derives a new context and stores it back on the same context the handler
already threads through `ctx`. Every later side-effect function in the same
`action = [...]` list — `send()`, `http::post()`, a Kafka/MQTT publish — re-reads
that context when it runs and injects the current baggage into its outbound
headers. So a `set()` early in a list is visible to everything after it, with no
extra wiring:

```hcl
subscription "route_tenant" {
    target = bus.raw
    topics = ["ingest/#"]
    action = [
        set(ctx.baggage, "tenant_id", ctx.msg.tenant),
        send(ctx, bus.enriched, ctx.topic, ctx.msg),  # carries tenant_id
    ]
}
```

The mutation is **scoped to the current handler invocation** — each invocation
threads its own context, so a change made in one does not bleed into unrelated
concurrent invocations.

### Clearing at a trust boundary

`clear(ctx.baggage)` (or `delete(ctx.baggage)`) drops every entry — useful when
forwarding into a separate trust boundary where inbound baggage should not leak:

```hcl
handle "/public/echo" {
    action = [
        clear(ctx.baggage),                  # drop inbound baggage
        set(ctx.baggage, "origin", "public-edge"),
        send(ctx, bus.internal, "echo", get(ctx.request, "body")),
    ]
}
```

To remove specific keys instead, use `delete(ctx.baggage, key)` or
`set(ctx.baggage, key, null)`.

---

## Server-side trust filtering

Incoming baggage is **untrusted input**. By default — with no `baggage {}` block,
or an empty one — a server **strips all inbound baggage** before it reaches
`ctx.baggage` or is re-propagated downstream. This is the secure default: a
server that configures nothing trusts nothing.

Two things are deliberately *not* affected:

- **Trace propagation.** `traceparent` is never filtered, so distributed traces
  stitch together regardless of baggage policy.
- **Baggage your config sets.** `set(ctx.baggage, …)` is your own configuration,
  not untrusted input, so it always propagates outbound.

A server opts into trusting upstream baggage with a `baggage {}` sub-block:

```hcl
server "http" "api" {
    listen = ":8080"

    baggage {
        # Trust only these keys from inbound headers; drop all others.
        allow = ["tenant_id", "user_id", "request_origin"]

        # Alternatives (mutually exclusive with allow):
        # deny        = ["internal.", "debug."]  # trust all but these prefixes
        # passthrough = true                      # trust all inbound baggage
    }

    handle "/api/data" { action = "ok" }
}
```

### Attributes

| Attribute     | Type           | Description |
|---------------|----------------|-------------|
| `passthrough` | bool           | Trust **all** inbound baggage. Mutually exclusive with `allow`/`deny`. |
| `allow`       | `list(string)` | Trust only these exact keys; drop all others. |
| `deny`        | `list(string)` | Drop keys matching any of these prefixes; trust all others. |
| `max_entries` | number         | Cap on total entries (default 64), applied within `allow`/`deny`. Surplus dropped. |
| `max_bytes`   | number         | Cap on total serialized size in bytes (default 8192), applied within `allow`/`deny`. Surplus dropped. |

`passthrough`, `allow`, and `deny` are mutually exclusive per block. Omitting the
block (or an empty block) strips all inbound baggage. `passthrough` skips the
size caps; `allow`/`deny` enforce them. Surplus entries beyond the caps are
dropped silently with a debug log.

The filter runs immediately after the inbound baggage is extracted but before
action evaluation begins, so dropped entries never appear in `ctx.baggage` and
are not re-injected on outbound calls made during that handler. It is supported
on these inbound surfaces, each taking the same `baggage {}` block:

| Surface                                               | Granularity                                                   |
|-------------------------------------------------------|---------------------------------------------------------------|
| [`server "http"`](server-http.md)                     | per server                                                    |
| [`server "mcp"`](server-mcp.md)                       | per server (mounted under HTTP inherits that server's filter) |
| [`client "kafka"`](client-kafka.md) `receiver`        | per receiver                                                  |
| [`client "rabbitmq"`](client-rabbitmq.md) `receiver`  | per receiver                                                  |
| [`client "mqtt"`](client-mqtt.md) `receiver`          | per receiver                                                  |
| [`client "sqs_receiver"`](client-sqs.md)              | per client                                                    |
| [`client "redis_stream"`](client-redis.md) `consumer` | per consumer                                                  |

Transports without a header/metadata mechanism cannot carry baggage at all, so
there is nothing to filter and no `baggage {}` block applies — notably
[`client "redis_pubsub"`](client-redis.md) (Redis pub/sub has no headers).

A public edge needs **no block at all** to be safe — the default already strips
everything. Add a block only to *loosen* the default for trusted peers:

```hcl
server "http" "public" {
    listen = ":443"

    baggage {
        allow = ["request_id"]   # trust only this; default strips the rest
    }

    handle "/api/*" {
        action = http_forward("http://internal-api${ctx.request.url.path}")
    }
}
```

---

## Projecting baggage onto spans

To make request-scoped values searchable in the trace UI, copy named baggage
entries onto every span as attributes with `record_baggage` on
[`client "otlp"`](client-otlp.md):

```hcl
client "otlp" "tracer" {
    endpoint     = "http://localhost:4318"
    service_name = "my-app"

    # Copy these baggage entries onto every locally-started span as
    # attributes named exactly these keys.
    record_baggage = ["tenant_id", "user_id", "request_origin"]
}
```

Every span produced by Vinculum (HTTP handler, outbound HTTP call, publish,
trigger execution, …) gains those attributes when the keys are present in the
current baggage. Keys not present produce no attribute. Defaults to disabled.

---

## Notes

- Baggage entry **properties** (the rarely-used `;key=value` metadata tail on an
  entry) are preserved on pass-through but are not readable or writable from VCL.
- `ctx.baggage` is only present where `ctx` is. In an eval context with no `ctx`,
  referencing it is a normal "unsupported attribute" error.
