# OTLP Client (`client "otlp"`)

Vinculum can export distributed traces and metrics to any
[OpenTelemetry](https://opentelemetry.io/) collector using the OTLP/HTTP
protocol. Declare a `client "otlp"` block to configure the exporter and wire
it to servers that produce spans and metrics.

---

## `client "otlp" "<name>"`

```hcl
client "otlp" "tracer" {
    # OTLP/HTTP endpoint — the base URL of the collector
    endpoint = "http://localhost:4318"

    # Identifies this service in traces
    service_name    = "my-app"
    service_version = "1.0.0"   # optional, default ""

    # Fraction of root spans to sample (0.0–1.0). Incoming traces that
    # already carry a trace context are always continued regardless of
    # this ratio.
    sampling_ratio = 1.0        # optional, default 1.0 (sample everything)

    # When true this client is used automatically by servers that do not
    # specify an explicit tracing = attribute. If there is only one
    # client "otlp" block it is always the default regardless of this flag.
    default = false             # optional

    # Copy these baggage entries onto every locally-started span as
    # attributes named exactly these keys. See doc/baggage.md.
    record_baggage = ["tenant_id", "user_id"]   # optional, default []

    # --- Metrics ---

    # Separate endpoint for metric export (defaults to endpoint).
    metric_endpoint = "http://localhost:4318"  # optional

    # Push interval for periodic metric export.
    metric_interval = "60s"     # optional, default "60s"

    # Include Go runtime metrics (goroutines, memory, GC, etc.).
    include_go_metrics = true   # optional, default true

    # When true this client is used as the default metrics backend.
    # Controls which backend metric blocks, buses, and servers wire to.
    default_metrics = false     # optional

    # Optional HTTP headers added to every export request (e.g. auth tokens).
    headers = {
        Authorization = "Bearer ${env.OTEL_API_KEY}"
    }

    # Optional TLS configuration for the collector connection.
    tls {
        ca_cert              = "/etc/certs/ca.crt"
        cert                 = "/etc/certs/client.crt"  # optional, mTLS
        key                  = "/etc/certs/client.key"  # optional, mTLS
        insecure_skip_verify = false                     # default: false
    }
}
```

### Attributes

`disabled` skips the client entirely — no exporters, not registered, not
auto-wired — and required attributes are not validated, so the block can hold
placeholders. It is often driven by an env var, e.g.
`disabled = try(env.OTEL_EXPORTER_OTLP_ENDPOINT, "") == ""`. See
[Projecting baggage onto spans](baggage.md#projecting-baggage-onto-spans) for
what `record_baggage` does, and [TLS configuration](config.md#tls) for the `tls`
block.

<!-- vinculum:begin block-attrs client otlp level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `endpoint` | string (url) | yes |  | OTLP collector endpoint to export traces to. |
| `service_name` | string | yes |  | Value of the `service.name` resource attribute. |
| `default` | bool |  |  | Make this the default tracing backend. |
| `default_metrics` | bool |  |  | Make this the default metrics backend. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `headers` | expression |  |  | Headers sent with each export request. |
| `include_go_metrics` | bool |  | `true` | Export Go runtime metrics. |
| `metric_endpoint` | string (url) |  |  | Separate endpoint for metrics. |
| `metric_interval` | string (duration) |  | `60s` | How often metrics are pushed to the collector. |
| `record_baggage` | list |  | `[]` | Baggage keys to copy onto each span as attributes. |
| `sampling_ratio` | number |  | `1.0` | Fraction of traces to sample, from 0 to 1. |
| `service_version` | string |  |  | Value of the `service.version` resource attribute. |

**`service_name`**

This is how the exported telemetry identifies this process.

**`default`**

Blocks that emit traces without naming one use the default. A single otlp client is the default automatically.

**`default_metrics`**

The same rule as `default`, but for metrics: at most one backend — this or a `server "metrics"` — may claim it.

**`disabled`**

The block is parsed and validated, but nothing is created from it.

**`headers`**

Typically an API key for a hosted collector.

**`include_go_metrics`**

Set false to omit goroutine, memory, and GC metrics.

**`metric_endpoint`**

`endpoint` is used when omitted. Setting either this or `metric_interval` is what enables metric export at all.

**`record_baggage`**

Nothing is copied when omitted, since baggage can carry data that should not reach a trace backend.

**`sampling_ratio`**

Head-based, and applied when a root span starts — a span with a sampled parent is kept regardless.

### Blocks

- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs client otlp -->

---

## How it works

On `Start()` the client:

1. Creates an OTLP/HTTP trace exporter and a `TracerProvider` configured with
   the given sampling ratio and a `ParentBased` sampler — incoming requests
   that carry a W3C `traceparent` header are always continued as child spans;
   requests without one create a new root span sampled at `sampling_ratio`.
2. Creates an OTLP/HTTP metric exporter with a `PeriodicReader` at
   `metric_interval` and a `MeterProvider`. If `include_go_metrics` is true
   (the default), Go runtime metrics are registered automatically.
3. Sets the OpenTelemetry global `TracerProvider` and `MeterProvider`, and
   configures W3C TraceContext + Baggage propagation globally.
4. On `Stop()`, flushes all pending metrics and spans before the process exits.

---

## Auto-wiring

### Tracing

The following components accept an optional `tracing = client.<name>` attribute
to select a specific OTLP client for tracing:

| Component                       | `tracing =` attribute | Auto-wires |
|---------------------------------|-----------------------|------------|
| `server "http"`                 | yes                   | yes        |
| `server "mcp"` (standalone)    | yes                   | yes        |
| `server "metrics"` (standalone) | yes                   | yes        |
| `client "http"`                 | yes                   | yes        |
| `client "kafka"`                | yes                   | yes        |
| `client "mqtt"`                 | yes                   | yes        |
| `client "openai"`               | yes                   | yes        |
| all `trigger` types             | yes                   | yes        |

### Metrics

When `default_metrics = true` is set, the OTLP client also serves as a metrics
backend. The following components accept an optional `metrics =` attribute
pointing to either a `server "metrics"` or `client "otlp"`:

| Component                       | `metrics =` attribute | Auto-wires |
|---------------------------------|-----------------------|------------|
| `server "http"`                 | yes                   | yes        |
| `server "mcp"` (standalone)    | yes                   | yes        |
| `bus`                           | yes                   | yes        |
| `server "vws"`                  | yes                   | yes        |
| `client "http"`                 | yes                   | yes        |
| `client "kafka"`                | yes                   | yes        |
| `client "mqtt"`                 | yes                   | yes        |
| `metric` blocks                 | `server =`            | yes        |

```hcl
server "http" "api" {
    listen  = ":8080"
    tracing = client.tracer
    ...
}

client "kafka" "events" {
    tracing = client.tracer
    ...
}

trigger "interval" "heartbeat" {
    tracing = client.tracer
    delay   = "30s"
    action  = send(bus.events, "ping")
}
```

If there is **exactly one** `client "otlp"` block, or one marked
`default = true`, components that support `tracing =` and omit it are wired to
it automatically. With multiple clients and no default, each component must
specify `tracing =` explicitly.

---

## Trace context in VCL expressions

When an action expression is evaluated inside an active OTel span, two variables
are available in the `ctx` object. This applies to all contexts: HTTP handler
actions, trigger actions, Kafka/MQTT receiver actions, and subscription actions.

| Variable | Type | Description |
|----------|------|-------------|
| `ctx.trace_id` | `string` | W3C trace ID (32 hex chars), or `""` if no span is active |
| `ctx.span_id` | `string` | W3C span ID (16 hex chars), or `""` if no span is active |

Example — include the trace ID in a response header or log entry:

```hcl
handle "/api/data" {
    action = http::response(200, {
        "X-Trace-Id" = ctx.trace_id
    }, fetch_data())
}
```

---

## Example

Minimal setup with Jaeger running locally (all-in-one image):

```hcl
client "otlp" "jaeger" {
    endpoint     = "http://localhost:4318"
    service_name = "my-service"
}

server "http" "api" {
    listen = ":8080"

    handle "/hello" {
        action = "Hello!"
    }
}
```

Because there is only one `client "otlp"`, the HTTP server auto-wires to it.
Every inbound request produces an OTel span that appears in the Jaeger UI.
