trigger "at" "oneshot" {
    time   = timeadd(now(), duration("50ms"))
    repeat = false
    action = "ring"
}
