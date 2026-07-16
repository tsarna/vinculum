trigger "signals" "first" {
    SIGUSR1 = log::info("first handler", {})
}

trigger "signals" "second" {
    SIGUSR1 = log::info("second handler", {})
}
