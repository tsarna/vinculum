condition "timer" "high_temp" {}

condition "timer" "low_voltage" {
    debounce = "200ms"
}

condition "timer" "system_fault" {
    input = get(condition.high_temp) || get(condition.low_voltage)
    latch = true
}
