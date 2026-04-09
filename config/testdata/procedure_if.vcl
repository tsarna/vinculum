procedure "classify" {
    spec {
        params {
            n = required
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

procedure "my_abs" {
    spec {
        params {
            n = required
        }
    }

    if "n < 0" {
        n = 0 - n
    }

    return = n
}

procedure "nested_if" {
    spec {
        params {
            x = required
        }
    }

    if "x > 10" {
        if "x > 100" {
            return = "big"
        }
        else {
            return = "medium"
        }
    }

    return = "small"
}

procedure "if_no_else" {
    spec {
        params {
            x = required
        }
    }

    result = "default"

    if "x > 0" {
        result = "positive"
    }

    return = result
}

const {
    pos   = classify(5)
    neg   = classify(-3)
    zer   = classify(0)
    abs1  = my_abs(-7)
    abs2  = my_abs(3)
    big   = nested_if(200)
    med   = nested_if(50)
    sml   = nested_if(5)
    noelse1 = if_no_else(1)
    noelse2 = if_no_else(-1)
}

assert "pos"   { condition = (pos == "positive") }
assert "neg"   { condition = (neg == "negative") }
assert "zer"   { condition = (zer == "zero") }
assert "abs1"  { condition = (abs1 == 7) }
assert "abs2"  { condition = (abs2 == 3) }
assert "big"   { condition = (big == "big") }
assert "med"   { condition = (med == "medium") }
assert "sml"   { condition = (sml == "small") }
assert "noelse1" { condition = (noelse1 == "positive") }
assert "noelse2" { condition = (noelse2 == "default") }
