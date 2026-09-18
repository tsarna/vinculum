# The Vinculum reference, as an MCP server

Serves Vinculum's own configuration-language reference to a coding agent, so a
model writing a `.vcl` looks the language up instead of recalling it.

The reference is generated from the same decode structs the parser uses, so a
tool answer **cannot describe an attribute this binary cannot parse**. That is a
different guarantee from "the model read the manual during training", and it is
the reason this example exists.

It is also the demonstration the project most wants to be able to make: a
documentation server for a configuration language, written in that
configuration language, in one file that is mostly comments.

## Running it

```sh
vinculum serve -f /path/to/vinculum examples/man-site/
```

`-f` (`--file-path`) should point at a checkout of Vinculum. Only `vcl_doc`
reads from disk — the hand-written pages under `doc/` — and everything else
comes from the binary. Without the flag the server still starts and every other
tool works, but `vcl_doc` fails on every call.

Then point an MCP client at `http://localhost:9000/mcp`:

```json
{"mcpServers": {"vcl": {"url": "http://localhost:9000/mcp"}}}
```

Ask it "what attributes does `client \"mqtt\"` take?", or "which blocks have a
`keep_alive`?", or "write me a config that bridges MQTT to an HTTP endpoint".

## What it exposes

| Tool | Answers |
|---|---|
| `vcl_man` | One topic of the reference: a block, a type variant, an attribute, a `ctx` shape, a namespace member, a function, or a `vinculum` command with its flags. `serve` is how an agent learns that `file()` needs `--file-path`, and a function a flag switches on says so on its own page. Takes an optional `kind` for a name that means more than one thing. |
| `vcl_apropos` | Keyword search over names and one-line summaries, for when you know a word but not which block owns it. Each row names a topic path to pass to `vcl_man`; at most fifty rows are shown, with a count of the rest. |
| `vcl_synopsis` | Just the skeleton of a block, or a function's calling conventions — much smaller than the page for a block, and the right first call before writing one. A block with type labels, such as `client`, answers with its list of types. |
| `vcl_doc` | One hand-written `doc/` page: the HCL syntax, functy, transforms, testing. The generated reference describes the blocks; these describe the language the blocks are written in. |
| `vcl_check` | **Only when `MAN_CHECK` is set.** Builds one `.vcl` file's text without running it and answers what `vinculum check` would: that it is valid, or each problem with its line quoted. An agent that can check what it wrote does not need to be right first time. |

| Resource | Holds |
|---|---|
| `vcl://index` | Every block, `ctx` shape, namespace and command with a one-line summary — the whole map of the language, in a few KB. A good default attachment. |
| `vcl://topic/{+path}` | One topic, addressed by path: `vcl://topic/client/mqtt`. |

The `write_vcl` prompt grounds a model in the order to use them in: search, then
skeleton, then detail, then a check — `vcl_check` when it is offered, `vinculum
check` otherwise — then the `serve` page for the flags the config needs to run.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `MAN_DOC_DIR` | `doc` | Where the hand-written pages live, relative to `--file-path`. |
| `MAN_LISTEN` | `:9000` | Listen address. |
| `MAN_CHECK` | unset | Any non-empty value offers `vcl_check`. Unset, the tool is not registered, so `tools/list` does not mention it. |

## One file, two postures

The same file runs as a public, read-only reference and as a private one that
also checks what an agent wrote. Only the environment differs, so nothing is
edited to switch.

Checking is off by default because it builds text a caller sent. That build is
fenced:

- It is never read as a `.vinit` file, so no plugin loads and no repository is
  cloned.
- It sees no environment variables.
- Its file functions read an empty scratch directory.
- Recursion, size, time and concurrency are bounded.

Other things are not fenced. A `tls` block reads the certificate files it
names, and its error says whether a path exists. `client "aws"` reads the
profile it names. So:

- **Put `MAN_CHECK` behind authentication.** Anyone who can reach the tool can
  probe the server's filesystem this way. See
  [functions.md](../../doc/functions.md#checking-a-configuration) for the full
  list.
- **Point `--file-path` at the documentation and nothing else.** That keeps the
  rest of the deployment out of reach of the configuration being served, too.

A checked config can't see the operator's environment, so write
`try(env.NAME, default)` in anything meant to be checked this way.

## Worth reading the config for

- **`man::page`, `man::index`, `man::apropos`, `man::synopsis` and
  `man::check`** — the reference as data, and the checker beside it. See
  [functions.md](../../doc/functions.md#the-reference-as-markdown-man).
- **`coalesce()` at the call site.** Every lookup returns `null` when nothing is
  named that, and a tool result must be a string. The fallback cannot be pushed
  into a helper: user functions reject null arguments.
- **A miss answers with text, not `mcp::error()`.** A lookup that found nothing
  succeeded; an error reads to a model as "the tool broke".
- **`{+path}`** — RFC 6570 reserved expansion, so one resource template
  addresses a whole tree. A plain `{path}` would not match `client/mqtt` at all.
- **The page-name guard** on `vcl_doc`: the file functions resolve against
  `--file-path` but do not stop a path climbing out of it, and this endpoint is
  meant to be public.
- **`cond()`** for lazy branching, so a rejected name never reaches `file()`.

## What this does not do yet

Later steps of the same plan: an HTML site over the same functions, an `auth`
block gated on an environment variable (which `MAN_CHECK` wants), a `.vinit` `git` block so a
deployment fetches its own documentation at boot, and rate limiting, which is
the first thing a public deployment would want that Vinculum does not have.

Searching has no `kind` filter, because the rows say which kind they are when it
matters.
