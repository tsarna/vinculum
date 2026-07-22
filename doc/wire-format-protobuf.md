# Protobuf Wire Format (`wire_format "protobuf"`)

The `protobuf` wire format decodes [Protocol Buffers](https://protobuf.dev)
binary messages into VCL values and encodes VCL values back into protobuf
binary, driven by a schema you supply. Once declared, it is used exactly like
the built-in `auto`/`json`/`string`/`bytes` formats: assign it to a client's
`wire_format` attribute, or pass it anywhere a `wire_format` value is accepted.

```hcl
wire_format "protobuf" "orders" {
  descriptor_set = "schemas/orders.binpb"
  message        = "acme.orders.v1.Order"
}

client "mqtt" "broker" {
  # ...
  wire_format = wire_format.orders
}
```

Inbound payloads on that client are now decoded from protobuf binary into a VCL
object; values `send()`-ed to it are encoded back to protobuf binary.

The implementation is pure Go (no `protoc` at runtime) and CGO-clean, so it
ships in the minimal container image — unlike a `.so` plugin.

---

## How protobuf differs from the other formats

The built-in formats are self-describing enough to decode blind: JSON carries
its own structure, `string`/`bytes` are identity-ish. Protobuf binary is **not**
self-describing — the same bytes decode differently depending on the message
type, and field names are not present on the wire (only field numbers). Two
consequences:

1. **A protobuf wire format is bound to exactly one message type.** A block with
   `message` set is a single wire format; a block that references a whole schema
   produces one wire format *per message* (see [Message binding](#message-binding)).
2. **A schema is mandatory** — supplied as a compiled `FileDescriptorSet`.

Every Vinculum transport delivers a discrete payload (one MQTT message, one
Kafka record, one HTTP body), so each payload is exactly one protobuf message.
Length-delimited streaming framing is out of scope.

---

## The block

```hcl
wire_format "protobuf" "<name>" {
  descriptor_set = "<path>"         # required: compiled FileDescriptorSet
  message        = "<full.Name>"    # optional: bind to a single message type
  mode           = "native"         # optional: "native" (default) | "json"
}
```

| Attribute | Req | Description |
|---|---|---|
| `descriptor_set` | yes | Path to a compiled `FileDescriptorSet`. Relative paths resolve against the config directory (`--file-path`), so a `git`-materialized schema tree works. |
| `message` | no | Fully-qualified message name (e.g. `acme.orders.v1.Order`). When present, the block value is a single wire format capsule. When omitted, the block value is an object of capsules, one per message. |
| `mode` | no | Representation mode: `native` (default) or `json`. Applies to every message the block exposes. See [Representation modes](#representation-modes). |

### Producing a descriptor set

`descriptor_set` is a standard, language-neutral serialized `FileDescriptorSet`.
Produce one with either toolchain:

```sh
protoc --include_imports --descriptor_set_out=orders.binpb orders.proto
# or
buf build -o orders.binpb
```

`--include_imports` is recommended but not required: the `google/protobuf/*`
well-known types (Timestamp, Duration, Struct, Any, wrappers, FieldMask, Empty)
are bundled with Vinculum and resolved automatically, so your set need not carry
them.

---

## Message binding

### Single message

With `message` set, `wire_format.<name>` **is** the wire format for that one
type — interchangeable with a built-in:

```hcl
wire_format "protobuf" "order" {
  descriptor_set = "schemas/orders.binpb"
  message        = "acme.orders.v1.Order"
}
# wire_format.order  -> the Order wire format
```

### Multiple messages

With `message` omitted, `wire_format.<name>` is an **object** exposing every
message in the set. Two access paths coexist:

1. **Full name, always available, via index** — every message is keyed by its
   fully-qualified proto name:

   ```hcl
   wire_format.orders["acme.orders.v1.Order"]
   ```

2. **Short name, as a convenience attribute, when unambiguous** — if a message's
   short (final-segment) name is unique across the whole set, it *also* gets a
   bare-attribute alias:

   ```hcl
   wire_format.orders.Order          # alias exists because "Order" is unique here
   ```

**Disambiguation:** when two or more messages share a short name (e.g.
`acme.orders.v1.Order` and `acme.archive.Order`), none of the colliding messages
receives the short alias — each is reached only through its full-name index.
Messages with unique short names keep their aliases. The full-name index is the
guaranteed contract; adding a colliding message only ever removes a sugar alias,
never a full-name key.

Well-known types (`google.protobuf.*`) and synthetic map-entry messages are not
exposed as bindable messages.

---

## Representation modes

`mode` selects one of two coherent representations for the entire message. The
split follows the two real use cases: **acting on** message values inside VCL vs.
**relaying** them to a system that speaks protojson-flavored JSON.

| Aspect | `mode = "native"` (default) | `mode = "json"` |
|---|---|---|
| Intended use | Inspect/transform values in VCL | Relay to a JSON API; protojson fidelity |
| Field names | proto `snake_case` | `camelCase` (JSON names) |
| `Timestamp` | **Time capsule** | RFC 3339 string |
| `Duration` | **Duration capsule** | protojson duration string (`"3.5s"`) |
| `bytes` field | **bytes object** | base64 string |
| `int64`/`uint64`/`fixed64`/`sfixed64` | number | **string** (protojson rule) |
| enums | string name | string name |
| `Struct`/`Value`/`ListValue` | native object/any/list | native object/any/list |
| wrappers (`Int32Value`, …) | unwrapped scalar / null | unwrapped scalar / null |
| `Any` (type known) | unpacked object + `@type` | `{"@type": …, …}` |
| `FieldMask` | list of path strings | comma-joined string |
| `Empty` | `{}` | `{}` |

`native` is the default because VCL's job is usually to act on messages, and the
rich Time/Duration/bytes values are the same types the rest of Vinculum already
speaks — so `send()`, `jq`, functy, and the time functions operate on them
directly with no re-parsing. A proto→JSON-API pipeline flips one switch to
`json`.

### Well-known types on serialize

In `native` mode, serialize accepts flexible inputs for the rich types:

| WKT | native decode → | native serialize accepts |
|---|---|---|
| `Timestamp` | Time capsule | Time capsule / RFC 3339 string / epoch number |
| `Duration` | Duration capsule | Duration capsule / duration string / seconds number |
| `bytes` | bytes object | bytes object / string |
| `Struct`/`Value`/`ListValue` | native object/any/list | same |
| wrappers | scalar or null | scalar or null |
| `FieldMask` | list of path strings | list or comma-joined string |
| `Empty` | `{}` | `{}` / null |

### `Any`

`google.protobuf.Any` carries a type URL plus opaque bytes. When the referenced
message type is present in the loaded descriptor set, it is **auto-unpacked**:

- `native`: an object with the unpacked message's fields plus a `@type` key.
- `json`: the protojson form, `{"@type": "type.googleapis.com/…", …fields…}`.

When the type is **not** known to the set, the value stays opaque:
`{"@type": "<url>", "value": <bytes object / base64 string>}`. Serialize is
symmetric: a `@type` naming a known message packs it; an unknown `@type` with an
opaque `value` passes through.

### Enums

Enums render as their **string name** in both modes. An unknown numeric value
passes through as its integer. On serialize, both the string name and the integer
are accepted.

---

## Serialize semantics

`Serialize` takes a VCL value shaped like the message (keyed per the active
mode's field-naming rule) and produces protobuf binary.

- **Unknown fields** in the input object are an **error** — a stray or misspelled
  key almost always signals a bug.
- **Missing fields** take their proto default (proto3 implicit defaults; proto2
  optional fields are simply unset). Missing proto2 `required` fields are an
  error.
- **Type mismatches** are an error with the field path in the message.
- **No `[]byte` passthrough:** unlike the built-in formats, a raw byte payload is
  not passed through untouched — there is no meaningful "already-encoded"
  identity for a typed message. Use the `bytes` format if you want that.

---

## Deserialize semantics

`Deserialize` takes protobuf binary and produces a VCL value per the active mode.

- Malformed binary, or bytes that don't decode against the bound message
  descriptor, return an error.
- Unknown fields on the wire (field numbers not in the descriptor) are **ignored**
  — standard protobuf forward-compatibility.
- Decode failures integrate with the existing
  [`on_decode_error`](client-mqtt.md) hook. `DecodeError.Format` is reported as
  `protobuf:<full.MessageName>`, so the hook can distinguish message types.

---

## Errors

Reported as diagnostics at config-processing time, pointing at the offending
attribute:

- `descriptor_set` file missing / unreadable / not a valid `FileDescriptorSet`.
- `message` names a type not present in the set.
- `mode` is neither `native` nor `json`.
- An unknown attribute in the block.

Runtime serialize/deserialize failures surface through the normal channels:
serialize errors as action/eval errors, deserialize errors via `on_decode_error`.

---

## Non-goals

- **Runtime `.proto` compilation.** Descriptor sets cover the need without pulling
  a compiler into the dependency graph.
- **Length-delimited framing / streaming.** Each transport payload is one message.
- **gRPC.** This is a wire *format*, not a transport.
