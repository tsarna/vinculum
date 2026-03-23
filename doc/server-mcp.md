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

    resource ...
    tool ...
    prompt ...
}
```

- `listen` — address and port to listen on (e.g. `":9000"`). Omit when mounting under an HTTP server (see [Mounting under HTTP](#mounting-under-http)).
- `path` — URL path to mount the MCP endpoint on
- `server_name` / `server_version` — reported to clients during capability negotiation
- `disabled` — if true, the server block is skipped entirely

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
- `mcp_image(data, mime_type)` returns image content.
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

    action = mcp_user_message(
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
| `mcp_image(data, mime_type)` | Image content | resources, tools |
| `mcp_error(message)` | Tool error | tools only |
| `mcp_user_message(content)` | User-role prompt message | prompts |
| `mcp_assistant_message(content)` | Assistant-role prompt message | prompts |

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

        action = mcp_user_message(
            "Please review this ${ctx.args.language} code:\n\n```${ctx.args.language}\n${ctx.args.code}\n```"
        )
    }
}
```
