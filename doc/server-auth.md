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

---

## `ctx.auth` Object

When authentication succeeds, `ctx.auth` is available in all action expressions with these fields:

| Field | Type | Description |
|---|---|---|
| `ctx.auth.username` | string or null | Username from Basic auth credentials map, introspection `username` field, or JWT `preferred_username` claim; null if not available |
| `ctx.auth.subject` | string | Subject identifier (`sub` claim for OIDC/OAuth2; null for Basic auth) |
| `ctx.auth.claims` | object | All claims or attributes returned by the auth provider |

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
- An **http_response / http_redirect value** — that response is sent directly (e.g. for redirects)

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

| Attribute | Type | Default | Description |
|---|---|---|---|
| `issuer` | string | — | OIDC issuer URL; used for discovery. Required unless `jwks_url` is set |
| `jwks_url` | string | — | JWKS endpoint URL; skips OIDC discovery. Mutually exclusive with `introspect_url` |
| `audience` | list(string) | — | If set, the token must contain at least one of these values in its `aud` claim |
| `algorithms` | list(string) | `["RS256","ES256"]` | Permitted JWT signing algorithms |
| `clock_skew` | string, number, or duration | `"30s"` | Permitted clock skew for `exp`/`nbf` validation. String uses Go duration syntax (`"30s"`, `"1m"`); number is seconds |
| `introspect_url` | string | — | Use token introspection instead of local JWT validation |
| `introspect_client_id` | string | — | Client ID for the introspection endpoint (required with `introspect_url`) |
| `introspect_client_secret` | string | — | Client secret for the introspection endpoint (required with `introspect_url`) |

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

| Attribute | Type | Default | Description |
|---|---|---|---|
| `introspect_url` | string | — | RFC 7662 introspection endpoint URL |
| `client_id` | string | — | Client ID sent as HTTP Basic auth to the introspection endpoint |
| `client_secret` | string | — | Client secret sent as HTTP Basic auth to the introspection endpoint |
| `audience` | list(string) | — | If set, the token's `aud` claim must contain at least one of these values |
| `cache_ttl` | string, number, or duration | — | Cache introspection results for this duration. String uses Go duration syntax; number is seconds. By default every request calls the introspection endpoint |

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
- An **http_response / http_redirect value** — that response is sent directly

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
