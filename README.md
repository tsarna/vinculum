# Vinculum

"The [vinculum is the] processing device at the core of every Borg vessel.
It interconnects the minds of all the drones."
   -- Seven of Nine (In Voyager episode "Infinite Regress")

Vinculum is a low-code system for gluing together different systems and
bridging multiple protocols. Think of it as a Swiss Army Knife for communications:
while not the best tool for demanding tasks where you'd want a purpose-built solution,
it handles a large variety of integration tasks adequately — letting you quickly
MacGyver together a solution with a few lines of configuration.

## Getting Started

Vinculum is designed to be deployed as a container. Run the published image,
mounting a directory of `.vcl` config files at `/conf` (the image's default
command is `serve … /conf`):

```sh
docker run --rm -p 8080:8080 -v "$PWD:/conf:ro" ghcr.io/tsarna/vinculum:latest
```

To try it with no setup, point it at the credential-free
[weather-mcp](examples/weather-mcp/) example, which stands up an MCP server you
can connect Claude Desktop or Claude Code to:

```sh
git clone https://github.com/tsarna/vinculum
docker run --rm -p 9000:9000 -v "$PWD/vinculum/examples/weather-mcp:/conf:ro" \
    ghcr.io/tsarna/vinculum:latest
```

See [examples/](examples/) for more (including the traffic-light intersection
modeled entirely in config), [doc/container.md](doc/container.md) for image tags
and mount points, and [doc/overview.md](doc/overview.md) for the full reference.

### Or run the binary directly

Containers are the recommended way to deploy Vinculum, but prebuilt binaries are
also published for Linux and macOS (amd64 and arm64) with every
[release](https://github.com/tsarna/vinculum/releases) — handy for local use and
development. On macOS, install with Homebrew:

```sh
brew install tsarna/tap/vinculum
```

Otherwise download an archive from the
[releases page](https://github.com/tsarna/vinculum/releases), or build from source
with `go install github.com/tsarna/vinculum@latest`. Then point it at a directory
of `.vcl` config:

```sh
vinculum serve /conf
```

The prebuilt binaries are statically linked, like the minimal image: they do
**not** support Go plugins or the cgo-based SQLite driver (PostgreSQL and MySQL,
which are pure-Go, still work). Use the container image if you need either.

### Pull configuration from git

You don't have to bake config into the image. Drop a small [`.vinit`](doc/vinit.md)
bootstrap file into `/conf` (e.g. via a Kubernetes ConfigMap) and Vinculum will
clone a pinned revision of a repository and materialize it before any `.vcl` is
parsed. For instance, this deploys the traffic-light example straight from this
repo:

```hcl
# /conf/bootstrap.vinit

git "traffic_light_example" {
    repo   = "https://github.com/tsarna/vinculum.git"
    branch = "main"

    fetch "traffic_light" {
        from      = "examples/traffic-light"
        into      = "/conf/git/traffic-light"
        overwrite = true
    }
}
```

The fetch is pure Go, so it works even in the scratch-based minimal image, which
has no `git` binary or shell. See [doc/git.md](doc/git.md) for authentication,
revision pinning, and multi-subtree fetches.

## Key Features

- **HCL Configuration** — Declarative configuration in HashiCorp Config Language (similar to Terraform), with constants, expressions, and assertions
- **Git-Sourced Configuration** — Pull configuration (and static assets, MCP resource files, etc.) from a pinned revision of a git repository at startup, instead of baking it into the image — pure-Go, so it works even in the scratch-based minimal image
- **Publish/Subscribe Messaging** — One or more event buses with MQTT-style topic routing, wildcards, and parameter extraction
- **Server Protocols** — HTTP(S), Vinculum WebSocket (VWS), plain WebSocket, Model Context Protocol (MCP), and Prometheus/OpenMetrics, all with pluggable authentication (basic, OIDC, OAuth2)
- **Client Protocols** — HTTP(S), Kafka, MQTT, RabbitMQ (AMQP 0-9-1), Redis/Valkey (pub/sub, streams, key-value), AWS SQS, AWS SNS, VWS (to other Vinculum instances), OpenAI / LLM, and OpenTelemetry (OTLP) export
- **SQL Databases** — Query PostgreSQL, MySQL, and SQLite from config via the polymorphic `get()` / `call()` functions, with named queries, positional and named parameters, and structured result objects; Postgres and MySQL are pure-Go (no cgo)
- **Triggers** — A range of trigger types for time-, event-, and lifecycle-driven actions: cron, dynamic intervals with optional jitter, absolute / dynamic times including sunrise/sunset, file-system events, OS signals, startup/shutdown, watchdogs, and watches over reactive values
- **Conditions** — Named boolean primitives with temporal rules (activate/deactivate delays, hysteresis, retentive timing, latches, cooldown, inhibit), covering IEC 61131-3 timer and counter function-block behaviors and composable into pipelines
- **State Machines** — Finite state machines with guarded transitions, reactive events, key-value storage, MQTT topic matching, and OpenTelemetry tracing; composable with conditions and watchable for reactive integration
- **Transformations and Scripting** — JQ-based message transforms, structured-text `editor` blocks, and [functy](doc/functy.md) (`.cty`) files: a small imperative language with functions, typed locals, control flow, and structured errors
- **Built-in Functions** — A large standard library covering HTTP, files, templates, time, randomness, IDs, LLMs, sunrise/sunset, geographic calculations, and more.
- **Plugins** — Load Go shared-object plugins (`.so`) at startup to extend Vinculum with custom functions, transforms, server/client/trigger types, and more — using the same registration points as in-tree subsystems
- **Observability** — Context propagation, OpenTelemetry tracing and metrics, and Prometheus support baked in and trivial to configure.
- **Interactive REPL** — `serve -i` drops into a prompt that evaluates VCL expressions against the live, running configuration — read state, call functions, and send messages to real buses, with result history and tab completion

## Documentation

See [doc/overview.md](doc/overview.md) for full documentation including:
- Core concepts (buses, topics, subscribers, transformations)
- Configuration language reference
- Protocol details
- Examples

## Examples

The [examples/](examples/) directory contains complete working configurations.
See [examples/README.md](examples/README.md) for the index, including a dynamic
DNS zone updater that combines an HTTP server, basic authentication, a
[functy](doc/functy.md) function, and a line-editor block to safely rewrite BIND
zone files.

## Related Projects

Vinculum is built on top of several standalone Go libraries that can be used independently:

| Project | Description |
| ------- | ----------- |
| [vinculum-bus](https://github.com/tsarna/vinculum-bus) | High-performance in-process EventBus with MQTT-style topic patterns and optional OpenTelemetry observability |
| [vinculum-fsm](https://github.com/tsarna/vinculum-fsm) | cty-native finite state machine library with guarded transitions, storage, topic matching, and tracing |
| [vinculum-kafka](https://github.com/tsarna/vinculum-kafka) | Kafka producer/consumer adapters that plug into a vinculum-bus EventBus |
| [vinculum-mqtt](https://github.com/tsarna/vinculum-mqtt) | MQTT publisher/subscriber adapters that plug into a vinculum-bus EventBus |
| [vinculum-rabbitmq](https://github.com/tsarna/vinculum-rabbitmq) | RabbitMQ (AMQP 0-9-1) sender/receiver adapters that plug into a vinculum-bus EventBus |
| [vinculum-redis](https://github.com/tsarna/vinculum-redis) | Redis/Valkey pub/sub and streams adapters that plug into a vinculum-bus EventBus |
| [vinculum-sns](https://github.com/tsarna/vinculum-sns) | AWS SNS sender adapter that plugs into a vinculum-bus EventBus |
| [vinculum-sqs](https://github.com/tsarna/vinculum-sqs) | AWS SQS sender/receiver adapters that plug into a vinculum-bus EventBus |
| [vinculum-vws](https://github.com/tsarna/vinculum-vws) | Vinculum WebSocket Protocol — client and server for exposing an EventBus over WebSockets |
| [vinculum-wire](https://github.com/tsarna/vinculum-wire) | Pluggable wire-format system (`auto`, `json`, protobuf, …) for converting between Go values and byte sequences, used by Vinculum's clients and servers |
| [functy](https://github.com/tsarna/functy) | The functy (`.cty`) language — a small imperative language whose values are cty values and whose expressions are HCL, with functions, typed locals, control flow, and structured errors |

Vinculum also relies on several smaller standalone HCL/cty helper libraries: [go2cty2go](https://github.com/tsarna/go2cty2go), [hcl-jqfunc](https://github.com/tsarna/hcl-jqfunc), [go-structdiff](https://github.com/tsarna/go-structdiff), [rich-cty-types](https://github.com/tsarna/rich-cty-types), [bytes-cty-type](https://github.com/tsarna/bytes-cty-type), [url-cty-funcs](https://github.com/tsarna/url-cty-funcs), [time-cty-funcs](https://github.com/tsarna/time-cty-funcs), [rand-cty-funcs](https://github.com/tsarna/rand-cty-funcs), [sqid-cty-funcs](https://github.com/tsarna/sqid-cty-funcs), [geo-cty-funcs](https://github.com/tsarna/geo-cty-funcs), and [barcode-cty-func](https://github.com/tsarna/barcode-cty-func).

## License

Apache License 2.0 - see [LICENSE](LICENSE) file for details.

---

**Vinculum** (Latin: "bond" or "link") — connecting your systems with reliable, configurable messaging.
