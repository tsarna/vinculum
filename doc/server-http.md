# HTTP Server (`server "http"`)

The HTTP server exposes request handlers and static file trees over HTTP.

```hcl
server "http" "name" {
    listen   = ":8080"
    disabled = false     # optional

    tls {                # optional
        ...
    }

    handle "route" {
        ...
    }

    files "urlpath" {
        ...
    }
}
```

- `listen` — address and port to listen on (e.g. `":8080"`, `"127.0.0.1:9090"`)
- `disabled` — if true, the server block is skipped entirely
- `tls` — optional sub-block to enable HTTPS; see [TLS](#tls) below

The server is available in expressions as `server.<name>`.

---

## `handle` Block

Registers an action or sub-handler for a route pattern.

```hcl
handle "METHOD /path" {
    action   = expression  # evaluate an HCL expression on each request
    # OR
    handler  = server.other  # delegate to another server's HTTP handler
    disabled = false         # optional
}
```

Exactly one of `action` or `handler` must be specified.

### Route Patterns

Routes use [Go 1.22 `http.ServeMux` pattern syntax](https://pkg.go.dev/net/http#hdr-Patterns):

- `GET /api/status` — match only GET requests to an exact path
- `POST /api/events` — match only POST requests
- `/api/` — match any method and any path under `/api/` (trailing slash = subtree)
- `/items/{id}` — capture a path segment; accessible as `get(ctx.request, "path_value", "id")`
- `{method} /path` — placeholder in the method position is not standard; use a specific method or omit for any method

### `action` Expression

The action expression is evaluated for each matching request. The result value is
discarded; use `respond()` to write a response. See [Context Variables](#context-variables)
and [Request Functions](#request-functions) below.

### `handler` Attribute

Delegates handling to another server's HTTP handler. Use this to mount a `server "mcp"`
or `server "vws"` under a path on an existing HTTP server:

```hcl
server "mcp" "tools" {
    path = "/mcp"
    ...
}

server "http" "main" {
    listen = ":8080"

    handle "/mcp" {
        handler = server.tools
    }
}
```

---

## `files` Block

Serves a directory of static files.

```hcl
files "/static" {
    directory = "./web/static"
    disabled  = false           # optional
}
```

- `urlpath` (label) — URL path prefix. A trailing slash is added automatically.
  Requests under this prefix are served from `directory`.
- `directory` — filesystem path to the directory to serve
- `disabled` — if true, this block is skipped

---

## Request Object

Inside a `handle` action expression, the incoming request is available as
`ctx.request`. It is a rich object with direct attributes for common fields and
`get(ctx.request, ...)` for on-demand access.

### Direct Attributes

| Attribute | Type | Description |
|---|---|---|
| `ctx.request.method` | string | HTTP method (`"GET"`, `"POST"`, etc.) |
| `ctx.request.url` | url object | Parsed request URL (see URL object attributes below) |
| `ctx.request.host` | string | `Host` header value |
| `ctx.request.remote_addr` | string | Client IP address and port |
| `ctx.request.proto` | string | Protocol string (e.g. `"HTTP/1.1"`) |
| `ctx.request.proto_major` | number | Major protocol version |
| `ctx.request.proto_minor` | number | Minor protocol version |
| `ctx.request.user` | string | Basic auth username, or `""` if not sent |
| `ctx.request.password` | string | Basic auth password, or `""` if not sent |
| `ctx.request.password_set` | bool | `true` if Basic Auth was present |
| `ctx.request.headers` | map(list(string)) | All request headers; keys in canonical form |

The `url` attribute is a full URL object — all URL fields are accessible directly:
`ctx.request.url.path`, `ctx.request.url.hostname`, `ctx.request.url.query`, etc.
See the urlparse function documentation for the full field list.

### `get(ctx.request, ...)` — On-demand Access

These operations may have side effects (consuming the body, triggering form parsing)
so they are accessed via `get()` rather than direct attributes.

| Call | Returns | Description |
|---|---|---|
| `get(ctx.request, "body")` | string | Read entire request body as a string |
| `get(ctx.request, "body_bytes")` | bytes object | Read body as a bytes object; `content_type` is populated from the `Content-Type` header (media type only, parameters stripped) |
| `get(ctx.request, "body_json")` | dynamic | Read body and parse as JSON into cty values |
| `get(ctx.request, "header", name)` | string | First value of the named header, or `""` if absent |
| `get(ctx.request, "header_all", name)` | list(string) | All values for the named header; empty list if absent |
| `get(ctx.request, "cookie", name)` | cookie object | Named cookie; error if not present |
| `get(ctx.request, "path_value", name)` | string | Named path segment from a `{name}` placeholder in the route pattern |
| `get(ctx.request, "form_value", name)` | string | Form field from query string or URL-encoded body |
| `get(ctx.request, "post_form_value", name)` | string | Form field from POST body only (excludes query string) |

### Cookie Object

Returned by `get(ctx.request, "cookie", name)`.

| Field | Type | Description |
|---|---|---|
| `name` | string | Cookie name |
| `value` | string | Cookie value |
| `path` | string | Cookie path |
| `domain` | string | Cookie domain |
| `expires` | string | Expiry time in RFC 3339 format, or `""` if not set |
| `raw_expires` | string | Raw `Expires` header value |
| `max_age` | number | Max-Age in seconds (`0` = not set, negative = delete) |
| `secure` | bool | Secure flag |
| `http_only` | bool | HttpOnly flag |
| `same_site` | string | `"Default"`, `"Lax"`, `"Strict"`, or `"None"` |
| `partitioned` | bool | Partitioned flag |
| `quoted` | bool | Whether the value was originally quoted |
| `raw` | string | Raw `Set-Cookie` line |

---

## Response Functions

These functions are only available inside `handle` action expressions.

#### `respond(code, body)`
Write an HTTP response. `code` is the integer status code. If `body` is a string it
is written as-is; otherwise it is JSON-encoded and `Content-Type: application/json`
is set automatically. Returns `true`.

#### `setheader(key, value)`
Set a response header. Must be called before `respond`. Returns `true`.

#### `redirect(code, url)`
Send an HTTP redirect response. `code` should be a 3xx status (e.g.
`httpstatus.Found` for a temporary redirect). Returns `true`.

---

## TLS

Add a `tls {}` sub-block to serve HTTPS instead of plain HTTP. See [TLS configuration](config.md#tls) for the full attribute reference.

```hcl
server "http" "secure" {
    listen = ":8443"

    tls {
        enabled = true
        cert    = "/etc/certs/server.crt"
        key     = "/etc/certs/server.key"
    }

    handle "/hello" {
        action = respond(200, "Hello, TLS!")
    }
}
```

For local development, use `self_signed = true` to generate an ephemeral certificate automatically:

```hcl
server "http" "dev" {
    listen = ":8443"

    tls {
        enabled     = true
        self_signed = true
    }
}
```

---

## Example

```hcl
server "http" "api" {
    listen = ":8080"

    handle "POST /events/{kind}" {
        action = send(ctx, bus.main, "http/" + get(ctx.request, "path_value", "kind"), get(ctx.request, "body_json"))
    }

    handle "GET /health" {
        action = respond(httpstatus.OK, {status = "ok"})
    }

    files "/app" {
        directory = "./web"
    }
}
```
