# Interactive REPL

The `-i` / `--interactive` flag on `vinculum serve` starts the server normally
and then, instead of waiting for a termination signal, drops you into an
interactive read-eval-print loop (REPL). At the prompt you type VCL
**expressions** and see them evaluated against the **live, running**
configuration. Exiting the REPL shuts the server down.

```sh
vinculum serve -i config.vcl
vinculum serve --interactive ./configs/
vinculum serve -i                      # no config — explore ambient values and built-in functions
```

Unlike a normal `serve`, interactive mode does not require a config file or
directory: `vinculum serve -i` with no arguments starts an empty environment,
which is handy for exploring `env.*`, built-in functions, and expression syntax.

Startup is identical to a normal `serve` — the same config is parsed, built, and
started, and all servers, clients, triggers, and subscriptions are live. The
prompt is simply a window into that running instance: every expression is
evaluated in the same context a subscription action uses, so `bus.*`, `server.*`,
`client.*`, `env.*`, the full function library, and a working `ctx` are all
available.

Because evaluation happens against the genuine runtime, the REPL is useful for:

- **Exploration** — read live state without editing config
  (`get(condition.fault)`, `length(...)`, `jsonencode(server.api)`).
- **Stimulus** — drive the system by hand (`send(...)`, `publish(...)`) to test
  subscriptions, transforms, and state machines interactively.
- **Debugging** — see exactly what a function or expression returns, with real
  diagnostics, before committing it to a `.vcl` file.

> Interactive mode requires a terminal on stdin and stdout. If stdin is not a
> terminal (piped or redirected), `serve -i` exits immediately with an error.

## Evaluating expressions

Type any VCL expression and press Enter:

```
1> 1 + 1
2
2> upper("hello")
"HELLO"
3> {name = "ada", roles = ["admin", "dev"], active = true}
{
  active = true
  name   = "ada"
  roles  = ["admin", "dev"]
}
```

Results print in HCL/VCL style — the way you would write the value in a `.vcl`
file. Rich runtime objects that have no literal form (buses, servers, clients,
`bytes`, …) print a short typed summary instead. A multi-line string prints as a
heredoc, so that its lines read as lines.

## Finding your way around: `help()`

`help()` with no argument lists every function available; `help("name")` explains one
— its signature, what it does, and what each parameter is for. `doc("name")` returns
just the description. See [functions.md](functions.md#reflection).

```console
1> help("count")
<<EOT
count(ctx?: ctx, thing) -> number

How many times something has happened — a counter's running total. Distinct from
length(), which is how many elements a collection holds.

Parameters:
  ctx?   request context
  thing  the thing to count (must be Countable)
EOT
```

The signature shown is the function's real one, which is not always the one its cty
type can express — `count` above genuinely takes an optional *leading* `ctx`, which a
cty signature has no way to say.

The number in the prompt is the index the **next** result will be bound to: at
`3>` a successful, non-null result becomes `_3` (see
[Result history](#result-history-_-and-_1--_n) below). Inputs that don't produce
a numbered result — errors, top-level `null`, meta-commands — leave the number
unchanged, so the prompt is a stable index into your scrollback.

Side effects are **real**. Calling `send()` actually publishes to the running
bus; there is no confirmation prompt. This is a live REPL by design.

```
4> send(ctx, bus.main, "sensors/test", {temp = 21})
true
```

### Result history: `_` and `_1` … `_N`

Every successful, value-producing evaluation is numbered and can be referenced
later. `_` is the most recent result; `_1`, `_2`, … are the individual results
in order:

The prompt index tells you, in advance, which `_N` a result will get:

```
1> jsondecode(file("data.json"))
{ ... }                          # _1
2> _.users
[ ... ]                          # _2  (_ was _1 here)
3> length(_)
3                                # _3
4> _1.meta
{ ... }                          # earlier results stay addressable
```

Evaluations that fail, or that produce a top-level `null`, are **not** numbered
and leave `_` unchanged (and the prompt number does not advance). This keeps the
history focused on meaningful values and keeps side-effecting calls quiet:

```
5> log::info("checkpoint", {})    # returns null → nothing printed, _ unchanged
5>
```

## Session bindings

Stash an intermediate value under a name with a bare assignment, or the explicit
`:set` form:

```
1> users = jsondecode(file("users.json"))
[ ... ]
2> :set admins = [for u in users : u if u.role == "admin"]
[ ... ]
3> length(admins)
1
```

- A bare `NAME = EXPR` is distinguished from a comparison: `x == 1` is an
  expression, `x = 1` is an assignment.
- The right-hand side is evaluated in the current context, so it can reference
  prior bindings, `_`, and `_N`.
- Reserved names cannot be bound: `ctx`, `bus`, `server`, `client`, `env`,
  `http_status` and any other built-in namespace, plus the managed `_` and
  `_<digits>`.
- Bindings live only for the current session; they are not persisted.

Remove a binding with `:unset NAME`, and list your bindings with `:vars`.

## Multi-line input

If a line is an incomplete expression — an unclosed `{`, `[`, `(`, string, or a
trailing operator — the REPL switches to a dotted continuation prompt (aligned
to the width of the primary prompt) and keeps reading until the expression is
complete:

```
4> {
..   host = "localhost"
..   port = 8080
.. }
{ host = "localhost", port = 8080 }
```

Press `Ctrl-C` at any point to discard the partial input and return to the main
prompt.

## Meta-commands

Lines beginning with `:` are meta-commands rather than expressions:

| Command | Description |
|---|---|
| `:help`, `:?` | List commands. |
| `:quit`, `:q`, `:exit` | Exit the REPL and shut down the server. |
| `:set NAME = EXPR` | Bind `EXPR` to `NAME` (same as a bare `NAME = EXPR`). |
| `:unset NAME` | Remove a session binding. |
| `:vars` | List session bindings with their types. |
| `:loglevel LEVEL` | Set the async log level (`debug`/`info`/`warn`/`error`). |
| `:quiet` | Mute async logs. |
| `:logs on` / `:logs off` | Unmute / mute async logs. |

## Logging while at the prompt

The server keeps running while you are at the prompt — subscriptions fire,
clients connect, triggers run — and these emit log lines asynchronously. Logs
are written to **stderr** with a human-readable console format, and the line
editor redraws your prompt cleanly around them. Results and diagnostics go to
**stdout**/**stderr** respectively, so you can keep an uncluttered prompt by
redirecting logs:

```sh
vinculum serve -i config.vcl 2>vinculum.log
```

`:loglevel`, `:quiet`, and `:logs` control how much is logged at runtime without
restarting. Muting raises the log level rather than discarding output, so the
destination is unaffected.

## History and completion

Command history is persisted across sessions to
`$XDG_STATE_HOME/vinculum/history` (or `~/.vinculum_history`), so the up/down
arrows and `Ctrl-R` reverse search work as in a normal shell.

Tab completion completes the word under the cursor against built-in namespaces
(`bus`, `server`, `client`, `env`, …), function names, your session bindings,
and the `:` meta-commands. After a `.` it completes the next segment against the
attributes of whatever the leading path resolves to — so `env.<Tab>` lists
environment variables, `bus.<Tab>` lists bus names, `ctx.<Tab>` lists `auth` /
`trace_id` / `span_id`, and it works at any depth through nested objects and
maps.

## `ctx` and tracing

`ctx` is available exactly as in any handler action, so functions that take a
context as their first argument (`send`, `publish`, the `mcp_*` constructors,
HTTP functions, …) work unchanged. Each evaluation opens an OpenTelemetry span
named `repl.eval`; when a [`client "otlp"`](client-otlp.md) is configured,
`ctx.trace_id` is populated and anything the expression triggers downstream nests
under that span. With no OTLP client configured the span is a no-op and the trace
IDs are empty strings.

## Exiting

| Action | Effect |
|---|---|
| `:quit` / `:q` / `:exit` | Exit the REPL, then shut the server down gracefully. |
| `Ctrl-D` on an empty line | Same as `:quit`. |
| `SIGTERM` | Break out of the REPL and shut down gracefully. |
| `Ctrl-C` (`SIGINT`) | Cancel the current input line only — does **not** exit. |

Note that `Ctrl-C` does not stop the server in interactive mode (that would be a
hostile REPL experience); use `:quit`, `Ctrl-D`, or send `SIGTERM`. This is the
one behavioral difference from a non-interactive `serve`, where `Ctrl-C` triggers
shutdown.
