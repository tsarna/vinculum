procedure "add" {
    spec {
        params {
            a = required
            b = required
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

procedure "no_spec" {
    return = "fixed"
}

procedure "no_return" {
    x = 1
}

procedure "null_default" {
    spec {
        params {
            val = null
        }
    }

    return = val
}

const {
    sum     = add(3, 4)
    hello   = greet()
    hello2  = greet("Claude")
    fixed   = no_spec()
    nothing = no_return()
    nd1     = null_default()
    nd2     = null_default("provided")
}

assert "sum" {
    condition = (sum == 7)
}

assert "hello" {
    condition = (hello == "hello world")
}

assert "hello2" {
    condition = (hello2 == "hello Claude")
}

assert "fixed" {
    condition = (fixed == "fixed")
}

assert "nothing" {
    condition = (nothing == null)
}

assert "nd1" {
    condition = (nd1 == null)
}

assert "nd2" {
    condition = (nd2 == "provided")
}
