// End-to-end tests for the switch() dispatch function.

const {
    // Basic match on the first arm
    s_first  = switch(1, 1, "one", 2, "two", "default")

    // Match on a later arm
    s_second = switch(2, 1, "one", 2, "two", 3, "three", "default")

    // No match falls through to default
    s_default = switch(99, 1, "one", 2, "two", "default")

    // String values
    s_str = switch("warn", "error", "E", "warn", "W", "info", "I", "?")

    // Heterogeneous result types — DynamicPseudoType lets each arm be its own type
    mixed = switch("k", "k", 42, "j", "string", true)

    // Type mismatches don't match (strict equality)
    type_mismatch = switch(200, "200", "string match", 200, "number match", "default")

    // Laziness: results paired with non-matching values must not run
    lazy_results = switch(1, 1, "ok", 2, error("r2 should not run"), error("default should not run"))

    // Laziness: case values past the matching arm must not be evaluated
    lazy_later_v = switch(1, 1, "ok", error("v2 should not be evaluated"), "never", "never")

    // Laziness: when no match, default fires (and unselected results are skipped)
    lazy_default = switch(99, 1, error("r1"), 2, error("r2"), "ok")

    // No-default form: must match (otherwise switch errors). Here we do match.
    no_default_hit = switch(2, 1, "one", 2, "two")
}

assert "s_first"        { condition = s_first         == "one" }
assert "s_second"       { condition = s_second        == "two" }
assert "s_default"      { condition = s_default       == "default" }
assert "s_str"          { condition = s_str           == "W" }
assert "mixed"          { condition = mixed           == 42 }
assert "type_mismatch"  { condition = type_mismatch   == "number match" }
assert "lazy_results"   { condition = lazy_results    == "ok" }
assert "lazy_later_v"   { condition = lazy_later_v    == "ok" }
assert "lazy_default"   { condition = lazy_default    == "ok" }
assert "no_default_hit" { condition = no_default_hit  == "two" }
