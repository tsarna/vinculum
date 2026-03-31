# Vinculum Configuration Language

Vinculum is configured using [HashiCorp Configuration Language (HCL)](https://github.com/hashicorp/hcl),
the same language used by Terraform. Configuration is typically split across one or
more `.vcl` files in a directory.

## HCL Syntax

```hcl
# Comments start with #
block_type {
    attribute = value
    nested_block {
        nested_attribute = expression
    }
}

# Blocks may have zero or more labels
block_type "label" {
    nested_block "foo" "bar" {
        ...
    }
}
```

How many labels a block expects and what they mean depends on the block type.

HCL has a rich expression syntax including string interpolation, arithmetic,
conditionals, and function calls. See the
[HCL documentation](https://github.com/hashicorp/hcl/blob/main/hclsyntax/spec.md)
for the full expression language reference.

---

## Variables

### Built-in Variables

- `bus.<name>`: Each bus defined may be referenced by name. `bus.main` always exists,
  even if not declared explicitly.
- `client.<name>`: Each client defined via a `client` block may be referenced by name.
- `env.<name>`: Environment variables are exposed through the `env` object. For
  example, `env.HOME` is the value of the `HOME` environment variable.
- `httpstatus.<Name>`: Constants for each HTTP status code, using PascalCase names
  matching Go's `net/http` package. All standard 1xx–5xx codes are included. For
  example: `httpstatus.OK` → `200`, `httpstatus.NotFound` → `404`,
  `httpstatus.BadRequest` → `400`.
- `httpstatus.bycode`: A map from status code number (as a string key) to its name.
  Useful when you have a raw integer and need the canonical name. For example,
  `httpstatus.bycode["200"]` → `"OK"`, `httpstatus.bycode["404"]` → `"NotFound"`.
- `server.<name>`: Each server defined may be referenced by name. All server types
  share a single namespace — you cannot have both an HTTP server and a WebSocket
  server with the same name.
- `trigger.<name>`: Each `trigger "start"` block that evaluates an action expression
  creates a value accessible by name. All trigger types share a single name namespace.
- `sys.*`: Process and host identity facts captured at startup. All values are
  read-only. Available attributes:
  - `sys.pid` (number): Process ID of the running process.
  - `sys.hostname` (string): Hostname of the machine.
  - `sys.user` (string): Username the process is running as.
  - `sys.uid` (number): Numeric user ID.
  - `sys.group` (string): Primary group name.
  - `sys.gid` (number): Numeric primary group ID.
  - `sys.os` (string): Operating system (e.g. `"linux"`, `"darwin"`, `"windows"`).
  - `sys.arch` (string): CPU architecture (e.g. `"amd64"`, `"arm64"`).
  - `sys.cpus` (number): Number of logical CPUs available.
  - `sys.executable` (string): Path to the running executable.
  - `sys.cwd` (string): Current working directory at startup.
  - `sys.homedir` (string): Home directory of the current user.
  - `sys.tempdir` (string): Default directory for temporary files.
  - `sys.filepath` (string): The value of the `--file-path` flag passed on the
    command line, or empty string if it was not specified. This is the base
    directory used by the file read and write functions.
    See [File Functions](functions.md#file-functions) for details.
  - `sys.writepath` (string): The value of the `--write-path` flag (`-w`), or
    empty string if not set. This is the base directory used by the `filewrite`
    and `fileappend` functions; it must be within `sys.filepath`. See
    [File Write Functions](functions.md#file-write-functions) for details.
  - `sys.starttime` (time): Approximate process start time, captured once when
    the process loads. Use with
    `since(sys.starttime)` to compute process uptime.
  - `sys.boottime` (time): Approximate system boot time. On macOS this is exact
    (via `kern.boottime` sysctl); on Linux it is accurate to ±1 second (via
    `sysinfo(2)`). On other platforms it falls back to `sys.starttime`. Use with
    `since(sys.boottime)` to compute host uptime.
  - `sys.plugins` (list of string): Names of all registered plugin components,
    e.g. `["ambient.sys", "client.kafka", "functions.kill", "server.mcp"]`.
    Useful for introspection and conditional logic.
  - `sys.features` (list of string): Names of all enabled feature flags. Each
    CLI flag that gates optional capabilities registers a feature name:
    `"readfiles"` (`--file-path`), `"writefiles"` (`--write-path`),
    `"allowkill"` (`--allow-kill`). Use `contains(sys.features, "allowkill")` to
    branch on feature availability.
  - `sys.signals`: Platform signal table. Each attribute `sys.signals.SIGXXX` is
    the integer number for signal `SIGXXX` on the current OS. The set of available
    signals is OS-dependent (all signals enumerated by the OS for numbers 1–64 are
    included). Use this instead of hardcoding platform-specific numbers:

    ```hcl
    kill(sys.pid, sys.signals.SIGUSR1)   # portable signal reference
    ```

    - `sys.signals.bynumber`: A `map(string)` keyed by signal number (as a string)
      mapping back to the signal name. E.g. `sys.signals.bynumber["9"]` → `"SIGKILL"`.
      HCL coerces integer literals to string keys, so `sys.signals.bynumber[9]` also
      works.
- `var.<name>`: Each variable defined via a `var` block may be referenced by name.
  Variables are mutable and goroutine-safe; use `get()`, `set()`, and `increment()`
  to read and write their values.

### Context Variables

In many contexts, particularly when evaluating `action` expressions, `ctx` is an
object representing the current execution context. The exact attributes it provides
depend on the context and are described alongside the relevant block type. Some
functions (such as `send`) require `ctx` to be passed as a parameter for
observability purposes.

### User-defined Constants and Variables

Users may define their own constants using the `const` block, and mutable runtime
variables using the `var` block. Both are described in the Block Reference below.

---

## Block Ordering

Declaration order does not matter. Vinculum automatically determines the correct
initialization order by analysing dependencies between blocks — similar to how
Terraform handles resource dependencies. For example, you can declare a
`subscription` before the `bus` it targets, and vinculum will ensure the bus is
initialized first.

If a circular dependency is detected, vinculum reports an error at startup rather
than silently processing blocks in an incorrect order.

---

## Block Reference

### `assert`

```hcl
assert "name" {
    condition = expression
}
```

Checks that `condition` is true at startup; aborts if not. Primarily intended for
internal test cases, but can also be used in user configurations to validate that
required environment variables are set or have sensible values.

The `name` label is included in the error message if the assertion fails.

---

### `bus`

```hcl
bus "name" {
    queue_size = 1000  # optional, default 1000
}
```

Declares an event bus. The bus is available in expressions as `bus.<name>`. For
example, `bus "foo" {}` creates `bus.foo`.

`queue_size` controls the maximum number of messages that can be queued before
messages start being dropped.

`bus.main` always exists implicitly and does not need to be declared.

---

### `const`

```hcl
const {
    pi           = 3.14159
    greeting     = "Hello"
    some_numbers = [1, 2, 3]
}
```

Defines named constants available in all expressions. Attributes are evaluated once
at startup. Multiple `const` blocks are merged.

Expressions in `const` may reference other constants, environment variables, HTTP
status codes, and most functions including user-defined and JQ functions.

---

### `trigger`

```hcl
trigger "type" "name" {
    disabled = false  # optional
    ...
}
```

Defines a lifecycle trigger. The `type` label determines when and how the trigger
fires. All trigger types share a single name namespace — you cannot have two triggers
with the same name regardless of type. `disabled`, if true, causes the block to be
skipped entirely.

For details on each trigger type, see [Trigger Reference](trigger.md):

- [`trigger "cron"`](trigger.md#trigger-cron) — cron-style scheduled actions
- [`trigger "shutdown"`](trigger.md#trigger-shutdown) — action evaluated during graceful shutdown
- [`trigger "signals"`](trigger.md#trigger-signals) — OS signal handlers (SIGHUP, SIGUSR1, etc.)
- [`trigger "start"`](trigger.md#trigger-start) — action evaluated once at startup

---

### `function`

```hcl
function "name" {
    params         = [a, b]  # list of parameter names (not strings)
    variadic_param = rest    # optional: collects extra arguments into a list
    result         = expression
}
```

Defines a user-callable function using the
[HCL userfunc extension](https://pkg.go.dev/github.com/hashicorp/hcl/v2/ext/userfunc).
The function is available by name in all expressions after it is defined.

Note that `params` takes variable names, not strings:

```hcl
function "circle_area" {
    params = [radius]
    result = 3.14159 * radius * radius
}

const {
    unit_area = circle_area(1.0)
}
```

---

### `jq`

```hcl
jq "name" {
    params = [param1, param2]  # optional
    query  = "jq expression"
}
```

Defines a function backed by a [JQ](https://jqlang.org/) query, using the
[hcl-jqfunc](https://github.com/tsarna/hcl-jqfunc) extension.

The resulting function takes an input value as its first argument, followed by any
declared `params`. Parameters are available inside the query with a `$` prefix.

**String input:** if the input is a string it is parsed as JSON, the query runs on
the parsed value, and the result is re-encoded as a JSON string. Exception: if the
result is a single string it is returned as-is (not double-encoded), since the common
use case is extracting a single field.

**Non-string input:** maps, lists, objects, etc. are passed through as HCL values and
the result is returned as an HCL value, with no JSON encoding step.

```hcl
jq "calculate_price" {
    params = [tax_rate, discount]
    query  = ".price * (1 + $tax_rate) * (1 - $discount)"
}

const {
    price = calculate_price("{\"price\": 1.23}", 0.06, 0.10)
}
```

---

### `client`

```hcl
client "type" "name" {
    disabled = false  # optional
    ...
}
```

Defines a client connection to an external service. The `type` label determines
the client type. The client is available in expressions as `client.<name>`. All
clients share a single name namespace.

`disabled`, if true, causes the block to be skipped entirely.

For details on each client type, see the dedicated pages:

- [`client "kafka"`](client-kafka.md) — Apache Kafka producer and consumer
- [`client "openai"`](client-llm.md) — OpenAI and OpenAI-compatible LLM APIs
- [`client "vws"`](server-vws.md#client-vws) — Vinculum WebSocket Protocol client

---

### `server`

```hcl
server "type" "name" {
    disabled = false  # optional
    ...
}
```

Defines a network server. The `type` label determines the server type; see the
server-type sections below. The server is available in expressions as `server.<name>`.
All server types share a single name namespace.

`disabled`, if true, causes the block to be skipped entirely.

For details on each server type, see the dedicated pages:

- [`server "http"`](server-http.md) — HTTP request handlers and static file serving
- [`server "websocket"`](server-websocket.md) — Simple WebSocket server (raw frames)
- [`server "vws"`](server-vws.md) — Vinculum WebSocket Protocol server
- [`server "mcp"`](server-mcp.md) — Model Context Protocol server

---

### `subscription`

```hcl
subscription "name" {
    target     = bus.main       # required: bus or client to subscribe to
    topics     = ["topic/#"]    # required: MQTT-style topic patterns

    action     = expression     # evaluate an expression for each message
    # OR
    subscriber = server.something  # forward messages to a subscriber

    transforms = [...]          # optional: transform pipeline
    queue_size = 100            # optional: async queue depth
    disabled   = false          # optional
}
```

Subscribes to messages from a bus (or client) and either evaluates an `action`
expression for each message or forwards messages to another subscriber.

Exactly one of `action` or `subscriber` must be specified.

#### Attributes

- `target` — the bus or client to subscribe to (e.g. `bus.main`, `bus.events`). Required.
- `topics` — list of MQTT-style topic patterns to subscribe to. `+` matches one
  segment, `#` matches any number of trailing segments. Required for bus targets.
- `action` — expression evaluated once per message. See context variables below.
- `subscriber` — instead of evaluating an expression, forward messages directly to
  another subscriber. Can be a bus (`bus.other`) or a server that acts as a subscriber.
- `transforms` — transform pipeline applied to messages before they reach the action
  or subscriber. See [transforms.md](transforms.md).
- `queue_size` — if set, wraps the subscriber in an async queue of this depth,
  decoupling the publisher from the action so slow actions don't block the bus.
- `disabled` — if true, the block is skipped entirely.

#### Action Context Variables

When `action` is used, `ctx` provides:

| Variable | Description |
|---|---|
| `ctx.topic` | Topic of the received message |
| `ctx.msg` | Message payload |
| `ctx.fields` | Map of string metadata fields attached to the message (only present if the message has fields) |

#### Examples

Log every message on `events/#`:

```hcl
subscription "logger" {
    target = bus.main
    topics = ["events/#"]
    action = loginfo("received", {topic = ctx.topic, msg = ctx.msg})
}
```

Forward processed messages to another bus:

```hcl
subscription "forwarder" {
    target     = bus.raw
    topics     = ["sensors/#"]
    transforms = [
        jq("select(.value != null)"),
        add_topic_prefix("clean/"),
    ]
    subscriber = bus.main
}
```

Bridge a VWS client's inbound messages to a bus (client as target):

```hcl
client "vws" "upstream" {
    url = "ws://hub.internal:9000/events"
}

subscription "from_upstream" {
    target     = client.upstream
    topics     = ["#"]
    action     = send(ctx, bus.main, ctx.topic, ctx.msg)
}
```

---

### `var`

```hcl
var "name" {
    type     = "typename"  # optional; if set, enforces value type
    nullable = bool        # optional; default true; if false, null is rejected
    value    = expression  # optional; defaults to null
}
```

Declares a mutable variable. The variable is available in expressions as `var.<name>`.
For example, `var "counter" {}` creates `var.counter`.

Unlike `const`, variables are not static — their value can be changed at runtime using
`set()` and `increment()`. Variables are goroutine-safe and may be read and written
from concurrent subscription handlers and cron jobs.

The optional `value` attribute sets the initial value at startup. If omitted, the
variable starts as `null`.

The optional `type` attribute constrains the variable to values of a specific type.
The value must be a string matching the cty type's friendly name, such as `"number"`,
`"string"`, or `"bool"`. If set, any attempt to `set()` the variable to an
incompatible type returns an error.

The optional `nullable` attribute controls whether `null` is a valid value. It
defaults to `true`. If set to `false`, any attempt to `set()` the variable to `null`
(including calling `set()` with no value argument) returns an error. `nullable =
false` may be combined with or used independently of `type`.

Use `get()`, `set()`, and `increment()` to access and modify variables at runtime;
see [functions.md](functions.md#variables) for details.

Example — count received messages and log a warning every 100:

```hcl
var "message_count" {
    value = 0
}

subscription "counter" {
    target = bus.main
    topics = ["#"]
    action = [
        increment(var.message_count, 1),
        get(var.message_count) % 100 == 0 ? logwarn("milestone", {count = get(var.message_count)}) : true,
    ]
}
```

---

## TLS

The `tls {}` sub-block is supported on several server and client blocks to configure
transport security. The same block type is used in both directions; the set of
relevant attributes differs slightly between clients and servers.

```hcl
tls {
    enabled     = true

    # --- server-side ---
    cert        = "/etc/certs/server.crt"   # server certificate (PEM)
    key         = "/etc/certs/server.key"   # server private key (PEM)
    # OR generate an ephemeral self-signed cert (testing/development only):
    self_signed = true

    # optional — require clients to present a certificate (mTLS)
    ca_cert             = "/etc/certs/ca.crt"
    require_client_cert = true

    # --- client-side ---
    ca_cert              = "/etc/certs/ca.crt"   # verify the server's certificate
    cert                 = "/etc/certs/client.crt"  # present a client cert (mTLS)
    key                  = "/etc/certs/client.key"
    insecure_skip_verify = false                    # skip server cert verification
}
```

### TLS Attributes

| Attribute | Type | Side | Description |
|---|---|---|---|
| `enabled` | bool | both | Must be `true` for TLS to take effect. Default: `false`. |
| `cert` | string | both | Path to a PEM-encoded certificate file. Required on servers (unless `self_signed = true`); optional on clients (mTLS). |
| `key` | string | both | Path to the private key corresponding to `cert`. Required whenever `cert` is set. |
| `self_signed` | bool | server | Generate an ephemeral self-signed ECDSA certificate at startup. Mutually exclusive with `cert`/`key`. For development and testing only. |
| `ca_cert` | string | both | Path to a PEM-encoded CA certificate. On servers, used to verify client certificates. On clients, used to verify the server certificate. |
| `require_client_cert` | bool | server | Require and verify a client certificate (mTLS). Default: `false`. |
| `insecure_skip_verify` | bool | client | Skip server certificate verification. Not recommended outside of testing. Default: `false`. |

Relative paths for `cert`, `key`, and `ca_cert` are resolved against the
`--file-path` base directory.

### `self_signed`

When `self_signed = true`, vinculum generates an ephemeral ECDSA P-256 certificate
at process startup. The certificate is valid for `localhost` and `127.0.0.1` and
expires after one year. A new certificate is generated on every restart.

This is intended for local development and integration testing where a real
certificate is not available. Do not use in production.

```hcl
tls {
    enabled     = true
    self_signed = true
}
```
