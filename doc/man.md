# Reading the Reference (`vinculum man`)

`vinculum man` shows the reference documentation for one part of the
configuration language, from the binary you are running.

```
vinculum man                        # what there is to read
vinculum man var                    # the var block
vinculum man client                 # the client block, listing its types
vinculum man client mqtt            # client "mqtt" in full
vinculum man client mqtt tls        # one sub-block of it
vinculum man subscription action    # one attribute, with the ctx it sees
vinculum man message                # a ctx shape
```

It is generated from the same decode structs the parser uses, so it describes
exactly what this binary can parse. See
[Config Language Schema](schema.md) for how that works and why it can be
trusted — `man` is that document, rendered for a person instead of a program.

---

## Topics

A topic is a **path** through the language. Each element narrows the last:

| Path | Names |
|---|---|
| `subscription` | a block type |
| `client mqtt` | one type variant of a typed block |
| `client mqtt tls` | a sub-block of that variant |
| `client mqtt tls cert` | an attribute of that sub-block |
| `server mcp tool param` | nesting goes as deep as the language does |

A type label resolves on its own wherever it is unambiguous, so
`vinculum man mqtt` and `vinculum man client mqtt` are the same page.

Attribute names are **not** addressable on their own. `action` appears in
dozens of blocks, so it is only reachable as the end of a path.

### `ctx` shapes

Every attribute that is evaluated at event time sees a `ctx`, and the shape of
that `ctx` differs per attribute. The shapes have names, and each is a topic:

```
vinculum man message          # what a subscription's action sees
vinculum man decode-error     # what an on_decode_error sees
```

A shape's page lists every attribute evaluated against it. Going the other way,
an attribute's page inlines the shape it sees, so you rarely need to look one up
by name.

---

## When a name is ambiguous

Some names mean more than one thing — `http` and `vws` are each both a client
type and a server type. Rather than guessing, `man` prints the commands that
resolve the ambiguity:

```console
$ vinculum man http
"http" is ambiguous, choose one of:

    vinculum man client http
    vinculum man server http
```

A name that matches nothing gets the same treatment from the other side:

```console
$ vinculum man subscriptions
no topic named "subscriptions"

did you mean:

    vinculum man subscription
```

Both go to **stderr**, so redirecting a lookup that turns out to be ambiguous
never writes a menu into your file.

`--type` narrows the search to one kind of topic — `block` or `context` — for
the rarer case where the ambiguity is not between paths but between kinds.

---

## Output

Output going to a terminal is styled, wrapped, and paged. Output going anywhere
else is Markdown, so it can be piped or redirected as it stands:

```sh
vinculum man client mqtt            # styled and paged
vinculum man client mqtt > mqtt.md  # Markdown
vinculum man client mqtt | glow     # Markdown, rendered by something else
```

| Flag | Default | Meaning |
|---|---|---|
| `--type <kind>` | — | Restrict to one kind of topic: `block` or `context`. |
| `--format <fmt>` | `auto` | `term`, `markdown`, or `auto` (term on a terminal). |
| `--color <when>` | `auto` | `always`, `never`, or `auto`. |
| `--width <n>` | terminal's | Wrap width, clamped to 40–100. |
| `--no-pager` | | Write to stdout without invoking a pager. |
| `--config <path>` | — | Config path to search for `.vinit` plugin blocks. |
| `--plugin-path <dir>` | — | Directory of plugin `.so` files (with `--config`). |

### Environment

| Variable | Effect |
|---|---|
| `VINCULUM_PAGER` | The pager to use. Takes precedence over `PAGER`. |
| `PAGER` | The pager to use. `cat` disables paging. |
| `LESS` | Passed through. When unset and the pager is `less`, `FRX` is supplied — `-F` so a short page does not trap you in the pager. |
| `NO_COLOR` | Set to anything to disable colour. |
| `MANWIDTH` | Wrap width, if `--width` is not given. |

---

## Plugins

By default the reference describes a stock binary. To include the block types a
plugin contributes, give the config whose `.vinit` declares it together with the
directory to load from:

```sh
vinculum man --config ./configs --plugin-path /plugins client acme
```

Only the plugin bootstrap runs: no `.vcl` file is parsed and no `git` block is
materialized. See [Plugins](plugins.md).

---

## Shell completion

`vinculum man <Tab>` completes topics, and completion at each position offers
exactly what would resolve there — block types and variant names first, then
that block's attributes and sub-blocks. Install it the usual way:

```sh
source <(vinculum completion bash)     # or zsh, fish, powershell
```

---

## See also

- [Config Language Schema](schema.md) — the machine-readable form of the same
  document, for editors and other tooling.
- [Configuration Language](config.md) — the hand-written guide: HCL syntax,
  the evaluation namespace, and block ordering.
