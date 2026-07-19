# Plugins

A **plugin** is a Go shared object (`.so`) that Vinculum loads at
startup. Plugins extend Vinculum with the same registration mechanisms
that in-tree subsystems use — they can contribute functions, transforms,
ambient context variables, server types, client types, trigger types,
condition subtypes, wire formats, and editor types.

Plugins let third parties (and operators) ship Vinculum extensions
without forking and rebuilding the binary. They are declared in
[`.vinit` files](vinit.md) and loaded before any `.vcl` parsing happens,
so their contributions are visible everywhere in the VCL configuration.

## `--plugin-path` Flag

Plugin loading is opt-in via a CLI flag:

```
vinculum serve --plugin-path /plugins /conf
vinculum check --plugin-path /plugins /conf
```

| Flag | Meaning |
|---|---|
| `--plugin-path <dir>` | Directory containing the plugin `.so` files. Required if any `plugin` block is declared in any `.vinit` file. |

If a `plugin` block is processed and `--plugin-path` was not given,
startup fails with a fatal diagnostic pointing at the block. The
default container image sets `--plugin-path /plugins` as part of its
default `CMD`, so just dropping `.so` files into `/plugins/` is enough.

The flag is read once at startup and is not reloadable.

## The `plugin` Block

```hcl
plugin "<label>" {
    disabled = <bool-expression>    # optional

    # ... plugin-defined body ...
}
```

### Block label

The block label (`"<label>"`) is the plugin name **and** determines the
file loaded:

```
${plugin-path}/<label>.so
```

For example, `plugin "custom_client" {}` with `--plugin-path /plugins` loads
`/plugins/custom_client.so`.

Label restrictions:

- Must match `^[A-Za-z0-9_][A-Za-z0-9_-]*$`.
- May not contain `/`, `\`, `.`, or `..`.
- Labels are case-sensitive, matching the underlying filesystem
  convention on Linux.

The loader always appends `.so` to the label on every supported
platform, matching Go's `plugin` package convention even on macOS where
Go produces `.so` files in plugin build mode.

A label that fails the regex is a fatal configuration error.

### Attributes handled by Vinculum

- `disabled` — bool expression. Standard
  [`.vinit` `disabled` semantics](vinit.md#the-disabled-attribute):
  skip the block entirely if true. Not visible to the plugin.

### Plugin-defined body

After Vinculum extracts `disabled`, the **remaining body** is passed to
the plugin's entry point. The plugin defines and decodes its own
schema. If a plugin needs no configuration, the body is empty:

```hcl
plugin "minimal" {}
```

If a plugin accepts configuration, the body holds whatever attributes
and nested blocks the plugin documents — `gohcl`-decoded structs and
raw `hcl.Body` decoding are both common.

### Duplicate labels

Two `plugin` blocks with the same label — anywhere across the `.vinit`
file set — are a fatal error. A plugin is loaded and initialized at
most once.

## Platform Support

Plugin loading uses Go's `plugin` package, which is only available on
**Linux, macOS, and FreeBSD**. On other platforms (notably Windows),
any `plugin` block produces a fatal diagnostic.

Plugins also work on macOS in development but the plugin/host pairing is
fragile across macOS releases; production deployments should run on
Linux.

## ABI Compatibility

Go plugins are **highly version-sensitive**. The plugin and the host
binary must agree on:

- **Go toolchain version**, down to the patch release
- **Module versions** of every shared dependency between the plugin and
  Vinculum (most importantly, `github.com/tsarna/vinculum/config` and
  everything it transitively imports)
- **Build flags** (`-trimpath`, `-buildvcs`, build tags)
- **GOOS / GOARCH** and the **C library** the binaries link against

**cgo must be enabled, on both sides.** `-buildmode=plugin` always forces
external linking, so a plugin *cannot* be built with `CGO_ENABLED=0` — the
build fails with `requires external (cgo) linking, but cgo is not enabled`.
For the same reason the host binary must be cgo-enabled and dynamically
linked: a statically linked host cannot `dlopen` a plugin at all. This is
why the default (alpine) Vinculum image — which is built cgo-enabled — can
load plugins, while the statically linked **minimal (scratch) image
cannot**. The plugin and host must also share the same C library (both
musl, as in the official alpine images).

Any mismatch typically results in `plugin.Open` failing with an
"different version of package X" diagnostic. There is no
"plugin failed but Vinculum keeps running" mode: if a declared plugin
cannot load, Vinculum refuses to start.

The recommended workflow is to build plugins inside the matching
`vinculum-build` Docker image, which pins the toolchain, dependency
versions, and build flags to the same values used by the matching
Vinculum release. See [container.md](container.md#vinculum-build) for
details.

## Container Deployment

The default container image pre-creates `/plugins/` and passes
`--plugin-path /plugins` in its default `CMD`. To deploy a plugin,
build it against the matching `vinculum-build` image and mount or
`COPY` the resulting `.so` into `/plugins/`:

```dockerfile
FROM ghcr.io/tsarna/vinculum:0.36.0
COPY custom_client.so /plugins/
COPY internal_tools.so /plugins/
```

…and reference each plugin in a `.vinit`:

```hcl
plugin "custom_client" {}

plugin "internal_tools" {
    disabled    = env.DISABLE_INTERNAL == "true"
    license_key = env.INTERNAL_LICENSE
}
```

Operators who do **not** intend to load any plugins can simply not
declare any `plugin` blocks — the pre-created `/plugins/` directory is
otherwise harmless.

## Errors

| Condition | Behavior |
|---|---|
| `plugin` block in `.vinit` but no `--plugin-path` given | Fatal |
| Label fails regex / contains forbidden characters | Fatal |
| Resolved `.so` file does not exist | Fatal |
| `plugin.Open` fails (corrupt file, ABI mismatch, etc.) | Fatal, including the underlying error message |
| Plugin entry-point symbol missing or wrong type | Fatal |
| Plugin's init function returns error diagnostics | Fatal — diagnostics surfaced as-is |
| Plugin's init function panics | Fatal — caught and converted to a diagnostic |
| Duplicate `plugin "<label>"` blocks | Fatal |

## Writing a Plugin

Plugin source code lives outside the Vinculum repository and is built
as a Go shared object using `go build -buildmode=plugin`. See the
[vinculum-plugin-example](https://github.com/tsarna/vinculum-plugin-example)
repository for a runnable demo, a project skeleton, and the
plugin-authoring tutorial.

The summary, in case you already know what you're doing:

- The plugin's main package must export a single symbol:
  ```go
  func VinculumPluginInit(ctx *config.PluginContext) hcl.Diagnostics
  ```
- Inside it, call the relevant `config.Register*` functions to contribute
  whatever the plugin provides — see [Extension points](#extension-points)
  below.
- Decode the plugin block's body (`ctx.Block.Body`) with `gohcl` or
  raw HCL APIs if the plugin accepts configuration.
- Build with the matching `vinculum-build` image so the resulting `.so`
  is ABI-compatible with the target Vinculum release.

### Extension points

A plugin contributes by calling these from `VinculumPluginInit`. They are
the same functions the in-tree subsystems call from their `init()`, so
anything a built-in server, client, or trigger can do, a plugin can do.

| Function | Contributes |
|---|---|
| `RegisterFunctionPlugin` | callable functions ([functions.md](functions.md)) |
| `RegisterTransformPlugin` | transform-pipeline constructors ([transforms.md](transforms.md)) |
| `RegisterAmbientProvider` | a top-level VCL value, e.g. `myplugin.*` |
| `RegisterServerType` | `server "type"` blocks |
| `RegisterClientType` | `client "type"` blocks |
| `RegisterTriggerType` | `trigger "type"` blocks ([trigger.md](trigger.md)) |
| `RegisterConditionalTriggerType` | a trigger type whose availability depends on config state (e.g. a feature flag), resolved once per `Build()` |
| `RegisterConditionSubtype` | `condition "subtype"` blocks ([condition.md](condition.md)) |
| `RegisterWireFormatType` | `wire_format "type"` blocks |
| `RegisterEditorType` | `editor "type"` blocks ([editor.md](editor.md)) |
| `RegisterFunctyType` | a named type usable in [functy](functy.md) (`.cty`) annotations: a capsule type, or a rich object's type when its attribute set is fixed |
| `RegisterFunctyOpenType` | the open, predicate-backed form of the above — for a type with no single fixed `cty.Type` (attributes vary per instance, or interface dispatch spans several capsule types) |
| `RegisterFunctyExterns` | a `//functy:extern` source declaring the *real* signatures of the functions the plugin contributes |

The last three matter if the plugin contributes its own types or
functions. Registering a type makes it nameable in `.cty` annotations
(and in the `var` block's `type` attribute, which shares the same
grammar). Externs exist because a cty function's own metadata often
cannot describe it: cty can only make a *trailing* parameter optional, so
a function taking an optional leading `ctx` has to fake it with a
variadic and reflects uselessly as `f(thing, ...args)` — an extern states
what it actually accepts, so `help()` and editor tooling show the true
signature. Register the externs from the same place as the function
plugin they describe.

Note that functy checks extern names for collisions, which the
function-plugin registry does not: two packages declaring an extern for
the same name is an error, as is one that collides with a user's `.cty`
function.

Name contributed functions with a namespace, as Vinculum's own families
do (`log::info`, `http::get`, `time::add`). HCL parses `a::b(x)` natively
and resolves it as a single flat map key, so a namespaced name costs
nothing beyond spelling the registry key that way, and the leaf name
should not repeat the namespace.

`RegisteredPlugins()` reports what has been registered; it is a query,
not a contribution point. For what plugins *cannot* do, see
[Limitations](#limitations).

### Build contract

To produce a `.so` that loads, three things must line up between your
plugin and the runtime image. The `vinculum-build` image's
`vinculum-plugin-build` wrapper enforces all three, so prefer it over a
raw `go build`:

1. **Exact vinculum version.** Your `go.mod` must
   `require github.com/tsarna/vinculum vX.Y.Z` at the *same* version as the
   runtime image (a tagged release; for `:latest`/`:dev` see
   [container.md](container.md#vinculum-build)). The host is built from that
   versioned module, and `-buildmode=plugin` bakes the module path+version
   into every package's build ID.
2. **Build flags and toolchain.** `-buildmode=plugin -trimpath`, cgo on,
   and the same Go toolchain. The wrapper applies these for you.
3. **No shared-dependency drift.** Do not require a shared dependency at a
   version different from the one vinculum pins; a differing version of any
   package compiled into both the plugin and the host fails to load. Run
   `go mod tidy` after pinning vinculum and don't bump shared deps. The
   wrapper diffs your compiled dependency closure against the release and
   refuses to build on a mismatch, naming the offending modules.

Build with the wrapper:

```sh
docker run --rm -v "$PWD":/plugin -w /plugin \
    ghcr.io/tsarna/vinculum-build:X.Y.Z \
    vinculum-plugin-build -o myplugin.so .
```

## Limitations

Plugins extend the existing extension points; they **cannot** add
entirely new top-level `.vcl` block types. The set of recognized block
types (`bus`, `subscription`, `server`, `client`, `trigger`, etc.) is
fixed by the host binary.

There is no hot-reload, unload, or shutdown hook. A loaded plugin lives
for the lifetime of the Vinculum process.

Plugins run with the same privileges as Vinculum itself — there is no
sandboxing or capability restriction.
