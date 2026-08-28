# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Authentication is now a named top-level `auth` block.** It was an anonymous
  `auth "<mode>" { … }` block written inside each server or route; it is now
  `auth "<type>" "<name>"` at the top level, referenced with `auth = auth.<name>` from
  `server "http"` (and its `handle` and `files` blocks) and `server "metrics"`. The
  transform is mechanical — hoist the block, name it, reference it; `auth "none" {}`
  becomes `auth = auth.anonymous`. See [doc/deprecations.md](doc/deprecations.md) for
  before/after and [doc/auth.md](doc/auth.md) for the full reference.

  Naming it is what the rest rests on. An anonymous block was rebuilt at every site
  that inherited it, so one `auth "oidc"` on a server with eight routes opened eight
  connections to the issuer and ran eight background key refreshers. A named block is
  built once, however many routes name it.

  Each mechanism now has its own attributes rather than sharing one struct, so
  `auth "basic" "x" { issuer = … }` is an error instead of being silently ignored, and
  a plugin can register a mechanism the way it can already register a server or client
  type. `doc/server-auth.md` is now `doc/auth.md`, since it documents a block of its
  own rather than part of `server`.

- **`auth "oauth2"` is renamed `auth "introspection"`.** The old name claimed a whole
  framework but implemented one corner of it — RFC 7662 token introspection. `oidc` is
  equally OAuth2-based, so the pair implied the two were alternatives at the same
  level, when both take bearer tokens and differ only in how the token is checked.
  Worse, the familiar name invited the reading that this was the interactive login
  flow, which Vinculum does not implement at all. Every attribute is unchanged; only
  the block label moves. `ctx.auth.method` reports `"introspection"` to match.

### Fixed

- **A server that could not bind its port came up anyway, serving nothing.** Both
  HTTP-bearing servers called `ListenAndServe` inside their own goroutine, so a bind
  failure was logged and otherwise invisible — `server "metrics"` discarded the error
  outright. `Start` now calls `net.Listen` synchronously, and a bind failure stops the
  process: it is logged naming the server, whatever did start is torn down, and the
  exit code is non-zero.

  Exiting is the change from the first pass at this, which came up and reported `503`
  instead. A port conflict is local and never resolves itself, so there is nothing to
  retry and nothing readiness could usefully say about it — and `readiness = false` on
  a server, which is a statement about a dependency not gating traffic, must not also
  mean that a listener which will never accept a connection goes unnoticed. It matches
  what the standalone health listener already did, so the three listeners now agree.

- **`log::` printed a list or object as Go source.** Complex values were formatted by
  hand, falling back to cty's `GoString()`, so logging a structure emitted
  `[cty.ObjectVal(map[string]cty.Value{"component":cty.StringVal(…` — neither readable
  nor JSON. Structures now reach the log as real arrays and objects, a `time` logs as a
  timestamp rather than an opaque handle, and an empty list logs as `[]` rather than
  `null`. Scalars are unchanged and keep their types, so a number is still a number.

- **Overriding a container image's command silently disabled file functions and
  plugins.** The images carried `-f /data`, `-w /data/write`, and
  `--plugin-path /plugins` in `CMD`, and Docker replaces the entire `CMD` as soon as
  the user passes arguments — so `docker run … serve /myconf` dropped all three, and a
  config calling `file()` failed with "no function named file" for no visible reason.
  The three are now `ENV` (`VINCULUM_FILE_PATH`, `VINCULUM_WRITE_PATH`,
  `VINCULUM_PLUGIN_PATH`) and `CMD` is `["serve", "/conf"]`, so they survive a command
  the user supplies and each is separately overridable with `-e`.

  `docker run … vinculum` with no arguments is unchanged. Where a command *was*
  overridden, file functions become enabled rooted at `/data` where they had been
  silently off, and `docker run … check /conf` now validates the configuration under
  the same capabilities `serve` will give it.

- **Two servers of different types sharing a name crashed config processing.**
  `server "http" "x"` alongside `server "mcp" "x"` panicked instead of reporting the
  conflict: server names are global — `server.x` names one server whatever its type —
  but the check looked the colliding block up in its own type's bucket, which is the
  one place a cross-type collision cannot be. Both `server` and `client` now report it
  cleanly and say that the namespace is global. Declaring two same-named blocks and
  using `disabled` to select between them is unaffected.

### Added

- **Readiness and liveness.** Vinculum now knows whether it is able to do its job, and
  will tell you. A process whose broker connection is down is running but not serving,
  and the difference matters to anything routing traffic at it.

  Servers and connection-oriented clients report whether they are serving; a new
  `check "<name>"` block adds any test an expression can express. `/readyz`, `/livez`,
  and `/healthz` serve the result — from `--health-listen` on its own port, or from an
  existing `server "http"` with `health_endpoints`, outside auth and outside logging so
  a kubelet that cannot authenticate can still reach them. A configuration reads the
  same answer with `sys.ready`, `health::ready`, `health::failing`, and friends, and
  `trigger "watch"` over `sys.ready` acts on the transition. See
  [doc/health.md](doc/health.md).

  Readiness and liveness are deliberately not the same question. A broker outage that
  makes every replica *not ready* is a degraded service; one that makes every replica
  *not live* is a fleet-wide restart loop. So everything that can lose a connection
  contributes to readiness, and nothing contributes to liveness except the process
  itself and checks written for it. Startup is not a third answer: readiness reports
  `starting` until boot completes, so a `startupProbe` aimed at `/readyz` behaves
  correctly without a second endpoint.

  There is no background poller. A report is computed when something asks — a probe, a
  function call, a metrics scrape — and cached briefly, so an unauthenticated caller
  hammering `/readyz` cannot make the process work harder than the TTL allows. A client
  that loses its connection does not wait to be asked, though: it reports the drop from
  the same callback that fires `on_disconnect`, so `sys.ready` flips within
  milliseconds even with nothing polling.

  `readiness = false` opts a component out where losing it should not take the process
  out of rotation. It exists only on the types that have readiness to report, and is
  rejected elsewhere rather than silently ignored.

  Every entry carries a `since` timestamp — how long it has held its state — so a fresh
  outage reads differently from one that has been broken all along. Two metrics,
  `vinculum.health.status` and `vinculum.health.component.status`, follow OpenTelemetry's
  status-metric convention, and a verdict change is logged once on the transition rather
  than on every probe.

  `vinculum test` waits for readiness before running, naming whatever is still failing
  if it gives up, and `:ready` prints the report at the interactive prompt.

- **Every CLI flag is settable from the environment.** A flag's name derives its
  variable: uppercase, `-` to `_`, prefixed `VINCULUM_`. Both a bare form
  (`VINCULUM_PLUGIN_PATH`, applying to whichever command runs) and a command-scoped
  one (`VINCULUM_SCHEMA_FORMAT`, applying to that command alone) resolve to the flag,
  with the scoped name winning and an explicit flag winning over both. A variable that
  is set but empty is applied as an empty value, which is how a default baked into a
  container image is switched off. A value the flag rejects stops the run, naming
  every bad variable at once. `--help` names each flag's variable. See
  [doc/cli-env.md](doc/cli-env.md).

  This exists because Docker replaces a container's entire `CMD` as soon as the user
  passes arguments of their own, so a flag the image needs cannot survive there.

- **OAuth discovery for clients given only a URL — `resource` on `auth "oidc"`, and
  `external_url` on `server "http"`.** Setting `resource` publishes an
  [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728.html) protected resource metadata
  document and adds a `resource_metadata` pointer to it on every `401`, so a client
  holding no credentials can find the issuer, obtain a token, and retry. This is what
  the MCP authorization spec requires; a client already configured with credentials
  needs none of it, and blocks that do not set `resource` are unaffected.

  `resource` is either an absolute URL or a path resolved against the referencing
  server's `external_url` — needed because a proxy that terminates TLS leaves the real
  scheme and host invisible from inside. The value must match the URL clients are
  pointed at exactly, since a client checks it against the URL it dialled and refuses a
  mismatch; a relative one therefore belongs to a single `server "http"`. The document
  is served unauthenticated, which is the point of it. Requires `issuer`, since
  `authorization_servers` carries the issuer identifier and a `jwks_url` cannot be
  turned back into one. See [doc/auth.md](doc/auth.md#oauth-discovery).

- **`shutdown_timeout` on every block that accepts inbound connections** — `server "http"`,
  `"metrics"`, `"vws"`, and `"websocket"`. It bounds how long that server waits
  for in-flight work while shutting down, defaulting to `10s`; whatever is still running
  when the time is up is closed out from under it, so one stuck request or one client that
  has stopped reading cannot hold shutdown open. `0` waits indefinitely. On the two
  WebSocket servers what it bounds is connections closing rather than requests finishing,
  since that is what those blocks own.

- **A route may accept more than one authentication mechanism** —
  `auth = [auth.corp, auth.break_glass]`. The first mechanism that *recognizes* the
  request's credential judges it, and its rejection is final rather than falling
  through to the next: falling through would let a caller grind against every
  mechanism a route accepts, and would make which one rejected them unknowable. A
  request no mechanism recognized gets a 401 offering every challenge the route can
  issue (RFC 7235 permits more than one `WWW-Authenticate` header).

  This fixes being locked out of your own admin route by an identity-provider outage.
  It also makes `[auth.corp, auth.anonymous]` meaningful: a request carrying no
  credential is anonymous and `ctx.auth` is null, while one carrying a *bad* credential
  is still rejected — anonymous reads with authenticated writes on a single route.

- **`auth.anonymous` and `auth.disabled`.** `auth.anonymous` is how a route allows an
  unauthenticated request, replacing the inline `auth "none" {}`. `auth.disabled` is
  what the name of a switched-off block resolves to, and is dropped from a route's
  list rather than permitting anonymous access — so disabling one of two mechanisms
  leaves the route protected by the other. Disabling the *only* mechanism still opens
  the route, as it always has, and now logs a warning naming it.

  The sentinel is `anonymous` rather than `none` because these two are exactly where
  a misreading is dangerous, and "none" and "disabled" are near-synonyms in English
  while meaning opposite things here. `anonymous` also reads as a list element:
  `[auth.corp, auth.anonymous]` is "corp, or anonymous", where "none" would read as
  negating the whole list.

  Several `auth` blocks may share a name so long as no more than one is enabled,
  making the "declare both, let an environment variable choose" idiom a rule rather
  than something that happened to work. They need not share a mechanism.

- **`auth "proxy"`** — identity asserted by a reverse proxy that has already
  authenticated the user, reading oauth2-proxy's `X-Forwarded-User`/`-Email`/`-Groups`
  by default and any other names on request. `trusted_proxies` is required: the
  headers are plaintext, so a request from any other address is rejected outright.
  Where the proxy can pass on the token it verified, `auth "oidc"` remains the
  stronger choice and is documented as such.

- **`token_header` on `auth "oidc"`**, for a proxy that presents the token under its
  own header — Cloudflare Access's `Cf-Access-Jwt-Assertion`, an AWS ALB's
  `x-amzn-oidc-data`. The signature is still verified, so this needs no network-level
  trust in the proxy.

- **`ctx.auth.method`** names the mechanism that authenticated the request, so an
  expression can tell which one won on a route that accepts several. It is a reserved
  key: an `auth "custom"` action returning an object that sets it is now an error
  rather than having its value silently overwritten.

### Removed

- **Standalone `server "mcp"`.** An MCP server is now always mounted on a route of a
  `server "http"` block, so the attributes belonging to a listener it no longer owns —
  `listen`, `path`, `tls`, `shutdown_timeout` — are gone, along with its `auth` and
  `baggage` sub-blocks. Authentication moves to the route that mounts it, which makes it
  the single place a request is authenticated; baggage trust belongs to the block that
  accepts the request from outside.

  Standalone mode was a second, permanently incomplete HTTP server: it never grew the
  request log, `real_ip`, host-scoped routing, or co-residency with `handle` and `files`
  blocks, and TLS, tracing, HTTP metrics, and baggage each had to be back-filled into it
  separately after landing on `server "http"`. Mounting is also what the MCP authorization
  spec needs, since the `/.well-known` endpoint a client looks for lives at the host root.

  The RFC 8414 document a standalone server published at
  `/.well-known/oauth-authorization-server` is no longer served: it describes an
  *authorization server*, which is the identity provider's to publish rather than a
  resource server's, and MCP clients look for RFC 9728 protected-resource metadata.

  See [doc/deprecations.md](doc/deprecations.md) for a before/after config.

- **`server "mcp"` loses its `baggage` block.** Every other `baggage` block sits on
  something that owns an inbound edge — the HTTP listener, or a Kafka/MQTT/SQS/RabbitMQ/
  Redis-stream receiver. An MCP server no longer has one, and its filter already did
  nothing when mounted, so which inbound baggage keys are trusted is decided by the
  hosting `server "http"` block.

### Fixed

- **An unreachable OIDC provider no longer stops vinculum from starting.** `auth "oidc"`
  fetched the discovery document and the JWKS while the config was being processed, with
  no timeout, so a provider that was down failed the entire config and one that hung
  never returned at all. Both fetches now happen on first use, bounded by a 10s timeout
  and retried on a backoff; until they succeed, protected routes answer `503` with a
  `Retry-After` rather than ever opening up. Local configuration errors are still
  reported immediately.

- **RFC 7662 introspection had the same missing timeout**, on a path that runs per request
  rather than once at startup. `auth "introspection"` now shares the same bounded client.

- **Authenticators are shut down with the process.** The JWKS background refresher and the
  `auth "introspection"` token-cache sweep both ran on `context.Background()` for the life
  of the process, one per authenticator built. Both are now cancelled during teardown.

- **Servers now stop accepting before the runtime behind them is torn down.** No listening
  server in the tree implemented shutdown at all: `server "http"` registered only a
  `Startable`, `"mcp"` and `"metrics"` built their `http.Server` as a local inside `Start()`
  and dropped the reference, and the graceful `Shutdown` the two WebSocket servers already
  had was never called by anything. Listeners therefore stayed up for the whole teardown
  sequence and died only with the process.

  The visible symptom was not the missing drain but the ordering. Shutdown stopped clients,
  buses, and subscriptions while requests were still arriving, so a request that landed
  during teardown ran its handler against a closed SQL pool, a stopped bus, or a
  disconnected MQTT client — sporadic errors on every deploy rollover, hard to attribute to
  the rollover itself. Teardown now runs in three phases: listeners drain, then
  `trigger "shutdown"` actions, then clients and buses. A shutdown action consequently runs
  with the front door closed and the runtime it needs still up, which is the guarantee it
  always should have had.

  In-flight requests are also now allowed to finish, and WebSocket clients get a close
  frame instead of a severed socket — `http.Server.Shutdown` deliberately ignores hijacked
  connections, so the `vws` and `websocket` blocks drain their own.

- **A WebSocket client that had stopped reading could stall shutdown by seconds per
  connection.** `server "websocket"` closed its connections one after another, and closing
  a WebSocket performs a closing handshake that waits for the peer to answer — about five
  seconds for a peer that never will. Ten such clients meant the better part of a minute,
  independent of any configured grace period. The closes now overlap, so
  `shutdown_timeout` governs the total.

### Security

- **`auth "oidc"` now enforces `algorithms`.** The attribute was parsed, validated as a
  list of strings, and stored — and then never consulted, so `algorithms = ["RS256"]`
  restricted nothing and an operator who narrowed the list got none of the narrowing they
  asked for. It is now applied: a key from the issuer's JWKS is offered to the verifier
  only if the algorithm it advertises is on the list.

  The exposure this closes is narrower than the attribute's name suggests, and worth
  stating precisely. jwx takes the verification algorithm from the JWKS key's own `alg`
  and never from the token header, so the classic header-substitution attacks —
  `alg: none`, RS256 downgraded to HS256 with the public key as the HMAC
  secret — were not reachable before this change and are not what it fixes.
  What was reachable: if the issuer published a key whose algorithm the operator had
  deliberately excluded, a token genuinely signed with that key was accepted anyway.
  
### Changed

- **`auth "oidc"` rejects a bad `algorithms` list at config load** instead of ignoring it.
  Unrecognized algorithm names, an empty list, and `"none"` are now errors. This can fail a
  config that loaded before: a misspelled name such as `["RSA256"]` was previously accepted
  and — like the rest of the list — discarded, so the typo was invisible. It now names the
  offending value at startup.
- **JWT handling moved from `github.com/lestrrat-go/jwx` v2 to v3.** The v2 line is no
  longer maintained. Nothing about `auth "oidc"` changes for a config author beyond the two
  entries above; the JWKS cache still refreshes in the background on the same interval and
  still re-fetches once on an unknown `kid` to ride out a key rotation.

  This stops at v3 rather than going on to v4. jwx v4
  requires `encoding/json/v2`, still gated behind `GOEXPERIMENT=jsonv2` as of Go 1.26 — so
  adopting it would mean setting that flag for every build of vinculum, including CI,
  GoReleaser, all three images, the plugin-build image, and every plugin author's own
  build.

  The `auth "oidc"` code path had no test coverage of its own before this port. It now has
  tests covering the claim mapping onto `ctx.auth`, algorithm enforcement, expiry and clock
  skew, audience mismatch, unknown signing keys, malformed and absent bearer tokens, the
  explicit `jwks_url` path that skips discovery, and the config errors above.

## [0.45.1] - 2026-08-17

### Fixed

- **OTLP export over HTTP reaches the collector again.** `client "otlp"` given a collector's
  base URL — `endpoint = "http://collector:4318"`, the common configuration — POSTed every
  export to `/` instead of `/v1/traces` and `/v1/metrics`, so a stock OpenTelemetry
  Collector answered `404` and no telemetry arrived. The failure was near-silent: a stdlib
  log line on stderr, and spans and metrics simply stopping.

  The exporters used to append each signal's default path themselves; OTel Go v1.45.0
  changed `WithEndpointURL` to pin a path-less URL to `/` instead, and 0.45.0 picked that up
  through two dependency bumps. Vinculum now appends the default path when the configured
  endpoint carries none, which is also the OTLP spec's convention. An endpoint written with
  a path of its own is still used verbatim.

  There was no workaround: `metric_endpoint` defaults to `endpoint`, so spelling out
  `/v1/traces` fixed traces and broke metrics, and `client "otlp"` offers no gRPC transport
  to fall back to.

- **`vinculum serve` logs at `info` again.** It had been running at `warn`, so the startup
  banner, the bus and server start-up lines, HTTP request logs, and the shutdown line were
  all silently dropped — a deployment looked like it was producing no logs at all. Three
  commands bound `--log-level` to the same package-level variable, and pflag writes a
  flag's default into the bound variable at registration time, so `vinculum test`'s
  deliberately quieter `warn` default (added in 0.45.0) overwrote `serve`'s `info` at
  process start. `--help` still printed `info`, since that reads the declared default
  rather than the variable. Each command now owns its own variable, and a test asserts that
  every flag in the tree reads back the default it declared.

## [0.45.0] - 2026-08-14

### Added

- **`vinculum schema` — a machine-readable description of the configuration language.**
  One JSON document covering every block type, every type-specific variant (`client "http"`
  versus `client "mqtt"`), every attribute and nested sub-block, plus prose documentation,
  value hints, and semantic constraints — for editor tooling (completion, hover, linting)
  and generated reference docs. The structure is **reflected from the same decode structs
  the parser uses**, so it describes exactly what that binary can parse rather than what
  someone believed it parsed, and the prose beside it is validated against that structure:
  documenting an attribute that does not exist is an error, and so is adding one without
  documenting it. CI enforces both, so the two cannot drift apart. It describes what an
  expression may *name* as well as what the parser accepts — the shape of `ctx` at each
  kind of evaluation site, and the twelve namespace roots a reference can start from — and
  covers both languages, `.vcl` and the `.vinit` bootstrap format, each block tagged with
  which file it belongs in. Plugin-contributed block types are included when
  `--plugin-path` is given with config paths. Every release attaches a `schema.json`, for
  consumers that would rather fetch one file than run the binary. See
  [`doc/schema.md`](doc/schema.md).
- **Generated reference documentation.** `vinculum schema --format markdown` renders that
  same document as prose instead of JSON: a single-page reference on stdout, or — with
  `--update` — the *marked regions* of the hand-written pages in `doc/`. `doc/` is not
  generated and should not become generated; it carries worked examples, syntax, and the
  reasoning behind a design, none of which the schema knows, so a page marks only the parts
  that are mechanically derivable and keeps the rest. `--check` reports any region that is
  out of date and is what CI runs, so the loop closes in both directions: an undocumented
  attribute fails `--strict --require-docs`, and a documented one whose prose was never
  regenerated fails `--check`. 161 regions across 26 pages are generated today, and
  adopting them is what turned up the drift: the shared `wire_format` prose had been wrong
  for all nine clients that share it — it said payloads pass through unchanged when the
  attribute is omitted, where the default `auto` passes through only strings and bytes and
  JSON-encodes everything else — mqtt's `client_id` was documented as a random identifier
  when it is `vinculum-<name>-<hostname>`, and mqtt's `sender` and `receiver` had no
  attribute table on either side.
- **`vinculum man` — read the configuration-language reference from the binary.** Renders
  what `vinculum schema` emits as documentation for a person, so the reference and the
  parser cannot disagree: it describes exactly what *that* binary parses, including the
  block types a plugin adds. A topic is a path through the language — `vinculum man client
  mqtt` for a type, `client mqtt tls` for a sub-block, `subscription action` for one
  attribute together with the shape of the `ctx` it sees, `sys` for a namespace, `send` for
  a function — and an ambiguous or misspelled one is answered with the commands that
  resolve it rather than an error. Output to a terminal is styled, wrapped, and paged;
  anywhere else it is Markdown, so `vinculum man client mqtt > mqtt.md` and `| glow` both
  work as they stand.

  **`--apropos` (`-k`) searches it**, for when you have a word rather than a path — an
  attribute name from someone else's config, a term out of an error message. It searches
  block types, variants, sub-blocks, attributes, `ctx` shapes and their fields, namespaces
  and their members, and the function library, printing each hit with the command that
  reads it. This is what makes a bare attribute name findable at all: `action` appears in
  dozens of blocks, so it is deliberately not resolvable as a path, and search is the other
  half of that bargain. The same reference is reachable from the two other places you might
  be standing: `:man` and `:apropos` in the REPL, which also search that session's own
  functions, and `help("client", "mqtt")` from inside any expression, which now answers
  questions about the block language and not only about functions. See
  [`doc/man.md`](doc/man.md).
- **`vinculum test` — run a configuration's `.cty` test blocks against the running
  system.** Boots the full server exactly as `vinculum serve` would — buses, servers,
  subscriptions, triggers — runs the functy `test "..." { ... }` blocks embedded in the
  configuration's `.cty` files, then shuts down, exiting non-zero if any test failed.
  Because the tests execute inside a live Vinculum they are integration tests, not just
  unit tests: they can reference `bus.*`/`client.*`/`server.*`, `send()` messages, and
  assert on the resulting state, with functy's `eventually` / `never` for asynchronous
  effects. A new **`sys.testing`** ambient bool is true only under this command, so a
  configuration can switch real external I/O off while under test (`disabled =
  sys.testing`) and gate recording sinks on (`disabled = !sys.testing`). Requires functy
  v0.12.0. See [`doc/testing.md`](doc/testing.md).
- **`vinculum fmt` — canonically format config and functy source.** Formats by extension:
  `.vcl`/`.vinit` as HCL (2-space, matching `terraform fmt`) and `.cty` as functy source,
  reading stdin or walking the files and directories it is given, printing the result,
  rewriting in place with `-w`, or listing what differs with `-l`. A file that does not
  parse is reported and left byte-for-byte unchanged — formatting never drops or reorders
  code. See [`doc/overview.md`](doc/overview.md).
- **Prebuilt binaries and Homebrew install.** Every release now publishes statically-linked
  `vinculum` binaries for Linux and macOS (amd64 and arm64) as downloadable archives with
  checksums, plus a Homebrew cask: `brew install tsarna/tap/vinculum`. These are an
  alternative to the container for local use and development — the container image remains
  the recommended way to deploy. Like the minimal image, they do **not** support Go plugins
  or the cgo-based SQLite driver (PostgreSQL and MySQL, which are pure-Go, still work).

### Changed

- **`vinculum check --format json`.** The text form spends its effort on being read by a
  person — quoting the offending line, wrapping the prose — which is the wrong shape for an
  editor extension drawing squiggles, and scraping it was the only alternative on offer.
  `--format json` writes the same diagnostics to **stdout** as a report: `valid`, a
  `diagnostics` list with severity, summary, detail, and 1-based `location`, and a `summary`
  counting errors and warnings. `valid` is false only for errors, so a warning is reported
  without failing the check. The report is emitted whatever the answer, so a consumer parses
  one shape rather than reading silence as success. Exit codes are unchanged: `0` valid,
  `1` invalid, `2` usage. `vinculum check` also has a section of its own in
  [`doc/overview.md`](doc/overview.md) now, which it never did.
- **A mistyped block type says what you probably meant.** `server "htp"` reported `Invalid
  server type: htp` — repeating back the label the author could already see — though the
  registry it just failed to find it in is the list of answers. Every typed block now
  offers the nearest name: `There is no server type "htp". Did you mean "http"?`, and
  likewise for `client`, `trigger`, `condition`, `metric`, `wire_format`, and `editor`.
  With nothing close enough to be a correction it names the types that are available
  instead, up to twelve of them, and points at `vinculum man client` past that — eighteen
  client types read as a wall rather than a list. A **conditional** type is told apart from
  a wrong one: `trigger "file"` in a process started without `--file-path` reported
  `Invalid trigger type: "file"`, sending the author to look for a typo that was not there,
  and now says the type exists but is not available in this configuration and points at the
  page that says what it needs — which now says it.
- **`vinculum check` catches a bad reference in an expression that has not run yet.** An
  `action`, an `on_connect`, a computed metric's `value` are stored at load and evaluated
  when something happens, so a name that resolves to nothing used to pass `check` cleanly
  and then fail at the first event — and at every event after it, identically, forever,
  with nothing escalating. Those expressions are now resolved against the namespace they
  will actually see, once every block has been processed, and an unresolvable reference is
  an error with a source range like any other: a leading name that is in no namespace at
  all, a `ctx` field this attribute's context does not provide, a name read out of
  something that has names in it (`bus.mian` when the bus is called `main`, `sys.hostnam`),
  or a call to a function that does not exist. Each message names what *is* available, or
  points at the `vinculum man` page that lists it. `try()`/`can()` arguments are exempt, as
  is anything whose names the language does not choose — `env`, `sys.signals`, and the
  functions a feature flag provides. See "Reference Checking" in
  [`doc/config.md`](doc/config.md).
- **`condition` blocks accept `disabled`.** Every other block that creates a runtime
  component already did; a condition rejected it outright. A disabled condition registers
  nothing, so `condition.<name>` is undefined and any expression referring to it fails to
  resolve — the same as a disabled `fsm`.
- **Config that was silently ignored is now rejected.** Four cases, all of which used to
  report "Configuration is valid" while doing something other than what they said:
  - A `condition "flipflop"` edge attribute without its wire — `set_edge` with no `set_on`
    — was parsed and dropped, so a typo, or a wire deleted without its edge, looked
    configured.
  - A `var` block ignored any attribute other than `type`, `nullable`, and `value`, so a
    misspelled attribute parsed cleanly and had no effect.
  - A `bus` block accepted *anything*: it carried a catch-all body that nothing read, so
    `bus "main" { queue_sizee = 500 }` validated cleanly and the bus quietly took its
    default queue size.
  - A `reconnect` block that cannot back off — `backoff_factor` below 1, or an
    `initial_delay` of zero — describes a retry schedule whose wait *shrinks* toward zero
    rather than growing, which at runtime is a client reconnecting continuously against a
    service that is already down. `backoff_factor = 1` remains the way to ask for a
    constant delay.
- **A `reconnect` block means the same schedule wherever it appears.** With `max_delay`
  omitted it capped backoff at 30s on `client "vws"` and 60s on the protocol clients — the
  same block, two schedules, for no reason a reader of it could have predicted. Both paths
  now resolve through one place and settle on **60s**, which is what two of the three
  clients already did. A `client "vws"` that writes a `reconnect` block and omits
  `max_delay` will back off further apart than before; one that sets `max_delay`, or omits
  the block entirely, is unaffected.

### Fixed

- **An action that fails at event time says which line failed.** A functy throw was
  rendered against its `.cty` source — the failing line and the operand that tripped an
  assert — while a VCL expression got one line of diagnostic text and a `file:4,12-19`
  range for the reader to go and look up. The sources are now retained on the built
  `Config`, so every runtime evaluation failure is reported the way `check` reports a
  load-time one:

  ```text
  Error: Error in function call

    on config.vcl line 4, in trigger "start" "boom":
     4:   action = length(42)

  Call to function "length" failed: collection must be a list, a map or a tuple.
  ```

  This covers every site that logs through `ActionError` — triggers, subscriptions, HTTP
  and MCP handlers, client lifecycle hooks, decode-error hooks, signal actions — and where
  hcl can say what the expression's variables held at the point of failure (`with ctx.msg
  as "hello"`), it does. A failure whose range points at something synthesized rather than
  read from a file still gets the one-line form, since there is nothing to quote.

  A subscription failure is now logged **once**, not twice. The bus logged every delivery
  error itself, so the rendered report arrived alongside the one-line version it was meant
  to replace; a subscriber that has already reported a failure now says so when it returns
  it (`bus.ReportedError`, new in vinculum-bus v0.16.0) and the bus skips only its own log
  line. The error is unchanged in every other respect — still returned, still on the
  delivery span, still counted — so dead-lettering and ack decisions behave exactly as
  before.
- **`check`, `serve`, and `test` report every configuration error, each quoting its own
  source line.** All three handed their diagnostics back as an `error` for `main` to render
  with `%v`, and `hcl.Diagnostics.Error()` is *the first diagnostic plus a count* — so a
  file with three errors printed one of them, no quoted line, and "and 2 other
  diagnostic(s)". They now render the full set through the same diagnostic writer `fmt`,
  `test`, and `schema` already used. The source of every file each pass parses — `.vinit`,
  `.vcl`, and `.cty` — is retained for the purpose, so a bootstrap error and a functy parse
  error read the same way as a VCL one; a `.vinit` diagnostic had no source context at all
  before. Warnings are rendered the same way, and still do not fail the run. A config path
  that does not exist is now one of those diagnostics rather than a nil-pointer panic.
- **A mistyped top-level block or a stray top-level attribute is now an error.** The
  configuration's top-level schema was consumed with `PartialContent` and the remainder
  thrown away, so anything that did not match a known block header simply vanished:
  `serverr "http" "web" { listen = ":8080" }` and a bare `whatever = 42` both reported
  "Configuration is valid", and the mistyped block was a config that did nothing, forever,
  with no signal. The remainder is now consumed and reported, with the same "Did you mean"
  suggestion a *nested* typo already got. The blocks extracted before the general block
  pass (`function`, `jq`, `editor`, `procedure`) are unaffected: their own extraction hides
  them from the closed schema.
- **`try()` and `can()` work in a `.vinit` file.** The bootstrap context was assembled from
  the cty standard library alone, and `try`/`can` are functy builtins, so they were missing
  from the one place they are needed most: `env` is the only namespace a `.vinit` has, and
  a variable that is not set is not an attribute of it — so `disabled = env.SKIP_THIS ==
  "true"` aborted startup rather than evaluating to false when `SKIP_THIS` is unset.
  `try(env.SKIP_THIS, "") == "true"` is what that should have been, and now is. The rest of
  functy's host-agnostic builtins (`cond`, `switch`, `typeof`, `error`, `assert`) come with
  them; all are pure, which is what makes them safe to evaluate before anything else
  exists. The examples in [`doc/vinit.md`](doc/vinit.md) and
  [`doc/plugins.md`](doc/plugins.md) were showing the form that fails.
- **A computed `metric`'s `value` is evaluated with a `ctx`, and a failing one stops
  shouting.** It used to be evaluated against the bare global namespace, so no function
  taking a context could be called from it — no `http::get()` to poll an upstream, no
  `sql::query()`, not even `log::warn()` when something went wrong — leaving a computed
  metric able only to project state already in memory, which is not what "polled" suggests.
  Each poll now runs in a `metric.poll <name>` span, so an HTTP call inside the expression
  is traced beneath it rather than emitting an orphan; the block accepts `tracing` to
  select the backend, and `ctx.metric` names the metric being polled.

  The old failure was quiet in the worst way: nothing evaluates `value` at config time, so
  `vinculum check` reported a configuration valid and it then failed at *every* poll,
  forever, with `There is no variable named "ctx"` — logged at Error level with a Go
  stacktrace into the polling plumbing, restating one fact every `computed_interval` and
  burying whatever else the log had to say, including a *different* failure. Those errors
  are the user's expression failing and now go to `UserLogger`; the first is still loud,
  identical repeats drop to Debug with a `repeats` count, a failure that changes is loud
  again, and a poll that succeeds after failures logs a recovery. Only the log line is
  dampened — the span is still marked and the duration histogram still records it.
- **An `editor` expression can read `ctx.auth`, `ctx.baggage`, `ctx.trace_id`, and
  `ctx.span_id`.** Every other evaluation site builds its `ctx` through the one helper that
  supplies those four; the editor blocks assembled their context object themselves and so
  carried none of them. An editor called from an HTTP handler sat inside a live trace it
  had no way to read — `ctx.trace_id` was an "unsupported attribute" error — and could not
  see who was asking. The Go context was threaded through correctly the whole time; only
  the projection into VCL was missing.
- **An `on_decode_error` hook on an mqtt or kafka receiver can read the transport's
  topic.** Both clients offered it under the key `topic`, which collides with the hook's own
  `ctx.topic`; the collision is resolved in favour of the fixed field, so the key was
  dropped and `ctx` never carried it. They are now `ctx.mqtt_topic` and `ctx.kafka_topic`
  (requiring `vinculum-mqtt` v0.10.0 and `vinculum-kafka` v0.12.0), matching how every
  other receiver names its transport identifier — `routing_key`, `stream`, `channel`. The
  reserved set now has one definition, `wire.IsReservedAttr` in `vinculum-wire` v0.5.0,
  shared with the receivers that choose these keys, and a key that still collides is logged
  rather than dropped in silence. Both [`doc/client-mqtt.md`](doc/client-mqtt.md) and
  [`doc/client-kafka.md`](doc/client-kafka.md) had listed `ctx.topic` twice, the second row
  describing a field that never existed at runtime.
- **`client "redis_pubsub"` no longer hangs at startup on a short channel name.**
  Subscribing to a channel of six characters or fewer (or a pattern of four or fewer)
  blocked forever inside `Start()`, so the whole runtime failed to come up on a
  configuration that parsed and validated cleanly, with no error and no timeout. The cause
  was `github.com/redis/go-redis/v9` v9.21.0, which changed `PeekPushNotificationName` from
  a peek clamped to what was already buffered into an unconditional `bufio` `Peek(36)`. A
  subscribe confirmation is 29 bytes plus the channel name, so a short one has no 36th byte
  to read; nothing more arrives until someone publishes, and the read carries no deadline.
  Fixed upstream in v9.22.0
  ([redis/go-redis#3935](https://github.com/redis/go-redis/issues/3935)), which Vinculum
  now requires, with a regression test that subscribes to a single short channel.
- **`client "mqtt"` and `client "rabbitmq"` no longer stop backing off after a long
  outage.** Given a `reconnect` block, the wait before attempt *n* is `initial_delay ×
  backoff_factor^n`, clamped to `max_delay` — but the clamp was applied *after* converting
  that product to a duration, and on the default schedule the product passes the largest
  representable duration at attempt 34, around half an hour into an outage. Go leaves an
  out-of-range float-to-integer conversion implementation-defined, and on **amd64** — what
  the published Linux images run — it yields the most *negative* duration there is. A
  negative wait is no wait: the client stopped backing off altogether and reconnected as
  fast as the loop would turn, against a broker that was already down. Present since
  v0.19.0, and invisible on arm64, where the same conversion saturates the other way and
  the clamp happens to hold. `client "vws"` was never affected — it backs off through the
  bus reconnector, which multiplies and clamps once per attempt.
- **BREAKING: `max_retries` in a `reconnect` block is honoured on the mqtt and rabbitmq
  clients.** A client that sets it now *stops reconnecting* once the limit is reached,
  where on these two it previously retried forever — so a configuration asking to give up
  after three attempts gets exactly that, and a client which used to come back eventually
  may now stay down. The attribute has been decoded and validated since it was introduced,
  and read by exactly one of the three client types that accept the block; on the other two
  it parsed cleanly and did nothing, with no diagnostic to say otherwise. Giving up is a
  property of a retry loop and
  neither loop lives in this repository, so the fix was upstream first — vinculum-mqtt
  v0.11.0 and vinculum-rabbitmq v0.4.0 each gained the capability. All three clients now
  behave identically: zero or negative retries forever, the limit bounds *reconnection*
  only and never the initial connection, and giving up is quiet and final.

  **Check any config that sets `max_retries = 0` expecting it to disable retrying.**
  `doc/server-vws.md` documented that spelling as "no retries", which was never true on any
  path — zero has always meant unlimited — so a configuration written against that sentence
  got the opposite of what it asked for.
- **`server "websocket"` honours `ping_interval` and `write_timeout`.** Both were parsed,
  described in the schema, and then discarded — the code that applied them sat commented
  out behind a TODO — so writing either attribute did nothing at all. They now default to
  30s and 10s respectively, and either can be disabled with `0`. `write_timeout` also
  bounds ordinary writes, so a client that stops reading can no longer pin the connection's
  writer while its queue fills with messages that will never be delivered. A ping that goes
  unanswered closes the connection immediately rather than opening a close handshake, since
  a peer that did not answer a ping is precisely the peer that will never send a close
  frame back.
- **An MCP tool `param`'s `default` and `enum` do something.** Both are decoded from HCL,
  and both were then dropped on the floor: the default was never assigned or read anywhere,
  and the branch that would have published an enum could not fire because the field was
  always empty. Writing either attribute was silently inert. Both are now evaluated at
  config time and checked against the param's declared type — so `type = "number"` with
  `default = "ten"` is a configuration error rather than a published input schema that
  contradicts itself — and both reach the model through the tool's JSON Schema. A default
  is also substituted server-side when the argument is absent, since a client is free to
  ignore the default it was shown. A `prompt`'s params differ, because the protocol does:
  an MCP prompt argument carries only a name, a description, and whether it is required, so
  `type` and `enum` constrain nothing at runtime there and every argument arrives as a
  string. See [`doc/server-mcp.md`](doc/server-mcp.md).
- **Nested block labels are named in HCL diagnostics.** A missing label on a nested block
  reported `Missing  for match; All match blocks must have 1 labels ().` — the label name
  was blank because nearly every nested block's decode struct left it unset. Affected
  `sender`/`receiver` sub-blocks across mqtt, kafka, rabbitmq, and redis, plus `query`,
  `auth`, `match`, `fetch`, and the MCP `resource`/`tool`/`prompt` blocks.
- **`client "vws"` accepts a `reconnect` block.** It was tagged as an attribute, so the form
  shown in [`doc/server-vws.md`](doc/server-vws.md) — and used by the mqtt and rabbitmq
  clients — did not parse at all.
- **An explicitly named `.cty` or `.vinit` file is no longer parsed as VCL.** The `.vcl`
  pass parsed any file given by path regardless of extension, so naming a functy or
  bootstrap file directly (e.g. `vinculum test config.vcl tests.cty`) failed with HCL syntax
  errors. Those extensions have their own passes and are now skipped by the VCL pass; a
  directory argument was already filtered by extension and is unaffected.
- **Documentation corrections found by building the schema.** `trigger "start"` runs after
  every startable component is ready, not "during the configuration build phase before any
  server or client starts", and an error in it is logged rather than aborting startup.
  `trigger "signals"` has no `ctx.trigger`. A subscription action's `ctx.fields` is always
  present, empty rather than absent when a message carries no metadata. An MCP resource or
  tool action does not JSON-encode a non-string result, it fails with "unsupported type"
  (`jsonencode()` is the caller's job), and `server_version` defaults to `0.0.0`.

## [0.44.0] - 2026-07-22

### Changed

- **BREAKING: wire-format decode failures are now errors on the receive side.** Every
  messaging receiver (rabbitmq, mqtt, kafka, sqs, redis pub/sub, redis stream) used to
  swallow a deserialize failure — log a warning, substitute the **raw bytes** for the
  payload, and deliver the message anyway — even when the config explicitly said
  `wire_format = "json"`. A decode failure is now fatal to the message: it is not
  delivered, and each client applies its normal failure path.

  This applies to custom formats too: any format registered by a `wire_format` block or
  a plugin is now strict, since the client no longer catches its `Deserialize` errors.

  The closest replacement is the new `wire_format = "auto_bytes"`, which decodes JSON
  and hands back the undecoded payload as a `bytes` value otherwise; use `"auto"` if your
  handler wants text instead. See
  [doc/deprecations.md](doc/deprecations.md#tolerant-wire-format-decoding).

  Per-client effect of a failed message:

  | Client | Effect | Poison-message risk |
  | --- | --- | --- |
  | rabbitmq | nacked without requeue | none — dropped, or to a DLX if bound |
  | mqtt | dropped (no ack semantics) | none |
  | redis pub/sub | dropped (no ack) | none |
  | kafka | offset not committed; routed to `dlq_topic` when set | **stalls the partition without `dlq_topic`** |
  | redis stream | entry left in the PEL; dead-lettered after `dead_letter_after` | **stays pending without `dead_letter_stream`** |
  | sqs | not deleted; visible again after the visibility timeout | **redelivers forever without an AWS redrive policy** |

  Vinculum now warns at config load when a kafka or redis-stream receiver combines a
  strict `wire_format` with no dead-letter destination.

- **A payload decoded as bytes is now a `bytes` value, not a string.** Wire formats that
  produce `[]byte` — `bytes`, and the new `auto_bytes` — previously reached VCL as a
  string, because the cty conversion layer had no `[]byte` case and fell through to text.
  They now become a [`bytes`](doc/functions.md) rich object, so `tostring()`, `length()`,
  and the bytes functions dispatch correctly on them.

  No data was being lost before (a cty string holds arbitrary bytes), but the type was
  wrong. Config that did string operations directly on a `bytes`-format payload must now
  call `tostring()` on it first.

  Sending is symmetric: a `bytes` value passed to `send()` is serialized as its raw bytes
  rather than being flattened into the object's attributes, so a payload received as
  bytes can be forwarded unchanged.

- **A decoded JSON object always has a stable type now.** A JSON
  object decoded into a message (`msg`) is a cty object regardless of whether its fields
  happen to share a type. Previously an all-same-type object (e.g. `{"count":1,"total":2}`)
  became a cty *map*, while a mixed object became an object — so the type of `msg` depended
  on the data. That was observable: `lookup(msg, "missing", "default")` succeeded or failed
  depending on the message's shape. Attribute and index access (`msg.field`, `msg["field"]`)
  are unchanged; `keys(msg)` now returns a tuple rather than a list. This matches what
  `jsondecode()` already produced.

### Added

- **`wire_format "protobuf"` — schema-driven Protocol Buffers encode/decode.** Declares a
  wire format bound to a compiled `FileDescriptorSet` (`descriptor_set`), so inbound
  payloads decode from protobuf binary into VCL values and outbound values encode back.
  A block with `message` set is a single format; without it, the block value is an object
  of one format per message, keyed by full name with a short-name alias when unambiguous.
  Two representation modes: `native` (default — proto field names, rich Time/Duration/bytes
  values, 64-bit ints as numbers) and `json` (protojson fidelity for relaying to a JSON API).
  Well-known types (Timestamp, Duration, Struct, Any, wrappers, FieldMask, Empty) are
  bundled — a descriptor set need not include them — and `Any` auto-unpacks when its type
  is known. Serialize is strict (unknown fields and type mismatches error); deserialize
  failures flow through `on_decode_error` with `DecodeError.Format = "protobuf:<message>"`.
  Pure Go and CGO-clean, so it ships in the minimal image. See
  [doc/wire-format-protobuf.md](doc/wire-format-protobuf.md).

- **`wire_format = "auto_bytes"`** — decodes JSON exactly like `auto`, but yields a
  `bytes` value rather than a string when the payload isn't JSON. Intended for streams
  carrying a mix of JSON and opaque binary, and as the closest replacement for the
  removed tolerant-decode fallback. Like `auto`, it never fails to decode.

- **`on_decode_error` on every receiver block.** An optional expression evaluated when an
  inbound payload fails to deserialize, so failures can be logged, published to a
  dead-letter topic, or counted. It is an *observer*: it cannot suppress the failure or
  cause delivery, and errors inside the hook are logged and otherwise ignored. The eval
  context exposes `ctx.raw` (a `bytes` object), `ctx.error`, `ctx.wire_format`,
  `ctx.topic`, `ctx.fields`, plus per-client identity fields (`ctx.routing_key`,
  `ctx.partition`, `ctx.entry_id`, …). Like `action`, it is excluded from config-load
  dependency extraction, so a hook may reference its own client.

- Deserialize failures are now recorded on each client's error counter with
  `error.type = "deserialize"`. The mqtt and kafka error counters gained an `error.type`
  attribute, and the sqs receiver gained a `vinculum.messaging.errors` counter (it had
  none) that also covers subscriber and delete failures.

- `hclutil.EvalContextBuilder.WithStringMapAttribute` — sets an attribute to an object of
  strings, yielding an empty object rather than a null for an empty map.

### Documentation

- Corrected the license stated in `README.md` from MIT to Apache 2.0, matching the
  `LICENSE` file.
- Added a **Getting Started** section to `README.md` covering container deployment,
  a zero-setup `weather-mcp` example, and pulling configuration from a git repository
  via a `.vinit` block.
- Rounded out **Related Projects** in `README.md`: added [functy](https://github.com/tsarna/functy)
  and [vinculum-wire](https://github.com/tsarna/vinculum-wire), plus the `rich-cty-types`,
  `bytes-cty-type`, `url-cty-funcs`, `geo-cty-funcs`, and `barcode-cty-func` helper
  libraries.

## [0.43.0] - 2026-07-18

### Changed

- **BREAKING: the time and duration functions are namespaced.** `timeadd` → `time::add`,
  `durationtruncate` → `duration::truncate`, `nextzoneserial` → `dns::next_zone_serial`, and so
  on. HCL parses `a::b(x)` natively and resolves it as a single flat map key, so this is a
  naming change, not a structural one — but **existing `.vcl` and `.cty` files must be updated**.

  The names follow HashiCorp's conventions for provider-defined functions: the leaf name does
  not repeat the namespace, and namespaced functions use underscores (HCL's *built-in*
  functions run words together for historical reasons). The nine functions that all began with
  the word "duration" are the clearest win, and the two DNS functions get a namespace of their
  own — they were never time functions.

  `duration()` keeps its bare name: it is the type constructor, and reads as one.

  | was | is |
  | --- | --- |
  | `now`, `parsetime`, `formattime` | `time::now`, `time::parse`, `time::format` |
  | `timeadd`, `timesub` | `time::add`, `time::sub` |
  | `since`, `until` | `time::since`, `time::until` |
  | `fromunix`, `unix` | `time::from_unix`, `time::to_unix` |
  | `timezone`, `intimezone` | `time::zone`, `time::in_zone` |
  | `addyears`, `addmonths`, `adddays` | `time::add_years`, `time::add_months`, `time::add_days` |
  | `timebefore`, `timeafter` | `time::before`, `time::after` |
  | `strftime`, `strptime` | `time::strftime`, `time::strptime` |
  | `formatduration`, `absduration` | `duration::format`, `duration::abs` |
  | `durationadd`, `durationsub`, … | `duration::add`, `duration::sub`, … |
  | `durationtruncate`, `durationround` | `duration::truncate`, `duration::round` |
  | `durationlt`, `durationgt` | `duration::lt`, `duration::gt` |
  | `nextzoneserial`, `parsezoneserial` | `dns::next_zone_serial`, `dns::parse_zone_serial` |

- **BREAKING: the geo functions are namespaced under `geo::` and `sky::`.** The geographic,
  geodesic, and geometric functions — whose bare names (`point`, `area`, `contains`) would be
  too generic to expose globally — move under `geo::`; the solar-event and celestial-position
  functions move under `sky::`. The `geopoint` type is not namespaced (it is a type, not a function).

  | was | is |
  | --- | --- |
  | `geo_point`, `geo_format` | `geo::point`, `geo::format` |
  | `geo_inverse`, `geo_destination`, `geo_waypoints` | `geo::inverse`, `geo::destination`, `geo::waypoints` |
  | `geo_area`, `geo_contains`, `geo_nearest`, `geo_line_intersect` | `geo::area`, `geo::contains`, `geo::nearest`, `geo::line_intersect` |
  | `sunrise`, `sunset`, `solar_noon`, `solar_midnight` | `sky::sunrise`, `sky::sunset`, `sky::solar_noon`, `sky::solar_midnight` |
  | `sun_position`, `moon_position`, `moon_phase` | `sky::sun_position`, `sky::moon_position`, `sky::moon_phase` |

- **BREAKING: the random functions are namespaced under `rand::`.** `random` → `rand::float`
  (it returns a float in `[0.0, 1.0)`; a leaf name should not repeat its namespace),
  `randint` → `rand::int`, `randuniform` → `rand::uniform`, `randgauss` → `rand::gauss`,
  `randchoice` → `rand::choice`, `randsample` → `rand::sample`, `randshuffle` → `rand::shuffle`.

- **BREAKING: the URL functions are namespaced under `url::`.** `urlparse` → `url::parse`,
  `urljoin` → `url::join`, `urljoinpath` → `url::join_path`, `urlqueryencode` →
  `url::query_encode`, `urlquerydecode` → `url::query_decode`, `urldecode` → `url::decode`.
  The `url` object type keeps its name (it is a type, not a function). `urlencode` (the flat
  HCL builtin) is unchanged, and is now **also** available as `url::encode`, so the
  encode/decode pair is symmetric under the namespace.

- **BREAKING: vinculum's own functions are namespaced.** The multi-member families move
  under a namespace, dropping the prefix the flat names carried; the `http_response` and
  `http_request` *object types* keep their names (they are types, not functions).

  | family | was → is |
  | --- | --- |
  | **`log::`** | `log_debug/info/warn/error/msg` → `log::debug/info/warn/error/msg` |
  | **`http::`** | `http_get/post/put/delete/head/options/patch/request/must` → `http::get/…/must`; `http_response/redirect/error` → `http::response/redirect/error`; `addheader/removeheader/setcookie` → `http::add_header/remove_header/set_cookie`; `basicauth` → `http::basic_auth` |
  | **`mcp::`** | `mcp_image/error/usermessage/assistantmessage` → `mcp::image/error/user_message/assistant_message` |
  | **`wire::`** | `serialize/serializestr/deserialize` → `wire::serialize/serialize_str/deserialize` |
  | **`send`** | `sendjson/sendgo` → `send::json/send::go` (bare `send` keeps its name) |
  | **`llm::` / `sql::`** | `llm_wrap` → `llm::wrap`; `sql_must` → `sql::must` |
  | **`sqs::` / `redis::`** | `sqs_delete` → `sqs::delete`, `sqs_extend_visibility` → `sqs::extend_visibility`; `redis_ack` → `redis::ack` |

  `diff`, `patch`, `kill`, `help`, and `doc` stay flat — they have no multi-member family.
  The `file`/`template` families (`file`, `filebytes`, `filebase64`, `fileexists`, `fileset`,
  `filewrite`, `fileappend`, `templatefile`, `gotemplatefile`) also stay flat: `file`,
  `fileexists`, `fileset`, `filebase64`, and `templatefile` are Terraform/OpenTofu standard
  function names, kept for compatibility, and the rest keep their families uniform. The cty
  standard-library builtins (`upper`, `jsonencode`, `md5`, `abspath`, …) are likewise unchanged.

- **BREAKING: `timeadd` is now the flat HCL builtin — strings in, string out.** It used to be
  an upgraded version that also accepted `time` and `duration` capsules. That name now belongs
  to cty's `stdlib.TimeAddFunc` again, and the capsule-aware function is `time::add`, **which
  always returns a `time`**.

  A call passing capsules must be updated, and will fail loudly rather than silently:

  ```hcl
  timeadd(now(), duration("50ms"))          # before
  time::add(time::now(), duration("50ms"))  # after
  ```

  As a side effect, a duration string is now parsed the same way in every form of `time::add`:
  `time::add("2024-01-01T00:00:00Z", "PT5M")` used to fail (that path accepted only Go duration
  syntax) while the equivalent with a parsed timestamp succeeded. Both work now.

- **`typeof()` returns functy's type-annotation grammar** (e.g. `list(string)`, `object({ a = string })`) rather than the cty friendly name (`list of string`), so its output round-trips as a type spec. The `error()` builtin now raises a structured, catchable error. Both are now provided by functy's standard library, replacing Vinculum's overlapping copies.

### Added

- **`help()` and `doc()` — reflection over Vinculum's function set.** `help("f")` returns a function's signature, description, and per-parameter docs; with no argument it lists every callable name. `doc("f")` returns just the description. Both are available wherever Vinculum evaluates an expression, and in particular in the REPL (`serve -i`), where `help()` is now the way to find your way around the function set.

  `help()` shows the *real* signature of a host function, not the one cty can express. A cty function may only make its **trailing** parameters optional, so the generic capability family — `get`, `set`, `count`, `increment`, `observe`, … — which takes an optional **leading** context, has to fake it with a variadic and reflects as the useless `get(thing, ...args)`. Those functions now ship declarations of what they actually accept, and `help()` prefers them:

  ```console
  > help("get")
  get(ctx?: ctx, thing, fallback?, *args) -> any

  Read a thing's current value.

  Parameters:
    ctx?       request context
    thing      the thing to read (must be Gettable)
    fallback?  Conventionally the value to return when the thing has none. …
    *args      further arguments, interpreted by the thing
  ```

  The `bytes` functions are declared the same way, for the other reason cty cannot describe a function: they are **overload sets**. Each of `bytes`, `base64encode`, and `base64decode` takes a string *or* a `bytes` value, and cty has no union type — its metadata can only say "dynamic". `base64decode` goes further and picks its *return* type from how many arguments it was given, which one cty signature cannot express at all. `help()` now shows every form:

  ```console
  > help("base64decode")
  base64decode(s: string) -> string
  base64decode(s: string, content_type: string) -> bytes
  ```

- **`RegisterFunctyExterns(filename, src)`** — a package that contributes a function plugin can register a `//functy:extern` source declaring the true signatures of the functions it provides, alongside the plugin itself. Unlike the function-plugin registry, extern names are checked for collisions: two packages declaring the same name is an error, as is a user `.cty` function that collides with one.

- **functy (`.cty` files)**: a configuration directory may now contain [functy](https://github.com/tsarna/functy) source files (`.cty`) alongside its `.vcl` files. functy is a small expression/statement language with real syntax for functions, typed locals, reassignment, branching, loops, `try`/`catch`, and structured errors — a more expressive alternative to `function`, `jq`, and the `procedure` block. Its top-level `func` declarations join the same user-function namespace. In an unnamespaced file, top-level `var`/`const` bindings fold into Vinculum's own var/const pools (a functy `const` may reference a VCL `const` and vice versa in any order; a functy `var` becomes a mutable `var.<name>`). A file with a `namespace` declaration instead registers its functions under the qualified name and scopes its `const`s to that namespace — they are not folded into the shared surface, so two namespaces may each declare the same const name; a top-level `var` inside a namespace is an error, since a Vinculum `var` is always global. Type annotations can name Vinculum's capsule and rich-object types (`bus`, `server`, `client`, `subscriber`, `variable`, `time`, `duration`, `url`, `bytes`, `baggage`, `metric`, `wire_format`, `ctx`, `http_request`/`http_response`/`http_client_response`, `http_client`, `sql_client`/`sql_query`, `mcp_result`), and the same type grammar backs the `var` block's `type` attribute. An uncaught throw or failed `assert` inside a `.cty` function called from VCL is rendered against the offending `.cty` line (with `assert` operand detail) through the user log. See [doc/functy.md](doc/functy.md).
- **functy standard library**: adopting functy's stdlib adds the `typekind`, `assert`, and `can` builtins (alongside the existing `typeof`, `cond`, `switch`, `error`, `try`), available anywhere Vinculum evaluates an expression. See [doc/functions.md](doc/functions.md#control-flow).
- **The `var` block `type` attribute accepts a type spec**, not just a quoted string: `type = number`, `type = list(string)`, `type = object({ host = string, port = number })`, or a host-registered named type such as `type = bus`. Enforcement now coerces where the grammar allows. See [doc/config.md](doc/config.md#var).
- **Configuration warnings are now surfaced by `vinculum check` and `vinculum serve`.** Non-fatal warning diagnostics (deprecations, duration-precision loss, etc.) were previously computed but silently dropped — the commands acted only on errors. They are now printed to stderr.

### Deprecated

- **The `procedure` block** is superseded by functy (`.cty` files) and will be removed in a future release. Loading a configuration that contains a `procedure` block emits a deprecation warning. See [doc/functy.md](doc/functy.md) and [doc/procedure.md](doc/procedure.md).
- **The quoted-string form of the `var` block's `type` attribute** (`type = "number"`) is deprecated in favor of the unquoted type spec (`type = number`); it still works but emits a warning.

### Fixed

- **Functions with an optional trailing argument no longer accept, and silently ignore, extra ones.** `bytes()`, `base64decode()`, `filebytes()`, and the `time::*` family fake an *optional* argument with a variadic, because that is the only way cty offers — and a variadic has no upper bound. Reading only the argument they expected, they dropped the rest without a word: `bytes("hi", "text/plain", "junk")` returned a bytes value, and `time::now("UTC", "junk")` returned a time. They now report `takes at most N arguments`. A call that was silently wrong is now an error; no call that was doing something meaningful is affected.

- **`vinculum check .` / `serve .` from inside a configuration directory loaded nothing** and reported the (empty) config as valid. The directory walk visited the walk root first with name `.`, which the skip-hidden-directories check treated as a dot-directory and skipped, discarding the whole tree. The walk root is now excluded from that check; hidden subdirectories are still skipped.

## [0.42.0] - 2026-06-27

### Added

- **OpenTelemetry Baggage in VCL (`ctx.baggage`)**: [W3C Baggage](https://opentelemetry.io/docs/specs/otel/baggage/) — key/value pairs propagated alongside a trace — is now readable and writable from action expressions. `ctx.baggage` is a capsule that plugs into the existing generic functions: `get(ctx.baggage)` returns all entries as a `map(string)` (materialized only on demand), `get(ctx.baggage, key[, default])` reads one, `set(ctx.baggage, key, value)` / `set(ctx.baggage, {…})` add or overwrite (a `null` value removes a key), `delete(ctx.baggage[, key])` removes one or all, `clear(ctx.baggage)` drops everything, and `length()` / `tostring()` give the entry count and the W3C header encoding. Writes derive a new context and store it back through the same pointer the handler's `ctx` already carries, so a `set()` early in an `action = [...]` list is seen by every later `send()`, `http_*()`, or Kafka/MQTT publish — they inject the updated baggage into outbound headers with no extra plumbing. Available in every context that exposes `ctx` (HTTP/MCP handlers, subscriptions, triggers, client receive handlers, `on_connect`/`on_disconnect`, …). Baggage entry *properties* (the `;key=value` metadata tail) are preserved on pass-through but not exposed to VCL. Keys/values are validated per the W3C spec, surfacing an evaluation error on a rejected entry.
- **Secure-by-default inbound baggage filtering (`baggage {}` block on `server "http"`, `server "mcp"`, `client "kafka"`/`client "rabbitmq"`/`client "mqtt"`/`client "redis_stream"` receivers, and `client "sqs_receiver"`)**: incoming baggage is untrusted input, so it is now **stripped by default** before it reaches `ctx.baggage` or is re-propagated downstream — consistently across every inbound surface, with no configuration required to be safe. Trace propagation (`traceparent`) is unaffected, and baggage your own VCL sets via `set(ctx.baggage, …)` always propagates outbound. A surface opts into trusting upstream baggage with a `baggage {}` sub-block: `passthrough = true` trusts everything, `allow = [...]` keeps only the listed keys, or `deny = [...]` drops the listed key prefixes (mutually exclusive; `allow`/`deny` also enforce optional `max_entries` (default 64) and `max_bytes` (default 8192) caps). The filter runs immediately after the baggage is extracted but before action evaluation. The HTTP/MCP block is per server (a mounted MCP server inherits the HTTP server's filter); the Kafka, RabbitMQ, MQTT, and Redis-stream blocks are per `receiver`/`consumer`; the SQS block is on the `client "sqs_receiver"`. (MQTT, SQS, and Redis-stream support also required fixes in vinculum-mqtt v0.8.1, vinculum-sqs v0.3.1, and vinculum-redis v0.3.1, where inbound baggage was extracted but not carried onto the consumer context, so it never reached actions.) Redis pub/sub has no header mechanism and cannot carry baggage at all. See [doc/baggage.md](doc/baggage.md#server-side-trust-filtering).
- **Project baggage onto spans (`record_baggage` on `client "otlp"`)**: `record_baggage = ["tenant_id", "user_id", …]` copies the named baggage entries onto every locally-started span as attributes of the same name, via the [`baggagecopy`](https://pkg.go.dev/go.opentelemetry.io/contrib/processors/baggagecopy) span processor — making request-scoped values searchable in the trace UI. Keys absent from the current baggage produce no attribute; invalid keys are a config-time error. Defaults to disabled.

- **OpenTelemetry GenAI metrics and spans for `client "openai"`**: the LLM client had HTTP-level tracing (its outbound transport was wrapped with `otelhttp`) but no GenAI-aware telemetry and no metrics. Each `call()` now creates a `gen_ai.inference` client span named `chat {model}` — the existing HTTP request span nests beneath it — carrying the [OpenTelemetry GenAI semantic-convention](https://github.com/open-telemetry/semantic-conventions-genai/tree/main/model/gen-ai) attributes (`gen_ai.operation.name=chat`, `gen_ai.provider.name` — default `openai`, configurable via a new `provider` attribute so OpenAI-compatible endpoints can report their real upstream (e.g. `groq`, `mistral_ai`, `x_ai`) — `gen_ai.request.model`/`max_tokens`/`temperature`, `gen_ai.response.model`/`id`/`finish_reasons`, `gen_ai.usage.input_tokens`/`output_tokens`, `server.address`/`server.port`, and `error.type` on failure, with span status `ERROR` on a failed call). Two metrics are emitted: `gen_ai.client.operation.duration` (histogram, seconds) and `gen_ai.client.token.usage` (histogram, `{token}`, recorded per `gen_ai.token.type` of `input`/`output`), both attributed by operation/provider/model/server and `error.type`. A new `metrics` block attribute selects the backend (a `server "metrics"` or `client "otlp"` block), resolving like every other Vinculum client/server — auto-wired when there is a single backend, no-op when none is configured; the existing `tracing` attribute is unchanged. See [doc/client-llm.md](doc/client-llm.md#observability).
- **OpenTelemetry traces and metrics for `server "mcp"`**: the MCP server,
now emits telemetry conforming to the [OpenTelemetry GenAI/MCP semantic conventions](https://github.com/open-telemetry/semantic-conventions-genai/tree/main/model/mcp). A single SDK receiving middleware instruments every inbound request/notification, so coverage is identical whether the server runs standalone or mounted under `server "http"` (the HTTP transport itself was already wrapped with `otelhttp` in both modes). Each operation produces an `mcp.server` span (kind `server`, named `{method} {tool-or-prompt}` — e.g. `tools/call get_weather`, `prompts/get summary`, `resources/read`, with the resource URI kept out of the span name for cardinality) carrying `mcp.method.name`, `gen_ai.tool.name`/`gen_ai.operation.name`, `gen_ai.prompt.name`, `mcp.resource.uri`, `mcp.session.id`, `network.transport`/`network.protocol.name`, and `error.type` (`tool_error` when a tool returns an error result, else `_OTHER`), with span status set to `ERROR` on failure. An `mcp.server.operation.duration` histogram (seconds) records the same low-cardinality attribute subset (session id and resource URI omitted). Tracing and metrics backends are selected with the existing `tracing` / `metrics` block attributes using the same resolution rules as `server "http"` (auto-wired when there is a single backend; no-op when none is configured). Session-duration metrics are not yet emitted — the MCP SDK exposes no session-disconnect hook. See [doc/server-mcp.md](doc/server-mcp.md#observability).
- **Real client IP behind a proxy (`real_ip` block on `server "http"`)**: when the server runs behind a reverse proxy or load balancer (Traefik, nginx, an ELB) that terminates the client connection, the TCP peer is the proxy, so `ctx.request.remote_addr` and the request log would show the proxy's address. A `real_ip` sub-block recovers the original client from a forwarded header, porting nginx's `real_ip` module: `trusted_proxies` (CIDRs or bare IPs — `set_real_ip_from`), `header` (default `X-Forwarded-For`, but any header including single-value `X-Real-IP` — `real_ip_header`), and `recursive` (walk the chain right-to-left skipping trusted addresses — `real_ip_recursive`). The forwarded header is honored only when the immediate peer is itself trusted, so a direct client cannot spoof its address. The substitution rewrites `RemoteAddr` outermost — before tracing, logging, auth, and action evaluation — so everything downstream sees the real client. See [doc/server-http.md](doc/server-http.md#real-client-ip-real_ip).
- **`remote_addr` is now included in the `server "http"` request log**, alongside `method`, `route`, `path`, `status`, `duration_ms`, and `bytes` (and reflects the [`real_ip`](doc/server-http.md#real-client-ip-real_ip) substitution when configured).
- **`disabled` attribute on `auth` and `real_ip` blocks**: both sub-blocks now accept an optional `disabled` expression that, when `true`, leaves the block parsed but inert — auth falls back to no authentication (mode-specific required fields are not validated) and `real_ip` performs no rewrite (`trusted_proxies` is not required). Since the expression sees `env.*`, a single environment variable can both supply a value and toggle its feature, which HCL cannot otherwise express (a block is either written or not). The `auth` toggle applies to `server "http"`, `server "mcp"`, and `server "metrics"`. The `traffic-light` example uses this to make its `real_ip` and basic-auth optional and env-configurable, and its environment variables are now `TRAFFIC_`-prefixed (`TRAFFIC_HTML_DIR` — renamed from `HTML_DIR` — plus the new `TRAFFIC_TRUSTED_PROXIES`, `TRAFFIC_WEB_USER`, and `TRAFFIC_WEB_PASSWORD`).
- **Weather MCP example (`examples/weather-mcp.vcl`)**: a worked `server "mcp"` that wraps the free, no-API-key [Open-Meteo](https://open-meteo.com) service and exposes live weather to an MCP client (Claude Desktop, Claude Code, …) as two tools (`current_weather`, `forecast`), a templated `resource "weather://current/{place}"`, and a `trip_packing` prompt — running with no env vars or credentials. The MCP server is mounted under a `server "http"` block (rather than given its own `listen`), so each MCP call appears in the HTTP request log. It shows `client "http"` wrapping a third-party JSON API with `function`/`jq` blocks factoring the geocode→forecast flow out of the handler actions (each request resolves the place once and calls the network once), `cond()` for lazy branching (HCL's own `a ? b : c` eagerly evaluates *both* branches, so a side-effecting not-found path needs it), returning an empty list as a "not found" sentinel since user functions reject `null` arguments, and `mcp::error()` for tool failures vs. a plain string for a resource. It also includes an optional, env-toggled `client "otlp"` (`disabled = try(env.OTEL_EXPORTER_OTLP_ENDPOINT, "") == ""`) that the HTTP and MCP servers auto-wire to for traces and metrics — off by default, enabled by setting `OTEL_EXPORTER_OTLP_ENDPOINT`. (`client "otlp"` has always honored the generic `disabled` client attribute; it is now documented in [doc/client-otlp.md](doc/client-otlp.md).) See [examples/README.md](examples/README.md).
- **Virtual hosts on `server "http"`**: a `handle` or `files` block can now be scoped to a specific `Host` by prefixing its route/urlpath label with a host, using Go 1.22's `[METHOD ][HOST]/[PATH]` `http.ServeMux` pattern grammar — so one listener can serve `api.example.com`, `cdn.example.com`, and a host-less catch-all from a single `server` block. Host matching is exact (no wildcards — a `ServeMux` limitation; branch on `ctx.request.host` for suffix/wildcard needs), more-specific patterns win, and a host-less route remains the default for any unmatched host, so existing configs are unchanged. `files` learned the host prefix (the host scopes the mux registration while `StripPrefix` still uses the path portion only) and rejects a method token (a file server serves GET/HEAD only). Serving multiple hosts over HTTPS still requires a multi-SAN certificate or SNI selection — a separate TLS concern. See [doc/server-http.md](doc/server-http.md#virtual-hosts).

### Changed

- **Breaking: `server "mcp"` resource template variables are now exposed under `ctx.args.<name>` instead of directly on `ctx.<name>`.** A templated `resource "db://records/{table}/{id}"` action now reads `ctx.args.table` / `ctx.args.id` rather than `ctx.table` / `ctx.id`. This makes resources consistent with tools and prompts, whose arguments have always been under `ctx.args`; `ctx.args` is now always present (empty for a static URI). `ctx.uri` and `ctx.server_name` are unchanged. See [doc/server-mcp.md](doc/server-mcp.md#resources).

### Fixed

- **Malformed or conflicting `server "http"` route patterns now produce a config diagnostic instead of crashing.** `http.ServeMux.Handle` panics on an invalid pattern or a duplicate registration; both the `handle` and `files` registration paths now recover that panic and report it as an `hcl` error anchored to the offending block.

## [0.41.0] - 2026-06-19

### Added

- **Flipflop condition (`condition "flipflop"`)**: a fourth condition subtype exposing the standard digital-logic bistables — T, SR, gated SR, D, D-latch, JK — through one uniform "wire" attribute surface, where the *combination* of wires declared names the variant. Event wires `set_on` / `reset_on` / `toggle_on` fire on a configurable edge (`set_edge`/`reset_edge`/`toggle_edge` ∈ `rising` (default)/`falling`/`both`) to drive the output true, false, or flip it; `set_from` + `gate_on` implement sample-and-hold, with `gate_edge` accepting the edge modes plus level-sensitive `high`/`low` for a transparent D latch, and `gate_on` alone gating the window of the event wires (gated SR/T). Simultaneous wire fires in one notification resolve atomically by a fixed priority (gate → `dominant` set/reset → set/reset over toggle → D-sample over toggle). The first evaluation of every wire only establishes a baseline and fires no edge, so a source asserting at boot does not spuriously drive the flipflop (`start_active` is the explicit boot-output knob). Reuses the shared four-state machine, so `latch`, `invert`, `cooldown`, `inhibit`, the lifecycle hooks, `get()`/`state()`/`set()`/`toggle()`/`clear()`, and `trigger "watch"` all carry over; the temporal attributes (`activate_after`/`deactivate_after`/`timeout`/`retentive`/`debounce`/`input =`) deliberately do not apply. See [doc/condition.md](doc/condition.md#condition-flipflop-name).
- **Interactive REPL (`serve -i` / `--interactive`)**: after normal startup, instead of blocking on a termination signal, `vinculum serve` can present an interactive prompt that evaluates VCL **expressions** against the live, running configuration. Everything a handler action sees is available — `bus.*`, `server.*`, `client.*`, `env.*`, the full function library, and a working `ctx` — so you can read live state (`get(condition.x)`), drive the system by hand (`send(...)`, `publish(...)`), and inspect what a function returns before committing it to a `.vcl`. Results print in HCL/VCL style and are numbered into `_` and `_1`…`_N` (the prompt shows the index the next result will be bound to, so scrollback doubles as an index into the history); bare `NAME = EXPR` assignments and `:set` stash intermediate values; multi-line expressions continue at a `...` prompt; and `:help`, `:vars`, `:loglevel`/`:quiet`/`:logs` and friends control the session. Async runtime logs redraw cleanly around the prompt (console format on stderr; `:loglevel`/`:quiet` adjust verbosity live), and command history plus tab completion of top-level names are persisted across sessions. Requires a terminal; `Ctrl-C` cancels the current line while `:quit`/`Ctrl-D`/`SIGTERM` shut the server down. See [doc/repl.md](doc/repl.md).

## [0.40.0] - 2026-06-13

### Added

- **Postgres SQL client (`client "postgres"`)**: the second SQL dialect, sharing the engine, `query` sub-blocks, parameter syntax, result object, and `get()`/`call()` surface introduced for SQLite. Connect via a `dsn` or discrete `host`/`port`/`user`/`password`/`database`/`sslmode` fields, with an optional `search_path` and a `tls {}` block (mapped to libpq `sslrootcert`/`sslcert`/`sslkey`). Postgres SQLSTATE codes surface in `result.error.sqlstate`; `last_insert_id` is always null (use `RETURNING`). Built on the pure-Go `jackc/pgx/v5/stdlib` driver, so — unlike SQLite — it needs no cgo and is available in **every** published image, including the minimal one. See [doc/client-sql.md](doc/client-sql.md).
- **MySQL SQL client (`client "mysql"`)**: the third dialect, **completing the SQL feature**. Same shared engine and surface as the others; connect via a `dsn` (the go-sql-driver form) or discrete `host`/`port`/`user`/`password`/`database` fields, with an optional `tls {}` block (built from the shared TLS config and registered with the driver, honoring `insecure_skip_verify`). `parseTime=true`/`loc=UTC` are forced unless overridden in the `dsn`, so `DATETIME`/`TIMESTAMP` map to `time`. Unlike Postgres, MySQL populates `result.last_insert_id` from `AUTO_INCREMENT`; `result.error.code` carries the MySQL error number (e.g. `1062`) and `result.error.sqlstate` the ANSI SQLSTATE (e.g. `23000`). Pure Go (`go-sql-driver/mysql`), so available in every image. See [doc/client-sql.md](doc/client-sql.md).

### Fixed

- **Inline SQL with Postgres `::type` casts no longer fails as a phantom named parameter.** The `:name` placeholder detector matched the second colon of a `::` cast (e.g. `value::jsonb`), so a cast-using statement with no params or positional (`?`) params errored with *"statement uses :name placeholders but no parameters were supplied"*. Casts are now passed through untouched in those paths. (In a *named*-param statement, `sqlx` still collapses `::` to `:`; use `CAST(value AS type)` there — now documented.)

## [0.39.0] - 2026-06-12

### Added

- **Git fetch (`git` bootstrap block)**: a new `.vinit` block that clones a remote git repository during startup pass 1 and materializes one or more subtrees of it onto the local filesystem **before** any `.vcl` is parsed — so configuration and assets can be driven by a pinned repository instead of being baked into the image or a configmap. The fetched `.vcl` is then discovered by the normal pipeline as if it had shipped in the image. Supports branch/tag/commit revision selection (with shallow `depth`, default 1; `0` for full history), submodule recursion, and per-subtree `fetch "<name>" { from = …, into = … }` destinations sharing a single clone. Authentication covers HTTPS (`token` PAT shorthand, or `username`/`password`) and SSH (`private_key`/`private_key_file` + `passphrase`, with `known_hosts` host-key verification or an explicit `insecure_ignore_host_key`); credentials typically come from `env.*` and are never logged. Each destination is fetch-owned: an absent or empty `into` is populated, while a non-empty one is refused unless `overwrite = true`. The client is implemented in **pure Go** ([`go-git`](https://github.com/go-git/go-git)), so the feature works in **every** published image — including the scratch-based minimal image, which has no `git` binary or shell — and stays `CGO_ENABLED=0`-clean. All fetch errors are fatal unless the block is `disabled`. See [doc/git.md](doc/git.md).

### Fixed

- **`:latest` Docker tag now points to the standard image, not the minimal one.** The image-publishing workflow's minimal-image metadata applied its `-minimal` suffix to the semver tags but not to `latest` (docker/metadata-action skips the `latest` tag unless `onlatest=true`), so the minimal build pushed a bare `latest` that — being pushed after the standard image — clobbered it. Added `onlatest=true` so the minimal image publishes `latest-minimal` and `latest` reliably tracks the standard image.
- **Container images can now run the `git` bootstrap fetch out of the box.** Both runtime images run as UID 65534 (nobody), but shipped `/conf/git` and `/data/write` root-owned `0755` (and the minimal image's `/tmp` as `0755`), so a `git` block failed at startup with `mkdir /tmp/vinculum-git-…: permission denied`. The runtime-writable directories are now `chown`ed to 65534, and the minimal image's `/tmp` is given the conventional sticky `1777` (the go-git clone work directory).

### Changed

- The `traffic-light` example's web-UI directory (`HTML_DIR`) and the `voipms` example's OTLP `service_name` are now environment-overridable.

## [0.38.1] - 2026-06-08

### Fixed

- **SQLite `mode = "rw"` now creates the database file when missing**, matching the documented behavior. SQLite's URI `mode=rw` opens read-write but does not create the file (only `rwc` does), so a file-based `client "sqlite"` failed at startup with `unable to open database file`. The dialect now maps the documented `rw`/`rwc` modes to SQLite's `rwc`, and `ro` to `ro`.
- **SQL `call()` / `get()` no longer panic when the client failed to connect.** If `Start()` failed (e.g. an unwritable path), the connection pool handle was nil and a subsequent call dereferenced it; they now return a clear `sql client <name> is not connected` error.

## [0.38.0] - 2026-06-08

### Added

- **SQL clients (`client "sqlite"`)**: run SQL statements from a config via the existing polymorphic `get()` (exactly one row) and `call()` (general — any number of rows, modifying statements, and the result object) functions; no SQL-specific verbs. Supports named `query` sub-blocks with declared cardinality (`one`/`zero_or_one`/`many`/`exec`), positional (`?`) and named (`:name`) parameters, a result object (`rows`/`row`/`row_count`/`affected`/`last_insert_id`/`error`) where execution failures ride in `result.error` rather than raising, and column→cty type mapping (number/string/bool/`bytes`/`time`/decoded JSON/null). Built on `database/sql` + `sqlx`. SQLite requires a cgo-enabled build; on the minimal image `client "sqlite"` fails at config load with a clear "not compiled into this build" error. A companion `sql::must(result)` function fail-fasts on the error: it raises an evaluation error when `result.error` is non-null and otherwise returns the result unchanged, so it composes inline (`sql::must(call(...)).last_insert_id`). This is the first round of the SQL feature — the dialect-agnostic engine is in place and Postgres/MySQL are forthcoming. See [doc/client-sql.md](doc/client-sql.md).

## [0.37.1] - 2026-06-04

### Fixed

- **Container images can now actually build and load plugins** (0.37.0 shipped the plugin feature but the images could not use it). `-buildmode=plugin` always requires external (cgo) linking, and a statically linked host cannot `dlopen` a plugin — but the `vinculum-build` and runtime images were built with `CGO_ENABLED=0`. The `vinculum-build` image and the default (alpine) runtime image are now built **cgo-enabled** (with `gcc` + `musl-dev`, dynamically linked against musl). The scratch-based **minimal image remains statically linked and cannot load plugins** — this is now documented; its pre-created `/plugins` dir and `--plugin-path` flag are inert unless a config declares a `plugin` block.
- **Plugin ABI identity**: the default runtime image now builds its host binary by installing the versioned module (`go install github.com/tsarna/vinculum@<ref>`) instead of `go build .` from the working tree. A plugin imports vinculum as a versioned dependency, and `-buildmode=plugin` bakes the module path+version into every package's build ID; a `go build .` host gave those packages "main-module" identity and dependency-built plugins failed to load with `different version of package …`. A new `VINCULUM_REF` build-arg selects the version (the release tag; a commit pseudo-version for `:latest`/`:dev`).

### Added

- **`vinculum-plugin-build` wrapper** bundled in the `vinculum-build` image. It removes the common ways a plugin drifts out of ABI compatibility: it forces the toolchain (`GOTOOLCHAIN=local`), cgo, and `-buildmode=plugin -trimpath`; verifies the plugin's `go.mod` requires the exact `github.com/tsarna/vinculum` version the image targets; and diffs the plugin's *compiled* dependency closure against the release's pinned versions, **failing the build with the offending modules named** instead of letting it surface as a runtime `plugin.Open` error. Usage: `vinculum-plugin-build -o myplugin.so .`. See [doc/plugins.md](doc/plugins.md#build-contract) and [doc/container.md](doc/container.md#vinculum-build).
- **Release smoke gate**: the image-publishing workflow now builds a self-contained fixture plugin in the freshly published `vinculum-build` image and loads it in the freshly published runtime image (`vinculum check`), failing the run if the container plugin workflow does not work end to end. This guards against shipping a release where plugins cannot load.

### Changed

- **Plugin ABI documentation corrected**: `doc/plugins.md` and `doc/container.md` previously listed "CGO state (both enabled or both disabled)" as a valid choice — building a plugin with `CGO_ENABLED=0` is impossible (`-buildmode=plugin requires external (cgo) linking`). The docs now describe the real contract: cgo required on both sides, the host and plugin must reference the same `github.com/tsarna/vinculum` version, no shared-dependency drift, and the minimal image does not support plugins.

## [0.37.0] - 2026-06-02

### Added

- **Plugin loading**: Vinculum can now load Go shared-object plugins (`.so`) at startup. Plugins are declared in `.vinit` bootstrap files (a new HCL configuration file format processed before any `.vcl`) and extend Vinculum with the same registration points used by in-tree subsystems — functions, transforms, server types, client types, trigger types, conditions, wire formats, and editors. A new `--plugin-path` CLI flag (defaulted to `/plugins` in the container image) tells the loader where to find the `.so` files. Linux, macOS, and FreeBSD are supported; the spec, `.vinit` semantics, and ABI rules are documented in [doc/vinit.md](doc/vinit.md) and [doc/plugins.md](doc/plugins.md).
- **`RegisterTransformPlugin`** registration API for plugins (or in-tree subsystems) that contribute message-transform DSL functions. Collisions with built-in transforms or between plugins are reported as fatal diagnostics at config build time.
- **`vinculum-build` container image**: New `ghcr.io/tsarna/vinculum-build` multi-arch image (linux/amd64, linux/arm64) for compiling plugins ABI-compatible with a matching Vinculum release. Built from the same `golang:1.26-alpine` toolchain as the runtime images, with `CGO_ENABLED=0` / `GOOS=linux` baked in and the Go module cache pre-populated from Vinculum's `go.sum`. Published alongside the runtime images on every release with matching tags (`:X.Y.Z`, `:X.Y`, `:X`, `:latest`, `:dev`). See [doc/container.md](doc/container.md).
- **Runtime container images** (`vinculum` and `vinculum:*-minimal`) now pre-create `/plugins/` and pass `--plugin-path /plugins` in the default `CMD`, so plugins can be loaded by dropping a `.so` into `/plugins/` and declaring it in a `.vinit`.
- **`toggle()` on variables**: `var` blocks now implement `Toggleable` from `rich-cty-types` v0.2.0, so `toggle(var.x)` flips a boolean variable in place and returns the new value.
- **`toggle()` on timer conditions**: `condition "timer"` blocks (used as a bistable, i.e. without a declared `input =`) now implement `Toggleable`, so `toggle(condition.x)` flips the bistable's input — equivalent to `set(condition.x, !current)` and returning the new input value. The flip is routed through the same debounce + state-machine path as `set()`, so `activate_after`, `deactivate_after`, `cooldown`, `latch`, `inhibit`, and `invert` all compose unchanged. `toggle()` is rejected on a timer with a declared `input =` (same rule as `set()` — the condition is then level-tracking, not bistable). See [doc/condition.md](doc/condition.md#functions).

### Changed

- The license has been changed to Apache-2.0
- Bumped `rich-cty-types` to v0.2.0 and `vinculum-fsm` to v0.5.0. The new `Watcher.OnChange(ctx, source, old, new)` signature lets a single watcher disambiguate between multiple Watchables it has registered with. All in-tree watchers (reactive expressions, `trigger "watch"`, `trigger "watchdog"`, condition hooks) are adapted; the change is internal to vinculum but is a breaking change for any external Go code that registers a Watcher on a vinculum `Variable`, metric, condition state machine, or FSM `Instance`.

## [0.36.0] - 2026-05-27

### Added

- **RabbitMQ client (`client "rabbitmq"`)**: First-class AMQP 0-9-1 client built on [amqp091-go](https://github.com/rabbitmq/amqp091-go), via the new [`vinculum-rabbitmq`](https://github.com/tsarna/vinculum-rabbitmq) module (v0.1.0). A single `client "rabbitmq"` block owns one AMQP connection shared by any number of named `sender` sub-blocks (vinculum → exchange) and `receiver` sub-blocks (queue → vinculum), each on its own channel per RabbitMQ's recommendation. Highlights:
  - **Topology contract**: vinculum never declares exchanges (treat them as operational topology); queues are passive-declared by default or actively declared via an inline `declare` block, and bindings are declared in VCL and re-declared idempotently on every reconnect.
  - **Topic mapping with named captures**: senders use MQTT-style vinculum topic patterns with `+name`/`#name` capture; receivers use AMQP-style routing-key patterns with `*name` (one word) and `#name` (zero or more, dot-joined) capture. Captures land in `ctx.fields` for use in `routing_key` / `vinculum_topic` expressions.
  - **Mandatory delivery with broker-return correlation**: with `mandatory = true` + `confirm_mode = true` (default), an unroutable message — which the broker `Ack`s while also issuing a `Basic.Return` — surfaces as an `OnEvent` error carrying the broker's reply code and text. The sender sets a unique `Publishing.MessageId` per mandatory publish and drains the returns channel inline immediately after the ack; AMQP's wire ordering (Return before Ack) makes this race-free with no per-publish latency penalty.
  - **At-least-once delivery**: receiver default is manual ack after `subscriber.OnEvent` returns; on error the message is nacked without requeue (forwarded to a DLX if the queue has one configured via policy). Configurable `prefetch` caps in-flight unacked messages.
  - **Multi-broker failover + reconnect recovery**: the `brokers` list is walked in order on every connect and reconnect. Connection drops fire `on_disconnect`, walk the list with configurable exponential backoff, re-open all channels, re-declare receiver topology, re-register consumers, and fire `on_connect`. A separate per-channel watcher recovers channel-level errors (e.g. publishing to a non-existent exchange) without a full reconnect.
  - **TLS** via the standard `tls` block (auto-enabled for `amqps://`); **wire formats** pluggable via `vinculum-wire` (default `auto`); **fields** ↔ `headers` table conversion with W3C trace headers stripped from the visible fields map.
  - **OTel tracing**: producer span on send; new-root consumer span linked to the producer span on receive (the OTel async-messaging convention); link survives `AsyncQueueingSubscriber` queues.
  - **OTel metrics** follow messaging semconv v1.26.0 (`messaging.client.sent.messages`, `messaging.client.operation.duration`, `messaging.client.consumed.messages`, `messaging.process.duration`) plus rabbitmq-specific instruments (`rabbitmq.publisher.returned`, `rabbitmq.consumer.nacks`, `rabbitmq.client.connected`, `rabbitmq.client.reconnections`, `rabbitmq.client.channel_reopens`).
  - See [doc/client-rabbitmq.md](doc/client-rabbitmq.md) for the full VCL reference.

## [0.35.0] - 2026-05-14

### Fixed

  - **`server "http"` logging middleware now passes through `http.Hijacker` and `http.Flusher`**: the `statusCapturingResponseWriter` wrapper embedded only the `http.ResponseWriter` interface, which hid extra capabilities of the underlying writer behind type assertions. As a result, mounting `server "vws"` (or any other WebSocket-upgrading handler) under an HTTP `handle` block failed at upgrade time with `http.ResponseWriter does not implement http.Hijacker`. The wrapper now implements `Hijack()` and `Flush()` that delegate to the underlying writer when supported, so WebSocket upgrades and streaming responses (e.g. SSE) work correctly through the request-logging middleware.
  - **`set()` after a timeout-driven deactivation now correctly re-activates a `condition "timer"`**: previously, when a timer condition with `timeout = ...` (no declared `input =`) auto-deactivated, both the StateMachine's `rawInput` tracker and the `TimerCondition` wrapper's `stableInput` cache were left at `true` — so a subsequent `set(condition.name, true)` was silently deduped at the wrapper layer, never reached the state machine, and produced no transition (no `on_activate`, no watcher fire). Effectively any consumer of a `timeout` + `set()`-driven condition could be re-armed exactly once per process lifetime. `onTimeoutTimer` now resets `sm.rawInput = false` (mirroring `Clear()`'s reset for the same reason: the timeout consumes the active session, so re-assertion must register as a fresh edge), and `TimerCondition.submitInput` reconciles its cached `stableInput` from the SM's `RawInput()` accessor on every call so that wrapper-level dedup and debounce edge-collapse decisions stay consistent with the SM's actual view of the input. Verified with regression tests covering both the no-debounce and debounce paths in `conditions/timer_test.go`.
  - **`clear()` on a latched condition no longer masks an ongoing-true input**: previously, calling `clear(condition.name)` on a `timer` or `threshold` condition with a declared `input =` expression released the latch and unconditionally returned the condition to `inactive` — even when the input was still asserted. The latched-fault-you-can-clear-while-the-cause-persists pattern was a footgun for safety-style use cases (the very thing `latch = true` is meant to surface). `clear()` now re-samples the declared input after resetting: if it is still truthy, the condition re-activates and re-engages the latch. For threshold conditions, hysteresis is applied from the freshly-reset baseline, so values inside the deadband stay inactive (consistent with the first-sample initial-value rule). Debounce is bypassed on the re-activation edge since the signal has already proven stable; `activate_after`, `cooldown`, and `inhibit` still apply normally.
  - **Async dispatch context propagation**: Two bugs where in-flight trigger/FSM actions could be corrupted by the caller's context lifecycle.
  - **`trigger "watch"` cancellation leak**: the dispatched action's goroutine inherited the caller's ctx verbatim, so an upstream cancel (e.g. the HTTP request that drove the watched change completing) could cancel the action mid-flight. Fixed by applying `context.WithoutCancel` at the dispatch boundary. The action's trace span is now a new root linked to the caller's span (matches OTel async-messaging semantic conventions), replacing the prior child-span relationship that violated parent-before-child discipline.
  - **FSM events dropped the caller's context entirely**: the vinculum-fsm library's `Event` struct carried no ctx, so reactive-when and bus-delivered events processed under `context.Background()`, losing the caller's trace span, auth, and everything else. Fixed upstream in vinculum-fsm (new `Event.Ctx` field, wired through `OnEvent` subscriber and the event loop with `context.WithoutCancel` applied at the dequeue boundary). Vinculum's reactive-when enqueue now threads the callback's ctx into the event, and FSM transition spans are now linked roots. Requires vinculum-fsm v0.3.0.
  - Added `hclutil.StartLinkedTriggerSpan` as the canonical helper for the async dispatch pattern: `LinkFromContext` + `WithoutCancel` + `WithNewRoot` + `WithLinks`.
  - Audited and confirmed unaffected: `trigger "watchdog"`, `trigger "file"`, `config/signals`, `ActionSubscriber.OnEvent` (sync; bus's `deliverAsync` already applies the correct pattern), computed metrics (use a private ctx), server startup goroutines.
  - **CLI error reporting**: `vinculum check` and `vinculum serve` no longer print the command usage block when a config file has a syntax or validation error, and no longer print the error twice. Config diagnostics are now shown once as a clean `Error: ...` line with no stack-trace log spam. Usage is still printed for genuine argument-parsing errors (missing required args, unknown flags).

### Added

- **Sliding-window rate primitive on `condition "counter"` — `window = T`**: when set, the counter's count reflects the number of `increment()` calls in the last `T` (the classic "N events in the last T minutes" rate primitive). Each increment timestamps an entry into a FIFO; entries age out automatically when `event_time + window <= now`. A single internal timer is armed for the next-to-expire event, so the cost is `O(1)` timers regardless of event rate (memory is `O(N-in-window)` — one timestamp per live event). `decrement(condition.x [, n])` pops the `n` oldest entries (useful for "retract an in-flight count"); decrementing past empty is a no-op. `reset()` and `clear()` both empty the FIFO and release any latch. `window` is incompatible with `rollover`, `count_down`, and a non-zero `initial` (rejected at parse time, since rollover snap-back, count-down semantics, and synthetic baseline events all require a notion of "current count" independent of event timestamps). `clear()` is now also defined on counter conditions (equivalent to `reset()`, since counters have no `input =` to re-sample) — the previous restriction (`clear()` for timer and threshold only) is lifted. See [doc/condition.md](doc/condition.md#window).
- **Lazy control-flow functions `cond()`, `switch()`, and `try()`**: New `cond(c1, r1, c2, r2, ..., else)` for multi-branch conditionals and `switch(on, v1, r1, v2, r2, ..., default?)` for value-dispatch — both evaluate only the selected branch, making them safe for side-effectful VCL expressions (unlike HCL's `?:` ternary which evaluates both sides eagerly). `switch()` evaluates `on` exactly once and short-circuits as soon as a case-value matches; the trailing default is optional, with a runtime error if no case matches. Also replaces the stock HCL `try()` (from `hcl/v2/ext/tryfunc`) with a single-evaluation variant: the upstream implementation evaluates the selected expression twice (once in the type-inference callback, once in the value callback), which is a footgun when wrapping side-effectful calls like `try(send(ctx, ...), fallback)`. Vinculum's `try()` returns `cty.DynamicPseudoType` from its type callback so each expression is evaluated at most once, at the cost of a dynamic return type. `can()` is unaffected (it already evaluates its argument exactly once since it has a static `bool` return type). See [doc/functions.md](doc/functions.md#control-flow).
- **Unified `subscriber | action` delivery for receiver blocks**: every block that accepts an inline `action =` or a named `subscriber =` now also accepts `transforms = [...]` (a transform pipeline) and `queue_size = N` (wraps delivery in an async background queue) — matching what the top-level `subscription` block already offered. This covers `client "sqs_receiver"`, `client "kafka"` receivers, `client "mqtt"` subscribers, `client "redis_stream"` consumers, and `client "redis_pubsub"` subscribers. Implementation is shared via a new `config.SubscriberSource` helper so the four attributes behave identically everywhere (same exactly-one validation, same wrapping order: transforms → async queue). The async queue uses `vinculum-bus` v0.13.0's new-root consumer-span tracing, so every wrapped receiver's tracer provider is now propagated into the queue and async processing shows up in traces as spans linked to the upstream producer span. Fixes a latent bug where `subscription { queue_size = ... }` would wrap the subscriber but never `.Start()` the goroutine.
- **`trigger "at"` imperative control**: At triggers now support `set()` and `reset()` for imperative control from VCL expressions (e.g. FSM `on_entry` hooks). `set(trigger.<name>, time)` overrides the next fire with an explicit absolute time and revives dormant triggers (the override is consumed on fire; subsequent iterations fall back to the configured `time` expression). `set(trigger.<name>)` with no argument re-evaluates the `time` expression (as before), but now also resets `run_count` and can revive dormancy. `reset(trigger.<name>)` cancels any pending fire and goes dormant. New `repeat = false` attribute fires once then waits for `set()`. New `stop_when` attribute stops the trigger after a fire when the expression is true. Omitting `time` starts the trigger dormant, waiting for the first `set(trigger.<name>, time_value)` — enabling FSM-driven alarms at per-state wall-clock times. `ctx.last_error` is now exposed in `time` and `action` eval contexts. See [doc/trigger.md](doc/trigger.md#trigger-at) for details.
- **Condition lifecycle hooks**: New optional `on_init`, `on_activate`, and `on_deactivate` action-expression attributes on every condition subtype (`timer`, `threshold`, `counter`). `on_init` fires once in the `PostStart` phase — after every Startable has bootstrapped — so cross-condition references in init expressions are safe. `on_activate` and `on_deactivate` fire synchronously on each user-visible output transition (respecting `invert`), with caller-ctx propagated for trace-span continuity. Hooks replace the common `trigger "watch" ... skip_when = !ctx.new_value` boilerplate for condition-local reactions; `trigger "watch"` remains the right tool for async dispatch or cross-cutting observers. See [doc/condition.md](doc/condition.md#lifecycle-hooks).
- **`start_active` condition attribute**: New common attribute on all three `condition` subtypes (`timer`, `threshold`, `counter`) that forces the condition to begin in the `active` state at startup. Combined with `latch = true`, implements the classic fail-safe pattern where power loss is itself treated as a fault — the system comes up with the condition latched and must be explicitly cleared (`clear()` / `reset()`) before it can resume operation. No synthetic transition event fires at boot; `inhibit` does not suppress the configured initial state; `invert` still applies as a final output transform. See [doc/condition.md](doc/condition.md#start_active).
- **Build version identity**: New `version` package exposes `Version`, `Commit`, `BuildTime`, and `Modified` for the vinculum binary. Values come from `-ldflags "-X …"` when set, falling back to Go's automatic `runtime/debug` VCS stamping for local builds. Surfaced in three places:
  - `vinculum version` subcommand prints version, commit, build time, Go version, and platform.
  - Startup log message now includes a `version` field (e.g. `"version":"v0.34.0 (abc123def456)"`).
  - `sys.version`, `sys.commit`, `sys.build_time`, and `sys.modified` available in VCL expressions.
  The Docker image builds (`Dockerfile`, `Dockerfile.minimal`, `.github/workflows/docker.yml`) now plumb `VERSION` (from the resolved image tag), `COMMIT` (`github.sha`), and `BUILD_TIME` through as build-args, since `.git/` is excluded from the Docker context and VCS stamping would otherwise be unavailable.
- **CI: GHCR image cleanup workflow**: New scheduled workflow (`.github/workflows/ghcr-cleanup.yml`) that weekly prunes untagged `vinculum` and `vinculum-minimal` GHCR package versions older than 14 days. Preserves all tagged releases (`latest`, `dev`, and semver tags) and multi-arch manifest children via [dataaxiom/ghcr-cleanup-action](https://github.com/dataaxiom/ghcr-cleanup-action). Supports manual dispatch with a dry-run preview.
- **Example: traffic-light intersection**: New multi-file example under [examples/traffic-light/](examples/traffic-light/) modeling a four-way traffic intersection — `fsm` for the phase cycle, latched `condition "timer"` blocks for fault detection / emergency preempt / manual override, `trigger` blocks (interval, cron, start, watchdog) for phase advancement and mode switching, plus a `server "http"` + `server "vws"` pair serving a live web UI that pushes state changes over a WebSocket. A running instance is available at <https://traffic.thevinculum.org>.
- **Example: VoIP.ms metrics exporter — OTLP option**: The [examples/voipms/](examples/voipms/) configuration can now push metrics via `client "otlp"` when `OTLP_URL` is set, falling back to the existing Prometheus `server "metrics"` endpoint otherwise.

## [0.34.0] - 2026-04-22

### Added

- **`client "sns_sender"`**: AWS SNS sender support. Publishes vinculum bus events to SNS topics, target ARNs, or phone numbers with auto-detection, FIFO topic support, and per-message expression evaluation. See [doc/client-sns.md](doc/client-sns.md).

### Fixed

- **Diagnostic source locations**: Fixed `hcl:",def_range"` producing `:0,0-0` in all error diagnostics from client, server, trigger, wire_format, and metric block processors. The `gohcl.DecodeBody` tag only works for nested blocks, not top-level body decodes; all handlers now set `DefRange` from `block.DefRange` after decoding.
- **`ctx.fields` always present in action contexts**: `ctx.fields` is now always set as an empty object when a message has no fields, rather than being absent. Previously, referencing `ctx.fields` in a subscription action or per-message expression would error when no fields were present. Affects subscription actions, and all client per-message expressions (SNS, SQS, Kafka, MQTT, Redis pub/sub, Redis streams).

### Changed

- **`client "sqs_sender"`**: Removed batching (`SendMessageBatch`) support due to incompatibility with single-goroutine dispatch model.

## [0.33.0] - 2026-04-20

### Added

- **`fsm` block**: Finite state machines with guarded transitions, reactive `when` expressions (edge-triggered), key-value storage, snapshot/restore, MQTT topic pattern matching, and OpenTelemetry tracing. Built on [vinculum-fsm](https://github.com/tsarna/vinculum-fsm) (`v0.2.0`). See [doc/fsm.md](doc/fsm.md) for details.
- **`trigger "interval"` imperative control**: Interval triggers now support `set()` and `reset()` for imperative control from VCL expressions (e.g. FSM `on_entry` hooks). `set(trigger.<name>, duration)` restarts the trigger with a specific delay (+/- jitter); `reset(trigger.<name>)` cancels any pending timer and goes dormant. New `repeat = false` attribute makes the trigger fire once then wait for `set()`. Omitting `delay` starts the trigger dormant, waiting for the first `set()` — enabling FSM-driven one-shot timers with variable delays per state. See [doc/trigger.md](doc/trigger.md#trigger-interval) for details.

### Fixed

- **Dependency cycle between FSM and trigger blocks**: FSM `on_entry`/`on_exit`/`on_init`/`on_change`/`on_error` hooks and trigger `action`/`stop_when` attributes are now correctly excluded from config-time dependency extraction, since they are evaluated at runtime. Previously, an FSM referencing a trigger in `on_entry` while that trigger's action referenced the FSM would produce a spurious circular dependency error.

## [0.32.0] - 2026-04-18

### Fixed

- Skip hidden directories (names starting with `.`) during config directory walking. This prevents errors from Kubernetes ConfigMap `..data` symlink directories and follows standard Unix conventions.

### Added

- **AWS SQS support**: Two new client types for sending to and receiving from Amazon SQS queues, plus a shared AWS credentials block. Built on the [AWS SDK for Go v2](https://github.com/aws/aws-sdk-go-v2) via the new [vinculum-sqs](https://github.com/tsarna/vinculum-sqs) (`v0.1.0`) module.
  - **`client "aws"`**: Shared AWS credentials and region configuration with support for static credentials, assume-role (STS), custom endpoints (LocalStack/ElasticMQ), and AWS profiles. Child clients reference it via `aws = client.<name>`.
  - **`client "sqs_sender"`**: Sends vinculum bus events to an SQS queue. Features include wire format serialization, field-to-attribute mapping, optional topic attribute, FIFO queue support (`message_group_id`, `deduplication_id`), batching via `SendMessageBatch`, and W3C trace context propagation.
  - **`client "sqs_receiver"`**: Polls an SQS queue via long-polling and dispatches messages to a vinculum subscriber or action. Features include configurable concurrency, auto-delete or manual acknowledgement, per-message vinculum topic resolution via HCL expressions, system attribute mapping (`$message_id`, `$receipt_handle`, `$receive_count`, etc.), and W3C trace context extraction.
  - **`sqs_delete()` and `sqs_extend_visibility()` functions**: Global VCL functions for manual message deletion and visibility timeout extension. See [doc/client-sqs.md](doc/client-sqs.md) for details.

## [0.31.0] - 2026-04-18

### Added

- **`barcode()` function**: Generates barcode images as PNG `bytes` objects via the new [barcode-cty-func](https://github.com/tsarna/barcode-cty-func) (`v0.1.0`) module. Supports 11 formats: QR, DataMatrix, Aztec, PDF417, Code 128, Code 93, Code 39, Codabar, EAN-13, EAN-8, and Interleaved 2-of-5. Options include `scale`, `width`/`height`, and QR `error_correction`. 1D barcodes get sensible default heights automatically. See [doc/functions.md](doc/functions.md#barcode) for details.
- **`ctx.request.path`**: Route path parameters from `{name}` placeholders are now direct attributes on `ctx.request.path` (e.g. `ctx.request.path.id`), pre-extracted at route registration time for zero per-request parsing overhead.
- **`ctx.request.form`**: Parsed form data (query string + URL-encoded POST body) is now a direct attribute as `map(list(string))` (e.g. `ctx.request.form.name[0]`).
- **`ExtractPathParams()` utility**: Parses path parameter names from a Go 1.22+ route pattern once at registration time, returning a `[]string` for `BuildHTTPRequestObject`.

### Changed

- Extracted the `bytes` capsule/object type and `bytes()`/`base64encode()`/`base64decode()` functions into a new standalone module: [bytes-cty-type](https://github.com/tsarna/bytes-cty-type) (`v0.1.0`). Internal-only refactor with no VCL-visible changes; vinculum now depends on the external module for these symbols. The `filebytes()` function remains in vinculum.
- **HTTP request/response object redesign**: Commonly-needed fields are now direct attributes while infrequently-used or expensive fields use `get()`. Headers are accessed via `get(ctx.request, "header", name)` / `get(ctx.request, "header_all", name)` instead of the static `headers` map. The same change applies to HTTP client response objects.

### Removed

- **`ctx.request.headers`**: Removed from the static request object. Use `get(ctx.request, "header", name)` or `get(ctx.request, "header_all", name)` instead.
- **`get(ctx.request, "path_value", name)`**: Replaced by `ctx.request.path.name`.
- **`get(ctx.request, "form_value", name)`**: Replaced by `ctx.request.form.name`.
- **Response `headers` attribute**: Removed from the HTTP client response object. Use `get(resp, "header", name)` or `get(resp, "header_all", name)` instead.

## [0.30.0] - 2026-04-17

### Added

- **Pluggable wire format system** for consistent payload serialization/deserialization across all messaging clients and servers. Replaces the copy-pasted `serializePayload`/`deserializePayload` functions with a shared interface from the new [vinculum-wire](https://github.com/tsarna/vinculum-wire) (`v0.1.0`) module.
  - **`wire_format` attribute** on `client "kafka"`, `client "mqtt"`, `client "redis_pubsub"`, `client "redis_stream"`, and `client "redis_kv"`. Accepts `"auto"` (default), `"json"`, `"string"`, or `"bytes"`, or a custom `wire_format` capsule.
  - **`wire_format "<type>" "<name>"` block** for registering custom wire format plugins (e.g. Protocol Buffers, MessagePack). Custom formats participate in the dependency graph and are addressable as `wire_format.<name>` in expressions.
  - **`wire::serialize(wire_format, value)`**, **`wire::serialize_str(wire_format, value)`**, and **`wire::dewire::serialize(wire_format, data)`** expression functions for ad-hoc use in VCL. See [doc/functions.md](doc/functions.md#wire-format-serialize--deserialize).
  - **`CtyWireFormat` decorator** in the config layer transparently converts between cty values and native Go types, so messaging clients never need to import cty.

### Changed

- **`client "redis_kv"`: `value_encoding` replaced by `wire_format`** — the old `value_encoding` attribute (`auto`/`raw`/`json`) is replaced by the shared `wire_format` system. The old `raw` mode maps to `wire_format = "string"`.
- **Strings serialize verbatim in auto mode** — the `auto` wire format passes strings through unchanged (not JSON-encoded). Use `wire_format = "json"` for the old behavior.
- Updated vinculum-kafka to v0.9.0, vinculum-mqtt to v0.7.0, vinculum-redis to v0.2.0.

## [0.29.0] - 2026-04-17

### Added

- **Geographic functions**: 16 new functions for working with locations, solar events, celestial positions, geodesic calculations, and geometric queries via the new [geo-cty-funcs](https://github.com/tsarna/geo-cty-funcs) (`v0.2.0`) module. Includes `geo_point`, `geo_format`, `sunrise`/`sunset`/`solar_noon`/`solar_midnight`, `sun_position`/`moon_position`/`moon_phase`, `geo_inverse`/`geo_destination`/`geo_waypoints`, and `geo_area`/`geo_contains`/`geo_nearest`/`geo_line_intersect`. See [doc/functions.md](doc/functions.md#geographic) for details.

### Changed

- Extracted the URL capsule/object types and `url*` cty functions (`urlparse`, `urljoin`, `urljoinpath`, `urlqueryencode`, `urlquerydecode`, `urldecode`) into a new standalone module: [url-cty-funcs](https://github.com/tsarna/url-cty-funcs) (`v0.1.0`). Internal-only refactor with no VCL-visible changes; vinculum now depends on the external module for these symbols.
- Upgraded [time-cty-funcs](https://github.com/tsarna/time-cty-funcs) to `v0.2.0`. The `time` and `duration` capsule types now implement the rich-cty-types `Stringable` and `Gettable` interfaces, so `tostring(t)` / `tostring(d)` and `get(t, part)` / `get(d, unit)` work in VCL. See [doc/functions.md](doc/functions.md#timestamp--decomposition).

### Removed

- **VCL-visible:** `timepart(t, part)` and `durationpart(d, unit)` have been removed. Use the generic `get(t, part)` / `get(d, unit)` instead — same part/unit names, same return types.

## [0.28.0] - 2026-04-16

### Added

- **Redis/Valkey support**: Four new client blocks for Redis- and Valkey-compatible servers. `client "redis"` is a passive connection manager (standalone, cluster, or sentinel) referenced by three child clients: `client "redis_pubsub"` for `PUBLISH`/`SUBSCRIBE`/`PSUBSCRIBE` channel messaging, `client "redis_stream"` for persistent `XADD`/`XREADGROUP` logs with consumer groups, manual ack (`redis_ack()`), reclaim, and dead-letter, and `client "redis_kv"` exposing `GET`/`SET`/`INCR`/`HGET`/`HSET` through the generic `get()` / `set()` / `increment()` interface. See [doc/client-redis.md](doc/client-redis.md) for details.

### Changed

- Extracted the generic capability interfaces (`Stringable`, `Gettable`, `Watchable`, …), the generic dispatcher functions (`get`, `set`, `tostring`, `length`, …), and the `_ctx` / `_capsule` rich-object helpers into a new standalone module: [rich-cty-types](https://github.com/tsarna/rich-cty-types) (`v0.1.0`). Internal-only refactor with no VCL-visible changes; vinculum now depends on the external module for these symbols.

## [0.27.0] - 2026-04-13

### Added

- **`condition` blocks**: A new automation primitive producing a named, Watchable boolean from various input types and behavioral rules. Three subtypes — `condition "timer"` (temporal rules over a boolean signal), `condition "threshold"` (boolean derived from a numeric input via hysteresis), and `condition "counter"` (boolean from event counts via `increment()`/`decrement()`) — covering IEC 61131-3 timer and counter function-block behaviors and composable into pipelines via `input = get(condition.other)`. See [doc/condition.md](doc/condition.md) for details.
- **`reset(trigger.<name>)` on `trigger "watchdog"`**: Returns a watchdog to its post-startup state, reviving it if it had auto-stopped via `max_misses` or `stop_when`.

## [0.26.0] - 2026-04-12

### Added

- **`client "http"`**: A new HTTP(S) client capability. See [doc/client-http.md](doc/client-http.md) for details.

### Fixed

- consts can now reference ambients like env.* again without a dependency error.
- Handle implicit metrics wiring by making metrics blocks without explicit wiring depend on all otlp client and metrics servers.

## [0.25.0] - 2026-04-10

### Added

- **`procedure` blocks**: A limited imperative language for defining callable functions with variable assignments, conditionals (`if`/`elif`/`else`), loops (`while`, `range`), `switch`/`case`/`default`, and `break`/`continue`. Procedures compile to `cty/function.Function` values callable from any expression. See [doc/procedure.md](doc/procedure.md) for details.

### Breaking Changes

- **Log functions renamed**: `logdebug` → `log_debug`, `loginfo` → `log_info`, `logwarn` → `log_warn`, `logerror` → `log_error`, `logmsg` → `log_msg`. Update all VCL files accordingly.

## [0.24.0] - 2026-04-09

### Added

- **OTel-native metrics**: All metrics are now OpenTelemetry SDK-native. `metric` blocks create OTel instruments via a `MeterProvider`; `server "metrics"` bridges them to Prometheus exposition using the OTel-to-Prometheus exporter. `client "otlp"` can now push metrics via OTLP in addition to traces.
- **`client "otlp"` push metrics**: New attributes `metric_endpoint`, `metric_interval` (default `"60s"`), `include_go_metrics` (default `true`), and `default_metrics` for OTLP metric export alongside traces.
- **`metrics =` attribute on `server "http"` and `server "mcp"`**: Enables automatic HTTP server metrics (`http.server.request.duration`, `http.server.active_requests`, etc.) via `otelhttp`, following OTel semantic conventions.
- **HTTP metrics on standalone `server "metrics"` and `server "mcp"`**: Standalone servers that create their own HTTP listeners now produce HTTP metrics when a metrics backend is available.
- **Unified metrics auto-wire**: New `InstrumentMetrics` interface and `GetDefaultInstrumentMetrics()` search both `server "metrics"` and `client "otlp"` blocks for a default metrics backend. The `default_metrics` attribute controls which backend is used when both are configured.
- **Dot-separated metric names**: Metric block labels can use dots (e.g. `metric "gauge" "http.server.active_requests"`). OTel instrument names preserve dots; VCL access uses underscore translation (`metric.http_server_active_requests`). Collisions are detected at parse time.
- **`computed_interval` attribute on metric blocks**: Controls the polling interval for computed metrics (default `"15s"`).
- **`ResolveMeterProvider()`**: New config function for resolving a `metric.MeterProvider` from an HCL expression or default, accepting both server and client references.

### Changed

- **Go runtime metrics via OTel**: `server "metrics"` now uses OTel runtime instrumentation instead of raw Prometheus Go/process collectors. Metric names change from `go_goroutines` to `go_goroutine_count`, etc.
- **Computed metrics use polling**: Computed metrics (`value = expr`) now evaluate on a fixed interval (default 15s) instead of at each Prometheus scrape. Configurable via `computed_interval`. OTLP push benefits from this change.
- **Metric namespace separator**: `namespace` now uses dots for OTel instrument names (`namespace.name`) instead of underscores.

### Removed

- **`internal/promadapter` package deleted** — the `promadapter.Provider` (which bridged `prometheus.Registry` to `o11y.MetricsProvider`) is no longer needed since all consumers now use `metric.MeterProvider` directly.
- **`GetMetricsProvider()` removed from `MetricsRegistrar` interface** — consumers use `GetMeterProvider()` instead.
- **`GetDefaultMetricsProvider()` and `ResolveMetricsProvider()` removed** — replaced by `ResolveMeterProvider()`.
- **`o11y.MetricsProvider` dependency removed** — vinculum no longer imports or uses the `o11y.MetricsProvider`, `Counter`, `Histogram`, `Gauge`, or `Label` types.

### Breaking Changes

- **`server "metrics"`: `default` renamed to `default_metrics`** — Update existing HCL configs that use `default = true` on a `server "metrics"` block.
- **Prometheus metric name changes**: The OTel-to-Prometheus exporter adds `_total` suffix to counters and converts dots to underscores. Go runtime metric names have changed (e.g. `go_goroutines` to `go_goroutine_count`). Dashboard queries may need updating.
- **`client "kafka"` / `client "mqtt"` / `server "vws"` / `server "websocket"` metrics API** — these now use `WithMeterProvider(metric.MeterProvider)` instead of `WithMetricsProvider(o11y.MetricsProvider)`. Metric names follow OTel semantic conventions. Requires vinculum-bus v0.11.0, vinculum-kafka v0.8.0, vinculum-mqtt v0.6.0, vinculum-vws v0.11.0.

## [0.23.0] - 2026-04-07

### Added

- **`editor "line"` — `when` is now a post-match guard**: `when` is evaluated after the regex matches rather than before it. The full match context — `ctx.groups`, `ctx.named`, `ctx.count`, `ctx.line` — is available inside `when`, enabling capture-group–based qualification (e.g. `when = ctx.groups[1] == recordname`). If `when` is falsy the line continues to the next rule uncounted.

- **`editor "line"` blocks** — compile into callable functions that edit a text file (or string) in-place using ordered regex match-and-replace rules. Processed early alongside `function` and `jq` blocks so the resulting functions are available throughout the rest of the config.
  - `params` / `variadic_param` — declare function parameters, same semantics as `function` blocks
  - `mode = "file"` (default) — edits a file on disk; requires `--write-path`. Returns `true` if the file was modified, `false` otherwise.
  - `mode = "string"` — operates on a string in memory; returns the processed string. Does not require `--write-path`.
  - `match "<regex>" { ... }` — ordered match rules; first matching rule wins per line. Attributes: `required` (minimum match count), `max` (stop after n matches), `when` (post-match guard expression), `replace` (replacement text), `abort` (clean-abort if truthy), `update_state` (merge into running state), `incidental` (see below).
  - `before { content = expr }` / `after { content = expr }` — prepend/append content to the output. Both blocks see the **final accumulated state** after all lines are processed. `before` uses a two-pass mechanism internally so that prepended content can reference state collected during the body. Both accept `incidental = true`.
  - `incidental = true` on a `match`, `before`, or `after` block — the replacement/content is written but does not itself count as a change. If all modifications in an edit run were incidental, the file is not written and the function returns `false`. Useful for housekeeping updates (timestamps, serial numbers) that should ride along with a real change but not trigger a write on their own.
  - `state = { ... }` — declares initial values for state variables; rules accumulate state via `update_state`; `state.<name>` is in scope in all expressions.
  - `backup = "<suffix>"` — hard-links the original file to `<path><suffix>` before the atomic rename (e.g. `backup = "~"` or `backup = ".bak"`).
  - `create_if_absent = true` — treat a missing file as empty rather than an error.
  - `lock = true` — acquire an exclusive `flock(2)` on a sibling `.lock` file before editing, serializing concurrent invocations. Works on local filesystems and NFSv4 (including AWS EFS). Lock is released automatically on return.
  - Regex capture groups exposed as `ctx.groups` (list) and `ctx.named` (map); `ctx.count` tracks per-rule match count; `ctx.line` / `ctx.lineno` / `ctx.filename` provide line context.
  - See [doc/editor.md](doc/editor.md) for full reference.

## [0.22.0] - 2026-04-03

### Changed

- **`PreStoppable` lifecycle interface and `trigger "shutdown"` fix** — new `PreStoppable`
  interface with a `PreStop() error` method, called in reverse registration order before any
  `Stoppable.Stop()` calls begin. `trigger "shutdown"` now implements `PreStoppable` instead of
  `Stoppable`, guaranteeing that shutdown actions execute while all clients, buses, and
  subscriptions are still fully operational. Previously, whether a shutdown action had access to a
  given client depended on registration order in the config file.

### Added

- **`client "otlp"` — distributed tracing via OpenTelemetry** — new client type that configures the OTel SDK and exports spans to any OTLP/HTTP collector (Jaeger, Grafana Tempo, Honeycomb, etc.):
  - `endpoint` — OTLP/HTTP base URL (e.g. `"http://localhost:4318"`)
  - `service_name` / `service_version` — recorded on every span
  - `sampling_ratio` — head-based sampling for new root spans (default `1.0`); inherited traces are always continued
  - `default = true` — marks this client as the auto-wire target when multiple OTLP clients are declared; omit when there is only one
  - `headers` — `map(string)` of HTTP headers added to every export request (e.g. auth tokens)
  - `tls {}` — optional TLS configuration for the collector connection
  - On `Start()`, sets the global OTel `TracerProvider` and installs W3C TraceContext + Baggage propagation; flushes pending spans on `Stop()`
  - See [doc/client-otlp.md](doc/client-otlp.md) for full reference
- **Distributed tracing for `server "http"`** — add `tracing = client.<name>` to instrument an HTTP server with OTel spans. Each inbound request produces a span: incoming `traceparent` headers are continued as child spans; requests without one start a new root span.
  - Auto-wires to the single `client "otlp"` block (or one marked `default = true`) when `tracing =` is omitted
  - Span name: `METHOD /path`; OTel HTTP semantic conventions applied via `otelhttp`
  - Rich access log at request completion: method, route, path, status, duration_ms, bytes, trace_id
- **Distributed tracing for `server "mcp"` (standalone mode)** — when a `server "mcp"` block has a `listen` address, it now accepts an optional `tracing = client.<name>` attribute. Incoming W3C `traceparent` / `tracestate` headers are extracted and a server span is created for each request, exactly as with `server "http"`. Auto-wires to the default OTLP client when `tracing =` is omitted. Mounted MCP servers (no `listen`) continue to inherit tracing from the parent `server "http"`.
- **Distributed tracing for `server "metrics"` (standalone mode)** — same as above: when `server "metrics"` has a `listen` address, add `tracing = client.<name>` (or rely on auto-wiring) to extract trace context and create spans for `/metrics` scrape requests. Mounted metrics servers inherit tracing from their parent HTTP server.
- **Distributed tracing for `client "mqtt"`** — add `tracing = client.<name>` to instrument an MQTT client with OTel spans. The subscriber extracts incoming `traceparent`/`tracestate` user properties and creates a `process <topic>` span (new trace root linked to the producer span per OTel messaging conventions); the publisher injects trace context into outbound user properties and creates a `send <topic>` span. Both spans carry OTel messaging semantic convention attributes (`messaging.system`, `messaging.destination.name`, `messaging.operation.type`, `messaging.operation.name`) and correct span kinds (`SpanKindProducer` / `SpanKindConsumer`). Auto-wires to the default OTLP client when `tracing =` is omitted. Trace headers are filtered from the `fields` map delivered to VCL actions.
- **Distributed tracing for `client "kafka"`** — add `tracing = client.<name>` to instrument a Kafka client with OTel spans. Uses the official `kotel` plugin from franz-go with `LinkSpans()` enabled: consumer spans are new trace roots linked to the producer span (per OTel messaging semantic conventions for async pub/sub), with full messaging semantic convention attributes set automatically. The producer injects trace context into outbound record headers. Auto-wires to the default OTLP client when `tracing =` is omitted. Trace headers are filtered from the `fields` map delivered to VCL actions.
- **`ctx.trace_id` and `ctx.span_id`** — when a request is being handled inside an active OTel span, these string variables are available in all VCL action expressions (not just HTTP); both are `""` when no span is active (NOOP tracer or no `client "otlp"` configured)
- **Distributed tracing for outgoing OpenAI API calls** — `client "openai"` now wraps its HTTP transport with `otelhttp.NewTransport`, so every LLM API call creates a child span of the current context and injects `traceparent` / `tracestate` headers into the outgoing request. No configuration change required; uses the global TracerProvider set by `client "otlp"`.
- **Distributed tracing for all trigger types** — every trigger firing now creates its own root OTel span (or a child span when context is available), so the full work chain from trigger through bus events to outgoing clients is traceable as a single trace:
  - Span name: `trigger.<type> <name>` (e.g. `trigger.interval my_poll`, `trigger.cron nightly/cleanup`)
  - Span covers only the action execution, not idle wait time (delay, sleep, or timer countdown)
  - Errors from action expression evaluation are recorded on the span (`span.RecordError` + `codes.Error` status)
  - `ctx.trace_id` / `ctx.span_id` are populated in the VCL action context for all trigger types
  - `trigger "watch"` uses the incoming context as parent (preserving the trace from the `set()` caller); all other triggers start a new root span
  - `trigger "signals"` creates a span per signal delivery (timing and errors recorded; VCL context uses pre-built eval context from config time)
  - **Distributed tracing for `bus` blocks** — add `tracing = client.<name>` to instrument an event bus with OTel spans. Each `Publish` and `PublishSync` call creates a producer span; each subscriber delivery creates a consumer span (child for sync, new root with a link for async) per OTel messaging semantic conventions. Auto-wires to the default OTLP client when `tracing =` is omitted.
- **Distributed tracing for VWS** - vinculum now depends on v0.10.0 of vinculum-vws, which adds support for tracing headers.

## [0.21.0] - 2026-04-02

### Changed

- **`PostStartable` lifecycle interface** — new `PostStartable` interface with a `PostStart() error`
  method, called once for each registered component after all `Startable.Start()` calls complete.
  This guarantees that buses, clients, and subscriptions are fully initialised before any
  PostStartable component dispatches events or evaluates action expressions.

- **`trigger "start"` now fires in `PostStart()`** — previously the action was evaluated
  synchronously during config parsing (before any component had started), which allowed action
  expressions to race the runtime. The action now fires in `PostStart()`, after the full startup
  sequence. `trigger.<name>` is `null` until `PostStart()` completes. Use `const` for values
  that must be computed at parse time (e.g. a startup timestamp captured before any I/O).

- **`trigger "after"`, `trigger "interval"`, `trigger "at"`, `trigger "watchdog"` goroutines now
  launch in `PostStart()`** — previously goroutines were launched in `Start()`, meaning very short
  delays or zero initial delays could cause the first action invocation to race other components
  that had not yet completed `Start()`. Goroutines (and their delay/timeout clocks) now start only
  after all `Startable`s have completed, so the full runtime is available on every action
  invocation. `Stop()` continues to work correctly if called before `PostStart()` (returns nil
  immediately with no goroutine to wait for).

### Added

- **`try()` and `can()` functions** — HCL-native error-handling functions from
  `github.com/hashicorp/hcl/v2/ext/tryfunc`, now registered in the standard function set:
  - `try(expr...)` — evaluates each argument in sequence and returns the first one that
    succeeds without error; returns an error only if all arguments fail
  - `can(expr)` — evaluates the expression and returns `true` if it succeeds, `false` if
    it produces any error; useful for conditional logic based on whether a value is valid

- **`trigger "file"`** — new filesystem-event trigger backed by
  [fsnotify](https://github.com/fsnotify/fsnotify); fires an action expression each time
  a matching file-system event occurs:
  - `path = expression` — required; directory to watch (absolute or relative to `--file-path`)
  - `action = expression` — evaluated on each matching event in a new goroutine
  - `events = ["create", "write", "delete", "rename", "chmod"]` — optional; defaults to all events
  - `recursive = true` — optional; also watch all subdirectories (including ones created after start)
  - `filter = "glob"` — optional; only dispatch events whose filename matches the glob pattern
  - `debounce = "duration"` — optional; coalesce bursts of events on the same path into a single
    dispatch after the specified quiet period
  - `on_start_existing = true` — optional; dispatch synthetic `create` events for all files already
    present in `path` at `PostStart()` time (the live watch is already active so no real events are
    lost)
  - `skip_when = expression` — optional; skip this firing if the expression returns `true`
  - `ctx.event_path` — full path of the file that triggered the event
  - `ctx.event` — event type string: `"create"`, `"write"`, `"delete"`, `"rename"`, or `"chmod"`
  - `ctx.path` — the configured watch directory
  - `ctx.run_count`, `ctx.last_result`, `ctx.last_error` — standard trigger context values
  - `get(trigger.<name>)` returns the most recent action result, or `null` before the first firing
  - Requires `--file-path` to be set; the trigger type is not registered when the feature is absent
  - See [doc/trigger.md](doc/trigger.md) for full reference and examples

- **`trigger "at"`** — new trigger type that fires an action at a dynamically computed
  absolute time, then repeats by re-evaluating the `time` expression each cycle:
  - `time = expression` — required; must evaluate to a time capsule (e.g. from `now()`,
    `timeadd()`, or future `sunrise()`/`sunset()` functions)
  - `action = expression` — evaluated each time the trigger fires
  - Always repeats: after each firing the `time` expression is re-evaluated to schedule
    the next occurrence; this is the natural fit for non-uniform recurring schedules
    where the interval between firings varies (contrast with `trigger "cron"` for fixed
    schedules and `trigger "interval"` for delays)
  - `get(trigger.<name>)` returns the currently scheduled fire time as a time capsule,
    or `null` before the first evaluation; use with `until()` to compute time remaining
  - `set(trigger.<name>)` wakes the goroutine to re-evaluate `time` immediately without
    firing the action — use from a `trigger "interval"` to recompute the schedule
    dynamically as conditions change (e.g. a vehicle's position shifting)
  - If `time` evaluates to a past time, the action fires immediately with a warning
  - If the `time` expression errors, the trigger logs the error and retries after one minute
  - `ctx.trigger`, `ctx.name`, `ctx.run_count`, and `ctx.last_result` are available in
    both `time` and `action` expressions
  - See [doc/trigger.md](doc/trigger.md) for full reference and examples

- **Watchables** — `var`, gauge `metric`, and counter `metric` values now implement the
  `Watchable` interface, enabling reactive, event-driven patterns without polling:
  - Every `set()` and `increment()` call on a watchable notifies all registered watchers
    synchronously after the value mutex is released; the calling goroutine blocks until all
    `OnChange` callbacks return
  - Notifications fire on **every** `set()`/`increment()` call, even when the value is
    unchanged — this is intentional so watchdog heartbeat patterns work correctly; consumers
    that want changes-only behavior use `skip_when = ctx.old_value == ctx.new_value`
  - The `context.Context` passed to `set()`/`increment()` is forwarded verbatim to all
    `OnChange` callbacks, preserving request-scoped metadata (trace IDs, auth tokens, etc.)
  - `HistogramMetric` and computed metric variants are **not** Watchable in this release
  - See [doc/trigger.md](doc/trigger.md) for `trigger "watch"` and the watchdog `watch` attribute

- **`trigger "watch"`** — new reactive trigger type that fires an action expression each time
  a Watchable value changes:
  - `watch = expression` — required; must evaluate to a watchable `var` or `metric` capsule
  - `action = expression` — evaluated on each change in a new goroutine (non-blocking)
  - `skip_when = expression` — optional; skip this firing if the expression returns `true`
  - `ctx.old_value` / `ctx.new_value` provide the before/after values in the action and skip_when contexts
  - `get(trigger.<name>)` returns the most recently observed value, or `null` before any change
  - `Stop()` unregisters the watcher and waits for all in-flight action goroutines to finish

- **`trigger "watchdog"` `watch` attribute** — optional `watch = expression` attribute on
  watchdog triggers to auto-feed the watchdog whenever a Watchable's value changes, eliminating
  the need for explicit `set(trigger.<name>, ...)` calls in every producer path; manual `set()`
  calls remain valid and are unaffected

- **`trigger "watchdog"` `max_misses` / `stop_when`** — two new optional attributes to
  auto-stop a watchdog after a condition is met, consistent with `trigger "interval"`:
  - `max_misses = N` — stops after N consecutive fires without a `set()` in between
  - `stop_when = expression` — stops when the boolean expression evaluates `true` after a fire;
    the same `ctx` as the action is available, including the updated `ctx.miss_count`
  - Both attributes are independent; if both are provided, the trigger stops when either
    condition is satisfied
  - A stopped watchdog is **revived** by calling `set()`: `miss_count` resets to 0 (clearing
    any `max_misses` condition), and `stop_when` is re-evaluated against the post-`set()` state;
    if it is now `false`, the watchdog re-arms immediately

## [0.20.0] - 2026-04-01

### Breaking Changes

- **HTTP action return value is now the response** — the return value of a `handle` action
  expression determines the HTTP response sent to the client. Previously it was ignored.
  Automatic coercion applies: `string` → 200 text/plain, `null` → 204, objects/maps/lists →
  200 application/json, `bytes` → 200 with its content type.
- **`respond()`, `setheader()`, `redirect()` removed** — these HTTP-action-context-only
  side-effect functions have been removed. Use the return value and the new response
  functions instead (see Added below).
- **`httpstatus` renamed to `http_status`** — the ambient HTTP status code constant object
  is now `http_status.NotFound`, `http_status.OK`, etc. Update all references.

- **`bytes` is now a rich object type** — `bytes()`, `base64decode(..., ct)`, and `filebytes()` now return an object with a `content_type` attribute and a `_capsule` for interface dispatch, rather than a raw capsule. Callers should use `b.content_type` instead of `get(b, "content_type")`. The `get()` function is no longer supported on `bytes` values.
- **`bytes` `get()` modes removed** — all `get()` modes on bytes values are gone:
  - `get(b)` / `get(b, "utf8")` / `get(b, "string")` / `get(b, "text")` → use `tostring(b)` instead
  - `get(b, "base64")` → use `base64encode(b)` instead
  - `get(b, "len")` / `get(b, "length")` / `get(b, "size")` → use `length(b)` instead
  - `get(b, "content_type")` → use `b.content_type` instead

### Changed

- **`server "http"` `files` block now requires `--file-path`** — any non-disabled `files` block
  requires `vinculum serve` to be started with `--file-path`. Relative `directory` values are
  resolved against that base directory (previously they resolved against the process working
  directory and `--file-path` was not involved).

### Added

- **Authentication (`auth` block)** — optional authentication for `server "http"`, `server "mcp"`, and `server "metrics"` blocks. Five modes are supported:
  - `auth "basic"` — HTTP Basic authentication against a static credentials map or a custom per-request expression
  - `auth "oidc"` — OpenID Connect bearer token validation via local JWKS (with automatic OIDC discovery and background key refresh) or RFC 7662 token introspection
  - `auth "oauth2"` — RFC 7662 token introspection with optional result caching (`cache_ttl`)
  - `auth "custom"` — arbitrary per-request expression; return an object (success), null (401), or an `http_response`/`http_redirect` value (e.g. for login redirects)
  - `auth "none"` — explicitly opt out of inherited server-level auth on a specific `handle` or `files` block
  - On `server "http"`, auth may be set at server level (applies to all routes) and overridden per `handle`/`files` block; `auth "none"` disables inherited auth for a route
  - On success, `ctx.auth` is available in all action expressions: `ctx.auth.username`, `ctx.auth.subject`, `ctx.auth.claims`
  - `username` is populated from Basic auth credentials, the introspection `username` field, or the JWT `preferred_username` claim
  - `clock_skew` (OIDC) and `cache_ttl` (OAuth2) accept a string (Go duration syntax), a plain number (seconds), or a `duration` capsule
  - On standalone `server "mcp"` with `auth "oidc"`, vinculum automatically serves `GET /.well-known/oauth-authorization-server` for MCP client OAuth2 discovery
  - New dependency: `github.com/lestrrat-go/jwx/v2` for JWKS caching and JWT validation
  - See [doc/server-auth.md](doc/server-auth.md) for the full reference

- **HTTP response functions** — new globally-available functions for building HTTP responses
  (not scoped to `handle` actions; usable from any action expression):
  - `http::response(status[, body[, headers]])` — build a response with explicit status,
    optional body (auto-coerced by type), and optional headers (`map(string)` or
    `map(list(string))`)
  - `http::redirect(url)` / `http::redirect(status, url)` — redirect response; defaults to
    302 Found
  - `http::error(status, message)` — error response with plain-text body; integrates
    naturally with `try()` for mapping errors to specific HTTP status codes
  - `http::add_header(response, name, value)` — return new response with header value appended
  - `http::remove_header(response, name)` — return new response with header removed
  - `http::set_cookie(cookieObj)` — format a `Set-Cookie` header value from a cookie definition
    object; use with `http::add_header()` to attach cookies to any response
- **`mcp::user_message()` and `mcp::assistant_message()`** — renamed from
  `mcp_user_message()` and `mcp_assistant_message()` for naming consistency (underscore
  as namespace separator only, not word separator).

- **`http::basic_auth(user, password)` function** — returns the `Authorization` header value for HTTP Basic authentication (`"Basic <base64(user:password)>"`); available in the new `httputil` function plugin
- **URL parsing and manipulation functions** — new `url` function plugin:
  - `urlparse(rawURL)` — parse a URL string into a URL object with named attributes (`scheme`, `host`, `hostname`, `port`, `path`, `query`, `fragment`, etc.) accessible directly (e.g. `u.scheme`, `u.path`)
  - `urljoin(base, ref)` — resolve `ref` against `base` following RFC 3986; accepts strings, URL objects, or URL capsules
  - `urljoinpath(base, elem...)` — append percent-escaped path elements to `base`
  - `urlqueryencode(params)` — encode a `map(string)` or `map(list(string))` into a query string
  - `urlquerydecode(query)` — decode a query string into `map(list(string))`
  - `urldecode(str)` — percent-decode a string (inverse of `urlencode`; `+` decoded as space)
  - `get(u, "query_param", key)` — return `list(string)` of all values for a named query parameter
  - `tostring(u)` — return the canonical URL string from a URL object
- **Enhanced `tostring()` and `length()`** — the built-in `tostring()` and `length()` functions now dispatch on rich VCL types: `tostring(b)` returns the UTF-8 content of a `bytes` value; `length(b)` returns its byte count; `tostring(u)` returns the canonical string of a URL object. Falls back to standard behavior for all other types.
- **`Stringable` and `Lengthable` interfaces** — capsule types may now implement `ToString(ctx) (string, error)` and `Length(ctx) (int64, error)` to integrate with `tostring()` and `length()` respectively. Objects carrying a `_capsule` attribute are also supported (following the same convention as `_ctx` for context propagation).
- **TLS support for `server "http"`** — add a `tls {}` sub-block to serve HTTPS. Supports file-based certificates (`cert`/`key`) and the new `self_signed = true` option for development.
- **TLS support for `server "metrics"`** (standalone mode) — add a `tls {}` sub-block to a metrics server with a `listen` address to serve the scrape endpoint over HTTPS.
- **TLS support for `server "mcp"`** (standalone mode) — add a `tls {}` sub-block to a standalone MCP server to serve the MCP Streamable HTTP endpoint over HTTPS.
- **`self_signed` TLS option** — setting `self_signed = true` in any server `tls {}` block generates an ephemeral ECDSA P-256 certificate at startup (valid for `localhost`/`127.0.0.1`). Useful for local development and integration testing; mutually exclusive with `cert`/`key`.

## [0.19.0] - 2026-03-30

### Breaking Change!

- `vinculum server` command line changed to `vinculum serve` to me more consistent with typical verb-like subcommand naming.

### Added

- **`vinculum check` command** — validates configuration files without starting any services;
  exits non-zero with diagnostics on error, exits zero with a confirmation message on success;
  accepts the same `--file-path` and `--write-path` flags as `serve`
- **`bytes` capsule type** — first-class binary data type with an optional MIME/content type:
  - `bytes(str [, content_type])` — create a `bytes` value from a UTF-8 string
  - `bytes(b [, content_type])` — re-wrap an existing `bytes` value, optionally overriding its content type
  - `get(b)` / `get(b, "utf8")` — read as UTF-8 string; `get(b, "base64")` — base64 string;
    `get(b, "len")` — byte count; `get(b, "content_type")` — MIME type
  - `base64decode(str, content_type)` — decodes to a **`bytes` capsule** (new two-arg form);
    `base64decode(str)` continues to return a string (backward compatible)
  - `base64encode(value)` — now also accepts a `bytes` capsule in addition to strings
  - `filebytes(path [, content_type])` — read a file into a `bytes` capsule (gated by `--file-path`)
  - `mcp::image()` — now accepts a `bytes` capsule as its first argument; MIME type is taken from the
    capsule's content type and may be overridden by an explicit second argument
- Variables may now optionally have a defined type and nullability.
- `sys.plugins` lists the names of all plugin components
- `sys.features` lists the names of all enabled feature flags (e.g. `"readfiles"`, `"writefiles"`, `"allowkill"`)
- `sys.signals` — platform signal table available in VCL:
  - `sys.signals.SIGXXX` → the signal number for `SIGXXX` (all signals known on the current OS)
  - `sys.signals.bynumber["N"]` → the signal name for number `N`
- **`kill(pid, signal)` VCL function** — sends a signal to a process; both arguments are integers
  (see `sys.signals` for portable signal numbers); gated by the `--allow-kill` flag
- **`sqid(id[, options])` and `unsqid(s[, options])` VCL functions** — encode/decode [sqids](https://sqids.org): short, URL-safe IDs generated from one or more non-negative integers; `id` may be a single number or a list; optional `options` object supports `alphabet`, `min_length`, and `blocklist`

### Changed

- Refactor context-building utilities into ctyutil (for possible later moving to an external repo) and hclutil
- Reorganize code and move to plugin registries for functions, servers, clients, triggers, and ambient values (env.*, httpstatus.*, sys.*)
- Feature flags are now a proper registry: `ConfigBuilder.WithFeature(name, value)` replaces the former `WithBaseDir`/`WithWriteDir` methods; `--file-path` and `--write-path` are now registered as the `"readfiles"` and `"writefiles"` features internally
- `get`, `set`, `increment`, and `observe` now accept an optional leading context argument —
  `get([ctx,] thing [, ...])` — allowing callers to propagate context into implementations;
  when omitted, `context.Background()` is used. Internal refactor: `Gettable`, `Settable`,
  `Incrementable`, and `Observable` interfaces now take a `context.Context` as their first
  parameter, consistent with `Callable`.

## [0.18.0] - 2026-03-28

### Breaking Change!

- client "kafka": producer blocks -> sender, consumer blocks -> receiver
- topic_mapping { pattern = "..." } -> topic "..." {} (labeled block)
- topic_subscription { kafka_topic/mqtt_topic = "..." } -> subscription "... {} (labeled block)
- client.* attributes: producers/producer -> senders/sender

### Added

- **[`client "mqtt"`](doc/client-mqtt.md)** — MQTT send and receive support.

## [0.17.0] - 2026-03-26

### Breaking Change!

- `cron` and `signals` blocks replaced by `trigger "cron" ...` and `trigger "signals" ...` blocks.

### Added

- Metrics can now be computed on-demand. Labels are not currently supported for computed metrics.
- New `trigger` block subsuming the previous `cron` and `signals` blocks, and
  adding new trigger types `after`, `interval`, `once`, `shutdown`, `start`, and `watchdog`.

## [0.16.0] - 2026-03-25

### Added

- **[`client "kafka"`](doc/client-kafka.md)** — Apache Kafka producer and consumer support
- **`metrics =` on `bus` and `server "vws"`** — bus and VWS server blocks now accept a
  `metrics = server.<name>` attribute to wire Prometheus instrumentation

## [0.15.0] - 2026-03-23

### Breaking Change!

- **Logging functions renamed** — underscores removed for naming consistency: `log_debug` →
  `logdebug`, `log_info` → `loginfo`, `log_warn` → `logwarn`, `log_error` → `logerror`,
  `log_msg` → `logmsg`

### Added

- **MCP server mounting under HTTP** — MCP server blocks no longer require a `listen` address;
  omit `listen` and reference the server via `handler = server.<name>` in an HTTP `handle` block
  to serve MCP alongside other routes on a shared port
- **`sys.*` built-in variable namespace** — read-only VCL variables exposing process and host
  identity captured at config-build time: `sys.pid`, `sys.hostname`, `sys.user`, `sys.uid`,
  `sys.group`, `sys.gid`, `sys.os`, `sys.arch`, `sys.cpus`, `sys.executable`, `sys.cwd`,
  `sys.homedir`, `sys.tempdir`, `sys.filepath`, `sys.writepath`, `sys.starttime`, `sys.boottime`
- **File write functions** — `filewrite(path, content)` and `fileappend(path, content)`, gated by
  `--write-path <dir>` and sandboxed to that directory; `sys.writepath` exposes the configured
  path in VCL
- **`templatefile(path, vars)` and `gotemplatefile(path, vars)` functions** — render a HCL-style
  or Go `text/template` template file with a variable map
- **Time and duration types and functions** — two new first-class VCL capsule types (`time`,
  `duration`) and a full function library:
  - Core: `now()`, `parsetime()`, `duration()`, `timeadd()`, `timesub()`, `since()`, `until()`,
    `formattime()`, `formatduration()`; `sys.starttime` and `sys.boottime`
  - Unix/decomposition/timezone: `fromunix()`, `unix()`, `timepart()`, `durationpart()`,
    `intimezone()`, `timezone()`, `absduration()`
  - Calendar arithmetic: `adddays()`, `addmonths()`, `addyears()`, `timeround()`,
    `timetruncate()`
  - Comparison: `timebefore()`, `timeafter()`, `durationlt()`, `durationgt()`
  - Formatting: `strftime()`, `strptime()`, named `@format` aliases (`@rfc3339`, `@date`,
    `@time`, etc.) for `formattime`/`parsetime`
  - Duration arithmetic: `durationadd()`, `durationsub()`, `durationmul()`, `durationdiv()`,
    `durationtruncate()`, `durationround()`
- **`nextzoneserial()` and `parsezoneserial()`** — DNS zone serial number utilities (YYYYMMDDNN
  format); `nextzoneserial(s[, t])` computes the next valid serial, `parsezoneserial(s)` converts
  back to an approximate time

## [0.14.1] - 2026-03-22

No functional changes. This release just adds a documentation index for the benefit of the
vinculum-ai tool (see github.com/tsarna/vscode-vinculum)

## [0.14.0] - 2026-03-22

### Added

- **LLM client support** — new `client "openai"` block and `call()` VCL function for synchronous
  LLM API calls; works with OpenAI and any OpenAI-compatible provider (Groq, Together AI, Mistral,
  Ollama, LM Studio, Google Gemini, etc.)
  - `api_key`, `model`, `base_url`, `max_tokens`, `temperature`, `timeout` attributes
  - `max_input_length` — optional character cap on user/assistant message content; returns
    `stop_reason = "error"` with `error.code = "input_too_long"` without making an API call
    when exceeded (system messages are not counted)
  - Response object always present: `content`, `stop_reason` (`"stop"`, `"max_tokens"`, or
    `"error"`), `model`, `usage` (`input_tokens`, `output_tokens`, `total_tokens`), `error`
    (`code`, `message`); API failures become error responses rather than Go-level errors
- **`call(ctx, client, request)` VCL function** — synchronous call to a `CallableClient`; request
  supports `messages`, `system` shorthand (prepended as `role = "system"`), and per-call overrides
  for `model`, `max_tokens`, and `temperature`
- **`llm::wrap(content)` VCL function** — wraps a string in `<user_input>…</user_input>` XML-like
  delimiters as a structural prompt injection mitigation; the system prompt should reference the
  tags to signal to the model where untrusted input begins and ends
- Documentation: `doc/client-llm.md` with full reference, provider table, Security section,
  and examples; `doc/config.md` and `doc/functions.md` updated

### Changed

- **`Client` interface refactored** into a clean hierarchy to support non-bus clients:
  - `Client` — base identity interface (`GetName`, `GetDefRange`)
  - `BusClient` — extends `Client` with bus plumbing (`Build`, `GetClient`, `GetSubscriber`,
    `SetSubscriber`); used by `VinculumWebsocketClient`
  - `Callable` — standalone call/response capability (`Call(ctx, request) → response`)
  - `CallableClient` — combines `Client` + `Callable`; implemented by `OpenAIClient`
  - `BaseBusClient` embeds `BaseClient` and holds the shared bus fields previously scattered
    across `VinculumWebsocketClient`

## [0.13.0] - 2026-03-22

### Added

- **Prometheus/OpenMetrics metrics support** — new `metric` block and `server "metrics"` block
  - `gauge`, `counter`, and `histogram` metric types with optional label declarations
  - Metrics server exposes a `/metrics` endpoint with automatic content negotiation between
    Prometheus text format and OpenMetrics (via `promhttp.HandlerFor`)
  - Can run standalone (own port) or be mounted into an HTTP server via `handle "/metrics" { handler = server.metrics }`
  - Go runtime and process collectors registered automatically
  - Internal `promadapter` package wires `prometheus.Registry` to the `o11y.MetricsProvider` interface,
    enabling bus and WebSocket server instrumentation
- **`observe()` VCL function** — records a value to a histogram metric
- **Labeled metric support in VCL** — `get`, `set`, `increment`, and `observe` all accept an optional
  labels object as a final argument (e.g. `increment(metric.hits, 1, {queue = ctx.msg.queue})`)
- **Random VCL functions** — seven new functions in the `random` category:
  - `random(n)` — random integer in [0, n)
  - `randint(lo, hi)` — random integer in [lo, hi]
  - `randuniform(lo, hi)` — random float in [lo, hi)
  - `randgauss(mean, stddev)` — normally distributed float
  - `randchoice(list)` — uniformly random element from a list
  - `randsample(list, n)` — random sample of n elements without replacement
  - `randshuffle(list)` — shuffled copy of a list
- Documentation: `doc/metric.md` and `doc/server-metrics.md` added; `doc/functions.md` expanded
  with Random and Variables & Metrics sections

### Changed

- `get`, `set`, and `increment` VCL functions now dispatch on `MetricCapsuleType` in addition to
  `VariableCapsuleType`, supporting labeled metric series alongside plain variables

## [0.12.0] - 2026-03-22

### Added

- Block dependency DAG and topological sort — configuration blocks are now processed in dependency
  order, enabling forward references and correct initialization sequencing (e.g. a `server` block
  is always started before `metric` or `subscription` blocks that reference it)

## [0.11.0] - 2026-03-21

### Added

- `var` block with `get`, `set`, and `increment` VCL functions for mutable in-process state

### Changed

- Switched back to upstream `github.com/amir-yaghoubi/mqttpattern` after our changes were accepted

[Unreleased]: https://github.com/tsarna/vinculum/compare/v0.45.1...HEAD
[0.45.1]: https://github.com/tsarna/vinculum/compare/v0.45.0...v0.45.1
[0.45.0]: https://github.com/tsarna/vinculum/compare/v0.44.0...v0.45.0
[0.44.0]: https://github.com/tsarna/vinculum/compare/v0.43.0...v0.44.0
[0.43.0]: https://github.com/tsarna/vinculum/compare/v0.42.0...v0.43.0
[0.42.0]: https://github.com/tsarna/vinculum/compare/v0.41.0...v0.42.0
[0.41.0]: https://github.com/tsarna/vinculum/compare/v0.40.0...v0.41.0
[0.40.0]: https://github.com/tsarna/vinculum/compare/v0.39.0...v0.40.0
[0.39.0]: https://github.com/tsarna/vinculum/compare/v0.38.1...v0.39.0
[0.38.1]: https://github.com/tsarna/vinculum/compare/v0.38.0...v0.38.1
[0.38.0]: https://github.com/tsarna/vinculum/compare/v0.37.1...v0.38.0
[0.37.1]: https://github.com/tsarna/vinculum/compare/v0.37.0...v0.37.1
[0.37.0]: https://github.com/tsarna/vinculum/compare/v0.36.0...v0.37.0
[0.36.0]: https://github.com/tsarna/vinculum/compare/v0.35.0...v0.36.0
[0.35.0]: https://github.com/tsarna/vinculum/compare/v0.34.0...v0.35.0
[0.34.0]: https://github.com/tsarna/vinculum/compare/v0.33.0...v0.34.0
[0.33.0]: https://github.com/tsarna/vinculum/compare/v0.32.0...v0.33.0
[0.32.0]: https://github.com/tsarna/vinculum/compare/v0.31.0...v0.32.0
[0.31.0]: https://github.com/tsarna/vinculum/compare/v0.30.0...v0.31.0
[0.30.0]: https://github.com/tsarna/vinculum/compare/v0.29.0...v0.30.0
[0.29.0]: https://github.com/tsarna/vinculum/compare/v0.28.0...v0.29.0
[0.28.0]: https://github.com/tsarna/vinculum/compare/v0.27.0...v0.28.0
[0.27.0]: https://github.com/tsarna/vinculum/compare/v0.26.0...v0.27.0
[0.26.0]: https://github.com/tsarna/vinculum/compare/v0.25.0...v0.26.0
[0.25.0]: https://github.com/tsarna/vinculum/compare/v0.24.0...v0.25.0
[0.24.0]: https://github.com/tsarna/vinculum/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/tsarna/vinculum/compare/v0.22.0...v0.23.0
[0.22.0]: https://github.com/tsarna/vinculum/compare/v0.21.0...v0.22.0
[0.21.0]: https://github.com/tsarna/vinculum/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/tsarna/vinculum/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/tsarna/vinculum/compare/v0.18.0...v0.19.0
[0.18.0]: https://github.com/tsarna/vinculum/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/tsarna/vinculum/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/tsarna/vinculum/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/tsarna/vinculum/compare/v0.14.1...v0.15.0
[0.14.1]: https://github.com/tsarna/vinculum/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/tsarna/vinculum/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/tsarna/vinculum/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/tsarna/vinculum/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/tsarna/vinculum/compare/v0.10.0...v0.11.0
