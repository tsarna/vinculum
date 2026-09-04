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

A **renamed function or `ctx` field** is not listed individually below, because
`vinculum check` names the replacement itself:

```text
There is no function named "sunrise". It was renamed "sky::sunrise" in 0.43.0.
```

It knows the renames whose spelling moved far enough that guessing could not
find them — `now` to `time::now`, `basicauth` to `http::basic_auth`, `serialize`
to `wire::serialize`. For the rest it offers a nearest match instead
(`randint` → "Did you mean `rand::int`?"), which is the same answer arrived at
differently. Either way an upgrade can be driven by running `vinculum check`
until it is quiet, rather than by reading a list.

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

### `commit_mode = "manual"`

**Removed in 0.46.0.** On the [`kafka`](client-kafka.md) `receiver` block.

`manual` was documented as never committing automatically, reserved for a
caller-controlled commit. Nothing could perform that commit, and rather than
committing nothing, the mode left the Kafka client's own periodic autocommit
switched on. **Offsets advanced on a five-second timer regardless of whether
processing succeeded** — so a config that selected `manual` to take control of
its commits got `periodic`, the weakest guarantee in the enum, while reading
documentation promising the strongest.

It is not quietly redefined, because a config that asked for `manual` did not
ask for any of the surviving modes and the difference between them is which
messages it loses:

| You had | Use | What changes |
| --- | --- | --- |
| `commit_mode = "manual"` | `ack = "auto"` | At-least-once. The offset advances only once the message has been handled, so a failed message is redelivered rather than skipped. |
| `commit_mode = "manual"` | `ack = "manual"` + `settle_timeout` | What the name always promised: nothing is committed until the configuration calls `inbound::ack()` or `inbound::nack()`. |
| `commit_mode = "manual"` | `ack = "periodic"` | Nothing. This is what the receiver was already doing. |

`ack = "auto"` is the right choice for almost everyone, and is the default.
Pick `periodic` only to keep the existing behavior deliberately, and `manual`
only where the configuration really does decide when a record is finished.

The attribute itself was renamed in the same release — see
[settle attributes](#settle-attributes-auto_ack-auto_delete-commit_mode) below.
**Caller-controlled settle arrived in the same release it was removed in**, on
the low-water-mark commit tracker it always needed: see
[how offsets are committed](client-kafka.md#how-offsets-are-committed).

### Settle attributes: `auto_ack`, `auto_delete`, `commit_mode`

**Removed in 0.46.0.** Replaced by a single `ack` attribute on every receiver.

Four receivers spelled one concept four ways, and they disagreed worse than
cosmetically: `redis_stream.auto_ack` defaulted true and meant *vinculum*
acknowledges after delivery; `rabbitmq.auto_ack` defaulted false and meant the
*broker*-side no-ack mode; `sqs_receiver.auto_delete` was the Redis thing under
a third name; and `kafka.commit_mode` was a three-way enum none of whose values
was manual in the sense the other three meant it.

**No configuration changes meaning**, because every one of the four already
behaved as `ack = "auto"` by default — RabbitMQ's `auto_ack = false` included:

| Receiver | You had | Use |
| --- | --- | --- |
| `redis_stream` `consumer` | `auto_ack = true` *(default)* | `ack = "auto"` *(default)* |
| `redis_stream` `consumer` | `auto_ack = false` | `ack = "manual"` + `settle_timeout` |
| `sqs_receiver` | `auto_delete = true` *(default)* | `ack = "auto"` *(default)* |
| `sqs_receiver` | `auto_delete = false` | `ack = "manual"` + `settle_timeout` |
| `rabbitmq` `receiver` | `auto_ack = false` *(default)* | `ack = "auto"` *(default)* |
| `rabbitmq` `receiver` | `auto_ack = true` | `ack = "none"` |
| `kafka` `receiver` | `commit_mode = "after_process"` *(default)* | `ack = "auto"` *(default)* |
| `kafka` `receiver` | `commit_mode = "periodic"` | `ack = "periodic"` |
| `kafka` `receiver` | `commit_mode = "manual"` | `ack = "manual"` + `settle_timeout` — see [above](#commit_mode--manual) |

Each type accepts only what it can honour, and its generated attribute
reference lists exactly that: `auto` and `manual` are common to all four,
`periodic` is kafka's alone, and `none` is rabbitmq's.

Writing a retired name reports what it became rather than that the argument is
not expected here, so an upgrade can be driven by running `vinculum check`
until it is quiet:

```text
"auto_ack" is now "ack" (since 0.46.0); `auto_ack = true` is the default, now
written `ack = "auto"`. `auto_ack = false` is `ack = "manual"`, which also
requires `settle_timeout` …
```

A RabbitMQ `receiver`'s `declare { auto_delete }` is untouched — that is AMQP's
delete-the-queue-when-unused, a different attribute in a different block.

### `redis::ack()`, `sqs::delete()`, `sqs::extend_visibility()`

**Removed in 0.46.0.** Replaced by
[`inbound::ack()` / `inbound::nack()` / `inbound::keepalive()`](functions.md#acknowledging-an-inbound-message).

Acknowledgement is a property of the inbound delivery, not of the payload and
not of the subscriber that handles it. The old functions each took two extra
arguments — a client and a settle token — and the token travelled in `fields`,
which the bus rewrites per subscription with that subscription's own topic
captures. So settling only ever worked from the receiver's own `action`, and a
config had to know which protocol delivered a message in order to pick the
function, which defeats routing through a bus.

The replacements take a `ctx` and nothing else, and work from anywhere
downstream:

| You had | Use |
| --- | --- |
| `redis::ack(ctx, client.rs.consumer.in, ctx.fields["$entry_id"])` | `inbound::ack(ctx)` |
| `sqs::delete(ctx, client.tasks, ctx.fields["$receipt_handle"])` | `inbound::ack(ctx)` |
| `sqs::extend_visibility(ctx, client.tasks, handle, 60)` | `inbound::keepalive(ctx)` |

Set `ack = "manual"` on the receiver, with a `settle_timeout`. `vinculum check`
names the replacement for each.

Two things went with them:

- **`$receipt_handle` is no longer a delivered field** on `sqs_receiver`. It had
  no use but deletion — an opaque, per-receive, expiring token that is
  meaningless to log, correlate, or compare — and keeping it would advertise
  saving it somewhere to settle later, which is storing a lease rather than a
  value: it expires while it sits in the variable. Every other `$` system
  attribute stays. `$entry_id` on `redis_stream` also stays, because a Redis
  entry ID identifies the entry independently of acknowledgement.
- **`client.<name>.consumer.<c>`** on a `redis_stream` client is gone, along
  with the receiver capsule `client.<name>` produced on an `sqs_receiver` —
  both existed only to be passed to the removed functions. `client.<name>` on
  an `sqs_receiver` is now the ordinary client capsule.
