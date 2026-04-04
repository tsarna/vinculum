# Editor Blocks

`editor` blocks compile into callable functions that perform structured text editing.
They are processed early during configuration loading — alongside `function` and `jq`
blocks — because the functions they produce must be available for the rest of the config.

The initial editor type is `"line"`, which edits a file (or string) line by line using
ordered regex match-and-replace rules.

```hcl
editor "line" "name" {
    params         = [param1, param2]  # optional — declared parameter names
    variadic_param = rest              # optional — collects extra args into a list

    mode            = "file"           # optional — "file" (default) or "string"
    backup          = "~"              # optional — suffix for hard-link backup
    create_if_absent = false           # optional — treat missing file as empty

    state = {                          # optional — initial state variable values
        count = 0
        last  = ""
    }

    before {                           # optional — content prepended to output
        content = expr
    }

    match "<regex>" {
        required     = 1               # optional — minimum required matches
        max          = 1               # optional — stop matching after n occurrences
        when         = expr            # optional — guard; skip rule if falsy
        replace      = expr            # optional — replacement text
        abort        = expr            # optional — discard and return false/error if truthy
        update_state = expr            # optional — merge into state after this match
    }

    # additional match blocks...

    after {                            # optional — content appended to output
        content = expr
    }
}
```

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
returned as a string. `backup`, `create_if_absent`, and path-restriction semantics do
not apply. The `--write-path` flag is not required.

---

## Match Blocks

```hcl
match "<regex>" {
    required     = n     # optional; default 0
    max          = n     # optional; default unlimited
    when         = expr  # optional
    replace      = expr  # optional
    abort        = expr  # optional
    update_state = expr  # optional
}
```

The label is a Go RE2 regular expression. Match rules are evaluated in **declaration
order**: the first rule whose guards pass and whose regex matches wins for that line.

**`required`**: This rule must match at least `n` lines, or the edit is aborted cleanly
(returns `false`, temp file discarded). `required = true` is eqivalent to `required = 1`.

**`max`**: The rule stops matching after `n` occurrences. Lines that would have matched
are passed to subsequent rules instead (or copied unchanged if no later rule matches).
`required = 1, max = 1` means the pattern must match exactly once.

**`when`**: A guard expression evaluated before the regex. If falsy, the rule is skipped
for that line. Only `ctx.filename`, `ctx.lineno`, `state`, and function parameters are in
scope — the match has not yet occurred.

**`replace`**: An expression evaluated to produce the output for this line. If absent, the
original line is written unchanged (but the match is still counted for `required`).

- `replace = "${ctx.groups[1]}: ${value}\n"` — replace the line
- `replace = ""` — delete the line
- `replace = "inserted\n${ctx.line}"` — insert a line before
- `replace = "${ctx.line}inserted\n"` — insert a line after
- `replace = error("message")` — abort with an error

Replacements should end with `\n` for proper line termination.

**`abort`**: If truthy, immediately discard the temp file and return `false` (file mode)
or an error (string mode). Useful when a match indicates the edit is unnecessary or
invalid without being an error condition. Has the same expression context as `replace`.

**`update_state`**: An object expression merged into the running state after `replace`
and `abort` have been evaluated. Keys not present in the expression are preserved
unchanged. State is then available to subsequent rules via `state.<name>`.

---

## Context Variables in Expressions

### Inside `replace`, `abort`, and `update_state`

| Variable | Description |
|---|---|
| `ctx.line` | The original line, including its trailing newline |
| `ctx.lineno` | 1-based line number in the original file |
| `ctx.filename` | Resolved absolute path of the file (empty in string mode) |
| `ctx.groups` | List of regex capture groups (`ctx.groups[0]` = full match, `ctx.groups[1]` = first group, etc.) |
| `ctx.named` | Map of named capture groups (`(?P<name>...)` syntax) |
| `ctx.count` | Number of times this rule has matched so far, including this line (1 on the first match) |
| `state` | Current accumulated state object |
| `<param>` | Declared function parameters, by name |

### Inside `when`

Same as above, except `ctx.groups`, `ctx.named`, `ctx.count`, and `ctx.line` are not
available (the match has not yet occurred).

### Inside `before` and `after`

| Variable | Description |
|---|---|
| `ctx.filename` | Resolved absolute path of the file (empty in string mode) |
| `state` | Final accumulated state (all rules have been processed) |
| `<param>` | Declared function parameters, by name |

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

**`before`**: Content written before any input lines in the output. Evaluated once, after
all lines have been processed (so that `state` reflects accumulated values from the entire
file). Content must end with `\n` for proper line termination.

**`after`**: Content written after all input lines in the output. Evaluated once, after
all lines have been processed.

Both `before` and `after` have access to final state and function parameters, but not to
line-specific context (`ctx.line`, `ctx.groups`, etc.).

---

## `backup` and `create_if_absent`

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
        replace  = "${ctx.groups[1]}${nextzoneserial(ctx.groups[2])}${ctx.groups[3]}\n"
    }

    # Replace the A record for the named host: matches "www    IN A    1.2.3.4"
    match "^(${recordname}\\s+(?:IN\\s+)?A\\s+)\\S+" {
        required = 1
        replace  = "${ctx.groups[1]}${ipaddr}\n"
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
