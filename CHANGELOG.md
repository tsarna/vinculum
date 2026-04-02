# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- **`ctx.trace_id` and `ctx.span_id`** — when a request is being handled inside an active OTel span, these string variables are available in all VCL action expressions (not just HTTP); both are `""` when no span is active (NOOP tracer or no `client "otlp"` configured)

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
