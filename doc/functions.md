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

### Control Flow

- `error(message)`: Abort evaluation with the given error message. Useful for asserting preconditions in expressions.

### Messaging

- `send(ctx, subscriber, topic, payload, fields?)`: Publish a message to a bus or other subscriber. `fields` is an optional map of string metadata attached to the event. Returns `true`.
- `sendjson(ctx, subscriber, topic, payload, fields?)`: Same as `send`, but first serializes `payload` to JSON bytes before publishing.
- `sendgo(ctx, subscriber, topic, payload, fields?)`: Same as `send`, but first converts `payload` from a cty value to a Go native value (map/slice/scalar) before publishing. Use this when the subscriber expects idiomatic Go types.

### HTTP Action Functions

These functions are only available inside `handle` block action expressions in [`server "http"`](server-http.md) blocks.

- `respond(code, body)`: Write an HTTP response. `code` is the integer status code. If `body` is a string it is written as-is; otherwise it is JSON-encoded and `Content-Type: application/json` is set. Returns `true`.
- `set_header(key, value)`: Set a response header. Must be called before `respond`. Returns `true`.

### File Functions

These functions are available when Vinculum is started with a configuration directory
(which is always the case in normal use). Relative paths are resolved against the
configuration base directory.

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
