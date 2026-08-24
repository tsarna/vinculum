# Configuring from the Environment

Every Vinculum command-line flag is also settable from the environment, under a
`VINCULUM_`-prefixed name derived from the flag:

```sh
VINCULUM_LOG_LEVEL=debug vinculum serve /conf     # same as --log-level debug
VINCULUM_PLUGIN_PATH=/plugins vinculum check /conf
```

The motivation is containers. Docker replaces a container's **entire** command
as soon as you supply arguments of your own, so any flag baked into an image's
`CMD` is discarded the moment you customize your run. Settings that belong to
the *environment* — where the data directory is, where the plugins are — survive
there.

`--help` names each flag's variable, so nothing here has to be memorised:

```
  -f, --file-path string     base directory for file functions [env: VINCULUM_FILE_PATH]
      --plugin-path string   directory containing Go plugin .so files [env: VINCULUM_PLUGIN_PATH]
```

---

## The two name forms

Each flag can be reached by two variables, both derived from its name:

| Form | Example | Applies to |
|---|---|---|
| **Bare** | `VINCULUM_FORMAT` | `--format` on whichever command runs |
| **Command-scoped** | `VINCULUM_SCHEMA_FORMAT` | `--format` on `vinculum schema` only |

To derive either: take the flag's **long** name, uppercase it, replace `-` with
`_`, and prefix `VINCULUM_`. The scoped form additionally carries the command
path, joined with `_` (`vinculum schema` → `SCHEMA_`).

The name always comes from the long form. `-f` is `VINCULUM_FILE_PATH`, never
`VINCULUM_F`. That matters where a shorthand is reused: `-l` is `--log-level` on
`serve` and `--list` on `fmt`, and `-w` is `--write-path` on `serve` and
`--write` on `fmt`. Each gets its own variable, because their long names differ.

### Which form to use

Reach for the bare form by default. One `VINCULUM_PLUGIN_PATH` configures
`serve`, `check`, `test`, `fmt`, `schema`, and `man` alike — it is one directory
and it means the same thing to all six.

Use the scoped form where a flag name means different things on different
commands:

| Flag | `check` | `man` | `schema` | `test` | `publish` |
|---|---|---|---|---|---|
| `--format` | `text` \| `json` | `term` \| `markdown` \| `auto` | `json` \| `markdown` | — | — |
| `--timeout` | — | — | — | whole-run budget | per-operation timeout |
| `--verbose` | *(verbose output)* | | | list every test | |

`VINCULUM_FORMAT=markdown` is meaningful to `schema` and `man` and an outright
error to `check`, so a shell that sets it for one will break the other. Setting
`VINCULUM_SCHEMA_FORMAT` instead affects only the command you meant.

---

## Precedence

**An explicit flag beats the environment, which beats the flag's default.**
Setting a variable is equivalent to typing the flag.

Within the environment the **scoped name wins outright**. If both are set, the
bare one is ignored — not merged, not warned about.

```sh
VINCULUM_FORMAT=json  vinculum check /conf                 # json
VINCULUM_FORMAT=json  vinculum check --format text /conf   # text — the flag wins

VINCULUM_FORMAT=text VINCULUM_CHECK_FORMAT=json \
                      vinculum check /conf                 # json — scoped wins
```

---

## Turning a default off

A variable that is **set but empty** is applied as an empty value, which is how
you switch off a default baked into an image:

```sh
docker run -e VINCULUM_PLUGIN_PATH= ghcr.io/tsarna/vinculum     # plugins off
docker run -e VINCULUM_PLUGIN_PATH=/opt/plugins …               # or point elsewhere
```

Clear a whole setting rather than half of one. The image sets both
`VINCULUM_FILE_PATH` and `VINCULUM_WRITE_PATH`, and a write path requires a
file path to be under, so clearing only the first is rejected:

```sh
docker run -e VINCULUM_FILE_PATH= …                             # error
docker run -e VINCULUM_FILE_PATH= -e VINCULUM_WRITE_PATH= …     # file functions off
```

**For a boolean flag, write `false` rather than leaving the value empty.** Each
value is parsed by its flag's own type, and the empty string is not a valid
boolean:

```sh
VINCULUM_SCHEMA_PRETTY=false vinculum schema     # correct
VINCULUM_SCHEMA_PRETTY= vinculum schema          # error: invalid boolean
```

---

## Values and errors

Durations accept `30s`. Booleans accept `1`, `t`, `true`, `TRUE`, and their
false counterparts.

A flag that may be repeated (`--config`, `--update`, `--check`) takes **one
element** from the environment, used verbatim — there is no separator
convention, so `VINCULUM_CONFIG=/a:/b` is a single path containing a colon. Pass
flags where you need several.

A value the flag rejects stops the run rather than falling back to the default.
Every bad variable is reported, each naming the variable, its value, and the
reason:

```
$ VINCULUM_TIMEOUT="30 seconds" VINCULUM_JSON=maybe vinculum test /conf
Error: invalid environment variables:
  VINCULUM_JSON="maybe": strconv.ParseBool: parsing "maybe": invalid syntax
  VINCULUM_TIMEOUT="30 seconds": time: unknown unit " seconds" in duration "30 seconds"
```

The exit code is 2, as for any other bad value given to a command.

---

## Which flags are bound

All of them, on every command, except --help.

Two are worth knowing about before you export anything broadly, since both
write to disk:

- `vinculum fmt --write` (`VINCULUM_WRITE`) rewrites source files in place.
- `vinculum schema --update` (`VINCULUM_UPDATE`) rewrites generated regions of
  documentation.

An exported `VINCULUM_WRITE=true` therefore makes `vinculum fmt` destructive.
Scope such a variable to the command you mean it for — `VINCULUM_FMT_WRITE` — or
set it per-invocation rather than in a shell profile.

### Two names to know about

- **`VINCULUM_CONFIG` does not supply config paths to `serve`.** Config paths
  are positional arguments, and those are not bound to the environment. The name
  belongs to something else: `vinculum man --config` names configs to search for
  `.vinit` plugin declarations, and that is what `VINCULUM_CONFIG` sets.
- **`VINCULUM_VERBOSE` means two things.** `--verbose` is a global flag meaning
  "verbose output", and also a local flag on `vinculum test` meaning "list every
  test". On `test` the variable selects test listing; everywhere else it selects
  verbose output.

---

## Variables that predate this

These follow git's and man's conventions, have no corresponding flags, and are
unaffected:

| Variable | Effect |
|---|---|
| `VINCULUM_PAGER`, `PAGER` | The pager [`vinculum man`](man.md#output) uses. |
| `LESS` | Passed through to `less`. |
| `MANWIDTH` | Wrap width for `vinculum man`, if `--width` is not given. |
| `NO_COLOR` | Set to anything to disable colour. |

`vinculum man --width` and `MANWIDTH` coexist, with the flag winning.
`VINCULUM_MAN_WIDTH` and `VINCULUM_WIDTH` sit alongside them and take precedence
over `MANWIDTH`, being the more specific statement.

---

## What the published image sets

The [container images](container.md) carry their defaults as environment
variables rather than command arguments, so overriding the command does not
discard them:

| Variable | Value | Effect |
|---|---|---|
| `VINCULUM_FILE_PATH` | `/data` | Enables `file()`, `fileexists()`, `fileset()`, rooted there. |
| `VINCULUM_WRITE_PATH` | `/data/write` | Enables the file-write functions, rooted there. |
| `VINCULUM_PLUGIN_PATH` | `/plugins` | Where [plugins](plugins.md) are loaded from. |

The command is `serve /conf`, so replacing it keeps all three:

```sh
docker run … ghcr.io/tsarna/vinculum serve /myconf   # file functions still work
docker run … ghcr.io/tsarna/vinculum check /conf     # checks what it will serve
```

---

## See also

- [Container Images](container.md) — the images, their mount points, and their
  defaults.
- [Plugins](plugins.md) — `--plugin-path` and the `plugin` block.
- [Reading the Reference](man.md#output) — the pager and colour environment.
