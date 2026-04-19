fsm "door" {
    initial  = "closed"
    disabled = true

    state "closed" {}
    state "open" {}

    event "open" {
        transition "closed" "open" {}
    }
}
