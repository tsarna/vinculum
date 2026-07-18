# functy (`.cty` files)

[functy](https://github.com/tsarna/functy) is a small expression/statement language
with its own parser, type system, and standard library. A Vinculum configuration
directory may contain `.cty` (functy source) files alongside its `.vcl` files: the
functions, top-level `var`/`const` declarations, and type annotations in those `.cty`
files participate in the **same** evaluation context, dependency graph, and namespace
as their VCL equivalents.

functy is a more expressive, more programmer-friendly alternative to `function`, `jq`,
and especially [`procedure`](procedure.md) blocks. Unlike the `procedure` block — which
bends HCL into an imperative language and inherits HCL's restrictions (no statement-level
side effects, quoted-string block-label conditions, one-assignment-per-block, …) —
functy has real syntax for statements, reassignment, loops, `try`/`catch`, typed
locals, and structured errors.

```functy
// scrape.cty
func celsius_to_f(c: number) -> number {
    return c * 9 / 5 + 32
}

func describe(n: number) -> string {
    if n > 0 {
        return "positive"
    } else if n < 0 {
        return "negative"
    }
    return "zero"
}
```

```hcl
# uses.vcl — the functy functions are callable from any VCL expression
const {
    freezing_f = celsius_to_f(0)   # 32
}
```

---

## Loading

`.cty` files are discovered from the same sources you already pass to Vinculum: point
`vinculum` at a directory and every `.cty` file in it (recursively, skipping
dot-directories) is parsed alongside the `.vcl` files. All `.cty` files share one
namespace and one type environment, so a function or type declared in one file is
visible to another.

```console
vinculum check .          # validates .vcl and .cty together
vinculum serve config/    # loads both
```

`.cty` parsing happens after the `.vinit` bootstrap pass (so plugin-registered
functions, ambient providers, and types are available) and alongside `.vcl` parsing.

---

## Functions

A top-level `func` becomes a callable function in the shared user-function namespace —
indistinguishable, at the call site, from a `function`/`jq`/`procedure` block or a
built-in. Functions may call each other (and VCL-defined functions) regardless of
declaration order; mutual recursion works.

```functy
func greet(name: string) -> string {
    return "hello ${name}"
}

func greet_default(name: string = "world") -> string {   // default parameter
    return greet(name)
}

func total(*values: number) -> number {                  // variadic parameter
    var sum = 0
    for v in values {
        sum = sum + v
    }
    return sum
}
```

A function name that collides with a built-in or another user function is a
configuration error, the same as for `function`/`procedure` blocks.

---

## Top-level `const` and `var`

A `.cty` file may declare top-level `const` (immutable) and `var` (mutable) bindings.
These fold into Vinculum's **own** const and var pools — a functy `const` is
indistinguishable from a VCL `const {}` attribute, and a functy `var` is
indistinguishable from a VCL [`var "name" {}`](config.md#var) block (mutable, exposed as
`var.<name>`, runtime-settable via `set()`).

```functy
const pi          = 3.14159
const tau: number = pi * 2      // may reference another const, in any file, any order
var   counter: number = 0       // mutable; read with get(var.counter), write with set()
```

Consequences, identical to VCL:

- Consts are dependency-sorted across **both** surfaces, so a functy `const` may
  reference a VCL `const` and vice versa, in any order.
- All consts resolve before any `var`.
- Duplicate names across `.cty` and `.vcl` are a configuration error.

A functy `var` initializer may reference consts, ambients (`env.*`), and functions;
referencing another variable's initial value is not ordering-guaranteed (the same
caveat applies to VCL `var` blocks).

---

## Namespaces

A `.cty` file may open with a `namespace` declaration, which qualifies its functions
and scopes its top-level `const`s to that namespace:

```functy
namespace acme::math

const pi = 3.14159                      // acme::math's own const

func area(r: number) -> number {        // registered as acme::math::area
    return pi * r * r                   // resolves acme::math's pi, then the global surface
}
```

- **Functions** register under their qualified name (`acme::math::area`) and are
  called that way from VCL and other namespaces. Within the namespace, siblings call
  each other by bare name.
- **Consts** are scoped to the namespace: a namespaced `const` is **not** folded into
  the shared const surface and has no VCL spelling (there is no `acme::math::pi` for a
  value). It is visible only to that namespace's own functions, **own-plus-global** —
  a namespaced body resolves a bare name in its own namespace first, then falls back
  to the global const surface (VCL consts and unnamespaced functy consts), with the
  local winning on a clash. Two namespaces may therefore each declare `const greeting`
  without colliding.
- **Unnamespaced** (global) consts fold into the shared surface exactly as described
  above, visible to VCL and to every namespace.
- **`var` cannot be namespaced.** A Vinculum `var` is global (`var.<name>`), so a
  top-level `var` declared inside a `namespace` is a configuration error — move it to
  the global namespace.

---

## Types

functy annotations (`param: T`, `-> T`, `var x: T`) enforce a type via **coercion** —
richer than a bare name check. Vinculum registers its capsule and rich-object types
with the functy parser so `.cty` annotations can name them, and the **same** type
grammar is available to the VCL [`var` block's `type` attribute](config.md#var).

Besides the standard functy types (`number`, `string`, `bool`, `list(T)`, `map(T)`,
`set(T)`, `tuple([...])`, `object({...})`, `any`), these host types are nameable:

| Category | Type names |
| --- | --- |
| Buses / servers / clients | `bus`, `server`, `client`, `subscriber` |
| Variables | `variable` |
| Time | `time`, `duration` |
| Rich values | `url`, `bytes`, `baggage`, `metric`, `wire_format` |
| HTTP | `http::request`, `http::response`, `http_client_response`, `http_client` |
| SQL | `sql_client`, `sql_query` |
| MCP | `mcp_result` |
| Handler context | `ctx` |

```functy
func on_message(c: ctx, db: sql_client) -> string {
    return "ok"
}
```

`client`, `subscriber`, and `ctx` (and `sql_client`) are "open" types — they match by
interface/shape and pass the value through untouched, because their concrete form
varies. The rest are enforced by identity.

### VCL `var` `type`: type spec or string

Because the same type engine backs the VCL `var` block, its `type` attribute accepts
the functy grammar as a **type spec**, not just a string:

```hcl
var "port"  { type = number;            value = 8080 }
var "hosts" { type = list(string);      value = ["a", "b"] }
var "main"  { type = bus;               value = bus.main }
```

The older quoted-string form (`type = "number"`) still works for backwards
compatibility but is **deprecated** and emits a warning; write the unquoted type spec
instead.

---

## Standard library

Merging functy's standard library adds host-agnostic builtins that are available
**anywhere** Vinculum evaluates an expression (not just inside `.cty` functions):
`typeof`, `typekind`, `cond`, `switch`, `error`, `assert`, `try`, and `can`. See
[functions.md](functions.md#control-flow) for details. (`typeof` returns functy's
type-annotation grammar, e.g. `list(string)`.)

---

## Errors

functy has structured, catchable errors (`throw`, `try`/`catch`, the `error()` and
`assert()` builtins). When an uncaught throw or a failed `assert` inside a `.cty`
function propagates out to a VCL action, Vinculum renders it against the **`.cty`
source** — the failing line plus any captured operand detail — through the user log:

```console
Error: must be positive

  on scrape.cty line 5:
   5:     assert(n > 0, "must be positive")

n = -3
```

---

## Relationship to other block types

functy overlaps with several block types; which to reach for is largely a matter of
convenience:

- A [`function`](config.md#function) or [`jq`](config.md#jq) block is a fine, lightweight
  way to name a single pure expression inline in a `.vcl` file. But if you're writing a
  `.cty` file anyway, defining that function there — next to related code, with optional
  type annotations — is just as good and often more convenient. Use whichever keeps the
  code where you want it.
- For multi-step logic — branching, loops, typed locals, reassignment, error handling —
  functy is the natural fit; plain HCL expressions and `function`/`jq` blocks can't
  express it cleanly.
- The [`procedure`](procedure.md) block covered the multi-step case before functy and is
  now **deprecated** in favor of it (it will be removed in a future release; loading one
  emits a deprecation warning). Port procedures to `.cty` functions.
