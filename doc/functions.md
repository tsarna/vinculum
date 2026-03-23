# Vinculum Built-in Functions

These are functions callable from general expression contexts: action expressions,
`const` blocks, cron actions, signal handlers, etc.

For the transform pipeline constructors used in `transforms` / `inbound_transforms` /
`outbound_transforms` attributes, see [transforms.md](transforms.md) — those are a
separate, context-specific mini-DSL, not regular functions.

## Utility Functions

These functions are available in any expression context (actions, `const` blocks, etc.).

### Logging

All logging functions accept an optional second argument for structured fields.
If a single map or object is passed as the fields argument, its keys become log field
names. If multiple positional arguments are passed, they are logged as `$1`, `$2`, etc.
All logging functions return `true`.

- `log_debug(message, fields?)`: Log at DEBUG level.
- `log_info(message, fields?)`: Log at INFO level.
- `log_warn(message, fields?)`: Log at WARN level.
- `log_error(message, fields?)`: Log at ERROR level.
- `log_msg(level, message, fields?)`: Log at the given level string (`"debug"`, `"info"`, `"warn"` / `"warning"`, `"error"`).

### Data Manipulation

- `diff(a, b)`: Compute a structural diff between two values and return it as a value. See also the `diff(old_key, new_key)` transform constructor in [transforms.md](transforms.md).
- `patch(target, patch)`: Apply a diff (as produced by `diff`) to `target` and return the patched result. Both `target` and `patch` must be objects/maps.
- `jsonencode(value)`: Encode a value as a JSON string.
- `jsondecode(json_string)`: Parse a JSON string and return the decoded value.
- `typeof(value)`: Return a string describing the type of `value` (e.g. `"string"`, `"number"`, `"bool"`, `"object"`).

### Random

- `random()`: Return a random float in `[0.0, 1.0)`.
- `randint(a, b)`: Return a random integer N such that `a <= N <= b` (both bounds inclusive).
- `randuniform(a, b)`: Return a random float N such that `a <= N <= b`.
- `randgauss(mu, sigma)`: Return a random float drawn from a Gaussian (normal) distribution
  with the given mean (`mu`) and standard deviation (`sigma`).
- `randchoice(list)`: Return a single random element from `list`. Errors if the list is empty.
- `randsample(list, k)`: Return a new list of `k` unique elements chosen at random from `list`,
  without replacement. Errors if `k` exceeds the length of `list`.
- `randshuffle(list)`: Return a shuffled copy of `list`. The original list is not modified.

### Time and Duration

VCL has two native time types — `time` (a point in time) and `duration` (a length of time). Both
support `==` and `!=`. Ordering comparisons (`<`, `>`, etc.) and require the dedicated comparison
functions listed below, because go-cty does not dispatch those operators to capsule types.

Duration strings are accepted in two formats:

- **Go format** — `5m`, `1h30m`, `2h3m4s`, `500ms`, `1.5s`. Units: `ns`, `us`, `µs`, `ms`, `s`, `m`, `h`.
- **ISO 8601 P-notation** — `PT5M`, `PT1H30M`, `P1DT12H`, `PT0.5S`. Calendar fields (`P1Y`, `P1M`)
  are rejected — they cannot be represented as a fixed duration; use `addyears()` / `addmonths()`
  (Phase 2) for calendar arithmetic.

#### Timestamp Creation

- `now()`: Return the current time in the local system timezone.
- `now(tz)`: Return the current time in the named IANA timezone, e.g. `now("UTC")`, `now("America/New_York")`.
- `parsetime(s)`: Parse an RFC 3339 timestamp string (timezone required). Accepts sub-second
  precision. Example: `parsetime("2024-01-15T10:30:00Z")`.

#### Timestamp Formatting

- `formattime(format, t)`: Format `t` using Go's reference-time layout. The reference moment is
  `Mon Jan 2 15:04:05 MST 2006`; use those specific values as placeholders in the format string.
  Example: `formattime("2006-01-02", now("UTC"))` → `"2024-01-15"`.

  A format string starting with `@` selects a predefined layout *(Phase 3)*:

  | Name | Equivalent format string | Example output |
  |------|--------------------------|----------------|
  | `@ansic` | `Mon Jan _2 15:04:05 2006` | `Mon Jan 15 10:30:00 2024` |
  | `@unixdate` | `Mon Jan _2 15:04:05 MST 2006` | `Mon Jan 15 10:30:00 UTC 2024` |
  | `@rubydate` | `Mon Jan 02 15:04:05 -0700 2006` | `Mon Jan 15 10:30:00 +0000 2024` |
  | `@rfc822` | `02 Jan 06 15:04 MST` | `15 Jan 24 10:30 UTC` |
  | `@rfc822z` | `02 Jan 06 15:04 -0700` | `15 Jan 24 10:30 +0000` |
  | `@rfc850` | `Monday, 02-Jan-06 15:04:05 MST` | `Monday, 15-Jan-24 10:30:00 UTC` |
  | `@rfc1123` | `Mon, 02 Jan 2006 15:04:05 MST` | `Mon, 15 Jan 2024 10:30:00 UTC` |
  | `@rfc1123z` | `Mon, 02 Jan 2006 15:04:05 -0700` | `Mon, 15 Jan 2024 10:30:00 +0000` |
  | `@rfc3339` | `2006-01-02T15:04:05Z07:00` | `2024-01-15T10:30:00Z` |
  | `@rfc3339nano` | `2006-01-02T15:04:05.999999999Z07:00` | `2024-01-15T10:30:00.123Z` |
  | `@kitchen` | `3:04PM` | `10:30AM` |
  | `@stamp` | `Jan _2 15:04:05` | `Jan 15 10:30:00` |
  | `@stampmilli` | `Jan _2 15:04:05.000` | `Jan 15 10:30:00.000` |
  | `@stampmicro` | `Jan _2 15:04:05.000000` | `Jan 15 10:30:00.000000` |
  | `@stampnano` | `Jan _2 15:04:05.000000000` | `Jan 15 10:30:00.000000000` |
  | `@datetime` | `2006-01-02 15:04:05` | `2024-01-15 10:30:00` |
  | `@date` | `2006-01-02` | `2024-01-15` |
  | `@time` | `15:04:05` | `10:30:00` |

  The same `@name` aliases will be accepted by `parsetime` once multi-format parsing is added.

- `formatdate(fmt, ts)`: A legacy hcl function, prefer `formattime`. Format an RFC 3339 timestamp *string*
  using a token-based format (`YYYY`, `MM`, `DD`, `HH`, `mm`, `ss`, `Z`). Operates on strings
  only; for `time` capsule values use `formattime` instead.

#### Timestamp Arithmetic

- `timeadd(ts, dur)`: Add a duration to a time. Accepts four type combinations:

  | `ts` type | `dur` type | Return type | Notes |
  |-----------|------------|-------------|-------|
  | `string`  | `string`   | `string`    | Backward-compatible: RFC 3339 in, RFC 3339 out; duration in Go format |
  | `time`    | `duration` | `time`      | Native capsule path |
  | `time`    | `string`   | `time`      | Duration string auto-parsed (Go or ISO 8601) |
  | `string`  | `duration` | `time`      | Timestamp string auto-parsed as RFC 3339 |

- `timesub(t1, t2)`: Subtract. Return type depends on argument types:
  - `timesub(time, time)` → `duration` — elapsed from `t2` to `t1`; negative if `t1 < t2`.
  - `timesub(time, duration)` → `time` — `t` minus `d`.
- `since(t)`: Duration elapsed since `t` (equivalent to `timesub(now(), t)`).
- `until(t)`: Duration remaining until `t` (equivalent to `timesub(t, now())`).

#### Timestamp Comparison

The `<`/`>`/`<=`/`>=` operators do not work on `time` values (go-cty limitation). Use:

- `timebefore(t1, t2)`: `true` if `t1` is before `t2`. *(Phase 2)*
- `timeafter(t1, t2)`: `true` if `t1` is after `t2`. *(Phase 2)*

#### Duration Creation

- `duration(s)`: Parse a duration string — Go format (`"5m30s"`) or ISO 8601 (`"PT5M30S"`).
- `duration(n, unit)`: Create a duration from a number and a unit string.
  Valid units: `"h"`, `"m"`, `"s"`, `"ms"`, `"us"`, `"ns"`. Fractional values are accepted
  (e.g. `duration(1.5, "s")` = 1500ms).

#### Duration Formatting

- `formatduration(d)`: Format `d` in Go format, e.g. `"1h30m0s"`.
- `formatduration(d, fmt)`: Format with explicit format. `fmt` is `"go"` (default) or `"iso"`
  (ISO 8601 P-notation, e.g. `"PT1H30M"`).

#### Duration Comparison

- `durationlt(d1, d2)`: `true` if `d1 < d2`. *(Phase 2)*
- `durationgt(d1, d2)`: `true` if `d1 > d2`. *(Phase 2)*

#### Examples

```hcl
# Current time
now("UTC")                                      # → time in UTC
now("America/New_York")                         # → time in Eastern timezone

# Parse and format
t = parsetime("2024-01-15T10:30:00Z")
formattime("2006-01-02", t)                     # → "2024-01-15"
formattime("15:04:05", t)                       # → "10:30:00"

# Arithmetic
timeadd(now("UTC"), duration("1h30m"))          # → 1.5 hours from now
timeadd(now("UTC"), duration(90, "m"))          # → same
timesub(ctx.end_time, ctx.start_time)           # → duration elapsed
timesub(ctx.deadline, duration("30m"))          # → 30 minutes before deadline

# Elapsed / remaining
since(sys.starttime)                            # → process uptime
since(sys.boottime)                             # → host uptime
since(ctx.start_time)                           # → duration since start
formatduration(since(ctx.start_time))           # → "5m32s"
formatduration(since(ctx.start_time), "iso")    # → "PT5M32S"

# Comparison (use functions, not operators)
timebefore(ctx.expires_at, now("UTC"))          # → true if already expired   [Phase 2]
durationgt(since(ctx.last_seen), duration(24, "h"))  # → true if inactive > 1 day [Phase 2]

# Backward-compatible string form of timeadd
timeadd("2024-01-15T10:30:00Z", "1h")          # → "2024-01-15T11:30:00Z"
```

### Variables and Metrics

These functions read and write mutable variables (`var` blocks) and Prometheus
metrics (`metric` blocks). The first argument is a `var.<name>` or `metric.<name>`
reference. Dispatch is based on the type of the first argument at runtime.

#### `get(thing, default_or_labels?)`

Returns the current value of `thing`.

- **Variable**: if the value is `null` and a second argument is provided, returns
  the second argument as the default.
- **Metric (no-label series)**: returns the current in-process accumulated value.
- **Metric (labeled series)**: pass a label object as the second argument:
  `get(metric.m, {queue = "fast"})`.

Histograms are not gettable — calling `get` on a histogram is a runtime error.

#### `set(thing, value, labels?)`

Sets the value of `thing` to `value`. Returns the new value.

- **Variable**: `set(var.counter, 0)`.
- **Metric gauge (no-label)**: `set(metric.temperature, 21.5)`.
- **Metric gauge (labeled)**: `set(metric.active_jobs, 3, {queue = "default"})`.

Calling `set` on a counter or histogram is a runtime error.

#### `increment(thing, delta, labels?)`

Adds `delta` to the current numeric value of `thing` and returns the new value.
Delta must be a number; for counters it must be ≥ 0.

- **Variable**: `increment(var.hits, 1)`.
- **Metric (no-label)**: `increment(metric.errors_total, 1)`.
- **Metric (labeled)**: `increment(metric.requests_total, 1, {method = "POST"})`.

#### `observe(metric, value, labels?)`

Records a single observation on a histogram metric. Only valid on `metric` values
of type `histogram`.

- **No-label**: `observe(metric.request_duration, elapsed)`.
- **Labeled**: `observe(metric.request_duration, elapsed, {route = "/api/v1"})`.

Calling `observe` on a gauge, counter, or variable is a runtime error.

#### Examples

```hcl
var "hits" { value = 0 }

metric "counter" "requests_total" {
    help        = "Total requests"
    label_names = ["method"]
}

metric "histogram" "request_duration_seconds" {
    help        = "Request processing time"
    label_names = ["method"]
    buckets     = [0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0]
}

# In an HTTP server handle action:
action = [
    increment(var.hits, 1),
    increment(metric.requests_total, 1, {method = ctx.method}),
    observe(metric.request_duration_seconds, ctx.elapsed, {method = ctx.method}),
    respond(httpstatus.OK, {hits = get(var.hits)}),
]
```

See [metric.md](metric.md) for the full reference on declaring and using metrics.

### Control Flow

- `error(message)`: Abort evaluation with the given error message. Useful for asserting preconditions in expressions.

### Messaging

- `send(ctx, subscriber, topic, payload, fields?)`: Publish a message to a bus or other subscriber. `fields` is an optional map of string metadata attached to the event. Returns `true`.
- `sendjson(ctx, subscriber, topic, payload, fields?)`: Same as `send`, but first serializes `payload` to JSON bytes before publishing.
- `sendgo(ctx, subscriber, topic, payload, fields?)`: Same as `send`, but first converts `payload` from a cty value to a Go native value (map/slice/scalar) before publishing. Use this when the subscriber expects idiomatic Go types.
- `call(ctx, client, request)`: Make a synchronous request to a client and return the response. Currently supported for LLM clients (`client "openai"`). Always returns a response object — API errors are represented as `stop_reason = "error"` in the response rather than Go-level errors. See [client-llm.md](client-llm.md) for the full request/response schema.
- `llm_wrap(content)`: Wrap a string in `<user_input>` XML-like delimiters as a prompt injection mitigation. Use in the `content` of user messages when the content comes from an untrusted source. The system prompt should reference the tags (e.g. `"Summarize the text in the <user_input> tags."`). Returns `"<user_input>\n{content}\n</user_input>"`.

### HTTP Action Functions

These functions are only available inside `handle` block action expressions in [`server "http"`](server-http.md) blocks.

- `respond(code, body)`: Write an HTTP response. `code` is the integer status code. If `body` is a string it is written as-is; otherwise it is JSON-encoded and `Content-Type: application/json` is set. Returns `true`.
- `set_header(key, value)`: Set a response header. Must be called before `respond`. Returns `true`.

### Path Functions

These functions manipulate file path strings and are always available regardless
of whether `--file-path` is set.

- `abspath(path)`: Convert `path` to an absolute path (resolves relative to the process working directory).
- `basename(path)`: Return the final element of `path` (the filename component).
- `dirname(path)`: Return all but the final element of `path` (the directory component).
- `pathexpand(path)`: Expand a leading `~` to the current user's home directory.

### File Functions

These functions read files from disk. They are only available when Vinculum is
started with `--file-path <dir>` (`-f <dir>`), which sets the base directory for
all file operations. Relative paths are resolved against that base directory.
The base directory is also accessible as `sys.filepath`.

- `file(path)`: Read a file and return its contents as a string.
- `filebase64(path)`: Read a file and return its contents as a base64-encoded string.
- `fileexists(path)`: Return `true` if the file exists, `false` otherwise.
- `fileset(dir, pattern)`: Return a set of file paths within `dir` that match `pattern` (glob syntax).
- `templatefile(path, vars)`: Read `path` as an HCL template, evaluate it with the
  variables in `vars` (an object/map), and return the result. Standard VCL variables
  (`env`, `sys`, `var`, `metric`, etc.) are also available in the template. Variables
  in `vars` shadow any same-named standard variables. Uses HCL's native template syntax:
  `${expr}` for interpolation, `%{ if cond }…%{ endif }` for conditionals, and
  `%{ for item in list }…%{ endfor }` for iteration. A template that consists of a
  single `${expr}` interpolation preserves the type of the expression; all others
  produce a string.
- `gotemplatefile(path, vars)`: Like `templatefile`, but uses Go's `text/template`
  syntax instead of HCL templates. Always returns a string. The template data (`.`)
  is a merged map of standard VCL variables and `vars`; entries in `vars` shadow
  same-named standard variables. Standard VCL namespaces are nested exactly as in
  VCL — `var.foo` is `{{.var.foo}}`, `env.HOME` is `{{.env.HOME}}` — while keys
  passed in `vars` are top-level: `gotemplatefile("t.tmpl", {bar: "x"})` exposes
  `{{.bar}}`. Example syntax: `{{.name}}` for interpolation,
  `{{if .flag}}…{{else}}…{{end}}` for conditionals, and
  `{{range .items}}…{{end}}` for iteration.

### File Write Functions

These functions modify files on disk. They require **both** `--file-path <dir>` **and**
`--write-path <dir>` (`-w`) to be set. `--write-path` must be equal to or a
subdirectory of `--file-path`. All paths are resolved relative to `--write-path`;
attempts to write outside that directory are rejected. The configured paths are
readable as `sys.filepath` and `sys.writepath`.

- `filewrite(path, content)`: Write `content` to `path`, creating or overwriting the file. Returns `true`.
- `fileappend(path, content)`: Append `content` to `path` (creates the file if absent). Returns `true`.

---

## MCP Functions

These functions are used in `action` expressions inside `server "mcp"` blocks.
They are available globally, so they can also be used in bus subscriptions that
construct MCP values for async handlers (future feature).

A plain string return from an action is also valid and is treated as text content —
the functions below are only needed for non-text result types.

### `mcp_image(data, mime_type)`

Returns image content for an MCP resource or tool result.

- `data` — base64-encoded image data (string)
- `mime_type` — MIME type of the image, e.g. `"image/png"` (string)

Valid in: resource and tool action expressions.

### `mcp_error(message)`

Returns an error result for an MCP tool call.

- `message` — error message to return to the caller (string)

Valid in: tool action expressions only.

### `mcp_user_message(content)`

Returns a user-role message for an MCP prompt result.

- `content` — message text (string)

Valid in: prompt action expressions.

### `mcp_assistant_message(content)`

Returns an assistant-role message for an MCP prompt result. Used to provide
few-shot examples alongside a `mcp_user_message()`.

- `content` — message text (string)

Valid in: prompt action expressions.

---

## User-defined Functions

### HCL Functions

Simple functions can be defined in HCL using the `function` block. See the
[`function` block](config.md#function) in the configuration reference for details.

### JQ Functions

Functions backed by a JQ query can be defined using the `jq` block. See the
[`jq` block](config.md#jq) in the configuration reference for details.

<!-- TODO: Complete function reference with parameters and return types -->
