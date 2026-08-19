# Deprecated Features

Features listed here still work but are slated for removal. Each entry gives the date
it was deprecated, what to use instead, and when it is expected to be removed. Loading a
configuration that uses a deprecated feature emits a warning (printed by `vinculum
check` and `vinculum serve`).

| Feature | Deprecated | Replacement | Planned removal |
| --- | --- | --- | --- |
| The [`procedure`](procedure.md) block | 2026-07-03 | [functy (`.cty`) files](functy.md) | a future major release |
| Quoted-string `var` `type` (`type = "number"`) | 2026-07-03 | Unquoted type spec (`type = number`) — see [`var`](config.md#var) and [functy types](functy.md#types) | a future release |

Dates are the release in which the deprecation warning was introduced; see
[CHANGELOG.md](../CHANGELOG.md) for the corresponding version.

---

## Removed behavior

These are not deprecations — the old behavior is gone. They are recorded here
because upgrading requires a config change.

### Inline `auth` blocks

**Removed in 0.46.0.**

Authentication was an anonymous `auth "<mode>" { … }` block written inside each
server or route. It is now a named top-level [`auth`](auth.md) block that servers
and routes reference by name:

```hcl
# Before
server "http" "main" {
    listen = ":8080"

    auth "oidc" {
        issuer = "https://accounts.example.com"
    }

    handle "/healthz" {
        auth "none" {}
        action = "ok"
    }
}

# After
auth "oidc" "corp" {
    issuer = "https://accounts.example.com"
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

The transform is mechanical: hoist the block out, give it a name, and replace it
with a reference. `auth "none" {}` becomes `auth = auth.anonymous` — renamed
because the sentinel's near-twin is `auth.disabled`, and "none" and "disabled"
read as synonyms while meaning opposite things in a list: one admits a request,
the other drops out and leaves the remaining mechanisms enforcing.

Naming it is what makes the rest possible. An anonymous block was rebuilt at
every site that inherited it, so one `auth "oidc"` on a server with eight routes
opened eight connections to the issuer and ran eight key refreshers; a named
block is built once. It is also what lets a route accept
[more than one mechanism](auth.md#accepting-more-than-one-mechanism), and what
gives a protected resource an identity to publish under.

Three further changes come with it:

- **`auth "oauth2"` is now `auth "introspection"`.** The name claimed a whole
  framework but implemented one corner of it — RFC 7662 token introspection.
  `oidc` is equally OAuth2-based, so the old pair implied the two were
  alternatives at the same level, when both take bearer tokens and differ only in
  *how the token is checked*. `oidc` versus `introspection` says that; `oidc`
  versus `oauth2` invited the reading that one of them was the interactive login
  flow, which neither is. Rename the block label; every attribute is unchanged.

  `ctx.auth.method` reports `"introspection"` for this mechanism accordingly.

- **`auth "oidc"` loses `introspect_url`, `introspect_client_id`, and
  `introspect_client_secret`.** Introspection now lives only on
  [`auth "introspection"`](auth.md#introspection), which is the same code path and
  additionally supports `cache_ttl` — so `oidc` with `introspect_url` had been
  making an uncacheable round trip per request. When introspecting, `oidc`'s
  `issuer`, `jwks_url`, `algorithms`, and `clock_skew` did nothing.

- **The Basic realm defaults to the auth block's name** rather than the server's,
  since the block is no longer inside a server. Set `realm` explicitly to keep a
  particular value — browsers key saved credentials on it.

- **`method` is reserved in an `auth "custom"` action's returned object.** It
  names the mechanism that authenticated the request. An action that returned a
  `method` key previously had it passed through to `ctx.auth`; it is now an
  error.

### Standalone `server "mcp"`

**Removed in 0.46.0.**

A `server "mcp"` block could either run its own HTTP listener or be mounted on a
route of a `server "http"` block. It is now always mounted, and the attributes
belonging to a listener it no longer owns — `listen`, `path`, `tls`,
`shutdown_timeout` — are gone, along with its `auth` and `baggage` sub-blocks.

Standalone mode was a second, permanently incomplete HTTP server: it never grew
the request log, `real_ip`, host-scoped routing, or co-residency with `handle`
and `files` blocks, and TLS, tracing, HTTP metrics, and baggage each had to be
back-filled into it separately after landing on `server "http"`. Mounting is
also what the MCP authorization spec needs, since the `/.well-known` endpoint a
client looks for lives at the host root, which the HTTP server owns.

Move the listener to a `server "http"` block and point a route at the MCP
server. Authentication moves to that route, which is now the only place a
request is authenticated:

```hcl
# Before
server "mcp" "tools" {
    listen = ":9000"
    path   = "/mcp"

    tls { enabled = true, cert = "…", key = "…" }
    auth "oidc" { issuer = "https://accounts.example.com" }

    tool "echo" { … }
}

# After
server "mcp" "tools" {
    tool "echo" { … }
}

server "http" "main" {
    listen = ":9000"

    tls { enabled = true, cert = "…", key = "…" }

    handle "/mcp" {
        handler = server.tools

        auth "oidc" { issuer = "https://accounts.example.com" }
    }
}
```

The RFC 8414 authorization-server metadata document that a standalone server
published at `/.well-known/oauth-authorization-server` is no longer served. It
was the wrong document to publish — that one describes an *authorization
server*, and belongs to the identity provider rather than to a resource server —
and MCP clients look for the protected-resource metadata of RFC 9728 instead.

### Tolerant wire-format decoding

**Removed in 0.44.0.**

Every messaging receiver (rabbitmq, mqtt, kafka, sqs, redis pub/sub, redis
stream) used to swallow deserialize failures: it logged a warning, substituted
the **raw bytes** for the payload, and delivered the message anyway. This
happened even when the config explicitly said `wire_format = "json"`, so there
was no way to express "messages on this stream must be JSON".

A decode failure is now fatal to the message. It is not delivered; each client
applies its normal failure path (nack, no offset commit, no delete, and so on).

Pick a replacement based on what your subscriber does with the payload:

| You want | Use | You get on undecodable input |
| --- | --- | --- |
| Decode JSON, keep anything else as binary | `wire_format = "auto_bytes"` | a [`bytes`](functions.md) value |
| Decode JSON, keep anything else as text | `wire_format = "auto"` | a `string` |
| Never decode | `wire_format = "bytes"` | a `bytes` value, but valid JSON is never decoded either |
| Strict (the new default for named formats) | `wire_format = "json"` | the message fails |

`auto_bytes` is the closest analogue to the old fallback: it decodes JSON just
like `auto`, and hands you the undecoded payload as a `bytes` value otherwise.
Use `auto` instead if your subscriber wants text.

This applies to custom formats too: any format registered by a
[`wire_format` block](config.md) or a [plugin](plugins.md) is now strict, since
the client no longer catches its `Deserialize` errors.

To observe failures rather than just drop them, set
[`on_decode_error`](client-rabbitmq.md#on_decode_error) on the receiver.

**Poison messages.** For kafka and redis streams a failed message is retried
rather than dropped, so without a dead-letter destination a malformed message
can stall progress. Vinculum warns at config load in both cases; see the
per-client "Decode failures" sections for details.
