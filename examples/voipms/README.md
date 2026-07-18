# VoIP.ms Metrics Exporter

A multi-file Vinculum configuration that scrapes the [VoIP.ms](https://voip.ms)
REST API on an interval and exposes the results as Prometheus or OpenTelemetry metrics. Demonstrates:

- [`client "http"`](../../doc/client-http.md) wrapping a third-party JSON API,
  with reusable [functy (`.cty`)](../../doc/functy.md) functions for each endpoint
- [`metric`](../../doc/metric.md) blocks (gauges and counters, with and without
  labels) driven from functy scrape functions that [`set`](../../doc/metric.md)
  values after each scrape
- [`trigger "interval"`](../../doc/trigger.md) blocks with jitter to
  stagger scrapes
- [`server "metrics"`](../../doc/server-metrics.md) exposing a Prometheus
  endpoint
- [`client "otlp"`](../../doc/client-otlp.md) for OpenTelemetry push metrics.
- Splitting a single logical configuration across multiple `.vcl` and `.cty`
  files sharing one evaluation context. Every `.cty` file declares
  `namespace voipms`, so its functions call each other by bare name
  (`get_balance(ctx, true)`) while VCL calls them by their qualified name
  (`voipms::scrape_balance_metrics(ctx)`) — keeping the API and scrape helpers
  out of the global function namespace without hand-prefixing every name. Each
  concern pairs a `.vcl` file (its blocks) with a `.cty` file (its functions):

  - [voipms-api.vcl](voipms-api.vcl) / [voipms-api.cty](voipms-api.cty) — the
    HTTP client and the API wrapper functions that call it
  - [voipms-balance.vcl](voipms-balance.vcl) /
    [voipms-balance.cty](voipms-balance.cty) — reseller account balance and
    call/spend totals
  - [voipms-clients.vcl](voipms-clients.vcl) /
    [voipms-clients.cty](voipms-clients.cty) — per-client balance, threshold,
    and call counts (labeled metrics)
  - [voipms-sip.vcl](voipms-sip.vcl) / [voipms-sip.cty](voipms-sip.cty) — SIP
    account registration status
  - [voipms-stats.vcl](voipms-stats.vcl) — the Prometheus `server "metrics"`
    block and OpenTelemetry `client "otlp"`.

Required environment variables: `VOIPMS_API_USER` and `VOIPMS_API_PASSWORD`
(see [the VoIP.ms API documentation](https://voip.ms/m/apidocs.php) for how to
enable API access and generate credentials).

Optional environment variable OTLP_URL configures push metrics via OTLP.
If not set or empty, metrics are instead exposed via Prometheus on port 9090.

Optional environment variable OTLP_SERVICE_NAME overrides the OTLP
`service_name` (default `vinculum-voipms`) — useful when a deployment needs the
emitted `service.name` (and the derived Prometheus `job` label) to match an
existing convention.
