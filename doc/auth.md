# Authentication (`auth` block)

An `auth` block declares a way of authenticating an HTTP request and gives it a
name. Servers and routes then reference that name.

```hcl
auth "oidc" "corp" {
    issuer   = "https://accounts.example.com"
    audience = ["api.example.com"]
}

server "http" "main" {
    listen = ":8080"
    auth   = auth.corp

    handle "/healthz" {
        auth   = auth.anonymous
        action = "ok"
    }
}
```

The first label selects the mechanism, and the second names it. The name is what
appears in expressions as `auth.<name>`, and one block is one instance — an
`auth "oidc"` referenced from twenty routes fetches the issuer's keys once and
refreshes them once, not twenty times.

> **Changed in 0.46.0.** Authentication used to be an `auth "<mode>" { … }`
> block written inside each server or route. Hoist those into named top-level
> blocks and reference them; `auth "none" {}` becomes `auth = auth.anonymous`. See
> [deprecations](deprecations.md#inline-auth-blocks).

This block configures **inbound** authentication. The `auth` sub-blocks on
`client "mqtt"`, `client "rabbitmq"`, and the `.vinit` `git` block are unrelated
— they carry *outbound* credentials and are documented with those blocks.

---

## Where authentication applies

`auth` is an attribute of [`server "http"`](server-http.md) and
[`server "metrics"`](server-metrics.md), and of the `handle` and `files` blocks
inside an HTTP server.

A route with no `auth` of its own inherits its server's. A route that sets one
**replaces** what it inherited rather than adding to it, so what protects a
route is readable from the route:

```hcl
server "http" "main" {
    listen = ":8080"
    auth   = auth.corp          # the default for everything below

    # inherits auth.corp
    handle "/api/" { action = … }

    # deliberately open
    handle "/healthz" {
        auth   = auth.anonymous
        action = "ok"
    }

    # something stricter
    handle "/admin/" {
        auth   = auth.admins
        action = …
    }
}
```

A server that is mounted into a route — [`server "mcp"`](server-mcp.md),
`server "vws"`, `server "websocket"`, or a mounted `server "metrics"` — is
authenticated by the route that mounts it. It has no `auth` of its own, so that
there is exactly one answer to whether a request was authenticated, and one
place a 401 can come from.

---

## `auth.anonymous` — allowing unauthenticated requests

`auth.anonymous` is predefined. It asserts that a request may proceed without
authenticating, and is the only way to write that:

```hcl
handle "/healthz" {
    auth   = auth.anonymous
    action = "ok"
}
```

Use it for what genuinely must be reachable without credentials — a health
check, a login endpoint that cannot require the credential it hands out, a
public landing page under an otherwise-protected server.

---

## Accepting more than one mechanism

`auth` also takes a list. The first mechanism that **recognizes** the request's
credential judges it, and its rejection is final:

```hcl
auth "oidc"  "corp"  { issuer = "https://accounts.example.com" }
auth "basic" "break_glass" { credentials = { ops = env.EMERGENCY_PASSWORD } }

handle "/admin/" {
    auth = [auth.corp, auth.break_glass]
}
```

A request with a bearer token is judged by `auth.corp` — if the token is bad it
gets a 401 rather than falling through to try the password. That matters: falling
through would let a caller grind against every mechanism a route accepts, and
would make which one rejected them unknowable.

The motivating case is the one above. An identity provider outage otherwise locks
you out of the very route you would use to fix it.

Written last, `auth.anonymous` allows a request that carried no credential at all —
which is different from allowing a *bad* one:

```hcl
handle "/wiki/" {
    auth = [auth.corp, auth.anonymous]     # read anonymously, sign in to edit
}
```

A reader with no cookie is anonymous and `ctx.auth` is null; a reader with an
expired token is rejected. The handler branches on `ctx.auth == null`.
`auth.anonymous` must be written last — it applies however it is ordered, and writing
it first reads as though anonymous access wins.

When a request is not claimed by anything and `auth.anonymous` is absent, the response
is a 401 offering **every** challenge the listed mechanisms can issue (RFC 7235
permits more than one `WWW-Authenticate` header), so a client is told about all
of its options rather than one picked arbitrarily.

---

## Turning a mechanism off

`disabled = true` leaves the block parsed but inert, and its required attributes
unvalidated — so one environment variable can both supply a credential and switch
the mechanism off:

```hcl
auth "basic" "web" {
    disabled    = env.WEB_PASSWORD == ""
    credentials = { admin = env.WEB_PASSWORD }
}
```

The name still resolves. A route naming only a disabled mechanism is
unauthenticated; a route naming it alongside others is left with the others:

```hcl
handle "/admin/" { auth = [auth.corp, auth.break_glass] }
```

With `break_glass` disabled this is `[auth.corp]` — still protected. This is why
disabling is not the same as `auth.anonymous`: if it were, switching off one mechanism
would silently open a route that another mechanism was still guarding.

A route that becomes unauthenticated *because* every mechanism it named is
disabled logs a warning at startup naming the route. Writing `auth = auth.anonymous`
does not warn — that is the deliberate opt-out.

### Choosing between mechanisms

Several blocks may share a name so long as no more than one is enabled, which
lets an environment variable pick between them:

```hcl
auth "oidc" "site" {
    disabled = env.DEV_MODE != ""
    issuer   = "https://accounts.example.com"
}

auth "basic" "site" {
    disabled    = env.DEV_MODE == ""
    credentials = { dev = "dev" }
}

server "http" "main" {
    listen = ":8080"
    auth   = auth.site
    …
}
```

They need not share a mechanism, which is the point. Two *enabled* declarations
of one name is an error — which one a reference meant would otherwise depend on
declaration order.

---

## `ctx.auth`

An authenticated request exposes its identity to every expression it reaches:

| Field | Value |
|---|---|
| `ctx.auth.username` | Human-readable name, or null when the mechanism has none |
| `ctx.auth.subject` | Stable identifier — the `sub` claim, the Basic username, the proxy's user header |
| `ctx.auth.claims` | Everything else the mechanism produced: all JWT claims, all introspection fields, the proxy's email and groups |
| `ctx.auth.method` | Which mechanism authenticated the request (`"oidc"`, `"basic"`, …) |

`ctx.auth` is **null** when no authentication ran — on a route allowing anonymous
access, and everywhere outside an HTTP request. A handler that serves both
anonymous and authenticated callers branches on that:

```hcl
handle "/wiki/{page}" {
    auth   = [auth.corp, auth.anonymous]
    action = ctx.auth == null ? render_readonly(ctx.request.path.page)
                              : render_editable(ctx.request.path.page, ctx.auth.username)
}
```

`method` is reserved: an `auth "custom"` action returning an object that sets it
is an error, rather than having its value silently overwritten.

---

## Mechanisms

### `basic`

<!-- vinculum:begin block-attrs auth basic level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) |  |  | Expression that checks the credentials itself. |
| `credentials` | expression |  |  | Map of username to password. |
| `disabled` | bool |  |  | Switch this mechanism off. |
| `realm` | string |  | `the block's name` | Realm shown in the `WWW-Authenticate` header. |

- Specify at most one of credentials or action.
- Basic auth needs either a map of credentials or an action to check them.

**`action`**

For credentials this block cannot express — a database, an API. `ctx.request.user` and `ctx.request.password` carry what the client sent. Return an object to accept (it becomes `ctx.auth`, with `username` filled in), or a falsey value to reject.

Evaluated against the `http-request` context.

**`credentials`**

Supply passwords from the environment rather than as literals. Comparison is constant-time, so a wrong password does not leak its correct prefix through timing.

**`disabled`**

Unlike other blocks, the name still resolves — to a sentinel meaning "disabled", which is dropped from a route's list of mechanisms. A route naming only this one is then unauthenticated; a route naming it alongside another is left with that one. Required attributes are not validated, so one variable can both supply credentials and switch the mechanism off.

**`realm`**

Browsers show it in the password prompt, and use it to decide which saved credentials to offer.

<!-- vinculum:end block-attrs auth basic -->

Credentials from a map:

```hcl
auth "basic" "ops" {
    realm       = "Ops"
    credentials = {
        alice = env.ALICE_PASSWORD
        bob   = env.BOB_PASSWORD
    }
}
```

Passwords are compared in constant time. Keep them out of the config file
itself — `env.*` reads them from the environment.

Or check them yourself, for credentials a map cannot express:

```hcl
auth "basic" "db_users" {
    action = lookup_user(ctx.request.user, ctx.request.password)
}
```

The action returns an object to accept (it becomes `ctx.auth`, with `username`
filled in from the request) or a falsey value to reject.

Basic credentials are base64-encoded, not encrypted — this belongs behind TLS.

### `oidc`

<!-- vinculum:begin block-attrs auth oidc level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `algorithms` | expression |  | `["RS256", "ES256"]` | Permitted token signing algorithms. |
| `audience` | expression |  |  | Accepted `aud` values. |
| `clock_skew` | expression (duration) |  | `30s` | Tolerance applied to `exp` and `nbf`. |
| `disabled` | bool |  |  | Switch this mechanism off. |
| `issuer` | string (url) |  |  | OIDC issuer URL. |
| `jwks_url` | string (url) |  |  | Key endpoint, named directly. |
| `token_header` | string |  | `Authorization` | Header carrying the token, instead of `Authorization: Bearer`. |

- Setting jwks_url replaces discovery, which is what issuer is for.
- Verifying a token needs the issuer's keys, found either by discovery from issuer or directly at jwks_url.

**`algorithms`**

A key verifies a token only if the algorithm it advertises is listed here, so narrowing the list narrows what the issuer can present. The algorithm always comes from the key rather than from the token header, which is attacker-controlled. Unrecognized names, an empty list, and `"none"` are rejected at config load.

**`audience`**

The token must carry at least one of them. Without this, any token the issuer signed is accepted, including one minted for a different service — set it whenever the issuer serves more than this API.

**`clock_skew`**

Accepts a duration string, a number of seconds, or a duration value.

**`disabled`**

Unlike other blocks, the name still resolves — to a sentinel meaning "disabled", which is dropped from a route's list of mechanisms. A route naming only this one is then unauthenticated; a route naming it alongside another is left with that one. Required attributes are not validated, so one variable can both supply credentials and switch the mechanism off.

**`issuer`**

Its `/.well-known/openid-configuration` document is fetched to find the key endpoint.

**`jwks_url`**

Skips discovery, for an issuer that publishes no discovery document.

**`token_header`**

For a reverse proxy that presents the token under its own name — Cloudflare Access uses `Cf-Access-Jwt-Assertion`, an AWS ALB uses `x-amzn-oidc-data`. The value is the bare token, with no `Bearer ` prefix. The signature is still verified, so unlike [`proxy`](auth.md#proxy) this needs no network-level trust in the proxy.

<!-- vinculum:end block-attrs auth oidc -->

Verifies a JWT against the issuer's published keys:

```hcl
auth "oidc" "corp" {
    issuer   = "https://accounts.example.com"
    audience = ["api.example.com"]
}
```

Verification is local — the issuer is not called per request. The discovery
document and JWKS are fetched on first use and refreshed in the background, and
re-fetched when a token arrives with an unknown key id, so key rotation needs no
restart.

**Set `audience`.** Without it, any token the issuer signed is accepted,
including one minted for a different service by the same provider.

An issuer that is unreachable does not prevent startup: protected routes answer
`503` with `Retry-After` until it responds, and the fetch is retried on a
backoff. It never falls back to allowing requests through.

### `introspection`

<!-- vinculum:begin block-attrs auth introspection level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `client_id` | string | yes |  | Client ID this server presents to the introspection endpoint. |
| `client_secret` | string | yes |  | Client secret this server presents to the introspection endpoint. |
| `introspect_url` | string (url) | yes |  | RFC 7662 token introspection endpoint. |
| `audience` | expression |  |  | Accepted `aud` values. |
| `cache_ttl` | expression (duration) |  | `0s` | How long to reuse an introspection result. |
| `disabled` | bool |  |  | Switch this mechanism off. |

**`client_secret`**

Supply it from the environment rather than as a literal.

**`audience`**

The introspection response must carry at least one of them.

**`cache_ttl`**

Zero calls the endpoint on every request, so a revoked token stops working at once. A non-zero value trades that immediacy for the round trip: a token revoked at the authorization server keeps working here for up to this long.

**`disabled`**

Unlike other blocks, the name still resolves — to a sentinel meaning "disabled", which is dropped from a route's list of mechanisms. A route naming only this one is then unauthenticated; a route naming it alongside another is left with that one. Required attributes are not validated, so one variable can both supply credentials and switch the mechanism off.

<!-- vinculum:end block-attrs auth introspection -->

Asks the authorization server about each token, through its
[RFC 7662](https://datatracker.ietf.org/doc/html/rfc7662) introspection endpoint:

```hcl
auth "introspection" "rs" {
    introspect_url = "https://accounts.example.com/oauth2/introspect"
    client_id      = "my-api"
    client_secret  = env.INTROSPECT_SECRET
    cache_ttl      = "60s"
}
```

Reach for it with opaque tokens, which carry nothing to verify locally, and
wherever a revocation has to take effect before the token would have expired on
its own.

Choose this over `oidc` when revocation must take effect immediately: a signature
stays valid until the token expires, whereas introspection reflects a revoked
token at once. The cost is a round trip per request, which `cache_ttl` trades
back against how quickly a revocation is noticed.

### `custom`

<!-- vinculum:begin block-attrs auth custom level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `action` | expression (action-expression) | yes |  | Expression that authenticates the request. |
| `claims` | expression |  | `claims every request` | Whether the request carries this mechanism's kind of credential. |
| `disabled` | bool |  |  | Switch this mechanism off. |

**`action`**

Return an object to accept — it becomes `ctx.auth` — or `null` to reject with 401. Returning an `http::redirect()` or `http::response()` sends that instead, which is how a browser is sent to a login page. The object may not set `method`, which names the mechanism that authenticated the request and is filled in automatically.

Evaluated against the `http-request` context.

**`claims`**

Only consulted when a route names several mechanisms, to decide which one judges the request — the first to claim it decides, and its rejection is final. Check for the credential's presence here, not its validity: a request bearing a bad session cookie should be claimed and rejected, not handed to the next mechanism. The other mechanisms can be asked this by inspecting a header; an action cannot, since answering would mean running it.

Evaluated against the `http-request` context.

**`disabled`**

Unlike other blocks, the name still resolves — to a sentinel meaning "disabled", which is dropped from a route's list of mechanisms. A route naming only this one is then unauthenticated; a route naming it alongside another is left with that one. Required attributes are not validated, so one variable can both supply credentials and switch the mechanism off.

<!-- vinculum:end block-attrs auth custom -->

For a scheme none of the others covers:

```hcl
auth "custom" "session" {
    claims = get(ctx.request, "cookie", "session") != ""
    action = lookup_session(get(ctx.request, "cookie", "session"))
}
```

Return an object to accept, `null` to reject with 401, or an `http::redirect()`
to send the caller somewhere — a login page, say.

`claims` matters only when a route names several mechanisms: it says whether the
request carries *this* kind of credential, so the right mechanism judges it. Ask
about presence, not validity — a request with an expired session cookie should be
claimed and rejected, not passed to the next mechanism. Without `claims` the
block claims every request, which is what a route naming one mechanism wants.

> **Sessions are yours to get right here.** Vinculum has no cookie sealing —
> no HMAC or AEAD function — so a session cookie built this way is only as good
> as what you put in it. A cookie holding an unauthenticated identifier is
> forgeable by anyone who can guess one. Prefer a long random opaque token
> looked up server-side, and see [`proxy`](#proxy) for delegating the whole
> problem.

### `proxy`

<!-- vinculum:begin block-attrs auth proxy level=4 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `trusted_proxies` | list | yes |  | CIDRs or bare IPs whose identity headers are believed. |
| `disabled` | bool |  |  | Switch this mechanism off. |
| `email_header` | string |  | `X-Forwarded-Email` | Header carrying the user's email address. |
| `groups_header` | string |  | `X-Forwarded-Groups` | Header carrying the user's groups, comma-separated. |
| `user_header` | string |  | `X-Forwarded-User` | Header carrying the authenticated user. |

**`trusted_proxies`**

Required. A request from any other address is rejected outright rather than having its headers ignored, since a request that reached this server without traversing the proxy is not one this mechanism can say anything about.

**`disabled`**

Unlike other blocks, the name still resolves — to a sentinel meaning "disabled", which is dropped from a route's list of mechanisms. A route naming only this one is then unauthenticated; a route naming it alongside another is left with that one. Required attributes are not validated, so one variable can both supply credentials and switch the mechanism off.

**`email_header`**

Becomes `ctx.auth.claims.email`, when the proxy sends it.

**`groups_header`**

Becomes `ctx.auth.claims.groups`, a list, when the proxy sends it.

**`user_header`**

Becomes `ctx.auth.username` and `ctx.auth.subject`.

<!-- vinculum:end block-attrs auth proxy -->

Trusts identity headers set by a reverse proxy that has already authenticated the
user:

```hcl
auth "proxy" "edge" {
    trusted_proxies = ["10.0.0.0/8"]
}
```

The defaults match [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/)'s
`--pass-user-headers` set, which several other proxies have adopted. Override
them for a proxy that uses different names — oauth2-proxy itself has a second
family behind `--set-xauthrequest`:

```hcl
auth "proxy" "edge" {
    trusted_proxies = ["10.0.0.0/8"]
    user_header     = "X-Auth-Request-User"
    email_header    = "X-Auth-Request-Email"
    groups_header   = "X-Auth-Request-Groups"
}
```

**These headers are plaintext.** The only thing making them trustworthy is that
the request came from the proxy, which requires two things — and only the first
is enforceable from here:

1. **Vinculum must not be reachable except through the proxy.**
   `trusted_proxies` rejects a request arriving from any other address, but
   cannot help if an attacker can route through the proxy's own address.
2. **The proxy must replace these headers on every request**, never append to
   what the client sent. Only the proxy's own configuration can guarantee that.

Where the proxy can pass on the token it verified, prefer `oidc` — see below.

---

## Working with a reverse proxy

If you are using an authenticating proxy, there are three integrations, in
descending order of preference.

**1. The proxy passes the token through** (oauth2-proxy's
`--pass-authorization-header`). Use `oidc` unchanged:

```hcl
auth "oidc" "corp" { issuer = "https://accounts.example.com" }
```

Vinculum verifies the signature itself, so it does not have to trust the network
path at all. Neither condition in [`proxy`](#proxy) applies. This is the
integration to reach for.

**2. The proxy passes the token under its own header** — Cloudflare Access sends
`Cf-Access-Jwt-Assertion`, an AWS ALB sends `x-amzn-oidc-data`. Same mechanism,
one attribute:

```hcl
auth "oidc" "cloudflare" {
    issuer       = "https://<team>.cloudflareaccess.com"
    token_header = "Cf-Access-Jwt-Assertion"
}
```

Still a verified signature, so still no network trust required.

**3. The proxy passes a plaintext identity.** Then [`proxy`](#proxy) is the
answer, with the caveats above.

Vinculum does not implement the interactive authorization-code flow — there is no
login redirect, session cookie, or logout endpoint built in. A proxy is the
supported way to put a browser login in front of it.

---

## Examples

### An API and its admin route

```hcl
auth "oidc" "corp" {
    issuer   = "https://accounts.example.com"
    audience = ["api.example.com"]
}

auth "basic" "break_glass" {
    disabled    = env.EMERGENCY_PASSWORD == ""
    credentials = { ops = env.EMERGENCY_PASSWORD }
}

server "http" "main" {
    listen = ":8080"
    auth   = auth.corp

    handle "/healthz" {
        auth   = auth.anonymous
        action = "ok"
    }

    handle "/api/" { action = … }

    handle "/admin/" {
        auth   = [auth.corp, auth.break_glass]
        action = …
    }
}
```

`break_glass` is off unless the environment supplies a password, and switching it
off leaves `/admin/` protected by `auth.corp` rather than opening it.

### An MCP server behind OIDC

```hcl
auth "oidc" "corp" { issuer = "https://accounts.example.com" }

server "mcp" "tools" {
    tool "whoami" {
        description = "Return the caller's identity"
        action      = jsonencode(ctx.auth)
    }
}

server "http" "main" {
    listen = ":8080"

    handle "/mcp" {
        handler = server.tools
        auth    = auth.corp
    }
}
```

### Metrics behind a scrape credential

```hcl
auth "basic" "prometheus" {
    credentials = { prometheus = env.SCRAPE_PASSWORD }
}

server "metrics" "metrics" {
    listen = ":9090"
    auth   = auth.prometheus
}
```

### Behind oauth2-proxy

```hcl
auth "proxy" "edge" {
    trusted_proxies = ["10.0.0.0/8"]
}

auth "oidc" "api" {
    issuer   = "https://accounts.example.com"
    audience = ["api.example.com"]
}

server "http" "main" {
    listen = "10.0.1.5:8080"     # bound where only the proxy can reach it

    handle "/" {
        auth   = [auth.edge, auth.api]
        action = "hello ${ctx.auth.username}"
    }
}
```

Browser users arrive through the proxy with identity headers; scripts present a
bearer token directly. Both reach the same route, and `ctx.auth.method` says
which was used.
