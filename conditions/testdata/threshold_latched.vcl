var "temp" {
    value = 0
}

condition "threshold" "high_temp" {
    input     = get(var.temp)
    on_above  = 80.0
    off_below = 70.0
    latch     = true
}
