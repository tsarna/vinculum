# Vinculum Server Documentation

## Table of Contents

1. [Introduction](#introduction)
2. [Core Concepts](#core-concepts)
3. [Triggers](#triggers)
4. [Configuration Language](#configuration-language)
5. [Built-in Functions](#built-in-functions)
6. [Message Transforms](#message-transforms)
7. [Procedures](#procedures)
8. [Editors](#editors)
9. [Metrics](#metrics)
10. [Servers](#servers)
11. [Clients](#clients)
12. [Examples](#examples)
13. [Observability](#observability)
14. [Block Type Reference](#block-type-reference)

## Introduction

### What is Vinculum?

Vinculum is a no-code/low-code system for gluing together different systems and interfacing between multiple protocols.
Think of it like a Swiss Army Knife for communications.
While it isn't the best tool for demanding tasks where you would want a purpose-built tool, it can perform a large variety of tasks adequately.
Like a Swiss Army Knife, it's a handy thing to have in your pocket, allowing you to quickly MacGyver together a solution.

See the [examples/](../examples/) directory for working configurations, including a dynamic
DNS zone updater that exposes an authenticated HTTP endpoint and uses an `editor "line"`
block to safely rewrite BIND zone files in place.

### Key Features

- **HCL Configuration** — Declarative configuration in HashiCorp Config Language (similar to Terraform), with constants, expressions, and assertions
- **Publish/Subscribe Messaging** — One or more event buses with MQTT-style topic routing, wildcards, and parameter extraction
- **Server Protocols** — HTTP(S), Vinculum WebSocket (VWS), plain WebSocket, Model Context Protocol (MCP), and Prometheus/OpenMetrics, with pluggable authentication (basic, OIDC, OAuth2)
- **Client Protocols** — Kafka, MQTT, VWS (to other Vinculum instances), OpenAI / LLM, and OpenTelemetry (OTLP) export
- **Triggers** — A range of trigger types for time-, event-, and lifecycle-driven actions: cron, dynamic intervals with optional jitter, absolute / dynamic times, file-system events, OS signals, startup/shutdown, watchdogs, and watches over reactive values
- **Transformations and Procedures** — JQ-based message transforms, structured-text `editor` blocks, and `procedure` blocks for small imperative helpers
- **Built-in Functions** — A large standard library covering HTTP, files, templates, time, randomness, IDs, LLMs, and more
- **Observability** — Context propagation, OpenTelemetry tracing and metrics, and Prometheus exposition throughout

## Core Concepts

### Event Buses

Event buses are the core messaging channels in Vinculum. Functioning like an internal MQTT server, they provide:

- **Topic-based Routing**: Messages are routed based on hierarchical topics
- **Multiple Subscribers**: Many subscribers can listen to the same topics
- **Queue Management**: Configurable queue sizes and buffering

You can have more than one bus, for isolation or organization purposes.

### Messages

Messages, also referred to as events, are sent and received from buses and external protocols. Messages have:

- **Topic**: as described above, for routing purposes
- **Payload**: The actual message content (JSON, binary, etc.)
- **Context**: Contexts are tracked through the system for observability. An event received via HTTP, for example, will retain that original context as it passes through the system, allowing traceability.

### Topics

Topics follow a hierarchical naming convention matching that of MQTT. A topic is a string with slash-separated segments, like `category/subcategory/event`. Topics create a hierarchy to organize different kinds of messages, and allow subscribers to register to get the subset of messages they're interested in. The segments are meant to be organized from broadest to most specific.

### Topic Patterns

Subscribers register to receive messages from a bus using one or more topic patterns. These follow the MQTT syntax, with `+` matching a single path segment, and `#` matching any number of segments. For example, the pattern `sensors/+/update` would match `sensors/weather/update` but not `sensors/weather/alarms` or `sensors/weather/configuration/units/update`, while `sensors/weather/#` would match all three.

Some Vinculum features allow naming the wildcarded segments and extracting the value. For example `sensors/kind+/update` would match `sensors/weather/update` and extract `weather` into a variable or value under the name `kind`.

### Subscribers

Subscribers register with buses or other message sources to receive messages. A subscriber may be an action that is performed with the message, or a client or server protocol over which the message will be sent, or it may be a bus to which the message will be published.

### Transformations

Subscriptions may declare some transformations to be performed on messages before they are given to the receiver. A number of common transformations are built in. For more complicated cases, you can create an action that can do whatever is needed then call the `send()` function to pass the result on to another subscriber.

## Triggers

Triggers cause an action to be evaluated in response to time, lifecycle, or external events. Vinculum supports many trigger types: classic cron schedules, fixed and dynamically computed intervals, absolute / dynamically computed wall-clock times (e.g. sunrise/sunset), file-system events, OS signals, application startup and shutdown, watchdogs (fire when expected work *stops*), and watches over reactive values.

See [trigger.md](trigger.md) for the full reference covering all trigger types.

## Configuration Language

See [config.md](config.md) for the full configuration language reference, including HCL syntax, built-in variables, block types, and dependency ordering.

## Built-in Functions

Vinculum ships with a large standard library of functions for working with HTTP requests and responses, files, templates, time and dates, randomness, sequential IDs, JSON, JQ, LLMs, and more.

See [functions.md](functions.md) for the full reference.

## Message Transforms

Subscriptions can declare a pipeline of transforms that run on each message before delivery — JQ expressions, topic prefixing/stripping, conditional transforms based on topic patterns, and composition operators.

See [transforms.md](transforms.md) for the full reference.

## Procedures

A `procedure` block defines a callable function with a small imperative body — locals, conditionals (`if`/`elif`), loops (`while`, `for`), and `return`. Procedures are compiled at config load time and can be called from any expression. They're useful when a piece of logic is awkward to express as a single HCL expression but doesn't warrant building a Go plugin.

See [procedure.md](procedure.md) for syntax, scoping rules, and examples.

## Editors

An `editor` block defines a callable function for structured text editing. The current implementation, `editor "line"`, performs regex match-and-replace on a file or string with persistent state, before/after blocks, optional file locking, and a notion of "incidental" edits that don't, on their own, count as a real change.

See [editor.md](editor.md) for the full reference.

## Metrics

A `metric` block declares a Prometheus-style metric (gauge, counter, or histogram), optionally with labels and a computed expression. Metrics can be exposed via the Prometheus/OpenMetrics endpoint (see [Servers](#servers) below) or pushed via OTLP (see [Clients](#clients) below).

See [metric.md](metric.md) for metric declaration syntax.

## Servers

A `server` block accepts inbound connections or requests over a particular protocol. Each server type has its own dedicated reference page:

| Server | Description |
| ------ | ----------- |
| [`server "http"`](server-http.md) | HTTP(S) server with route handlers, static-file serving, TLS, cookies, and pluggable authentication |
| [`server "vws"`](server-vws.md) | Vinculum WebSocket Protocol — exposes a bus over WebSockets with pub/sub semantics |
| [`server "websocket"`](server-websocket.md) | Plain (raw) WebSocket server with bus integration and inbound/outbound transforms |
| [`server "mcp"`](server-mcp.md) | Model Context Protocol server exposing resources, tools, and prompts to LLM clients |
| [`server "metrics"`](server-metrics.md) | Prometheus / OpenMetrics exposition endpoint, standalone or mounted into an existing HTTP server |
| [`server "auth"`](server-auth.md) | Authentication providers (basic, OIDC, OAuth2, custom) used as middleware by other servers |

## Clients

A `client` block makes outbound connections to a remote service. Each client type has its own dedicated reference page:

| Client | Description |
| ------ | ----------- |
| [`client "kafka"`](client-kafka.md) | Kafka producer and consumer adapters with SASL/TLS, commit modes, and dead-letter queue support |
| [`client "mqtt"`](client-mqtt.md) | MQTT 5.0 publisher and subscriber, including last-will and shared subscriptions |
| [`client "vws"`](server-vws.md) | Vinculum WebSocket client — connects to another Vinculum instance over the VWS protocol (documented alongside the VWS server) |
| [`client "openai"`](client-llm.md) | OpenAI and OpenAI-compatible LLM API client (used via the `call()` function) |
| [`client "otlp"`](client-otlp.md) | OpenTelemetry Protocol exporter for traces and metrics (push-based) |

## Examples

The [examples/](../examples/) directory contains complete working configurations. The snippets below illustrate common patterns; see [examples/README.md](../examples/README.md) for the index of full examples.

### Basic Event Routing

```hcl
bus "main" {
    queue_size = 1000
}

subscription "logger" {
    bus = bus.main
    topics = ["app/#"]
    action = log_info("Received message", ctx.msg)
}

trigger "cron" "heartbeat" {
    at "*/30 * * * * *" "ping" {
        action = send(ctx, bus.main, "system/heartbeat", {
            timestamp = formattime("@rfc3339", now("UTC"))
            status    = "alive"
        })
    }
}
```

### Message Transformation Pipeline

```hcl
subscription "data_processor" {
    bus = bus.main
    topics = ["raw/data/#"]
    transforms = [
        jq("select(.valid == true)"),
        add_topic_prefix("processed/"),
        jq("{id: .id, processed_data: .data, topic: $topic}")
    ]
    action = send(ctx, bus.output, ctx.topic, ctx.msg)
}
```

### HTTP Server Integration

```hcl
server "http" "api" {
    listen = ":8080"

    handle "POST /events" {
        action = [
            send(ctx, bus.main, "api/event", ctx.body),
            http_response(http_status.Accepted, {status = "received"})
        ]
    }

    files "/dashboard" {
        directory = "./web/dashboard"
    }
}
```

### Conditional Transforms by Topic

```hcl
const {
    processors = {
        user_events   = jq("select(.type == \"user\") | {user_id: .user.id, action: .action}")
        system_events = jq("select(.type == \"system\") | {component: .component, status: .status}")
    }
}

subscription "event_router" {
    bus = bus.main
    topics = ["events/#"]
    transforms = [
        if_else_topic_pattern(
            "events/user/#",
            processors.user_events,
            processors.system_events
        )
    ]
    action = send(ctx, bus.processed, "categorized/" + ctx.msg.type, ctx.msg)
}
```

## Observability

Vinculum propagates a context through the system as messages flow from sources through buses, transforms, and subscribers, so events received via one protocol retain their originating context as they're processed and forwarded. OpenTelemetry traces and metrics are emitted throughout, and can be exposed in Prometheus/OpenMetrics format via [`server "metrics"`](server-metrics.md) or pushed to an OTel collector via [`client "otlp"`](client-otlp.md). Application-defined metrics are declared with [`metric`](metric.md) blocks.

## Block Type Reference

Top-level block types:

- `bus` — event bus declaration
- `subscription` — subscribes a target to one or more topic patterns on a bus, with optional transforms and an action
- `server` — server protocol instance (see [Servers](#servers))
- `client` — client protocol instance (see [Clients](#clients))
- `trigger` — time, lifecycle, or event-driven action (see [trigger.md](trigger.md))
- `metric` — metric declaration for Prometheus/OTLP exposition (see [metric.md](metric.md))
- `procedure` — imperative function definition (see [procedure.md](procedure.md))
- `editor` — structured text editing function (see [editor.md](editor.md))
- `jq` — named JQ function definition
- `const` — named constants
- `assert` — configuration validation assertions
