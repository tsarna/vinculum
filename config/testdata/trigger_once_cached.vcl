var "counter" {
    value = 0
}

trigger "once" "myinit" {
    action = increment(var.counter, 1)
}

assert "get_returns_value" {
    condition = get(trigger.myinit) == 1
}

assert "get_returns_cached_value" {
    condition = get(trigger.myinit) == 1
}

assert "action_evaluated_only_once" {
    condition = get(var.counter) == 1
}
