# Authentication (`auth` block)

Authentication can be added to `server "http"`, `server "mcp"`, and `server "metrics"` blocks
using an `auth` sub-block. The block label selects the authentication mode.

```hcl
auth "mode" {
    # mode-specific attributes
}
```

On success, the authenticated identity is exposed as `ctx.auth` in action expressions.
On failure, vinculum returns an appropriate HTTP error (401 or 403) before the action is evaluated.

---

## Scoping (HTTP server)

On `server "http"`, an `auth` block may appear at server level and/or inside individual
`handle` and `files` blocks. Block-level auth takes precedence over server-level auth.
`auth "none"` can be used on a block to explicitly opt out of server-level auth.

```hcl
server "http" "api" {
    listen = ":8080"

    auth "basic" {
        credentials = { admin = env.ADMIN_PASSWORD }
    }

    handle "GET /public" {
        auth "none" {}          # this route is unauthenticated
        action = "public"
    }

    handle "GET /private" {
        action = ctx.auth.username  # inherits server-level basic auth
    }
}
```

On `server "mcp"` and `server "metrics"`, a single `auth` block applies to the whole server.

### `disabled`

Any `auth` block (in any mode) accepts an optional `disabled` attribute. When it
evaluates to `true` the block is parsed but inert — treated exactly as if it were
absent (a server-level block falls back to no auth) and its mode-specific required
fields are not validated. The expression sees `env.*`, so one environment variable
can both supply a credential and toggle auth on/off:

```hcl
auth "basic" {
    disabled    = try(env.WEB_PASSWORD, "") == ""   # unset ⇒ no auth
    credentials = { admin = try(env.WEB_PASSWORD, "") }
}
```

This differs from `auth "none"`, which is an explicit, unconditional opt-out used
to override an inherited server-level block.

---

## `ctx.auth` Object

When authentication succeeds, `ctx.auth` is available in all action expressions with these fields:

| Field | Type | Description |
|---|---|---|
| `ctx.auth.username` | string or null | Username from Basic auth credentials map, introspection `username` field, or JWT `preferred_username` claim; null if not available |
| `ctx.auth.subject` | string | Subject identifier (`sub` claim for OIDC/OAuth2; null for Basic auth) |
| `ctx.auth.claims` | object | All claims or attributes returned by the auth provider |

---

## Attributes

The block's label selects the mode, and each mode uses a different subset of
these. Which subset is noted per attribute below, and the [Modes](#modes)
section covers each one in context.

<!-- vinculum:begin block-attrs server http auth level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) |  |  | Expression that authenticates the request itself. |
| `algorithms` | expression |  | `["RS256", "ES256"]` | Permitted token signing algorithms. |
| `audience` | expression |  |  | Accepted `aud` values. |
| `cache_ttl` | expression (duration) |  | `0s` | How long to cache introspection results. |
| `client_id` | string |  |  | Client ID for the introspection endpoint. |
| `client_secret` | string |  |  | Client secret for the introspection endpoint. |
| `clock_skew` | expression (duration) |  | `30s` | Tolerance applied to `exp` and `nbf`. |
| `credentials` | expression |  |  | Map of username to plaintext password. |
| `disabled` | bool |  |  | Parse the block but do not enforce it. |
| `introspect_client_id` | string |  |  | Client ID for the introspection endpoint. |
| `introspect_client_secret` | string |  |  | Client secret for the introspection endpoint. |
| `introspect_url` | string (url) |  |  | RFC 7662 token introspection endpoint. |
| `issuer` | string (url) |  |  | OIDC issuer URL, used for discovery. |
| `jwks_url` | string (url) |  |  | JWKS endpoint to fetch signing keys from. |
| `realm` | string |  |  | Realm shown in the `WWW-Authenticate` header. |

- client_id and client_secret must be specified together.
- introspect_client_id and introspect_client_secret must be specified together.
- Setting jwks_url replaces OIDC discovery, which is what issuer is used for.

**`action`**

`custom` (and `basic`) only. Returns the identity to expose as `ctx.auth`, or a falsey value to reject.

Evaluated against the `http-request` context.

**`algorithms`**

`oidc` only. A JWKS key is used to verify a token only if the algorithm it advertises is listed here, so narrowing the list narrows what the issuer can present. The algorithm always comes from the key rather than the token header, which is attacker-controlled. Unrecognized names, an empty list, and `"none"` are rejected at config load.

**`audience`**

`oidc` and `oauth2`. The token must carry at least one of them.

**`cache_ttl`**

`oauth2` only. Zero calls the introspection endpoint on every request; a cache trades revocation latency for that round trip.

**`client_id`**

`oauth2` only, where it is required.

**`client_secret`**

`oauth2` only, where it is required. Supply it from the environment.

**`clock_skew`**

`oidc` only. Accepts a duration string, a number of seconds, or a duration value.

**`credentials`**

`basic` only. Supply passwords from the environment rather than literals.

**`disabled`**

Behaves as if the block were absent, without validating the mode's required fields. Usually driven by an env expression so one variable can both supply credentials and switch auth off.

**`introspect_client_id`**

`oidc` introspection only.

**`introspect_client_secret`**

`oidc` introspection only.

**`introspect_url`**

Required for `oauth2`; optional for `oidc`, where it adds a revocation check.

**`issuer`**

`oidc` only.

**`jwks_url`**

`oidc` only. Setting it skips OIDC discovery.

**`realm`**

`basic` only. Defaults to the server's name.

<!-- vinculum:end block-attrs server http auth -->

---

## Modes

### `auth "none"`

Explicitly opts out of authentication. On `server "http"`, use this on a `handle` or `files`
block to make a specific route unauthenticated when the server has a default auth block.

```hcl
auth "none" {}
```

---

### `auth "basic"`

HTTP Basic authentication. Credentials are checked against a static map or a custom expression.

```hcl
auth "basic" {
    realm       = "My API"     # optional; defaults to server name
    credentials = expression   # map(string): username → password
    # OR
    action      = expression   # evaluated per request; see below
}
```

Exactly one of `credentials` or `action` must be specified.

#### `credentials`

A `map(string)` or object expression mapping usernames to plaintext passwords.
Evaluated once at config load time (unless it references per-request values).

```hcl
auth "basic" {
    credentials = {
        alice = "s3cr3t"
        bob   = env.BOB_PASSWORD
    }
}
```

On success, `ctx.auth.username` is the authenticated username and `ctx.auth.claims` is null.

#### `action`

An expression evaluated per request. The request is available as `ctx.request`.
Must return one of:

- An **object** — authentication succeeds; the object becomes the base of `ctx.auth`
  (`username` is merged in from the request's Basic auth username)
- **null** — authentication fails (401)
- An **`http_response` value** (from `http::response()` or `http::redirect()`) — that response is sent directly (e.g. for redirects)

```hcl
auth "basic" {
    action = lookup_user(ctx.request.user, ctx.request.password)
}
```

---

### `auth "oidc"`

OpenID Connect authentication. By default, validates the Bearer JWT locally using
the issuer's JWKS endpoint. Alternatively, delegates to a token introspection endpoint.

```hcl
auth "oidc" {
    issuer    = "https://accounts.example.com"   # required unless jwks_url is set
    jwks_url  = "https://..."                     # optional; skip OIDC discovery

    audience   = ["api.example.com"]             # optional; list of accepted aud values
    algorithms = ["RS256", "ES256"]              # optional; default ["RS256", "ES256"]
    clock_skew = "30s"                           # optional; default 30s

    # Alternatively, use introspection instead of local JWT validation:
    introspect_url          = "https://..."      # RFC 7662 introspection endpoint
    introspect_client_id    = "..."
    introspect_client_secret = "..."
}
```

Uses `issuer` (required unless `jwks_url` is set), `jwks_url`, `audience`,
`algorithms`, `clock_skew`, and — to introspect rather than validate locally —
`introspect_url` with `introspect_client_id` and `introspect_client_secret`. See
[Attributes](#attributes) for their types and defaults.

At startup, vinculum fetches the OIDC discovery document from `{issuer}/.well-known/openid-configuration`
and caches the JWKS endpoint. The JWKS key set is refreshed automatically in the background and on
unknown `kid` values (to handle key rotation).

On `server "mcp"` in standalone mode, vinculum automatically serves the discovery document at
`GET /.well-known/oauth-authorization-server` to support MCP clients that implement the
[MCP OAuth2 authorization flow](https://spec.modelcontextprotocol.io/specification/2025-03-26/basic/authorization/).

On success, `ctx.auth` contains:

| Field | Value |
|---|---|
| `ctx.auth.username` | `preferred_username` claim (local JWT) or `username` field (introspection); null if absent |
| `ctx.auth.subject` | `sub` claim |
| `ctx.auth.claims` | All JWT claims / introspection fields (excluding `active`) |

---

### `auth "oauth2"`

OAuth2 token introspection (RFC 7662). Validates opaque or JWT Bearer tokens by calling
the introspection endpoint on each request (with optional caching).

```hcl
auth "oauth2" {
    introspect_url = "https://auth.example.com/oauth/v2/introspect"   # required
    client_id      = "my-client-id"                                    # required
    client_secret  = env.CLIENT_SECRET                                 # required

    audience  = ["api.example.com"]  # optional; list of accepted aud values
    cache_ttl = "60s"                # optional; default no caching
}
```

Uses `introspect_url`, `client_id`, and `client_secret` — all required — plus
optional `audience` and `cache_ttl`. The client ID and secret are sent as HTTP
Basic auth to the introspection endpoint. See [Attributes](#attributes) for
their types and defaults.

On success, `ctx.auth` contains:

| Field | Value |
|---|---|
| `ctx.auth.username` | `username` field from the introspection response; null if absent |
| `ctx.auth.subject` | `sub` field from the introspection response |
| `ctx.auth.claims` | All fields from the introspection response (excluding `active`) |

---

### `auth "custom"`

Evaluates an HCL expression per request. The result determines the outcome:

```hcl
auth "custom" {
    action = expression
}
```

The `action` expression has `ctx.request` in scope (the incoming HTTP request).
It must return one of:

- An **object** — authentication succeeds; the object becomes `ctx.auth`
- **null** — authentication fails (401 Unauthorized)
- An **`http_response` value** (from `http::response()` or `http::redirect()`) — that response is sent directly

```hcl
auth "custom" {
    action = lookup_session(get(ctx.request, "cookie", "session").value)
}
```

---

## Examples

### OIDC with Zitadel (local JWT validation)

```hcl
server "http" "api" {
    listen = ":8080"

    auth "oidc" {
        issuer   = "https://auth.example.com"
        audience = ["my-api-client-id"]
    }

    handle "GET /me" {
        action = jsonencode(ctx.auth)
    }
}
```

### OAuth2 introspection with caching

```hcl
server "http" "api" {
    listen = ":8080"

    auth "oauth2" {
        introspect_url = "https://auth.example.com/oauth/v2/introspect"
        client_id      = env.INTROSPECT_CLIENT_ID
        client_secret  = env.INTROSPECT_CLIENT_SECRET
        cache_ttl      = "60s"
    }

    handle "GET /me" {
        action = jsonencode(ctx.auth)
    }
}
```

### MCP server with OIDC (supports MCP OAuth2 flow)

```hcl
server "mcp" "tools" {
    listen = ":9000"

    auth "oidc" {
        issuer   = "https://auth.example.com"
        audience = ["my-api-client-id"]
    }

    tool "whoami" {
        description = "Return the authenticated user's identity"
        action      = jsonencode(ctx.auth)
    }
}
```

### Metrics server with Basic auth

```hcl
server "metrics" "metrics" {
    listen = ":9090"

    auth "basic" {
        credentials = { prometheus = env.SCRAPE_PASSWORD }
    }
}
```

### Per-route auth with server-level default

```hcl
server "http" "api" {
    listen = ":8080"

    auth "oidc" {
        issuer = "https://auth.example.com"
    }

    handle "GET /.well-known/health" {
        auth "none" {}
        action = "ok"
    }

    handle "GET /api/me" {
        action = jsonencode(ctx.auth)
    }

    handle "GET /api/admin" {
        auth "basic" {
            credentials = { admin = env.ADMIN_PASSWORD }
        }
        action = "admin area"
    }
}
```
