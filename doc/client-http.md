# HTTP Client (`client "http"`)

Vinculum can make outbound HTTP(S) requests from action expressions using
`client "http"` blocks and the `http_*` verb functions. The design is loosely
modeled on the JavaScript `fetch()` API: a small set of verbs take a URL, an
optional body, and an options object, and return a rich response object.

A `client "http"` block centralizes connection settings, default headers,
TLS, authentication, redirect handling, retry policy, cookie jar, and a
default base URL — so call sites stay short and a single block can configure
an entire upstream API integration.

For one-off probes you don't need to declare a client at all: pass `null` as
the client argument and the verb function will use a built-in default.

---

## Quick examples

### One-off request, no client

```hcl
trigger "start" "ping" {
    action = log_info("ping result", {
        status = http_get(ctx, null, "https://example.com/ping").status,
    })
}
```

### Basic API client

```hcl
client "http" "github" {
    base_url = "https://api.github.com"

    headers = {
        Accept       = "application/vnd.github+json"
        "User-Agent" = "vinculum-myapp/1.0"
    }

    auth = "Bearer ${env.GITHUB_TOKEN}"

    timeout = "10s"

    retry {
        max_attempts  = 3
        initial_delay = "200ms"
    }
}

# A handler somewhere uses the client by relative path:
handle "GET /repos/{name}" {
    action = http_must(http_get(ctx, client.github, "/repos/tsarna/${ctx.request.path.name}"))
}
```

### POST with auto-serialized JSON body

```hcl
http_post(ctx, client.api, "/widgets", {
    name  = "frobnicator"
    color = "blue"
})
```

### POST with explicit body bytes and per-call options

```hcl
http_post(ctx, client.api, "/upload",
    filebytes("/tmp/photo.jpg", "image/jpeg"),
    {
        headers = { "X-Upload-Source" = "vinculum" }
        timeout = "60s"
    },
)
```

---

## `client "http" "<name>"`

The full schema, with all attributes shown — almost everything is optional.

```hcl
client "http" "<name>" {
    disabled = false                  # standard for all client blocks

    # ─── Endpoint defaults ──────────────────────────────────────
    base_url   = "https://api.example.com/v1"
    user_agent = "my-app/1.2"         # convenience for the User-Agent header

    # ─── Default headers ────────────────────────────────────────
    headers = {
        Accept = "application/json"
        "X-App" = "vinculum"
    }

    # ─── TLS ────────────────────────────────────────────────────
    tls {
        enabled              = true
        ca_cert              = "/etc/ssl/myca.pem"
        cert                 = "/etc/ssl/client.pem"
        key                  = "/etc/ssl/client.key"
        insecure_skip_verify = false
    }

    # ─── Authentication ─────────────────────────────────────────
    # Action expression evaluated lazily to produce the Authorization
    # header value. May call any function, including http_*() against
    # this same client (an OAuth-token-from-same-host pattern).
    auth              = basicauth(env.USER, env.PASS)
    auth_max_lifetime = "55m"   # optional: re-evaluate at least this often
    auth_max_failures = 5

    auth_retry_backoff {
        initial_delay  = "1s"
        max_delay      = "60s"
        backoff_factor = 2.0
    }

    # ─── Redirects ──────────────────────────────────────────────
    redirects {
        follow                = true   # default true
        max                   = 10     # default 10
        keep_auth_on_redirect = false  # default false (drop Authorization on cross-origin redirect)
    }

    # ─── Cookie jar ─────────────────────────────────────────────
    cookies {
        enabled       = true   # default true when the cookies block is present
        public_suffix = true   # use the public-suffix list when scoping cookies
    }

    # ─── Timeouts ───────────────────────────────────────────────
    timeout = "30s"             # whole-request deadline

    # ─── Retry policy ───────────────────────────────────────────
    retry {
        max_attempts        = 3
        initial_delay       = "200ms"
        max_delay           = "30s"
        backoff_factor      = 2.0
        jitter              = true                  # default true
        retry_on            = [429, 502, 503, 504]  # plus all network/timeout errors
        respect_retry_after = true                  # honor Retry-After on 429/503

        # Optional hook: receives the in-flight response and decides what to do.
        on_response = ctx.response.status == 429
            ? duration("${get(ctx.response, "header", "Retry-After")}s")
            : null
    }

    # ─── OpenTelemetry ──────────────────────────────────────────
    otel {
        propagate = true             # default true
    }
    tracing = client.tracer          # optional explicit TracerProvider
    metrics = client.tracer          # optional explicit MeterProvider

    # ─── HTTP/2 and connection pooling ──────────────────────────
    http2                    = true  # default true
    max_connections_per_host = 0     # 0 = Go default
    max_idle_connections     = 100   # 0 = Go default
    disable_keep_alives      = false
}
```

### Schema reference

| Attribute / sub-block | Type | Default | Description |
|---|---|---|---|
| `disabled` | bool | `false` | Standard `disabled` flag for all client blocks. |
| `base_url` | string | — | URL prefix; relative URLs in calls are resolved against it. Absolute call URLs override it entirely. Query parameters present on `base_url` (e.g. an `api_key`) are merged into every relative call — see *Base URL query parameters* below. |
| `user_agent` | string | *(see precedence)* | Convenience for setting the `User-Agent` header. |
| `headers` | map(string) or map(list(string)) | `{}` | Default headers added to every request. Per-call headers override these on a name-by-name basis. |
| `tls` | block | none | Standard TLS configuration block. See [TLS configuration](config.md#tls). |
| `auth` | expression | — | Action expression evaluated lazily to produce the `Authorization` header. See *Authentication* below. |
| `auth_max_lifetime` | duration | none | Maximum age of a cached credential. After this elapses the next request re-evaluates the `auth` expression. |
| `auth_max_failures` | number | `5` | Consecutive auth-hook failures before requests start failing fast. |
| `auth_retry_backoff` | block | sensible defaults | Backoff for repeated auth-hook failures. |
| `redirects` | block | `follow=true, max=10` | Redirect behavior. |
| `cookies` | block | disabled | Cookie jar. |
| `timeout` | duration | none | Whole-request deadline. |
| `retry` | block | none (no retries) | Retry policy and hook. |
| `otel` | block | `propagate=true` | OpenTelemetry header propagation toggle. |
| `tracing` | expression | global default | Explicit TracerProvider, mirroring the convention used by other clients. |
| `metrics` | expression | global default | Explicit MeterProvider, mirroring the convention used by other clients. |
| `http2` | bool | `true` | Allow HTTP/2 negotiation. |
| `max_connections_per_host` | number | `0` | Per-host connection cap (0 = Go default). |
| `max_idle_connections` | number | `0` | Idle connection pool size (0 = Go default). |
| `disable_keep_alives` | bool | `false` | Force `Connection: close`. |

> **Durations.** Every duration-typed attribute accepts either a string in
> Go duration format (`"30s"`, `"1m30s"`) or a duration capsule value,
> consistent with the rest of the configuration language.

The client is available in expressions as `client.<name>` and is passed as
the second argument to any `http_*()` verb function.

---

## Verb functions

Eight verb functions are provided, mirroring `net/http`. They live in the
global function namespace alongside the other built-ins.

| Function | Body? | Idempotent (per RFC) |
|---|---|---|
| `http_get(ctx, client, url[, opts])` | no | yes |
| `http_head(ctx, client, url[, opts])` | no | yes |
| `http_options(ctx, client, url[, opts])` | no | yes |
| `http_delete(ctx, client, url[, opts])` | no | yes |
| `http_post(ctx, client, url[, body[, opts]])` | yes | no |
| `http_put(ctx, client, url[, body[, opts]])` | yes | yes |
| `http_patch(ctx, client, url[, body[, opts]])` | yes | no |

A generic form is also provided for unusual methods or when the method is
dynamic:

| Function | Description |
|---|---|
| `http_request(ctx, client, method, url[, body[, opts]])` | Generic; useful for `PROPFIND`, `MKCOL`, etc., or when the method is computed. |

### Argument semantics

- **`ctx`** — the current execution context (the same `ctx` available in all
  action expressions). Carries the `context.Context` used for cancellation
  and timeout propagation, and the OpenTelemetry span used for header
  injection.
- **`client`** — `client.<name>` (an `http` client capsule), or `null` to
  use built-in defaults (no auth, no cookies, no retries, follow redirects,
  TLS with system roots, 30-second timeout, OTel propagation on).
- **`url`** — string, [`url`](functions.md#url-parsing-and-manipulation)
  object, or url capsule. If the client has a `base_url` and the supplied
  URL is relative, the two are resolved together — see *Base URL query
  parameters* below for the merge rule. Absolute URLs ignore `base_url`
  entirely.
- **`body`** — only on body-bearing verbs. See *Body coercion* below.
- **`opts`** — optional object literal; see *Options* below.

### Return value

Every verb function returns an `httpclientresponse` rich object — see
*Response object* below.

If the request cannot be sent (DNS failure, connection refused, TLS error,
context canceled, retries exhausted on a network error), the function
**raises an HCL error** — the same mechanism used by `error()`.
HTTP-level non-2xx responses do **not** raise: a `404` is
just a response with `status = 404`, the same as `fetch()`'s behavior. Use
[`http_must()`](#http_must) when you want a non-2xx response to fail loudly.

---

## Body coercion

When the body argument is present, the verb function applies these rules:

| Body type | Default `Content-Type` | Encoding |
|---|---|---|
| `null` or omitted | *(no body, no Content-Type)* | — |
| `string` | `text/plain; charset=utf-8` | UTF-8 bytes of the string |
| `bytes` rich object | `bytes.content_type` (or `application/octet-stream` if empty) | raw bytes |
| `bool`, `number` | `application/json` | JSON-encoded scalar |
| `object`, `map`, `tuple`, `list` | `application/json` | JSON-encoded value |

The body type sets the **default** `Content-Type` only — explicit headers
on the call or the client block override it (see *Header precedence*).

> Body-less verbs (`GET`, `HEAD`, `OPTIONS`, `DELETE`) have no body slot in
> their signature. To send a body with one of those methods (yes, `DELETE`
> *can* technically carry a body), use `http_request(ctx, client, "DELETE",
> url, body, opts)`.

---

## Options

The `opts` argument is an object literal. All keys are optional.

| Key | Type | Description |
|---|---|---|
| `headers` | map(string) or map(list(string)) | Per-call headers; merged into the precedence chain (see below). A `null` value at a key removes that header. |
| `query` | map(string), map(number), map(bool), or map(list(...)) | Query parameters appended to the URL. Existing query params on the URL are preserved. |
| `timeout` | duration | Per-call request timeout, overriding the client's `timeout`. |
| `follow_redirects` | bool | Per-call override of `redirects.follow`. |
| `max_redirects` | number | Per-call override of `redirects.max`. |
| `cookies` | map(string) or list(cookie object) | Per-call cookies, sent in addition to whatever the jar would supply (and *not* added to the jar). |
| `auth` | string or null | Per-call `Authorization` header value. Bypasses the client's `auth` hook entirely for this request. A `null` value sends no `Authorization` and also bypasses the hook. |
| `retry` | bool or block | `false` disables retries for this call; an inline `{ ... }` overrides the client's policy (counts/delays only — `on_response` is client-only). |
| `as` | string | Pre-decode the body to this form, attaching it to the response object so callers don't need a `get()`. Allowed: `"string"`, `"bytes"`, `"json"`, `"none"`. Default `"none"`. |
| `body_limit` | number | Maximum bytes to read from the response body. Default unlimited. |
| `otel` | object | Object with a `propagate` boolean: per-call OTel header propagation override. |

The `as` option is a convenience: `http_get(ctx, c, url, { as = "json" }).body`
gives a directly usable cty value with no `get()` round-trip.

---

## Header precedence

When building the outbound request, headers are assembled by walking these
sources in order, **per header name**, with later sources overriding earlier
ones. A header set explicitly on a call always wins; defaults from the
client only fill in values that weren't supplied.

| Order | Source | Notes |
|---|---|---|
| 1 (lowest) | Built-in defaults | `User-Agent` defaults to `"vinculum"`. `Accept` defaults to `*/*`. `Content-Type` defaults from the body's type for non-`bytes` bodies. |
| 2 | Client `headers = {...}` | Default headers configured on the client block. `user_agent` shorthand also lands here. |
| 3 | Special-handling headers | `Authorization` from the `auth` hook; OTel propagation headers (`traceparent`, `tracestate`, `baggage`). Layered after step 2 so they can override a `client.headers` `Authorization`, but below the body and call layers so an explicit override still wins. |
| 4 | `Content-Type` from a `bytes` body | When the body is a `bytes` rich object with a non-empty `content_type`, that value is treated as carried with the payload — strictly stronger than the type-based default at level 1 and stronger than anything in `client.headers`. |
| 5 (highest) | Per-call `opts.headers` | Always wins. Setting a header to `null` removes any value from earlier sources. |

Override is **per header name**, not per source: if `client.headers` sets
`Accept` and `opts.headers` sets `X-Foo`, the merged set has both. The
`Content-Type` rule deserves special note: a body's content type is a
property of the payload, not a request default, so
`filebytes("/photo.jpg", "image/jpeg")` (which produces a `bytes` value
with `content_type = "image/jpeg"`) wins over a client default of
`Content-Type = application/json`. If the caller
really wants to lie about the content type, they can still do so via
`opts.headers."Content-Type" = "..."` at level 5.

---

## Base URL query parameters

When `base_url` includes query parameters, those parameters are **merged
into every relative call**, not dropped. This deviates from RFC 3986's
`ResolveReference` algorithm (which would drop the base's query whenever
the reference has any query of its own) but matches the user expectation
for "set this once on the client, send it on every request" — the same
ergonomics that `headers` and `auth` provide.

```hcl
client "http" "weather" {
    base_url = "https://api.weather.example.com/v1/?api_key=secret"
}

# Sends: GET /v1/forecast?api_key=secret&zip=02139
http_get(ctx, client.weather, "forecast", { query = { zip = "02139" } })

# Sends: GET /v1/forecast?api_key=secret&zip=02139
http_get(ctx, client.weather, "forecast?zip=02139")
```

Merge rules, in precedence order:

1. Per-call `opts.query` and any query string on the call's `url` argument
   are the strongest source. On a key collision with `base_url`, the call's
   value(s) win and the base's value(s) for that key are dropped (not
   appended).
2. `base_url` query parameters fill in any keys the call did not set.
3. The merge applies only when the call's URL is **relative** to
   `base_url`. An absolute call URL bypasses `base_url` entirely, including
   its query parameters.

---

## Response object

The return value of every verb function is an `httpclientresponse` rich
object. Static attributes are materialized for direct access; a small set
of `get()` keys cover the body and other expensive operations.

### Static attributes

| Attribute | Type | Description |
|---|---|---|
| `status` | number | Status code (e.g. `200`). |
| `status_text` | string | Status line (e.g. `"200 OK"`). |
| `ok` | bool | `true` if `status` is in `200..299`. Mirrors `fetch().ok`. |
| `redirected` | bool | True if at least one redirect was followed. |
| `final_url` | url object | URL of the final response after redirects. |
| `proto` | string | E.g. `"HTTP/2.0"`. |
| `content_length` | number | Response `Content-Length`, or `-1` if unknown. |
| `content_type` | string | `Content-Type` cleaned of parameters (e.g. `"application/json"`). |
| `body` | dynamic | Pre-decoded body, set only when `opts.as` was used. |

### `get(r, ...)` keys

The response object also implements `Gettable` for body access and other
expensive lookups:

- `get(r, "body")` — full body as `string`.
- `get(r, "body_bytes")` — `bytes` rich object with `content_type` populated
  from the response's `Content-Type` header.
- `get(r, "body_json")` — body parsed as JSON to a dynamic value.
- `get(r, "header", name)` → string (first/only value, case-insensitive).
- `get(r, "header_all", name)` → list(string).
- `get(r, "cookie", name)` → cookie object (any cookie set on the response).
- `get(r, "cookies")` → list(cookie object) — all `Set-Cookie` headers.

The body is buffered in memory at request time, so `body*` accessors can
be called any number of times without consuming the stream. (Streaming
responses are a future extension.)

The response object also implements:

- `tostring(r)` — returns the body as a string.
- `length(r)` — returns the buffered body length, or `content_length` if no
  body has been read.

---

## Authentication

The `auth` attribute is an action expression that produces the value of
the `Authorization` header. It is evaluated **lazily**: never at config
load time, only on demand.

### When the hook runs

1. Just before the first request the client makes, if no cached value
   exists.
2. Whenever a response comes back with status `401 Unauthorized`. The
   cached value is invalidated, the hook is re-evaluated, and the same
   request is retried **once** with the new value.
3. When the cached credential's TTL has elapsed (see below). This is
   triggered lazily by the next outbound request — there is no background
   refresh goroutine.
4. **Never** on `403`. A 403 means *authenticated but not allowed* — re-running
   the auth hook will not help, so the response is surfaced to the caller.

### Static client-level TTL

Set `auth_max_lifetime` on the client block. After that much time has
passed since the last successful evaluation, the next request triggers
re-evaluation regardless of whether a 401 has been seen. Useful when the
upstream's token lifetime is well-known and fixed.

```hcl
client "http" "azure" {
    auth              = basicauth(env.USER, env.PASS)
    auth_max_lifetime = "55m"   # tokens last 60m, refresh 5m early
}
```

### Dynamic per-credential TTL

When the upstream returns the token's actual lifetime, the auth expression
can return that information directly. The expression may return either:

- A **string** — used directly as the `Authorization` header. Cached until
  the next 401 (or until `auth_max_lifetime`, if set).
- An **object** with the following shape:

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `value` | string | yes | The header value. |
| `expires_in` | duration | no | Time until this credential should be considered stale. |
| `expires_at` | time | no | Absolute expiry time. Mutually exclusive with `expires_in`. |
| `refresh_early` | duration | no | Subtract this from the computed expiry to refresh proactively. Defaults to `0s`. |

Both forms compose with `auth_max_lifetime`: the *earlier* of the
expression's TTL and the client's `auth_max_lifetime` wins. A 401 always
invalidates the cache regardless of TTL — TTL is an *upper bound* on
credential age, not a lower bound on its validity.

```hcl
client "http" "azure" {
    base_url = "https://management.azure.com"

    # The hook makes its own HTTP call back through this same client to
    # fetch a token. Reentrancy suppression (see below) prevents an
    # infinite loop.
    auth = "Bearer ${get(
        http_post(ctx, client.azure, "/${env.TENANT_ID}/oauth2/v2.0/token",
            {
                client_id     = env.CLIENT_ID
                client_secret = env.CLIENT_SECRET
                grant_type    = "client_credentials"
                scope         = "https://management.azure.com/.default"
            }
        ),
        "body_json"
    ).access_token}"
}
```

### Reentrancy suppression

The hook can call any function, including the `http_*` verbs — even
against the same client. To make this safe, the verb function maintains
a per-client marker on the Go context for the duration of the hook
evaluation. Any `http_*` call made from inside the hook against the same
client is sent **without an `Authorization` header** (or with whatever
`opts.auth` supplies for that one call). Calls to *other* clients are
unaffected.

### Concurrency

When multiple in-flight requests on the same client all see a 401
simultaneously, only one re-evaluation of the hook runs; the others wait
on its result. This is the standard "singleflight" pattern.

### Repeated failures

If the auth hook returns an error or returns a value that the next
request also responds to with 401, that counts as a failure. After
`auth_max_failures` consecutive failures, the client enters a *failed*
state:

- New requests fail fast (raise an error) instead of attempting to call
  the hook again.
- A backoff timer (`auth_retry_backoff`) governs the next retry attempt.
- A successful response resets the failure counter and the backoff.

The combination of "fail fast" + "exponential backoff" prevents tight
loops against an upstream that's rejecting credentials.

### `ctx.auth_attempt`

When evaluating the `auth` expression, the eval context includes a small
extra object describing why the hook is running:

| Field | Type | Description |
|---|---|---|
| `ctx.auth_attempt.reason` | string | `"initial"` or `"unauthorized"` |
| `ctx.auth_attempt.failures` | number | Number of consecutive prior failures |
| `ctx.auth_attempt.previous_status` | number | Status of the prior response (`0` on initial) |

---

## Retry and backoff

When `retry` is configured, retries are applied to:

- Network/connection errors (dial, TLS handshake, premature close, reset)
- Request timeouts (Go-level `context deadline exceeded`)
- HTTP statuses listed in `retry.retry_on` (default `[429, 502, 503, 504]`)

Retries are **only attempted on idempotent verbs** (`GET`, `HEAD`, `PUT`,
`DELETE`, `OPTIONS`) by default. `PATCH` is **not** retried by default
(per RFC 5789, `PATCH` is not guaranteed idempotent), nor is `POST`. To
opt either of them in, set `retry.allow_non_idempotent = true`.

### Backoff

Standard exponential backoff with optional full jitter:

- attempt 1: immediate
- attempt 2: `initial_delay`
- attempt 3: `initial_delay * backoff_factor`
- ...
- capped at `max_delay`
- with full jitter applied if `jitter = true` (the default)

### `respect_retry_after`

When `true` (the default), an HTTP `Retry-After` header on a `429` or
`503` response overrides the computed backoff for that single retry.
Both delta-seconds and HTTP-date forms are honored.

### `on_response` hook

Useful for upstreams that signal back-pressure in non-standard ways. The
expression has access to:

- `ctx.response` — the response object as it would be returned to the caller
- `ctx.attempt` — current attempt number (1-indexed) that just completed

Return values:

| Return | Effect |
|---|---|
| number / duration | Wait this long, then retry |
| `true` | Retry using normal backoff |
| `false` / `null` | Do not retry; return the response as-is |

```hcl
retry {
    max_attempts = 5

    # Azure-style: x-ms-retry-after-ms header gives the delay in milliseconds
    on_response = ctx.response.status == 429
        ? duration("${get(ctx.response, "header", "x-ms-retry-after-ms")}ms")
        : null
}
```

The `on_response` hook is supported on the client only, not as a per-call
override.

### Per-call retry override

The verb function's `opts.retry` accepts:

- `false` — disable retries for this call entirely
- an inline block — override the client's retry policy

```hcl
http_get(ctx, client.api, "/data", { retry = false })

http_get(ctx, client.api, "/data", {
    retry = {
        max_attempts  = 5
        initial_delay = "1s"
    }
})
```

---

## Cookie jar

When `cookies.enabled = true` (the default when the `cookies` block is
present), the client maintains a Go `cookiejar.Jar` behind the transport.
Cookies set by responses are stored and re-sent on subsequent matching
requests, scoped per the public-suffix list when `public_suffix = true`
(the default).

The jar is **in-memory and per-client**: it does not survive process
restarts. Persistence to disk is a possible future extension.

Per-call `opts.cookies` are sent in addition to whatever the jar supplies
and **do not modify the jar**. Two shapes are accepted:

- `map(string)` — name → value pairs (most common case)
- `list(cookie object)` — full cookie objects with optional `path`,
  `domain`, `secure`, `http_only`

```hcl
client "http" "api" {
    base_url = "https://api.example.com"
    cookies {}
}

# Login response sets a session cookie; the jar stores it.
http_post(ctx, client.api, "/login", { user = "alice", pass = "..." })

# Subsequent calls automatically include the session cookie.
http_get(ctx, client.api, "/me")

# A per-call cookie alongside the jar.
http_get(ctx, client.api, "/things", {
    cookies = { tracking = "abc123" }
})
```

---

## Redirects

By default the client follows up to 10 redirects (matching Go's
`http.DefaultClient`). The `final_url` attribute of the response object
reflects the URL after redirects, and `redirected = true` indicates that at
least one hop occurred.

### Authorization on redirect

When a redirect crosses origins (different scheme/host/port), the
`Authorization` header is **stripped by default** to avoid leaking
credentials to a different host. This matches the behavior of Go's
`http.Client`. Set `keep_auth_on_redirect = true` to override.

---

## OpenTelemetry

### Header propagation

When `otel.propagate = true` (the default), the client injects the
standard W3C trace propagation headers (`traceparent`, `tracestate`,
`baggage`) into every outbound request, derived from the
`context.Context` carried by the `ctx` argument.

These headers slot into the special-handling tier of the precedence chain
(level 3): they override `client.headers` defaults but lose to per-call
`opts.headers` (and to a `bytes` body's `Content-Type`).

A caller can disable propagation per request:

```hcl
http_get(ctx, client.api, "/probe", {
    otel = { propagate = false }
})
```

### Spans

A new client span is started for the duration of each HTTP call, using
the standard
[`otelhttp`](https://pkg.go.dev/go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp)
instrumentation. Span attributes follow the OpenTelemetry HTTP client
semantic conventions (`http.request.method`, `url.full`, `server.address`,
`server.port`, `http.response.status_code`, `error.type`).

### Metrics

When a metrics provider is configured (auto-wired via a default
[`client "otlp"`](client-otlp.md) or by setting `metrics =` explicitly),
the client emits the standard OpenTelemetry HTTP client metrics:
`http.client.request.duration`, `http.client.request.body.size`,
`http.client.response.body.size`, and `http.client.active_requests`.

The setup is automatic — no per-client metrics attribute is required.

---

## Status assertions: `http_must`

For "this call must succeed with status N or blow up" call sites, use
`http_must()` rather than threading status checks through every caller:

```hcl
# Default: any 2xx is acceptable
data = get(http_must(http_get(ctx, client.api, "/widgets")), "body_json")

# Explicit single status
http_must(http_post(ctx, client.api, "/widgets", w), 201)

# Multiple acceptable statuses
http_must(http_delete(ctx, client.api, "/widgets/${id}"), [200, 204, 404])

# A range
http_must(http_get(ctx, client.api, "/data"), [[200, 299]])

# Mix of numbers and ranges
http_must(http_get(ctx, client.api, "/data"), [404, [200, 299]])
```

When the assertion fails, `http_must` raises a plain HCL error containing
the method, final URL, actual status code and text, expected set, and a
truncated excerpt of the response body (first 512 bytes for text bodies;
binary bodies are summarized as `<N bytes of <media-type>>`).

`http_must` is a thin wrapper. Its value is in making the *common* case
short, not in being the only error-handling tool — for full programmatic
access to a failing response, handle the status check yourself:

```hcl
locals = {
    resp = http_post(ctx, client.api, "/widgets", w)
}
result = local.resp.status == 201
    ? get(local.resp, "body_json")
    : retry_with_fallback(local.resp)
```

---

## The default `null` client

Passing `null` as the client argument is supported and equivalent to a
zero-configured client:

- No default headers (other than the built-in `User-Agent` of `"vinculum"`)
- No auth, no cookies, no retries
- Follow redirects (up to 10)
- TLS using system roots, full verification
- 30-second whole-request timeout
- OTel propagation **on**

This is convenient for one-off probes and trivial scripts. For anything
non-trivial — especially anything that talks to a real upstream API —
configure a `client "http"` block.

---

## Examples

### Authenticated GitHub API client

```hcl
client "http" "github" {
    base_url = "https://api.github.com"

    headers = {
        Accept       = "application/vnd.github+json"
        "User-Agent" = "vinculum-bot/1.0"
    }

    auth = "Bearer ${env.GITHUB_TOKEN}"

    retry {
        max_attempts        = 5
        initial_delay       = "500ms"
        respect_retry_after = true   # GitHub honors Retry-After on 429
    }
}

server "http" "api" {
    listen = ":8080"

    handle "GET /repos/{owner}/{name}" {
        action = http_must(http_get(
            ctx,
            client.github,
            "/repos/${ctx.request.path.owner}/${ctx.request.path.name}",
        ))
    }
}
```

### POST JSON, get JSON back, with `as` pre-decode

```hcl
trigger "interval" "report" {
    delay = "1m"
    action = log_info("widget count", {
        count = http_post(
            ctx,
            client.api,
            "/widgets/search",
            { color = "blue" },
            { as = "json" },
        ).body.total
    })
}
```

### OAuth client-credentials with self-referencing auth hook

```hcl
client "http" "azure" {
    base_url = "https://management.azure.com"

    auth = (
        locals = {
            resp = get(http_post(
                ctx,
                client.azure,
                "/${env.TENANT_ID}/oauth2/v2.0/token",
                {
                    grant_type    = "client_credentials"
                    client_id     = env.CLIENT_ID
                    client_secret = env.CLIENT_SECRET
                    scope         = "https://management.azure.com/.default"
                }
            ), "body_json")
        }
        {
            value         = "Bearer ${locals.resp.access_token}"
            expires_in    = duration("${locals.resp.expires_in}s")
            refresh_early = "30s"
        }
    )
}
```

### Login flow with cookie jar

```hcl
client "http" "myapp" {
    base_url = "https://myapp.example.com"
    cookies {}    # enables the jar
}

trigger "start" "login" {
    action = http_must(http_post(ctx, client.myapp, "/login", {
        username = env.MYAPP_USER
        password = env.MYAPP_PASS
    }))
}

# Subsequent calls automatically include the session cookie set by /login.
trigger "interval" "poll" {
    delay  = "30s"
    action = log_info("status", {
        body = get(http_must(http_get(ctx, client.myapp, "/status")), "body_json")
    })
}
```

### Probe with no client

```hcl
trigger "cron" "healthcheck" {
    at "0 */5 * * * *" "ping" {
        action = log_info("upstream", {
            status = http_get(ctx, null, "https://upstream.example.com/health").status
        })
    }
}
```
