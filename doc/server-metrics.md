# Prometheus/OpenMetrics Metrics Server (`server "metrics"`)

A `server "metrics"` block creates an OTel `MeterProvider` bridged to a private
Prometheus registry and exposes it as an HTTP endpoint in either **standalone** mode
(owns its own listener) or **mounted** mode (acts as an `http.Handler` inside a
[`server "http"`](server-http.md) block).

All metrics — including user-defined `metric` blocks, Go runtime metrics, and
bus/client instrumentation — flow through the OTel SDK. The OTel-to-Prometheus
exporter bridge converts them to Prometheus format for scraping. This means the
same `metric` blocks also work with [`client "otlp"`](client-otlp.md) for push-based
OTLP export.

The endpoint serves both the classic Prometheus text format and the OpenMetrics format
transparently via HTTP content negotiation — Prometheus-compatible scrapers (Prometheus
server, Grafana Agent/Alloy, VictoriaMetrics, etc.) receive the format they request
automatically.

---

## Standalone Mode

The server starts its own HTTP listener and serves the metrics endpoint directly.

```hcl
server "metrics" "name" {
    listen = ":9090"      # required in standalone mode
    path   = "/metrics"   # optional, default "/metrics"

    tls {                 # optional; standalone mode only
        enabled = true
        cert    = "/etc/certs/server.crt"
        key     = "/etc/certs/server.key"
    }
}
```

## Mounted Mode

When `listen` is omitted, the block does not open a listener of its own. Instead it
implements the `HandlerServer` interface, which means it can be mounted inside a
`server "http"` block via a `handle` sub-block:

```hcl
server "metrics" "metrics" {
    # no listen — will be mounted by the http server below
}

server "http" "api" {
    listen = ":8080"

    handle "/metrics" {
        handler = server.metrics
    }
}
```

When mounted, the path is controlled by the `handle` block's route label; the
`path` attribute is ignored (a warning is emitted if set).

## No-listener Mode

A `server "metrics"` block with neither `listen` nor a `handle` mount is also valid.
The registry exists and can be used as a metrics provider for buses and VWS servers,
but the metrics will not be scraped. This may be useful during development.

---

## Attributes

| Attribute            | Type   | Required | Default      | Description                                                               |
|----------------------|--------|----------|--------------|---------------------------------------------------------------------------|
| `listen`             | string | no       | —            | If set, starts a standalone HTTP server on this address (e.g. `":9090"`)  |
| `path`               | string | no       | `"/metrics"` | Metrics endpoint path; only used in standalone mode                       |
| `default_metrics`    | bool   | no       | see below    | Whether this is the default metrics backend                               |
| `include_go_metrics` | bool   | no       | `true`       | When `false`, Go runtime metrics are not registered                       |
| `tls {}`             | block  | no       | —            | Enable HTTPS; standalone mode only. See [TLS configuration](config.md#tls)|

---

## The Default Metrics Backend

Many blocks (`bus`, `server "vws"`, `client "kafka"`, `metric`, `server "http"`,
`server "mcp"`) need a metrics backend. If none is explicitly configured on the
block, Vinculum looks for a *default* backend by searching both `server "metrics"`
and `client "otlp"` blocks using these rules:

1. If exactly one metrics-capable block exists (either type), it is automatically the default.
2. If more than one exists, the one with `default_metrics = true` is the default.
3. If more than one has `default_metrics = true`, it is a configuration error.
4. If more than one exists and none has `default_metrics = true`, there is no implicit
   default — explicit `metrics = server.<name>` or `metrics = client.<name>` wiring is required.

In the common single-server case no extra declaration is needed:

```hcl
server "metrics" "metrics" {
    listen = ":9090"
    # automatically the default because it is the only metrics backend
}

bus "main" {}           # picks up the default metrics provider automatically
server "vws" "ws" { bus = bus.main }  # same
metric "counter" "requests_total" { help = "Total requests" }  # same
```

When both Prometheus and OTLP are configured, mark one as the default:

```hcl
server "metrics" "prom" {
    listen          = ":9090"
    default_metrics = true   # metric blocks and bus instrumentation go here
}

client "otlp" "collector" {
    endpoint     = "http://localhost:4318"
    service_name = "my-app"
    default      = true      # tracing default (unchanged)
    # default_metrics not set — not the metrics default
}
```

---

## Behaviour

- On initialisation the block creates an OTel `MeterProvider` bridged to a private
  `prometheus.Registry` via the OTel-to-Prometheus exporter. All metrics flow through
  the OTel SDK, ensuring they work identically with both Prometheus scraping and OTLP push.
- Go runtime metrics (`go_goroutine_count`, `go_memory_used_bytes`, etc.) are registered
  via OTel runtime instrumentation unless `include_go_metrics = false` is set.
- In standalone mode, HTTP server metrics (`http_server_request_duration_seconds`, etc.)
  are automatically produced via `otelhttp`.
- The server is available in VCL expressions as `server.<name>` and can be passed
  wherever a metrics backend is accepted (e.g. `metrics = server.metrics`).

---

## Wiring to Buses, VWS Servers, and Kafka Clients

The `metrics` attribute is supported on `bus`, `server "vws"`, and
`client "kafka"` blocks. It is optional — when omitted the default metrics
server is used automatically.

```hcl
bus "main" {
    metrics = server.metrics   # optional; inferred from default otherwise
}

server "vws" "ws" {
    bus     = bus.main
    metrics = server.metrics   # optional; inferred from default otherwise
}

client "kafka" "events" {
    brokers = ["broker:9092"]
    metrics = server.metrics   # optional; inferred from default otherwise
    ...
}
```

When `metrics` is set on a bus, the following internal metrics are exposed:

| Metric | Type | Description |
|--------|------|-------------|
| `eventbus_messages_published_total` | counter | Messages published |
| `eventbus_messages_published_sync_total` | counter | Synchronous publishes |
| `eventbus_subscriptions_total` | counter | Subscriptions created |
| `eventbus_unsubscriptions_total` | counter | Subscriptions removed |
| `eventbus_errors_total` | counter | Bus errors |
| `eventbus_publish_duration_seconds` | histogram | Publish latency |
| `eventbus_active_subscribers` | gauge | Current active subscribers |

The `bus` label identifies the bus by name on all metrics.

When `metrics` is set on a `server "vws"`, WebSocket connection metrics (connection
duration, active connections, message counts, etc.) are also exposed.

`client "kafka"` picks up the default metrics provider automatically and exposes
producer and consumer metrics — see [Kafka client observability](client-kafka.md#observability).
Explicit `metrics = server.<name>` wiring is also supported on all three block types.

---

## Examples

### Standalone metrics server (minimal)

```hcl
server "metrics" "metrics" {
    listen = ":9090"
}

bus "main" {}
```

### Standalone metrics server with TLS

```hcl
server "metrics" "metrics" {
    listen = ":9090"

    tls {
        enabled = true
        cert    = "/etc/certs/server.crt"
        key     = "/etc/certs/server.key"
    }
}
```

For development, `self_signed = true` generates an ephemeral certificate automatically — see [TLS configuration](config.md#tls).

### Metrics mounted under an existing HTTP server

```hcl
server "metrics" "metrics" {}

server "http" "api" {
    listen = ":8080"

    handle "/webhook" {
        action = [
            send(ctx, bus.main, "webhooks/${ctx.method}", ctx.body),
            http_response(http_status.OK, {status = "ok"}),
        ]
    }

    handle "/metrics" {
        handler = server.metrics
    }
}

bus "main" {}
```

### Multiple registries, explicit wiring

When two or more `server "metrics"` blocks are present, mark one `default_metrics = true` so
that blocks with no explicit `metrics =` attribute know which registry to use.
Blocks that need a different registry set `metrics` explicitly.

```hcl
# :9090 — bus and application metrics (default for everything without explicit wiring)
server "metrics" "app" {
    listen  = ":9090"
    default_metrics = true
}

# :9091 — Kafka-only metrics, isolated from the bus registry
server "metrics" "kafka" {
    listen = ":9091"
}

bus "main" {}   # picks up server.app automatically (it is the default)

client "kafka" "events" {
    brokers = ["broker:9092"]
    metrics = server.kafka   # explicit: goes to the kafka-only registry
    ...
}
```

Scraping `:9090` will show only `eventbus_*` (and Go runtime) metrics.
Scraping `:9091` will show only `kafka_consumer_*` and `kafka_producer_*`
metrics. Each registry is completely isolated.

---

## Standard Go Runtime Metrics

OTel runtime instrumentation is registered automatically on every `server "metrics"`
block unless `include_go_metrics = false` is set. The following metrics are produced
(shown in Prometheus format with dots converted to underscores):

| Metric                      | Unit          | Description                                    |
|-----------------------------|---------------|------------------------------------------------|
| `go_goroutine_count`        | {goroutine}   | Count of live goroutines                       |
| `go_memory_used_bytes`      | By            | Memory used by the Go runtime                  |
| `go_memory_limit_bytes`     | By            | Go runtime memory limit (if configured)        |
| `go_memory_allocated_bytes` | By            | Memory allocated to the heap                   |
| `go_memory_allocations`     | {allocation}  | Count of heap allocations                      |
| `go_memory_gc_goal_bytes`   | By            | Heap size target for end of GC cycle           |
| `go_processor_limit`        | {thread}      | OS threads for user-level Go code              |
| `go_config_gogc`            | %             | GOGC percentage                                |

---

## Authentication

Add an `auth` sub-block to require authentication before serving the metrics endpoint.
This is useful when the metrics server is exposed externally (e.g. to a Prometheus scraper
over the internet) and should not be publicly accessible.

```hcl
server "metrics" "metrics" {
    listen = ":9090"

    auth "basic" {
        credentials = { prometheus = env.SCRAPE_PASSWORD }
    }
}
```

Any auth mode is supported. See [Authentication](server-auth.md) for the full reference.
