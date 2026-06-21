# MCP Server

Vinculum can act as a [Model Context Protocol (MCP)](https://modelcontextprotocol.io/)
server, exposing resources, tools, and prompts to MCP clients such as AI assistants.

## Server Block

```hcl
server "mcp" "name" {
    listen         = ":8080"       # required unless mounted under an HTTP server
    path           = "/mcp"        # optional, default "/"
    server_name    = "My Server"   # optional
    server_version = "1.0.0"       # optional
    disabled       = false         # optional

    tls {                          # optional; standalone mode only
        ...
    }

    resource ...
    tool ...
    prompt ...
}
```

- `listen` — address and port to listen on (e.g. `":9000"`). Omit when mounting under an HTTP server (see [Mounting under HTTP](#mounting-under-http)).
- `path` — URL path to mount the MCP endpoint on
- `server_name` / `server_version` — reported to clients during capability negotiation
- `disabled` — if true, the server block is skipped entirely
- `tracing` — optional reference to a `client "otlp"` block for OpenTelemetry tracing (auto-wired when there is exactly one OTLP client). See [Observability](#observability).
- `metrics` — optional reference to a `server "metrics"` or `client "otlp"` block for metrics (auto-wired when there is only one metrics backend). See [Observability](#observability).
- `tls` — optional sub-block to enable HTTPS; standalone mode only. See [TLS](#tls) below.

The server uses the [Streamable HTTP transport](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/transports/#streamable-http)
(MCP spec 2025-03-26).

---

## Resources

Resources expose data to MCP clients. Both static URIs and templated URIs are
supported.

```hcl
resource "uri" {
    name        = "Display Name"
    description = "Human-readable description"  # optional
    mime_type   = "text/plain"                   # optional
    action      = expression
}
```

The `action` expression is evaluated when a client requests the resource. Its return
value becomes the resource content:

- A string is returned as-is (using `mime_type` if set).
- Any other value is JSON-encoded.

### Static URI

```hcl
resource "status://current" {
    name   = "Status"
    action = "OK"
}
```

### Templated URI

Curly-brace placeholders in the URI are extracted and made available as `ctx.<name>`:

```hcl
resource "db://records/{table}/{id}" {
    name   = "Record"
    action = jsonencode(dbquery(ctx.table, ctx.id))
}
```

### Resource Context Variables

| Variable | Description |
|---|---|
| `ctx.server_name` | Name of the MCP server |
| `ctx.uri` | The fully resolved URI of the request |
| `ctx.<name>` | Value of each `{name}` placeholder in a templated URI |

---

## Tools

Tools are callable functions exposed to MCP clients.

```hcl
tool "name" {
    description = "What this tool does"

    param "param_name" {
        type        = "string"   # "string", "number", or "boolean"
        description = "..."      # optional
        required    = true       # optional, default false
        default     = value      # optional
        enum        = [...]      # optional, list of allowed values
    }

    action = expression
}
```

The `action` expression is evaluated when a client calls the tool. Return values:

- A string is returned as text content.
- `mcp_image(data [, mime_type])` returns image content — `data` may be a base64 string or a `bytes` capsule.
- `mcp_error(message)` signals a tool error to the client.

### Tool Context Variables

| Variable | Description |
|---|---|
| `ctx.server_name` | Name of the MCP server |
| `ctx.tool_name` | Name of the tool being called |
| `ctx.args` | Object containing all arguments passed by the client |
| `ctx.args.<param>` | Value of a specific parameter |

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
        type        = "string"
        description = "..."
        required    = true
        default     = value
        enum        = [...]
    }

    action = expression
}
```

The `action` expression must return one or more prompt messages using the MCP message
functions. A single message or a list of messages is accepted.

### Prompt Context Variables

| Variable | Description |
|---|---|
| `ctx.server_name` | Name of the MCP server |
| `ctx.prompt_name` | Name of the prompt being rendered |
| `ctx.args` | Object containing all arguments passed by the client |
| `ctx.args.<param>` | Value of a specific parameter |

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

    action = mcp_usermessage(
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
| `mcp_image(data [, mime_type])` | Image content | resources, tools |
| `mcp_error(message)` | Tool error | tools only |
| `mcp_usermessage(content)` | User-role prompt message | prompts |
| `mcp_assistantmessage(content)` | Assistant-role prompt message | prompts |

`data` in `mcp_image` may be a base64-encoded string (requires `mime_type`) or a `bytes` capsule
(MIME type taken from the capsule's content type, optionally overridden by a second argument).
See [functions.md](functions.md#mcp_image-data--mime_type) for full details.

---

## Authentication

Add an `auth` sub-block to require authentication on the MCP server. All tools,
resources, and prompts are protected; `ctx.auth` is available in their action expressions.

```hcl
server "mcp" "tools" {
    listen = ":9000"

    auth "oidc" {
        issuer   = "https://auth.example.com"
        audience = ["my-api-client-id"]
    }

    tool "whoami" {
        description = "Return the caller's identity"
        action      = jsonencode(ctx.auth)
    }
}
```

When `auth "oidc"` is configured on a standalone MCP server (one with `listen` set),
vinculum automatically serves the OIDC discovery document at
`GET /.well-known/oauth-authorization-server`. This allows MCP clients such as
Claude Desktop to discover the authorization server and complete the OAuth2 login
flow on behalf of the user.

See [Authentication](server-auth.md) for the full reference including all modes
(`basic`, `oidc`, `oauth2`, `custom`, `none`) and the `ctx.auth` object shape.

---

## TLS

Add a `tls {}` sub-block to serve the MCP endpoint over HTTPS. TLS is only available
in standalone mode (when `listen` is set); mounted servers inherit TLS from the parent
HTTP server. See [TLS configuration](config.md#tls) for the full attribute reference.

```hcl
server "mcp" "tools" {
    listen      = ":9000"
    server_name = "My Tools"

    tls {
        enabled = true
        cert    = "/etc/certs/server.crt"
        key     = "/etc/certs/server.key"
    }

    tool "echo" {
        description = "Echo the input"
        param "text" { type = "string"; required = true }
        action = ctx.args.text
    }
}
```

For local development, use `self_signed = true`:

```hcl
tls {
    enabled     = true
    self_signed = true
}
```

---

## Mounting under HTTP

An MCP server can be mounted at a path of an existing HTTP server instead of
listening on its own port. Omit `listen` from the MCP block and reference it
from the HTTP server's `handle` block:

```hcl
server "mcp" "mytools" {
    # no listen — mounted under HTTP below
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
    listen  = ":9000"
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
  server span (`POST /…`) plus standard HTTP server metrics are produced by
  `otelhttp`. This happens in **both** standalone mode and when mounted under a
  `server "http"` block (in the mounted case the parent HTTP server provides it).
- **MCP protocol** — every inbound MCP request/notification produces an
  `mcp.server` span (child of the HTTP span) and an
  `mcp.server.operation.duration` metric. This works identically in standalone
  and mounted mode.

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
    listen      = ":9000"
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
        action      = file("./docs/${ctx.page}.md")
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

        action = mcp_usermessage(
            "Please review this ${ctx.args.language} code:\n\n```${ctx.args.language}\n${ctx.args.code}\n```"
        )
    }
}
```
