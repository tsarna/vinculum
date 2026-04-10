trigger "signals" "first" {
    SIGUSR1 = log_info("first handler", {})
}

trigger "signals" "second" {
    SIGUSR1 = log_info("second handler", {})
}
