# Vinculum Message Transforms

## What Are Transforms?

Transforms are **not regular functions**. They are pipeline constructors — each one
returns an opaque `MessageTransform` object that describes an operation to perform on
a message. The actual transformation happens later, when the event bus delivers a
message to a subscriber.

Transform constructors are only available in specific attributes that accept a
transform pipeline:

- `transforms` on `subscription` blocks
- `inbound_transforms` and `outbound_transforms` on WebSocket server blocks

They cannot be called from action expressions, `const` blocks, or any other general
expression context.

A `transforms` value is either a single transform or a list of transforms applied
left-to-right:

```hcl
subscription "example" {
    bus    = bus.main
    topics = ["sensors/#"]

    # single transform
    transforms = add_topic_prefix("processed/")

    # or a pipeline (list applied left to right)
    transforms = [
        drop_topic_prefix("sensors/"),
        add_topic_prefix("data/"),
        jq("select(.value != null)"),
    ]

    action = send(ctx, bus.output, ctx.topic, ctx.msg)
}
```

---

## Topic Transforms

### `add_topic_prefix(prefix)`

Prepend `prefix` to the message topic.

```hcl
transforms = add_topic_prefix("processed/")
# "sensors/temp" → "processed/sensors/temp"
```

### `drop_topic_prefix(prefix)`

Remove `prefix` from the message topic. No-op if the topic does not start with the
prefix.

```hcl
transforms = drop_topic_prefix("sensors/")
# "sensors/temp" → "temp"
```

### `drop_topic_pattern(pattern)`

Discard the message entirely if its topic matches the given MQTT-style pattern
(`+` matches one segment, `#` matches any number of trailing segments).

```hcl
transforms = drop_topic_pattern("internal/#")
# any message on "internal/..." is dropped
```

---

## Conditional Transforms

### `if_topic_prefix(prefix, transform)`

Apply `transform` only if the topic has the given prefix. Messages that do not match
pass through unchanged.

### `if_pattern(pattern, transform)`

Apply `transform` only if the topic matches the MQTT-style pattern. Messages that do
not match pass through unchanged.

### `if_else_topic_prefix(prefix, if_transform, else_transform)`

Apply `if_transform` if the topic has the prefix, otherwise apply `else_transform`.

### `if_else_topic_pattern(pattern, if_transform, else_transform)`

Apply `if_transform` if the topic matches the pattern, otherwise apply
`else_transform`.

---

## Payload Transforms

### `jq(query)`

Apply a JQ query to the message payload. The current topic is available inside the
query as `$topic`.

If the query returns no results (e.g. a `select(...)` that does not match), the
message is dropped.

```hcl
transforms = jq("select(.value > 0) | {value: .value, topic: $topic}")
```

### `diff(old_key, new_key)`

Read two fields from the message payload by key, compute a structural diff between
them, and replace the payload with the diff result. See also the `diff(a, b)` utility
function in [functions.md](functions.md) for computing diffs directly in expressions.

### `cty2go()`

Convert the message payload from an HCL/cty value to a plain Go native value
(map, slice, scalar, etc.). Useful when the downstream subscriber expects Go types
rather than cty values — for example, when bridging to a WebSocket client.

---

## Pipeline Composition

### `chain(transforms...)`

Compose multiple transforms into a single transform, applying them left-to-right.
Useful when you want to name or reuse a sub-pipeline:

```hcl
const {
    normalize = chain(
        drop_topic_prefix("raw/"),
        add_topic_prefix("data/"),
        jq("select(.valid == true)")
    )
}

subscription "a" {
    transforms = normalize
    ...
}
```

### `stop()`

Discard the message, stopping the pipeline immediately. Useful as the `else` branch
of a conditional:

```hcl
transforms = if_else_topic_prefix("sensors/", jq(".value"), stop())
```
