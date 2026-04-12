# VoIP.ms Prometheus Exporter

A multi-file Vinculum configuration that scrapes the [VoIP.ms](https://voip.ms)
REST API on an interval and exposes the results as Prometheus metrics.
Demonstrates:

- [`client "http"`](../../doc/client-http.md) wrapping a third-party JSON API,
  with reusable [`function`](../../doc/functions.md) blocks for each endpoint
- [`metric`](../../doc/metric.md) blocks (gauges and counters, with and without
  labels) driven from [`procedure`](../../doc/procedure.md) blocks that
  [`set`](../../doc/metric.md) values after each scrape
- [`trigger "interval"`](../../doc/trigger.md) blocks with jitter to
  stagger scrapes
- [`server "metrics"`](../../doc/server-metrics.md) exposing a Prometheus
  endpoint
- Splitting a single logical configuration across multiple `.vcl` files:

  - [voipms-api.vcl](voipms-api.vcl) — HTTP client and API wrapper functions
  - [voipms-balance.vcl](voipms-balance.vcl) — reseller account balance and
    call/spend totals
  - [voipms-clients.vcl](voipms-clients.vcl) — per-client balance, threshold,
    and call counts (labeled metrics)
  - [voipms-sip.vcl](voipms-sip.vcl) — SIP account registration status
  - [voipms-stats.vcl](voipms-stats.vcl) — the Prometheus `server "metrics"`
    block

Required environment variables: `VOIPMS_API_USER` and `VOIPMS_API_PASSWORD`
(see [the VoIP.ms API documentation](https://voip.ms/m/apidocs.php) for how to
enable API access and generate credentials).
