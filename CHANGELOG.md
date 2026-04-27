# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

  - **`server "http"` logging middleware now passes through `http.Hijacker` and `http.Flusher`**: the `statusCapturingResponseWriter` wrapper embedded only the `http.ResponseWriter` interface, which hid extra capabilities of the underlying writer behind type assertions. As a result, mounting `server "vws"` (or any other WebSocket-upgrading handler) under an HTTP `handle` block failed at upgrade time with `http.ResponseWriter does not implement http.Hijacker`. The wrapper now implements `Hijack()` and `Flush()` that delegate to the underlying writer when supported, so WebSocket upgrades and streaming responses (e.g. SSE) work correctly through the request-logging middleware.
  - **`set()` after a timeout-driven deactivation now correctly re-activates a `condition "timer"`**: previously, when a timer condition with `timeout = ...` (no declared `input =`) auto-deactivated, both the StateMachine's `rawInput` tracker and the `TimerCondition` wrapper's `stableInput` cache were left at `true` — so a subsequent `set(condition.name, true)` was silently deduped at the wrapper layer, never reached the state machine, and produced no transition (no `on_activate`, no watcher fire). Effectively any consumer of a `timeout` + `set()`-driven condition could be re-armed exactly once per process lifetime. `onTimeoutTimer` now resets `sm.rawInput = false` (mirroring `Clear()`'s reset for the same reason: the timeout consumes the active session, so re-assertion must register as a fresh edge), and `TimerCondition.submitInput` reconciles its cached `stableInput` from the SM's `RawInput()` accessor on every call so that wrapper-level dedup and debounce edge-collapse decisions stay consistent with the SM's actual view of the input. Verified with regression tests covering both the no-debounce and debounce paths in `conditions/timer_test.go`.
  - **`clear()` on a latched condition no longer masks an ongoing-true input**: previously, calling `clear(condition.name)` on a `timer` or `threshold` condition with a declared `input =` expression released the latch and unconditionally returned the condition to `inactive` — even when the input was still asserted. The latched-fault-you-can-clear-while-the-cause-persists pattern was a footgun for safety-style use cases (the very thing `latch = true` is meant to surface). `clear()` now re-samples the declared input after resetting: if it is still truthy, the condition re-activates and re-engages the latch. For threshold conditions, hysteresis is applied from the freshly-reset baseline, so values inside the deadband stay inactive (consistent with the first-sample initial-value rule). Debounce is bypassed on the re-activation edge since the signal has already proven stable; `activate_after`, `cooldown`, and `inhibit` still apply normally.
  - **Async dispatch context propagation**: Two bugs where in-flight trigger/FSM actions could be corrupted by the caller's context lifecycle.
  - **`trigger "watch"` cancellation leak**: the dispatched action's goroutine inherited the caller's ctx verbatim, so an upstream cancel (e.g. the HTTP request that drove the watched change completing) could cancel the action mid-flight. Fixed by applying `context.WithoutCancel` at the dispatch boundary. The action's trace span is now a new root linked to the caller's span (matches OTel async-messaging semantic conventions), replacing the prior child-span relationship that violated parent-before-child discipline.
  - **FSM events dropped the caller's context entirely**: the vinculum-fsm library's `Event` struct carried no ctx, so reactive-when and bus-delivered events processed under `context.Background()`, losing the caller's trace span, auth, and everything else. Fixed upstream in vinculum-fsm (new `Event.Ctx` field, wired through `OnEvent` subscriber and the event loop with `context.WithoutCancel` applied at the dequeue boundary). Vinculum's reactive-when enqueue now threads the callback's ctx into the event, and FSM transition spans are now linked roots. Requires vinculum-fsm v0.3.0.
  - Added `hclutil.StartLinkedTriggerSpan` as the canonical helper for the async dispatch pattern: `LinkFromContext` + `WithoutCancel` + `WithNewRoot` + `WithLinks`.
  - Audited and confirmed unaffected: `trigger "watchdog"`, `trigger "file"`, `config/signals`, `ActionSubscriber.OnEvent` (sync; bus's `deliverAsync` already applies the correct pattern), computed metrics (use a private ctx), server startup goroutines.
  - **CLI error reporting**: `vinculum check` and `vinculum serve` no longer print the command usage block when a config file has a syntax or validation error, and no longer print the error twice. Config diagnostics are now shown once as a clean `Error: ...` line with no stack-trace log spam. Usage is still printed for genuine argument-parsing errors (missing required args, unknown flags).

### Added

- **Sliding-window rate primitive on `condition "counter"` — `window = T`**: when set, the counter's count reflects the number of `increment()` calls in the last `T` (the classic "N events in the last T minutes" rate primitive). Each increment timestamps an entry into a FIFO; entries age out automatically when `event_time + window <= now`. A single internal timer is armed for the next-to-expire event, so the cost is `O(1)` timers regardless of event rate (memory is `O(N-in-window)` — one timestamp per live event). `decrement(condition.x [, n])` pops the `n` oldest entries (useful for "retract an in-flight count"); decrementing past empty is a no-op. `reset()` and `clear()` both empty the FIFO and release any latch. `window` is incompatible with `rollover`, `count_down`, and a non-zero `initial` (rejected at parse time, since rollover snap-back, count-down semantics, and synthetic baseline events all require a notion of "current count" independent of event timestamps). `clear()` is now also defined on counter conditions (equivalent to `reset()`, since counters have no `input =` to re-sample) — the previous restriction (`clear()` for timer and threshold only) is lifted. See [doc/condition.md](doc/condition.md#window).
- **Lazy control-flow functions `cond()`, `switch()`, and `try()`**: New `cond(c1, r1, c2, r2, ..., else)` for multi-branch conditionals and `switch(on, v1, r1, v2, r2, ..., default?)` for value-dispatch — both evaluate only the selected branch, making them safe for side-effectful VCL expressions (unlike HCL's `?:` ternary which evaluates both sides eagerly). `switch()` evaluates `on` exactly once and short-circuits as soon as a case-value matches; the trailing default is optional, with a runtime error if no case matches. Also replaces the stock HCL `try()` (from `hcl/v2/ext/tryfunc`) with a single-evaluation variant: the upstream implementation evaluates the selected expression twice (once in the type-inference callback, once in the value callback), which is a footgun when wrapping side-effectful calls like `try(send(ctx, ...), fallback)`. Vinculum's `try()` returns `cty.DynamicPseudoType` from its type callback so each expression is evaluated at most once, at the cost of a dynamic return type. `can()` is unaffected (it already evaluates its argument exactly once since it has a static `bool` return type). See [doc/functions.md](doc/functions.md#control-flow).
- **Unified `subscriber | action` delivery for receiver blocks**: every block that accepts an inline `action =` or a named `subscriber =` now also accepts `transforms = [...]` (a transform pipeline) and `queue_size = N` (wraps delivery in an async background queue) — matching what the top-level `subscription` block already offered. This covers `client "sqs_receiver"`, `client "kafka"` receivers, `client "mqtt"` subscribers, `client "redis_stream"` consumers, and `client "redis_pubsub"` subscribers. Implementation is shared via a new `config.SubscriberSource` helper so the four attributes behave identically everywhere (same exactly-one validation, same wrapping order: transforms → async queue). The async queue uses `vinculum-bus` v0.13.0's new-root consumer-span tracing, so every wrapped receiver's tracer provider is now propagated into the queue and async processing shows up in traces as spans linked to the upstream producer span. Fixes a latent bug where `subscription { queue_size = ... }` would wrap the subscriber but never `.Start()` the goroutine.
- **`trigger "at"` imperative control**: At triggers now support `set()` and `reset()` for imperative control from VCL expressions (e.g. FSM `on_entry` hooks). `set(trigger.<name>, time)` overrides the next fire with an explicit absolute time and revives dormant triggers (the override is consumed on fire; subsequent iterations fall back to the configured `time` expression). `set(trigger.<name>)` with no argument re-evaluates the `time` expression (as before), but now also resets `run_count` and can revive dormancy. `reset(trigger.<name>)` cancels any pending fire and goes dormant. New `repeat = false` attribute fires once then waits for `set()`. New `stop_when` attribute stops the trigger after a fire when the expression is true. Omitting `time` starts the trigger dormant, waiting for the first `set(trigger.<name>, time_value)` — enabling FSM-driven alarms at per-state wall-clock times. `ctx.last_error` is now exposed in `time` and `action` eval contexts. See [doc/trigger.md](doc/trigger.md#trigger-at) for details.
- **Condition lifecycle hooks**: New optional `on_init`, `on_activate`, and `on_deactivate` action-expression attributes on every condition subtype (`timer`, `threshold`, `counter`). `on_init` fires once in the `PostStart` phase — after every Startable has bootstrapped — so cross-condition references in init expressions are safe. `on_activate` and `on_deactivate` fire synchronously on each user-visible output transition (respecting `invert`), with caller-ctx propagated for trace-span continuity. Hooks replace the common `trigger "watch" ... skip_when = !ctx.new_value` boilerplate for condition-local reactions; `trigger "watch"` remains the right tool for async dispatch or cross-cutting observers. See [doc/condition.md](doc/condition.md#lifecycle-hooks).
- **`start_active` condition attribute**: New common attribute on all three `condition` subtypes (`timer`, `threshold`, `counter`) that forces the condition to begin in the `active` state at startup. Combined with `latch = true`, implements the classic fail-safe pattern where power loss is itself treated as a fault — the system comes up with the condition latched and must be explicitly cleared (`clear()` / `reset()`) before it can resume operation. No synthetic transition event fires at boot; `inhibit` does not suppress the configured initial state; `invert` still applies as a final output transform. See [doc/condition.md](doc/condition.md#start_active).
- **Build version identity**: New `version` package exposes `Version`, `Commit`, `BuildTime`, and `Modified` for the vinculum binary. Values come from `-ldflags "-X …"` when set, falling back to Go's automatic `runtime/debug` VCS stamping for local builds. Surfaced in three places:
  - `vinculum version` subcommand prints version, commit, build time, Go version, and platform.
  - Startup log message now includes a `version` field (e.g. `"version":"v0.34.0 (abc123def456)"`).
  - `sys.version`, `sys.commit`, `sys.build_time`, and `sys.modified` available in VCL expressions.
  The Docker image builds (`Dockerfile`, `Dockerfile.minimal`, `.github/workflows/docker.yml`) now plumb `VERSION` (from the resolved image tag), `COMMIT` (`github.sha`), and `BUILD_TIME` through as build-args, since `.git/` is excluded from the Docker context and VCS stamping would otherwise be unavailable.
- **CI: GHCR image cleanup workflow**: New scheduled workflow (`.github/workflows/ghcr-cleanup.yml`) that weekly prunes untagged `vinculum` and `vinculum-minimal` GHCR package versions older than 14 days. Preserves all tagged releases (`latest`, `dev`, and semver tags) and multi-arch manifest children via [dataaxiom/ghcr-cleanup-action](https://github.com/dataaxiom/ghcr-cleanup-action). Supports manual dispatch with a dry-run preview.

### Changed

- **Quieter logging for user-caused errors**: Log output from VCL `log_debug`/`log_info`/`log_warn`/`log_error`/`log_msg` functions no longer includes the Go `caller` field or a Go stacktrace — those always pointed at `functions/log.go` and carried no signal about the VCL call site. Similarly, errors from evaluating user expressions in triggers, conditions, FSM hooks, signal actions, HTTP actions, MQTT lifecycle hooks, and assertions are now emitted via a new `Config.UserLogger` with caller and stacktrace suppressed. Infrastructure/operational errors (fsnotify watcher failures, HTTP server bind failures) still carry caller + stacktrace. The global stacktrace threshold is now pinned to `error` level regardless of `--debug`, so routine warnings no longer drag Go stacks along.

## [0.34.0] - 2026-04-22

### Added

- **`client "sns_sender"`**: AWS SNS sender support. Publishes vinculum bus events to SNS topics, target ARNs, or phone numbers with auto-detection, FIFO topic support, and per-message expression evaluation. See [doc/client-sns.md](doc/client-sns.md).

### Fixed

- **Diagnostic source locations**: Fixed `hcl:",def_range"` producing `:0,0-0` in all error diagnostics from client, server, trigger, wire_format, and metric block processors. The `gohcl.DecodeBody` tag only works for nested blocks, not top-level body decodes; all handlers now set `DefRange` from `block.DefRange` after decoding.
- **`ctx.fields` always present in action contexts**: `ctx.fields` is now always set as an empty object when a message has no fields, rather than being absent. Previously, referencing `ctx.fields` in a subscription action or per-message expression would error when no fields were present. Affects subscription actions, and all client per-message expressions (SNS, SQS, Kafka, MQTT, Redis pub/sub, Redis streams).

### Changed

- **`client "sqs_sender"`**: Removed batching (`SendMessageBatch`) support due to incompatibility with single-goroutine dispatch model.

## [0.33.0] - 2026-04-20

### Added

- **`fsm` block**: Finite state machines with guarded transitions, reactive `when` expressions (edge-triggered), key-value storage, snapshot/restore, MQTT topic pattern matching, and OpenTelemetry tracing. Built on [vinculum-fsm](https://github.com/tsarna/vinculum-fsm) (`v0.2.0`). See [doc/fsm.md](doc/fsm.md) for details.
- **`trigger "interval"` imperative control**: Interval triggers now support `set()` and `reset()` for imperative control from VCL expressions (e.g. FSM `on_entry` hooks). `set(trigger.<name>, duration)` restarts the trigger with a specific delay (+/- jitter); `reset(trigger.<name>)` cancels any pending timer and goes dormant. New `repeat = false` attribute makes the trigger fire once then wait for `set()`. Omitting `delay` starts the trigger dormant, waiting for the first `set()` — enabling FSM-driven one-shot timers with variable delays per state. See [doc/trigger.md](doc/trigger.md#trigger-interval) for details.

### Fixed

- **Dependency cycle between FSM and trigger blocks**: FSM `on_entry`/`on_exit`/`on_init`/`on_change`/`on_error` hooks and trigger `action`/`stop_when` attributes are now correctly excluded from config-time dependency extraction, since they are evaluated at runtime. Previously, an FSM referencing a trigger in `on_entry` while that trigger's action referenced the FSM would produce a spurious circular dependency error.

## [0.32.0] - 2026-04-18

### Fixed

- Skip hidden directories (names starting with `.`) during config directory walking. This prevents errors from Kubernetes ConfigMap `..data` symlink directories and follows standard Unix conventions.

### Added

- **AWS SQS support**: Two new client types for sending to and receiving from Amazon SQS queues, plus a shared AWS credentials block. Built on the [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) via the new [vinculum-sqs](https://github.com/tsarna/vinculum-sqs) (`v0.1.0`) module.
  - **`client "aws"`**: Shared AWS credentials and region configuration with support for static credentials, assume-role (STS), custom endpoints (LocalStack/ElasticMQ), and AWS profiles. Child clients reference it via `aws = client.<name>`.
  - **`client "sqs_sender"`**: Sends vinculum bus events to an SQS queue. Features include wire format serialization, field-to-attribute mapping, optional topic attribute, FIFO queue support (`message_group_id`, `deduplication_id`), batching via `SendMessageBatch`, and W3C trace context propagation.
  - **`client "sqs_receiver"`**: Polls an SQS queue via long-polling and dispatches messages to a vinculum subscriber or action. Features include configurable concurrency, auto-delete or manual acknowledgement, per-message vinculum topic resolution via HCL expressions, system attribute mapping (`$message_id`, `$receipt_handle`, `$receive_count`, etc.), and W3C trace context extraction.
  - **`sqs_delete()` and `sqs_extend_visibility()` functions**: Global VCL functions for manual message deletion and visibility timeout extension. See [doc/client-sqs.md](doc/client-sqs.md) for details.

## [0.31.0] - 2026-04-18

### Added

- **`barcode()` function**: Generates barcode images as PNG `bytes` objects via the new [barcode-cty-func](https://github.com/tsarna/barcode-cty-func) (`v0.1.0`) module. Supports 11 formats: QR, DataMatrix, Aztec, PDF417, Code 128, Code 93, Code 39, Codabar, EAN-13, EAN-8, and Interleaved 2-of-5. Options include `scale`, `width`/`height`, and QR `error_correction`. 1D barcodes get sensible default heights automatically. See [doc/functions.md](doc/functions.md#barcode) for details.
- **`ctx.request.path`**: Route path parameters from `{name}` placeholders are now direct attributes on `ctx.request.path` (e.g. `ctx.request.path.id`), pre-extracted at route registration time for zero per-request parsing overhead.
- **`ctx.request.form`**: Parsed form data (query string + URL-encoded POST body) is now a direct attribute as `map(list(string))` (e.g. `ctx.request.form.name[0]`).
- **`ExtractPathParams()` utility**: Parses path parameter names from a Go 1.22+ route pattern once at registration time, returning a `[]string` for `BuildHTTPRequestObject`.

### Changed

- Extracted the `bytes` capsule/object type and `bytes()`/`base64encode()`/`base64decode()` functions into a new standalone module: [bytes-cty-type](https://github.com/tsarna/bytes-cty-type) (`v0.1.0`). Internal-only refactor with no VCL-visible changes; vinculum now depends on the external module for these symbols. The `filebytes()` function remains in vinculum.
- **HTTP request/response object redesign**: Commonly-needed fields are now direct attributes while infrequently-used or expensive fields use `get()`. Headers are accessed via `get(ctx.request, "header", name)` / `get(ctx.request, "header_all", name)` instead of the static `headers` map. The same change applies to HTTP client response objects.

### Removed

- **`ctx.request.headers`**: Removed from the static request object. Use `get(ctx.request, "header", name)` or `get(ctx.request, "header_all", name)` instead.
- **`get(ctx.request, "path_value", name)`**: Replaced by `ctx.request.path.name`.
- **`get(ctx.request, "form_value", name)`**: Replaced by `ctx.request.form.name`.
- **Response `headers` attribute**: Removed from the HTTP client response object. Use `get(resp, "header", name)` or `get(resp, "header_all", name)` instead.

## [0.30.0] - 2026-04-17

### Added

- **Pluggable wire format system** for consistent payload serialization/deserialization across all messaging clients and servers. Replaces the copy-pasted `serializePayload`/`deserializePayload` functions with a shared interface from the new [vinculum-wire](https://github.com/tsarna/vinculum-wire) (`v0.1.0`) module.
  - **`wire_format` attribute** on `client "kafka"`, `client "mqtt"`, `client "redis_pubsub"`, `client "redis_stream"`, and `client "redis_kv"`. Accepts `"auto"` (default), `"json"`, `"string"`, or `"bytes"`, or a custom `wire_format` capsule.
  - **`wire_format "<type>" "<name>"` block** for registering custom wire format plugins (e.g. Protocol Buffers, MessagePack). Custom formats participate in the dependency graph and are addressable as `wire_format.<name>` in expressions.
  - **`serialize(wire_format, value)`**, **`serializestr(wire_format, value)`**, and **`deserialize(wire_format, data)`** expression functions for ad-hoc use in VCL. See [doc/functions.md](doc/functions.md#wire-format-serialize--deserialize).
  - **`CtyWireFormat` decorator** in the config layer transparently converts between cty values and native Go types, so messaging clients never need to import cty.

### Changed

- **`client "redis_kv"`: `value_encoding` replaced by `wire_format`** — the old `value_encoding` attribute (`auto`/`raw`/`json`) is replaced by the shared `wire_format` system. The old `raw` mode maps to `wire_format = "string"`.
- **Strings serialize verbatim in auto mode** — the `auto` wire format passes strings through unchanged (not JSON-encoded). Use `wire_format = "json"` for the old behavior.
- Updated vinculum-kafka to v0.9.0, vinculum-mqtt to v0.7.0, vinculum-redis to v0.2.0.

## [0.29.0] - 2026-04-17

### Added

- **Geographic functions**: 16 new functions for working with locations, solar events, celestial positions, geodesic calculations, and geometric queries via the new [geo-cty-funcs](https://github.com/tsarna/geo-cty-funcs) (`v0.2.0`) module. Includes `geo_point`, `geo_format`, `sunrise`/`sunset`/`solar_noon`/`solar_midnight`, `sun_position`/`moon_position`/`moon_phase`, `geo_inverse`/`geo_destination`/`geo_waypoints`, and `geo_area`/`geo_contains`/`geo_nearest`/`geo_line_intersect`. See [doc/functions.md](doc/functions.md#geographic) for details.

### Changed

- Extracted the URL capsule/object types and `url*` cty functions (`urlparse`, `urljoin`, `urljoinpath`, `urlqueryencode`, `urlquerydecode`, `urldecode`) into a new standalone module: [url-cty-funcs](https://github.com/tsarna/url-cty-funcs) (`v0.1.0`). Internal-only refactor with no VCL-visible changes; vinculum now depends on the external module for these symbols.
- Upgraded [time-cty-funcs](https://github.com/tsarna/time-cty-funcs) to `v0.2.0`. The `time` and `duration` capsule types now implement the rich-cty-types `Stringable` and `Gettable` interfaces, so `tostring(t)` / `tostring(d)` and `get(t, part)` / `get(d, unit)` work in VCL. See [doc/functions.md](doc/functions.md#timestamp--decomposition).

### Removed

- **VCL-visible:** `timepart(t, part)` and `durationpart(d, unit)` have been removed. Use the generic `get(t, part)` / `get(d, unit)` instead — same part/unit names, same return types.

## [0.28.0] - 2026-04-16

### Added

- **Redis/Valkey support**: Four new client blocks for Redis- and Valkey-compatible servers. `client "redis"` is a passive connection manager (standalone, cluster, or sentinel) referenced by three child clients: `client "redis_pubsub"` for `PUBLISH`/`SUBSCRIBE`/`PSUBSCRIBE` channel messaging, `client "redis_stream"` for persistent `XADD`/`XREADGROUP` logs with consumer groups, manual ack (`redis_ack()`), reclaim, and dead-letter, and `client "redis_kv"` exposing `GET`/`SET`/`INCR`/`HGET`/`HSET` through the generic `get()` / `set()` / `increment()` interface. See [doc/client-redis.md](doc/client-redis.md) for details.

### Changed

- Extracted the generic capability interfaces (`Stringable`, `Gettable`, `Watchable`, …), the generic dispatcher functions (`get`, `set`, `tostring`, `length`, …), and the `_ctx` / `_capsule` rich-object helpers into a new standalone module: [rich-cty-types](https://github.com/tsarna/rich-cty-types) (`v0.1.0`). Internal-only refactor with no VCL-visible changes; vinculum now depends on the external module for these symbols.

## [0.27.0] - 2026-04-13

### Added

- **`condition` blocks**: A new automation primitive producing a named, Watchable boolean from various input types and behavioral rules. Three subtypes — `condition "timer"` (temporal rules over a boolean signal), `condition "threshold"` (boolean derived from a numeric input via hysteresis), and `condition "counter"` (boolean from event counts via `increment()`/`decrement()`) — covering IEC 61131-3 timer and counter function-block behaviors and composable into pipelines via `input = get(condition.other)`. See [doc/condition.md](doc/condition.md) for details.
- **`reset(trigger.<name>)` on `trigger "watchdog"`**: Returns a watchdog to its post-startup state, reviving it if it had auto-stopped via `max_misses` or `stop_when`.

## [0.26.0] - 2026-04-12

### Added

- **`client "http"`**: A new HTTP(S) client capability. See [doc/client-http.md](doc/client-http.md) for details.

### Fixed

- consts can now reference ambients like env.* again without a dependency error.
- Handle implicit metrics wiring by making metrics blocks without explicit wiring depend on all otlp client and metrics servers.

## [0.25.0] - 2026-04-10

### Added

- **`procedure` blocks**: A limited imperative language for defining callable functions with variable assignments, conditionals (`if`/`elif`/`else`), loops (`while`, `range`), `switch`/`case`/`default`, and `break`/`continue`. Procedures compile to `cty/function.Function` values callable from any expression. See [doc/procedure.md](doc/procedure.md) for details.

### Breaking Changes

- **Log functions renamed**: `logdebug` → `log_debug`, `loginfo` → `log_info`, `logwarn` → `log_warn`, `logerror` → `log_error`, `logmsg` → `log_msg`. Update all VCL files accordingly.

## [0.24.0] - 2026-04-09

### Added

- **OTel-native metrics**: All metrics are now OpenTelemetry SDK-native. `metric` blocks create OTel instruments via a `MeterProvider`; `server "metrics"` bridges them to Prometheus exposition using the OTel-to-Prometheus exporter. `client "otlp"` can now push metrics via OTLP in addition to traces.
- **`client "otlp"` push metrics**: New attributes `metric_endpoint`, `metric_interval` (default `"60s"`), `include_go_metrics` (default `true`), and `default_metrics` for OTLP metric export alongside traces.
- **`metrics =` attribute on `server "http"` and `server "mcp"`**: Enables automatic HTTP server metrics (`http.server.request.duration`, `http.server.active_requests`, etc.) via `otelhttp`, following OTel semantic conventions.
- **HTTP metrics on standalone `server "metrics"` and `server "mcp"`**: Standalone servers that create their own HTTP listeners now produce HTTP metrics when a metrics backend is available.
- **Unified metrics auto-wire**: New `InstrumentMetrics` interface and `GetDefaultInstrumentMetrics()` search both `server "metrics"` and `client "otlp"` blocks for a default metrics backend. The `default_metrics` attribute controls which backend is used when both are configured.
- **Dot-separated metric names**: Metric block labels can use dots (e.g. `metric "gauge" "http.server.active_requests"`). OTel instrument names preserve dots; VCL access uses underscore translation (`metric.http_server_active_requests`). Collisions are detected at parse time.
- **`computed_interval` attribute on metric blocks**: Controls the polling interval for computed metrics (default `"15s"`).
- **`ResolveMeterProvider()`**: New config function for resolving a `metric.MeterProvider` from an HCL expression or default, accepting both server and client references.

### Changed

- **Go runtime metrics via OTel**: `server "metrics"` now uses OTel runtime instrumentation instead of raw Prometheus Go/process collectors. Metric names change from `go_goroutines` to `go_goroutine_count`, etc.
- **Computed metrics use polling**: Computed metrics (`value = expr`) now evaluate on a fixed interval (default 15s) instead of at each Prometheus scrape. Configurable via `computed_interval`. OTLP push benefits from this change.
- **Metric namespace separator**: `namespace` now uses dots for OTel instrument names (`namespace.name`) instead of underscores.

### Removed

- **`internal/promadapter` package deleted** — the `promadapter.Provider` (which bridged `prometheus.Registry` to `o11y.MetricsProvider`) is no longer needed since all consumers now use `metric.MeterProvider` directly.
- **`GetMetricsProvider()` removed from `MetricsRegistrar` interface** — consumers use `GetMeterProvider()` instead.
- **`GetDefaultMetricsProvider()` and `ResolveMetricsProvider()` removed** — replaced by `ResolveMeterProvider()`.
- **`o11y.MetricsProvider` dependency removed** — vinculum no longer imports or uses the `o11y.MetricsProvider`, `Counter`, `Histogram`, `Gauge`, or `Label` types.

### Breaking Changes

- **`server "metrics"`: `default` renamed to `default_metrics`** — Update existing HCL configs that use `default = true` on a `server "metrics"` block.
- **Prometheus metric name changes**: The OTel-to-Prometheus exporter adds `_total` suffix to counters and converts dots to underscores. Go runtime metric names have changed (e.g. `go_goroutines` to `go_goroutine_count`). Dashboard queries may need updating.
- **`client "kafka"` / `client "mqtt"` / `server "vws"` / `server "websocket"` metrics API** — these now use `WithMeterProvider(metric.MeterProvider)` instead of `WithMetricsProvider(o11y.MetricsProvider)`. Metric names follow OTel semantic conventions. Requires vinculum-bus v0.11.0, vinculum-kafka v0.8.0, vinculum-mqtt v0.6.0, vinculum-vws v0.11.0.

## [0.23.0] - 2026-04-07

### Added

- **`editor "line"` — `when` is now a post-match guard**: `when` is evaluated after the regex matches rather than before it. The full match context — `ctx.groups`, `ctx.named`, `ctx.count`, `ctx.line` — is available inside `when`, enabling capture-group–based qualification (e.g. `when = ctx.groups[1] == recordname`). If `when` is falsy the line continues to the next rule uncounted.

- **`editor "line"` blocks** — compile into callable functions that edit a text file (or string) in-place using ordered regex match-and-replace rules. Processed early alongside `function` and `jq` blocks so the resulting functions are available throughout the rest of the config.
  - `params` / `variadic_param` — declare function parameters, same semantics as `function` blocks
  - `mode = "file"` (default) — edits a file on disk; requires `--write-path`. Returns `true` if the file was modified, `false` otherwise.
  - `mode = "string"` — operates on a string in memory; returns the processed string. Does not require `--write-path`.
  - `match "<regex>" { ... }` — ordered match rules; first matching rule wins per line. Attributes: `required` (minimum match count), `max` (stop after n matches), `when` (post-match guard expression), `replace` (replacement text), `abort` (clean-abort if truthy), `update_state` (merge into running state), `incidental` (see below).
  - `before { content = expr }` / `after { content = expr }` — prepend/append content to the output. Both blocks see the **final accumulated state** after all lines are processed. `before` uses a two-pass mechanism internally so that prepended content can reference state collected during the body. Both accept `incidental = true`.
  - `incidental = true` on a `match`, `before`, or `after` block — the replacement/content is written but does not itself count as a change. If all modifications in an edit run were incidental, the file is not written and the function returns `false`. Useful for housekeeping updates (timestamps, serial numbers) that should ride along with a real change but not trigger a write on their own.
  - `state = { ... }` — declares initial values for state variables; rules accumulate state via `update_state`; `state.<name>` is in scope in all expressions.
  - `backup = "<suffix>"` — hard-links the original file to `<path><suffix>` before the atomic rename (e.g. `backup = "~"` or `backup = ".bak"`).
  - `create_if_absent = true` — treat a missing file as empty rather than an error.
  - `lock = true` — acquire an exclusive `flock(2)` on a sibling `.lock` file before editing, serializing concurrent invocations. Works on local filesystems and NFSv4 (including AWS EFS). Lock is released automatically on return.
  - Regex capture groups exposed as `ctx.groups` (list) and `ctx.named` (map); `ctx.count` tracks per-rule match count; `ctx.line` / `ctx.lineno` / `ctx.filename` provide line context.
  - See [doc/editor.md](doc/editor.md) for full reference.

## [0.22.0] - 2026-04-03

### Changed

- **`PreStoppable` lifecycle interface and `trigger "shutdown"` fix** — new `PreStoppable`
  interface with a `PreStop() error` method, called in reverse registration order before any
  `Stoppable.Stop()` calls begin. `trigger "shutdown"` now implements `PreStoppable` instead of
  `Stoppable`, guaranteeing that shutdown actions execute while all clients, buses, and
  subscriptions are still fully operational. Previously, whether a shutdown action had access to a
  given client depended on registration order in the config file.

### Added

- **`client "otlp"` — distributed tracing via OpenTelemetry** — new client type that configures the OTel SDK and exports spans to any OTLP/HTTP collector (Jaeger, Grafana Tempo, Honeycomb, etc.):
  - `endpoint` — OTLP/HTTP base URL (e.g. `"http://localhost:4318"`)
  - `service_name` / `service_version` — recorded on every span
  - `sampling_ratio` — head-based sampling for new root spans (default `1.0`); inherited traces are always continued
  - `default = true` — marks this client as the auto-wire target when multiple OTLP clients are declared; omit when there is only one
  - `headers` — `map(string)` of HTTP headers added to every export request (e.g. auth tokens)
  - `tls {}` — optional TLS configuration for the collector connection
  - On `Start()`, sets the global OTel `TracerProvider` and installs W3C TraceContext + Baggage propagation; flushes pending spans on `Stop()`
  - See [doc/client-otlp.md](doc/client-otlp.md) for full reference
- **Distributed tracing for `server "http"`** — add `tracing = client.<name>` to instrument an HTTP server with OTel spans. Each inbound request produces a span: incoming `traceparent` headers are continued as child spans; requests without one start a new root span.
  - Auto-wires to the single `client "otlp"` block (or one marked `default = true`) when `tracing =` is omitted
  - Span name: `METHOD /path`; OTel HTTP semantic conventions applied via `otelhttp`
  - Rich access log at request completion: method, route, path, status, duration_ms, bytes, trace_id
- **Distributed tracing for `server "mcp"` (standalone mode)** — when a `server "mcp"` block has a `listen` address, it now accepts an optional `tracing = client.<name>` attribute. Incoming W3C `traceparent` / `tracestate` headers are extracted and a server span is created for each request, exactly as with `server "http"`. Auto-wires to the default OTLP client when `tracing =` is omitted. Mounted MCP servers (no `listen`) continue to inherit tracing from the parent `server "http"`.
- **Distributed tracing for `server "metrics"` (standalone mode)** — same as above: when `server "metrics"` has a `listen` address, add `tracing = client.<name>` (or rely on auto-wiring) to extract trace context and create spans for `/metrics` scrape requests. Mounted metrics servers inherit tracing from their parent HTTP server.
- **Distributed tracing for `client "mqtt"`** — add `tracing = client.<name>` to instrument an MQTT client with OTel spans. The subscriber extracts incoming `traceparent`/`tracestate` user properties and creates a `process <topic>` span (new trace root linked to the producer span per OTel messaging conventions); the publisher injects trace context into outbound user properties and creates a `send <topic>` span. Both spans carry OTel messaging semantic convention attributes (`messaging.system`, `messaging.destination.name`, `messaging.operation.type`, `messaging.operation.name`) and correct span kinds (`SpanKindProducer` / `SpanKindConsumer`). Auto-wires to the default OTLP client when `tracing =` is omitted. Trace headers are filtered from the `fields` map delivered to VCL actions.
- **Distributed tracing for `client "kafka"`** — add `tracing = client.<name>` to instrument a Kafka client with OTel spans. Uses the official `kotel` plugin from franz-go with `LinkSpans()` enabled: consumer spans are new trace roots linked to the producer span (per OTel messaging semantic conventions for async pub/sub), with full messaging semantic convention attributes set automatically. The producer injects trace context into outbound record headers. Auto-wires to the default OTLP client when `tracing =` is omitted. Trace headers are filtered from the `fields` map delivered to VCL actions.
- **`ctx.trace_id` and `ctx.span_id`** — when a request is being handled inside an active OTel span, these string variables are available in all VCL action expressions (not just HTTP); both are `""` when no span is active (NOOP tracer or no `client "otlp"` configured)
- **Distributed tracing for outgoing OpenAI API calls** — `client "openai"` now wraps its HTTP transport with `otelhttp.NewTransport`, so every LLM API call creates a child span of the current context and injects `traceparent` / `tracestate` headers into the outgoing request. No configuration change required; uses the global TracerProvider set by `client "otlp"`.
- **Distributed tracing for all trigger types** — every trigger firing now creates its own root OTel span (or a child span when context is available), so the full work chain from trigger through bus events to outgoing clients is traceable as a single trace:
  - Span name: `trigger.<type> <name>` (e.g. `trigger.interval my_poll`, `trigger.cron nightly/cleanup`)
  - Span covers only the action execution, not idle wait time (delay, sleep, or timer countdown)
  - Errors from action expression evaluation are recorded on the span (`span.RecordError` + `codes.Error` status)
  - `ctx.trace_id` / `ctx.span_id` are populated in the VCL action context for all trigger types
  - `trigger "watch"` uses the incoming context as parent (preserving the trace from the `set()` caller); all other triggers start a new root span
  - `trigger "signals"` creates a span per signal delivery (timing and errors recorded; VCL context uses pre-built eval context from config time)
  - **Distributed tracing for `bus` blocks** — add `tracing = client.<name>` to instrument an event bus with OTel spans. Each `Publish` and `PublishSync` call creates a producer span; each subscriber delivery creates a consumer span (child for sync, new root with a link for async) per OTel messaging semantic conventions. Auto-wires to the default OTLP client when `tracing =` is omitted.
- **Distributed tracing for VWS** - vinculum now depends on v0.10.0 of vinculum-vws, which adds support for tracing headers.

## [0.21.0] - 2026-04-02

### Changed

- **`PostStartable` lifecycle interface** — new `PostStartable` interface with a `PostStart() error`
  method, called once for each registered component after all `Startable.Start()` calls complete.
  This guarantees that buses, clients, and subscriptions are fully initialised before any
  PostStartable component dispatches events or evaluates action expressions.

- **`trigger "start"` now fires in `PostStart()`** — previously the action was evaluated
  synchronously during config parsing (before any component had started), which allowed action
  expressions to race the runtime. The action now fires in `PostStart()`, after the full startup
  sequence. `trigger.<name>` is `null` until `PostStart()` completes. Use `const` for values
  that must be computed at parse time (e.g. a startup timestamp captured before any I/O).

- **`trigger "after"`, `trigger "interval"`, `trigger "at"`, `trigger "watchdog"` goroutines now
  launch in `PostStart()`** — previously goroutines were launched in `Start()`, meaning very short
  delays or zero initial delays could cause the first action invocation to race other components
  that had not yet completed `Start()`. Goroutines (and their delay/timeout clocks) now start only
  after all `Startable`s have completed, so the full runtime is available on every action
  invocation. `Stop()` continues to work correctly if called before `PostStart()` (returns nil
  immediately with no goroutine to wait for).

### Added

- **`try()` and `can()` functions** — HCL-native error-handling functions from
  `github.com/hashicorp/hcl/v2/ext/tryfunc`, now registered in the standard function set:
  - `try(expr...)` — evaluates each argument in sequence and returns the first one that
    succeeds without error; returns an error only if all arguments fail
  - `can(expr)` — evaluates the expression and returns `true` if it succeeds, `false` if
    it produces any error; useful for conditional logic based on whether a value is valid

- **`trigger "file"`** — new filesystem-event trigger backed by
  [fsnotify](https://github.com/fsnotify/fsnotify); fires an action expression each time
  a matching file-system event occurs:
  - `path = expression` — required; directory to watch (absolute or relative to `--file-path`)
  - `action = expression` — evaluated on each matching event in a new goroutine
  - `events = ["create", "write", "delete", "rename", "chmod"]` — optional; defaults to all events
  - `recursive = true` — optional; also watch all subdirectories (including ones created after start)
  - `filter = "glob"` — optional; only dispatch events whose filename matches the glob pattern
  - `debounce = "duration"` — optional; coalesce bursts of events on the same path into a single
    dispatch after the specified quiet period
  - `on_start_existing = true` — optional; dispatch synthetic `create` events for all files already
    present in `path` at `PostStart()` time (the live watch is already active so no real events are
    lost)
  - `skip_when = expression` — optional; skip this firing if the expression returns `true`
  - `ctx.event_path` — full path of the file that triggered the event
  - `ctx.event` — event type string: `"create"`, `"write"`, `"delete"`, `"rename"`, or `"chmod"`
  - `ctx.path` — the configured watch directory
  - `ctx.run_count`, `ctx.last_result`, `ctx.last_error` — standard trigger context values
  - `get(trigger.<name>)` returns the most recent action result, or `null` before the first firing
  - Requires `--file-path` to be set; the trigger type is not registered when the feature is absent
  - See [doc/trigger.md](doc/trigger.md) for full reference and examples

- **`trigger "at"`** — new trigger type that fires an action at a dynamically computed
  absolute time, then repeats by re-evaluating the `time` expression each cycle:
  - `time = expression` — required; must evaluate to a time capsule (e.g. from `now()`,
    `timeadd()`, or future `sunrise()`/`sunset()` functions)
  - `action = expression` — evaluated each time the trigger fires
  - Always repeats: after each firing the `time` expression is re-evaluated to schedule
    the next occurrence; this is the natural fit for non-uniform recurring schedules
    where the interval between firings varies (contrast with `trigger "cron"` for fixed
    schedules and `trigger "interval"` for delays)
  - `get(trigger.<name>)` returns the currently scheduled fire time as a time capsule,
    or `null` before the first evaluation; use with `until()` to compute time remaining
  - `set(trigger.<name>)` wakes the goroutine to re-evaluate `time` immediately without
    firing the action — use from a `trigger "interval"` to recompute the schedule
    dynamically as conditions change (e.g. a vehicle's position shifting)
  - If `time` evaluates to a past time, the action fires immediately with a warning
  - If the `time` expression errors, the trigger logs the error and retries after one minute
  - `ctx.trigger`, `ctx.name`, `ctx.run_count`, and `ctx.last_result` are available in
    both `time` and `action` expressions
  - See [doc/trigger.md](doc/trigger.md) for full reference and examples

- **Watchables** — `var`, gauge `metric`, and counter `metric` values now implement the
  `Watchable` interface, enabling reactive, event-driven patterns without polling:
  - Every `set()` and `increment()` call on a watchable notifies all registered watchers
    synchronously after the value mutex is released; the calling goroutine blocks until all
    `OnChange` callbacks return
  - Notifications fire on **every** `set()`/`increment()` call, even when the value is
    unchanged — this is intentional so watchdog heartbeat patterns work correctly; consumers
    that want changes-only behavior use `skip_when = ctx.old_value == ctx.new_value`
  - The `context.Context` passed to `set()`/`increment()` is forwarded verbatim to all
    `OnChange` callbacks, preserving request-scoped metadata (trace IDs, auth tokens, etc.)
  - `HistogramMetric` and computed metric variants are **not** Watchable in this release
  - See [doc/trigger.md](doc/trigger.md) for `trigger "watch"` and the watchdog `watch` attribute

- **`trigger "watch"`** — new reactive trigger type that fires an action expression each time
  a Watchable value changes:
  - `watch = expression` — required; must evaluate to a watchable `var` or `metric` capsule
  - `action = expression` — evaluated on each change in a new goroutine (non-blocking)
  - `skip_when = expression` — optional; skip this firing if the expression returns `true`
  - `ctx.old_value` / `ctx.new_value` provide the before/after values in the action and skip_when contexts
  - `get(trigger.<name>)` returns the most recently observed value, or `null` before any change
  - `Stop()` unregisters the watcher and waits for all in-flight action goroutines to finish

- **`trigger "watchdog"` `watch` attribute** — optional `watch = expression` attribute on
  watchdog triggers to auto-feed the watchdog whenever a Watchable's value changes, eliminating
  the need for explicit `set(trigger.<name>, ...)` calls in every producer path; manual `set()`
  calls remain valid and are unaffected

- **`trigger "watchdog"` `max_misses` / `stop_when`** — two new optional attributes to
  auto-stop a watchdog after a condition is met, consistent with `trigger "interval"`:
  - `max_misses = N` — stops after N consecutive fires without a `set()` in between
  - `stop_when = expression` — stops when the boolean expression evaluates `true` after a fire;
    the same `ctx` as the action is available, including the updated `ctx.miss_count`
  - Both attributes are independent; if both are provided, the trigger stops when either
    condition is satisfied
  - A stopped watchdog is **revived** by calling `set()`: `miss_count` resets to 0 (clearing
    any `max_misses` condition), and `stop_when` is re-evaluated against the post-`set()` state;
    if it is now `false`, the watchdog re-arms immediately

## [0.20.0] - 2026-04-01

### Breaking Changes

- **HTTP action return value is now the response** — the return value of a `handle` action
  expression determines the HTTP response sent to the client. Previously it was ignored.
  Automatic coercion applies: `string` → 200 text/plain, `null` → 204, objects/maps/lists →
  200 application/json, `bytes` → 200 with its content type.
- **`respond()`, `setheader()`, `redirect()` removed** — these HTTP-action-context-only
  side-effect functions have been removed. Use the return value and the new response
  functions instead (see Added below).
- **`httpstatus` renamed to `http_status`** — the ambient HTTP status code constant object
  is now `http_status.NotFound`, `http_status.OK`, etc. Update all references.

- **`bytes` is now a rich object type** — `bytes()`, `base64decode(..., ct)`, and `filebytes()` now return an object with a `content_type` attribute and a `_capsule` for interface dispatch, rather than a raw capsule. Callers should use `b.content_type` instead of `get(b, "content_type")`. The `get()` function is no longer supported on `bytes` values.
- **`bytes` `get()` modes removed** — all `get()` modes on bytes values are gone:
  - `get(b)` / `get(b, "utf8")` / `get(b, "string")` / `get(b, "text")` → use `tostring(b)` instead
  - `get(b, "base64")` → use `base64encode(b)` instead
  - `get(b, "len")` / `get(b, "length")` / `get(b, "size")` → use `length(b)` instead
  - `get(b, "content_type")` → use `b.content_type` instead

### Changed

- **`server "http"` `files` block now requires `--file-path`** — any non-disabled `files` block
  requires `vinculum serve` to be started with `--file-path`. Relative `directory` values are
  resolved against that base directory (previously they resolved against the process working
  directory and `--file-path` was not involved).

### Added

- **Authentication (`auth` block)** — optional authentication for `server "http"`, `server "mcp"`, and `server "metrics"` blocks. Five modes are supported:
  - `auth "basic"` — HTTP Basic authentication against a static credentials map or a custom per-request expression
  - `auth "oidc"` — OpenID Connect bearer token validation via local JWKS (with automatic OIDC discovery and background key refresh) or RFC 7662 token introspection
  - `auth "oauth2"` — RFC 7662 token introspection with optional result caching (`cache_ttl`)
  - `auth "custom"` — arbitrary per-request expression; return an object (success), null (401), or an `http_response`/`http_redirect` value (e.g. for login redirects)
  - `auth "none"` — explicitly opt out of inherited server-level auth on a specific `handle` or `files` block
  - On `server "http"`, auth may be set at server level (applies to all routes) and overridden per `handle`/`files` block; `auth "none"` disables inherited auth for a route
  - On success, `ctx.auth` is available in all action expressions: `ctx.auth.username`, `ctx.auth.subject`, `ctx.auth.claims`
  - `username` is populated from Basic auth credentials, the introspection `username` field, or the JWT `preferred_username` claim
  - `clock_skew` (OIDC) and `cache_ttl` (OAuth2) accept a string (Go duration syntax), a plain number (seconds), or a `duration` capsule
  - On standalone `server "mcp"` with `auth "oidc"`, vinculum automatically serves `GET /.well-known/oauth-authorization-server` for MCP client OAuth2 discovery
  - New dependency: `github.com/lestrrat-go/jwx/v2` for JWKS caching and JWT validation
  - See [doc/server-auth.md](doc/server-auth.md) for the full reference

- **HTTP response functions** — new globally-available functions for building HTTP responses
  (not scoped to `handle` actions; usable from any action expression):
  - `http_response(status[, body[, headers]])` — build a response with explicit status,
    optional body (auto-coerced by type), and optional headers (`map(string)` or
    `map(list(string))`)
  - `http_redirect(url)` / `http_redirect(status, url)` — redirect response; defaults to
    302 Found
  - `http_error(status, message)` — error response with plain-text body; integrates
    naturally with `try()` for mapping errors to specific HTTP status codes
  - `addheader(response, name, value)` — return new response with header value appended
  - `removeheader(response, name)` — return new response with header removed
  - `setcookie(cookieObj)` — format a `Set-Cookie` header value from a cookie definition
    object; use with `addheader()` to attach cookies to any response
- **`mcp_usermessage()` and `mcp_assistantmessage()`** — renamed from
  `mcp_user_message()` and `mcp_assistant_message()` for naming consistency (underscore
  as namespace separator only, not word separator).

- **`basicauth(user, password)` function** — returns the `Authorization` header value for HTTP Basic authentication (`"Basic <base64(user:password)>"`); available in the new `httputil` function plugin
- **URL parsing and manipulation functions** — new `url` function plugin:
  - `urlparse(rawURL)` — parse a URL string into a URL object with named attributes (`scheme`, `host`, `hostname`, `port`, `path`, `query`, `fragment`, etc.) accessible directly (e.g. `u.scheme`, `u.path`)
  - `urljoin(base, ref)` — resolve `ref` against `base` following RFC 3986; accepts strings, URL objects, or URL capsules
  - `urljoinpath(base, elem...)` — append percent-escaped path elements to `base`
  - `urlqueryencode(params)` — encode a `map(string)` or `map(list(string))` into a query string
  - `urlquerydecode(query)` — decode a query string into `map(list(string))`
  - `urldecode(str)` — percent-decode a string (inverse of `urlencode`; `+` decoded as space)
  - `get(u, "query_param", key)` — return `list(string)` of all values for a named query parameter
  - `tostring(u)` — return the canonical URL string from a URL object
- **Enhanced `tostring()` and `length()`** — the built-in `tostring()` and `length()` functions now dispatch on rich VCL types: `tostring(b)` returns the UTF-8 content of a `bytes` value; `length(b)` returns its byte count; `tostring(u)` returns the canonical string of a URL object. Falls back to standard behavior for all other types.
- **`Stringable` and `Lengthable` interfaces** — capsule types may now implement `ToString(ctx) (string, error)` and `Length(ctx) (int64, error)` to integrate with `tostring()` and `length()` respectively. Objects carrying a `_capsule` attribute are also supported (following the same convention as `_ctx` for context propagation).
- **TLS support for `server "http"`** — add a `tls {}` sub-block to serve HTTPS. Supports file-based certificates (`cert`/`key`) and the new `self_signed = true` option for development.
- **TLS support for `server "metrics"`** (standalone mode) — add a `tls {}` sub-block to a metrics server with a `listen` address to serve the scrape endpoint over HTTPS.
- **TLS support for `server "mcp"`** (standalone mode) — add a `tls {}` sub-block to a standalone MCP server to serve the MCP Streamable HTTP endpoint over HTTPS.
- **`self_signed` TLS option** — setting `self_signed = true` in any server `tls {}` block generates an ephemeral ECDSA P-256 certificate at startup (valid for `localhost`/`127.0.0.1`). Useful for local development and integration testing; mutually exclusive with `cert`/`key`.

## [0.19.0] - 2026-03-30

### Breaking Change!

- `vinculum server` command line changed to `vinculum serve` to me more consistent with typical verb-like subcommand naming.

### Added

- **`vinculum check` command** — validates configuration files without starting any services;
  exits non-zero with diagnostics on error, exits zero with a confirmation message on success;
  accepts the same `--file-path` and `--write-path` flags as `serve`
- **`bytes` capsule type** — first-class binary data type with an optional MIME/content type:
  - `bytes(str [, content_type])` — create a `bytes` value from a UTF-8 string
  - `bytes(b [, content_type])` — re-wrap an existing `bytes` value, optionally overriding its content type
  - `get(b)` / `get(b, "utf8")` — read as UTF-8 string; `get(b, "base64")` — base64 string;
    `get(b, "len")` — byte count; `get(b, "content_type")` — MIME type
  - `base64decode(str, content_type)` — decodes to a **`bytes` capsule** (new two-arg form);
    `base64decode(str)` continues to return a string (backward compatible)
  - `base64encode(value)` — now also accepts a `bytes` capsule in addition to strings
  - `filebytes(path [, content_type])` — read a file into a `bytes` capsule (gated by `--file-path`)
  - `mcp_image()` — now accepts a `bytes` capsule as its first argument; MIME type is taken from the
    capsule's content type and may be overridden by an explicit second argument
- Variables may now optionally have a defined type and nullability.
- `sys.plugins` lists the names of all plugin components
- `sys.features` lists the names of all enabled feature flags (e.g. `"readfiles"`, `"writefiles"`, `"allowkill"`)
- `sys.signals` — platform signal table available in VCL:
  - `sys.signals.SIGXXX` → the signal number for `SIGXXX` (all signals known on the current OS)
  - `sys.signals.bynumber["N"]` → the signal name for number `N`
- **`kill(pid, signal)` VCL function** — sends a signal to a process; both arguments are integers
  (see `sys.signals` for portable signal numbers); gated by the `--allow-kill` flag
- **`sqid(id[, options])` and `unsqid(s[, options])` VCL functions** — encode/decode [sqids](https://sqids.org): short, URL-safe IDs generated from one or more non-negative integers; `id` may be a single number or a list; optional `options` object supports `alphabet`, `min_length`, and `blocklist`

### Changed

- Refactor context-building utilities into ctyutil (for possible later moving to an external repo) and hclutil
- Reorganize code and move to plugin registries for functions, servers, clients, triggers, and ambient values (env.*, httpstatus.*, sys.*)
- Feature flags are now a proper registry: `ConfigBuilder.WithFeature(name, value)` replaces the former `WithBaseDir`/`WithWriteDir` methods; `--file-path` and `--write-path` are now registered as the `"readfiles"` and `"writefiles"` features internally
- `get`, `set`, `increment`, and `observe` now accept an optional leading context argument —
  `get([ctx,] thing [, ...])` — allowing callers to propagate context into implementations;
  when omitted, `context.Background()` is used. Internal refactor: `Gettable`, `Settable`,
  `Incrementable`, and `Observable` interfaces now take a `context.Context` as their first
  parameter, consistent with `Callable`.

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
