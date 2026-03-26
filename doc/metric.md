# Metric Blocks

The `metric` block declares an application-level Prometheus/OpenMetrics metric.
Declared metrics are registered in a [`server "metrics"`](server-metrics.md)
registry and are then accessible in VCL expressions as `metric.<name>`.

---

## Supported Types

| Type | Description | VCL operations |
|------|-------------|----------------|
| `gauge` | Freely increasing/decreasing value | `set`, `get`, `increment` |
| `counter` | Monotonically increasing value | `increment`, `get`, `set` (delta) |
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
| `value` | expression | no | Expression evaluated at each Prometheus scrape (see [Computed Metrics](#computed-metrics)) |

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
| `set(metric, value)` | gauge, counter | — | Sets gauge to `value`; for counters, only a positive difference is applied (delta semantics) |
| `set(metric, value, {k=v...})` | gauge, counter | required | Sets labeled series; same delta semantics for counters |
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

- Calling `set` on a counter applies delta semantics: only positive differences are forwarded to Prometheus (the counter never decreases). If the supplied value is less than the last set value, the call is a no-op.
- Calling `observe` on a gauge or counter is a runtime error.
- Calling `get` on a histogram is a runtime error.
- Supplying a label object with missing or extra keys is a runtime error.
- Calling `set` or `increment` on a computed metric (one with `value = expression`) is a runtime error.

---

## Computed Metrics

Adding `value = <expression>` to a metric block makes it **computed**: the expression
is evaluated automatically each time Prometheus scrapes the `/metrics` endpoint.

```hcl
metric "gauge" "queue_depth" {
    help  = "Current depth of the work queue"
    value = get(var.queue_depth)
}

metric "counter" "events_total" {
    help  = "Total events processed (tracked via variable)"
    value = get(var.events_processed)
}

metric "histogram" "sampled_latency_seconds" {
    help  = "Periodically sampled latency"
    value = get(var.latest_latency_seconds)
}
```

### Per-type behaviour

| Type | What the expression computes | Prometheus behaviour |
|------|------------------------------|---------------------|
| `gauge` | Current gauge value | Reported as-is at each scrape |
| `counter` | Current total value | Reported as raw counter value; Prometheus detects resets automatically (e.g. after a reboot) |
| `histogram` | A single observation value | One observation is recorded per scrape |

### Constraints

- `label_names` and `value` cannot be combined — computed metrics always operate on the no-label series.
- Calling `set()` or `increment()` on a computed metric is a runtime error.
- `get()` on a computed gauge or counter returns the value from the most recent scrape (0 before the first scrape).
- `observe()` on a computed histogram still works — it records an additional manual observation.

### Counter resets

For a computed counter, the expression is expected to return a monotonically increasing total.
When the source resets (e.g. a process restarts and returns 0), the counter value drops in
Prometheus, which treats the drop as a counter reset. PromQL functions such as `rate()` and
`increase()` handle this correctly.

For labeled counters where you need to sync an external total per label, use `set()` on a
regular (non-computed) counter instead. Note that `set()` on a regular counter uses delta
semantics and cannot propagate resets.

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
