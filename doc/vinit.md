# Bootstrap Configuration (`.vinit` Files)

A `.vinit` file (Vinculum Init) is a configuration file processed
**before** any `.vcl` file. It declares everything that must happen to
prepare Vinculum's runtime before the regular VCL configuration can be
parsed and evaluated — currently, that means loading
[plugins](plugins.md).

`.vinit` files use HCL syntax but a **distinct, closed schema** from
`.vcl`. They are not VCL: the set of allowed block types is small, and
the evaluation context available to expressions inside a `.vinit` is
intentionally minimal.

```hcl
# /conf/bootstrap.vinit

plugin "custom_client" {}

plugin "internal_tools" {
    disabled    = env.DISABLE_INTERNAL == "true"
    license_key = env.INTERNAL_LICENSE  # decoded by the plugin
}
```

## File Discovery

`.vinit` files live alongside `.vcl` files under the directories passed
to `vinculum serve` (typically `/conf` in a container). Discovery happens
in two passes over the same directory tree:

| Pass | Extension | When | What it does |
|---|---|---|---|
| 1 | `*.vinit` | Before any VCL parsing | Loads plugins (and, in the future, other bootstrap concerns) |
| 2 | `*.vcl`   | After bootstrap completes | Normal VCL parse → preprocess → process pipeline |

**Snapshot rule.** Pass 1 enumerates `.vinit` files **once**, at the
very start, before any block in any `.vinit` file is processed. Files
that appear later — for example, `.vinit` files that might appear inside
content fetched at runtime — are ignored. This prevents recursive
bootstrap chains.

Pass 2 enumerates `.vcl` files **fresh**, after pass 1 has fully
completed, so it naturally picks up any `.vcl` files produced during
bootstrap.

The asymmetry is intentional: bootstrap is closed, VCL is open.

## Processing Order Within Pass 1

`.vinit` files are parsed, then their blocks are processed in this fixed
order across all parsed files:

1. **`plugin` blocks** — in source order (file order, then block order
   within a file). Each plugin is loaded and given the opportunity to
   register functions, transforms, ambient context variables, server
   types, client types, and so on.

Within a single block type, blocks are processed in source order. There
is no dependency graph at the `.vinit` level; if you need ordering
between two plugins, declare them in the order you need.

After pass 1 completes, the normal `.vcl` pipeline begins, with
plugin-contributed registrations now visible to all subsequent parsing
and evaluation.

## Evaluation Context

Expressions inside `.vinit` files (block attribute values, `disabled`
expressions, nested attribute values) are evaluated against a
**minimal** context:

| Available | Not available |
|---|---|
| `env.<NAME>` — environment variables | `const` values (const is a VCL block) |
| Standard library functions (`upper`, `lower`, `jsonencode`, etc.) | User `function` / `jq` definitions |
|   | Plugin-contributed functions |
|   | Values from other `.vinit` blocks |
|   | `bus.*`, `server.*`, `client.*`, `ctx` |

This restriction is structural: none of those things exist yet when
`.vinit` expressions are evaluated. In practice, environment variables
cover the realistic use cases:

```hcl
plugin "experimental" {
    disabled = env.ENABLE_EXPERIMENTAL != "true"
}
```

Plugins **do not** extend the `.vinit` evaluation context. A plugin's
contributions become visible only in `.vcl` evaluation.

## The `disabled` Attribute

Every `.vinit` block type supports an optional `disabled` attribute.
When the expression evaluates to `true`, the block is skipped entirely:
no plugin is loaded, no side effects occur.

```hcl
plugin "internal_tools" {
    disabled    = env.ENVIRONMENT != "production"
    license_key = env.INTERNAL_LICENSE
}
```

Errors evaluating `disabled` are fatal and abort startup.

## Block Types

Vinculum currently recognizes one `.vinit` block type:

- **`plugin "<label>" { ... }`** — load a Go plugin and let it register
  extensions. See [plugins.md](plugins.md).

Unknown block types in a `.vinit` file are a fatal error.

## Container Deployment

The default Docker image pre-creates these directories:

```
/conf/         — base config directory (CMD passes /conf as a source)
/plugins/      — conventional plugin location
```

A `.vinit` file may live in a configmap mounted at `/conf` or under
`/conf/configmap/`, or in any other directory passed to `vinculum
serve`. Vinculum scans configured directories recursively, so the
exact layout is up to you.

## Errors

| Condition | Behavior |
|---|---|
| `.vinit` parse error | Fatal — abort startup with an HCL diagnostic |
| Unknown block type in `.vinit` | Fatal |
| `disabled` expression error | Fatal |
| Plugin load failure | Fatal unless `disabled` (see [plugins.md](plugins.md)) |

Bootstrap is structurally pre-deployment: a misconfigured `.vinit`
fails fast and loudly rather than letting Vinculum start in a
partially-configured state.
