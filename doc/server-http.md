# HTTP Server (`server "http"`)

The HTTP server exposes request handlers and static file trees over HTTP.

```hcl
server "http" "name" {
    listen   = ":8080"
    disabled = false     # optional

    tls {                # optional
        ...
    }

    baggage {            # optional
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

The `tls` block enables HTTPS; see [TLS](#tls) below. The `baggage` block
controls which inbound [baggage](baggage.md) keys are trusted — inbound baggage
is **stripped by default**; see
[Server-side trust filtering](baggage.md#server-side-trust-filtering). The `auth`
block is covered under [Authentication](#authentication), and in full on the
[shared auth page](server-auth.md).

<!-- vinculum:begin block-attrs server http level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `listen` | string (listen-addr) | yes | Address and port to listen on. |
| `disabled` | bool |  | Skip this block entirely. |
| `metrics` | expression (metrics-ref) |  | Where to report metrics. |
| `tracing` | expression (tracing-ref) |  | Where to report traces. |

**`listen`**

For example `":8080"` or `"127.0.0.1:9090"`.

**`disabled`**

The block is parsed and validated, but nothing is created from it. A block that would publish a name — `condition.<name>`, `client.<name>` — does not, so any expression reading that name fails to resolve. Disable the blocks that read it too, or drop the reference.

**`metrics`**

A `server "metrics"` or `client "otlp"` block. Auto-wires to the default metrics backend when omitted.

**`tracing`**

A `client "otlp"` block. Auto-wires to the default tracing backend when omitted.

### Blocks

- `auth "<mode>"` (optional) — Authentication required by this server or handler.
- `baggage` (optional) — Which inbound baggage keys to trust.
- `files "<urlpath>"` (0..n) — Serves a directory tree of static files.
- `handle "<route>"` (0..n) — A route handler.
- `real_ip` (optional) — Recover the client's real IP from a forwarded header.
- `tls` (optional) — TLS settings for this connection.

<!-- vinculum:end block-attrs server http -->

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

Routes use [Go 1.22 `http.ServeMux` pattern syntax](https://pkg.go.dev/net/http#hdr-Patterns).
The full grammar is `[METHOD ][HOST]/[PATH]`:

- **METHOD** (optional) — followed by a single space; `GET` also matches `HEAD`.
- **HOST** (optional) — the text immediately before the first `/`; an exact host
  match with **no wildcards**. See [Virtual Hosts](#virtual-hosts).
- **PATH** — begins at the first `/`.

Examples:

- `GET /api/status` — match only GET requests to an exact path
- `POST /api/events` — match only POST requests
- `/api/` — match any method and any path under `/api/` (trailing slash = subtree)
- `/{$}` — match only the exact path `/` (the `{$}` anchor prevents subtree matching)
- `/items/{id}` — capture a path segment; accessible as `ctx.request.path.id`
- `api.example.com/v1/items/{id}` — scope the route to a specific host
- `GET api.example.com/v1/items/{id}` — method **and** host together
- `{method} /path` — placeholder in the method position is not standard; use a specific method or omit for any method

### Virtual Hosts

A host in the pattern scopes the route to that `Host` header, so one listener can
serve several hosts (name-based virtual hosting):

```hcl
server "http" "main" {
    listen = ":8080"

    handle "api.example.com/v1/items/{id}" {
        action = ctx.request.path.id
    }

    files "cdn.example.com/static" {
        directory = "./web/static"
    }

    # Host-less = default / catch-all for any other Host
    handle "GET /health" {
        action = "ok"
    }
}
```

- **Host-less routes are the default.** A pattern with no host matches all hosts,
  so a host-less `handle`/`files` acts as the catch-all for hosts with no
  specific route.
- **Exact host match only.** No wildcard or suffix matching (`*.example.com` is
  not supported — a `ServeMux` limitation). For wildcard/suffix needs, route to a
  host-less handler and branch on `ctx.request.host` inside the action; the two
  approaches compose.
- **Specific beats general.** `api.example.com/v1/x` wins over `/v1/x` for that
  host; other hosts fall through to the host-less route.
- **Do not include a port** in the host segment.
- `files` understands the same host prefix; a method token in a `files` label is
  a configuration error (a file server serves GET/HEAD only).

Serving multiple hosts over **HTTPS** requires a certificate covering every host
(multi-SAN) or SNI-based selection — see [TLS](#tls).

### `handle` attributes

<!-- vinculum:begin block-attrs server http handle level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `action` | expression (action-expression) |  | Expression evaluated for each matching request. |
| `disabled` | bool |  | Skip this route entirely. |
| `handler` | expression (server-ref) |  | Another server to delegate this route to. |

- Specify at most one of action or handler.
- A route needs either an action to evaluate or a handler to delegate to.

**`action`**

Its value becomes the response: a string is sent as `text/plain`, a
bytes object with its own content type, anything else as JSON, and `null` as
204. Use `http::response()` or `http::error()` to control the status.

Evaluated against the `http-request` context.

**`handler`**

Mounts a server that exposes an HTTP handler, such as `server "mcp"` or `server "metrics"`.

### Blocks

- `auth "<mode>"` (optional) — Authentication required by this server or handler.

<!-- vinculum:end block-attrs server http handle -->

### `action` Expression

The action expression is evaluated for each matching request. The **return value**
determines the HTTP response sent to the client — see [Response](#response) below.
The response helpers are documented under
[HTTP Response Functions](functions.md#http-response-functions).

It is evaluated against this context:

<!-- vinculum:begin block-ctx server http handle action level=3 -->

Fields readable as `ctx.<name>` (shape `http-request`):

| Field | Type | Description |
|---|---|:---|
| `ctx.request` | object | The inbound request. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.request`**

Carries `method`, `url`, `host`, `remote_addr`, `proto`, the basic-auth `user`/`password`/`password_set`, the route's `path` parameters, and `form`. Read headers, cookies, and the body through the request functions — see doc/server-http.md.

**`ctx.auth`**

Populated by the auth middleware when the event arrived through an authenticated path; null everywhere else.

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

<!-- vinculum:end block-ctx server http handle action -->

`ctx.request` is a rich object; its own attributes are listed under
[Request Object](#request-object) below.

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

The label is a URL path prefix, and a trailing slash is added automatically.
Requests under the prefix are served from `directory`. The label may be prefixed
with a host to scope the tree to that `Host` (`"cdn.example.com/static"`) — see
[Virtual Hosts](#virtual-hosts). A method token is **not** allowed here, since a
file server serves GET and HEAD only.

<!-- vinculum:begin block-attrs server http files level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `directory` | string | yes | Directory to serve files from. |
| `disabled` | bool |  | Skip this tree entirely. |

**`directory`**

A relative path resolves against the `--file-path` base directory, which `vinculum serve` requires whenever any `files` block is active.

### Blocks

- `auth "<mode>"` (optional) — Authentication required by this server or handler.

<!-- vinculum:end block-attrs server http files -->

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
| `ctx.request.remote_addr` | string | Client IP address and port (or the real client IP when [`real_ip`](#real-client-ip-real_ip) is configured) |
| `ctx.request.proto` | string | Protocol string (e.g. `"HTTP/1.1"`) |
| `ctx.request.proto_major` | number | Major protocol version |
| `ctx.request.proto_minor` | number | Minor protocol version |
| `ctx.request.user` | string | Basic auth username, or `""` if not sent |
| `ctx.request.password` | string | Basic auth password, or `""` if not sent |
| `ctx.request.password_set` | bool | `true` if Basic Auth was present |
| `ctx.request.path` | map(string) | Path parameters extracted from `{name}` placeholders in the route pattern |
| `ctx.request.form` | map(list(string)) | Parsed form data from query string and URL-encoded POST body |

The `url` attribute is a full URL object — all URL fields are accessible directly:
`ctx.request.url.path`, `ctx.request.url.hostname`, `ctx.request.url.query`, etc.
See the url::parse function documentation for the full field list.

### `get(ctx.request, ...)` — On-demand Access

These operations may have side effects (consuming the body) or require a key
argument, so they are accessed via `get()` rather than direct attributes.

| Call | Returns | Description |
|---|---|---|
| `get(ctx.request, "body")` | string | Read entire request body as a string |
| `get(ctx.request, "body_bytes")` | bytes object | Read body as a bytes object; `content_type` is populated from the `Content-Type` header (media type only, parameters stripped) |
| `get(ctx.request, "body_json")` | dynamic | Read body and parse as JSON into cty values |
| `get(ctx.request, "header", name)` | string | First value of the named header, or `""` if absent |
| `get(ctx.request, "header_all", name)` | list(string) | All values for the named header; empty list if absent |
| `get(ctx.request, "cookie", name)` | cookie object | Named cookie; error if not present |
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
| `http::error(...)` result | from call | `text/plain; charset=utf-8` | message |
| `http::response(...)` result | from call | from call | from call |

### Response Functions

#### `http::response(status[, body[, headers]])`
Build a response with the given status code, optional body, and optional headers.
`body` is coerced using the same rules as automatic coercion. `headers` may be
`map(string)` or `map(list(string))`.

#### `http::redirect(url)` / `http::redirect(status, url)`
Build a redirect response. The single-argument form uses `302 Found`. Valid status
codes: 301, 302, 303, 307, 308.

#### `http::error(status, message)`
Build an error response with the given status code and plain-text body.
Useful with `try()` to map errors to specific HTTP status codes.

#### `http::add_header(response, name, value)`
Return a new response with the given header value appended (multi-value safe).

#### `http::remove_header(response, name)`
Return a new response with all values for the given header removed.

#### `http::set_cookie(cookieObj)`
Format a `Set-Cookie` header value from a cookie definition object.
Use with `http::add_header()` to set cookies on a response. Required fields: `name`, `value`.
Optional: `path`, `domain`, `expires` (`time` capsule, `duration` capsule, or RFC 3339 string), `max_age`, `secure`, `http_only`,
`same_site` (`"Lax"`, `"Strict"`, `"None"`, or `"Default"`), `partitioned`.

### `http_status` Object

HTTP status code constants are available as `http_status.<Name>` (PascalCase, matching
Go's `net/http` constants). Also provides `http_status.bycode["404"]` → `"NotFound"` for
reverse lookup. See [config.md](config.md#ambient-variables) for the full list.

---

## Distributed Tracing

Add a `tracing` attribute to connect the server to a `client "otlp"` block.
Every inbound request will produce an OTel span. If the request carries a W3C
`traceparent` header the span is created as a child of the upstream trace;
otherwise a new root span is started.

```hcl
client "otlp" "jaeger" {
    endpoint     = "http://localhost:4318"
    service_name = "my-app"
}

server "http" "api" {
    listen  = ":8080"
    tracing = client.jaeger   # optional; auto-wired when there is only one client "otlp"
    ...
}
```

When a span is active, two variables are available in `action` expressions:

| Variable | Type | Description |
|----------|------|-------------|
| `ctx.trace_id` | string | W3C trace ID (32 hex chars) |
| `ctx.span_id` | string | W3C span ID (16 hex chars) |

Both are empty strings when no tracing client is configured.

See [client "otlp"](client-otlp.md) for full configuration options and
auto-wiring rules.

---

## HTTP Server Metrics

Add a `metrics` attribute to enable automatic HTTP server metrics via OTel's
`otelhttp` instrumentation. Metrics follow the
[OTel HTTP semantic conventions](https://opentelemetry.io/docs/specs/semconv/http/http-metrics/).

```hcl
server "http" "api" {
    listen  = ":8080"
    metrics = server.metrics   # optional; auto-wired when there is only one metrics backend
}
```

When enabled, the following metrics are produced automatically:

| Metric (OTel name)                  | Type      | Description                       |
|-------------------------------------|-----------|-----------------------------------|
| `http.server.request.duration`      | histogram | Request duration in seconds       |
| `http.server.active_requests`       | gauge     | Currently active requests         |
| `http.server.request.body.size`     | histogram | Request body size in bytes        |
| `http.server.response.body.size`    | histogram | Response body size in bytes       |

Attributes include `http.request.method`, `http.response.status_code`,
`url.scheme`, `server.address` (set to the server block name), and
`network.protocol.version`.

In Prometheus format, dots are converted to underscores automatically (e.g.
`http_server_request_duration_seconds_bucket`). OTLP receivers get the canonical
dotted names.

The `metrics` attribute accepts a reference to either a `server "metrics"` or
`client "otlp"` block. If omitted and exactly one metrics backend exists, it is
auto-wired.

---

## Authentication

Add an `auth` sub-block to require authentication on the server or on individual routes.

```hcl
server "http" "api" {
    listen = ":8080"

    auth "oidc" {                    # server-level default
        issuer = "https://auth.example.com"
    }

    handle "GET /public" {
        auth "none" {}               # opt out of server-level auth
        action = "public"
    }

    handle "GET /me" {
        action = jsonencode(ctx.auth) # ctx.auth populated on success
    }
}
```

Block-level `auth` (on `handle` or `files`) overrides the server-level `auth`.
`auth "none" {}` explicitly disables inherited auth for a route.

An `auth` block also accepts an optional `disabled` attribute. When it evaluates
to `true` the block is parsed but inert — exactly as if it were absent (so a
server-level block falls back to no auth, and mode-specific required fields like
`credentials` are not validated). Because the expression sees `env.*`, a single
environment variable can both supply the credential and toggle auth on/off:

```hcl
auth "basic" {
    disabled    = try(env.WEB_PASSWORD, "") == ""   # off when the var is unset
    credentials = { admin = try(env.WEB_PASSWORD, "") }
}
```

On success, the authenticated identity is available as `ctx.auth` in the `action` expression.
See [Authentication](server-auth.md) for the full reference including all modes
(`basic`, `oidc`, `oauth2`, `custom`, `none`) and the `ctx.auth` object shape.

---

## Real Client IP (`real_ip`)

When the server runs behind a reverse proxy or load balancer (e.g. Traefik,
nginx, an ELB), the TCP peer is the proxy, not the client — so
`ctx.request.remote_addr` and the request log would show the proxy's address.
The `real_ip` block recovers the original client address from a forwarded
header, equivalent to nginx's `real_ip` module:

```hcl
server "http" "main" {
    listen = ":8080"

    real_ip {
        trusted_proxies = ["10.0.0.0/8"]    # required: trusted proxy networks
        header          = "X-Forwarded-For" # optional, this is the default
        recursive       = true              # optional, default false
    }

    files "/static" { directory = "./web" }
}
```

<!-- vinculum:begin block-attrs server http real_ip level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `trusted_proxies` | list | yes |  | CIDRs or bare IPs whose forwarded headers are believed. |
| `disabled` | bool |  |  | Skip this block entirely. |
| `header` | string |  | `X-Forwarded-For` | Header to read the client address from. |
| `recursive` | bool |  | `false` | Walk the header right to left, skipping trusted proxies. |

**`trusted_proxies`**

A header arriving from any other address is ignored, which is what stops a client spoofing its own IP by sending one. nginx spells this `set_real_ip_from`.

**`header`**

Any header works; a single-valued one such as `X-Real-IP` is just the one-element case. nginx spells this `real_ip_header`.

**`recursive`**

The first untrusted address found is the client. Use it when a chain of proxies each append an address; without it the rightmost entry is taken, which is right for a single hop. nginx spells this `real_ip_recursive`.

<!-- vinculum:end block-attrs server http real_ip -->

The forwarded header is honored **only when the immediate peer is trusted**, so
a request arriving directly from an untrusted address keeps its real peer
address. A disabled block is parsed but inert, and does not require
`trusted_proxies` — so one environment variable can both supply the proxies and
toggle the feature: `disabled = try(env.TRUSTED_PROXIES, "") == ""`.

When a substitution applies, `r.RemoteAddr` is rewritten **before** tracing,
logging, auth, and action evaluation run, so `ctx.request.remote_addr`, the
`remote_addr` request-log field, and any auth/rate-limiting all see the real
client. (nginx parity: `set_real_ip_from` → `trusted_proxies`, `real_ip_header`
→ `header`, `real_ip_recursive` → `recursive`.)

> **HTTP→HTTPS redirects:** when TLS is terminated at the proxy, vinculum only
> sees plain HTTP and cannot dispatch on the URL scheme. Perform scheme
> redirects at the proxy edge (e.g. Traefik's `redirectScheme` middleware) —
> the proxy is the TLS terminator and the natural place for it.

---

## TLS

Add a `tls {}` sub-block to serve HTTPS instead of plain HTTP. See [TLS configuration](config.md#tls) for the full attribute reference.

> **Virtual hosts over HTTPS:** name-based [virtual hosting](#virtual-hosts) is a
> routing concern, separate from TLS. To serve several hosts over HTTPS from one
> listener, the certificate must cover every host (a multi-SAN certificate) or use
> SNI-based certificate selection. The single `tls` block here presents one
> certificate; per-host certificate selection is not currently supported.

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
    action = http::response(http_status.Created, created_item, {
        "Location" = "/items/${created_item.id}"
    })
}

handle "/old-path" {
    action = http::redirect(http_status.MovedPermanently, "/new-path")
}
```

### Error handling with `try()`

```hcl
handle "GET /items/{id}" {
    action = try(
        lookup_item(ctx.request.path.id),
        http::error(http_status.NotFound, "item not found"),
    )
}
```

### Setting cookies

```hcl
handle "POST /login" {
    action = http::add_header(
        http::response(http_status.OK, {ok = true}),
        "Set-Cookie",
        http::set_cookie({
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
            "http/" + ctx.request.path.kind,
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
