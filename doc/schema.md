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
[`vinculum man`](man.md), which renders it a topic at a time. To put parts of
it into `doc/`, see [Generated regions](#generated-regions).

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
  "default": "30s",
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
| `default` | The value used when the attribute is omitted, written as you would write it in a configuration. Absent means *no default worth stating* rather than *the zero value*: a required attribute never carries one, and neither does an optional one whose absence means "do nothing" rather than "do this instead". |
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

---

## Generated regions

Parts of `doc/` are mechanically derivable, and those parts are where the drift
has actually happened: `config.md` listed `cron` and `signals` as top-level
blocks long after they became trigger types. So a page marks the derivable
parts and keeps the rest.

```md
### `client`

<!-- vinculum:begin block-index client level=3 -->

- [`client "http"`](client-http.md) — An HTTP(S) client for making outbound requests.
- [`client "kafka"`](client-kafka.md) — A Kafka client bridging Kafka topics to the bus.
…

<!-- vinculum:end block-index client -->
```

Everything outside the markers is left byte for byte. The author chooses the
granularity per section, which matters because the granularity is genuinely
mixed: `client` wants a list of types linking out, `subscription` wants its
attribute table but not the synopsis above it.

| Region | Renders |
|---|---|
| `block-index <blocktype>` | A typed block's variants, linked to their `DocPage`s. |
| `block-body <topic path>` | A block, variant, or sub-block in full — the same content `vinculum man` shows. |
| `block-synopsis <topic path>` | The HCL skeleton alone. |
| `block-attrs <topic path>` | The attribute table, the rules governing how they combine, and the per-attribute detail. Sub-blocks are listed, not expanded. |
| `block-ctx <topic path ending in an attribute>` | The `ctx` field table for that attribute, including the fields its own site adds to an open shape. |
| `context <name>` | One `ctx` shape and the attributes evaluated against it. |

`level=<n>` sets the heading level generated headings start at, so a region
sits correctly under the hand-written heading above it. It defaults to 2.

The last four exist so a page can keep what it writes better than the generator
can. `doc/config.md`'s `subscription` section has a hand-tuned synopsis whose
inline comments say more than a generated one could, and three worked examples
the schema knows nothing about; what it should not maintain by hand is the
attribute table and the `ctx` field list, which is exactly what drifts:

```md
#### Attributes

<!-- vinculum:begin block-attrs subscription level=4 -->
<!-- vinculum:end block-attrs subscription -->

#### Action Context Variables

<!-- vinculum:begin block-ctx subscription action level=4 -->
<!-- vinculum:end block-ctx subscription action -->
```

The section regions emit no heading of their own — the hand-written heading
above the region is the section. A section that does not apply to its topic is
an error, not an empty region: `block-attrs client` fails because a typed block
has no body of its own, and an empty region under a hand-written heading would
claim the block has no attributes rather than that the region was wrong.

```
vinculum schema --format markdown                 # the whole language, on stdout
vinculum schema --format markdown --update doc/   # rewrite the regions in place
vinculum schema --format markdown --check doc/    # exit 1 if any is out of date
```

`--update` and `--check` take files or directories; a directory contributes
every `.md` under it.

A malformed marker stops the run and names the file and line: a begin with no
end, an end with no begin, a nested pair, an end naming something other than
what its begin named, or a region whose topic does not resolve — `block-body
http` is ambiguous, since `http` is both a client type and a server type.
Each of these would otherwise silently swallow or duplicate hand-written prose,
which is worse than refusing to run.

### Keeping it current

CI runs `--check` next to `--strict --require-docs`. Together they close both
directions:

- `--strict --require-docs` fails on an attribute added without documentation.
- `--check` fails on documentation that has not been regenerated.

When `--check` fails, run `--update` and commit the result. Regeneration is
idempotent, so a second run changes nothing — which is what lets `--check`
distinguish a stale document from a generator that is not deterministic.
