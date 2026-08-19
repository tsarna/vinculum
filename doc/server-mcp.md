# MCP Server

Vinculum can act as a [Model Context Protocol (MCP)](https://modelcontextprotocol.io/)
server, exposing resources, tools, and prompts to MCP clients such as AI assistants.

## Server Block

```hcl
server "mcp" "name" {
    server_name = "My Server"

    resource ...
    tool ...
    prompt ...
}

server "http" "main" {
    listen = ":8080"

    handle "/mcp" {
        handler = server.name
    }
}
```

The server uses the [Streamable HTTP transport](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/transports/#streamable-http)
(MCP spec 2025-03-26).

An MCP block owns no socket: it is always mounted on a route of a
`server "http"` block, with `handler = server.<name>` — see
[Mounting under HTTP](#mounting-under-http). Everything about the connection
belongs to that block rather than to this one: the listen address, TLS,
[authentication](auth.md), the request log, `real_ip`, host-scoped
routing, and draining on shutdown. One HTTP server can host an MCP endpoint
alongside ordinary routes and static files.

<!-- vinculum:begin block-attrs server mcp level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `disabled` | bool |  |  | Skip this block entirely. |
| `metrics` | expression (metrics-ref) |  |  | Where to report metrics. |
| `server_name` | string |  | `<name>` | Name reported to clients during initialization. |
| `server_version` | string |  | `0.0.0` | Version reported to clients during initialization. |
| `tracing` | expression (tracing-ref) |  |  | Where to report request traces. |

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`tracing`**

A `client "otlp"` block. Spans follow the GenAI/MCP semantic conventions. Auto-wires to the default when omitted.

### Blocks

- `prompt "<name>"` (0..n) — A reusable prompt template clients can render.
- `resource "<uri>"` (0..n) — Data the server exposes to clients.
- `tool "<name>"` (0..n) — An operation the model can invoke.

<!-- vinculum:end block-attrs server mcp -->

---

## Resources

Resources expose data to MCP clients. Both static URIs and templated URIs are
supported.

The block label is the resource URI.

### Static URI

```hcl
resource "status://current" {
    name   = "Status"
    action = "OK"
}
```

### Templated URI

Curly-brace placeholders in the URI make it a template. Each one is captured and
arrives as `ctx.args.<name>`, the same way tool and prompt arguments do:

```hcl
resource "db://records/{table}/{id}" {
    name   = "Record"
    action = jsonencode(dbquery(ctx.args.table, ctx.args.id))
}
```

### Resource attributes

<!-- vinculum:begin block-attrs server mcp resource level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `name` | string | yes | Display name shown to clients. |
| `action` | expression (action-expression) |  | Expression evaluated when a client reads the resource. |
| `description` | string |  | What the resource holds, shown to the model. |
| `disabled` | bool |  | Skip this resource entirely. |
| `mime_type` | string |  | Content type of the resource's contents. |

**`action`**

Required unless the resource is disabled. Its value becomes the contents, and must be a string — served as-is under `mime_type` — or an `mcp::image()`. Wrap structured data in `jsonencode()`; anything else is an error at request time. `ctx.uri` is the resolved URI and `ctx.args` holds any template placeholders.

Evaluated against the `mcp-resource` context.

**`disabled`**

The resource is not registered, so clients never see it.

<!-- vinculum:end block-attrs server mcp resource -->

### Resource action context

<!-- vinculum:begin block-ctx server mcp resource action level=4 -->

Fields readable as `ctx.<name>` (shape `mcp-resource`):

| Field | Type | Description |
|---|---|:---|
| `ctx.server_name` | string | Name of the enclosing `server "mcp"` block. |
| `ctx.uri` | string | The URI that was requested. |
| `ctx.args` | object | Variables captured from the URI template. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.uri`**

The concrete URI, not the template — `"file:///logs/app.log"` rather than `"file:///logs/{name}"`.

**`ctx.args`**

Empty for a static resource, which has nothing to capture.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx server mcp resource action -->

---

## Tools

Tools are callable functions exposed to MCP clients.

```hcl
tool "name" {
    description = "What this tool does"

    param "param_name" {
        type     = "string"
        required = true
    }

    action = expression
}
```

The block label is the tool name.

### Tool attributes

<!-- vinculum:begin block-attrs server mcp tool level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `description` | string | yes | What the tool does, shown to the model. |
| `action` | expression (action-expression) |  | Expression evaluated when the tool is called. |
| `disabled` | bool |  | Skip this tool entirely. |

**`description`**

This is how the model decides when to call it, so be specific.

**`action`**

Required unless the tool is disabled. Arguments arrive as `ctx.args.<param>`. A string becomes text content, `mcp::image()` image content, and `mcp::error(message)` reports failure to the model; any other type is an error. Wrap structured data in `jsonencode()`.

Evaluated against the `mcp-tool` context.

**`disabled`**

The tool is not registered, so the model never sees it.

#### Blocks

- `param "<name>"` (0..n) — One argument the client may pass.

<!-- vinculum:end block-attrs server mcp tool -->

### `param` blocks

`param` blocks declare the arguments a tool or prompt accepts. Each label is a
parameter name, and its value arrives as `ctx.args.<name>`.

Tools and prompts carry them differently, because the two protocols differ. A
tool publishes a full JSON Schema, so the model sees each parameter's type,
`enum`, and `default`, and arguments arrive typed. The prompt protocol carries
only a name, a description, and whether the argument is required — so on a
prompt, `type` and `enum` are checked at config time but constrain nothing at
runtime, and every argument reaches the action as a string.

<!-- vinculum:begin block-attrs server mcp tool param level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `type` | string | yes |  | Type of the parameter. |
| `default` | expression |  |  | Value used when the client omits the parameter. |
| `description` | string |  |  | What the parameter means, shown to the model. |
| `enum` | expression |  |  | Closed set of values the parameter accepts. |
| `required` | bool |  | `false` | Whether the client must supply the parameter. |

- A parameter with a default is not required.

**`type`**

Published to the model on a tool. On a prompt it checks `default` and `enum` at config time only, since prompt arguments are strings on the wire.

One of: `string`, `number`, `boolean`.

**`default`**

Applied when the argument is absent, whether or not the client honours the default published in a tool's schema. It must match `type`. On a prompt it is stringified with every other argument.

**`enum`**

Every entry must match `type`. Published in a tool's input schema; the prompt protocol has nowhere to carry it, so on a prompt it documents intent without constraining the caller.

<!-- vinculum:end block-attrs server mcp tool param -->

### Tool action context

<!-- vinculum:begin block-ctx server mcp tool action level=4 -->

Fields readable as `ctx.<name>` (shape `mcp-tool`):

| Field | Type | Description |
|---|---|:---|
| `ctx.server_name` | string | Name of the enclosing `server "mcp"` block. |
| `ctx.tool_name` | string | Name of the tool being called. |
| `ctx.args` | object | The call's arguments, keyed by the tool's declared `param` names. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.args`**

Already validated against each param's declared type.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx server mcp tool action -->

### Example

```hcl
tool "calculate" {
    description = "Evaluate a mathematical expression"

    param "expression" {
        type        = "string"
        description = "The expression to evaluate"
        required    = true
    }

    action = tostring(evalmath(ctx.args.expression))
}
```

---

## Prompts

Prompts are reusable prompt templates exposed to MCP clients.

```hcl
prompt "name" {
    description = "What this prompt does"

    param "param_name" {
        type     = "string"
        required = true
    }

    action = expression
}
```

Prompts take the same [`param` blocks](#param-blocks) as tools, with the
runtime differences noted there: prompt arguments always arrive as strings.

### Prompt attributes

<!-- vinculum:begin block-attrs server mcp prompt level=4 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) |  | Expression evaluated when a client requests the prompt. |
| `description` | string |  | What the prompt is for, shown to the model. |
| `disabled` | bool |  | Skip this prompt entirely. |

**`action`**

Required unless the prompt is disabled. Arguments arrive as `ctx.args.<param>`. Return a string, or `mcp::user_message()`/`mcp::assistant_message()` values — singly or as a list — to control message roles.

Evaluated against the `mcp-prompt` context.

**`disabled`**

The prompt is not registered, so clients never see it.

#### Blocks

- `param "<name>"` (0..n) — One argument the client may pass.

<!-- vinculum:end block-attrs server mcp prompt -->

### Prompt action context

<!-- vinculum:begin block-ctx server mcp prompt action level=4 -->

Fields readable as `ctx.<name>` (shape `mcp-prompt`):

| Field | Type | Description |
|---|---|:---|
| `ctx.server_name` | string | Name of the enclosing `server "mcp"` block. |
| `ctx.prompt_name` | string | Name of the prompt being requested. |
| `ctx.args` | object | The request's arguments, keyed by the prompt's declared `param` names. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx server mcp prompt action -->

### Example

```hcl
prompt "code_review" {
    description = "Review a piece of code"

    param "language" {
        type     = "string"
        required = true
    }
    param "code" {
        type     = "string"
        required = true
    }

    action = mcp::user_message(
        "Please review this ${ctx.args.language} code:\n\n```${ctx.args.language}\n${ctx.args.code}\n```"
    )
}
```

---

## MCP Functions

The following functions are available in MCP action expressions. They are also
available globally (e.g. in bus subscriptions) for future async handler support.
See [functions.md](functions.md#mcp-functions) for full details.

| Function | Returns | Valid in |
|---|---|---|
| plain string | Text content | resources, tools, prompts |
| `mcp::image(data [, mime_type])` | Image content | resources, tools |
| `mcp::error(message)` | Tool error | tools only |
| `mcp::user_message(content)` | User-role prompt message | prompts |
| `mcp::assistant_message(content)` | Assistant-role prompt message | prompts |

An action must return one of these. There is no implicit encoding of other
types — a resource or tool that wants to return structured data passes it
through `jsonencode()` first, as the examples on this page do.

`data` in `mcp::image` may be a base64-encoded string (requires `mime_type`) or a `bytes` capsule
(MIME type taken from the capsule's content type, optionally overridden by a second argument).
See [functions.md](functions.md#mcpimagedata--mime_type) for full details.

---

## Authentication

Authentication belongs to the route that mounts the MCP server, not to the MCP
block. Protect that route and every tool, resource, and prompt behind it is
protected; the identity reaches their action expressions as `ctx.auth`.

```hcl
server "mcp" "tools" {
    tool "whoami" {
        description = "Return the caller's identity"
        action      = jsonencode(ctx.auth)
    }
}

server "http" "main" {
    listen = ":9000"

    handle "/mcp" {
        handler = server.tools

        auth "oidc" {
            issuer   = "https://auth.example.com"
            audience = ["my-api-client-id"]
        }
    }
}
```

See [Authentication](auth.md) for the full reference and the `ctx.auth`
object shape.

---

## TLS

TLS is terminated by the [`server "http"`](server-http.md) block that hosts the
MCP endpoint — see [TLS configuration](config.md#tls).

---

## Mounting under HTTP

An MCP server is reached through a route of an HTTP server, referenced from that
server's `handle` block:

```hcl
server "mcp" "mytools" {
    server_name = "My Tools"

    tool "echo" {
        description = "Echo the input"
        param "text" { type = "string"; required = true }
        action = ctx.args.text
    }
}

server "http" "main" {
    listen = ":8080"

    handle "/mcp/" {
        handler = server.mytools
    }
}
```

The MCP endpoint is then reachable at `http://host:8080/mcp/`. This is useful
when you want to serve MCP alongside other HTTP routes on a single port.

---

## Observability

The MCP server emits OpenTelemetry traces and metrics conforming to the
[OpenTelemetry GenAI/MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai/tree/main/model/mcp).
Tracing and metrics backends are wired with the `tracing` and `metrics`
attributes, using the same resolution rules as [`server "http"`](server-http.md):

```hcl
client "otlp" "telemetry" {
    endpoint = "http://collector:4318"
}

server "mcp" "tools" {
    tracing = client.telemetry   # optional; auto-wired if it's the only OTLP client
    metrics = client.telemetry   # optional; auto-wired if it's the only metrics backend
    ...
}
```

When there is exactly one OTLP client (for `tracing`) or one metrics backend
(`server "metrics"` or `client "otlp"`, for `metrics`), the attribute may be
omitted and is auto-wired. With no backend configured, instrumentation is a
no-op.

### Two layers of telemetry

- **HTTP transport** — incoming W3C trace context is extracted and an HTTP
  server span (`POST /…`) plus standard HTTP server metrics are produced by the
  [`server "http"`](server-http.md) block hosting the endpoint, the same way it
  does for any other route.
- **MCP protocol** — every inbound MCP request/notification produces an
  `mcp.server` span (child of the HTTP span) and an
  `mcp.server.operation.duration` metric. These describe the protocol rather
  than the transport, and are what the `tracing` and `metrics` attributes on
  this block configure.

### `mcp.server` span

Span kind is `server`. The name is `{method} {target}`, where `target` is the
tool or prompt name when applicable, otherwise just `{method}` (e.g.
`tools/call get_weather`, `prompts/get summary`, `resources/read`). The resource
URI is **not** included in the name to keep span-name cardinality low.

| Attribute | When set |
|---|---|
| `mcp.method.name` | always (`tools/call`, `resources/read`, `prompts/get`, `initialize`, `tools/list`, …) |
| `gen_ai.tool.name` | tool calls |
| `gen_ai.operation.name` (`execute_tool`) | tool calls |
| `gen_ai.prompt.name` | prompt gets |
| `mcp.resource.uri` | resource reads |
| `mcp.session.id` | when the session has an id |
| `network.transport` (`tcp`), `network.protocol.name` (`http`) | always |
| `error.type` | on failure — `tool_error` when a tool returns an error result, otherwise `_OTHER` |

The span status is set to `ERROR` whenever `error.type` is present.

### `mcp.server.operation.duration` metric

A histogram (unit: seconds) recording how long each inbound MCP operation took.
Its attributes are the low-cardinality subset of the span attributes:
`mcp.method.name`, `gen_ai.tool.name`, `gen_ai.operation.name`,
`gen_ai.prompt.name`, `network.transport`, `network.protocol.name`, and
`error.type` (on failure). The session id and resource URI are intentionally
omitted to avoid metric-cardinality blowups.

> Session-lifetime metrics (`mcp.server.session.duration`) are not yet emitted —
> the underlying MCP SDK does not expose a session-disconnect hook.

---

## Full Example

```hcl
server "mcp" "assistant_tools" {
    server_name = "Assistant Tools"

    resource "config://environment" {
        name        = "Environment Info"
        description = "Current runtime environment"
        mime_type   = "application/json"
        action      = jsonencode({
            env     = env.APP_ENV
            version = env.APP_VERSION
        })
    }

    resource "docs://{page}" {
        name        = "Documentation"
        description = "Fetch a documentation page by name"
        mime_type   = "text/markdown"
        action      = file("./docs/${ctx.args.page}.md")
    }

    tool "ping" {
        description = "Check whether a host is reachable"

        param "host" {
            type        = "string"
            description = "Hostname or IP to ping"
            required    = true
        }

        action = ping(ctx.args.host)
    }

    tool "calculate" {
        description = "Evaluate a mathematical expression"

        param "expression" {
            type        = "string"
            description = "The expression to evaluate"
            required    = true
        }

        action = tostring(evalmath(ctx.args.expression))
    }

    prompt "code_review" {
        description = "Review a piece of code"

        param "language" {
            type        = "string"
            description = "Programming language"
            required    = true
        }
        param "code" {
            type        = "string"
            description = "Code to review"
            required    = true
        }

        action = mcp::user_message(
            "Please review this ${ctx.args.language} code:\n\n```${ctx.args.language}\n${ctx.args.code}\n```"
        )
    }
}

server "http" "main" {
    listen = ":9000"

    handle "/mcp" {
        handler = server.assistant_tools
    }
}
```
