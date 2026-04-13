var "temp" {
    value = 0
}

condition "threshold" "high_temp" {
    input     = get(var.temp)
    on_above  = 80.0
    off_below = 70.0
}

condition "counter" "high_temp_events" {
    preset = 3
    latch  = true
}

trigger "watch" "count_exceedances" {
    watch     = condition.high_temp
    skip_when = !ctx.new_value
    action    = increment(condition.high_temp_events)
}

condition "timer" "system_fault" {
    input = get(condition.high_temp) || get(condition.high_temp_events)
    latch = true
}
