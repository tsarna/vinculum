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
