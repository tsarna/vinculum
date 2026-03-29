# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Breaking Change!

- `vinculum server` command line changed to `vinculum serve` to me more consistent with typical verb-like subcommand naming.

### Added

- Variables may now optionally have a defined type and nullability.
- `sys.plugins` lists the names of all plugin components

### Changed

- Reorganize code and move to plugin registries for functions, servers, clients, triggers, and ambient values (env.* and sys.*)

## [0.18.0] - 2026-03-28

### Breaking Change!

- client "kafka": producer blocks -> sender, consumer blocks -> receiver
- topic_mapping { pattern = "..." } -> topic "..." {} (labeled block)
- topic_subscription { kafka_topic/mqtt_topic = "..." } -> subscription "... {} (labeled block)
- client.* attributes: producers/producer -> senders/sender

### Added

- **[`client "mqtt"`](doc/client-mqtt.md)** — MQTT send and receive support.

## [0.17.0] - 2026-03-26

### Breaking Change!

- `cron` and `signals` blocks replaced by `trigger "cron" ...` and `trigger "signals" ...` blocks.

### Added

- Metrics can now be computed on-demand. Labels are not currently supported for computed metrics.
- New `trigger` block subsuming the previous `cron` and `signals` blocks, and
  adding new trigger types `after`, `interval`, `once`, `shutdown`, `start`, and `watchdog`.

## [0.16.0] - 2026-03-25

### Added

- **[`client "kafka"`](doc/client-kafka.md)** — Apache Kafka producer and consumer support
- **`metrics =` on `bus` and `server "vws"`** — bus and VWS server blocks now accept a
  `metrics = server.<name>` attribute to wire Prometheus instrumentation

## [0.15.0] - 2026-03-23

### Breaking Change!

- **Logging functions renamed** — underscores removed for naming consistency: `log_debug` →
  `logdebug`, `log_info` → `loginfo`, `log_warn` → `logwarn`, `log_error` → `logerror`,
  `log_msg` → `logmsg`

### Added

- **MCP server mounting under HTTP** — MCP server blocks no longer require a `listen` address;
  omit `listen` and reference the server via `handler = server.<name>` in an HTTP `handle` block
  to serve MCP alongside other routes on a shared port
- **`sys.*` built-in variable namespace** — read-only VCL variables exposing process and host
  identity captured at config-build time: `sys.pid`, `sys.hostname`, `sys.user`, `sys.uid`,
  `sys.group`, `sys.gid`, `sys.os`, `sys.arch`, `sys.cpus`, `sys.executable`, `sys.cwd`,
  `sys.homedir`, `sys.tempdir`, `sys.filepath`, `sys.writepath`, `sys.starttime`, `sys.boottime`
- **File write functions** — `filewrite(path, content)` and `fileappend(path, content)`, gated by
  `--write-path <dir>` and sandboxed to that directory; `sys.writepath` exposes the configured
  path in VCL
- **`templatefile(path, vars)` and `gotemplatefile(path, vars)` functions** — render a HCL-style
  or Go `text/template` template file with a variable map
- **Time and duration types and functions** — two new first-class VCL capsule types (`time`,
  `duration`) and a full function library:
  - Core: `now()`, `parsetime()`, `duration()`, `timeadd()`, `timesub()`, `since()`, `until()`,
    `formattime()`, `formatduration()`; `sys.starttime` and `sys.boottime`
  - Unix/decomposition/timezone: `fromunix()`, `unix()`, `timepart()`, `durationpart()`,
    `intimezone()`, `timezone()`, `absduration()`
  - Calendar arithmetic: `adddays()`, `addmonths()`, `addyears()`, `timeround()`,
    `timetruncate()`
  - Comparison: `timebefore()`, `timeafter()`, `durationlt()`, `durationgt()`
  - Formatting: `strftime()`, `strptime()`, named `@format` aliases (`@rfc3339`, `@date`,
    `@time`, etc.) for `formattime`/`parsetime`
  - Duration arithmetic: `durationadd()`, `durationsub()`, `durationmul()`, `durationdiv()`,
    `durationtruncate()`, `durationround()`
- **`nextzoneserial()` and `parsezoneserial()`** — DNS zone serial number utilities (YYYYMMDDNN
  format); `nextzoneserial(s[, t])` computes the next valid serial, `parsezoneserial(s)` converts
  back to an approximate time

## [0.14.1] - 2026-03-22

No functional changes. This release just adds a documentation index for the benefit of the
vinculum-ai tool (see github.com/tsarna/vscode-vinculum)

## [0.14.0] - 2026-03-22

### Added

- **LLM client support** — new `client "openai"` block and `call()` VCL function for synchronous
  LLM API calls; works with OpenAI and any OpenAI-compatible provider (Groq, Together AI, Mistral,
  Ollama, LM Studio, Google Gemini, etc.)
  - `api_key`, `model`, `base_url`, `max_tokens`, `temperature`, `timeout` attributes
  - `max_input_length` — optional character cap on user/assistant message content; returns
    `stop_reason = "error"` with `error.code = "input_too_long"` without making an API call
    when exceeded (system messages are not counted)
  - Response object always present: `content`, `stop_reason` (`"stop"`, `"max_tokens"`, or
    `"error"`), `model`, `usage` (`input_tokens`, `output_tokens`, `total_tokens`), `error`
    (`code`, `message`); API failures become error responses rather than Go-level errors
- **`call(ctx, client, request)` VCL function** — synchronous call to a `CallableClient`; request
  supports `messages`, `system` shorthand (prepended as `role = "system"`), and per-call overrides
  for `model`, `max_tokens`, and `temperature`
- **`llm_wrap(content)` VCL function** — wraps a string in `<user_input>…</user_input>` XML-like
  delimiters as a structural prompt injection mitigation; the system prompt should reference the
  tags to signal to the model where untrusted input begins and ends
- Documentation: `doc/client-llm.md` with full reference, provider table, Security section,
  and examples; `doc/config.md` and `doc/functions.md` updated

### Changed

- **`Client` interface refactored** into a clean hierarchy to support non-bus clients:
  - `Client` — base identity interface (`GetName`, `GetDefRange`)
  - `BusClient` — extends `Client` with bus plumbing (`Build`, `GetClient`, `GetSubscriber`,
    `SetSubscriber`); used by `VinculumWebsocketClient`
  - `Callable` — standalone call/response capability (`Call(ctx, request) → response`)
  - `CallableClient` — combines `Client` + `Callable`; implemented by `OpenAIClient`
  - `BaseBusClient` embeds `BaseClient` and holds the shared bus fields previously scattered
    across `VinculumWebsocketClient`

## [0.13.0] - 2026-03-22

### Added

- **Prometheus/OpenMetrics metrics support** — new `metric` block and `server "metrics"` block
  - `gauge`, `counter`, and `histogram` metric types with optional label declarations
  - Metrics server exposes a `/metrics` endpoint with automatic content negotiation between
    Prometheus text format and OpenMetrics (via `promhttp.HandlerFor`)
  - Can run standalone (own port) or be mounted into an HTTP server via `handle "/metrics" { handler = server.metrics }`
  - Go runtime and process collectors registered automatically
  - Internal `promadapter` package wires `prometheus.Registry` to the `o11y.MetricsProvider` interface,
    enabling bus and WebSocket server instrumentation
- **`observe()` VCL function** — records a value to a histogram metric
- **Labeled metric support in VCL** — `get`, `set`, `increment`, and `observe` all accept an optional
  labels object as a final argument (e.g. `increment(metric.hits, 1, {queue = ctx.msg.queue})`)
- **Random VCL functions** — seven new functions in the `random` category:
  - `random(n)` — random integer in [0, n)
  - `randint(lo, hi)` — random integer in [lo, hi]
  - `randuniform(lo, hi)` — random float in [lo, hi)
  - `randgauss(mean, stddev)` — normally distributed float
  - `randchoice(list)` — uniformly random element from a list
  - `randsample(list, n)` — random sample of n elements without replacement
  - `randshuffle(list)` — shuffled copy of a list
- Documentation: `doc/metric.md` and `doc/server-metrics.md` added; `doc/functions.md` expanded
  with Random and Variables & Metrics sections

### Changed

- `get`, `set`, and `increment` VCL functions now dispatch on `MetricCapsuleType` in addition to
  `VariableCapsuleType`, supporting labeled metric series alongside plain variables

## [0.12.0] - 2026-03-22

### Added

- Block dependency DAG and topological sort — configuration blocks are now processed in dependency
  order, enabling forward references and correct initialization sequencing (e.g. a `server` block
  is always started before `metric` or `subscription` blocks that reference it)

## [0.11.0] - 2026-03-21

### Added

- `var` block with `get`, `set`, and `increment` VCL functions for mutable in-process state

### Changed

- Switched back to upstream `github.com/amir-yaghoubi/mqttpattern` after our changes were accepted

[Unreleased]: https://github.com/tsarna/vinculum/compare/v0.16.0...HEAD
[0.16.0]: https://github.com/tsarna/vinculum/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/tsarna/vinculum/compare/v0.14.1...v0.15.0
[0.14.1]: https://github.com/tsarna/vinculum/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/tsarna/vinculum/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/tsarna/vinculum/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/tsarna/vinculum/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/tsarna/vinculum/compare/v0.10.0...v0.11.0
