var "counter" {
    value = 0
}

var "greeting" {
    value = "hello"
}

var "unset" {}

assert "counter_not_null" {
    condition = get(var.counter) != null
}

assert "counter_initial" {
    condition = get(var.counter) == 0
}

assert "greeting_initial" {
    condition = get(var.greeting) == "hello"
}

assert "unset_is_null" {
    condition = get(var.unset) == null
}

assert "unset_with_default" {
    condition = get(var.unset, "fallback") == "fallback"
}

assert "set_works" {
    condition = set(var.counter, 10) == 10
}

assert "increment_works" {
    condition = increment(var.counter, 5) == 15
}

assert "get_after_increment" {
    condition = get(var.counter) == 15
}
