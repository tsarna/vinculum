# Vinculum — Claude Session Guide

## What is Vinculum?

Vinculum is a no-code/low-code protocol bridging and message routing system
configured via files in HashiCorp Configuration Language (HCL). Think of it as a
Swiss Army Knife for connecting systems: a few lines of config can bridge HTTP,
WebSockets, MQTT-style pub/sub, cron scheduling, and more.

Configuration files use the `.vcl` extension (Vinculum Configuration Language).
A separate `.vinit` file format is processed before any `.vcl` to bootstrap the
runtime — currently used to load Go shared-object plugins (`.so`).

---

## Documentation

User-facing documentation lives in `doc/`. Each file covers one topic:

| File | Contents |
|---|---|
| `doc/overview.md` | Introduction, concepts, table of contents |
| `doc/config.md` | HCL syntax, variables, all block types (`bus`, `const`, `cron`, `function`, `jq`, `server`, `signals`, `subscription`) |
| `doc/condition.md` | `condition` block subtypes (`timer`, `threshold`, `counter`, `flipflop`), four-state model, common attributes, lifecycle hooks |
| `doc/functions.md` | Built-in callable functions (logging, messaging, data, MCP, file, HTTP) |
| `doc/functy.md` | functy (`.cty`) language: functions, typed locals, control flow, namespaces, types, errors |
| `doc/procedure.md` | `procedure` block (**deprecated** in favor of functy) |
| `doc/transforms.md` | Message transform pipeline DSL (`add_topic_prefix`, `jq`, `chain`, etc.) |
| `doc/editor.md` | `editor` block: structured-text editing |
| `doc/fsm.md` | `fsm` block: state machines |
| `doc/trigger.md` | `trigger` block types (`cron`, `at`, `after`, `interval`, `once`, `watch`, `watchdog`, `signals`, `start`, `shutdown`, `file`) |
| `doc/metric.md` | `metric` block and metric types |
| `doc/baggage.md` | OTel `ctx.baggage`: read/write/delete, secure-by-default `baggage {}` trust filtering, `record_baggage` span projection |
| `doc/server-http.md` | `server "http"`: handle/files blocks, context vars, request functions |
| `doc/server-mcp.md` | `server "mcp"`: resources, tools, prompts, MCP functions |
| `doc/server-vws.md` | `server "vws"` and `client "vws"`: VWS protocol, allow_send, reconnect |
| `doc/server-websocket.md` | `server "websocket"`: simple raw WebSocket push server |
| `doc/server-metrics.md` | `server "metrics"`: Prometheus-style metrics endpoint |
| `doc/server-auth.md` | Shared HTTP auth (`basic`, `oauth2`, `oidc`, `custom`) |
| `doc/client-*.md` | Per-client references: `http`, `mqtt`, `kafka`, `rabbitmq`, `redis`, `sns`, `sqs`, `sql`, `llm`, `otlp` |
| `doc/deprecations.md` | Deprecated features, replacements, and planned removal |
| `doc/vinit.md` | `.vinit` bootstrap file format, two-pass discovery, minimal eval context, `disabled` |
| `doc/plugins.md` | `plugin` block, `--plugin-path`, ABI rules, container deployment |
| `doc/git.md` | `git` block: clone-and-materialize, HTTP/SSH auth, revision pinning, destination ownership |
| `doc/repl.md` | Interactive REPL (`serve -i`): live expression eval, result history, session bindings, meta-commands, log control |
| `doc/container.md` | Published Docker images: `vinculum`, `vinculum:*-minimal`, `vinculum-build` |

---

## Repository Layout

```
cmd/            CLI commands (serve, publish, subscribe, check, plugins, version)
config/         Core config parsing, block registries, and shared block impls
  blocks.go     BlockHandler interface + registry (GetBlockHandlers)
  server.go     Listener/Startable/HandlerServer interfaces, BaseServer,
                RegisterServerType registry + ServerBlockHandler dispatch
  client.go     Client interface, RegisterClientType registry + ClientBlockHandler dispatch
  trigger.go    Trigger interface + RegisterTriggerType registry
  condition.go  Condition subtype registry (RegisterConditionSubtype)
  bus.go        bus block
  subs.go       subscription block
  config.go     Config and ConfigBuilder types
  dep.go        dependency graph + topological sort
  parse.go      HCL file/directory/bytes parsing — `.vcl` and `.vinit` extensions
  transforms.go message transform functions
  transformplugin.go    RegisterTransformPlugin + collision detection
  vinit.go      .vinit pass-1 orchestration, minimal eval context, plugin + git collection
  git.go        git block decode structs + static validation (processGitBlock)
  gitfetch.go   go-git clone/checkout + HTTP/SSH auth construction
  gitmaterialize.go     subtree copy + destination ownership materialization
  plugin_common.go      PluginContext + entry-point type (all platforms)
  plugin.go     Plugin loader (linux/darwin/freebsd build tag)
  plugin_unsupported.go Plugin loader stub (other platforms)
  tls.go        shared TLS sub-block (TLSConfig)
  fsm.go reactive.go procedure.go wireformat*.go auth.go ...  other block impls
  testdata/     .vcl fixtures (incl. testdata/plugins/ for integration test)
servers/        Server implementations (each registers via RegisterServerType in init())
  http/         server "http"
  vws/          server "vws"
  websocket/    server "websocket" (low-level helper + config)
  mcp/          server "mcp" — MCP protocol (server.go, resources.go, tools.go,
                prompts.go, context.go, schema.go, auth.go)
  metrics/      server "metrics"
  auth/         shared HTTP auth middleware (basic, oauth2, oidc, custom)
clients/        Client implementations (each registers via RegisterClientType in init())
                http/ vws/ mqtt/ kafka/ rabbitmq/ redis*/ sns/ sqs/ aws/ sql/ llm/ openai/ otlp/
triggers/       Trigger implementations: cron, at, after, interval, once, watch,
                watchdog, signals, start, shutdown, file
conditions/     Condition subtypes (threshold, counter, timer, state, hooks)
procedure/      Procedure compiler/interpreter (ir, scope, signal, spec)
editors/        Editor implementations (line)
ambient/        Ambient providers (env, sys, httpstatus, boottime)
functions/      Built-in HCL functions (log, stdlib, jq, diff, mcp_*, http, etc.)
repl/           Interactive REPL (serve -i)
hclutil/        Shared HCL helpers (ContextObjectBuilder, capsule/ctx/auth/env/tracing)
internal/       Internal-only helpers
types/          Rich object/capsule types (httprequest, httpresponse, metric, variable)
transform/      Message transform pipeline types
platform/       OS signal handling
version/        Build version info
specs/          Design specs
doc/            User-facing documentation
```

---

## Key Dependency Map

| Package | Purpose |
|---|---|
| `github.com/hashicorp/hcl/v2` | HCL parsing |
| `github.com/hashicorp/hcl/v2/gohcl` | Decode HCL bodies into Go structs |
| `github.com/zclconf/go-cty` | Type system for HCL expression evaluation |
| `github.com/hashicorp/go-cty-funcs` | Standard library functions for cty |
| `github.com/tsarna/vinculum-bus` | Core event bus (MQTT-style pub/sub) |
| `github.com/tsarna/vinculum-vws` | Vinculum WebSocket protocol |
| `github.com/tsarna/hcl-jqfunc` | JQ function support in HCL |
| `github.com/tsarna/go-structdiff` | Structural diff for the `diff()` function |
| `github.com/itchyny/gojq` | JQ query engine |
| `github.com/robfig/cron/v3` | Cron scheduling |
| `github.com/heimdalr/dag` | DAG for dependency sorting |
| `go.uber.org/zap` | Structured logging |
| `github.com/spf13/cobra` | CLI |

### Related repos (also in `~/src/`)
- `vinculum-bus` — the event bus library
- `vinculum-vws` — the VWS WebSocket protocol library
- `hcl-jqfunc` — HCL JQ function integration
- `go-structdiff` — structural diff used by the `diff()` function

---

## Architecture: Config Processing Pipeline

1. **Parse** — HCL files/directories parsed into `*hcl.File` ASTs (`parse.go`)
2. **Extract functions** — user-defined `function` and `jq` blocks extracted first
3. **Build eval context** — stdlib + user functions assembled into `*hcl.EvalContext`
4. **Preprocess blocks** — each `BlockHandler.Preprocess()` called (dep ID extraction)
5. **Topological sort** — blocks ordered by declared dependencies (`dep.go`)
6. **Process blocks** — each `BlockHandler.Process()` called in dependency order
7. **Finish** — `FinishProcessing()` called on each handler

The result is a `*Config` containing maps of buses, servers, clients, crons, and a
list of `Startable` items that are started in order when the server runs.

### BlockHandler interface (`config/blocks.go`)

```go
type BlockHandler interface {
    Preprocess(block *hcl.Block) hcl.Diagnostics
    FinishPreprocessing(config *Config) hcl.Diagnostics
    GetBlockDependencyId(block *hcl.Block) (string, hcl.Diagnostics)
    GetBlockDependencies(block *hcl.Block) ([]string, hcl.Diagnostics)
    Process(config *Config, block *hcl.Block) hcl.Diagnostics
    FinishProcessing(config *Config) hcl.Diagnostics
}
```

`BlockHandlerBase` provides no-op defaults for all methods. Embed it and override
only what you need.

### Registering a new top-level block type

Add to `GetBlockHandlers()` in `config/blocks.go` and to `blockSchema` in the same
file.

---

## Architecture: Servers

### Interfaces

```go
type Listener interface {
    GetName() string
    GetDefRange() hcl.Range
}

type Startable interface {
    Start() error   // launched in a goroutine by the server command
}

type HandlerServer interface {
    Listener
    GetHandler() http.Handler   // allows mounting under server "http"
}
```

`BaseServer` provides `GetName()` and `GetDefRange()`. Embed it in every server
struct.

### Adding a new server type

1. Create a package `servers/<type>/` (e.g., `servers/mcp/`).
2. Define a definition struct decoded by `gohcl`:
   ```go
   type MyServerDefinition struct {
       Listen   string    `hcl:"listen"`
       Disabled bool      `hcl:"disabled,optional"`
       DefRange hcl.Range `hcl:",def_range"`
       // hcl:",remain" for sub-blocks
   }
   ```
3. Define a server struct embedding `cfg.BaseServer`. Implement `Startable` if it
   runs a goroutine; implement `HandlerServer` if it can be mounted under `server "http"`.
4. Write `ProcessMyServerBlock(config *cfg.Config, block *hcl.Block, body hcl.Body) (cfg.Listener, hcl.Diagnostics)`.
5. Register it from an `init()`: `cfg.RegisterServerType("mytype", ProcessMyServerBlock)`.
   `ServerBlockHandler.Process()` in `config/server.go` dispatches through that registry.

Servers are stored in `config.Servers["type"]["name"]` and exposed to HCL
expressions as `server.<name>` (a cty capsule value).

### Servers as Subscribers

A server can implement `bus.Subscriber` from vinculum-bus to receive messages sent
to it via `send(ctx, server.myserver, topic, payload)` or a `subscription` block
with `subscriber = server.myserver`. See `servers/vws/` and `servers/websocket/`
for examples.

---

## Architecture: Clients

Clients connect to external services. They follow the same pattern as servers
(one package per type under `clients/<type>/`):

- Implement the `Client` interface
- Stored in `config.Clients["type"]["name"]`
- Exposed as `client.<name>` in HCL expressions
- Register from an `init()`: `cfg.RegisterClientType("mytype", ProcessMyClientBlock)`.
  `ClientBlockHandler.Process()` in `config/client.go` dispatches through that registry.

### Shared sub-blocks

Reusable sub-block definitions live in `config/` and are embedded by client (and
server) implementations rather than duplicated per protocol:

| File | Struct | Purpose |
|---|---|---|
| `config/tls.go` | `TLSConfig` | TLS/mTLS config; provides `TLSClientConfig() (*tls.Config, error)` |

Add to this table as new shared sub-blocks are defined.

---

## Architecture: Bootstrap and Plugins

`.vinit` files are processed in a "pass 1" before any `.vcl` parsing. The
pass is invoked at the very top of `ConfigBuilder.Build()`
(`config/config.go`), before `ParseConfigFiles` and before ambient
population, so plugin-registered contributions are visible to the rest of
`Build()`.

### Pipeline

1. `Build()` calls `processVinit(sources, pluginPath, logger)`
   (`config/vinit.go`).
2. `ParseVinitFiles` walks the sources, filtering on `.vinit` extension.
   It explicitly **skips `[]byte` and `[]string`** sources so test fixtures
   that pass VCL content as bytes don't get treated as vinit.
3. Each body is decoded against `vinitSchema` (closed schema:
   `plugin "<label>"` and `git "<label>"`); unknown block types are fatal.
   The single schema walk collects `plugin` and `git` blocks into separate
   slices.
4. For each `plugin` block: label is regex-validated, `disabled` is
   evaluated against the minimal eval context (`env.*` + cty stdlib —
   no const, no user functions, no plugin contributions), duplicates
   are detected, and `loadPlugin` is invoked.
5. `loadPlugin` (`config/plugin.go`, build-tagged
   linux/darwin/freebsd) does `plugin.Open` → symbol lookup →
   panic-recovered `VinculumPluginInit(*PluginContext) hcl.Diagnostics`
   invocation.
6. **After all plugin blocks** (so plugin registrations are available
   first), `git` blocks are processed in source order via
   `processGitBlock` (`config/git.go`), each with its own duplicate-label
   namespace. The git label carries no filesystem meaning, so it is *not*
   regex-restricted. A non-disabled block is statically validated
   (revision mutual-exclusion, fetch presence, `from` repo-escape, auth
   consistency with the inferred transport) then cloned with go-git
   (`config/gitfetch.go`) into a temp workdir and its subtrees copied to
   their destinations (`config/gitmaterialize.go`). The feature is pure Go
   (`CGO_ENABLED=0`-clean) so it works in the scratch minimal image —
   unlike plugins, which need a dynamically linked host.

### Minimal `.vinit` eval context

Built in `vinitEvalContext()` in `config/vinit.go`:

- `env.<NAME>` — via `hclutil.EnvObject()` (the canonical env-to-cty
  helper, shared with the `ambient` package's `env` provider).
- Stdlib functions — looked up by name (`"stdlib"`) in the
  `functionPlugins` registry. The stdlib FunctionPlugin's getter ignores
  its `*Config` argument so passing `nil` is safe.

### What plugins can register

Plugins call the same `Register*` functions as in-tree subsystems:
`RegisterFunctionPlugin`, `RegisterTransformPlugin`,
`RegisterAmbientProvider`, `RegisterServerType`, `RegisterClientType`,
`RegisterTriggerType`, `RegisterConditionalTriggerType`,
`RegisterConditionSubtype`, `RegisterWireFormatType`,
`RegisterEditorType`, and the functy hooks `RegisterFunctyType` /
`RegisterFunctyOpenType` / `RegisterFunctyExterns` (`config/functy.go` —
the same path leaf packages use to make their capsule and rich-object
types nameable in `.cty` annotations, and to declare real signatures for
`help()`). Adding entirely new top-level `.vcl` block types is not
supported.

`RegisteredPlugins()` is a query, not a contribution point. The
[vinculum-plugin-example](https://github.com/tsarna/vinculum-plugin-example)
README carries the same list in table form; keep the two in sync.

### `RegisterTransformPlugin` + collision check

Transform plugins are merged into the transform-only eval context at
`Build()` time, not on every `transforms = [...]` evaluation. The
single source of truth for built-in transform names is
`(*Config).builtinTransforms()` in `config/transforms.go`;
`buildTransformPluginFunctions()` in `config/transformplugin.go`
derives the built-in name set from that map's keys, so adding a new
built-in transform automatically extends the collision check.

### Cross-platform PluginContext

`PluginContext` lives in `config/plugin_common.go` with no build tag so
plugin author code referencing `config.PluginContext` compiles on every
platform. Only the *loader* is platform-gated.

### Testing notes

- `package config` (internal) tests use `withCleanTransformPlugins(t)`
  in `config/transformplugin_test.go` to save/restore the process-global
  `transformPlugins` slice across tests.
- The integration test (`config/plugin_integration_test.go`, build tag
  `integration`) exec's a real `go build`-produced `vinculum` binary
  rather than loading the `.so` into the test binary itself — Go's
  plugin loader rejects `.so` files built without `go test`'s
  test-mode instrumentation as "different version of package X". The
  workaround mirrors how production loads plugins.

---

## VCL Language Reference

### Block types

```hcl
assert "name" { condition = expr }
bus "name" { queue_size = 1000 }
client "type" "name" { ... }
condition "type" "name" { ... }
const { name = expr; ... }
editor "type" "name" { ... }
fsm "name" { ... }
function "name" { params = [a, b]; result = expr }
jq "name" { params = [a]; query = ".field" }
metric "type" "name" { ... }
procedure "name" { spec { ... }; ...; return = expr }
server "type" "name" { ... }
subscription "name" { target = bus.x; topics = [...]; action = expr }
trigger "type" "name" { ... }   # incl. trigger "cron", trigger "signals", trigger "at", ...
var "name" { value = expr }     # mutable; exposed as var.<name>
wire_format "type" "name" { ... }
```

`cron` and `signals` are **not** top-level blocks; they are trigger types
(`trigger "cron" "name"`, `trigger "signals" "name"`). `function`, `jq`, `editor`,
and `procedure` are function-definition blocks extracted early in `Build()`
(before general block processing); the rest are processed via `GetBlockHandlers()`.

### Built-in variables

| Variable | Description |
|---|---|
| `bus.<name>` | Event bus (capsule) |
| `server.<name>` | Server (capsule) |
| `client.<name>` | Client (capsule) |
| `env.<NAME>` | Environment variable |
| `http_status.OK` etc. | HTTP status code constants |
| `ctx` | Handler-specific context (varies) |

### action = expressions

The `action` attribute accepts an HCL expression or a list. Lists are evaluated in
order; the **last value** is the result. Earlier values are evaluated for side
effects (logging, sending messages, etc.).

```hcl
action = [
    log::info("doing thing", {key = val}),
    send(ctx, bus.main, "topic", "payload"),
    "final return value",
]
```

### Transforms

Subscription `transforms` are a list of transform function values applied in order
to each message before delivery. The transform functions (`add_topic_prefix`,
`replace_in_topic`, `jq`, etc.) form a **DSL for declaring a transformation
chain** — they are not part of the general VCL expression language. They are only
present in the eval context for attributes that expect a transform list, and must
not be added to the general function registry.

```hcl
transforms = [
    add_topic_prefix("out/"),
    drop_topic_prefix("in/"),
    if_topic_prefix("foo/", add_topic_prefix("bar/")),
    jq(".payload"),
    diff(),
    stop(),
]
```

---

## Conventions

### Rich Object Types (`_capsule` convention)

Some VCL types are "rich objects" — `cty.Object` values that expose named attributes
directly (e.g. `u.scheme`, `b.content_type`) **and** carry an underlying Go capsule
in a `_capsule` attribute for interface dispatch. This mirrors the `_ctx` convention
for contexts.

**Existing rich object types:**

| VCL Type | Object type var | Capsule type var | Source |
|---|---|---|---|
| `bytes` | `bytescty.BytesObjectType` | `bytescty.BytesCapsuleType` | [github.com/tsarna/bytes-cty-type](https://github.com/tsarna/bytes-cty-type) (wired in `functions/bytes.go`) |
| URL object | `urlcty.URLObjectType` | `urlcty.URLCapsuleType` | [github.com/tsarna/url-cty-funcs](https://github.com/tsarna/url-cty-funcs) |

**How to implement a new rich object type:**

1. Define the Go struct (e.g. `type Foo struct { ... }`).
2. Define `FooCapsuleType` via `cty.CapsuleWithOps(...)`.
3. Define `FooObjectType = cty.Object(map[string]cty.Type{ ..., "_capsule": FooCapsuleType })`.
4. Write `BuildFooObject(...)` returning the object with all fields populated.
5. Write `GetFooFromValue(val cty.Value) (*Foo, error)` — delegates to
   `ctyutil.GetCapsuleFromValue(val)` then type-asserts.
6. Implement `types.Stringable` (`ToString`) and/or `types.Lengthable` (`Length`) on
   the Go struct pointer if you want `tostring()` / `length()` to dispatch on it.
7. Functions that **produce** the type return `FooObjectType`; functions that **consume**
   it call `GetFooFromValue`, which accepts a raw capsule, an object, or a string where
   applicable.

**`ctyutil.GetCapsuleFromValue`** is the shared extractor: it accepts a capsule
directly or an object with a `_capsule` attribute, and returns the raw `interface{}`
for type-asserting to an interface. All `extract*` helpers in `functions/generic.go`
use it, enabling `get()`, `set()`, `tostring()`, `length()`, etc. to work on both
raw capsules and rich objects transparently.

**`tostring()` and `length()` dispatch** (`functions/generic.go`): these enhanced
versions call `GetCapsuleFromValue` first; if the result implements `Stringable` /
`Lengthable`, that method is called. Otherwise they fall back to the stdlib
implementation. They are registered in the `generic` plugin, **not** `stdlib`
(stdlib's versions were removed to avoid override conflicts — alphabetical `init()`
order would make stdlib win).

### System bus topics

System-generated/internal topics are prefixed with `$`. Do not use `$`-prefixed
topics in user config. Examples:
- `$metrics` — event bus metrics (from vinculum-bus)
- `$server/mcp/<name>/reply/<uuid>` — MCP server reply topics (planned)

### HCL struct tags

```go
hcl:"fieldname"           // required attribute
hcl:"fieldname,optional"  // optional attribute
hcl:"fieldname,label"     // block label
hcl:",def_range"          // captures the block's definition range (hcl.Range)
hcl:",remain"             // captures remaining undecoded body (hcl.Body)
hcl:"blockname,block"     // nested block (single)
hcl:"blockname,block"     // nested blocks (slice)
```

### Error reporting

Always return `hcl.Diagnostics` with a meaningful `Summary`, `Detail`, and
`Subject` (pointing to the relevant source range). Never panic or log-and-continue
for config errors.

### Runtime logging: `Logger` vs `UserLogger`

`Config` exposes two zap loggers. Pick based on the cause of the message, not the
log level:

- **`config.Logger`** — operational/infrastructure events where a Go caller and
  stacktrace help diagnose an internal bug (e.g. fsnotify watcher failure, HTTP
  listen error, startup/shutdown lifecycle).
- **`config.UserLogger`** — anything caused by the user's VCL: expression eval
  errors, action errors, assertion failures, condition-expression type mismatches,
  FSM/lifecycle hook failures, `skip_when`/`stop_when` errors, etc. Caller and
  stacktrace are suppressed because the Go source location is always the generic
  eval plumbing and carries no signal about the VCL site.

`SignalActionHandler` mirrors this with its own `Logger` / `UserLogger` fields.

The VCL `log_*` functions already derive their own caller/stack-suppressed logger
inside `functions/log.go` — callers of `GetLogFunctions` pass the normal logger.

### Disabled flag

All server and client blocks support `disabled = true` (optional, default false).
Check it early in `Process()` and return nil if set, before doing any real work.

### Dependency declarations

Blocks that reference other blocks (e.g., a server referencing a bus) should
declare that dependency in `GetBlockDependencies()` so the topological sort runs
them in the right order.

### on_connect / on_disconnect for client blocks

Every `client "type"` block should support optional `on_connect` and
`on_disconnect` HCL expressions where it makes sense (i.e. the client has a
meaningful connected/disconnected lifecycle).

**Semantics:**

- `on_connect` — evaluated **synchronously** after the connection is established
  and ready. No messages are produced or consumed until it returns.
- `on_disconnect` — evaluated **before any reconnection attempt** (hard
  guarantee). On graceful shutdown, also evaluated before the connection is torn
  down (best-effort — network failures cannot give advance notice).
- `on_connect` / `on_disconnect` pairs bracket each connection session: every
  `on_connect` is preceded by an `on_disconnect` (except the very first connect).

**Evaluation context:** standard VCL context (`ctx`, `bus.*`, `send()`, etc.).
No message variables (`topic`, `msg`, `fields`) — there is no message in flight.

**Typical uses:** publish a lifecycle event to a bus, trigger a notification,
log to an external system.

---

## In-Progress Work

### MCP Server

The MCP (Model Context Protocol) server MVP is partially implemented in `servers/mcp/`.
See `MCP-SPEC.md` (full spec) and `MCP-MVP.md` (MVP scope) for details.

**What's implemented (MVP Phase 0–3):**
- `server "mcp" "name"` block parsed in `servers/mcp/`, dispatched via `ServerBlockHandler`
- Streamable HTTP transport using `github.com/modelcontextprotocol/go-sdk`
- Static and URI-template resources with action-based handlers
- Tools with typed params and action-based handlers
- Prompts with params and action-based handlers
- `mcp::image()`, `mcp::error()`, `mcp::user_message()`, `mcp::assistant_message()` — available globally in all action expressions (not just MCP handlers)
- Unit tests for all three handler types using in-process SDK client
- Observability (`servers/mcp/observability.go`): OTel tracing + metrics
  conforming to the GenAI/MCP semantic conventions. An SDK receiving middleware
  (`Server.AddReceivingMiddleware`) emits an `mcp.server` span and the
  `mcp.server.operation.duration` histogram per inbound request/notification —
  transport-independent, so identical in standalone and HTTP-mounted modes. The
  HTTP transport itself is separately wrapped with `otelhttp`. `tracing` /
  `metrics` block attributes resolve like `server "http"`. Session-duration
  metrics are deferred (no SDK session-disconnect hook in go-sdk v1.6.1).

**Key design decisions:**
- `mcp_*` functions live in `functions/mcp.go` and are included in `GetFunctions()` so bus subscriptions and other handlers can construct MCP values for future async support
- `ctx` is the only top-level variable in every action eval context (consistent with all other vinculum handlers); MCP-specific data is nested under it (`ctx.uri`, `ctx.args.expression`, etc.)
- Reply topics use format `$server/mcp/<server-name>/reply/<uuid>` (for future async)
- OAuth2 via external OIDC issuer only (no built-in auth server)

**Not yet implemented (deferred from MVP):**
- Bus-based async handlers
- `mcp_progress()` (depends on async)
- Notifications (`mcp_notify_*()`)
- OAuth2
- Pagination (`mcp_text_page()`)
- Complex param types (array/object with nested JSON schema)
- Mounting under `server "http"` block

---

## Testing

Tests live alongside the code (`*_test.go`) and use `config/testdata/*.vcl` as
fixtures. Run with:

```
go test ./...
```

The `ConfigBuilder` API is the entry point for test configs:

```go
config, diags := NewConfig().
    WithLogger(logger).
    WithSources("testdata/mytest.vcl").
    Build()
```
