# Metric Blocks

The `metric` block declares an application-level Prometheus/OpenMetrics metric.
Declared metrics are registered in a [`server "metrics"`](server-metrics.md)
registry and are then accessible in VCL expressions as `metric.<name>`.

---

## Supported Types

| Type | Description | VCL operations |
|------|-------------|----------------|
| `gauge` | Freely increasing/decreasing value | `set`, `get`, `increment` |
| `counter` | Monotonically increasing value | `increment`, `get` |
| `histogram` | Sample observations into configurable buckets | `observe` |

---

## Gauge

```hcl
metric "gauge" "active_jobs" {
    help        = "Number of currently active jobs"
    label_names = ["queue", "priority"]   # optional
    namespace   = "myapp"                 # optional
}
```

## Counter

```hcl
metric "counter" "jobs_processed_total" {
    help        = "Total number of jobs processed"
    label_names = ["queue", "result"]     # optional
}
```

Counter names should follow the Prometheus convention of ending in `_total`.

## Histogram

```hcl
metric "histogram" "job_duration_seconds" {
    help    = "Job processing duration in seconds"
    buckets = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0]
              # optional; default: Prometheus default bucket set
}
```

---

## Common Attributes

| Attribute | Type | Required | Description |
|-----------|------|----------|-------------|
| `help` | string | **yes** | Human-readable description shown in `/metrics` output |
| `label_names` | list(string) | no | Names of dynamic labels; if omitted, the metric has no labels |
| `namespace` | string | no | Prefix prepended as `<namespace>_<name>` |
| `buckets` | list(number) | no | Histogram bucket boundaries (histograms only) |
| `server` | expression | no | Reference to a specific `server "metrics"` block |

If `server` is omitted and exactly one `server "metrics"` block is defined, that
server is used automatically. See [server-metrics.md#default](server-metrics.md#the-default-metrics-server)
for the full default-resolution rules.

---

## VCL Integration

Once declared, a metric is available in any expression as `metric.<name>`.

### No-label Metrics

```hcl
metric "gauge" "temperature" { help = "Current temperature" }
metric "counter" "readings_total" { help = "Total readings" }
metric "histogram" "read_latency" { help = "Read latency in seconds" }

subscription "reader" {
    target = bus.main
    topics = ["sensors/#"]
    action = [
        set(metric.temperature, ctx.msg.value),
        increment(metric.readings_total, 1),
        observe(metric.read_latency, ctx.msg.elapsed),
    ]
}
```

### Labeled Metrics

When a metric has `label_names`, a label object must be supplied as the last
argument to each recording function. The object keys must exactly match
`label_names` — missing or extra keys are a runtime error.

```hcl
metric "gauge" "active_jobs" {
    help        = "Active jobs"
    label_names = ["queue"]
}

metric "counter" "jobs_processed_total" {
    help        = "Jobs processed"
    label_names = ["queue", "result"]
}

metric "histogram" "job_duration_seconds" {
    help        = "Job processing duration"
    label_names = ["queue"]
    buckets     = [0.01, 0.05, 0.1, 0.5, 1.0, 5.0]
}

subscription "jobs_started" {
    target = bus.main
    topics = ["jobs/started/#"]
    action = [
        increment(metric.active_jobs, 1, {queue = ctx.msg.queue}),
    ]
}

subscription "jobs_completed" {
    target = bus.main
    topics = ["jobs/completed/#"]
    action = [
        increment(metric.jobs_processed_total, 1,
            {queue = ctx.msg.queue, result = "success"}),
        observe(metric.job_duration_seconds, ctx.msg.elapsed,
            {queue = ctx.msg.queue}),
        increment(metric.active_jobs, -1, {queue = ctx.msg.queue}),
    ]
}
```

### Function Reference for Metrics

| Function | Metric types | Labels | Notes |
|----------|-------------|--------|-------|
| `set(metric, value)` | gauge | — | Sets gauge to `value` (no-label series) |
| `set(metric, value, {k=v...})` | gauge | required | Sets labeled gauge series |
| `increment(metric, delta)` | gauge, counter | — | Adds `delta` (no-label series) |
| `increment(metric, delta, {k=v...})` | gauge, counter | required | Adds `delta` to labeled series; counter requires `delta >= 0` |
| `get(metric)` | gauge, counter | — | Returns current value (no-label series) |
| `get(metric, {k=v...})` | gauge, counter | required | Returns current value of labeled series |
| `observe(metric, value)` | histogram | — | Records one observation (no-label series) |
| `observe(metric, value, {k=v...})` | histogram | required | Records one labeled observation |

The same `get`, `set`, `increment` functions work on both `var` variables and
`metric` values; dispatch is based on the type of the first argument. `observe`
is metric-only.

#### Type Safety

- Calling `set` on a counter is a runtime error (counters are write-once-upward).
- Calling `observe` on a gauge or counter is a runtime error.
- Calling `get` on a histogram is a runtime error.
- Supplying a label object with missing or extra keys is a runtime error.

---

## Complete Example

```hcl
server "metrics" "metrics" {
    listen = ":9090"
}

bus "main" {}

metric "gauge" "active_connections" {
    help        = "Currently active WebSocket connections"
    label_names = ["region"]
}

metric "counter" "messages_received_total" {
    help        = "Total messages received from WebSocket clients"
    label_names = ["region", "topic_prefix"]
}

metric "histogram" "message_size_bytes" {
    help        = "Size of received messages"
    label_names = ["region"]
    buckets     = [64, 256, 1024, 4096, 16384]
}

subscription "ws_events" {
    target = bus.main
    topics = ["ws/#"]
    action = [
        increment(metric.messages_received_total, 1,
            {region = ctx.msg.region, topic_prefix = ctx.msg.prefix}),
        observe(metric.message_size_bytes, ctx.msg.size,
            {region = ctx.msg.region}),
    ]
}
```
