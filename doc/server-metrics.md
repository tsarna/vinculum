# Prometheus/OpenMetrics Metrics Server (`server "metrics"`)

A `server "metrics"` block creates a private Prometheus registry and exposes it as
an HTTP endpoint in either **standalone** mode (owns its own listener) or **mounted**
mode (acts as an `http.Handler` inside a [`server "http"`](server-http.md) block).

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

| Attribute            | Type   | Required | Default      | Description |
|----------------------|--------|----------|--------------|-------------|
| `listen`             | string | no       | —            | If set, starts a standalone HTTP server on this address (e.g. `":9090"`) |
| `path`               | string | no       | `"/metrics"` | Metrics endpoint path; only used in standalone mode |
| `default`            | bool   | no       | see below    | Whether this is the default metrics server |
| `include_go_metrics` | bool   | no       | `true`       | When `false`, the Go runtime (`go_*`) and process (`process_*`) collectors are not registered |

---

## The Default Metrics Server

Many blocks (`bus`, `server "vws"`, `client "kafka"`, `metric`) need a metrics provider. If none is
explicitly configured on the block, Vinculum looks for a *default* metrics server
using these rules:

1. If exactly one `server "metrics"` is defined, it is automatically the default.
2. If more than one is defined, the one with `default = true` is the default.
3. If more than one has `default = true`, it is a configuration error.
4. If more than one is defined and none has `default = true`, there is no implicit
   default — explicit `metrics = server.<name>` wiring is required.

In the common single-server case no extra declaration is needed:

```hcl
server "metrics" "metrics" {
    listen = ":9090"
    # automatically the default because it is the only metrics server
}

bus "main" {}           # picks up the default metrics provider automatically
server "vws" "ws" { bus = bus.main }  # same
metric "counter" "requests_total" { help = "Total requests" }  # same
```

---

## Behaviour

- On initialisation the block creates a **private** `prometheus.Registry` (not the
  global default registry, so unrelated Go library metrics are excluded).
- A Go runtime collector (`go_goroutines`, `go_memstats_*`, etc.) and a process
  collector (`process_cpu_seconds_total`, `process_open_fds`, etc.) are registered
  automatically unless `include_go_metrics = false` is set.
- The server is available in VCL expressions as `server.<name>` and can be passed
  wherever a metrics provider is accepted (e.g. `metrics = server.metrics`).

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

### Metrics mounted under an existing HTTP server

```hcl
server "metrics" "metrics" {}

server "http" "api" {
    listen = ":8080"

    handle "/webhook" {
        action = [
            send(ctx, bus.main, "webhooks/${ctx.method}", ctx.body),
            respond(httpstatus.OK, {status = "ok"}),
        ]
    }

    handle "/metrics" {
        handler = server.metrics
    }
}

bus "main" {}
```

### Multiple registries, explicit wiring

When two or more `server "metrics"` blocks are present, mark one `default = true` so
that blocks with no explicit `metrics =` attribute know which registry to use.
Blocks that need a different registry set `metrics` explicitly.

```hcl
# :9090 — bus and application metrics (default for everything without explicit wiring)
server "metrics" "app" {
    listen  = ":9090"
    default = true
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

## Standard Go and Process Metrics

The following collectors are registered automatically on every `server "metrics"` block
unless `include_go_metrics = false` is set.

### Go Runtime (`go_*`)

`go_goroutines`, `go_threads`, `go_gc_duration_seconds`, `go_info`,
`go_memstats_alloc_bytes`, `go_memstats_heap_alloc_bytes`, and many more.

### Process (`process_*`)

`process_cpu_seconds_total`, `process_open_fds`, `process_max_fds`,
`process_virtual_memory_bytes`, `process_resident_memory_bytes`,
`process_start_time_seconds`.

> Note: Process metrics are Linux-only. On macOS/Windows they are partially or
> fully unavailable; the collector degrades gracefully.

---

## Future Attributes (not yet implemented)

- `tls {}` sub-block for TLS (standalone mode only).
- `basic_auth {}` sub-block for scrape authentication.
