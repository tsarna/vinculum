# Vinculum Examples

This directory contains complete, working Vinculum configurations that
demonstrate how to combine features to solve real problems. See the top-level
[README.md](../README.md) and [doc/overview.md](../doc/overview.md) for an
introduction to Vinculum, and [doc/](../doc/) for the full reference
documentation.

## Examples

### [dns-zone-updater/](dns-zone-updater/)

A dynamic DNS service that exposes a small HTTP API for updating BIND zone
files in place. Demonstrates:

- [`server "http"`](../doc/server-http.md) with route handlers and
  [basic authentication](../doc/server-auth.md)
- A [functy (`.cty`)](../doc/functy.md) function that encapsulates the update
  logic and authorizes callers based on their authenticated username, sitting in
  a `.cty` file alongside the `.vcl` and callable from the handlers like a
  built-in
- An [`editor "line"`](../doc/editor.md) block that performs idempotent,
  locked, line-by-line edits on a zone file (header timestamp, SOA serial bump,
  A-record replace), using "incidental" edits that don't, on their own, count
  as a real file change — so unrelated header/serial updates are discarded
  when no record actually changed

The configuration is compatible with the Unifi Network Controller's Dynamic DNS
feature; see the comments at the top of the file for the controller-side
configuration.

Required environment variables: `ZONES_DIR` (path to the directory containing
BIND zone files) and one `PASS_<ZONE>_<HOST>` variable per credential, as
referenced from the `auth "basic"` block in the example.

### [weather-mcp/](weather-mcp/)

A [`server "mcp"`](../doc/server-mcp.md) that exposes live weather data to an
MCP client (Claude Desktop, Claude Code, etc.) as tools, a templated resource,
and a prompt. It wraps the free, no-API-key [Open-Meteo](https://open-meteo.com)
service, so it runs with no environment variables or credentials. Demonstrates:

- `server "mcp"` with `tool`, a templated `resource`, and a `prompt` block,
  mounted under a [`server "http"`](../doc/server-http.md) block so each MCP
  call appears in the HTTP request log
- [`client "http"`](../doc/client-http.md) wrapping a third-party JSON API,
  with `function`/`jq` blocks factoring the geocode→forecast flow out of the
  handler actions so each request resolves the place once and calls the network
  once
- `cond()` for lazy branching — HCL's own `a ? b : c` eagerly evaluates *both*
  branches, so a side-effecting not-found path needs `cond()`
- `mcp_error()` for tool failures vs. a plain string for a resource
- an optional, env-toggled [`client "otlp"`](../doc/client-otlp.md) that the
  HTTP and MCP servers auto-wire to for OpenTelemetry traces and metrics — set
  `OTEL_EXPORTER_OTLP_ENDPOINT` to enable it, leave it unset to stay zero-config

Run it with `vinculum serve examples/weather-mcp/`, then point an MCP client
at `http://localhost:9000/mcp`. See the comments at the top of the file for the
client config and example prompts.

### [traffic-light/](traffic-light/)

A simulated four-way traffic intersection: a multi-file configuration combining
an [`fsm`](../doc/fsm.md) for the phase cycle, latched
[`condition "timer"`](../doc/condition.md) blocks for fault detection and
emergency preemption, [`trigger`](../doc/trigger.md) blocks (interval, cron,
start, watchdog) for phase advancement and mode switching, and a
[`server "http"`](../doc/server-http.md) +
[`server "vws"`](../doc/server-vws.md) pair serving a live web UI that pushes
state changes over a WebSocket. A running instance is available at
<https://traffic.thevinculum.org>. See
[traffic-light/README.md](traffic-light/README.md) for details.

### [voipms/](voipms/)

A Prometheus exporter for the [VoIP.ms](https://voip.ms) REST API, split across
multiple `.vcl` and [functy `.cty`](../doc/functy.md) files. Demonstrates
`client "http"` wrapping a third-party JSON API, labeled `metric` gauges and
counters populated from functy scrape functions, `trigger "interval"` scrapes
with jitter, and a `server "metrics"` Prometheus endpoint. See
[voipms/README.md](voipms/README.md) for details.
