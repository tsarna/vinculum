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

| Attribute | Type   | Required | Default      | Description |
|-----------|--------|----------|--------------|-------------|
| `listen`  | string | no       | —            | If set, starts a standalone HTTP server on this address (e.g. `":9090"`) |
| `path`    | string | no       | `"/metrics"` | Metrics endpoint path; only used in standalone mode |
| `default` | bool   | no       | see below    | Whether this is the default metrics server |

---

## The Default Metrics Server

Many blocks (`bus`, `server "vws"`, `metric`) need a metrics provider. If none is
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
  automatically.
- The server is available in VCL expressions as `server.<name>` and can be passed
  wherever a metrics provider is accepted (e.g. `metrics = server.metrics`).

---

## Wiring to Buses and VWS Servers

```hcl
bus "main" {
    metrics = server.metrics   # optional; inferred from default otherwise
}

server "vws" "ws" {
    bus     = bus.main
    metrics = server.metrics   # optional; inferred from default otherwise
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

```hcl
server "metrics" "app" {
    listen  = ":9090"
    default = true
}

server "metrics" "infra" {
    listen = ":9091"
}

bus "main" {
    metrics = server.app
}

bus "infra" {
    metrics = server.infra
}
```

---

## Standard Go and Process Metrics

The following collectors are registered automatically on every `server "metrics"` block.

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

- `include_go_metrics = false` — disable Go runtime and process collectors.
- `tls {}` sub-block for TLS (standalone mode only).
- `basic_auth {}` sub-block for scrape authentication.
