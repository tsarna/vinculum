procedure "sum_list" {
    spec {
        params {
            items = required
        }
    }

    total = 0

    range "item" "items" {
        total = total + item
    }

    return = total
}

procedure "map_keys_concat" {
    spec {
        params {
            m = required
        }
    }

    result = ""

    range "entry" "m" {
        if "result != \"\"" {
            result = "${result},"
        }
        result = "${result}${entry.key}"
    }

    return = result
}

procedure "first_match" {
    spec {
        params {
            items = required
            target = required
        }
    }

    idx = -1
    i = 0

    range "item" "items" {
        if "item == target" {
            idx = i
            break = true
        }
        i = i + 1
    }

    return = idx
}

procedure "skip_negatives" {
    spec {
        params {
            items = required
        }
    }

    total = 0

    range "item" "items" {
        if "item < 0" {
            continue = true
        }
        total = total + item
    }

    return = total
}

procedure "early_return_range" {
    spec {
        params {
            items = required
        }
    }

    range "item" "items" {
        if "item == 0" {
            return = "found zero"
        }
    }

    return = "no zero"
}

const {
    s1       = sum_list([1, 2, 3, 4, 5])
    s2       = sum_list([])
    keys     = map_keys_concat({a = 1, b = 2, c = 3})
    found    = first_match(["x", "y", "z"], "y")
    notfound = first_match(["x", "y", "z"], "w")
    skip     = skip_negatives([1, -2, 3, -4, 5])
    ret1     = early_return_range([1, 2, 0, 3])
    ret2     = early_return_range([1, 2, 3])
}

assert "s1"       { condition = (s1 == 15) }
assert "s2"       { condition = (s2 == 0) }
assert "keys"     { condition = (keys == "a,b,c") }
assert "found"    { condition = (found == 1) }
assert "notfound" { condition = (notfound == -1) }
assert "skip"     { condition = (skip == 9) }
assert "ret1"     { condition = (ret1 == "found zero") }
assert "ret2"     { condition = (ret2 == "no zero") }
