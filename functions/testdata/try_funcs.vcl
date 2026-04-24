// End-to-end tests for the single-eval try() implementation.

const {
    // First succeeds: later args are never evaluated (so error() is fine there).
    first_ok   = try("first", error("should not run"))

    // First fails, fallback wins.
    fallback   = try(error("first fails"), "fallback")

    // Later-in-chain recovery.
    chained    = try(error("a"), error("b"), "c")

    // Heterogeneous types: returning the raw value of the chosen branch.
    num_ok     = try(1, "fallback")
    str_fb     = try(error("nope"), "fallback")
}

assert "first_ok" { condition = first_ok == "first" }
assert "fallback" { condition = fallback == "fallback" }
assert "chained"  { condition = chained  == "c" }
assert "num_ok"   { condition = num_ok   == 1 }
assert "str_fb"   { condition = str_fb   == "fallback" }
