// End-to-end tests for the cond() lazy conditional.

const {
    // Happy-path: three-arg form
    three_true  = cond(true,  "b", "c")
    three_false = cond(false, "b", "c")

    // Five-arg form: walks conditions in order
    five_first  = cond(true,  "b", false, "d", "e")
    five_second = cond(false, "b", true,  "d", "e")
    five_none   = cond(false, "b", false, "d", "e")

    // Heterogeneous result types — DynamicPseudoType avoids unification
    mixed_num  = cond(true,  1,       "fallback")
    mixed_str  = cond(false, 1,       "fallback")

    // Laziness: error() in unreached branches must not fire.
    // If cond eagerly evaluated any branch the config build would fail.
    lazy_result  = cond(false, error("result branch should not run"), "ok")
    lazy_else    = cond(true,  "ok", error("else branch should not run"))
    lazy_later_c = cond(true,  "ok", error("later cond should not be evaluated"), "never", "never")
    lazy_later_r = cond(false, error("unreached true result"), true, "ok", "never")

    // Bool-convertible string conditions (HCL convert rules)
    str_true    = cond("true",  "yes", "no")
    str_false   = cond("false", "yes", "no")
}

assert "three_true"   { condition = three_true  == "b" }
assert "three_false"  { condition = three_false == "c" }
assert "five_first"   { condition = five_first  == "b" }
assert "five_second"  { condition = five_second == "d" }
assert "five_none"    { condition = five_none   == "e" }
assert "mixed_num"    { condition = mixed_num   == 1 }
assert "mixed_str"    { condition = mixed_str   == "fallback" }
assert "lazy_result"  { condition = lazy_result  == "ok" }
assert "lazy_else"    { condition = lazy_else    == "ok" }
assert "lazy_later_c" { condition = lazy_later_c == "ok" }
assert "lazy_later_r" { condition = lazy_later_r == "ok" }
assert "str_true"     { condition = str_true  == "yes" }
assert "str_false"    { condition = str_false == "no" }
