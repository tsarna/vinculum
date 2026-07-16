trigger "at" "oneshot" {
    time   = time::add(time::now(), duration("50ms"))
    repeat = false
    action = "ring"
}
