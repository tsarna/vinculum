// Edge case: empty procedure returns null
procedure "empty_proc" {
    spec {}
}

// Edge case: scope isolation — variable created inside if is not visible outside
procedure "scope_isolation" {
    spec {
        params {
            x = required
        }
    }

    result = "before"

    if "x > 0" {
        inner = "inside"
        result = inner
    }

    // "inner" is not visible here; result was updated via outer binding
    return = result
}

// Edge case: nested scope update — deeply nested block updates outer variable
procedure "deep_scope_update" {
    spec {
        params {
            items = required
        }
    }

    count = 0

    range "item" "items" {
        if "item > 0" {
            count = count + 1
        }
    }

    return = count
}

// Edge case: while loop with scope — variable created in loop not visible after
procedure "loop_scope" {
    spec {
        params {
            n = required
        }
    }

    total = 0
    i = 0

    while "i < n" {
        contribution = i * 2
        total = total + contribution
        i = i + 1
    }

    // "contribution" is not visible here, but total was updated
    return = total
}

// Edge case: switch all-branches-return makes subsequent code unreachable
// (tested separately as an error case)

// Edge case: procedure with null default param
procedure "null_opt" {
    spec {
        params {
            val = null
        }
    }

    if "val == null" {
        return = "was null"
    }

    return = "was not null"
}

// Edge case: range over empty collection
procedure "range_empty" {
    spec {
        params {
            items = required
        }
    }

    count = 0

    range "item" "items" {
        count = count + 1
    }

    return = count
}

// Edge case: nested control flow — while inside switch inside if
procedure "nested_control" {
    spec {
        params {
            mode = required
            n = required
        }
    }

    if "mode == \"sum\"" {
        total = 0
        i = 1
        while "i <= n" {
            total = total + i
            i = i + 1
        }
        return = total
    }
    elif "mode == \"count\"" {
        switch "n" {
            case "0" {
                return = "zero"
            }
            default {
                return = "nonzero"
            }
        }
    }

    return = "unknown mode"
}

const {
    empty        = empty_proc()
    iso_pos      = scope_isolation(1)
    iso_neg      = scope_isolation(-1)
    deep         = deep_scope_update([1, -2, 3, -4, 5])
    lscope       = loop_scope(4)
    null_default = null_opt()
    null_given   = null_opt("hello")
    empty_range  = range_empty([])
    nc_sum       = nested_control("sum", 5)
    nc_count0    = nested_control("count", 0)
    nc_count1    = nested_control("count", 1)
    nc_other     = nested_control("other", 0)
}

assert "empty"        { condition = (empty == null) }
assert "iso_pos"      { condition = (iso_pos == "inside") }
assert "iso_neg"      { condition = (iso_neg == "before") }
assert "deep"         { condition = (deep == 3) }
assert "lscope"       { condition = (lscope == 12) }
assert "null_default" { condition = (null_default == "was null") }
assert "null_given"   { condition = (null_given == "was not null") }
assert "empty_range"  { condition = (empty_range == 0) }
assert "nc_sum"       { condition = (nc_sum == 15) }
assert "nc_count0"    { condition = (nc_count0 == "zero") }
assert "nc_count1"    { condition = (nc_count1 == "nonzero") }
assert "nc_other"     { condition = (nc_other == "unknown mode") }
