trigger "cron" "ticker" {
    at "* * * * *" "tick" {
        action = "ticked"
    }
}
