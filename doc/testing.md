# `vinculum test` — Testing a Configuration

`vinculum test` runs the functy `test "..." { ... }` blocks embedded in a
configuration's `.cty` files **against the running system**. By default it boots
the full server exactly as [`vinculum serve`](config.md) would — buses, servers,
subscriptions, triggers — runs the tests, then shuts down. Because the tests
execute inside a live Vinculum, they are integration tests: they can reference
`bus.*` / `client.*` / `server.*`, `send()` messages, and assert on the
resulting state.

```
vinculum test [config-files-or-directories...] [flags]
```

```
vinculum test ./configs/ tests.cty
vinculum test --run 'router' -v ./configs/
vinculum test --no-serve funcs.cty tests.cty
```

The command accepts the same heterogeneous sources as `serve` (`.vcl`, `.vinit`,
`.cty`, directories) and the same capability flags (`-f`/`--file-path`,
`-w`/`--write-path`, `--allow-kill`, `--plugin-path`).

A test **passes** if its body runs to completion, is **skipped** if it calls
`skip(...)`, and **fails** if any other error (a failed `assert`, a `throw`, an
evaluation error) unwinds out of it — or if it recorded any `expect(...)` soft
failure (see below). The command exits non-zero if any test fails — suitable for
CI.

Writing the `test` blocks themselves — `assert`, `expect`, `skip`, `test setup`,
operand-rendered failures — is covered in the [functy language guide](functy.md);
this page covers what running them *inside Vinculum* adds.

## `sys.testing` — gating external I/O under test

During a test run the `sys.testing` ambient is `true` (it is `false` under
`serve`, `check`, everything else). Use it to switch off real external
connections while a configuration is under test:

```hcl
client "mqtt" "broker" {
    brokers  = ["tls://prod.example.com:8883"]
    disabled = sys.testing        # never dial prod under `vinculum test`
}
```

Because the client is disabled, it never starts — so there is nothing to connect
to, and nothing to wait for. What remains up (in-memory buses, subscriptions,
transforms, in-process servers) is what the tests exercise.

The negation gates a block *on* only under test — handy for a recording sink that
captures bus traffic for assertions:

```hcl
subscription "capture" {
    target   = bus.main
    topics   = ["out/#"]
    disabled = !sys.testing       # only active while testing
    action   = set(var.last, ctx.msg)
}
```

`sys.testing` is a plain bool, evaluated before blocks are processed, so it works
anywhere a normal expression does — most usefully in `disabled`.

## `ctx` in tests

A `test` body is evaluated with a top-level `ctx` (the standard `_ctx` object)
so it can call the many context-taking runtime functions — `send`, `http`,
`log::*`, MCP builders, etc.:

```
test "publishes a greeting" {
    send(ctx, bus.main, "in/greet", "hello")
    ...
}
```

The `ctx` carries no request-specific fields (there is no message in flight), and
is cancelable through `--timeout`.

## Soft assertions (`expect`) and shared setup (`test setup`)

`expect(cond, message?)` is the non-fatal twin of `assert`: on failure it
records the failure — with the same source range and operand detail `assert`
renders — but keeps running, so one test can check several things and report
*every* failed expectation at once instead of stopping at the first. A test with
any recorded `expect` failure fails (even if it later calls `skip`). In `--json`
output each failed test carries a `failures` array with one entry per failure.

```
test "response looks right" {
    resp = get_status(ctx)
    expect(resp.code == 200, "status code")
    expect(resp.body != "", "non-empty body")   # still checked even if the first failed
}
```

`test setup { ... }` declares statements spliced onto the front of every `test`
in the **same `.cty` file**, in the same scope — shared fixtures for that file's
tests. Because functy `defer`s are function-scoped, a `defer` in the setup runs
at the end of each test (even on failure), which is the idiom for shared
*teardown*:

```
test setup {
    send(ctx, bus.main, "in/reset", "")
    defer clear(var.last)        # runs after each test in this file
}
```

Setup runs fresh before each test; a `skip`/`throw`/failed `assert` in it skips
or fails that test. See the [functy language guide](functy.md) for the full
semantics.

## Asserting asynchronous effects: `eventually` / `never`

`send()` returns immediately and subscribers react on other goroutines, so a
plain `assert` right after a `send` races the reaction. The two test-only
polling assertions bridge that gap:

- `eventually(cond, timeout)` — re-evaluates `cond` until it holds, failing (like
  `assert`, with the condition's source range and operand values) if it never did
  within `timeout`.
- `never(cond, timeout)` — the inverse: polls for the whole window and fails the
  instant `cond` becomes true. Use it to assert an effect does *not* happen.

Both take an optional third `interval` argument; `timeout`/`interval` are
duration strings (`"250ms"`, `"1s"`) or a number of seconds.

> **Read var state with `get()`.** A condition observes change only if it
> re-reads live state each poll. A VCL `var` is a capsule, so a bare
> `var.last == "hello"` compares the *capsule*, not its value, and never
> converges — write `get(var.last) == "hello"`. This is the standard var-access
> idiom (see [config.md](config.md)).

### The observation pattern

The observable effect of async work is usually a `var` read. If a subscriber
writes a var, assert on it directly. If the effect is a message on a bus with no
var, wire a `sys.testing`-gated recording subscription that captures into a var
(as `capture` above), then poll that var:

```
# tests.cty
test "router rewrites in/ to out/" {
    send(ctx, bus.main, "in/thing", "hello")
    eventually(get(var.last) == "hello", "1s")
}

test "unmatched topics are not routed" {
    send(ctx, bus.main, "other/thing", "x")
    never(get(var.last) == "x", "250ms")
}
```

Tests share the running system's mutable state (vars, bus contents) and run in
source order; there is no per-test reset. Order-dependent assertions see earlier
tests' effects.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `--run PATTERN` | (all) | Run only tests whose description matches this Go regular expression. Non-matching tests are counted as "deselected". |
| `-v`, `--verbose` | off | Print every test's `ok`/`SKIP`/`FAIL` line, not just failures. |
| `--json` | off | Emit a machine-readable JSON report to **stdout** (logs stay on stderr), for CI and editor tooling. |
| `--timeout D` | `60s` | Overall wall-clock budget for the run (boot + all tests). Backs the injected `ctx`'s cancellation; the run fails if it overruns. |
| `--no-serve` | off | Do not start the runtime: build the config and run tests only. For pure function unit tests that do not need a live bus. |
| `--fail-if-no-tests` | off | Exit non-zero if the configuration contains no `test` blocks. |
| `-l`, `--log-level` | `warn` | Log level. Defaults quieter than `serve` so routine startup/shutdown logs do not clutter test output. |

## `--no-serve` (fast path)

`--no-serve` skips starting the runtime (and the shutdown) — it only builds the
config and runs the tests. `sys.testing` is still `true`, so the config evaluates
identically; the difference is purely that nothing is live. Use it for tests that
only call functions and assert on their results. Tests that `send()` and expect a
subscriber to react need the default full-boot mode.

## Exit codes

- `0` — all selected tests passed or were skipped (or there were no tests and
  `--fail-if-no-tests` was not set).
- non-zero — any test failed, a build/parse error occurred, `--timeout` expired,
  or `--fail-if-no-tests` was set with zero tests. Skips never fail the run.
