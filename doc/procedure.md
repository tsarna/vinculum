# Procedure Blocks

`procedure` blocks compile into callable functions that provide a limited imperative
escape hatch for computations that need intermediate bindings or branching. They are
processed early during configuration loading — alongside `function`, `jq`, and `editor`
blocks — and are callable from any expression just like built-in functions.

As its name implies, the Hashicorp Config Language (HCL) system on which Vinculum Config Language is based, was intended for configuration, not programming. Creating a procedure language inside of it is really pushing the bounds of what's possible or sensible, and comes with limitations. As Vinculum is intended as a low-code/no-code, primarily declarative system, we feel these limitations are acceptable, but you need to be aware of them:

- HCL does not allow a variable to be reassigned within the same block. You can assign a value and then overwrite it in a conditional or loop block, but not within the same block.
- There is no syntax for evaluating an expression for side effects. Every statement must be either an assignment or a block. See the section on Discard Names below for how to deal with this.
- Expressions in block labels (conditions for `if`, `elif`, `while`, etc.) must be plain quoted strings. Template syntax (`${}`) is not supported in HCL block labels, so write `if "x > 0"` not `if "${x > 0}"`.
- HCL requires a newline after every block closing brace. This means `} elif "cond" {` and `} else {` on the same line as the preceding block are syntax errors. Each block must start on its own line.

Since procedures are both somewhat clunky and limited, if you find yourself bumping up against these limitations frequently or writing many procedures, think about whether Vinculum is the right tool in which to implement that logic. Consider writing a purpose-built program for Vinculum to call via one of the supported protocols, or simply writing a custom program instead of using Vinculum at all. Vinculum is like a Swiss Army Knife: it can do many tasks adequately, but sometimes you need a purpose-built tool.

```hcl
procedure "name" {
    spec {
        params {
            req   = null    # required parameter (no default)
            limit = 10      # optional parameter, defaults to 10
        }

        variadic_param = extra_args   # optional
    }

    # statements...

    return = some_expr
}
```

---

## The `spec` Block

The `spec` block contains compile-time declarations for the procedure. It must appear
as the first element in the procedure body, before any statements. At most one `spec`
block is allowed. The `spec` block is optional; a procedure with no `spec` block takes
no parameters.

### Parameters

Inside `spec`, the `params` block declares named parameters. Each attribute defines one
parameter: the name is the parameter name, and the value is the default.

```hcl
spec {
    params {
        req     = null    # required — caller must provide
        limit   = 10      # optional — defaults to 10
        verbose = false   # optional — defaults to false
    }
}
```

A value of `null` means the parameter is required. Any other value becomes the default,
making the parameter optional. Required parameters must precede optional parameters in
source order.

Parameters are mapped to positional arguments in source order (determined by byte
offset in the source file).

### Variadic Parameter

An optional `variadic_param` attribute names a parameter that collects extra positional
arguments into a list:

```hcl
spec {
    params {
        url = null
    }
    variadic_param = headers
}
```

If present, extra arguments beyond the named parameters are collected into a list bound
to the named variable. If absent, passing extra arguments is an error.

---

## Statements

The procedure body consists of an ordered sequence of statements, executed top to
bottom. HCL stores attributes in an unordered map internally; source order is recovered
by sorting attributes and blocks by their starting byte offset.

### Variable Assignment

```hcl
result = call(req)
count  = count + 1
```

A plain attribute evaluates the right-hand expression and binds the result to the name.

**Scoping:** Each block body introduces a new scope chained onto its enclosing scope.
Variable lookup walks outward through the chain. Assignments to a name that already
exists in an enclosing scope update that outer binding; assignments to a new name create
it in the current (innermost) scope.

### Discard Names

Any name starting with `_` is a discard — the expression is evaluated (for side
effects) but the value is never stored. Different discard names allow multiple
side-effect-only calls in the same scope:

```hcl
_1 = log_debug("step 1")
_2 = log_debug("step 2")
```

This is necessary because the language does not allow variables to be reassigned within the same block.

### Return

```hcl
return = expr
```

`return` is a reserved name. Assigning to it evaluates the expression and immediately
exits the procedure with that value as the result. Statements after a `return` in the
same scope are unreachable and produce a compile-time error.

If execution reaches the end of the procedure body without encountering a `return`, the
procedure returns `null`.

---

## Control Flow

### If / Elif / Else

```hcl
if "condition_expr" {
    # ...
}
elif "condition_expr" {
    # ...
}
else {
    # ...
}
```

`if` takes a single label: a quoted string that is parsed as an HCL expression at
compile time and evaluated as a boolean at runtime. `elif` has the same form. `else`
takes no label.

`elif` and `else` blocks must immediately follow an `if` or `elif` block with no
intervening statements. An `elif` or `else` that does not immediately follow a valid
predecessor is a compile-time error. At most one `else` may appear, and it must be last
in the chain.

Each branch body introduces a new scope. Variables assigned inside a branch are not
visible after the chain exits, but assignments to variables from an enclosing scope
update that outer binding.

If all branches of an if/elif/else chain (including an else) contain a `return`,
statements after the chain are unreachable and produce a compile-time error.

---

## Example

```hcl
procedure "add" {
    spec {
        params {
            a = null
            b = null
        }
    }

    result = a + b
    return = result
}

procedure "greet" {
    spec {
        params {
            name = "world"
        }
    }

    return = "hello ${name}"
}
```

Called from any expression as `add(3, 4)` or `greet()` or `greet("Vinculum")`.

A more complete example with conditionals:

```hcl
procedure "classify" {
    spec {
        params {
            n = null
        }
    }

    if "n > 0" {
        return = "positive"
    }
    elif "n < 0" {
        return = "negative"
    }
    else {
        return = "zero"
    }
}
```

---

## Relation to Other Function-Definition Blocks

| Block | Style | Best for |
|---|---|---|
| `function` | Single HCL expression | Simple transformations |
| `jq` | jq DSL | JSON manipulation |
| `editor "line"` | Declarative match/replace DSL | Structured text editing |
| `procedure` | Imperative statements | Logic requiring intermediate bindings or branching |
