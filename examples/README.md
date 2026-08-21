# Vinculum Examples

This directory contains complete, working Vinculum configurations that
demonstrate how to combine features to solve real problems. See the top-level
[README.md](../README.md) and [doc/overview.md](../doc/overview.md) for an
introduction to Vinculum, and [doc/](../doc/) for the full reference
documentation.

## Examples

### [dns-zone-updater/](dns-zone-updater/)

A dynamic DNS service that exposes a small HTTP API for updating BIND zone files
in place, compatible with the Unifi Network Controller's Dynamic DNS feature.
Combines a [`server "http"`](../doc/server-http.md) under
[basic authentication](../doc/auth.md), a [functy (`.cty`)](../doc/functy.md)
function holding the update and authorization logic, and an
[`editor "line"`](../doc/editor.md) block making idempotent, locked,
line-by-line edits to the zone file. Its credentials come from a mounted secret
rather than from the config, so the configuration carries nothing site-specific
and can be fetched at boot by a [`.vinit` `git` block](../doc/git.md). See
[dns-zone-updater/README.md](dns-zone-updater/README.md) for the endpoints,
required environment and flags, and deployment.

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
- `mcp::error()` for tool failures vs. a plain string for a resource
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
