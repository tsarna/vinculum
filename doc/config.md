# Vinculum Configuration Language

Vinculum is configured using [HashiCorp Configuration Language (HCL)](https://github.com/hashicorp/hcl),
the same language used by Terraform. Configuration is typically split across one or
more `.vcl` files in a directory.

A directory may also contain [functy](functy.md) (`.cty`) files: their functions and
top-level `var`/`const` declarations join the same namespace and evaluation context as
the `.vcl` files. functy is a more expressive alternative to `function` blocks. See [functy.md](functy.md).

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
- `http_status.<Name>`: Constants for each HTTP status code, using PascalCase names
  matching Go's `net/http` package. All standard 1xx–5xx codes are included. For
  example: `http_status.OK` → `200`, `http_status.NotFound` → `404`,
  `http_status.BadRequest` → `400`.
- `http_status.bycode`: A map from status code number (as a string key) to its name.
  Useful when you have a raw integer and need the canonical name. For example,
  `http_status.bycode["200"]` → `"OK"`, `http_status.bycode["404"]` → `"NotFound"`.
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
  - `sys.version` (string): Vinculum release version (e.g. `"v1.2.3"`), or `"dev"`
    for a local build.
  - `sys.commit` (string): Git commit SHA the binary was built from, or empty if
    unknown.
  - `sys.build_time` (string): Build/commit timestamp (RFC 3339), or empty if unknown.
  - `sys.modified` (bool): True if the working tree had uncommitted changes at build
    time.
  - `sys.functy.version` (string): Version of the bundled [functy](functy.md) (`.cty`)
    language (e.g. `"v0.11.0"`), read from the binary's build info — `"(devel)"` in a
    workspace build, `""` if unavailable. Only the module version is recorded for a
    dependency; functy's own commit and build time are not (the `sys.commit` /
    `sys.build_time` above describe the Vinculum binary, not functy).
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
    `time::since(sys.starttime)` to compute process uptime.
  - `sys.boottime` (time): Approximate system boot time. On macOS this is exact
    (via `kern.boottime` sysctl); on Linux it is accurate to ±1 second (via
    `sysinfo(2)`). On other platforms it falls back to `sys.starttime`. Use with
    `time::since(sys.boottime)` to compute host uptime.
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

#### Attributes

<!-- vinculum:begin block-attrs assert level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `condition` | bool (expression) | yes | Expression that must evaluate to true. |

**`condition`**

Evaluated once, while the configuration is loaded — not at runtime.

<!-- vinculum:end block-attrs assert -->

---

### `bus`

```hcl
bus "name" {
    queue_size = 1000          # optional, default 1000
    metrics    = server.prom   # optional; auto-wires to default server "metrics"
    tracing    = client.tracer # optional; auto-wires to default client "otlp"
}
```

Declares an event bus. The bus is available in expressions as `bus.<name>`. For
example, `bus "foo" {}` creates `bus.foo`.

`bus.main` always exists implicitly and does not need to be declared.

#### Attributes

<!-- vinculum:begin block-attrs bus level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `metrics` | expression (metrics-ref) |  | Where to report metrics. |
| `queue_size` | number |  | Maximum messages queued before messages are dropped. |
| `tracing` | expression (tracing-ref) |  | Where to report bus traces. |
| `type` | string |  | Bus implementation to use. |

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`queue_size`**

Defaults to 1000.

**`tracing`**

A `client "otlp"` block. When set, each publish and delivery is wrapped in an OTel span. Auto-wires to the default when omitted.

**`type`**

Reserved for alternative bus implementations; omit for the default in-process bus.

<!-- vinculum:end block-attrs bus -->

#### Tracing

When `tracing` is configured (or auto-wired to a `client "otlp"` block), each
publish and delivery is wrapped in an OTel span:

- **`Publish`** — a `SpanKindProducer` span (`publish <topic>`) is created when
  a message is enqueued. The span context flows with the message.
- **`PublishSync`** — same producer span, plus a `SpanKindConsumer` child span
  (`process <topic>`) per subscriber, giving a complete trace tree:
  `publish → process → subscriber work`.
- **Async delivery** — each subscriber delivery creates a new root
  `SpanKindConsumer` span linked to the publish span, per
  [OTel messaging semantic conventions](https://opentelemetry.io/docs/specs/otel/trace/semantic_conventions/messaging/)
  for async pub/sub.

All bus spans carry `messaging.system = "vinculum"`, `messaging.destination.name`,
`messaging.operation.type`, `messaging.operation.name`, and `vinculum.bus.name`
attributes. See [`client "otlp"`](client-otlp.md) for tracing configuration and
auto-wiring rules.

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

<!-- vinculum:begin block-index trigger level=3 -->

- [`trigger "after"`](trigger.md#trigger-after) — Waits a fixed duration after startup, then fires once.
- [`trigger "at"`](trigger.md#trigger-at) — Fires at a computed absolute time, rescheduling after each firing.
- [`trigger "cron"`](trigger.md#trigger-cron) — A cron-style scheduler holding one or more scheduled rules.
- [`trigger "file"`](trigger.md#trigger-file) — Fires in response to filesystem events. (available only in some configurations)
- [`trigger "interval"`](trigger.md#trigger-interval) — Repeatedly evaluates an action on a dynamic schedule.
- [`trigger "once"`](trigger.md#trigger-once) — Evaluates an action lazily, at most once, and caches the result.
- [`trigger "shutdown"`](trigger.md#trigger-shutdown) — Evaluates an action once during graceful shutdown.
- [`trigger "signals"`](trigger.md#trigger-signals) — Maps OS signals to actions.
- [`trigger "start"`](trigger.md#trigger-start) — Evaluates an action once at startup.
- [`trigger "watch"`](trigger.md#trigger-watch) — Fires each time a watched value changes.
- [`trigger "watchdog"`](trigger.md#trigger-watchdog) — Fires when a window elapses without being fed.

<!-- vinculum:end block-index trigger -->

---

### `function`

> For anything beyond a single expression — typed parameters, locals, branching,
> loops, or error handling — [functy (`.cty`) files](functy.md) are a more
> expressive alternative, and a `func` there is callable from VCL exactly like a
> `function` block.

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

#### Attributes

<!-- vinculum:begin block-attrs function level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `params` | expression | yes | Parameter names, as identifiers rather than strings. |
| `result` | expression | yes | Expression the function returns. |
| `variadic_param` | expression |  | Name that collects any extra arguments into a list. |

**`params`**

Write `params = [a, b]`, not `params = ["a", "b"]`. Each name is in scope in `result`.

**`result`**

Evaluated with the parameter names in scope.

<!-- vinculum:end block-attrs function -->

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

#### Attributes

<!-- vinculum:begin block-attrs jq level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `query` | string | yes | The jq query to evaluate. |
| `params` | expression |  | Parameter names, as identifiers rather than strings. |

**`query`**

Runs against the function's first argument.

**`params`**

Each becomes `$name` inside the query.

<!-- vinculum:end block-attrs jq -->

#### Input and result

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

### `editor`

Defines a callable function that performs structured text editing using ordered
match-and-replace rules. See [editor.md](editor.md) for the full reference.

```hcl
editor "line" "name" {
    params         = [param1, param2]
    variadic_param = rest

    match "regex" {
        replace = expr
    }
}
```

<!-- vinculum:begin block-index editor level=3 -->

- [`editor "line"`](editor.md) — Edits text line by line with ordered regex rules.

<!-- vinculum:end block-index editor -->

---

### `procedure`

See [procedure.md](procedure.md).

> **Deprecated** in favor of [functy (`.cty`) files](functy.md); loading a `procedure`
> block emits a deprecation warning and the block will be removed in a future release.

---

### `condition`

```hcl
condition "type" "name" {
    input = expression
    ...
}
```

Declares a named boolean derived from an input and a set of behavioral rules —
debouncing, hysteresis, counting, or latching. Conditions are composable: one
condition's `input` may be another. The result is available as `condition.<name>`
and is observable with `trigger "watch"`.

For details on each condition type, see [Condition Reference](condition.md):

<!-- vinculum:begin block-index condition level=3 -->

- [`condition "counter"`](condition.md#condition-counter-name) — Counts events and activates when the count reaches a preset.
- [`condition "flipflop"`](condition.md#condition-flipflop-name) — Edge-driven bistable: T, SR, JK, D, D-latch, and gated variants.
- [`condition "threshold"`](condition.md#condition-threshold-name) — Derives a boolean from a numeric input, with hysteresis.
- [`condition "timer"`](condition.md#condition-timer-name) — Applies temporal rules to a boolean signal.

<!-- vinculum:end block-index condition -->

---

### `fsm`

<!-- vinculum:begin block-synopsis fsm -->

```hcl
fsm "<name>" {
    initial        = string  # required
    disabled       = bool
    on_change      = expression
    on_error       = expression
    queue_size     = number
    shutdown_event = string
    tracing        = expression

    event "<name>" { … }  # 0..n

    state "<name>" { … }  # 0..n

    storage { … }  # optional
}
```

<!-- vinculum:end block-synopsis fsm -->

Declares a finite state machine with named states, events, and transitions.
The machine is available in expressions as `fsm.<name>` and acts as a subscriber,
so a `subscription` can drive it directly.

See [fsm.md](fsm.md) for the full reference.

---

### `metric`

```hcl
metric "type" "name" {
    description = "..."
    ...
}
```

Declares an application metric reported via OpenTelemetry, exposed through
`server "metrics"` or pushed via `client "otlp"`.

For details on each metric type, see [Metric Reference](metric.md):

<!-- vinculum:begin block-index metric level=3 -->

- [`metric "counter"`](metric.md#counter) — A monotonically increasing value.
- [`metric "gauge"`](metric.md#gauge) — A value that can go up and down.
- [`metric "histogram"`](metric.md#histogram) — Sample observations bucketed by value.

<!-- vinculum:end block-index metric -->

---

### `wire_format`

```hcl
wire_format "type" "name" {
    ...
}
```

Declares a named encoder/decoder for message payloads, referenced by clients and
servers that carry structured messages.

For details on each wire format, see the dedicated pages:

<!-- vinculum:begin block-index wire_format level=3 -->

- [`wire_format "protobuf"`](wire-format-protobuf.md) — Protocol Buffers binary, decoded and encoded against a supplied schema.

<!-- vinculum:end block-index wire_format -->

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

<!-- vinculum:begin block-index client level=3 -->

- [`client "aws"`](client-sqs.md#client-aws-name) — Shared AWS credentials and region for other AWS clients.
- [`client "http"`](client-http.md) — An HTTP(S) client for making outbound requests.
- [`client "kafka"`](client-kafka.md) — A Kafka client bridging Kafka topics to the bus.
- [`client "mqtt"`](client-mqtt.md) — An MQTT client bridging an MQTT broker to the bus.
- [`client "mysql"`](client-sql.md#client-mysql-name) — A MySQL or MariaDB database client.
- [`client "openai"`](client-llm.md#client-openai-name) — A client for an OpenAI-compatible chat completion API.
- [`client "otlp"`](client-otlp.md) — An OpenTelemetry exporter for traces and metrics.
- [`client "postgres"`](client-sql.md#client-postgres-name) — A PostgreSQL database client.
- [`client "rabbitmq"`](client-rabbitmq.md) — A RabbitMQ (AMQP 0-9-1) client bridging exchanges and queues to the bus.
- [`client "redis"`](client-redis.md#client-redis-name) — A Redis connection shared by the Redis key/value, pub/sub, and stream clients.
- [`client "redis_kv"`](client-redis.md#client-redis_kv-name) — Redis-backed key/value storage.
- [`client "redis_pubsub"`](client-redis.md#client-redis_pubsub-name) — A Redis pub/sub client bridging channels to the bus.
- [`client "redis_stream"`](client-redis.md#client-redis_stream-name) — A Redis Streams client bridging streams to the bus.
- [`client "sns_sender"`](client-sns.md#client-sns_sender-name) — Publishes messages to an Amazon SNS topic.
- [`client "sqlite"`](client-sql.md#client-sqlite-name) — A SQLite database client.
- [`client "sqs_receiver"`](client-sqs.md#client-sqs_receiver-name) — Receives messages from an Amazon SQS queue.
- [`client "sqs_sender"`](client-sqs.md#client-sqs_sender-name) — Sends messages to an Amazon SQS queue.
- [`client "vws"`](server-vws.md#client-vws) — A client connection to a Vinculum (VWS) WebSocket server.

<!-- vinculum:end block-index client -->

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

<!-- vinculum:begin block-index server level=3 -->

- [`server "http"`](server-http.md) — An HTTP server exposing request handlers and static files.
- [`server "mcp"`](server-mcp.md) — A Model Context Protocol server.
- [`server "metrics"`](server-metrics.md) — A Prometheus-style metrics endpoint.
- [`server "vws"`](server-vws.md#server-vws) — A WebSocket server speaking the Vinculum (VWS) protocol.
- [`server "websocket"`](server-websocket.md) — A WebSocket server that pushes bus messages as raw frames.

<!-- vinculum:end block-index server -->

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

The `subscriber`/`action`/`transforms`/`queue_size` set of attributes is a
shared delivery-target pattern: the same four attributes, with identical
semantics, are also accepted by every client *receiver* block
(`client "sqs_receiver"`, `client "kafka"` receivers, `client "mqtt"`
subscribers, `client "redis_stream"` consumers, `client "redis_pubsub"`
subscribers). The attribute reference below applies in all of those contexts.

#### Attributes

<!-- vinculum:begin block-attrs subscription level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `topics` | list (topic-pattern) | yes | Topic patterns to subscribe to. |
| `action` | expression (action-expression) |  | Expression evaluated once per message. |
| `disabled` | bool |  | Skip this block entirely. |
| `queue_size` | number |  | Depth of an async queue wrapping the subscriber. |
| `subscriber` | expression (subscriber-ref) |  | Subscriber to forward messages to, instead of evaluating an action. |
| `target` | expression (bus-ref) |  | Bus to subscribe to. |
| `transforms` | expression (transform-pipeline) |  | Transform pipeline applied before the action or subscriber. |

- Specify at most one of action or subscriber.
- Specify either an action to evaluate or a subscriber to forward to.

**`topics`**

MQTT-style patterns: `+` matches one segment, `#` matches any number of trailing segments.

**`action`**

`ctx.topic` is the message topic, `ctx.msg` the payload, and `ctx.fields` any string metadata attached to it.

Evaluated against the `message` context.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`queue_size`**

When set, decouples delivery from the action so a slow action does not block the source.

**`subscriber`**

Anything that can receive messages: a bus, an FSM, a subscriber-implementing server or client.

**`target`**

A bus — `bus.main`, `bus.events`. Defaults to `bus.main`. Unlike `subscriber`, this slot resolves an event bus and nothing else.

**`transforms`**

A list of transform functions applied in order to each message. Only transform functions are in scope here.

<!-- vinculum:end block-attrs subscription -->

See [transforms.md](transforms.md) for the transform pipeline DSL.

#### Action Context Variables

When `action` is used, `ctx` provides:

<!-- vinculum:begin block-ctx subscription action level=4 -->

Fields readable as `ctx.<name>` (shape `message`):

| Field | Type | Description |
|---|---|:---|
| `ctx.topic` | string | Topic the message was delivered on. |
| `ctx.msg` | dynamic | The message payload. |
| `ctx.fields` | object | String metadata attached to the message. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

<!-- vinculum:end block-ctx subscription action -->

#### Examples

Log every message on `events/#`:

```hcl
subscription "logger" {
    target = bus.main
    topics = ["events/#"]
    action = log::info("received", {topic = ctx.topic, msg = ctx.msg})
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
    type     = typespec    # optional; if set, enforces value type
    nullable = bool        # optional; default true; if false, null is rejected
    value    = expression  # optional; defaults to null
}
```

Declares a mutable variable. The variable is available in expressions as `var.<name>`.
For example, `var "counter" {}` creates `var.counter`.

Unlike `const`, variables are not static — their value can be changed at runtime using
`set()` and `increment()`. Variables are goroutine-safe and may be read and written
from concurrent subscription handlers and cron jobs.

Use `get()`, `set()`, and `increment()` to access and modify variables at runtime;
see [functions.md](functions.md#variables) for details. The `type` attribute uses the
[functy type grammar](functy.md#types).

#### Attributes

<!-- vinculum:begin block-attrs var level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `nullable` | bool |  | Whether null is a valid value. |
| `type` | expression |  | Type the variable is constrained to. |
| `value` | expression |  | Initial value. |

**`nullable`**

Defaults to true. When false, `set()` to null (including `set()` with no value) fails. May be combined with `type` or used on its own.

**`type`**

A functy type spec — `number`, `list(string)`,
`object({ host = string, port = number })`, or a host-registered named type such
as `bus`. A `set()` of an incompatible value fails; values are coerced where
the grammar allows.

The older quoted form (`type = "number"`) still works but is deprecated and warns.

**`value`**

Evaluated at startup. The variable starts as `null` when omitted.

<!-- vinculum:end block-attrs var -->

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
        get(var.message_count) % 100 == 0 ? log::warn("milestone", {count = get(var.message_count)}) : true,
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

<!-- vinculum:begin block-attrs client mqtt tls level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `ca_cert` | string |  | PEM file of CA certificates to trust. |
| `cert` | string |  | PEM file holding this side's certificate. |
| `enabled` | bool |  | Turn TLS on. |
| `insecure_skip_verify` | bool |  | Accept any server certificate without verifying it. |
| `key` | string |  | PEM file holding the private key for `cert`. |
| `require_client_cert` | bool |  | Require clients to present a certificate. |
| `self_signed` | bool |  | Generate a self-signed certificate at startup. |

- cert and key must be specified together.
- Specify at most one of self_signed or cert.

**`ca_cert`**

On a client, verifies the server's certificate. On a server, verifies presented client certificates.

**`enabled`**

Nothing else in the block takes effect while this is false.

**`insecure_skip_verify`**

Client-side only, and unsafe outside development.

**`require_client_cert`**

Server-side only; verified against `ca_cert`.

**`self_signed`**

Server-side only, for development. Mutually exclusive with `cert`/`key`.

<!-- vinculum:end block-attrs client mqtt tls -->

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
