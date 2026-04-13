# Vinculum

"The [vinculum is the] processing device at the core of every Borg vessel.
It interconnects the minds of all the drones."
   -- Seven of Nine (In Voyager episode "Infinite Regress")

Vinculum is a no-code/low-code system for gluing together different systems and
bridging multiple protocols. Think of it as a Swiss Army Knife for communications:
while not the best tool for demanding tasks where you'd want a purpose-built solution,
it handles a large variety of integration tasks adequately — letting you quickly
MacGyver together a solution with a few lines of configuration.

## Key Features

- **HCL Configuration** — Declarative configuration in HashiCorp Config Language (similar to Terraform), with constants, expressions, and assertions
- **Publish/Subscribe Messaging** — One or more event buses with MQTT-style topic routing, wildcards, and parameter extraction
- **Server Protocols** — HTTP(S), Vinculum WebSocket (VWS), plain WebSocket, Model Context Protocol (MCP), and Prometheus/OpenMetrics, with pluggable authentication (basic, OIDC, OAuth2)
- **Client Protocols** — HTTP(S), Kafka, MQTT, VWS (to other Vinculum instances), OpenAI / LLM, and OpenTelemetry (OTLP) export
- **Triggers** — A range of trigger types for time-, event-, and lifecycle-driven actions: cron, dynamic intervals with optional jitter, absolute / dynamic times, file-system events, OS signals, startup/shutdown, watchdogs, and watches over reactive values
- **Conditions** — Named boolean primitives with temporal rules (activate/deactivate delays, hysteresis, retentive timing, latches, cooldown, inhibit), covering IEC 61131-3 timer and counter function-block behaviors and composable into pipelines
- **Transformations and Procedures** — JQ-based message transforms, structured-text `editor` blocks, and `procedure` blocks for small imperative helpers
- **Built-in Functions** — A large standard library covering HTTP, files, templates, time, randomness, IDs, LLMs, and more
- **Observability** — Context propagation, OpenTelemetry tracing and metrics, and Prometheus exposition throughout

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
procedure, and a line-editor block to safely rewrite BIND zone files.

## Related Projects

Vinculum is built on top of several standalone Go libraries that can be used independently:

| Project | Description |
| ------- | ----------- |
| [vinculum-bus](https://github.com/tsarna/vinculum-bus) | High-performance in-process EventBus with MQTT-style topic patterns and optional OpenTelemetry observability |
| [vinculum-vws](https://github.com/tsarna/vinculum-vws) | Vinculum WebSocket Protocol — client and server for exposing an EventBus over WebSockets |
| [vinculum-kafka](https://github.com/tsarna/vinculum-kafka) | Kafka producer/consumer adapters that plug into a vinculum-bus EventBus |
| [vinculum-mqtt](https://github.com/tsarna/vinculum-mqtt) | MQTT publisher/subscriber adapters that plug into a vinculum-bus EventBus |

Vinculum also relies on several smaller standalone HCL/cty helper libraries: [go2cty2go](https://github.com/tsarna/go2cty2go), [hcl-jqfunc](https://github.com/tsarna/hcl-jqfunc), [go-structdiff](https://github.com/tsarna/go-structdiff), [time-cty-funcs](https://github.com/tsarna/time-cty-funcs), [rand-cty-funcs](https://github.com/tsarna/rand-cty-funcs), and [sqid-cty-funcs](https://github.com/tsarna/sqid-cty-funcs).

## License

MIT License - see [LICENSE](LICENSE) file for details.

---

**Vinculum** (Latin: "bond" or "link") — connecting your systems with reliable, configurable messaging.
