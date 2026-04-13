var "battery" {
    value = 100
}

condition "threshold" "low_battery" {
    input     = get(var.battery)
    on_below  = 20.0
    off_above = 25.0
    latch     = true
}
