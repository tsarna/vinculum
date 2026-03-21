# Vinculum Server Documentation

## Table of Contents

1. [Introduction](#introduction)
3. [Core Concepts and Features](#core-concepts)
4. [Features](#features)
5. [Configuration Language](#configuration-language)
6. [Built-in Functions](#built-in-functions)
7. [Protocols and Transports](#protocols-and-transports)
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

#### Vinculum Websocket Server

Vinculum Websockets Server offers a simple MQTT-like publish/subscribe protocol to expose a bus over WebSockets.

#### Vinculum Websockets Client

Vinculum can speak the same protocol as a client, eg to another Vinculum instance.

## Configuration Language

### HCL Syntax

Vinculum uses HashiCorp Configuration Language (HCL) for configuration:

```hcl
# Comments start with #
block_type {
    attribute = value
    nested_block {
        nested_attribute = expression
    }
}

# Blocks may have zero or more labels
block_type "label" {
    nested_block "foo" "bar {
        ...
    }
}
```

How many labels are expected and their meaning depends on the block type.

HCL has a rich expression syntax. See the HCL documentation.

### Variables

Various varibles may be referenced in expressions.

#### Built-in Variables

- `bus.*`: Each bus defined may be referenced by name. `bus.main` always exists, even if not declared explicitly.
- `env.*`: environment variables are exposed through the `env` object. For example, `env.HOME` will be the value of the `HOME` environment variable.
- `http_status.*`: For convenience, constants are provided for each HTTP status code. For example, `http_status.OK` has the value `200`
- `http_status.by_code`: A map of code to status name. For example `http_status.by_code[200]` is equal to the string `"OK"`.
- `server.*`: Each server defined may be referenced by name. Servers of all types share a single namespace, eg you cannot have both a http server `foo` and a websockets server `foo`.

#### Context Variables

In many contexts, prticularly when evaluating action expressions, `ctx` will be an object representing the execution context.
The exact attributes it offers depend on the context and are covered with the relevant section of the documentation.
Some functions expect a context to be passed as a paramater.

#### User-Defined Constants

Users may define their own constants using the `const` block, explained below.

### Configuration Blocks

#### Assert

```hcl
assert "name" {
    condition = expresion
}
```

The assert block checks that a condition is true, and fails startup of the vinculum server if not.
It is primarily intended for interal use in Vinculum test csses, but it could be used in user configurations to ensure
the configuration is valid. For example, if a configuration references environment variables, assertions could be
used to ensure that they have reasonable values.

The `name` label names the assertion and will be included in the output if the assertion fails.

The `condition` expression is evaluated at startup. If it is true, startup proceeds. If false, startup will fail.

#### Bus

```hcl
bus "name" {
    queue_size = 1000 # optional, default 1000
}
```

This block declares an event bus to be created. The name label names the bus to be created. The bus will be available to
expressions as `bus.name`. For example `bus "foo" {}` creates a bus named `foo`, which will be referenceable as `bus.foo`
elsewhere in the configurtion.

The optional `queue_size` configures how large of a backlog the bus can sustain before dropping messages.

#### Const

```hcl
const {
    pi = 3.141529
    name = "John"
    some_numbers = [1, 2, 3]
}
```

The `const` block allows users to set their own variables that will be available to expressions.
Each attribute's expression is evaluated at startup and assigned to the given variable. If there are multiple
const blocks, they will be merged.

Expressions in the const block may reference other constants, environment variables, http status codes, and most functions, including user-defined and JQ functions. 

#### Cron

```hcl
cron "name" {
    timezone = "UTC" # optional
    disable = boolean expression # optional, default false TODO implement this!
     
     at "schedule" "rulename" { # 1 or more
        action = expression
        disable = boolean expression # optional, default false TODO implement this
     }
}
```

This block defines a cron-like scheduler. Multiple cronts may be defined, each with a different name.
Each cron runs in a `timezone`, by default in your local time zone. One reason to use multiple cron blocks is if you want to use schedules in multiple different timezones.

If `disable` is true, the cron will not run. This allows disabling schedules via, for example, an environment variable
or a constant defined in a different config file.

Each cron block can have one or more "at" blocks to define an schedule and an action to execute on that schedule.
`at` blocks can also be disabled individually.

Each `at` block has a schedule and a rule name. See the https://en.wikipedia.org/wiki/Cron for an explanation of the schedule format.
Note that here though, only the schedule part of the syntax is used. In Vinculum, the action to be executed is instead specified
by the `action` expression.

This cron also allows a six-element schedule format where the first element represents seconds.

As a trivial example, this would run every hour at 5 minutes past the hour:

```hcl
at "5 * * * *" "my_hourly_job" {
     action = send(ctx, bus.main, "cron/hourly", "The cron ${ctx.cron_name] is executing the action for ${ctx.at_name}")
}
```

When an `at` rule fires, it evaluates the `action` expression. Within the execution context of this expression, `ctx` will be an object
with fields `cron_name` and `at_name`, referencing the names used in the `cron` and `at`
blocks that define the rule which triggered this action.

#### Function

```hcl
function "name" {
  params = [a, b]
  variadic_param = c # optional
  result = expression
}
```

Vinculum allows simple user-defined functions using this extension: https://pkg.go.dev/github.com/hashicorp/hcl/v2/ext/userfunc#section-readme

The `name` label specifies what name the function will have.

`params` is a list of zero or more variables names that are to be the parameters of the function. Note that these are variable names and not strings.

`variadic_param`, if given, specifies the name of the variable that will hold the list of any additional parameters passed.

`result` gives the expression to be evaluated and returned.

Example:

```hcl
function "circle_area" {
    params = [radius]
    result = pi * radius * radius
}
```

The function may then be used in expressions, eg `unit_circle_area = circle_area(1.0)`

#### JQ

```hcl
jq "calculate_price" {
   params = [tax_rate, discount]
   query = ".price * (1 + $tax_rate) * (1 - $discount)"
}
```

Functions may also be written in the JQ JSON Query Language https://jqlang.org/
as immplemented by the hcl-jq extension https://github.com/tsarna/hcl-jqfunc/blob/main/README.md

Like with user-defined functions, the `name` label gives the name of the function to create and `params` is a list of variable names that will be parameters to the function.

`query` is the JQ query to be evaluated. The paramaters become variables accessible in the JQ query with `$` prepended.

The resulting function takes an initial parameter that is the input data to be processed, and then the declared paramaters, if any. For example the function defined above could be called like `calculate_price("{\"price\": 1.23}", 0.06, 0.10)`

If the input is a string, the function will parse it as JSON, operate on it, and then encode the result as a JSON string. (But, as a special excpetion, if the input is a JSON string and the result is a single string, it will be returned as-is, not JSON-encoded. This is because the typical use cause of returning a singe string would be a function to extract one field. If you really want the string JSON-encoded, use the `tojson` function in JQ).

If the input is not a string (eg list, tuple, map, object, ...) it will be processed as HCL values and the result will be a HCL value. Eg, a `jq` function might directly take a map and return a list, without the need to encode into JSON and then decode the result back.

#### Server

```hcl
server "type" "name" {
    disabled = false # default
    ...
}
```

The server block defines a network service that will be provided. The details of this block depend on the type of the service. See the section Server Types for details on the avalable options.

All service blocks include a "disabled" attribute, which if true will cause the block to be skipped.

The server will be available to expressions as `server.name`. For example `server "http" "foo" {...}` creates a HTTP server named `foo`, which will be referenceable as `server.foo` elsewhere in the configuration. There is a single namespace for servers of all types. 

#### Signals

```hcl
signals {
    SIGHUP = log_info("got signal", {signal=ctx.signal, signal_num=ctx.signal_num})
    SIGINFO = log_info("got signal", {signal=ctx.signal, signal_num=ctx.signal_num})
    SIGUSR1 = log_info("got signal", {signal=ctx.signal, signal_num=ctx.signal_num})
    SIGUSR2 = log_info("got signal", {signal=ctx.signal, signal_num=ctx.signal_num})

    disabled = false # default
}
```

The signals block defines actions to be taken when signals are received. The signals available vary by system. The complete set of possible signals is `SIGHUP`, `SIGINFO`, `SIGUSR1`, and `SIGUSR2`.

The signal's action expression will be evaluated in a context where `ctx` is a context object with attributes `signal` (the name of the signal) and `signal_num` (the signal's OS-level number).

There can be multiple `servers` blocks. Each has an optional `disabled` attribute, and if it evaluates to true then that block will be skipped.

A given signal may only be defined in one active `signals` block.

#### Subscription

### Server Types

#### HTTP

```hcl
server "http" "name {
    ... TODO
}
```

## Built-in Functions

### Message Transform Functions

#### Topic Manipulation
- `add_topic_prefix(prefix)`: Add prefix to message topic
- `drop_topic_prefix(prefix)`: Remove prefix from message topic
- `drop_topic_pattern(pattern)`: Remove topics matching pattern

#### Conditional Transforms
- `if_topic_prefix(prefix, transform)`: Apply transform if topic has prefix
- `if_pattern(pattern, transform)`: Apply transform if topic matches pattern
- `if_else_topic_prefix(prefix, if_transform, else_transform)`: Conditional transformation
- `if_else_topic_pattern(pattern, if_transform, else_transform)`: Pattern-based conditional

#### Data Transformation
- `jq(query)`: Apply JQ query to message payload (with `$topic` variable)
- `diff(old_key, new_key)`: Generate structural diff between values
- `chain(transforms...)`: Chain multiple transformations

#### Flow Control
- `stop()`: Stop transformation pipeline

### Utility Functions

#### Logging Functions
- `log_debug(message, data...)`: Debug level logging
- `log_info(message, data...)`: Info level logging  
- `log_warn(message, data...)`: Warning level logging
- `log_error(message, data...)`: Error level logging
- `log_msg(level, message, data...)`: Generic logging with level

#### Data Manipulation
- `jsonencode(value)`: Encode value as JSON
- `jsondecode(json_string)`: Decode JSON string
- `patch(object, diff)`: Apply diff to object
- `typeof(value)`: Get type information

#### Action Functions
- `send(context, bus, topic, payload)`: Send message to bus
- `sendjson(context, bus, topic, payload)`: Send JSON-encoded message
- `sendgo(context, bus, topic, template)`: Send with Go template interpolation
- `respond(status, payload)`: HTTP response (in server contexts)

### User-defined Functions

#### JQ Function Definition
```hcl
jq "function_name" {
    query = ".field | transformation"
}
```

#### Custom Function Usage
Functions defined in configuration can be used in expressions and transforms.

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
    topics = ["app/*"]
    action = log_info("Received message", ctx.msg)
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
    topics = ["raw/data/*"]
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
            respond(httpstatus.Accepted, {status: "received"})
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
    topics = ["events/*"]
    transforms = [
        if_else_topic_pattern(
            "events/user/*",
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

<!-- TODO: Complete function reference with parameters and return types -->

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
