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

procedure "no_spec" {
    return = "fixed"
}

procedure "no_return" {
    x = 1
}

const {
    sum     = add(3, 4)
    hello   = greet()
    hello2  = greet("Claude")
    fixed   = no_spec()
    nothing = no_return()
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
