# HTTP Server (`server "http"`)

The HTTP server exposes request handlers and static file trees over HTTP.

```hcl
server "http" "name" {
    listen   = ":8080"
    disabled = false     # optional

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
- `/items/{id}` — capture a path segment; accessible as `getpathvalue("id")`
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

## Context Variables

Inside a `handle` action expression, `ctx` provides:

| Variable | Type | Description |
|---|---|---|
| `ctx.method` | string | HTTP method (`"GET"`, `"POST"`, etc.) |
| `ctx.url` | string | Full request URL |
| `ctx.host` | string | `Host` header value |
| `ctx.remote_addr` | string | Client IP address and port |
| `ctx.proto` | string | Protocol string (e.g. `"HTTP/1.1"`) |
| `ctx.proto_major` | number | Major protocol version |
| `ctx.proto_minor` | number | Minor protocol version |
| `ctx.user` | string | Basic auth username (only present if Basic Auth was sent) |
| `ctx.password` | string | Basic auth password (only present if Basic Auth was sent) |

---

## Request Functions

These functions are only available inside `handle` action expressions.

### Reading the Request

#### `getbody()`
Read and return the entire request body as a string.

#### `getheader(key)`
Return the value of the named request header, or an empty string if absent.

#### `getcookie(key)`
Return a cookie object for the named cookie, or null if absent.

The object has these fields:

| Field | Type | Description |
|---|---|---|
| `name` | string | Cookie name |
| `value` | string | Cookie value |
| `path` | string | Cookie path |
| `domain` | string | Cookie domain |
| `expires` | string | Expiry time in RFC3339 format, or empty if not set |
| `maxAge` | number | Max-Age in seconds (`0` = not set, negative = delete) |
| `secure` | bool | Secure flag |
| `httpOnly` | bool | HttpOnly flag |
| `sameSite` | string | `"Default"`, `"Lax"`, `"Strict"`, or `"None"` |
| `partitioned` | bool | Partitioned flag |
| `quoted` | bool | Whether the value was originally quoted |

#### `getformvalue(key)`
Return the value of a form field (from query string or `application/x-www-form-urlencoded` body).

#### `getpostformvalue(key)`
Like `getformvalue`, but only looks in the POST body, not the query string.

#### `getpathvalue(key)`
Return the value of a named path segment captured by a `{name}` placeholder in the route pattern.

### Writing the Response

#### `respond(code, body)`
Write an HTTP response. `code` is the integer status code. If `body` is a string it
is written as-is; otherwise it is JSON-encoded and `Content-Type: application/json`
is set automatically. Returns `true`.

#### `setheader(key, value)`
Set a response header. Must be called before `respond`. Returns `true`.

#### `redirect(code, url)`
Send an HTTP redirect response. `code` should be a 3xx status (e.g.
`http_status.Found` for a temporary redirect). Returns `true`.

---

## Example

```hcl
server "http" "api" {
    listen = ":8080"

    handle "POST /events/{kind}" {
        action = send(ctx, bus.main, "http/" + getpathvalue("kind"), jsondecode(getbody()))
    }

    handle "GET /health" {
        action = respond(http_status.OK, {status = "ok"})
    }

    files "/app" {
        directory = "./web"
    }
}
```
