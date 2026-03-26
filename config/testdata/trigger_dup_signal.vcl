trigger "signals" "first" {
    SIGUSR1 = loginfo("first handler", {})
}

trigger "signals" "second" {
    SIGUSR1 = loginfo("second handler", {})
}
