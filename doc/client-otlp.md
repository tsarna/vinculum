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

| Attribute            | Required | Description                                                                  |
|----------------------|----------|------------------------------------------------------------------------------|
| `endpoint`           | yes      | Base URL of the OTLP/HTTP collector (e.g. `"http://localhost:4318"`)         |
| `service_name`       | yes      | Service name recorded on every span and metric                               |
| `service_version`    | no       | Service version recorded on every span and metric                            |
| `sampling_ratio`     | no       | Head-based sampling ratio for new root spans (default `1.0`)                 |
| `default`            | no       | Mark as the default tracing backend for auto-wiring                          |
| `metric_endpoint`    | no       | Separate endpoint for metric export (defaults to `endpoint`)                 |
| `metric_interval`    | no       | Push interval for periodic metric export (default `"60s"`)                   |
| `include_go_metrics` | no       | Include Go runtime metrics (default `true`)                                  |
| `default_metrics`    | no       | Mark as the default metrics backend for auto-wiring                          |
| `headers`            | no       | `map(string)` of HTTP headers added to export requests                       |
| `tls`                | no       | TLS configuration block; see [TLS configuration](config.md#tls)              |

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
    action = http_response(200, {
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
