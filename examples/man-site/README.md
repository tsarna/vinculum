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

That is the public posture: anonymous, and read-only. Set `MAN_CHECK_PASSWORD`
to also get `vcl_check`, which puts that password in front of the route — see
[below](#one-file-three-postures).

Ask it "what attributes does `client \"mqtt\"` take?", or "which blocks have a
`keep_alive`?", or "write me a config that bridges MQTT to an HTTP endpoint".

## What it exposes

| Tool | Answers |
|---|---|
| `vcl_man` | One topic of the reference: a block, a type variant, an attribute, a `ctx` shape, a namespace member, a function, or a `vinculum` command with its flags. `serve` is how an agent learns that `file()` needs `--file-path`, and a function a flag switches on says so on its own page. Takes an optional `kind` for a name that means more than one thing. |
| `vcl_apropos` | Keyword search over names and one-line summaries, for when you know a word but not which block owns it. Each row names a topic path to pass to `vcl_man`; at most fifty rows are shown, with a count of the rest. |
| `vcl_synopsis` | Just the skeleton of a block, or a function's calling conventions — much smaller than the page for a block, and the right first call before writing one. A block with type labels, such as `client`, answers with its list of types. |
| `vcl_doc` | One hand-written `doc/` page: the HCL syntax, functy, transforms, testing. The generated reference describes the blocks; these describe the language the blocks are written in. |
| `vcl_check` | **Only when `MAN_CHECK_PASSWORD` is set**, which also puts that password in front of `/mcp`. Builds one `.vcl` file's text without running it and answers what `vinculum check` would: that it is valid, or each problem with its line quoted. An agent that can check what it wrote does not need to be right first time. |

| Resource | Holds |
|---|---|
| `vcl://index` | Every block, `ctx` shape, namespace and command with a one-line summary — the whole map of the language, in a few KB. A good default attachment. |
| `vcl://topic/{+path}` | One topic, addressed by path: `vcl://topic/client/mqtt`. |

The `write_vcl` prompt grounds a model in the order to use them in: search, then
skeleton, then detail, then a check — `vcl_check` when it is offered, `vinculum
check` otherwise — then the `serve` page for the flags the config needs to run.

## Layout

| File | Contents |
|---|---|
| [mcp.vcl](mcp.vcl) | The `server "mcp"` block — its tools, resources and prompt — and the `server "http"` block that mounts it. |
| [auth.vcl](auth.vcl) | The two `auth` blocks, each switched on by its own password variable. |
| [docs.vinit](docs.vinit) | A `git` block that fetches `doc/` at boot, for a container that does not carry it. |

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `MAN_LISTEN` | `:9000` | Listen address. |
| `MAN_DOC_DIR` | `doc` | Where the hand-written pages live, relative to `--file-path`. |
| `MAN_PASSWORD` | _unset → anonymous_ | Password for the reference. Set it and every route needs it. |
| `MAN_USER` | `docs` | Username for `MAN_PASSWORD`. |
| `MAN_CHECK_PASSWORD` | _unset → no checker_ | Offers `vcl_check` **and** requires this password on `/mcp`. |
| `MAN_CHECK_USER` | `check` | Username for `MAN_CHECK_PASSWORD`. |
| `MAN_DOC_FETCH` | _unset → no clone_ | Any non-empty value fetches `doc/` at boot through the `git` block. |
| `MAN_DOC_TAG` | _unset → default branch_ | The release to fetch the pages from, e.g. `v0.46.0`. |
| `MAN_DOC_REPO` | `https://github.com/tsarna/vinculum.git` | Repository to fetch them from. |
| `MAN_DOC_INTO` | `/conf/doc` | Where to materialize them. Point `--file-path` and `MAN_DOC_DIR` at the result. |

## One file, three postures

The same files run as a public, read-only reference and as a private one that
also checks what an agent wrote. Only the environment differs, so nothing is
edited to switch.

| Set | `/mcp` | `vcl_check` |
|---|---|---|
| nothing | anonymous, on purpose | not registered, not listed |
| `MAN_PASSWORD` | that password | not registered, not listed |
| `MAN_CHECK_PASSWORD` | **that** password | offered |

A disabled `auth` block is parsed but inert, and its required attributes are not
validated, which is what lets one variable both supply the credential and switch
the mechanism on. The unset case says `auth.anonymous` rather than leaving the
policy empty: both are unauthenticated, but an empty one logs a warning naming
the route at startup, and this deployment is public on purpose.

There is no variable that offers the checker without a password. That is the
whole point of `MAN_CHECK_PASSWORD` being a password rather than a flag: the
mistake it prevents is an exposed checker.

The cost is that turning the checker on closes the reference **over MCP** too,
since the tools of one `server "mcp"` block are one list and a second anonymous
endpoint would mean a second copy of every tool. `MAN_PASSWORD` still governs
the HTTP site the example will grow.

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

- **The password on `MAN_CHECK_PASSWORD` is load-bearing.** Anyone who can reach
  the tool can probe the server's filesystem this way. See
  [functions.md](../../doc/functions.md#checking-a-configuration) for the full
  list.
- **Point `--file-path` at the documentation and nothing else.** That keeps the
  rest of the deployment out of reach of the configuration being served, too.

A checked config can't see the operator's environment, so write
`try(env.NAME, default)` in anything meant to be checked this way.

## Deploying it

The published images carry the binary and nothing else — the minimal one is a
scratch build with no shell — so the `doc/` pages `vcl_doc` serves are not in
them. [docs.vinit](docs.vinit) fetches them at boot with a
[`git` block](../../doc/git.md), which is pure Go for exactly this reason and so
works in an image with no `git` in it. The pages then update by restarting
rather than by rebuilding an image.

```sh
docker run -p 9000:9000 \
    -v "$PWD/examples/man-site:/conf" \
    -e MAN_DOC_FETCH=1 -e MAN_DOC_TAG=v0.46.0 \
    ghcr.io/tsarna/vinculum:0.46.0 serve -f /conf /conf
```

`-f /conf` is what `MAN_DOC_DIR` is relative to, and `MAN_DOC_INTO` defaults to
`/conf/doc`, so the fetched pages land where `vcl_doc` looks with nothing else
set. The mount has to be writable, since that is where the clone is
materialized; to keep the config read-only, set `MAN_DOC_INTO` to a writable
path of its own and `MAN_DOC_DIR` to the same place.

Each fetch owns its destination, and this one sets `overwrite = true`, so every
boot replaces the last boot's copy. Point it at a directory nothing else writes.

Pin `MAN_DOC_TAG` to the release the image is, so the hand-written pages and the
generated reference describe the same binary. Without it the repository's
default branch is fetched, which is what a deployment tracking `main` wants.

For the private posture, add `-e MAN_CHECK_PASSWORD=…`, and put the whole thing
behind TLS: basic auth over plain HTTP sends the password in every request.

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
- **The policies are written at the routes, not in a `const`.** A `const` is
  evaluated before any `auth` block is processed, so `auth.site` does not resolve
  in one — and naming the block at the route is what lets the dependency sort
  order the server after the blocks it names.
- **A route's `auth` replaces the server's** rather than adding to it, which is
  why the site password does not open `/mcp` once the checker is on.

## What this does not do yet

Later steps of the same plan: an HTML site over the same functions, for a person
with a browser rather than a model with a client. And rate limiting, which is
the first thing a public deployment would want that Vinculum does not have.

Searching has no `kind` filter, because the rows say which kind they are when it
matters.
