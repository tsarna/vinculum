trigger "cron" "inactive" {
    disabled = true
    at "* * * * *" "tick" {
        action = "never"
    }
}
