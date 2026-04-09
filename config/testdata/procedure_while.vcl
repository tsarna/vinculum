procedure "countdown" {
    spec {
        params {
            n = required
        }
    }

    result = ""

    while "n > 0" {
        result = "${result}${n} "
        n = n - 1
    }

    return = result
}

procedure "sum_to" {
    spec {
        params {
            n = required
        }
    }

    total = 0
    i = 0

    while "i < n" {
        i = i + 1
        total = total + i
    }

    return = total
}

procedure "find_first_even" {
    spec {
        params {
            items = required
        }
    }

    i = 0
    result = -1

    while "i < length(items)" {
        item = items[i]
        i = i + 1

        if "item % 2 != 0" {
            continue = true
        }

        result = item
        break = true
    }

    return = result
}

procedure "nested_loops" {
    spec {
        params {
            rows = required
            cols = required
        }
    }

    total = 0
    r = 0

    while "r < rows" {
        c = 0
        while "c < cols" {
            total = total + 1
            c = c + 1
        }
        r = r + 1
    }

    return = total
}

procedure "early_return_in_loop" {
    spec {
        params {
            items = required
            target = required
        }
    }

    i = 0

    while "i < length(items)" {
        if "items[i] == target" {
            return = i
        }
        i = i + 1
    }

    return = -1
}

procedure "conditional_break" {
    spec {
        params {
            limit = required
        }
    }

    i = 0
    while "true" {
        i = i + 1
        break = (i >= limit)
    }
    return = i
}

const {
    cd       = countdown(3)
    sum5     = sum_to(5)
    even     = find_first_even([1, 3, 4, 6])
    no_even  = find_first_even([1, 3, 5])
    grid     = nested_loops(3, 4)
    found    = early_return_in_loop(["a", "b", "c"], "b")
    notfound = early_return_in_loop(["a", "b", "c"], "z")
    cbreak   = conditional_break(5)
}

assert "cd"       { condition = (cd == "3 2 1 ") }
assert "sum5"     { condition = (sum5 == 15) }
assert "even"     { condition = (even == 4) }
assert "no_even"  { condition = (no_even == -1) }
assert "grid"     { condition = (grid == 12) }
assert "found"    { condition = (found == 1) }
assert "notfound" { condition = (notfound == -1) }
assert "cbreak"   { condition = (cbreak == 5) }
