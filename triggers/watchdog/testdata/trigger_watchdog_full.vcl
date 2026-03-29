trigger "watchdog" "full" {
    window        = "5m"
    initial_grace = "2m"
    repeat        = true
    action        = "missed"
}
