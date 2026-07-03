# The var `type` attribute accepts the functy type grammar as a spec (not just a
# string): bare primitives, structural collection types, and host-registered
# capsule types.
var "n" {
    type  = number
    value = 0
}

var "names" {
    type  = list(string)
    value = ["a", "b"]
}

bus "main" {}

var "b" {
    type  = bus
    value = bus.main
}

assert "n_set" {
    condition = set(var.n, 42) == 42
}

assert "names_initial" {
    condition = get(var.names)[0] == "a"
}

assert "b_is_bus" {
    condition = get(var.b) != null
}
