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
