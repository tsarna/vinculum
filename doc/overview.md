# Vinculum Server Documentation

## Table of Contents

1. [Introduction](#introduction)
3. [Core Concepts and Features](#core-concepts)
4. [Features](#features)
5. [Configuration Language](config.md)
6. [Built-in Functions](functions.md)
7. [Message Transforms](transforms.md)
8. [MCP Server](server-mcp.md)
9. [HTTP Server](server-http.md)
10. [VWS Server & Client](server-vws.md)
11. [Simple WebSocket Server](server-websocket.md)
11. [Protocols and Transports](#protocols-and-transports)
8. [Advanced Topics](#advanced-topics)
9. [Examples](#examples)
10. [API Reference](#api-reference)

## Introduction

### What is Vinculum?

Vinculum is a no-code/low-code system for gluing together different systems and interfacing between multiple protocols.
Think of it like a Swiss Army Knife for communications.
While it isn't the best tool for demanding tasks where you would want a purpose-built tool, it can perform a large variety of tasks adequately.
Like a Swiss Army Knife, it's a handy thing to have in your pocket, allowing you to quickly MacGyver together a solution.

For example, with a few lines of configuration, you could create:

TODO examples

### Key Features

- **Configuraton via files in Hashicorp Config Language (HCL), similar to Terraform**
- **Publish/Subscribe Messaging**
- **Suport for many different protocols**
- **A "cron-like" scheduler**
- **JSON data transformations using the JQ language**

## Core Concepts and Features

### Event Buses

Event buses are the core messaging channels in Vinculum. Functioning like an internal MQTT server, they provide:

- **Topic-based Routing**: Messages are routed based on hierarchical topics
- **Multiple Subscribers**: Many subscribers can listen to the same topics
- **Queue Management**: Configurable queue sizes and buffering

You can have more than one bus, for isolation or organization purposes. 

#### Messages

Messages, also referred to as events, are sent and received from buses and external protocols. Mesages have:

- **Topic**: as described above, for routing purposes
- **Payload**: The actual message content (JSON, binary, etc.)
- **Context**: Contexts are tracked through the system for observability. An event received via HTTP, for example, will retain that original context as it passes through the system, allowing tracability.

#### Topics

Topics follow a hierarchical naming convention matching that of MQTT. A topic is a string with slash-separated segments, like `category/subcategory/event`. Topics create a hierarchy to organize different kinds of messages, and allow subscribers to register to get the subset of messages they're interested in. The segments are meant to be organized from broadest to most specific.

#### Topic Patterns

Subscribers register to receive messages from a bus using one or more topic patterns. These follow the MQTT syntax, with `+` matching a single path segment, and `#` matching any number of segments. For example, the pattern `sensors/+/update` would match `sensors/weather/update` but not `sensors/weather/alarms` or `sensors/weather/configuration/units/update`, while `sensors/weather/#` would match all three.

Some Vinculum features allow naming the wildcarded segments and extracting the value. For example `sensors/kind+/update` would match `sensors/weather/update` and extract `weather` into a variable or value under the name `kind`.

#### Subscribers

Subscribers register with busses or other message sources to recieve messages. A subscriber may be an action that is performed with the message, or a client or server protocol over which the message will be sent, or it may be a bus to which the message will be published.

#### Transformations

Subscriptions may declare some transformations to be performed on messages before they are given to the receiver. A number of common transformations are built in. For more complicated cases, you can create an action that can do whatever is needed then call the send() function to pass the result on to another subscriber.

### Cron

Vinculum includes a cron-style scheduling facility. An action can be performed on the given schedule, for example to send messages to a bus or other subscriber.

### Protocols

Vinculum implements a number of protocols as servers, clients, or both. Depending on the protocol, received data is either sent to a subscriber or triggers an action (which may in turn send to a subscriber, or do something else). Likewise, depending on the protocol, data is sent by actions or by subscribing the protocol to a bus or other message source.

#### HTTP Server

The HTTP Server protocol can have actions configured in response to GET, POST, PUT, etc HTTP calls on paths. Multiple routes may be configured, each with their own action.

It can also be configured to serve static files.

#### Kafka

Vinculum can produce messages to and consume messages from Apache Kafka. A
`client "kafka"` block may contain any number of named `producer` and
`consumer` sub-blocks. Producers map vinculum topics to Kafka topics (with
optional record keys) and implement `bus.Subscriber`, so they can be the target
of any `subscription` block or used with the send() functions. Consumers run
independent poll loops and publish received messages to a target bus, with
configurable consumer groups, offset management, and dead-letter queue support. See
[client-kafka.md](client-kafka.md) for the full reference.

#### Vinculum Websocket Server

Vinculum Websockets Server offers a simple MQTT-like publish/subscribe protocol to expose a bus over WebSockets.

#### Vinculum Websockets Client

Vinculum can speak the same protocol as a client, eg to another Vinculum instance.

## Configuration Language

See [config.md](config.md) for the full configuration language reference, including
HCL syntax, built-in variables, and all block types.

## Built-in Functions

See [functions.md](functions.md) for the full function reference.

## Protocols and Transports

### WebSocket Protocol

#### Connection Management
- Automatic reconnection handling
- Connection lifecycle events
- Heartbeat and keepalive mechanisms

#### Message Format
- JSON-based message framing
- Binary payload support
- Protocol negotiation

#### Client Libraries
- Go client library
- JavaScript/TypeScript clients
- Protocol specifications

### HTTP Protocol

#### RESTful Endpoints
- Standard HTTP methods (GET, POST, PUT, DELETE)
- Custom route handlers
- Middleware support

#### Server-sent Events (SSE)
- Real-time event streaming
- Browser-compatible format
- Connection management

#### File Serving
- Static file hosting
- Directory browsing
- MIME type detection

### Protocol Extensions

#### Custom Protocols
- Plugin architecture for new protocols
- Protocol-specific configuration
- Message format adaptation

#### Multi-protocol Support
- Protocol bridging
- Message format conversion
- Unified client experience

## Advanced Topics

### Performance and Scaling

#### Queue Management
- Backpressure handling
- Queue size optimization
- Memory management

#### Connection Pooling
- Client connection management
- Resource optimization
- Load balancing

### Observability

#### Metrics
- Built-in performance metrics
- Custom metric collection
- Integration with monitoring systems

#### Logging
- Structured logging
- Log level configuration
- Audit trails

#### Tracing
- Distributed tracing support
- Request correlation
- Performance profiling

### Security

#### Authentication
- Client authentication mechanisms
- Token-based authentication
- Integration with identity providers

#### Authorization
- Role-based access control
- Topic-level permissions
- Action authorization

#### Transport Security
- TLS/SSL support
- Certificate management
- Secure WebSocket connections

### High Availability

#### Clustering
- Multi-node deployment
- Leader election
- State synchronization

#### Fault Tolerance
- Automatic failover
- Circuit breaker patterns
- Graceful degradation

## Examples

### Basic Event Routing

```hcl
# Basic configuration example
bus "main" {
    queue_size = 1000
}

subscription "logger" {
    bus = bus.main
    topics = ["app/#"]
    action = loginfo("Received message", ctx.msg)
}

cron "heartbeat" {
    at "*/30 * * * * *" "ping" {
        action = send(ctx, bus.main, "system/heartbeat", {
            timestamp = timestamp()
            status = "alive"
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

### Complex Data Processing

```hcl
const {
    processors = {
        user_events = jq("select(.type == \"user\") | {user_id: .user.id, action: .action}")
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

## API Reference

### Configuration Schema

#### Block Types
- `bus`: Event bus configuration
- `subscription`: Message subscription and processing
- `server`: HTTP/WebSocket server configuration
- `cron`: Scheduled task configuration
- `const`: Constant value definitions
- `assert`: Configuration validation
- `jq`: JQ function definitions

#### Attribute Types
- String literals and interpolated strings
- Numbers (integers and floats)
- Booleans
- Lists and maps
- Function calls and expressions

### Function Reference

See [functions.md](functions.md) and [transforms.md](transforms.md).

### Error Codes

<!-- TODO: Error code reference -->

## Troubleshooting

### Common Issues

#### Configuration Errors
- HCL syntax errors
- Missing required attributes
- Type mismatches
- Function call errors

#### Runtime Issues
- Connection failures
- Message delivery problems
- Performance bottlenecks
- Memory leaks

#### Protocol-specific Issues
- WebSocket connection drops
- HTTP timeout errors
- Message format problems

### Debugging Tools

#### Logging Configuration
```hcl
# Enable debug logging
const {
    log_level = "debug"
}
```

#### Health Checks
- Built-in health endpoints
- System status monitoring
- Performance metrics

#### Diagnostic Commands
- Configuration validation
- Connection testing
- Message tracing

### Performance Tuning

#### Queue Optimization
- Queue size tuning
- Memory allocation
- Garbage collection

#### Network Optimization
- Connection pooling
- Message batching
- Compression settings

### Migration Guides

#### Version Upgrades
- Breaking changes
- Configuration migration
- Feature deprecations

#### Protocol Changes
- Client library updates
- Message format changes
- Backward compatibility

---

*This documentation is a work in progress. Please contribute improvements and report issues.*
