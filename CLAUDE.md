# Vinculum — Claude Session Guide

## What is Vinculum?

Vinculum is a no-code/low-code protocol bridging and message routing system
configured via files in HashiCorp Configuration Language (HCL). Think of it as a
Swiss Army Knife for connecting systems: a few lines of config can bridge HTTP,
WebSockets, MQTT-style pub/sub, cron scheduling, and more.

Configuration files use the `.vcl` extension (Vinculum Configuration Language).

---

## Documentation

User-facing documentation lives in `doc/`. Each file covers one topic:

| File | Contents |
|---|---|
| `doc/overview.md` | Introduction, concepts, table of contents |
| `doc/config.md` | HCL syntax, variables, all block types (`bus`, `const`, `cron`, `function`, `jq`, `server`, `signals`, `subscription`) |
| `doc/functions.md` | Built-in callable functions (logging, messaging, data, MCP, file, HTTP) |
| `doc/transforms.md` | Message transform pipeline DSL (`add_topic_prefix`, `jq`, `chain`, etc.) |
| `doc/server-http.md` | `server "http"`: handle/files blocks, context vars, request functions |
| `doc/server-mcp.md` | `server "mcp"`: resources, tools, prompts, MCP functions |
| `doc/server-vws.md` | `server "vws"` and `client "vws"`: VWS protocol, allow_send, reconnect |
| `doc/server-websocket.md` | `server "websocket"`: simple raw WebSocket push server |

---

## Repository Layout

```
cmd/            CLI commands (server, publish, subscribe)
config/         Core configuration parsing and all block implementations
  blocks.go     BlockHandler interface + registry (GetBlockHandlers)
  server.go     Listener interface, BaseServer, ServerBlockHandler dispatch
  http.go       server "http" implementation
  vws.go        server "vws" and client "vws" implementations
  websockets.go server "websocket" implementation
  bus.go        bus block
  subs.go       subscription block
  cron.go       cron block
  ctx.go        evaluation context builders (per handler type)
  dep.go        dependency graph + topological sort
  config.go     Config and ConfigBuilder types
  parse.go      HCL file/directory/bytes parsing
  transforms.go message transform functions
  testdata/     .vcl fixtures for tests
functions/      Built-in HCL functions (log, stdlib, jq, diff, mcp_*, etc.)
internal/
  hclutil/      Shared HCL helpers (ContextObjectBuilder, capsule utilities)
mcp/            MCP server implementation (server "mcp" block)
  server.go     Server struct, New(), Start(), GetHandler()
  resources.go  Resource + ResourceTemplate registration and handlers
  tools.go      Tool registration and handlers
  prompts.go    Prompt registration and handlers
  context.go    Per-request eval context builders for each handler type
  schema.go     JSON schema generation for tool input params
  testdata/     .vcl fixtures for mcp tests
platform/       OS signal handling
transform/      Message transform pipeline types
websockets/     Low-level WebSocket server helper
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

1. Create a file `config/<type>.go` (e.g., `config/mcp.go`).
2. Define a definition struct decoded by `gohcl`:
   ```go
   type MyServerDefinition struct {
       Listen   string    `hcl:"listen"`
       Disabled bool      `hcl:"disabled,optional"`
       DefRange hcl.Range `hcl:",def_range"`
       // hcl:",remain" for sub-blocks
   }
   ```
3. Define a server struct embedding `BaseServer`. Implement `Startable` if it runs
   a goroutine; implement `HandlerServer` if it can be mounted under `server "http"`.
4. Write `ProcessMyServerBlock(config *Config, block *hcl.Block, body hcl.Body) (Listener, hcl.Diagnostics)`.
5. Add a `case "mytype":` to the switch in `ServerBlockHandler.Process()` in
   `config/server.go`.

Servers are stored in `config.Servers["type"]["name"]` and exposed to HCL
expressions as `server.<name>` (a cty capsule value).

### Servers as Subscribers

A server can implement `bus.Subscriber` from vinculum-bus to receive messages sent
to it via `send(ctx, server.myserver, topic, payload)` or a `subscription` block
with `subscriber = server.myserver`. See `vws.go` and `websockets.go` for examples.

---

## Architecture: Clients

Clients connect to external services. They follow the same pattern as servers:

- Implement the `Client` interface
- Stored in `config.Clients["type"]["name"]`
- Exposed as `client.<name>` in HCL expressions
- Add a `case` to `ClientBlockHandler.Process()` in `config/client.go`

---

## VCL Language Reference

### Block types

```hcl
assert "name" { condition = expr }
bus "name" { queue_size = 1000 }
client "type" "name" { ... }
const { name = expr; ... }
cron "name" { at "schedule" "label" { action = expr } }
function "name" { params = [a, b]; result = expr }
jq "name" { params = [a]; query = ".field" }
server "type" "name" { ... }
signals { SIGHUP = expr }
subscription "name" { target = bus.x; topics = [...]; action = expr }
```

### Built-in variables

| Variable | Description |
|---|---|
| `bus.<name>` | Event bus (capsule) |
| `server.<name>` | Server (capsule) |
| `client.<name>` | Client (capsule) |
| `env.<NAME>` | Environment variable |
| `httpstatus.OK` etc. | HTTP status code constants |
| `ctx` | Handler-specific context (varies) |

### action = expressions

The `action` attribute accepts an HCL expression or a list. Lists are evaluated in
order; the **last value** is the result. Earlier values are evaluated for side
effects (logging, sending messages, etc.).

```hcl
action = [
    log_info("doing thing", {key = val}),
    send(ctx, bus.main, "topic", "payload"),
    "final return value",
]
```

### Transforms

Subscription `transforms` are a list of transform function values applied in order
to each message before delivery:

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

### Disabled flag

All server and client blocks support `disabled = true` (optional, default false).
Check it early in `Process()` and return nil if set, before doing any real work.

### Dependency declarations

Blocks that reference other blocks (e.g., a server referencing a bus) should
declare that dependency in `GetBlockDependencies()` so the topological sort runs
them in the right order.

---

## In-Progress Work

### MCP Server

The MCP (Model Context Protocol) server MVP is partially implemented in `mcp/`.
See `MCP-SPEC.md` (full spec) and `MCP-MVP.md` (MVP scope) for details.

**What's implemented (MVP Phase 0–3):**
- `server "mcp" "name"` block parsed in `config/mcp.go`, dispatched via `ServerBlockHandler`
- Streamable HTTP transport using `github.com/modelcontextprotocol/go-sdk`
- Static and URI-template resources with action-based handlers
- Tools with typed params and action-based handlers
- Prompts with params and action-based handlers
- `mcp_image()`, `mcp_error()`, `mcp_user_message()`, `mcp_assistant_message()` — available globally in all action expressions (not just MCP handlers)
- Unit tests for all three handler types using in-process SDK client

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
