condition "timer" "a" {
    input = get(condition.b)
}

condition "timer" "b" {
    input = get(condition.a)
}
