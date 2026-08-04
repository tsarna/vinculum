# Config Language Schema (`vinculum schema`)

`vinculum schema` prints a machine-readable description of the whole
configuration language: every block type, every type-specific variant
(`client "http"` versus `client "mqtt"`), every attribute and nested
sub-block, plus prose documentation, value hints, and semantic constraints.

```
vinculum schema                                    # pretty JSON to stdout
vinculum schema -o schema.json                     # write to a file
vinculum schema --pretty=false                     # compact, one line
vinculum schema --strict --require-docs -o /dev/null   # check, don't consume
```

It is meant for tooling that needs to know what Vinculum accepts without
reimplementing the parser: editor completion and hover, linting, and
generated reference documentation.

To read the same description yourself rather than feed it to a program, use
[`vinculum man`](man.md), which renders it a topic at a time.

---

## Why it can be trusted

The structure is **reflected from the same decode structs the parser uses**.
A block's attributes, their required-ness, and its nested blocks are read out
of the `hcl` struct tags that `gohcl` decodes, so the document describes
exactly what the binary in your hands can parse — not a hand-maintained
description of what it was believed to parse when someone last looked.

Prose, hints, and constraints cannot be reflected, so they are written by
hand — but they are written *next to the struct they describe*, and validated
against it. Documenting an attribute that does not exist is an error, and so
is adding an attribute without documenting it. `--strict --require-docs`
turns both into a non-zero exit, and CI runs it on every change.

Two consequences worth knowing:

- **The output tracks the binary.** Ask the binary you are running, or take
  the `schema.json` attached to that release.
- **Plugins are loaded only if you ask.** By default the document describes a
  stock binary. See [Plugins](#plugins).

---

## Getting the document

| Source | When to use it |
|---|---|
| `vinculum schema` | Whatever binary you have, including a local build |
| `schema.json` on the [GitHub Release](https://github.com/tsarna/vinculum/releases) | A specific published version, without installing it |

A consumer typically vendors one copy and records which version it targets,
then bumps it deliberately — the diff is the list of language changes between
those two versions, which is worth reading.

---

## Flags

| Flag | Default | Effect |
|---|---|---|
| `--format` | `json` | Output format. Only `json` today. |
| `--pretty` | `true` | Indent the JSON. `--pretty=false` for one line. |
| `-o`, `--output` | — | Write to a file instead of stdout. |
| `--strict` | `false` | Exit non-zero if curated metadata does not match the reflected structure. |
| `--require-docs` | `false` | With `--strict`, also require everything to be documented. |
| `--plugin-path` | — | Directory of plugin `.so` files. Requires config paths — see [Plugins](#plugins). |

Problems are always printed to stderr; `--strict` decides whether they stop
the command. `--require-docs` without `--strict` is a usage error.

**Exit codes:** `0` success, `1` validation failure under `--strict`, `2`
usage or I/O error.

---

## Plugins

By default no plugins are loaded, so the document describes a stock binary and
a plugin's block types are absent. Give the config paths whose `.vinit` files
declare the plugins, together with `--plugin-path`:

```
vinculum schema --plugin-path /plugins ./configs/
```

Only the plugin bootstrap runs — `git` blocks are not materialized and no
`.vcl` file is parsed. The paths exist solely to find `plugin` blocks, so
passing one without the other is a usage error rather than a quietly
stock-binary document.

The registry entries the plugins contributed then appear at the top level:

```json
"plugins": ["client.acme", "functions.acme"]
```

The key is absent when no plugins were loaded, which is how a consumer tells
the two kinds of document apart. A plugin that registers a block type without
passing `config.WithSchema` still appears here, but its variant carries
`"undocumented": true` and only the block's common attributes — the rest of
its structure is reflected from a decode struct the plugin never supplied. See
[plugins.md](plugins.md).

---

## Output format

```json
{
  "schemaVersion": "1",
  "vinculumVersion": "0.44.0",
  "blocks": {
    "subscription": { ... },
    "client": { ... }
  }
}
```

`schemaVersion` versions **the format**; `vinculumVersion` versions **the
content**. See [Versioning](#versioning) below. A `plugins` key is present only
when plugins were loaded — see [Plugins](#plugins).

### Blocks

`blocks` maps a top-level block type name to its description. A block comes in
one of two shapes, told apart by the presence of `variantLabel`.

A **plain** block has a single body:

```json
"subscription": {
  "labels": ["name"],
  "summary": "Subscribes to messages from a bus or client.",
  "doc": "...",
  "attributes": [ ... ],
  "blocks": { ... },
  "constraints": [ ... ]
}
```

A **typed** block — one whose first label selects a variant — has no body of
its own, and carries a map of variants instead:

```json
"client": {
  "labels": ["type", "name"],
  "variantLabel": "type",
  "summary": "A connection to an external service.",
  "variants": {
    "mqtt":  { "summary": "...", "attributes": [ ... ] },
    "kafka": { "summary": "...", "attributes": [ ... ] }
  }
}
```

Attributes the block's handler decodes before dispatching to the variant —
`disabled`, and `tracing` on a trigger — are **spliced into every variant**,
because that is where a config author writes them.

### Attributes

`attributes` is a list, in declaration order. Showing every field an entry can
carry — most attributes have only some of them, and the optional ones are
omitted rather than emitted empty:

```json
{
  "name": "topics",
  "required": true,
  "type": "list",
  "summary": "Topic patterns to subscribe to.",
  "doc": "...",
  "hint": "topic-pattern",
  "context": "message",
  "enum": ["one", "many"],
  "deprecated": "Use `subscriber` instead."
}
```

| Field | Notes |
|---|---|
| `required` | The author's intent: no `,optional` on the tag. Note that HCL itself treats a pointer or expression field as optional and leaves the requirement to the block's own processor, which reports it. |
| `type` | Coarse: `expression`, `string`, `bool`, `number`, `list`, or `map`. |
| `hint` | What kind of value belongs here — see below. |
| `context` | Names the `ctx` shape an expression here sees. |
| `contextFields` | Fields this site adds to that shape, for an open shape only — see below. |
| `enum` | The accepted values, when there is a fixed set. |
| `deprecated` | Present only when the attribute is deprecated; the text says what to use instead. |

### Hints

A hint says what a slot *means*, which is what completion needs and what
`type` cannot express. The most consequential distinction is among the kinds
of expression, because they differ in when they run and what is in scope:

| Hint | Meaning |
|---|---|
| `expression` | Evaluated once, at config load, for its value. |
| `action-expression` | Evaluated per event, for its side effects, with a `ctx` named by `context`. |
| `predicate-expression` | A boolean gate — `skip_when`, `stop_when`, a guard. |
| `reactive-expression` | Re-evaluated whenever a watchable it references changes. |
| `transform-pipeline` | A list from the transform DSL, whose functions exist only in this slot. |

The rest name what a value refers to or how it is written: `subscriber-ref`,
`bus-ref`, `client-ref`, `server-ref`, `metric-ref`, `var-ref`,
`tracing-ref`, `metrics-ref`, `topic-pattern`, `cron-expr`, `duration`,
`url`, `listen-addr`, `bool`.

### Contexts

An attribute whose expression runs at event time carries a `context` naming
the shape of `ctx` it sees, and the document's top-level `contexts` map says
what is in each one:

```json
"contexts": {
  "message": {
    "summary": "Evaluated once per message delivered.",
    "fields": [
      {"name": "topic", "type": "string", "summary": "Topic the message was delivered on."},
      {"name": "msg",   "type": "dynamic", "summary": "The message payload."},
      {"name": "fields", "type": "object", "summary": "String metadata attached to the message."},
      {"name": "auth",  "type": "object", "summary": "…", "universal": true}
    ]
  }
}
```

`ctx` is assembled per evaluation site, so the shape varies by **attribute**,
not by block: a receiver's `action` sees the message, while `on_connect` on the
same client sees no message at all and `on_decode_error` sees the failure
instead.

| Field | Meaning |
|---|---|
| `type` | The attribute vocabulary plus `object`, `dynamic` (type follows the data), and `capsule` (an opaque handle passed to a function rather than read directly). |
| `optional` | Absent from some evaluations of the same shape — a condition's `on_init` reports a starting state, so it has no `ctx.old_value`. |
| `universal` | Carried by every `ctx`: `auth`, `baggage`, `trace_id`, `span_id`. Listed last, and present in every shape without exception. |

A shape may have no fields of its own — `connection` is just the universal
four, because nothing is in flight when a connection opens or closes.

Unlike the rest of the document these field lists are hand-written: a `ctx` is
built by imperative Go, so there is nothing to reflect. What *is* checked is
that the two halves agree — naming a shape nothing describes, or describing one
nothing names, is a reported problem.

#### Open shapes

A shape marked `"openFields": true` lists what every site carries, not the whole
list: individual sites carry more, and each says which in its attribute's
`contextFields`.

```json
{
  "name": "on_decode_error",
  "context": "decode-error",
  "contextFields": [
    {"name": "routing_key", "type": "string", "summary": "Routing key the message was delivered with."}
  ]
}
```

Read those as appended to the shape's own fields, for that attribute only.
`decode-error` is the case: the five fields describing the failure are the same
everywhere, and then the receiver adds the identity of its transport —
`routing_key` on rabbitmq, `mqtt_topic` on mqtt, `stream` and `entry_id` on a
redis stream. Treat an unlisted field on an open shape as unknown-but-possible
rather than as an error.

A site's field may not shadow one the shape already declares — including a
universal — because at runtime the fixed field wins and the site's value is
dropped. Trying to declare one is a reported problem, and the field is left out
of the document.

### Nested blocks

`blocks` maps a sub-block name to its header plus its body:

```json
"receiver": {
  "labels": ["name"],
  "repeatable": true,
  "required": false,
  "summary": "...",
  "attributes": [ ... ],
  "blocks": { ... }
}
```

`labels` are the block's label names in order, and they nest to any depth.

### Constraints

Rules a body must satisfy that its structure cannot express:

```json
{ "kind": "mutually_exclusive", "attributes": ["action", "subscriber"],
  "message": "Specify at most one of action or subscriber." }
```

`kind` is `mutually_exclusive`, `at_least_one_of`, `required_together`, or
`requires` (where the first attribute needs the rest). Only constraints the
parser actually enforces are listed — a rule that is value-sensitive, such as
a counter's `window` versus `rollover = true`, is described in the
attribute's `doc` instead of stated as a constraint that would fire on legal
configuration.

### Other flags on a body

| Field | Meaning |
|---|---|
| `conditional` | The variant's availability depends on config state. It is described as part of the superset — the schema says what *can* exist. `trigger "file"` needs a base directory, for instance. |
| `freeAttributes` | Attribute names here are chosen by the config author, not fixed by the parser, so an unknown name is not an error. `const` and an `fsm` block's `storage` are the cases. |
| `undocumented` | No curated description was registered. Never present for an in-tree type, since CI rejects it — only for a plugin type registered without `WithSchema`. |

---

## Versioning

`schemaVersion` is bumped on any breaking change to the shape of this
document. `vinculumVersion` is the version of the binary that produced it,
so it changes with every release whose language surface changed.

While Vinculum is pre-1.0 the format may still change; `schemaVersion` exists
so a consumer can notice rather than misparse.

---

## Adding to the schema

Adding an `hcl` field to a decode struct and nothing else fails the build:

```
bus.new_thing: missing summary
```

The fix is to describe it where the struct lives, in that block's
`TypeSchema`:

```go
var busSchema = cfg.TypeSchema{
    Sample: &BusDefinition{},
    Attrs: map[string]cfg.AttrMeta{
        "new_thing": {
            Summary: "One line, sentence case, ending in a period.",
            Doc:     "The detail that does not fit in the summary.",
            Hint:    cfg.HintDuration,
        },
    },
}
```

The reverse also fails: documenting an attribute the struct does not have
reports `documented attribute "x" does not exist`. Renaming a field is
therefore a two-sided change, which is the point.

A block type registers its schema through the same call that registers its
processor:

```go
cfg.RegisterClientType("mqtt", process, cfg.WithSchema(mqttClientSchema))
```

Plugins use the identical call — see [plugins.md](plugins.md) — though
`vinculum schema` does not load plugins, so a plugin's schema is visible only
to a binary that has already loaded it.

### `DocPage`

Every type also names its hand-written reference page, relative to `doc/`:

```go
var mqttClientSchema = cfg.TypeSchema{
    Sample:  &MQTTClientDefinition{},
    Summary: "An MQTT client bridging an MQTT broker to the bus.",
    DocPage: "client-mqtt.md",
    // …
}
```

A type documented in a section of a shared page names the section too —
`"client-sql.md#client-sqlite-name"`, `"trigger.md#trigger-cron"`.

It exists because the generated indexes have to link somewhere, and deriving a
link target by convention is exactly the kind of thing that rots in silence:
the page is renamed and the index keeps pointing at where it used to be. A test
checks that every `DocPage` names a file that exists and, where it carries a
fragment, a heading that is actually in that file. It is required for every
variant of a typed block, which is what the per-type indexes are made of.

The same value is what `vinculum man` prints at the foot of a page, so a reader
who wants the worked examples and the reasoning — the two things the schema
cannot carry — is told where they are.
