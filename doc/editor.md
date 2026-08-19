# Editor Blocks

`editor` blocks compile into callable functions that perform structured text editing.
They are processed early during configuration loading — alongside `function` and `jq`
blocks — because the functions they produce must be available for the rest of the config.

The initial editor type is `"line"`, which edits a file (or string) line by line using
ordered regex match-and-replace rules.

```hcl
editor "line" "name" {
    params = [param1, param2]

    match "<regex>" {
        when    = expr
        replace = expr
    }
}
```

<!-- vinculum:begin block-attrs editor line level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `backup` | string |  |  | Suffix for a hard-link backup of the original file. |
| `create_if_absent` | bool |  |  | Treat a missing file as empty rather than an error. |
| `lock` | bool |  |  | Take an exclusive lock on the file for the duration of the edit. |
| `mode` | string |  | `"file"` | Whether the function edits a file or a string. |
| `params` | expression |  |  | Names of the parameters the compiled function takes. |
| `state` | expression |  |  | Initial values for the state accumulated across lines. |
| `variadic_param` | expression |  |  | Name for a parameter collecting any extra arguments. |

**`backup`**

For example `"~"` keeps the previous contents as `file~`. File mode only.

**`create_if_absent`**

File mode only.

**`lock`**

File mode only.

**`mode`**

File mode edits a file on disk and returns whether it was written; it requires `--write-path`, resolves relative paths against it, and rejects paths outside it. String mode processes its argument in memory and returns the result, with `backup`, `create_if_absent`, `lock`, and the path restrictions not applying.

One of: `file`, `string`.

**`params`**

Written as bare identifiers, e.g. `params = [host, port]`. They come after the target argument: `params = [a, b]` yields `name(ctx, target, a, b)`. Each is in scope in every expression in the block.

**`state`**

An object. `update_state` on a match rule merges into it, and every expression in the block reads it as `state.<name>`.

**`variadic_param`**

A bare identifier. Arguments beyond the declared `params` are gathered into it as a list.

### Blocks

- `after` (optional) — Content appended to the output.
- `before` (optional) — Content prepended to the output.
- `match "<pattern>"` (0..n) — One match-and-replace rule.

<!-- vinculum:end block-attrs editor line -->

---

## Generated Function

An `editor "line" "foo"` block with `params = [a, b]` defines the function:

```
foo(ctx, filename, a, b) → bool
```

Or in string mode (`mode = "string"`):

```
foo(ctx, input, a, b) → string
```

With `variadic_param = rest`, extra positional arguments are collected into `rest`.

---

## Modes

### `mode = "file"` (default)

Edits a file on disk. Requires the `--write-path` flag (`-w`) to be set — any
`editor "line"` block without `mode = "string"` is a configuration error if `--write-path`
is absent.

- `filename` is the path to the file to edit. Relative paths are resolved against the
  `--write-path` directory. Absolute paths must fall within that directory. `~` expansion
  is not permitted. See also `sys.writepath` in [config.md](config.md).
- Returns `true` if the file was written, `false` if no effective change was made or the
  edit was aborted cleanly.

### `mode = "string"`

Operates on a string in memory. The function signature becomes:

```
foo(ctx, input, a, b) → string
```

The input string is processed through the same match/replace rules, and the result is
returned as a string. `backup`, `create_if_absent`, `lock`, and path-restriction semantics
do not apply. The `--write-path` flag is not required.

---

## Match Blocks

The label is a Go RE2 regular expression. Match rules are evaluated in **declaration
order**: the first rule whose guards pass and whose regex matches wins for that line.

<!-- vinculum:begin block-attrs editor line match level=3 -->

| Attribute | Type | Required | Default | Description |
|---|---|---|---|:---|
| `abort` | expression (predicate-expression) |  |  | When true, discard the whole edit immediately. |
| `incidental` | expression |  |  | Don't let this rule's replacement count as a change on its own. |
| `max` | expression |  |  | Stop applying this rule after this many matches. |
| `replace` | expression |  |  | The output for this line. |
| `required` | expression |  | `0` | This rule must match at least this many lines. |
| `update_state` | expression |  |  | Object merged into the running state after this match. |
| `when` | expression (predicate-expression) |  |  | Guard evaluated after the regex matches; skip the rule if false. |

**`abort`**

Returns false in file mode, an error in string mode. For when a match shows the edit is unnecessary rather than wrong.

Evaluated against the `editor-match` context.

**`incidental`**

The replacement still happens. If every modification in the whole edit was incidental, the file is not written and the function returns false — so housekeeping edits like a timestamp bump ride along with a real change without causing a write by themselves. Evaluated once at config load, not per line.

**`max`**

Unlimited when omitted. Further lines that would have matched fall through to later rules instead, so `required = 1, max = 1` means the pattern must match exactly once. Evaluated once at config load, not per line.

**`replace`**

Should end with `\n`. Absent, the line is written unchanged but still counts toward `required`. `""` deletes the line; `"${ctx.line}extra\n"` inserts after it; `error("...")` aborts with an error.

Evaluated against the `editor-match` context.

**`required`**

Otherwise the edit is abandoned cleanly: the file is left alone and the function returns false. `required = true` means 1. Evaluated once at config load, not per line.

**`update_state`**

Evaluated after `replace` and `abort`. Keys it does not mention are left as they were, and later rules see the result.

Evaluated against the `editor-match` context.

**`when`**

Matching continues with the next rule. The full match context is in scope, so the guard can inspect capture groups — `ctx.count` reflects the count this match *would* have if the guard passes.

Evaluated against the `editor-match` context.

<!-- vinculum:end block-attrs editor line match -->

A `when` guard is what lets one rule's regex be broad while the rule itself is
narrow — the full match context is in scope, so the guard can inspect capture
groups:

```hcl
# Match any A record but only act on the one for the target host
match "^(\\S+)(\\s+(?:IN\\s+)?A\\s+)\\S+" {
    when    = ctx.groups[1] == recordname
    replace = "${ctx.groups[1]}${ctx.groups[2]}${ipaddr}\n"
}
```

`replace` is where a rule earns its keep, and the whole line's output is whatever
it produces:

- `replace = "${ctx.groups[1]}: ${value}\n"` — replace the line
- `replace = ""` — delete the line
- `replace = "inserted\n${ctx.line}"` — insert a line before
- `replace = "${ctx.line}inserted\n"` — insert a line after
- `replace = error("message")` — abort with an error

---

## Context Variables in Expressions

Alongside `ctx`, every expression in an editor block also sees `state` and the
editor's declared `params` as top-level variables. Those are not `ctx` fields,
so they do not appear in the tables below.

### Inside `when`, `replace`, `abort`, and `update_state`

<!-- vinculum:begin context editor-match level=4 -->

Evaluated for a line the rule's regex matched.

`state.<name>` and the editor's declared params are also in scope.

Fields readable as `ctx.<name>` (shape `editor-match`):

| Field | Type | Description |
|---|---|:---|
| `ctx.line` | string | The original line, including its trailing newline. |
| `ctx.lineno` | number | 1-based line number in the input. |
| `ctx.filename` | string | Resolved absolute path of the file. |
| `ctx.groups` | list | Regex capture groups. |
| `ctx.named` | map | Named capture groups, from `(?P<name>...)`. |
| `ctx.count` | number | How many times this rule has matched, including this line. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.filename`**

Empty in string mode.

**`ctx.groups`**

`ctx.groups[0]` is the whole match, `ctx.groups[1]` the first group. Empty when the pattern has no groups.

**`ctx.named`**

Empty when the pattern has none.

**`ctx.count`**

1 on the first match. In `when`, the count this match *would* have if the guard passes.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

##### Evaluated by

- `editor "line"` › `match` › `when`
- `editor "line"` › `match` › `replace`
- `editor "line"` › `match` › `abort`
- `editor "line"` › `match` › `update_state`

<!-- vinculum:end context editor-match -->

### Inside `before` and `after`

<!-- vinculum:begin context editor-content level=4 -->

Evaluated once, after every line has been processed.

There is no line in scope. `state.<name>` holds the final accumulated state, and the editor's declared params are in scope too.

Fields readable as `ctx.<name>` (shape `editor-content`):

| Field | Type | Description |
|---|---|:---|
| `ctx.filename` | string | Resolved absolute path of the file. |
| `ctx.auth` | object | The authenticated identity, or null. *(every `ctx` carries this)* |
| `ctx.baggage` | capsule | OpenTelemetry baggage riding with this context. *(every `ctx` carries this)* |
| `ctx.trace_id` | string | Trace ID of the active span, or empty. *(every `ctx` carries this)* |
| `ctx.span_id` | string | Span ID of the active span, or empty. *(every `ctx` carries this)* |

**`ctx.filename`**

Empty in string mode.

**`ctx.auth`**

Set when the request was authenticated: `username`, `subject`, `claims`, and `method` naming the mechanism. Null on a route that allows unauthenticated requests, and everywhere the event did not arrive over an authenticated path — so a route accepting both branches on `ctx.auth == null`. See [the auth block](auth.md).

**`ctx.baggage`**

Read, write, and delete with `get()`, `set()`, and `clear()`. Changes are seen by later `send()` and `http::*()` calls on the same context. See [the baggage reference](baggage.md).

**`ctx.trace_id`**

Falls back to the trace ID extracted from inbound headers, so it is populated even with no `client "otlp"` configured.

##### Evaluated by

- `editor "line"` › `after` › `content`
- `editor "line"` › `before` › `content`

<!-- vinculum:end context editor-content -->

The universal fields come from the context passed as the editor function's first
argument, so an editor called from an HTTP handler can log against the request's
trace, or make an edit that depends on who is asking.

> **Changed in 0.45.0.** These four were missing: the editor built its context
> object directly instead of the way every other evaluation site does, so
> `ctx.trace_id` and friends were an "unsupported attribute" error even though
> the caller's context carried them.

---

## State Variables

The `state` attribute declares initial values for state variables that accumulate during
line processing. State is an object; `update_state` in a match block merges new values
into it. All expressions — `when`, `replace`, `abort`, `update_state`, `before`, and
`after` — can read the current state as `state.<name>`.

```hcl
editor "line" "add_header" {
    params = [program]

    state = {
        found = false
    }

    # If a managed header is already there, mark it found and remove it
    match "^# Managed by " {
        replace      = ""
        update_state = { found = true }
    }

    # Prepend the header (runs after all lines are processed; state.found is final)
    before {
        content = "# Managed by ${program}\n"
    }
}
```

Because `before` has access to the **final** accumulated state, it can reference values
set during line processing. Internally, this uses a two-pass mechanism: the body and
`after` content are written to a temporary file first, then `before` content (evaluated
with final state) is prepended atomically.

---

## `before` and `after` Blocks

`before` content goes ahead of every input line and `after` content follows them,
but both are *evaluated* at the same moment: once, after every line has been
processed, so each sees the final accumulated `state`. Neither sees line-specific
context — there is no `ctx.line` or `ctx.groups` by then.

The two blocks share a body:

<!-- vinculum:begin block-attrs editor line before level=3 -->

| Attribute | Type | Required | Description |
|---|---|---|:---|
| `content` | expression | yes | The text to add. |
| `incidental` | expression |  | Don't let this content count as a change on its own. |

**`content`**

Evaluated once, after every line has been processed, so it sees the final accumulated `state`.

Evaluated against the `editor-content` context.

**`incidental`**

As on a `match` rule: if every modification in the edit was incidental, nothing is written. Evaluated once at config load.

<!-- vinculum:end block-attrs editor line before -->

Content should end with `\n` for proper line termination.

---

## `backup`, `create_if_absent`, and `lock`

**`backup`**: When set, the original file is hard-linked to `<path><suffix>` before the
atomic rename. Using a hard link keeps the original filename present throughout, so there
is no window during which the target is absent.

```hcl
backup = "~"    # /etc/zones/example.com → /etc/zones/example.com~
backup = ".bak" # /etc/zones/example.com → /etc/zones/example.com.bak
```

Only applies when the file exists and was modified.

**`create_if_absent`**: When `true`, a missing file is treated as empty (zero lines).
The function proceeds normally and creates the file if the output is non-empty. When
`false` (the default), a missing file is an error.

**`lock`**: When `true`, an exclusive `flock(2)` is acquired on a sibling `.lock` file
(e.g. `/etc/zones/example.com.lock`) before the edit begins, and released automatically
when the function returns. This serializes concurrent invocations — useful when multiple
processes or webhook handlers may edit the same file simultaneously.

The lock file is created on first use and left on disk (it is empty and harmless).
Locking is advisory: other processes that do not use `lock = true` are not excluded.

Works on local filesystems and NFSv4 (including AWS EFS). On NFSv3, the lock manager
(NLM) may leave stale locks after a server reboot; NFSv4+ handles this via lease expiry.
`lock` is not supported on non-Unix platforms and will return an error if attempted there.

---

## Return Values

**File mode** (`bool`):

| Condition | Return |
|---|---|
| File was written (changed) | `true` |
| Output identical to input | `false` |
| A `required` constraint not met | `false` |
| An `abort` expression fired | `false` |
| `replace = error(...)` in a match | propagated error |
| File I/O error | propagated error |
| File not found and `create_if_absent = false` | propagated error |
| Path outside permitted write directory | propagated error |

**String mode** (`string`): Returns the processed string. If an `abort` expression
fires, an error is propagated rather than returning a value.

---

## Full Example

A webhook handler that updates a DNS zone file — replacing the A record for a named host
and incrementing the SOA serial number:

```hcl
editor "line" "update_zone_record" {
    params = [recordname, ipaddr]

    # Update the SOA serial: matches "        2024010101 ; Serial"
    match "^(\\s*)(\\d{10})(\\s*;\\s*[Ss]erial)" {
        required = 1
        replace  = "${ctx.groups[1]}${dns::next_zone_serial(ctx.groups[2])}${ctx.groups[3]}\n"
    }

    # Replace the A record for the named host: matches "www    IN A    1.2.3.4"
    # when = ... filters to just the target host; other A records pass through unchanged.
    match "^(\\S+)(\\s+(?:IN\\s+)?A\\s+)\\S+" {
        required = 1
        when     = ctx.groups[1] == recordname
        replace  = "${ctx.groups[1]}${ctx.groups[2]}${ipaddr}\n"
    }
}

server "http" "webhook" {
    listen = ":8443"

    handle "POST /dns/update/{name}" {
        action = respond(
            update_zone_record(ctx, "/etc/bind/zones/example.com",
                               ctx.request.vars.name, ctx.request.remote_ip)
                ? 200
                : 409,
            ""
        )
    }
}
```

---

## String Mode Example

Transform a message payload by normalizing hostnames:

```hcl
editor "line" "normalize_hosts" {
    mode   = "string"
    params = [canonical]

    match "^Host:\\s*(\\S+)" {
        replace = "Host: ${canonical}\n"
    }
}

subscription "inbound" {
    bus    = bus.main
    topics = ["requests/#"]
    action = send(ctx, bus.processed, ctx.topic,
                  normalize_hosts(ctx, ctx.msg.body, "example.com"))
}
```

---

## State Accumulation Example

Count occurrences of a pattern and append a summary:

```hcl
editor "line" "count_errors" {
    mode = "string"

    state = {
        n = 0
    }

    match "ERROR" {
        update_state = { n = state.n + 1 }
    }

    after {
        content = "\n# ${state.n} error(s) found\n"
    }
}
```
