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

The action expression is evaluated for each matching request. The **return value**
determines the HTTP response sent to the client — see [Response](#response) below.
See [Context Variables](#context-variables) and [Request Functions](#request-functions).

### `handler` Attribute

Delegates handling to another server's HTTP handler. Use this to mount a `server "mcp"`, `server "metrics"`, 
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
- `directory` — filesystem path to the directory to serve. Relative paths are
  resolved against the `--file-path` base directory.
- `disabled` — if true, this block is skipped

`vinculum serve` must be started with `--file-path` whenever any non-disabled
`files` block is present.

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
| `expires` | time | Expiry time as a `time` capsule, or `null` if not set |
| `raw_expires` | string | Raw `Expires` header value |
| `max_age` | number | Max-Age in seconds (`0` = not set, negative = delete) |
| `secure` | bool | Secure flag |
| `http_only` | bool | HttpOnly flag |
| `same_site` | string | `"Default"`, `"Lax"`, `"Strict"`, or `"None"` |
| `partitioned` | bool | Partitioned flag |
| `quoted` | bool | Whether the value was originally quoted |
| `raw` | string | Raw `Set-Cookie` line |

---

## Response

The return value of the `action` expression determines the HTTP response. Automatic
type coercion handles the common cases; use the constructor functions for full control.

### Automatic Coercion

| Return type | Status | Content-Type | Body |
|---|---|---|---|
| `null` | 204 | — | none |
| `string` | 200 | `text/plain; charset=utf-8` | string bytes |
| `bytes` object | 200 | from `bytes.content_type` | raw bytes |
| `bool`, `number`, `object`, `map`, `list`, `tuple` | 200 | `application/json` | JSON-encoded |
| `http_error(...)` result | from call | `text/plain; charset=utf-8` | message |
| `http_response(...)` result | from call | from call | from call |

### Response Functions

#### `http_response(status[, body[, headers]])`
Build a response with the given status code, optional body, and optional headers.
`body` is coerced using the same rules as automatic coercion. `headers` may be
`map(string)` or `map(list(string))`.

#### `http_redirect(url)` / `http_redirect(status, url)`
Build a redirect response. The single-argument form uses `302 Found`. Valid status
codes: 301, 302, 303, 307, 308.

#### `http_error(status, message)`
Build an error response with the given status code and plain-text body.
Useful with `try()` to map errors to specific HTTP status codes.

#### `addheader(response, name, value)`
Return a new response with the given header value appended (multi-value safe).

#### `removeheader(response, name)`
Return a new response with all values for the given header removed.

#### `setcookie(cookieObj)`
Format a `Set-Cookie` header value from a cookie definition object.
Use with `addheader()` to set cookies on a response. Required fields: `name`, `value`.
Optional: `path`, `domain`, `expires` (`time` capsule, `duration` capsule, or RFC 3339 string), `max_age`, `secure`, `http_only`,
`same_site` (`"Lax"`, `"Strict"`, `"None"`, or `"Default"`), `partitioned`.

### `http_status` Object

HTTP status code constants are available as `http_status.<Name>` (PascalCase, matching
Go's `net/http` constants). Also provides `http_status.bycode["404"]` → `"NotFound"` for
reverse lookup. See [config.md](config.md#ambient-variables) for the full list.

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
        action = "Hello, TLS!"
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

## Examples

### Simple responses via auto-coercion

```hcl
# string → 200 text/plain
handle "GET /hello" {
    action = "Hello, World!"
}

# object → 200 application/json
handle "GET /health" {
    action = {status = "ok"}
}

# null → 204 No Content
handle "DELETE /item/{id}" {
    action = null
}
```

### Explicit responses

```hcl
handle "POST /items" {
    action = http_response(http_status.Created, created_item, {
        "Location" = "/items/${created_item.id}"
    })
}

handle "/old-path" {
    action = http_redirect(http_status.MovedPermanently, "/new-path")
}
```

### Error handling with `try()`

```hcl
handle "GET /items/{id}" {
    action = try(
        lookup_item(get(ctx.request, "path_value", "id")),
        http_error(http_status.NotFound, "item not found"),
    )
}
```

### Setting cookies

```hcl
handle "POST /login" {
    action = addheader(
        http_response(http_status.OK, {ok = true}),
        "Set-Cookie",
        setcookie({
            name      = "session"
            value     = create_session(get(ctx.request, "body_json"))
            path      = "/"
            http_only = true
            secure    = true
            same_site = "Lax"
            max_age   = 86400
        })
    )
}
```

### Full example

```hcl
server "http" "api" {
    listen = ":8080"

    handle "POST /events/{kind}" {
        action = send(ctx, bus.main,
            "http/" + get(ctx.request, "path_value", "kind"),
            get(ctx.request, "body_json"))
    }

    handle "GET /health" {
        action = {status = "ok"}
    }

    files "/app" {
        directory = "./web"
    }
}
```
