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
vinculum man sys starttime          # a member of a namespace
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
| `sys` | a namespace an expression may start from |
| `sys signals bynumber` | a member of one |

A type label resolves on its own wherever it is unambiguous, so
`vinculum man mqtt` and `vinculum man client mqtt` are the same page.

Attribute names are **not** addressable on their own. `action` appears in
dozens of blocks, so it is only reachable as the end of a path — which is what
[searching](#searching) is for when the block is the part you do not know.

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

### Namespaces

Where a `ctx` shape is what one expression sees at one site, a **namespace** is
what every expression sees everywhere — the names a reference may start from.
Those are topics too, and unlike a shape they have members:

```
vinculum man sys                    # what sys carries
vinculum man sys starttime          # one member of it
vinculum man sys signals bynumber   # nesting goes as deep as the value does
vinculum man http_status            # the status-code constants, with their values
```

Two things are deliberately *not* addressable here:

- **A name the language does not choose.** `env` is the environment of whichever
  process is running, and `sys.signals` carries whichever signals the host OS
  defines, so `vinculum man env HOME` is not a page. The namespace's own page
  says so, and lists the members that *are* fixed — `sys.signals.bynumber` is
  one.
- **The roots a configuration fills in.** `bus.<name>`, `var.<name>`,
  `client.<name>` and the rest are named by the blocks that declare them, so
  they are documented on the block's page: read `vinculum man bus`, not
  `vinculum man` a namespace of the same name. The schema document describes
  them all the same — see [schema.md](schema.md#namespaces).

### Functions

The callable functions are a topic too:

```
vinculum man send
vinculum man --type function assert
```

They are not on the index page — there are a couple of hundred of them, and
`help()` in the [REPL](repl.md) is the better way to browse.

A function page is laid out like a block's: the calling convention as a
synopsis, then the parameters as a table. The signature is the one `help()`
prints — both come from the same renderer — but the parameter list is a table
here rather than a fixed-width block, so it re-wraps for a narrow terminal and
becomes a real table in Markdown.

Functions whose real signature its Go type cannot express — the `get` / `set` /
`count` family, which take an *optional leading* `ctx` — carry a declaration
that says so, and it is what you see:

```
vinculum man get      → get(ctx?: ctx, thing, fallback?, *args) -> any
```

A bare name declared in two `.cty` namespaces resolves to neither, so `man`
lists the qualified names instead of reporting it missing.

---

## Searching

A path is the right way in when you know where something lives. When you have a
*word* instead — an attribute name from someone else's config, a term from an
error message — `--apropos` (`-k`) searches the whole reference and prints the
command that reads each hit:

```console
$ vinculum man -k keep_alive
2 topics match "keep_alive":

vinculum man client http disable_keep_alives
  Close each connection after a single request.
vinculum man client mqtt keep_alive
  Interval at which to send keep-alive pings.
```

It searches names and one-line summaries across everything: block types, type
variants, sub-blocks, attributes, `ctx` shapes and their fields, namespaces and
their members, and the callable functions. Matching is case-insensitive
substring, and **every** keyword must match, so a second word narrows rather
than widens:

```sh
vinculum man -k baggage          # everything about baggage
vinculum man -k baggage keys     # only where both words appear
vinculum man --type context -k topic    # only ctx shapes
```

Two things follow from how hits are addressed:

- **Every printed command works.** Where one word names topics of two kinds, the
  rows carry `--type` so each still resolves to the page it was printed for.
- **A `ctx` field names its shape**, because a field is searchable but not
  addressable. The row points at the shape and says which field matched:

  ```
  vinculum man fsm-hook
    ctx.topic_params — Named captures from matching the event's topic pattern.
  ```

A search that matches nothing exits 1 and says so on stderr. The
[REPL](repl.md) spells the same search `:apropos`.

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

`--type` narrows the search to one kind of topic — `block`, `context`,
`namespace`, or `function` — for when the ambiguity is not between paths but
between kinds. That happens where one word names things in two corpora at once:

```console
$ vinculum man assert
"assert" is ambiguous, choose one of:

    vinculum man --type block assert
    vinculum man --type function assert
```

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
| `--type <kind>` | — | Restrict to one kind of topic: `block`, `context`, `namespace`, or `function`. |
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
- [`help()`](functions.md#reflection) — the same lookups from inside an
  expression, and the natural way to use them at the [REPL](repl.md).
