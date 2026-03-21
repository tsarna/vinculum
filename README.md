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

- **HCL Configuration** — Declarative configuration in HashiCorp Config Language (similar to Terraform)
- **Publish/Subscribe Messaging** — MQTT-style topic routing with wildcards and parameter extraction
- **Protocol Support** — HTTP server, Vinculum WebSocket server and client, and more
- **Cron Scheduling** — Built-in cron-style scheduler for time-driven actions
- **JQ Transformations** — Data transformations using the JQ language
- **Observability** — Context propagation, tracing, and metrics throughout

## Documentation

See [doc/overview.md](doc/overview.md) for full documentation including:
- Core concepts (buses, topics, subscribers, transformations)
- Configuration language reference
- Protocol details
- Examples

## Related Projects

Vinculum is built on top of two standalone Go libraries that can be used independently:

| Project | Description |
|---------|-------------|
| [vinculum-bus](https://github.com/tsarna/vinculum-bus) | High-performance in-process EventBus with MQTT-style topic patterns and optional OpenTelemetry observability |
| [vinculum-vws](https://github.com/tsarna/vinculum-vws) | Vinculum WebSocket Protocol — client and server for exposing an EventBus over WebSockets |

## License

MIT License - see [LICENSE](LICENSE) file for details.

---

**Vinculum** (Latin: "bond" or "link") — connecting your systems with reliable, configurable messaging.
