trigger "watchdog" "full" {
    window        = "5m"
    initial_grace = "2m"
    repeat        = true
    max_misses    = 3
    stop_when     = ctx.miss_count >= 5
    action        = "missed"
}
